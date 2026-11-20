package sqlmeta

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
)

type mockSQLDriver struct {
	executed []string
	managed  []driver.ObjectActionRequest
	handler  func(database, query string) ([]driver.QueryResult, error)
}

func (m *mockSQLDriver) Meta() driver.DriverMeta                        { return driver.DriverMeta{} }
func (m *mockSQLDriver) Ping(context.Context) (driver.ServerInfo, error) { return driver.ServerInfo{}, nil }
func (m *mockSQLDriver) Close() error                                   { return nil }
func (m *mockSQLDriver) ListDatabases(context.Context) ([]driver.DatabaseSummary, error) {
	return nil, nil
}
func (m *mockSQLDriver) CreateDatabase(context.Context, string, string) error       { return nil }
func (m *mockSQLDriver) ListSchemas(context.Context, string) ([]string, error)      { return nil, nil }
func (m *mockSQLDriver) ListObjects(context.Context, string, string) (driver.SchemaObjects, error) {
	return driver.SchemaObjects{}, nil
}
func (m *mockSQLDriver) GetTableStructure(context.Context, string, string, string) (driver.TableStructure, error) {
	return driver.TableStructure{DDL: "CREATE TABLE t (id INT);"}, nil
}
func (m *mockSQLDriver) BrowseData(context.Context, driver.BrowseDataRequest) (driver.QueryResult, error) {
	return driver.QueryResult{}, nil
}
func (m *mockSQLDriver) BuildMutationPreviewSQL(driver.RowMutationRequest) ([]string, error) {
	return nil, nil
}
func (m *mockSQLDriver) ApplyRowMutations(context.Context, driver.RowMutationRequest) (driver.MutationResult, error) {
	return driver.MutationResult{}, nil
}
func (m *mockSQLDriver) CreateTable(context.Context, driver.CreateTableRequest) (string, error) {
	return "", nil
}
func (m *mockSQLDriver) AlterTable(context.Context, driver.AlterTableRequest) ([]string, error) {
	return nil, nil
}
func (m *mockSQLDriver) ManageObject(_ context.Context, req driver.ObjectActionRequest) error {
	m.managed = append(m.managed, req)
	return nil
}
func (m *mockSQLDriver) ExecuteSQL(_ context.Context, database, query string, _ int) ([]driver.QueryResult, error) {
	m.executed = append(m.executed, query)
	if m.handler != nil {
		return m.handler(database, query)
	}
	return []driver.QueryResult{{Statement: query}}, nil
}
func (m *mockSQLDriver) StreamExport(context.Context, driver.ExportRequest, io.Writer) error {
	return nil
}
func (m *mockSQLDriver) BatchImport(context.Context, driver.ImportRequest) (driver.ImportResult, error) {
	return driver.ImportResult{}, nil
}
func (m *mockSQLDriver) GetAutocompleteMeta(context.Context, string) (driver.AutocompleteMeta, error) {
	return driver.AutocompleteMeta{}, nil
}

func TestEnrichSchemaObjectsTriggersAndEngineExtras(t *testing.T) {
	ctx := context.Background()

	// 1. MySQL: Triggers (TargetName) + Events
	myMock := &mockSQLDriver{
		handler: func(_, q string) ([]driver.QueryResult, error) {
			switch {
			case strings.Contains(q, "information_schema.TRIGGERS"):
				return []driver.QueryResult{{
					Columns: []driver.QueryColumn{{Name: "TRIGGER_NAME"}, {Name: "EVENT_OBJECT_TABLE"}, {Name: "ACTION_TIMING"}, {Name: "EVENT_MANIPULATION"}},
					Rows:    [][]any{{"trg_orders_audit", "orders", "AFTER", "INSERT"}},
				}}, nil
			case strings.Contains(q, "information_schema.EVENTS"):
				return []driver.QueryResult{{
					Columns: []driver.QueryColumn{{Name: "EVENT_NAME"}, {Name: "STATUS"}, {Name: "EVENT_TYPE"}, {Name: "INTERVAL_VALUE"}, {Name: "INTERVAL_FIELD"}, {Name: "EXECUTE_AT"}, {Name: "EVENT_COMMENT"}},
					Rows:    [][]any{{"ev_cleanup", "ENABLED", "RECURRING", "1", "DAY", "", "Nightly cleanup"}},
				}}, nil
			default:
				return nil, nil
			}
		},
	}
	myObjs := driver.SchemaObjects{Database: "shop"}
	EnrichSchemaObjects(ctx, "mysql", myMock, &myObjs)
	if len(myObjs.Triggers) != 1 || myObjs.Triggers[0].TargetName != "orders" {
		t.Fatalf("expected MySQL trigger TargetName='orders', got %#v", myObjs.Triggers)
	}
	if len(myObjs.Events) != 1 || myObjs.Events[0].Status != "ENABLED" || myObjs.Events[0].Extra["intervalField"] != "DAY" {
		t.Fatalf("unexpected MySQL events: %#v", myObjs.Events)
	}

	// 2. DuckDB with alphabetically sorted columns (matching duckdb.go JSON output behavior)
	duckMock := &mockSQLDriver{
		handler: func(_, q string) ([]driver.QueryResult, error) {
			if strings.Contains(q, "duckdb_functions()") {
				return []driver.QueryResult{{
					Columns: []driver.QueryColumn{{Name: "comment"}, {Name: "definition"}, {Name: "function_name"}, {Name: "function_type"}, {Name: "parameters"}},
					Rows:    [][]any{{"Adds two ints", "a + b", "add_two", "macro", "[a, b]"}},
				}}, nil
			}
			if strings.Contains(q, "duckdb_extensions()") {
				return []driver.QueryResult{{
					Columns: []driver.QueryColumn{{Name: "description"}, {Name: "extension_name"}, {Name: "extension_version"}, {Name: "installed"}, {Name: "loaded"}},
					Rows:    [][]any{{"JSON support", "json", "v1.1", "true", "true"}},
				}}, nil
			}
			return nil, nil
		},
	}
	duckObjs := driver.SchemaObjects{Schema: "main"}
	EnrichSchemaObjects(ctx, "duckdb", duckMock, &duckObjs)
	if len(duckObjs.Macros) != 1 || duckObjs.Macros[0].Name != "add_two" || duckObjs.Macros[0].Extra["definition"] != "a + b" {
		t.Fatalf("unexpected DuckDB macros: %#v", duckObjs.Macros)
	}
	if len(duckObjs.Extensions) != 1 || duckObjs.Extensions[0].Name != "json" || duckObjs.Extensions[0].Status != "LOADED" {
		t.Fatalf("unexpected DuckDB extensions: %#v", duckObjs.Extensions)
	}
}

