package sqlbase

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"dbharbor/backend/internal/driver"

	_ "modernc.org/sqlite"
)

func TestSplitStatements(t *testing.T) {
	script := `
-- Leading comment
SELECT 'a;b' AS txt, "c;d" AS col FROM users;
/* multi-line;
   comment; */
INSERT INTO logs(msg) VALUES ('hello; world');
CREATE FUNCTION test() RETURNS void AS $$
BEGIN
  PERFORM 1;
END;
$$ LANGUAGE plpgsql;
`
	stmts := SplitStatementsForEngine("postgres", script)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d: %#v", len(stmts), stmts)
	}

	// Verify compound BEGIN ... END blocks in CREATE TRIGGER / CREATE PROCEDURE vs standalone transaction BEGIN;
	compoundScript := `
BEGIN;
CREATE TABLE items (id INTEGER PRIMARY KEY, val INTEGER);
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, item_id INTEGER);
CREATE TRIGGER trg_items_audit AFTER INSERT ON items
BEGIN
  INSERT INTO audit_log(item_id) VALUES (CASE WHEN NEW.id > 0 THEN NEW.id ELSE 0 END);
  UPDATE items SET val = val + 1 WHERE id = NEW.id;
END;
COMMIT;
CREATE DEFINER='root'@'%' PROCEDURE p_demo()
BEGIN
  IF 1 = 1 THEN
    INSERT INTO items VALUES (1, 10);
  END IF;
END;
`
	cStmts := SplitStatements(compoundScript)
	if len(cStmts) != 6 {
		t.Fatalf("expected 6 statements for compound trigger/proc script, got %d: %#v", len(cStmts), cStmts)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(cStmts[i]); err != nil {
			t.Fatalf("sqlite exec stmt[%d] (%s) failed: %v", i, cStmts[i], err)
		}
	}
}

func TestNormalizeLargeIntPrecision(t *testing.T) {
	var snowflakeID int64 = 1854123456789012345
	got := NormalizeValue(snowflakeID, "BIGINT")
	str, ok := got.(string)
	if !ok || str != "1854123456789012345" {
		t.Fatalf("expected exact string for unsafe JS int64, got %T (%v)", got, got)
	}

	var small int64 = 42
	if v, ok := NormalizeValue(small, "INT").(int64); !ok || v != 42 {
		t.Fatalf("expected int64(42), got %v", NormalizeValue(small, "INT"))
	}
}

func TestBuildCreateTableClickHouse(t *testing.T) {
	ddl, err := BuildCreateTableDDL(DialectClickHouse, driver.CreateTableRequest{
		Database: "analytics",
		Table:    "events",
		Columns: []driver.ColumnDef{
			{Name: "event_id", Type: "UInt64", IsPrimaryKey: true},
			{Name: "user_name", Type: "String", Nullable: true},
			{Name: "created_at", Type: "DateTime"},
		},
		Engine:      "ReplacingMergeTree",
		OrderBy:     "(event_id, created_at)",
		PartitionBy: "toYYYYMM(created_at)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ddl == "" {
		t.Fatal("expected non-empty ClickHouse DDL")
	}
}

func TestBuildAlterAndObjectActionSQLsAcrossDialects(t *testing.T) {
	// MySQL CHANGE COLUMN (rename + modify)
	mysqlSQLs, err := BuildAlterTableSQLs(DialectMySQL, driver.AlterTableRequest{
		Database: "shop",
		Table:    "orders",
		Actions: []driver.AlterAction{
			{
				Action:      "MODIFY_COLUMN",
				OldName:     "amt",
				Column:      driver.ColumnDef{Name: "total_amount", Type: "DECIMAL(18,2)", Nullable: false, DefaultValue: "0.00", Comment: "Order total"},
				AfterColumn: "id",
			},
			{
				Action:     "ADD_FOREIGN_KEY",
				ForeignKey: driver.ForeignKeyDef{Name: "fk_ord_user", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
			},
		},
	})
	if err != nil || len(mysqlSQLs) != 2 {
		t.Fatalf("mysql alter build failed: %v %#v", err, mysqlSQLs)
	}
	if !strings.Contains(mysqlSQLs[0], "CHANGE COLUMN `amt` `total_amount` DECIMAL(18,2) NOT NULL DEFAULT 0.00") {
		t.Fatalf("unexpected mysql change column sql: %s", mysqlSQLs[0])
	}

	// Postgres MODIFY_COLUMN (rename + type + nullability + default + comment)
	pgSQLs, err := BuildAlterTableSQLs(DialectPostgres, driver.AlterTableRequest{
		Schema: "public",
		Table:  "orders",
		Actions: []driver.AlterAction{
			{
				Action:  "MODIFY_COLUMN",
				OldName: "amt",
				Column:  driver.ColumnDef{Name: "total_amount", Type: "NUMERIC(18,2)", Nullable: false, DefaultValue: "0", Comment: "Order total"},
			},
		},
	})
	if err != nil || len(pgSQLs) != 5 {
		t.Fatalf("pg alter build expected 5 statements, got %d (%v): %#v", len(pgSQLs), err, pgSQLs)
	}

	// Object actions: COPY_DATA, CREATE_VIEW, RENAME
	copySQLs, err := BuildObjectActionSQLs(DialectMySQL, driver.ObjectActionRequest{
		Database: "shop", ObjectName: "orders", Action: "COPY_DATA", TargetName: "orders_bak",
	})
	if err != nil || len(copySQLs) != 2 {
		t.Fatalf("mysql copy_data failed: %v %#v", err, copySQLs)
	}
}

func TestValidateReadOnlyScriptExpandedGuards(t *testing.T) {
	blocked := []string{
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT PG_TERMINATE_BACKEND(1234)",
		"SELECT dblink('host=127.0.0.1', 'SELECT 1')",
		"SELECT * FROM url('http://169.254.169.254/latest/meta-data/', 'CSV', 'a String')",
		"SELECT * FROM remoteSecure('127.0.0.1:9440', 'system', 'one')",
		"SELECT * FROM s3('https://bucket.s3.amazonaws.com/key.parquet')",
		"SELECT * FROM OPENROWSET('SQLNCLI', 'Server=127.0.0.1;Trusted_Connection=yes;', 'SELECT 1')",
		"SELECT * FROM sys.fn_xe_file_target_read_file('*.xel', null, null, null)",
		"SELECT UTL_HTTP.REQUEST('http://169.254.169.254') FROM dual",
		"SELECT SYS.UTL_FILE.FOPEN('/tmp', 'a.txt', 'r') FROM dual",
		"SELECT HTTPURITYPE('http://127.0.0.1').GETCLOB() FROM dual",
	}
	for _, q := range blocked {
		if err := ValidateReadOnlyScript(q); err == nil {
			t.Errorf("expected read-only guard to reject %q, but it passed", q)
		}
	}

	allowed := []string{
		"SELECT id, url, file, input FROM assets WHERE url LIKE 'https://%'",
		"SELECT COUNT(*) FROM events",
	}
	for _, q := range allowed {
		if err := ValidateReadOnlyScript(q); err != nil {
			t.Errorf("expected read-only guard to allow %q, got error: %v", q, err)
		}
	}
}

func TestSQLMetaColumnTypesGeneratedAndChecks(t *testing.T) {
	if !ContainsUnquotedSQLMeta("1 = 1 # mysql comment") {
		t.Fatal("expected unquoted # to be rejected")
	}
	if ContainsUnquotedSQLMeta("[col#1] = '#literal'") {
		t.Fatal("expected quoted # inside [...] and '...' to be allowed")
	}
	if !ContainsUnquotedSQLMeta("[unclosed_bracket = 1") {
		t.Fatal("expected unclosed bracket quote to be rejected")
	}

	if err := ValidateColumnType("VARCHAR(255) --evil"); err == nil {
		t.Fatal("expected comment in column type to be rejected")
	}
	if err := ValidateColumnType("INT); DROP TABLE x; --"); err == nil {
		t.Fatal("expected semicolon/unbalanced paren in column type to be rejected")
	}
	if err := ValidateColumnType("NUMERIC(18, 4)"); err != nil {
		t.Fatalf("expected NUMERIC(18, 4) to be valid: %v", err)
	}

	// Generated columns & CHECK constraints across MySQL, SQL Server, and SQLite
	mysqlDDL, err := BuildCreateTableDDL(DialectMySQL, driver.CreateTableRequest{
		Database: "shop",
		Table:    "items",
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "BIGINT", IsPrimaryKey: true, IsAutoIncrement: true},
			{Name: "price", Type: "DECIMAL(10,2)", Nullable: false},
			{Name: "qty", Type: "INT", Nullable: false},
			{Name: "total", Type: "DECIMAL(12,2)", Nullable: false, GeneratedExpr: "price * qty", GeneratedStorage: "STORED"},
		},
		CheckConstraints: []driver.CheckConstraintDef{
			{Name: "chk_qty_pos", Expression: "qty >= 0"},
		},
	})
	if err != nil {
		t.Fatalf("mysql create table with generated+check failed: %v", err)
	}
	if !strings.Contains(mysqlDDL, "GENERATED ALWAYS AS (price * qty) STORED") || !strings.Contains(mysqlDDL, "CONSTRAINT `chk_qty_pos` CHECK (qty >= 0)") {
		t.Fatalf("unexpected mysql DDL:\n%s", mysqlDDL)
	}

	mssqlDDL, err := BuildCreateTableDDL(DialectSQLServer, driver.CreateTableRequest{
		Schema: "dbo",
		Table:  "items",
		Columns: []driver.ColumnDef{
			{Name: "a", Type: "INT", Nullable: false},
			{Name: "b", Type: "INT", Nullable: false},
			{Name: "sum_ab", GeneratedExpr: "[a] + [b]", GeneratedStorage: "PERSISTED"},
		},
	})
	if err != nil || !strings.Contains(mssqlDDL, "[sum_ab] AS ([a] + [b]) PERSISTED") {
		t.Fatalf("unexpected mssql computed DDL (%v):\n%s", err, mssqlDDL)
	}

	// MySQL DROP CHECK vs MariaDB DROP CONSTRAINT
	dropCheckReq := driver.AlterTableRequest{
		Database: "shop",
		Table:    "items",
		Actions: []driver.AlterAction{
			{Action: "ADD_CHECK", Check: driver.CheckConstraintDef{Name: "chk_p", Expression: "price > 0"}},
			{Action: "DROP_CHECK", Check: driver.CheckConstraintDef{Name: "chk_p"}},
		},
	}
	myAlters, err := BuildAlterTableSQLsForEngine("mysql", DialectMySQL, dropCheckReq)
	if err != nil || len(myAlters) != 2 || !strings.Contains(myAlters[1], "DROP CHECK `chk_p`") {
		t.Fatalf("unexpected mysql drop check: %v %#v", err, myAlters)
	}
	mariaAlters, err := BuildAlterTableSQLsForEngine("mariadb", DialectMySQL, dropCheckReq)
	if err != nil || len(mariaAlters) != 2 || !strings.Contains(mariaAlters[1], "DROP CONSTRAINT `chk_p`") {
		t.Fatalf("unexpected mariadb drop constraint: %v %#v", err, mariaAlters)
	}

	// CREATE_VIEW must reject multi-statement payloads
	if _, err := BuildObjectActionSQLs(DialectPostgres, driver.ObjectActionRequest{
		Schema: "public", ObjectName: "v_bad", ObjectType: "VIEW", Action: "CREATE_VIEW",
		ViewSQL: "SELECT 1; DROP TABLE users",
	}); err == nil {
		t.Fatal("expected multi-statement CREATE_VIEW to fail")
	}

	// Package splitter test for Oracle PL/SQL
	pkgScript := `
CREATE OR REPLACE PACKAGE pkg_math AS
  FUNCTION add_num(a NUMBER, b NUMBER) RETURN NUMBER;
  PROCEDURE reset_state;
END pkg_math;
`
	if got := SplitStatements(pkgScript); len(got) != 1 {
		t.Fatalf("expected Oracle PACKAGE spec to stay 1 statement, got %d: %#v", len(got), got)
	}
}

func TestMariaDBCommentsReadOnlyFunctionsDDLAndHostURIHardening(t *testing.T) {
	// 1. MariaDB executable comments (/*M!...*/ and /*m!...*/) + expanded read-only functions
	blockedReadOnly := []string{
		"/*M! DELETE FROM users */",
		"/*M!100100 DROP TABLE audit_logs */",
		"/*m!100305 UPDATE accounts SET is_admin = 1 */",
		"SELECT 1 /*M! UNION SELECT load_file('/etc/passwd') */",
		"SELECT * FROM remoteSecure('127.0.0.1:9440', 'default', 'secrets')",
		"SELECT * FROM urlCluster('cluster_a', 'http://169.254.169.254/', 'CSV', 'x String')",
		"SELECT * FROM s3Cluster('cluster_a', 'https://bucket/key.parquet')",
		"SELECT * FROM hdfsCluster('cluster_a', 'hdfs://nn:9000/data')",
		"SELECT * FROM file('/etc/passwd', 'LineAsString')",
		"SELECT * FROM fileCluster('cluster_a', '/etc/passwd', 'TSV')",
		"SELECT * FROM mongodb('127.0.0.1:27017', 'admin', 'users', 'u', 'p')",
		"SELECT * FROM redis('127.0.0.1:6379', 'key', 'val String')",
		"SELECT * FROM sqlite('/var/lib/dbharbor/dbharbor.db', 'users')",
		"SELECT * FROM executable('id', 'TabSeparated', 'out String')",
		"SELECT * FROM executable_pool('id', 'TabSeparated', 'out String')",
		"SELECT * FROM azureBlobStorage('DefaultEndpointsProtocol=https;...', 'c', 'b')",
	}
	for _, q := range blockedReadOnly {
		eng := ""
		if strings.Contains(q, "/*M!") || strings.Contains(q, "/*m!") {
			eng = "mariadb"
		}
		if err := ValidateReadOnlyScriptForEngine(eng, q); err == nil {
			t.Fatalf("expected ValidateReadOnlyScriptForEngine(%q) to block %q", eng, q)
		}
	}
	if stmts := SplitStatementsForEngine("mariadb", "/*M!100100 SELECT 1 */; /*m! SELECT 2 */;"); len(stmts) != 2 {
		t.Fatalf("expected MariaDB executable comments to count as 2 statements, got %d: %#v", len(stmts), stmts)
	}

	// 2. MySQL '#' line comment handling in IsQueryStatement / IsDDLStatement
	if !IsQueryStatement("# MySQL hash comment\nSELECT 1") {
		t.Fatal("expected IsQueryStatement to skip leading '#' line comment")
	}
	if !IsDDLStatement("# leading hash comment\nCREATE TABLE demo (id INT)") {
		t.Fatal("expected IsDDLStatement to skip leading '#' line comment")
	}

	// 3. Lexical DEFINER & expected DDL target guards
	for _, defDDL := range []string{
		"CREATE DEFINER='root'@'%' VIEW v1 AS SELECT 1",
		"CREATE DEFINER/*comment*/= 'root'@'%' VIEW v1 AS SELECT 1",
		"CREATE /*M!50013 DEFINER='root'@'localhost' */ SQL SECURITY DEFINER VIEW v1 AS SELECT 1",
		"CREATE SQL SECURITY DEFINER VIEW v1 AS SELECT 1",
	} {
		if !HasSQLDefinerClause(defDDL) {
			t.Fatalf("expected HasSQLDefinerClause to catch %q", defDDL)
		}
	}
	if HasSQLDefinerClause("CREATE VIEW v_ok AS SELECT 'DEFINER = root' AS label") {
		t.Fatal("expected string literal containing DEFINER to be allowed")
	}
	if err := ValidateExpectedDDLTarget("CREATE USER attacker IDENTIFIED BY 'Secret123!'", []string{"CREATE", "ALTER", "REPLACE"}, "VIEW"); err == nil {
		t.Fatal("expected ValidateExpectedDDLTarget to reject CREATE USER when VIEW is expected")
	}
	if err := ValidateExpectedDDLTarget("CREATE OR REPLACE VIEW v_ok AS SELECT 1", []string{"CREATE", "ALTER", "REPLACE"}, "VIEW"); err != nil {
		t.Fatalf("expected valid CREATE OR REPLACE VIEW target to pass, got %v", err)
	}

	// 4. ClickHouse external ENGINE and table-function guards
	for _, badEngine := range []string{
		"URL('http://169.254.169.254/latest/meta-data/', 'CSV')",
		"/*sneaky*/ URL('http://127.0.0.1:8080', 'JSONEachRow')",
		"MySQL('127.0.0.1:3306', 'db', 'tbl', 'root', 'pass')",
		"S3('https://s3.amazonaws.com/bucket/file.parquet', 'Parquet')",
		"Executable('sh -c id', 'TabSeparated')",
		"SQLite('/var/lib/dbharbor/dbharbor.db', 'users')",
	} {
		if err := ValidateClickHouseWebEngineClause(badEngine); err == nil {
			t.Fatalf("expected ValidateClickHouseWebEngineClause to reject %q", badEngine)
		}
	}
	if err := ValidateClickHouseWebEngineClause("ReplacingMergeTree(updated_at)"); err != nil {
		t.Fatalf("expected ReplacingMergeTree to be allowed, got %v", err)
	}
	for _, badCHSQL := range []string{
		"CREATE TABLE t_ext (x String) ENGINE = /*comment*/ URL('http://169.254.169.254/', 'CSV')",
		"SELECT * FROM url/**/( 'http://169.254.169.254/', 'LineAsString', 's String' )",
		"CREATE DICTIONARY d_ext (id UInt64) PRIMARY KEY id SOURCE/**/(HTTP(url 'http://127.0.0.1/')) LAYOUT(FLAT()) LIFETIME(60)",
	} {
		if err := ValidateClickHouseRestrictedWebSQL(badCHSQL); err == nil {
			t.Fatalf("expected ValidateClickHouseRestrictedWebSQL to reject %q", badCHSQL)
		}
	}

	// 5. Strict hostname & MongoDB restricted Web URI guards
	for _, badHost := range []string{
		"db.example.com/evil?param=1",
		"db.example.com?allowLoadLocalInfile=true",
		"db.example.com#frag",
		"user@db.example.com",
		`db.example.com\pipe`,
		"db.example.com:3306",
		"/tmp/mongodb-27017.sock",
		"db\x00.example.com",
	} {
		if _, err := ValidateStrictHostname(badHost); err == nil {
			t.Fatalf("expected ValidateStrictHostname to reject %q", badHost)
		}
	}
	for _, badMongoURI := range []string{
		"mongodb://db.example.com:27017/admin?tlsCAFile=/etc/passwd",
		"mongodb://db.example.com:27017/admin?TLSCertificateKeyFile=/etc/shadow",
		"mongodb://db.example.com:27017/admin?replicaSet=rs0;sslClientCertificateKeyFile=/root/.ssh/id_rsa",
		"mongodb://db.example.com:27017/admin?sslCertificateAuthorityFile=/etc/hosts",
		"mongodb://%2Ftmp%2Fmongodb-27017.sock/admin",
		"mongodb://unix.sock/admin",
	} {
		if err := ValidateMongoURIForRestrictedWeb(badMongoURI); err == nil {
			t.Fatalf("expected ValidateMongoURIForRestrictedWeb to reject %q", badMongoURI)
		}
	}
	if err := ValidateMongoURIForRestrictedWeb("mongodb://db.example.com:27017/admin?authSource=admin&tls=true"); err != nil {
		t.Fatalf("expected benign MongoDB URI to pass ValidateMongoURIForRestrictedWeb, got %v", err)
	}
}

