package sqlserver

import (
	"context"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
)

func TestNewEncryptModesAndRestrictedIP(t *testing.T) {
	if _, err := New(driver.ConnectionConfig{
		Type:    "sqlserver",
		Host:    "127.0.0.1",
		Options: map[string]string{"__restrict_internal_ip": "1"},
	}); err == nil {
		t.Fatal("expected loopback host to be rejected under __restrict_internal_ip=1")
	}

	if _, err := New(driver.ConnectionConfig{
		Type:    "sqlserver",
		Host:    "db.example.com",
		Options: map[string]string{"encrypt": "true", "__restrict_internal_ip": "1"},
	}); err == nil {
		t.Fatal("expected encrypt=true (TrustServerCertificate=true) to be rejected under __restrict_internal_ip=1")
	}

	if _, err := New(driver.ConnectionConfig{
		Type:    "sqlserver",
		Host:    "db.example.com",
		Options: map[string]string{"encrypt": "bogus"},
	}); err == nil {
		t.Fatal("expected unsupported encrypt mode error")
	}

	for _, mode := range []string{"disable", "false", "verified"} {
		drv, err := New(driver.ConnectionConfig{
			Type:    "sqlserver",
			Host:    "8.8.8.8",
			Options: map[string]string{"encrypt": mode, "__restrict_internal_ip": "1"},
		})
		if err != nil {
			t.Fatalf("expected encrypt=%s to succeed, got %v", mode, err)
		}
		_ = drv.Close()
	}
}

func TestFormatSQLServerColumnType(t *testing.T) {
	cases := []struct {
		typ                string
		maxLen, prec, scal int64
		want               string
	}{
		{"nvarchar", -1, 0, 0, "NVARCHAR(MAX)"},
		{"nvarchar", 100, 0, 0, "NVARCHAR(50)"},
		{"varchar", 50, 0, 0, "VARCHAR(50)"},
		{"decimal", 0, 18, 2, "DECIMAL(18,2)"},
		{"datetime2", 0, 0, 3, "DATETIME2(3)"},
		{"datetime2", 0, 0, 7, "DATETIME2"},
	}
	for _, tc := range cases {
		if got := formatSQLServerColumnType(tc.typ, tc.maxLen, tc.prec, tc.scal); got != tc.want {
			t.Fatalf("formatSQLServerColumnType(%q, %d, %d, %d) = %q, want %q", tc.typ, tc.maxLen, tc.prec, tc.scal, got, tc.want)
		}
	}
}

func TestDropSchemaSystemProtection(t *testing.T) {
	d := &Driver{}
	for _, sysSchema := range []string{"", "dbo", "sys", "information_schema", "guest", "DBO"} {
		if err := d.DropSchema(context.Background(), "app", sysSchema, false); err == nil {
			t.Fatalf("expected DropSchema(%q) to be rejected", sysSchema)
		}
	}
}

func TestPreviewDDLIndexesAndPartitionValidation(t *testing.T) {
	ctx := context.Background()
	d := &Driver{}

	script, err := d.CreateTable(ctx, driver.CreateTableRequest{
		Table:       "orders",
		Comment:     "Order table",
		PreviewOnly: true,
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "BIGINT", IsPrimaryKey: true, IsAutoIncrement: true, Comment: "PK"},
			{Name: "code", Type: "NVARCHAR(64)", Nullable: false},
		},
		Indexes: []driver.IndexDef{
			{Name: "idx_orders_code", Columns: []string{"code"}, Unique: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable preview failed: %v", err)
	}
	for _, want := range []string{
		`CREATE TABLE [dbo].[orders]`,
		`CREATE UNIQUE INDEX [idx_orders_code] ON [dbo].[orders] ([code])`,
		`sp_addextendedproperty`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in SQL Server CreateTable preview:\n%s", want, script)
		}
	}

	alterStmts, err := d.AlterTable(ctx, driver.AlterTableRequest{
		Table:       "orders",
		PreviewOnly: true,
		Actions:     []driver.AlterAction{{Action: "ADD_INDEX", Index: driver.IndexDef{Name: "ix1", Columns: []string{"code"}}}},
	})
	if err != nil || len(alterStmts) != 1 || !strings.Contains(alterStmts[0], `CREATE INDEX [ix1] ON [dbo].[orders]`) {
		t.Fatalf("unexpected AlterTable preview: err=%v stmts=%v", err, alterStmts)
	}

	muts, err := d.BuildMutationPreviewSQL(driver.RowMutationRequest{
		Table:   "orders",
		Inserts: []map[string]any{{"id": 1}},
	})
	if err != nil || len(muts) != 1 || !strings.Contains(muts[0], `[dbo].[orders]`) {
		t.Fatalf("unexpected BuildMutationPreviewSQL: err=%v muts=%v", err, muts)
	}

	partStmt, err := d.ManagePartition(ctx, driver.PartitionActionRequest{
		Table:       "orders",
		PartitionID: "2",
		Action:      "TRUNCATE",
		PreviewOnly: true,
	})
	if err != nil || !strings.Contains(partStmt, "WITH (PARTITIONS (2))") {
		t.Fatalf("unexpected ManagePartition TRUNCATE: err=%v stmt=%q", err, partStmt)
	}
	if _, err := d.ManagePartition(ctx, driver.PartitionActionRequest{Table: "orders", PartitionID: "0", Action: "TRUNCATE", PreviewOnly: true}); err == nil {
		t.Fatal("expected partitionID < 1 error")
	}
	if _, err := d.ManagePartition(ctx, driver.PartitionActionRequest{Table: "orders", PartitionID: "1", Action: "DROP", PreviewOnly: true}); err == nil {
		t.Fatal("expected unsupported non-TRUNCATE partition action error")
	}
	if err := d.KillSession(ctx, "not-an-int"); err == nil {
		t.Fatal("expected KillSession invalid SPID error")
	}
}
