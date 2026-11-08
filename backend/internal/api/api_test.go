package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/config"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
)

func TestFullBackendWorkflowAnd14Drivers(t *testing.T) {
	metas := driver.ListMetas()
	if len(metas) < 14 {
		t.Fatalf("expected at least 14 registered database drivers, got %d", len(metas))
	}

	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	st, err := store.Open(tmpDir, vault, "admin", "admin123")
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	tm := auth.NewTokenManager(vault.DeriveBytes("test"))
	router := api.NewRouter(st, pm, tm)

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. Login
	loginResp := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login failed (%d): %s", loginResp.Code, loginResp.Body.String())
	}
	var loginData struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginResp.Body.Bytes(), &loginData)
	token := loginData.Token
	if token == "" {
		t.Fatal("empty JWT token")
	}

	// 2. Create SQLite connection
	testDBFile := filepath.Join(tmpDir, "e2e_test.sqlite")
	createConnResp := doJSON(http.MethodPost, "/api/connections", token, driver.ConnectionConfig{
		Name:     "E2E SQLite",
		Type:     "sqlite",
		FilePath: testDBFile,
	})
	if createConnResp.Code != http.StatusOK {
		t.Fatalf("create connection failed: %s", createConnResp.Body.String())
	}
	var savedConn store.SavedConnection
	_ = json.Unmarshal(createConnResp.Body.Bytes(), &savedConn)

	// 3. Create Table via visual spec (with index)
	createTblResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/table/create", token, driver.CreateTableRequest{
		Database: "main",
		Table:    "products",
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "INTEGER", IsPrimaryKey: true, IsAutoIncrement: true},
			{Name: "title", Type: "TEXT", Nullable: false},
			{Name: "price", Type: "REAL", Nullable: false, DefaultValue: "0"},
		},
		Indexes: []driver.IndexDef{
			{Name: "idx_products_title", Columns: []string{"title"}, Unique: true},
		},
	})
	if createTblResp.Code != http.StatusOK {
		t.Fatalf("create table failed: %s", createTblResp.Body.String())
	}

	// 4. Insert & Update Rows via mutation API
	mutResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/table/mutate", token, driver.RowMutationRequest{
		Database: "main",
		Table:    "products",
		Inserts: []map[string]any{
			{"title": "ClickHouse Cluster", "price": 999.5},
			{"title": "Redis Enterprise", "price": 499.0},
		},
	})
	if mutResp.Code != http.StatusOK {
		t.Fatalf("insert rows failed: %s", mutResp.Body.String())
	}

	// 5. Alter Table: ADD_COLUMN + MODIFY_COLUMN (SQLite transactional table rebuild preserving data & unique index!)
	alterResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/table/alter", token, driver.AlterTableRequest{
		Database: "main",
		Table:    "products",
		Actions: []driver.AlterAction{
			{
				Action: "ADD_COLUMN",
				Column: driver.ColumnDef{Name: "sku", Type: "TEXT", Nullable: false, DefaultValue: "'SKU-DEFAULT'"},
			},
			{
				Action:  "MODIFY_COLUMN",
				OldName: "price",
				Column:  driver.ColumnDef{Name: "unit_price", Type: "NUMERIC", Nullable: false, DefaultValue: "10"},
			},
		},
	})
	if alterResp.Code != http.StatusOK {
		t.Fatalf("SQLite table modify/rebuild failed: %s", alterResp.Body.String())
	}

	// Verify structure & data after SQLite table rebuild
	stResp := doJSON(http.MethodGet, "/api/conn/"+savedConn.ID+"/table/structure?database=main&table=products", token, nil)
	if stResp.Code != http.StatusOK || !strings.Contains(stResp.Body.String(), "unit_price") || !strings.Contains(stResp.Body.String(), "idx_products_title") {
		t.Fatalf("structure after rebuild unexpected: %s", stResp.Body.String())
	}

	// 6. Browse table with filter on renamed column
	browseResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/table/browse", token, driver.BrowseDataRequest{
		Database: "main",
		Table:    "products",
		Page:     1,
		PageSize: 50,
		Filter:   "unit_price > 600",
	})
	if browseResp.Code != http.StatusOK {
		t.Fatalf("browse failed: %s", browseResp.Body.String())
	}
	var browseData driver.QueryResult
	_ = json.Unmarshal(browseResp.Body.Bytes(), &browseData)
	if browseData.Total != 1 || len(browseData.Rows) != 1 {
		t.Fatalf("expected 1 filtered row, got total=%d rows=%d", browseData.Total, len(browseData.Rows))
	}

	// 7. Table Object Operations: COPY_DATA + CREATE_VIEW
	copyResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/object/action", token, driver.ObjectActionRequest{
		Database:   "main",
		ObjectName: "products",
		ObjectType: "TABLE",
		Action:     "COPY_DATA",
		TargetName: "products_archive",
	})
	if copyResp.Code != http.StatusOK {
		t.Fatalf("copy table failed: %s", copyResp.Body.String())
	}

	viewResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/object/action", token, driver.ObjectActionRequest{
		Database:   "main",
		ObjectName: "v_high_value",
		ObjectType: "VIEW",
		Action:     "CREATE_VIEW",
		ViewSQL:    "SELECT id, title, unit_price FROM products WHERE unit_price >= 500",
	})
	if viewResp.Code != http.StatusOK {
		t.Fatalf("create view failed: %s", viewResp.Body.String())
	}

	// 8. Execute Multi-Statement Custom SQL
	queryResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/query", token, map[string]any{
		"database": "main",
		"query":    "SELECT COUNT(*) AS cnt FROM products_archive; SELECT * FROM v_high_value;",
	})
	if queryResp.Code != http.StatusOK {
		t.Fatalf("multi-query failed: %s", queryResp.Body.String())
	}

	// 9. Full-Database SQL Dump Export via single-use export ticket
	ticketResp := doJSON(http.MethodPost, "/api/auth/ticket", token, nil)
	if ticketResp.Code != http.StatusOK {
		t.Fatalf("issue ticket failed: %s", ticketResp.Body.String())
	}
	var ticketPayload struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(ticketResp.Body.Bytes(), &ticketPayload)

	dumpReq := httptest.NewRequest(http.MethodGet, "/api/conn/"+savedConn.ID+"/export?database=main&format=sql&includeDdl=true&dropIfExists=true&ticket="+ticketPayload.Ticket, nil)
	dumpW := httptest.NewRecorder()
	router.ServeHTTP(dumpW, dumpReq)
	if dumpW.Code != http.StatusOK || !strings.Contains(dumpW.Body.String(), "CREATE TABLE") || !strings.Contains(dumpW.Body.String(), "products_archive") {
		t.Fatalf("full db sql dump failed (%d): %s", dumpW.Code, dumpW.Body.String())
	}

	// Replaying single-use ticket must be rejected
	replayReq := httptest.NewRequest(http.MethodGet, "/api/conn/"+savedConn.ID+"/export?database=main&format=sql&ticket="+ticketPayload.Ticket, nil)
	replayW := httptest.NewRecorder()
	router.ServeHTTP(replayW, replayReq)
	if replayW.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on replayed single-use export ticket, got %d", replayW.Code)
	}

	// 10. Batched CSV Import (250 rows in 2 multi-row INSERT batches)
	var csvData strings.Builder
	csvData.WriteString("title,unit_price,sku\n")
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&csvData, "Batch Item %d,%d.5,SKU-%04d\n", i, i*10, i)
	}
	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	_ = mw.WriteField("database", "main")
	_ = mw.WriteField("table", "products")
	_ = mw.WriteField("format", "csv")
	fw, _ := mw.CreateFormFile("file", "batch.csv")
	_, _ = fw.Write([]byte(csvData.String()))
	_ = mw.Close()

	impReq := httptest.NewRequest(http.MethodPost, "/api/conn/"+savedConn.ID+"/import", &mpBuf)
	impReq.Header.Set("Authorization", "Bearer "+token)
	impReq.Header.Set("Content-Type", mw.FormDataContentType())
	impW := httptest.NewRecorder()
	router.ServeHTTP(impW, impReq)
	if impW.Code != http.StatusOK {
		t.Fatalf("batched csv import failed: %s", impW.Body.String())
	}

	// 11. Direct SQL Script Import / Execution endpoint
	sqlImpResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/import/sql", token, map[string]any{
		"database": "main",
		"sql":      "CREATE TABLE imported_notes (id INTEGER PRIMARY KEY, body TEXT); INSERT INTO imported_notes (body) VALUES ('hello'), ('world');",
	})
	if sqlImpResp.Code != http.StatusOK {
		t.Fatalf("direct sql script import failed: %s", sqlImpResp.Body.String())
	}

	// 12. Monitor & System Runtime Memory Stats
	monResp := doJSON(http.MethodGet, "/api/conn/"+savedConn.ID+"/monitor", token, nil)
	if monResp.Code != http.StatusOK {
		t.Fatalf("monitor failed: %s", monResp.Body.String())
	}
	sysResp := doJSON(http.MethodGet, "/api/system/stats", token, nil)
	if sysResp.Code != http.StatusOK || !strings.Contains(sysResp.Body.String(), "allocMB") {
		t.Fatalf("system stats failed: %s", sysResp.Body.String())
	}

	// 13. Autocomplete short-TTL cache + DDL invalidation
	ac1 := doJSON(http.MethodGet, "/api/conn/"+savedConn.ID+"/autocomplete?database=main", token, nil)
	if ac1.Code != http.StatusOK || !strings.Contains(ac1.Body.String(), "imported_notes") {
		t.Fatalf("autocomplete 1 failed: %s", ac1.Body.String())
	}
	ac2 := doJSON(http.MethodGet, "/api/conn/"+savedConn.ID+"/autocomplete?database=main", token, nil)
	if ac2.Code != http.StatusOK || !strings.Contains(ac2.Body.String(), "imported_notes") {
		t.Fatalf("autocomplete cached failed: %s", ac2.Body.String())
	}
	// Execute DDL via /query and verify autocomplete cache is invalidated immediately
	ddlQueryResp := doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/query", token, map[string]any{
		"database": "main",
		"query":    "CREATE TABLE cache_invalidation_probe (id INTEGER PRIMARY KEY, tag TEXT);",
	})
	if ddlQueryResp.Code != http.StatusOK {
		t.Fatalf("ddl query failed: %s", ddlQueryResp.Body.String())
	}
	ac3 := doJSON(http.MethodGet, "/api/conn/"+savedConn.ID+"/autocomplete?database=main", token, nil)
	if ac3.Code != http.StatusOK || !strings.Contains(ac3.Body.String(), "cache_invalidation_probe") {
		t.Fatalf("autocomplete cache was not invalidated after DDL: %s", ac3.Body.String())
	}

	// 14. Oversized UTF-8 query history truncation safety & BrowseData pageSize upper bound
	oversizedSQL := "SELECT 1; /* " + strings.Repeat("UTF8-Perf-Check-🚀-", 600) + " */"
	_ = doJSON(http.MethodPost, "/api/conn/"+savedConn.ID+"/query", token, map[string]any{
		"database": "main",
		"query":    oversizedSQL,
		"maxRows":  999999,
	})
	histResp := doJSON(http.MethodGet, "/api/history?connectionId="+savedConn.ID+"&limit=5", token, nil)
	if histResp.Code != http.StatusOK {
		t.Fatalf("history list failed: %s", histResp.Body.String())
	}
	var histPayload struct {
		History []store.HistoryEntry `json:"history"`
	}
	_ = json.Unmarshal(histResp.Body.Bytes(), &histPayload)
	if len(histPayload.History) == 0 || len(histPayload.History[0].Query) > 8300 || !utf8.ValidString(histPayload.History[0].Query) {
		t.Fatalf("expected truncated valid UTF-8 history entry, got len=%d", len(histPayload.History[0].Query))
	}

	// 15. Explicit connection pool release (/disconnect) drops activePools to 0
	discResp := doJSON(http.MethodPost, "/api/connections/"+savedConn.ID+"/disconnect", token, nil)
	if discResp.Code != http.StatusOK {
		t.Fatalf("disconnect failed: %s", discResp.Body.String())
	}
	if pm.Stats().ActivePools != 0 {
		t.Fatalf("expected 0 active pools after disconnect, got %d", pm.Stats().ActivePools)
	}

	// 16. Embedded SPA static asset ETag & conditional HTTP 304 Not Modified
	idxReq1 := httptest.NewRequest(http.MethodGet, "/", nil)
	idxReq1.Header.Set("Accept-Encoding", "gzip")
	idxW1 := httptest.NewRecorder()
	router.ServeHTTP(idxW1, idxReq1)
	if idxW1.Code == http.StatusOK {
		etag := idxW1.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("expected ETag header on embedded index.html response")
		}
		idxReq2 := httptest.NewRequest(http.MethodGet, "/", nil)
		idxReq2.Header.Set("If-None-Match", etag)
		idxW2 := httptest.NewRecorder()
		router.ServeHTTP(idxW2, idxReq2)
		if idxW2.Code != http.StatusNotModified || idxW2.Body.Len() != 0 {
			t.Fatalf("expected 304 Not Modified with empty body on matching If-None-Match, got %d (len=%d)", idxW2.Code, idxW2.Body.Len())
		}
	}
}

