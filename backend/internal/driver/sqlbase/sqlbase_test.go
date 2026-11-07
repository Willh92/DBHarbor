package sqlbase

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

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
	stmts := SplitStatements(script)
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
		if err := ValidateReadOnlyScript(q); err == nil {
			t.Fatalf("expected ValidateReadOnlyScript to block %q", q)
		}
	}
	if stmts := SplitStatements("/*M!100100 SELECT 1 */; /*m! SELECT 2 */;"); len(stmts) != 2 {
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
