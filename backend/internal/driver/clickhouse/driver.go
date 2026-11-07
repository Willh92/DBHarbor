package clickhouse

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

var reClickHouseTTL = regexp.MustCompile(`(?is)\bTTL\s+(.+?)(?:\bSETTINGS\b|\bCOMMENT\b|$)`)

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

	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, errors.New("ClickHouse host cannot be empty")
	}
	if cfg.Option("__web_mode", "") == "1" {
		cleanHost, err := sqlbase.ValidateStrictHostname(host)
		if err != nil {
			return nil, err
		}
		host = cleanHost
	} else {
		host = strings.Trim(host, "[]")
	}

	opts := &ch.Options{
		Addr:     []string{net.JoinHostPort(host, strconv.Itoa(port))},
		Protocol: proto,
		Auth: ch.Auth{
			Database: dbName,
			Username: user,
			Password: cfg.Password,
		},
		DialTimeout: 8 * time.Second,
		Compression: &ch.Compression{Method: ch.CompressionLZ4},
	}
	if cfg.Option("__restrict_internal_ip", "") == "1" {
		restricted := sqlbase.NewRestrictedDialContext(8 * time.Second)
		opts.DialContext = func(ctx context.Context, addr string) (net.Conn, error) {
			return restricted(ctx, "tcp", addr)
		}
	}
	switch cfg.Option("tls", "false") {
	case "true":
		opts.TLS = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	case "skip-verify":
		opts.TLS = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}

	db := ch.OpenDB(opts)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
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
		Database:          database,
		Tables:            []driver.ObjectItem{},
		Views:             []driver.ObjectItem{},
		MaterializedViews: []driver.ObjectItem{},
		Dictionaries:      []driver.ObjectItem{},
		Routines:          []driver.ObjectItem{},
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
		switch {
		case strings.EqualFold(engine, "MaterializedView"):
			item.Type = "MATERIALIZED_VIEW"
			res.MaterializedViews = append(res.MaterializedViews, item)
		case strings.EqualFold(engine, "Dictionary"):
			item.Type = "DICTIONARY"
			res.Dictionaries = append(res.Dictionaries, item)
		case strings.Contains(strings.ToUpper(engine), "VIEW"):
			item.Type = "VIEW"
			res.Views = append(res.Views, item)
		default:
			item.Type = "TABLE"
			res.Tables = append(res.Tables, item)
		}
	}
	return res, rows.Err()
}

func parseClickHouseProjectionsFromDDL(ddl string) []driver.ProjectionDef {
	var out []driver.ProjectionDef
	upper := strings.ToUpper(ddl)
	searchFrom := 0
	for searchFrom < len(ddl) {
		idx := strings.Index(upper[searchFrom:], "PROJECTION ")
		if idx < 0 {
			break
		}
		pos := searchFrom + idx
		if pos > 0 {
			prev := upper[pos-1]
			if (prev >= 'A' && prev <= 'Z') || (prev >= '0' && prev <= '9') || prev == '_' {
				searchFrom = pos + len("PROJECTION ")
				continue
			}
		}
		i := pos + len("PROJECTION ")
		for i < len(ddl) && (ddl[i] == ' ' || ddl[i] == '\t' || ddl[i] == '\r' || ddl[i] == '\n') {
			i++
		}
		if i >= len(ddl) {
			break
		}
		var name string
		if ddl[i] == '`' || ddl[i] == '"' {
			q := ddl[i]
			i++
			start := i
			for i < len(ddl) && ddl[i] != q {
				i++
			}
			name = ddl[start:i]
			if i < len(ddl) {
				i++
			}
		} else {
			start := i
			for i < len(ddl) && ddl[i] != ' ' && ddl[i] != '\t' && ddl[i] != '\r' && ddl[i] != '\n' && ddl[i] != '(' {
				i++
			}
			name = ddl[start:i]
		}
		for i < len(ddl) && (ddl[i] == ' ' || ddl[i] == '\t' || ddl[i] == '\r' || ddl[i] == '\n') {
			i++
		}
		if name == "" || i >= len(ddl) || ddl[i] != '(' {
			searchFrom = i
			continue
		}
		depth := 0
		inSingle := false
		inBacktick := false
		qStart := i + 1
		qEnd := -1
		for j := i; j < len(ddl); j++ {
			ch := ddl[j]
			if inSingle {
				if ch == '\\' && j+1 < len(ddl) {
					j++
					continue
				}
				if ch == '\'' {
					inSingle = false
				}
				continue
			}
			if inBacktick {
				if ch == '`' {
					inBacktick = false
				}
				continue
			}
			switch ch {
			case '\'':
				inSingle = true
			case '`':
				inBacktick = true
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					qEnd = j
				}
			}
			if qEnd >= 0 {
				break
			}
		}
		if qEnd < 0 {
			break
		}
		out = append(out, driver.ProjectionDef{
			Name:  name,
			Query: strings.TrimSpace(ddl[qStart:qEnd]),
		})
		searchFrom = qEnd + 1
	}
	return out
}

