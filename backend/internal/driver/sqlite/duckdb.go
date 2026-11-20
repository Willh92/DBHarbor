package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:            "duckdb",
		Name:            "DuckDB",
		Category:        driver.CategoryOLAP,
		BadgeColor:      "#d4b106",
		DefaultPort:     0,
		Description:     "Cross-platform in-process OLAP analytical database (supports .duckdb files and direct SQL queries over Parquet/CSV)",
		SupportsSchemas: true,
		SupportsExplain: true,
		DefaultTypes:    []string{"BIGINT", "INTEGER", "VARCHAR", "DOUBLE", "DECIMAL(18,2)", "BOOLEAN", "TIMESTAMP", "DATE", "JSON"},
		Fields: []driver.ConnFieldSchema{
			{Key: "filePath", Label: "Database File Path", Type: "text", Target: "root", Placeholder: "./data/analytics.duckdb", Required: true},
			{Key: "cliPath", Label: "DuckDB CLI Path (optional, auto-detected from PATH if empty)", Type: "text", Target: "options", Placeholder: "duckdb"},
		},
	}, NewDuckDB)
}

var _ driver.SchemaManager = (*DuckDBDriver)(nil)
var _ driver.FileExporter = (*DuckDBDriver)(nil)

type DuckDBDriver struct {
	cfg      driver.ConnectionConfig
	filePath string
	tempDir  string
	cliBin   string
	webMode  bool
}

const maxDuckDBJSONBytes = 32 << 20 // 32 MiB stdout safety ceiling for interactive JSON queries

var verifiedDuckDBBinaries sync.Map // map[string]bool
var duckFileLocks sync.Map          // map[string]*sync.Mutex

func canonicalDuckDBLockKey(filePath string) string {
	fp := strings.TrimSpace(filePath)
	if fp == "" || fp == ":memory:" {
		return ""
	}
	abs, err := filepath.Abs(fp)
	if err != nil {
		abs = filepath.Clean(fp)
	} else {
		abs = filepath.Clean(abs)
	}
	if realPath, err := filepath.EvalSymlinks(abs); err == nil && realPath != "" {
		abs = filepath.Clean(realPath)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

func duckFileLock(filePath string) *sync.Mutex {
	key := canonicalDuckDBLockKey(filePath)
	if key == "" {
		return nil
	}
	if v, ok := duckFileLocks.Load(key); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := duckFileLocks.LoadOrStore(key, mu)
	return actual.(*sync.Mutex)
}

// ReleaseDuckDBFileLock removes the per-file mutex entry from duckFileLocks when an ephemeral file is cleaned up.
func ReleaseDuckDBFileLock(filePath string) {
	key := canonicalDuckDBLockKey(filePath)
	if key != "" {
		duckFileLocks.Delete(key)
	}
}

func resolveDuckDBBinary(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	binName := "duckdb"
	if runtime.GOOS == "windows" {
		binName = "duckdb.exe"
	}
	if p, err := exec.LookPath(binName); err == nil {
		return p, nil
	}
	localCandidate := filepath.Join("drivers", binName)
	if _, err := os.Stat(localCandidate); err == nil {
		return localCandidate, nil
	}
	return binName, nil
}

func verifyDuckDBExecutable(binPath string) error {
	if _, ok := verifiedDuckDBBinaries.Load(binPath); ok {
		return nil
	}
	resolved := binPath
	if p, err := exec.LookPath(binPath); err == nil {
		resolved = p
	} else if _, statErr := os.Stat(binPath); statErr != nil {
		return fmt.Errorf("duckdb executable (%s) not found; please install DuckDB CLI into system PATH or configure security.duckdbCliPath in config.json", binPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolved, "--version").CombinedOutput()
	if err != nil || !strings.Contains(strings.ToLower(string(out)), "duckdb") {
		return fmt.Errorf("configured binary %q failed DuckDB CLI verification (--version)", binPath)
	}
	verifiedDuckDBBinaries.Store(binPath, true)
	return nil
}

func NewDuckDB(cfg driver.ConnectionConfig) (driver.Driver, error) {
	fp := strings.TrimSpace(cfg.FilePath)
	if fp == "" {
		return nil, errors.New("DuckDB database file path cannot be empty")
	}
	if fp != ":memory:" && hasInvalidLocalDBPathChars(fp) {
		return nil, errors.New("invalid DuckDB file path: query parameters, fragments, and control characters are not allowed")
	}
	webMode := cfg.Option("__web_mode", "") == "1"
	if webMode && strings.Contains(fp, ":") {
		if !(runtime.GOOS == "windows" && len(fp) >= 3 && ((fp[0] >= 'a' && fp[0] <= 'z') || (fp[0] >= 'A' && fp[0] <= 'Z')) && fp[1] == ':' && (fp[2] == '\\' || fp[2] == '/') && !strings.Contains(fp[2:], ":")) && fp != ":memory:" {
			return nil, errors.New("invalid DuckDB file path syntax")
		}
	}
	var tempDir string
	if fp == ":memory:" {
		td, err := os.MkdirTemp("", "dbharbor-duckdb-mem-*")
		if err != nil {
			return nil, fmt.Errorf("failed to allocate ephemeral DuckDB directory: %w", err)
		}
		tempDir = td
		fp = filepath.Join(td, "memory.duckdb")
	} else {
		fp = filepath.Clean(fp)
		if dir := filepath.Dir(fp); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
	cliOption := cfg.Option("cliPath", "")
	if webMode {
		// In Web Server Edition, never execute client-supplied cliPath; use server-configured duckdbCliPath or system PATH only
		cliOption = cfg.Option("__server_duckdb_cli", "")
	}
	bin, _ := resolveDuckDBBinary(cliOption)
	return &DuckDBDriver{cfg: cfg, filePath: fp, tempDir: tempDir, cliBin: bin, webMode: webMode}, nil
}

func (d *DuckDBDriver) buildDuckDBArgs() []string {
	dbArg := d.filePath
	if dbArg != "" && strings.HasPrefix(dbArg, "-") {
		dbArg = "./" + dbArg
	}
	if d.webMode {
		return []string{
			"-safe",
			"-cmd", "SET enable_external_access=false; SET autoinstall_known_extensions=false; SET autoload_known_extensions=false; SET lock_configuration=true;",
			"-json",
			dbArg,
		}
	}
	return []string{"-json", dbArg}
}

type duckLexToken struct {
	kind  string // "word", "ident", "string", "punct"
	upper string
}

func matchDuckDollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	if s[1] == '$' {
		return "$$", true
	}
	if !((s[1] >= 'a' && s[1] <= 'z') || (s[1] >= 'A' && s[1] <= 'Z') || s[1] == '_' || s[1] >= 0x80) {
		return "", false
	}
	for i := 2; i < len(s); i++ {
		ch := s[i]
		if ch == '$' {
			return s[:i+1], true
		}
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch >= 0x80) {
			return "", false
		}
	}
	return "", false
}

func lexDuckDBTokens(s string) []duckLexToken {
	var tokens []duckLexToken
	n := len(s)
	i := 0
	prevIsIdentChar := false
	isIdentByte := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$' || b >= 0x80
	}
	for i < n {
		ch := s[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			prevIsIdentChar = false
			i++
			continue
		}
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			prevIsIdentChar = false
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if i+1 < n && s[i] == '/' && s[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if i+1 < n && s[i] == '*' && s[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				i++
			}
			prevIsIdentChar = false
			continue
		}
		if ch == '\'' || (!prevIsIdentChar && (ch == 'E' || ch == 'e') && i+1 < n && s[i+1] == '\'') {
			escMode := false
			j := i + 1
			if ch != '\'' {
				escMode = true
				j = i + 2
			}
			for j < n {
				if escMode && s[j] == '\\' {
					if j+1 < n {
						j += 2
					} else {
						j++
					}
					continue
				}
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
			tokens = append(tokens, duckLexToken{kind: "string"})
			prevIsIdentChar = false
			i = j
			continue
		}
		if !prevIsIdentChar && ch == '$' {
			if tag, ok := matchDuckDollarTag(s[i:]); ok {
				endIdx := strings.Index(s[i+len(tag):], tag)
				if endIdx < 0 {
					tokens = append(tokens, duckLexToken{kind: "string"})
					break
				}
				tokens = append(tokens, duckLexToken{kind: "string"})
				prevIsIdentChar = false
				i = i + len(tag) + endIdx + len(tag)
				continue
			}
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
			tokens = append(tokens, duckLexToken{kind: "ident", upper: strings.ToUpper(strings.TrimSpace(b.String()))})
			prevIsIdentChar = false
			i = j
			continue
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch >= 0x80 {
			j := i + 1
			for j < n {
				c := s[j]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c >= 0x80 {
					j++
				} else {
					break
				}
			}
			tokens = append(tokens, duckLexToken{kind: "word", upper: strings.ToUpper(s[i:j])})
			prevIsIdentChar = true
			i = j
			continue
		}
		tokens = append(tokens, duckLexToken{kind: "punct", upper: s[i : i+1]})
		prevIsIdentChar = isIdentByte(ch)
		i++
	}
	return tokens
}

func validateDuckDBWebSandbox(script string) error {
	return sqlbase.ValidateDuckDBRestrictedWebSQL(script)
}

func formatDuckDBStdinSQL(sqlStmt string) string {
	trimmed := strings.TrimSpace(sqlStmt)
	if trimmed == "" {
		return ""
	}
	tokens := lexDuckDBTokens(trimmed)
	if len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		if !(last.kind == "punct" && last.upper == ";") {
			return sqlStmt + "\n;\n"
		}
	}
	return sqlStmt + "\n"
}

func (d *DuckDBDriver) runJSON(ctx context.Context, sqlStmt string) ([]map[string]any, error) {
	sets, err := d.runJSONMulti(ctx, sqlStmt)
	if err != nil || len(sets) == 0 {
		return []map[string]any{}, err
	}
	return sets[0], nil
}

func (d *DuckDBDriver) runJSONMulti(ctx context.Context, sqlStmt string) ([][]map[string]any, error) {
	if mu := duckFileLock(d.filePath); mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	return d.runJSONMultiUnlocked(ctx, sqlStmt)
}

func (d *DuckDBDriver) runJSONMultiUnlocked(ctx context.Context, sqlStmt string) ([][]map[string]any, error) {
	if d.webMode || sqlbase.IsRestrictedWebContext(ctx) {
		if err := verifyDuckDBExecutable(d.cliBin); err != nil {
			return nil, err
		}
	} else {
		if _, err := exec.LookPath(d.cliBin); err != nil {
			if _, statErr := os.Stat(d.cliBin); statErr != nil {
				return nil, fmt.Errorf("duckdb executable (%s) not found; please install DuckDB CLI into system PATH or specify cliPath in connection options", d.cliBin)
			}
		}
	}
	cmd := exec.CommandContext(ctx, d.cliBin, d.buildDuckDBArgs()...)
	cmd.Stdin = strings.NewReader(formatDuckDBStdinSQL(sqlStmt))
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdoutPipe, maxDuckDBJSONBytes+1))
	if readErr != nil || len(raw) > maxDuckDBJSONBytes {
		_ = stdoutPipe.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("DuckDB query output exceeded %d MB memory safety limit; please add a LIMIT clause or use streaming export", maxDuckDBJSONBytes>>20)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return nil, errors.New(msg)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return [][]map[string]any{}, nil
	}
	var sets [][]map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	for dec.More() {
		var records []map[string]any
		if err := dec.Decode(&records); err != nil {
			return nil, err
		}
		sets = append(sets, records)
	}
	return sets, nil
}

