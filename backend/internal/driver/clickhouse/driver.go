package clickhouse

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:               "clickhouse",
		Name:               "ClickHouse",
		Category:           driver.CategoryOLAP,
		BadgeColor:         "#faad14",
		DefaultPort:        9000,
		Description:        "High-performance columnar OLAP analytical database (supports Native TCP / HTTP protocols, MergeTree partition management, and system metrics)",
		SupportsSchemas:    false,
		SupportsPartitions: true,
		SupportsExplain:    true,
		DefaultTypes:       []string{"UInt64", "Int64", "Int32", "String", "FixedString(32)", "Float64", "Decimal(18,2)", "DateTime", "DateTime64(3)", "Date", "UUID", "LowCardinality(String)", "Array(String)", "Map(String, String)"},
		Fields: []driver.ConnFieldSchema{
			{
				Key: "protocol", Label: "Protocol", Type: "select", Target: "options", DefaultValue: "native",
				SelectOptions: []driver.SelectOption{
					{Label: "Native TCP (default port 9000)", Value: "native"},
					{Label: "HTTP / HTTPS (default port 8123)", Value: "http"},
				},
			},
			{Key: "host", Label: "Host", Type: "text", Target: "root", Placeholder: "127.0.0.1", DefaultValue: "127.0.0.1", Required: true},
			{Key: "port", Label: "Port", Type: "number", Target: "root", DefaultValue: "9000", Required: true},
			{Key: "username", Label: "Username", Type: "text", Target: "root", DefaultValue: "default"},
			{Key: "password", Label: "Password", Type: "password", Target: "root"},
			{Key: "database", Label: "Default Database", Type: "text", Target: "root", DefaultValue: "default"},
			{
				Key: "tls", Label: "Enable TLS Encryption", Type: "select", Target: "options", DefaultValue: "false",
				SelectOptions: []driver.SelectOption{
					{Label: "Disabled", Value: "false"},
					{Label: "TLS (Verify Server Certificate)", Value: "true"},
					{Label: "TLS (Skip Certificate Verification)", Value: "skip-verify"},
				},
			},
		},
	}, New)
}

type Driver struct {
	cfg driver.ConnectionConfig
	db  *sql.DB
}

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	port := cfg.Port
	proto := ch.Native
	if strings.EqualFold(cfg.Option("protocol", "native"), "http") {
		proto = ch.HTTP
		if port == 0 {
			port = 8123
		}
	} else if port == 0 {
		port = 9000
	}
	user := cfg.Username
	if user == "" {
		user = "default"
	}
	dbName := cfg.Database
	if dbName == "" {
		dbName = "default"
	}

	opts := &ch.Options{
		Addr:     []string{fmt.Sprintf("%s:%d", cfg.Host, port)},
		Protocol: proto,
		Auth: ch.Auth{
			Database: dbName,
			Username: user,
			Password: cfg.Password,
		},
		DialTimeout: 8 * time.Second,
		Compression: &ch.Compression{Method: ch.CompressionLZ4},
	}
	switch cfg.Option("tls", "false") {
	case "true":
		opts.TLS = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	case "skip-verify":
		opts.TLS = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}

	db := ch.OpenDB(opts)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)
	return &Driver{cfg: cfg, db: db}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("clickhouse")
	return m
}

func (d *Driver) Close() error {
	return d.db.Close()
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var version string
	if err := d.db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{
		Version:   version,
		Product:   "ClickHouse",
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			d.name,
			count(t.name) AS tbl_cnt,
			coalesce(sum(t.total_bytes), 0) AS total_bytes
		FROM system.databases AS d
		LEFT JOIN system.tables AS t ON d.name = t.database
		GROUP BY d.name
		ORDER BY d.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []driver.DatabaseSummary
	for rows.Next() {
		var name string
		var cnt uint64
		var size uint64
		if err := rows.Scan(&name, &cnt, &size); err != nil {
			return nil, err
		}
		out = append(out, driver.DatabaseSummary{
			Name:        name,
			TablesCount: int(cnt),
			SizeBytes:   int64(size),
		})
	}
	return out, rows.Err()
}

func (d *Driver) CreateDatabase(ctx context.Context, name, _ string) error {
	_, err := d.db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, name))
	return err
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, "DROP DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, name))
	return err
}

func (d *Driver) ListSchemas(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}

