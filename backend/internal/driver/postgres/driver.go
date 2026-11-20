package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
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
			Type:               variant.typ,
			Name:               variant.name,
			Category:           driver.CategoryRelational,
			BadgeColor:         variant.color,
			DefaultPort:        variant.port,
			Description:        variant.desc,
			SupportsSchemas:    true,
			SupportsPartitions: true,
			SupportsExplain:    true,
			DefaultTypes:       pgTypes,
			Fields:             fields,
		}, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
			return New(cfg, variant.typ, variant.name, variant.port)
		})
	}
}

const (
	maxPGPoolsSoft           = 4
	maxPGPoolsHard           = 16
	maxPGAutocompleteColumns = 25000
)

type pgPoolEntry struct {
	db      *sql.DB
	seq     uint64
	active  int
	evicted bool
}

type Driver struct {
	cfg         driver.ConnectionConfig
	variantType string
	productName string
	defaultPort int
	mu          sync.Mutex
	pools       map[string]*pgPoolEntry
	poolSeq     uint64
	openDBFn    func(dbName string) (*sql.DB, error)
}

func New(cfg driver.ConnectionConfig, variantType, productName string, defaultPort int) (driver.Driver, error) {
	d := &Driver{
		cfg:         cfg,
		variantType: variantType,
		productName: productName,
		defaultPort: defaultPort,
		pools:       make(map[string]*pgPoolEntry),
	}
	initDB := cfg.Database
	if initDB == "" {
		initDB = "postgres"
	}
	_, release, err := d.acquirePool(initDB)
	if err != nil {
		return nil, err
	}
	release()
	return d, nil
}

func (d *Driver) effectiveDefaultDB() string {
	if strings.TrimSpace(d.cfg.Database) != "" {
		return d.cfg.Database
	}
	return "postgres"
}

func (d *Driver) evictIdlePoolsLocked(defaultDB string, targetCap int, allowDefault bool) {
	if len(d.pools) <= targetCap {
		return
	}
	type idleItem struct {
		key   string
		seq   uint64
		entry *pgPoolEntry
	}
	var secondaryIdle []idleItem
	var defaultIdle *idleItem
	for k, entry := range d.pools {
		if entry == nil || entry.db == nil {
			delete(d.pools, k)
			continue
		}
		if entry.active > 0 || entry.db.Stats().InUse != 0 {
			continue
		}
		if k == defaultDB {
			defaultIdle = &idleItem{key: k, seq: entry.seq, entry: entry}
		} else {
			secondaryIdle = append(secondaryIdle, idleItem{key: k, seq: entry.seq, entry: entry})
		}
	}
	sort.Slice(secondaryIdle, func(i, j int) bool {
		return secondaryIdle[i].seq < secondaryIdle[j].seq
	})
	for _, item := range secondaryIdle {
		if len(d.pools) <= targetCap {
			break
		}
		item.entry.evicted = true
		if item.entry.active == 0 && item.entry.db != nil {
			_ = item.entry.db.Close()
			item.entry.db = nil
		}
		delete(d.pools, item.key)
	}
	if allowDefault && len(d.pools) > targetCap && defaultIdle != nil {
		defaultIdle.entry.evicted = true
		if defaultIdle.entry.active == 0 && defaultIdle.entry.db != nil {
			_ = defaultIdle.entry.db.Close()
			defaultIdle.entry.db = nil
		}
		delete(d.pools, defaultIdle.key)
	}
}

func (d *Driver) openDBForName(dbName string) (*sql.DB, error) {
	if d.openDBFn != nil {
		return d.openDBFn(dbName)
	}
	restrictIP := d.cfg.Option("__restrict_internal_ip", "") == "1"
	host := strings.TrimSpace(d.cfg.Host)
	if host == "" && !restrictIP {
		host = "127.0.0.1"
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
		Host:   net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)),
		Path:   "/" + dbName,
	}
	q := u.Query()
	q.Set("sslmode", d.cfg.Option("sslmode", "disable"))
	q.Set("connect_timeout", "8")
	u.RawQuery = q.Encode()

	var (
		db  *sql.DB
		err error
	)
	if restrictIP {
		connCfg, pErr := pgx.ParseConfig(u.String())
		if pErr != nil {
			return nil, pErr
		}
		connCfg.DialFunc = sqlbase.NewRestrictedDialContext(8 * time.Second)
		db = stdlib.OpenDB(*connCfg)
	} else {
		db, err = sql.Open("pgx", u.String())
		if err != nil {
			return nil, err
		}
	}

	defaultDB := d.effectiveDefaultDB()
	if dbName == defaultDB {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(2)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(1)
	}
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)
	return db, nil
}