func (d *DuckDBDriver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("duckdb")
	return m
}

func (d *DuckDBDriver) Close() error {
	if d.tempDir != "" {
		ReleaseDuckDBFileLock(d.filePath)
		_ = os.RemoveAll(d.tempDir)
	}
	return nil
}

func (d *DuckDBDriver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	recs, err := d.runJSON(ctx, "SELECT version() AS ver")
	if err != nil {
		return driver.ServerInfo{}, err
	}
	ver := "DuckDB"
	if len(recs) > 0 {
		ver = fmt.Sprintf("%v", recs[0]["ver"])
	}
	return driver.ServerInfo{Version: ver, Product: "DuckDB", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *DuckDBDriver) ListDatabases(_ context.Context) ([]driver.DatabaseSummary, error) {
	var sz int64
	if st, err := os.Stat(d.filePath); err == nil {
		sz = st.Size()
	}
	return []driver.DatabaseSummary{{Name: "main", SizeBytes: sz}}, nil
}

func (d *DuckDBDriver) CreateDatabase(_ context.Context, _, _ string) error {
	return errors.New("DuckDB single-file connections do not require CREATE DATABASE")
}

func normalizeDuckSchema(schema string) string {
	s := strings.TrimSpace(schema)
	if s == "" {
		return "main"
	}
	return s
}

func parseDuckDBBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case json.Number:
		n, _ := b.Int64()
		return n != 0
	default:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
		return s == "true" || s == "1" || s == "t" || s == "yes"
	}
}