func TestSecurity2FASessionsAndRateLimitE2E(t *testing.T) {
	tmpDir := t.TempDir()
	vault, _ := crypto.NewVaultFromDataDir(tmpDir, "")
	st, err := store.Open(tmpDir, vault, "admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pm := pool.NewManager(st, 5)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("jwt"))
	router := api.NewRouter(st, pm, tm)

	doReq := func(method, path, token, ip string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if ip != "" {
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("X-Forwarded-For", ip)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. Login & setup TOTP 2FA
	r1 := doReq(http.MethodPost, "/api/auth/login", "", "10.0.0.1", map[string]string{
		"username": "admin", "password": "admin123",
	})
	var l1 struct{ Token string }
	_ = json.Unmarshal(r1.Body.Bytes(), &l1)

	setupResp := doReq(http.MethodPost, "/api/auth/2fa/setup", l1.Token, "10.0.0.1", nil)
	if setupResp.Code != http.StatusOK {
		t.Fatalf("2fa setup failed: %s", setupResp.Body.String())
	}
	var setupData struct {
		Secret        string `json:"secret"`
		QRCodeDataURL string `json:"qrCodeDataUrl"`
	}
	_ = json.Unmarshal(setupResp.Body.Bytes(), &setupData)
	if setupData.Secret == "" || !strings.HasPrefix(setupData.QRCodeDataURL, "data:image/svg+xml;base64,") {
		t.Fatalf("invalid 2fa setup payload: %+v", setupData)
	}

	totpCode, _, err := auth.GenerateTOTPCodeAt(setupData.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	enableResp := doReq(http.MethodPost, "/api/auth/2fa/enable", l1.Token, "10.0.0.1", map[string]string{"code": totpCode})
	if enableResp.Code != http.StatusOK {
		t.Fatalf("2fa enable failed: %s", enableResp.Body.String())
	}
	var enableData struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	_ = json.Unmarshal(enableResp.Body.Bytes(), &enableData)
	if len(enableData.RecoveryCodes) != 8 {
		t.Fatalf("expected 8 recovery codes, got %d", len(enableData.RecoveryCodes))
	}

	// 2. Logout revokes current session token immediately
	logoutResp := doReq(http.MethodPost, "/api/auth/logout", l1.Token, "10.0.0.1", nil)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout failed: %s", logoutResp.Body.String())
	}
	meAfterLogout := doReq(http.MethodGet, "/api/auth/me", l1.Token, "10.0.0.1", nil)
	if meAfterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked token to return 401, got %d", meAfterLogout.Code)
	}

	// 3. Login with 2FA enabled returns challengeToken, then complete via backup recovery code
	step1 := doReq(http.MethodPost, "/api/auth/login", "", "10.0.0.2", map[string]string{
		"username": "admin", "password": "admin123",
	})
	var step1Data struct {
		Requires2FA    bool   `json:"requires2FA"`
		ChallengeToken string `json:"challengeToken"`
	}
	_ = json.Unmarshal(step1.Body.Bytes(), &step1Data)
	if !step1Data.Requires2FA || step1Data.ChallengeToken == "" {
		t.Fatalf("expected requires2FA challenge, got: %s", step1.Body.String())
	}

	// Challenge token MUST NOT be accepted as a session token on /api/connections
	illegalUse := doReq(http.MethodGet, "/api/connections", step1Data.ChallengeToken, "10.0.0.2", nil)
	if illegalUse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when using challengeToken as session token, got %d", illegalUse.Code)
	}
	if strings.Contains(step1.Body.String(), `"username"`) {
		t.Fatalf("2FA challenge response must not echo username: %s", step1.Body.String())
	}

	step2 := doReq(http.MethodPost, "/api/auth/login/2fa", "", "10.0.0.2", map[string]string{
		"challengeToken": step1Data.ChallengeToken,
		"code":           enableData.RecoveryCodes[0],
	})
	if step2.Code != http.StatusOK || !strings.Contains(step2.Body.String(), `"usedRecoveryCode":true`) {
		t.Fatalf("2fa recovery login failed (%d): %s", step2.Code, step2.Body.String())
	}
	var step2Data struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(step2.Body.Bytes(), &step2Data)

	// Unauthenticated /api/version must return 401; authenticated /api/version exposes Willh92/DBHarbor repo & embedded changelog
	unauthVerResp := doReq(http.MethodGet, "/api/version", "", "10.0.0.2", nil)
	if unauthVerResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated /api/version, got %d: %s", unauthVerResp.Code, unauthVerResp.Body.String())
	}
	verResp := doReq(http.MethodGet, "/api/version", step2Data.Token, "10.0.0.2", nil)
	if verResp.Code != http.StatusOK || !strings.Contains(verResp.Body.String(), "https://github.com/Willh92/DBHarbor.git") {
		t.Fatalf("unexpected authenticated /api/version response (%d): %s", verResp.Code, verResp.Body.String())
	}

	// 4. Brute-force protection from a separate attacker IP triggers captcha & 429 lockout without leaking remainingAttempts
	attackerIP := "192.168.99.99"
	for i := 1; i <= 5; i++ {
		bad := doReq(http.MethodPost, "/api/auth/login", "", attackerIP, map[string]string{
			"username": "admin", "password": "wrong-password",
		})
		if strings.Contains(bad.Body.String(), "remainingAttempts") {
			t.Fatalf("failed login response must not leak remainingAttempts: %s", bad.Body.String())
		}
		if i == 5 && bad.Code != http.StatusTooManyRequests {
			t.Fatalf("expected HTTP 429 lockout on 5th failed attempt, got %d: %s", bad.Code, bad.Body.String())
		}
	}
}

