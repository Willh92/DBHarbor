package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/dbprivs"
	"dbharbor/backend/internal/driver/sqlbase"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
)

func extractCaptchaCodeFromSVGDataURL(t *testing.T, dataURL string) string {
	t.Helper()
	const prefix = "data:image/svg+xml;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("unexpected captcha dataURL prefix: %s", dataURL)
	}
	rawSVG, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("base64 decode captcha SVG failed: %v", err)
	}
	re := regexp.MustCompile(`>([A-Z0-9])</text>`)
	matches := re.FindAllStringSubmatch(string(rawSVG), -1)
	if len(matches) != 5 {
		t.Fatalf("expected 5 captcha characters in SVG, got %d (%s)", len(matches), string(rawSVG))
	}
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m[1])
	}
	return b.String()
}

func TestClientIPRightToLeftXFFAndLoopbackUntrusted(t *testing.T) {
	mustCIDR := func(s string) *net.IPNet {
		_, ipNet, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", s, err)
		}
		return ipNet
	}

	// 1. Empty TrustedProxies: loopback peer MUST NOT auto-trust X-Forwarded-For or X-Real-IP
	reqLoopback := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	reqLoopback.RemoteAddr = "127.0.0.1:54321"
	reqLoopback.Header.Set("X-Forwarded-For", "198.51.100.88")
	reqLoopback.Header.Set("X-Real-IP", "198.51.100.99")
	ctxUntrustedLoop := &api.Context{Request: reqLoopback, TrustedProxies: nil}
	if got := ctxUntrustedLoop.ClientIP(); got != "127.0.0.1" {
		t.Fatalf("expected untrusted loopback peer to return 127.0.0.1, got %q", got)
	}

	// 2. Trusted proxy chain: right-to-left parsing skips trusted hops and stops at first untrusted hop,
	// defeating attacker-prepended leftmost X-Forwarded-For spoofing.
	trusted := []*net.IPNet{mustCIDR("10.0.0.0/8"), mustCIDR("172.16.0.0/12")}
	reqSpoof := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	reqSpoof.RemoteAddr = "10.0.1.9:8080"
	reqSpoof.Header.Set("X-Forwarded-For", "1.1.1.1, 8.8.8.8, 172.16.5.4")
	ctxTrusted := &api.Context{Request: reqSpoof, TrustedProxies: trusted}
	if got := ctxTrusted.ClientIP(); got != "8.8.8.8" {
		t.Fatalf("expected right-to-left XFF parser to return 8.8.8.8 (ignoring spoofed 1.1.1.1), got %q", got)
	}

	// 3. Untrusted direct peer with non-empty TrustedProxies: headers must be ignored
	reqDirect := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	reqDirect.RemoteAddr = "198.51.100.42:33445"
	reqDirect.Header.Set("X-Forwarded-For", "8.8.4.4")
	ctxDirect := &api.Context{Request: reqDirect, TrustedProxies: trusted}
	if got := ctxDirect.ClientIP(); got != "198.51.100.42" {
		t.Fatalf("expected untrusted peer IP 198.51.100.42, got %q", got)
	}
}

func TestIsRestrictedAddrComprehensiveCoverage(t *testing.T) {
	restricted := []string{
		"0.0.0.0",
		"0.0.0.1",
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"100.100.100.200", // CGNAT / Alibaba Cloud IMDS
		"169.254.169.254", // Link-local Cloud IMDS
		"::1",
		"::ffff:127.0.0.1",
		"64:ff9b::7f00:1",   // NAT64 -> 127.0.0.1
		"64:ff9b::a9fe:a9fe", // NAT64 -> 169.254.169.254
		"2002:7f00:1::",     // 6to4 -> 127.0.0.1
		"fc00::1",
		"fe80::1",
	}
	for _, raw := range restricted {
		addr := netip.MustParseAddr(raw)
		if !sqlbase.IsRestrictedAddr(addr) {
			t.Fatalf("expected address %s to be restricted", raw)
		}
	}

	public := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",
		"2606:4700:4700::1111",
		"64:ff9b::0808:0808", // NAT64 -> 8.8.8.8 (public)
	}
	for _, raw := range public {
		addr := netip.MustParseAddr(raw)
		if sqlbase.IsRestrictedAddr(addr) {
			t.Fatalf("expected public address %s NOT to be restricted", raw)
		}
	}
}

func TestCaptchaFloodResilienceAndSingleUseAntiReplay(t *testing.T) {
	lp := auth.NewLoginProtector([]byte("captcha-flood-hardening-secret-key"))

	tok0, svg0, err := lp.GenerateCaptcha()
	if err != nil {
		t.Fatalf("GenerateCaptcha initial failed: %v", err)
	}
	ans0 := extractCaptchaCodeFromSVGDataURL(t, svg0)

	// Simulate 5,000 unauthenticated GET /api/auth/captcha requests from an attacker
	for i := 0; i < 5000; i++ {
		if _, _, err := lp.GenerateCaptcha(); err != nil {
			t.Fatalf("GenerateCaptcha flood #%d failed: %v", i, err)
		}
	}

	// Legitimate user's unspent CAPTCHA token MUST still verify cleanly
	if !lp.VerifyCaptcha(tok0, ans0) {
		t.Fatal("expected unspent CAPTCHA token to remain valid after 5,000 GenerateCaptcha flood")
	}

	// Replaying the spent CAPTCHA token MUST be rejected
	if lp.VerifyCaptcha(tok0, ans0) {
		t.Fatal("expected replayed spent CAPTCHA token to be rejected")
	}
}

func TestDBPrivsRedisACLAndOraclePasswordHardening(t *testing.T) {
	badRedisRules := []string{
		"~* +@read >secretpass",
		"~* +@read <oldpass",
		"on nopass ~* +@all",
		"resetpass ~* +@read",
		"#0123456789abcdef ~* +@read",
		"~* +@read ;FLUSHALL",
	}
	for _, rule := range badRedisRules {
		req := dbprivs.ActionRequest{
			Action:   "CREATE_USER",
			Username: "acl_test_user",
			Password: "SafePassword123!",
			ACLPreset: "custom",
			ACLRules: rule,
		}
		if _, err := dbprivs.BuildStatements("redis", req); err == nil {
			t.Fatalf("expected Redis ACLRules %q to be rejected in BuildStatements", rule)
		}
		if _, err := dbprivs.BuildRedactedStatements("redis", req); err == nil {
			t.Fatalf("expected Redis ACLRules %q to be rejected in BuildRedactedStatements", rule)
		}
	}

	// Valid custom Redis ACL rules without password tokens succeed and redact req.Password
	validReq := dbprivs.ActionRequest{
		Action:    "CREATE_USER",
		Username:  "acl_safe_user",
		Password:  "SuperSecretPass!",
		ACLPreset: "custom",
		ACLRules:  "~orders:* &events:* +@read -@dangerous resetchannels",
	}
	if _, err := dbprivs.BuildStatements("redis", validReq); err != nil {
		t.Fatalf("expected valid Redis ACLRules to pass: %v", err)
	}
	redacted, err := dbprivs.BuildRedactedStatements("redis", validReq)
	if err != nil || len(redacted) != 1 || strings.Contains(redacted[0], "SuperSecretPass!") {
		t.Fatalf("expected redacted Redis statement without password leak, got %v (err=%v)", redacted, err)
	}

	// Oracle & Dameng password injection via double quotes / newlines must be rejected
	for _, engine := range []string{"oracle", "dameng"} {
		for _, action := range []string{"CREATE_USER", "ALTER_USER"} {
			for _, badPass := range []string{`Pass"123" ACCOUNT UNLOCK`, "Pass\n123", "Pass\r123", "Pass\x00123"} {
				_, err := dbprivs.BuildStatements(engine, dbprivs.ActionRequest{
					Action:   action,
					Username: "APP_USER",
					Password: badPass,
				})
				if err == nil {
					t.Fatalf("expected %s %s to reject password %q", engine, action, badPass)
				}
			}
		}
	}
}

