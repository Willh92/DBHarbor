package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func init() {
	pgTypes := []string{"BIGSERIAL", "SERIAL", "BIGINT", "INTEGER", "VARCHAR(255)", "TEXT", "NUMERIC(18,2)", "DOUBLE PRECISION", "BOOLEAN", "TIMESTAMPTZ", "TIMESTAMP", "DATE", "JSONB", "UUID", "BYTEA"}

	variants := []struct {
		typ   string
		name  string
		color string
		port  int
		desc  string
	}{
		{"postgres", "PostgreSQL", "#1d39c4", 5432, "Enterprise open-source object-relational database (full support for multi-database, multi-schema, JSONB, and session management)"},
		{"kingbase", "KingbaseES", "#c41d7f", 54321, "Enterprise relational database KingbaseES (PostgreSQL-compatible protocol with multi-schema management)"},
	}

	for _, v := range variants {
		variant := v
		fields := driver.StandardHostFields(variant.port, "Initial Database", true)
		fields[4].DefaultValue = "postgres"
		fields = append(fields, driver.ConnFieldSchema{
			Key: "sslmode", Label: "SSL Mode (sslmode)", Type: "select", Target: "options", DefaultValue: "disable",
			SelectOptions: []driver.SelectOption{
				{Label: "Disable (disable)", Value: "disable"},
				{Label: "Prefer (prefer)", Value: "prefer"},
				{Label: "Require Encryption (require)", Value: "require"},
			},
		})
		driver.Register(driver.DriverMeta{
			Type:            variant.typ,
			Name:            variant.name,
			Category:        driver.CategoryRelational,
			BadgeColor:      variant.color,
			DefaultPort:     variant.port,
			Description:     variant.desc,
			SupportsSchemas: true,
			SupportsExplain: true,
			DefaultTypes:    pgTypes,
			Fields:          fields,
		}, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
			return New(cfg, variant.typ, variant.name, variant.port)
		})
	}
}

type Driver struct {
	cfg         driver.ConnectionConfig
	variantType string
	productName string
	defaultPort int
	mu          sync.Mutex
	pools       map[string]*sql.DB
}

func New(cfg driver.ConnectionConfig, variantType, productName string, defaultPort int) (driver.Driver, error) {
	d := &Driver{
		cfg:         cfg,
		variantType: variantType,
		productName: productName,
		defaultPort: defaultPort,
		pools:       make(map[string]*sql.DB),
	}
	initDB := cfg.Database
	if initDB == "" {
		initDB = "postgres"
	}
	if _, err := d.poolForDB(initDB); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Driver) poolForDB(dbName string) (*sql.DB, error) {
	if dbName == "" {
		dbName = d.cfg.Database
	}
	if dbName == "" {
		dbName = "postgres"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.pools[dbName]; ok {
		return existing, nil
	}

	port := d.cfg.Port
	if port == 0 {
		port = d.defaultPort
	}
	user := d.cfg.Username
	if user == "" {
		user = "postgres"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, d.cfg.Password),
		Host:   fmt.Sprintf("%s:%d", d.cfg.Host, port),
		Path:   "/" + dbName,
	}
	q := u.Query()
	q.Set("sslmode", d.cfg.Option("sslmode", "disable"))
	q.Set("connect_timeout", "8")
	u.RawQuery = q.Encode()

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(15 * time.Minute)
	d.pools[dbName] = db
	return db, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta(d.variantType)
	return m
}

func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, db := range d.pools {
		_ = db.Close()
	}
	d.pools = map[string]*sql.DB{}
	return nil
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	db, err := d.poolForDB("")
	if err != nil {
		return driver.ServerInfo{}, err
	}
	var version string
	if err := db.QueryRowContext(ctx, "SHOW server_version").Scan(&version); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{
		Version:   version,
		Product:   d.productName,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	db, err := d.poolForDB("")
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT datname, pg_encoding_to_char(encoding), COALESCE(pg_database_size(datname), 0)
		FROM pg_database WHERE datistemplate = false ORDER BY datname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []driver.DatabaseSummary
	for rows.Next() {
		var name, enc string
		var sz int64
		if err := rows.Scan(&name, &enc, &sz); err != nil {
			return nil, err
		}
		list = append(list, driver.DatabaseSummary{Name: name, Charset: enc, SizeBytes: sz})
	}
	return list, rows.Err()
}