func TestWebInitialAdminSetupBrandingAndZeroLeakage(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	// Empty bootstrap credentials -> initial Web setup mode
	st, err := store.Open(tmpDir, vault, "", "")
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 5)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("jwt-setup"))
	router := api.NewRouter(st, pm, tm)

	doReq := func(method, path, token string, body any) *httptest.ResponseRecorder {
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

	// 1. /robots.txt & anti-indexing headers + zero product name leakage
	robotsW := doReq(http.MethodGet, "/robots.txt", "", nil)
	if robotsW.Code != http.StatusOK || robotsW.Body.String() != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("unexpected /robots.txt (%d): %q", robotsW.Code, robotsW.Body.String())
	}
	if strings.Contains(strings.ToLower(robotsW.Body.String()), "dbharbor") {
		t.Fatalf("/robots.txt must not leak product name: %q", robotsW.Body.String())
	}
	if !strings.Contains(robotsW.Header().Get("X-Robots-Tag"), "noindex") || robotsW.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected X-Robots-Tag and Referrer-Policy headers, got %+v", robotsW.Header())
	}

	// 2. /api/health must return minimal {"status":"ok"} without app/drivers/version fingerprinting
	healthW := doReq(http.MethodGet, "/api/health", "", nil)
	if healthW.Code != http.StatusOK {
		t.Fatalf("/api/health failed: %d", healthW.Code)
	}
	var healthMap map[string]any
	_ = json.Unmarshal(healthW.Body.Bytes(), &healthMap)
	if len(healthMap) != 1 || healthMap["status"] != "ok" {
		t.Fatalf("expected minimal health payload {\"status\":\"ok\"}, got %s", healthW.Body.String())
	}

	// 3. Unauthenticated /api/version must return 401
	if verW := doReq(http.MethodGet, "/api/version", "", nil); verW.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated /api/version, got %d", verW.Code)
	}

	// 4. /api/auth/status reports needsSetup=true before initialization
	status1W := doReq(http.MethodGet, "/api/auth/status", "", nil)
	if status1W.Code != http.StatusOK {
		t.Fatalf("/api/auth/status failed (%d): %s", status1W.Code, status1W.Body.String())
	}
	var status1 struct {
		AuthEnabled bool                      `json:"authEnabled"`
		NeedsSetup  bool                      `json:"needsSetup"`
		Branding    store.LoginBrandingConfig `json:"branding"`
	}
	_ = json.Unmarshal(status1W.Body.Bytes(), &status1)
	if !status1.AuthEnabled || !status1.NeedsSetup || status1.Branding.IconMode != "shield" {
		t.Fatalf("unexpected pre-setup auth status: %+v", status1)
	}

	// 5. /api/auth/login returns 403 while needsSetup=true
	preLoginW := doReq(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": "admin", "password": "admin123",
	})
	if preLoginW.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /api/auth/login prior to initial setup, got %d: %s", preLoginW.Code, preLoginW.Body.String())
	}

	// 6. Initialize primary admin + custom login branding via POST /api/auth/setup
	setupW := doReq(http.MethodPost, "/api/auth/setup", "", map[string]any{
		"username":    "chief_dba",
		"displayName": "Chief DBA",
		"password":    "UltraSecr3t!2026",
		"branding": map[string]any{
			"pageTitle": "Corp Console",
			"iconMode":  "database",
			"heading":   "Unified Data Gateway",
			"subtitle":  "Internal SSO / Authorized Access",
		},
	})
	if setupW.Code != http.StatusOK {
		t.Fatalf("POST /api/auth/setup failed (%d): %s", setupW.Code, setupW.Body.String())
	}
	var setupRes struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(setupW.Body.Bytes(), &setupRes)
	if setupRes.Token == "" {
		t.Fatal("expected JWT token from POST /api/auth/setup")
	}

	// 7. Re-calling POST /api/auth/setup must be permanently blocked with 403
	dupSetupW := doReq(http.MethodPost, "/api/auth/setup", "", map[string]any{
		"username": "hacker",
		"password": "HackerPass123!",
	})
	if dupSetupW.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on duplicate /api/auth/setup, got %d: %s", dupSetupW.Code, dupSetupW.Body.String())
	}

	// 8. /api/auth/status now reports needsSetup=false & custom branding
	status2W := doReq(http.MethodGet, "/api/auth/status", "", nil)
	var status2 struct {
		NeedsSetup bool                      `json:"needsSetup"`
		Branding   store.LoginBrandingConfig `json:"branding"`
	}
	_ = json.Unmarshal(status2W.Body.Bytes(), &status2)
	if status2.NeedsSetup || status2.Branding.PageTitle != "Corp Console" || status2.Branding.Heading != "Unified Data Gateway" {
		t.Fatalf("unexpected post-setup auth status: %+v", status2)
	}

	// 9. Admin can update & fetch login branding via PUT / GET /api/admin/login-branding
	updBrandW := doReq(http.MethodPut, "/api/admin/login-branding", setupRes.Token, map[string]any{
		"pageTitle": "Ops Sign In",
		"iconMode":  "none",
		"heading":   "Welcome Back",
		"subtitle":  "",
	})
	if updBrandW.Code != http.StatusOK {
		t.Fatalf("PUT /api/admin/login-branding failed (%d): %s", updBrandW.Code, updBrandW.Body.String())
	}
	getBrandW := doReq(http.MethodGet, "/api/admin/login-branding", setupRes.Token, nil)
	if getBrandW.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/login-branding failed (%d): %s", getBrandW.Code, getBrandW.Body.String())
	}
	var adminBrandRes struct {
		Branding store.LoginBrandingConfig `json:"branding"`
	}
	_ = json.Unmarshal(getBrandW.Body.Bytes(), &adminBrandRes)
	if adminBrandRes.Branding.PageTitle != "Ops Sign In" || adminBrandRes.Branding.Heading != "Welcome Back" || adminBrandRes.Branding.IconMode != "none" {
		t.Fatalf("unexpected GET /api/admin/login-branding payload: %+v", adminBrandRes)
	}
}