func TestFetchObjectDDLViewsMatViewsAndRedactedDbLink(t *testing.T) {
	ctx := context.Background()
	pgMock := &mockSQLDriver{
		handler: func(_, q string) ([]driver.QueryResult, error) {
			return []driver.QueryResult{{
				Columns: []driver.QueryColumn{{Name: "viewdef"}},
				Rows:    [][]any{{"SELECT id FROM users"}},
			}}, nil
		},
	}
	viewDDL, err := FetchObjectDDL(ctx, "postgres", pgMock, "app", "public", "VIEW", "v_users")
	if err != nil || !strings.HasPrefix(viewDDL, `CREATE OR REPLACE VIEW "public"."v_users" AS`) {
		t.Fatalf("unexpected PG VIEW DDL (%v): %s", err, viewDDL)
	}
	mvDDL, err := FetchObjectDDL(ctx, "postgres", pgMock, "app", "public", "MATERIALIZED_VIEW", "mv_users")
	if err != nil || !strings.HasPrefix(mvDDL, `CREATE MATERIALIZED VIEW "public"."mv_users" AS`) {
		t.Fatalf("unexpected PG MATERIALIZED_VIEW DDL (%v): %s", err, mvDDL)
	}

	oraMock := &mockSQLDriver{
		handler: func(_, q string) ([]driver.QueryResult, error) {
			return []driver.QueryResult{{
				Columns: []driver.QueryColumn{{Name: "OWNER"}, {Name: "DB_LINK"}, {Name: "USERNAME"}, {Name: "HOST"}},
				Rows:    [][]any{{"HR", "REMOTE_ERP", "ERP_USER", "erp.example.com:1521/ORCL"}},
			}}, nil
		},
	}
	linkDDL, err := FetchObjectDDL(ctx, "oracle", oraMock, "", "HR", "DB_LINK", "REMOTE_ERP")
	if err != nil || !strings.Contains(linkDDL, `IDENTIFIED BY "********"`) {
		t.Fatalf("expected redacted DB_LINK DDL, got (%v): %s", err, linkDDL)
	}
}

