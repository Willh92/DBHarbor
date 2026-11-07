package sqlmeta

import (
	"context"
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
	for _, badRoutine := range []string{
		"CREATE DEFINER/**/= 'root'@'%' EVENT ev_esc ON SCHEDULE EVERY 1 HOUR DO SELECT 1",
		"CREATE /*M!50017 DEFINER='root'@'localhost' */ EVENT ev_esc ON SCHEDULE EVERY 1 HOUR DO SELECT 1",
	} {
		if _, err := ExecuteObjectAction(ctx, "mysql", drv, driver.ObjectActionRequest{
			Database:   "shop",
			ObjectName: "ev_esc",
			ObjectType: "EVENT",
			Action:     "CREATE_OR_REPLACE",
			WebMode:    true,
			IsAdmin:    false,
			RoutineSQL: badRoutine,
		}); err == nil {
			t.Fatalf("expected comment-obfuscated DEFINER %q to be blocked", badRoutine)
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
}