func TestV101ConfigPrecedenceAndAdminRecovery(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. First Load auto-creates <DataDir>/config.json with safe defaults
	cfg1, err := config.Load([]string{"-data-dir", tmpDir})
	if err != nil {
		t.Fatalf("config.Load 1: %v", err)
	}
	cfgPath := filepath.Join(tmpDir, "config.json")
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		t.Fatalf("expected auto-created config.json at %s: %v", cfgPath, statErr)
	}
	if cfg1.Port != 8787 || cfg1.Recovery.Enabled {
		t.Fatalf("unexpected default config: %+v", cfg1)
	}

	// 2. Write custom values + recovery block into config.json and verify 4-tier precedence (CLI > Env > config.json > Default)
	rawCfgJSON := `{
  "host": "127.0.0.1",
  "port": 9191,
  "maxImportMB": 128,
  "trustedProxies": ["10.10.0.0/16"],
  "recovery": {
    "enabled": true,
    "adminUsername": "admin",
    "newPassword": "auto",
    "reset2FA": true
  }
}`
	if err := os.WriteFile(cfgPath, []byte(rawCfgJSON), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	t.Setenv("DBHARBOR_PORT", "9292")
	cfgEnv, err := config.Load([]string{"-data-dir", tmpDir})
	if err != nil {
		t.Fatalf("config.Load env: %v", err)
	}
	if cfgEnv.Host != "127.0.0.1" || cfgEnv.Port != 9292 || cfgEnv.MaxImportMB != 128 || !cfgEnv.Recovery.Enabled {
		t.Fatalf("expected Env > config.json > Default precedence, got %+v", cfgEnv)
	}

	cfgCLI, err := config.Load([]string{"-data-dir", tmpDir, "-port", "9393", "-recover-admin", "root_dba", "-recover-pass", "Recovered#2026!", "-reset-2fa"})
	if err != nil {
		t.Fatalf("config.Load CLI: %v", err)
	}
	if cfgCLI.Port != 9393 || !cfgCLI.RecoverAdminCLI || cfgCLI.Recovery.AdminUsername != "root_dba" || !cfgCLI.Recovery.Reset2FA {
		t.Fatalf("expected CLI > Env precedence and shorthand -recover-admin <user>, got %+v", cfgCLI)
	}

	// 3. Store recovery: TOTP 2FA enabled + active session -> ExecuteAdminRecovery ("auto") resets password, clears 2FA, revokes sessions, writes backup, and DisarmRecoveryConfigFile clears config.json
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(tmpDir, vault, "admin", "OldPass123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	adminUser, err := st.Authenticate("admin", "OldPass123!")
	if err != nil {
		t.Fatalf("initial auth failed: %v", err)
	}
	secret, _, _, err := st.BeginTOTPSetup(adminUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := auth.GenerateTOTPCodeAt(secret, time.Now())
	if _, err := st.ConfirmTOTPSetup(adminUser.ID, code); err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(adminUser.ID, "127.0.0.1", "GoTest", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	recRes, err := st.ExecuteAdminRecovery(store.AdminRecoveryRequest{
		AdminUsername: cfgEnv.Recovery.AdminUsername,
		NewPassword:   cfgEnv.Recovery.NewPassword, // "auto"
		Reset2FA:      cfgEnv.Recovery.Reset2FA,
		Source:        "config.json",
	})
	if err != nil {
		t.Fatalf("ExecuteAdminRecovery failed: %v", err)
	}
	if !recRes.PasswordReset || len(recRes.IssuedBootstrapCode) < 16 || !recRes.Reset2FA || recRes.RevokedSessions < 1 || recRes.BackupPath == "" {
		t.Fatalf("unexpected recovery result: %+v", recRes)
	}
	if _, err := os.Stat(recRes.BackupPath); err != nil {
		t.Fatalf("expected pre-recovery backup file at %s: %v", recRes.BackupPath, err)
	}
	if st.IsSessionActive(sess.ID) {
		t.Fatal("expected pre-recovery session to be revoked")
	}
	recoveredUser, err := st.Authenticate("admin", recRes.IssuedBootstrapCode)
	if err != nil {
		t.Fatalf("authenticate with generated recovery password failed: %v", err)
	}
	if recoveredUser.TOTPEnabled {
		t.Fatal("expected TOTP 2FA to be disabled after recovery")
	}

	if err := config.DisarmRecoveryConfigFile(cfgPath); err != nil {
		t.Fatalf("DisarmRecoveryConfigFile failed: %v", err)
	}
	disarmedCfg, err := config.EnsureAndLoadFileConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if disarmedCfg.Recovery.Enabled || disarmedCfg.Recovery.NewPassword != "" || disarmedCfg.Recovery.Reset2FA {
		t.Fatalf("expected recovery block in config.json to be disarmed & scrubbed, got %+v", disarmedCfg.Recovery)
	}
}

func TestV101WebSecuritySandboxesVsDesktopZeroAccountIsolation(t *testing.T) {
	// --- Part A: Web Server Hardening & Multi-Tenant Sandboxes ---
	webDir := t.TempDir()
	vault, _ := crypto.NewVaultFromDataDir(webDir, "")
	_, _ = config.EnsureAndLoadFileConfig(filepath.Join(webDir, "config.json"))
	st, err := store.Open(webDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pm := pool.NewManager(st, 8)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("v101-web"))
	webRouter := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret: vault.DeriveBytes("protector"),
		MaxImportMB:     16,
	})

	doWeb := func(method, path, token, remoteAddr, xff string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		webRouter.ServeHTTP(w, req)
		return w
	}

	// 1. Untrusted peer cannot bypass rate limiter by rotating X-Forwarded-For
	for i := 1; i <= 5; i++ {
		spoofedXFF := fmt.Sprintf("198.51.100.%d", i)
		r := doWeb(http.MethodPost, "/api/auth/login", "", "203.0.113.77:54321", spoofedXFF, map[string]string{
			"username": "ghost_user",
			"password": "wrong-password-123",
		})
		if i == 5 && r.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 lockout on 5th attempt despite spoofed XFF from untrusted peer, got %d: %s", r.Code, r.Body.String())
		}
	}

	// 2. Single-use CAPTCHA nonce cannot be replayed
	lp := auth.NewLoginProtector([]byte("captcha-replay-test-secret"))
	capTok, _, err := lp.GenerateCaptcha()
	if err != nil || capTok == "" {
		t.Fatalf("GenerateCaptcha failed: %v", err)
	}
	if lp.VerifyCaptcha(capTok, "wrong") {
		t.Fatal("expected wrong captcha answer to fail")
	}
	if lp.VerifyCaptcha(capTok, "wrong") {
		t.Fatal("expected consumed captcha token to stay rejected")
	}

	// 3. Admin login + Web /api/drivers strips DuckDB cliPath + SQLite Web authorizer blocks ATTACH DATABASE
	adminLogin := doWeb(http.MethodPost, "/api/auth/login", "", "127.0.0.1:10001", "10.20.30.1", map[string]string{
		"username": "admin",
		"password": "AdminPass123!",
	})
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("admin login failed: %s", adminLogin.Body.String())
	}
	var adminTok struct{ Token string }
	_ = json.Unmarshal(adminLogin.Body.Bytes(), &adminTok)

	drvW := doWeb(http.MethodGet, "/api/drivers", adminTok.Token, "", "", nil)
	if strings.Contains(drvW.Body.String(), `"cliPath"`) {
		t.Fatalf("Web Server /api/drivers must not expose host cliPath field: %s", drvW.Body.String())
	}

	sharedDBPath := filepath.Join(webDir, "local_databases", "team.sqlite")
	_ = os.MkdirAll(filepath.Dir(sharedDBPath), 0o700)
	connW := doWeb(http.MethodPost, "/api/connections", adminTok.Token, "", "", map[string]any{
		"name":       "Team SQLite",
		"type":       "sqlite",
		"filePath":   sharedDBPath,
		"visibility": "shared",
	})
	if connW.Code != http.StatusOK {
		t.Fatalf("create shared sqlite failed: %s", connW.Body.String())
	}
	var sharedConn store.SavedConnection
	_ = json.Unmarshal(connW.Body.Bytes(), &sharedConn)

	// Seed a benign table
	_ = doWeb(http.MethodPost, "/api/conn/"+sharedConn.ID+"/query", adminTok.Token, "", "", map[string]any{
		"database": "main",
		"query":    "CREATE TABLE items (id INTEGER PRIMARY KEY, price REAL); INSERT INTO items (price) VALUES (25.0);",
	})

	// Web mode SQLite must block ATTACH DATABASE
	attachTarget := filepath.Join(webDir, "local_databases", "other.sqlite")
	attachW := doWeb(http.MethodPost, "/api/conn/"+sharedConn.ID+"/query", adminTok.Token, "", "", map[string]any{
		"database": "main",
		"query":    fmt.Sprintf("ATTACH DATABASE '%s' AS ext_db;", filepath.ToSlash(attachTarget)),
	})
	if attachW.Code == http.StatusOK {
		t.Fatalf("expected Web Server SQLite authorizer to block ATTACH DATABASE, got 200: %s", attachW.Body.String())
	}

	// ?token= query parameter must be rejected on /export (only single-use ?ticket= allowed)
	rawTokenExport := doWeb(http.MethodGet, "/api/conn/"+sharedConn.ID+"/export?database=main&format=sql&token="+adminTok.Token, "", "", "", nil)
	if rawTokenExport.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when passing session JWT via ?token= query string, got %d", rawTokenExport.Code)
	}

	// Protecting config.json from connection open
	cfgProtectW := doWeb(http.MethodPost, "/api/connections", adminTok.Token, "", "", map[string]any{
		"name":     "Internal Config",
		"type":     "sqlite",
		"filePath": filepath.Join(webDir, "config.json"),
	})
	if cfgProtectW.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when attempting to open config.json as database, got %d: %s", cfgProtectW.Code, cfgProtectW.Body.String())
	}

	// 4. Non-admin Web user sandboxes: local_databases jail + private IP SSRF block + readonly BrowseData WHERE filter guard
	createUserW := doWeb(http.MethodPost, "/api/admin/users", adminTok.Token, "", "", map[string]any{
		"username":      "dev_bob",
		"displayName":   "Bob",
		"password":      "BobPass123!",
		"role":          "readonly",
		"canCreateConn": true,
		"connScope":     "all",
	})
	if createUserW.Code != http.StatusOK {
		t.Fatalf("create readonly user failed: %s", createUserW.Body.String())
	}
	bobLogin := doWeb(http.MethodPost, "/api/auth/login", "", "127.0.0.1:10002", "10.20.30.2", map[string]string{
		"username": "dev_bob",
		"password": "BobPass123!",
	})
	var bobTok struct{ Token string }
	_ = json.Unmarshal(bobLogin.Body.Bytes(), &bobTok)

	// Non-admin blocked from opening file outside local_databases
	escapeFile := filepath.Join(t.TempDir(), "outside.sqlite")
	escapeW := doWeb(http.MethodPost, "/api/connections", bobTok.Token, "", "", map[string]any{
		"name":     "Outside Jail",
		"type":     "sqlite",
		"filePath": escapeFile,
	})
	if escapeW.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when non-admin opens SQLite path outside local_databases, got %d: %s", escapeW.Code, escapeW.Body.String())
	}

	// Non-admin blocked from connecting to loopback / RFC1918 private network
	ssrfW := doWeb(http.MethodPost, "/api/connections", bobTok.Token, "", "", map[string]any{
		"name": "Internal PG",
		"type": "postgres",
		"host": "127.0.0.1",
		"port": 5432,
	})
	if ssrfW.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when non-admin targets loopback/private address, got %d: %s", ssrfW.Code, ssrfW.Body.String())
	}

	// Readonly user BrowseData benign filter succeeds, statement-Stacked/comment-smuggled filter is rejected
	okBrowse := doWeb(http.MethodPost, "/api/conn/"+sharedConn.ID+"/table/browse", bobTok.Token, "", "", driver.BrowseDataRequest{
		Database: "main",
		Table:    "items",
		Filter:   "price > 10",
	})
	if okBrowse.Code != http.StatusOK {
		t.Fatalf("expected benign readonly BrowseData filter to succeed, got %d: %s", okBrowse.Code, okBrowse.Body.String())
	}
	badBrowse := doWeb(http.MethodPost, "/api/conn/"+sharedConn.ID+"/table/browse", bobTok.Token, "", "", driver.BrowseDataRequest{
		Database: "main",
		Table:    "items",
		Filter:   "1=1; SELECT 2",
	})
	if badBrowse.Code != http.StatusForbidden && badBrowse.Code != http.StatusBadRequest {
		t.Fatalf("expected non-simple BrowseData filter to be rejected, got %d: %s", badBrowse.Code, badBrowse.Body.String())
	}

	// --- Part B: Native Desktop Edition Zero-Account & Unrestricted Local Mode Verification ---
	deskDir := t.TempDir()
	deskVault, _ := crypto.NewVaultFromDataDir(deskDir, "")
	deskStore, err := store.OpenLocal(deskDir, deskVault)
	if err != nil {
		t.Fatalf("store.OpenLocal failed: %v", err)
	}
	defer deskStore.Close()
	deskPool := pool.NewManager(deskStore, 5)
	defer deskPool.CloseAll()

	bootTicket := "desk-boot-ticket-123"
	cookieSecret := "desk-cookie-secret-456"
	deskRouter := api.NewDesktopRouter(deskStore, deskPool, api.DesktopConfig{
		Enabled:             true,
		ListenPort:          18787,
		DataDir:             deskDir,
		BootTicket:          bootTicket,
		SessionCookieSecret: cookieSecret,
	})

	// Boot handshake issues HttpOnly SameSite=Strict session cookie; zero login/user accounts required
	bootReq := httptest.NewRequest(http.MethodGet, "/__desktop_boot?ticket="+bootTicket, nil)
	bootW := httptest.NewRecorder()
	deskRouter.ServeHTTP(bootW, bootReq)
	if bootW.Code != http.StatusFound {
		t.Fatalf("expected 302 from /__desktop_boot, got %d", bootW.Code)
	}
	cookies := bootW.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected desktop session cookies from /__desktop_boot")
	}

	doDesk := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
		w := httptest.NewRecorder()
		deskRouter.ServeHTTP(w, req)
		return w
	}

	// Desktop /api/auth/me reports authEnabled=false, role=admin, local workspace
	deskMeW := doDesk(http.MethodGet, "/api/auth/me", nil)
	if deskMeW.Code != http.StatusOK || !strings.Contains(deskMeW.Body.String(), `"authEnabled":false`) {
		t.Fatalf("expected Desktop /api/auth/me with authEnabled=false, got %d: %s", deskMeW.Code, deskMeW.Body.String())
	}

	// Desktop /api/version reports edition="desktop" and authEnabled=false
	deskVerW := doDesk(http.MethodGet, "/api/version", nil)
	if deskVerW.Code != http.StatusOK || !strings.Contains(deskVerW.Body.String(), `"edition":"desktop"`) || !strings.Contains(deskVerW.Body.String(), `"authEnabled":false`) {
		t.Fatalf("unexpected Desktop /api/version: %s", deskVerW.Body.String())
	}

	// Desktop /api/drivers preserves cliPath for local DuckDB CLI workflows
	deskDrvW := doDesk(http.MethodGet, "/api/drivers", nil)
	if !strings.Contains(deskDrvW.Body.String(), `"cliPath"`) {
		t.Fatalf("expected Desktop /api/drivers to preserve DuckDB cliPath field, got: %s", deskDrvW.Body.String())
	}

	// Desktop allows arbitrary local SQLite paths outside DataDir AND allows ATTACH DATABASE
	extDeskDir := t.TempDir()
	deskDB1 := filepath.Join(extDeskDir, "desk_primary.sqlite")
	deskDB2 := filepath.Join(extDeskDir, "desk_attached.sqlite")
	deskConnW := doDesk(http.MethodPost, "/api/connections", map[string]any{
		"name":     "Local Desktop SQLite",
		"type":     "sqlite",
		"filePath": deskDB1,
	})
	if deskConnW.Code != http.StatusOK {
		t.Fatalf("expected Desktop to allow arbitrary local SQLite path, got %d: %s", deskConnW.Code, deskConnW.Body.String())
	}
	var deskConn store.SavedConnection
	_ = json.Unmarshal(deskConnW.Body.Bytes(), &deskConn)

	deskAttachW := doDesk(http.MethodPost, "/api/conn/"+deskConn.ID+"/query", map[string]any{
		"database": "main",
		"query":    fmt.Sprintf("ATTACH DATABASE '%s' AS second_local_db; CREATE TABLE second_local_db.t1 (id INT);", filepath.ToSlash(deskDB2)),
	})
	if deskAttachW.Code != http.StatusOK {
		t.Fatalf("expected Desktop SQLite to allow local ATTACH DATABASE without Web sandbox interference, got %d: %s", deskAttachW.Code, deskAttachW.Body.String())
	}
}