func TestSSRFFailClosedPerUserLocalDBIsolationAndCSPHardening(t *testing.T) {
	webDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(webDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(webDir, vault, "admin", "AdminStrong123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 6)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("sec-hardening-jwt"))
	router := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret: vault.DeriveBytes("sec-hardening-protector"),
		MaxImportMB:     16,
	})

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	login := func(username, password string) string {
		w := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": username,
			"password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed (%d): %s", username, w.Code, w.Body.String())
		}
		var res struct{ Token string }
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Token
	}

	adminTok := login("admin", "AdminStrong123!")

	// Check Web Server CSP header hardening & lightweight /api/version + /api/version/changelog
	verW := doJSON(http.MethodGet, "/api/version", adminTok, nil)
	if verW.Code != http.StatusOK {
		t.Fatalf("/api/version failed: %d", verW.Code)
	}
	webCSP := verW.Header().Get("Content-Security-Policy")
	if !strings.Contains(webCSP, "default-src 'self'; script-src 'self';") {
		t.Fatalf("expected hardened Web CSP header, got %q", webCSP)
	}
	if strings.Contains(verW.Body.String(), "embeddedChangelogs") {
		t.Fatalf("expected lightweight /api/version to omit embeddedChangelogs by default: %s", verW.Body.String())
	}
	var verPayload struct {
		EmbeddedChangelog string `json:"embeddedChangelog"`
	}
	_ = json.Unmarshal(verW.Body.Bytes(), &verPayload)
	if verPayload.EmbeddedChangelog != "" {
		t.Fatalf("expected empty embeddedChangelog by default, got len=%d", len(verPayload.EmbeddedChangelog))
	}

	clW := doJSON(http.MethodGet, "/api/version/changelog?lang=zh-CN", adminTok, nil)
	if clW.Code != http.StatusOK {
		t.Fatalf("GET /api/version/changelog failed (%d): %s", clW.Code, clW.Body.String())
	}
	var clPayload struct {
		Lang     string `json:"lang"`
		Markdown string `json:"markdown"`
	}
	_ = json.Unmarshal(clW.Body.Bytes(), &clPayload)
	if clPayload.Lang != "zh-CN" || len(strings.TrimSpace(clPayload.Markdown)) < 50 {
		t.Fatalf("unexpected changelog payload: lang=%q len=%d", clPayload.Lang, len(clPayload.Markdown))
	}

	// Create 3 users:
	// - ro_uploader: readonly + canCreateConn=true (must be blocked by RequireWrite on /api/fs/upload-db)
	// - alice_rw: readwrite + canCreateConn=true
	// - bob_rw: readwrite + canCreateConn=true
	for _, u := range []map[string]any{
		{"username": "ro_uploader", "password": "UserPass123!", "role": "readonly", "canCreateConn": true, "connScope": "all"},
		{"username": "alice_rw", "password": "UserPass123!", "role": "readwrite", "canCreateConn": true, "connScope": "all"},
		{"username": "bob_rw", "password": "UserPass123!", "role": "readwrite", "canCreateConn": true, "connScope": "all"},
	} {
		if w := doJSON(http.MethodPost, "/api/admin/users", adminTok, u); w.Code != http.StatusOK {
			t.Fatalf("create user %v failed: %s", u["username"], w.Body.String())
		}
	}

	roTok := login("ro_uploader", "UserPass123!")
	aliceTok := login("alice_rw", "UserPass123!")
	bobTok := login("bob_rw", "UserPass123!")

	// 1. SSRF Fail-Closed DNS & Restricted Address Rejection on non-admin connection test
	ssrfCases := []map[string]any{
		{"name": "Unresolvable Host", "type": "postgres", "host": "unresolvable-ssrf-check.invalid", "port": 5432},
		{"name": "Unresolvable Mongo SRV", "type": "mongodb", "uri": "mongodb+srv://cluster.unresolvable-ssrf-check.invalid/admin"},
		{"name": "Zero Net", "type": "mysql", "host": "0.0.0.1", "port": 3306},
		{"name": "CGNAT Metadata", "type": "redis", "host": "100.100.100.200", "port": 6379},
		{"name": "AWS IMDS", "type": "postgres", "host": "169.254.169.254", "port": 5432},
		{"name": "NAT64 Loopback", "type": "postgres", "host": "64:ff9b::7f00:1", "port": 5432},
		{"name": "6to4 Loopback", "type": "postgres", "host": "2002:7f00:1::", "port": 5432},
		{"name": "Empty Host Default Loopback", "type": "redis", "host": "", "port": 6379},
	}
	for _, tc := range ssrfCases {
		w := doJSON(http.MethodPost, "/api/connections/test", aliceTok, tc)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for non-admin SSRF case %q, got %d: %s", tc["name"], w.Code, w.Body.String())
		}
	}

	// Build a real valid SQLite 3 binary file in a temp dir for upload testing
	seedDir := t.TempDir()
	seedSQLitePath := filepath.Join(seedDir, "valid_seed.sqlite")
	rawSQLite, err := sql.Open("sqlite", seedSQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawSQLite.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT); INSERT INTO notes (title) VALUES ('alice-private');"); err != nil {
		_ = rawSQLite.Close()
		t.Fatal(err)
	}
	_ = rawSQLite.Close()
	validSQLiteBytes, err := os.ReadFile(seedSQLitePath)
	if err != nil {
		t.Fatal(err)
	}

	uploadMultipart := func(token, filename string, content []byte, trailingOverwrite bool) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(content)
		if trailingOverwrite {
			_ = mw.WriteField("overwrite", "true")
		}
		_ = mw.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/fs/upload-db", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 2. RequireWrite: readonly user MUST be blocked with 403 on /api/fs/upload-db even when canCreateConn=true
	if w := uploadMultipart(roTok, "ro_attempt.sqlite", validSQLiteBytes, false); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when readonly user uploads DB file, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Invalid SQLite magic bytes MUST be rejected with 400 and cleaned up
	if w := uploadMultipart(aliceTok, "spoofed.sqlite", []byte("not a valid sqlite3 header at all!"), false); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid SQLite magic header, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Alice uploads a valid SQLite file -> isolated under local_databases/u_<aliceHash>/alice_db.sqlite
	upW := uploadMultipart(aliceTok, "alice_db.sqlite", validSQLiteBytes, true)
	if upW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when alice_rw uploads valid SQLite DB, got %d: %s", upW.Code, upW.Body.String())
	}
	var upRes struct {
		FilePath string `json:"filePath"`
		FileName string `json:"fileName"`
	}
	_ = json.Unmarshal(upW.Body.Bytes(), &upRes)
	aliceUserDir := filepath.Dir(upRes.FilePath)
	if !strings.HasPrefix(filepath.Base(aliceUserDir), "u_") {
		t.Fatalf("expected non-admin upload to be isolated inside u_<hash> subdir, got %s", upRes.FilePath)
	}

	// 5. Cross-user isolation: bob_rw CANNOT browse Alice's u_<hash> dir or shared local_databases root,
	// only sees his own u_<hash> directory in default /api/fs/browse, and CANNOT connect to Alice's uploaded file!
	if w := doJSON(http.MethodGet, "/api/fs/browse?path="+url.QueryEscape(aliceUserDir), bobTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when bob_rw browses alice_rw's isolated directory, got %d: %s", w.Code, w.Body.String())
	}
	bobBrowseShared := doJSON(http.MethodGet, "/api/fs/browse?path="+url.QueryEscape(filepath.Join(webDir, "local_databases")), bobTok, nil)
	if bobBrowseShared.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when bob_rw browses shared local_databases root, got %d: %s", bobBrowseShared.Code, bobBrowseShared.Body.String())
	}
	bobBrowseOwn := doJSON(http.MethodGet, "/api/fs/browse", bobTok, nil)
	if bobBrowseOwn.Code != http.StatusOK {
		t.Fatalf("expected 200 when bob_rw browses his own isolated local_databases directory, got %d: %s", bobBrowseOwn.Code, bobBrowseOwn.Body.String())
	}
	if strings.Contains(bobBrowseOwn.Body.String(), filepath.Base(aliceUserDir)) {
		t.Fatalf("expected alice_rw's u_<hash> bucket to be hidden from bob_rw, got: %s", bobBrowseOwn.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/connections", bobTok, map[string]any{
		"name":     "Steal Alice DB",
		"type":     "sqlite",
		"filePath": upRes.FilePath,
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when bob_rw tries to connect to alice_rw's isolated SQLite file, got %d: %s", w.Code, w.Body.String())
	}

	// Meanwhile Alice CAN connect to her own uploaded SQLite database
	aliceConnW := doJSON(http.MethodPost, "/api/connections", aliceTok, map[string]any{
		"name":     "Alice Own DB",
		"type":     "sqlite",
		"filePath": upRes.FilePath,
	})
	if aliceConnW.Code != http.StatusOK {
		t.Fatalf("expected 200 when alice_rw connects to her own uploaded SQLite file, got %d: %s", aliceConnW.Code, aliceConnW.Body.String())
	}

	// 6. Desktop mode CSP nonce verification
	deskDir := t.TempDir()
	deskVault, _ := crypto.NewVaultFromDataDir(deskDir, "")
	deskStore, err := store.OpenLocal(deskDir, deskVault)
	if err != nil {
		t.Fatal(err)
	}
	defer deskStore.Close()
	deskPool := pool.NewManager(deskStore, 4)
	defer deskPool.CloseAll()
	deskRouter := api.NewDesktopRouter(deskStore, deskPool, api.DesktopConfig{
		Enabled:             true,
		ListenPort:          18999,
		DataDir:             deskDir,
		BootTicket:          "boot-tkt",
		SessionCookieSecret: "desk-sec",
	})
	deskIdxReq := httptest.NewRequest(http.MethodGet, "/", nil)
	deskIdxW := httptest.NewRecorder()
	deskRouter.ServeHTTP(deskIdxW, deskIdxReq)
	deskCSP := deskIdxW.Header().Get("Content-Security-Policy")
	nonceRe := regexp.MustCompile(`'nonce-([0-9a-f]+)'`)
	m := nonceRe.FindStringSubmatch(deskCSP)
	if len(m) != 2 {
		t.Fatalf("expected Desktop CSP header to include 'nonce-<hex>', got %q", deskCSP)
	}
	if deskIdxW.Code == http.StatusOK && !strings.Contains(deskIdxW.Body.String(), `<script nonce="`+m[1]+`">`) {
		t.Fatalf("expected Desktop index.html boot script to carry matching nonce %q", m[1])
	}
}

type closeProbeDriver struct {
	driver.Driver
	closed *atomic.Bool
}

func (d *closeProbeDriver) Meta() driver.DriverMeta {
	return driver.DriverMeta{Type: "evictwaitprobe", Name: "EvictWaitProbe"}
}
func (d *closeProbeDriver) Ping(context.Context) (driver.ServerInfo, error) {
	return driver.ServerInfo{Version: "1.0"}, nil
}
func (d *closeProbeDriver) Close() error {
	time.Sleep(35 * time.Millisecond)
	d.closed.Store(true)
	return nil
}

