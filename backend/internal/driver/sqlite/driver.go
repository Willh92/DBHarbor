package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	_ "modernc.org/sqlite"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:            "sqlite",
		Name:            "SQLite 3",
		Category:        driver.CategoryRelational,
		BadgeColor:      "#13c2c2",
		DefaultPort:     0,
		Description:     "Cross-platform zero-configuration embedded relational database (opens or creates local .db / .sqlite files)",
		SupportsSchemas: false,
		SupportsExplain: true,
		DefaultTypes:    []string{"INTEGER", "TEXT", "REAL", "NUMERIC", "BLOB", "BOOLEAN", "DATETIME"},
		Fields: []driver.ConnFieldSchema{
			{Key: "filePath", Label: "Database File Path", Type: "text", Target: "root", Placeholder: "./data/app.sqlite or :memory:", Required: true},
		},
	}, NewSQLite)
}

type SQLiteDriver struct {
	cfg      driver.ConnectionConfig
	filePath string
	webMode  bool
	db       *sql.DB
}

func NewSQLite(cfg driver.ConnectionConfig) (driver.Driver, error) {
	fp := strings.TrimSpace(cfg.FilePath)
	if fp == "" {
		return nil, errors.New("SQLite database file path cannot be empty")
	}
	webMode := cfg.Option("__web_mode", "") == "1"
	if fp != ":memory:" && strings.HasPrefix(strings.ToLower(fp), "file:") {
		return nil, errors.New("SQLite URI file paths are not supported; please specify a standard file path or :memory:")
	}
	dsn := fp
	if fp != ":memory:" {
		cleanPath := filepath.Clean(fp)
		if dir := filepath.Dir(cleanPath); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		fp = cleanPath
		dsn = filepath.ToSlash(cleanPath) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-4000)"
		if webMode {
			dsn += "&_pragma=trusted_schema(0)"
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if fp == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
		db.SetConnMaxIdleTime(3 * time.Minute)
	}
	return &SQLiteDriver{cfg: cfg, filePath: fp, webMode: webMode, db: db}, nil
}

func validateSQLiteWebSandbox(script string) error {
	for _, stmt := range sqlbase.SplitStatements(script) {
		trimmed := strings.TrimSpace(sqlbase.StripLeadingSQLComments(stmt))
		if trimmed == "" {
			continue
		}
		upper := strings.ToUpper(trimmed)
		firstWord := upper
		for i, r := range upper {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '(' {
				firstWord = upper[:i]
				break
			}
		}
		if firstWord == "ATTACH" || firstWord == "DETACH" {
			return errors.New("ATTACH / DETACH DATABASE is disabled in Web Server mode for host sandbox isolation")
		}
		if strings.Contains(upper, "LOAD_EXTENSION") {
			return errors.New("load_extension is disabled in Web Server mode")
		}
	}
	return nil
}

func (d *SQLiteDriver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("sqlite")
	return m
}

func (d *SQLiteDriver) Close() error {
	return d.db.Close()
}

func (d *SQLiteDriver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var ver string
	if err := d.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&ver); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{
		Version:   ver,
		Product:   "SQLite",
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *SQLiteDriver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	var sz int64
	if d.filePath != ":memory:" {
		if st, err := os.Stat(d.filePath); err == nil {
			sz = st.Size()
		}
	}
	var cnt int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&cnt)
	return []driver.DatabaseSummary{
		{Name: "main", SizeBytes: sz, TablesCount: cnt, Charset: "UTF-8"},
	}, nil
}

func (d *SQLiteDriver) CreateDatabase(_ context.Context, _, _ string) error {
	return errors.New("SQLite uses single-file databases and does not require CREATE DATABASE; create a new connection with a new file path instead")
}

func (d *SQLiteDriver) ListSchemas(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}