func startMockAuthRedisServer(t *testing.T, initialPass string) (host string, port int, setExpectedPassword func(string)) {
	t.Helper()
	var expectedPass atomic.Value
	expectedPass.Store(initialPass)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock redis: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				authedPass := ""
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if !strings.HasPrefix(line, "*") {
						continue
					}
					argc, err := strconv.Atoi(line[1:])
					if err != nil || argc <= 0 {
						continue
					}
					args := make([]string, 0, argc)
					for i := 0; i < argc; i++ {
						szLine, err := r.ReadString('\n')
						if err != nil {
							return
						}
						sz, err := strconv.Atoi(strings.TrimPrefix(strings.TrimRight(szLine, "\r\n"), "$"))
						if err != nil || sz < 0 {
							return
						}
						buf := make([]byte, sz+2)
						if _, err := io.ReadFull(r, buf); err != nil {
							return
						}
						args = append(args, string(buf[:sz]))
					}

					cmd := strings.ToUpper(args[0])
					curExpected, _ := expectedPass.Load().(string)

					switch cmd {
					case "HELLO":
						// Force RESP2 fallback so go-redis sends standard AUTH
						_, _ = io.WriteString(c, "-ERR unknown command 'HELLO'\r\n")
					case "AUTH":
						given := ""
						if len(args) >= 3 {
							given = args[2]
						} else if len(args) == 2 {
							given = args[1]
						}
						if curExpected != "" && given != curExpected {
							authedPass = ""
							_, _ = io.WriteString(c, "-WRONGPASS invalid username-password pair or user is disabled.\r\n")
						} else {
							authedPass = given
							_, _ = io.WriteString(c, "+OK\r\n")
						}
					case "QUIT":
						_, _ = io.WriteString(c, "+OK\r\n")
						return
					default:
						if curExpected != "" && authedPass != curExpected {
							_, _ = io.WriteString(c, "-NOAUTH Authentication required.\r\n")
							continue
						}
						switch cmd {
						case "PING":
							_, _ = io.WriteString(c, "+PONG\r\n")
						case "CLIENT", "SELECT":
							_, _ = io.WriteString(c, "+OK\r\n")
						case "INFO":
							payload := "# Server\r\nredis_version:7.2.4\r\n# Keyspace\r\ndb0:keys=2,expires=0\r\n"
							_, _ = fmt.Fprintf(c, "$%d\r\n%s\r\n", len(payload), payload)
						case "SCAN":
							_, _ = io.WriteString(c, "*2\r\n$1\r\n0\r\n*2\r\n$7\r\nuser:01\r\n$7\r\nuser:02\r\n")
						default:
							_, _ = io.WriteString(c, "+OK\r\n")
						}
					}
				}
			}(conn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, func(next string) {
		expectedPass.Store(next)
	}
}

