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

func hasDuckDBDotCommandOutsideStrings(s string) bool {
	if strings.HasPrefix(strings.TrimSpace(s), ".") {
		return true
	}
	atLineStart := true
	n := len(s)
	for i := 0; i < n; i++ {
		ch := s[i]
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			atLineStart = true
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			wasAtLineStart := atLineStart
			sawNewline := false
			commentLineStart := false
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if i+1 < n && s[i] == '/' && s[i+1] == '*' {
					commentLineStart = false
					depth++
					i += 2
					continue
				}
				if i+1 < n && s[i] == '*' && s[i+1] == '/' {
					depth--
					i += 2
					continue
				}
				if s[i] == '\n' || s[i] == '\r' {
					sawNewline = true
					commentLineStart = true
					i++
					continue
				}
				if commentLineStart {
					if s[i] == ' ' || s[i] == '\t' {
						i++
						continue
					}
					if s[i] == '.' {
						return true
					}
					commentLineStart = false
				}
				i++
			}
			if sawNewline {
				atLineStart = commentLineStart
			} else {
				atLineStart = wasAtLineStart
			}
			i--
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			atLineStart = false
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
		if ch == '$' {
			if tag, ok := matchDuckDollarTag(s[i:]); ok {
				atLineStart = false
				endIdx := strings.Index(s[i+len(tag):], tag)
				if endIdx < 0 {
					return false
				}
				i = i + len(tag) + endIdx + len(tag) - 1
				continue
			}
		}
		if ch == '\n' || ch == '\r' {
			atLineStart = true
			continue
		}
		if atLineStart {
			if ch == ' ' || ch == '\t' {
				continue
			}
			if ch == '.' {
				return true
			}
			atLineStart = false
		}
	}
	return false
}

func matchDuckDollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	if s[1] == '$' {
		return "$$", true
	}
	if !((s[1] >= 'a' && s[1] <= 'z') || (s[1] >= 'A' && s[1] <= 'Z') || s[1] == '_') {
		return "", false
	}
	for i := 2; i < len(s); i++ {
		ch := s[i]
		if ch == '$' {
			return s[:i+1], true
		}
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return "", false
		}
	}
	return "", false
}

func lexDuckDBTokens(s string) []duckLexToken {
	var tokens []duckLexToken
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
			continue
		}
		if ch == '\'' {
			j := i + 1
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
			tokens = append(tokens, duckLexToken{kind: "string"})
			i = j
			continue
		}
		if ch == '$' {
			if tag, ok := matchDuckDollarTag(s[i:]); ok {
				endIdx := strings.Index(s[i+len(tag):], tag)
				if endIdx < 0 {
					tokens = append(tokens, duckLexToken{kind: "string"})
					break
				}
				tokens = append(tokens, duckLexToken{kind: "string"})
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
			i = j
			continue
		}
		tokens = append(tokens, duckLexToken{kind: "punct", upper: s[i : i+1]})
		i++
	}
	return tokens
}

var blockedDuckDBIdents = map[string]bool{
	"READ_CSV":                      true,
	"READ_CSV_AUTO":                 true,
	"READ_PARQUET":                  true,
	"READ_JSON":                     true,
	"READ_JSON_AUTO":                true,
	"READ_JSON_OBJECTS":             true,
	"READ_NDJSON":                   true,
	"READ_NDJSON_AUTO":              true,
	"READ_NDJSON_OBJECTS":           true,
	"READ_TEXT":                     true,
	"READ_BLOB":                     true,
	"PARQUET_SCAN":                  true,
	"CSV_SCAN":                      true,
	"JSON_SCAN":                     true,
	"SQLITE_SCAN":                   true,
	"SQLITE_ATTACH":                 true,
	"POSTGRES_SCAN":                 true,
	"POSTGRES_ATTACH":               true,
	"MYSQL_SCAN":                    true,
	"MYSQL_ATTACH":                  true,
	"ICEBERG_SCAN":                  true,
	"DELTA_SCAN":                    true,
	"ENABLE_EXTERNAL_ACCESS":        true,
	"LOCK_CONFIGURATION":            true,
	"AUTOINSTALL_KNOWN_EXTENSIONS":  true,
	"AUTOLOAD_KNOWN_EXTENSIONS":     true,
	"CUSTOM_EXTENSION_REPOSITORY":   true,
	"EXTENSION_DIRECTORY":           true,
	"ALLOWED_DIRECTORIES":           true,
	"ALLOWED_PATHS":                 true,
	"DISABLED_FILESYSTEMS":          true,
	"HTTPFS":                        true,
	"GETENV":                        true,
	"CURRENT_SETTING":               true,
	"DUCKDB_SECRETS":                true,
}