func TestExecuteMultiSQLSharedMemoryBudget(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`CREATE TABLE big (payload TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("X", 256*1024) // 256 KiB per row
	tx, _ := db.Begin()
	for i := 0; i < 40; i++ { // ~10 MiB per SELECT
		_, _ = tx.Exec(`INSERT INTO big VALUES (?)`, blob)
	}
	_ = tx.Commit()

	// 5 SELECTs * ~10 MiB = ~50 MiB > 32 MiB cumulative budget, even though each SELECT alone is < 32 MiB!
	script := strings.Repeat("SELECT payload FROM big;\n", 5)
	results, err := ExecuteMultiSQL(context.Background(), db, script, 1000)
	if err != nil {
		t.Fatalf("ExecuteMultiSQL failed: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 result sets, got %d", len(results))
	}
	truncatedAny := false
	for _, r := range results {
		if r.Truncated {
			truncatedAny = true
		}
	}
	if !truncatedAny {
		t.Fatal("expected cumulative 32 MiB budget across multi-SELECT script to truncate later result sets")
	}
}

func BenchmarkStreamStatements(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "INSERT INTO bench_items (id, name, note) VALUES (%d, 'item_%d', 'a;b;c');\n", i, i)
	}
	payload := sb.String()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cnt := 0
		_ = StreamStatements(strings.NewReader(payload), func(_ string) error {
			cnt++
			return nil
		})
		if cnt != 500 {
			b.Fatalf("expected 500 stmts, got %d", cnt)
		}
	}
}

func BenchmarkExecuteSingleQueryRowScan(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, _ = db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, code TEXT, amount REAL, active INTEGER)`)
	tx, _ := db.Begin()
	for i := 1; i <= 500; i++ {
		_, _ = tx.Exec(`INSERT INTO items VALUES (?, ?, ?, ?)`, i, fmt.Sprintf("SKU-%04d", i), float64(i)*1.25, i%2)
	}
	_ = tx.Commit()

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, qErr := ExecuteSingleQuery(ctx, db, "SELECT * FROM items", 1000)
		if qErr != nil || len(res.Rows) != 500 {
			b.Fatalf("unexpected query result: %v len=%d", qErr, len(res.Rows))
		}
	}
}

func TestReadOnlyPragmaGuardAllowlist(t *testing.T) {
	blocked := []string{
		"PRAGMA main.wal_checkpoint(FULL)",
		"PRAGMA temp.optimize",
		"PRAGMA main.incremental_vacuum(10)",
		`PRAGMA "wal_checkpoint"(TRUNCATE)`,
		"PRAGMA `writable_schema`",
		"PRAGMA [incremental_vacuum](10)",
		`PRAGMA "main"."wal_checkpoint"(FULL)`,
		"PRAGMA main.a.table_info('x')",
		"PRAGMA journal_mode(DELETE)",
		"PRAGMA user_version(1)",
		"PRAGMA user_version = 5",
		"PRAGMA table_info('orders') SELECT 1",
		"PRAGMA force_checkpoint",
	}
	for _, stmt := range blocked {
		if ok, _ := IsReadOnlySafeStatement(stmt); ok {
			t.Fatalf("expected read-only guard to reject %q", stmt)
		}
	}

	allowed := []string{
		"PRAGMA table_info('orders')",
		"PRAGMA main.table_info('orders')",
		`PRAGMA "main"."table_info"('orders')`,
		"PRAGMA quick_check",
		"PRAGMA foreign_key_check",
		"PRAGMA integrity_check",
		"PRAGMA database_list",
		"PRAGMA compile_options",
		"PRAGMA encoding",
		"PRAGMA user_version",
		"PRAGMA journal_mode",
	}
	for _, stmt := range allowed {
		if ok, reason := IsReadOnlySafeStatement(stmt); !ok {
			t.Fatalf("expected read-only guard to allow %q, got reason=%q", stmt, reason)
		}
	}
}

func TestDuckDBRestrictedWebSQLParity(t *testing.T) {
	blocked := []string{
		"SELECT * FROM ('/etc/passwd')",
		"SELECT * FROM ((('/etc/passwd')))",
		"SELECT * FROM orders JOIN ('/etc/passwd') ON true",
		"SELECT * FROM orders, '/etc/passwd'",
		"SELECT * FROM orders, ('/etc/passwd')",
		"DESCRIBE '/etc/passwd'",
		"SUMMARIZE ('/etc/passwd')",
		"WITH c AS (SELECT * FROM ('/etc/passwd')) SELECT * FROM c",
		"SELECT * FROM read_xlsx('a.xlsx')",
		"SELECT * FROM st_read('a.geojson')",
		"SELECT * FROM glob('*')",
		"SELECT * FROM ls('.')",
		"SELECT * FROM main.glob('*')",
		"SELECT * FROM read_ndjson('a.json')",
		"SELECT * FROM sniff_csv('a.csv')",
		"SELECT * FROM parquet_metadata('a.parquet')",
		"SELECT * FROM postgres_query('db', 'SELECT 1')",
		"SELECT * FROM mysql_query('db', 'SELECT 1')",
		"SELECT * FROM duckdb_extensions()",
		"PRAGMA threads=4",
		"CALL duckdb_secrets()",
		"/* c */ CALL duckdb_secrets()",
		"EXPORT DATABASE '/tmp/out'",
		"IMPORT DATABASE '/tmp/in'",
		"/* outer /* inner */\n.shell id\n*/ SELECT 1",
	}
	for _, stmt := range blocked {
		if err := ValidateDuckDBRestrictedWebSQL(stmt); err == nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to reject %q", stmt)
		}
	}

	allowed := []string{
		"SELECT * FROM (SELECT 1) AS sub",
		"SELECT * FROM orders AS o WHERE o.name GLOB 'a*'",
		"SELECT export, import FROM jobs",
		"SELECT secret FROM users",
		"SELECT * FROM orders ORDER BY id DESC",
		"SELECT * FROM (SELECT 1, 'safe') AS sub",
		"SELECT $$line1\n.not_a_cmd$$ AS body",
		"SELECT * FROM fn('a', 'b') AS t",
		"SELECT 'a\\'b' AS txt",
	}
	for _, stmt := range allowed {
		if err := ValidateDuckDBRestrictedWebSQL(stmt); err != nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to allow %q, got %v", stmt, err)
		}
	}

	// Iteration 2 & 3 DuckDB sandbox bypass regressions
	extraBlocked := []string{
		"SELECT '\\'; CALL duckdb_secrets(); SELECT ''",
		"SELECT '\\'; ATTACH '/etc/passwd' AS p; SELECT ''",
		"SELECT '\\'; SELECT * FROM read_csv('/etc/passwd'); SELECT ''",
		"SELECT E'\\''; CALL duckdb_secrets(); SELECT ''",
		"SELECT '\\';\n.shell id; SELECT ''",
		"SELECT E'\\'';\n.shell id",
		"SELECT * FROM (SELECT * FROM safe_tbl) AS sub, '/etc/passwd'",
		"SELECT * FROM (SELECT 1 FROM safe_tbl WHERE id = 1) AS sub, ('/etc/passwd')",
		"SELECT * FROM (SELECT 1) AS sub, ('/etc/passwd')",
		"EXPLAIN DESCRIBE '/etc/passwd'",
		"EXPLAIN SUMMARIZE '/etc/passwd'",
		"CREATE TABLE t AS SUMMARIZE '/etc/passwd'",
		"EXPLAIN DESC '/etc/passwd'",
		"EXPLAIN DESC ('/etc/passwd')",
		"CREATE TABLE t AS DESC '/etc/passwd'",
		"SELECT /* /* */ ' */ ; CALL duckdb_secrets(); -- '",
		"SELECT /* /* */ ' */ ; ATTACH '/etc/passwd' AS p; -- '",
		"SELECT /* /* */ ' */ ;\n.shell id",
		"SELECT #1 FROM (SELECT 42); CALL duckdb_secrets()",
		"SELECT #1 FROM (SELECT 42);\n.shell id",
		"SELECT * FROM read_csv_objects('/etc/passwd')",
		"SELECT * FROM duckdb_settings()",
		"SELECT * FROM which_secret('s1')",
		"CREATE SECRET s1 (TYPE S3, KEY_ID 'a', SECRET 'b')",
		"CREATE OR REPLACE PERSISTENT SECRET s1 (TYPE S3, KEY_ID 'a')",
		"CREATE TEMPORARY SECRET s1 (TYPE S3, KEY_ID 'a')",
		"DROP SECRET s1",
		"DROP PERSISTENT SECRET IF EXISTS s1",
	}
	for _, stmt := range extraBlocked {
		if err := ValidateDuckDBRestrictedWebSQL(stmt); err == nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to reject %q", stmt)
		}
	}

	for _, okStmt := range []string{
		"SELECT * FROM orders ORDER BY id DESC NULLS LAST",
		"SELECT /* /* ' */ */ 1 AS ok",
	} {
		if err := ValidateDuckDBRestrictedWebSQL(okStmt); err != nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to allow %q, got %v", okStmt, err)
		}
	}
}

func TestBuildCreateTableIndexSQLsAndIndexQualification(t *testing.T) {
	req := driver.CreateTableRequest{
		Schema: "app_schema",
		Table:  "orders",
		Indexes: []driver.IndexDef{
			{Name: "idx_orders_code", Columns: []string{"code"}, Unique: true},
			{Columns: []string{"user_id", "created_at"}},
		},
	}

	pgIdx, err := BuildCreateTableIndexSQLs(DialectPostgres, req)
	if err != nil || len(pgIdx) != 2 {
		t.Fatalf("PG BuildCreateTableIndexSQLs failed: err=%v stmts=%v", err, pgIdx)
	}
	if pgIdx[0] != `CREATE UNIQUE INDEX "idx_orders_code" ON "app_schema"."orders" ("code")` {
		t.Fatalf("unexpected PG index 0: %s", pgIdx[0])
	}
	if pgIdx[1] != `CREATE INDEX "idx_orders_user_id_created_at" ON "app_schema"."orders" ("user_id", "created_at")` {
		t.Fatalf("unexpected PG index 1: %s", pgIdx[1])
	}

	msIdx, err := BuildCreateTableIndexSQLs(DialectSQLServer, req)
	if err != nil || len(msIdx) != 2 || msIdx[0] != `CREATE UNIQUE INDEX [idx_orders_code] ON [app_schema].[orders] ([code])` {
		t.Fatalf("unexpected SQLServer index: err=%v stmts=%v", err, msIdx)
	}

	oraIdx, err := BuildCreateTableIndexSQLs(DialectOracle, req)
	if err != nil || len(oraIdx) != 2 || oraIdx[0] != `CREATE UNIQUE INDEX "app_schema"."idx_orders_code" ON "app_schema"."orders" ("code")` {
		t.Fatalf("unexpected Oracle schema-qualified index: err=%v stmts=%v", err, oraIdx)
	}
	oraDrop, err := BuildAlterTableSQLs(DialectOracle, driver.AlterTableRequest{
		Schema:  "app_schema",
		Table:   "orders",
		Actions: []driver.AlterAction{{Action: "DROP_INDEX", Index: driver.IndexDef{Name: "idx_orders_code"}}},
	})
	if err != nil || len(oraDrop) != 1 || oraDrop[0] != `DROP INDEX "app_schema"."idx_orders_code"` {
		t.Fatalf("unexpected Oracle DROP_INDEX: err=%v stmts=%v", err, oraDrop)
	}

	dmIdx, err := BuildCreateTableIndexSQLs(DialectDameng, req)
	if err != nil || len(dmIdx) != 2 || dmIdx[0] != `CREATE UNIQUE INDEX "app_schema"."idx_orders_code" ON "app_schema"."orders" ("code")` {
		t.Fatalf("unexpected Dameng schema-qualified index: err=%v stmts=%v", err, dmIdx)
	}

	if myIdx, err := BuildCreateTableIndexSQLs(DialectMySQL, req); err != nil || len(myIdx) != 0 {
		t.Fatalf("expected nil indexes for MySQL inline DDL, got err=%v stmts=%v", err, myIdx)
	}
	if chIdx, err := BuildCreateTableIndexSQLs(DialectClickHouse, req); err != nil || len(chIdx) != 0 {
		t.Fatalf("expected nil indexes for ClickHouse, got err=%v stmts=%v", err, chIdx)
	}
}

func TestBuildDriverTLSConfig(t *testing.T) {
	if cfg, err := BuildDriverTLSConfig("db.example.com", "false", "", false); err != nil || cfg != nil {
		t.Fatalf("expected nil TLS config when disabled, got cfg=%v err=%v", cfg, err)
	}
	if _, err := BuildDriverTLSConfig("db.example.com", "skip-verify", "", true); err == nil {
		t.Fatal("expected skip-verify with restrictIP=true to fail")
	}
	skipCfg, err := BuildDriverTLSConfig("db.example.com", "skip-verify", "", false)
	if err != nil || skipCfg == nil || !skipCfg.InsecureSkipVerify || skipCfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected admin skip-verify config: cfg=%+v err=%v", skipCfg, err)
	}

	// Generate valid self-signed CA PEM in memory
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dbharbor-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	verCfg, err := BuildDriverTLSConfig("db.example.com", "true", caPEM, true)
	if err != nil || verCfg == nil || verCfg.InsecureSkipVerify || verCfg.RootCAs == nil || verCfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected verified TLS config with custom CA: cfg=%+v err=%v", verCfg, err)
	}
	if _, err := BuildDriverTLSConfig("db.example.com", "true", "not-a-valid-pem", true); err == nil {
		t.Fatal("expected invalid CA PEM to fail")
	}
}