func TestExecuteObjectActionSecurityGuardrailsAndBuilders(t *testing.T) {
	ctx := context.Background()
	drv := &mockSQLDriver{}

	// 1. MySQL EVENT typed Spec builder + non-admin Web DEFINER block
	evSQLs, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "ev_purge_sessions",
		ObjectType:  "EVENT",
		Action:      "CREATE_OR_REPLACE",
		PreviewOnly: true,
		Comment:     "Purge expired sessions",
		Spec: &driver.ObjectSpec{
			ScheduleKind: "EVERY",
			EveryValue:   6,
			EveryUnit:    "HOUR",
			Preserve:     true,
			BodySQL:      "DELETE FROM sessions WHERE expires_at < NOW()",
		},
	})
	if err != nil || len(evSQLs) != 2 || !strings.Contains(evSQLs[1], "ON SCHEDULE EVERY 6 HOUR ON COMPLETION PRESERVE ENABLE") {
		t.Fatalf("unexpected MySQL EVENT preview (%v): %#v", err, evSQLs)
	}

	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:   "shop",
		ObjectName: "ev_priv_esc",
		ObjectType: "EVENT",
		Action:     "CREATE_OR_REPLACE",
		WebMode:    true,
		IsAdmin:    false,
		RoutineSQL: "CREATE DEFINER='root'@'localhost' EVENT ev_priv_esc ON SCHEDULE EVERY 1 DAY DO SELECT 1",
	}); err == nil {
		t.Fatal("expected DEFINER= in Web non-admin mode to be rejected")
	}

	// 2. PostgreSQL EXTENSION host-escape blocklist in Web non-admin mode
	for _, ext := range []string{"adminpack", "file_fdw", "dblink", "postgres_fdw", "plpython3u", "plsh"} {
		if _, err := ExecuteObjectAction(ctx, "postgres", drv, driver.ObjectActionRequest{
			Database:   "app",
			ObjectName: ext,
			ObjectType: "EXTENSION",
			Action:     "INSTALL",
			WebMode:    true,
			IsAdmin:    false,
		}); err == nil {
			t.Errorf("expected dangerous PG extension %q to be blocked in Web non-admin mode", ext)
		}
	}
	// Safe extension (uuid-ossp) should pass preview
	safeExtSQL, err := ExecuteObjectAction(ctx, "postgres", drv, driver.ObjectActionRequest{
		Database:    "app",
		Schema:      "public",
		ObjectName:  "uuid-ossp",
		ObjectType:  "EXTENSION",
		Action:      "INSTALL",
		PreviewOnly: true,
		WebMode:     true,
		IsAdmin:     false,
	})
	if err != nil || len(safeExtSQL) != 1 || safeExtSQL[0] != `CREATE EXTENSION IF NOT EXISTS "uuid-ossp" SCHEMA "public"` {
		t.Fatalf("unexpected safe PG extension SQL (%v): %#v", err, safeExtSQL)
	}
	// Injection in extension name must fail even for admins
	if _, err := ExecuteObjectAction(ctx, "postgres", drv, driver.ObjectActionRequest{
		ObjectName: `uuid"; DROP TABLE users; --`,
		ObjectType: "EXTENSION",
		Action:     "INSTALL",
		IsAdmin:    true,
	}); err == nil {
		t.Fatal("expected invalid extension name to be rejected")
	}

	// 3. DuckDB EXTENSION & MACRO Web sandbox blocks
	if _, err := ExecuteObjectAction(ctx, "duckdb", drv, driver.ObjectActionRequest{
		ObjectName: "httpfs",
		ObjectType: "EXTENSION",
		Action:     "INSTALL",
		WebMode:    true,
		IsAdmin:    false,
	}); err == nil {
		t.Fatal("expected DuckDB INSTALL extension to be blocked for non-admin Web users")
	}

	for _, badExpr := range []string{
		"SELECT * FROM read_csv('/etc/passwd')",
		"SELECT * FROM read_parquet('s3://bucket/data.parquet')",
		"SELECT getenv('AWS_SECRET_ACCESS_KEY')",
	} {
		if _, err := ExecuteObjectAction(ctx, "duckdb", drv, driver.ObjectActionRequest{
			Schema:     "main",
			ObjectName: "m_evil",
			ObjectType: "TABLE_MACRO",
			Action:     "CREATE_OR_REPLACE",
			WebMode:    true,
			Spec: &driver.ObjectSpec{
				IsTableMacro: true,
				MacroExpr:    badExpr,
			},
		}); err == nil {
			t.Errorf("expected DuckDB macro sandbox to block %q", badExpr)
		}
	}

	if _, err := ExecuteObjectAction(ctx, "duckdb", drv, driver.ObjectActionRequest{
		Schema:     "main",
		ObjectName: "m_inj",
		ObjectType: "MACRO",
		Action:     "CREATE_OR_REPLACE",
		Spec: &driver.ObjectSpec{
			Parameters: []string{"a) AS 1; DROP TABLE x; --"},
			MacroExpr:  "1",
		},
	}); err == nil {
		t.Fatal("expected invalid DuckDB macro parameter name to be rejected")
	}

	// 4. Oracle DB_LINK SSRF protection & password redaction
	for _, ssrfHost := range []string{
		"127.0.0.1:1521/XE",
		"localhost:1521/ORCL",
		"169.254.169.254:1521/META",
		"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=10.0.0.12)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=ORCL)))",
	} {
		if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
			ObjectName:  "LNK_INTERNAL",
			ObjectType:  "DB_LINK",
			Action:      "CREATE_OR_REPLACE",
			AllowPrivIP: false,
			PreviewOnly: true,
			Spec: &driver.ObjectSpec{
				DbLinkHost: ssrfHost,
				DbLinkUser: "scott",
				DbLinkPass: "tiger",
			},
		}); err == nil {
			t.Errorf("expected DB_LINK SSRF guard to block internal host %q", ssrfHost)
		}
	}

	// Quote injection in DB_LINK password must fail
	if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName:  "LNK_INJ",
		ObjectType:  "DB_LINK",
		Action:      "CREATE_OR_REPLACE",
		AllowPrivIP: true,
		Spec: &driver.ObjectSpec{
			DbLinkHost: "10.0.0.12:1521/ORCL",
			DbLinkUser: "scott",
			DbLinkPass: `bad" USING 'evil`,
		},
	}); err == nil {
		t.Fatal("expected quote character in DbLinkPass to be rejected")
	}

	// Executed DB_LINK must execute real password internally while returning redacted SQL!
	drv.executed = nil
	redactedOut, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName:  "LNK_SAFE",
		ObjectType:  "DB_LINK",
		Action:      "CREATE_OR_REPLACE",
		AllowPrivIP: true,
		PreviewOnly: false,
		Spec: &driver.ObjectSpec{
			DbLinkHost: "10.0.0.12:1521/ORCL",
			DbLinkUser: "scott",
			DbLinkPass: "SuperSecret123!",
		},
	})
	if err != nil {
		t.Fatalf("unexpected DB_LINK execution error: %v", err)
	}
	if len(redactedOut) != 1 || strings.Contains(redactedOut[0], "SuperSecret123!") || !strings.Contains(redactedOut[0], `"********"`) {
		t.Fatalf("password leaked or missing mask in returned SQL: %#v", redactedOut)
	}
	if len(drv.executed) != 1 || !strings.Contains(drv.executed[0], `"SuperSecret123!"`) {
		t.Fatalf("expected driver execution to receive real password, got: %#v", drv.executed)
	}

	// 5. Legacy PostgreSQL DROP TRIGGER preview must include ON "schema"."targetName"
	pgDropTrig, err := ExecuteObjectAction(ctx, "postgres", drv, driver.ObjectActionRequest{
		Database:    "app",
		Schema:      "public",
		ObjectName:  "trg_audit",
		ObjectType:  "TRIGGER",
		Action:      "DROP",
		TargetName:  "orders",
		PreviewOnly: true,
	})
	if err != nil || len(pgDropTrig) != 1 || pgDropTrig[0] != `DROP TRIGGER IF EXISTS "trg_audit" ON "public"."orders"` {
		t.Fatalf("unexpected PG DROP TRIGGER preview (%v): %#v", err, pgDropTrig)
	}

	// 6. SQLite HEALTH_CHECK
	hcDrv := &mockSQLDriver{
		handler: func(_, _ string) ([]driver.QueryResult, error) {
			return []driver.QueryResult{{Rows: [][]any{{"ok"}}}}, nil
		},
	}
	hcOut, err := ExecuteObjectAction(ctx, "sqlite", hcDrv, driver.ObjectActionRequest{
		Action:     "HEALTH_CHECK",
		TargetName: "quick_check",
	})
	if err != nil || len(hcOut) != 1 || !strings.Contains(hcOut[0], "PRAGMA quick_check") {
		t.Fatalf("unexpected SQLite HEALTH_CHECK result (%v): %#v", err, hcOut)
	}
}