func TestConnectionPasswordPolicySessionIsolationAndAuthFailureRePrompt(t *testing.T) {
	redisHost, redisPort, setRedisPass := startMockAuthRedisServer(t, "InitPass#1")

	webDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(webDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(webDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 8)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("phase8-web"))
	router := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret: vault.DeriveBytes("phase8-protector"),
	})

	doReq := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.RemoteAddr = "127.0.0.1:23456"
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

	loginNewSession := func() string {
		w := doReq(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": "admin",
			"password": "AdminPass123!",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login failed (%d): %s", w.Code, w.Body.String())
		}
		var res struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Token
	}

	// 1. Session A creates a Redis connection WITHOUT specifying rememberPassword -> must default to rememberPassword=false,
	//    store zero password on disk, yet remain unlocked=true in Session A!
	tokA := loginNewSession()
	createW := doReq(http.MethodPost, "/api/connections", tokA, map[string]any{
		"name":     "Session Redis",
		"type":     "redis",
		"host":     redisHost,
		"port":     redisPort,
		"password": "InitPass#1",
	})
	if createW.Code != http.StatusOK {
		t.Fatalf("POST /api/connections failed (%d): %s", createW.Code, createW.Body.String())
	}
	var created store.SavedConnection
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	if created.RememberPassword {
		t.Fatalf("expected new Redis connection to default to rememberPassword=false, got true")
	}
	if !created.Unlocked {
		t.Fatalf("expected creator session A to see unlocked=true immediately after creation")
	}

	// Disk check: decrypted password on disk must be empty
	diskCfg, err := st.GetDecryptedConnection(created.ID)
	if err != nil {
		t.Fatalf("GetDecryptedConnection: %v", err)
	}
	if diskCfg.Password != "" {
		t.Fatalf("expected zero password persisted on disk when rememberPassword=false, got %q", diskCfg.Password)
	}

	// Session A can immediately query databases via its in-memory session credential
	dbsAW := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokA, nil)
	if dbsAW.Code != http.StatusOK {
		t.Fatalf("expected Session A listDatabases to succeed using session credential, got %d: %s", dbsAW.Code, dbsAW.Body.String())
	}

	// 2. Session B logs in separately -> sees unlocked=false and receives 400 + code="DB_AUTH_REQUIRED"
	tokB := loginNewSession()
	listBW := doReq(http.MethodGet, "/api/connections", tokB, nil)
	var listBRes struct {
		Connections []store.SavedConnection `json:"connections"`
	}
	_ = json.Unmarshal(listBW.Body.Bytes(), &listBRes)
	var foundRedisInB *store.SavedConnection
	for i := range listBRes.Connections {
		if listBRes.Connections[i].ID == created.ID {
			foundRedisInB = &listBRes.Connections[i]
			break
		}
	}
	if foundRedisInB == nil || foundRedisInB.RememberPassword || foundRedisInB.Unlocked {
		t.Fatalf("expected Session B to see rememberPassword=false & unlocked=false for Session Redis, got %+v", listBRes.Connections)
	}

	dbsBW := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokB, nil)
	if dbsBW.Code != http.StatusBadRequest || !strings.Contains(dbsBW.Body.String(), `"code":"DB_AUTH_REQUIRED"`) {
		t.Fatalf("expected 400 DB_AUTH_REQUIRED in Session B, got %d: %s", dbsBW.Code, dbsBW.Body.String())
	}

	// Unlocking Session B with wrong password returns 400 + code="DB_AUTH_FAILED"
	badUnlockW := doReq(http.MethodPost, "/api/connections/"+created.ID+"/unlock", tokB, map[string]any{
		"password":         "WrongPass#0",
		"rememberPassword": false,
	})
	if badUnlockW.Code != http.StatusBadRequest || !strings.Contains(badUnlockW.Body.String(), `"code":"DB_AUTH_FAILED"`) {
		t.Fatalf("expected 400 DB_AUTH_FAILED on wrong unlock password, got %d: %s", badUnlockW.Code, badUnlockW.Body.String())
	}

	// Unlocking Session B with valid password succeeds for Session B only (disk remains empty)
	okUnlockW := doReq(http.MethodPost, "/api/connections/"+created.ID+"/unlock", tokB, map[string]any{
		"password":         "InitPass#1",
		"rememberPassword": false,
	})
	if okUnlockW.Code != http.StatusOK {
		t.Fatalf("expected Session B unlock to succeed, got %d: %s", okUnlockW.Code, okUnlockW.Body.String())
	}
	if dbsB2W := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokB, nil); dbsB2W.Code != http.StatusOK {
		t.Fatalf("expected Session B listDatabases after unlock to succeed, got %d: %s", dbsB2W.Code, dbsB2W.Body.String())
	}

	// Logging out Session B evicts its session credentials
	if logoutBW := doReq(http.MethodPost, "/api/auth/logout", tokB, nil); logoutBW.Code != http.StatusOK {
		t.Fatalf("Session B logout failed: %d", logoutBW.Code)
	}

	// 3. Session A edits connection to turn ON rememberPassword=true (leaving password blank promotes Session A's vault secret!)
	promoteW := doReq(http.MethodPut, "/api/connections/"+created.ID, tokA, map[string]any{
		"name":               "Session Redis",
		"type":               "redis",
		"host":               redisHost,
		"port":               redisPort,
		"password":           "",
		"keepExistingSecret": true,
		"rememberPassword":   true,
	})
	if promoteW.Code != http.StatusOK {
		t.Fatalf("PUT promote rememberPassword=true failed (%d): %s", promoteW.Code, promoteW.Body.String())
	}
	diskCfgPromoted, _ := st.GetDecryptedConnection(created.ID)
	if diskCfgPromoted.Password != "InitPass#1" || !diskCfgPromoted.ShouldRememberPassword() {
		t.Fatalf("expected session secret to be promoted to disk on rememberPassword=true, got %+v", diskCfgPromoted)
	}

	// 4. Fresh Session C auto-connects without prompting, then Redis password rotates -> must return DB_AUTH_FAILED!
	tokC := loginNewSession()
	if dbsCW := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokC, nil); dbsCW.Code != http.StatusOK {
		t.Fatalf("expected Session C auto-connect with remembered password to succeed, got %d: %s", dbsCW.Code, dbsCW.Body.String())
	}

	setRedisPass("RotatedPass#2")
	rotatedErrW := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokC, nil)
	if rotatedErrW.Code != http.StatusBadRequest || !strings.Contains(rotatedErrW.Body.String(), `"code":"DB_AUTH_FAILED"`) {
		t.Fatalf("expected 400 DB_AUTH_FAILED after server password rotation on remembered connection, got %d: %s", rotatedErrW.Code, rotatedErrW.Body.String())
	}

	// Session C re-authenticates via /unlock with new password + rememberPassword=true -> updates disk & reconnects!
	reauthW := doReq(http.MethodPost, "/api/connections/"+created.ID+"/unlock", tokC, map[string]any{
		"password":         "RotatedPass#2",
		"rememberPassword": true,
	})
	if reauthW.Code != http.StatusOK {
		t.Fatalf("expected re-auth unlock with RotatedPass#2 to succeed, got %d: %s", reauthW.Code, reauthW.Body.String())
	}
	if dbsC2W := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokC, nil); dbsC2W.Code != http.StatusOK {
		t.Fatalf("expected listDatabases after re-auth unlock to succeed, got %d: %s", dbsC2W.Code, dbsC2W.Body.String())
	}

	// 5. Turn OFF rememberPassword via Edit Connection (without re-typing password) -> wipes disk password immediately,
	//    keeps Session C unlocked in memory, and prompts fresh Session D with DB_AUTH_REQUIRED!
	turnOffW := doReq(http.MethodPut, "/api/connections/"+created.ID, tokC, map[string]any{
		"name":               "Session Redis",
		"type":               "redis",
		"host":               redisHost,
		"port":               redisPort,
		"password":           "",
		"keepExistingSecret": true,
		"rememberPassword":   false,
	})
	if turnOffW.Code != http.StatusOK {
		t.Fatalf("PUT turn off rememberPassword failed (%d): %s", turnOffW.Code, turnOffW.Body.String())
	}
	var turnedOffConn store.SavedConnection
	_ = json.Unmarshal(turnOffW.Body.Bytes(), &turnedOffConn)
	if turnedOffConn.RememberPassword || !turnedOffConn.Unlocked {
		t.Fatalf("expected rememberPassword=false & unlocked=true in editor Session C, got %+v", turnedOffConn)
	}
	diskCfgWiped, _ := st.GetDecryptedConnection(created.ID)
	if diskCfgWiped.Password != "" || diskCfgWiped.ShouldRememberPassword() {
		t.Fatalf("expected disk password to be wiped after turning off rememberPassword, got %+v", diskCfgWiped)
	}
	if dbsC3W := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokC, nil); dbsC3W.Code != http.StatusOK {
		t.Fatalf("expected editor Session C to remain connected during current session, got %d: %s", dbsC3W.Code, dbsC3W.Body.String())
	}

	tokD := loginNewSession()
	dbsDW := doReq(http.MethodGet, "/api/conn/"+created.ID+"/databases", tokD, nil)
	if dbsDW.Code != http.StatusBadRequest || !strings.Contains(dbsDW.Body.String(), `"code":"DB_AUTH_REQUIRED"`) {
		t.Fatalf("expected fresh Session D to receive DB_AUTH_REQUIRED after rememberPassword was turned off, got %d: %s", dbsDW.Code, dbsDW.Body.String())
	}
}