func TestDialectAwareSingleQuoteEscapingAndReadOnlyGuards(t *testing.T) {
	// 1. Standard SQL engines (default, sqlite, duckdb, postgres, kingbase, oracle, dameng, sqlserver):
	// '\'' inside '...' is literal '\' + closing quote, so smuggled DROP TABLE must be split and blocked.
	smuggleStd := "SELECT '\\'; DROP TABLE items; SELECT ''"
	for _, eng := range []string{"", "sqlite", "duckdb", "postgres", "kingbase", "oracle", "dameng", "sqlserver"} {
		if stmts := SplitStatementsForEngine(eng, smuggleStd); len(stmts) != 3 {
			t.Fatalf("engine %q: expected 3 split statements for %q, got %d: %#v", eng, smuggleStd, len(stmts), stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, smuggleStd); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, smuggleStd)
		}
	}
	if err := ValidateReadOnlyScript(smuggleStd); err == nil {
		t.Fatalf("expected ValidateReadOnlyScript to reject %q", smuggleStd)
	}

	// Double-quoted identifier \" smuggle in standard SQL must also split and be blocked.
	smuggleDbl := `SELECT "\"; DROP TABLE items; SELECT "`
	if err := ValidateReadOnlyScriptForEngine("sqlite", smuggleDbl); err == nil {
		t.Fatalf("expected sqlite read-only guard to reject %q", smuggleDbl)
	}

	// Benign string containing trailing backslash on SQLite/DuckDB must stay 1 statement and pass read-only guard.
	if stmts := SplitStatementsForEngine("sqlite", "SELECT 'a\\' AS s"); len(stmts) != 1 {
		t.Fatalf("expected 1 statement for sqlite 'a\\' literal, got %d: %#v", len(stmts), stmts)
	}
	if err := ValidateReadOnlyScriptForEngine("sqlite", "SELECT 'a\\' AS s"); err != nil {
		t.Fatalf("expected sqlite 'a\\' literal to pass read-only guard, got %v", err)
	}

	// 2. MySQL-family & ClickHouse treat \' as an escaped quote; '\'' is literal \' + closing quote,
	// so SELECT '\''; DROP TABLE items; SELECT '' must split into 3 statements and be blocked.
	smuggleBS := "SELECT '\\''; DROP TABLE items; SELECT ''"
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase", "clickhouse"} {
		if stmts := SplitStatementsForEngine(eng, smuggleBS); len(stmts) != 3 {
			t.Fatalf("engine %q: expected 3 statements for %q, got %d: %#v", eng, smuggleBS, len(stmts), stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, smuggleBS); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, smuggleBS)
		}
		// Legitimate MySQL/ClickHouse backslash-escaped quote remains 1 statement and passes read-only guard.
		if stmts := SplitStatementsForEngine(eng, "SELECT 'O\\'Reilly'"); len(stmts) != 1 {
			t.Fatalf("engine %q: expected SELECT 'O\\'Reilly' to parse as 1 statement, got %d: %#v", eng, len(stmts), stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 'O\\'Reilly'"); err != nil {
			t.Fatalf("engine %q: expected SELECT 'O\\'Reilly' to pass read-only guard, got %v", eng, err)
		}
	}

	// ClickHouse restricted web guard catches '\''; <external_fn> bypasses
	if err := ValidateClickHouseRestrictedWebSQL("SELECT '\\''; SELECT * FROM url('http://127.0.0.1/', 'CSV', 'x String'); SELECT ''"); err == nil {
		t.Fatal("expected ValidateClickHouseRestrictedWebSQL to reject backslash-escaped smuggled URL call")
	}

	// 3. PostgreSQL / Kingbase / DuckDB E'...' and e'...' escape string literals
	smugglePG := "SELECT E'\\''; DROP TABLE items; SELECT ''"
	for _, eng := range []string{"postgres", "kingbase", "duckdb"} {
		if stmts := SplitStatementsForEngine(eng, smugglePG); len(stmts) != 3 {
			t.Fatalf("engine %q: expected 3 statements for %q, got %d: %#v", eng, smugglePG, len(stmts), stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, smugglePG); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, smugglePG)
		}
		if stmts := SplitStatementsForEngine(eng, "SELECT e'O\\'Reilly;ok'"); len(stmts) != 1 {
			t.Fatalf("engine %q: expected e'O\\'Reilly;ok' to stay 1 statement, got %d: %#v", eng, len(stmts), stmts)
		}
	}
	// Non-escape-string engines (SQLite) must NOT treat e'...' as E'...' (prevents SELECT e'\'; DROP TABLE items; SELECT '' alias smuggle)
	if err := ValidateReadOnlyScriptForEngine("sqlite", "SELECT e'\\'; DROP TABLE items; SELECT ''"); err == nil {
		t.Fatal("expected sqlite read-only guard to reject e'\\'; DROP TABLE items")
	}
	// Identifier boundary check: table_e'...' must NOT trigger E'...' escape mode on Postgres
	if stmts := SplitStatementsForEngine("postgres", "SELECT table_e'\\'; DROP TABLE items;"); len(stmts) < 2 {
		t.Fatalf("expected identifier boundary to prevent table_e'...' from entering E'...' mode, got %#v", stmts)
	}
}

func TestDialectNestedBlockCommentsHashCommentsAndDDLGuards(t *testing.T) {
	// 1. Verify SyntaxOptionsForEngine flags across all supported engine names
	for _, eng := range []string{"mysql", "tidb", "oceanbase"} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.BackslashEscapes || !opts.HashComments || !opts.ExecComments || opts.MariaExecComments || opts.NestedBlockComments || opts.EscapeStringPrefix || opts.DollarQuotes {
			t.Fatalf("unexpected SyntaxOptionsForEngine(%q): %+v", eng, opts)
		}
	}
	mariaOpts := SyntaxOptionsForEngine("mariadb")
	if !mariaOpts.BackslashEscapes || !mariaOpts.HashComments || !mariaOpts.ExecComments || !mariaOpts.MariaExecComments || mariaOpts.NestedBlockComments || mariaOpts.EscapeStringPrefix || mariaOpts.DollarQuotes {
		t.Fatalf("unexpected SyntaxOptionsForEngine(\"mariadb\"): %+v", mariaOpts)
	}
	chOpts := SyntaxOptionsForEngine("clickhouse")
	if !chOpts.BackslashEscapes || !chOpts.HashComments || chOpts.NestedBlockComments || chOpts.EscapeStringPrefix || chOpts.ExecComments || chOpts.MariaExecComments || chOpts.DollarQuotes {
		t.Fatalf("unexpected SyntaxOptionsForEngine(\"clickhouse\"): %+v", chOpts)
	}
	for _, eng := range []string{"postgres", "postgresql", "kingbase", "duckdb"} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.EscapeStringPrefix || !opts.NestedBlockComments || !opts.DollarQuotes || opts.HashComments || opts.BackslashEscapes || opts.ExecComments || opts.MariaExecComments {
			t.Fatalf("unexpected SyntaxOptionsForEngine(%q): %+v", eng, opts)
		}
	}
	for _, eng := range []string{"sqlserver", "mssql"} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.NestedBlockComments || opts.HashComments || opts.BackslashEscapes || opts.EscapeStringPrefix || opts.ExecComments || opts.MariaExecComments || opts.DollarQuotes {
			t.Fatalf("unexpected SyntaxOptionsForEngine(%q): %+v", eng, opts)
		}
	}
	for _, eng := range []string{"sqlite", "oracle", "dameng", "dm8"} {
		opts := SyntaxOptionsForEngine(eng)
		if opts.NestedBlockComments || opts.HashComments || opts.BackslashEscapes || opts.EscapeStringPrefix || opts.ExecComments || opts.MariaExecComments || opts.DollarQuotes {
			t.Fatalf("unexpected SyntaxOptionsForEngine(%q): %+v", eng, opts)
		}
	}
	defOpts := SyntaxOptionsForEngine("")
	if defOpts.NestedBlockComments || defOpts.HashComments || defOpts.BackslashEscapes || defOpts.EscapeStringPrefix || !defOpts.ExecComments || defOpts.MariaExecComments || defOpts.DollarQuotes {
		t.Fatalf("unexpected SyntaxOptionsForEngine(\"\"): %+v", defOpts)
	}

	// 2. Nested block comment smuggles on nested-comment engines
	nestedSmuggle := "SELECT /* /* */ ' */ ; DROP TABLE items; -- '"
	for _, eng := range []string{"postgres", "postgresql", "kingbase", "duckdb", "sqlserver", "mssql"} {
		if stmts := SplitStatementsForEngine(eng, nestedSmuggle); len(stmts) < 2 {
			t.Fatalf("engine %q: expected nested block comment smuggle to split, got %#v", eng, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, nestedSmuggle); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, nestedSmuggle)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT /* /* /* */ */ ' */ ; DROP TABLE items"); err == nil {
			t.Fatalf("engine %q: expected 3-level nested comment smuggle to be rejected", eng)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT /* /* ' */ */ 1"); err != nil {
			t.Fatalf("engine %q: expected valid nested comment to pass, got %v", eng, err)
		}
	}

	// Non-nested engines close at the first '*/', so /* /* */ ; DROP TABLE items must split and be rejected
	flatSmuggle := "SELECT /* /* */ ; DROP TABLE items"
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase", "clickhouse", "sqlite", "oracle", "dameng", "dm8"} {
		if stmts := SplitStatementsForEngine(eng, flatSmuggle); len(stmts) != 2 {
			t.Fatalf("engine %q: expected flat block comment smuggle to split into 2, got %#v", eng, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, flatSmuggle); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, flatSmuggle)
		}
	}

	// Non-MySQL engines must NOT unwrap /*! ... */ as executable comments
	execCommentSmuggle := "SELECT /*! ' */ ; DROP TABLE items"
	for _, eng := range []string{"postgres", "duckdb", "sqlserver", "sqlite", "oracle", "dameng", "clickhouse"} {
		if err := ValidateReadOnlyScriptForEngine(eng, execCommentSmuggle); err == nil {
			t.Fatalf("engine %q: expected /*! ' */ smuggle to be rejected", eng)
		}
	}

	// 3. '#' line comment handling ONLY for MySQL-family & ClickHouse
	hashCases := []struct {
		eng string
		sql string
	}{
		{"duckdb", "SELECT #1 FROM (SELECT 42); CALL duckdb_secrets()"},
		{"sqlserver", "SELECT * FROM #temp; DROP TABLE items"},
		{"mssql", "SELECT * FROM #temp; DROP TABLE items"},
		{"postgres", `SELECT '{"a":1}'::jsonb #> '{a}'; DROP TABLE items`},
		{"kingbase", `SELECT '{"a":1}'::jsonb #> '{a}'; DROP TABLE items`},
		{"oracle", "SELECT col#1 FROM dual; DROP TABLE items"},
		{"dameng", "SELECT col#1 FROM dual; DROP TABLE items"},
		{"sqlite", "SELECT #1; DROP TABLE items"},
	}
	for _, tc := range hashCases {
		if stmts := SplitStatementsForEngine(tc.eng, tc.sql); len(stmts) != 2 {
			t.Fatalf("engine %q: expected '#' not to hide '; DROP' in %q, got %#v", tc.eng, tc.sql, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", tc.eng, tc.sql)
		}
	}
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase", "clickhouse"} {
		if stmts := SplitStatementsForEngine(eng, "SELECT 1 # comment; not split"); len(stmts) != 1 {
			t.Fatalf("engine %q: expected '#' line comment to stay 1 statement, got %#v", eng, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 1 # harmless comment; DROP TABLE items"); err != nil {
			t.Fatalf("engine %q: expected '#' line comment to pass read-only guard, got %v", eng, err)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 1 # comment\n; DROP TABLE items"); err == nil {
			t.Fatalf("engine %q: expected statement on line after '#' comment to be blocked", eng)
		}
	}

	// StripLeadingSQLCommentsForEngine & HasSQLDefinerClauseForEngine
	if got := StripLeadingSQLCommentsForEngine("mysql", "# c\nSELECT 1"); got != "SELECT 1" {
		t.Fatalf("expected mysql StripLeadingSQLCommentsForEngine to strip '#', got %q", got)
	}
	if got := StripLeadingSQLCommentsForEngine("postgres", "#1\nSELECT 1"); got != "#1\nSELECT 1" {
		t.Fatalf("expected postgres StripLeadingSQLCommentsForEngine NOT to strip '#', got %q", got)
	}
	if got := StripLeadingSQLCommentsForEngine("postgres", "/* /* */ */ SELECT 1"); got != "SELECT 1" {
		t.Fatalf("expected postgres StripLeadingSQLCommentsForEngine to strip nested block comment, got %q", got)
	}
	if !HasSQLDefinerClauseForEngine("postgres", "CREATE VIEW v AS /* /* */ ' */ DEFINER = root SELECT 1") {
		t.Fatal("expected postgres HasSQLDefinerClauseForEngine to catch nested comment bypass")
	}
	if !HasSQLDefinerClauseForEngine("postgres", "CREATE VIEW v AS SELECT 'a' #> '{b}'; DEFINER = root") {
		t.Fatal("expected postgres HasSQLDefinerClauseForEngine to catch '#>' bypass")
	}

	// 4. validateDDLFragment dialect-awareness in BuildCreateTableDDL & BuildAlterTableSQLs
	if _, err := BuildCreateTableDDL(DialectMySQL, driver.CreateTableRequest{
		Database: "app",
		Table:    "t_my",
		Columns:  []driver.ColumnDef{{Name: "val", Type: "VARCHAR(64)", DefaultValue: "'\\''; DROP TABLE items; -- '"}},
	}); err == nil {
		t.Fatal("expected MySQL BuildCreateTableDDL to reject backslash-escaped defaultValue smuggle")
	}
	if _, err := BuildCreateTableDDL(DialectMySQL, driver.CreateTableRequest{
		Database: "app",
		Table:    "t_my_ok",
		Columns:  []driver.ColumnDef{{Name: "val", Type: "VARCHAR(64)", DefaultValue: "'O\\'Reilly'"}},
	}); err != nil {
		t.Fatalf("expected MySQL BuildCreateTableDDL to allow valid 'O\\'Reilly' defaultValue, got %v", err)
	}
	if _, err := BuildAlterTableSQLs(DialectMySQL, driver.AlterTableRequest{
		Database: "app",
		Table:    "t_my",
		Actions: []driver.AlterAction{
			{Action: "ADD_COLUMN", Column: driver.ColumnDef{Name: "v2", Type: "VARCHAR(64)", DefaultValue: "'\\''; DROP TABLE items; -- '"}},
		},
	}); err == nil {
		t.Fatal("expected MySQL BuildAlterTableSQLs ADD_COLUMN to reject backslash-escaped defaultValue smuggle")
	}
	if _, err := BuildAlterTableSQLs(DialectMySQL, driver.AlterTableRequest{
		Database: "app",
		Table:    "t_my",
		Actions: []driver.AlterAction{
			{Action: "MODIFY_COLUMN", Column: driver.ColumnDef{Name: "v2", Type: "VARCHAR(64)", DefaultValue: "'\\''; DROP TABLE items; -- '"}},
		},
	}); err == nil {
		t.Fatal("expected MySQL BuildAlterTableSQLs MODIFY_COLUMN to reject backslash-escaped defaultValue smuggle")
	}
	if _, err := BuildCreateTableDDL(DialectClickHouse, driver.CreateTableRequest{
		Database: "default",
		Table:    "t_ch",
		Columns:  []driver.ColumnDef{{Name: "id", Type: "UInt64"}},
		OrderBy:  "tuple(), '\\''; DROP TABLE items; -- '",
	}); err == nil {
		t.Fatal("expected ClickHouse BuildCreateTableDDL to reject backslash-escaped orderBy smuggle")
	}
	if _, err := BuildCreateTableDDL(DialectPostgres, driver.CreateTableRequest{
		Schema:  "public",
		Table:   "t_pg",
		Columns: []driver.ColumnDef{{Name: "val", Type: "TEXT", DefaultValue: "e'\\''; DROP TABLE items; -- '"}},
	}); err == nil {
		t.Fatal("expected PostgreSQL BuildCreateTableDDL to reject e'\\'' defaultValue smuggle")
	}
	if _, err := BuildCreateTableDDL(DialectPostgres, driver.CreateTableRequest{
		Schema:  "public",
		Table:   "t_pg_ok",
		Columns: []driver.ColumnDef{{Name: "val", Type: "TEXT", DefaultValue: "E'O\\'Reilly'"}},
	}); err != nil {
		t.Fatalf("expected PostgreSQL BuildCreateTableDDL to allow valid E'O\\'Reilly' defaultValue, got %v", err)
	}

	// 5. BuildObjectActionSQLs HasSQLDefinerClauseForEngine
	if _, err := BuildObjectActionSQLs(DialectMySQL, driver.ObjectActionRequest{
		Database:   "app",
		ObjectName: "v_ok",
		ObjectType: "VIEW",
		Action:     "CREATE_VIEW",
		WebMode:    true,
		IsAdmin:    false,
		ViewSQL:    "CREATE VIEW v_ok AS SELECT 'it\\'s DEFINER = ok' AS note",
	}); err != nil {
		t.Fatalf("expected MySQL BuildObjectActionSQLs to allow escaped quote before DEFINER = inside string literal, got %v", err)
	}
	if _, err := BuildObjectActionSQLs(DialectMySQL, driver.ObjectActionRequest{
		Database:   "app",
		ObjectName: "p_evil",
		ObjectType: "PROCEDURE",
		Action:     "CREATE_ROUTINE",
		WebMode:    true,
		IsAdmin:    false,
		RoutineSQL: "CREATE PROCEDURE p_evil() COMMENT 'abc\\'' SQL SECURITY DEFINER BEGIN SELECT 1; END",
	}); err == nil {
		t.Fatal("expected MySQL BuildObjectActionSQLs to reject SQL SECURITY DEFINER after escaped quote")
	}
}

