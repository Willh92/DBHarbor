package mysql

import (
	"context"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	mysqldrv "github.com/go-sql-driver/mysql"
)

var (
	reValidMySQLCharset        = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	registerRestrictedDialOnce sync.Once
)

func ensureRestrictedMySQLDialer() {
	registerRestrictedDialOnce.Do(func() {
		restricted := sqlbase.NewRestrictedDialContext(8 * time.Second)
		mysqldrv.RegisterDialContext("tcp_restricted", func(ctx context.Context, addr string) (net.Conn, error) {
			return restricted(ctx, "tcp", addr)
		})
	})
}

func init() {
	variants := []struct {
		typ   string
		name  string
		color string
		port  int
		desc  string
	}{
		{"mysql", "MySQL", "#1677ff", 3306, "Popular open-source relational database (supports InnoDB schema design, foreign keys, indexes, and process management)"},
		{"mariadb", "MariaDB", "#2f54eb", 3306, "High-performance open-source relational database, fully compatible with the MySQL ecosystem"},
		{"tidb", "TiDB", "#eb2f96", 4000, "PingCAP open-source distributed NewSQL HTAP database (MySQL protocol compatible)"},
		{"oceanbase", "OceanBase", "#0958d9", 2881, "Cloud-native distributed relational database (MySQL-compatible tenant mode)"},
	}

	mysqlTypes := []string{"BIGINT", "INT", "TINYINT", "VARCHAR(255)", "TEXT", "LONGTEXT", "DECIMAL(18,2)", "DOUBLE", "DATETIME", "TIMESTAMP", "DATE", "JSON", "BLOB"}

	for _, v := range variants {
		variant := v
		fields := driver.StandardHostFields(variant.port, "Default Database (optional)", false)
		fields = append(fields, driver.ConnFieldSchema{
			Key: "tls", Label: "TLS Mode", Type: "select", Target: "options", DefaultValue: "false",
			SelectOptions: []driver.SelectOption{
				{Label: "Disabled (false)", Value: "false"},
				{Label: "Skip Certificate Verification (skip-verify)", Value: "skip-verify"},
				{Label: "Strict Verification (true)", Value: "true"},
			},
		})
		driver.Register(driver.DriverMeta{
			Type:               variant.typ,
			Name:               variant.name,
			Category:           driver.CategoryRelational,
			BadgeColor:         variant.color,
			DefaultPort:        variant.port,
			Description:        variant.desc,
			SupportsSchemas:    false,
			SupportsPartitions: true,
			SupportsExplain:    true,
			DefaultTypes:       mysqlTypes,
			Fields:             fields,
		}, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
			return New(cfg, variant.typ, variant.name, variant.port)
		})
	}
}

type Driver struct {
	cfg         driver.ConnectionConfig
	variantType string
	productName string
	db          *sql.DB
}

func New(cfg driver.ConnectionConfig, variantType, productName string, defaultPort int) (driver.Driver, error) {
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	user := cfg.Username
	if user == "" {
		user = "root"
	}
	restrictIP := cfg.Option("__restrict_internal_ip", "") == "1"
	host := strings.TrimSpace(cfg.Host)
	if host == "" && !restrictIP {
		host = "127.0.0.1"
	}
	mc := mysqldrv.NewConfig()
	mc.User = user
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	if restrictIP {
		ensureRestrictedMySQLDialer()
		mc.Net = "tcp_restricted"
	}
	mc.Addr = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
	mc.DBName = cfg.Database
	mc.ParseTime = false
	mc.Timeout = 8 * time.Second
	mc.MultiStatements = false
	mc.InterpolateParams = true
	mc.Params = map[string]string{"charset": "utf8mb4"}
	if tlsMode := cfg.Option("tls", "false"); tlsMode != "false" && tlsMode != "" {
		mc.TLSConfig = tlsMode
	}

	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)
	return &Driver{cfg: cfg, variantType: variantType, productName: productName, db: db}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta(d.variantType)
	return m
}

