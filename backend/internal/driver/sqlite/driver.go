package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

var _ driver.FileExporter = (*SQLiteDriver)(nil)

type SQLiteDriver struct {
	cfg      driver.ConnectionConfig
	filePath string
	webMode  bool
	db       *sql.DB
}

func hasInvalidLocalDBPathChars(fp string) bool {
	for i := 0; i < len(fp); i++ {
		ch := fp[i]
		if ch < 0x20 || ch == 0x7f || ch == '?' || ch == '#' {
			return true
		}
	}
	return false
}

func NewSQLite(cfg driver.ConnectionConfig) (driver.Driver, error) {
	fp := strings.TrimSpace(cfg.FilePath)
	if fp == "" {
		return nil, errors.New("SQLite database file path cannot be empty")
	}
	webMode := cfg.Option("__web_mode", "") == "1"
	if fp != ":memory:" {
		if hasInvalidLocalDBPathChars(fp) {
			return nil, errors.New("invalid SQLite file path: query parameters, fragments, and control characters are not allowed")
		}
		if strings.HasPrefix(strings.ToLower(fp), "file:") {
			return nil, errors.New("SQLite URI file paths are not supported; please specify a standard file path or :memory:")
		}
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

type sqliteSandboxToken struct {
	kind  string // "word", "ident", "string", "punct"
	upper string
}

func lexSQLiteSandboxTokens(s string) []sqliteSandboxToken {
	var tokens []sqliteSandboxToken
	n := len(s)
	i := 0
	for i < n {
		ch := s[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			i++
			continue
		}
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		if (ch == 'x' || ch == 'X') && i+1 < n && s[i+1] == '\'' {
			j := i + 2
			for j < n {
				if s[j] == '\'' {
					if j+1 < n && s[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			tokens = append(tokens, sqliteSandboxToken{kind: "string"})
			i = j
			continue
		}
		if ch == '\'' {
			var b strings.Builder
			j := i + 1
			for j < n {
				if s[j] == '\'' {
					if j+1 < n && s[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(s[j])
				j++
			}
			tokens = append(tokens, sqliteSandboxToken{kind: "string", upper: strings.ToUpper(strings.TrimSpace(b.String()))})
			i = j
			continue
		}
		if ch == '"' || ch == '`' {
			q := ch
			var b strings.Builder
			j := i + 1
			for j < n {
				if s[j] == q {
					if j+1 < n && s[j+1] == q {
						b.WriteByte(q)
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(s[j])
				j++
			}
			tokens = append(tokens, sqliteSandboxToken{kind: "ident", upper: strings.ToUpper(strings.TrimSpace(b.String()))})
			i = j
			continue
		}
		if ch == '[' {
			var b strings.Builder
			j := i + 1
			for j < n {
				if s[j] == ']' {
					if j+1 < n && s[j+1] == ']' {
						b.WriteByte(']')
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(s[j])
				j++
			}
			tokens = append(tokens, sqliteSandboxToken{kind: "ident", upper: strings.ToUpper(strings.TrimSpace(b.String()))})
			i = j
			continue
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' || ch >= 0x80 {
			j := i + 1
			for j < n {
				c := s[j]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' || c >= 0x80 {
					j++
				} else {
					break
				}
			}
			tokens = append(tokens, sqliteSandboxToken{kind: "word", upper: strings.ToUpper(s[i:j])})
			i = j
			continue
		}
		tokens = append(tokens, sqliteSandboxToken{kind: "punct", upper: s[i : i+1]})
		i++
	}
	return tokens
}

var forbiddenSQLiteSandboxPragmas = map[string]bool{
	"WRITABLE_SCHEMA":      true,
	"TRUSTED_SCHEMA":       true,
	"TEMP_STORE_DIRECTORY": true,
	"DATA_STORE_DIRECTORY": true,
}

var forbiddenSQLiteSandboxFunctions = map[string]bool{
	"LOAD_EXTENSION": true,
	"READFILE":       true,
	"WRITEFILE":      true,
	"FTS3_TOKENIZER": true,
	"EDIT":           true,
}

func validateSQLiteWebSandbox(script string) error {
	for _, stmt := range sqlbase.SplitStatements(script) {
		tokens := lexSQLiteSandboxTokens(stmt)
		if len(tokens) == 0 {
			continue
		}
		hasVacuum := false
		hasInto := false
		for i, tok := range tokens {
			if tok.kind == "word" {
				switch tok.upper {
				case "ATTACH", "DETACH":
					return errors.New("ATTACH / DETACH DATABASE is disabled in Web Server mode for host sandbox isolation")
				case "VACUUM":
					hasVacuum = true
				case "INTO":
					if hasVacuum {
						hasInto = true
					}
				case "PRAGMA":
					if i+1 < len(tokens) {
						targetTok := tokens[i+1]
						if i+3 < len(tokens) && tokens[i+2].kind == "punct" && tokens[i+2].upper == "." {
							targetTok = tokens[i+3]
						}
						if targetTok.kind != "punct" && forbiddenSQLiteSandboxPragmas[targetTok.upper] {
							return fmt.Errorf("PRAGMA %s is disabled in Web Server mode for host sandbox isolation", strings.ToLower(targetTok.upper))
						}
					}
				}
			}
			if (tok.kind == "word" || tok.kind == "ident") && forbiddenSQLiteSandboxFunctions[tok.upper] {
				if i+1 < len(tokens) && tokens[i+1].kind == "punct" && tokens[i+1].upper == "(" {
					return errors.New("load_extension and filesystem/editor SQL functions are disabled in Web Server mode")
				}
			}
		}
		if hasVacuum && hasInto {
			return errors.New("VACUUM INTO arbitrary paths is disabled in Web Server mode; use the built-in Export Database File action instead")
		}
	}
	return nil
}

type sqliteDDLMetadata struct {
	isVirtual     bool
	virtualModule string
	withoutRowID  bool
	strict        bool
	genExprs      map[string]string
	checks        []driver.CheckConstraintDef
}

func unquoteSQLiteIdent(tok string) (name string, wasQuoted bool) {
	t := strings.TrimSpace(tok)
	if len(t) >= 2 {
		if t[0] == '"' && t[len(t)-1] == '"' {
			return strings.ReplaceAll(t[1:len(t)-1], `""`, `"`), true
		}
		if t[0] == '`' && t[len(t)-1] == '`' {
			return strings.ReplaceAll(t[1:len(t)-1], "``", "`"), true
		}
		if t[0] == '[' && t[len(t)-1] == ']' {
			return strings.ReplaceAll(t[1:len(t)-1], "]]", "]"), true
		}
		if t[0] == '\'' && t[len(t)-1] == '\'' {
			return strings.ReplaceAll(t[1:len(t)-1], "''", "'"), true
		}
	}
	return t, false
}

func nextSQLiteTopToken(s string, start int) (tok string, nextPos int) {
	n := len(s)
	i := start
	for i < n {
		ch := s[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			i++
			continue
		}
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		break
	}
	if i >= n {
		return "", n
	}
	ch := s[i]
	if ch == '(' || ch == ')' || ch == ',' || ch == '.' {
		return s[i : i+1], i + 1
	}
	if ch == '"' || ch == '`' || ch == '\'' {
		quote := ch
		j := i + 1
		for j < n {
			if s[j] == quote {
				if j+1 < n && s[j+1] == quote {
					j += 2
					continue
				}
				j++
				break
			}
			j++
		}
		return s[i:j], j
	}
	if ch == '[' {
		j := i + 1
		for j < n {
			if s[j] == ']' {
				if j+1 < n && s[j+1] == ']' {
					j += 2
					continue
				}
				j++
				break
			}
			j++
		}
		return s[i:j], j
	}
	j := i
	for j < n {
		c := s[j]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '(' || c == ')' || c == ',' || c == '.' ||
			c == '"' || c == '`' || c == '\'' || c == '[' {
			break
		}
		if c == '-' && j+1 < n && s[j+1] == '-' {
			break
		}
		if c == '/' && j+1 < n && s[j+1] == '*' {
			break
		}
		j++
	}
	return s[i:j], j
}

func extractSQLiteBalancedParen(s string, openPos int) (inner string, afterClose int, ok bool) {
	if openPos >= len(s) || s[openPos] != '(' {
		return "", openPos, false
	}
	depth := 0
	n := len(s)
	for i := openPos; i < n; i++ {
		ch := s[i]
		if ch == '\'' || ch == '"' || ch == '`' {
			q := ch
			i++
			for i < n {
				if s[i] == q {
					if i+1 < n && s[i+1] == q {
						i += 2
						continue
					}
					break
				}
				i++
			}
			continue
		}
		if ch == '[' {
			i++
			for i < n && s[i] != ']' {
				i++
			}
			continue
		}
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[openPos+1 : i]), i + 1, true
			}
		}
	}
	return "", n, false
}

func parseSQLiteVirtualModule(ddl string) (string, bool) {
	pos := 0
	var tokens []string
	for len(tokens) < 12 && pos < len(ddl) {
		tok, next := nextSQLiteTopToken(ddl, pos)
		if tok == "" {
			break
		}
		pos = next
		if tok == "(" {
			break
		}
		tokens = append(tokens, tok)
	}
	if len(tokens) < 5 {
		return "", false
	}
	if !strings.EqualFold(tokens[0], "CREATE") || !strings.EqualFold(tokens[1], "VIRTUAL") || !strings.EqualFold(tokens[2], "TABLE") {
		return "", false
	}
	for i := 3; i+1 < len(tokens); i++ {
		if strings.EqualFold(tokens[i], "USING") {
			mod, _ := unquoteSQLiteIdent(tokens[i+1])
			if mod != "" {
				return mod, true
			}
		}
	}
	return "", false
}

var sqliteShadowSuffixes = []string{
	"_data", "_idx", "_content", "_docsize", "_config",
	"_node", "_rowid", "_parent", "_segdir", "_segments", "_stat",
}

func isSQLiteVirtualShadowTable(name string, virtualLower map[string]string) bool {
	if len(virtualLower) == 0 {
		return false
	}
	lower := strings.ToLower(name)
	for _, suf := range sqliteShadowSuffixes {
		if strings.HasSuffix(lower, suf) && len(lower) > len(suf) {
			parent := lower[:len(lower)-len(suf)]
			if _, ok := virtualLower[parent]; ok {
				return true
			}
		}
	}
	return false
}

func parseSQLiteDDLMetadata(tableName, ddl string) sqliteDDLMetadata {
	meta := sqliteDDLMetadata{
		genExprs: map[string]string{},
	}
	if strings.TrimSpace(ddl) == "" {
		return meta
	}
	if mod, ok := parseSQLiteVirtualModule(ddl); ok {
		meta.isVirtual = true
		meta.virtualModule = mod
		return meta
	}

	// Find opening '(' of CREATE TABLE (...)
	pos := 0
	openParen := -1
	for pos < len(ddl) {
		tok, next := nextSQLiteTopToken(ddl, pos)
		if tok == "" {
			break
		}
		if tok == "(" {
			openParen = next - 1
			break
		}
		pos = next
	}
	if openParen < 0 {
		return meta
	}
	body, afterClose, ok := extractSQLiteBalancedParen(ddl, openParen)
	if !ok {
		return meta
	}

	// Parse trailing table options (WITHOUT ROWID, STRICT)
	tailPos := afterClose
	var tailTokens []string
	for tailPos < len(ddl) {
		tok, next := nextSQLiteTopToken(ddl, tailPos)
		if tok == "" {
			break
		}
		tailPos = next
		tailTokens = append(tailTokens, strings.ToUpper(tok))
	}
	for i := 0; i < len(tailTokens); i++ {
		if tailTokens[i] == "STRICT" {
			meta.strict = true
		} else if tailTokens[i] == "WITHOUT" && i+1 < len(tailTokens) && tailTokens[i+1] == "ROWID" {
			meta.withoutRowID = true
			i++
		}
	}

	// Split body into top-level comma-separated items
	var items []string
	itemStart := 0
	bPos := 0
	for bPos < len(body) {
		tok, next := nextSQLiteTopToken(body, bPos)
		if tok == "" {
			break
		}
		if tok == "(" {
			_, after, okParen := extractSQLiteBalancedParen(body, next-1)
			if okParen {
				bPos = after
				continue
			}
		} else if tok == "," {
			items = append(items, strings.TrimSpace(body[itemStart:next-1]))
			itemStart = next
		}
		bPos = next
	}
	if rest := strings.TrimSpace(body[itemStart:]); rest != "" {
		items = append(items, rest)
	}

	autoChkIdx := 0
	for _, item := range items {
		if item == "" {
			continue
		}
		firstTok, p1 := nextSQLiteTopToken(item, 0)
		if firstTok == "" {
			continue
		}
		firstIdent, firstWasQuoted := unquoteSQLiteIdent(firstTok)
		upperFirst := strings.ToUpper(firstTok)

		if !firstWasQuoted && (upperFirst == "CONSTRAINT" || upperFirst == "CHECK") {
			chkName := ""
			kwPos := p1
			if upperFirst == "CONSTRAINT" {
				nameTok, p2 := nextSQLiteTopToken(item, p1)
				chkName, _ = unquoteSQLiteIdent(nameTok)
				kwTok, p3 := nextSQLiteTopToken(item, p2)
				if !strings.EqualFold(kwTok, "CHECK") {
					continue
				}
				kwPos = p3
			}
			parTok, pPar := nextSQLiteTopToken(item, kwPos)
			if parTok == "(" {
				if expr, _, okExpr := extractSQLiteBalancedParen(item, pPar-1); okExpr && expr != "" {
					autoChkIdx++
					if chkName == "" {
						if tableName != "" {
							chkName = fmt.Sprintf("chk_%s_%d", tableName, autoChkIdx)
						} else {
							chkName = fmt.Sprintf("chk_%d", autoChkIdx)
						}
					}
					meta.checks = append(meta.checks, driver.CheckConstraintDef{
						Name:       chkName,
						Expression: expr,
						Enforced:   true,
					})
				}
			}
			continue
		}

		if !firstWasQuoted && (upperFirst == "PRIMARY" || upperFirst == "UNIQUE" || upperFirst == "FOREIGN") {
			continue
		}

		// Column definition: scan top-level tokens for [GENERATED ALWAYS] AS (<expr>)
		scanPos := p1
		for scanPos < len(item) {
			tok, next := nextSQLiteTopToken(item, scanPos)
			if tok == "" {
				break
			}
			if tok == "(" {
				if _, after, okParen := extractSQLiteBalancedParen(item, next-1); okParen {
					scanPos = after
					continue
				}
			}
			if strings.EqualFold(tok, "AS") {
				parTok, pPar := nextSQLiteTopToken(item, next)
				if parTok == "(" {
					if expr, _, okExpr := extractSQLiteBalancedParen(item, pPar-1); okExpr && expr != "" {
						meta.genExprs[strings.ToLower(firstIdent)] = expr
						break
					}
				}
			}
			scanPos = next
		}
	}
	return meta
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
		Triggers: []driver.ObjectItem{},
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, COALESCE(tbl_name, ''), COALESCE(sql, '') FROM sqlite_master
		WHERE type IN ('table', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return res, err
	}
	type rawObj struct {
		name    string
		typ     string
		tblName string
		ddl     string
	}
	var rawObjs []rawObj
	virtualLower := map[string]string{}
	for rows.Next() {
		var n, t, tbl, ddl string
		if rows.Scan(&n, &t, &tbl, &ddl) == nil {
			rawObjs = append(rawObjs, rawObj{name: n, typ: t, tblName: tbl, ddl: ddl})
			if t == "table" {
				if mod, ok := parseSQLiteVirtualModule(ddl); ok {
					virtualLower[strings.ToLower(n)] = mod
				}
			}
		}
	}
	_ = rows.Close()

	filtered := make([]rawObj, 0, len(rawObjs))
	tableCount := 0
	for _, item := range rawObjs {
		if item.typ == "table" {
			if isSQLiteVirtualShadowTable(item.name, virtualLower) {
				continue
			}
			tableCount++
		}
		filtered = append(filtered, item)
	}

	// Skip per-table SELECT COUNT(*) full B-Tree scans unless the SQLite database is small (<= 4 MB & <= 20 tables),
	// keeping sidebar metadata loading instantaneous on large database files.
	countRows := tableCount <= 20
	if countRows && d.filePath != ":memory:" {
		if st, statErr := os.Stat(d.filePath); statErr == nil && st.Size() > 4*1024*1024 {
			countRows = false
		}
	}

	for _, item := range filtered {
		var rCnt int64
		switch item.typ {
		case "table":
			engine := "SQLite B-Tree"
			if mod, ok := virtualLower[strings.ToLower(item.name)]; ok {
				engine = "VIRTUAL (" + mod + ")"
			}
			if countRows {
				_ = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+sqlbase.QuoteIdent(sqlbase.DialectSQLite, item.name)).Scan(&rCnt)
			}
			res.Tables = append(res.Tables, driver.ObjectItem{Name: item.name, Type: "TABLE", Engine: engine, Rows: rCnt})
		case "trigger":
			res.Triggers = append(res.Triggers, driver.ObjectItem{Name: item.name, Type: "TRIGGER", TargetName: item.tblName})
		default:
			res.Views = append(res.Views, driver.ObjectItem{Name: item.name, Type: "VIEW"})
		}
	}
	return res, nil
}

func (d *SQLiteDriver) readDiagnostics(ctx context.Context, ddlMeta sqliteDDLMetadata) map[string]string {
	diag := map[string]string{
		"strict":        strconv.FormatBool(ddlMeta.strict),
		"without_rowid": strconv.FormatBool(ddlMeta.withoutRowID),
	}
	for _, p := range []string{"journal_mode", "synchronous", "foreign_keys", "page_size", "page_count", "freelist_count"} {
		var v any
		if d.db.QueryRowContext(ctx, "PRAGMA "+p).Scan(&v) == nil && v != nil {
			diag[p] = fmt.Sprintf("%v", v)
		}
	}
	return diag
}

func (d *SQLiteDriver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	st := driver.TableStructure{
		Database:         database,
		Table:            table,
		Engine:           "SQLite B-Tree",
		Columns:          []driver.ColumnDef{},
		Indexes:          []driver.IndexDef{},
		ForeignKeys:      []driver.ForeignKeyDef{},
		CheckConstraints: []driver.CheckConstraintDef{},
	}

	_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, table).Scan(&st.DDL)
	upperDDL := strings.ToUpper(st.DDL)
	ddlMeta := parseSQLiteDDLMetadata(table, st.DDL)
	if ddlMeta.isVirtual {
		st.Engine = "VIRTUAL (" + ddlMeta.virtualModule + ")"
	}
	if len(ddlMeta.checks) > 0 {
		st.CheckConstraints = ddlMeta.checks
	}
	st.Diagnostics = d.readDiagnostics(ctx, ddlMeta)

	qTbl := sqlbase.QuoteIdent(sqlbase.DialectSQLite, table)
	cRows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_xinfo(%s)", qTbl))
	if err != nil {
		return st, err
	}
	defer cRows.Close()

	for cRows.Next() {
		var cid, notNull, pk, hidden int
		var name, cType string
		var dflt sql.NullString
		if err := cRows.Scan(&cid, &name, &cType, &notNull, &dflt, &pk, &hidden); err != nil {
			return st, err
		}
		// hidden == 1 represents internal hidden columns (e.g., rowid / FTS5 hidden rank/table column)
		if hidden == 1 {
			continue
		}
		defVal := ""
		if dflt.Valid {
			defVal = dflt.String
		}
		genStorage := ""
		genExpr := ""
		if hidden == 2 {
			genStorage = "VIRTUAL"
			genExpr = ddlMeta.genExprs[strings.ToLower(name)]
		} else if hidden == 3 {
			genStorage = "STORED"
			genExpr = ddlMeta.genExprs[strings.ToLower(name)]
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:             name,
			Type:             cType,
			Nullable:         notNull == 0 && pk == 0,
			DefaultValue:     defVal,
			IsPrimaryKey:     pk > 0,
			IsAutoIncrement:  pk > 0 && strings.Contains(upperDDL, "AUTOINCREMENT"),
			GeneratedExpr:    genExpr,
			GeneratedStorage: genStorage,
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
	if d.webMode && strings.TrimSpace(req.Filter) != "" {
		if sbErr := validateSQLiteWebSandbox(dataSQL); sbErr != nil {
			return driver.QueryResult{}, sbErr
		}
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

func buildSQLiteVirtualTableDDL(req driver.CreateTableRequest) (string, error) {
	if strings.TrimSpace(req.Table) == "" {
		return "", errors.New("table name cannot be empty")
	}
	mod := strings.ToLower(strings.TrimSpace(req.VirtualModule))
	switch mod {
	case "fts5", "fts4", "rtree":
	default:
		return "", fmt.Errorf("unsupported or disallowed SQLite virtual table module %q (allowed: fts5, fts4, rtree)", req.VirtualModule)
	}

	var parts []string
	for _, col := range req.Columns {
		name := strings.TrimSpace(col.Name)
		if name == "" {
			continue
		}
		isSimpleIdent := true
		for i := 0; i < len(name); i++ {
			ch := name[i]
			if i == 0 {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_') {
					isSimpleIdent = false
					break
				}
			} else if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
				isSimpleIdent = false
				break
			}
		}
		if isSimpleIdent {
			parts = append(parts, name)
		} else {
			parts = append(parts, sqlbase.QuoteIdent(sqlbase.DialectSQLite, name))
		}
	}
	if vArgs := strings.TrimSpace(req.VirtualArgs); vArgs != "" {
		if err := sqlbase.ValidateSQLExpression("virtual table arguments", vArgs); err != nil {
			return "", err
		}
		parts = append(parts, vArgs)
	}
	if len(parts) == 0 {
		return "", errors.New("virtual table requires at least one column or module argument")
	}
	return fmt.Sprintf("CREATE VIRTUAL TABLE %s USING %s(%s)",
		sqlbase.QuoteIdent(sqlbase.DialectSQLite, req.Table), mod, strings.Join(parts, ", ")), nil
}

func buildSQLitePragmaStmt(key, val string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	v := strings.ToUpper(strings.TrimSpace(val))
	switch k {
	case "synchronous":
		switch v {
		case "0", "1", "2", "3", "OFF", "NORMAL", "FULL", "EXTRA":
			return fmt.Sprintf("PRAGMA synchronous = %s", v), nil
		default:
			return "", fmt.Errorf("invalid PRAGMA synchronous value %q", val)
		}
	case "foreign_keys":
		switch v {
		case "0", "1", "ON", "OFF", "TRUE", "FALSE":
			return fmt.Sprintf("PRAGMA foreign_keys = %s", v), nil
		default:
			return "", fmt.Errorf("invalid PRAGMA foreign_keys value %q", val)
		}
	case "journal_mode":
		switch v {
		case "DELETE", "TRUNCATE", "PERSIST", "MEMORY", "WAL", "OFF":
			return fmt.Sprintf("PRAGMA journal_mode = %s", v), nil
		default:
			return "", fmt.Errorf("invalid PRAGMA journal_mode value %q", val)
		}
	case "auto_vacuum":
		switch v {
		case "0", "1", "2", "NONE", "FULL", "INCREMENTAL":
			return fmt.Sprintf("PRAGMA auto_vacuum = %s", v), nil
		default:
			return "", fmt.Errorf("invalid PRAGMA auto_vacuum value %q", val)
		}
	default:
		return "", fmt.Errorf("disallowed or unsupported SQLite PRAGMA %q (allowed: synchronous, foreign_keys, journal_mode, auto_vacuum)", key)
	}
}

func (d *SQLiteDriver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	if strings.TrimSpace(req.VirtualModule) != "" {
		ddl, err := buildSQLiteVirtualTableDDL(req)
		if err != nil || req.PreviewOnly {
			return ddl, err
		}
		if d.webMode {
			if sbErr := validateSQLiteWebSandbox(ddl); sbErr != nil {
				return ddl, sbErr
			}
		}
		_, err = d.db.ExecContext(ctx, ddl)
		return ddl, err
	}
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
	if d.webMode {
		if sbErr := validateSQLiteWebSandbox(fullScript); sbErr != nil {
			return fullScript, sbErr
		}
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
	var pragmaStmts []string
	var tableActions []driver.AlterAction
	for _, act := range req.Actions {
		if strings.EqualFold(strings.TrimSpace(act.Action), "SET_PRAGMA") {
			pStmt, err := buildSQLitePragmaStmt(act.PragmaKey, act.PragmaValue)
			if err != nil {
				return nil, err
			}
			pragmaStmts = append(pragmaStmts, pStmt)
		} else {
			tableActions = append(tableActions, act)
		}
	}

	if len(tableActions) == 0 {
		if req.PreviewOnly {
			return pragmaStmts, nil
		}
		for _, ps := range pragmaStmts {
			if _, err := d.db.ExecContext(ctx, ps); err != nil {
				return pragmaStmts, err
			}
		}
		return pragmaStmts, nil
	}

	// Block schema-altering column/constraint operations on Virtual Tables (except RENAME_TABLE)
	if strings.TrimSpace(req.Table) != "" {
		var rawDDL string
		_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, req.Table).Scan(&rawDDL)
		if mod, isVirt := parseSQLiteVirtualModule(rawDDL); isVirt {
			for _, act := range tableActions {
				if !strings.EqualFold(strings.TrimSpace(act.Action), "RENAME_TABLE") {
					return nil, fmt.Errorf("SQLite virtual table %q (VIRTUAL %s) does not support column or constraint alterations; drop and recreate the virtual table instead", req.Table, mod)
				}
			}
		}
	}

	subReq := req
	subReq.Actions = tableActions

	var stmts []string
	var err error
	if d.needsTableRebuild(ctx, subReq) {
		stmts, err = d.alterTableWithRebuild(ctx, subReq)
		if err != nil {
			return nil, err
		}
		if req.PreviewOnly {
			return append(pragmaStmts, stmts...), nil
		}
		for _, ps := range pragmaStmts {
			if _, pErr := d.db.ExecContext(ctx, ps); pErr != nil {
				return append(pragmaStmts, stmts...), pErr
			}
		}
		return append(pragmaStmts, stmts...), nil
	}

	stmts, err = sqlbase.BuildAlterTableSQLs(sqlbase.DialectSQLite, subReq)
	if err != nil {
		return nil, err
	}
	if d.webMode {
		for _, s := range stmts {
			if sbErr := validateSQLiteWebSandbox(s); sbErr != nil {
				return nil, sbErr
			}
		}
	}
	if req.PreviewOnly {
		return append(pragmaStmts, stmts...), nil
	}
	for _, ps := range pragmaStmts {
		if _, pErr := d.db.ExecContext(ctx, ps); pErr != nil {
			return append(pragmaStmts, stmts...), pErr
		}
	}
	for _, s := range stmts {
		if _, execErr := d.db.ExecContext(ctx, s); execErr != nil {
			// Fallback to safe transactional table rebuild when SQLite rejects native ALTER
			rbStmts, rbErr := d.alterTableWithRebuild(ctx, subReq)
			return append(pragmaStmts, rbStmts...), rbErr
		}
	}
	return append(pragmaStmts, stmts...), nil
}

func (d *SQLiteDriver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectSQLite, req)
	if err != nil {
		return err
	}
	for _, s := range sqls {
		if d.webMode {
			if err := validateSQLiteWebSandbox(s); err != nil {
				return err
			}
		}
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
		start := time.Now()
		res := driver.ImportResult{}
		conn, connErr := d.db.Conn(ctx)
		if connErr != nil {
			return res, connErr
		}
		defer conn.Close()

		canAutoBatch := !req.ContinueOnError
		userInTx := false
		var autoTx *sql.Tx
		autoTxCount := 0

		commitAutoTx := func() error {
			if autoTx == nil {
				return nil
			}
			cErr := autoTx.Commit()
			autoTx = nil
			autoTxCount = 0
			return cErr
		}
		defer func() {
			if autoTx != nil {
				_ = autoTx.Rollback()
			}
		}()

		err := sqlbase.StreamStatements(req.Reader, func(stmt string) error {
			if cErr := ctx.Err(); cErr != nil {
				return cErr
			}
			if sbErr := validateSQLiteWebSandbox(stmt); sbErr != nil {
				return sbErr
			}
			trimmed := strings.TrimSpace(sqlbase.StripLeadingSQLComments(stmt))
			upperKw := strings.ToUpper(trimmed)
			for i, r := range upperKw {
				if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '(' {
					upperKw = upperKw[:i]
					break
				}
			}

			if canAutoBatch {
				switch upperKw {
				case "BEGIN", "START":
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
					userInTx = true
				case "COMMIT", "END", "ROLLBACK":
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
					userInTx = false
				}

				isBatchable := upperKw == "INSERT" || upperKw == "UPDATE" || upperKw == "DELETE" || upperKw == "REPLACE" ||
					upperKw == "CREATE" || upperKw == "DROP" || upperKw == "ALTER"
				if !userInTx && isBatchable {
					if autoTx == nil {
						if tx, txErr := conn.BeginTx(ctx, nil); txErr == nil {
							autoTx = tx
							autoTxCount = 0
						}
					}
					if autoTx != nil {
						if _, execErr := autoTx.ExecContext(ctx, stmt); execErr != nil {
							_ = autoTx.Rollback()
							autoTx = nil
							autoTxCount = 0
							return fmt.Errorf("SQL import execution failed: %w", execErr)
						}
						res.ImportedRows++
						autoTxCount++
						if autoTxCount >= 500 {
							if cErr := commitAutoTx(); cErr != nil {
								return cErr
							}
						}
						return nil
					}
				} else if !userInTx && autoTx != nil {
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
				}
			}

			if _, execErr := conn.ExecContext(ctx, stmt); execErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, execErr.Error())
					}
					return nil
				}
				return fmt.Errorf("SQL import execution failed: %w", execErr)
			}
			res.ImportedRows++
			return nil
		})
		if err == nil {
			err = commitAutoTx()
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err
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

func (d *SQLiteDriver) ExportDatabaseFile(ctx context.Context, destPath string) error {
	cleanDest := filepath.Clean(strings.TrimSpace(destPath))
	if cleanDest == "" || cleanDest == "." || cleanDest == ":memory:" {
		return errors.New("invalid snapshot destination path")
	}
	_ = os.Remove(cleanDest)
	escapedDest := strings.ReplaceAll(filepath.ToSlash(cleanDest), "'", "''")
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escapedDest)); err != nil {
		_ = os.Remove(cleanDest)
		return fmt.Errorf("failed to export SQLite snapshot: %w", err)
	}
	_ = os.Chmod(cleanDest, 0o600)
	return nil
}