func TestIter4DollarQuotesExecCommentsDuckDBPivotAndHashFilter(t *testing.T) {
	// 1. DollarQuotes dialect-gating: blocked on non-PostgreSQL/non-DuckDB engines, preserved on PG/KB/DuckDB
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase"} {
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 1 AS $x$, LOAD_FILE('/etc/passwd') AS $x$"); err == nil {
			t.Fatalf("engine %q: expected $x$ wrapper around LOAD_FILE to be rejected", eng)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 1 AS $x$ INTO OUTFILE '/tmp/pwn.txt' FROM dual HAVING $x$ = 1"); err == nil {
			t.Fatalf("engine %q: expected $x$ wrapper around INTO OUTFILE to be rejected", eng)
		}
		if !HasSQLDefinerClauseForEngine(eng, "CREATE FUNCTION f($x$ INT) RETURNS INT SQL SECURITY DEFINER RETURN $x$") {
			t.Fatalf("engine %q: expected $x$ wrapper around SQL SECURITY DEFINER to be caught", eng)
		}
	}
	if err := ValidateReadOnlyScriptForEngine("clickhouse", "SELECT 1 AS $x$, s3('http://169.254.169.254/') AS $x$"); err == nil {
		t.Fatal("expected clickhouse read-only guard to reject $x$ wrapper around s3()")
	}
	if err := ValidateClickHouseRestrictedWebSQL("SELECT 1 AS $x$, s3('http://169.254.169.254/') AS $x$"); err == nil {
		t.Fatal("expected ValidateClickHouseRestrictedWebSQL to reject $x$ wrapper around s3()")
	}
	for _, eng := range []string{"sqlite", "sqlserver", "oracle", "dameng", ""} {
		if stmts := SplitStatementsForEngine(eng, "SELECT $x$; DROP TABLE items; SELECT $x$"); len(stmts) != 3 {
			t.Fatalf("engine %q: expected $x$ not to swallow '; DROP TABLE items;', got %#v", eng, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT $x$; DROP TABLE items; SELECT $x$"); err == nil {
			t.Fatalf("engine %q: expected read-only guard to reject $x$; DROP TABLE items", eng)
		}
	}
	for _, eng := range []string{"postgres", "postgresql", "kingbase", "duckdb"} {
		if stmts := SplitStatementsForEngine(eng, "SELECT $tag$a; DROP TABLE items;$tag$ AS txt;"); len(stmts) != 1 {
			t.Fatalf("engine %q: expected legitimate $tag$...$tag$ string to stay 1 statement, got %#v", eng, stmts)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT $$SELECT; DROP TABLE items$$ AS txt"); err != nil {
			t.Fatalf("engine %q: expected legitimate $$...$$ string to pass read-only guard, got %v", eng, err)
		}
		// Ident boundary: col$x$ must not trigger dollar quote
		if stmts := SplitStatementsForEngine(eng, "SELECT col$x$; DROP TABLE items; SELECT $x$"); len(stmts) < 2 {
			t.Fatalf("engine %q: expected identifier boundary col$x$ not to enter dollar-quote mode, got %#v", eng, stmts)
		}
	}
	// Postgres validateDDLFragment must catch $$'$$; DROP TABLE items
	if _, err := BuildCreateTableDDL(DialectPostgres, driver.CreateTableRequest{
		Schema:  "public",
		Table:   "t_pg_dq",
		Columns: []driver.ColumnDef{{Name: "val", Type: "TEXT", DefaultValue: "$$'$$; DROP TABLE items; -- '"}},
	}); err == nil {
		t.Fatal("expected PostgreSQL BuildCreateTableDDL to reject $$'$$; DROP TABLE items defaultValue smuggle")
	}

	// 2. MySQL / MariaDB executable comment quote-swallowing & /*M! isolation
	for _, eng := range []string{"mysql", "tidb", "oceanbase", "mariadb"} {
		for _, defStmt := range []string{
			"CREATE PROCEDURE p_b1() /*M! ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_b2() /*!99999 ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_b3() /*!50001 ' */ SQL SECURITY DEFINER /* ' */ BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_b4() /*!99999 -- */ SQL SECURITY DEFINER BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_b5() /*!99999 [ */ SQL SECURITY DEFINER /* ] */ BEGIN SELECT 1; END",
		} {
			if !HasSQLDefinerClauseForEngine(eng, defStmt) {
				t.Fatalf("engine %q: expected HasSQLDefinerClauseForEngine to catch %q", eng, defStmt)
			}
		}
		for _, roBad := range []string{
			"SELECT 1 /*M! ' */ INTO OUTFILE '/tmp/pwned' /* ' */",
			"SELECT 1 /*!99999 ' */ , LOAD_FILE('/etc/passwd') /* ' */",
			"SELECT /*! ' */ ; DROP TABLE orders; -- '",
			"SELECT /*! DROP TABLE items */",
			"SELECT /*! -- */ ; DROP TABLE orders",
		} {
			if err := ValidateReadOnlyScriptForEngine(eng, roBad); err == nil {
				t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, roBad)
			}
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT /*!50001 'ok' */ AS val"); err != nil {
			t.Fatalf("engine %q: expected benign /*!50001 'ok' */ to pass, got %v", eng, err)
		}
		if stmts := SplitStatementsForEngine(eng, "CREATE VIEW v1 AS SELECT 1 /*!99999 ' */ ; DROP TABLE users; -- ' */"); len(stmts) < 2 {
			t.Fatalf("engine %q: expected unbalanced quote in /*!99999 ' */ not to swallow '; DROP TABLE users', got %#v", eng, stmts)
		}
		if err := ValidateExpectedDDLTargetForEngine(eng, "CREATE /*!99999 ' */ VIEW /* ' */ USER evil IDENTIFIED BY 'x'", []string{"CREATE"}, "VIEW"); err == nil {
			t.Fatalf("engine %q: expected ValidateExpectedDDLTargetForEngine to reject skipped-comment VIEW smuggling CREATE USER", eng)
		}
	}
	// /*M! is NOT unwrapped on mysql, tidb, oceanbase, ""
	for _, eng := range []string{"mysql", "tidb", "oceanbase", ""} {
		if stmts := SplitStatementsForEngine(eng, "/*M!100100 SELECT 1 */;"); len(stmts) != 0 {
			t.Fatalf("engine %q: expected /*M!...*/ to remain regular block comment, got %#v", eng, stmts)
		}
	}

	// 3. DuckDB PIVOT / UNPIVOT direct file scans & FROM VALUES bypass
	for _, badDuck := range []string{
		"PIVOT '/etc/passwd' ON col USING SUM(val)",
		"PIVOT ('/etc/passwd') ON col USING SUM(val)",
		"UNPIVOT '/etc/passwd' ON col",
		"UNPIVOT ('/etc/passwd') ON col",
		"EXPLAIN PIVOT '/etc/passwd' ON col",
		"SELECT * FROM VALUES (1) AS t(a), '/etc/passwd'",
		"SELECT * FROM VALUES (1) t(a), ('/etc/passwd')",
		"SELECT * FROM VALUES (1), '/etc/passwd'",
		"SELECT * FROM (VALUES (1)) AS t(a), '/etc/passwd'",
	} {
		if err := ValidateDuckDBRestrictedWebSQL(badDuck); err == nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to reject %q", badDuck)
		}
	}
	for _, okDuck := range []string{
		"INSERT INTO t VALUES (1, 'a')",
		"SELECT * FROM VALUES (1) AS t(a)",
		"SELECT * FROM VALUES ('a', 1), ('b', 2) AS t(k, v)",
		"PIVOT safe_tbl ON col USING SUM(val)",
		"SELECT * FROM safe_tbl UNPIVOT (val FOR col IN (a, b))",
	} {
		if err := ValidateDuckDBRestrictedWebSQL(okDuck); err != nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to allow %q, got %v", okDuck, err)
		}
	}
	// Non-DuckDB engine must not treat $x$ as dollar quote inside hasSQLDotCommandOutsideQuotesWithOpts
	if !hasSQLDotCommandOutsideQuotesWithOpts("SELECT $x$\n.shell id\n$x$", SyntaxOptionsForEngine("sqlite")) {
		t.Fatal("expected hasSQLDotCommandOutsideQuotesWithOpts with DollarQuotes=false to detect .shell inside $x$...$x$")
	}

	// 4. Safe '#' handling in containsUnquotedSQLMetaWithOpts / BuildBrowseQuery
	if _, _, err := BuildBrowseQuery(DialectPostgres, driver.BrowseDataRequest{
		Schema: "public",
		Table:  "events",
		Filter: "data #>> '{a}' = '1'",
	}); err != nil {
		t.Fatalf("expected Postgres BrowseData JSONB #>> filter to pass, got %v", err)
	}
	if _, _, err := BuildBrowseQuery(DialectPostgres, driver.BrowseDataRequest{
		Schema: "public",
		Table:  "events",
		Filter: "data #>> '{a}' = '1'; DROP TABLE events",
	}); err == nil {
		t.Fatal("expected Postgres BrowseData filter with '; DROP TABLE' after #>> to be rejected")
	}
	for _, d := range []Dialect{DialectMySQL, DialectClickHouse} {
		if _, _, err := BuildBrowseQuery(d, driver.BrowseDataRequest{
			Database: "app",
			Table:    "events",
			Filter:   "1 = 1 # comment",
		}); err == nil {
			t.Fatalf("dialect %s: expected BrowseData '#' comment filter to be rejected", d)
		}
	}
}

func TestIter5ExecCommentBackslashCloseAndMariaObjectActions(t *testing.T) {
	// 1. HasSQLDefinerClauseForEngine: '\*/', '"\*/', '\\*/', '\\\*/' inside MySQL/MariaDB executable comments
	for _, tc := range []struct {
		eng  string
		sql  string
		want bool
	}{
		{"mysql", "CREATE PROCEDURE p9() SQL /*!50000 SECURITY */ /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mariadb", "CREATE PROCEDURE p9() SQL /*!50000 SECURITY */ /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql", "CREATE PROCEDURE p12() SQL /*!50000 SECURITY */ /*!99999 \" \\*/ DEFINER /* \" */ BEGIN SELECT 1; END", true},
		{"mariadb", "CREATE PROCEDURE p12() SQL /*!50000 SECURITY */ /*!99999 \" \\*/ DEFINER /* \" */ BEGIN SELECT 1; END", true},
		{"mariadb", "CREATE PROCEDURE p10() SQL /*M!50000 SECURITY */ /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql", "CREATE PROCEDURE p() /*!99999 '\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END", true},
		{"mariadb", "CREATE PROCEDURE p() /*!99999 '\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END", true},
		{"mysql", "CREATE PROCEDURE p_even() /*!99999 '\\\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END", true},
		{"mysql", "CREATE PROCEDURE p_odd() /*!99999 '\\\\\\*/ /*!50001 SQL SECURITY DEFINER -- '\n */ BEGIN SELECT 1; END", true},
		{"mysql", "CREATE PROCEDURE p_ok() COMMENT /*!50001 'abc\\\\' */ BEGIN SELECT 1; END", false},
		{"mariadb", "CREATE PROCEDURE p_ok() COMMENT /*M!50001 'abc\\\\' */ BEGIN SELECT 1; END", false},
	} {
		if got := HasSQLDefinerClauseForEngine(tc.eng, tc.sql); got != tc.want {
			t.Fatalf("engine %q: HasSQLDefinerClauseForEngine(%q) = %v, want %v", tc.eng, tc.sql, got, tc.want)
		}
	}

	// 2. ValidateReadOnlyScriptForEngine: '\*/', '"\*/', '\\\*/' locking reads & LOAD_FILE bypasses
	for _, tc := range []struct {
		eng string
		sql string
	}{
		{"mysql", "SELECT * FROM users /*!50000 FOR */ /*!99999 ' \\*/ SHARE /* ' */"},
		{"mariadb", "SELECT * FROM users /*!50000 FOR */ /*!99999 ' \\*/ SHARE /* ' */"},
		{"mysql", "SELECT * FROM users /*!50000 FOR */ /*!99999 \" \\*/ SHARE /* \" */"},
		{"mariadb", "SELECT * FROM users /*M!50000 FOR */ /*M!999999 ' \\*/ SHARE /* ' */"},
		{"mysql", "SELECT 1 /*!99999 '\\*/ /*!50001 UNION SELECT LOAD_FILE(0x2f6574632f706173737764) -- '\n */"},
		{"mysql", "SELECT 1 /*!99999 \"\\*/ /*!50001 UNION SELECT LOAD_FILE(0x2f6574632f706173737764) -- \"\n */"},
		{"mysql", "SELECT 1 /*!99999 '\\\\\\*/ /*!50001 UNION SELECT LOAD_FILE(0x2f6574632f706173737764) -- '\n */"},
	} {
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", tc.eng, tc.sql)
		}
	}
	for _, eng := range []string{"mysql", "mariadb"} {
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT /*!50001 'abc\\\\' */ AS val"); err != nil {
			t.Fatalf("engine %q: expected benign escaped-backslash literal inside exec comment to pass, got %v", eng, err)
		}
	}

	// 3. StreamStatementsWithOptions 1-byte chunk boundary safety & StripLeadingSQLCommentsForEngine
	var chunkedStmts []string
	if err := StreamStatementsWithOptions(
		iotest.OneByteReader(strings.NewReader("SELECT 1 /*!99999 '\\*/ ; DROP TABLE items; -- ' */")),
		SyntaxOptionsForEngine("mysql"),
		func(stmt string) error {
			chunkedStmts = append(chunkedStmts, stmt)
			return nil
		},
	); err != nil {
		t.Fatalf("StreamStatementsWithOptions OneByteReader error: %v", err)
	}
	if len(chunkedStmts) < 2 {
		t.Fatalf("expected 1-byte chunked StreamStatementsWithOptions not to swallow '; DROP TABLE items', got %#v", chunkedStmts)
	}

	var benignChunked []string
	if err := StreamStatementsWithOptions(
		iotest.OneByteReader(strings.NewReader("SELECT /*!50001 'abc\\\\;not_split' */ AS v; SELECT 2")),
		SyntaxOptionsForEngine("mysql"),
		func(stmt string) error {
			benignChunked = append(benignChunked, stmt)
			return nil
		},
	); err != nil {
		t.Fatalf("StreamStatementsWithOptions benign OneByteReader error: %v", err)
	}
	if len(benignChunked) != 2 {
		t.Fatalf("expected benign escaped-backslash literal with ';' inside exec comment to yield 2 statements, got %#v", benignChunked)
	}

	if stripped := StripLeadingSQLCommentsForEngine("mysql", "/*!99999 '\\*/ DROP TABLE items"); stripped != "DROP TABLE items" {
		t.Fatalf("expected StripLeadingSQLCommentsForEngine to expose DROP TABLE items, got %q", stripped)
	}

	// 4. BuildObjectActionSQLsForEngine("mariadb", DialectMySQL, ...) parity for /*M!...*/
	for _, tc := range []struct {
		name string
		req  driver.ObjectActionRequest
	}{
		{
			name: "view_multi_stmt",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_maria_pwn", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "/*M!50003 DROP TABLE items; */ CREATE VIEW v_maria_pwn AS SELECT 1",
			},
		},
		{
			name: "view_single_stmt_drop",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_maria_pwn", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "/*M!50003 DROP TABLE items -- */\nCREATE VIEW v_maria_pwn AS SELECT 1",
			},
		},
		{
			name: "view_target_spoof",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_maria_pwn", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE /*M!50003 TABLE pwn(x INT) */ VIEW v_maria_pwn AS SELECT 1",
			},
		},
		{
			name: "routine_multi_stmt",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_maria_pwn", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "/*M!50003 DROP TABLE items; */ CREATE PROCEDURE p_maria_pwn() BEGIN SELECT 1; END",
			},
		},
		{
			name: "trigger_multi_stmt",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "t_maria_pwn", ObjectType: "TRIGGER", Action: "CREATE_TRIGGER",
				RoutineSQL: "/*M!50003 DROP TABLE items; */ CREATE TRIGGER t_maria_pwn BEFORE INSERT ON items FOR EACH ROW SET @a=1",
			},
		},
		{
			name: "routine_maria_definer",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_maria_def", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: "CREATE /*M!50017 DEFINER='root'@'localhost' */ PROCEDURE p_maria_def() BEGIN SELECT 1; END",
			},
		},
		{
			name: "routine_maria_backslash_close_definer",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p10", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: "CREATE PROCEDURE p10() SQL /*M!50000 SECURITY */ /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END",
			},
		},
	} {
		if _, err := BuildObjectActionSQLsForEngine("mariadb", DialectMySQL, tc.req); err == nil {
			t.Fatalf("case %s: expected BuildObjectActionSQLsForEngine(\"mariadb\", ...) to reject %+v", tc.name, tc.req)
		}
	}

	if sqls, err := BuildObjectActionSQLsForEngine("mariadb", DialectMySQL, driver.ObjectActionRequest{
		Database: "app", ObjectName: "v_maria_ok", ObjectType: "VIEW", Action: "CREATE_VIEW",
		WebMode: true, IsAdmin: false,
		ViewSQL: "CREATE VIEW v_maria_ok AS SELECT /*M!50001 'ok' */ 1",
	}); err != nil || len(sqls) != 1 {
		t.Fatalf("expected benign MariaDB CREATE_VIEW to pass, got err=%v sqls=%#v", err, sqls)
	}
}

func TestIter6NestedExecCommentSecurityGuards(t *testing.T) {
	// 1. Nested executable-comment SQL SECURITY DEFINER & direct unclosed-quote clamping regressions
	for _, tc := range []struct {
		name string
		eng  string
		sql  string
		want bool
	}{
		{"mysql_nest_bs_1close", "mysql", "CREATE PROCEDURE p1() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql_nest_bs_2close", "mysql", "CREATE PROCEDURE p2() SQL /*!50000 SECURITY /*!99999 ' \\*/ DEFINER /* ' */ */ BEGIN SELECT 1; END", true},
		{"mysql_nest_nobs_1close", "mysql", "CREATE PROCEDURE p3() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql_nest_nobs_2close", "mysql", "CREATE PROCEDURE p4() SQL /*!50000 SECURITY /*!99999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END", true},
		{"mysql_nest_dbl_nobs", "mysql", "CREATE PROCEDURE p5() SQL /*!50000 SECURITY /*!99999 \" */ DEFINER /* \" */ */ BEGIN SELECT 1; END", true},
		{"mysql_nest_bt_nobs", "mysql", "CREATE PROCEDURE p6() SQL /*!50000 SECURITY /*!99999 ` */ DEFINER /* ` */ */ BEGIN SELECT 1; END", true},
		{"maria_nest_nobs_2close", "mariadb", "CREATE PROCEDURE pm1() SQL /*M!50000 SECURITY /*M!999999 ' */ DEFINER /* ' */ */ BEGIN SELECT 1; END", true},
		{"maria_nest_bs_1close", "mariadb", "CREATE PROCEDURE pm2() SQL /*M!50000 SECURITY /*M!999999 ' \\*/ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql_direct_unclosed", "mysql", "CREATE PROCEDURE pu() SQL /*!50000 SECURITY ' */ DEFINER /* ' */ BEGIN SELECT 1; END", true},
		{"mysql_closed_inner_99999", "mysql", "CREATE PROCEDURE pc1() /*!50000 SQL SECURITY /*!99999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"mysql_low_inner_50000", "mysql", "CREATE PROCEDURE pc2() /*!50000 /*!50000 ' */ ' SQL SECURITY DEFINER BEGIN SELECT 1; END", true},
		{"mysql_benign_ok", "mysql", "CREATE PROCEDURE p_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END", false},
		{"maria_benign_ok", "mariadb", "CREATE PROCEDURE pm_ok() COMMENT /*M!50000 'ok' */ BEGIN SELECT 1; END", false},
	} {
		if got := HasSQLDefinerClauseForEngine(tc.eng, tc.sql); got != tc.want {
			t.Fatalf("%s (%s): HasSQLDefinerClauseForEngine(%q) = %v, want %v", tc.name, tc.eng, tc.sql, got, tc.want)
		}
	}

	// 2. Read-only nested executable-comment locking reads & LOAD_FILE + benign 'ok'
	for _, tc := range []struct {
		eng string
		sql string
	}{
		{"mysql", "SELECT * FROM users /*!50000 FOR /*!99999 ' */ SHARE /* ' */ */"},
		{"mariadb", "SELECT * FROM users /*M!50000 FOR /*M!999999 ' */ SHARE /* ' */ */"},
		{"mysql", "SELECT 1 /*!50000 /*!99999 ' */ , LOAD_FILE('/etc/passwd') /* ' */ */"},
		{"mysql", "SELECT * FROM users /*!50000 FOR ' */ SHARE /* ' */"},
	} {
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", tc.eng, tc.sql)
		}
		if ok, _ := IsReadOnlySafeStatementForEngine(tc.eng, tc.sql); ok {
			t.Fatalf("engine %q: expected IsReadOnlySafeStatementForEngine to reject %q", tc.eng, tc.sql)
		}
	}
	for _, eng := range []string{"mysql", "mariadb"} {
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT /*!50000 'ok' */ AS val"); err != nil {
			t.Fatalf("engine %q: expected benign /*!50000 'ok' */ to pass, got %v", eng, err)
		}
	}
}