func validateDuckDBWebSandbox(script string) error {
	if hasDuckDBDotCommandOutsideStrings(script) {
		return errors.New("DuckDB CLI dot-commands are disabled in Web Server mode for host sandbox isolation")
	}
	for _, stmt := range sqlbase.SplitStatements(script) {
		trimmed := strings.TrimSpace(sqlbase.StripLeadingSQLComments(stmt))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, ".") {
			return errors.New("DuckDB CLI dot-commands are disabled in Web Server mode for host sandbox isolation")
		}
		tokens := lexDuckDBTokens(stmt)
		if len(tokens) == 0 {
			continue
		}
		if tokens[0].kind == "punct" && tokens[0].upper == "." {
			return errors.New("DuckDB CLI dot-commands are disabled in Web Server mode for host sandbox isolation")
		}
		for idx, tok := range tokens {
			if tok.kind == "word" {
				switch tok.upper {
				case "ATTACH", "DETACH", "COPY", "INSTALL", "LOAD":
					return fmt.Errorf("%s is disabled in Web Server mode for host sandbox isolation", tok.upper)
				case "EXPORT", "IMPORT":
					if idx == 0 || (idx+1 < len(tokens) && tokens[idx+1].kind == "word" && tokens[idx+1].upper == "DATABASE") {
						return fmt.Errorf("%s is disabled in Web Server mode for host sandbox isolation", tok.upper)
					}
				case "FROM", "JOIN":
					if idx+1 < len(tokens) && tokens[idx+1].kind == "string" {
						return errors.New("direct file path table scans are disabled in Web Server mode for host sandbox isolation")
					}
				}
			}
			if (tok.kind == "word" || tok.kind == "ident") && blockedDuckDBIdents[tok.upper] {
				return fmt.Errorf("%s is disabled in Web Server mode for host sandbox isolation", tok.upper)
			}
		}
	}
	return nil
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
	stmts := sqlbase.SplitStatements(query)
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
		out = append(out, buildDuckQueryResult(stmt, recs, maxRows, time.Since(start).Milliseconds(), sqlbase.IsQueryStatement(stmt)))
	}
	return out, nil
}