func (d *SQLiteDriver) ListObjects(ctx context.Context, database, _ string) (driver.SchemaObjects, error) {
	if database == "" {
		database = "main"
	}
	res := driver.SchemaObjects{
		Database: database,
		Tables:   []driver.ObjectItem{},
		Views:    []driver.ObjectItem{},
		Routines: []driver.ObjectItem{},
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return res, err
	}
	var names [][2]string
	for rows.Next() {
		var n, t string
		if rows.Scan(&n, &t) == nil {
			names = append(names, [2]string{n, t})
		}
	}
	_ = rows.Close()

	// Skip per-table SELECT COUNT(*) full B-Tree scans unless the SQLite database is small (<= 4 MB & <= 20 tables),
	// keeping sidebar metadata loading instantaneous on large database files.
	countRows := len(names) <= 20
	if countRows && d.filePath != ":memory:" {
		if st, statErr := os.Stat(d.filePath); statErr == nil && st.Size() > 4*1024*1024 {
			countRows = false
		}
	}

	for _, pair := range names {
		name, otype := pair[0], pair[1]
		var rCnt int64
		if otype == "table" {
			if countRows {
				_ = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+sqlbase.QuoteIdent(sqlbase.DialectSQLite, name)).Scan(&rCnt)
			}
			res.Tables = append(res.Tables, driver.ObjectItem{Name: name, Type: "TABLE", Engine: "SQLite B-Tree", Rows: rCnt})
		} else {
			res.Views = append(res.Views, driver.ObjectItem{Name: name, Type: "VIEW"})
		}
	}
	return res, nil
}

func (d *SQLiteDriver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	st := driver.TableStructure{
		Database:    database,
		Table:       table,
		Engine:      "SQLite B-Tree",
		Columns:     []driver.ColumnDef{},
		Indexes:     []driver.IndexDef{},
		ForeignKeys: []driver.ForeignKeyDef{},
	}

	_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, table).Scan(&st.DDL)
	upperDDL := strings.ToUpper(st.DDL)

	qTbl := sqlbase.QuoteIdent(sqlbase.DialectSQLite, table)
	cRows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", qTbl))
	if err != nil {
		return st, err
	}
	defer cRows.Close()

	for cRows.Next() {
		var cid, notNull, pk int
		var name, cType string
		var dflt sql.NullString
		if err := cRows.Scan(&cid, &name, &cType, &notNull, &dflt, &pk); err != nil {
			return st, err
		}
		defVal := ""
		if dflt.Valid {
			defVal = dflt.String
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:            name,
			Type:            cType,
			Nullable:        notNull == 0 && pk == 0,
			DefaultValue:    defVal,
			IsPrimaryKey:    pk > 0,
			IsAutoIncrement: pk > 0 && strings.Contains(upperDDL, "AUTOINCREMENT"),
		})
	}

	// Indexes
	iRows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%s)", qTbl))
	if err == nil {
		type rawIdx struct {
			name   string
			unique bool
			origin string
		}
		var rawList []rawIdx
		for iRows.Next() {
			var seq, uniq, partial int
			var name, origin string
			if iRows.Scan(&seq, &name, &uniq, &origin, &partial) == nil {
				rawList = append(rawList, rawIdx{name: name, unique: uniq == 1, origin: origin})
			}
		}
		_ = iRows.Close()

		for _, ri := range rawList {
			var cols []string
			infoRows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%s)", sqlbase.QuoteIdent(sqlbase.DialectSQLite, ri.name)))
			if err == nil {
				for infoRows.Next() {
					var seqno, cid int
					var colName sql.NullString
					if infoRows.Scan(&seqno, &cid, &colName) == nil && colName.Valid {
						cols = append(cols, colName.String)
					}
				}
				_ = infoRows.Close()
			}
			st.Indexes = append(st.Indexes, driver.IndexDef{
				Name:      ri.name,
				Columns:   cols,
				Unique:    ri.unique,
				Primary:   ri.origin == "pk",
				IndexType: "BTREE",
			})
		}
	}

	// Foreign Keys (grouped by id for composite foreign keys)
	fkRows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", qTbl))
	if err == nil {
		defer fkRows.Close()
		fkMap := map[int]*driver.ForeignKeyDef{}
		var fkOrder []int
		for fkRows.Next() {
			var id, seq int
			var refTbl, fromCol, toCol, onUpd, onDel, match string
			if fkRows.Scan(&id, &seq, &refTbl, &fromCol, &toCol, &onUpd, &onDel, &match) == nil {
				item, exists := fkMap[id]
				if !exists {
					item = &driver.ForeignKeyDef{
						Name:     fmt.Sprintf("fk_%s_%d", table, id),
						RefTable: refTbl,
						OnUpdate: onUpd,
						OnDelete: onDel,
					}
					fkMap[id] = item
					fkOrder = append(fkOrder, id)
				}
				item.Columns = append(item.Columns, fromCol)
				item.RefColumns = append(item.RefColumns, toCol)
			}
		}
		for _, id := range fkOrder {
			st.ForeignKeys = append(st.ForeignKeys, *fkMap[id])
		}
	}
	return st, nil
}

