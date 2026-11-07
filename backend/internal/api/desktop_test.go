package api_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"

	_ "dbharbor/backend/internal/driver/sqlite"
)

func TestDesktopPureLocalModeE2E(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	st, err := store.OpenLocal(tmpDir, vault)
	if err != nil {
		t.Fatalf("store.OpenLocal failed: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	exportOutPath := filepath.Join(tmpDir, "desktop_dump.sql")
	router := api.NewDesktopRouter(st, pm, api.DesktopConfig{
		BootTicket:          "boot-secret-123",
		SessionCookieSecret: "cookie-secret-456",
		SaveFileDialog: func(defaultName string) (string, bool, error) {
			return exportOutPath, false, nil
		},
	})
	ts := httptest.NewServer(router)
	defer ts.Close()

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. Without desktop session cookie, /api/connections must reject unauthorized localhost callers
	unauthResp, err := noRedirectClient.Get(ts.URL + "/api/connections")
	if err != nil {
		t.Fatalf("GET /api/connections failed: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without desktop session cookie, got %d", unauthResp.StatusCode)
	}
	if cors := unauthResp.Header.Get("Access-Control-Allow-Origin"); cors != "" {
		t.Fatalf("expected empty CORS origin in desktop mode, got %q", cors)
	}

	// 2. Account login endpoint must not exist in desktop mode
	loginResp, err := noRedirectClient.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/auth/login failed: %v", err)
	}
	_ = loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /api/auth/login in desktop mode, got %d", loginResp.StatusCode)
	}

	// 3. Exchange boot ticket for HttpOnly SameSite=Strict session cookie
	bootResp, err := noRedirectClient.Get(ts.URL + "/__desktop_boot?ticket=boot-secret-123")
	if err != nil {
		t.Fatalf("GET /__desktop_boot failed: %v", err)
	}
	_ = bootResp.Body.Close()
	if bootResp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect from /__desktop_boot, got %d", bootResp.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, ck := range bootResp.Cookies() {
		if ck.Name == "dbharbor_desktop_session" {
			sessionCookie = ck
			break
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected HttpOnly SameSite=Strict desktop session cookie, got %+v", sessionCookie)
	}

	// 4. Verify unauthenticated /api/version returns 401, and authenticated /api/version reports desktop edition & authEnabled=false
	unauthVerResp, err := noRedirectClient.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatalf("unauthenticated GET /api/version failed: %v", err)
	}
	_ = unauthVerResp.Body.Close()
	if unauthVerResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/version, got %d", unauthVerResp.StatusCode)
	}

	robotsResp, err := noRedirectClient.Get(ts.URL + "/robots.txt")
	if err != nil {
		t.Fatalf("GET /robots.txt failed: %v", err)
	}
	var robotsBuf bytes.Buffer
	_, _ = robotsBuf.ReadFrom(robotsResp.Body)
	_ = robotsResp.Body.Close()
	if robotsResp.StatusCode != http.StatusOK || robotsBuf.String() != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("unexpected /robots.txt: status=%d body=%q", robotsResp.StatusCode, robotsBuf.String())
	}
	if !strings.Contains(robotsResp.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("expected X-Robots-Tag header on /robots.txt, got %q", robotsResp.Header.Get("X-Robots-Tag"))
	}

	reqVer, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/version", nil)
	reqVer.AddCookie(sessionCookie)
	verResp, err := noRedirectClient.Do(reqVer)
	if err != nil {
		t.Fatalf("GET /api/version failed: %v", err)
	}
	var verPayload map[string]any
	_ = json.NewDecoder(verResp.Body).Decode(&verPayload)
	_ = verResp.Body.Close()
	if verResp.StatusCode != http.StatusOK || verPayload["edition"] != "desktop" || verPayload["authEnabled"] != false {
		t.Fatalf("expected 200 with edition=desktop and authEnabled=false, got status=%d payload=%+v", verResp.StatusCode, verPayload)
	}

	// 5. List sample SQLite connection & execute desktop native file export without any login/JWT
	reqConn, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/connections", nil)
	reqConn.AddCookie(sessionCookie)
	connResp, err := noRedirectClient.Do(reqConn)
	if err != nil {
		t.Fatalf("authenticated GET /api/connections failed: %v", err)
	}
	var connList struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	_ = json.NewDecoder(connResp.Body).Decode(&connList)
	_ = connResp.Body.Close()
	if connResp.StatusCode != http.StatusOK || len(connList.Connections) == 0 {
		t.Fatalf("expected sample connection in desktop mode, status=%d count=%d", connResp.StatusCode, len(connList.Connections))
	}
	connID := connList.Connections[0].ID

	// Create a quick table in sample SQLite DB and export via /export/desktop-save
	qBody, _ := json.Marshal(map[string]any{
		"database": "main",
		"query":    "CREATE TABLE desktop_items (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO desktop_items VALUES (1, 'HarborDesktop');",
	})
	reqQ, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conn/"+connID+"/query", bytes.NewReader(qBody))
	reqQ.Header.Set("Content-Type", "application/json")
	reqQ.AddCookie(sessionCookie)
	respQ, err := noRedirectClient.Do(reqQ)
	if err != nil || respQ.StatusCode != http.StatusOK {
		t.Fatalf("desktop SQLite query failed: status=%v err=%v", respQ, err)
	}
	_ = respQ.Body.Close()

	expBody, _ := json.Marshal(map[string]any{
		"database":   "main",
		"table":      "desktop_items",
		"format":     "sql",
		"includeDdl": true,
	})
	reqExp, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conn/"+connID+"/export/desktop-save", bytes.NewReader(expBody))
	reqExp.Header.Set("Content-Type", "application/json")
	reqExp.AddCookie(sessionCookie)
	respExp, err := noRedirectClient.Do(reqExp)
	if err != nil {
		t.Fatalf("POST /export/desktop-save failed: %v", err)
	}
	var expResult struct {
		Canceled  bool   `json:"canceled"`
		SavedPath string `json:"savedPath"`
		Bytes     int64  `json:"bytes"`
	}
	_ = json.NewDecoder(respExp.Body).Decode(&expResult)
	_ = respExp.Body.Close()
	if respExp.StatusCode != http.StatusOK || expResult.Canceled || expResult.SavedPath != exportOutPath || expResult.Bytes == 0 {
		t.Fatalf("unexpected desktop export result: status=%d res=%+v", respExp.StatusCode, expResult)
	}
	sqlBytes, err := os.ReadFile(exportOutPath)
	if err != nil || !strings.Contains(string(sqlBytes), "HarborDesktop") {
		t.Fatalf("exported SQL file missing expected content: err=%v body=%s", err, string(sqlBytes))
	}
}

