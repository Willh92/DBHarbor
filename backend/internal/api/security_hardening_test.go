package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
