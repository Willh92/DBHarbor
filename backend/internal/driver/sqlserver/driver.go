package sqlserver

import (
	"context"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	mssql "github.com/microsoft/go-mssqldb"
)

func init() {
	fields := driver.StandardHostFields(1433, "Default Database", false)
	fields[4].DefaultValue = "master"
	fields = append(fields, driver.ConnFieldSchema{
		Key: "encrypt", Label: "Encryption (encrypt)", Type: "select", Target: "options", DefaultValue: "disable",
		SelectOptions: []driver.SelectOption{
			{Label: "Disabled (disable)", Value: "disable"},
			{Label: "Verified TLS (encrypt=true)", Value: "verified"},
			{Label: "Skip Certificate Verification (true + TrustServerCertificate)", Value: "true"},
		},
	})
	driver.Register(driver.DriverMeta{
		Type:               "sqlserver",
		Name:               "SQL Server",
		Category:           driver.CategoryRelational,
		BadgeColor:         "#cf1322",
		DefaultPort:        1433,
		Description:        "Microsoft SQL Server enterprise relational database (supports multi-schema and DMV session monitoring)",
		SupportsSchemas:    true,
		SupportsPartitions: true,
		SupportsExplain:    false,
		DefaultTypes:       []string{"BIGINT", "INT", "NVARCHAR(255)", "VARCHAR(255)", "NVARCHAR(MAX)", "DECIMAL(18,2)", "FLOAT", "BIT", "DATETIME2", "DATE", "UNIQUEIDENTIFIER"},
		Fields:             fields,
	}, New)
}

var _ driver.PartitionManager = (*Driver)(nil)
var _ driver.SchemaManager = (*Driver)(nil)

type restrictedMSSQLDialer struct {
	fn func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d restrictedMSSQLDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.fn(ctx, network, addr)
}

type Driver struct {
	cfg driver.ConnectionConfig
	db  *sql.DB
}

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	restrictIP := cfg.Option("__restrict_internal_ip", "") == "1"
	if restrictIP {
		if err := sqlbase.ValidateRemoteHost(context.Background(), cfg.Host); err != nil {
			return nil, err
		}
	}
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
	host := strings.TrimSpace(cfg.Host)
	if host == "" && !restrictIP {
		host = "127.0.0.1"
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, cfg.Password),
		Host:   net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)),
	}
	q := u.Query()
	q.Set("database", dbName)
	enc := cfg.Option("encrypt", "disable")
	switch enc {
	case "verified":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "false")
	case "true":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	default:
		q.Set("encrypt", enc)
	}
	u.RawQuery = q.Encode()

	var (
		db  *sql.DB
		err error
	)
	if restrictIP {
		connector, cErr := mssql.NewConnector(u.String())
		if cErr != nil {
			return nil, cErr
		}
		connector.Dialer = restrictedMSSQLDialer{fn: sqlbase.NewRestrictedDialContext(8 * time.Second)}
		db = sql.OpenDB(connector)
	} else {
		db, err = sql.Open("sqlserver", u.String())
		if err != nil {
			return nil, err
		}
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)
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
	return d.execInDatabase(ctx, database, []string{stmt})
}