func (d *Driver) ListObjects(ctx context.Context, database, _ string) (driver.SchemaObjects, error) {
	if database == "" {
		database = "default"
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			name,
			engine,
			coalesce(total_rows, 0),
			coalesce(total_bytes, 0),
			comment
		FROM system.tables
		WHERE database = ?
		ORDER BY name
	`, database)
	if err != nil {
		return driver.SchemaObjects{}, err
	}
	defer rows.Close()

	res := driver.SchemaObjects{
		Database: database,
		Tables:   []driver.ObjectItem{},
		Views:    []driver.ObjectItem{},
		Routines: []driver.ObjectItem{},
	}
	for rows.Next() {
		var name, engine, comment string
		var rowCnt, bytesCnt uint64
		if err := rows.Scan(&name, &engine, &rowCnt, &bytesCnt, &comment); err != nil {
			return res, err
		}
		item := driver.ObjectItem{
			Name:      name,
			Engine:    engine,
			Rows:      int64(rowCnt),
			DataBytes: int64(bytesCnt),
			Comment:   comment,
		}
		if strings.Contains(strings.ToUpper(engine), "VIEW") {
			item.Type = "VIEW"
			res.Views = append(res.Views, item)
		} else {
			item.Type = "TABLE"
			res.Tables = append(res.Tables, item)
		}
	}
	return res, rows.Err()
}

func (d *Driver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	if database == "" {
		database = "default"
	}
	st := driver.TableStructure{
		Database:    database,
		Table:       table,
		Columns:     []driver.ColumnDef{},
		Indexes:     []driver.IndexDef{},
		ForeignKeys: []driver.ForeignKeyDef{},
		Options:     map[string]string{},
	}

	var engine, sortingKey, partitionKey, comment, createQuery string
	_ = d.db.QueryRowContext(ctx, `
		SELECT engine, sorting_key, partition_key, comment, create_table_query
		FROM system.tables WHERE database = ? AND name = ?
	`, database, table).Scan(&engine, &sortingKey, &partitionKey, &comment, &createQuery)

	st.Engine = engine
	st.Comment = comment
	st.DDL = createQuery
	st.Options["sortingKey"] = sortingKey
	st.Options["partitionKey"] = partitionKey

	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, default_kind, default_expression, is_in_primary_key, comment
		FROM system.columns
		WHERE database = ? AND table = ?
		ORDER BY position
	`, database, table)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	var pkCols []string
	for rows.Next() {
		var name, colType, defKind, defExpr, colComment string
		var isPK uint8
		if err := rows.Scan(&name, &colType, &defKind, &defExpr, &isPK, &colComment); err != nil {
			return st, err
		}
		nullable := strings.HasPrefix(colType, "Nullable(")
		defVal := ""
		if defKind != "" && defExpr != "" {
			defVal = defExpr
		}
		if isPK > 0 {
			pkCols = append(pkCols, name)
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:         name,
			Type:         colType,
			Nullable:     nullable,
			DefaultValue: defVal,
			IsPrimaryKey: isPK > 0,
			Comment:      colComment,
		})
	}
	if len(pkCols) > 0 {
		st.Indexes = append(st.Indexes, driver.IndexDef{
			Name:      "PRIMARY / ORDER BY",
			Columns:   pkCols,
			Primary:   true,
			Unique:    false,
			IndexType: "SPARSE_PRIMARY",
		})
	}
	return st, rows.Err()
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Database == "" {
		req.Database = "default"
	}
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectClickHouse, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	res, err := sqlbase.ExecuteSingleQuery(ctx, d.db, dataSQL, req.PageSize)
	if err != nil {
		return res, err
	}
	var total uint64
	if err := d.db.QueryRowContext(ctx, countSQL).Scan(&total); err == nil {
		res.Total = int64(total)
		res.HasTotal = true
	}
	if st, err := d.GetTableStructure(ctx, req.Database, "", req.Table); err == nil {
		for _, c := range st.Columns {
			if c.IsPrimaryKey {
				res.PrimaryKeys = append(res.PrimaryKeys, c.Name)
			}
		}
	}
	return res, nil
}

func (d *Driver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	return sqlbase.BuildMutationSQLs(sqlbase.DialectClickHouse, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectClickHouse, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectClickHouse, req)
	if err != nil {
		return "", err
	}
	if req.PreviewOnly {
		return ddl, nil
	}
	_, err = d.db.ExecContext(ctx, ddl)
	return ddl, err
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectClickHouse, req)
	if err != nil {
		return nil, err
	}
	if req.PreviewOnly {
		return stmts, nil
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectClickHouse, req)
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

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	if database != "" {
		conn, cErr := d.db.Conn(ctx)
		if cErr == nil {
			defer func() {
				defaultDB := d.cfg.Database
				if defaultDB == "" {
					defaultDB = "default"
				}
				if defaultDB != database {
					_, _ = conn.ExecContext(context.Background(), "USE "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, defaultDB))
				}
				_ = conn.Close()
			}()
			_, _ = conn.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, database))
			return sqlbase.ExecuteMultiSQLConn(ctx, conn, query, maxRows)
		}
	}
	return sqlbase.ExecuteMultiSQL(ctx, d.db, query, maxRows)
}

func (d *Driver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if strings.TrimSpace(req.CustomSQL) == "" && (strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0) {
		names := req.Tables
		if len(names) == 0 {
			objs, err := d.ListObjects(ctx, req.Database, "")
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
				if st, sErr := d.GetTableStructure(ctx, req.Database, "", name); sErr == nil {
					ddl = st.DDL
				}
			}
			items = append(items, sqlbase.TableExportItem{Table: name, DDL: ddl})
		}
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectClickHouse, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, err := d.GetTableStructure(ctx, req.Database, "", req.Table); err == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectClickHouse, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectClickHouse, req)
}

