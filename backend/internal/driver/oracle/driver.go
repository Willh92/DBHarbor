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
		Type:               "oracle",
		Name:               "Oracle",
		Category:           driver.CategoryRelational,
		BadgeColor:         "#f5222d",
		DefaultPort:        1521,
		Description:        "Pure Go zero-dependency Oracle driver (no Instant Client required, supports multi-schema and session monitoring)",
		SupportsSchemas:    true,
		SupportsPartitions: true,
		SupportsExplain:    false,
		DefaultTypes:       []string{"NUMBER(19)", "NUMBER(10)", "NUMBER(18,2)", "VARCHAR2(255)", "NVARCHAR2(255)", "CLOB", "DATE", "TIMESTAMP", "BLOB"},
		Fields:             fields,
	}, New)
}

var _ driver.PartitionManager = (*Driver)(nil)

type Driver struct {
	cfg driver.ConnectionConfig
	db  *sql.DB
}

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	restrictIP := cfg.Option("__restrict_internal_ip", "") == "1"
	rawHost := strings.TrimSpace(cfg.Host)
	if rawHost == "" && !restrictIP {
		rawHost = "127.0.0.1"
	}
	connectHost, err := sqlbase.ValidateStrictHostname(rawHost)
	if err != nil {
		return nil, err
	}
	if restrictIP {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		pinnedIP, pinErr := sqlbase.ResolveAndPinSinglePublicIP(ctx, connectHost)
		if pinErr != nil {
			return nil, pinErr
		}
		connectHost = pinnedIP
	}
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
	connStr := go_ora.BuildUrl(strings.Trim(connectHost, "[]"), port, svc, cfg.Username, cfg.Password, urlOpts)
	db, err := sql.Open("oracle", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
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

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	var dummy int
	if err := d.db.QueryRowContext(ctx, "SELECT 1 FROM DUAL").Scan(&dummy); err != nil {
		return nil, err
	}
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
		if sqlbase.IsAuthError("oracle", err) {
			return nil, err
		}
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
	if err != nil && sqlbase.IsAuthError("oracle", err) {
		return driver.SchemaObjects{}, err
	}
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

func formatOracleColumnType(dataType string, charLen, prec, scale sql.NullInt64) string {
	raw := strings.TrimSpace(dataType)
	if strings.Contains(raw, "(") {
		return raw
	}
	dt := strings.ToUpper(raw)
	switch dt {
	case "VARCHAR2", "NVARCHAR2", "VARCHAR", "NVARCHAR", "CHAR", "NCHAR", "RAW":
		if charLen.Valid && charLen.Int64 > 0 {
			return fmt.Sprintf("%s(%d)", dt, charLen.Int64)
		}
	case "NUMBER", "DECIMAL", "NUMERIC", "DEC":
		if prec.Valid && prec.Int64 > 0 {
			if scale.Valid && scale.Int64 != 0 {
				return fmt.Sprintf("%s(%d,%d)", dt, prec.Int64, scale.Int64)
			}
			return fmt.Sprintf("%s(%d)", dt, prec.Int64)
		}
	case "FLOAT":
		if prec.Valid && prec.Int64 > 0 && prec.Int64 != 126 {
			return fmt.Sprintf("FLOAT(%d)", prec.Int64)
		}
	}
	return raw
}

func (d *Driver) getPrimaryKeys(ctx context.Context, schema, table string) ([]string, string) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT c.CONSTRAINT_NAME, cc.COLUMN_NAME
		FROM ALL_CONSTRAINTS c
		JOIN ALL_CONS_COLUMNS cc ON c.OWNER = cc.OWNER AND c.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 AND c.CONSTRAINT_TYPE = 'P'
		ORDER BY cc.POSITION
	`, schema, table)
	if err != nil {
		return nil, ""
	}
	defer rows.Close()
	var pks []string
	var pkConstraint string
	for rows.Next() {
		var cname, col string
		if rows.Scan(&cname, &col) == nil {
			pkConstraint = cname
			pks = append(pks, col)
		}
	}
	return pks, pkConstraint
}

func (d *Driver) GetTableStructure(ctx context.Context, database, schema, table string) (driver.TableStructure, error) {
	if schema == "" {
		schema = strings.ToUpper(d.cfg.Username)
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

	_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(COMMENTS, '') FROM ALL_TAB_COMMENTS WHERE OWNER = :1 AND TABLE_NAME = :2`, schema, table).Scan(&st.Comment)

	pks, pkConstraint := d.getPrimaryKeys(ctx, schema, table)
	pkSet := make(map[string]bool, len(pks))
	for _, pk := range pks {
		pkSet[pk] = true
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT c.COLUMN_NAME, c.DATA_TYPE, c.CHAR_LENGTH, c.DATA_PRECISION, c.DATA_SCALE,
		       c.NULLABLE, c.DATA_DEFAULT, COALESCE(cc.COMMENTS, '')
		FROM ALL_TAB_COLUMNS c
		LEFT JOIN ALL_COL_COMMENTS cc
		  ON c.OWNER = cc.OWNER AND c.TABLE_NAME = cc.TABLE_NAME AND c.COLUMN_NAME = cc.COLUMN_NAME
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2
		ORDER BY c.COLUMN_ID
	`, schema, table)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, t, nul, cmt string
		var charLen, prec, scale sql.NullInt64
		var dflt sql.NullString
		if rows.Scan(&n, &t, &charLen, &prec, &scale, &nul, &dflt, &cmt) == nil {
			defVal := ""
			if dflt.Valid {
				defVal = strings.TrimSpace(dflt.String)
			}
			st.Columns = append(st.Columns, driver.ColumnDef{
				Name:         n,
				Type:         formatOracleColumnType(t, charLen, prec, scale),
				Nullable:     nul == "Y",
				DefaultValue: defVal,
				IsPrimaryKey: pkSet[n],
				Comment:      strings.TrimSpace(cmt),
			})
		}
	}

	// Indexes
	idxRows, idxErr := d.db.QueryContext(ctx, `
		SELECT i.INDEX_NAME, COALESCE(i.UNIQUENESS, ''), COALESCE(i.INDEX_TYPE, 'NORMAL'), ic.COLUMN_NAME
		FROM ALL_INDEXES i
		JOIN ALL_IND_COLUMNS ic ON i.OWNER = ic.INDEX_OWNER AND i.INDEX_NAME = ic.INDEX_NAME
		WHERE i.TABLE_OWNER = :1 AND i.TABLE_NAME = :2
		ORDER BY i.INDEX_NAME, ic.COLUMN_POSITION
	`, schema, table)
	if idxErr == nil {
		defer idxRows.Close()
		idxMap := map[string]*driver.IndexDef{}
		var idxOrder []string
		for idxRows.Next() {
			var iName, uniq, iType, colName string
			if idxRows.Scan(&iName, &uniq, &iType, &colName) == nil {
				item, exists := idxMap[iName]
				if !exists {
					item = &driver.IndexDef{
						Name:      iName,
						Unique:    strings.EqualFold(uniq, "UNIQUE"),
						Primary:   pkConstraint != "" && strings.EqualFold(iName, pkConstraint),
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

	// Foreign Keys
	fkRows, fkErr := d.db.QueryContext(ctx, `
		SELECT c.CONSTRAINT_NAME, COALESCE(c.DELETE_RULE, ''), acc.COLUMN_NAME, rc.TABLE_NAME, rcc.COLUMN_NAME
		FROM ALL_CONSTRAINTS c
		JOIN ALL_CONS_COLUMNS acc ON c.OWNER = acc.OWNER AND c.CONSTRAINT_NAME = acc.CONSTRAINT_NAME
		JOIN ALL_CONSTRAINTS rc ON c.R_OWNER = rc.OWNER AND c.R_CONSTRAINT_NAME = rc.CONSTRAINT_NAME
		JOIN ALL_CONS_COLUMNS rcc ON rc.OWNER = rcc.OWNER AND rc.CONSTRAINT_NAME = rcc.CONSTRAINT_NAME AND acc.POSITION = rcc.POSITION
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 AND c.CONSTRAINT_TYPE = 'R'
		ORDER BY c.CONSTRAINT_NAME, acc.POSITION
	`, schema, table)
	if fkErr == nil {
		defer fkRows.Close()
		fkMap := map[string]*driver.ForeignKeyDef{}
		var fkOrder []string
		for fkRows.Next() {
			var fkName, delRule, colName, refTbl, refCol string
			if fkRows.Scan(&fkName, &delRule, &colName, &refTbl, &refCol) == nil {
				item, exists := fkMap[fkName]
				if !exists {
					item = &driver.ForeignKeyDef{
						Name:     fkName,
						RefTable: refTbl,
						OnDelete: delRule,
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
	chkRows, chkErr := d.db.QueryContext(ctx, `
		SELECT CONSTRAINT_NAME, SEARCH_CONDITION, COALESCE(STATUS, 'ENABLED')
		FROM ALL_CONSTRAINTS
		WHERE OWNER = :1 AND TABLE_NAME = :2 AND CONSTRAINT_TYPE = 'C' AND GENERATED = 'USER NAME'
		ORDER BY CONSTRAINT_NAME
	`, schema, table)
	if chkErr == nil {
		defer chkRows.Close()
		for chkRows.Next() {
			var cName, status string
			var cond sql.NullString
			if chkRows.Scan(&cName, &cond, &status) == nil && cond.Valid && strings.TrimSpace(cond.String) != "" {
				st.CheckConstraints = append(st.CheckConstraints, driver.CheckConstraintDef{
					Name:       cName,
					Expression: strings.TrimSpace(cond.String),
					Enforced:   strings.EqualFold(status, "ENABLED"),
				})
			}
		}
	}

	_ = d.db.QueryRowContext(ctx, "SELECT DBMS_METADATA.GET_DDL('TABLE', :1, :2) FROM DUAL", table, schema).Scan(&st.DDL)
	if strings.TrimSpace(st.DDL) == "" && len(st.Columns) > 0 {
		st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectOracle, driver.CreateTableRequest{
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
	pks, _ := d.getPrimaryKeys(ctx, req.Schema, req.Table)
	res.PrimaryKeys = pks
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

func (d *Driver) GetAutocompleteMeta(ctx context.Context, database string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Tables: map[string][]string{}}
	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			meta.Databases = append(meta.Databases, item.Name)
		}
	} else if sqlbase.IsAuthError("oracle", err) {
		return meta, err
	}
	defaultOwner := strings.ToUpper(strings.TrimSpace(d.cfg.Username))
	owner := strings.ToUpper(strings.TrimSpace(database))
	if owner == "" || strings.EqualFold(owner, d.cfg.Database) || owner == "ORCL" {
		owner = defaultOwner
	}
	loadCols := func(targetOwner string) error {
		if targetOwner == "" {
			return nil
		}
		rows, err := d.db.QueryContext(ctx, `
			SELECT TABLE_NAME, COLUMN_NAME
			FROM ALL_TAB_COLUMNS
			WHERE OWNER = :1
			ORDER BY TABLE_NAME, COLUMN_ID
		`, targetOwner)
		if err != nil {
			if sqlbase.IsAuthError("oracle", err) {
				return err
			}
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var tName, cName string
			if rows.Scan(&tName, &cName) == nil {
				meta.Tables[tName] = append(meta.Tables[tName], cName)
			}
		}
		return nil
	}
	if err := loadCols(owner); err != nil {
		return meta, err
	}
	if len(meta.Tables) == 0 && owner != defaultOwner {
		if err := loadCols(defaultOwner); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

func (d *Driver) ListPartitions(ctx context.Context, _, schema, table string) ([]driver.PartitionInfo, error) {
	if schema == "" {
		schema = strings.ToUpper(d.cfg.Username)
	}
	out := []driver.PartitionInfo{}
	var strategy string
	if err := d.db.QueryRowContext(ctx, `SELECT COALESCE(PARTITIONING_TYPE, '') FROM ALL_PART_TABLES WHERE OWNER = :1 AND TABLE_NAME = :2`, schema, table).Scan(&strategy); err != nil && sqlbase.IsAuthError("oracle", err) {
		return nil, err
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT PARTITION_NAME, COALESCE(NUM_ROWS, 0), COALESCE(BLOCKS, 0) * 8192, HIGH_VALUE
		FROM ALL_TAB_PARTITIONS
		WHERE TABLE_OWNER = :1 AND TABLE_NAME = :2
		ORDER BY PARTITION_POSITION
	`, schema, table)
	if err != nil {
		if sqlbase.IsAuthError("oracle", err) {
			return nil, err
		}
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var pName string
		var numRows, bytesOnDisk int64
		var highVal sql.NullString
		if rows.Scan(&pName, &numRows, &bytesOnDisk, &highVal) == nil {
			out = append(out, driver.PartitionInfo{
				ID:               pName,
				Name:             pName,
				Rows:             numRows,
				BytesOnDisk:      bytesOnDisk,
				Active:           true,
				Strategy:         strategy,
				BoundDescription: strings.TrimSpace(highVal.String),
			})
		}
	}
	return out, nil
}

