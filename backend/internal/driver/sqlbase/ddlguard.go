package sqlbase

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type restrictedWebCtxKey struct{}

// WithRestrictedWebContext marks ctx as originating from a non-administrator user in restricted Web mode,
// ensuring even admin-created shared connections enforce engine/filesystem/network SQL sandboxes.
func WithRestrictedWebContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, restrictedWebCtxKey{}, true)
}

// IsRestrictedWebContext reports whether ctx carries the restricted Web sandbox marker.
func IsRestrictedWebContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(restrictedWebCtxKey{}).(bool)
	return v
}

var forbiddenClickHouseExternalEnginesAndFuncs = map[string]struct{}{
	"URL":               {},
	"URLCLUSTER":        {},
	"MYSQL":             {},
	"POSTGRESQL":        {},
	"MONGODB":           {},
	"REDIS":             {},
	"FILE":              {},
	"FILECLUSTER":       {},
	"EXECUTABLE":        {},
	"EXECUTABLE_POOL":   {},
	"EXECUTABLEPOOL":    {},
	"S3":                {},
	"S3CLUSTER":         {},
	"S3QUEUE":           {},
	"HDFS":              {},
	"HDFSCLUSTER":       {},
	"ODBC":              {},
	"JDBC":              {},
	"SQLITE":            {},
	"LIBRARY":           {},
	"REMOTE":            {},
	"REMOTESECURE":      {},
	"AZUREBLOBSTORAGE":  {},
	"AZUREQUEUE":        {},
	"KAFKA":             {},
	"RABBITMQ":          {},
	"NATS":              {},
	"HIVE":              {},
	"ICEBERG":           {},
	"DELTA_LAKE":        {},
	"DELTALAKE":         {},
	"HUDI":              {},
	"MEILISEARCH":       {},
}

var forbiddenClickHouseDictSources = map[string]struct{}{
	"FILE":               {},
	"URL":                {},
	"HTTP":               {},
	"HTTPS":              {},
	"MYSQL":              {},
	"POSTGRESQL":         {},
	"CLICKHOUSE":         {},
	"ODBC":               {},
	"JDBC":               {},
	"S3":                 {},
	"HDFS":               {},
	"MONGODB":            {},
	"REDIS":              {},
	"SQLITE":             {},
	"EXECUTABLE":         {},
	"EXECUTABLE_POOL":    {},
	"EXECUTABLEPOOL":     {},
	"LIBRARY":            {},
	"AZURE_BLOB_STORAGE": {},
	"AZUREBLOBSTORAGE":   {},
	"CASSANDRA":          {},
	"YTSAURUS":           {},
}

var forbiddenDuckDBWebKeywordsAndFuncs = map[string]struct{}{
	"READ_CSV":                     {},
	"READ_CSV_AUTO":                {},
	"READ_CSV_OBJECTS":             {},
	"READ_PARQUET":                 {},
	"READ_JSON":                    {},
	"READ_JSON_AUTO":               {},
	"READ_JSON_OBJECTS":            {},
	"READ_NDJSON":                  {},
	"READ_NDJSON_AUTO":             {},
	"READ_NDJSON_OBJECTS":          {},
	"READ_TEXT":                    {},
	"READ_BLOB":                    {},
	"PARQUET_SCAN":                 {},
	"PARQUET_METADATA":             {},
	"PARQUET_SCHEMA":               {},
	"PARQUET_KV_METADATA":          {},
	"PARQUET_FILE_METADATA":        {},
	"CSV_SCAN":                     {},
	"SNIFF_CSV":                    {},
	"JSON_SCAN":                    {},
	"SQLITE_SCAN":                  {},
	"SQLITE_ATTACH":                {},
	"POSTGRES_SCAN":                {},
	"POSTGRES_ATTACH":              {},
	"POSTGRES_QUERY":               {},
	"MYSQL_SCAN":                   {},
	"MYSQL_ATTACH":                 {},
	"MYSQL_QUERY":                  {},
	"ICEBERG_SCAN":                 {},
	"ICEBERG_METADATA":             {},
	"ICEBERG_SNAPSHOTS":            {},
	"DELTA_SCAN":                   {},
	"READ_XLSX":                    {},
	"ST_READ":                      {},
	"COPY":                         {},
	"ATTACH":                       {},
	"DETACH":                       {},
	"INSTALL":                      {},
	"LOAD":                         {},
	"HTTPFS":                       {},
	"GETENV":                       {},
	"CURRENT_SETTING":              {},
	"DUCKDB_SETTINGS":              {},
	"DUCKDB_SECRETS":               {},
	"WHICH_SECRET":                 {},
	"DUCKDB_EXTENSIONS":            {},
	"ENABLE_EXTERNAL_ACCESS":       {},
	"LOCK_CONFIGURATION":           {},
	"AUTOINSTALL_KNOWN_EXTENSIONS": {},
	"AUTOLOAD_KNOWN_EXTENSIONS":    {},
	"CUSTOM_EXTENSION_REPOSITORY":  {},
	"EXTENSION_DIRECTORY":          {},
	"ALLOWED_DIRECTORIES":          {},
	"ALLOWED_PATHS":                {},
	"DISABLED_FILESYSTEMS":         {},
	"SECRET_DIRECTORY":             {},
	"TEMP_DIRECTORY":               {},
	"ALLOW_UNSIGNED_EXTENSIONS":    {},
	"ALLOW_COMMUNITY_EXTENSIONS":   {},
	"ALLOW_PERSISTENT_SECRETS":     {},
}

