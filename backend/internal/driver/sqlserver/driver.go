package sqlserver

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

	_ "github.com/microsoft/go-mssqldb"
)

func init() {
	fields := driver.StandardHostFields(1433, "Default Database", false)
	fields[4].DefaultValue = "master"
	fields = append(fields, driver.ConnFieldSchema{
		Key: "encrypt", Label: "Encryption (encrypt)", Type: "select", Target: "options", DefaultValue: "disable",
		SelectOptions: []driver.SelectOption{
			{Label: "Disabled (disable)", Value: "disable"},
			{Label: "Skip Certificate Verification (true + TrustServerCertificate)", Value: "true"},
		},
	})
	driver.Register(driver.DriverMeta{
		Type:            "sqlserver",
		Name:            "SQL Server",
		Category:        driver.CategoryRelational,
		BadgeColor:      "#cf1322",
		DefaultPort:     1433,
		Description:     "Microsoft SQL Server enterprise relational database (supports multi-schema and DMV session monitoring)",
		SupportsSchemas: true,
		SupportsExplain: false,
		DefaultTypes:    []string{"BIGINT", "INT", "NVARCHAR(255)", "VARCHAR(255)", "NVARCHAR(MAX)", "DECIMAL(18,2)", "FLOAT", "BIT", "DATETIME2", "DATE", "UNIQUEIDENTIFIER"},
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
		port = 1433
	}
	user := cfg.Username
	if user == "" {
		user = "sa"
	}
	dbName := cfg.Database
	if dbName == "" {
		dbName = "master"
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, cfg.Password),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, port),
	}
	q := u.Query()
	q.Set("database", dbName)
	enc := cfg.Option("encrypt", "disable")
	q.Set("encrypt", enc)
	if enc == "true" {
		q.Set("TrustServerCertificate", "true")
	}
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	return &Driver{cfg: cfg, db: db}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("sqlserver")
	return m
}

func (d *Driver) Close() error { return d.db.Close() }

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var ver string
	if err := d.db.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('ProductVersion') AS NVARCHAR(128))").Scan(&ver); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{Version: ver, Product: "SQL Server", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name FROM sys.databases ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []driver.DatabaseSummary
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out = append(out, driver.DatabaseSummary{Name: name})
		}
	}
	return out, nil
}

func (d *Driver) CreateDatabase(ctx context.Context, name, _ string) error {
	_, err := d.db.ExecContext(ctx, "CREATE DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, name))
	return err
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, "DROP DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, name))
	return err
}

func (d *Driver) CreateSchema(ctx context.Context, database, schema string) error {
	stmt := "CREATE SCHEMA " + sqlbase.QuoteIdent(sqlbase.DialectSQLServer, schema)
	if database != "" {
		escaped := strings.ReplaceAll(stmt, "'", "''")
		stmt = fmt.Sprintf("USE %s; EXEC(N'%s');", sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database), escaped)
	}
	_, err := d.db.ExecContext(ctx, stmt)
	return err
}

func (d *Driver) DropSchema(ctx context.Context, database, schema string, _ bool) error {
	lower := strings.ToLower(strings.TrimSpace(schema))
	if lower == "" || lower == "dbo" || lower == "sys" || lower == "information_schema" || lower == "guest" {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	stmt := "DROP SCHEMA " + sqlbase.QuoteIdent(sqlbase.DialectSQLServer, schema)
	if database != "" {
		escaped := strings.ReplaceAll(stmt, "'", "''")
		stmt = fmt.Sprintf("USE %s; EXEC(N'%s');", sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database), escaped)
	}
	_, err := d.db.ExecContext(ctx, stmt)
	return err
}

func (d *Driver) ListSchemas(ctx context.Context, database string) ([]string, error) {
	prefix := ""
	if database != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf("SELECT name FROM %ssys.schemas WHERE principal_id = 1 OR name = 'dbo' ORDER BY CASE WHEN name='dbo' THEN 0 ELSE 1 END, name", prefix))
	if err != nil {
		return []string{"dbo"}, nil
	}
	defer rows.Close()
	var list []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			list = append(list, s)
		}
	}
	if len(list) == 0 {
		list = []string{"dbo"}
	}
	return list, nil
}

