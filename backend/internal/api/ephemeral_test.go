package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/crypto"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"

	_ "modernc.org/sqlite"
)

func buildSampleSQLiteBytes(t *testing.T) []byte {
	t.Helper()
	srcPath := filepath.Join(t.TempDir(), "sample_local.sqlite")
	db, err := sql.Open("sqlite", filepath.ToSlash(srcPath))
	if err != nil {
		t.Fatalf("open temp sqlite: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price INTEGER NOT NULL);
		INSERT INTO products (id, name, price) VALUES (1, 'Mechanical Keyboard', 129), (2, 'USB-C Dock', 79);
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed temp sqlite: %v", err)
	}
	_ = db.Close()

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read temp sqlite: %v", err)
	}
	return raw
}

func TestEphemeralSingleFileDBLifecycleAndSecurityIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	st, err := store.Open(tmpDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	tm := auth.NewTokenManager(vault.DeriveBytes("ephemeral-test"))
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

	uploadEphemeral := func(token, filename string, fileBytes []byte, driverType string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if driverType != "" {
			_ = mw.WriteField("driverType", driverType)
		}
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("write form file: %v", err)
		}
		_ = mw.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/fs/ephemeral-open", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	login := func(username, password string) (string, string) {
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
		uid, _ := out["userId"].(string)
		return tok, uid
	}

	adminTok, adminUID := login("admin", "AdminPass123!")

	// Create Alice (readwrite + canCreateConn), Bob (readwrite + canCreateConn), Reader (readonly)
	createUser := func(uname, role string, canCreate bool) string {
		w := doJSON(http.MethodPost, "/api/admin/users", adminTok, map[string]any{
			"username":      uname,
			"password":      "UserSecret123!",
			"role":          role,
			"canCreateConn": canCreate,
			"connScope":     "all",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("create user %s failed (%d): %s", uname, w.Code, w.Body.String())
		}
		var u store.User
		_ = json.Unmarshal(w.Body.Bytes(), &u)
		return u.ID
	}
	aliceUID := createUser("alice", "readwrite", true)
	_ = createUser("bob", "readwrite", true)
	_ = createUser("viewer", "readonly", false)

	aliceTok, _ := login("alice", "UserSecret123!")
	bobTok, _ := login("bob", "UserSecret123!")
	viewerTok, _ := login("viewer", "UserSecret123!")

	sqliteBytes := buildSampleSQLiteBytes(t)

	// 1. ReadOnly user MUST be denied opening ephemeral DB files
	if w := uploadEphemeral(viewerTok, "items.sqlite", sqliteBytes, ""); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for readonly user on ephemeral-open, got %d", w.Code)
	}

	// 2. Forged/Invalid file headers MUST be rejected (prevents uploading arbitrary files disguised as DBs)
	invalidPayloads := []struct {
		name string
		data []byte
	}{
		{"passwd.sqlite", []byte("root:x:0:0:root:/root:/bin/bash\n")},
		{"evil.db", []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff\x00\x00")},
		{"fake.duckdb", sqliteBytes}, // SQLite header disguised with .duckdb extension
		{"fake.sqlite", []byte("\x00\x00\x00\x00\x00\x00\x00\x00DUCK\x00\x00\x00\x00")}, // DuckDB header disguised with .sqlite extension
		{"notallowed.txt", sqliteBytes},
	}
	for _, tc := range invalidPayloads {
		w := uploadEphemeral(aliceTok, tc.name, tc.data, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid upload %s, got %d: %s", tc.name, w.Code, w.Body.String())
		}
	}

	// 3. Arbitrary File Download Prevention:
	// Even if Admin or Alice creates a normal connection AND tries to forge __ephemeral options in JSON,
	// GET /api/conn/{id}/file/export MUST return 403 Forbidden!
	secretHostFile := filepath.Join(tmpDir, "sensitive_host_notes.db")
	_ = os.WriteFile(secretHostFile, sqliteBytes, 0o600)

	wForgedAdmin := doJSON(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":     "Server Hosted SQLite",
		"type":     "sqlite",
		"filePath": secretHostFile,
		"options": map[string]string{
			"__ephemeral":       "1",
			"__ephemeral_owner": adminUID,
		},
	})
	if wForgedAdmin.Code != http.StatusOK {
		t.Fatalf("create server sqlite failed (%d): %s", wForgedAdmin.Code, wForgedAdmin.Body.String())
	}
	var forgedConn store.SavedConnection
	_ = json.Unmarshal(wForgedAdmin.Body.Bytes(), &forgedConn)
	if forgedConn.Ephemeral {
		t.Fatal("client-supplied __ephemeral option MUST be stripped on /api/connections")
	}
	if w := doJSON(http.MethodGet, "/api/conn/"+forgedConn.ID+"/file/export", adminTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when attempting binary download on non-ephemeral server connection, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Alice opens a genuine local SQLite file in an isolated ephemeral session
	wOpen := uploadEphemeral(aliceTok, "本地库存_v1.sqlite", sqliteBytes, "")
	if wOpen.Code != http.StatusOK {
		t.Fatalf("alice ephemeral-open failed (%d): %s", wOpen.Code, wOpen.Body.String())
	}
	var openResp struct {
		Connection  store.SavedConnection `json:"connection"`
		Database    string                `json:"database"`
		FirstTable  string                `json:"firstTable"`
		TablesCount int                   `json:"tablesCount"`
	}
	if err := json.Unmarshal(wOpen.Body.Bytes(), &openResp); err != nil {
		t.Fatalf("decode ephemeral-open response: %v", err)
	}
	ephID := openResp.Connection.ID
	if !openResp.Connection.Ephemeral {
		t.Fatal("expected connection.ephemeral == true")
	}
	if openResp.FirstTable != "products" || openResp.TablesCount != 1 {
		t.Fatalf("expected firstTable=products, tablesCount=1, got %+v", openResp)
	}
	// Host filesystem path MUST NEVER leak in SavedConnection.FilePath
	if strings.Contains(openResp.Connection.FilePath, "ephemeral_workspaces") || filepath.IsAbs(openResp.Connection.FilePath) {
		t.Fatalf("ephemeral host path leaked to client: %q", openResp.Connection.FilePath)
	}

	// 5. Cross-User & Cross-Admin Isolation:
	// Neither Bob NOR Admin should see Alice's private ephemeral session in /api/connections,
	// nor be able to query, export, update, duplicate, or delete it!
	for _, actor := range []struct {
		name string
		tok  string
	}{
		{"bob", bobTok},
		{"admin", adminTok},
	} {
		wList := doJSON(http.MethodGet, "/api/connections", actor.tok, nil)
		var payload struct {
			Connections []store.SavedConnection `json:"connections"`
		}
		_ = json.Unmarshal(wList.Body.Bytes(), &payload)
		for _, c := range payload.Connections {
			if c.ID == ephID {
				t.Fatalf("%s MUST NOT see Alice's ephemeral connection in /api/connections", actor.name)
			}
		}
		if w := doJSON(http.MethodGet, "/api/conn/"+ephID+"/databases", actor.tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for %s querying Alice's ephemeral DB, got %d", actor.name, w.Code)
		}
		if w := doJSON(http.MethodGet, "/api/conn/"+ephID+"/file/export", actor.tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for %s exporting Alice's ephemeral DB, got %d", actor.name, w.Code)
		}
		if w := doJSON(http.MethodDelete, "/api/connections/"+ephID, actor.tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for %s deleting Alice's ephemeral DB, got %d", actor.name, w.Code)
		}
	}

	// Protected directory check: neither Admin nor Bob can browse or manually create connections inside ephemeral_workspaces
	if w := doJSON(http.MethodGet, "/api/fs/browse?path="+filepath.Join(tmpDir, "ephemeral_workspaces"), adminTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 browsing ephemeral_workspaces even for admin, got %d", w.Code)
	}
	if w := doJSON(http.MethodPost, "/api/connections", adminTok, map[string]any{
		"name":     "Sneaky Manual Reference",
		"type":     "sqlite",
		"filePath": filepath.Join(tmpDir, "ephemeral_workspaces", "x", "y.sqlite"),
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 creating manual connection inside ephemeral_workspaces, got %d", w.Code)
	}

	// Ephemeral connection must NOT appear in exported connection JSON bundles and cannot be reconfigured or duplicated
	wAliceExportProfile := doJSON(http.MethodGet, "/api/connections/export", aliceTok, nil)
	if strings.Contains(wAliceExportProfile.Body.String(), "本地库存_v1.sqlite") {
		t.Fatalf("ephemeral connection must be excluded from /api/connections/export, got: %s", wAliceExportProfile.Body.String())
	}
	if w := doJSON(http.MethodPut, "/api/connections/"+ephID, aliceTok, map[string]any{
		"name": "Hijack Path", "type": "sqlite", "filePath": secretHostFile,
	}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 updating ephemeral connection filePath, got %d", w.Code)
	}
	if w := doJSON(http.MethodPost, "/api/connections/"+ephID+"/duplicate", aliceTok, nil); w.Code == http.StatusOK {
		t.Fatal("expected duplicating ephemeral connection to be disallowed")
	}

	// 6. SQL Sandbox Escape Prevention inside Ephemeral SQLite Session
	for _, escapeSQL := range []string{
		"ATTACH DATABASE ':memory:' AS aux",
		"VACUUM INTO '" + filepath.ToSlash(filepath.Join(tmpDir, "escaped.sqlite")) + "'",
		"SELECT readfile('/etc/passwd')",
	} {
		w := doJSON(http.MethodPost, "/api/conn/"+ephID+"/query", aliceTok, map[string]any{
			"database": "main",
			"query":    escapeSQL,
		})
		if w.Code == http.StatusOK {
			t.Fatalf("expected SQLite sandbox escape %q to be blocked, but got 200", escapeSQL)
		}
	}

	// 7. Alice edits rows in her ephemeral SQLite DB and exports the updated binary .sqlite file
	wMutate := doJSON(http.MethodPost, "/api/conn/"+ephID+"/table/mutate", aliceTok, map[string]any{
		"database":    "main",
		"table":       "products",
		"primaryKeys": []string{"id"},
		"inserts": []map[string]any{
			{"id": 3, "name": "4K Monitor", "price": 399},
		},
		"updates": []map[string]any{
			{"locator": map[string]any{"id": 1}, "changes": map[string]any{"price": 109}},
		},
	})
	if wMutate.Code != http.StatusOK {
		t.Fatalf("mutate ephemeral sqlite failed (%d): %s", wMutate.Code, wMutate.Body.String())
	}

	// Export via Bearer header (used by File System Access API direct local write-back)
	reqExp := httptest.NewRequest(http.MethodGet, "/api/conn/"+ephID+"/file/export", nil)
	reqExp.Header.Set("Authorization", "Bearer "+aliceTok)
	wExpBin := httptest.NewRecorder()
	router.ServeHTTP(wExpBin, reqExp)
	if wExpBin.Code != http.StatusOK {
		t.Fatalf("binary export via Bearer failed (%d): %s", wExpBin.Code, wExpBin.Body.String())
	}
	disp := wExpBin.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "filename*=UTF-8''") {
		t.Fatalf("expected RFC 5987 UTF-8 Content-Disposition, got %q", disp)
	}
	exportedBytes, _ := io.ReadAll(wExpBin.Result().Body)
	if len(exportedBytes) < 16 || string(exportedBytes[:16]) != "SQLite format 3\x00" {
		t.Fatalf("exported file is not a valid SQLite 3 binary (len=%d)", len(exportedBytes))
	}

	// Verify exported binary via single-use ?ticket=... (used by mobile & Safari/Firefox downloads)
	wTicket := doJSON(http.MethodPost, "/api/auth/ticket", aliceTok, nil)
	var ticketOut struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(wTicket.Body.Bytes(), &ticketOut)
	reqTicketExp := httptest.NewRequest(http.MethodGet, "/api/conn/"+ephID+"/file/export?ticket="+ticketOut.Ticket, nil)
	wTicketExp := httptest.NewRecorder()
	router.ServeHTTP(wTicketExp, reqTicketExp)
	if wTicketExp.Code != http.StatusOK {
		t.Fatalf("binary export via ticket failed (%d): %s", wTicketExp.Code, wTicketExp.Body.String())
	}
	// Replay of single-use ticket MUST fail with 401
	reqReplay := httptest.NewRequest(http.MethodGet, "/api/conn/"+ephID+"/file/export?ticket="+ticketOut.Ticket, nil)
	wReplay := httptest.NewRecorder()
	router.ServeHTTP(wReplay, reqReplay)
	if wReplay.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on replayed export ticket, got %d", wReplay.Code)
	}

	// Open exportedBytes locally to verify Alice's edits are intact inside the standalone .sqlite file
	verifyLocalPath := filepath.Join(t.TempDir(), "downloaded_back.sqlite")
	if err := os.WriteFile(verifyLocalPath, exportedBytes, 0o600); err != nil {
		t.Fatalf("write verify sqlite: %v", err)
	}
	vDB, err := sql.Open("sqlite", filepath.ToSlash(verifyLocalPath))
	if err != nil {
		t.Fatalf("open downloaded sqlite: %v", err)
	}
	var count int
	var kbPrice int
	if err := vDB.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&count); err != nil || count != 3 {
		_ = vDB.Close()
		t.Fatalf("expected 3 rows in downloaded sqlite, got count=%d err=%v", count, err)
	}
	if err := vDB.QueryRow(`SELECT price FROM products WHERE id = 1`).Scan(&kbPrice); err != nil || kbPrice != 109 {
		_ = vDB.Close()
		t.Fatalf("expected updated price=109 in downloaded sqlite, got %d err=%v", kbPrice, err)
	}
	_ = vDB.Close()

	// 8. Closing (deleting) the ephemeral session immediately removes the connection AND wipes the disk folder
	wDel := doJSON(http.MethodDelete, "/api/connections/"+ephID, aliceTok, nil)
	if wDel.Code != http.StatusOK {
		t.Fatalf("alice delete ephemeral connection failed (%d): %s", wDel.Code, wDel.Body.String())
	}
	entries, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces"))
	for _, e := range entries {
		sub, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces", e.Name()))
		if len(sub) > 0 {
			t.Fatalf("expected Alice's ephemeral workspace directory to be wiped after close, found %d subdirs", len(sub))
		}
	}

	// 9. User Deletion Cleanup: open another ephemeral session as Alice, then Admin deletes Alice -> disk wiped!
	wOpen2 := uploadEphemeral(aliceTok, "temp2.sqlite", sqliteBytes, "")
	if wOpen2.Code != http.StatusOK {
		t.Fatalf("second ephemeral open failed: %s", wOpen2.Body.String())
	}
	if wDelUser := doJSON(http.MethodDelete, "/api/admin/users/"+aliceUID, adminTok, nil); wDelUser.Code != http.StatusOK {
		t.Fatalf("delete user alice failed (%d): %s", wDelUser.Code, wDelUser.Body.String())
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "ephemeral_workspaces")); statErr == nil {
		userDirs, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces"))
		for _, ud := range userDirs {
			children, _ := os.ReadDir(filepath.Join(tmpDir, "ephemeral_workspaces", ud.Name()))
			if len(children) > 0 {
				t.Fatalf("expected zero leftover ephemeral workspaces after user deletion, found %d", len(children))
			}
		}
	}
}