func parseDuckDBStringList(v any) []string {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		out := make([]string, 0, len(arr))
		for _, elem := range arr {
			if elem == nil {
				continue
			}
			name, _ := unquoteSQLiteIdent(fmt.Sprintf("%v", elem))
			if name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return nil
		}
		parts := strings.Split(inner, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			name, _ := unquoteSQLiteIdent(p)
			if name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

func extractDuckCheckExpr(text string) string {
	t := strings.TrimSpace(text)
	upper := strings.ToUpper(t)
	if strings.HasPrefix(upper, "CHECK") {
		rest := strings.TrimSpace(t[len("CHECK"):])
		if len(rest) >= 2 && rest[0] == '(' {
			if inner, _, ok := extractSQLiteBalancedParen(rest, 0); ok {
				return inner
			}
		}
	}
	return t
}

func parseDuckForeignKeyText(name string, localCols []string, text string) (driver.ForeignKeyDef, bool) {
	fk := driver.ForeignKeyDef{
		Name:    name,
		Columns: localCols,
	}
	pos := 0
	for pos < len(text) {
		tok, next := nextSQLiteTopToken(text, pos)
		if tok == "" {
			break
		}
		pos = next
		if strings.EqualFold(tok, "REFERENCES") {
			refTok, p2 := nextSQLiteTopToken(text, pos)
			if refTok == "" {
				break
			}
			refIdent, _ := unquoteSQLiteIdent(refTok)
			pos = p2
			dotTok, p3 := nextSQLiteTopToken(text, pos)
			if dotTok == "." {
				tblTok, p4 := nextSQLiteTopToken(text, p3)
				tblIdent, _ := unquoteSQLiteIdent(tblTok)
				if tblIdent != "" {
					refIdent = tblIdent
				}
				pos = p4
			}
			fk.RefTable = refIdent
			parTok, pPar := nextSQLiteTopToken(text, pos)
			if parTok == "(" {
				if inner, after, ok := extractSQLiteBalancedParen(text, pPar-1); ok {
					for _, rawCol := range strings.Split(inner, ",") {
						colName, _ := unquoteSQLiteIdent(rawCol)
						if colName != "" {
							fk.RefColumns = append(fk.RefColumns, colName)
						}
					}
					pos = after
				}
			}
		} else if strings.EqualFold(tok, "ON") {
			evTok, p2 := nextSQLiteTopToken(text, pos)
			actTok1, p3 := nextSQLiteTopToken(text, p2)
			action := strings.ToUpper(actTok1)
			pos = p3
			if action == "SET" || action == "NO" {
				actTok2, p4 := nextSQLiteTopToken(text, pos)
				if actTok2 != "" {
					action = action + " " + strings.ToUpper(actTok2)
					pos = p4
				}
			}
			if strings.EqualFold(evTok, "DELETE") {
				fk.OnDelete = action
			} else if strings.EqualFold(evTok, "UPDATE") {
				fk.OnUpdate = action
			}
		}
	}
	if fk.RefTable == "" || len(fk.Columns) == 0 || len(fk.RefColumns) == 0 {
		return fk, false
	}
	return fk, true
}

func (d *DuckDBDriver) ListSchemas(ctx context.Context, _ string) ([]string, error) {
	recs, err := d.runJSON(ctx, `
		SELECT DISTINCT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'pg_catalog')
		  AND schema_name NOT LIKE 'pg_temp%'
		ORDER BY CASE WHEN schema_name = 'main' THEN 0 ELSE 1 END, schema_name
	`)
	if err != nil {
		return []string{"main"}, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range recs {
		name := strings.TrimSpace(fmt.Sprintf("%v", r["schema_name"]))
		if name != "" && name != "<nil>" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{"main"}, nil
	}
	return out, nil
}

func (d *DuckDBDriver) CreateSchema(ctx context.Context, _, schema string) error {
	sc := strings.TrimSpace(schema)
	if sc == "" {
		return errors.New("schema name cannot be empty")
	}
	stmt := "CREATE SCHEMA " + sqlbase.QuoteIdent(sqlbase.DialectPostgres, sc)
	_, err := d.runJSON(ctx, stmt)
	return err
}

func (d *DuckDBDriver) DropSchema(ctx context.Context, _, schema string, cascade bool) error {
	sc := strings.TrimSpace(schema)
	lower := strings.ToLower(sc)
	if lower == "" || lower == "main" || lower == "information_schema" || lower == "pg_catalog" || lower == "system" || lower == "temp" {
		return fmt.Errorf("system schema %q cannot be dropped", schema)
	}
	mode := " RESTRICT"
	if cascade {
		mode = " CASCADE"
	}
	stmt := "DROP SCHEMA " + sqlbase.QuoteIdent(sqlbase.DialectPostgres, sc) + mode
	_, err := d.runJSON(ctx, stmt)
	return err
}

func (d *DuckDBDriver) ListObjects(ctx context.Context, _, schema string) (driver.SchemaObjects, error) {
	sc := normalizeDuckSchema(schema)
	scLit := sqlbase.FormatLiteral(sc)
	res := driver.SchemaObjects{
		Database: "main",
		Schema:   sc,
		Tables:   []driver.ObjectItem{},
		Views:    []driver.ObjectItem{},
		Routines: []driver.ObjectItem{},
	}
	tRecs, err := d.runJSON(ctx, fmt.Sprintf(
		"SELECT table_name, estimated_size, COALESCE(comment, '') AS comment FROM duckdb_tables() WHERE internal = false AND schema_name = %s ORDER BY table_name",
		scLit,
	))
	if err != nil {
		return res, err
	}
	for _, r := range tRecs {
		var rows int64
		if num, ok := r["estimated_size"].(json.Number); ok {
			rows, _ = num.Int64()
		}
		cmt := ""
		if r["comment"] != nil {
			cmt = fmt.Sprintf("%v", r["comment"])
		}
		res.Tables = append(res.Tables, driver.ObjectItem{
			Name:    fmt.Sprintf("%v", r["table_name"]),
			Type:    "TABLE",
			Engine:  "DuckDB Columnar",
			Rows:    rows,
			Comment: cmt,
		})
	}
	vRecs, _ := d.runJSON(ctx, fmt.Sprintf(
		"SELECT view_name, COALESCE(comment, '') AS comment FROM duckdb_views() WHERE internal = false AND schema_name = %s ORDER BY view_name",
		scLit,
	))
	for _, r := range vRecs {
		cmt := ""
		if r["comment"] != nil {
			cmt = fmt.Sprintf("%v", r["comment"])
		}
		res.Views = append(res.Views, driver.ObjectItem{
			Name:    fmt.Sprintf("%v", r["view_name"]),
			Type:    "VIEW",
			Comment: cmt,
		})
	}
	return res, nil
}

func (d *DuckDBDriver) GetTableStructure(ctx context.Context, _, schema, table string) (driver.TableStructure, error) {
	sc := normalizeDuckSchema(schema)
	scLit := sqlbase.FormatLiteral(sc)
	tblLit := sqlbase.FormatLiteral(table)
	qTbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, table)

	st := driver.TableStructure{
		Database:         "main",
		Schema:           sc,
		Table:            table,
		Engine:           "DuckDB Columnar",
		Columns:          []driver.ColumnDef{},
		Indexes:          []driver.IndexDef{},
		ForeignKeys:      []driver.ForeignKeyDef{},
		CheckConstraints: []driver.CheckConstraintDef{},
	}

	recs, err := d.runJSON(ctx, fmt.Sprintf("DESCRIBE %s", qTbl))
	if err != nil {
		return st, err
	}
	for _, r := range recs {
		defVal := ""
		if r["default"] != nil {
			defVal = fmt.Sprintf("%v", r["default"])
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:         fmt.Sprintf("%v", r["column_name"]),
			Type:         fmt.Sprintf("%v", r["column_type"]),
			Nullable:     fmt.Sprintf("%v", r["null"]) == "YES",
			DefaultValue: defVal,
			IsPrimaryKey: fmt.Sprintf("%v", r["key"]) == "PRI",
		})
	}

	// Column comments from duckdb_columns()
	if colMeta, cErr := d.runJSON(ctx, fmt.Sprintf(
		"SELECT column_name, COALESCE(comment, '') AS comment FROM duckdb_columns() WHERE internal = false AND schema_name = %s AND table_name = %s",
		scLit, tblLit,
	)); cErr == nil {
		cmtMap := map[string]string{}
		for _, r := range colMeta {
			if r["comment"] != nil {
				c := strings.TrimSpace(fmt.Sprintf("%v", r["comment"]))
				if c != "" {
					cmtMap[fmt.Sprintf("%v", r["column_name"])] = c
				}
			}
		}
		for i := range st.Columns {
			if c, ok := cmtMap[st.Columns[i].Name]; ok {
				st.Columns[i].Comment = c
			}
		}
	}

	// Exact DDL and Table Comment from duckdb_tables() (fallback duckdb_views())
	if tMeta, tErr := d.runJSON(ctx, fmt.Sprintf(
		"SELECT COALESCE(sql, '') AS ddl, COALESCE(comment, '') AS comment FROM duckdb_tables() WHERE internal = false AND schema_name = %s AND table_name = %s",
		scLit, tblLit,
	)); tErr == nil && len(tMeta) > 0 {
		if tMeta[0]["ddl"] != nil {
			st.DDL = strings.TrimSpace(fmt.Sprintf("%v", tMeta[0]["ddl"]))
		}
		if tMeta[0]["comment"] != nil {
			st.Comment = strings.TrimSpace(fmt.Sprintf("%v", tMeta[0]["comment"]))
		}
	} else if vMeta, vErr := d.runJSON(ctx, fmt.Sprintf(
		"SELECT COALESCE(sql, '') AS ddl, COALESCE(comment, '') AS comment FROM duckdb_views() WHERE internal = false AND schema_name = %s AND view_name = %s",
		scLit, tblLit,
	)); vErr == nil && len(vMeta) > 0 {
		if vMeta[0]["ddl"] != nil {
			st.DDL = strings.TrimSpace(fmt.Sprintf("%v", vMeta[0]["ddl"]))
		}
		if vMeta[0]["comment"] != nil {
			st.Comment = strings.TrimSpace(fmt.Sprintf("%v", vMeta[0]["comment"]))
		}
	}

	// Secondary Indexes from duckdb_indexes()
	if idxRecs, iErr := d.runJSON(ctx, fmt.Sprintf(
		"SELECT index_name, is_unique, is_primary, expressions, COALESCE(sql, '') AS sql FROM duckdb_indexes() WHERE schema_name = %s AND table_name = %s ORDER BY index_name",
		scLit, tblLit,
	)); iErr == nil {
		for _, r := range idxRecs {
			iName := fmt.Sprintf("%v", r["index_name"])
			cols := parseDuckDBStringList(r["expressions"])
			if len(cols) == 0 && r["sql"] != nil {
				idxSQL := fmt.Sprintf("%v", r["sql"])
				if openIdx := strings.LastIndex(idxSQL, "("); openIdx >= 0 {
					if inner, _, ok := extractSQLiteBalancedParen(idxSQL, openIdx); ok {
						for _, rawCol := range strings.Split(inner, ",") {
							cName, _ := unquoteSQLiteIdent(rawCol)
							if cName != "" {
								cols = append(cols, cName)
							}
						}
					}
				}
			}
			st.Indexes = append(st.Indexes, driver.IndexDef{
				Name:      iName,
				Columns:   cols,
				Unique:    parseDuckDBBool(r["is_unique"]),
				Primary:   parseDuckDBBool(r["is_primary"]),
				IndexType: "ART",
			})
		}
	}

	// Primary Keys, Foreign Keys, and Check Constraints from duckdb_constraints()
	if conRecs, conErr := d.runJSON(ctx, fmt.Sprintf(
		"SELECT constraint_index, constraint_type, COALESCE(constraint_text, '') AS constraint_text, constraint_column_names FROM duckdb_constraints() WHERE schema_name = %s AND table_name = %s ORDER BY constraint_index",
		scLit, tblLit,
	)); conErr == nil {
		for _, r := range conRecs {
			cType := strings.ToUpper(strings.TrimSpace(fmt.Sprintf("%v", r["constraint_type"])))
			cText := strings.TrimSpace(fmt.Sprintf("%v", r["constraint_text"]))
			cCols := parseDuckDBStringList(r["constraint_column_names"])
			cIdx := fmt.Sprintf("%v", r["constraint_index"])

			switch cType {
			case "PRIMARY KEY":
				pkMap := map[string]bool{}
				for _, pk := range cCols {
					pkMap[pk] = true
				}
				for i := range st.Columns {
					if pkMap[st.Columns[i].Name] {
						st.Columns[i].IsPrimaryKey = true
						st.Columns[i].Nullable = false
					}
				}
			case "CHECK":
				expr := extractDuckCheckExpr(cText)
				if expr != "" {
					st.CheckConstraints = append(st.CheckConstraints, driver.CheckConstraintDef{
						Name:       fmt.Sprintf("chk_%s_%s", table, cIdx),
						Expression: expr,
						Enforced:   true,
					})
				}
			case "FOREIGN KEY":
				fkName := fmt.Sprintf("fk_%s_%s", table, cIdx)
				if fk, ok := parseDuckForeignKeyText(fkName, cCols, cText); ok {
					st.ForeignKeys = append(st.ForeignKeys, fk)
				}
			}
		}
	}

	if st.DDL == "" && len(st.Columns) > 0 {
		st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, driver.CreateTableRequest{
			Schema:           sc,
			Table:            table,
			Columns:          st.Columns,
			ForeignKeys:      st.ForeignKeys,
			CheckConstraints: st.CheckConstraints,
		})
	}
	return st, nil
}

