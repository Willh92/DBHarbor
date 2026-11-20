package dbprivs

import (
	"context"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/version"
)

func TestBuildStatementsAcrossEngines(t *testing.T) {
	// 1. MySQL / MariaDB CREATE USER + GRANT
	mysqlStmts, err := BuildStatements("mariadb", ActionRequest{
		Action:          "CREATE_USER",
		Username:        "app_reader",
		Host:            "10.0.%",
		Password:        "S3cr'et!",
		ScopeType:       "database",
		Database:        "orders_db",
		Privileges:      []string{"SELECT", "SHOW VIEW"},
		WithGrantOption: true,
	})
	if err != nil {
		t.Fatalf("mariadb CREATE_USER failed: %v", err)
	}
	if len(mysqlStmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %v", len(mysqlStmts), mysqlStmts)
	}
	if !strings.Contains(mysqlStmts[0], "CREATE USER 'app_reader'@'10.0.%' IDENTIFIED BY 'S3cr''et!'") {
		t.Fatalf("unexpected CREATE USER SQL: %s", mysqlStmts[0])
	}
	if !strings.Contains(mysqlStmts[1], "GRANT SELECT, SHOW VIEW ON `orders_db`.* TO 'app_reader'@'10.0.%' WITH GRANT OPTION") {
		t.Fatalf("unexpected GRANT SQL: %s", mysqlStmts[1])
	}

	// 2. PostgreSQL GRANT table + schema + role
	pgStmts, err := BuildStatements("postgres", ActionRequest{
		Action:     "GRANT",
		Username:   "analytics_user",
		Database:   "warehouse",
		Schema:     "public",
		Table:      "events",
		ScopeType:  "table",
		Privileges: []string{"SELECT", "USAGE", "CONNECT"},
		Roles:      []string{"pg_monitor"},
	})
	if err != nil {
		t.Fatalf("postgres GRANT failed: %v", err)
	}
	if len(pgStmts) != 4 {
		t.Fatalf("expected 4 postgres GRANT statements, got %d: %v", len(pgStmts), pgStmts)
	}

	// 3. Redis ACL SETUSER & default user protection
	redisStmts, err := BuildStatements("redis", ActionRequest{
		Action:    "CREATE_USER",
		Username:  "cache_ro",
		Password:  "RedisPass99",
		ACLPreset: "readonly",
	})
	if err != nil || len(redisStmts) != 1 || !strings.Contains(redisStmts[0], "ACL SETUSER cache_ro on >RedisPass99 ~* +@read") {
		t.Fatalf("unexpected redis ACL statement: %v, err=%v", redisStmts, err)
	}
	if _, err := BuildStatements("redis", ActionRequest{Action: "DROP_USER", Username: "default"}); err == nil {
		t.Fatal("expected dropping Redis default user to be rejected")
	}

	// 4. MongoDB role grant command
	mongoStmts, err := BuildStatements("mongodb", ActionRequest{
		Action:   "GRANT",
		Username: "report_svc",
		Database: "analytics",
		Roles:    []string{"read", "readWrite@staging"},
	})
	if err != nil || len(mongoStmts) != 1 || !strings.Contains(mongoStmts[0], `"grantRolesToUser":"report_svc"`) {
		t.Fatalf("unexpected mongodb statement: %v, err=%v", mongoStmts, err)
	}

	// 5. Reject SQL injection tokens in privilege names
	if _, err := BuildStatements("mysql", ActionRequest{
		Action:     "GRANT",
		Username:   "u1",
		ScopeType:  "global",
		Privileges: []string{"SELECT; DROP USER root"},
	}); err == nil {
		t.Fatal("expected invalid privilege token to be rejected")
	}
}

func TestEmbeddedLocalizedChangelogs(t *testing.T) {
	m := version.EmbeddedChangelogsMap()
	for _, loc := range []string{"en-US", "zh-CN", "zh-TW", "ja-JP"} {
		body := strings.TrimSpace(m[loc])
		if len(body) < 100 {
			t.Fatalf("expected non-empty embedded changelog for %s, got len=%d", loc, len(body))
		}
	}
	if m["en-US"] == m["zh-CN"] || m["zh-CN"] == m["ja-JP"] {
		t.Fatal("expected localized embedded changelogs to have distinct language contents")
	}
}

type mockRedisKVDriver struct {
	capturedArgs [][]any
	listOutput   string
}

