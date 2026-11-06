package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/crypto"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/driver/mongodb"
	"dbharbor/backend/internal/driver/redis"
	"dbharbor/backend/internal/driver/sqlbase"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
)

func TestRBACMultiUserIsolationAndOperations(t *testing.T) {
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

	tm := auth.NewTokenManager(vault.DeriveBytes("rbac-test"))
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

	loginAs := func(username, password string) (string, map[string]any) {
		t.Helper()
		resp := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
			"username": username,
			"password": password,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("login as %s failed (%d): %s", username, resp.Code, resp.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(resp.Body.Bytes(), &out)
		tok, _ := out["token"].(string)
		if tok == "" {
			t.Fatalf("empty token for %s", username)
		}
		return tok, out
	}

	// 1. Admin login & profile check
	adminTok, adminLogin := loginAs("admin", "AdminSecret123!")
	if adminLogin["role"] != store.RoleAdmin {
		t.Fatalf("expected admin role, got %v", adminLogin["role"])
	}

	// Last-admin protection: cannot demote or delete the sole active admin
	adminUID, _ := adminLogin["userId"].(string)
	if w := doJSON(http.MethodPut, "/api/admin/users/"+adminUID, adminTok, map[string]any{"role": "readonly"}); w.Code == http.StatusOK {
		t.Fatal("expected demoting last active admin to fail")
	}
	if w := doJSON(http.MethodDelete, "/api/admin/users/"+adminUID, adminTok, nil); w.Code == http.StatusOK {
		t.Fatal("expected deleting own/last admin account to fail")
	}

	// 2. Admin creates two shared SQLite connections: ConnA (granted) and ConnB (restricted)
	createConn := func(tok, name, dbFile, vis string) string {
		t.Helper()
		w := doJSON(http.MethodPost, "/api/connections", tok, map[string]any{
			"name":       name,
			"type":       "sqlite",
			"filePath":   filepath.Join(tmpDir, dbFile),
			"visibility": vis,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("create connection %s failed (%d): %s", name, w.Code, w.Body.String())
		}
		var sc store.SavedConnection
		_ = json.Unmarshal(w.Body.Bytes(), &sc)
		return sc.ID
	}
	connAID := createConn(adminTok, "Shared Analytics A", "conn_a.db", "shared")
	connBID := createConn(adminTok, "Restricted Payroll B", "conn_b.db", "shared")

	// Seed a table in ConnA via Admin
	if w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/query", adminTok, map[string]any{
		"database": "main",
		"query":    "CREATE TABLE orders (id INTEGER PRIMARY KEY, item TEXT, amount INT, start TEXT); INSERT INTO orders VALUES (1, 'Keyboard', 99, '2026-01-01');",
	}); w.Code != http.StatusOK {
		t.Fatalf("seed table failed (%d): %s", w.Code, w.Body.String())
	}

	// 3. Admin creates ReadWrite developer & ReadOnly auditor scoped only to ConnA
	wDev := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":       "dev_alice",
		"displayName":    "Alice Dev",
		"password":       "DevPass123!",
		"role":           "readwrite",
		"canCreateConn":  true,
		"connScope":      "selected",
		"allowedConnIds": []string{connAID},
	})
	if wDev.Code != http.StatusOK {
		t.Fatalf("create dev_alice failed (%d): %s", wDev.Code, wDev.Body.String())
	}
	var devUser store.User
	_ = json.Unmarshal(wDev.Body.Bytes(), &devUser)

	wAudit := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":       "auditor_bob",
		"displayName":    "Bob Auditor",
		"password":       "BobPass123!",
		"role":           "readonly",
		"canCreateConn":  false,
		"connScope":      "selected",
		"allowedConnIds": []string{connAID},
	})
	if wAudit.Code != http.StatusOK {
		t.Fatalf("create auditor_bob failed (%d): %s", wAudit.Code, wAudit.Body.String())
	}

	devTok, devLogin := loginAs("dev_alice", "DevPass123!")
	if devLogin["role"] != "readwrite" || devLogin["canCreateConn"] != true {
		t.Fatalf("unexpected dev login metadata: %+v", devLogin)
	}
	auditTok, auditLogin := loginAs("auditor_bob", "BobPass123!")
	if auditLogin["role"] != "readonly" || auditLogin["canCreateConn"] != false {
		t.Fatalf("unexpected auditor login metadata: %+v", auditLogin)
	}

	// Non-admins cannot access /api/admin/users
	if w := doJSON(http.MethodGet, "/api/admin/users", devTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for readwrite user on /api/admin/users, got %d", w.Code)
	}
	if w := doJSON(http.MethodGet, "/api/admin/users", auditTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for readonly user on /api/admin/users, got %d", w.Code)
	}

	// 4. Connection scoping & Private connection ownership
	// Neither dev_alice nor auditor_bob can query Restricted ConnB
	if w := doJSON(http.MethodGet, "/api/conn/"+connBID+"/databases", devTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for dev_alice on unassigned ConnB, got %d", w.Code)
	}
	// dev_alice cannot delete or edit admin's Shared ConnA
	if w := doJSON(http.MethodDelete, "/api/connections/"+connAID, devTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when dev_alice attempts to delete admin shared ConnA, got %d", w.Code)
	}
	// dev_alice creates a private connection (even if requesting shared, non-admin gets private)
	privConnID := createConn(devTok, "Alice Scratch DB", "alice_priv.db", "shared")
	// auditor_bob cannot create connections and cannot see Alice's private connection
	if w := doJSON(http.MethodPost, "/api/connections", auditTok, map[string]any{
		"name": "Illegal Conn", "type": "sqlite", "filePath": filepath.Join(tmpDir, "illegal.db"),
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when readonly user creates connection, got %d", w.Code)
	}
	wBobConns := doJSON(http.MethodGet, "/api/connections", auditTok, nil)
	var bobConnsPayload struct {
		Connections []store.SavedConnection `json:"connections"`
	}
	_ = json.Unmarshal(wBobConns.Body.Bytes(), &bobConnsPayload)
	if len(bobConnsPayload.Connections) != 1 || bobConnsPayload.Connections[0].ID != connAID {
		t.Fatalf("expected auditor_bob to see only ConnA, got %+v", bobConnsPayload.Connections)
	}
	if bobConnsPayload.Connections[0].Editable {
		t.Fatal("expected ConnA to be non-editable for auditor_bob")
	}

	// Duplicate & Export/Import connections
	wDup := doJSON(http.MethodPost, "/api/connections/"+privConnID+"/duplicate", devTok, nil)
	if wDup.Code != http.StatusOK {
		t.Fatalf("duplicate connection failed (%d): %s", wDup.Code, wDup.Body.String())
	}
	wExp := doJSON(http.MethodGet, "/api/connections/export", devTok, nil)
	if wExp.Code != http.StatusOK {
		t.Fatalf("export connections failed (%d): %s", wExp.Code, wExp.Body.String())
	}
	if wImp := doJSON(http.MethodPost, "/api/connections/import", adminTok, json.RawMessage(wExp.Body.Bytes())); wImp.Code != http.StatusOK {
		t.Fatalf("import connections failed (%d): %s", wImp.Code, wImp.Body.String())
	}

	// 5. Read-Only Enforcement (REST endpoints + SQL statement parser)
	// Safe reads succeed for auditor_bob
	for _, safeSQL := range []string{
		"SELECT id, item, amount, start FROM orders",
		"-- comment header\nWITH top_orders AS (SELECT * FROM orders WHERE amount > 10) SELECT * FROM top_orders",
		"EXPLAIN QUERY PLAN SELECT * FROM orders",
		"PRAGMA table_info('orders')",
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/query", auditTok, map[string]any{
			"database": "main",
			"query":    safeSQL,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("expected safe read query %q to succeed for readonly user, got %d: %s", safeSQL, w.Code, w.Body.String())
		}
	}

	// Mutating SQL statements MUST be blocked with 403 for auditor_bob
	for _, unsafeSQL := range []string{
		"INSERT INTO orders VALUES (2, 'Mouse', 49, '2026-01-02')",
		"UPDATE orders SET amount = 0 WHERE id = 1",
		"DELETE FROM orders WHERE id = 1",
		"DROP TABLE orders",
		"SELECT 1; DELETE FROM orders",
		"/* sneaky comment */ TRUNCATE TABLE orders",
		"WITH del AS (DELETE FROM orders RETURNING *) SELECT * FROM del",
		"SELECT * INTO leaked_table FROM orders",
		"SELECT * FROM orders FOR UPDATE",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA wal_checkpoint(FULL)",
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/query", auditTok, map[string]any{
			"database": "main",
			"query":    unsafeSQL,
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected unsafe SQL %q to be blocked with 403, got %d: %s", unsafeSQL, w.Code, w.Body.String())
		}
	}

	// Mutating REST endpoints MUST be blocked with 403 for auditor_bob
	if w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/table/mutate", auditTok, map[string]any{
		"database": "main", "table": "orders", "deletes": []map[string]any{{"id": 1}},
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /table/mutate for readonly user, got %d", w.Code)
	}
	if w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/object/action", auditTok, map[string]any{
		"database": "main", "objectName": "orders", "objectType": "TABLE", "action": "DROP",
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /object/action for readonly user, got %d", w.Code)
	}

	// Meanwhile dev_alice (readwrite) CAN insert rows into ConnA
	if w := doJSON(http.MethodPost, "/api/conn/"+connAID+"/query", devTok, map[string]any{
		"database": "main",
		"query":    "INSERT INTO orders VALUES (2, 'Monitor', 299, '2026-02-01')",
	}); w.Code != http.StatusOK {
		t.Fatalf("expected readwrite user INSERT to succeed, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Query History User Isolation & Private/Shared Snippets
	wDevHist := doJSON(http.MethodGet, "/api/history", devTok, nil)
	var devHist struct {
		History []store.HistoryEntry `json:"history"`
	}
	_ = json.Unmarshal(wDevHist.Body.Bytes(), &devHist)
	if len(devHist.History) != 1 {
		t.Fatalf("expected dev_alice to see only her 1 query in history, got %d", len(devHist.History))
	}

	// Alice saves 1 private snippet and 1 shared snippet
	wPrivSnip := doJSON(http.MethodPost, "/api/snippets", devTok, map[string]any{
		"title": "Alice Secret Query", "dbType": "sqlite", "sql": "SELECT 42", "isShared": false,
	})
	if wPrivSnip.Code != http.StatusOK {
		t.Fatalf("save private snippet failed: %s", wPrivSnip.Body.String())
	}
	wSharedSnip := doJSON(http.MethodPost, "/api/snippets", devTok, map[string]any{
		"title": "Team Orders Report", "dbType": "sqlite", "sql": "SELECT * FROM orders", "isShared": true,
	})
	var sharedSnip store.SavedSnippet
	_ = json.Unmarshal(wSharedSnip.Body.Bytes(), &sharedSnip)

	// Bob sees only the shared snippet and cannot delete Alice's shared snippet
	wBobSnips := doJSON(http.MethodGet, "/api/snippets", auditTok, nil)
	var bobSnips struct {
		Snippets []store.SavedSnippet `json:"snippets"`
	}
	_ = json.Unmarshal(wBobSnips.Body.Bytes(), &bobSnips)
	if len(bobSnips.Snippets) != 1 || bobSnips.Snippets[0].Title != "Team Orders Report" {
		t.Fatalf("expected Bob to see only shared snippet, got %+v", bobSnips.Snippets)
	}
	if w := doJSON(http.MethodDelete, "/api/snippets/"+sharedSnip.ID, auditTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when Bob tries to delete Alice's snippet, got %d", w.Code)
	}

	// 7. System Database Drop Protection & Audit Log eventType Serialization
	if w := doJSON(http.MethodDelete, "/api/conn/"+connAID+"/databases?name=main", adminTok, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("expected dropping protected 'main' database to fail with 400, got %d", w.Code)
	}
	if !sqlbase.IsProtectedSystemDatabase("mysql", "information_schema") || !sqlbase.IsProtectedSystemDatabase("postgres", "postgres") {
		t.Fatal("expected system databases to be protected")
	}

	wLogs := doJSON(http.MethodGet, "/api/auth/audit-logs?eventType=USER_CREATED", adminTok, nil)
	var logsPayload struct {
		Logs []store.AuditLogEntry `json:"logs"`
	}
	_ = json.Unmarshal(wLogs.Body.Bytes(), &logsPayload)
	if len(logsPayload.Logs) < 2 || logsPayload.Logs[0].EventType != "USER_CREATED" {
		t.Fatalf("expected USER_CREATED audit logs with populated eventType, got %+v", logsPayload.Logs)
	}

	// 7b. Local FS Browse & Internal DB Protection for Web Multi-User Roles
	// Even Admin cannot open DBHarbor's own internal dbharbor.db metadata store
	if w := doJSON(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":     "Internal Vault DB",
		"type":     "sqlite",
		"filePath": filepath.Join(tmpDir, "dbharbor.db"),
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when admin attempts to open internal dbharbor.db, got %d", w.Code)
	}

	// Non-admin readwrite user (dev_alice) is sandboxed to DataDir: browsing or connecting outside DataDir is blocked
	outsideDir := t.TempDir()
	if w := doJSON(http.MethodGet, "/api/fs/browse?path="+url.QueryEscape(outsideDir), devTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when non-admin browses outside DataDir, got %d", w.Code)
	}
	if w := doJSON(http.MethodPost, "/api/connections", devTok, map[string]any{
		"name":     "Outside Host Escape",
		"type":     "sqlite",
		"filePath": filepath.Join(outsideDir, "escape.db"),
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when non-admin opens SQLite file outside DataDir, got %d", w.Code)
	}

	// Non-admin browsing default DataDir succeeds and never exposes dbharbor.db
	wDevBrowse := doJSON(http.MethodGet, "/api/fs/browse?path="+url.QueryEscape(tmpDir)+"&all=1", devTok, nil)
	if wDevBrowse.Code != http.StatusOK {
		t.Fatalf("expected 200 when dev_alice browses DataDir, got %d: %s", wDevBrowse.Code, wDevBrowse.Body.String())
	}
	if bytes.Contains(wDevBrowse.Body.Bytes(), []byte("dbharbor.db")) {
		t.Fatalf("expected dbharbor.db to be hidden in /api/fs/browse response: %s", wDevBrowse.Body.String())
	}

	// 8. Immediate Disabled-User Rejection without waiting for JWT expiry
	disabledTrue := true
	if w := doJSON(http.MethodPut, "/api/admin/users/"+devUser.ID, adminTok, map[string]any{"disabled": disabledTrue}); w.Code != http.StatusOK {
		t.Fatalf("disable dev_alice failed (%d): %s", w.Code, w.Body.String())
	}
	if w := doJSON(http.MethodGet, "/api/connections", devTok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected disabled user's active JWT to immediately fail with 401, got %d", w.Code)
	}
}

func TestNoSQLReadOnlyCommandGuards(t *testing.T) {
	safeRedis := []string{
		"GET user:1",
		"HGETALL session:abc",
		"SCAN 0 MATCH user:* COUNT 50",
		"SLOWLOG GET 10",
		"CLIENT LIST",
		"SORT leaderboard DESC",
		"INFO memory",
	}
	for _, cmd := range safeRedis {
		if ok, reason := redis.IsReadOnlyCommand(cmd); !ok {
			t.Fatalf("expected Redis command %q to be allowed, got reason: %s", cmd, reason)
		}
	}

	unsafeRedis := []string{
		"SET user:1 hacked",
		"DEL user:1",
		"FLUSHDB",
		"FLUSHALL",
		"EVAL \"return redis.call('set','a','1')\" 0",
		"CONFIG SET dir /tmp",
		"SLOWLOG RESET",
		"CLIENT KILL ID 12",
		"SORT leaderboard STORE dumped_list",
	}
	for _, cmd := range unsafeRedis {
		if ok, _ := redis.IsReadOnlyCommand(cmd); ok {
			t.Fatalf("expected mutating Redis command %q to be blocked in read-only mode", cmd)
		}
	}

	safeMongo := []string{
		`{"find": "users", "filter": {"active": true}}`,
		`{"count": "orders"}`,
		`{"aggregate": "orders", "pipeline": [{"$match": {"amount": {"$gt": 50}}}, {"$group": {"_id": "$status", "n": {"$sum": 1}}}], "cursor": {}}`,
		`{"explain": {"find": "users"}}`,
	}
	for _, cmd := range safeMongo {
		if ok, reason := mongodb.IsReadOnlyCommand(cmd); !ok {
			t.Fatalf("expected MongoDB command %s to be allowed, got: %s", cmd, reason)
		}
	}

	unsafeMongo := []string{
		`{"insert": "users", "documents": [{"name": "x"}]}`,
		`{"delete": "users", "deletes": [{"q": {}, "limit": 0}]}`,
		`{"drop": "users"}`,
		`{"findAndModify": "users", "remove": true}`,
		`{"aggregate": "orders", "pipeline": [{"$match": {}}, {"$out": "pwned"}], "cursor": {}}`,
		`{"aggregate": "orders", "pipeline": [{"$merge": "target_coll"}], "cursor": {}}`,
		`{"explain": {"delete": "users"}}`,
	}
	for _, cmd := range unsafeMongo {
		if ok, _ := mongodb.IsReadOnlyCommand(cmd); ok {
			t.Fatalf("expected mutating MongoDB command %s to be blocked in read-only mode", cmd)
		}
	}
}