func extractClickHouseTableTTL(engineFull, ddl string) string {
	for _, src := range []string{engineFull, ddl} {
		if strings.TrimSpace(src) == "" {
			continue
		}
		// Inspect portion starting at ENGINE = when parsing full CREATE TABLE DDL so column TTLs inside (...) are skipped
		target := src
		if engIdx := strings.Index(strings.ToUpper(src), "ENGINE ="); engIdx >= 0 {
			target = src[engIdx:]
		} else if engIdx := strings.Index(strings.ToUpper(src), "ENGINE="); engIdx >= 0 {
			target = src[engIdx:]
		}
		if m := reClickHouseTTL.FindStringSubmatch(target); len(m) == 2 {
			ttl := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), ";"))
			if ttl != "" {
				return ttl
			}
		}
	}
	return ""
}

func (d *Driver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	if database == "" {
		database = "default"
	}
	st := driver.TableStructure{
		Database:        database,
		Table:           table,
		Columns:         []driver.ColumnDef{},
		Indexes:         []driver.IndexDef{},
		ForeignKeys:     []driver.ForeignKeyDef{},
		Projections:     []driver.ProjectionDef{},
		SkippingIndexes: []driver.SkippingIndexDef{},
		Options:         map[string]string{},
	}

	var engine, engineFull, sortingKey, partitionKey, comment, createQuery string
	_ = d.db.QueryRowContext(ctx, `
		SELECT engine, engine_full, sorting_key, partition_key, comment, create_table_query
		FROM system.tables WHERE database = ? AND name = ?
	`, database, table).Scan(&engine, &engineFull, &sortingKey, &partitionKey, &comment, &createQuery)

	if strings.TrimSpace(createQuery) == "" {
		qTbl := sqlbase.QualifyTable(sqlbase.DialectClickHouse, database, "", table)
		_ = d.db.QueryRowContext(ctx, "SHOW CREATE TABLE "+qTbl).Scan(&createQuery)
	}

	st.Engine = engine
	st.Comment = comment
	st.DDL = createQuery
	st.TableTTL = extractClickHouseTableTTL(engineFull, createQuery)
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

	// Data skipping indices
	hasSkipBytes := true
	skRows, skErr := d.db.QueryContext(ctx, `
		SELECT name, coalesce(type_full, type), expr, granularity, coalesce(data_compressed_bytes, 0)
		FROM system.data_skipping_indices
		WHERE database = ? AND table = ?
		ORDER BY name
	`, database, table)
	if skErr != nil {
		hasSkipBytes = false
		skRows, skErr = d.db.QueryContext(ctx, `
			SELECT name, type, expr, granularity
			FROM system.data_skipping_indices
			WHERE database = ? AND table = ?
			ORDER BY name
		`, database, table)
	}
	if skErr == nil {
		defer skRows.Close()
		for skRows.Next() {
			var sName, sType, sExpr string
			var gran, diskBytes uint64
			var scanErr error
			if hasSkipBytes {
				scanErr = skRows.Scan(&sName, &sType, &sExpr, &gran, &diskBytes)
			} else {
				scanErr = skRows.Scan(&sName, &sType, &sExpr, &gran)
			}
			if scanErr == nil {
				st.SkippingIndexes = append(st.SkippingIndexes, driver.SkippingIndexDef{
					Name:        sName,
					Type:        sType,
					Expression:  sExpr,
					Granularity: int64(gran),
					BytesOnDisk: int64(diskBytes),
				})
			}
		}
	}

	// Projections (parsed from DDL + merged with active system.projection_parts)
	projList := parseClickHouseProjectionsFromDDL(st.DDL)
	projIndex := make(map[string]int, len(projList))
	for i, p := range projList {
		projIndex[p.Name] = i
	}
	ppRows, ppErr := d.db.QueryContext(ctx, `
		SELECT name, coalesce(sum(rows), 0), coalesce(sum(bytes_on_disk), 0)
		FROM system.projection_parts
		WHERE database = ? AND table = ? AND active = 1
		GROUP BY name
		ORDER BY name
	`, database, table)
	if ppErr == nil {
		defer ppRows.Close()
		for ppRows.Next() {
			var pName string
			var pRows, pBytes uint64
			if ppRows.Scan(&pName, &pRows, &pBytes) == nil {
				if idx, ok := projIndex[pName]; ok {
					projList[idx].Rows = int64(pRows)
					projList[idx].BytesOnDisk = int64(pBytes)
				} else {
					projList = append(projList, driver.ProjectionDef{
						Name:        pName,
						Rows:        int64(pRows),
						BytesOnDisk: int64(pBytes),
					})
				}
			}
		}
	}
	st.Projections = projList

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

func (d *Driver) isRestrictedWeb() bool {
	return d.cfg.Option("__web_mode", "") == "1" && d.cfg.Option("__restrict_internal_ip", "") == "1"
}

func (d *Driver) acquireConnForDB(ctx context.Context, database string) (*sql.Conn, func(), error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defaultDB := strings.TrimSpace(d.cfg.Database)
	if defaultDB == "" {
		defaultDB = "default"
	}
	targetDB := strings.TrimSpace(database)
	if targetDB == "" || targetDB == defaultDB {
		return conn, func() { _ = conn.Close() }, nil
	}
	if _, err := conn.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, targetDB)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	cleanup := func() {
		rCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, rErr := conn.ExecContext(rCtx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectClickHouse, defaultDB)); rErr != nil {
			_ = conn.Raw(func(_ any) error { return stdsql_driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	return conn, cleanup, nil
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	if d.isRestrictedWeb() {
		if err := sqlbase.ValidateClickHouseWebEngineClause(req.Engine); err != nil {
			return "", err
		}
	}
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectClickHouse, req)
	if err != nil {
		return "", err
	}
	if d.isRestrictedWeb() {
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(ddl); err != nil {
			return ddl, err
		}
	}
	if req.PreviewOnly {
		return ddl, nil
	}
	_, err = d.db.ExecContext(ctx, ddl)
	return ddl, err
}

func buildClickHouseAlterTableSQLs(req driver.AlterTableRequest) ([]string, error) {
	if strings.TrimSpace(req.Table) == "" {
		return nil, errors.New("table name cannot be empty")
	}
	tbl := sqlbase.QualifyTable(sqlbase.DialectClickHouse, req.Database, req.Schema, req.Table)
	var stmts []string

	for _, act := range req.Actions {
		actUpper := strings.ToUpper(strings.TrimSpace(act.Action))
		switch actUpper {
		case "ADD_PROJECTION":
			pName := strings.TrimSpace(act.Projection.Name)
			pQuery := strings.TrimSuffix(strings.TrimSpace(act.Projection.Query), ";")
			if pName == "" || pQuery == "" {
				return nil, errors.New("projection name and SELECT query are required")
			}
			if len(sqlbase.SplitStatements(pQuery)) != 1 {
				return nil, errors.New("projection query must be a single SELECT statement")
			}
			if err := sqlbase.ValidateReadOnlyScript(pQuery); err != nil {
				return nil, fmt.Errorf("invalid projection query: %w", err)
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD PROJECTION %s (%s)",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, pName), pQuery))

		case "DROP_PROJECTION":
			pName := strings.TrimSpace(act.Projection.Name)
			if pName == "" {
				pName = strings.TrimSpace(act.OldName)
			}
			if pName == "" {
				return nil, errors.New("projection name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP PROJECTION IF EXISTS %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, pName)))

		case "MATERIALIZE_PROJECTION":
			pName := strings.TrimSpace(act.Projection.Name)
			if pName == "" {
				pName = strings.TrimSpace(act.OldName)
			}
			if pName == "" {
				return nil, errors.New("projection name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MATERIALIZE PROJECTION %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, pName)))

		case "CLEAR_PROJECTION":
			pName := strings.TrimSpace(act.Projection.Name)
			if pName == "" {
				pName = strings.TrimSpace(act.OldName)
			}
			if pName == "" {
				return nil, errors.New("projection name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s CLEAR PROJECTION IF EXISTS %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, pName)))

		case "ADD_SKIPPING_INDEX":
			sk := act.SkippingIndex
			sName := strings.TrimSpace(sk.Name)
			sExpr := strings.TrimSpace(sk.Expression)
			sType := strings.TrimSpace(sk.Type)
			if sName == "" || sExpr == "" || sType == "" {
				return nil, errors.New("skipping index name, expression, and type are required")
			}
			if err := sqlbase.ValidateSQLExpression("skipping index expression", sExpr); err != nil {
				return nil, err
			}
			if err := sqlbase.ValidateSQLExpression("skipping index type", sType); err != nil {
				return nil, err
			}
			gran := sk.Granularity
			if gran <= 0 {
				gran = 1
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD INDEX %s (%s) TYPE %s GRANULARITY %d",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, sName), sExpr, sType, gran))

		case "DROP_SKIPPING_INDEX":
			sName := strings.TrimSpace(act.SkippingIndex.Name)
			if sName == "" {
				sName = strings.TrimSpace(act.OldName)
			}
			if sName == "" {
				return nil, errors.New("skipping index name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP INDEX IF EXISTS %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, sName)))

		case "MATERIALIZE_SKIPPING_INDEX":
			sName := strings.TrimSpace(act.SkippingIndex.Name)
			if sName == "" {
				sName = strings.TrimSpace(act.OldName)
			}
			if sName == "" {
				return nil, errors.New("skipping index name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MATERIALIZE INDEX %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, sName)))

		case "CLEAR_SKIPPING_INDEX":
			sName := strings.TrimSpace(act.SkippingIndex.Name)
			if sName == "" {
				sName = strings.TrimSpace(act.OldName)
			}
			if sName == "" {
				return nil, errors.New("skipping index name is required")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s CLEAR INDEX IF EXISTS %s",
				tbl, sqlbase.QuoteIdent(sqlbase.DialectClickHouse, sName)))

		case "MODIFY_TTL":
			if err := sqlbase.ValidateSQLExpression("table TTL expression", act.TTLExpr); err != nil {
				return nil, err
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY TTL %s", tbl, strings.TrimSpace(act.TTLExpr)))

		case "REMOVE_TTL":
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s REMOVE TTL", tbl))

		default:
			subStmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectClickHouse, driver.AlterTableRequest{
				Database: req.Database,
				Schema:   req.Schema,
				Table:    req.Table,
				Actions:  []driver.AlterAction{act},
			})
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, subStmts...)
		}
	}
	return stmts, nil
}

func (d *Driver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := buildClickHouseAlterTableSQLs(req)
	if err != nil {
		return nil, err
	}
	if d.isRestrictedWeb() {
		for _, s := range stmts {
			if err := sqlbase.ValidateClickHouseRestrictedWebSQL(s); err != nil {
				return stmts, err
			}
		}
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
	if d.isRestrictedWeb() {
		for _, s := range sqls {
			if err := sqlbase.ValidateClickHouseRestrictedWebSQL(s); err != nil {
				return err
			}
		}
	}
	for _, s := range sqls {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	if d.isRestrictedWeb() {
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(query); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(database) != "" {
		conn, release, cErr := d.acquireConnForDB(ctx, database)
		if cErr != nil {
			return nil, cErr
		}
		defer release()
		return sqlbase.ExecuteMultiSQLConn(ctx, conn, query, maxRows)
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
	if d.isRestrictedWeb() && (strings.TrimSpace(req.Format) == "" || strings.EqualFold(strings.TrimSpace(req.Format), "sql")) && req.Reader != nil {
		raw, err := io.ReadAll(req.Reader)
		if err != nil {
			return driver.ImportResult{}, err
		}
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(string(raw)); err != nil {
			return driver.ImportResult{}, err
		}
		req.Reader = bytes.NewReader(raw)
	}
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
func (d *Driver) ListPartitions(ctx context.Context, database, schema, table string) ([]driver.PartitionInfo, error) {
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

	list := []driver.PartitionInfo{}
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
	if err := rows.Err(); err != nil {
		return list, err
	}

	// Detached partitions from system.detached_parts so UI can ATTACH or DROP DETACHED them
	hasDetachedBytes := true
	dRows, dErr := d.db.QueryContext(ctx, `
		SELECT coalesce(partition_id, ''), count() AS parts_cnt, coalesce(sum(bytes_on_disk), 0) AS disk_bytes
		FROM system.detached_parts
		WHERE database = ? AND table = ? AND partition_id != ''
		GROUP BY partition_id
		ORDER BY partition_id DESC
	`, database, table)
	if dErr != nil {
		hasDetachedBytes = false
		dRows, dErr = d.db.QueryContext(ctx, `
			SELECT coalesce(partition_id, ''), count() AS parts_cnt
			FROM system.detached_parts
			WHERE database = ? AND table = ? AND partition_id != ''
			GROUP BY partition_id
			ORDER BY partition_id DESC
		`, database, table)
	}
	if dErr == nil {
		defer dRows.Close()
		for dRows.Next() {
			var pid string
			var pCnt, bCnt uint64
			var scanErr error
			if hasDetachedBytes {
				scanErr = dRows.Scan(&pid, &pCnt, &bCnt)
			} else {
				scanErr = dRows.Scan(&pid, &pCnt)
			}
			if scanErr == nil && pid != "" {
				list = append(list, driver.PartitionInfo{
					ID:          pid,
					Name:        pid,
					BytesOnDisk: int64(bCnt),
					PartsCount:  int64(pCnt),
					Active:      false,
					Detached:    true,
				})
			}
		}
	}

	return list, nil
}

func (d *Driver) ManagePartition(ctx context.Context, req driver.PartitionActionRequest) (string, error) {
	tbl := sqlbase.QualifyTable(sqlbase.DialectClickHouse, req.Database, req.Schema, req.Table)
	safeID := sqlbase.FormatLiteralForDialect(sqlbase.DialectClickHouse, req.PartitionID)
	var stmt string
	switch strings.ToUpper(req.Action) {
	case "DROP":
		stmt = fmt.Sprintf("ALTER TABLE %s DROP PARTITION ID %s", tbl, safeID)
	case "DETACH":
		stmt = fmt.Sprintf("ALTER TABLE %s DETACH PARTITION ID %s", tbl, safeID)
	case "ATTACH":
		stmt = fmt.Sprintf("ALTER TABLE %s ATTACH PARTITION ID %s", tbl, safeID)
	case "OPTIMIZE":
		stmt = fmt.Sprintf("OPTIMIZE TABLE %s PARTITION ID %s FINAL", tbl, safeID)
	default:
		return "", fmt.Errorf("unsupported partition action: %s", req.Action)
	}
	if req.PreviewOnly {
		return stmt, nil
	}
	_, err := d.db.ExecContext(ctx, stmt)
	return stmt, err
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
