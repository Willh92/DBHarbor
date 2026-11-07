package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
)

func TestMain(m *testing.M) {
	if os.Getenv("DBHARBOR_MOCK_DUCKDB_CLI") == "1" {
		if len(os.Args) >= 2 && os.Args[1] == "--version" {
			fmt.Println("v1.1.3 duckdb mock")
			os.Exit(0)
		}
		stdinBytes, _ := io.ReadAll(os.Stdin)
		if capFile := os.Getenv("DBHARBOR_MOCK_DUCKDB_CAPTURE"); capFile != "" {
			payload, _ := json.Marshal(map[string]any{
				"args":  os.Args[1:],
				"stdin": string(stdinBytes),
			})
			_ = os.WriteFile(capFile, append(payload, '\n'), 0o600)
		}
		trimmed := strings.TrimSpace(string(stdinBytes))
		if strings.Contains(trimmed, "FAIL_SECOND_STMT") {
			fmt.Fprintln(os.Stderr, "Error: simulated constraint violation")
			os.Exit(1)
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "SELECT") {
			fmt.Println(`[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]`)
		} else {
			fmt.Println(`[]`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSingleFileWebSandboxesAndExporters(t *testing.T) {
	// 1. SQLite Web Sandbox guards (including comment/quote token boundary tricks and PRAGMAs)
	for _, badSQL := range []string{
		"ATTACH DATABASE '/etc/passwd' AS p",
		"ATTACH/*x*/DATABASE '/etc/passwd' AS p",
		"ATTACH'dbharbor.db'AS x",
		"DETACH DATABASE p",
		"DETACH/*comment*/'p'",
		"VACUUM INTO '/tmp/leak.sqlite'",
		"VACUUM/**/INTO '/tmp/leak.sqlite'",
		"VACUUM main INTO '/tmp/leak.sqlite'",
		"PRAGMA writable_schema = 1",
		"PRAGMA main.writable_schema=1",
		"PRAGMA \"trusted_schema\"(1)",
		"PRAGMA temp_store_directory = '/tmp'",
		"PRAGMA data_store_directory = '/tmp'",
		"SELECT load_extension('evil.so')",
		"SELECT \"load_extension\"('evil.so')",
		"SELECT readfile('/etc/hosts')",
		"SELECT READFILE/**/( '/etc/hosts' )",
		"SELECT writefile('/tmp/x', 'abc')",
		"SELECT fts3_tokenizer('simple')",
		"SELECT edit('secret', 'sh -c id')",
	} {
		if err := validateSQLiteWebSandbox(badSQL); err == nil {
			t.Fatalf("expected validateSQLiteWebSandbox to reject %q", badSQL)
		}
	}
	for _, safeSQL := range []string{
		"VACUUM;",
		"SELECT id, edit FROM posts WHERE body = 'ATTACH DATABASE x; VACUUM INTO y; readfile(z)'",
	} {
		if err := validateSQLiteWebSandbox(safeSQL); err != nil {
			t.Fatalf("expected validateSQLiteWebSandbox to allow %q, got %v", safeSQL, err)
		}
	}

	// DSN query-param / fragment / control-char injection guards on NewSQLite and NewDuckDB
	for _, badPath := range []string{
		"app.sqlite?_pragma=writable_schema(1)",
		"app.sqlite?cache=shared",
		"app.sqlite#frag",
		"app\x00.sqlite",
		"app\n.sqlite",
	} {
		if _, err := NewSQLite(driver.ConnectionConfig{Type: "sqlite", FilePath: badPath}); err == nil {
			t.Fatalf("expected NewSQLite to reject filePath %q", badPath)
		}
		if _, err := NewDuckDB(driver.ConnectionConfig{Type: "duckdb", FilePath: badPath}); err == nil {
			t.Fatalf("expected NewDuckDB to reject filePath %q", badPath)
		}
	}

	// 2. DuckDB Web Sandbox guards (including comment bypasses and CLI dot-commands)
	for _, badDuck := range []string{
		"ATTACH '/etc/passwd' AS ext",
		"ATTACH'db.duckdb'AS ext",
		"COPY items TO '/tmp/out.csv'",
		"INSTALL httpfs",
		"INSTALL/**/httpfs",
		"FORCE INSTALL httpfs",
		"LOAD/*comment*/httpfs",
		"SELECT * FROM read_csv('/etc/passwd')",
		"SELECT * FROM read_csv_auto('/etc/passwd')",
		"SELECT * FROM \"read_csv\"('/etc/passwd')",
		"SELECT * FROM read_parquet('secret.parquet')",
		"SELECT * FROM read_text('/etc/hosts')",
		"SELECT * FROM '/etc/passwd'",
		"SET enable_external_access = true",
		"SET/*x*/autoinstall_known_extensions = true",
		"SELECT getenv('AWS_SECRET_ACCESS_KEY')",
		".shell id",
		"  .read /etc/passwd",
		"SELECT 1;\n.shell whoami",
		"SELECT 1; .open /etc/passwd",
		"/* comment\n.system calc\n*/ SELECT 1",
	} {
		if err := validateDuckDBWebSandbox(badDuck); err == nil {
			t.Fatalf("expected validateDuckDBWebSandbox to reject %q", badDuck)
		}
	}
	if err := validateDuckDBWebSandbox("SELECT id, name, 'line1\n.not_a_dot_cmd' AS note FROM orders WHERE price > 10.5 ORDER BY id DESC;"); err != nil {
		t.Fatalf("expected normal DuckDB SELECT to pass sandbox check, got %v", err)
	}

	// 3. SQLite ExportDatabaseFile via VACUUM INTO
	tmpDir := t.TempDir()
	srcSQLite := filepath.Join(tmpDir, "src.sqlite")
	drv, err := NewSQLite(driver.ConnectionConfig{
		Type:     "sqlite",
		FilePath: srcSQLite,
		Options:  map[string]string{"__web_mode": "1"},
	})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer drv.Close()
	sqlDrv := drv.(*SQLiteDriver)
	if _, err := sqlDrv.ExecuteSQL(context.Background(), "main", "CREATE TABLE t1 (id INT PRIMARY KEY, v TEXT); INSERT INTO t1 VALUES (1, 'ok');", 10); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	snapSQLite := filepath.Join(tmpDir, "snap.sqlite")
	if err := sqlDrv.ExportDatabaseFile(context.Background(), snapSQLite); err != nil {
		t.Fatalf("SQLite ExportDatabaseFile: %v", err)
	}
	rawSnap, err := os.ReadFile(snapSQLite)
	if err != nil || len(rawSnap) < 16 || string(rawSnap[:16]) != "SQLite format 3\x00" {
		t.Fatalf("invalid SQLite snapshot output: err=%v len=%d", err, len(rawSnap))
	}

	// 4. DuckDB ExportDatabaseFile header validation (rejects non-DuckDB files; exports valid DUCK binary)
	fakeFile := filepath.Join(tmpDir, "not_duck.txt")
	_ = os.WriteFile(fakeFile, []byte("plain text secret content that is NOT a DuckDB database"), 0o600)
	badDuckDrv := &DuckDBDriver{filePath: fakeFile, cliBin: "nonexistent-duckdb-cli", webMode: true}
	if err := badDuckDrv.ExportDatabaseFile(context.Background(), filepath.Join(tmpDir, "out_bad.duckdb")); err == nil {
		t.Fatal("expected DuckDB ExportDatabaseFile to reject file lacking DUCK magic header")
	}

	validDuck := filepath.Join(tmpDir, "valid.duckdb")
	duckPayload := append([]byte{0, 0, 0, 0, 0, 0, 0, 0}, []byte("DUCK_TEST_HEADER_PAYLOAD_1234567890")...)
	_ = os.WriteFile(validDuck, duckPayload, 0o600)
	goodDuckDrv := &DuckDBDriver{filePath: validDuck, cliBin: "nonexistent-duckdb-cli", webMode: true}
	snapDuck := filepath.Join(tmpDir, "snap.duckdb")
	if err := goodDuckDrv.ExportDatabaseFile(context.Background(), snapDuck); err != nil {
		t.Fatalf("valid DuckDB ExportDatabaseFile failed: %v", err)
	}
	gotDuck, err := os.ReadFile(snapDuck)
	if err != nil || string(gotDuck) != string(duckPayload) {
		t.Fatalf("unexpected DuckDB snapshot content: err=%v", err)
	}
}

func TestSQLiteGeneratedColumnsChecksPragmasAndRebuild(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbFile := filepath.Join(tmpDir, "gen_rebuild.sqlite")
	drv, err := NewSQLite(driver.ConnectionConfig{
		Type:     "sqlite",
		FilePath: dbFile,
		Options:  map[string]string{"__web_mode": "1"},
	})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer drv.Close()
	sqlDrv := drv.(*SQLiteDriver)

	_, err = sqlDrv.CreateTable(ctx, driver.CreateTableRequest{
		Table:        "items",
		Strict:       true,
		WithoutRowID: true,
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "INTEGER", IsPrimaryKey: true},
			{Name: "price", Type: "INTEGER", Nullable: false},
			{Name: "qty", Type: "INTEGER", Nullable: false},
			{Name: "subtotal", Type: "INTEGER", Nullable: true, GeneratedExpr: "price * qty", GeneratedStorage: "STORED"},
			{Name: "label", Type: "TEXT", Nullable: true, GeneratedExpr: "'#' || CAST(id AS TEXT)", GeneratedStorage: "VIRTUAL"},
		},
		CheckConstraints: []driver.CheckConstraintDef{
			{Name: "chk_positive_price", Expression: "price >= 0", Enforced: true},
			{Name: "chk_positive_qty", Expression: "qty > 0", Enforced: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable with generated cols & checks: %v", err)
	}

	if _, err := sqlDrv.ExecuteSQL(ctx, "main", "INSERT INTO items (id, price, qty) VALUES (10, 25, 4);", 10); err != nil {
		t.Fatalf("seed items: %v", err)
	}

	st, err := sqlDrv.GetTableStructure(ctx, "main", "", "items")
	if err != nil {
		t.Fatalf("GetTableStructure: %v", err)
	}
	if st.Diagnostics["strict"] != "true" || st.Diagnostics["without_rowid"] != "true" {
		t.Fatalf("expected strict=true and without_rowid=true in diagnostics, got %+v", st.Diagnostics)
	}
	if len(st.CheckConstraints) != 2 {
		t.Fatalf("expected 2 check constraints, got %+v", st.CheckConstraints)
	}
	colByName := map[string]driver.ColumnDef{}
	for _, c := range st.Columns {
		colByName[c.Name] = c
	}
	if colByName["subtotal"].GeneratedStorage != "STORED" || !strings.Contains(colByName["subtotal"].GeneratedExpr, "price") {
		t.Fatalf("unexpected subtotal generated column metadata: %+v", colByName["subtotal"])
	}
	if colByName["label"].GeneratedStorage != "VIRTUAL" || colByName["label"].GeneratedExpr == "" {
		t.Fatalf("unexpected label generated column metadata: %+v", colByName["label"])
	}

	// Transactional rebuild: add a column in middle, drop one check, add a new check
	_, err = sqlDrv.AlterTable(ctx, driver.AlterTableRequest{
		Table: "items",
		Actions: []driver.AlterAction{
			{Action: "ADD_COLUMN", AfterColumn: "qty", Column: driver.ColumnDef{Name: "note", Type: "TEXT", Nullable: false, DefaultValue: "'std'"}},
			{Action: "DROP_CHECK", Check: driver.CheckConstraintDef{Name: "chk_positive_qty"}},
			{Action: "ADD_CHECK", Check: driver.CheckConstraintDef{Name: "chk_qty_cap", Expression: "qty BETWEEN 1 AND 1000"}},
		},
	})
	if err != nil {
		t.Fatalf("AlterTable transactional rebuild failed: %v", err)
	}

	res, err := sqlDrv.ExecuteSQL(ctx, "main", "SELECT id, subtotal, label, note FROM items WHERE id = 10;", 10)
	if err != nil || len(res) == 0 || len(res[0].Rows) != 1 {
		t.Fatalf("query after rebuild failed: err=%v res=%+v", err, res)
	}
	row := res[0].Rows[0]
	if row[1] != int64(100) || row[2] != "#10" || row[3] != "std" {
		t.Fatalf("unexpected rebuilt row values (generated columns must recompute): %+v", row)
	}

	stAfter, err := sqlDrv.GetTableStructure(ctx, "main", "", "items")
	if err != nil {
		t.Fatalf("GetTableStructure after rebuild: %v", err)
	}
	if stAfter.Diagnostics["strict"] != "true" || stAfter.Diagnostics["without_rowid"] != "true" {
		t.Fatalf("expected STRICT and WITHOUT ROWID preserved after rebuild, got %+v", stAfter.Diagnostics)
	}
	if len(stAfter.CheckConstraints) != 2 || stAfter.CheckConstraints[1].Name != "chk_qty_cap" {
		t.Fatalf("unexpected check constraints after rebuild: %+v", stAfter.CheckConstraints)
	}

	// SET_PRAGMA allowlist
	if _, err := sqlDrv.AlterTable(ctx, driver.AlterTableRequest{
		Actions: []driver.AlterAction{{Action: "SET_PRAGMA", PragmaKey: "synchronous", PragmaValue: "FULL"}},
	}); err != nil {
		t.Fatalf("expected valid SET_PRAGMA synchronous=FULL to succeed, got %v", err)
	}
	if _, err := sqlDrv.AlterTable(ctx, driver.AlterTableRequest{
		Actions: []driver.AlterAction{{Action: "SET_PRAGMA", PragmaKey: "writable_schema", PragmaValue: "1"}},
	}); err == nil {
		t.Fatal("expected disallowed SET_PRAGMA writable_schema to be rejected")
	}
}

func TestSQLiteVirtualTablesShadowHidingAndWebSandbox(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbFile := filepath.Join(tmpDir, "virt_sandbox.sqlite")
	drv, err := NewSQLite(driver.ConnectionConfig{
		Type:     "sqlite",
		FilePath: dbFile,
		Options:  map[string]string{"__web_mode": "1"},
	})
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer drv.Close()
	sqlDrv := drv.(*SQLiteDriver)

	if _, err := sqlDrv.CreateTable(ctx, driver.CreateTableRequest{
		Table:         "bad_vt",
		VirtualModule: "evil_module",
		Columns:       []driver.ColumnDef{{Name: "body", Type: "TEXT"}},
	}); err == nil {
		t.Fatal("expected unapproved virtual module to be rejected")
	}

	if _, err := sqlDrv.CreateTable(ctx, driver.CreateTableRequest{
		Table:         "docs_fts",
		VirtualModule: "fts5",
		Columns: []driver.ColumnDef{
			{Name: "title"},
			{Name: "body"},
		},
	}); err != nil {
		t.Fatalf("CreateTable fts5 virtual table: %v", err)
	}

	objs, err := sqlDrv.ListObjects(ctx, "main", "")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objs.Tables) != 1 || objs.Tables[0].Name != "docs_fts" || objs.Tables[0].Engine != "VIRTUAL (fts5)" {
		t.Fatalf("expected only docs_fts with VIRTUAL (fts5) engine and shadow tables hidden, got %+v", objs.Tables)
	}

	if _, err := sqlDrv.AlterTable(ctx, driver.AlterTableRequest{
		Table:   "docs_fts",
		Actions: []driver.AlterAction{{Action: "ADD_COLUMN", Column: driver.ColumnDef{Name: "extra", Type: "TEXT"}}},
	}); err == nil {
		t.Fatal("expected column alteration on virtual table to be blocked cleanly")
	}

	// Web sandbox filter block in BrowseData
	if _, err := sqlDrv.ExecuteSQL(ctx, "main", "CREATE TABLE notes (id INT PRIMARY KEY, body TEXT); INSERT INTO notes VALUES (1, 'hello');", 10); err != nil {
		t.Fatalf("seed notes: %v", err)
	}
	if err := sqlDrv.ManageObject(ctx, driver.ObjectActionRequest{
		Database:   "main",
		ObjectName: "trg_notes_noop",
		ObjectType: "TRIGGER",
		Action:     "CREATE_TRIGGER",
		TargetName: "notes",
		RoutineSQL: "CREATE TRIGGER trg_notes_noop AFTER INSERT ON notes BEGIN SELECT 1; END;",
	}); err != nil {
		t.Fatalf("ManageObject CREATE_TRIGGER: %v", err)
	}
	objsWithTrig, err := sqlDrv.ListObjects(ctx, "main", "")
	if err != nil {
		t.Fatalf("ListObjects with trigger: %v", err)
	}
	if len(objsWithTrig.Triggers) != 1 || objsWithTrig.Triggers[0].Name != "trg_notes_noop" || objsWithTrig.Triggers[0].TargetName != "notes" {
		t.Fatalf("expected ListObjects to return trigger trg_notes_noop on notes, got %+v", objsWithTrig.Triggers)
	}
	if _, err := sqlDrv.BrowseData(ctx, driver.BrowseDataRequest{
		Table:  "notes",
		Filter: "readfile('/etc/hosts') IS NOT NULL",
	}); err == nil {
		t.Fatal("expected BrowseData in Web mode to reject readfile() in Filter")
	}
	if _, err := sqlDrv.BrowseData(ctx, driver.BrowseDataRequest{
		Table:  "notes",
		Filter: "body = 'hello'",
	}); err != nil {
		t.Fatalf("expected safe BrowseData Filter to succeed, got %v", err)
	}

	// Streaming SQL BatchImport sandbox check
	_, err = sqlDrv.BatchImport(ctx, driver.ImportRequest{
		Format: "sql",
		Reader: strings.NewReader("INSERT INTO notes VALUES (2, 'two');\nATTACH DATABASE ':memory:' AS aux;\n"),
	})
	if err == nil {
		t.Fatal("expected streaming SQL BatchImport in Web mode to reject ATTACH DATABASE")
	}
}

func TestDuckDBArgsAndConstraintHelpers(t *testing.T) {
	duckWeb := &DuckDBDriver{
		filePath: "-flag-injection.duckdb",
		cliBin:   "duckdb",
		webMode:  true,
	}
	args := duckWeb.buildDuckDBArgs()
	if len(args) != 5 {
		t.Fatalf("unexpected DuckDB webMode args length (must not include SQL stmt in argv): %+v", args)
	}
	if args[0] != "-safe" {
		t.Fatalf("expected -safe as first DuckDB webMode arg, got %q", args[0])
	}
	if args[1] != "-cmd" || !strings.Contains(args[2], "SET lock_configuration=true;") {
		t.Fatalf("expected -cmd SET lock_configuration=true; in webMode args, got %+v", args)
	}
	if args[3] != "-json" {
		t.Fatalf("expected -json flag at index 3, got %q", args[3])
	}
	if args[4] != "./-flag-injection.duckdb" {
		t.Fatalf("expected leading-dash filePath to be prefixed with ./, got %q", args[4])
	}

	duckDesktop := &DuckDBDriver{
		filePath: "local.duckdb",
		cliBin:   "duckdb",
		webMode:  false,
	}
	desktopArgs := duckDesktop.buildDuckDBArgs()
	if len(desktopArgs) != 2 || desktopArgs[0] != "-json" || desktopArgs[1] != "local.duckdb" {
		t.Fatalf("unexpected DuckDB desktop args: %+v", desktopArgs)
	}

	// Verify canonical per-file mutex returns identical lock instance for equivalent paths
	tmpDir := t.TempDir()
	lockA := duckFileLock(filepath.Join(tmpDir, "sub", "..", "same.duckdb"))
	lockB := duckFileLock(filepath.Join(tmpDir, "same.duckdb"))
	if lockA == nil || lockA != lockB {
		t.Fatalf("expected identical *sync.Mutex for canonicalized DuckDB file paths")
	}
	if duckFileLock(":memory:") != nil {
		t.Fatalf("expected nil file lock for :memory: DuckDB")
	}

	fk, ok := parseDuckForeignKeyText("fk_orders_0", []string{"user_id"}, `FOREIGN KEY (user_id) REFERENCES "main"."users"(id) ON DELETE CASCADE`)
	if !ok || fk.RefTable != "users" || len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" || fk.OnDelete != "CASCADE" {
		t.Fatalf("unexpected parsed DuckDB foreign key: ok=%v fk=%+v", ok, fk)
	}
	if got := extractDuckCheckExpr("CHECK((price >= 0))"); got != "(price >= 0)" {
		t.Fatalf("unexpected extracted DuckDB check expression: %q", got)
	}
}

func TestDuckDBStdinExecutionAtomicMutationsAndStreamExport(t *testing.T) {
	tmpDir := t.TempDir()
	capFile := filepath.Join(tmpDir, "duck_capture.json")
	dbPath := filepath.Join(tmpDir, "stdin_test.duckdb")

	t.Setenv("DBHARBOR_MOCK_DUCKDB_CLI", "1")
	t.Setenv("DBHARBOR_MOCK_DUCKDB_CAPTURE", capFile)

	duck := &DuckDBDriver{
		filePath: dbPath,
		cliBin:   os.Args[0],
		webMode:  true,
	}

	// 1. ApplyRowMutations combines multiple DMLs into a single BEGIN TRANSACTION ... COMMIT stdin script
	mutRes, err := duck.ApplyRowMutations(context.Background(), driver.RowMutationRequest{
		Schema: "main",
		Table:  "orders",
		Inserts: []map[string]any{
			{"id": 1, "name": "first"},
			{"id": 2, "name": "second"},
		},
	})
	if err != nil {
		t.Fatalf("ApplyRowMutations failed: %v", err)
	}
	if mutRes.Inserted != 2 {
		t.Fatalf("expected Inserted=2, got %d", mutRes.Inserted)
	}
	rawCap, err := os.ReadFile(capFile)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	var captured struct {
		Args  []string `json:"args"`
		Stdin string   `json:"stdin"`
	}
	if err := json.Unmarshal(rawCap, &captured); err != nil {
		t.Fatalf("unmarshal capture: %v", err)
	}
	if len(captured.Args) != 5 || captured.Args[0] != "-safe" {
		t.Fatalf("expected 5 args starting with -safe and zero SQL in argv, got %#v", captured.Args)
	}
	for _, a := range captured.Args {
		if strings.Contains(a, "INSERT INTO") {
			t.Fatalf("SQL statement leaked into process argv: %#v", captured.Args)
		}
	}
	if !strings.HasPrefix(captured.Stdin, "BEGIN TRANSACTION;\n") || !strings.Contains(captured.Stdin, "COMMIT;") {
		t.Fatalf("expected transactional stdin script, got:\n%s", captured.Stdin)
	}

	// 2. StreamExport streams JSON rows cleanly without error
	var jsonBuf bytes.Buffer
	if err := duck.StreamExport(context.Background(), driver.ExportRequest{
		Schema:    "main",
		CustomSQL: "SELECT id, name FROM orders",
		Format:    "json",
	}, &jsonBuf); err != nil {
		t.Fatalf("StreamExport JSON failed: %v", err)
	}
	var exported []map[string]any
	if err := json.Unmarshal(jsonBuf.Bytes(), &exported); err != nil || len(exported) != 2 {
		t.Fatalf("unexpected StreamExport JSON output (err=%v): %s", err, jsonBuf.String())
	}
}