func TestIter7VersionCutoffAndMultiStmtExecComments(t *testing.T) {
	// 1. Version-mismatched sequential & nested SQL SECURITY DEFINER payloads + benign controls
	definerCases := []struct {
		name string
		eng  string
		sql  string
		want bool
	}{
		{"ps1", "mysql", "CREATE PROCEDURE ps1() /*!50000 SQL SECURITY */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"ps1_dq", "mysql", `CREATE PROCEDURE p_dq() /*!50000 SQL SECURITY */ /*!99999 "b" */ DEFINER BEGIN SELECT 1; END`, true},
		{"ps1_bt", "mysql", "CREATE PROCEDURE p_bt() /*!50000 SQL SECURITY */ /*!99999 `b` */ DEFINER BEGIN SELECT 1; END", true},
		{"ps1_plus", "mysql", "CREATE PROCEDURE p_s3() /*!50000 SQL SECURITY */ /*!99999 + */ DEFINER BEGIN SELECT 1; END", true},
		{"ps2", "mysql", "CREATE PROCEDURE ps2() /*!50000 SQL SECURITY */ /*!99999 1 */ DEFINER BEGIN SELECT 1; END", true},
		{"ps3_maria", "mariadb", "CREATE PROCEDURE ps3() /*M!50000 SQL SECURITY */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"p_50001", "mysql", "CREATE PROCEDURE p_50001() /*!50000 SQL SECURITY */ /*!50001 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"p_unver", "mysql", "CREATE PROCEDURE p_unver() /*! SQL SECURITY */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"ph1", "mysql", "CREATE PROCEDURE ph1() /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"ph2_maria", "mariadb", "CREATE PROCEDURE ph2() /*M!50000 SQL SECURITY /*M!999999 'a' */ /*M!999999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"ph3", "mysql", `CREATE PROCEDURE ph3() /*!50000 SQL SECURITY /*!99999 "a" */ /*!99999 "b" */ DEFINER BEGIN SELECT 1; END`, true},
		{"ph4", "mysql", "CREATE PROCEDURE ph4() /*!50000 SQL SECURITY /*!99999 `a` */ /*!99999 `b` */ DEFINER BEGIN SELECT 1; END", true},
		{"ph5", "mysql", "CREATE PROCEDURE ph5() /*!50000 SQL SECURITY /* ' */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"fh1", "mysql", "CREATE FUNCTION fh1() RETURNS INT DETERMINISTIC /*!50000 SQL SECURITY /*!99999 'a' */ /*!99999 'b' */ DEFINER RETURN 1", true},
		{"B1", "mysql", "CREATE PROCEDURE p_adj1() SQL /*!50000 SECURITY */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"B2", "mysql", "CREATE FUNCTION f_adj1() RETURNS INT DETERMINISTIC SQL /*!50000 SECURITY */ /*!99999 'a' */ DEFINER RETURN 1", true},
		{"B3", "mysql", `CREATE PROCEDURE p_adj2() SQL /*!50000 SECURITY */ /*!99999 "a" */ DEFINER BEGIN SELECT 1; END`, true},
		{"B4", "mysql", "CREATE PROCEDURE p_adj3() SQL /*!50000 SECURITY */ /*!99999 `a` */ DEFINER BEGIN SELECT 1; END", true},
		{"B5", "mysql", "CREATE PROCEDURE p_adj4() SQL /*!50000 SECURITY /* x */ /*!99999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"B6_maria", "mariadb", "CREATE PROCEDURE pm_adj1() SQL /*M!50000 SECURITY */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"B7_maria", "mariadb", "CREATE PROCEDURE pm_adj2() SQL /*M!50000 SECURITY /* x */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"p_num", "mysql", "CREATE PROCEDURE p_num() SQL /*!50000 SECURITY */ /*!99999 1 */ DEFINER BEGIN SELECT 1; END", true},
		{"p_reset", "mysql", "CREATE PROCEDURE p_reset() /*!50000 SQL SECURITY /*!99999 */ /*!99999 'b' */ DEFINER BEGIN SELECT 1; END", true},
		{"p_omg", "mysql", "CREATE PROCEDURE p_omg() /*!50000 SQL SECURITY /* x */ /*!50000 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"pm_mix_maria", "mariadb", "CREATE PROCEDURE pm_mix() /*M!100300 SQL SECURITY */ /*!80000 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"benign_50000_ok", "mysql", "CREATE PROCEDURE p_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END", false},
		{"benign_50001_bs", "mysql", "CREATE PROCEDURE p_bs() COMMENT /*!50001 'abc\\\\' */ BEGIN SELECT 1; END", false},
		{"benign_m50003_view", "mariadb", "CREATE /*M!50003 ALGORITHM=UNDEFINED */ VIEW v_ok AS SELECT 1", false},
		{"benign_m100001_ok", "mariadb", "CREATE PROCEDURE pm_ok() COMMENT /*M!100001 'ok' */ BEGIN SELECT 1; END", false},
	}
	for _, tc := range definerCases {
		if got := HasSQLDefinerClauseForEngine(tc.eng, tc.sql); got != tc.want {
			t.Fatalf("%s (%s): HasSQLDefinerClauseForEngine = %v, want %v", tc.name, tc.eng, got, tc.want)
		}
	}

	// 2. Version-mismatched FOR SHARE locking-read bypasses
	for _, tc := range []struct {
		name string
		eng  string
		sql  string
	}{
		{"mysql_for_share", "mysql", "SELECT * FROM users /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"maria_for_share", "mariadb", "SELECT * FROM users /*M!50000 FOR */ /*M!999999 x */ SHARE"},
		{"mysql_for_share_50001", "mysql", "SELECT * FROM users /*!50000 FOR */ /*!50001 x */ SHARE"},
		{"mysql_for_share_plain", "mysql", "SELECT * FROM users /*!50000 FOR /* x */ /*!99999 x */ SHARE"},
		{"maria_for_share_mix", "mariadb", "SELECT * FROM users /*M!100300 FOR */ /*!80000 x */ SHARE"},
	} {
		if ok, _ := IsReadOnlySafeStatementForEngine(tc.eng, tc.sql); ok {
			t.Fatalf("%s (%s): expected IsReadOnlySafeStatementForEngine to reject %q", tc.name, tc.eng, tc.sql)
		}
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err == nil {
			t.Fatalf("%s (%s): expected ValidateReadOnlyScriptForEngine to reject %q", tc.name, tc.eng, tc.sql)
		}
	}

	// 3. Multi-statement split bypasses via /*!99999 BEGIN */ / /*M!999999 BEGIN */ in BuildObjectActionSQLsForEngine
	for _, tc := range []struct {
		name string
		eng  string
		req  driver.ObjectActionRequest
	}{
		{
			name: "mysql_routine_99999_begin",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_b1", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_b1() /*!99999 BEGIN */ SELECT 1; DROP TABLE users;",
			},
		},
		{
			name: "maria_routine_999999_begin",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_b2", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_b2() /*M!999999 BEGIN */ SELECT 1; DROP TABLE users;",
			},
		},
		{
			name: "mysql_routine_mixed_begin_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_b3", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_b3() /*!99999 BEGIN */ /*!50000 SELECT 1; DROP TABLE users; */",
			},
		},
		{
			name: "mysql_trigger_99999_begin",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "t_b1", ObjectType: "TRIGGER", Action: "CREATE_TRIGGER",
				RoutineSQL: "CREATE TRIGGER t_b1 BEFORE INSERT ON items FOR EACH ROW /*!99999 BEGIN */ SET @a=1; DROP TABLE users;",
			},
		},
		{
			name: "maria_trigger_999999_begin",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "t_b2", ObjectType: "TRIGGER", Action: "CREATE_TRIGGER",
				RoutineSQL: "CREATE TRIGGER t_b2 BEFORE INSERT ON items FOR EACH ROW /*M!999999 BEGIN */ SET @a=1; DROP TABLE users;",
			},
		},
		{
			name: "mysql_view_multi_exec",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_b1", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE VIEW v_b1 AS /*!99999 SELECT 1; DROP TABLE users */",
			},
		},
		{
			name: "maria_view_multi_exec",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_b2", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE VIEW v_b2 AS /*M!999999 SELECT 1; DROP TABLE users */",
			},
		},
	} {
		if _, err := BuildObjectActionSQLsForEngine(tc.eng, DialectMySQL, tc.req); err == nil {
			t.Fatalf("%s (%s): expected BuildObjectActionSQLsForEngine to reject multi-statement exec comment bypass", tc.name, tc.eng)
		}
	}

	// Benign object actions continue to succeed
	for _, tc := range []struct {
		name string
		eng  string
		req  driver.ObjectActionRequest
	}{
		{
			name: "benign_mysql_proc_ok",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_ok", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: "CREATE PROCEDURE p_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END",
			},
		},
		{
			name: "benign_mysql_proc_bs",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_bs", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: `CREATE PROCEDURE p_bs() COMMENT /*!50001 'abc\\' */ BEGIN SELECT 1; END`,
			},
		},
		{
			name: "benign_maria_view",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_ok", ObjectType: "VIEW", Action: "CREATE_VIEW",
				WebMode: true, IsAdmin: false,
				ViewSQL: "CREATE /*M!50003 ALGORITHM=UNDEFINED */ VIEW v_ok AS SELECT 1",
			},
		},
		{
			name: "benign_maria_proc_100001",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "pm_ok", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: "CREATE PROCEDURE pm_ok() COMMENT /*M!100001 'ok' */ BEGIN SELECT 1; END",
			},
		},
	} {
		if sqls, err := BuildObjectActionSQLsForEngine(tc.eng, DialectMySQL, tc.req); err != nil || len(sqls) == 0 {
			t.Fatalf("%s (%s): expected benign DDL to pass, got err=%v sqls=%#v", tc.name, tc.eng, err, sqls)
		}
	}
}

func TestIter8ExecCommentHardeningRegressions(t *testing.T) {
	makeDummy := func(prefix string, start, count int, body string) string {
		var sb strings.Builder
		for i := 0; i < count; i++ {
			fmt.Fprintf(&sb, "%s%d%s*/ ", prefix, start+i, body)
		}
		return sb.String()
	}

	emptyMySQL35 := makeDummy("/*!", 10001, 35, "")
	exprMySQL40 := makeDummy("/*!", 10001, 40, " 1")
	emptyMaria35 := makeDummy("/*M!", 10001, 35, "")
	exprMaria40 := makeDummy("/*M!", 10001, 40, " 1")
	crossMySQL5 := makeDummy("/*!", 10001, 5, "")
	crossMaria4 := makeDummy("/*M!", 10001, 4, "")
	crossExprMySQL5 := makeDummy("/*!", 10001, 5, " 1")
	crossExprMaria4 := makeDummy("/*M!", 10001, 4, " 1")

	// 1. parseExecCommentHeader: 5 digits for /*! vs 6 digits for /*M!
	if adv, ver, isMaria, ok := parseExecCommentHeader([]byte("*!500001*/")); !ok || isMaria || adv != 7 || ver != 50000 {
		t.Fatalf("unexpected parseExecCommentHeader for /*!500001*/: adv=%d ver=%d isMaria=%v ok=%v", adv, ver, isMaria, ok)
	}
	if adv, ver, isMaria, ok := parseExecCommentHeader([]byte("*M!1003001*/")); !ok || !isMaria || adv != 9 || ver != 100300 {
		t.Fatalf("unexpected parseExecCommentHeader for /*M!1003001*/: adv=%d ver=%d isMaria=%v ok=%v", adv, ver, isMaria, ok)
	}
	if stmts := SplitStatementsForEngine("mysql", "SELECT /*!500001*/ AS v"); len(stmts) != 1 {
		t.Fatalf("expected /*!500001*/ to split into 1 statement, got %#v", stmts)
	}
	if ok, reason := IsReadOnlySafeStatementForEngine("mysql", "SELECT /*!500001*/ AS v"); !ok {
		t.Fatalf("expected SELECT /*!500001*/ AS v to be read-only safe, got %s", reason)
	}

	// 2. HasSQLDefinerClauseForEngine: preserveExecQuotes clamping, 35+ empty, 40 expr, and MariaDB cross-family >16
	for _, tc := range []struct {
		name string
		eng  string
		sql  string
		want bool
	}{
		{"pq_definer_clamp", "mysql", "CREATE PROCEDURE p() COMMENT /*!50000 ' */ ' */ /*!50000 SQL SECURITY */ /*!80000 x */ DEFINER BEGIN SELECT 1; END", true},
		{"pq_definer_chain", "mysql", "CREATE PROCEDURE p() COMMENT /*!50000 ' */ ' */ /*!80000 ' */ /*!55000 SQL SECURITY */ DEFINER BEGIN SELECT 1; END", true},
		{"pq_definer_eq", "mysql", "CREATE PROCEDURE p() COMMENT /*!50000 ' */ ' */ /*!50000 DEFINER */ /*!80000 x */ = 'root'@'localhost' BEGIN SELECT 1; END", true},
		{"cap35_empty_sec_def", "mysql", "CREATE PROCEDURE p() " + emptyMySQL35 + " /*!50000 SQL SECURITY */ /*!99999 'x' */ DEFINER BEGIN SELECT 1; END", true},
		{"cap40_expr_sec_def", "mysql", "CREATE PROCEDURE p() " + exprMySQL40 + " /*!50000 SQL SECURITY */ /*!99999 'x' */ DEFINER BEGIN SELECT 1; END", true},
		{"cap35_empty_def_eq", "mysql", "CREATE PROCEDURE p() " + emptyMySQL35 + " /*!50000 DEFINER */ /*!99999 'x' */ = 'root'@'localhost' BEGIN SELECT 1; END", true},
		{"cap35_maria_empty", "mariadb", "CREATE PROCEDURE p() " + emptyMaria35 + " SQL /*M!50000 SECURITY */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"cap40_maria_expr", "mariadb", "CREATE PROCEDURE p() " + exprMaria40 + " SQL /*M!50000 SECURITY */ /*M!999999 'a' */ DEFINER BEGIN SELECT 1; END", true},
		{"maria_cross_5x4_empty", "mariadb", "CREATE PROCEDURE p() " + crossMySQL5 + crossMaria4 + " /*M!100300 SQL SECURITY */ /*!80000 x */ /*!50000 DEFINER */ BEGIN SELECT 1; END", true},
		{"maria_cross_5x4_expr", "mariadb", "CREATE PROCEDURE p() " + crossExprMySQL5 + crossExprMaria4 + " /*M!100300 SECURITY */ /*!80000 'x' */ /*!50000 DEFINER */ BEGIN SELECT 1; END", true},
		{"benign_35_empty", "mysql", "CREATE PROCEDURE p_ok() " + emptyMySQL35 + " COMMENT 'ok' BEGIN SELECT 1; END", false},
		{"benign_40_expr", "mysql", "CREATE PROCEDURE p_ok() BEGIN SELECT " + exprMySQL40 + " 1; END", false},
		{"benign_same_ver_sep", "mysql", "CREATE PROCEDURE p_ok() COMMENT /*!50000 'ok' */ BEGIN SELECT 1; END", false},
	} {
		if got := HasSQLDefinerClauseForEngine(tc.eng, tc.sql); got != tc.want {
			t.Fatalf("%s (%s): HasSQLDefinerClauseForEngine = %v, want %v", tc.name, tc.eng, got, tc.want)
		}
	}

	// 3. Read-only guard: preserveExecQuotes clamping, 35+ empty, 40 expr, disallowed starter, call-only fn, MariaDB 5x4
	for _, tc := range []struct {
		name string
		eng  string
		sql  string
	}{
		{"pq_for_share", "mysql", "SELECT * FROM users WHERE 'a' = /*!50000 ' */ ' */ /*!50000 FOR */ /*!80000 x */ SHARE"},
		{"pq_for_update", "mysql", "SELECT * FROM users WHERE 'a' = /*!50000 ' */ ' */ /*!50000 FOR */ /*!80000 x */ UPDATE"},
		{"cap35_empty_for_share", "mysql", "SELECT " + emptyMySQL35 + " * FROM users /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"cap40_expr_for_share", "mysql", "SELECT * FROM users WHERE 1 = " + exprMySQL40 + " 1 /*!50000 FOR */ /*!99999 x */ SHARE"},
		{"cap35_maria_for_share", "mariadb", "SELECT " + emptyMaria35 + " * FROM users /*M!50000 FOR */ /*M!999999 x */ SHARE"},
		{"maria_cross_5x4_for_share", "mariadb", "SELECT " + crossMySQL5 + crossMaria4 + " * FROM users /*M!100300 FOR */ /*!80000 x */ /*!50000 SHARE */"},
		{"maria_cross_5x4_expr_for_share", "mariadb", "SELECT * FROM users WHERE 1 = " + crossExprMySQL5 + crossExprMaria4 + " 1 /*!50000 FOR */ /*!80000 x */ /*M!100000 SHARE */"},
		{"cap40_disallowed_starter", "mysql", exprMySQL40 + " /*!99999 SELECT */ /*!50000 FLUSH PRIVILEGES */"},
		{"cap40_callonly_fn", "mysql", "SELECT " + exprMySQL40 + " /*!50000 FILE */ /*!99999 x */ ('/etc/passwd')"},
	} {
		if ok, _ := IsReadOnlySafeStatementForEngine(tc.eng, tc.sql); ok {
			t.Fatalf("%s (%s): expected IsReadOnlySafeStatementForEngine to reject", tc.name, tc.eng)
		}
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err == nil {
			t.Fatalf("%s (%s): expected ValidateReadOnlyScriptForEngine to reject", tc.name, tc.eng)
		}
	}

	for _, tc := range []struct {
		name string
		eng  string
		sql  string
	}{
		{"benign_35_empty", "mysql", "SELECT " + emptyMySQL35 + " 'ok' AS v"},
		{"benign_40_expr", "mysql", "SELECT " + exprMySQL40 + " 1 AS v"},
		{"benign_50000_ok", "mysql", "SELECT /*!50000 'ok' */ AS v"},
		{"benign_50001_bs", "mysql", `SELECT /*!50001 'abc\\' */ AS v`},
		{"benign_maria_100001", "mariadb", "SELECT /*M!100001 'ok' */ AS v"},
	} {
		if ok, reason := IsReadOnlySafeStatementForEngine(tc.eng, tc.sql); !ok {
			t.Fatalf("%s (%s): expected IsReadOnlySafeStatementForEngine to pass, got %s", tc.name, tc.eng, reason)
		}
		if err := ValidateReadOnlyScriptForEngine(tc.eng, tc.sql); err != nil {
			t.Fatalf("%s (%s): expected ValidateReadOnlyScriptForEngine to pass, got %v", tc.name, tc.eng, err)
		}
	}

	// 4. SplitSingleStatementForEngine & ValidateExpectedDDLTargetForEngine + BuildObjectActionSQLsForEngine
	for _, tc := range []struct {
		name string
		eng  string
		req  driver.ObjectActionRequest
	}{
		{
			name: "pq_routine_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_pq", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_pq() COMMENT /*!50000 ' */ ' */ /*!80000 BEGIN */ SELECT 1; DROP TABLE users;",
			},
		},
		{
			name: "pq_chained_unclosed_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_pq2", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_pq2() COMMENT /*!50000 ' */ ' */ /*!80000 ' */ /*!99999 BEGIN */ SELECT 1; DROP TABLE users;",
			},
		},
		{
			name: "cap35_empty_routine_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_c35", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_c35() " + emptyMySQL35 + " /*!99999 BEGIN */ SELECT 1 /*!50000 ; DROP TABLE users */",
			},
		},
		{
			name: "cap40_expr_routine_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_c40", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_c40() BEGIN SELECT " + exprMySQL40 + " 1; END; DROP TABLE users",
			},
		},
		{
			name: "cap40_expr_begin_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_c40b", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_c40b() /*!99999 BEGIN */ SELECT " + exprMySQL40 + " 1 /*!50000 ; DROP TABLE users */",
			},
		},
		{
			name: "cap35_empty_trigger_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "t_c35", ObjectType: "TRIGGER", Action: "CREATE_TRIGGER",
				RoutineSQL: "CREATE TRIGGER t_c35 BEFORE INSERT ON items FOR EACH ROW " + emptyMySQL35 + " /*!99999 BEGIN */ SET @a=1 /*!50000 ; DROP TABLE users */",
			},
		},
		{
			name: "cap35_empty_view_split",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_c35", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE VIEW v_c35 AS SELECT " + emptyMySQL35 + " 1 /*!50000 ; DROP TABLE users */",
			},
		},
		{
			name: "maria_cross_5x4_structural_split",
			eng:  "mariadb",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_mc", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				RoutineSQL: "CREATE PROCEDURE p_mc() " + makeDummy("/*!", 10001, 5, " IF") + makeDummy("/*M!", 10001, 4, " END IF") + " /*!50000 BEGIN */ /*M!100300 END */ /*!80000 BEGIN */ SELECT 1 /*!50000 ; DROP TABLE users */",
			},
		},
		{
			name: "target_spoof_35_empty",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_sp", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE " + emptyMySQL35 + " /*!50000 /* */ TABLE pwn(x INT) */ VIEW v_sp AS SELECT 1",
			},
		},
		{
			name: "target_spoof_pq_definer",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_sp2", ObjectType: "VIEW", Action: "CREATE_VIEW",
				ViewSQL: "CREATE /*!50000 DEFINER = */ /*!80000 ' */ /*!50000 root@localhost PROCEDURE pwn() BEGIN SELECT 1; END -- */ /*!80000 ' */ VIEW v_sp2 AS SELECT 1",
			},
		},
	} {
		if _, err := BuildObjectActionSQLsForEngine(tc.eng, DialectMySQL, tc.req); err == nil {
			t.Fatalf("%s (%s): expected BuildObjectActionSQLsForEngine to reject", tc.name, tc.eng)
		}
	}

	// Benign 35 empty & 40 expr DDL controls still succeed
	for _, tc := range []struct {
		name string
		eng  string
		req  driver.ObjectActionRequest
	}{
		{
			name: "benign_35_empty_proc",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "p_ok35", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
				WebMode: true, IsAdmin: false,
				RoutineSQL: "CREATE PROCEDURE p_ok35() " + emptyMySQL35 + " COMMENT 'ok' BEGIN SELECT 1; END",
			},
		},
		{
			name: "benign_40_expr_view",
			eng:  "mysql",
			req: driver.ObjectActionRequest{
				Database: "app", ObjectName: "v_ok40", ObjectType: "VIEW", Action: "CREATE_VIEW",
				WebMode: true, IsAdmin: false,
				ViewSQL: "CREATE VIEW v_ok40 AS SELECT " + exprMySQL40 + " 1 AS x",
			},
		},
	} {
		if sqls, err := BuildObjectActionSQLsForEngine(tc.eng, DialectMySQL, tc.req); err != nil || len(sqls) == 0 {
			t.Fatalf("%s (%s): expected benign DDL to pass, got err=%v sqls=%#v", tc.name, tc.eng, err, sqls)
		}
	}

	// 5. Fail-closed behavior on structural version cap overflow (>64 distinct structural versions & >64 MariaDB structural cross-pairs)
	overflowMySQL := "SELECT " + makeDummy("/*!", 10001, 66, " ;") + " 1"
	if ok, _ := IsReadOnlySafeStatementForEngine("mysql", overflowMySQL); ok {
		t.Fatalf("expected IsReadOnlySafeStatementForEngine to fail closed on >64 structural versions")
	}
	if err := ValidateReadOnlyScriptForEngine("mysql", overflowMySQL); err == nil {
		t.Fatalf("expected ValidateReadOnlyScriptForEngine to fail closed on >64 structural versions")
	}
	if _, err := SplitSingleStatementForEngine("mysql", "CREATE PROCEDURE p_ov() "+makeDummy("/*!", 10001, 66, " BEGIN")+" BEGIN SELECT 1; END"); err == nil {
		t.Fatalf("expected SplitSingleStatementForEngine to fail closed on >64 structural versions")
	}
	if !HasSQLDefinerClauseForEngine("mysql", "CREATE PROCEDURE p_ov() "+makeDummy("/*!", 10001, 66, " ;")+" BEGIN SELECT 1; END") {
		t.Fatalf("expected HasSQLDefinerClauseForEngine to fail closed (return true) on >64 structural versions")
	}
	if err := ValidateExpectedDDLTargetForEngine("mysql", "CREATE VIEW v_ov AS SELECT "+makeDummy("/*!", 10001, 66, " ;")+" 1", []string{"CREATE"}, "VIEW"); err == nil {
		t.Fatalf("expected ValidateExpectedDDLTargetForEngine to fail closed on >64 structural versions")
	}

	overflowMariaCross := "SELECT " + makeDummy("/*!", 10001, 9, " ;") + makeDummy("/*M!", 10001, 9, " ;") + " 1"
	if err := ValidateReadOnlyScriptForEngine("mariadb", overflowMariaCross); err == nil {
		t.Fatalf("expected ValidateReadOnlyScriptForEngine to fail closed on >64 MariaDB structural cross-pairs")
	}
}

func TestIter9MySQLDashCommentRequiresSpaceRegressions(t *testing.T) {
	mysqlEngines := []string{"mysql", "mariadb", "tidb", "oceanbase"}
	ansiEngines := []string{"postgres", "kingbase", "duckdb", "clickhouse", "sqlite", "sqlserver", "oracle", "dameng"}

	// 1. Malicious '--<non-whitespace>' and '--- '\'' payloads must be blocked on all MySQL-family engines
	blockedPayloads := []struct {
		name string
		sql  string
	}{
		{"dd1_for_update", "SELECT * FROM users WHERE id = 1 --1 FOR UPDATE"},
		{"dd1_for_share", "SELECT * FROM users WHERE id = 1 --1 FOR SHARE"},
		{"dd1_outfile", "SELECT 1 --1 INTO OUTFILE '/tmp/pwn.txt'"},
		{"dd1_load_file", "SELECT 1 --1 UNION SELECT LOAD_FILE('/etc/passwd')"},
		{"dd1_stacked", "SELECT 1 --1; DROP TABLE users"},
		{"dda_for_update", "SELECT 1 --a FOR UPDATE"},
		{"ddplus_for_update", "SELECT 1 --+1 FOR UPDATE"},
		{"ddparen_for_update", "SELECT 1 --(1) FOR UPDATE"},
		{"ddquote_for_update", "SELECT 1 --'x' FOR UPDATE"},
		{"ddd1_for_update", "SELECT * FROM users WHERE id = 1 ---1 FOR UPDATE"},
		{"ddd_quote_for_update", "SELECT 1 --- '\n 1 FOR UPDATE"},
		{"ddd_quote_stacked", "SELECT 1 --- '\n 1; DROP TABLE users"},
		{"ddd_quote_semi_stacked", "SELECT 1 --- '\n; DROP TABLE users"},
		{"dddd_quote_for_update", "SELECT 1---- '\n 1 FOR UPDATE"},
		{"ddd_quote_outfile", "SELECT 1 --- '\n 1 INTO OUTFILE '/tmp/pwn.txt'"},
		{"exec_dd1_newline_for_update", "/*!50000 SELECT 1 --1\n FOR UPDATE */"},
		{"exec_ddd_quote_stacked", "/*!50000 SELECT 1 --- '\n 1; DROP TABLE users */"},
		{"exec_ddd_quote_for_update", "/*!50000 SELECT 1 --- '\n 1 FOR UPDATE */"},
		{"exec_space_close_for_update", "SELECT 1 /*!50000 -- */ FOR UPDATE"},
		{"exec_nospace_close_for_update", "SELECT 1 /*!50000 --*/ FOR UPDATE"},
		{"del_0x7f_for_update", "SELECT 1 --\x7f'\n1 FOR UPDATE"},
		{"del_0x7f_stacked", "SELECT 1 --\x7f'\n1; DROP TABLE users"},
		{"exec_dash_nl_stacked", "SELECT 1 /*!50000 -- ' */ '\n */ ; DROP TABLE users"},
		{"exec_hash_nl_for_update", "SELECT 1 /*!50000 # ' */ '\n */ FOR UPDATE"},
		{"exec_dash_nl_ver_cutoff", "SELECT 1 /*!50000 -- ' */ '\n */ /*!80000 ' */ ; DROP TABLE users /* ' */"},
	}
	for _, eng := range mysqlEngines {
		for _, tc := range blockedPayloads {
			if ok, _ := IsReadOnlySafeStatementForEngine(eng, tc.sql); ok {
				t.Fatalf("%s (%s): expected IsReadOnlySafeStatementForEngine to reject %q", tc.name, eng, tc.sql)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, tc.sql); err == nil {
				t.Fatalf("%s (%s): expected ValidateReadOnlyScriptForEngine to reject %q", tc.name, eng, tc.sql)
			}
		}
	}

	// 2. Multi-statement split & DEFINER bypasses with '--1', '--- '\n', '--\x7f'\n', and active exec '--'/'#'
	for _, eng := range mysqlEngines {
		for _, badSplit := range []struct {
			name string
			sql  string
		}{
			{"proc_dd1", "CREATE PROCEDURE p_dd1() BEGIN SELECT 1 --1; END; DROP TABLE users"},
			{"view_dd1", "CREATE VIEW v_dd1 AS SELECT 1 --1; DROP TABLE users"},
			{"view_dddq", "CREATE VIEW v_dddq AS SELECT 1 --- '\n1; DROP TABLE users"},
			{"proc_ddd1", "CREATE PROCEDURE p_ddd1() BEGIN SELECT 1 --- '\n 1; END; DROP TABLE users"},
			{"view_del", "CREATE VIEW v_del AS SELECT 1 --\x7f'\n1; DROP TABLE users"},
			{"proc_exd", "CREATE PROCEDURE p_exd() BEGIN SELECT 1 /*!50000 -- ' */ '\n */ ; END; DROP TABLE users"},
			{"proc_exh", "CREATE PROCEDURE p_exh() BEGIN SELECT 1 /*!50000 # ' */ '\n */ ; END; DROP TABLE users"},
		} {
			if _, err := SplitSingleStatementForEngine(eng, badSplit.sql); err == nil {
				t.Fatalf("%s (%s): expected SplitSingleStatementForEngine to reject %q", badSplit.name, eng, badSplit.sql)
			}
		}
		if _, err := BuildObjectActionSQLsForEngine(eng, DialectMySQL, driver.ObjectActionRequest{
			Database: "app", ObjectName: "p_dd1", ObjectType: "PROCEDURE", Action: "CREATE_ROUTINE",
			RoutineSQL: "CREATE PROCEDURE p_dd1() BEGIN SELECT 1 --1; END; DROP TABLE users",
		}); err == nil {
			t.Fatalf("%s: expected BuildObjectActionSQLsForEngine CREATE_ROUTINE to reject --1 multi-stmt", eng)
		}
		if _, err := BuildObjectActionSQLsForEngine(eng, DialectMySQL, driver.ObjectActionRequest{
			Database: "app", ObjectName: "v_dd1", ObjectType: "VIEW", Action: "CREATE_VIEW",
			ViewSQL: "CREATE VIEW v_dd1 AS SELECT 1 --1; DROP TABLE users",
		}); err == nil {
			t.Fatalf("%s: expected BuildObjectActionSQLsForEngine CREATE_VIEW to reject --1 multi-stmt", eng)
		}
		for _, defSQL := range []string{
			"CREATE PROCEDURE p_def_dd1() SELECT 1 --1 SQL SECURITY DEFINER\n BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_ddd_def() SELECT 1 --- '\n 1 SQL SECURITY DEFINER BEGIN SELECT 1; END",
			"CREATE PROCEDURE p_exd_def() SELECT 1 /*!50000 -- ' */ '\n */ SQL SECURITY DEFINER BEGIN SELECT 1; END",
		} {
			if !HasSQLDefinerClauseForEngine(eng, defSQL) {
				t.Fatalf("%s: expected HasSQLDefinerClauseForEngine to catch SQL SECURITY DEFINER in %q", eng, defSQL)
			}
		}
	}

	// 3. StripLeadingSQLCommentsForEngine, inspectExecCommentInterior & hasExecutableContentWithOpts edge cases
	mysqlOpts := SyntaxOptionsForEngine("mysql")
	if got := StripLeadingSQLCommentsForEngine("mysql", "--1\nSELECT 1"); got != "--1\nSELECT 1" {
		t.Fatalf("expected MySQL StripLeadingSQLCommentsForEngine not to strip --1, got %q", got)
	}
	if got := StripLeadingSQLCommentsForEngine("mysql", "--\x7f'\nSELECT 1"); got != "SELECT 1" {
		t.Fatalf("expected MySQL StripLeadingSQLCommentsForEngine to strip --\\x7f comment, got %q", got)
	}
	if IsQueryStatementForEngine("mysql", "--1\nSELECT 1") {
		t.Fatalf("expected MySQL IsQueryStatementForEngine not to treat --1 as leading comment")
	}
	for _, commentLead := range []string{"-- comment\nSELECT 1", "--\tcomment\nSELECT 1", "--\nSELECT 1", "--\r\nSELECT 1", "--\vcomment\nSELECT 1", "--\fcomment\nSELECT 1", "--\x7fcomment\nSELECT 1", "/*!50000 -- */ SELECT 1", "/*!50000 -- */\nSELECT 1"} {
		if !IsQueryStatementForEngine("mysql", commentLead) {
			t.Fatalf("expected MySQL IsQueryStatementForEngine to strip valid comment %q", commentLead)
		}
	}
	if !hasExecutableContentWithOpts("--1", mysqlOpts) || !hasExecutableContentWithOpts("---", mysqlOpts) {
		t.Fatalf("expected MySQL hasExecutableContentWithOpts to return true for --1 and ---")
	}
	if hasExecutableContentWithOpts("--", mysqlOpts) || hasExecutableContentWithOpts("-- ", mysqlOpts) || hasExecutableContentWithOpts("--\t", mysqlOpts) || hasExecutableContentWithOpts("--\x7f", mysqlOpts) {
		t.Fatalf("expected MySQL hasExecutableContentWithOpts to return false for empty -- comment")
	}
	if !hasExecutableContentWithOpts("/*!50000 -- */ SELECT 1", mysqlOpts) {
		t.Fatalf("expected MySQL hasExecutableContentWithOpts to clamp -- comment at */ inside exec comment")
	}
	if !hasExecutableContentWithOpts("/*!50000 -- */\nSELECT 1", mysqlOpts) {
		t.Fatalf("expected MySQL hasExecutableContentWithOpts to return true for /*!50000 -- */\\nSELECT 1")
	}
	if structural, unclosed := inspectExecCommentInterior("/*!50000 --- '\n*/", len("/*!50000"), mysqlOpts); !structural || unclosed {
		t.Fatalf("expected inspectExecCommentInterior(\"/*!50000 --- '\\n*/\") = (true, false), got (%v, %v)", structural, unclosed)
	}
	if _, err := SplitSingleStatementForEngine("mysql", "/*!50000 -- */\nSELECT 1"); err != nil {
		t.Fatalf("expected SplitSingleStatementForEngine to allow /*!50000 -- */\\nSELECT 1, got %v", err)
	}
	if _, err := SplitSingleStatementForEngine("mysql", "CREATE VIEW v_ex_ok AS /*!50000 -- */ SELECT 1"); err != nil {
		t.Fatalf("expected SplitSingleStatementForEngine to allow CREATE VIEW v_ex_ok AS /*!50000 -- */ SELECT 1, got %v", err)
	}

	// 4. Benign MySQL controls & ANSI '--' engines
	benignMySQL := []string{
		"SELECT 1 --1",
		"SELECT * FROM users WHERE id = 1 --- FOR UPDATE",
		"SELECT 1 -- comment\n",
		"SELECT 1 --\tcomment\n",
		"SELECT 1 --\vcomment\n",
		"SELECT 1 --\fcomment\n",
		"SELECT 1 --\x7fcomment\n",
		"SELECT 1 --",
		"SELECT 1 --\tFOR UPDATE",
		"SELECT 1 --\n",
		"/*!50000 -- */ SELECT 1",
		"/*!50000 -- */\nSELECT 1",
	}
	for _, eng := range mysqlEngines {
		for _, q := range benignMySQL {
			if ok, reason := IsReadOnlySafeStatementForEngine(eng, q); !ok {
				t.Fatalf("%s: expected benign query %q to pass IsReadOnlySafeStatementForEngine, got %s", eng, q, reason)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, q); err != nil {
				t.Fatalf("%s: expected benign query %q to pass ValidateReadOnlyScriptForEngine, got %v", eng, q, err)
			}
		}
	}
	for _, eng := range ansiEngines {
		if ok, reason := IsReadOnlySafeStatementForEngine(eng, "SELECT * FROM users WHERE id = 1 --1 FOR UPDATE"); !ok {
			t.Fatalf("%s (ANSI): expected --1 FOR UPDATE to remain a comment, got %s", eng, reason)
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT 1 --1; DROP TABLE users"); err != nil {
			t.Fatalf("%s (ANSI): expected --1; DROP TABLE to remain a comment, got %v", eng, err)
		}
	}
}

func TestIter11BracketAndBacktickDialectScoping(t *testing.T) {
	// 1. Verify SyntaxOptionsForEngine BracketIdentifiers & BacktickIdentifiers across all engines
	for _, eng := range []string{"sqlserver", "mssql"} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.BracketIdentifiers || opts.BacktickIdentifiers {
			t.Fatalf("engine %q: expected BracketIdentifiers=true, BacktickIdentifiers=false, got %+v", eng, opts)
		}
	}
	for _, eng := range []string{"sqlite", ""} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.BracketIdentifiers || !opts.BacktickIdentifiers {
			t.Fatalf("engine %q: expected BracketIdentifiers=true, BacktickIdentifiers=true, got %+v", eng, opts)
		}
	}
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase", "clickhouse"} {
		opts := SyntaxOptionsForEngine(eng)
		if opts.BracketIdentifiers || !opts.BacktickIdentifiers {
			t.Fatalf("engine %q: expected BracketIdentifiers=false, BacktickIdentifiers=true, got %+v", eng, opts)
		}
	}
	for _, eng := range []string{"postgres", "postgresql", "kingbase", "duckdb", "oracle", "dameng", "dm8"} {
		opts := SyntaxOptionsForEngine(eng)
		if opts.BracketIdentifiers || opts.BacktickIdentifiers {
			t.Fatalf("engine %q: expected BracketIdentifiers=false, BacktickIdentifiers=false, got %+v", eng, opts)
		}
	}

	// 2. Bracket-quote smuggle payloads rejected across non-bracket dialects in ContainsUnquotedSQLMetaForEngine & ValidateSQLExpressionForEngine
	nonBracketEngines := []string{
		"postgres", "postgresql", "kingbase", "duckdb",
		"clickhouse", "mysql", "mariadb", "tidb", "oceanbase",
		"oracle", "dameng", "dm8",
	}
	bracketSmuggles := []string{
		"ARRAY['a]b']) ; DROP TABLE users -- '",
		"['a]b'] ; DROP TABLE users -- '",
		"['a]b']); DROP TABLE users -- '",
		"[1; DROP TABLE users; -- ]",
		"[1 /*]",
	}
	for _, eng := range nonBracketEngines {
		for _, payload := range bracketSmuggles {
			if !ContainsUnquotedSQLMetaForEngine(eng, payload) {
				t.Fatalf("engine %q: expected ContainsUnquotedSQLMetaForEngine to reject %q", eng, payload)
			}
			if err := ValidateSQLExpressionForEngine(eng, "expr", payload); err == nil {
				t.Fatalf("engine %q: expected ValidateSQLExpressionForEngine to reject %q", eng, payload)
			}
		}
	}

	// 3. Backtick-quote smuggle payloads rejected on non-backtick dialects
	nonBacktickEngines := []string{"postgres", "postgresql", "kingbase", "duckdb", "oracle", "dameng", "dm8", "sqlserver", "mssql"}
	for _, eng := range nonBacktickEngines {
		for _, payload := range []string{
			"`'`; DROP TABLE users -- `",
			"`a`; DROP TABLE users -- `",
			"`1 /*`",
		} {
			if !ContainsUnquotedSQLMetaForEngine(eng, payload) {
				t.Fatalf("engine %q: expected ContainsUnquotedSQLMetaForEngine to reject backtick smuggle %q", eng, payload)
			}
			if err := ValidateSQLExpressionForEngine(eng, "expr", payload); err == nil {
				t.Fatalf("engine %q: expected ValidateSQLExpressionForEngine to reject backtick smuggle %q", eng, payload)
			}
		}
		if err := ValidateReadOnlyScriptForEngine(eng, "SELECT `a`; DROP TABLE users; SELECT `b`"); err == nil {
			t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject backtick-wrapped multi-statement query", eng)
		}
	}

	// 4. BuildCreateTableDDL & BuildAlterTableSQLs reject bracket & backtick smuggles on non-bracket dialects
	for _, d := range []Dialect{DialectPostgres, DialectClickHouse, DialectMySQL, DialectOracle, DialectDameng} {
		for _, payload := range bracketSmuggles {
			if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
				Database: "app",
				Schema:   "public",
				Table:    "t_pwn",
				Columns: []driver.ColumnDef{
					{Name: "id", Type: "INT", IsPrimaryKey: true},
					{Name: "val", Type: "TEXT", DefaultValue: payload},
				},
			}); err == nil {
				t.Fatalf("dialect %s: expected BuildCreateTableDDL defaultValue to reject %q", d, payload)
			}
			if _, err := BuildAlterTableSQLs(d, driver.AlterTableRequest{
				Database: "app",
				Schema:   "public",
				Table:    "t_pwn",
				Actions: []driver.AlterAction{
					{Action: "ADD_COLUMN", Column: driver.ColumnDef{Name: "v", Type: "TEXT", DefaultValue: payload}},
				},
			}); err == nil {
				t.Fatalf("dialect %s: expected BuildAlterTableSQLs ADD_COLUMN defaultValue to reject %q", d, payload)
			}
		}
		if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
			Database: "app",
			Schema:   "public",
			Table:    "t_ch_pwn",
			Columns:  []driver.ColumnDef{{Name: "id", Type: "INT"}},
			OrderBy:  "['a]b'] ; DROP TABLE users -- '",
		}); err == nil {
			t.Fatalf("dialect %s: expected BuildCreateTableDDL orderBy to reject bracket smuggle", d)
		}
	}

	for _, d := range []Dialect{DialectPostgres, DialectOracle, DialectDameng, DialectSQLServer} {
		if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
			Schema: "public",
			Table:  "t_bt_pwn",
			Columns: []driver.ColumnDef{
				{Name: "id", Type: "INT", IsPrimaryKey: true},
				{Name: "val", Type: "TEXT", DefaultValue: "`'`; DROP TABLE users -- `"},
			},
		}); err == nil {
			t.Fatalf("dialect %s: expected BuildCreateTableDDL defaultValue to reject backtick smuggle", d)
		}
	}

	// 5. BuildBrowseQuery rejects '[1 /*]' and '[1; DROP TABLE users; -- ]' on non-bracket dialects
	for _, d := range []Dialect{DialectPostgres, DialectClickHouse, DialectMySQL, DialectOracle, DialectDameng} {
		for _, badFilter := range []string{"[1 /*]", "[1; DROP TABLE users; -- ]", "['a]b'] ; DROP TABLE users -- '"} {
			if _, _, err := BuildBrowseQuery(d, driver.BrowseDataRequest{
				Database: "app",
				Schema:   "public",
				Table:    "users",
				Filter:   badFilter,
			}); err == nil {
				t.Fatalf("dialect %s: expected BuildBrowseQuery to reject filter %q", d, badFilter)
			}
		}
	}
	for _, d := range []Dialect{DialectPostgres, DialectOracle, DialectDameng, DialectSQLServer} {
		if _, _, err := BuildBrowseQuery(d, driver.BrowseDataRequest{
			Schema: "public",
			Table:  "users",
			Filter: "`'`; DROP TABLE users -- `",
		}); err == nil {
			t.Fatalf("dialect %s: expected BuildBrowseQuery to reject backtick smuggle filter", d)
		}
	}

	// 6. DuckDB hasSQLDotCommandOutsideQuotesWithOpts must not treat [...] or `...` as quotes
	if !hasSQLDotCommandOutsideQuotesWithOpts("SELECT [\n.shell id\n]", SyntaxOptionsForEngine("duckdb")) {
		t.Fatal("expected DuckDB hasSQLDotCommandOutsideQuotesWithOpts not to treat [...] as quote")
	}
	if !hasSQLDotCommandOutsideQuotesWithOpts("SELECT `\n.shell id\n`", SyntaxOptionsForEngine("duckdb")) {
		t.Fatal("expected DuckDB hasSQLDotCommandOutsideQuotesWithOpts not to treat `...` as quote")
	}

	// 7. Benign controls & SQL Server / SQLite bracket-identifier enforcement
	if _, err := BuildCreateTableDDL(DialectPostgres, driver.CreateTableRequest{
		Schema: "public",
		Table:  "t_pg_ok",
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "INT", IsPrimaryKey: true},
			{Name: "tags", Type: "TEXT[]", DefaultValue: "ARRAY['a', 'b']"},
			{Name: "nums", Type: "INT[]", DefaultValue: "ARRAY[1, 2]"},
		},
	}); err != nil {
		t.Fatalf("expected PostgreSQL benign ARRAY['a', 'b'] / ARRAY[1, 2] defaults to pass, got %v", err)
	}
	if _, err := BuildCreateTableDDL(DialectClickHouse, driver.CreateTableRequest{
		Database: "default",
		Table:    "t_ch_ok",
		Columns:  []driver.ColumnDef{{Name: "id", Type: "UInt64"}, {Name: "ts", Type: "DateTime"}},
		OrderBy:  "(id, ts)",
	}); err != nil {
		t.Fatalf("expected ClickHouse benign ORDER BY (id, ts) to pass, got %v", err)
	}
	for _, d := range []Dialect{DialectSQLServer, DialectSQLite} {
		if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
			Schema: "dbo",
			Table:  "t_br_ok",
			Columns: []driver.ColumnDef{
				{Name: "id", Type: "INT", IsPrimaryKey: true},
				{Name: "val", Type: "INT", DefaultValue: "[my_col]"},
			},
		}); err != nil {
			t.Fatalf("dialect %s: expected benign [my_col] to pass, got %v", d, err)
		}
		if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
			Schema: "dbo",
			Table:  "t_br_bad",
			Columns: []driver.ColumnDef{
				{Name: "id", Type: "INT", IsPrimaryKey: true},
				{Name: "val", Type: "INT", DefaultValue: "[my_col]; DROP TABLE users"},
			},
		}); err == nil {
			t.Fatalf("dialect %s: expected [my_col]; DROP TABLE users to be rejected", d)
		}
		if stmts := SplitStatementsForEngine(string(d), "SELECT [a;b] FROM [my]]table];"); len(stmts) != 1 {
			t.Fatalf("dialect %s: expected bracketed identifier [a;b] to stay 1 statement, got %#v", d, stmts)
		}
	}
}

