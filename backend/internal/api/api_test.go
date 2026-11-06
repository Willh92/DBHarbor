package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
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

	step2 := doReq(http.MethodPost, "/api/auth/login/2fa", "", "10.0.0.2", map[string]string{
		"challengeToken": step1Data.ChallengeToken,
		"code":           enableData.RecoveryCodes[0],
	})
	if step2.Code != http.StatusOK || !strings.Contains(step2.Body.String(), `"usedRecoveryCode":true`) {
		t.Fatalf("2fa recovery login failed (%d): %s", step2.Code, step2.Body.String())
	}

	// Verify /api/version exposes Willh92/DBHarbor repo & embedded changelog
	verResp := doReq(http.MethodGet, "/api/version", "", "10.0.0.2", nil)
	if verResp.Code != http.StatusOK || !strings.Contains(verResp.Body.String(), "https://github.com/Willh92/DBHarbor.git") {
		t.Fatalf("unexpected /api/version response (%d): %s", verResp.Code, verResp.Body.String())
	}

	// 4. Brute-force protection from a separate attacker IP triggers captcha & 429 lockout
	attackerIP := "192.168.99.99"
	for i := 1; i <= 5; i++ {
		bad := doReq(http.MethodPost, "/api/auth/login", "", attackerIP, map[string]string{
			"username": "admin", "password": "wrong-password",
		})
		if i == 5 && bad.Code != http.StatusTooManyRequests {
			t.Fatalf("expected HTTP 429 lockout on 5th failed attempt, got %d: %s", bad.Code, bad.Body.String())
		}
	}
}
