package dameng

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	_ "gitee.com/chunanyong/dm"
)

func init() {
	fields := driver.StandardHostFields(5236, "Default Schema (optional)", false)
	fields[2].DefaultValue = "SYSDBA"
	driver.Register(driver.DriverMeta{
		Type:            "dameng",
		Name:            "Dameng DM8",
		Category:        driver.CategoryRelational,
		BadgeColor:      "#fa541c",
		DefaultPort:     5236,
		Description:     "Enterprise relational database Dameng DM8 (pure Go cross-platform driver with multi-schema and session management)",
		SupportsSchemas: true,
		SupportsExplain: true,
		DefaultTypes:    []string{"BIGINT", "INT", "VARCHAR(255)", "TEXT", "DECIMAL(18,2)", "DOUBLE", "BIT", "TIMESTAMP", "DATE", "BLOB"},
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
		port = 5236
	}
	user := cfg.Username
	if user == "" {
		user = "SYSDBA"
	}
	dsn := fmt.Sprintf("dm://%s:%s@%s:%d", url.PathEscape(user), url.PathEscape(cfg.Password), cfg.Host, port)
	if cfg.Database != "" {
		dsn += "?schema=" + url.QueryEscape(cfg.Database)
	}
	db, err := sql.Open("dm", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	return &Driver{cfg: cfg, db: db}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("dameng")
	return m
}

func (d *Driver) Close() error { return d.db.Close() }

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var banner string
	if err := d.db.QueryRowContext(ctx, "SELECT BANNER FROM V$VERSION WHERE ROWNUM = 1").Scan(&banner); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{Version: banner, Product: "Dameng DM8", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *Driver) ListDatabases(_ context.Context) ([]driver.DatabaseSummary, error) {
	return []driver.DatabaseSummary{{Name: "DAMENG"}}, nil
}

func (d *Driver) CreateDatabase(ctx context.Context, name, _ string) error {
	_, err := d.db.ExecContext(ctx, "CREATE SCHEMA "+sqlbase.QuoteIdent(sqlbase.DialectDameng, name))
	return err
}

func (d *Driver) CreateSchema(ctx context.Context, _, schema string) error {
	_, err := d.db.ExecContext(ctx, "CREATE SCHEMA "+sqlbase.QuoteIdent(sqlbase.DialectDameng, schema))
	return err
}

func (d *Driver) DropSchema(ctx context.Context, _, schema string, cascade bool) error {
	upper := strings.ToUpper(strings.TrimSpace(schema))
	if upper == "" || upper == "SYS" || upper == "SYSDBA" || upper == "SYSSSO" || upper == "SYSAUDITOR" || upper == "CTISYS" {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	mode := " RESTRICT"
	if cascade {
		mode = " CASCADE"
	}
	_, err := d.db.ExecContext(ctx, "DROP SCHEMA "+sqlbase.QuoteIdent(sqlbase.DialectDameng, schema)+mode)
	return err
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
	tRows, err := d.db.QueryContext(ctx, "SELECT TABLE_NAME, COALESCE(NUM_ROWS, 0) FROM ALL_TABLES WHERE OWNER = ? ORDER BY TABLE_NAME", schema)
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
	vRows, err := d.db.QueryContext(ctx, "SELECT VIEW_NAME FROM ALL_VIEWS WHERE OWNER = ? ORDER BY VIEW_NAME", schema)
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
	rows, err := d.db.QueryContext(ctx, "SELECT COLUMN_NAME, DATA_TYPE, NULLABLE FROM ALL_TAB_COLUMNS WHERE OWNER = ? AND TABLE_NAME = ? ORDER BY COLUMN_ID", schema, table)
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
	st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectDameng, driver.CreateTableRequest{Schema: schema, Table: table, Columns: st.Columns})
	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Schema == "" {
		req.Schema = strings.ToUpper(d.cfg.Username)
	}
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectDameng, req)
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
	return sqlbase.BuildMutationSQLs(sqlbase.DialectDameng, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectDameng, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectDameng, req)
	if err != nil || req.PreviewOnly {
		return ddl, err
	}
	_, err = d.db.ExecContext(ctx, ddl)
	return ddl, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectDameng, req)
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
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectDameng, reqCopy)
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
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectDameng, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); sErr == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectDameng, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectDameng, req)
}

func (d *Driver) GetAutocompleteMeta(_ context.Context, _ string) (driver.AutocompleteMeta, error) {
	return driver.AutocompleteMeta{Tables: map[string][]string{}}, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}, CanKillSession: true}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	}
	rows, err := d.db.QueryContext(ctx, "SELECT SESS_ID, COALESCE(USER_NAME,''), COALESCE(CLNT_IP,''), COALESCE(STATE,''), COALESCE(SQL_TEXT,'') FROM V$SESSIONS LIMIT 100")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sid int64
			var usr, ip, st, q string
			if rows.Scan(&sid, &usr, &ip, &st, &q) == nil {
				snap.Sessions = append(snap.Sessions, driver.SessionItem{ID: strconv.FormatInt(sid, 10), User: usr, Host: ip, State: st, Query: q})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	sid, err := strconv.ParseInt(sessionID, 10, 64)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, fmt.Sprintf("CALL SP_CLOSE_SESSION(%d)", sid))
	return err
}