func TestClickHouseDefinerTargetAndDbLinkHardening(t *testing.T) {
	ctx := context.Background()
	drv := &mockSQLDriver{}

	// 1. Comment-obfuscated DEFINER/**/= and SQL SECURITY DEFINER blocked for non-admin Web users
	for _, tc := range []struct {
		eng        string
		badRoutine string
	}{
		{"mysql", "CREATE DEFINER/**/= 'root'@'%' EVENT ev_esc ON SCHEDULE EVERY 1 HOUR DO SELECT 1"},
		{"mysql", "CREATE /*!50017 DEFINER='root'@'localhost' */ EVENT ev_esc ON SCHEDULE EVERY 1 HOUR DO SELECT 1"},
		{"mariadb", "CREATE /*M!50017 DEFINER='root'@'localhost' */ EVENT ev_esc ON SCHEDULE EVERY 1 HOUR DO SELECT 1"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:   "shop",
			ObjectName: "ev_esc",
			ObjectType: "EVENT",
			Action:     "CREATE_OR_REPLACE",
			WebMode:    true,
			IsAdmin:    false,
			RoutineSQL: tc.badRoutine,
		}); err == nil {
			t.Fatalf("expected %s comment-obfuscated DEFINER %q to be blocked", tc.eng, tc.badRoutine)
		}
	}

	// 2. Arbitrary DDL target smuggling (e.g., CREATE USER inside EVENT / DICTIONARY / PACKAGE / MACRO) blocked
	for _, tc := range []struct {
		engine  string
		objType string
	}{
		{"mysql", "EVENT"},
		{"clickhouse", "DICTIONARY"},
		{"oracle", "PACKAGE"},
		{"postgres", "TYPE"},
		{"duckdb", "MACRO"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.engine, drv, driver.ObjectActionRequest{
			Database:    "app",
			ObjectName:  "rogue",
			ObjectType:  tc.objType,
			Action:      "CREATE_OR_REPLACE",
			PreviewOnly: true,
			RoutineSQL:  "CREATE USER rogue_admin IDENTIFIED BY 'Pwned123!'",
		}); err == nil {
			t.Fatalf("expected %s %s to reject mismatched CREATE USER raw DDL", tc.engine, tc.objType)
		}
	}

	// 3. ClickHouse external SOURCE(...) dictionary & external ENGINE = URL(...) matview blocked in restricted Web mode
	if _, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
		Database:    "default",
		ObjectName:  "dict_ssrf",
		ObjectType:  "DICTIONARY",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     true,
		IsAdmin:     false,
		AllowPrivIP: false,
		RoutineSQL:  "CREATE DICTIONARY dict_ssrf (id UInt64, val String) PRIMARY KEY id SOURCE/**/(HTTP(url 'http://169.254.169.254/latest/meta-data/' format 'TabSeparated')) LAYOUT(FLAT()) LIFETIME(60)",
	}); err == nil {
		t.Fatal("expected ClickHouse CREATE DICTIONARY with SOURCE/**/(HTTP(...)) to be rejected in restricted Web mode")
	}
	if _, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
		Database:    "default",
		ObjectName:  "dict_pg_ssrf",
		ObjectType:  "DICTIONARY",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     true,
		IsAdmin:     false,
		AllowPrivIP: false,
		RoutineSQL:  "CREATE DICTIONARY dict_pg_ssrf (id UInt64) PRIMARY KEY id SOURCE(POSTGRESQL(host '8.8.8.8' port 5432 user 'u' password 'p' db 'd' table 't')) LAYOUT(FLAT()) LIFETIME(60)",
	}); err == nil {
		t.Fatal("expected ClickHouse external dictionary SOURCE(...) to require admin in restricted Web mode")
	}
	if _, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
		Database:    "default",
		ObjectName:  "mv_ssrf",
		ObjectType:  "MATERIALIZED_VIEW",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     true,
		IsAdmin:     false,
		AllowPrivIP: false,
		Spec: &driver.ObjectSpec{
			EngineClause: "URL('http://169.254.169.254/', 'JSONEachRow')",
			SelectSQL:    "SELECT 1 AS x",
		},
	}); err == nil {
		t.Fatal("expected ClickHouse MATERIALIZED_VIEW with ENGINE = URL(...) to be rejected in restricted Web mode")
	}

	// 4. Oracle/Dameng DB_LINK Public & restricted Web mode require administrator + TNS IPC smuggling blocked
	if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName:  "PUB_LINK",
		ObjectType:  "DB_LINK",
		Action:      "CREATE_OR_REPLACE",
		IsAdmin:     false,
		AllowPrivIP: true,
		Spec: &driver.ObjectSpec{
			Public:     true,
			DbLinkHost: "10.0.0.12:1521/ORCL",
			DbLinkUser: "scott",
			DbLinkPass: "tiger",
		},
	}); err == nil {
		t.Fatal("expected PUBLIC DB_LINK creation to require IsAdmin")
	}
	if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName: "PUB_LINK",
		ObjectType: "DB_LINK",
		Action:     "DROP",
		IsAdmin:    false,
		Spec:       &driver.ObjectSpec{Public: true},
	}); err == nil {
		t.Fatal("expected PUBLIC DB_LINK DROP to require IsAdmin")
	}
	if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName:  "WEB_LINK",
		ObjectType:  "DB_LINK",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     true,
		IsAdmin:     false,
		AllowPrivIP: false,
		Spec: &driver.ObjectSpec{
			DbLinkHost: "8.8.8.8:1521/ORCL",
			DbLinkUser: "scott",
			DbLinkPass: "tiger",
		},
	}); err == nil {
		t.Fatal("expected DB_LINK in restricted Web mode to require administrator")
	}
	if _, err := ExecuteObjectAction(ctx, "oracle", drv, driver.ObjectActionRequest{
		ObjectName:  "IPC_LINK",
		ObjectType:  "DB_LINK",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     false,
		IsAdmin:     true,
		AllowPrivIP: false,
		Spec: &driver.ObjectSpec{
			DbLinkHost: "(DESCRIPTION=(ADDRESS=(PROTOCOL=IPC)(KEY=EXTPROC1))(CONNECT_DATA=(SID=PLSExtProc)))",
			DbLinkUser: "scott",
			DbLinkPass: "tiger",
		},
	}); err == nil {
		t.Fatal("expected TNS PROTOCOL=IPC smuggling to be blocked when AllowPrivIP=false")
	}

	// 5. Dialect-aware DEFINER & nested-comment / hash-comment regressions via ExecuteObjectAction
	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "v_safe_quote",
		ObjectType:  "VIEW",
		Action:      "CREATE_VIEW",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		ViewSQL:     "CREATE VIEW v_safe_quote AS SELECT 'it\\'s DEFINER = ok' AS note",
	}); err != nil {
		t.Fatalf("expected MySQL CREATE_VIEW with escaped quote before DEFINER = in string literal to pass, got %v", err)
	}
	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "p_evil_def",
		ObjectType:  "PROCEDURE",
		Action:      "CREATE_ROUTINE",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		RoutineSQL:  "CREATE PROCEDURE p_evil_def() COMMENT 'abc\\'' SQL SECURITY DEFINER BEGIN SELECT 1; END",
	}); err == nil {
		t.Fatal("expected MySQL CREATE_ROUTINE with SQL SECURITY DEFINER after escaped quote to be rejected")
	}
	if _, err := ExecuteObjectAction(ctx, "postgres", drv, driver.ObjectActionRequest{
		Schema:      "public",
		ObjectName:  "v_nested_def",
		ObjectType:  "VIEW",
		Action:      "CREATE_VIEW",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		ViewSQL:     "CREATE VIEW v_nested_def AS /* /* */ ' */ DEFINER = root SELECT 1",
	}); err == nil {
		t.Fatal("expected PostgreSQL CREATE_VIEW with nested comment DEFINER bypass to be rejected")
	}
	if _, err := ExecuteObjectAction(ctx, "duckdb", drv, driver.ObjectActionRequest{
		Schema:      "main",
		ObjectName:  "v_duck_bad",
		ObjectType:  "VIEW",
		Action:      "CREATE_VIEW",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		ViewSQL:     "SELECT #1 FROM (SELECT 42); CALL duckdb_secrets()",
	}); err == nil {
		t.Fatal("expected DuckDB CREATE_VIEW with #1 multi-statement CALL duckdb_secrets() to be rejected")
	}

	// 6. Iteration 4 regressions: DollarQuotes gating, MySQL /*M! & /*!99999 quote-swallowing, DuckDB PIVOT/UNPIVOT/FROM VALUES
	for _, badMySQLRoutine := range []string{
		"CREATE FUNCTION f_dollar_single($x$ INT) RETURNS INT DETERMINISTIC SQL SECURITY DEFINER RETURN $x$",
		"CREATE PROCEDURE p_dollar_single(IN $x$ INT) SQL SECURITY DEFINER SELECT $x$",
		"CREATE PROCEDURE p_maria_cmt() /*M! ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END",
		"CREATE PROCEDURE p_ver_cmt() /*!99999 ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END",
	} {
		if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  "p_iter4",
			ObjectType:  "PROCEDURE",
			Action:      "CREATE_ROUTINE",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			RoutineSQL:  badMySQLRoutine,
		}); err == nil {
			t.Fatalf("expected MySQL CREATE_ROUTINE to reject %q", badMySQLRoutine)
		}
	}
	if _, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
		Database:    "default",
		ObjectName:  "v_ch_dollar",
		ObjectType:  "VIEW",
		Action:      "CREATE_VIEW",
		WebMode:     true,
		IsAdmin:     false,
		AllowPrivIP: false,
		PreviewOnly: true,
		ViewSQL:     "CREATE VIEW v_ch_dollar AS SELECT 1 AS $x$, s3('http://169.254.169.254/') AS $x$",
	}); err == nil {
		t.Fatal("expected ClickHouse CREATE_VIEW with $x$ wrapper around s3() to be rejected in restricted Web mode")
	}
	for _, badDuckView := range []string{
		"PIVOT '/etc/passwd' ON col USING SUM(val)",
		"UNPIVOT '/etc/passwd' ON col",
		"SELECT * FROM VALUES (1) AS t(a), '/etc/passwd'",
	} {
		if _, err := ExecuteObjectAction(ctx, "duckdb", drv, driver.ObjectActionRequest{
			Schema:      "main",
			ObjectName:  "v_duck_pivot",
			ObjectType:  "VIEW",
			Action:      "CREATE_VIEW",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			ViewSQL:     badDuckView,
		}); err == nil {
			t.Fatalf("expected DuckDB CREATE_VIEW sandbox to reject %q", badDuckView)
		}
	}

	// 7. Iteration 5 regressions: '\*/' inside MySQL/MariaDB executable comments + MariaDB /*M!50003 DDL target/split parity
	for _, tc := range []struct {
		eng string
		sql string
	}{
		{"mysql", "CREATE PROCEDURE p9() SQL /*!50000 SECURITY */ /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql", "CREATE PROCEDURE p12() SQL /*!50000 SECURITY */ /*!99999 \" \\*/ DEFINER /* \" */ BEGIN SELECT 1; END"},
		{"mysql", "CREATE PROCEDURE p() /*!99999 '\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END"},
		{"mysql", "CREATE PROCEDURE p_odd() /*!99999 '\\\\\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END"},
		{"mariadb", "CREATE PROCEDURE p10() SQL /*M!50000 SECURITY */ /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  "p_iter5",
			ObjectType:  "PROCEDURE",
			Action:      "CREATE_ROUTINE",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			RoutineSQL:  tc.sql,
		}); err == nil {
			t.Fatalf("expected %s ExecuteObjectAction to reject DEFINER bypass %q", tc.eng, tc.sql)
		}
	}

	for _, tc := range []struct {
		objType string
		action  string
		viewSQL string
		routSQL string
	}{
		{"VIEW", "CREATE_VIEW", "/*M!50003 DROP TABLE items; */ CREATE VIEW v_maria_pwn AS SELECT 1", ""},
		{"VIEW", "CREATE_VIEW", "/*M!50003 DROP TABLE items -- */\nCREATE VIEW v_maria_pwn AS SELECT 1", ""},
		{"VIEW", "CREATE_VIEW", "CREATE /*M!50003 TABLE pwn(x INT) */ VIEW v_maria_pwn AS SELECT 1", ""},
		{"PROCEDURE", "CREATE_ROUTINE", "", "/*M!50003 DROP TABLE items; */ CREATE PROCEDURE p_maria_pwn() BEGIN SELECT 1; END"},
		{"TRIGGER", "CREATE_TRIGGER", "", "/*M!50003 DROP TABLE items; */ CREATE TRIGGER t_maria_pwn BEFORE INSERT ON items FOR EACH ROW SET @a=1"},
	} {
		if _, err := ExecuteObjectAction(ctx, "mariadb", drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  "maria_pwn",
			ObjectType:  tc.objType,
			Action:      tc.action,
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			ViewSQL:     tc.viewSQL,
			RoutineSQL:  tc.routSQL,
		}); err == nil {
			t.Fatalf("expected mariadb ExecuteObjectAction %s to reject /*M!50003 bypass", tc.action)
		}
	}

	// 8. Iteration 6 regressions: nested executable-comment DEFINER bypasses & benign /*!50000 'ok' */
	for _, tc := range []struct {
		name string
		eng  string
		sql  string
	}{
		{"mysql_nest_bs_1close", "mysql", "CREATE PROCEDURE p1() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_nest_bs_2close", "mysql", "CREATE PROCEDURE p2() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_nobs_1close", "mysql", "CREATE PROCEDURE p3() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_nest_nobs_2close", "mysql", "CREATE PROCEDURE p4() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_dbl_nobs", "mysql", "CREATE PROCEDURE p5() SQL /*!50000 SECURITY /*!99999 \" */ DEFINER /* \" */ */ BEGIN SELECT 1; END"},
		{"mysql_nest_bt_nobs", "mysql", "CREATE PROCEDURE p6() SQL /*!50000 SECURITY /*!99999 ` */ DEFINER /* ` */ */ BEGIN SELECT 1; END"},
		{"maria_nest_nobs_2close", "mariadb", "CREATE PROCEDURE pm1() SQL /*M!50000 SECURITY /*M!999999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END"},
		{"maria_nest_bs_1close", "mariadb", "CREATE PROCEDURE pm2() SQL /*M!50000 SECURITY /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END"},
		{"mysql_direct_unclosed", "mysql", "CREATE PROCEDURE pu() SQL /*!50000 SECURITY ' */ DEFINER /* ' */ BEGIN SELECT 1; END"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  tc.name,
			ObjectType:  "PROCEDURE",
			Action:      "CREATE_ROUTINE",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			RoutineSQL:  tc.sql,
		}); err == nil {
			t.Fatalf("%s (%s): expected ExecuteObjectAction to reject nested exec-comment DEFINER bypass", tc.name, tc.eng)
		}
	}

	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "p_benign_exec_ok",
		ObjectType:  "PROCEDURE",
		Action:      "CREATE_ROUTINE",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		RoutineSQL:  "CREATE PROCEDURE p_benign_exec_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END",
	}); err != nil {
		t.Fatalf("expected benign MySQL CREATE_ROUTINE with /*!50000 'ok' */ to pass, got %v", err)
	}

	// 9. Iteration 7 regressions: version-mismatched sequential/nested DEFINER + /*!99999 BEGIN */ multi-statement split bypasses
	for _, tc := range []struct {
		name    string
		eng     string
		objType string
		action  string
		sql     string
	}{
		{"ps1", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE ps1() /*!50000 SQL SECURITY */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ps3", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE ps3() /*M!50000 SQL SECURITY */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"p_50001", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_50001() /*!50000 SQL SECURITY */ /*!50001 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph1", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE ph1() /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph2", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE ph2() /*M!50000 SQL SECURITY /*M!999999 'a' */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"ph5", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE ph5() /*!50000 SQL SECURITY /* ' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"fh1", "mysql", "FUNCTION", "CREATE_ROUTINE", "CREATE FUNCTION fh1() RETURNS INT DETERMINISTIC /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER RETURN 1"},
		{"B1", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_adj1() SQL /*!50000 SECURITY */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"B5", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_adj4() SQL /*!50000 SECURITY /* x */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"B7", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE pm_adj2() SQL /*M!50000 SECURITY /* x */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END"},
		{"p_num", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_num() SQL /*!50000 SECURITY */ /*!99999 1 */ DEFINER BEGIN SELECT 1; END"},
		{"p_reset", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_reset() /*!50000 SQL SECURITY /*!99999 */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END"},
		{"split_proc_mysql", "mysql", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_b1() /*!99999 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"split_proc_maria", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "CREATE PROCEDURE p_b2() /*M!999999 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"split_trig_mysql", "mysql", "TRIGGER", "CREATE_TRIGGER", "CREATE TRIGGER t_b1 BEFORE INSERT ON items FOR EACH ROW /*!99999 BEGIN */ SET @a=1; DROP TABLE users;"},
		{"split_event_raw", "mysql", "EVENT", "CREATE_OR_REPLACE", "CREATE EVENT ev_b ON SCHEDULE EVERY 1 DAY DO /*!99999 BEGIN */ SELECT 1; DROP TABLE users;"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  tc.name,
			ObjectType:  tc.objType,
			Action:      tc.action,
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			RoutineSQL:  tc.sql,
		}); err == nil {
			t.Fatalf("%s (%s): expected ExecuteObjectAction to reject %q", tc.name, tc.eng, tc.sql)
		}
	}

	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "ev_spec_b",
		ObjectType:  "EVENT",
		Action:      "CREATE_OR_REPLACE",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		Spec: &driver.ObjectSpec{
			ScheduleKind: "EVERY",
			EveryValue:   1,
			EveryUnit:    "DAY",
			BodySQL:      "/*!99999 BEGIN */ SELECT 1; DROP TABLE users",
		},
	}); err == nil {
		t.Fatalf("expected EVENT spec BodySQL /*!99999 BEGIN */ multi-stmt bypass to be rejected")
	}

	if _, err := ExecuteObjectAction(ctx, "mariadb", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "v_maria_alg_ok",
		ObjectType:  "VIEW",
		Action:      "CREATE_VIEW",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		ViewSQL:     "CREATE /*M!50003 ALGORITHM=UNDEFINED */ VIEW v_maria_alg_ok AS SELECT 1",
	}); err != nil {
		t.Fatalf("expected benign MariaDB CREATE_VIEW with /*M!50003 ALGORITHM=UNDEFINED */ to pass, got %v", err)
	}

	// 10. Iteration 8 regressions: preserveExecQuotes clamping, 32+ dummy comments, MariaDB 5x4 cross-family
	var sb35 strings.Builder
	for i := 0; i < 35; i++ {
		fmt.Fprintf(&sb35, "/*!%d*/ ", 10001+i)
	}
	empty35 := sb35.String()

	var sbMariaCross strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sbMariaCross, "/*!%d*/ ", 10001+i)
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&sbMariaCross, "/*M!%d*/ ", 10001+i)
	}
	mariaCross := sbMariaCross.String()

	for _, tc := range []struct {
		name    string
		eng     string
		objType string
		action  string
		viewSQL string
		routSQL string
	}{
		{"pq_definer", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_pq() COMMENT /*!50000 ' */ ' */ /*!50000 SQL SECURITY */ /*!80000 x */ DEFINER BEGIN SELECT 1; END"},
		{"pq_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_pqs() COMMENT /*!50000 ' */ ' */ /*!80000 BEGIN */ SELECT 1; DROP TABLE users;"},
		{"cap35_definer", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c35() " + empty35 + " /*!50000 SQL SECURITY */ /*!99999 'x' */ DEFINER BEGIN SELECT 1; END"},
		{"cap35_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_c35s() " + empty35 + " /*!99999 BEGIN */ SELECT 1 /*!50000 ; DROP TABLE users */"},
		{"cap35_trig", "mysql", "TRIGGER", "CREATE_TRIGGER", "", "CREATE TRIGGER t_c35 BEFORE INSERT ON items FOR EACH ROW " + empty35 + " /*!99999 BEGIN */ SET @a=1 /*!50000 ; DROP TABLE users */"},
		{"cap35_view", "mysql", "VIEW", "CREATE_VIEW", "CREATE VIEW v_c35 AS SELECT " + empty35 + " 1 /*!50000 ; DROP TABLE users */", ""},
		{"maria_cross", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_cr() " + mariaCross + " /*M!100300 SQL SECURITY */ /*!80000 x */ /*!50000 DEFINER */ BEGIN SELECT 1; END"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  tc.name,
			ObjectType:  tc.objType,
			Action:      tc.action,
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			ViewSQL:     tc.viewSQL,
			RoutineSQL:  tc.routSQL,
		}); err == nil {
			t.Fatalf("%s (%s): expected ExecuteObjectAction to reject Iteration 8 payload", tc.name, tc.eng)
		}
	}

	if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
		Database:    "shop",
		ObjectName:  "p_benign_35_empty",
		ObjectType:  "PROCEDURE",
		Action:      "CREATE_ROUTINE",
		WebMode:     true,
		IsAdmin:     false,
		PreviewOnly: true,
		RoutineSQL:  "CREATE PROCEDURE p_benign_35_empty() " + empty35 + " COMMENT 'ok' BEGIN SELECT 1; END",
	}); err != nil {
		t.Fatalf("expected benign 35-empty-comment procedure to pass, got %v", err)
	}

	// 11. Iteration 9 regressions: MySQL/MariaDB '--<non-whitespace>' is not a comment
	for _, tc := range []struct {
		name    string
		eng     string
		objType string
		action  string
		viewSQL string
		routSQL string
	}{
		{"proc_dd1_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_dd1() BEGIN SELECT 1 --1; END; DROP TABLE users"},
		{"view_dd1_split", "mysql", "VIEW", "CREATE_VIEW", "CREATE VIEW v_dd1 AS SELECT 1 --1; DROP TABLE users", ""},
		{"maria_proc_dd1_split", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_dd1() BEGIN SELECT 1 --1; END; DROP TABLE users"},
		{"maria_view_dd1_split", "mariadb", "VIEW", "CREATE_VIEW", "CREATE VIEW vm_dd1 AS SELECT 1 --1; DROP TABLE users", ""},
		{"event_dd1_split", "mysql", "EVENT", "CREATE_OR_REPLACE", "", "CREATE EVENT ev_dd1 ON SCHEDULE EVERY 1 DAY DO BEGIN SELECT 1 --1; END; DROP TABLE users"},
		{"proc_dd1_definer", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_dd1_def() SELECT 1 --1 SQL SECURITY DEFINER\n BEGIN SELECT 1; END"},
		{"view_dddq_split", "mysql", "VIEW", "CREATE_VIEW", "CREATE VIEW v_dddq AS SELECT 1 --- '\n1; DROP TABLE users", ""},
		{"proc_ddd1_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_ddd1() BEGIN SELECT 1 --- '\n 1; END; DROP TABLE users"},
		{"proc_ddd_definer", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_ddd_def() SELECT 1 --- '\n 1 SQL SECURITY DEFINER BEGIN SELECT 1; END"},
		{"view_del_split", "mysql", "VIEW", "CREATE_VIEW", "CREATE VIEW v_del AS SELECT 1 --\x7f'\n1; DROP TABLE users", ""},
		{"proc_exd_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_exd() BEGIN SELECT 1 /*!50000 -- ' */ '\n */ ; END; DROP TABLE users"},
		{"proc_exh_split", "mysql", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE p_exh() BEGIN SELECT 1 /*!50000 # ' */ '\n */ ; END; DROP TABLE users"},
		{"maria_proc_exd_split", "mariadb", "PROCEDURE", "CREATE_ROUTINE", "", "CREATE PROCEDURE pm_exd() BEGIN SELECT 1 /*M!50000 -- ' */ '\n */ ; END; DROP TABLE users"},
	} {
		if _, err := ExecuteObjectAction(ctx, tc.eng, drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  tc.name,
			ObjectType:  tc.objType,
			Action:      tc.action,
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			ViewSQL:     tc.viewSQL,
			RoutineSQL:  tc.routSQL,
		}); err == nil {
			t.Fatalf("%s (%s): expected ExecuteObjectAction to reject bypass", tc.name, tc.eng)
		}
	}

	for _, tc := range []struct {
		name    string
		viewSQL string
	}{
		{"v_dd1_benign", "CREATE VIEW v_dd1_benign AS SELECT 1 --1"},
		{"v_ex_benign_nl", "CREATE VIEW v_ex_benign_nl AS /*!50000 -- */\nSELECT 1"},
		{"v_ex_benign_sp", "CREATE VIEW v_ex_benign_sp AS /*!50000 -- */ SELECT 1"},
	} {
		if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
			Database:    "shop",
			ObjectName:  tc.name,
			ObjectType:  "VIEW",
			Action:      "CREATE_VIEW",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			ViewSQL:     tc.viewSQL,
		}); err != nil {
			t.Fatalf("expected benign MySQL CREATE_VIEW %q to pass, got %v", tc.viewSQL, err)
		}
	}
}