func (d *Driver) Close() error {
	return d.db.Close()
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	var version string
	if err := d.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return driver.ServerInfo{}, err
	}
	return driver.ServerInfo{
		Version:   version,
		Product:   d.productName,
		LatencyMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			s.SCHEMA_NAME,
			s.DEFAULT_CHARACTER_SET_NAME,
			COUNT(t.TABLE_NAME) AS tbl_cnt,
			COALESCE(SUM(COALESCE(t.DATA_LENGTH, 0) + COALESCE(t.INDEX_LENGTH, 0)), 0) AS total_bytes
		FROM information_schema.SCHEMATA s
		LEFT JOIN information_schema.TABLES t ON s.SCHEMA_NAME = t.TABLE_SCHEMA
		GROUP BY s.SCHEMA_NAME, s.DEFAULT_CHARACTER_SET_NAME
		ORDER BY s.SCHEMA_NAME
	`)
	if err != nil {
		// Fallback to SHOW DATABASES if restricted privileges
		r2, err2 := d.db.QueryContext(ctx, "SHOW DATABASES")
		if err2 != nil {
			return nil, err
		}
		defer r2.Close()
		var out []driver.DatabaseSummary
		for r2.Next() {
			var name string
			if r2.Scan(&name) == nil {
				out = append(out, driver.DatabaseSummary{Name: name})
			}
		}
		return out, nil
	}
	defer rows.Close()

	var out []driver.DatabaseSummary
	for rows.Next() {
		var name, charset string
		var cnt int
		var sz int64
		if err := rows.Scan(&name, &charset, &cnt, &sz); err != nil {
			return nil, err
		}
		out = append(out, driver.DatabaseSummary{
			Name:        name,
			Charset:     charset,
			TablesCount: cnt,
			SizeBytes:   sz,
		})
	}
	return out, rows.Err()
}

func (d *Driver) CreateDatabase(ctx context.Context, name, charset string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("database name cannot be empty")
	}
	charset = strings.TrimSpace(charset)
	if charset == "" {
		charset = "utf8mb4"
	}
	if !reValidMySQLCharset.MatchString(charset) {
		return fmt.Errorf("invalid database charset %q", charset)
	}
	_, err := d.db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s CHARACTER SET %s", sqlbase.QuoteIdent(sqlbase.DialectMySQL, name), charset))
	return err
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	_, err := d.db.ExecContext(ctx, "DROP DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectMySQL, name))
	return err
}

func (d *Driver) ListSchemas(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}

func (d *Driver) ListObjects(ctx context.Context, database, _ string) (driver.SchemaObjects, error) {
	res := driver.SchemaObjects{
		Database:  database,
		Tables:    []driver.ObjectItem{},
		Views:     []driver.ObjectItem{},
		Routines:  []driver.ObjectItem{},
		Sequences: []driver.ObjectItem{},
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			TABLE_NAME,
			TABLE_TYPE,
			COALESCE(ENGINE, ''),
			COALESCE(TABLE_ROWS, 0),
			COALESCE(DATA_LENGTH, 0),
			COALESCE(INDEX_LENGTH, 0),
			COALESCE(TABLE_COLLATION, ''),
			COALESCE(TABLE_COMMENT, '')
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME
	`, database)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var name, ttype, engine, coll, comment string
		var rCnt, dataLen, idxLen int64
		if err := rows.Scan(&name, &ttype, &engine, &rCnt, &dataLen, &idxLen, &coll, &comment); err != nil {
			return res, err
		}
		item := driver.ObjectItem{
			Name:       name,
			Engine:     engine,
			Rows:       rCnt,
			DataBytes:  dataLen,
			IndexBytes: idxLen,
			Collation:  coll,
			Comment:    comment,
		}
		ttUpper := strings.ToUpper(strings.TrimSpace(ttype))
		switch {
		case ttUpper == "SEQUENCE":
			item.Type = "SEQUENCE"
			res.Sequences = append(res.Sequences, item)
		case strings.Contains(ttUpper, "VIEW"):
			item.Type = "VIEW"
			res.Views = append(res.Views, item)
		default:
			item.Type = "TABLE"
			res.Tables = append(res.Tables, item)
		}
	}

	rRows, err := d.db.QueryContext(ctx, `
		SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME
	`, database)
	if err == nil {
		defer rRows.Close()
		for rRows.Next() {
			var rName, rType string
			if rRows.Scan(&rName, &rType) == nil {
				res.Routines = append(res.Routines, driver.ObjectItem{Name: rName, Type: rType})
			}
		}
	}
	return res, nil
}