func TestSecurityRegressionsH1H2M7AndDelayedExportHeaders(t *testing.T) {
	redisHost, redisPort, _ := startMockAuthRedisServer(t, "SharedInternal#1")

	// Start a mock ClickHouse HTTP endpoint that rejects queries with 401 / Code 516 auth failure
	chAuthFailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "Code: 516. DB::Exception: default: Authentication failed: password is incorrect.")
	}))
	defer chAuthFailSrv.Close()
	chTCPAddr := chAuthFailSrv.Listener.Addr().(*net.TCPAddr)

	webDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(webDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(webDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 8)
	defer pm.CloseAll()
	tm := auth.NewTokenManager(vault.DeriveBytes("sec-reg-jwt"))
	router := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret:            vault.DeriveBytes("sec-reg-protector"),
		AllowPrivateNetForNonAdmin: false,
	})
	defer router.ClearAllSessionCreds()

	doReq := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.RemoteAddr = "127.0.0.1:24567"
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

	loginAs := func(username, password string) string {
		w := doReq(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": username,
			"password": password,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("login(%s) failed (%d): %s", username, w.Code, w.Body.String())
		}
		var res struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Token
	}

	adminTok := loginAs("admin", "AdminPass123!")

	// 1. [H1] PUT /api/connections/{id} must reject KeepExistingSecret=true when URI host changes or TLS is downgraded
	createMongoW := doReq(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":             "Prod Mongo TLS",
		"type":             "mongodb",
		"uri":              "mongodb://alice:UltraSecret99@db1.example.com:27017/prod?tls=true",
		"rememberPassword": true,
	})
	if createMongoW.Code != http.StatusOK {
		t.Fatalf("create Prod Mongo TLS failed (%d): %s", createMongoW.Code, createMongoW.Body.String())
	}
	var mongoTLSConn store.SavedConnection
	_ = json.Unmarshal(createMongoW.Body.Bytes(), &mongoTLSConn)

	// [H1a] Attempt exfiltration by changing URI host with KeepExistingSecret=true and blank password
	exfilHostW := doReq(http.MethodPut, "/api/connections/"+mongoTLSConn.ID, adminTok, map[string]any{
		"name":               "Prod Mongo TLS",
		"type":               "mongodb",
		"uri":                "mongodb://alice@attacker.example.com:27017/prod?tls=true",
		"password":           "",
		"keepExistingSecret": true,
		"rememberPassword":   true,
	})
	if exfilHostW.Code != http.StatusOK {
		t.Fatalf("PUT host change failed (%d): %s", exfilHostW.Code, exfilHostW.Body.String())
	}
	decAfterHostChange, err := st.GetDecryptedConnection(mongoTLSConn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if decAfterHostChange.Password != "" || strings.Contains(decAfterHostChange.URI, "UltraSecret99") {
		t.Fatalf("[H1] secret leaked into changed URI host: pass=%q uri=%q", decAfterHostChange.Password, decAfterHostChange.URI)
	}

	// [H1b] Attempt TLS downgrade (sslmode=require -> sslmode=disable, and URI ?tls=true -> plaintext) with KeepExistingSecret=true
	createPGTLSW := doReq(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":             "Prod PG TLS",
		"type":             "postgres",
		"host":             "pg.example.com",
		"port":             5432,
		"username":         "pgadmin",
		"password":         "PGTopSecret#1",
		"options":          map[string]string{"sslmode": "require"},
		"rememberPassword": true,
	})
	if createPGTLSW.Code != http.StatusOK {
		t.Fatalf("create Prod PG TLS failed (%d): %s", createPGTLSW.Code, createPGTLSW.Body.String())
	}
	var pgTLSConn store.SavedConnection
	_ = json.Unmarshal(createPGTLSW.Body.Bytes(), &pgTLSConn)

	downgradePGW := doReq(http.MethodPut, "/api/connections/"+pgTLSConn.ID, adminTok, map[string]any{
		"name":               "Prod PG TLS",
		"type":               "postgres",
		"host":               "pg.example.com",
		"port":               5432,
		"username":           "pgadmin",
		"password":           "",
		"options":            map[string]string{"sslmode": "disable"},
		"keepExistingSecret": true,
		"rememberPassword":   true,
	})
	if downgradePGW.Code != http.StatusOK {
		t.Fatalf("PUT PG TLS downgrade failed (%d): %s", downgradePGW.Code, downgradePGW.Body.String())
	}
	decDowngradedPG, err := st.GetDecryptedConnection(pgTLSConn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if decDowngradedPG.Password != "" {
		t.Fatalf("[H1] expected TLS downgrade to clear stored password when KeepExistingSecret=true, got %q", decDowngradedPG.Password)
	}

	// 2. [H2] Non-admin (readonly & readwrite) users unlocking an admin-owned shared loopback/internal connection
	sharedRedisW := doReq(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":             "Admin Shared Loopback Redis",
		"type":             "redis",
		"host":             redisHost,
		"port":             redisPort,
		"visibility":       "shared",
		"rememberPassword": false,
	})
	if sharedRedisW.Code != http.StatusOK {
		t.Fatalf("create shared loopback Redis failed (%d): %s", sharedRedisW.Code, sharedRedisW.Body.String())
	}
	var sharedRedis store.SavedConnection
	_ = json.Unmarshal(sharedRedisW.Body.Bytes(), &sharedRedis)

	for _, u := range []struct {
		username string
		role     string
	}{
		{"ro_operator", store.RoleReadOnly},
		{"rw_operator", store.RoleReadWrite},
	} {
		cw := doReq(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
			"username":      u.username,
			"password":      "UserPass123!",
			"role":          u.role,
			"connScope":     "all",
			"canCreateConn": false,
		})
		if cw.Code != http.StatusOK {
			t.Fatalf("create user %s failed (%d): %s", u.username, cw.Code, cw.Body.String())
		}
		uTok := loginAs(u.username, "UserPass123!")
		unlockW := doReq(http.MethodPost, "/api/connections/"+sharedRedis.ID+"/unlock", uTok, map[string]any{
			"password":         "SharedInternal#1",
			"rememberPassword": false,
		})
		if unlockW.Code != http.StatusOK {
			t.Fatalf("[H2] expected %s (%s) to unlock admin-shared loopback connection, got %d: %s", u.username, u.role, unlockW.Code, unlockW.Body.String())
		}
		dbsW := doReq(http.MethodGet, "/api/conn/"+sharedRedis.ID+"/databases", uTok, nil)
		if dbsW.Code != http.StatusOK {
			t.Fatalf("[H2] expected %s listDatabases after unlock to succeed, got %d: %s", u.username, dbsW.Code, dbsW.Body.String())
		}
	}

	// 3. [M7] MongoDB URI connection with RememberPassword=false: metadata edit (KeepExistingSecret=true, stripped URI)
	//    must preserve the unlocked session password instead of wiping it!
	createSessMongoW := doReq(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":             "Session Mongo URI",
		"type":             "mongodb",
		"uri":              "mongodb://mongo_user:SessMongoPass#77@mongo.example.com:27017/appdb?authSource=admin",
		"rememberPassword": false,
	})
	if createSessMongoW.Code != http.StatusOK {
		t.Fatalf("create Session Mongo URI failed (%d): %s", createSessMongoW.Code, createSessMongoW.Body.String())
	}
	var sessMongo store.SavedConnection
	_ = json.Unmarshal(createSessMongoW.Body.Bytes(), &sessMongo)
	if !sessMongo.Unlocked || sessMongo.RememberPassword {
		t.Fatalf("expected Session Mongo URI to start unlocked=true & rememberPassword=false, got %+v", sessMongo)
	}

	metaEditW := doReq(http.MethodPut, "/api/connections/"+sessMongo.ID, adminTok, map[string]any{
		"name":               "Session Mongo URI Renamed",
		"type":               "mongodb",
		"color":              "#10b981",
		"username":           "mongo_user",
		"password":           "",
		"uri":                "mongodb://mongo_user@mongo.example.com:27017/appdb?authSource=admin",
		"keepExistingSecret": true,
		"rememberPassword":   false,
	})
	if metaEditW.Code != http.StatusOK {
		t.Fatalf("metadata edit Session Mongo URI failed (%d): %s", metaEditW.Code, metaEditW.Body.String())
	}
	var editedSessMongo store.SavedConnection
	_ = json.Unmarshal(metaEditW.Body.Bytes(), &editedSessMongo)
	if !editedSessMongo.Unlocked {
		t.Fatalf("[M7] expected Session Mongo URI to stay unlocked=true after metadata edit with stripped URI")
	}

	// Promote to RememberPassword=true without retyping password (still passing stripped URI) to verify the exact secret survived
	promoteMongoW := doReq(http.MethodPut, "/api/connections/"+sessMongo.ID, adminTok, map[string]any{
		"name":               "Session Mongo URI Renamed",
		"type":               "mongodb",
		"username":           "mongo_user",
		"password":           "",
		"uri":                "mongodb://mongo_user@mongo.example.com:27017/appdb?authSource=admin",
		"keepExistingSecret": true,
		"rememberPassword":   true,
	})
	if promoteMongoW.Code != http.StatusOK {
		t.Fatalf("promote Session Mongo URI failed (%d): %s", promoteMongoW.Code, promoteMongoW.Body.String())
	}
	decPromotedMongo, err := st.GetDecryptedConnection(sessMongo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if decPromotedMongo.Password != "SessMongoPass#77" || store.ExtractPasswordFromURI(decPromotedMongo.URI) != "SessMongoPass#77" {
		t.Fatalf("[M7] expected session password to survive metadata edit and promote intact, got pass=%q uri=%q", decPromotedMongo.Password, decPromotedMongo.URI)
	}

	// 4. Streaming export delayed headers: when StreamExport fails with an auth error before writing bytes,
	//    handleExport must return 400 JSON with DB_AUTH_FAILED and NO Content-Disposition attachment header!
	createCHW := doReq(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":             "AuthFail ClickHouse HTTP",
		"type":             "clickhouse",
		"host":             "127.0.0.1",
		"port":             chTCPAddr.Port,
		"username":         "default",
		"password":         "BadPass",
		"database":         "default",
		"options":          map[string]string{"protocol": "http"},
		"rememberPassword": true,
	})
	if createCHW.Code != http.StatusOK {
		t.Fatalf("create ClickHouse HTTP connection failed (%d): %s", createCHW.Code, createCHW.Body.String())
	}
	var chConn store.SavedConnection
	_ = json.Unmarshal(createCHW.Body.Bytes(), &chConn)

	exportW := doReq(http.MethodGet, "/api/conn/"+chConn.ID+"/export?database=default&table=events&format=csv", adminTok, nil)
	if exportW.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on StreamExport auth error before bytes written, got %d: %s", exportW.Code, exportW.Body.String())
	}
	if disp := exportW.Header().Get("Content-Disposition"); disp != "" {
		t.Fatalf("expected no Content-Disposition header when export fails before first write, got %q", disp)
	}
	if !strings.Contains(exportW.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected application/json error response, got Content-Type=%q", exportW.Header().Get("Content-Type"))
	}
	if !strings.Contains(exportW.Body.String(), `"code":"DB_AUTH_FAILED"`) {
		t.Fatalf("expected DB_AUTH_FAILED JSON body from handleExport, got %s", exportW.Body.String())
	}
}