func (m *mockRedisKVDriver) Meta() driver.DriverMeta                        { return driver.DriverMeta{Type: "redis"} }
func (m *mockRedisKVDriver) Ping(context.Context) (driver.ServerInfo, error) { return driver.ServerInfo{}, nil }
func (m *mockRedisKVDriver) Close() error                                   { return nil }
func (m *mockRedisKVDriver) ListDatabases(context.Context) ([]driver.DatabaseSummary, error) {
	return nil, nil
}
func (m *mockRedisKVDriver) CreateDatabase(context.Context, string, string) error { return nil }
func (m *mockRedisKVDriver) ScanKeys(context.Context, int, string, uint64, int64) (driver.KVScanResult, error) {
	return driver.KVScanResult{}, nil
}
func (m *mockRedisKVDriver) GetKey(context.Context, int, string) (driver.KVEntryDetail, error) {
	return driver.KVEntryDetail{}, nil
}
func (m *mockRedisKVDriver) SetKey(context.Context, int, driver.KVSetRequest) error { return nil }
func (m *mockRedisKVDriver) MutateCollectionItem(context.Context, int, driver.KVItemMutationRequest) error {
	return nil
}
func (m *mockRedisKVDriver) DeleteKeys(context.Context, int, []string) (int64, error) { return 0, nil }
func (m *mockRedisKVDriver) SetTTL(context.Context, int, string, int64) error          { return nil }
func (m *mockRedisKVDriver) ExecuteCommand(context.Context, int, string) (driver.KVCommandResult, error) {
	return driver.KVCommandResult{}, nil
}
func (m *mockRedisKVDriver) ExecuteCommandArgs(_ context.Context, _ int, args ...any) (driver.KVCommandResult, error) {
	cp := append([]any(nil), args...)
	m.capturedArgs = append(m.capturedArgs, cp)
	return driver.KVCommandResult{Output: m.listOutput}, nil
}

func TestRedisACLStructuredArgsPreservesSpecialPasswordChars(t *testing.T) {
	specialPass := `p@"s's\w#o r d!`
	req := ActionRequest{
		Action:    "CREATE_USER",
		Username:  "app_acl",
		Password:  specialPass,
		ACLPreset: "readonly",
	}

	// BuildRedactedStatements must mask password
	redacted, err := BuildRedactedStatements("redis", req)
	if err != nil || len(redacted) != 1 || !strings.Contains(redacted[0], ">********") {
		t.Fatalf("unexpected redacted Redis ACL statement: %v, err=%v", redacted, err)
	}
	if strings.Contains(redacted[0], specialPass) {
		t.Fatalf("redacted statement leaked password: %s", redacted[0])
	}

	// ExecuteAction must pass discrete RESP args verbatim without splitCommandArgs corruption
	mockKV := &mockRedisKVDriver{}
	stmts, err := ExecuteAction(context.Background(), "redis", mockKV, req)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("ExecuteAction failed: err=%v stmts=%v", err, stmts)
	}
	if len(mockKV.capturedArgs) != 1 {
		t.Fatalf("expected 1 ExecuteCommandArgs call, got %d", len(mockKV.capturedArgs))
	}
	gotArgs := mockKV.capturedArgs[0]
	wantPassArg := ">" + specialPass
	if len(gotArgs) < 6 || gotArgs[0] != "ACL" || gotArgs[1] != "SETUSER" || gotArgs[2] != "app_acl" || gotArgs[3] != "on" || gotArgs[4] != wantPassArg {
		t.Fatalf("unexpected RESP args sent to ExecuteCommandArgs: %#v", gotArgs)
	}

	// Reject control characters in password
	for _, bad := range []string{"bad\rpass", "bad\npass", "bad\x00pass"} {
		if _, err := BuildStatements("redis", ActionRequest{
			Action:   "ALTER_USER",
			Username: "app_acl",
			Password: bad,
		}); err == nil {
			t.Fatalf("expected control char password %q to be rejected", bad)
		}
	}
}

func TestRedisACLRulesAndOraclePasswordInjectionGuards(t *testing.T) {
	for _, badRule := range []string{">leakedpass ~* +@read", "<oldpass", "nopass ~*", "resetpass", "#deadbeef"} {
		req := ActionRequest{
			Action:    "ALTER_USER",
			Username:  "svc_user",
			ACLPreset: "custom",
			ACLRules:  badRule,
		}
		if _, err := BuildStatements("redis", req); err == nil {
			t.Fatalf("expected BuildStatements to reject Redis ACLRules %q", badRule)
		}
		if _, err := BuildRedactedStatements("redis", req); err == nil {
			t.Fatalf("expected BuildRedactedStatements to reject Redis ACLRules %q", badRule)
		}
	}

	for _, engine := range []string{"oracle", "dameng"} {
		for _, badPass := range []string{`bad"quote`, "bad\nline", "bad\rret", "bad\x00nul"} {
			if _, err := BuildStatements(engine, ActionRequest{
				Action:   "CREATE_USER",
				Username: "SEC_USER",
				Password: badPass,
			}); err == nil {
				t.Fatalf("expected %s CREATE_USER to reject password %q", engine, badPass)
			}
		}
	}
}