func (d *Driver) DropSchema(ctx context.Context, database, schema string, _ bool) error {
	lower := strings.ToLower(strings.TrimSpace(schema))
	if lower == "" || lower == "dbo" || lower == "sys" || lower == "information_schema" || lower == "guest" {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	stmt := "DROP SCHEMA " + sqlbase.QuoteIdent(sqlbase.DialectSQLServer, schema)
	return d.execInDatabase(ctx, database, []string{stmt})
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

func formatSQLServerColumnType(typeName string, maxLen, prec, scale int64) string {
	t := strings.ToLower(strings.TrimSpace(typeName))
	if t == "" {
		return ""
	}
	upper := strings.ToUpper(t)
	switch t {
	case "nvarchar", "nchar":
		if maxLen == -1 {
			return upper + "(MAX)"
		}
		if maxLen > 0 {
			return fmt.Sprintf("%s(%d)", upper, maxLen/2)
		}
	case "varchar", "char", "varbinary", "binary":
		if maxLen == -1 {
			return upper + "(MAX)"
		}
		if maxLen > 0 {
			return fmt.Sprintf("%s(%d)", upper, maxLen)
		}
	case "decimal", "numeric":
		if prec > 0 {
			return fmt.Sprintf("%s(%d,%d)", upper, prec, scale)
		}
	case "datetime2", "datetimeoffset", "time":
		if scale >= 0 && scale != 7 {
			return fmt.Sprintf("%s(%d)", upper, scale)
		}
	}
	return upper
}

func (d *Driver) getPrimaryKeys(ctx context.Context, database, schema, table string) []string {
	if schema == "" {
		schema = "dbo"
	}
	prefix := ""
	if database != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT c.name
		FROM %ssys.indexes i
		JOIN %ssys.tables t ON i.object_id = t.object_id
		JOIN %ssys.schemas s ON t.schema_id = s.schema_id
		JOIN %ssys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id AND ic.is_included_column = 0
		JOIN %ssys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		WHERE s.name = @p1 AND t.name = @p2 AND i.is_primary_key = 1
		ORDER BY ic.key_ordinal
	`, prefix, prefix, prefix, prefix, prefix), schema, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var pks []string
	for rows.Next() {
		var col string
		if rows.Scan(&col) == nil {
			pks = append(pks, col)
		}
	}
	return pks
}

func (d *Driver) acquireConnForDB(ctx context.Context, database string) (*sql.Conn, func(), error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	targetDB := strings.TrimSpace(database)
	defaultDB := strings.TrimSpace(d.cfg.Database)
	if defaultDB == "" {
		defaultDB = "master"
	}
	switched := false
	if targetDB != "" {
		if _, uErr := conn.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, targetDB)); uErr != nil {
			_ = conn.Close()
			return nil, nil, uErr
		}
		if !strings.EqualFold(targetDB, defaultDB) {
			switched = true
		}
	}
	cleanup := func() {
		if switched {
			rCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, rErr := conn.ExecContext(rCtx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectSQLServer, defaultDB))
			cancel()
			if rErr != nil {
				_ = conn.Raw(func(_ any) error { return stdsql_driver.ErrBadConn })
			}
		}
		_ = conn.Close()
	}
	return conn, cleanup, nil
}

func (d *Driver) execInDatabase(ctx context.Context, database string, stmts []string) error {
	if len(stmts) == 0 {
		return nil
	}
	if strings.TrimSpace(database) != "" {
		conn, cleanup, cErr := d.acquireConnForDB(ctx, database)
		if cErr != nil {
			return cErr
		}
		defer cleanup()
		for _, s := range stmts {
			if _, err := conn.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) GetTableStructure(ctx context.Context, database, schema, table string) (driver.TableStructure, error) {
	if schema == "" {
		schema = "dbo"
	}
	st := driver.TableStructure{
		Database:         database,
		Schema:           schema,
		Table:            table,
		Columns:          []driver.ColumnDef{},
		Indexes:          []driver.IndexDef{},
		ForeignKeys:      []driver.ForeignKeyDef{},
		CheckConstraints: []driver.CheckConstraintDef{},
	}
	prefix := ""
	if database != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}

	// Table comment
	_ = d.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(CAST(ep.value AS NVARCHAR(4000)), '')
		FROM %ssys.tables t
		JOIN %ssys.schemas s ON t.schema_id = s.schema_id
		JOIN %ssys.extended_properties ep ON ep.major_id = t.object_id AND ep.minor_id = 0 AND ep.class = 1 AND ep.name = 'MS_Description'
		WHERE s.name = @p1 AND t.name = @p2
	`, prefix, prefix, prefix), schema, table).Scan(&st.Comment)

	// Indexes + Primary Keys
	pkSet := map[string]bool{}
	idxRows, idxErr := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT i.name, i.is_unique, i.is_primary_key, COALESCE(i.type_desc, ''), c.name
		FROM %ssys.indexes i
		JOIN %ssys.tables t ON i.object_id = t.object_id
		JOIN %ssys.schemas s ON t.schema_id = s.schema_id
		JOIN %ssys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id AND ic.is_included_column = 0
		JOIN %ssys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
		WHERE s.name = @p1 AND t.name = @p2 AND i.name IS NOT NULL
		ORDER BY i.index_id, ic.key_ordinal
	`, prefix, prefix, prefix, prefix, prefix), schema, table)
	if idxErr == nil {
		defer idxRows.Close()
		idxMap := map[string]*driver.IndexDef{}
		var idxOrder []string
		for idxRows.Next() {
			var iName, iType, colName string
			var isUniq, isPK bool
			if idxRows.Scan(&iName, &isUniq, &isPK, &iType, &colName) == nil {
				if isPK {
					pkSet[colName] = true
				}
				item, exists := idxMap[iName]
				if !exists {
					item = &driver.IndexDef{
						Name:      iName,
						Unique:    isUniq,
						Primary:   isPK,
						IndexType: iType,
					}
					idxMap[iName] = item
					idxOrder = append(idxOrder, iName)
				}
				item.Columns = append(item.Columns, colName)
			}
		}
		for _, name := range idxOrder {
			st.Indexes = append(st.Indexes, *idxMap[name])
		}
	}

	// Columns (works for both tables and views via sys.objects)
	cRows, cErr := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT c.name,
		       COALESCE(tp.name, ''),
		       CAST(c.max_length AS BIGINT),
		       CAST(c.precision AS BIGINT),
		       CAST(c.scale AS BIGINT),
		       c.is_nullable,
		       c.is_identity,
		       COALESCE(dc.definition, ''),
		       COALESCE(cc.definition, ''),
		       COALESCE(cc.is_persisted, CAST(0 AS BIT)),
		       COALESCE(CAST(ep.value AS NVARCHAR(4000)), '')
		FROM %ssys.columns c
		JOIN %ssys.objects o ON c.object_id = o.object_id AND o.type IN ('U', 'V')
		JOIN %ssys.schemas s ON o.schema_id = s.schema_id
		LEFT JOIN %ssys.types tp ON c.user_type_id = tp.user_type_id
		LEFT JOIN %ssys.default_constraints dc ON c.default_object_id = dc.object_id
		LEFT JOIN %ssys.computed_columns cc ON c.object_id = cc.object_id AND c.column_id = cc.column_id
		LEFT JOIN %ssys.extended_properties ep ON ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.class = 1 AND ep.name = 'MS_Description'
		WHERE s.name = @p1 AND o.name = @p2
		ORDER BY c.column_id
	`, prefix, prefix, prefix, prefix, prefix, prefix, prefix), schema, table)
	if cErr != nil {
		return st, cErr
	}
	defer cRows.Close()
	for cRows.Next() {
		var name, typeName, defVal, compDef, cmt string
		var maxLen, prec, scale int64
		var isNull, isIdent, isPersisted bool
		if cRows.Scan(&name, &typeName, &maxLen, &prec, &scale, &isNull, &isIdent, &defVal, &compDef, &isPersisted, &cmt) == nil {
			genExpr := strings.TrimSpace(compDef)
			genStorage := ""
			if genExpr != "" {
				if isPersisted {
					genStorage = "STORED"
				} else {
					genStorage = "VIRTUAL"
				}
			}
			st.Columns = append(st.Columns, driver.ColumnDef{
				Name:             name,
				Type:             formatSQLServerColumnType(typeName, maxLen, prec, scale),
				Nullable:         isNull && !pkSet[name],
				DefaultValue:     strings.TrimSpace(defVal),
				IsPrimaryKey:     pkSet[name],
				IsAutoIncrement:  isIdent,
				Comment:          strings.TrimSpace(cmt),
				GeneratedExpr:    genExpr,
				GeneratedStorage: genStorage,
			})
		}
	}

	// Foreign Keys
	fkRows, fkErr := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT fk.name,
		       pc.name,
		       rt.name,
		       rc.name,
		       COALESCE(REPLACE(fk.update_referential_action_desc, '_', ' '), ''),
		       COALESCE(REPLACE(fk.delete_referential_action_desc, '_', ' '), '')
		FROM %ssys.foreign_keys fk
		JOIN %ssys.tables pt ON fk.parent_object_id = pt.object_id
		JOIN %ssys.schemas ps ON pt.schema_id = ps.schema_id
		JOIN %ssys.foreign_key_columns fkc ON fk.object_id = fkc.constraint_object_id
		JOIN %ssys.columns pc ON fkc.parent_object_id = pc.object_id AND fkc.parent_column_id = pc.column_id
		JOIN %ssys.tables rt ON fk.referenced_object_id = rt.object_id
		JOIN %ssys.columns rc ON fkc.referenced_object_id = rc.object_id AND fkc.referenced_column_id = rc.column_id
		WHERE ps.name = @p1 AND pt.name = @p2
		ORDER BY fk.name, fkc.constraint_column_id
	`, prefix, prefix, prefix, prefix, prefix, prefix, prefix), schema, table)
	if fkErr == nil {
		defer fkRows.Close()
		fkMap := map[string]*driver.ForeignKeyDef{}
		var fkOrder []string
		for fkRows.Next() {
			var fkName, colName, refTbl, refCol, onUpd, onDel string
			if fkRows.Scan(&fkName, &colName, &refTbl, &refCol, &onUpd, &onDel) == nil {
				item, exists := fkMap[fkName]
				if !exists {
					item = &driver.ForeignKeyDef{
						Name:     fkName,
						RefTable: refTbl,
						OnUpdate: onUpd,
						OnDelete: onDel,
					}
					fkMap[fkName] = item
					fkOrder = append(fkOrder, fkName)
				}
				item.Columns = append(item.Columns, colName)
				item.RefColumns = append(item.RefColumns, refCol)
			}
		}
		for _, name := range fkOrder {
			st.ForeignKeys = append(st.ForeignKeys, *fkMap[name])
		}
	}

	// Check Constraints
	chkRows, chkErr := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT cc.name, COALESCE(cc.definition, ''), cc.is_disabled
		FROM %ssys.check_constraints cc
		JOIN %ssys.tables t ON cc.parent_object_id = t.object_id
		JOIN %ssys.schemas s ON t.schema_id = s.schema_id
		WHERE s.name = @p1 AND t.name = @p2
		ORDER BY cc.name
	`, prefix, prefix, prefix), schema, table)
	if chkErr == nil {
		defer chkRows.Close()
		for chkRows.Next() {
			var cName, expr string
			var isDisabled bool
			if chkRows.Scan(&cName, &expr, &isDisabled) == nil && strings.TrimSpace(expr) != "" {
				st.CheckConstraints = append(st.CheckConstraints, driver.CheckConstraintDef{
					Name:       cName,
					Expression: strings.TrimSpace(expr),
					Enforced:   !isDisabled,
				})
			}
		}
	}

	if len(st.Columns) > 0 {
		st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectSQLServer, driver.CreateTableRequest{
			Schema:           schema,
			Table:            table,
			Columns:          st.Columns,
			ForeignKeys:      st.ForeignKeys,
			CheckConstraints: st.CheckConstraints,
		})
	}
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
	var res driver.QueryResult
	if strings.TrimSpace(req.Database) != "" {
		conn, cleanup, cErr := d.acquireConnForDB(ctx, req.Database)
		if cErr != nil {
			return driver.QueryResult{}, cErr
		}
		defer cleanup()
		res, err = sqlbase.ExecuteSingleQueryConn(ctx, conn, dataSQL, req.PageSize)
		if err != nil {
			return res, err
		}
		var total int64
		if conn.QueryRowContext(ctx, countSQL).Scan(&total) == nil {
			res.Total = total
			res.HasTotal = true
		}
		res.PrimaryKeys = d.getPrimaryKeys(ctx, req.Database, req.Schema, req.Table)
		return res, nil
	}
	res, err = sqlbase.ExecuteSingleQuery(ctx, d.db, dataSQL, req.PageSize)
	if err != nil {
		return res, err
	}
	var total int64
	if d.db.QueryRowContext(ctx, countSQL).Scan(&total) == nil {
		res.Total = total
		res.HasTotal = true
	}
	res.PrimaryKeys = d.getPrimaryKeys(ctx, req.Database, req.Schema, req.Table)
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
	if err != nil {
		return ddl, err
	}
	stmts := []string{ddl}
	if strings.TrimSpace(req.Comment) != "" {
		stmts = append(stmts, sqlbase.BuildSQLServerExtendedPropertySQL(req.Schema, req.Table, "", req.Comment))
	}
	for _, col := range req.Columns {
		if strings.TrimSpace(col.Name) != "" && strings.TrimSpace(col.Comment) != "" {
			stmts = append(stmts, sqlbase.BuildSQLServerExtendedPropertySQL(req.Schema, req.Table, col.Name, col.Comment))
		}
	}
	fullScript := strings.Join(stmts, ";\n")
	if req.PreviewOnly {
		return fullScript, nil
	}
	err = d.execInDatabase(ctx, req.Database, stmts)
	return fullScript, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectSQLServer, req)
	if err != nil || req.PreviewOnly {
		return stmts, err
	}
	err = d.execInDatabase(ctx, req.Database, stmts)
	return stmts, err
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	if req.Schema == "" {
		req.Schema = "dbo"
	}
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectSQLServer, req)
	if err != nil {
		return err
	}
	return d.execInDatabase(ctx, req.Database, sqls)
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	if strings.TrimSpace(database) != "" {
		conn, cleanup, cErr := d.acquireConnForDB(ctx, database)
		if cErr != nil {
			return nil, cErr
		}
		defer cleanup()
		return sqlbase.ExecuteMultiSQLConn(ctx, conn, query, maxRows)
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
	prefix := ""
	if strings.TrimSpace(database) != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT TABLE_NAME, COLUMN_NAME
		FROM %sINFORMATION_SCHEMA.COLUMNS
		ORDER BY TABLE_NAME, ORDINAL_POSITION
	`, prefix))
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var tName, cName string
			if rows.Scan(&tName, &cName) == nil {
				meta.Tables[tName] = append(meta.Tables[tName], cName)
			}
		}
	}
	return meta, nil
}

func (d *Driver) ListPartitions(ctx context.Context, database, schema, table string) ([]driver.PartitionInfo, error) {
	if schema == "" {
		schema = "dbo"
	}
	out := []driver.PartitionInfo{}
	prefix := ""
	if strings.TrimSpace(database) != "" {
		prefix = sqlbase.QuoteIdent(sqlbase.DialectSQLServer, database) + "."
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT CAST(p.partition_number AS NVARCHAR(32)),
		       COALESCE(SUM(p.rows), 0),
		       COALESCE(SUM(au.total_pages), 0) * 8192,
		       COALESCE(MAX(pf.name), ''),
		       COALESCE(MAX(CAST(rv.value AS NVARCHAR(512))), '')
		FROM %ssys.partitions p
		JOIN %ssys.tables t ON p.object_id = t.object_id
		JOIN %ssys.schemas s ON t.schema_id = s.schema_id
		LEFT JOIN %ssys.allocation_units au ON p.partition_id = au.container_id
		LEFT JOIN %ssys.indexes i ON p.object_id = i.object_id AND p.index_id = i.index_id
		LEFT JOIN %ssys.partition_schemes ps ON i.data_space_id = ps.data_space_id
		LEFT JOIN %ssys.partition_functions pf ON ps.function_id = pf.function_id
		LEFT JOIN %ssys.partition_range_values rv ON pf.function_id = rv.function_id AND rv.boundary_id = p.partition_number
		WHERE s.name = @p1 AND t.name = @p2 AND p.index_id IN (0, 1)
		GROUP BY p.partition_number
		ORDER BY p.partition_number
	`, prefix, prefix, prefix, prefix, prefix, prefix, prefix, prefix), schema, table)
	if err != nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var pNum, pfName, bound string
		var numRows, bytesOnDisk int64
		if rows.Scan(&pNum, &numRows, &bytesOnDisk, &pfName, &bound) == nil {
			out = append(out, driver.PartitionInfo{
				ID:               pNum,
				Name:             "Partition #" + pNum,
				Rows:             numRows,
				BytesOnDisk:      bytesOnDisk,
				Active:           true,
				Strategy:         pfName,
				BoundDescription: bound,
			})
		}
	}
	return out, nil
}

func (d *Driver) ManagePartition(ctx context.Context, req driver.PartitionActionRequest) (string, error) {
	schema := strings.TrimSpace(req.Schema)
	if schema == "" {
		schema = "dbo"
	}
	if strings.TrimSpace(req.Table) == "" || strings.TrimSpace(req.PartitionID) == "" {
		return "", fmt.Errorf("table and partitionId are required")
	}
	if !strings.EqualFold(strings.TrimSpace(req.Action), "TRUNCATE") {
		return "", fmt.Errorf("unsupported partition action %q for SQL Server (only TRUNCATE is supported)", req.Action)
	}
	partID, err := strconv.ParseInt(strings.TrimSpace(req.PartitionID), 10, 64)
	if err != nil || partID < 1 {
		return "", fmt.Errorf("invalid SQL Server partition number %q", req.PartitionID)
	}
	tbl := sqlbase.QualifyTable(sqlbase.DialectSQLServer, "", schema, req.Table)
	stmt := fmt.Sprintf("TRUNCATE TABLE %s WITH (PARTITIONS (%d))", tbl, partID)
	if req.PreviewOnly {
		return stmt, nil
	}
	err = d.execInDatabase(ctx, req.Database, []string{stmt})
	return stmt, err
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
