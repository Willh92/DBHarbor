package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/crypto"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
)

func TestLargeSchemaScaleAndMemoryFootprint(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	st, err := store.OpenLocal(tmpDir, vault)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer st.Close()

	pm := pool.NewManager(st, 10)
	defer pm.CloseAll()

	router := api.NewDesktopRouter(st, pm, api.DesktopConfig{
		BootTicket:          "boot-perf-ticket",
		SessionCookieSecret: "cookie-perf-secret",
	})

	bootReq := httptest.NewRequest(http.MethodGet, "/__desktop_boot?ticket=boot-perf-ticket", nil)
	bootW := httptest.NewRecorder()
	router.ServeHTTP(bootW, bootReq)
	cookies := bootW.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected desktop session cookie, got 0 cookies (status=%d)", bootW.Code)
	}

	doReq := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. Create a local SQLite connection
	dbPath := filepath.Join(tmpDir, "scale_bench.db")
	createResp := doReq(http.MethodPost, "/api/connections", map[string]any{
		"name":     "Scale Bench SQLite",
		"type":     "sqlite",
		"filePath": dbPath,
	})
	if createResp.Code != http.StatusOK && createResp.Code != http.StatusCreated {
		t.Fatalf("create connection failed (%d): %s", createResp.Code, createResp.Body.String())
	}
	var connRes struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(createResp.Body.Bytes(), &connRes)
	connID := connRes.ID

	// 2. Provision 300 tables (1,800 columns) + 2,500 rows in tbl_000 via streaming SQL import
	var ddl strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&ddl,
			"CREATE TABLE tbl_%03d (id INTEGER PRIMARY KEY, code TEXT NOT NULL, category TEXT, amount REAL DEFAULT 0, active INTEGER DEFAULT 1, created_at TEXT);\n",
			i,
		)
	}
	for r := 1; r <= 2500; r++ {
		fmt.Fprintf(&ddl,
			"INSERT INTO tbl_000 (id, code, category, amount, active, created_at) VALUES (%d, 'ORD-%05d', 'CAT_%d', %.2f, 1, '2026-01-01T00:00:00Z');\n",
			r, r, r%12, float64(r)*1.25,
		)
	}

	impStart := time.Now()
	impResp := doReq(http.MethodPost, "/api/conn/"+connID+"/import/sql", map[string]any{
		"database": "main",
		"sql":      ddl.String(),
	})
	if impResp.Code != http.StatusOK {
		t.Fatalf("import/sql failed (%d): %s", impResp.Code, impResp.Body.String())
	}
	impElapsed := time.Since(impStart)

	// 3. Measure /objects on 300-table database
	objStart := time.Now()
	objResp := doReq(http.MethodGet, "/api/conn/"+connID+"/objects?database=main", nil)
	objElapsed := time.Since(objStart)
	if objResp.Code != http.StatusOK {
		t.Fatalf("objects failed (%d): %s", objResp.Code, objResp.Body.String())
	}
	var objRes struct {
		Tables []any `json:"tables"`
	}
	_ = json.Unmarshal(objResp.Body.Bytes(), &objRes)
	if len(objRes.Tables) != 300 {
		t.Fatalf("expected 300 tables, got %d", len(objRes.Tables))
	}

	// 4. Measure cold vs 20s-TTL cached /autocomplete across 300 tables (1,800 columns)
	acColdStart := time.Now()
	acColdResp := doReq(http.MethodGet, "/api/conn/"+connID+"/autocomplete?database=main", nil)
	acColdElapsed := time.Since(acColdStart)
	if acColdResp.Code != http.StatusOK {
		t.Fatalf("autocomplete cold failed (%d): %s", acColdResp.Code, acColdResp.Body.String())
	}
	var acColdData struct {
		Tables map[string][]string `json:"tables"`
	}
	_ = json.Unmarshal(acColdResp.Body.Bytes(), &acColdData)
	if len(acColdData.Tables) != 300 {
		t.Fatalf("expected 300 autocomplete tables, got %d", len(acColdData.Tables))
	}

	acHitStart := time.Now()
	acHitResp := doReq(http.MethodGet, "/api/conn/"+connID+"/autocomplete?database=main", nil)
	acHitElapsed := time.Since(acHitStart)
	if acHitResp.Code != http.StatusOK {
		t.Fatalf("autocomplete cached failed (%d)", acHitResp.Code)
	}

	// 5. Measure /table/browse pagination & verify server-side pageSize upper clamp (<= 1000)
	brStart := time.Now()
	brResp := doReq(http.MethodPost, "/api/conn/"+connID+"/table/browse", map[string]any{
		"database": "main",
		"table":    "tbl_000",
		"page":     1,
		"pageSize": 99999,
	})
	brElapsed := time.Since(brStart)
	if brResp.Code != http.StatusOK {
		t.Fatalf("browse failed (%d): %s", brResp.Code, brResp.Body.String())
	}
	var brData struct {
		Total    int64 `json:"total"`
		PageSize int   `json:"pageSize"`
		Rows     []any `json:"rows"`
	}
	_ = json.Unmarshal(brResp.Body.Bytes(), &brData)
	if brData.Total != 2500 {
		t.Fatalf("expected total=2500, got %d", brData.Total)
	}
	if len(brData.Rows) != 1000 {
		t.Fatalf("expected clamped 1000 rows, got %d", len(brData.Rows))
	}

	// 6. Inspect Go heap footprint & disconnect pool release
	runtime.GC()
	statsResp := doReq(http.MethodGet, "/api/system/stats", nil)
	if statsResp.Code != http.StatusOK {
		t.Fatalf("stats failed (%d)", statsResp.Code)
	}
	var sysStats struct {
		AllocMB     float64 `json:"allocMB"`
		Goroutines  int     `json:"goroutines"`
		ActivePools int     `json:"activePools"`
	}
	_ = json.Unmarshal(statsResp.Body.Bytes(), &sysStats)
	if sysStats.ActivePools != 1 {
		t.Fatalf("expected 1 active pool before disconnect, got %d", sysStats.ActivePools)
	}
	if sysStats.AllocMB > 80 {
		t.Fatalf("unexpectedly high Go heap allocation: %.2f MB", sysStats.AllocMB)
	}

	discResp := doReq(http.MethodPost, "/api/connections/"+connID+"/disconnect", nil)
	if discResp.Code != http.StatusOK {
		t.Fatalf("disconnect failed (%d)", discResp.Code)
	}
	if pm.Stats().ActivePools != 0 {
		t.Fatalf("expected 0 active pools after disconnect, got %d", pm.Stats().ActivePools)
	}

	t.Logf(
		"[PERF] 300-table/1800-col/2500-row benchmark: seed=%v | listObjects=%v | autocompleteCold=%v -> cached=%v | browse1000=%v | heapAlloc=%.2f MB | goroutines=%d",
		impElapsed, objElapsed, acColdElapsed, acHitElapsed, brElapsed, sysStats.AllocMB, sysStats.Goroutines,
	)
}
