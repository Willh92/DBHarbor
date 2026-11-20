package dameng

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

type mockDMConnector struct {
	queries []string
	rows    int
}

func (c *mockDMConnector) Connect(context.Context) (stdsql_driver.Conn, error) {
	return &mockDMConn{parent: c}, nil
}
func (c *mockDMConnector) Driver() stdsql_driver.Driver { return nil }

type mockDMConn struct{ parent *mockDMConnector }

func (c *mockDMConn) Prepare(string) (stdsql_driver.Stmt, error) { return nil, nil }
func (c *mockDMConn) Close() error                              { return nil }
func (c *mockDMConn) Begin() (stdsql_driver.Tx, error)          { return nil, nil }
func (c *mockDMConn) QueryContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Rows, error) {
	c.parent.queries = append(c.parent.queries, query)
	if strings.Contains(query, "ALL_TAB_COLUMNS") {
		return &mockDMRows{total: c.parent.rows}, nil
	}
	return &mockDMRows{total: 0}, nil
}

type mockDMRows struct {
	total int
	idx   int
}

func (r *mockDMRows) Columns() []string { return []string{"TABLE_NAME", "COLUMN_NAME"} }
func (r *mockDMRows) Close() error      { return nil }
func (r *mockDMRows) Next(dest []stdsql_driver.Value) error {
	if r.idx >= r.total {
		return io.EOF
	}
	r.idx++
	dest[0] = fmt.Sprintf("DM_%d", r.idx)
	dest[1] = "COL"
	return nil
}

func TestNewValidationAndDefaultSchema(t *testing.T) {
	if _, err := New(driver.ConnectionConfig{
		Type:    "dameng",
		Host:    "bad/host?x=1",
		Options: map[string]string{"__web_mode": "1"},
	}); err == nil {
		t.Fatal("expected invalid hostname rejection in __web_mode=1")
	}
	if _, err := New(driver.ConnectionConfig{
		Type:    "dameng",
		Host:    "127.0.0.1",
		Options: map[string]string{"__restrict_internal_ip": "1"},
	}); err == nil {
		t.Fatal("expected loopback host rejection under __restrict_internal_ip=1")
	}

	dEmpty := &Driver{}
	if dEmpty.defaultSchema() != "SYSDBA" {
		t.Fatalf("expected empty username defaultSchema SYSDBA, got %q", dEmpty.defaultSchema())
	}
	dUser := &Driver{cfg: driver.ConnectionConfig{Username: "app_owner"}}
	if dUser.defaultSchema() != "APP_OWNER" {
		t.Fatalf("expected uppercased defaultSchema APP_OWNER, got %q", dUser.defaultSchema())
	}
}

func TestFormatDamengColumnTypeAndSystemSchemaProtection(t *testing.T) {
	cases := []struct {
		typ       string
		len, p, s sql.NullInt64
		want      string
	}{
		{"VARCHAR2(32)", sql.NullInt64{Int64: 32, Valid: true}, sql.NullInt64{}, sql.NullInt64{}, "VARCHAR2(32)"},
		{"varchar2", sql.NullInt64{Int64: 64, Valid: true}, sql.NullInt64{}, sql.NullInt64{}, "VARCHAR2(64)"},
		{"number", sql.NullInt64{}, sql.NullInt64{Int64: 10, Valid: true}, sql.NullInt64{}, "NUMBER(10)"},
		{"number", sql.NullInt64{}, sql.NullInt64{Int64: 18, Valid: true}, sql.NullInt64{Int64: 2, Valid: true}, "NUMBER(18,2)"},
	}
	for _, tc := range cases {
		if got := formatDamengColumnType(tc.typ, tc.len, tc.p, tc.s); got != tc.want {
			t.Fatalf("formatDamengColumnType(%q, %v, %v, %v) = %q, want %q", tc.typ, tc.len, tc.p, tc.s, got, tc.want)
		}
	}

	d := &Driver{}
	for _, sysSchema := range []string{"", "SYS", "SYSDBA", "SYSSSO", "SYSAUDITOR", "CTISYS"} {
		if err := d.DropSchema(context.Background(), "", sysSchema, false); err == nil {
			t.Fatalf("expected DropSchema(%q) to be rejected", sysSchema)
		}
	}
}