func TestWorkstreamAConnIsolationMongoDupEphemeralAndEvictWait(t *testing.T) {
	// 1. MongoDB role validation hardening in dbprivs
	for _, badRole := range []string{"__system", "__internal", "", "  ", "@admin", "read@", "read\x00Write", "read$admin"} {
		_, err := dbprivs.BuildStatements("mongodb", dbprivs.ActionRequest{
			Action:   "GRANT",
			Username: "app_user",
			Database: "shop",
			Roles:    []string{badRole},
		})
		if err == nil {
			t.Fatalf("expected MongoDB role %q to be rejected by dbprivs.BuildStatements", badRole)
		}
	}
	if _, err := dbprivs.BuildStatements("mongodb", dbprivs.ActionRequest{
		Action:   "GRANT",
		Username: "app_user",
		Database: "shop",
		Roles:    []string{"readWrite@analytics_db"},
	}); err != nil {
		t.Fatalf("expected valid MongoDB role readWrite@analytics_db to succeed, got %v", err)
	}

	// 2. Web Server setup
	webDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(webDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(webDir, vault, "admin", "AdminStrong123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 6)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("workstream-a-jwt"))
	router := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret: vault.DeriveBytes("workstream-a-protector"),
		MaxImportMB:     16,
	})

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	login := func(username, password string) string {
		w := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": username,
			"password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed (%d): %s", username, w.Code, w.Body.String())
		}
		var out struct{ Token string }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Token
	}

	adminTok := login("admin", "AdminStrong123!")

	wCreateAlice := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":      "alice_sec",
		"password":      "AliceStrong123!",
		"role":          "readwrite",
		"canCreateConn": true,
		"connScope":     "all",
	})
	if wCreateAlice.Code != http.StatusOK {
		t.Fatalf("create alice_sec failed: %s", wCreateAlice.Body.String())
	}
	var aliceUser store.User
	_ = json.Unmarshal(wCreateAlice.Body.Bytes(), &aliceUser)
	aliceTok := login("alice_sec", "AliceStrong123!")

	// 3. Seeded Sample Playground works seamlessly for non-admin queries, while non-owner duplicate is blocked (403)
	wConns := doJSON(http.MethodGet, "/api/connections", aliceTok, nil)
	var connList struct {
		Connections []store.SavedConnection `json:"connections"`
	}
	_ = json.Unmarshal(wConns.Body.Bytes(), &connList)
	if len(connList.Connections) == 0 {
		t.Fatal("expected seeded Sample Playground connection")
	}
	sampleID := connList.Connections[0].ID
	if wQ := doJSON(http.MethodPost, "/api/conn/"+sampleID+"/query", aliceTok, map[string]any{
		"database": "main",
		"query":    "SELECT 1 AS ok;",
	}); wQ.Code != http.StatusOK {
		t.Fatalf("expected non-admin query on seeded Sample Playground to succeed, got %d: %s", wQ.Code, wQ.Body.String())
	}
	if wDupShared := doJSON(http.MethodPost, "/api/connections/"+sampleID+"/duplicate", aliceTok, nil); wDupShared.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when non-owner duplicates admin-shared connection, got %d: %s", wDupShared.Code, wDupShared.Body.String())
	}

	// 4. Reject '?', '#', '%', 'file:', control chars, and disallowed extensions in connection filePath
	for _, badFP := range []string{
		"alice.sqlite?_pragma=writable_schema(1)",
		"alice.sqlite#frag",
		"alice%2esqlite",
		"file:alice.sqlite",
		"alice\x00.sqlite",
		"alice_notes.txt",
	} {
		w := doJSON(http.MethodPost, "/api/connections", aliceTok, map[string]any{
			"name":     "Bad FilePath",
			"type":     "sqlite",
			"filePath": badFP,
		})
		if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
			t.Fatalf("expected 400/403 for disallowed filePath %q, got %d: %s", badFP, w.Code, w.Body.String())
		}
	}

	// Create valid user-scoped DB for Alice so her local_databases/u_<hash> directory exists on disk
	wValidAlice := doJSON(http.MethodPost, "/api/connections", aliceTok, map[string]any{
		"name":     "Alice Scoped SQLite",
		"type":     "sqlite",
		"filePath": "alice_valid.sqlite",
	})
	if wValidAlice.Code != http.StatusOK {
		t.Fatalf("expected valid bare filename to auto-scope into Alice's u_<hash> dir, got %d: %s", wValidAlice.Code, wValidAlice.Body.String())
	}
	var aliceScopedConn store.SavedConnection
	_ = json.Unmarshal(wValidAlice.Body.Bytes(), &aliceScopedConn)
	aliceLocalBucketDir := filepath.Join(webDir, filepath.Dir(filepath.FromSlash(aliceScopedConn.FilePath)))
	if _, err := os.Stat(aliceLocalBucketDir); err != nil {
		t.Fatalf("expected Alice's isolated u_<hash> directory to exist at %s: %v", aliceLocalBucketDir, err)
	}

	// 5. MongoDB tlsCAFile / .sock rejection in create, test, and import
	for _, badMongo := range []map[string]any{
		{"name": "Mongo TLS CA Leak", "type": "mongodb", "uri": "mongodb://8.8.8.8:27017/admin?tlsCAFile=/etc/passwd"},
		{"name": "Mongo Cert Key Leak", "type": "mongodb", "uri": "mongodb://8.8.8.8:27017/admin?sslClientCertificateKeyFile=/etc/shadow"},
		{"name": "Mongo Unix Sock URI", "type": "mongodb", "uri": "mongodb://%2Ftmp%2Fmongodb-27017.sock/admin"},
		{"name": "Mongo Unix Sock Host", "type": "mongodb", "host": "/tmp/mongodb-27017.sock", "port": 27017},
	} {
		if w := doJSON(http.MethodPost, "/api/connections", aliceTok, badMongo); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for %q on /api/connections, got %d: %s", badMongo["name"], w.Code, w.Body.String())
		}
	}
	if wImpBad := doJSON(http.MethodPost, "/api/connections/import", aliceTok, map[string]any{
		"connections": []map[string]any{
			{
				"name":    "Imported TLS File Opt",
				"type":    "mongodb",
				"host":    "8.8.8.8",
				"port":    27017,
				"options": map[string]string{"tlsCAFile": "/etc/passwd"},
			},
		},
	}); wImpBad.Code != http.StatusForbidden {
		t.Fatalf("expected 403 importing connection with restricted tlsCAFile option, got %d: %s", wImpBad.Code, wImpBad.Body.String())
	}

	// 6. Ephemeral concurrent in-flight directory protection + <15m grace window during sweepEphemeralWorkspaces
	sqliteBytes := buildSampleSQLiteBytes(t)
	uploadCompleteEphemeral := func(filename string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", filename)
		_, _ = fw.Write(sqliteBytes)
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/fs/ephemeral-open", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+aliceTok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if wSeedEph := uploadCompleteEphemeral("seed_eph.sqlite"); wSeedEph.Code != http.StatusOK {
		t.Fatalf("seed ephemeral open failed (%d): %s", wSeedEph.Code, wSeedEph.Body.String())
	}

	ephRoot := filepath.Join(webDir, "ephemeral_workspaces")
	bucketEntries, err := os.ReadDir(ephRoot)
	if err != nil || len(bucketEntries) != 1 {
		t.Fatalf("expected 1 ephemeral user bucket, got err=%v len=%d", err, len(bucketEntries))
	}
	aliceEphBucketDir := filepath.Join(ephRoot, bucketEntries[0].Name())

	recentOrphanDir := filepath.Join(aliceEphBucketDir, "00000000-0000-0000-0000-000000000001")
	staleOrphanDir := filepath.Join(aliceEphBucketDir, "00000000-0000-0000-0000-000000000002")
	_ = os.MkdirAll(recentOrphanDir, 0o700)
	_ = os.WriteFile(filepath.Join(recentOrphanDir, "recent.sqlite"), sqliteBytes, 0o600)
	_ = os.MkdirAll(staleOrphanDir, 0o700)
	staleFile := filepath.Join(staleOrphanDir, "stale.sqlite")
	_ = os.WriteFile(staleFile, sqliteBytes, 0o600)
	past := time.Now().Add(-20 * time.Minute)
	_ = os.Chtimes(staleFile, past, past)
	_ = os.Chtimes(staleOrphanDir, past, past)

	// Stream an in-flight ephemeral upload paused mid-body via io.Pipe
	pr, pw := io.Pipe()
	const boundary = "dbharborInFlightBoundary12345"
	contentType := "multipart/form-data; boundary=" + boundary

	var slowW *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/api/fs/ephemeral-open", pr)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+aliceTok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		slowW = w
	}()

	partHeader := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"inflight_upload.sqlite\"\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n"
	_, _ = pw.Write([]byte(partHeader))
	_, _ = pw.Write(sqliteBytes[:64])

	// Wait until handleOpenEphemeralDBFile has created its in-flight connDir on disk (total subdirs == 4)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		subs, _ := os.ReadDir(aliceEphBucketDir)
		if len(subs) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Trigger a concurrent ephemeral open -> invokes sweepEphemeralWorkspaces while first upload is still mid-flight!
	if wConcurrent := uploadCompleteEphemeral("concurrent_trigger.sqlite"); wConcurrent.Code != http.StatusOK {
		t.Fatalf("concurrent ephemeral open failed (%d): %s", wConcurrent.Code, wConcurrent.Body.String())
	}

	// Finish streaming the paused upload
	_, _ = pw.Write(sqliteBytes[64:])
	_, _ = pw.Write([]byte("\r\n--" + boundary + "--\r\n"))
	_ = pw.Close()
	wg.Wait()

	if slowW == nil || slowW.Code != http.StatusOK {
		code := 0
		body := ""
		if slowW != nil {
			code = slowW.Code
			body = slowW.Body.String()
		}
		t.Fatalf("expected in-flight ephemeral upload to survive concurrent sweep with 200 OK, got %d: %s", code, body)
	}
	if _, err := os.Stat(recentOrphanDir); err != nil {
		t.Fatalf("expected <15m recent orphan workspace to be preserved by grace window, got %v", err)
	}
	if _, err := os.Stat(staleOrphanDir); !os.IsNotExist(err) {
		t.Fatalf("expected >15m stale orphan workspace to be swept, stat err=%v", err)
	}

	// 7. pool.Manager.EvictAndWait waits for active lease release & Close() completion
	var closedFlag atomic.Bool
	if _, exists := driver.GetMeta("evictwaitprobe"); !exists {
		driver.Register(driver.DriverMeta{Type: "evictwaitprobe", Name: "EvictWaitProbe"}, func(driver.ConnectionConfig) (driver.Driver, error) {
			return &closeProbeDriver{closed: &closedFlag}, nil
		})
	}
	probeSaved, err := st.SaveConnectionWithMeta(driver.ConnectionConfig{
		Name: "EvictWait Probe",
		Type: "evictwaitprobe",
	}, false, "", "shared")
	if err != nil {
		t.Fatalf("save probe connection: %v", err)
	}
	_, _, releaseProbe, err := pm.Acquire(probeSaved.ID)
	if err != nil {
		t.Fatalf("pm.Acquire probe: %v", err)
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		releaseProbe()
	}()
	pm.EvictAndWait(probeSaved.ID, 1500*time.Millisecond)
	if !closedFlag.Load() {
		t.Fatal("expected EvictAndWait to block until active lease release finished Close()")
	}

	// 8. Admin deleting Alice wipes her local_databases/u_<hash> directory as well
	if wDelAlice := doJSON(http.MethodDelete, "/api/admin/users/"+aliceUser.ID, adminTok, nil); wDelAlice.Code != http.StatusOK {
		t.Fatalf("delete alice_sec failed (%d): %s", wDelAlice.Code, wDelAlice.Body.String())
	}
	if _, err := os.Stat(aliceLocalBucketDir); !os.IsNotExist(err) {
		t.Fatalf("expected deleted user's local_databases/u_<hash> directory (%s) to be removed, stat err=%v", aliceLocalBucketDir, err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestSecurityHardeningM1PolicyTicketAndEarlyUploadValidation(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir: %v", err)
	}
	st, err := store.Open(tmpDir, vault, "admin", "AdminStrong123!")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	tokens := auth.NewTokenManager(vault.DeriveBytes("jwt-signing-v1"))
	router := api.NewRouterWithOptions(st, pm, tokens, api.RouterOptions{
		ProtectorSecret: vault.DeriveBytes("captcha-hmac-v1"),
		MaxImportMB:     16,
	})

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.RemoteAddr = "127.0.0.1:41000"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	login := func(u, p string) string {
		w := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{"username": u, "password": p})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed (%d): %s", u, w.Code, w.Body.String())
		}
		var out struct{ Token string }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Token
	}
	adminTok := login("admin", "AdminStrong123!")

	// 1. 8-char password minimum across admin create, admin reset, and self-change
	if w := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":      "short_u",
		"password":      "seven77",
		"role":          "readwrite",
		"canCreateConn": true,
		"connScope":     "all",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on 7-char admin user creation, got %d: %s", w.Code, w.Body.String())
	}
	wCreate := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":      "valid_u8",
		"password":      "Eight888",
		"role":          "readwrite",
		"canCreateConn": true,
		"connScope":     "all",
	})
	if wCreate.Code != http.StatusOK {
		t.Fatalf("expected 200 on 8-char admin user creation, got %d: %s", wCreate.Code, wCreate.Body.String())
	}
	var createdUser struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(wCreate.Body.Bytes(), &createdUser)
	if w := doJSON(http.MethodPut, "/api/admin/users/"+createdUser.ID+"/password", adminTok, map[string]any{
		"newPassword": "seven77",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on 7-char admin password reset, got %d: %s", w.Code, w.Body.String())
	}
	userTok := login("valid_u8", "Eight888")
	if w := doJSON(http.MethodPut, "/api/auth/password", userTok, map[string]any{
		"oldPassword": "Eight888",
		"newPassword": "seven77",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on 7-char self password change, got %d: %s", w.Code, w.Body.String())
	}

	// 2. ?ticket= restricted strictly to /api/conn/.../export (must NOT authenticate /api/connections/export)
	sqlitePath := filepath.Join(tmpDir, "ticket_scope.sqlite")
	sDB, err := sql.Open("sqlite", filepath.ToSlash(sqlitePath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, _ = sDB.Exec(`CREATE TABLE items (id INT PRIMARY KEY, name TEXT); INSERT INTO items VALUES (1, 'a');`)
	_ = sDB.Close()
	savedConn, err := st.SaveConnectionWithMeta(driver.ConnectionConfig{
		Name:     "TicketScopeSQLite",
		Type:     "sqlite",
		FilePath: sqlitePath,
	}, false, "", "shared")
	if err != nil {
		t.Fatalf("SaveConnectionWithMeta: %v", err)
	}

	wTicket := doJSON(http.MethodPost, "/api/auth/ticket", adminTok, nil)
	var tRes struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(wTicket.Body.Bytes(), &tRes)
	if tRes.Ticket == "" {
		t.Fatalf("expected non-empty ticket: %s", wTicket.Body.String())
	}
	wForbiddenExport := httptest.NewRecorder()
	router.ServeHTTP(wForbiddenExport, httptest.NewRequest(http.MethodGet, "/api/connections/export?ticket="+tRes.Ticket, nil))
	if wForbiddenExport.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on /api/connections/export?ticket=..., got %d: %s", wForbiddenExport.Code, wForbiddenExport.Body.String())
	}
	// The unconsumed ticket must still work on /api/conn/{id}/export
	wConnExport := httptest.NewRecorder()
	router.ServeHTTP(wConnExport, httptest.NewRequest(http.MethodGet, "/api/conn/"+savedConn.ID+"/export?database=main&table=items&format=json&ticket="+tRes.Ticket, nil))
	if wConnExport.Code != http.StatusOK || !strings.Contains(wConnExport.Body.String(), `"name":"a"`) {
		t.Fatalf("expected 200 on /api/conn/{id}/export?ticket=..., got %d: %s", wConnExport.Code, wConnExport.Body.String())
	}

	// 3. Early magic-header validation aborts BEFORE streaming 2 MiB to disk or creating dirs
	buildStreamMultipart := func(filename string, payload []byte, trailingField, trailingVal string) (string, *countingReader) {
		var pre, post bytes.Buffer
		mw := multipart.NewWriter(&pre)
		partW, _ := mw.CreateFormFile("file", filename)
		_ = partW
		// Write header + payload via io.MultiReader so we track how many bytes handler actually reads
		headerBytes := append([]byte(nil), pre.Bytes()...)
		if trailingField != "" {
			_ = mw.WriteField(trailingField, trailingVal)
		}
		_ = mw.Close()
		tailBytes := pre.Bytes()[len(headerBytes):]
		_ = post
		cr := &countingReader{
			r: io.MultiReader(bytes.NewReader(headerBytes), bytes.NewReader(payload), bytes.NewReader(tailBytes)),
		}
		return mw.FormDataContentType(), cr
	}

	beforeLocalEntries, _ := os.ReadDir(filepath.Join(tmpDir, "local_databases"))
	spoofed2MB := bytes.Repeat([]byte("NOT_A_VALID_DB_HEADER_"), 95000) // ~2.1 MB
	for _, endpoint := range []struct {
		url      string
		filename string
	}{
		{"/api/fs/ephemeral-open", "spoof.sqlite"},
		{"/api/fs/upload-db", "spoof.sqlite"},
		{"/api/fs/upload-db", "spoof.parquet"},
	} {
		ctype, cr := buildStreamMultipart(endpoint.filename, spoofed2MB, "", "")
		req := httptest.NewRequest(http.MethodPost, endpoint.url, cr)
		req.RemoteAddr = "127.0.0.1:41000"
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on spoofed %s to %s, got %d: %s", endpoint.filename, endpoint.url, w.Code, w.Body.String())
		}
		if cr.n > 64*1024 {
			t.Fatalf("expected early header abort <= 64 KiB on %s (%s), but read %d bytes", endpoint.url, endpoint.filename, cr.n)
		}
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "ephemeral_workspaces")); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces"))
		for _, e := range entries {
			sub, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces", e.Name()))
			if len(sub) > 0 {
				t.Fatalf("expected zero ephemeral workspace dirs created for spoofed upload, found %d", len(sub))
			}
		}
	}
	afterLocalEntries, _ := os.ReadDir(filepath.Join(tmpDir, "local_databases"))
	if len(afterLocalEntries) != len(beforeLocalEntries) {
		t.Fatalf("expected no new files in local_databases for spoofed uploads, before=%d after=%d", len(beforeLocalEntries), len(afterLocalEntries))
	}

	// Trailing driverType=duckdb on valid SQLite .db upload must still be caught after drain
	validSQLiteBytes, _ := os.ReadFile(sqlitePath)
	ctypeMismatch, crMismatch := buildStreamMultipart("trailing.db", validSQLiteBytes, "driverType", "duckdb")
	reqMismatch := httptest.NewRequest(http.MethodPost, "/api/fs/ephemeral-open", crMismatch)
	reqMismatch.RemoteAddr = "127.0.0.1:41000"
	reqMismatch.Header.Set("Content-Type", ctypeMismatch)
	reqMismatch.Header.Set("Authorization", "Bearer "+adminTok)
	wMismatch := httptest.NewRecorder()
	router.ServeHTTP(wMismatch, reqMismatch)
	if wMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when trailing driverType=duckdb conflicts with SQLite header, got %d: %s", wMismatch.Code, wMismatch.Body.String())
	}

	// Valid .parquet upload with trailing overwrite=true works cleanly
	validParquet := append([]byte("PAR1"), []byte("mock-parquet-footer-PAR1")...)
	ctypePQ, crPQ := buildStreamMultipart("events.parquet", validParquet, "overwrite", "true")
	reqPQ := httptest.NewRequest(http.MethodPost, "/api/fs/upload-db", crPQ)
	reqPQ.RemoteAddr = "127.0.0.1:41000"
	reqPQ.Header.Set("Content-Type", ctypePQ)
	reqPQ.Header.Set("Authorization", "Bearer "+adminTok)
	wPQ := httptest.NewRecorder()
	router.ServeHTTP(wPQ, reqPQ)
	if wPQ.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid PAR1 parquet upload, got %d: %s", wPQ.Code, wPQ.Body.String())
	}
}