func (d *Driver) CreateDatabase(ctx context.Context, name, _ string) error {
	db, err := d.poolForDB("")
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "CREATE DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, name))
	return err
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	d.mu.Lock()
	if existing, ok := d.pools[name]; ok {
		delete(d.pools, name)
		_ = existing.Close()
	}
	d.mu.Unlock()

	adminDBName := "postgres"
	if strings.EqualFold(d.cfg.Database, name) {
		adminDBName = "postgres"
	} else if d.cfg.Database != "" {
		adminDBName = d.cfg.Database
	}
	db, err := d.poolForDB(adminDBName)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "DROP DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, name))
	return err
}

func (d *Driver) CreateSchema(ctx context.Context, database, schema string) error {
	db, err := d.poolForDB(database)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, schema))
	return err
}

func (d *Driver) DropSchema(ctx context.Context, database, schema string, cascade bool) error {
	lower := strings.ToLower(strings.TrimSpace(schema))
	if lower == "" || lower == "pg_catalog" || lower == "information_schema" || strings.HasPrefix(lower, "pg_toast") {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	db, err := d.poolForDB(database)
	if err != nil {
		return err
	}
	mode := " RESTRICT"
	if cascade {
		mode = " CASCADE"
	}
	_, err = db.ExecContext(ctx, "DROP SCHEMA "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, schema)+mode)
	return err
}

func (d *Driver) ListSchemas(ctx context.Context, database string) ([]string, error) {
	db, err := d.poolForDB(database)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT schema_name FROM information_schema.schemata
		WHERE schema_name NOT LIKE 'pg_%' AND schema_name <> 'information_schema'
		ORDER BY CASE WHEN schema_name = 'public' THEN 0 ELSE 1 END, schema_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schemas []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			schemas = append(schemas, s)
		}
	}
	if len(schemas) == 0 {
		schemas = []string{"public"}
	}
	return schemas, nil
}