func (d *Driver) ListObjects(ctx context.Context, database, schema string) (driver.SchemaObjects, error) {
	if schema == "" {
		schema = "dbo"
	}
	res := driver.SchemaObjects{Database: database, Schema: schema, Tables: []driver.ObjectItem{}, Views: []driver.ObjectItem{}, Routines: []driver.ObjectItem{}}
	prefix := ""
	if database != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, TABLE_TYPE FROM %sINFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = @p1 ORDER BY TABLE_NAME
	`, prefix), schema)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, t string
		if rows.Scan(&n, &t) == nil {
			if strings.Contains(strings.ToUpper(t), "VIEW") {
				res.Views = append(res.Views, driver.ObjectItem{Name: n, Type: "VIEW"})
			} else {
				res.Tables = append(res.Tables, driver.ObjectItem{Name: n, Type: "TABLE"})
			}
		}
	}
	return res, nil
}

func (d *Driver) GetTableStructure(ctx context.Context, database, schema, table string) (driver.TableStructure, error) {
	if schema == "" {
		schema = "dbo"
	}
	st := driver.TableStructure{Database: database, Schema: schema, Table: table, Columns: []driver.ColumnDef{}, Indexes: []driver.IndexDef{}, ForeignKeys: []driver.ForeignKeyDef{}}
	prefix := ""
	if database != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '')
		FROM %sINFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2
		ORDER BY ORDINAL_POSITION
	`, prefix), schema, table)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, t, isNull, def string
		if rows.Scan(&n, &t, &isNull, &def) == nil {
			st.Columns = append(st.Columns, driver.ColumnDef{
				Name: n, Type: t, Nullable: strings.EqualFold(isNull, "YES"), DefaultValue: def,
			})
		}
	}
	st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectSQLServer, driver.CreateTableRequest{Schema: schema, Table: table, Columns: st.Columns})
	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectSQLServer, req)
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
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	return sqlbase.BuildMutationSQLs(sqlbase.DialectSQLServer, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectSQLServer, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectSQLServer, req)
	if err != nil || req.PreviewOnly {
		return ddl, err
	}
	_, err = d.db.ExecContext(ctx, ddl)
	return ddl, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectSQLServer, req)
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
		req.Schema = "dbo"
	}
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectSQLServer, req)
	if err != nil {
		return err
	}
	conn, cErr := d.db.Conn(ctx)
	if cErr == nil {
		defer conn.Close()
		if req.Database != "" {
			_, _ = conn.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, req.Database))
		}
		for _, s := range sqls {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}
	for _, s := range sqls {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	if database != "" {
		_, _ = d.db.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database))
	}
	return sqlbase.ExecuteMultiSQL(ctx, d.db, query, maxRows)
}

func (d *Driver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if req.Schema == "" {
		req.Schema = "dbo"
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
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectSQLServer, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); sErr == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectSQLServer, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectSQLServer, req)
}

func (d *Driver) GetAutocompleteMeta(ctx context.Context, database string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Tables: map[string][]string{}}
	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			meta.Databases = append(meta.Databases, item.Name)
		}
	}
	return meta, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}, CanKillSession: true}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT s.session_id, COALESCE(s.login_name,''), COALESCE(s.host_name,''), COALESCE(DB_NAME(r.database_id),''),
		       COALESCE(r.status, s.status), COALESCE(r.total_elapsed_time/1000, 0), COALESCE(t.text, '')
		FROM sys.dm_exec_sessions s
		LEFT JOIN sys.dm_exec_requests r ON s.session_id = r.session_id
		OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
		WHERE s.is_user_process = 1
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sid, dur int64
			var usr, host, dbn, st, q string
			if rows.Scan(&sid, &usr, &host, &dbn, &st, &dur, &q) == nil {
				snap.Sessions = append(snap.Sessions, driver.SessionItem{
					ID: strconv.FormatInt(sid, 10), User: usr, Host: host, Database: dbn, State: st, DurationSec: dur, Query: q,
				})
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
	_, err = d.db.ExecContext(ctx, fmt.Sprintf("KILL %d", sid))
	return err
}
