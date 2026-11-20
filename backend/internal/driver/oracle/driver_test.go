package oracle

import (
	"context"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
)

func TestParseOracleSessionTarget(t *testing.T) {
	sid, ser, inst, hasInst, err := parseOracleSessionTarget("142,58901")
	if err != nil || sid != 142 || ser != 58901 || hasInst || inst != 0 {
		t.Fatalf("unexpected parse result: sid=%d ser=%d inst=%d hasInst=%v err=%v", sid, ser, inst, hasInst, err)
	}
	sid, ser, inst, hasInst, err = parseOracleSessionTarget(" 142,58901,@2 ")
	if err != nil || sid != 142 || ser != 58901 || !hasInst || inst != 2 {
		t.Fatalf("unexpected RAC parse result: sid=%d ser=%d inst=%d hasInst=%v err=%v", sid, ser, inst, hasInst, err)
	}
	for _, bad := range []string{"", "142", "142,abc", "142,58901;DROP", "142,58901,2", "142,58901,@0", "142,58901,@-1"} {
		if _, _, _, _, err := parseOracleSessionTarget(bad); err == nil {
			t.Fatalf("expected error for invalid Oracle session ID %q", bad)
		}
	}
}

type mockOraConnector struct {
	queries []string
	rows    int
}

func (c *mockOraConnector) Connect(context.Context) (stdsql_driver.Conn, error) {
	return &mockOraConn{parent: c}, nil
}
func (c *mockOraConnector) Driver() stdsql_driver.Driver { return nil }

type mockOraConn struct{ parent *mockOraConnector }

func (c *mockOraConn) Prepare(string) (stdsql_driver.Stmt, error) { return nil, nil }
func (c *mockOraConn) Close() error                              { return nil }
func (c *mockOraConn) Begin() (stdsql_driver.Tx, error)          { return nil, nil }
func (c *mockOraConn) QueryContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Rows, error) {
	c.parent.queries = append(c.parent.queries, query)
	if strings.Contains(query, "ALL_TAB_COLUMNS") {
		return &mockOraRows{total: c.parent.rows}, nil
	}
	return &mockOraRows{total: 0}, nil
}

type mockOraRows struct {
	total int
	idx   int
}

func (r *mockOraRows) Columns() []string { return []string{"TABLE_NAME", "COLUMN_NAME"} }
func (r *mockOraRows) Close() error      { return nil }
func (r *mockOraRows) Next(dest []stdsql_driver.Value) error {
	if r.idx >= r.total {
		return io.EOF
	}
	r.idx++
	dest[0] = fmt.Sprintf("T_%d", r.idx)
	dest[1] = "COL"
	return nil
}

func TestOracleCreateTablePreviewIndexesCommentsAndAutocompleteCap(t *testing.T) {
	d := &Driver{cfg: driver.ConnectionConfig{Username: "hr"}}
	script, err := d.CreateTable(context.Background(), driver.CreateTableRequest{
		Schema:      "HR",
		Table:       "ORDERS",
		Comment:     "Customer orders",
		PreviewOnly: true,
		Columns: []driver.ColumnDef{
			{Name: "ID", Type: "NUMBER(10)", IsPrimaryKey: true, Comment: "Primary ID"},
			{Name: "CODE", Type: "VARCHAR2(64)", Nullable: false},
		},
		Indexes: []driver.IndexDef{
			{Name: "IDX_ORDERS_CODE", Columns: []string{"CODE"}, Unique: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable preview failed: %v", err)
	}
	for _, want := range []string{
		`CREATE TABLE "HR"."ORDERS"`,
		`CREATE UNIQUE INDEX "HR"."IDX_ORDERS_CODE" ON "HR"."ORDERS" ("CODE")`,
		`COMMENT ON TABLE "HR"."ORDERS" IS 'Customer orders'`,
		`COMMENT ON COLUMN "HR"."ORDERS"."ID" IS 'Primary ID'`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in Oracle CreateTable preview:\n%s", want, script)
		}
	}

	conn := &mockOraConnector{rows: 26000}
	d.db = sql.OpenDB(conn)
	defer d.db.Close()

	meta, err := d.GetAutocompleteMeta(context.Background(), "HR")
	if err != nil {
		t.Fatalf("GetAutocompleteMeta: %v", err)
	}
	if len(meta.Tables) != 25000 {
		t.Fatalf("expected GetAutocompleteMeta to cap at 25000 rows, got %d", len(meta.Tables))
	}
	foundRownum := false
	for _, q := range conn.queries {
		if strings.Contains(q, "ALL_TAB_COLUMNS") && strings.Contains(q, "ROWNUM <= 25000") {
			foundRownum = true
		}
	}
	if !foundRownum {
		t.Fatalf("expected Oracle GetAutocompleteMeta query to include ROWNUM <= 25000, got: %v", conn.queries)
	}
}