func (d *Driver) makePoolRelease(entry *pgPoolEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			var toClose *sql.DB
			d.mu.Lock()
			if entry.active > 0 {
				entry.active--
			}
			if entry.evicted && entry.active == 0 && entry.db != nil {
				toClose = entry.db
				entry.db = nil
			}
			d.mu.Unlock()
			if toClose != nil {
				_ = toClose.Close()
			}
		})
	}
}

func (d *Driver) acquirePool(dbName string) (*sql.DB, func(), error) {
	if dbName == "" {
		dbName = d.cfg.Database
	}
	if dbName == "" {
		dbName = "postgres"
	}
	defaultDB := d.effectiveDefaultDB()

	d.mu.Lock()
	if d.pools == nil {
		d.pools = make(map[string]*pgPoolEntry)
	}
	if existing, ok := d.pools[dbName]; ok && existing != nil && existing.db != nil && !existing.evicted {
		d.poolSeq++
		existing.seq = d.poolSeq
		existing.active++
		d.evictIdlePoolsLocked(defaultDB, maxPGPoolsSoft, false)
		db := existing.db
		d.mu.Unlock()
		return db, d.makePoolRelease(existing), nil
	}

	d.evictIdlePoolsLocked(defaultDB, maxPGPoolsSoft-1, false)
	if len(d.pools) >= maxPGPoolsHard {
		d.evictIdlePoolsLocked(defaultDB, maxPGPoolsHard-1, true)
	}
	if len(d.pools) >= maxPGPoolsHard {
		d.mu.Unlock()
		return nil, nil, errors.New("too many concurrent active PostgreSQL database pools; please retry shortly")
	}

	db, err := d.openDBForName(dbName)
	if err != nil {
		d.mu.Unlock()
		return nil, nil, err
	}
	d.poolSeq++
	entry := &pgPoolEntry{
		db:     db,
		seq:    d.poolSeq,
		active: 1,
	}
	d.pools[dbName] = entry
	d.mu.Unlock()
	return db, d.makePoolRelease(entry), nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta(d.variantType)
	return m
}

func (d *Driver) Close() error {
	d.mu.Lock()
	var toClose []*sql.DB
	for k, entry := range d.pools {
		if entry == nil {
			delete(d.pools, k)
			continue
		}
		entry.evicted = true
		if entry.active == 0 && entry.db != nil {
			toClose = append(toClose, entry.db)
			entry.db = nil
		}
		delete(d.pools, k)
	}
	d.pools = map[string]*pgPoolEntry{}
	d.mu.Unlock()
	for _, db := range toClose {
		_ = db.Close()
	}
	return nil
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	db, release, err := d.acquirePool("")
	if err != nil {
		return driver.ServerInfo{}, err
	}
	defer release()
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
	db, release, err := d.acquirePool("")
	if err != nil {
		return nil, err
	}
	defer release()
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
	db, release, err := d.acquirePool("")
	if err != nil {
		return err
	}
	defer release()
	return sqlbase.Exec(ctx, db, "CREATE DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, name))
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	d.mu.Lock()
	var dropDB *sql.DB
	if existing, ok := d.pools[name]; ok {
		delete(d.pools, name)
		if existing != nil {
			existing.evicted = true
			if existing.active == 0 && existing.db != nil {
				dropDB = existing.db
				existing.db = nil
			}
		}
	}
	d.mu.Unlock()
	if dropDB != nil {
		_ = dropDB.Close()
	}

	adminDBName := "postgres"
	if strings.EqualFold(d.cfg.Database, name) {
		adminDBName = "postgres"
	} else if d.cfg.Database != "" {
		adminDBName = d.cfg.Database
	}
	db, release, err := d.acquirePool(adminDBName)
	if err != nil {
		return err
	}
	defer release()
	return sqlbase.Exec(ctx, db, "DROP DATABASE "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, name))
}

func (d *Driver) CreateSchema(ctx context.Context, database, schema string) error {
	db, release, err := d.acquirePool(database)
	if err != nil {
		return err
	}
	defer release()
	return sqlbase.Exec(ctx, db, "CREATE SCHEMA IF NOT EXISTS "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, schema))
}