func (d *DuckDBDriver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	sc := normalizeDuckSchema(req.Schema)
	query := strings.TrimSpace(req.CustomSQL)
	if query == "" {
		if strings.TrimSpace(req.Table) == "" || len(req.Tables) > 0 {
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
			_, _ = io.WriteString(w, "-- DBHarbor DuckDB SQL Dump\n\n")
			for _, tName := range targetNames {
				if req.DropIfExists {
					_, _ = fmt.Fprintf(w, "DROP TABLE IF EXISTS %s;\n", sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, tName))
				}
				if req.IncludeDDL {
					if st, sErr := d.GetTableStructure(ctx, "main", sc, tName); sErr == nil && st.DDL != "" {
						ddlTxt := strings.TrimRight(strings.TrimSpace(st.DDL), ";")
						_, _ = io.WriteString(w, ddlTxt+";\n\n")
					}
				}
				subReq := req
				subReq.Schema = sc
				subReq.Table = tName
				subReq.Tables = nil
				subReq.Format = "sql"
				subReq.IncludeDDL = false
				subReq.DropIfExists = false
				if err := d.StreamExport(ctx, subReq, w); err != nil {
					return err
				}
			}
			return nil
		}
		query = "SELECT * FROM " + sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
	}
	resList, err := d.ExecuteSQL(ctx, req.Database, query, req.MaxRows)
	if err != nil || len(resList) == 0 {
		return err
	}
	res := resList[0]
	format := strings.ToLower(strings.TrimSpace(req.Format))
	switch format {
	case "csv", "csv_bom":
		if format == "csv_bom" {
			_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		cw := csv.NewWriter(w)
		headers := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			headers[i] = c.Name
		}
		_ = cw.Write(headers)
		for _, r := range res.Rows {
			rec := make([]string, len(r))
			for i, v := range r {
				if v != nil {
					rec[i] = fmt.Sprintf("%v", v)
				}
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		return cw.Error()
	case "sql":
		if req.DropIfExists && req.Table != "" {
			_, _ = fmt.Fprintf(w, "DROP TABLE IF EXISTS %s;\n", sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table))
		}
		if req.IncludeDDL && req.Table != "" {
			if st, sErr := d.GetTableStructure(ctx, "main", sc, req.Table); sErr == nil && st.DDL != "" {
				ddlTxt := strings.TrimRight(strings.TrimSpace(st.DDL), ";")
				_, _ = io.WriteString(w, ddlTxt+";\n\n")
			}
		}
		targetTbl := `"exported_table"`
		if req.Table != "" {
			targetTbl = sqlbase.QualifyTable(sqlbase.DialectPostgres, "", sc, req.Table)
		}
		qCols := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			qCols[i] = sqlbase.QuoteIdent(sqlbase.DialectPostgres, c.Name)
		}
		for _, r := range res.Rows {
			vals := make([]string, len(r))
			for i, v := range r {
				vals[i] = sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, v)
			}
			_, _ = fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES (%s);\n", targetTbl, strings.Join(qCols, ", "), strings.Join(vals, ", "))
		}
		return nil
	case "jsonl":
		bw := bufio.NewWriter(w)
		rowMap := make(map[string]any, len(res.Columns)+1)
		for _, r := range res.Rows {
			clear(rowMap)
			if req.Table != "" {
				rowMap["__table__"] = req.Table
			}
			for i, c := range res.Columns {
				if i < len(r) {
					rowMap[c.Name] = r[i]
				} else {
					rowMap[c.Name] = nil
				}
			}
			b, mErr := json.Marshal(rowMap)
			if mErr != nil {
				return mErr
			}
			if _, wErr := bw.Write(b); wErr != nil {
				return wErr
			}
			if wErr := bw.WriteByte('\n'); wErr != nil {
				return wErr
			}
		}
		return bw.Flush()
	case "markdown":
		escapeCell := func(s string) string {
			s = strings.ReplaceAll(s, "\r\n", " ")
			s = strings.ReplaceAll(s, "\n", " ")
			s = strings.ReplaceAll(s, `\`, `\\`)
			s = strings.ReplaceAll(s, "|", `\|`)
			return s
		}
		bw := bufio.NewWriter(w)
		_, _ = bw.WriteString("| ")
		for i, c := range res.Columns {
			if i > 0 {
				_, _ = bw.WriteString(" | ")
			}
			_, _ = bw.WriteString(escapeCell(c.Name))
		}
		_, _ = bw.WriteString(" |\n| ")
		for i := range res.Columns {
			if i > 0 {
				_, _ = bw.WriteString(" | ")
			}
			_, _ = bw.WriteString("---")
		}
		_, _ = bw.WriteString(" |\n")
		for _, r := range res.Rows {
			_, _ = bw.WriteString("| ")
			for i := range res.Columns {
				if i > 0 {
					_, _ = bw.WriteString(" | ")
				}
				var v any
				if i < len(r) {
					v = r[i]
				}
				if v == nil {
					_, _ = bw.WriteString("`NULL`")
				} else {
					_, _ = bw.WriteString(escapeCell(fmt.Sprintf("%v", v)))
				}
			}
			_, _ = bw.WriteString(" |\n")
		}
		return bw.Flush()
	case "json", "":
		bw := bufio.NewWriter(w)
		if _, err := bw.WriteString("["); err != nil {
			return err
		}
		enc := json.NewEncoder(bw)
		rowMap := make(map[string]any, len(res.Columns))
		for rIdx, r := range res.Rows {
			if rIdx > 0 {
				if _, err := bw.WriteString(","); err != nil {
					return err
				}
			}
			clear(rowMap)
			for i, c := range res.Columns {
				if i < len(r) {
					rowMap[c.Name] = r[i]
				} else {
					rowMap[c.Name] = nil
				}
			}
			if err := enc.Encode(rowMap); err != nil {
				return err
			}
		}
		if _, err := bw.WriteString("]\n"); err != nil {
			return err
		}
		return bw.Flush()
	default:
		return fmt.Errorf("unsupported export format: %s", req.Format)
	}
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
		err := sqlbase.StreamStatements(req.Reader, func(stmt string) error {
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