func (d *DuckDBDriver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	req.Schema = normalizeDuckSchema(req.Schema)
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectPostgres, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	resList, err := d.ExecuteSQL(ctx, req.Database, dataSQL, req.PageSize)
	if err != nil || len(resList) == 0 {
		return driver.QueryResult{}, err
	}
	res := resList[0]
	if cntRecs, cErr := d.runJSON(ctx, countSQL); cErr == nil && len(cntRecs) > 0 {
		for _, v := range cntRecs[0] {
			if n, ok := v.(json.Number); ok {
				res.Total, _ = n.Int64()
				res.HasTotal = true
			}
		}
	}
	if st, sErr := d.GetTableStructure(ctx, req.Database, req.Schema, req.Table); sErr == nil {
		for _, c := range st.Columns {
			if c.IsPrimaryKey {
				res.PrimaryKeys = append(res.PrimaryKeys, c.Name)
			}
		}
	}
	return res, nil
}

func (d *DuckDBDriver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	req.Schema = normalizeDuckSchema(req.Schema)
	return sqlbase.BuildMutationSQLs(sqlbase.DialectPostgres, req)
}

func (d *DuckDBDriver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	sqls, err := d.BuildMutationPreviewSQL(req)
	if err != nil {
		return driver.MutationResult{}, err
	}
	if len(sqls) == 0 {
		return driver.MutationResult{ExecutedSQL: sqls}, nil
	}
	var sb strings.Builder
	sb.WriteString("BEGIN TRANSACTION;\n")
	for _, s := range sqls {
		trimmed := strings.TrimRight(strings.TrimSpace(s), ";")
		if trimmed == "" {
			continue
		}
		sb.WriteString(trimmed)
		sb.WriteString(";\n")
	}
	sb.WriteString("COMMIT;\n")
	txScript := sb.String()
	if d.webMode {
		if sbErr := validateDuckDBWebSandbox(txScript); sbErr != nil {
			return driver.MutationResult{ExecutedSQL: sqls}, sbErr
		}
	}
	if _, err := d.runJSON(ctx, txScript); err != nil {
		return driver.MutationResult{ExecutedSQL: sqls}, err
	}
	return driver.MutationResult{Inserted: len(req.Inserts), Updated: len(req.Updates), Deleted: len(req.Deletes), ExecutedSQL: sqls}, nil
}

