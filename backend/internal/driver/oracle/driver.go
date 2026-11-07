package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	go_ora "github.com/sijms/go-ora/v2"
)

func init() {
	fields := []driver.ConnFieldSchema{
		{Key: "host", Label: "Host", Type: "text", Target: "root", DefaultValue: "127.0.0.1", Required: true},
		{Key: "port", Label: "Port", Type: "number", Target: "root", DefaultValue: "1521", Required: true},
		{Key: "database", Label: "Service Name / SID", Type: "text", Target: "root", DefaultValue: "ORCL", Required: true},
		{
			Key: "serviceMode", Label: "Connection Identifier Mode", Type: "select", Target: "options", DefaultValue: "service",
			SelectOptions: []driver.SelectOption{
				{Label: "Service Name", Value: "service"},
				{Label: "SID", Value: "sid"},
			},
		},
		{Key: "username", Label: "Username", Type: "text", Target: "root", Required: true},
		{Key: "password", Label: "Password", Type: "password", Target: "root"},
	}
	driver.Register(driver.DriverMeta{
		Type:            "oracle",
		Name:            "Oracle",
		Category:        driver.CategoryRelational,
		BadgeColor:      "#f5222d",
		DefaultPort:     1521,
		Description:     "Pure Go zero-dependency Oracle driver (no Instant Client required, supports multi-schema and session monitoring)",
		SupportsSchemas: true,
		SupportsExplain: false,
		DefaultTypes:    []string{"NUMBER(19)", "NUMBER(10)", "NUMBER(18,2)", "VARCHAR2(255)", "NVARCHAR2(255)", "CLOB", "DATE", "TIMESTAMP", "BLOB"},
		Fields:          fields,
	}, New)
}

type Driver struct {
	cfg driver.ConnectionConfig
	db  *sql.DB
}

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	port := cfg.Port
	if port == 0 {
		port = 1521
	}
	svc := cfg.Database
	if svc == "" {
		svc = "ORCL"
	}
	urlOpts := map[string]string{}
	if cfg.Option("serviceMode", "service") == "sid" {
		urlOpts["SID"] = svc
		svc = ""
	}
	connStr := go_ora.BuildUrl(cfg.Host, port, svc, cfg.Username, cfg.Password, urlOpts)
	db, err := sql.Open("oracle", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)
	return &Driver{cfg: cfg, db: db}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("oracle")
	return m
}

func (d *Driver) Close() error { return d.db.Close() }

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var banner string
	if err := d.db.QueryRowContext(ctx, "SELECT BANNER FROM V$VERSION WHERE ROWNUM = 1").Scan(&banner); err != nil {
		var dummy int
		if err2 := d.db.QueryRowContext(ctx, "SELECT 1 FROM DUAL").Scan(&dummy); err2 != nil {
			return driver.ServerInfo{}, err2
		}
		banner = "Oracle Database"
	}
	return driver.ServerInfo{Version: banner, Product: "Oracle", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *Driver) ListDatabases(_ context.Context) ([]driver.DatabaseSummary, error) {
	name := d.cfg.Database
	if name == "" {
		name = "ORCL"
	}
	return []driver.DatabaseSummary{{Name: name}}, nil
}

func (d *Driver) CreateDatabase(_ context.Context, _, _ string) error {
	return fmt.Errorf("for Oracle, please create tablespaces or user schemas via an administrator account")
}

func (d *Driver) ListSchemas(ctx context.Context, _ string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT USERNAME FROM ALL_USERS ORDER BY USERNAME")
	if err != nil {
		return []string{strings.ToUpper(d.cfg.Username)}, nil
	}
	defer rows.Close()
	var list []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			list = append(list, u)
		}
	}
	return list, nil
}

func (d *Driver) ListObjects(ctx context.Context, database, schema string) (driver.SchemaObjects, error) {
	if schema == "" {
		schema = strings.ToUpper(d.cfg.Username)
	}
	res := driver.SchemaObjects{Database: database, Schema: schema, Tables: []driver.ObjectItem{}, Views: []driver.ObjectItem{}, Routines: []driver.ObjectItem{}}
	tRows, err := d.db.QueryContext(ctx, "SELECT TABLE_NAME, COALESCE(NUM_ROWS, 0) FROM ALL_TABLES WHERE OWNER = :1 ORDER BY TABLE_NAME", schema)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var n string
			var r int64
			if tRows.Scan(&n, &r) == nil {
				res.Tables = append(res.Tables, driver.ObjectItem{Name: n, Type: "TABLE", Rows: r})
			}
		}
	}
	vRows, err := d.db.QueryContext(ctx, "SELECT VIEW_NAME FROM ALL_VIEWS WHERE OWNER = :1 ORDER BY VIEW_NAME", schema)
	if err == nil {
		defer vRows.Close()
		for vRows.Next() {
			var n string
			if vRows.Scan(&n) == nil {
				res.Views = append(res.Views, driver.ObjectItem{Name: n, Type: "VIEW"})
			}
		}
	}
	return res, nil
}

