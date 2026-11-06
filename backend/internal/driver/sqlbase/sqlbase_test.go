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