var callOnlyDuckDBWebFuncs = map[string]struct{}{
	"GLOB": {},
	"LS":   {},
}

// HasSQLDefinerClause lexes sqlText (stripping comments and unwrapping /*! ... */ / /*M! ... */)
// and reports whether it contains a DEFINER = ... clause or [SQL] SECURITY DEFINER privilege escalation clause.
func HasSQLDefinerClause(sqlText string) bool {
	return HasSQLDefinerClauseForEngine("", sqlText)
}

// HasSQLDefinerClauseForEngine behaves like HasSQLDefinerClause using lexical rules for engine.
func HasSQLDefinerClauseForEngine(engine, sqlText string) bool {
	if strings.TrimSpace(sqlText) == "" {
		return false
	}
	opts := SyntaxOptionsForEngine(engine)
	variants := []SQLSyntaxOptions{opts}
	if hasExecCommentCandidate(sqlText, opts) {
		var overflowed bool
		variants, overflowed = execCommentOptsVariants(opts, sqlText)
		if overflowed {
			return true
		}
	}
	for _, variant := range variants {
		_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(sqlText, variant)
		for i := 0; i < len(lexTokens); i++ {
			tok := lexTokens[i]
			if tok.kind != roTokWord {
				continue
			}
			if tok.val == "DEFINER" && i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokEq {
				return true
			}
			if tok.val == "SECURITY" && i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokWord && lexTokens[i+1].val == "DEFINER" {
				return true
			}
		}
		if variant.ExecComments || variant.MariaExecComments {
			if _, found := hasMonotonicTokenPair(
				lexTokens,
				func(t roLexToken) bool { return t.kind == roTokWord && t.val == "DEFINER" },
				func(t roLexToken) bool { return t.kind == roTokEq },
			); found {
				return true
			}
			if _, found := hasMonotonicTokenPair(
				lexTokens,
				func(t roLexToken) bool { return t.kind == roTokWord && t.val == "SECURITY" },
				func(t roLexToken) bool { return t.kind == roTokWord && t.val == "DEFINER" },
			); found {
				return true
			}
		}
	}
	return false
}

// ValidateExpectedDDLTarget lexically verifies (after comment stripping & executable-comment unwrapping)
// that stmt starts with one of allowedVerbs (e.g. CREATE, ALTER, REPLACE), followed only by benign
// SQL DDL modifiers, and targets one of expectedObjects (e.g. "VIEW", "MATERIALIZED VIEW", "FUNCTION", "PROCEDURE").
func ValidateExpectedDDLTarget(stmt string, allowedVerbs []string, expectedObjects ...string) error {
	return ValidateExpectedDDLTargetForEngine("", stmt, allowedVerbs, expectedObjects...)
}

