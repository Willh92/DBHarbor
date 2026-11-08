package sqlbase

import (
	"errors"
	"fmt"
	"testing"
)

type mockPgErr struct {
	code string
	msg  string
}

func (e mockPgErr) Error() string    { return e.msg }
func (e mockPgErr) SQLState() string { return e.code }

type mockMSSQLErr struct {
	num int32
	msg string
}

func (e mockMSSQLErr) Error() string          { return e.msg }
func (e mockMSSQLErr) SQLErrorNumber() int32  { return e.num }

func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name   string
		dbType string
		err    error
		want   bool
	}{
		{"nil_error", "mysql", nil, false},
		{"sqlite_never_prompts", "sqlite", errors.New("Access denied for user"), false},
		{"duckdb_never_prompts", "duckdb", errors.New("password authentication failed"), false},

		// MySQL / MariaDB / TiDB / OceanBase
		{"mysql_1045", "mysql", errors.New("Error 1045 (28000): Access denied for user 'root'@'localhost' (using password: YES)"), true},
		{"mysql_1045_with_to_database", "mysql", errors.New("Error 1045 (28000): Access denied for user 'app'@'%' to database 'prod'"), true},
		{"mariadb_1698", "mariadb", errors.New("Error 1698 (28000): Access denied for user 'root'@'localhost'"), true},
		{"tidb_1045", "tidb", errors.New("Error 1045: Access denied for user 'admin'@'10.0.0.1' (using password: NO)"), true},
		{"oceanbase_1045", "oceanbase", errors.New("Error 1045 (28000): Access denied for user 'root@sys'@'127.0.0.1'"), true},
		{"mysql_1044_db_priv_denied", "mysql", errors.New("Error 1044 (42000): Access denied for user 'app'@'%' to database 'prod'"), false},
		{"mysql_1142_table_priv_denied", "mysql", errors.New("Error 1142 (42000): SELECT command denied to user 'app'@'%' for table 'users'"), false},
		{"mysql_network_refused", "mysql", errors.New("dial tcp 127.0.0.1:3306: connect: connection refused"), false},

		// PostgreSQL & KingbaseES
		{"postgres_typed_28p01", "postgres", fmt.Errorf("connect failed: %w", mockPgErr{code: "28P01", msg: "password authentication failed for user \"postgres\""}), true},
		{"kingbase_28000", "kingbase", errors.New("FATAL: role \"baduser\" does not exist (SQLSTATE 28000)"), true},
		{"postgres_typed_42501_priv", "postgres", mockPgErr{code: "42501", msg: "permission denied for table accounts"}, false},
		{"postgres_string_42501_priv", "postgres", errors.New("ERROR: permission denied for table secrets (SQLSTATE 42501)"), false},

		// ClickHouse
		{"clickhouse_516", "clickhouse", errors.New("code: 516, message: default: Authentication failed: password is incorrect, or there is no user with such name."), true},
		{"clickhouse_192", "clickhouse", errors.New("code: 192, message: Unknown user ghost"), true},
		{"clickhouse_http_401", "clickhouse", errors.New("clickhouse [execute]:: 401 Unauthorized"), true},
		{"clickhouse_http_401_code", "clickhouse", errors.New("clickhouse [execute]:: 401 code: Authentication failed"), true},
		{"clickhouse_http_status_401", "clickhouse", errors.New("unexpected http status code: 401"), true},
		{"clickhouse_497_priv", "clickhouse", errors.New("code: 497, message: app_ro: Not enough privileges. To execute this query, it's necessary to have the grant SELECT"), false},

		// SQL Server
		{"sqlserver_typed_18456", "sqlserver", fmt.Errorf("ping: %w", mockMSSQLErr{num: 18456, msg: "mssql: Login failed for user 'sa'."}), true},
		{"sqlserver_typed_229_priv", "sqlserver", mockMSSQLErr{num: 229, msg: "mssql: The SELECT permission was denied on the object 'Payroll'."}, false},

		// Oracle
		{"oracle_01017", "oracle", errors.New("ORA-01017: invalid username/password; logon denied"), true},
		{"oracle_01005", "oracle", errors.New("ORA-01005: null password given; logon denied"), true},
		{"oracle_28000_locked", "oracle", errors.New("ORA-28000: The account is locked."), true},
		{"oracle_28001_expired", "oracle", errors.New("ORA-28001: the password has expired"), true},
		{"oracle_01031_priv", "oracle", errors.New("ORA-01031: insufficient privileges"), false},

		// Dameng (DM8)
		{"dameng_neg2501", "dameng", errors.New("[Error -2501]: 用户名或密码错误"), true},
		{"dameng_en_invalid", "dameng", errors.New("Error 2501: Invalid username or password"), true},
		{"dameng_priv", "dameng", errors.New("Error -5504: 没有查询权限"), false},

		// Redis
		{"redis_wrongpass", "redis", errors.New("WRONGPASS invalid username-password pair or user is disabled."), true},
		{"redis_noauth", "redis", errors.New("NOAUTH Authentication required."), true},
		{"redis_noperm_acl", "redis", errors.New("NOPERM this user has no permissions to run the 'flushall' command"), false},

		// MongoDB
		{"mongodb_auth_failed", "mongodb", errors.New("connection() error occurred during connection handshake: auth error: sasl conversation error: unable to authenticate using mechanism \"SCRAM-SHA-256\": (AuthenticationFailed) Authentication failed."), true},
		{"mongodb_requires_auth", "mongodb", errors.New("(Unauthorized) Command listDatabases requires authentication"), true},
		{"mongodb_collection_rbac", "mongodb", errors.New("(Unauthorized) not authorized on prod to execute command { find: \"audit_logs\" }"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAuthError(tc.dbType, tc.err)
			if got != tc.want {
				t.Fatalf("IsAuthError(%q, %v) = %v, want %v", tc.dbType, tc.err, got, tc.want)
			}
		})
	}
}