func TestIter12DollarTagsAltQuotesSQLiteBracketsAndStripParens(t *testing.T) {
	// 1. Verify SyntaxOptionsForEngine AlternativeQuotes & BracketDoubleEscape flags
	for _, eng := range []string{"oracle", "dameng", "dm8"} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.AlternativeQuotes || opts.BracketIdentifiers || opts.BracketDoubleEscape {
			t.Fatalf("engine %q: expected AlternativeQuotes=true and Bracket*=false, got %+v", eng, opts)
		}
	}
	for _, eng := range []string{"sqlserver", "mssql", ""} {
		opts := SyntaxOptionsForEngine(eng)
		if !opts.BracketIdentifiers || !opts.BracketDoubleEscape || opts.AlternativeQuotes {
			t.Fatalf("engine %q: expected BracketIdentifiers=true, BracketDoubleEscape=true, AlternativeQuotes=false, got %+v", eng, opts)
		}
	}
	sqliteOpts := SyntaxOptionsForEngine("sqlite")
	if !sqliteOpts.BracketIdentifiers || sqliteOpts.BracketDoubleEscape || sqliteOpts.AlternativeQuotes {
		t.Fatalf("engine \"sqlite\": expected BracketIdentifiers=true, BracketDoubleEscape=false, AlternativeQuotes=false, got %+v", sqliteOpts)
	}
	for _, eng := range []string{"mysql", "mariadb", "tidb", "oceanbase", "clickhouse", "postgres", "postgresql", "kingbase", "duckdb"} {
		opts := SyntaxOptionsForEngine(eng)
		if opts.AlternativeQuotes || opts.BracketDoubleEscape {
			t.Fatalf("engine %q: expected AlternativeQuotes=false, BracketDoubleEscape=false, got %+v", eng, opts)
		}
	}

	// 2. PostgreSQL & DuckDB UTF-8 and >64-byte dollar-quote tags
	tag65 := "$" + strings.Repeat("a", 65) + "$"
	tag80 := "$" + strings.Repeat("a", 80) + "$"
	tag200 := "$" + strings.Repeat("a", 200) + "$"
	tag500 := "$" + strings.Repeat("a", 500) + "$"

	pgDuckPayloads := []string{
		"$é$'$é$; DROP TABLE users -- '",
		tag65 + "'" + tag65 + "; DROP TABLE users -- '",
		tag200 + "'" + tag200 + "; CALL duckdb_secrets(); -- '",
		tag500 + "'" + tag500 + "; DROP TABLE users -- '",
	}
	for _, eng := range []string{"postgres", "postgresql", "kingbase", "duckdb"} {
		for _, payload := range pgDuckPayloads {
			if !ContainsUnquotedSQLMetaForEngine(eng, payload) {
				t.Fatalf("engine %q: expected ContainsUnquotedSQLMetaForEngine to reject %q", eng, payload)
			}
			if err := ValidateSQLExpressionForEngine(eng, "expr", payload); err == nil {
				t.Fatalf("engine %q: expected ValidateSQLExpressionForEngine to reject %q", eng, payload)
			}
			q := "SELECT " + payload
			if stmts := SplitStatementsForEngine(eng, q); len(stmts) < 2 {
				t.Fatalf("engine %q: expected SplitStatementsForEngine to split %q, got %#v", eng, q, stmts)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, q); err == nil {
				t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, q)
			}
		}
		for _, benignTag := range []string{"$é$", tag80, tag500} {
			benignExpr := benignTag + "hello; world" + benignTag
			if ContainsUnquotedSQLMetaForEngine(eng, benignExpr) {
				t.Fatalf("engine %q: expected benign dollar-quote %q to pass ContainsUnquotedSQLMetaForEngine", eng, benignExpr)
			}
			if err := ValidateSQLExpressionForEngine(eng, "expr", benignExpr); err != nil {
				t.Fatalf("engine %q: expected benign dollar-quote %q to pass ValidateSQLExpressionForEngine, got %v", eng, benignExpr, err)
			}
			q := "SELECT " + benignExpr + " AS txt"
			if stmts := SplitStatementsForEngine(eng, q); len(stmts) != 1 {
				t.Fatalf("engine %q: expected 1 statement for benign %q, got %#v", eng, q, stmts)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, q); err != nil {
				t.Fatalf("engine %q: expected benign %q to pass ValidateReadOnlyScriptForEngine, got %v", eng, q, err)
			}
		}
	}

	// DuckDB Web sandbox must block UTF-8 & 200-byte dollar-quote CALL bypasses, and allow benign controls
	for _, badDuck := range []string{
		"SELECT $é$'$é$; CALL duckdb_secrets(); -- '",
		"SELECT " + tag65 + "'" + tag65 + "; CALL duckdb_secrets(); -- '",
		"SELECT " + tag200 + "'" + tag200 + "; CALL duckdb_secrets(); -- '",
		"SELECT $é$line1\n$é$;\n.shell id",
	} {
		if err := ValidateDuckDBRestrictedWebSQL(badDuck); err == nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to reject %q", badDuck)
		}
	}
	for _, okDuck := range []string{
		"SELECT $é$hello; world\n.not_a_cmd$é$ AS body",
		"SELECT " + tag80 + "hello; world" + tag80 + " AS body",
	} {
		if err := ValidateDuckDBRestrictedWebSQL(okDuck); err != nil {
			t.Fatalf("expected ValidateDuckDBRestrictedWebSQL to allow %q, got %v", okDuck, err)
		}
	}

	// Streaming splitter edge cases: aborted long candidate ($foo... without closing '$') & >32KiB tag
	longCand := "$" + strings.Repeat("a", 500)
	if got := SplitStatementsForEngine("postgres", "SELECT "+longCand+" + 1; SELECT 2"); len(got) != 2 || !strings.Contains(got[0], longCand+" + 1") || got[1] != "SELECT 2" {
		t.Fatalf("expected aborted long $tag candidate + space to preserve all bytes and split cleanly, got %#v", got)
	}
	if got := SplitStatementsForEngine("postgres", "SELECT "+longCand+"'x;y'; SELECT 2"); len(got) != 2 || !strings.Contains(got[0], longCand+"'x;y'") || got[1] != "SELECT 2" {
		t.Fatalf("expected aborted long $tag candidate before single-quote to enter inSingle cleanly, got %#v", got)
	}
	if got := SplitStatementsForEngine("postgres", "SELECT "+longCand+"; DROP TABLE users"); len(got) != 2 || got[1] != "DROP TABLE users" {
		t.Fatalf("expected aborted long $tag candidate before ';' to split cleanly, got %#v", got)
	}
	hugeTag := "$" + strings.Repeat("x", 40000) + "$"
	if got := SplitStatementsForEngine("postgres", "SELECT "+hugeTag+"a;b$xxxx$c"+hugeTag+"; SELECT 2"); len(got) != 2 || !strings.Contains(got[0], "a;b$xxxx$c") || got[1] != "SELECT 2" {
		t.Fatalf("expected 40KB $tag$ to match closing tag cleanly without truncation, got %d stmts", len(got))
	}

	// 3. Oracle & Dameng q'<delim>...<matching_delim>' alternative quoting
	altSmuggles := []string{
		"q'!a'!'; DROP TABLE users -- '",
		"Q'[a]'; DROP TABLE users -- '",
		"q'{a}'; DROP TABLE users -- '",
		"q'(a)'; DROP TABLE users -- '",
		"q'<a'>; DROP TABLE users -- '",
		"q'<a>'; DROP TABLE users -- '",
		"nq'!a'!'; DROP TABLE users -- '",
		"NQ'[a]'; DROP TABLE users -- '",
	}
	for _, eng := range []string{"oracle", "dameng", "dm8"} {
		for _, payload := range altSmuggles {
			if !ContainsUnquotedSQLMetaForEngine(eng, payload) {
				t.Fatalf("engine %q: expected ContainsUnquotedSQLMetaForEngine to reject %q", eng, payload)
			}
			if err := ValidateSQLExpressionForEngine(eng, "expr", payload); err == nil {
				t.Fatalf("engine %q: expected ValidateSQLExpressionForEngine to reject %q", eng, payload)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, "SELECT "+payload+" FROM dual"); err == nil {
				t.Fatalf("engine %q: expected ValidateReadOnlyScriptForEngine to reject %q", eng, payload)
			}
		}
		for _, benign := range []string{
			"SELECT q'!hello's world!' FROM dual",
			"SELECT Q'[a;b]' FROM dual",
			"SELECT q'{a;b}' FROM dual",
			"SELECT q'(a;b)' FROM dual",
			"SELECT q'<a;b>' FROM dual",
			"SELECT nq'!hello's; world!' FROM dual",
			"SELECT q'éhello's; worldé' FROM dual",
		} {
			if stmts := SplitStatementsForEngine(eng, benign); len(stmts) != 1 {
				t.Fatalf("engine %q: expected 1 statement for benign %q, got %#v", eng, benign, stmts)
			}
			if err := ValidateReadOnlyScriptForEngine(eng, benign); err != nil {
				t.Fatalf("engine %q: expected benign %q to pass ValidateReadOnlyScriptForEngine, got %v", eng, benign, err)
			}
		}
		// Identifier boundary & whitespace delimiter checks:
		// If q'! were wrongly triggered after an identifier char (or on whitespace delim), !' at the end would swallow '; DROP TABLE users'.
		for _, boundaryAttack := range []string{
			"SELECT my_q'!a'; DROP TABLE users -- !' FROM dual",
			"SELECT col1q'!a'; DROP TABLE users -- !' FROM dual",
			"SELECT my#q'!a'; DROP TABLE users -- !' FROM dual",
			"SELECT my$q'!a'; DROP TABLE users -- !' FROM dual",
			"SELECT q' '; DROP TABLE users -- ' FROM dual",
		} {
			if err := ValidateReadOnlyScriptForEngine(eng, boundaryAttack); err == nil {
				t.Fatalf("engine %q: expected boundary/whitespace-delim attack %q to be rejected", eng, boundaryAttack)
			}
		}
	}
	for _, d := range []Dialect{DialectOracle, DialectDameng} {
		for _, payload := range altSmuggles {
			if _, err := BuildCreateTableDDL(d, driver.CreateTableRequest{
				Schema: "SCOTT", Table: "T_ALT_Q",
				Columns: []driver.ColumnDef{
					{Name: "ID", Type: "NUMBER", IsPrimaryKey: true},
					{Name: "VAL", Type: "VARCHAR2(64)", DefaultValue: payload},
				},
			}); err == nil {
				t.Fatalf("dialect %s: expected BuildCreateTableDDL to reject %q", d, payload)
			}
		}
	}

	// 4. SQLite ']]' bracket-escape differential vs SQL Server
	for _, sqBad := range []string{
		"[a]] , x INT); DROP TABLE users; -- ]",
		"[a]] ; DROP TABLE users; -- ]",
	} {
		if !ContainsUnquotedSQLMetaForEngine("sqlite", sqBad) {
			t.Fatalf("sqlite: expected ContainsUnquotedSQLMetaForEngine to reject %q", sqBad)
		}
		if err := ValidateReadOnlyScriptForEngine("sqlite", "SELECT "+sqBad); err == nil {
			t.Fatalf("sqlite: expected ValidateReadOnlyScriptForEngine to reject %q", sqBad)
		}
		if _, err := BuildCreateTableDDL(DialectSQLite, driver.CreateTableRequest{
			Table: "t_sq_br",
			Columns: []driver.ColumnDef{
				{Name: "id", Type: "INTEGER", IsPrimaryKey: true},
				{Name: "val", Type: "TEXT", DefaultValue: sqBad},
			},
		}); err == nil {
			t.Fatalf("sqlite: expected BuildCreateTableDDL to reject %q", sqBad)
		}
	}
	if _, err := BuildCreateTableDDL(DialectSQLServer, driver.CreateTableRequest{
		Schema: "dbo", Table: "t_ms_dbl",
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "INT", IsPrimaryKey: true},
			{Name: "val", Type: "INT", DefaultValue: "[my]]col]"},
		},
	}); err != nil {
		t.Fatalf("sqlserver: expected benign [my]]col] to pass BuildCreateTableDDL, got %v", err)
	}
	if _, _, err := BuildBrowseQuery(DialectSQLite, driver.BrowseDataRequest{Table: "items", Filter: "[id] = 1"}); err != nil {
		t.Fatalf("sqlite: expected benign [id] = 1 filter to pass, got %v", err)
	}

	// 5. stripOuterParensWithOpts hardening (brackets, backticks, alt-quotes, UTF-8 dollar-tags)
	for _, tc := range []struct {
		engine string
		input  string
		want   string
	}{
		{"sqlserver", "([a(] > 0) AND ([b)] = 1)", "([a(] > 0) AND ([b)] = 1)"},
		{"sqlite", "([a(] > 0) AND ([b)] = 1)", "([a(] > 0) AND ([b)] = 1)"},
		{"sqlserver", "([a]] (] > 0) AND ([b)] = 1)", "([a]] (] > 0) AND ([b)] = 1)"},
		{"mysql", "(`a(` > 0) AND (`b)` = 1)", "(`a(` > 0) AND (`b)` = 1)"},
		{"clickhouse", "(`a(` > 0) AND (`b)` = 1)", "(`a(` > 0) AND (`b)` = 1)"},
		{"oracle", "(q'!a)!' = '1') AND (q'!b(!' = '2')", "(q'!a)!' = '1') AND (q'!b(!' = '2')"},
		{"dameng", "(q'[a)]' = '1') AND (q'[b(]' = '2')", "(q'[a)]' = '1') AND (q'[b(]' = '2')"},
		{"postgres", "($é$a)$é$ = '1') AND ($é$b($é$ = '2')", "($é$a)$é$ = '1') AND ($é$b($é$ = '2')"},
		{"postgres", "((a > 0))", "a > 0"},
	} {
		got := stripOuterParensWithOpts(tc.input, SyntaxOptionsForEngine(tc.engine))
		if got != tc.want {
			t.Fatalf("stripOuterParensWithOpts(%q, %q) = %q, want %q", tc.engine, tc.input, got, tc.want)
		}
	}
}