func (d *DuckDBDriver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	req.Schema = normalizeDuckSchema(req.Schema)
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, req)
	if err != nil {
		return ddl, err
	}
	stmts := []string{ddl}
	if len(req.Indexes) > 0 {
		alterActions := make([]driver.AlterAction, 0, len(req.Indexes))
		for _, idx := range req.Indexes {
			if len(idx.Columns) > 0 {
				alterActions = append(alterActions, driver.AlterAction{Action: "ADD_INDEX", Index: idx})
			}
		}
		if idxStmts, iErr := sqlbase.BuildAlterTableSQLs(sqlbase.DialectPostgres, driver.AlterTableRequest{
			Schema:  req.Schema,
			Table:   req.Table,
			Actions: alterActions,
		}); iErr == nil {
			stmts = append(stmts, idxStmts...)
		}
	}
	qTbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", req.Schema, req.Table)
	if strings.TrimSpace(req.Comment) != "" {
		stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS %s", qTbl, sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, req.Comment)))
	}
	for _, col := range req.Columns {
		if strings.TrimSpace(col.Name) != "" && strings.TrimSpace(col.Comment) != "" {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s",
				qTbl, sqlbase.QuoteIdent(sqlbase.DialectPostgres, col.Name), sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, col.Comment)))
		}
	}
	fullScript := strings.Join(stmts, ";\n")
	if d.webMode {
		for _, s := range stmts {
			if sbErr := validateDuckDBWebSandbox(s); sbErr != nil {
				return fullScript, sbErr
			}
		}
	}
	if req.PreviewOnly {
		return fullScript, nil
	}
	for _, s := range stmts {
		if _, err := d.runJSON(ctx, s); err != nil {
			return fullScript, err
		}
	}
	return fullScript, nil
}

func (d *DuckDBDriver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	req.Schema = normalizeDuckSchema(req.Schema)
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectPostgres, req)
	if err != nil {
		return stmts, err
	}
	if d.webMode {
		for _, s := range stmts {
			if sbErr := validateDuckDBWebSandbox(s); sbErr != nil {
				return stmts, sbErr
			}
		}
	}
	if req.PreviewOnly {
		return stmts, nil
	}
	for _, s := range stmts {
		if _, err := d.runJSON(ctx, s); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}