func (d *SQLiteDriver) getPrimaryKeysFast(ctx context.Context, table string) []string {
	qTbl := sqlbase.QuoteIdent(sqlbase.DialectSQLite, table)
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", qTbl))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var pks []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, cType string
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &cType, &notNull, &dflt, &pk) == nil && pk > 0 {
			pks = append(pks, name)
		}
	}
	return pks
}

func (d *SQLiteDriver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectSQLite, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	res, err := sqlbase.ExecuteSingleQuery(ctx, d.db, dataSQL, req.PageSize)
	if err != nil {
		// Fallback for WITHOUT ROWID tables or views
		qTbl := sqlbase.QualifyTable(sqlbase.DialectSQLite, "", "", req.Table)
		page := req.Page
		if page < 1 {
			page = 1
		}
		ps := req.PageSize
		if ps <= 0 {
			ps = 100
		}
		wherePart := ""
		if f := strings.TrimSpace(req.Filter); f != "" {
			wherePart = " WHERE " + f
		}
		orderParts := make([]string, 0, len(req.Sorts))
		for _, s := range req.Sorts {
			if strings.TrimSpace(s.Column) != "" {
				dir := "ASC"
				if strings.EqualFold(s.Direction, "DESC") {
					dir = "DESC"
				}
				orderParts = append(orderParts, sqlbase.QuoteIdent(sqlbase.DialectSQLite, s.Column)+" "+dir)
			}
		}
		orderClause := ""
		if len(orderParts) > 0 {
			orderClause = " ORDER BY " + strings.Join(orderParts, ", ")
		}
		fallbackSQL := fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", qTbl, wherePart, orderClause, ps, (page-1)*ps)
		res, err = sqlbase.ExecuteSingleQuery(ctx, d.db, fallbackSQL, ps)
		if err != nil {
			return res, err
		}
	}
	var total int64
	if d.db.QueryRowContext(ctx, countSQL).Scan(&total) == nil {
		res.Total = total
		res.HasTotal = true
	}
	res.PrimaryKeys = d.getPrimaryKeysFast(ctx, req.Table)
	if len(res.PrimaryKeys) == 0 {
		for _, c := range res.Columns {
			if c.Name == "__dbharbor_rowid__" {
				res.PrimaryKeys = []string{"__dbharbor_rowid__"}
				break
			}
		}
	}
	return res, nil
}

func (d *SQLiteDriver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	return sqlbase.BuildMutationSQLs(sqlbase.DialectSQLite, req)
}

func (d *SQLiteDriver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectSQLite, req)
}

func (d *SQLiteDriver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectSQLite, req)
	if err != nil {
		return "", err
	}
	var idxStmts []string
	if len(req.Indexes) > 0 {
		alterActions := make([]driver.AlterAction, 0, len(req.Indexes))
		for _, idx := range req.Indexes {
			if len(idx.Columns) > 0 {
				alterActions = append(alterActions, driver.AlterAction{Action: "ADD_INDEX", Index: idx})
			}
		}
		idxStmts, _ = sqlbase.BuildAlterTableSQLs(sqlbase.DialectSQLite, driver.AlterTableRequest{
			Table:   req.Table,
			Actions: alterActions,
		})
	}

	fullScript := ddl
	if len(idxStmts) > 0 {
		fullScript = ddl + ";\n" + strings.Join(idxStmts, ";\n")
	}
	if req.PreviewOnly {
		return fullScript, nil
	}
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fullScript, err
	}
	for _, s := range idxStmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fullScript, err
		}
	}
	return fullScript, nil
}

func (d *SQLiteDriver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	if d.needsTableRebuild(ctx, req) {
		return d.alterTableWithRebuild(ctx, req)
	}
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectSQLite, req)
	if err != nil {
		return nil, err
	}
	if req.PreviewOnly {
		return stmts, nil
	}
	for _, s := range stmts {
		if _, execErr := d.db.ExecContext(ctx, s); execErr != nil {
			// Fallback to safe transactional table rebuild when SQLite rejects native ALTER
			return d.alterTableWithRebuild(ctx, req)
		}
	}
	return stmts, nil
}

func (d *SQLiteDriver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectSQLite, req)
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

func (d *SQLiteDriver) ExecuteSQL(ctx context.Context, _, query string, maxRows int) ([]driver.QueryResult, error) {
	if d.webMode {
		if err := validateSQLiteWebSandbox(query); err != nil {
			return nil, err
		}
	}
	return sqlbase.ExecuteMultiSQL(ctx, d.db, query, maxRows)
}