func (d *Driver) ListObjects(ctx context.Context, database, schema string) (driver.SchemaObjects, error) {
	if schema == "" {
		schema = "public"
	}
	db, err := d.poolForDB(database)
	if err != nil {
		return driver.SchemaObjects{}, err
	}
	res := driver.SchemaObjects{
		Database: database,
		Schema:   schema,
		Tables:   []driver.ObjectItem{},
		Views:    []driver.ObjectItem{},
		Routines: []driver.ObjectItem{},
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			c.relname,
			c.relkind::text,
			COALESCE(c.reltuples::bigint, 0),
			COALESCE(pg_total_relation_size(c.oid), 0),
			COALESCE(obj_description(c.oid), '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm')
		ORDER BY c.relname
	`, schema)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var name, kind, comment string
		var rCnt, bCnt int64
		if err := rows.Scan(&name, &kind, &rCnt, &bCnt, &comment); err != nil {
			return res, err
		}
		if rCnt < 0 {
			rCnt = 0
		}
		item := driver.ObjectItem{
			Name:      name,
			Rows:      rCnt,
			DataBytes: bCnt,
			Comment:   comment,
		}
		if kind == "v" || kind == "m" {
			item.Type = "VIEW"
			res.Views = append(res.Views, item)
		} else {
			item.Type = "TABLE"
			res.Tables = append(res.Tables, item)
		}
	}
	return res, nil
}

func (d *Driver) GetTableStructure(ctx context.Context, database, schema, table string) (driver.TableStructure, error) {
	if schema == "" {
		schema = "public"
	}
	db, err := d.poolForDB(database)
	if err != nil {
		return driver.TableStructure{}, err
	}
	st := driver.TableStructure{
		Database:    database,
		Schema:      schema,
		Table:       table,
		Columns:     []driver.ColumnDef{},
		Indexes:     []driver.IndexDef{},
		ForeignKeys: []driver.ForeignKeyDef{},
	}

	// Primary key set
	pkSet := map[string]bool{}
	pkRows, err := db.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.indisprimary AND n.nspname = $1 AND c.relname = $2
	`, schema, table)
	if err == nil {
		defer pkRows.Close()
		for pkRows.Next() {
			var pk string
			if pkRows.Scan(&pk) == nil {
				pkSet[pk] = true
			}
		}
	}

	cRows, err := db.QueryContext(ctx, `
		SELECT
			c.column_name,
			c.udt_name,
			c.character_maximum_length,
			c.is_nullable,
			c.column_default,
			COALESCE(pgd.description, '')
		FROM information_schema.columns c
		LEFT JOIN pg_catalog.pg_statio_all_tables st ON st.schemaname = c.table_schema AND st.relname = c.table_name
		LEFT JOIN pg_catalog.pg_description pgd ON pgd.objoid = st.relid AND pgd.objsubid = c.ordinal_position
		WHERE c.table_schema = $1 AND c.table_name = $2
		ORDER BY c.ordinal_position
	`, schema, table)
	if err != nil {
		return st, err
	}
	defer cRows.Close()

	for cRows.Next() {
		var name, udt, isNull, desc string
		var maxLen sql.NullInt64
		var defVal sql.NullString
		if err := cRows.Scan(&name, &udt, &maxLen, &isNull, &defVal, &desc); err != nil {
			return st, err
		}
		colType := udt
		if maxLen.Valid && maxLen.Int64 > 0 {
			colType = fmt.Sprintf("%s(%d)", udt, maxLen.Int64)
		}
		def := ""
		if defVal.Valid {
			def = defVal.String
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:            name,
			Type:            colType,
			Nullable:        strings.EqualFold(isNull, "YES"),
			DefaultValue:    def,
			IsPrimaryKey:    pkSet[name],
			IsAutoIncrement: strings.Contains(def, "nextval("),
			Comment:         desc,
		})
	}

	// Indexes
	iRows, err := db.QueryContext(ctx, `SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2`, schema, table)
	if err == nil {
		defer iRows.Close()
		for iRows.Next() {
			var idxName, idxDef string
			if iRows.Scan(&idxName, &idxDef) == nil {
				st.Indexes = append(st.Indexes, driver.IndexDef{
					Name:      idxName,
					Unique:    strings.Contains(strings.ToUpper(idxDef), "UNIQUE"),
					Primary:   strings.HasSuffix(idxName, "_pkey"),
					IndexType: idxDef,
				})
			}
		}
	}

	// Foreign keys
	fkRows, fkErr := db.QueryContext(ctx, `
		SELECT
			tc.constraint_name,
			kcu.column_name,
			ccu.table_name AS ref_table,
			ccu.column_name AS ref_column,
			rc.update_rule,
			rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_name = tc.constraint_name AND ccu.table_schema = tc.table_schema
		LEFT JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name AND rc.constraint_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY tc.constraint_name, kcu.ordinal_position
	`, schema, table)
	if fkErr == nil {
		defer fkRows.Close()
		fkMap := map[string]*driver.ForeignKeyDef{}
		var fkOrder []string
		for fkRows.Next() {
			var cName, colName, refTbl, refCol, updRule, delRule string
			if fkRows.Scan(&cName, &colName, &refTbl, &refCol, &updRule, &delRule) == nil {
				item, exists := fkMap[cName]
				if !exists {
					item = &driver.ForeignKeyDef{
						Name:     cName,
						RefTable: refTbl,
						OnUpdate: updRule,
						OnDelete: delRule,
					}
					fkMap[cName] = item
					fkOrder = append(fkOrder, cName)
				}
				item.Columns = append(item.Columns, colName)
				item.RefColumns = append(item.RefColumns, refCol)
			}
		}
		for _, k := range fkOrder {
			st.ForeignKeys = append(st.ForeignKeys, *fkMap[k])
		}
	}

	// Reconstruct clean DDL
	if ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, driver.CreateTableRequest{
		Schema: schema, Table: table, Columns: st.Columns, ForeignKeys: st.ForeignKeys,
	}); err == nil {
		st.DDL = ddl
	}
	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return driver.QueryResult{}, err
	}
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectPostgres, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	res, err := sqlbase.ExecuteSingleQuery(ctx, db, dataSQL, req.PageSize)
	if err != nil {
		return res, err
	}
	var total int64
	if db.QueryRowContext(ctx, countSQL).Scan(&total) == nil {
		res.Total = total
		res.HasTotal = true
	}
	if st, err := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); err == nil {
		for _, c := range st.Columns {
			if c.IsPrimaryKey {
				res.PrimaryKeys = append(res.PrimaryKeys, c.Name)
			}
		}
	}
	return res, nil
}

func (d *Driver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	return sqlbase.BuildMutationSQLs(sqlbase.DialectPostgres, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return driver.MutationResult{}, err
	}
	return sqlbase.ApplyMutations(ctx, db, sqlbase.DialectPostgres, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, req)
	if err != nil {
		return "", err
	}
	if req.PreviewOnly {
		return ddl, nil
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return ddl, err
	}
	_, err = db.ExecContext(ctx, ddl)
	return ddl, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectPostgres, req)
	if err != nil {
		return nil, err
	}
	if req.PreviewOnly {
		return stmts, nil
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return stmts, err
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return err
	}
	reqCopy := req
	reqCopy.Database = ""
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectPostgres, reqCopy)
	if err != nil {
		return err
	}
	for _, s := range sqls {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	db, err := d.poolForDB(database)
	if err != nil {
		return nil, err
	}
	return sqlbase.ExecuteMultiSQL(ctx, db, query, maxRows)
}