// ValidateExpectedDDLTargetForEngine behaves like ValidateExpectedDDLTarget using lexical rules for engine.
func ValidateExpectedDDLTargetForEngine(engine, stmt string, allowedVerbs []string, expectedObjects ...string) error {
	opts := SyntaxOptionsForEngine(engine)
	if !hasExecCommentCandidate(stmt, opts) {
		return validateExpectedDDLTargetWithOpts(stmt, opts, allowedVerbs, expectedObjects...)
	}
	variants, overflowed := execCommentOptsVariants(opts, stmt)
	if overflowed {
		return errors.New("statement exceeds executable-comment complexity limit")
	}
	for _, variant := range variants {
		if err := validateExpectedDDLTargetWithOpts(stmt, variant, allowedVerbs, expectedObjects...); err != nil {
			return err
		}
	}
	return nil
}

func validateExpectedDDLTargetWithOpts(stmt string, opts SQLSyntaxOptions, allowedVerbs []string, expectedObjects ...string) error {
	_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(stmt, opts)
	if len(lexTokens) == 0 || lexTokens[0].kind != roTokWord {
		return errors.New("DDL statement must begin with a valid SQL keyword")
	}
	firstVerb := lexTokens[0].val
	verbOK := false
	for _, v := range allowedVerbs {
		if strings.EqualFold(firstVerb, strings.TrimSpace(v)) {
			verbOK = true
			break
		}
	}
	if !verbOK {
		return fmt.Errorf("DDL statement must start with %s", strings.Join(allowedVerbs, " or "))
	}

	pos := 1
	for pos < len(lexTokens) && lexTokens[pos].kind == roTokWord {
		w := lexTokens[pos].val
		switch w {
		case "OR":
			if pos+1 < len(lexTokens) && lexTokens[pos+1].kind == roTokWord &&
				(lexTokens[pos+1].val == "REPLACE" || lexTokens[pos+1].val == "ALTER") {
				pos += 2
				continue
			}
		case "EDITIONABLE", "NONEDITIONABLE", "FORCE", "NOFORCE",
			"GLOBAL", "LOCAL", "PRIVATE", "SHARDED", "DUPLICATED", "IMMUTABLE", "DEFAULT",
			"TEMPORARY", "TEMP", "UNLOGGED", "RECURSIVE", "PUBLIC", "AGGREGATE", "WINDOW",
			"CONSTRAINT", "ONLINE", "UNIQUE", "TRUSTED", "SHARED", "SECURE", "TRANSIENT",
			"VOLATILE", "DETERMINISTIC", "PERMANENT":
			pos++
			continue
		case "ALGORITHM":
			if pos+2 < len(lexTokens) && lexTokens[pos+1].kind == roTokEq {
				pos += 3
				continue
			}
		case "SQL":
			if pos+2 < len(lexTokens) && lexTokens[pos+1].kind == roTokWord && lexTokens[pos+1].val == "SECURITY" {
				pos += 3
				continue
			}
		case "DEFINER":
			if pos+2 < len(lexTokens) && lexTokens[pos+1].kind == roTokEq {
				pos += 2
				if pos < len(lexTokens) && (lexTokens[pos].kind == roTokWord || lexTokens[pos].kind == roTokQuotedIdent || lexTokens[pos].kind == roTokString) {
					isCurrUser := lexTokens[pos].kind == roTokWord && lexTokens[pos].val == "CURRENT_USER"
					pos++
					if isCurrUser && pos+1 < len(lexTokens) && lexTokens[pos].kind == roTokLParen && lexTokens[pos+1].kind == roTokRParen {
						pos += 2
					} else if pos+1 < len(lexTokens) && lexTokens[pos].kind == roTokAt &&
						(lexTokens[pos+1].kind == roTokWord || lexTokens[pos+1].kind == roTokQuotedIdent || lexTokens[pos+1].kind == roTokString) {
						pos += 2
					}
				}
				continue
			}
		}
		break
	}

	for _, exp := range expectedObjects {
		parts := strings.Fields(strings.ToUpper(strings.TrimSpace(exp)))
		if len(parts) == 0 || pos+len(parts) > len(lexTokens) {
			continue
		}
		matched := true
		for j, p := range parts {
			if lexTokens[pos+j].kind != roTokWord || lexTokens[pos+j].val != p {
				matched = false
				break
			}
		}
		if matched {
			return nil
		}
	}

	got := "EOF"
	if pos < len(lexTokens) {
		if lexTokens[pos].val != "" {
			got = lexTokens[pos].val
		} else {
			got = "unexpected token"
		}
	}
	return fmt.Errorf("DDL target %q does not match expected object type (%s)", got, strings.Join(expectedObjects, " / "))
}