func TestIter11ClickHouseMatViewBracketSmuggle(t *testing.T) {
	ctx := context.Background()
	drv := &mockSQLDriver{}

	for _, badEngineClause := range []string{
		"MergeTree() ORDER BY ['a]b'] ; DROP TABLE users -- '",
		"MergeTree() ORDER BY [1; DROP TABLE users; -- ]",
		"MergeTree() ORDER BY [1 /*]",
	} {
		if _, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
			Database:    "default",
			ObjectName:  "mv_pwn",
			ObjectType:  "MATERIALIZED_VIEW",
			Action:      "CREATE_OR_REPLACE",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			Spec: &driver.ObjectSpec{
				EngineClause: badEngineClause,
				SelectSQL:    "SELECT 1",
			},
		}); err == nil {
			t.Fatalf("expected ClickHouse MATERIALIZED_VIEW to reject bracket smuggle EngineClause %q", badEngineClause)
		}
	}

	for _, okEngineClause := range []string{
		"MergeTree() ORDER BY (id, ts)",
		"MergeTree() ORDER BY ['a', 'b']",
	} {
		sqls, err := ExecuteObjectAction(ctx, "clickhouse", drv, driver.ObjectActionRequest{
			Database:    "default",
			ObjectName:  "mv_ok",
			ObjectType:  "MATERIALIZED_VIEW",
			Action:      "CREATE_OR_REPLACE",
			WebMode:     true,
			IsAdmin:     false,
			PreviewOnly: true,
			Spec: &driver.ObjectSpec{
				EngineClause: okEngineClause,
				SelectSQL:    "SELECT 1",
			},
		})
		if err != nil || len(sqls) != 1 {
			t.Fatalf("expected ClickHouse MATERIALIZED_VIEW benign EngineClause %q to pass, got err=%v sqls=%#v", okEngineClause, err, sqls)
		}
	}
}