func (d *Driver) DropSchema(ctx context.Context, database, schema string, cascade bool) error {
	lower := strings.ToLower(strings.TrimSpace(schema))
	if lower == "" || lower == "pg_catalog" || lower == "information_schema" || strings.HasPrefix(lower, "pg_toast") {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	db, release, err := d.acquirePool(database)
	if err != nil {
		return err
	}
	defer release()
	mode := " RESTRICT"
	if cascade {
		mode = " CASCADE"
	}
	return sqlbase.Exec(ctx, db, "DROP SCHEMA "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, schema)+mode)
}

func (d *Driver) ListSchemas(ctx context.Context, database string) ([]string, error) {
	db, release, err := d.acquirePool(database)
	if err != nil {
		return nil, err
	}
	defer release()
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
	db, release, err := d.acquirePool(database)
	if err != nil {
		return driver.SchemaObjects{}, err
	}
	defer release()
	res := driver.SchemaObjects{
		Database:          database,
		Schema:            schema,
		Tables:            []driver.ObjectItem{},
		Views:             []driver.ObjectItem{},
		MaterializedViews: []driver.ObjectItem{},
		Routines:          []driver.ObjectItem{},
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
		switch kind {
		case "v":
			item.Type = "VIEW"
			res.Views = append(res.Views, item)
		case "m":
			item.Type = "MATERIALIZED_VIEW"
			res.MaterializedViews = append(res.MaterializedViews, item)
		default:
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
	db, release, err := d.acquirePool(database)
	if err != nil {
		return driver.TableStructure{}, err
	}
	defer release()
	st := driver.TableStructure{
		Database:         database,
		Schema:           schema,
		Table:            table,
		Columns:          []driver.ColumnDef{},
		Indexes:          []driver.IndexDef{},
		ForeignKeys:      []driver.ForeignKeyDef{},
		CheckConstraints: []driver.CheckConstraintDef{},
	}

	// Table comment
	_ = db.QueryRowContext(ctx, `
		SELECT COALESCE(obj_description(c.oid, 'pg_class'), '')
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
	`, schema, table).Scan(&st.Comment)

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

	// Columns (PostgreSQL 12+ with attgenerated/attidentity, with fallback for older PG / KingbaseES)
	cRows, err := db.QueryContext(ctx, `
		SELECT
			a.attname,
			pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
			NOT a.attnotnull AS is_nullable,
			COALESCE(pg_get_expr(ad.adbin, ad.adrelid), '') AS expr_val,
			COALESCE(a.attgenerated::text, '') AS attgenerated,
			COALESCE(a.attidentity::text, '') AS attidentity,
			COALESCE(col_description(c.oid, a.attnum), '') AS col_comment
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum
	`, schema, table)
	if err == nil {
		defer cRows.Close()
		for cRows.Next() {
			var name, colType, exprVal, attGen, attIdent, desc string
			var isNullable bool
			if sErr := cRows.Scan(&name, &colType, &isNullable, &exprVal, &attGen, &attIdent, &desc); sErr != nil {
				return st, sErr
			}
			defVal := exprVal
			genExpr := ""
			genStorage := ""
			if strings.TrimSpace(attGen) != "" {
				defVal = ""
				genExpr = exprVal
				if strings.EqualFold(strings.TrimSpace(attGen), "v") {
					genStorage = "VIRTUAL"
				} else {
					genStorage = "STORED"
				}
			}
			st.Columns = append(st.Columns, driver.ColumnDef{
				Name:             name,
				Type:             colType,
				Nullable:         isNullable,
				DefaultValue:     defVal,
				IsPrimaryKey:     pkSet[name],
				IsAutoIncrement:  strings.TrimSpace(attIdent) != "" || strings.Contains(defVal, "nextval("),
				Comment:          desc,
				GeneratedExpr:    genExpr,
				GeneratedStorage: genStorage,
			})
		}
	} else {
		// Fallback for older PostgreSQL / KingbaseES
		fRows, fErr := db.QueryContext(ctx, `
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
		if fErr != nil {
			return st, fErr
		}
		defer fRows.Close()
		for fRows.Next() {
			var name, udt, isNull, desc string
			var maxLen sql.NullInt64
			var defVal sql.NullString
			if sErr := fRows.Scan(&name, &udt, &maxLen, &isNull, &defVal, &desc); sErr != nil {
				return st, sErr
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
	}

	// Indexes (with ordered columns, uniqueness, primary flag, and access method type)
	iRows, err := db.QueryContext(ctx, `
		SELECT
			ic.relname AS index_name,
			i.indisunique,
			i.indisprimary,
			UPPER(COALESCE(am.amname, 'btree')) AS index_type,
			COALESCE(ARRAY_TO_STRING(ARRAY(
				SELECT pg_get_indexdef(i.indexrelid, k::int, true)
				FROM generate_subscripts(i.indkey, 1) AS k
				ORDER BY k
			), E'\x1f'), '') AS cols_joined
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
		JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
		LEFT JOIN pg_catalog.pg_am am ON am.oid = ic.relam
		WHERE n.nspname = $1 AND tc.relname = $2
		ORDER BY i.indisprimary DESC, ic.relname
	`, schema, table)
	if err == nil {
		defer iRows.Close()
		for iRows.Next() {
			var idxName, idxType, colsJoined string
			var isUniq, isPri bool
			if iRows.Scan(&idxName, &isUniq, &isPri, &idxType, &colsJoined) == nil {
				var cols []string
				if colsJoined != "" {
					cols = strings.Split(colsJoined, "\x1f")
				}
				st.Indexes = append(st.Indexes, driver.IndexDef{
					Name:      idxName,
					Columns:   cols,
					Unique:    isUniq,
					Primary:   isPri,
					IndexType: idxType,
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
			COALESCE(rc.update_rule, ''),
			COALESCE(rc.delete_rule, '')
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

	// CHECK constraints (pg_constraint contype = 'c')
	chkRows, chkErr := db.QueryContext(ctx, `
		SELECT con.conname, pg_get_constraintdef(con.oid, true)
		FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND con.contype = 'c'
		ORDER BY con.conname
	`, schema, table)
	if chkErr == nil {
		defer chkRows.Close()
		for chkRows.Next() {
			var cName, cDef string
			if chkRows.Scan(&cName, &cDef) == nil {
				expr := strings.TrimSpace(cDef)
				if strings.HasPrefix(strings.ToUpper(expr), "CHECK") {
					expr = strings.TrimSpace(expr[len("CHECK"):])
				}
				st.CheckConstraints = append(st.CheckConstraints, driver.CheckConstraintDef{
					Name:       cName,
					Expression: expr,
					Enforced:   true,
				})
			}
		}
	}

	// Reconstruct clean DDL
	if ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, driver.CreateTableRequest{
		Schema:           schema,
		Table:            table,
		Columns:          st.Columns,
		ForeignKeys:      st.ForeignKeys,
		CheckConstraints: st.CheckConstraints,
	}); err == nil {
		st.DDL = ddl
	}
	return st, nil
}

func (d *Driver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return driver.QueryResult{}, err
	}
	defer release()
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectPostgres, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	res, err := sqlbase.ExecuteSingleQuery(ctx, db, dataSQL, req.PageSize)
	if err != nil {
		return res, err
	}
	if total, ok := sqlbase.QueryCount(ctx, db, countSQL); ok {
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
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return driver.MutationResult{}, err
	}
	defer release()
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
	stmts := []string{ddl}
	idxStmts, idxErr := sqlbase.BuildCreateTableIndexSQLs(sqlbase.DialectPostgres, req)
	if idxErr != nil {
		return "", idxErr
	}
	stmts = append(stmts, idxStmts...)

	tbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", req.Schema, req.Table)
	if req.Comment != "" {
		stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS %s", tbl, sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, req.Comment)))
	}
	for _, col := range req.Columns {
		if col.Comment != "" && strings.TrimSpace(col.Name) != "" {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, sqlbase.QuoteIdent(sqlbase.DialectPostgres, col.Name), sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, col.Comment)))
		}
	}

	fullScript := strings.Join(stmts, ";\n")
	if req.PreviewOnly {
		return fullScript, nil
	}
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return fullScript, err
	}
	defer release()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fullScript, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := sqlbase.ExecBatch(ctx, tx, stmts); err != nil {
		return fullScript, err
	}
	if err := tx.Commit(); err != nil {
		return fullScript, err
	}
	return fullScript, nil
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
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return stmts, err
	}
	defer release()
	if err := sqlbase.ExecBatch(ctx, db, stmts); err != nil {
		return stmts, err
	}
	return stmts, nil
}

func (d *Driver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return err
	}
	defer release()
	reqCopy := req
	reqCopy.Database = ""
	if strings.EqualFold(strings.TrimSpace(reqCopy.ObjectType), "TRIGGER") && strings.TrimSpace(reqCopy.TargetName) == "" {
		var parentTbl string
		_ = sqlbase.QueryRow(ctx, db, `
			SELECT event_object_table FROM information_schema.triggers
			WHERE trigger_schema = $1 AND trigger_name = $2 LIMIT 1
		`, reqCopy.Schema, reqCopy.ObjectName).Scan(&parentTbl)
		if parentTbl != "" {
			reqCopy.TargetName = parentTbl
		}
	}
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectPostgres, reqCopy)
	if err != nil {
		return err
	}
	return sqlbase.ExecBatch(ctx, db, sqls)
}

// PartitionManager implementation for PostgreSQL / KingbaseES declarative partitioning
func (d *Driver) ListPartitions(ctx context.Context, database, schema, table string) ([]driver.PartitionInfo, error) {
	if schema == "" {
		schema = "public"
	}
	db, release, err := d.acquirePool(database)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := db.QueryContext(ctx, `
		SELECT
			child.relname AS part_name,
			COALESCE(pg_get_partkeydef(parent.oid), '') AS part_key,
			COALESCE(pg_get_expr(child.relpartbound, child.oid, true), '') AS bound_expr,
			GREATEST(child.reltuples::bigint, 0) AS est_rows,
			COALESCE(pg_total_relation_size(child.oid), 0) AS disk_bytes
		FROM pg_catalog.pg_inherits inh
		JOIN pg_catalog.pg_class parent ON parent.oid = inh.inhparent
		JOIN pg_catalog.pg_namespace pn ON pn.oid = parent.relnamespace
		JOIN pg_catalog.pg_class child ON child.oid = inh.inhrelid
		WHERE pn.nspname = $1 AND parent.relname = $2
		ORDER BY child.relname
	`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []driver.PartitionInfo{}
	for rows.Next() {
		var pName, partKey, boundExpr string
		var rCnt, bCnt int64
		if err := rows.Scan(&pName, &partKey, &boundExpr, &rCnt, &bCnt); err != nil {
			return nil, err
		}
		strategy := ""
		if fields := strings.Fields(partKey); len(fields) > 0 {
			strategy = strings.ToUpper(fields[0])
		}
		list = append(list, driver.PartitionInfo{
			ID:               pName,
			Name:             pName,
			Rows:             rCnt,
			BytesOnDisk:      bCnt,
			PartsCount:       1,
			Active:           true,
			Strategy:         strategy,
			Expression:       partKey,
			BoundDescription: boundExpr,
		})
	}
	return list, rows.Err()
}

func (d *Driver) ManagePartition(ctx context.Context, req driver.PartitionActionRequest) (string, error) {
	sc := strings.TrimSpace(req.Schema)
	if sc == "" {
		sc = "public"
	}
	if strings.TrimSpace(req.Table) == "" || strings.TrimSpace(req.PartitionID) == "" {
		return "", errors.New("parent table and partition table name are required")
	}
	parentTbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
	partTbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.PartitionID)

	var stmt string
	switch strings.ToUpper(strings.TrimSpace(req.Action)) {
	case "DETACH":
		stmt = fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s", parentTbl, partTbl)
	case "ATTACH":
		expr := strings.TrimSpace(req.Expression)
		if err := sqlbase.ValidateSQLExpressionForEngine(d.variantType, "partition bound specification", expr); err != nil {
			return "", err
		}
		upper := strings.ToUpper(expr)
		if strings.HasPrefix(upper, "FOR VALUES ") || upper == "DEFAULT" {
			stmt = fmt.Sprintf("ALTER TABLE %s ATTACH PARTITION %s %s", parentTbl, partTbl, expr)
		} else {
			stmt = fmt.Sprintf("ALTER TABLE %s ATTACH PARTITION %s FOR VALUES %s", parentTbl, partTbl, expr)
		}
	case "DROP":
		stmt = fmt.Sprintf("DROP TABLE IF EXISTS %s", partTbl)
	default:
		return "", fmt.Errorf("unsupported PostgreSQL partition action: %s", req.Action)
	}
	if req.PreviewOnly {
		return stmt, nil
	}
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return stmt, err
	}
	defer release()
	err = sqlbase.Exec(ctx, db, stmt)
	return stmt, err
}

func (d *Driver) ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]driver.QueryResult, error) {
	db, release, err := d.acquirePool(database)
	if err != nil {
		return nil, err
	}
	defer release()
	return sqlbase.ExecuteMultiSQLForEngine(ctx, db, d.variantType, query, maxRows)
}

func (d *Driver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	if req.Schema == "" {
		req.Schema = "public"
	}
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return err
	}
	defer release()
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
				}
				idxRows, iErr := db.QueryContext(ctx, `
					SELECT i.indexdef
					FROM pg_catalog.pg_indexes i
					LEFT JOIN pg_catalog.pg_constraint c ON c.conname = i.indexname AND c.contype = 'p'
					WHERE i.schemaname = $1 AND i.tablename = $2 AND c.oid IS NULL
					ORDER BY i.indexname
				`, req.Schema, name)
				if iErr == nil {
					for idxRows.Next() {
						var idxDef string
						if idxRows.Scan(&idxDef) == nil && strings.TrimSpace(idxDef) != "" {
							idxDDLs = append(idxDDLs, idxDef)
						}
					}
					_ = idxRows.Close()
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
	db, release, err := d.acquirePool(req.Database)
	if err != nil {
		return driver.ImportResult{}, err
	}
	defer release()
	return sqlbase.BatchImport(ctx, db, sqlbase.DialectPostgres, req)
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
	db, release, err := d.acquirePool(database)
	if err != nil {
		if sqlbase.IsAuthError(d.variantType, err) {
			return meta, err
		}
		return meta, nil
	}
	defer release()
	rows, err := sqlbase.Query(ctx, db, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema NOT LIKE 'pg_%' AND table_schema <> 'information_schema'
		ORDER BY table_name, ordinal_position
		LIMIT 25000
	`)
	if err != nil {
		if sqlbase.IsAuthError(d.variantType, err) {
			return meta, err
		}
		return meta, nil
	}
	defer rows.Close()
	rowCount := 0
	for rows.Next() {
		if rowCount >= maxPGAutocompleteColumns {
			break
		}
		var t, c string
		if rows.Scan(&t, &c) == nil {
			meta.Tables[t] = append(meta.Tables[t], c)
			rowCount++
		}
	}
	return meta, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	db, release, err := d.acquirePool("")
	if err != nil {
		return driver.MonitorSnapshot{}, err
	}
	defer release()
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	if err := db.QueryRowContext(ctx, "SHOW server_version").Scan(&snap.Version); err != nil && sqlbase.IsAuthError(d.variantType, err) {
		return snap, err
	}
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))::bigint, 0)").Scan(&snap.UptimeSec); err != nil && sqlbase.IsAuthError(d.variantType, err) {
		return snap, err
	}

	var activeConns, totalConns int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE state = 'active'), COUNT(*) FROM pg_stat_activity").Scan(&activeConns, &totalConns); err != nil && sqlbase.IsAuthError(d.variantType, err) {
		return snap, err
	}
	snap.Metrics = append(snap.Metrics,
		driver.MonitorMetric{Key: "total_conns", Label: "Total Connections", Value: strconv.FormatInt(totalConns, 10)},
		driver.MonitorMetric{Key: "active_conns", Label: "Active Queries", Value: strconv.FormatInt(activeConns, 10)},
	)

	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, item := range dbs {
			snap.Storage = append(snap.Storage, driver.StorageItem{Name: item.Name, Category: "DATABASE", SizeBytes: item.SizeBytes})
		}
	} else if sqlbase.IsAuthError(d.variantType, err) {
		return snap, err
	}

	pRows, err := db.QueryContext(ctx, `
		SELECT pid, COALESCE(usename, ''), COALESCE(client_addr::text, 'local'), COALESCE(datname, ''),
		       COALESCE(state, ''), COALESCE(EXTRACT(EPOCH FROM (now() - query_start))::bigint, 0), COALESCE(query, '')
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		ORDER BY query_start ASC NULLS LAST LIMIT 100
	`)
	if err != nil {
		if sqlbase.IsAuthError(d.variantType, err) {
			return snap, err
		}
	} else {
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
	db, release, err := d.acquirePool("")
	if err != nil {
		return err
	}
	defer release()
	pid, err := strconv.ParseInt(sessionID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid PID: %s", sessionID)
	}
	_, err = db.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid)
	return err
}