// ValidateClickHouseWebEngineClause validates a ClickHouse ENGINE specification in restricted Web mode,
// rejecting external network, database, and host-filesystem engines even when obfuscated with SQL comments or quotes.
func ValidateClickHouseWebEngineClause(engineClause string) error {
	trimmed := strings.TrimSpace(engineClause)
	if trimmed == "" {
		return nil
	}
	_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(trimmed, SyntaxOptionsForEngine("clickhouse"))
	for i, tok := range lexTokens {
		if tok.kind != roTokWord && tok.kind != roTokQuotedIdent {
			continue
		}
		if i == 0 || (i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokLParen) {
			if _, bad := forbiddenClickHouseExternalEnginesAndFuncs[tok.val]; bad {
				return fmt.Errorf("ClickHouse external engine %q is disabled for non-administrator accounts in restricted Web mode", tok.val)
			}
		}
	}
	return nil
}

// ValidateClickHouseDictionaryDDL validates a ClickHouse DICTIONARY DDL statement in Web mode,
// blocking external SOURCE(...) definitions (even with embedded SQL comments) for non-admin users.
func ValidateClickHouseDictionaryDDL(stmt string, allowPrivIP bool) error {
	_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(stmt, SyntaxOptionsForEngine("clickhouse"))
	for i := 0; i < len(lexTokens); i++ {
		if lexTokens[i].kind == roTokWord && lexTokens[i].val == "SOURCE" &&
			i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokLParen {
			if !allowPrivIP {
				return errors.New("ClickHouse dictionary SOURCE(...) requires administrator privileges in restricted Web mode")
			}
			if i+2 < len(lexTokens) && (lexTokens[i+2].kind == roTokWord || lexTokens[i+2].kind == roTokQuotedIdent) {
				if _, bad := forbiddenClickHouseDictSources[lexTokens[i+2].val]; bad {
					return fmt.Errorf("external ClickHouse dictionary source %q is restricted for non-administrator accounts in Web mode", lexTokens[i+2].val)
				}
			}
		}
	}
	return nil
}