func TestValidateColumnTypeForEngineDialectEscapes(t *testing.T) {
	maliciousByEngine := []struct {
		engines  []string
		dialect  Dialect
		colTypes []string
	}{
		{
			engines: []string{"mysql", "mariadb", "tidb", "oceanbase"},
			dialect: DialectMySQL,
			colTypes: []string{
				`ENUM('a\'')); DROP TABLE users; SELECT (')`,
				`ENUM('a\''); DROP TABLE users -- ')`,
				`SET('a\'')); DROP TABLE users; SELECT (')`,
				`VARCHAR(255) --evil`,
				`INT(11) # comment`,
			},
		},
		{
			engines: []string{"clickhouse"},
			dialect: DialectClickHouse,
			colTypes: []string{
				`Enum8('a\'')); DROP TABLE users; SELECT (')`,
				`Enum16('x'=1, 'y\'')); DROP TABLE users; SELECT (')`,
				`String # comment`,
			},
		},
		{
			engines: []string{"postgres", "postgresql", "kingbase", "duckdb"},
			dialect: DialectPostgres,
			colTypes: []string{
				`INT(E'a\'')); DROP TABLE users; SELECT (')`,
				`INT(e'a\'')); DROP TABLE users; SELECT (')`,
				`INT($$a$$); DROP TABLE users; --`,
				`INT($tag$a'$tag$); DROP TABLE users; SELECT (')`,
				`INT(my_e'a\'); DROP TABLE users; SELECT (')`,
				"INT(`a; DROP TABLE users`)",
			},
		},
		{
			engines: []string{"oracle", "dameng", "dm8"},
			dialect: DialectOracle,
			colTypes: []string{
				`NUMBER((q'(a')'); DROP TABLE users; SELECT (')`,
				`VARCHAR2((Q'[a]'); DROP TABLE users; SELECT ('[')`,
				`VARCHAR2((nq'!a'!'); DROP TABLE users; SELECT ('!')`,
				`VARCHAR2((NQ'éa'é); DROP TABLE users; SELECT ('é')`,
				`NUMBER((my_q'!a'; DROP TABLE users; -- !')`,
			},
		},
		{
			engines: []string{"sqlite"},
			dialect: DialectSQLite,
			colTypes: []string{
				`[a]] ); DROP TABLE users; -- ]`,
				`INT([a]] ); DROP TABLE users; -- ])`,
			},
		},
		{
			engines: []string{"sqlserver", "mssql"},
			dialect: DialectSQLServer,
			colTypes: []string{
				`INT([a] ); DROP TABLE users; -- ])`,
				`INT([a]]]; DROP TABLE users; --)`,
				`INT([a]]; DROP TABLE users; --)`,
			},
		},
	}

	for _, grp := range maliciousByEngine {
		for _, eng := range grp.engines {
			for _, badType := range grp.colTypes {
				if err := ValidateColumnTypeForEngine(eng, badType); err == nil {
					t.Fatalf("engine %q: expected ValidateColumnTypeForEngine to reject %q", eng, badType)
				}
				if _, err := BuildAlterTableSQLsForEngine(eng, grp.dialect, driver.AlterTableRequest{
					Table: "t_col_pwn",
					Actions: []driver.AlterAction{
						{Action: "ADD_COLUMN", Column: driver.ColumnDef{Name: "c", Type: badType}},
					},
				}); err == nil {
					t.Fatalf("engine %q: expected BuildAlterTableSQLsForEngine ADD_COLUMN to reject %q", eng, badType)
				}
				if _, err := BuildAlterTableSQLsForEngine(eng, grp.dialect, driver.AlterTableRequest{
					Table: "t_col_pwn",
					Actions: []driver.AlterAction{
						{Action: "MODIFY_COLUMN", Column: driver.ColumnDef{Name: "c", Type: badType}},
					},
				}); err == nil {
					t.Fatalf("engine %q: expected BuildAlterTableSQLsForEngine MODIFY_COLUMN to reject %q", eng, badType)
				}
			}
		}
		for _, badType := range grp.colTypes {
			if _, err := BuildCreateTableDDL(grp.dialect, driver.CreateTableRequest{
				Table: "t_col_pwn",
				Columns: []driver.ColumnDef{
					{Name: "id", Type: "INT", IsPrimaryKey: true},
					{Name: "c", Type: badType},
				},
			}); err == nil {
				t.Fatalf("dialect %s: expected BuildCreateTableDDL to reject %q", grp.dialect, badType)
			}
		}
	}

	// Control characters inside quotes must still be rejected
	for _, ctrl := range []string{"ENUM('a\nb')", "ENUM('a\rb')", "ENUM('a\x00b')"} {
		if err := ValidateColumnTypeForEngine("mysql", ctrl); err == nil {
			t.Fatalf("expected control-char column type %q to be rejected", ctrl)
		}
	}

	// Legitimate column types must pass
	validByEngine := []struct {
		engine   string
		dialect  Dialect
		colTypes []string
	}{
		{
			engine:  "mysql",
			dialect: DialectMySQL,
			colTypes: []string{
				`ENUM('a', 'b')`,
				`ENUM('a\'b')`,
				`SET('x', 'y')`,
				`VARCHAR(255)`,
				`DECIMAL(10, 2)`,
				`INT(11)`,
				`NUMERIC(18, 4)`,
			},
		},
		{
			engine:  "mariadb",
			dialect: DialectMySQL,
			colTypes: []string{
				`ENUM('a', 'b')`,
				`ENUM('a\'b')`,
				`SET('x', 'y')`,
			},
		},
		{
			engine:  "clickhouse",
			dialect: DialectClickHouse,
			colTypes: []string{
				`Enum8('a'=1, 'b'=2)`,
				`Enum16('x' = 1, 'y' = -2)`,
				`Array(Tuple(String, Int64))`,
				`Nullable(Decimal(18, 4))`,
			},
		},
		{
			engine:  "postgres",
			dialect: DialectPostgres,
			colTypes: []string{
				`VARCHAR(255)`,
				`DECIMAL(10, 2)`,
				`TIMESTAMP(6) WITH TIME ZONE`,
				`NUMERIC(18, 4)`,
				`TEXT[]`,
				`INT[3]`,
			},
		},
		{
			engine:  "oracle",
			dialect: DialectOracle,
			colTypes: []string{
				`NUMBER(10, -2)`,
				`VARCHAR2(100 CHAR)`,
				`TIMESTAMP(6) WITH TIME ZONE`,
			},
		},
		{
			engine:  "sqlserver",
			dialect: DialectSQLServer,
			colTypes: []string{
				`NVARCHAR(255)`,
				`DECIMAL(18, 4)`,
				`[dbo].[MyType]`,
				`[my]]type]`,
			},
		},
		{
			engine:  "sqlite",
			dialect: DialectSQLite,
			colTypes: []string{
				`INTEGER`,
				`VARCHAR(255)`,
				`NUMERIC(18, 4)`,
			},
		},
	}

	for _, grp := range validByEngine {
		for _, okType := range grp.colTypes {
			if err := ValidateColumnTypeForEngine(grp.engine, okType); err != nil {
				t.Fatalf("engine %q: expected valid column type %q to pass, got %v", grp.engine, okType, err)
			}
			if _, err := BuildCreateTableDDL(grp.dialect, driver.CreateTableRequest{
				Table: "t_col_ok",
				Columns: []driver.ColumnDef{
					{Name: "id", Type: "INT", IsPrimaryKey: true},
					{Name: "c", Type: okType},
				},
			}); err != nil {
				t.Fatalf("dialect %s: expected BuildCreateTableDDL to accept %q, got %v", grp.dialect, okType, err)
			}
			if _, err := BuildAlterTableSQLsForEngine(grp.engine, grp.dialect, driver.AlterTableRequest{
				Table: "t_col_ok",
				Actions: []driver.AlterAction{
					{Action: "ADD_COLUMN", Column: driver.ColumnDef{Name: "c", Type: okType}},
				},
			}); err != nil {
				t.Fatalf("engine %q: expected BuildAlterTableSQLsForEngine ADD_COLUMN to accept %q, got %v", grp.engine, okType, err)
			}
		}
	}
}