func (d *DuckDBDriver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	req.Schema = normalizeDuckSchema(req.Schema)
	if strings.EqualFold(req.Action, "TRUNCATE") {
		qTbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", req.Schema, req.ObjectName)
		_, err := d.runJSON(ctx, "DELETE FROM "+qTbl)
		return err
	}
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectPostgres, req)
	if err != nil {
		return err
	}
	for _, s := range sqls {
		if d.webMode {
			if err := validateDuckDBWebSandbox(s); err != nil {
				return err
			}
		}
		if _, err := d.runJSON(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func duckScriptNeedsSingleSession(stmts []string) bool {
	if len(stmts) <= 1 {
		return false
	}
	for _, stmt := range stmts {
		tokens := lexDuckDBTokens(stmt)
		if len(tokens) == 0 {
			continue
		}
		if tokens[0].kind == "word" {
			switch tokens[0].upper {
			case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "SET", "RESET", "PRAGMA", "USE":
				return true
			}
		}
		for _, tok := range tokens {
			if tok.kind == "word" && (tok.upper == "TEMP" || tok.upper == "TEMPORARY") {
				return true
			}
		}
	}
	return false
}

func buildDuckQueryResult(stmt string, recs []map[string]any, maxRows int, elapsedMs int64, isSelect bool) driver.QueryResult {
	truncated := false
	if maxRows > 0 && len(recs) > maxRows {
		recs = recs[:maxRows]
		truncated = true
	}
	var colNames []string
	if len(recs) > 0 {
		for k := range recs[0] {
			colNames = append(colNames, k)
		}
		sort.Strings(colNames)
	}
	cols := make([]driver.QueryColumn, len(colNames))
	for i, c := range colNames {
		cols[i] = driver.QueryColumn{Name: c}
	}
	rows := make([][]any, len(recs))
	for rIdx, rec := range recs {
		row := make([]any, len(colNames))
		for cIdx, c := range colNames {
			row[cIdx] = rec[c]
		}
		rows[rIdx] = row
	}
	msg := ""
	if !isSelect && len(rows) == 0 {
		msg = "Executed successfully"
	}
	return driver.QueryResult{
		Statement: stmt,
		Columns:   cols,
		Rows:      rows,
		Total:     int64(len(rows)),
		ElapsedMs: elapsedMs,
		IsSelect:  isSelect,
		Truncated: truncated,
		Message:   msg,
	}
}

func (d *DuckDBDriver) ExecuteSQL(ctx context.Context, _, query string, maxRows int) ([]driver.QueryResult, error) {
	if d.webMode || sqlbase.IsRestrictedWebContext(ctx) {
		if err := validateDuckDBWebSandbox(query); err != nil {
			return nil, err
		}
	}
	stmts := sqlbase.SplitStatementsForEngine("duckdb", query)
	if len(stmts) == 0 {
		return []driver.QueryResult{}, nil
	}
	if duckScriptNeedsSingleSession(stmts) {
		start := time.Now()
		sets, err := d.runJSONMulti(ctx, query)
		if err != nil {
			return nil, err
		}
		elapsed := time.Since(start).Milliseconds()
		if len(sets) == 0 {
			return []driver.QueryResult{buildDuckQueryResult(query, nil, maxRows, elapsed, false)}, nil
		}
		out := make([]driver.QueryResult, 0, len(sets))
		for idx, recs := range sets {
			label := query
			if idx < len(stmts) {
				label = stmts[idx]
			}
			out = append(out, buildDuckQueryResult(label, recs, maxRows, elapsed, true))
		}
		return out, nil
	}

	var out []driver.QueryResult
	for _, stmt := range stmts {
		start := time.Now()
		recs, err := d.runJSON(ctx, stmt)
		if err != nil {
			return out, err
		}
		out = append(out, buildDuckQueryResult(stmt, recs, maxRows, time.Since(start).Milliseconds(), sqlbase.IsQueryStatementForEngine("duckdb", stmt)))
	}
	return out, nil
}

type limitedDuckStderrBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedDuckStderrBuffer) Write(p []byte) (int, error) {
	if l.buf.Len() < l.max {
		rem := l.max - l.buf.Len()
		if len(p) > rem {
			_, _ = l.buf.Write(p[:rem])
		} else {
			_, _ = l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedDuckStderrBuffer) String() string {
	return l.buf.String()
}

func normalizeDuckJSONNumber(v any) any {
	if num, ok := v.(json.Number); ok {
		if i, err := num.Int64(); err == nil {
			return i
		}
		if f, err := num.Float64(); err == nil {
			return f
		}
		return num.String()
	}
	return v
}

func decodeOrderedDuckJSONObject(dec *json.Decoder) ([]string, map[string]any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected JSON object in DuckDB stream, got %v", tok)
	}
	cols := make([]string, 0, 16)
	row := make(map[string]any, 16)
	for dec.More() {
		kTok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := kTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected JSON object key string, got %T", kTok)
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, nil, err
		}
		cols = append(cols, key)
		row[key] = val
	}
	endTok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if endDelim, ok := endTok.(json.Delim); !ok || endDelim != '}' {
		return nil, nil, fmt.Errorf("expected closing '}' in DuckDB JSON object, got %v", endTok)
	}
	return cols, row, nil
}

func (d *DuckDBDriver) verifyExportCLI(ctx context.Context) error {
	if d.webMode || sqlbase.IsRestrictedWebContext(ctx) {
		return verifyDuckDBExecutable(d.cliBin)
	}
	if _, err := exec.LookPath(d.cliBin); err != nil {
		if _, statErr := os.Stat(d.cliBin); statErr != nil {
			return fmt.Errorf("duckdb executable (%s) not found; please install DuckDB CLI into system PATH or specify cliPath in connection options", d.cliBin)
		}
	}
	return nil
}

func (d *DuckDBDriver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	sc := normalizeDuckSchema(req.Schema)
	query := strings.TrimSpace(req.CustomSQL)
	if (d.webMode || sqlbase.IsRestrictedWebContext(ctx)) && query != "" {
		if err := validateDuckDBWebSandbox(query); err != nil {
			return err
		}
	}
	if sqlbase.IsRestrictedWebContext(ctx) {
		if err := verifyDuckDBExecutable(d.cliBin); err != nil {
			return err
		}
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "json"
	}

	if query == "" && (strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0) {
		multiFormat := strings.ToLower(strings.TrimSpace(req.Format))
		if multiFormat == "" {
			multiFormat = "sql"
		}
		switch multiFormat {
		case "sql", "json", "jsonl":
		default:
			return errors.New("full-database or multi-table export requires SQL, JSON, or JSONL format (CSV/Markdown apply only to single tables or custom SQL)")
		}

		targetNames := req.Tables
		if len(targetNames) == 0 {
			objs, err := d.ListObjects(ctx, "main", sc)
			if err != nil {
				return err
			}
			for _, t := range objs.Tables {
				targetNames = append(targetNames, t.Name)
			}
		}

		bw := bufio.NewWriterSize(w, 64*1024)
		switch multiFormat {
		case "sql":
			_, _ = bw.WriteString("-- DBHarbor DuckDB SQL Dump\n\n")
			for _, tName := range targetNames {
				if err := ctx.Err(); err != nil {
					return err
				}
				if req.DropIfExists {
					_, _ = fmt.Fprintf(bw, "DROP TABLE IF EXISTS %s;\n", sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, tName))
				}
				if req.IncludeDDL {
					if st, sErr := d.GetTableStructure(ctx, "main", sc, tName); sErr == nil && st.DDL != "" {
						ddlTxt := strings.TrimRight(strings.TrimSpace(st.DDL), ";")
						_, _ = bw.WriteString(ddlTxt + ";\n\n")
					}
				}
				subReq := req
				subReq.Schema = sc
				subReq.Table = tName
				subReq.Tables = nil
				subReq.Format = "sql"
				subReq.IncludeDDL = false
				subReq.DropIfExists = false
				subQuery := "SELECT * FROM " + sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, tName)
				if req.MaxRows > 0 {
					subQuery += fmt.Sprintf(" LIMIT %d", req.MaxRows)
				}
				if err := d.streamDuckDBQueryExport(ctx, subQuery, subReq, sc, bw); err != nil {
					return err
				}
			}
			return bw.Flush()

		case "json":
			dbName := strings.TrimSpace(req.Database)
			if dbName == "" {
				dbName = "main"
			}
			dbJSON, _ := json.Marshal(dbName)
			_, _ = bw.WriteString("{\n  \"database\": ")
			_, _ = bw.Write(dbJSON)
			_, _ = bw.WriteString(",\n  \"tables\": {\n")
			for idx, tName := range targetNames {
				if err := ctx.Err(); err != nil {
					return err
				}
				if idx > 0 {
					_, _ = bw.WriteString(",\n")
				}
				tblJSON, _ := json.Marshal(tName)
				_, _ = bw.WriteString("    ")
				_, _ = bw.Write(tblJSON)
				_, _ = bw.WriteString(": ")
				subReq := req
				subReq.Schema = sc
				subReq.Table = tName
				subReq.Tables = nil
				subReq.Format = "json"
				subReq.IncludeDDL = false
				subReq.DropIfExists = false
				subQuery := "SELECT * FROM " + sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, tName)
				if req.MaxRows > 0 {
					subQuery += fmt.Sprintf(" LIMIT %d", req.MaxRows)
				}
				if err := d.streamDuckDBQueryExport(ctx, subQuery, subReq, sc, bw); err != nil {
					return err
				}
			}
			_, _ = bw.WriteString("\n  }\n}\n")
			return bw.Flush()

		case "jsonl":
			for _, tName := range targetNames {
				if err := ctx.Err(); err != nil {
					return err
				}
				subReq := req
				subReq.Schema = sc
				subReq.Table = tName
				subReq.Tables = nil
				subReq.Format = "jsonl"
				subReq.IncludeDDL = false
				subReq.DropIfExists = false
				subQuery := "SELECT * FROM " + sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, tName)
				if req.MaxRows > 0 {
					subQuery += fmt.Sprintf(" LIMIT %d", req.MaxRows)
				}
				if err := d.streamDuckDBQueryExport(ctx, subQuery, subReq, sc, bw); err != nil {
					return err
				}
			}
			return bw.Flush()
		}
	}

	switch format {
	case "csv", "csv_bom", "csv-bom", "sql", "jsonl", "markdown", "json":
	default:
		return fmt.Errorf("unsupported export format: %s", req.Format)
	}

	if query == "" {
		if strings.TrimSpace(req.Table) == "" {
			return errors.New("export requires specifying a table or custom SQL")
		}
		query = "SELECT * FROM " + sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
		if req.MaxRows > 0 {
			query += fmt.Sprintf(" LIMIT %d", req.MaxRows)
		}
	}

	bw := bufio.NewWriterSize(w, 64*1024)
	if format == "sql" {
		if req.DropIfExists && req.Table != "" {
			_, _ = fmt.Fprintf(bw, "DROP TABLE IF EXISTS %s;\n", sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table))
		}
		if req.IncludeDDL && req.Table != "" {
			// Fetch DDL before acquiring duckFileLock inside streamDuckDBQueryExport to avoid re-entrant file locking.
			if st, sErr := d.GetTableStructure(ctx, "main", sc, req.Table); sErr == nil && st.DDL != "" {
				ddlTxt := strings.TrimRight(strings.TrimSpace(st.DDL), ";")
				_, _ = bw.WriteString(ddlTxt + ";\n\n")
			}
		}
	}
	if err := d.streamDuckDBQueryExport(ctx, query, req, sc, bw); err != nil {
		return err
	}
	return bw.Flush()
}