func (d *Driver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.CustomSQL) == "" && (strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0) {
		names := req.Tables
		if len(names) == 0 {
			objs, oErr := d.ListObjects(ctx, req.Database, req.Schema)
			if oErr != nil {
				return oErr
			}
			for _, t := range objs.Tables {
				names = append(names, t.Name)
			}
		}
		items := make([]sqlbase.TableExportItem, 0, len(names))
		for _, name := range names {
			var ddl string
			var idxDDLs []string
			if req.IncludeDDL {
				if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, name); sErr == nil {
					ddl = st.DDL
					for _, idx := range st.Indexes {
						if !idx.Primary && strings.HasPrefix(strings.ToUpper(idx.IndexType), "CREATE ") {
							idxDDLs = append(idxDDLs, idx.IndexType)
						}
					}
				}
			}
			items = append(items, sqlbase.TableExportItem{Table: name, DDL: ddl, IndexDDLs: idxDDLs})
		}
		return sqlbase.StreamMultiTableExport(ctx, db, sqlbase.DialectPostgres, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); sErr == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, db, sqlbase.DialectPostgres, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, err := d.poolForDB(req.Database)
	if err != nil {
		return driver.ImportResult{}, err
	}
	return sqlbase.BatchImport(ctx, db, sqlbase.DialectPostgres, req)
}

func (d *Driver) GetAutocompleteMeta(ctx context.Context, database string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Tables: map[string][]string{}}
	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			meta.Databases = append(meta.Databases, item.Name)
		}
	}
	db, err := d.poolForDB(database)
	if err != nil {
		return meta, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema NOT LIKE 'pg_%' AND table_schema <> 'information_schema'
		ORDER BY table_name, ordinal_position
	`)
	if err != nil {
		return meta, nil
	}
	defer rows.Close()
	for rows.Next() {
		var t, c string
		if rows.Scan(&t, &c) == nil {
			meta.Tables[t] = append(meta.Tables[t], c)
		}
	}
	return meta, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	db, err := d.poolForDB("")
	if err != nil {
		return driver.MonitorSnapshot{}, err
	}
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	_ = db.QueryRowContext(ctx, "SHOW server_version").Scan(&snap.Version)
	_ = db.QueryRowContext(ctx, "SELECT COALESCE(EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))::bigint, 0)").Scan(&snap.UptimeSec)

	var activeConns, totalConns int64
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE state = 'active'), COUNT(*) FROM pg_stat_activity").Scan(&activeConns, &totalConns)
	snap.Metrics = append(snap.Metrics,
		driver.MonitorMetric{Key: "total_conns", Label: "Total Connections", Value: strconv.FormatInt(totalConns, 10)},
		driver.MonitorMetric{Key: "active_conns", Label: "Active Queries", Value: strconv.FormatInt(activeConns, 10)},
	)

	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			snap.Storage = append(snap.Storage, driver.StorageItem{Name: item.Name, Category: "DATABASE", SizeBytes: item.SizeBytes})
		}
	}

	pRows, err := db.QueryContext(ctx, `
		SELECT pid, COALESCE(usename, ''), COALESCE(client_addr::text, 'local'), COALESCE(datname, ''),
		       COALESCE(state, ''), COALESCE(EXTRACT(EPOCH FROM (now() - query_start))::bigint, 0), COALESCE(query, '')
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		ORDER BY query_start ASC NULLS LAST LIMIT 100
	`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var pid, dur int64
			var usr, host, dat, state, q string
			if pRows.Scan(&pid, &usr, &host, &dat, &state, &dur, &q) == nil {
				if dur < 0 {
					dur = 0
				}
				snap.Sessions = append(snap.Sessions, driver.SessionItem{
					ID: strconv.FormatInt(pid, 10), User: usr, Host: host, Database: dat, State: state, DurationSec: dur, Query: q,
				})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	db, err := d.poolForDB("")
	if err != nil {
		return err
	}
	pid, err := strconv.ParseInt(sessionID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid PID: %s", sessionID)
	}
	_, err = db.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid)
	return err
}