func TestDesktopHeaderTokenUpdateCheckAndTriggers(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	st, err := store.OpenLocal(tmpDir, vault)
	if err != nil {
		t.Fatalf("store.OpenLocal failed: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	var darkApplied *bool
	pickedDBPath := filepath.Join(tmpDir, "picked.sqlite")
	sessionSecret := hex.EncodeToString(vault.DeriveBytes("desktop-local-session-v1"))

	router := api.NewDesktopRouter(st, pm, api.DesktopConfig{
		ListenPort:          18999,
		BootTicket:          "boot-port-scoped",
		SessionCookieSecret: sessionSecret,
		DataDir:             tmpDir,
		SetWindowDarkMode: func(dark bool) {
			darkApplied = &dark
		},
		OpenFileDialog: func(kind string, currentPath string) (string, bool, error) {
			return pickedDBPath, false, nil
		},
	})
	ts := httptest.NewServer(router)
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. Exchange boot ticket and verify redirect to "/" with port-scoped cookie
	bootResp, err := client.Get(ts.URL + "/__desktop_boot?ticket=boot-port-scoped")
	if err != nil {
		t.Fatalf("GET /__desktop_boot failed: %v", err)
	}
	_ = bootResp.Body.Close()
	if loc := bootResp.Header.Get("Location"); loc != "/" {
		t.Fatalf("expected redirect location '/', got %q", loc)
	}
	desktopToken := sessionSecret

	var portCookie *http.Cookie
	for _, ck := range bootResp.Cookies() {
		if ck.Name == "dbharbor_desktop_session_18999" {
			portCookie = ck
			break
		}
	}
	if portCookie == nil || portCookie.Value != sessionSecret {
		t.Fatalf("expected port-scoped cookie dbharbor_desktop_session_18999 with sessionSecret")
	}

	// 2. Verify X-DBHarbor-Desktop header token works WITHOUT any Cookie (prevents 401 on multi-window / WebView2 cookie loss)
	reqTheme, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/desktop/window-theme", strings.NewReader(`{"darkMode":true}`))
	reqTheme.Header.Set("Content-Type", "application/json")
	reqTheme.Header.Set("X-DBHarbor-Desktop", desktopToken)
	respTheme, err := client.Do(reqTheme)
	if err != nil || respTheme.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/desktop/window-theme with header token failed: status=%v err=%v", respTheme, err)
	}
	_ = respTheme.Body.Close()
	if darkApplied == nil || !*darkApplied {
		t.Fatalf("expected SetWindowDarkMode(true) callback to be invoked")
	}

	// 3. Verify /api/desktop/pick-file and /api/fs/browse with header token
	reqPick, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/desktop/pick-file", strings.NewReader(`{"kind":"sqlite"}`))
	reqPick.Header.Set("Content-Type", "application/json")
	reqPick.Header.Set("X-DBHarbor-Desktop", desktopToken)
	respPick, err := client.Do(reqPick)
	if err != nil || respPick.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/desktop/pick-file failed: status=%v err=%v", respPick, err)
	}
	var pickRes struct {
		FilePath string `json:"filePath"`
	}
	_ = json.NewDecoder(respPick.Body).Decode(&pickRes)
	_ = respPick.Body.Close()
	if pickRes.FilePath != pickedDBPath {
		t.Fatalf("expected picked path %q, got %q", pickedDBPath, pickRes.FilePath)
	}

	reqBrowse, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/fs/browse?all=1", nil)
	reqBrowse.Header.Set("X-DBHarbor-Desktop", desktopToken)
	respBrowse, err := client.Do(reqBrowse)
	if err != nil || respBrowse.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/fs/browse failed: status=%v err=%v", respBrowse, err)
	}
	var browsePayload struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	_ = json.NewDecoder(respBrowse.Body).Decode(&browsePayload)
	_ = respBrowse.Body.Close()
	for _, ent := range browsePayload.Entries {
		if strings.HasPrefix(strings.ToLower(ent.Name), "dbharbor.db") || ent.Name == ".secret.key" {
			t.Fatalf("expected internal state file %q to be hidden from /api/fs/browse", ent.Name)
		}
	}

	// Self-connection to DBHarbor's internal vault DB must be rejected even in Desktop mode
	selfConnBody, _ := json.Marshal(map[string]any{
		"name":     "Illegal Self Vault",
		"type":     "sqlite",
		"filePath": filepath.Join(tmpDir, "dbharbor.db"),
	})
	reqSelf, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/connections", bytes.NewReader(selfConnBody))
	reqSelf.Header.Set("Content-Type", "application/json")
	reqSelf.Header.Set("X-DBHarbor-Desktop", desktopToken)
	respSelf, err := client.Do(reqSelf)
	if err != nil || respSelf.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when connecting to internal dbharbor.db, got status=%v err=%v", respSelf, err)
	}
	_ = respSelf.Body.Close()

	// 4. Verify CREATE_TRIGGER with ReplaceExisting & DELIMITER stripping on sample SQLite DB
	reqConns, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/connections", nil)
	reqConns.Header.Set("X-DBHarbor-Desktop", desktopToken)
	respConns, err := client.Do(reqConns)
	if err != nil || respConns.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/connections with header token failed: status=%v err=%v", respConns, err)
	}
	var conns struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	_ = json.NewDecoder(respConns.Body).Decode(&conns)
	_ = respConns.Body.Close()
	connID := conns.Connections[0].ID

	trigDDL := "DELIMITER $$\nCREATE TRIGGER trg_orders_touch AFTER INSERT ON orders\nBEGIN\n  SELECT 1;\nEND$$\nDELIMITER ;"
	manageBody, _ := json.Marshal(map[string]any{
		"database":        "main",
		"objectName":      "trg_orders_touch",
		"objectType":      "TRIGGER",
		"action":          "CREATE_TRIGGER",
		"replaceExisting": true,
		"routineSql":      trigDDL,
	})
	for attempt := 0; attempt < 2; attempt++ {
		reqTrig, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/conn/"+connID+"/object/action", bytes.NewReader(manageBody))
		reqTrig.Header.Set("Content-Type", "application/json")
		reqTrig.Header.Set("X-DBHarbor-Desktop", desktopToken)
		respTrig, err := client.Do(reqTrig)
		if err != nil || respTrig.StatusCode != http.StatusOK {
			t.Fatalf("CREATE_TRIGGER attempt %d failed: status=%v err=%v", attempt+1, respTrig, err)
		}
		_ = respTrig.Body.Close()
	}
}
