package dbprivs

import (
	"strings"
	"testing"

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