func (d *Driver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	st := driver.TableStructure{
		Database:         database,
		Table:            table,
		Columns:          []driver.ColumnDef{},
		Indexes:          []driver.IndexDef{},
		ForeignKeys:      []driver.ForeignKeyDef{},
		CheckConstraints: []driver.CheckConstraintDef{},
	}

	_ = d.db.QueryRowContext(ctx, `
		SELECT COALESCE(ENGINE, ''), COALESCE(TABLE_COLLATION, ''), COALESCE(TABLE_COMMENT, '')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	`, database, table).Scan(&st.Engine, &st.Collation, &st.Comment)

	// DDL
	var tblName, createDDL string
	qTbl := sqlbase.QualifyTable(sqlbase.DialectMySQL, database, "", table)
	if err := d.db.QueryRowContext(ctx, "SHOW CREATE TABLE "+qTbl).Scan(&tblName, &createDDL); err == nil {
		st.DDL = createDDL
	}

	// Columns (including GENERATION_EXPRESSION when supported)
	hasGenCol := true
	cRows, err := d.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT, COALESCE(GENERATION_EXPRESSION, '')
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION
	`, database, table)
	if err != nil {
		hasGenCol = false
		cRows, err = d.db.QueryContext(ctx, `
			SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION
		`, database, table)
		if err != nil {
			return st, err
		}
	}
	defer cRows.Close()

	for cRows.Next() {
		var cName, cType, isNull, cKey, extra, comment, genExpr string
		var cDef sql.NullString
		var scanErr error
		if hasGenCol {
			scanErr = cRows.Scan(&cName, &cType, &isNull, &cDef, &cKey, &extra, &comment, &genExpr)
		} else {
			scanErr = cRows.Scan(&cName, &cType, &isNull, &cDef, &cKey, &extra, &comment)
		}
		if scanErr != nil {
			return st, scanErr
		}
		defVal := ""
		if cDef.Valid {
			defVal = cDef.String
		}
		genStorage := ""
		genExpr = strings.TrimSpace(genExpr)
		if genExpr != "" {
			extraUpper := strings.ToUpper(extra)
			if strings.Contains(extraUpper, "STORED") || strings.Contains(extraUpper, "PERSISTENT") {
				genStorage = "STORED"
			} else {
				genStorage = "VIRTUAL"
			}
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:             cName,
			Type:             cType,
			Nullable:         strings.EqualFold(isNull, "YES"),
			DefaultValue:     defVal,
			IsPrimaryKey:     strings.EqualFold(cKey, "PRI"),
			IsAutoIncrement:  strings.Contains(strings.ToLower(extra), "auto_increment"),
			Comment:          comment,
			Extra:            extra,
			GeneratedExpr:    genExpr,
			GeneratedStorage: genStorage,
		})
	}

	// Indexes
	iRows, err := d.db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, INDEX_TYPE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY INDEX_NAME, SEQ_IN_INDEX
	`, database, table)
	if err == nil {
		defer iRows.Close()
		idxMap := map[string]*driver.IndexDef{}
		var idxOrder []string
		for iRows.Next() {
			var idxName, colName, idxType string
			var nonUniq int
			if iRows.Scan(&idxName, &nonUniq, &colName, &idxType) == nil {
				item, exists := idxMap[idxName]
				if !exists {
					item = &driver.IndexDef{
						Name:      idxName,
						Unique:    nonUniq == 0,
						Primary:   idxName == "PRIMARY",
						IndexType: idxType,
					}
					idxMap[idxName] = item
					idxOrder = append(idxOrder, idxName)
				}
				item.Columns = append(item.Columns, colName)
			}
		}
		for _, k := range idxOrder {
			st.Indexes = append(st.Indexes, *idxMap[k])
		}
	}

	// Foreign keys (joined with REFERENTIAL_CONSTRAINTS for OnUpdate / OnDelete)
	fkRows, err := d.db.QueryContext(ctx, `
		SELECT
			kcu.CONSTRAINT_NAME,
			kcu.COLUMN_NAME,
			kcu.REFERENCED_TABLE_NAME,
			kcu.REFERENCED_COLUMN_NAME,
			COALESCE(rc.UPDATE_RULE, ''),
			COALESCE(rc.DELETE_RULE, '')
		FROM information_schema.KEY_COLUMN_USAGE kcu
		LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
			ON kcu.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
			AND kcu.TABLE_NAME = rc.TABLE_NAME
			AND kcu.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
		WHERE kcu.TABLE_SCHEMA = ? AND kcu.TABLE_NAME = ? AND kcu.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY kcu.CONSTRAINT_NAME, kcu.ORDINAL_POSITION
	`, database, table)
	if err == nil {
		defer fkRows.Close()
		fkMap := map[string]*driver.ForeignKeyDef{}
		var fkOrder []string
		for fkRows.Next() {
			var name, col, refTbl, refCol, onUpd, onDel string
			if fkRows.Scan(&name, &col, &refTbl, &refCol, &onUpd, &onDel) == nil {
				item, exists := fkMap[name]
				if !exists {
					item = &driver.ForeignKeyDef{
						Name:     name,
						RefTable: refTbl,
						OnUpdate: onUpd,
						OnDelete: onDel,
					}
					fkMap[name] = item
					fkOrder = append(fkOrder, name)
				}
				item.Columns = append(item.Columns, col)
				item.RefColumns = append(item.RefColumns, refCol)
			}
		}
		for _, k := range fkOrder {
			st.ForeignKeys = append(st.ForeignKeys, *fkMap[k])
		}
	}

	// CHECK constraints (MySQL 8.0.16+ / MariaDB 10.2+ / TiDB; ignored gracefully on MySQL 5.7)
	chkRows, chkErr := d.db.QueryContext(ctx, `
		SELECT tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE, COALESCE(tc.ENFORCED, 'YES')
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
			ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
		ORDER BY tc.CONSTRAINT_NAME
	`, database, table)
	if chkErr != nil {
		// MariaDB omits ENFORCED from TABLE_CONSTRAINTS
		chkRows, chkErr = d.db.QueryContext(ctx, `
			SELECT tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE, 'YES'
			FROM information_schema.TABLE_CONSTRAINTS tc
			JOIN information_schema.CHECK_CONSTRAINTS cc
				ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
			WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
			ORDER BY tc.CONSTRAINT_NAME
		`, database, table)
	}
	if chkErr == nil {
		defer chkRows.Close()
		for chkRows.Next() {
			var cName, cExpr, enforced string
			if chkRows.Scan(&cName, &cExpr, &enforced) == nil {
				st.CheckConstraints = append(st.CheckConstraints, driver.CheckConstraintDef{
					Name:       cName,
					Expression: cExpr,
					Enforced:   !strings.EqualFold(strings.TrimSpace(enforced), "NO"),
				})
			}
		}
	}

	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectMySQL, req)
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
	return sqlbase.BuildMutationSQLs(sqlbase.DialectMySQL, req)
}