func TestDamengPreviewDDLIndexesPartitionAndAutocompleteCap(t *testing.T) {
	ctx := context.Background()
	conn := &mockDMConnector{rows: 26000}
	d := &Driver{
		cfg: driver.ConnectionConfig{Username: "app_dm"},
		db:  sql.OpenDB(conn),
	}
	defer d.db.Close()

	script, err := d.CreateTable(ctx, driver.CreateTableRequest{
		Schema:      "APP_DM",
		Table:       "ORDERS",
		Comment:     "DM orders",
		PreviewOnly: true,
		Columns: []driver.ColumnDef{
			{Name: "ID", Type: "BIGINT", IsPrimaryKey: true, Comment: "PK"},
			{Name: "CODE", Type: "VARCHAR2(64)", Nullable: false},
		},
		Indexes: []driver.IndexDef{
			{Name: "IDX_DM_CODE", Columns: []string{"CODE"}, Unique: true},
		},
	})
	if err != nil {
		t.Fatalf("Dameng CreateTable preview failed: %v", err)
	}
	for _, want := range []string{
		`CREATE TABLE "APP_DM"."ORDERS"`,
		`CREATE UNIQUE INDEX "APP_DM"."IDX_DM_CODE" ON "APP_DM"."ORDERS" ("CODE")`,
		`COMMENT ON TABLE "APP_DM"."ORDERS" IS 'DM orders'`,
		`COMMENT ON COLUMN "APP_DM"."ORDERS"."ID" IS 'PK'`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in Dameng CreateTable preview:\n%s", want, script)
		}
	}

	if stmts, err := d.AlterTable(ctx, driver.AlterTableRequest{
		Schema:      "APP_DM",
		Table:       "ORDERS",
		PreviewOnly: true,
		Actions:     []driver.AlterAction{{Action: "DROP_INDEX", Index: driver.IndexDef{Name: "IDX_DM_CODE"}}},
	}); err != nil || len(stmts) != 1 || stmts[0] != `DROP INDEX "APP_DM"."IDX_DM_CODE"` {
		t.Fatalf("unexpected Dameng DROP_INDEX preview: err=%v stmts=%v", err, stmts)
	}

	if muts, err := d.BuildMutationPreviewSQL(driver.RowMutationRequest{
		Schema:  "APP_DM",
		Table:   "ORDERS",
		Inserts: []map[string]any{{"ID": 1}},
	}); err != nil || len(muts) != 1 {
		t.Fatalf("BuildMutationPreviewSQL failed: err=%v muts=%v", err, muts)
	}

	if stmt, err := d.ManagePartition(ctx, driver.PartitionActionRequest{Table: "ORDERS", PartitionID: "P1", Action: "TRUNCATE", PreviewOnly: true}); err != nil || !strings.Contains(stmt, "TRUNCATE PARTITION") {
		t.Fatalf("unexpected ManagePartition TRUNCATE: err=%v stmt=%q", err, stmt)
	}
	if _, err := d.ManagePartition(ctx, driver.PartitionActionRequest{Table: "ORDERS", PartitionID: "P1", Action: "BOGUS", PreviewOnly: true}); err == nil {
		t.Fatal("expected unsupported partition action error")
	}
	if err := d.KillSession(ctx, "not-an-int"); err == nil {
		t.Fatal("expected KillSession invalid ID error")
	}

	meta, err := d.GetAutocompleteMeta(ctx, "")
	if err != nil {
		t.Fatalf("GetAutocompleteMeta: %v", err)
	}
	if len(meta.Tables) != 25000 {
		t.Fatalf("expected GetAutocompleteMeta to cap at 25000 rows, got %d", len(meta.Tables))
	}
	foundLimit := false
	for _, q := range conn.queries {
		if strings.Contains(q, "ALL_TAB_COLUMNS") && strings.Contains(q, "LIMIT 25000") {
			foundLimit = true
		}
	}
	if !foundLimit {
		t.Fatalf("expected Dameng GetAutocompleteMeta query to include LIMIT 25000, got: %v", conn.queries)
	}
}
