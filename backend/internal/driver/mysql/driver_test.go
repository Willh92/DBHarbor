package mysql

import (
	"context"
	"strings"
	"testing"

	"dbharbor/backend/internal/driver"
)

func TestNewVariantsAndTLSRestrictions(t *testing.T) {
	for _, v := range []struct {
		typ  string
		port int
	}{
		{"mysql", 3306},
		{"mariadb", 3306},
		{"tidb", 4000},
		{"oceanbase", 2881},
	} {
		meta, ok := driver.GetMeta(v.typ)
		if !ok || meta.DefaultPort != v.port {
			t.Fatalf("unexpected metadata for %s: ok=%v meta=%+v", v.typ, ok, meta)
		}
	}

	if _, err := New(driver.ConnectionConfig{
		Type:    "mysql",
		Host:    "10.0.0.10",
		Options: map[string]string{"tls": "skip-verify", "__restrict_internal_ip": "1"},
	}, "mysql", "MySQL", 3306); err == nil {
		t.Fatal("expected restricted tls=skip-verify to be rejected")
	}

	drv, err := New(driver.ConnectionConfig{
		Type:    "mariadb",
		Host:    "db.example.com",
		Options: map[string]string{"tls": "true", "__restrict_internal_ip": "1"},
	}, "mariadb", "MariaDB", 3306)
	if err != nil {
		t.Fatalf("expected valid verified TLS config to succeed: %v", err)
	}
	_ = drv.Close()
}

func TestCreateDatabaseValidation(t *testing.T) {
	d := &Driver{variantType: "mysql"}
	ctx := context.Background()
	if err := d.CreateDatabase(ctx, "   ", "utf8mb4"); err == nil {
		t.Fatal("expected empty database name error")
	}
	if err := d.CreateDatabase(ctx, "appdb", "utf8mb4; DROP TABLE users"); err == nil {
		t.Fatal("expected invalid charset error")
	}
}

func TestPreviewBuildersAndPartitionValidation(t *testing.T) {
	ctx := context.Background()
	d := &Driver{variantType: "mariadb"}

	ddl, err := d.CreateTable(ctx, driver.CreateTableRequest{
		Database:    "app",
		Table:       "items",
		PreviewOnly: true,
		Columns: []driver.ColumnDef{
			{Name: "id", Type: "BIGINT", IsPrimaryKey: true, IsAutoIncrement: true},
			{Name: "code", Type: "VARCHAR(64)", Nullable: false},
		},
		Indexes: []driver.IndexDef{
			{Name: "idx_items_code", Columns: []string{"code"}, Unique: true},
		},
	})
	if err != nil || !strings.Contains(ddl, "UNIQUE INDEX `idx_items_code`") {
		t.Fatalf("unexpected MySQL/MariaDB CreateTable preview: err=%v ddl=%s", err, ddl)
	}

	alterStmts, err := d.AlterTable(ctx, driver.AlterTableRequest{
		Database:    "app",
		Table:       "items",
		PreviewOnly: true,
		Actions: []driver.AlterAction{
			{Action: "DROP_CHECK", Check: driver.CheckConstraintDef{Name: "chk_price"}},
		},
	})
	if err != nil || len(alterStmts) != 1 || !strings.Contains(alterStmts[0], "DROP CONSTRAINT") {
		t.Fatalf("expected MariaDB DROP CONSTRAINT rewrite, got err=%v stmts=%v", err, alterStmts)
	}

	muts, err := d.BuildMutationPreviewSQL(driver.RowMutationRequest{
		Database: "app",
		Table:    "items",
		Inserts:  []map[string]any{{"id": 1}},
	})
	if err != nil || len(muts) != 1 || !strings.Contains(muts[0], "INSERT INTO `app`.`items`") {
		t.Fatalf("unexpected BuildMutationPreviewSQL: err=%v muts=%v", err, muts)
	}

	for _, tc := range []struct {
		req     driver.PartitionActionRequest
		wantErr bool
		wantSub string
	}{
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p2026", Action: "TRUNCATE", PreviewOnly: true}, false, "TRUNCATE PARTITION `p2026`"},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p2026", Action: "DROP", PreviewOnly: true}, false, "DROP PARTITION `p2026`"},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p2026", Action: "ANALYZE", PreviewOnly: true}, false, "ANALYZE PARTITION `p2026`"},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p2026", Action: "OPTIMIZE", PreviewOnly: true}, false, "OPTIMIZE PARTITION `p2026`"},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p_max", Action: "ADD", Expression: "MAXVALUE", PreviewOnly: true}, false, "VALUES LESS THAN MAXVALUE"},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p_bad", Action: "ADD", Expression: "100; DROP TABLE x", PreviewOnly: true}, true, ""},
		{driver.PartitionActionRequest{Database: "app", Table: "logs", PartitionID: "p1", Action: "INVALID", PreviewOnly: true}, true, ""},
	} {
		got, err := d.ManagePartition(ctx, tc.req)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("expected error for %+v, got %q", tc.req, got)
			}
			continue
		}
		if err != nil || !strings.Contains(got, tc.wantSub) {
			t.Fatalf("unexpected ManagePartition result: got=%q err=%v wantSub=%q", got, err, tc.wantSub)
		}
	}

	if err := d.KillSession(ctx, "not-a-number"); err == nil {
		t.Fatal("expected KillSession to reject non-integer session ID")
	}
}