func (d *DuckDBDriver) streamDuckDBQueryExport(ctx context.Context, query string, req driver.ExportRequest, sc string, w io.Writer) error {
	if err := d.verifyExportCLI(ctx); err != nil {
		return err
	}
	if lk := duckFileLock(d.filePath); lk != nil {
		lk.Lock()
		defer lk.Unlock()
	}

	cmd := exec.CommandContext(ctx, d.cliBin, d.buildDuckDBArgs()...)
	cmd.Stdin = strings.NewReader(formatDuckDBStdinSQL(query))
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &limitedDuckStderrBuffer{max: 64 * 1024}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "json"
	}

	bw := bufio.NewWriterSize(w, 64*1024)
	var cw *csv.Writer
	switch format {
	case "csv", "csv_bom", "csv-bom":
		if format == "csv_bom" || format == "csv-bom" {
			if _, err := bw.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
				_ = stdoutPipe.Close()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				_ = cmd.Wait()
				return err
			}
		}
		cw = csv.NewWriter(bw)
	case "json":
		if err := bw.WriteByte('['); err != nil {
			_ = stdoutPipe.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			return err
		}
	}

	escapeCell := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, "|", `\|`)
		return s
	}

	targetTbl := `"exported_table"`
	if req.Table != "" {
		targetTbl = sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
	}

	br := bufio.NewReaderSize(stdoutPipe, 64*1024)
	dec := json.NewDecoder(br)
	dec.UseNumber()

	var (
		colNames     []string
		qColList     string
		rowMap       map[string]any
		rowCount     int
		stoppedEarly bool
		streamErr    error
	)

	firstTok, tokErr := dec.Token()
	if tokErr != nil {
		if !errors.Is(tokErr, io.EOF) {
			streamErr = tokErr
		}
	} else {
		delim, ok := firstTok.(json.Delim)
		if !ok || delim != '[' {
			streamErr = fmt.Errorf("expected DuckDB JSON array output, got %v", firstTok)
		} else {
			var csvRec []string
			var sqlVals []string
			var enc *json.Encoder
			if format == "json" {
				enc = json.NewEncoder(bw)
			}

			for dec.More() {
				if req.MaxRows > 0 && rowCount >= req.MaxRows {
					stoppedEarly = true
					break
				}
				if rowCount == 0 {
					cols, firstRow, decErr := decodeOrderedDuckJSONObject(dec)
					if decErr != nil {
						streamErr = decErr
						break
					}
					colNames = cols
					rowMap = firstRow
					csvRec = make([]string, len(colNames))
					sqlVals = make([]string, len(colNames))

					switch format {
					case "csv", "csv_bom", "csv-bom":
						if wErr := cw.Write(colNames); wErr != nil {
							streamErr = wErr
						}
					case "sql":
						qCols := make([]string, len(colNames))
						for i, c := range colNames {
							qCols[i] = sqlbase.QuoteIdent(sqlbase.DialectPostgres, c)
						}
						qColList = strings.Join(qCols, ", ")
					case "markdown":
						_, _ = bw.WriteString("| ")
						for i, c := range colNames {
							if i > 0 {
								_, _ = bw.WriteString(" | ")
							}
							_, _ = bw.WriteString(escapeCell(c))
						}
						_, _ = bw.WriteString(" |\n| ")
						for i := range colNames {
							if i > 0 {
								_, _ = bw.WriteString(" | ")
							}
							_, _ = bw.WriteString("---")
						}
						_, streamErr = bw.WriteString(" |\n")
					}
					if streamErr != nil {
						break
					}
				} else {
					clear(rowMap)
					if decErr := dec.Decode(&rowMap); decErr != nil {
						streamErr = decErr
						break
					}
				}

				switch format {
				case "csv", "csv_bom", "csv-bom":
					for i, c := range colNames {
						v := rowMap[c]
						if v == nil {
							csvRec[i] = ""
						} else {
							csvRec[i] = fmt.Sprintf("%v", v)
						}
					}
					if wErr := cw.Write(csvRec); wErr != nil {
						streamErr = wErr
					}
				case "sql":
					for i, c := range colNames {
						sqlVals[i] = sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, normalizeDuckJSONNumber(rowMap[c]))
					}
					_, streamErr = fmt.Fprintf(bw, "INSERT INTO %s (%s) VALUES (%s);\n", targetTbl, qColList, strings.Join(sqlVals, ", "))
				case "jsonl":
					if req.Table != "" {
						rowMap["__table__"] = req.Table
					}
					b, mErr := json.Marshal(rowMap)
					if mErr != nil {
						streamErr = mErr
					} else if _, wErr := bw.Write(b); wErr != nil {
						streamErr = wErr
					} else {
						streamErr = bw.WriteByte('\n')
					}
				case "markdown":
					_, _ = bw.WriteString("| ")
					for i, c := range colNames {
						if i > 0 {
							_, _ = bw.WriteString(" | ")
						}
						v := rowMap[c]
						if v == nil {
							_, _ = bw.WriteString("`NULL`")
						} else {
							_, _ = bw.WriteString(escapeCell(fmt.Sprintf("%v", v)))
						}
					}
					_, streamErr = bw.WriteString(" |\n")
				case "json":
					if rowCount > 0 {
						if err := bw.WriteByte(','); err != nil {
							streamErr = err
							break
						}
					}
					streamErr = enc.Encode(rowMap)
				}
				if streamErr != nil {
					break
				}
				rowCount++
				if rowCount%500 == 0 {
					if cw != nil {
						cw.Flush()
						if cw.Error() != nil {
							streamErr = cw.Error()
							break
						}
					}
					if fErr := bw.Flush(); fErr != nil {
						streamErr = fErr
						break
					}
				}
			}
		}
	}

	if stoppedEarly || streamErr != nil {
		_ = stdoutPipe.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if streamErr != nil {
			return streamErr
		}
	} else {
		if tokErr == nil {
			_, _ = dec.Token() // consume closing ']'
		}
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = waitErr.Error()
			}
			return errors.New(msg)
		}
	}

	if format == "json" {
		if _, err := bw.WriteString("]\n"); err != nil {
			return err
		}
	}
	if cw != nil {
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func (d *DuckDBDriver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	start := time.Now()
	sc := normalizeDuckSchema(req.Schema)
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "sql" || format == "" {
		res := driver.ImportResult{}
		var batchedStmts []string
		flushBatched := func() error {
			if len(batchedStmts) == 0 {
				return nil
			}
			script := strings.Join(batchedStmts, ";\n")
			if _, execErr := d.runJSONMulti(ctx, script); execErr != nil {
				return execErr
			}
			res.ImportedRows += len(batchedStmts)
			batchedStmts = batchedStmts[:0]
			return nil
		}
		err := sqlbase.StreamStatementsForEngine("duckdb", req.Reader, func(stmt string) error {
			if d.webMode || sqlbase.IsRestrictedWebContext(ctx) {
				if sbErr := validateDuckDBWebSandbox(stmt); sbErr != nil {
					return sbErr
				}
			}
			if !req.ContinueOnError {
				batchedStmts = append(batchedStmts, stmt)
				if len(batchedStmts) >= 250 {
					return flushBatched()
				}
				return nil
			}
			if _, execErr := d.runJSON(ctx, stmt); execErr != nil {
				res.FailedCount++
				if len(res.Warnings) < 20 {
					res.Warnings = append(res.Warnings, execErr.Error())
				}
				return nil
			}
			res.ImportedRows++
			return nil
		})
		if err == nil && !req.ContinueOnError {
			err = flushBatched()
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err
	}
	if format == "csv" && strings.TrimSpace(req.Table) != "" {
		cr := csv.NewReader(req.Reader)
		cr.LazyQuotes = true
		cr.FieldsPerRecord = -1
		headers, err := cr.Read()
		if err != nil {
			return driver.ImportResult{}, err
		}
		if len(headers) == 0 {
			return driver.ImportResult{}, errors.New("CSV header is empty")
		}
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF")
		tbl := sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
		qCols := make([]string, len(headers))
		for i, h := range headers {
			qCols[i] = sqlbase.QuoteIdent(sqlbase.DialectPostgres, strings.TrimSpace(h))
		}
		colList := strings.Join(qCols, ", ")

		const batchCap = 200
		res := driver.ImportResult{}
		batchRows := make([][]string, 0, batchCap)

		flushCSVBatch := func() error {
			if len(batchRows) == 0 {
				return nil
			}
			var sb strings.Builder
			sb.WriteString("INSERT INTO ")
			sb.WriteString(tbl)
			sb.WriteString(" (")
			sb.WriteString(colList)
			sb.WriteString(") VALUES ")
			for rIdx, vals := range batchRows {
				if rIdx > 0 {
					sb.WriteString(", ")
				}
				sb.WriteByte('(')
				sb.WriteString(strings.Join(vals, ", "))
				sb.WriteByte(')')
			}
			if _, execErr := d.runJSON(ctx, sb.String()); execErr != nil {
				if req.ContinueOnError {
					for _, vals := range batchRows {
						single := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, colList, strings.Join(vals, ", "))
						if _, sErr := d.runJSON(ctx, single); sErr != nil {
							res.FailedCount++
							if len(res.Warnings) < 20 {
								res.Warnings = append(res.Warnings, sErr.Error())
							}
						} else {
							res.ImportedRows++
						}
					}
					batchRows = batchRows[:0]
					return nil
				}
				return execErr
			}
			res.ImportedRows += len(batchRows)
			batchRows = batchRows[:0]
			return nil
		}

		for {
			rec, rErr := cr.Read()
			if errors.Is(rErr, io.EOF) {
				break
			}
			if rErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, rErr.Error())
					}
					continue
				}
				return res, rErr
			}
			vals := make([]string, len(headers))
			for i := range headers {
				if i >= len(rec) || rec[i] == "" {
					vals[i] = "NULL"
				} else {
					vals[i] = sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, rec[i])
				}
			}
			batchRows = append(batchRows, vals)
			if len(batchRows) >= batchCap {
				if err := flushCSVBatch(); err != nil {
					res.ElapsedMs = time.Since(start).Milliseconds()
					return res, err
				}
			}
		}
		if err := flushCSVBatch(); err != nil {
			res.ElapsedMs = time.Since(start).Milliseconds()
			return res, err
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, nil
	}
	return driver.ImportResult{}, errors.New("DuckDB import supports SQL or CSV format only")
}