func (d *Driver) GetTableStructure(ctx context.Context, database, schema, table string) (driver.TableStructure, error) {
	if schema == "" {
		schema = strings.ToUpper(d.cfg.Username)
	}
	st := driver.TableStructure{Database: database, Schema: schema, Table: table, Columns: []driver.ColumnDef{}, Indexes: []driver.IndexDef{}, ForeignKeys: []driver.ForeignKeyDef{}}
	rows, err := d.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE, NULLABLE
		FROM ALL_TAB_COLUMNS WHERE OWNER = :1 AND TABLE_NAME = :2 ORDER BY COLUMN_ID
	`, schema, table)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, t, nul string
		if rows.Scan(&n, &t, &nul) == nil {
			st.Columns = append(st.Columns, driver.ColumnDef{Name: n, Type: t, Nullable: nul == "Y"})
		}
	}
	_ = d.db.QueryRowContext(ctx, "SELECT DBMS_METADATA.GET_DDL('TABLE', :1, :2) FROM DUAL", table, schema).Scan(&st.DDL)
	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Schema == "" {
		req.Schema = strings.ToUpper(d.cfg.Username)
	}
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectOracle, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	res, err := sqlbase.ExecuteSingleQuery(ctx, d.db, dataSQL, req.PageSize)
	if err != nil {
		return res, err
	}
	var total int64
	if d.db.QueryRowContext(ctx, countSQL).Scan(&total) == nil {
		res.Total = total
		res.HasTotal = true
	}
	return res, nil
}

func (d *Driver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	return sqlbase.BuildMutationSQLs(sqlbase.DialectOracle, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectOracle, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectOracle, req)
	if err != nil || req.PreviewOnly {
		return ddl, err
	}
	_, err = d.db.ExecContext(ctx, ddl)
	return ddl, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectOracle, req)
	if err != nil || req.PreviewOnly {
		return stmts, err
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	if req.Schema == "" {
		req.Schema = strings.ToUpper(d.cfg.Username)
	}
	reqCopy := req
	reqCopy.Database = ""
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectOracle, reqCopy)
	if err != nil {
		return err
	}
	for _, s := range sqls {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) ExecuteSQL(ctx context.Context, _, query string, maxRows int) ([]driver.QueryResult, error) {
	return sqlbase.ExecuteMultiSQL(ctx, d.db, query, maxRows)
}

func (d *Driver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if req.Schema == "" {
		req.Schema = strings.ToUpper(d.cfg.Username)
	}
	if strings.TrimSpace(req.CustomSQL) == "" && (strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0) {
		names := req.Tables
		if len(names) == 0 {
			objs, err := d.ListObjects(ctx, req.Database, req.Schema)
			if err != nil {
				return err
			}
			for _, t := range objs.Tables {
				names = append(names, t.Name)
			}
		}
		items := make([]sqlbase.TableExportItem, 0, len(names))
		for _, name := range names {
			ddl := ""
			if req.IncludeDDL {
				if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, name); sErr == nil {
					ddl = st.DDL
				}
			}
			items = append(items, sqlbase.TableExportItem{Table: name, DDL: ddl})
		}
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectOracle, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); sErr == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectOracle, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectOracle, req)
}

func (d *Driver) GetAutocompleteMeta(ctx context.Context, _ string) (driver.AutocompleteMeta, error) {
	return driver.AutocompleteMeta{Tables: map[string][]string{}}, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}, CanKillSession: true}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT SID || ',' || SERIAL#, COALESCE(USERNAME,''), COALESCE(MACHINE,''), STATUS, COALESCE(LAST_CALL_ET,0)
		FROM V$SESSION WHERE USERNAME IS NOT NULL
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, usr, mach, st string
			var dur int64
			if rows.Scan(&id, &usr, &mach, &st, &dur) == nil {
				snap.Sessions = append(snap.Sessions, driver.SessionItem{ID: id, User: usr, Host: mach, State: st, DurationSec: dur})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf("ALTER SYSTEM KILL SESSION %s IMMEDIATE", sqlbase.FormatLiteral(sessionID)))
	return err
}