func (d *Driver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return sqlbase.ApplyMutations(ctx, d.db, sqlbase.DialectMySQL, req)
}

func (d *Driver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectMySQL, req)
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
	stmts, err := sqlbase.BuildAlterTableSQLsForEngine(d.variantType, sqlbase.DialectMySQL, req)
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

func (d *Driver) acquireConnForDB(ctx context.Context, targetDB string) (*sql.Conn, func(), error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	targetDB = strings.TrimSpace(targetDB)
	defaultDB := strings.TrimSpace(d.cfg.Database)
	switched := false
	if targetDB != "" {
		if _, uErr := conn.ExecContext(ctx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectMySQL, targetDB)); uErr != nil {
			_ = conn.Close()
			return nil, nil, uErr
		}
		if !strings.EqualFold(targetDB, defaultDB) {
			switched = true
		}
	}
	cleanup := func() {
		if switched {
			if defaultDB == "" {
				_ = conn.Raw(func(_ any) error { return stdsql_driver.ErrBadConn })
			} else {
				rCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, rErr := conn.ExecContext(rCtx, "USE "+sqlbase.QuoteIdent(sqlbase.DialectMySQL, defaultDB))
				cancel()
				if rErr != nil {
					_ = conn.Raw(func(_ any) error { return stdsql_driver.ErrBadConn })
				}
			}
		}
		_ = conn.Close()
	}
	return conn, cleanup, nil
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectMySQL, req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.Database) != "" {
		conn, cleanup, cErr := d.acquireConnForDB(ctx, req.Database)
		if cErr != nil {
			return cErr
		}
		defer cleanup()
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

// PartitionManager implementation for MySQL / MariaDB / TiDB / OceanBase
func (d *Driver) ListPartitions(ctx context.Context, database, _, table string) ([]driver.PartitionInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT
			PARTITION_NAME,
			COALESCE(PARTITION_METHOD, ''),
			COALESCE(PARTITION_EXPRESSION, ''),
			COALESCE(CAST(PARTITION_DESCRIPTION AS CHAR), ''),
			COALESCE(TABLE_ROWS, 0),
			COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0)
		FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL
		ORDER BY PARTITION_ORDINAL_POSITION
	`, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []driver.PartitionInfo{}
	for rows.Next() {
		var pName, method, expr, desc string
		var rCnt, diskBytes int64
		if err := rows.Scan(&pName, &method, &expr, &desc, &rCnt, &diskBytes); err != nil {
			return nil, err
		}
		list = append(list, driver.PartitionInfo{
			ID:               pName,
			Name:             pName,
			Rows:             rCnt,
			BytesOnDisk:      diskBytes,
			PartsCount:       1,
			Active:           true,
			Strategy:         method,
			Expression:       expr,
			BoundDescription: desc,
		})
	}
	return list, rows.Err()
}

func (d *Driver) ManagePartition(ctx context.Context, req driver.PartitionActionRequest) (string, error) {
	if strings.TrimSpace(req.Table) == "" || strings.TrimSpace(req.PartitionID) == "" {
		return "", errors.New("table and partition name are required")
	}
	tbl := sqlbase.QualifyTable(sqlbase.DialectMySQL, req.Database, "", req.Table)
	qPart := sqlbase.QuoteIdent(sqlbase.DialectMySQL, req.PartitionID)

	var stmt string
	switch strings.ToUpper(strings.TrimSpace(req.Action)) {
	case "TRUNCATE":
		stmt = fmt.Sprintf("ALTER TABLE %s TRUNCATE PARTITION %s", tbl, qPart)
	case "DROP":
		stmt = fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", tbl, qPart)
	case "ANALYZE":
		stmt = fmt.Sprintf("ALTER TABLE %s ANALYZE PARTITION %s", tbl, qPart)
	case "OPTIMIZE":
		stmt = fmt.Sprintf("ALTER TABLE %s OPTIMIZE PARTITION %s", tbl, qPart)
	case "ADD":
		expr := strings.TrimSpace(req.Expression)
		if err := sqlbase.ValidateSQLExpression("partition bound expression", expr); err != nil {
			return "", err
		}
		upperExpr := strings.ToUpper(expr)
		if strings.HasPrefix(upperExpr, "VALUES ") {
			stmt = fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s %s)", tbl, qPart, expr)
		} else if upperExpr == "MAXVALUE" {
			stmt = fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN MAXVALUE)", tbl, qPart)
		} else {
			stmt = fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN (%s))", tbl, qPart, expr)
		}
	default:
		return "", fmt.Errorf("unsupported MySQL partition action: %s", req.Action)
	}
	if req.PreviewOnly {
		return stmt, nil
	}
	_, err := d.db.ExecContext(ctx, stmt)
	return stmt, err
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	conn, cleanup, err := d.acquireConnForDB(ctx, database)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return sqlbase.ExecuteMultiSQLConn(ctx, conn, query, maxRows)
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
		return sqlbase.StreamMultiTableExport(ctx, d.db, sqlbase.DialectMySQL, req, items, w)
	}
	ddl := ""
	if req.IncludeDDL && req.Table != "" {
		if st, err := d.GetTableStructure(ctx, req.Database, "", req.Table); err == nil {
			ddl = st.DDL
		}
	}
	return sqlbase.StreamExport(ctx, d.db, sqlbase.DialectMySQL, req, ddl, w)
}

func (d *Driver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	if strings.EqualFold(req.Format, "sql") && strings.TrimSpace(req.Database) != "" {
		conn, cleanup, err := d.acquireConnForDB(ctx, req.Database)
		if err != nil {
			return driver.ImportResult{}, err
		}
		defer cleanup()
		start := time.Now()
		res := driver.ImportResult{}
		sErr := sqlbase.StreamStatements(req.Reader, func(stmt string) error {
			if _, execErr := conn.ExecContext(ctx, stmt); execErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, execErr.Error())
					}
					return nil
				}
				return execErr
			}
			res.ImportedRows++
			return nil
		})
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, sErr
	}
	return sqlbase.BatchImport(ctx, d.db, sqlbase.DialectMySQL, req)
}

func (d *Driver) GetAutocompleteMeta(ctx context.Context, database string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Tables: map[string][]string{}}
	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			meta.Databases = append(meta.Databases, item.Name)
		}
	} else if sqlbase.IsAuthError(d.variantType, err) {
		return meta, err
	}
	if database == "" {
		return meta, nil
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION
	`, database)
	if err != nil {
		if sqlbase.IsAuthError(d.variantType, err) {
			return meta, err
		}
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
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	if err := d.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&snap.Version); err != nil && sqlbase.IsAuthError(d.variantType, err) {
		return snap, err
	}

	statusKeys := map[string]string{
		"Threads_connected": "Connected Threads",
		"Threads_running":   "Running Threads",
		"Questions":         "Total Queries",
		"Slow_queries":      "Slow Queries",
		"Uptime":            "Uptime Seconds",
	}
	sRows, err := d.db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected','Threads_running','Questions','Slow_queries','Uptime')")
	if err != nil {
		if sqlbase.IsAuthError(d.variantType, err) {
			return snap, err
		}
	} else {
		defer sRows.Close()
		for sRows.Next() {
			var k, v string
			if sRows.Scan(&k, &v) == nil {
				if k == "Uptime" {
					snap.UptimeSec, _ = strconv.ParseInt(v, 10, 64)
				} else {
					snap.Metrics = append(snap.Metrics, driver.MonitorMetric{Key: k, Label: statusKeys[k], Value: v})
				}
			}
		}
	}

	tRows, err := d.db.QueryContext(ctx, `
		SELECT CONCAT(TABLE_SCHEMA, '.', TABLE_NAME), COALESCE(ENGINE, ''), COALESCE(TABLE_ROWS, 0), COALESCE(DATA_LENGTH + INDEX_LENGTH, 0)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA NOT IN ('information_schema', 'performance_schema', 'mysql', 'sys')
		ORDER BY (COALESCE(DATA_LENGTH,0) + COALESCE(INDEX_LENGTH,0)) DESC LIMIT 15
	`)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var name, eng string
			var rCnt, bCnt int64
			if tRows.Scan(&name, &eng, &rCnt, &bCnt) == nil {
				snap.Storage = append(snap.Storage, driver.StorageItem{Name: name, Category: eng, Rows: rCnt, SizeBytes: bCnt})
			}
		}
	}

	pRows, err := d.db.QueryContext(ctx, `
		SELECT ID, USER, HOST, COALESCE(DB, ''), COMMAND, COALESCE(STATE, ''), TIME, COALESCE(INFO, '')
		FROM information_schema.PROCESSLIST ORDER BY TIME DESC LIMIT 100
	`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var id int64
			var usr, host, dbName, cmd, state, info string
			var dur int64
			if pRows.Scan(&id, &usr, &host, &dbName, &cmd, &state, &dur, &info) == nil {
				snap.Sessions = append(snap.Sessions, driver.SessionItem{
					ID: strconv.FormatInt(id, 10), User: usr, Host: host, Database: dbName,
					Command: cmd, State: state, DurationSec: dur, Query: info,
				})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	idNum, err := strconv.ParseInt(sessionID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid session ID: %s", sessionID)
	}
	_, err = d.db.ExecContext(ctx, fmt.Sprintf("KILL %d", idNum))
	return err
}