func (d *DuckDBDriver) GetAutocompleteMeta(ctx context.Context, _ string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Databases: []string{"main"}, Tables: map[string][]string{}}
	recs, err := d.runJSON(ctx, "SELECT table_name, column_name FROM duckdb_columns() WHERE internal = false")
	if err != nil {
		return meta, nil
	}
	for _, r := range recs {
		t := fmt.Sprintf("%v", r["table_name"])
		c := fmt.Sprintf("%v", r["column_name"])
		meta.Tables[t] = append(meta.Tables[t], c)
	}
	return meta, nil
}

func (d *DuckDBDriver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	}
	if objs, err := d.ListObjects(ctx, "main", ""); err == nil {
		for _, t := range objs.Tables {
			snap.Storage = append(snap.Storage, driver.StorageItem{Name: t.Name, Category: "TABLE", Rows: t.Rows})
		}
	}
	return snap, nil
}

func (d *DuckDBDriver) KillSession(_ context.Context, _ string) error {
	return errors.New("DuckDB does not have standalone background connection sessions")
}

func (d *DuckDBDriver) ExportDatabaseFile(ctx context.Context, destPath string) error {
	srcPath := filepath.Clean(strings.TrimSpace(d.filePath))
	if d.tempDir != "" || srcPath == "" || srcPath == "." || srcPath == ":memory:" {
		return errors.New("in-memory DuckDB databases do not persist as standalone files")
	}
	cleanDest := filepath.Clean(strings.TrimSpace(destPath))
	if cleanDest == "" || cleanDest == "." || cleanDest == ":memory:" {
		return errors.New("invalid snapshot destination path")
	}

	if mu := duckFileLock(srcPath); mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}

	// Flush any pending WAL frames into the main DuckDB file before snapshotting
	_, _ = d.runJSONMultiUnlocked(ctx, "CHECKPOINT")

	fi, err := os.Lstat(srcPath)
	if err != nil {
		return fmt.Errorf("failed to stat DuckDB database file: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return errors.New("refusing to export non-regular or symlinked file")
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open DuckDB database file: %w", err)
	}
	defer src.Close()

	var hdr [16]byte
	n, _ := io.ReadFull(src, hdr[:])
	if n > 0 && (n < 12 || string(hdr[8:12]) != "DUCK") {
		return errors.New("source file is not a valid DuckDB binary database")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}

	_ = os.Remove(cleanDest)
	dst, err := os.OpenFile(cleanDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create DuckDB snapshot file: %w", err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(cleanDest)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(cleanDest)
		return closeErr
	}
	return nil
}