func (d *SQLiteDriver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if strings.TrimSpace(req.CustomSQL) == "" && (strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0) {
		targetNames := req.Tables
		if len(targetNames) == 0 {
			rows, err := d.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					targetNames = append(targetNames, n)
				}
			}
			_ = rows.Close()
		}
		items := make([]sqlbase.TableExportItem, 0, len(targetNames))
		for _, tName := range targetNames {
			var ddl string
			_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND name = ?`, tName).Scan(&ddl)
			var idxDDLs []string
			if req.IncludeDDL {
				iRows, iErr := d.db.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL`, tName)
				if iErr == nil {
					for iRows.Next() {
						var idxSQL string
						if iRows.Scan(&idxSQL) == nil && strings.TrimSpace(idxSQL) != "" {
							idxDDLs = append(idxDDLs, idxSQL)
						}
					}
					_ = iRows.Close()
				}
			}
			items = append(items, sqlbase.TableExportItem{Table: tName, DDL: ddl, IndexDDLs: idxDDLs})
		}
		if req.Database == "" {
			req.Database = "main"
		}
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectSQLite, req, items, w)
	}

	if d.webMode && strings.TrimSpace(req.CustomSQL) != "" {
		if err := validateSQLiteWebSandbox(req.CustomSQL); err != nil {
			return err
		}
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, req.Table).Scan(&ddl)
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectSQLite, req, ddl, w)
}

func (d *SQLiteDriver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	if d.webMode && (req.Format == "" || strings.EqualFold(req.Format, "sql")) {
		raw, err := io.ReadAll(io.LimitReader(req.Reader, 64<<20))
		if err != nil {
			return driver.ImportResult{}, err
		}
		if err := validateSQLiteWebSandbox(string(raw)); err != nil {
			return driver.ImportResult{}, err
		}
		req.Reader = strings.NewReader(string(raw))
	}
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectSQLite, req)
}

func (d *SQLiteDriver) GetAutocompleteMeta(ctx context.Context, _ string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Databases: []string{"main"}, Tables: map[string][]string{}}
	rows, err := d.db.QueryContext(ctx, `
		SELECT m.name, p.name
		FROM sqlite_master m
		JOIN pragma_table_info(m.name) p
		WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, p.cid
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var tName, cName string
			if rows.Scan(&tName, &cName) == nil {
				meta.Tables[tName] = append(meta.Tables[tName], cName)
			}
		}
		return meta, nil
	}

	// Fallback if pragma table-valued function is disabled
	tRows, tErr := d.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if tErr != nil {
		return meta, nil
	}
	var names []string
	for tRows.Next() {
		var n string
		if tRows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	_ = tRows.Close()
	for _, n := range names {
		cRows, cErr := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", sqlbase.QuoteIdent(sqlbase.DialectSQLite, n)))
		if cErr == nil {
			for cRows.Next() {
				var cid, notNull, pk int
				var cName, cType string
				var dflt sql.NullString
				if cRows.Scan(&cid, &cName, &cType, &notNull, &dflt, &pk) == nil {
					meta.Tables[n] = append(meta.Tables[n], cName)
				}
			}
			_ = cRows.Close()
		}
	}
	return meta, nil
}

func (d *SQLiteDriver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: false,
	}
	_ = d.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&snap.Version)

	var pageCount, pageSize, freeList int64
	_ = d.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
	_ = d.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
	_ = d.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freeList)

	snap.Metrics = append(snap.Metrics,
		driver.MonitorMetric{Key: "file_size", Label: "Database Size", Value: fmt.Sprintf("%.2f", float64(pageCount*pageSize)/1024), Unit: "KB"},
		driver.MonitorMetric{Key: "page_count", Label: "Total Pages", Value: fmt.Sprintf("%d", pageCount)},
		driver.MonitorMetric{Key: "freelist", Label: "Free Pages", Value: fmt.Sprintf("%d", freeList)},
	)

	if objs, err := d.ListObjects(ctx, "main", ""); err == nil {
		for _, t := range objs.Tables {
			snap.Storage = append(snap.Storage, driver.StorageItem{
				Name: t.Name, Category: "TABLE", Rows: t.Rows, SizeBytes: t.Rows * 128,
			})
		}
	}
	return snap, nil
}

func (d *SQLiteDriver) KillSession(_ context.Context, _ string) error {
	return errors.New("embedded SQLite does not have standalone network connection sessions")
}