func (d *Driver) ManagePartition(ctx context.Context, req driver.PartitionActionRequest) (string, error) {
	schema := strings.TrimSpace(req.Schema)
	if schema == "" {
		schema = strings.ToUpper(d.cfg.Username)
	}
	if strings.TrimSpace(req.Table) == "" || strings.TrimSpace(req.PartitionID) == "" {
		return "", fmt.Errorf("table and partitionId are required")
	}
	tbl := sqlbase.QualifyTable(sqlbase.DialectOracle, "", schema, req.Table)
	part := sqlbase.QuoteIdent(sqlbase.DialectOracle, req.PartitionID)
	var stmt string
	switch strings.ToUpper(strings.TrimSpace(req.Action)) {
	case "TRUNCATE":
		stmt = fmt.Sprintf("ALTER TABLE %s TRUNCATE PARTITION %s", tbl, part)
	case "DROP":
		stmt = fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", tbl, part)
	default:
		return "", fmt.Errorf("unsupported partition action %q for Oracle", req.Action)
	}
	if req.PreviewOnly {
		return stmt, nil
	}
	_, err := d.db.ExecContext(ctx, stmt)
	return stmt, err
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}, CanKillSession: true}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	} else if sqlbase.IsAuthError("oracle", err) {
		return snap, err
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT SID || ',' || SERIAL#, COALESCE(USERNAME,''), COALESCE(MACHINE,''), STATUS, COALESCE(LAST_CALL_ET,0)
		FROM V$SESSION WHERE USERNAME IS NOT NULL
	`)
	if err != nil {
		if sqlbase.IsAuthError("oracle", err) {
			return snap, err
		}
	} else {
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