func (d *Driver) GetAutocompleteMeta(ctx context.Context, database string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Tables: map[string][]string{}}
	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			meta.Databases = append(meta.Databases, item.Name)
		}
	}
	if database == "" {
		database = "default"
	}
	rows, err := d.db.QueryContext(ctx, `SELECT table, name FROM system.columns WHERE database = ? ORDER BY table, position`, database)
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

// PartitionManager implementation for ClickHouse
func (d *Driver) ListPartitions(ctx context.Context, database, table string) ([]driver.PartitionInfo, error) {
	if database == "" {
		database = "default"
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			partition_id,
			partition,
			sum(rows) AS total_rows,
			sum(bytes_on_disk) AS disk_bytes,
			count() AS parts_cnt
		FROM system.parts
		WHERE database = ? AND table = ? AND active = 1
		GROUP BY partition_id, partition
		ORDER BY partition_id DESC
	`, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []driver.PartitionInfo
	for rows.Next() {
		var pid, pname string
		var rCnt, bCnt, pCnt uint64
		if err := rows.Scan(&pid, &pname, &rCnt, &bCnt, &pCnt); err != nil {
			return nil, err
		}
		list = append(list, driver.PartitionInfo{
			ID:          pid,
			Name:        pname,
			Rows:        int64(rCnt),
			BytesOnDisk: int64(bCnt),
			PartsCount:  int64(pCnt),
			Active:      true,
		})
	}
	if list == nil {
		list = []driver.PartitionInfo{}
	}
	return list, nil
}

func (d *Driver) ManagePartition(ctx context.Context, database, table, partitionID, action string) error {
	tbl := sqlbase.QualifyTable(sqlbase.DialectClickHouse, database, "", table)
	safeID := sqlbase.FormatLiteralForDialect(sqlbase.DialectClickHouse, partitionID)
	switch strings.ToUpper(action) {
	case "DROP":
		_, err := d.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP PARTITION ID %s", tbl, safeID))
		return err
	case "DETACH":
		_, err := d.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DETACH PARTITION ID %s", tbl, safeID))
		return err
	case "ATTACH":
		_, err := d.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ATTACH PARTITION ID %s", tbl, safeID))
		return err
	case "OPTIMIZE":
		_, err := d.db.ExecContext(ctx, fmt.Sprintf("OPTIMIZE TABLE %s PARTITION ID %s FINAL", tbl, safeID))
		return err
	default:
		return fmt.Errorf("unsupported partition action: %s", action)
	}
}

// MonitorProvider implementation
func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	_ = d.db.QueryRowContext(ctx, "SELECT version()").Scan(&snap.Version)
	var uptime uint32
	if d.db.QueryRowContext(ctx, "SELECT uptime()").Scan(&uptime) == nil {
		snap.UptimeSec = int64(uptime)
	}

	mRows, err := d.db.QueryContext(ctx, `
		SELECT metric, value FROM system.metrics
		WHERE metric IN ('Query', 'TCPConnection', 'HTTPConnection', 'MemoryTracking', 'BackgroundPoolTask')
	`)
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var k string
			var v int64
			if mRows.Scan(&k, &v) == nil {
				valStr := fmt.Sprintf("%d", v)
				unit := ""
				if k == "MemoryTracking" {
					valStr = fmt.Sprintf("%.2f", float64(v)/1024/1024)
					unit = "MB"
				}
				snap.Metrics = append(snap.Metrics, driver.MonitorMetric{Key: k, Label: k, Value: valStr, Unit: unit})
			}
		}
	}

	sRows, err := d.db.QueryContext(ctx, `
		SELECT concat(database, '.', name), engine, coalesce(total_rows, 0), coalesce(total_bytes, 0)
		FROM system.tables
		WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
		ORDER BY total_bytes DESC LIMIT 15
	`)
	if err == nil {
		defer sRows.Close()
		for sRows.Next() {
			var name, eng string
			var rCnt, bCnt uint64
			if sRows.Scan(&name, &eng, &rCnt, &bCnt) == nil {
				snap.Storage = append(snap.Storage, driver.StorageItem{
					Name: name, Category: eng, Rows: int64(rCnt), SizeBytes: int64(bCnt),
				})
			}
		}
	}

	pRows, err := d.db.QueryContext(ctx, `
		SELECT query_id, user, toString(address), elapsed, query
		FROM system.processes ORDER BY elapsed DESC LIMIT 100
	`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var qid, usr, addr, q string
			var elapsed float64
			if pRows.Scan(&qid, &usr, &addr, &elapsed, &q) == nil {
				snap.Sessions = append(snap.Sessions, driver.SessionItem{
					ID: qid, User: usr, Host: addr, State: "RUNNING", DurationSec: int64(elapsed), Query: q,
				})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	_, err := d.db.ExecContext(ctx, "KILL QUERY WHERE query_id = ?", sessionID)
	return err
}