func TestDialectSQLGuardHardeningLiveHTTP(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	st, err := store.Open(tmpDir, vault, "admin", "AdminSecret123!")
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	tm := auth.NewTokenManager(vault.DeriveBytes("dialect-http-hardening"))
	router := api.NewRouter(st, pm, tm)

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.RemoteAddr = "127.0.0.1:41001"
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	login := func(username, password string) string {
		t.Helper()
		w := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": username,
			"password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed (%d): %s", username, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		tok, _ := out["token"].(string)
		return tok
	}

	adminTok := login("admin", "AdminSecret123!")
	if w := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username": "rw_user", "password": "RwUserPass1!", "role": store.RoleReadWrite, "connScope": "all",
	}); w.Code != http.StatusOK {
		t.Fatalf("create rw_user failed: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username": "ro_user", "password": "RoUserPass1!", "role": store.RoleReadOnly, "connScope": "all",
	}); w.Code != http.StatusOK {
		t.Fatalf("create ro_user failed: %d %s", w.Code, w.Body.String())
	}
	rwTok := login("rw_user", "RwUserPass1!")
	roTok := login("ro_user", "RoUserPass1!")

	saveShared := func(cfg driver.ConnectionConfig) string {
		t.Helper()
		saved, sErr := st.SaveConnectionWithMeta(cfg, false, "", "shared")
		if sErr != nil {
			t.Fatalf("SaveConnectionWithMeta(%s): %v", cfg.Type, sErr)
		}
		return saved.ID
	}

	mysqlID := saveShared(driver.ConnectionConfig{Name: "My", Type: "mysql", Host: "127.0.0.1", Port: 3306, Username: "u", Database: "app"})
	mariaID := saveShared(driver.ConnectionConfig{Name: "Maria", Type: "mariadb", Host: "127.0.0.1", Port: 3306, Username: "u", Database: "app"})
	chID := saveShared(driver.ConnectionConfig{Name: "CH", Type: "clickhouse", Host: "127.0.0.1", Port: 9000, Username: "default", Database: "default"})
	pgID := saveShared(driver.ConnectionConfig{Name: "PG", Type: "postgres", Host: "127.0.0.1", Port: 5432, Username: "postgres", Database: "app"})
	mssqlID := saveShared(driver.ConnectionConfig{Name: "MSSQL", Type: "sqlserver", Host: "127.0.0.1", Port: 1433, Username: "sa", Database: "master"})
	oraID := saveShared(driver.ConnectionConfig{Name: "ORA", Type: "oracle", Host: "127.0.0.1", Port: 1521, Username: "scott", Database: "orcl"})
	damengID := saveShared(driver.ConnectionConfig{Name: "Dameng", Type: "dameng", Host: "127.0.0.1", Port: 5236, Username: "SYSDBA", Database: "DAMENG"})
	duckPath := filepath.Join(tmpDir, "sandbox.duckdb")
	duckID := saveShared(driver.ConnectionConfig{Name: "Duck", Type: "duckdb", FilePath: duckPath})
	sqlitePath := filepath.Join(tmpDir, "guard.sqlite")
	if sdb, sErr := sql.Open("sqlite", filepath.ToSlash(sqlitePath)); sErr == nil {
		_, _ = sdb.Exec(`CREATE TABLE items (id INT PRIMARY KEY, name TEXT);`)
		_ = sdb.Close()
	} else {
		t.Fatalf("open guard.sqlite: %v", sErr)
	}
	sqliteID := saveShared(driver.ConnectionConfig{Name: "SQLite", Type: "sqlite", FilePath: sqlitePath})

	// 1. validateDDLFragment dialect-awareness over live HTTP /table/create & /table/alter (previewOnly: true)
	if w := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/table/create", rwTok, map[string]any{
		"database": "app", "table": "t_my_def", "previewOnly": true,
		"columns": []map[string]any{
			{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
			{"name": "val", "type": "VARCHAR(64)", "nullable": false, "defaultValue": "'\\''; DROP TABLE items; -- '"},
		},
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for MySQL table/create backslash defaultValue smuggle, got %d: %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/table/alter", rwTok, map[string]any{
		"database": "app", "table": "t_my_def", "previewOnly": true,
		"actions": []map[string]any{
			{"action": "ADD_COLUMN", "column": map[string]any{"name": "val2", "type": "VARCHAR(64)", "nullable": false, "defaultValue": "'\\''; DROP TABLE items; -- '"}},
		},
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for MySQL table/alter backslash defaultValue smuggle, got %d: %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/conn/"+chID+"/table/create", rwTok, map[string]any{
		"database": "default", "table": "t_ch", "previewOnly": true,
		"columns": []map[string]any{{"name": "id", "type": "UInt64"}},
		"orderBy": "tuple(), '\\''; DROP TABLE items; -- '",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ClickHouse table/create orderBy backslash smuggle, got %d: %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/conn/"+pgID+"/table/create", rwTok, map[string]any{
		"schema": "public", "table": "t_pg_def", "previewOnly": true,
		"columns": []map[string]any{
			{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
			{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": "e'\\''; DROP TABLE items; -- '"},
		},
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for PostgreSQL table/create e'\\'' defaultValue smuggle, got %d: %s", w.Code, w.Body.String())
	}

	// 2. BuildObjectActionSQLs /object/action MySQL DEFINER string literal vs escaped-quote bypass
	if w := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
		"database": "app", "objectName": "v_ok", "objectType": "VIEW", "action": "CREATE_VIEW", "previewOnly": true,
		"viewSql": "CREATE VIEW v_ok AS SELECT 'it\\'s DEFINER = ok' AS note",
	}); w.Code != http.StatusOK {
		t.Fatalf("expected 200 for benign MySQL CREATE_VIEW with escaped quote before DEFINER =, got %d: %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
		"database": "app", "objectName": "p_bad", "objectType": "PROCEDURE", "action": "CREATE_ROUTINE", "previewOnly": true,
		"routineSql": "CREATE PROCEDURE p_bad() COMMENT 'abc\\'' SQL SECURITY DEFINER BEGIN SELECT 1; END",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for MySQL CREATE_ROUTINE SQL SECURITY DEFINER bypass, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Nested block comment & '#' operator read-only RBAC enforcement across PG / MSSQL / Oracle
	for _, tc := range []struct {
		connID string
		sql    string
	}{
		{pgID, "SELECT /* /* */ ' */ ; DROP TABLE items; -- '"},
		{pgID, `SELECT '{"a":1}'::jsonb #> '{a}'; DROP TABLE items`},
		{mssqlID, "SELECT /* /* */ ' */ ; DROP TABLE items; -- '"},
		{mssqlID, "SELECT * FROM #temp; DROP TABLE items"},
		{oraID, "SELECT col#1 FROM dual; DROP TABLE items"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{
			"query": tc.sql,
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for readonly query %q on conn %s, got %d: %s", tc.sql, tc.connID, w.Code, w.Body.String())
		}
	}

	// 4. DuckDB Web sandbox enforcement over live HTTP /query (nested comment, #1, EXPLAIN DESC, PIVOT/UNPIVOT, FROM VALUES)
	for _, badDuck := range []string{
		"SELECT /* /* */ ' */ ; CALL duckdb_secrets(); -- '",
		"SELECT #1 FROM (SELECT 42); CALL duckdb_secrets()",
		"EXPLAIN DESC '/etc/passwd'",
		"CREATE TABLE t AS DESC '/etc/passwd'",
		"PIVOT '/etc/passwd' ON col USING SUM(val)",
		"UNPIVOT '/etc/passwd' ON col",
		"SELECT * FROM VALUES (1) AS t(a), '/etc/passwd'",
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+duckID+"/query", rwTok, map[string]any{
			"query": badDuck,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for DuckDB Web sandbox bypass %q, got %d: %s", badDuck, w.Code, w.Body.String())
		}
	}

	// 5. Iteration 4 live HTTP regressions: DollarQuotes gating ($x$), MySQL /*M! & /*!99999 quote-swallowing, PG #>> BrowseData
	for _, tc := range []struct {
		objType    string
		routineSQL string
	}{
		{"FUNCTION", "CREATE FUNCTION f_dollar_single($x$ INT) RETURNS INT DETERMINISTIC SQL SECURITY DEFINER RETURN $x$"},
		{"PROCEDURE", "CREATE PROCEDURE p_dollar_single(IN $x$ INT) SQL SECURITY DEFINER SELECT $x$"},
		{"PROCEDURE", "CREATE PROCEDURE p_maria_cmt() /*M! ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"PROCEDURE", "CREATE PROCEDURE p_ver_cmt() /*!99999 ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
			"database":    "app",
			"objectName":  "p_iter4",
			"objectType":  tc.objType,
			"action":      "CREATE_ROUTINE",
			"previewOnly": true,
			"routineSql":  tc.routineSQL,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for MySQL DEFINER bypass %q, got %d: %s", tc.routineSQL, w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		connID string
		tok    string
		sql    string
	}{
		{mysqlID, roTok, "SELECT 1 AS $x$, LOAD_FILE('/etc/passwd') AS $x$"},
		{mysqlID, roTok, "SELECT 1 AS $x$ INTO OUTFILE '/tmp/pwn.txt' FROM dual HAVING $x$ = 1"},
		{mysqlID, roTok, "SELECT 1 /*M! ' */ INTO OUTFILE '/tmp/pwned' /* ' */"},
		{mysqlID, roTok, "SELECT 1 /*!99999 ' */ , LOAD_FILE('/etc/passwd') /* ' */"},
		{chID, roTok, "SELECT 1 AS $x$, s3('http://169.254.169.254/') AS $x$"},
		{chID, rwTok, "SELECT 1 AS $x$, s3('http://169.254.169.254/') AS $x$"},
		{sqliteID, roTok, "SELECT $x$; DROP TABLE items; SELECT $x$"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", tc.tok, map[string]any{
			"query": tc.sql,
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for query %q on conn %s, got %d: %s", tc.sql, tc.connID, w.Code, w.Body.String())
		}
	}

	// Postgres BrowseData JSONB #>> filter is NOT rejected by filter syntax check, whereas ';' smuggle & MySQL '#' comment ARE rejected
	wPGBrowse := doJSON(http.MethodPost, "/api/conn/"+pgID+"/table/browse", roTok, map[string]any{
		"schema": "public", "table": "events", "filter": "data #>> '{a}' = '1'",
	})
	if wPGBrowse.Code == http.StatusForbidden || strings.Contains(wPGBrowse.Body.String(), "WHERE filter") {
		t.Fatalf("expected Postgres JSONB #>> filter not to fail filter syntax/readonly validation, got %d: %s", wPGBrowse.Code, wPGBrowse.Body.String())
	}
	wPGBrowseBad := doJSON(http.MethodPost, "/api/conn/"+pgID+"/table/browse", roTok, map[string]any{
		"schema": "public", "table": "events", "filter": "data #>> '{a}' = '1'; DROP TABLE events",
	})
	if wPGBrowseBad.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for Postgres BrowseData '; DROP TABLE' filter, got %d: %s", wPGBrowseBad.Code, wPGBrowseBad.Body.String())
	}
	wMyBrowseHash := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/table/browse", rwTok, map[string]any{
		"database": "app", "table": "events", "filter": "1 = 1 # comment",
	})
	if wMyBrowseHash.Code != http.StatusBadRequest || !strings.Contains(wMyBrowseHash.Body.String(), "WHERE filter") {
		t.Fatalf("expected 400 WHERE filter rejection for MySQL '#' comment, got %d: %s", wMyBrowseHash.Code, wMyBrowseHash.Body.String())
	}

	// 6. Iteration 5 live HTTP regressions: '\*/' inside executable comments + MariaDB /*M!50003 /object/action parity
	for _, tc := range []struct {
		connID     string
		objType    string
		action     string
		viewSQL    string
		routineSQL string
	}{
		{mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p9() SQL /*!50000 SECURITY */ /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p12() SQL /*!50000 SECURITY */ /*!99999 \" \\*/ DEFINER /* \" */ BEGIN SELECT 1; END"},
		{mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p() /*!99999 '\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END"},
		{mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_odd() /*!99999 '\\\\\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END"},
		{mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p10() SQL /*M!50000 SECURITY */ /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{mariaID, "VIEW", "CREATE_VIEW", "/*M!50003 DROP TABLE items; */ CREATE VIEW v_maria_pwn AS SELECT 1", ""},
		{mariaID, "VIEW", "CREATE_VIEW", "/*M!50003 DROP TABLE items -- */\nCREATE VIEW v_maria_pwn AS SELECT 1", ""},
		{mariaID, "VIEW", "CREATE_VIEW", "CREATE /*M!50003 TABLE pwn(x INT) */ VIEW v_maria_pwn AS SELECT 1", ""},
		{mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "/*M!50003 DROP TABLE items; */ CREATE PROCEDURE p_maria_pwn() BEGIN SELECT 1; END"},
		{mariaID, "TRIGGER", "CREATE_TRIGGER", "", "/*M!50003 DROP TABLE items; */ CREATE TRIGGER t_maria_pwn BEFORE INSERT ON items FOR EACH ROW SET @a=1"},
	} {
		payload := map[string]any{
			"database":    "app",
			"objectName":  "iter5_obj",
			"objectType":  tc.objType,
			"action":      tc.action,
			"previewOnly": true,
		}
		if tc.viewSQL != "" {
			payload["viewSql"] = tc.viewSQL
		}
		if tc.routineSQL != "" {
			payload["routineSql"] = tc.routineSQL
		}
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s %s (%s), got %d: %s", tc.connID, tc.action, tc.viewSQL+tc.routineSQL, w.Code, w.Body.String())
		}
	}

	if wBenign := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
		"database":    "app",
		"objectName":  "p_benign_bs",
		"objectType":  "PROCEDURE",
		"action":      "CREATE_ROUTINE",
		"previewOnly": true,
		"routineSql":  "CREATE PROCEDURE p_benign_bs() COMMENT /*!50001 'abc\\\\' */ BEGIN SELECT 1; END",
	}); wBenign.Code != http.StatusOK {
		t.Fatalf("expected 200 for benign MySQL CREATE_ROUTINE with 'abc\\\\' inside exec comment, got %d: %s", wBenign.Code, wBenign.Body.String())
	}

	for _, tc := range []struct {
		connID string
		sql    string
	}{
		{mysqlID, "SELECT * FROM users /*!50000 FOR */ /*!99999 ' \\*/ SHARE /* ' */"},
		{mysqlID, "SELECT 1 /*!99999 '\\*/ /*!50001 UNION SELECT LOAD_FILE(0x2f6574632f706173737764) -- '\n */"},
		{mysqlID, "SELECT 1 /*!99999 \"\\*/ /*!50001 UNION SELECT LOAD_FILE(0x2f6574632f706173737764) -- \"\n */"},
		{mariaID, "SELECT * FROM users /*M!50000 FOR */ /*M!999999 ' \\*/ SHARE /* ' */"},
		{mysqlID, "SELECT * FROM users /*!50000 FOR /*!99999 ' */ SHARE /* ' */ */"},
		{mariaID, "SELECT * FROM users /*M!50000 FOR /*M!999999 ' */ SHARE /* ' */ */"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{
			"query": tc.sql,
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for readonly query %q on conn %s, got %d: %s", tc.sql, tc.connID, w.Code, w.Body.String())
		}
	}

	// 7. Iteration 6 live HTTP regressions: nested executable-comment DEFINER bypasses & benign /*!50000 'ok' */
	for _, tc := range []struct {
		name   string
		connID string
		sql    string
	}{
		{"mysql_nest_bs_1close", mysqlID, "CREATE PROCEDURE p1() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_nest_bs_2close", mysqlID, "CREATE PROCEDURE p2() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_nobs_1close", mysqlID, "CREATE PROCEDURE p3() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_nest_nobs_2close", mysqlID, "CREATE PROCEDURE p4() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_dbl_nobs", mysqlID, "CREATE PROCEDURE p5() SQL /*!50000 SECURITY /*!99999 \" */ DEFINER /* \" */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_bt_nobs", mysqlID, "CREATE PROCEDURE p6() SQL /*!50000 SECURITY /*!99999 ` */ DEFINER /* ` */ */ BEGIN SELECT 1; END"},
		{"maria_nest_nobs_2close", mariaID, "CREATE PROCEDURE pm1() SQL /*M!50000 SECURITY /*M!999999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"maria_nest_bs_1close", mariaID, "CREATE PROCEDURE pm2() SQL /*M!50000 SECURITY /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_direct_unclosed", mysqlID, "CREATE PROCEDURE pu() SQL /*!50000 SECURITY ' */ DEFINER /* ' */ BEGIN SELECT 1; END"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  "PROCEDURE",
			"action":      "CREATE_ROUTINE",
			"previewOnly": true,
			"routineSql":  tc.sql,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400 for nested exec-comment DEFINER bypass, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	if wBenignNest := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
		"database":    "app",
		"objectName":  "p_benign_exec_ok",
		"objectType":  "PROCEDURE",
		"action":      "CREATE_ROUTINE",
		"previewOnly": true,
		"routineSql":  "CREATE PROCEDURE p_benign_exec_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END",
	}); wBenignNest.Code != http.StatusOK {
		t.Fatalf("expected 200 for benign MySQL CREATE_ROUTINE with /*!50000 'ok' */, got %d: %s", wBenignNest.Code, wBenignNest.Body.String())
	}

	// 8. Iteration 7 live HTTP regressions: version-mismatched sequential/nested DEFINER, FOR SHARE, and /*!99999 BEGIN */ multi-statement split bypasses
	for _, tc := range []struct {
		name       string
		connID     string
		objType    string
		action     string
		viewSQL    string
		routineSQL string
	}{
		{"ps1", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ps1() /*!50000 SQL SECURITY */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ps1_dq", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", `CREATE PROCEDURE p_dq() /*!50000 SQL SECURITY */ /*!99999 "b" */ DEFINER BEGIN SELECT 1; END`},
		{"ps1_bt", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_bt() /*!50000 SQL SECURITY */ /*!99999 `b` */ DEFINER BEGIN SELECT 1; END"},
		{"ps1_plus", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_s3() /*!50000 SQL SECURITY */ /*!99999 + */ DEFINER BEGIN SELECT 1; END"},
		{"ps2", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ps2() /*!50000 SQL SECURITY */ /*!99999 1 */ DEFINER BEGIN SELECT 1; END"},
		{"ps3", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ps3() /*M!50000 SQL SECURITY */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"p_50001", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_50001() /*!50000 SQL SECURITY */ /*!50001 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph1", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ph1() /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph2", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ph2() /*M!50000 SQL SECURITY /*M!999999 'a' */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph3", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", `CREATE PROCEDURE ph3() /*!50000 SQL SECURITY /*!99999 "a" */ /*!99999 "b" */ DEFINER BEGIN SELECT 1; END`},
		{"ph4", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ph4() /*!50000 SQL SECURITY /*!99999 `a` */ /*!99999 `b` */ DEFINER BEGIN SELECT 1; END"},
		{"ph5", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE ph5() /*!50000 SQL SECURITY /* ' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"fh1", mysqlID, "FUNCTION", "CREATE_ROUTINE", "", "CREATE FUNCTION fh1() RETURNS INT DETERMINISTIC /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER RETURN 1"},
		{"B1", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_adj1() SQL /*!50000 SECURITY */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"B2", mysqlID, "FUNCTION", "CREATE_ROUTINE", "", "CREATE FUNCTION f_adj1() RETURNS INT DETERMINISTIC SQL /*!50000 SECURITY */ /*!99999 'a' */ DEFINER RETURN 1"},
		{"B3", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", `CREATE PROCEDURE p_adj2() SQL /*!50000 SECURITY */ /*!99999 "a" */ DEFINER BEGIN SELECT 1; END`},
		{"B4", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_adj3() SQL /*!50000 SECURITY */ /*!99999 `a` */ DEFINER BEGIN SELECT 1; END"},
		{"B5", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_adj4() SQL /*!50000 SECURITY /* x */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"B6", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_adj1() SQL /*M!50000 SECURITY */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"B7", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_adj2() SQL /*M!50000 SECURITY /* x */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"p_num", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_num() SQL /*!50000 SECURITY */ /*!99999 1 */ DEFINER BEGIN SELECT 1; END"},
		{"p_reset", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_reset() /*!50000 SQL SECURITY /*!99999 */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"split_proc_mysql", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_b1() /*!99999 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"split_proc_maria", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_b2() /*M!999999 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"split_trig_mysql", mysqlID, "TRIGGER", "CREATE_TRIGGER", "", "CREATE TRIGGER t_b1 BEFORE INSERT ON items FOR EACH ROW /*!99999 BEGIN */ SET @a=1; DROP TABLE users;"},
		{"split_view_mysql", mysqlID, "VIEW", "CREATE_VIEW", "CREATE VIEW v_b1 AS /*!99999 SELECT 1; DROP TABLE users */", ""},
	} {
		payload := map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  tc.objType,
			"action":      tc.action,
			"previewOnly": true,
		}
		if tc.viewSQL != "" {
			payload["viewSql"] = tc.viewSQL
		}
		if tc.routineSQL != "" {
			payload["routineSql"] = tc.routineSQL
		}
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		name   string
		connID string
		sql    string
	}{
		{"mysql_for_share_adj", mysqlID, "SELECT * FROM users /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"maria_for_share_adj", mariaID, "SELECT * FROM users /*M!50000 FOR */ /*M!999999 x */ SHARE"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{"query": tc.sql})
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403 for readonly query, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		name       string
		connID     string
		objType    string
		action     string
		viewSQL    string
		routineSQL string
	}{
		{"benign_maria_view", mariaID, "VIEW", "CREATE_VIEW", "CREATE /*M!50003 ALGORITHM=UNDEFINED */ VIEW v_ok AS SELECT 1", ""},
		{"benign_maria_100001", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_ok() COMMENT /*M!100001 'ok' */ BEGIN SELECT 1; END"},
	} {
		payload := map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  tc.objType,
			"action":      tc.action,
			"previewOnly": true,
		}
		if tc.viewSQL != "" {
			payload["viewSql"] = tc.viewSQL
		}
		if tc.routineSQL != "" {
			payload["routineSql"] = tc.routineSQL
		}
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, payload)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 for benign control, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// 9. Iteration 8 live HTTP regressions: preserveExecQuotes clamping, 35+ dummy empty/expr comments, MariaDB 5x4 cross-family
	var sbEmpty35 strings.Builder
	for i := 0; i < 35; i++ {
		fmt.Fprintf(&sbEmpty35, "/*!%d*/ ", 10001+i)
	}
	empty35 := sbEmpty35.String()

	var sbExpr40 strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sbExpr40, "/*!%d 1*/ ", 10001+i)
	}
	expr40 := sbExpr40.String()

	var sbMariaCross strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sbMariaCross, "/*!%d*/ ", 10001+i)
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&sbMariaCross, "/*M!%d*/ ", 10001+i)
	}
	mariaCross := sbMariaCross.String()

	for _, tc := range []struct {
		name       string
		connID     string
		objType    string
		action     string
		viewSQL    string
		routineSQL string
	}{
		{"pq_definer", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_pq() COMMENT /*!50000 ' */ ' */ /*!50000 SQL SECURITY */ /*!80000 x */ DEFINER BEGIN SELECT 1; END"},
		{"pq_split", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_pqs() COMMENT /*!50000 ' */ ' */ /*!80000 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"cap35_definer", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c35() " + empty35 + " /*!50000 SQL SECURITY */ /*!99999 'x' */ DEFINER BEGIN SELECT 1; END"},
		{"cap40_definer", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c40() " + expr40 + " /*!50000 SQL SECURITY */ /*!99999 'x' */ DEFINER BEGIN SELECT 1; END"},
		{"cap35_definer_eq", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c35eq() " + empty35 + " /*!50000 DEFINER */ /*!99999 'x' */ = 'root'@'localhost' BEGIN SELECT 1; END"},
		{"cap35_split_proc", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c35sp() " + empty35 + " /*!99999 BEGIN */ SELECT 1 /*!50000 ; DROP TABLE users */"},
		{"cap35_split_trig", mysqlID, "TRIGGER", "CREATE_TRIGGER", "", "CREATE TRIGGER t_c35sp BEFORE INSERT ON items FOR EACH ROW " + empty35 + " /*!99999 BEGIN */ SET @a=1 /*!50000 ; DROP TABLE users */"},
		{"cap35_split_view", mysqlID, "VIEW", "CREATE_VIEW", "CREATE VIEW v_c35sp AS SELECT " + empty35 + " 1 /*!50000 ; DROP TABLE users */", ""},
		{"maria_cross_5x4", mariaID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_c54() " + mariaCross + " /*M!100300 SQL SECURITY */ /*!80000 x */ /*!50000 DEFINER */ BEGIN SELECT 1; END"},
	} {
		payload := map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  tc.objType,
			"action":      tc.action,
			"previewOnly": true,
		}
		if tc.viewSQL != "" {
			payload["viewSql"] = tc.viewSQL
		}
		if tc.routineSQL != "" {
			payload["routineSql"] = tc.routineSQL
		}
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		name   string
		connID string
		sql    string
	}{
		{"pq_for_share", mysqlID, "SELECT * FROM users WHERE 'a' = /*!50000 ' */ ' */ /*!50000 FOR */ /*!80000 x */ SHARE"},
		{"cap35_for_share", mysqlID, "SELECT " + empty35 + " * FROM users /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"cap40_for_share", mysqlID, "SELECT * FROM users WHERE 1 = " + expr40 + " 1 /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"maria_cross_5x4_for_share", mariaID, "SELECT " + mariaCross + " * FROM users /*M!100300 FOR */ /*!80000 x */ /*!50000 SHARE */"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{"query": tc.sql})
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403 for readonly query, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	if wBenign35 := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
		"database":    "app",
		"objectName":  "p_benign_35_empty",
		"objectType":  "PROCEDURE",
		"action":      "CREATE_ROUTINE",
		"previewOnly": true,
		"routineSql":  "CREATE PROCEDURE p_benign_35_empty() " + empty35 + " COMMENT 'ok' BEGIN SELECT 1; END",
	}); wBenign35.Code != http.StatusOK {
		t.Fatalf("benign_35_empty: expected 200, got %d: %s", wBenign35.Code, wBenign35.Body.String())
	}

	// 10. Iteration 9/10 live HTTP regressions: MySQL/MariaDB '--<non-whitespace>', '--- '\n', '--\x7f', and active exec '--'/'#'
	for _, tc := range []struct {
		name   string
		connID string
		sql    string
	}{
		{"mysql_dd1_for_update", mysqlID, "SELECT * FROM users WHERE id = 1 --1 FOR UPDATE"},
		{"maria_dd1_for_share", mariaID, "SELECT * FROM users WHERE id = 1 --1 FOR SHARE"},
		{"mysql_dd1_outfile", mysqlID, "SELECT 1 --1 INTO OUTFILE '/tmp/pwn.txt'"},
		{"mysql_dd1_loadfile", mysqlID, "SELECT 1 --1 UNION SELECT LOAD_FILE('/etc/passwd')"},
		{"mysql_dd1_stacked", mysqlID, "SELECT 1 --1; DROP TABLE users"},
		{"mysql_ddd1_for_update", mysqlID, "SELECT * FROM users WHERE id = 1 ---1 FOR UPDATE"},
		{"mysql_ddd_quote_for_update", mysqlID, "SELECT 1 --- '\n 1 FOR UPDATE"},
		{"mysql_ddd_quote_stacked", mysqlID, "SELECT 1 --- '\n 1; DROP TABLE users"},
		{"mysql_ddd_quote_semi_stacked", mysqlID, "SELECT 1 --- '\n; DROP TABLE users"},
		{"mysql_dddd_quote_for_update", mysqlID, "SELECT 1---- '\n 1 FOR UPDATE"},
		{"mysql_ddd_quote_outfile", mysqlID, "SELECT 1 --- '\n 1 INTO OUTFILE '/tmp/pwn.txt'"},
		{"mysql_exec_dd1_for_update", mysqlID, "/*!50000 SELECT 1 --1\n FOR UPDATE */"},
		{"mysql_exec_ddd_quote_stacked", mysqlID, "/*!50000 SELECT 1 --- '\n 1; DROP TABLE users */"},
		{"mysql_exec_ddd_quote_for_update", mysqlID, "/*!50000 SELECT 1 --- '\n 1 FOR UPDATE */"},
		{"mysql_del_0x7f_for_update", mysqlID, "SELECT 1 --\x7f'\n1 FOR UPDATE"},
		{"maria_del_0x7f_stacked", mariaID, "SELECT 1 --\x7f'\n1; DROP TABLE users"},
		{"mysql_exec_dash_nl_stacked", mysqlID, "SELECT 1 /*!50000 -- ' */ '\n */ ; DROP TABLE users"},
		{"mysql_exec_hash_nl_for_update", mysqlID, "SELECT 1 /*!50000 # ' */ '\n */ FOR UPDATE"},
		{"maria_exec_hash_nl_for_share", mariaID, "SELECT 1 /*M!50000 # ' */ '\n */ FOR SHARE"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{"query": tc.sql})
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403 for readonly query %q, got %d: %s", tc.name, tc.sql, w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		name       string
		connID     string
		objType    string
		action     string
		viewSQL    string
		routineSQL string
	}{
		{"proc_dd1_split", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_dd1() BEGIN SELECT 1 --1; END; DROP TABLE users"},
		{"view_dd1_split", mysqlID, "VIEW", "CREATE_VIEW", "CREATE VIEW v_dd1 AS SELECT 1 --1; DROP TABLE users", ""},
		{"view_dddq_split", mysqlID, "VIEW", "CREATE_VIEW", "CREATE VIEW v_dddq AS SELECT 1 --- '\n1; DROP TABLE users", ""},
		{"proc_ddd1_split", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_ddd1() BEGIN SELECT 1 --- '\n 1; END; DROP TABLE users"},
		{"proc_ddd_def", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_ddd_def() SELECT 1 --- '\n 1 SQL SECURITY DEFINER BEGIN SELECT 1; END"},
		{"view_del_split", mysqlID, "VIEW", "CREATE_VIEW", "CREATE VIEW v_del AS SELECT 1 --\x7f'\n1; DROP TABLE users", ""},
		{"proc_exd_split", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_exd() BEGIN SELECT 1 /*!50000 -- ' */ '\n */ ; END; DROP TABLE users"},
		{"proc_exh_split", mysqlID, "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_exh() BEGIN SELECT 1 /*!50000 # ' */ '\n */ ; END; DROP TABLE users"},
	} {
		payload := map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  tc.objType,
			"action":      tc.action,
			"previewOnly": true,
		}
		if tc.viewSQL != "" {
			payload["viewSql"] = tc.viewSQL
		}
		if tc.routineSQL != "" {
			payload["routineSql"] = tc.routineSQL
		}
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/object/action", rwTok, payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// Benign MySQL '--1' arithmetic, '---' comment, whitespace/DEL '--' & executable-comment '--' controls + PostgreSQL ANSI '--1' comment
	for _, tc := range []struct {
		name    string
		viewSQL string
	}{
		{"v_dd1_benign", "CREATE VIEW v_dd1_benign AS SELECT 1 --1"},
		{"v_ex_benign_nl", "CREATE VIEW v_ex_benign_nl AS /*!50000 -- */\nSELECT 1"},
		{"v_ex_benign_sp", "CREATE VIEW v_ex_benign_sp AS /*!50000 -- */ SELECT 1"},
	} {
		if wBenignView := doJSON(http.MethodPost, "/api/conn/"+mysqlID+"/object/action", rwTok, map[string]any{
			"database":    "app",
			"objectName":  tc.name,
			"objectType":  "VIEW",
			"action":      "CREATE_VIEW",
			"previewOnly": true,
			"viewSql":     tc.viewSQL,
		}); wBenignView.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", tc.name, wBenignView.Code, wBenignView.Body.String())
		}
	}
	for _, tc := range []struct {
		name   string
		connID string
		sql    string
	}{
		{"mysql_benign_dd1", mysqlID, "SELECT 1 --1"},
		{"mysql_benign_ddd_comment", mysqlID, "SELECT * FROM users WHERE id = 1 --- FOR UPDATE"},
		{"mysql_benign_dd_space", mysqlID, "SELECT 1 -- comment\n"},
		{"mysql_benign_dd_tab", mysqlID, "SELECT 1 --\tcomment\n"},
		{"mysql_benign_dd_del", mysqlID, "SELECT 1 --\x7fcomment\n"},
		{"mysql_benign_dd_eof", mysqlID, "SELECT 1 --"},
		{"mysql_benign_exec_dd_sp", mysqlID, "/*!50000 -- */ SELECT 1"},
		{"mysql_benign_exec_dd_nl", mysqlID, "/*!50000 -- */\nSELECT 1"},
		{"pg_ansi_dd1_comment", pgID, "SELECT * FROM users WHERE id = 1 --1 FOR UPDATE"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{"query": tc.sql})
		if w.Code == http.StatusForbidden {
			t.Fatalf("%s: expected benign query %q to pass readonly guard (not 403), got %d: %s", tc.name, tc.sql, w.Code, w.Body.String())
		}
	}

	// 11. Iteration 11 live HTTP regressions: bracket-quote & backtick dialect scoping across /table/create, /table/alter, /object/action, /table/browse, and /query
	// All 6 challenger_1_iter10 reproduced endpoints + '[1; DROP TABLE users; -- ]' + backtick checks
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{
			name: "pg_create_default_bracket_smuggle",
			path: "/api/conn/" + pgID + "/table/create",
			body: map[string]any{
				"schema": "public", "table": "t_pwn", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": "ARRAY['a]b']) ; DROP TABLE users -- '"},
				},
			},
		},
		{
			name: "pg_alter_default_bracket_smuggle",
			path: "/api/conn/" + pgID + "/table/alter",
			body: map[string]any{
				"schema": "public", "table": "t_pwn", "previewOnly": true,
				"actions": []map[string]any{
					{"action": "ADD_COLUMN", "column": map[string]any{"name": "v", "type": "TEXT", "nullable": false, "defaultValue": "ARRAY['a]b'] ; DROP TABLE users -- '"}},
				},
			},
		},
		{
			name: "ch_create_orderby_bracket_smuggle",
			path: "/api/conn/" + chID + "/table/create",
			body: map[string]any{
				"database": "default", "table": "t_ch_pwn", "previewOnly": true,
				"columns": []map[string]any{{"name": "id", "type": "UInt64"}},
				"orderBy": "['a]b'] ; DROP TABLE users -- '",
			},
		},
		{
			name: "ch_matview_engine_clause_bracket_smuggle",
			path: "/api/conn/" + chID + "/object/action",
			body: map[string]any{
				"database": "default", "objectName": "mv_pwn", "objectType": "MATERIALIZED_VIEW", "action": "CREATE_OR_REPLACE", "previewOnly": true,
				"spec": map[string]any{
					"engineClause": "MergeTree() ORDER BY ['a]b'] ; DROP TABLE users -- '",
					"selectSql":    "SELECT 1",
				},
			},
		},
		{
			name: "mysql_create_default_bracket_smuggle",
			path: "/api/conn/" + mysqlID + "/table/create",
			body: map[string]any{
				"database": "app", "table": "t_my_pwn", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "VARCHAR(64)", "nullable": false, "defaultValue": "['a]b']); DROP TABLE users -- '"},
				},
			},
		},
		{
			name: "duckdb_create_default_bracket_smuggle",
			path: "/api/conn/" + duckID + "/table/create",
			body: map[string]any{
				"schema": "main", "table": "t_dk_pwn", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INTEGER", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "VARCHAR", "nullable": false, "defaultValue": "['a]b']); DROP TABLE users -- '"},
				},
			},
		},
		{
			name: "pg_create_unbalanced_bracket_drop",
			path: "/api/conn/" + pgID + "/table/create",
			body: map[string]any{
				"schema": "public", "table": "t_pg_br", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": "[1; DROP TABLE users; -- ]"},
				},
			},
		},
		{
			name: "ch_create_unbalanced_bracket_drop",
			path: "/api/conn/" + chID + "/table/create",
			body: map[string]any{
				"database": "default", "table": "t_ch_br", "previewOnly": true,
				"columns": []map[string]any{{"name": "id", "type": "UInt64"}},
				"orderBy": "[1; DROP TABLE users; -- ]",
			},
		},
		{
			name: "mysql_create_unbalanced_bracket_drop",
			path: "/api/conn/" + mysqlID + "/table/create",
			body: map[string]any{
				"database": "app", "table": "t_my_br", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "VARCHAR(64)", "nullable": false, "defaultValue": "[1; DROP TABLE users; -- ]"},
				},
			},
		},
		{
			name: "duckdb_create_unbalanced_bracket_drop",
			path: "/api/conn/" + duckID + "/table/create",
			body: map[string]any{
				"schema": "main", "table": "t_dk_br", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INTEGER", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "VARCHAR", "nullable": false, "defaultValue": "[1; DROP TABLE users; -- ]"},
				},
			},
		},
		{
			name: "pg_create_backtick_smuggle",
			path: "/api/conn/" + pgID + "/table/create",
			body: map[string]any{
				"schema": "public", "table": "t_pg_bt", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": "`'`; DROP TABLE users -- `"},
				},
			},
		},
		{
			name: "ora_create_backtick_smuggle",
			path: "/api/conn/" + oraID + "/table/create",
			body: map[string]any{
				"schema": "SCOTT", "table": "T_ORA_BT", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "ID", "type": "NUMBER", "isPrimaryKey": true, "nullable": false},
					{"name": "VAL", "type": "VARCHAR2(64)", "nullable": false, "defaultValue": "`'`; DROP TABLE users -- `"},
				},
			},
		},
		{
			name: "mssql_create_bracket_semi_smuggle",
			path: "/api/conn/" + mssqlID + "/table/create",
			body: map[string]any{
				"schema": "dbo", "table": "t_ms_semi", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "INT", "nullable": false, "defaultValue": "[my_col]; DROP TABLE users"},
				},
			},
		},
		{
			name: "sqlite_create_bracket_semi_smuggle",
			path: "/api/conn/" + sqliteID + "/table/create",
			body: map[string]any{
				"table": "t_sq_semi", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INTEGER", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "INTEGER", "nullable": false, "defaultValue": "[my_col]; DROP TABLE users"},
				},
			},
		},
	} {
		w := doJSON(http.MethodPost, tc.path, rwTok, tc.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// BrowseData '[1 /*]' & '[1; DROP TABLE users; -- ]' & backtick smuggle on non-bracket/non-backtick engines (must fail WHERE filter check with 400 before dialing)
	for _, tc := range []struct {
		name   string
		connID string
		filter string
	}{
		{"pg_browse_bracket_cmt", pgID, "[1 /*]"},
		{"ch_browse_bracket_cmt", chID, "[1 /*]"},
		{"mysql_browse_bracket_cmt", mysqlID, "[1 /*]"},
		{"duck_browse_bracket_cmt", duckID, "[1 /*]"},
		{"pg_browse_bracket_drop", pgID, "[1; DROP TABLE users; -- ]"},
		{"pg_browse_backtick_smuggle", pgID, "`'`; DROP TABLE users -- `"},
		{"ora_browse_backtick_smuggle", oraID, "`'`; DROP TABLE users -- `"},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/table/browse", rwTok, map[string]any{
			"database": "app", "schema": "public", "table": "items", "filter": tc.filter,
		})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "WHERE filter") {
			t.Fatalf("%s: expected 400 WHERE filter rejection, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// Readonly /query backtick multi-statement smuggling on Postgres / Oracle / SQL Server must return 403
	for _, tc := range []struct {
		name   string
		connID string
	}{
		{"pg_ro_backtick", pgID},
		{"ora_ro_backtick", oraID},
		{"mssql_ro_backtick", mssqlID},
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/query", roTok, map[string]any{
			"query": "SELECT `a`; DROP TABLE users; SELECT `b`",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403 for backtick multi-statement query, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// Benign controls: Postgres ARRAY['a', 'b'] / ARRAY[1, 2], ClickHouse ORDER BY (id, ts), SQL Server / SQLite [my_col]
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{
			name: "pg_benign_array_default",
			path: "/api/conn/" + pgID + "/table/create",
			body: map[string]any{
				"schema": "public", "table": "t_pg_arr_ok", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "tags", "type": "TEXT[]", "nullable": false, "defaultValue": "ARRAY['a', 'b']"},
					{"name": "nums", "type": "INT[]", "nullable": false, "defaultValue": "ARRAY[1, 2]"},
				},
			},
		},
		{
			name: "ch_benign_orderby",
			path: "/api/conn/" + chID + "/table/create",
			body: map[string]any{
				"database": "default", "table": "t_ch_ok", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "UInt64"},
					{"name": "ts", "type": "DateTime"},
				},
				"orderBy": "(id, ts)",
			},
		},
		{
			name: "ch_benign_matview_engine",
			path: "/api/conn/" + chID + "/object/action",
			body: map[string]any{
				"database": "default", "objectName": "mv_ok", "objectType": "MATERIALIZED_VIEW", "action": "CREATE_OR_REPLACE", "previewOnly": true,
				"spec": map[string]any{
					"engineClause": "MergeTree() ORDER BY (id, ts)",
					"selectSql":    "SELECT 1",
				},
			},
		},
		{
			name: "mssql_benign_bracket_col",
			path: "/api/conn/" + mssqlID + "/table/create",
			body: map[string]any{
				"schema": "dbo", "table": "t_ms_ok", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "INT", "nullable": false, "defaultValue": "[my_col]"},
				},
			},
		},
		{
			name: "sqlite_benign_bracket_col",
			path: "/api/conn/" + sqliteID + "/table/create",
			body: map[string]any{
				"table": "t_sq_ok", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INTEGER", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "INTEGER", "nullable": false, "defaultValue": "[my_col]"},
				},
			},
		},
	} {
		w := doJSON(http.MethodPost, tc.path, rwTok, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 benign control, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}
	if wSQLiteBr := doJSON(http.MethodPost, "/api/conn/"+sqliteID+"/table/browse", roTok, map[string]any{
		"table": "items", "filter": "[id] = 1",
	}); wSQLiteBr.Code != http.StatusOK {
		t.Fatalf("sqlite_benign_browse_bracket: expected 200, got %d: %s", wSQLiteBr.Code, wSQLiteBr.Body.String())
	}

	// 12. Iteration 12 live HTTP regressions: PG/DuckDB UTF-8 & >64-byte $tag$, Oracle/Dameng q'...' alt-quotes, SQLite ']]' bracket differential
	tag65 := "$" + strings.Repeat("a", 65) + "$"
	tag80 := "$" + strings.Repeat("a", 80) + "$"
	tag200 := "$" + strings.Repeat("a", 200) + "$"

	// 12a. PostgreSQL & DuckDB dollar-quote attacks across /table/create, /table/browse, /query (ro & DuckDB Web sandbox)
	pgDuckBadDefaults := []string{
		"$é$'$é$; DROP TABLE users -- '",
		tag65 + "'" + tag65 + "; DROP TABLE users -- '",
		tag200 + "'" + tag200 + "; CALL duckdb_secrets(); -- '",
	}
	for _, connTC := range []struct {
		name   string
		connID string
		schema string
	}{
		{"pg", pgID, "public"},
		{"duckdb", duckID, "main"},
	} {
		for idx, payload := range pgDuckBadDefaults {
			wCreate := doJSON(http.MethodPost, "/api/conn/"+connTC.connID+"/table/create", rwTok, map[string]any{
				"schema": connTC.schema, "table": "t_dq_pwn", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
					{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": payload},
				},
			})
			if wCreate.Code != http.StatusBadRequest {
				t.Fatalf("%s create[%d]: expected 400 for %q, got %d: %s", connTC.name, idx, payload, wCreate.Code, wCreate.Body.String())
			}

			wBrowse := doJSON(http.MethodPost, "/api/conn/"+connTC.connID+"/table/browse", rwTok, map[string]any{
				"schema": connTC.schema, "table": "items", "filter": "val = " + payload,
			})
			if wBrowse.Code != http.StatusBadRequest || !strings.Contains(wBrowse.Body.String(), "WHERE filter") {
				t.Fatalf("%s browse[%d]: expected 400 WHERE filter rejection for %q, got %d: %s", connTC.name, idx, payload, wBrowse.Code, wBrowse.Body.String())
			}

			wRO := doJSON(http.MethodPost, "/api/conn/"+connTC.connID+"/query", roTok, map[string]any{
				"query": "SELECT " + payload,
			})
			if wRO.Code != http.StatusForbidden {
				t.Fatalf("%s ro query[%d]: expected 403 for %q, got %d: %s", connTC.name, idx, payload, wRO.Code, wRO.Body.String())
			}
		}
	}
	for _, duckSandboxPayload := range []string{
		"SELECT $é$'$é$; CALL duckdb_secrets(); -- '",
		"SELECT " + tag65 + "'" + tag65 + "; CALL duckdb_secrets(); -- '",
		"SELECT " + tag200 + "'" + tag200 + "; CALL duckdb_secrets(); -- '",
	} {
		wDuck := doJSON(http.MethodPost, "/api/conn/"+duckID+"/query", rwTok, map[string]any{
			"query": duckSandboxPayload,
		})
		if wDuck.Code != http.StatusBadRequest ||
			!strings.Contains(wDuck.Body.String(), "forbidden DuckDB keyword/function") ||
			!strings.Contains(wDuck.Body.String(), "CALL") {
			t.Fatalf("duckdb sandbox: expected 400 forbidden CALL for %q, got %d: %s", duckSandboxPayload, wDuck.Code, wDuck.Body.String())
		}
	}
	// Benign PostgreSQL/DuckDB UTF-8 & >64-byte dollar-quote controls
	for _, benignDQ := range []string{
		"$é$hello; world$é$",
		tag80 + "hello; world" + tag80,
	} {
		wOK := doJSON(http.MethodPost, "/api/conn/"+pgID+"/table/create", rwTok, map[string]any{
			"schema": "public", "table": "t_dq_ok", "previewOnly": true,
			"columns": []map[string]any{
				{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
				{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": benignDQ},
			},
		})
		if wOK.Code != http.StatusOK {
			t.Fatalf("pg benign dollar-quote %q: expected 200, got %d: %s", benignDQ, wOK.Code, wOK.Body.String())
		}
		wDuckRO := doJSON(http.MethodPost, "/api/conn/"+duckID+"/query", roTok, map[string]any{
			"query": "SELECT " + benignDQ + " AS body",
		})
		if wDuckRO.Code == http.StatusForbidden {
			t.Fatalf("duckdb benign dollar-quote %q: unexpected 403 readonly rejection: %s", benignDQ, wDuckRO.Body.String())
		}
	}

	// 12b. Oracle & Dameng q'...' alternative quoting attacks & benign/boundary controls
	oraDamengBad := []string{
		"q'!a'!'; DROP TABLE users -- '",
		"Q'[a]'; DROP TABLE users -- '",
		"q'{a}'; DROP TABLE users -- '",
		"q'(a)'; DROP TABLE users -- '",
		"q'<a'>; DROP TABLE users -- '",
		"nq'!a'!'; DROP TABLE users -- '",
	}
	for _, engTC := range []struct {
		name   string
		connID string
		schema string
	}{
		{"oracle", oraID, "SCOTT"},
		{"dameng", damengID, "SYSDBA"},
	} {
		for idx, payload := range oraDamengBad {
			wCreate := doJSON(http.MethodPost, "/api/conn/"+engTC.connID+"/table/create", rwTok, map[string]any{
				"schema": engTC.schema, "table": "T_ALTQ_PWN", "previewOnly": true,
				"columns": []map[string]any{
					{"name": "ID", "type": "NUMBER", "isPrimaryKey": true, "nullable": false},
					{"name": "VAL", "type": "VARCHAR2(64)", "nullable": false, "defaultValue": payload},
				},
			})
			if wCreate.Code != http.StatusBadRequest {
				t.Fatalf("%s create[%d]: expected 400 for %q, got %d: %s", engTC.name, idx, payload, wCreate.Code, wCreate.Body.String())
			}

			wRO := doJSON(http.MethodPost, "/api/conn/"+engTC.connID+"/query", roTok, map[string]any{
				"query": "SELECT " + payload + " FROM dual",
			})
			if wRO.Code != http.StatusForbidden {
				t.Fatalf("%s ro query[%d]: expected 403 for %q, got %d: %s", engTC.name, idx, payload, wRO.Code, wRO.Body.String())
			}
		}
		// Benign Oracle/Dameng q'...' control on /table/create & /query (not 403)
		if wAltOK := doJSON(http.MethodPost, "/api/conn/"+engTC.connID+"/table/create", rwTok, map[string]any{
			"schema": engTC.schema, "table": "T_ALTQ_OK", "previewOnly": true,
			"columns": []map[string]any{
				{"name": "ID", "type": "NUMBER", "isPrimaryKey": true, "nullable": false},
				{"name": "VAL", "type": "VARCHAR2(64)", "nullable": false, "defaultValue": "q'!hello's world!'"},
			},
		}); wAltOK.Code != http.StatusOK {
			t.Fatalf("%s benign q'...' create: expected 200, got %d: %s", engTC.name, wAltOK.Code, wAltOK.Body.String())
		}
		if wAltQueryOK := doJSON(http.MethodPost, "/api/conn/"+engTC.connID+"/query", roTok, map[string]any{
			"query": "SELECT q'!hello's world!' FROM dual",
		}); wAltQueryOK.Code == http.StatusForbidden {
			t.Fatalf("%s benign q'...' readonly query: unexpected 403: %s", engTC.name, wAltQueryOK.Body.String())
		}
		// Identifier boundary control: my_q'! must NOT open alternative quote (otherwise !' would swallow '; DROP TABLE users')
		if wBoundary := doJSON(http.MethodPost, "/api/conn/"+engTC.connID+"/query", roTok, map[string]any{
			"query": "SELECT my_q'!a'; DROP TABLE users -- !' FROM dual",
		}); wBoundary.Code != http.StatusForbidden {
			t.Fatalf("%s boundary my_q'! attack: expected 403, got %d: %s", engTC.name, wBoundary.Code, wBoundary.Body.String())
		}
	}

	// 12c. SQLite ']]' bracket-escape differential vs SQL Server
	if wSqCreate := doJSON(http.MethodPost, "/api/conn/"+sqliteID+"/table/create", rwTok, map[string]any{
		"table": "t_sq_dbl_br", "previewOnly": true,
		"columns": []map[string]any{
			{"name": "id", "type": "INTEGER", "isPrimaryKey": true, "nullable": false},
			{"name": "val", "type": "TEXT", "nullable": false, "defaultValue": "[a]] , x INT); DROP TABLE users; -- ]"},
		},
	}); wSqCreate.Code != http.StatusBadRequest {
		t.Fatalf("sqlite ']]' create: expected 400, got %d: %s", wSqCreate.Code, wSqCreate.Body.String())
	}
	if wSqRO := doJSON(http.MethodPost, "/api/conn/"+sqliteID+"/query", roTok, map[string]any{
		"query": "SELECT [a]] ; DROP TABLE users; -- ]",
	}); wSqRO.Code != http.StatusForbidden {
		t.Fatalf("sqlite ']]' ro query: expected 403, got %d: %s", wSqRO.Code, wSqRO.Body.String())
	}
	if wMsOK := doJSON(http.MethodPost, "/api/conn/"+mssqlID+"/table/create", rwTok, map[string]any{
		"schema": "dbo", "table": "t_ms_dbl_ok", "previewOnly": true,
		"columns": []map[string]any{
			{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
			{"name": "val", "type": "INT", "nullable": false, "defaultValue": "[my]]col]"},
		},
	}); wMsOK.Code != http.StatusOK {
		t.Fatalf("mssql benign [my]]col]: expected 200, got %d: %s", wMsOK.Code, wMsOK.Body.String())
	}

	// 13. Iteration 13 live HTTP regressions: ValidateColumnTypeForEngine dialect escapes & SQLite virtual table/rebuild ']]'
	tidbID := saveShared(driver.ConnectionConfig{Name: "TiDB", Type: "tidb", Host: "127.0.0.1", Port: 4000, Username: "u", Database: "app"})
	obID := saveShared(driver.ConnectionConfig{Name: "OceanBase", Type: "oceanbase", Host: "127.0.0.1", Port: 2881, Username: "u", Database: "app"})
	kbID := saveShared(driver.ConnectionConfig{Name: "Kingbase", Type: "kingbase", Host: "127.0.0.1", Port: 54321, Username: "system", Database: "app"})

	badColTypeCases := []struct {
		name    string
		connID  string
		schema  string
		colType string
	}{
		{"mysql_enum_bs", mysqlID, "", `ENUM('a\'')); DROP TABLE users; SELECT (')`},
		{"mysql_enum_bs_comment", mysqlID, "", `ENUM('a\''); DROP TABLE users -- ')`},
		{"mariadb_enum_bs", mariaID, "", `ENUM('a\'')); DROP TABLE users; SELECT (')`},
		{"tidb_enum_bs", tidbID, "", `ENUM('a\'')); DROP TABLE users; SELECT (')`},
		{"oceanbase_enum_bs", obID, "", `ENUM('a\'')); DROP TABLE users; SELECT (')`},
		{"clickhouse_enum8_bs", chID, "", `Enum8('a\'')); DROP TABLE users; SELECT (')`},
		{"postgres_esc_prefix", pgID, "public", `INT(E'a\'')); DROP TABLE users; SELECT (')`},
		{"kingbase_esc_prefix", kbID, "public", `INT(E'a\'')); DROP TABLE users; SELECT (')`},
		{"duckdb_esc_prefix", duckID, "main", `INT(E'a\'')); DROP TABLE users; SELECT (')`},
		{"oracle_alt_quote", oraID, "SCOTT", `NUMBER((q'(a')'); DROP TABLE users; SELECT (')`},
		{"dameng_alt_quote", damengID, "SYSDBA", `NUMBER((q'(a')'); DROP TABLE users; SELECT (')`},
		{"sqlite_bracket_dbl", sqliteID, "", `INT([a]] ); DROP TABLE users; -- ])`},
		{"mssql_bracket_single", mssqlID, "dbo", `INT([a] ); DROP TABLE users; -- ])`},
	}
	for _, tc := range badColTypeCases {
		wCreate := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/table/create", rwTok, map[string]any{
			"database": "app", "schema": tc.schema, "table": "t_coltype_pwn", "previewOnly": true,
			"columns": []map[string]any{
				{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
				{"name": "c", "type": tc.colType, "nullable": false},
			},
		})
		if wCreate.Code != http.StatusBadRequest {
			t.Fatalf("%s /table/create: expected 400 for colType %q, got %d: %s", tc.name, tc.colType, wCreate.Code, wCreate.Body.String())
		}
		for _, actName := range []string{"ADD_COLUMN", "MODIFY_COLUMN"} {
			wAlter := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/table/alter", rwTok, map[string]any{
				"database": "app", "schema": tc.schema, "table": "t_coltype_pwn", "previewOnly": true,
				"actions": []map[string]any{
					{"action": actName, "column": map[string]any{"name": "c", "type": tc.colType, "nullable": false}},
				},
			})
			if wAlter.Code != http.StatusBadRequest {
				t.Fatalf("%s /table/alter %s: expected 400 for colType %q, got %d: %s", tc.name, actName, tc.colType, wAlter.Code, wAlter.Body.String())
			}
		}
	}

	// Legitimate dialect column types must remain accepted
	for _, tc := range []struct {
		name    string
		connID  string
		schema  string
		colType string
	}{
		{"mysql_enum_ok", mysqlID, "", `ENUM('a', 'b')`},
		{"mysql_enum_bs_ok", mysqlID, "", `ENUM('a\'b')`},
		{"mysql_set_ok", mysqlID, "", `SET('x', 'y')`},
		{"ch_enum8_ok", chID, "", `Enum8('a'=1, 'b'=2)`},
		{"ch_array_tuple_ok", chID, "", `Array(Tuple(String, Int64))`},
		{"pg_tstz_ok", pgID, "public", `TIMESTAMP(6) WITH TIME ZONE`},
		{"pg_decimal_ok", pgID, "public", `DECIMAL(10, 2)`},
		{"ora_numeric_ok", oraID, "SCOTT", `NUMERIC(18, 4)`},
	} {
		wOK := doJSON(http.MethodPost, "/api/conn/"+tc.connID+"/table/create", rwTok, map[string]any{
			"database": "app", "schema": tc.schema, "table": "t_coltype_ok", "previewOnly": true,
			"columns": []map[string]any{
				{"name": "id", "type": "INT", "isPrimaryKey": true, "nullable": false},
				{"name": "c", "type": tc.colType, "nullable": false},
			},
		})
		if wOK.Code != http.StatusOK {
			t.Fatalf("%s /table/create: expected 200 for valid colType %q, got %d: %s", tc.name, tc.colType, wOK.Code, wOK.Body.String())
		}
	}

	// SQLite virtual table virtualArgs ']]' previewOnly bypass & rebuild ADD_CHECK rejection
	if wVirt := doJSON(http.MethodPost, "/api/conn/"+sqliteID+"/table/create", rwTok, map[string]any{
		"table": "v_pwn2", "previewOnly": true, "virtualModule": "fts5",
		"columns":     []map[string]any{{"name": "body"}},
		"virtualArgs": "[a]] ); DROP TABLE users; -- ]",
	}); wVirt.Code != http.StatusBadRequest {
		t.Fatalf("sqlite virtualArgs ']]': expected 400, got %d: %s", wVirt.Code, wVirt.Body.String())
	}
	if wRebuildChk := doJSON(http.MethodPost, "/api/conn/"+sqliteID+"/table/alter", rwTok, map[string]any{
		"table": "items", "previewOnly": true,
		"actions": []map[string]any{
			{"action": "ADD_CHECK", "check": map[string]any{"name": "chk_bad", "expression": "[a]] ); DROP TABLE users; -- ]"}},
		},
	}); wRebuildChk.Code != http.StatusBadRequest {
		t.Fatalf("sqlite alter ADD_CHECK ']]': expected 400, got %d: %s", wRebuildChk.Code, wRebuildChk.Body.String())
	}
}