// ValidateClickHouseRestrictedWebSQL validates arbitrary ClickHouse SQL scripts executed by non-admin users
// in restricted Web mode (`webMode && !allowPrivIP`), blocking external table engines, external table functions,
// and external dictionary sources even with embedded SQL comments or quoted identifiers.
func ValidateClickHouseRestrictedWebSQL(script string) error {
	chOpts := SyntaxOptionsForEngine("clickhouse")
	stmts := SplitStatementsForEngine("clickhouse", script)
	if len(stmts) == 0 {
		if strings.TrimSpace(script) == "" {
			return nil
		}
		stmts = []string{script}
	}
	for _, stmt := range stmts {
		_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(stmt, chOpts)
		hasDict := false
		for i, tok := range lexTokens {
			if tok.kind == roTokWord && tok.val == "DICTIONARY" {
				hasDict = true
			}
			// 1. ENGINE = <ExternalEngine> (with or without parentheses)
			if tok.kind == roTokWord && tok.val == "ENGINE" && i+2 < len(lexTokens) && lexTokens[i+1].kind == roTokEq {
				engTok := lexTokens[i+2]
				if engTok.kind == roTokWord || engTok.kind == roTokQuotedIdent {
					if _, bad := forbiddenClickHouseExternalEnginesAndFuncs[engTok.val]; bad {
						return fmt.Errorf("ClickHouse external engine %q is disabled for non-administrator accounts in restricted Web mode", engTok.val)
					}
				}
			}
			// 2. External table/engine function call: <func>(...)
			if tok.kind == roTokLParen && i > 0 {
				fnTok := lexTokens[i-1]
				if fnTok.kind == roTokWord || fnTok.kind == roTokQuotedIdent {
					if _, bad := forbiddenClickHouseExternalEnginesAndFuncs[fnTok.val]; bad {
						return fmt.Errorf("ClickHouse external function/engine %q is disabled for non-administrator accounts in restricted Web mode", fnTok.val)
					}
				}
			}
		}
		if hasDict {
			if err := ValidateClickHouseDictionaryDDL(stmt, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateDuckDBRestrictedWebSQL lexically validates DuckDB SQL in Web mode, rejecting CLI dot-commands,
// comment-obfuscated forbidden extensions/functions, PRAGMA/CALL/SECRET, and direct file path scans in FROM/JOIN/DESCRIBE/SUMMARIZE clauses.
func ValidateDuckDBRestrictedWebSQL(script string) error {
	duckOpts := SyntaxOptionsForEngine("duckdb")
	if hasSQLDotCommandOutsideQuotesWithOpts(script, duckOpts) {
		return errors.New("DuckDB CLI dot-commands are disabled in Web sandbox mode")
	}
	stmts := SplitStatementsForEngine("duckdb", script)
	if len(stmts) == 0 {
		if strings.TrimSpace(script) == "" {
			return nil
		}
		stmts = []string{script}
	}
	for _, stmt := range stmts {
		trimmed := strings.TrimSpace(StripLeadingSQLCommentsForEngine("duckdb", stmt))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, ".") {
			return errors.New("DuckDB CLI dot-commands are disabled in Web sandbox mode")
		}
		_, lexTokens, _ := lexSQLForReadOnlyGuardWithOpts(stmt, duckOpts)
		if len(lexTokens) == 0 {
			continue
		}
		if lexTokens[0].kind == roTokDot {
			return errors.New("DuckDB CLI dot-commands are disabled in Web sandbox mode")
		}
		nextNonLParenIsString := func(start int) bool {
			j := start
			for j < len(lexTokens) && lexTokens[j].kind == roTokLParen {
				j++
			}
			return j < len(lexTokens) && lexTokens[j].kind == roTokString
		}

		parenDepth := 0
		fromDepths := map[int]bool{}
		inValuesDepth := map[int]bool{}

		for i, tok := range lexTokens {
			if tok.kind == roTokLParen {
				parenDepth++
				continue
			}
			if tok.kind == roTokRParen {
				delete(fromDepths, parenDepth)
				delete(inValuesDepth, parenDepth)
				if parenDepth > 0 {
					parenDepth--
				}
				continue
			}
			if tok.kind == roTokSemi {
				delete(fromDepths, parenDepth)
				delete(inValuesDepth, parenDepth)
				continue
			}
			if tok.kind == roTokComma {
				if inValuesDepth[parenDepth] && i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokLParen {
					continue
				}
				if fromDepths[parenDepth] && nextNonLParenIsString(i+1) {
					return errors.New("direct file path scans in FROM clauses are forbidden in DuckDB Web sandbox mode")
				}
				continue
			}
			if tok.kind == roTokWord || tok.kind == roTokQuotedIdent {
				if _, bad := forbiddenDuckDBWebKeywordsAndFuncs[tok.val]; bad {
					return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
				}
				if _, callBad := callOnlyDuckDBWebFuncs[tok.val]; callBad &&
					i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokLParen {
					return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
				}
				if tok.kind != roTokWord || tok.val != "VALUES" {
					delete(inValuesDepth, parenDepth)
				}
			}
			if tok.kind == roTokWord {
				switch tok.val {
				case "PRAGMA", "CALL":
					return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
				case "EXPORT", "IMPORT":
					if i == 0 || (i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokWord && lexTokens[i+1].val == "DATABASE") {
						return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
					}
				case "SECRET":
					if i > 0 && lexTokens[i-1].kind == roTokWord {
						switch lexTokens[i-1].val {
						case "CREATE", "DROP", "REPLACE", "PERSISTENT", "TEMPORARY", "TEMP":
							return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
						}
					}
				case "FROM", "JOIN":
					if nextNonLParenIsString(i + 1) {
						return errors.New("direct file path scans in FROM clauses are forbidden in DuckDB Web sandbox mode")
					}
					if tok.val == "FROM" {
						fromDepths[parenDepth] = true
					}
				case "DESCRIBE", "DESC", "SUMMARIZE", "PIVOT", "UNPIVOT":
					if nextNonLParenIsString(i + 1) {
						return errors.New("direct file path scans in FROM clauses are forbidden in DuckDB Web sandbox mode")
					}
				case "VALUES":
					if fromDepths[parenDepth] {
						inValuesDepth[parenDepth] = true
					}
				case "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "QUALIFY", "WINDOW",
					"UNION", "INTERSECT", "EXCEPT", "RETURNING", "SET":
					delete(fromDepths, parenDepth)
					delete(inValuesDepth, parenDepth)
				}
			}
		}
	}
	return nil
}

// HasSQLDotCommandOutsideQuotes reports whether s contains a CLI dot-command (e.g. .shell, .read)
// at the start of any line outside SQL single/double/backtick/dollar-quoted literals and SQL comments.
func HasSQLDotCommandOutsideQuotes(s string) bool {
	return hasSQLDotCommandOutsideQuotes(s)
}

func matchSQLDollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	if s[1] == '$' {
		return "$$", true
	}
	if !isDollarTagStartByte(s[1]) {
		return "", false
	}
	for i := 2; i < len(s); i++ {
		ch := s[i]
		if ch == '$' {
			return s[:i+1], true
		}
		if !isDollarTagContByte(ch) {
			return "", false
		}
	}
	return "", false
}

func hasSQLDotCommandOutsideQuotes(s string) bool {
	return hasSQLDotCommandOutsideQuotesWithOpts(s, SyntaxOptionsForEngine("duckdb"))
}

func hasSQLDotCommandOutsideQuotesWithOpts(s string, opts SQLSyntaxOptions) bool {
	if strings.HasPrefix(strings.TrimSpace(s), ".") {
		return true
	}
	atLineStart := true
	prevIsIdentChar := false
	isIdentByte := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$' || b >= 0x80 || (opts.AlternativeQuotes && b == '#')
	}
	n := len(s)
	for i := 0; i < n; i++ {
		ch := s[i]
		if (opts.HashComments && ch == '#') || isDashLineCommentAt(s, i, opts) {
			if ch == '-' {
				i += 2
			} else {
				i++
			}
			for i < n && s[i] != '\n' {
				i++
			}
			atLineStart = true
			prevIsIdentChar = false
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			wasAtLineStart := atLineStart
			sawNewline := false
			commentLineStart := false
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if opts.NestedBlockComments && i+1 < n && s[i] == '/' && s[i+1] == '*' {
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
			prevIsIdentChar = false
			i--
			continue
		}
		if ch == '\'' || (opts.EscapeStringPrefix && !prevIsIdentChar && (ch == 'E' || ch == 'e') && i+1 < n && s[i+1] == '\'') {
			atLineStart = false
			prevIsIdentChar = false
			escMode := opts.BackslashEscapes
			if ch != '\'' {
				escMode = true
				i++ // consume E/e
			}
			i++ // consume opening '
			for i < n {
				if escMode && s[i] == '\\' {
					if i+1 < n {
						i += 2
					} else {
						i++
					}
					continue
				}
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			continue
		}
		if ch == '"' || (opts.BacktickIdentifiers && ch == '`') {
			atLineStart = false
			prevIsIdentChar = false
			q := ch
			i++
			for i < n {
				if opts.BackslashEscapes && q == '"' && s[i] == '\\' {
					if i+1 < n {
						i += 2
					} else {
						i++
					}
					continue
				}
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
		if opts.BracketIdentifiers && ch == '[' {
			atLineStart = false
			prevIsIdentChar = false
			i++
			for i < n {
				if s[i] == ']' {
					if opts.BracketDoubleEscape && i+1 < n && s[i+1] == ']' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			continue
		}
		if opts.AlternativeQuotes && !prevIsIdentChar && (ch == 'q' || ch == 'Q' || ch == 'n' || ch == 'N') {
			if prefixLen, closeSeq, ok := matchAltQuoteOpen(s[i:]); ok {
				atLineStart = false
				prevIsIdentChar = false
				endIdx := strings.Index(s[i+prefixLen:], closeSeq)
				if endIdx < 0 {
					return false
				}
				i = i + prefixLen + endIdx + len(closeSeq) - 1
				continue
			}
		}
		if opts.DollarQuotes && !prevIsIdentChar && ch == '$' {
			if tag, ok := matchSQLDollarTag(s[i:]); ok {
				atLineStart = false
				prevIsIdentChar = false
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
			prevIsIdentChar = false
			continue
		}
		if atLineStart {
			if ch == ' ' || ch == '\t' {
				prevIsIdentChar = false
				continue
			}
			if ch == '.' {
				return true
			}
			atLineStart = false
		}
		prevIsIdentChar = isIdentByte(ch)
	}
	return false
}
