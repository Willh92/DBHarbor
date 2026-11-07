package sqlbase

import (
	"errors"
	"fmt"
	"strings"
)

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
	"READ_CSV":               {},
	"READ_CSV_AUTO":          {},
	"READ_PARQUET":           {},
	"READ_JSON":              {},
	"READ_JSON_AUTO":         {},
	"READ_JSON_OBJECTS":      {},
	"READ_TEXT":              {},
	"READ_BLOB":              {},
	"PARQUET_SCAN":           {},
	"CSV_SCAN":               {},
	"JSON_SCAN":              {},
	"SQLITE_SCAN":            {},
	"SQLITE_ATTACH":          {},
	"POSTGRES_SCAN":          {},
	"POSTGRES_ATTACH":        {},
	"MYSQL_SCAN":             {},
	"MYSQL_ATTACH":           {},
	"ICEBERG_SCAN":           {},
	"DELTA_SCAN":             {},
	"READ_XLSX":              {},
	"ST_READ":                {},
	"GLOB":                   {},
	"LS":                     {},
	"COPY":                   {},
	"ATTACH":                 {},
	"DETACH":                 {},
	"INSTALL":                {},
	"LOAD":                   {},
	"EXPORT":                 {},
	"IMPORT":                 {},
	"HTTPFS":                 {},
	"GETENV":                 {},
	"CURRENT_SETTING":        {},
	"DUCKDB_SECRETS":         {},
	"ENABLE_EXTERNAL_ACCESS": {},
	"LOCK_CONFIGURATION":     {},
}

// HasSQLDefinerClause lexes sqlText (stripping comments and unwrapping /*! ... */ / /*M! ... */)
// and reports whether it contains a DEFINER = ... clause or [SQL] SECURITY DEFINER privilege escalation clause.
func HasSQLDefinerClause(sqlText string) bool {
	if strings.TrimSpace(sqlText) == "" {
		return false
	}
	_, lexTokens, _ := lexSQLForReadOnlyGuard(sqlText)
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
	return false
}

// ValidateExpectedDDLTarget lexically verifies (after comment stripping & executable-comment unwrapping)
// that stmt starts with one of allowedVerbs (e.g. CREATE, ALTER, REPLACE), followed only by benign
// SQL DDL modifiers, and targets one of expectedObjects (e.g. "VIEW", "MATERIALIZED VIEW", "FUNCTION", "PROCEDURE").
func ValidateExpectedDDLTarget(stmt string, allowedVerbs []string, expectedObjects ...string) error {
	_, lexTokens, _ := lexSQLForReadOnlyGuard(stmt)
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
	_, lexTokens, _ := lexSQLForReadOnlyGuard(trimmed)
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
	_, lexTokens, _ := lexSQLForReadOnlyGuard(stmt)
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
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		if strings.TrimSpace(script) == "" {
			return nil
		}
		stmts = []string{script}
	}
	for _, stmt := range stmts {
		_, lexTokens, _ := lexSQLForReadOnlyGuard(stmt)
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
// comment-obfuscated forbidden extensions/functions, and direct file path scans in FROM/JOIN clauses.
func ValidateDuckDBRestrictedWebSQL(script string) error {
	if hasSQLDotCommandOutsideQuotes(script) {
		return errors.New("DuckDB CLI dot-commands are disabled in Web sandbox mode")
	}
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		if strings.TrimSpace(script) == "" {
			return nil
		}
		stmts = []string{script}
	}
	for _, stmt := range stmts {
		if strings.HasPrefix(strings.TrimSpace(StripLeadingSQLComments(stmt)), ".") {
			return errors.New("DuckDB CLI dot-commands are disabled in Web sandbox mode")
		}
		_, lexTokens, _ := lexSQLForReadOnlyGuard(stmt)
		for i, tok := range lexTokens {
			if tok.kind == roTokWord || tok.kind == roTokQuotedIdent {
				if _, bad := forbiddenDuckDBWebKeywordsAndFuncs[tok.val]; bad {
					return fmt.Errorf("forbidden DuckDB keyword/function %q in Web sandbox mode", tok.val)
				}
			}
			if tok.kind == roTokWord && (tok.val == "FROM" || tok.val == "JOIN") &&
				i+1 < len(lexTokens) && lexTokens[i+1].kind == roTokString {
				return errors.New("direct file path scans in FROM clauses are forbidden in DuckDB Web sandbox mode")
			}
		}
	}
	return nil
}

func hasSQLDotCommandOutsideQuotes(s string) bool {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if strings.HasPrefix(trimmed, ".") {
		return true
	}
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	atLineStart := true
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		if inLineComment {
			if c == '\n' {
				inLineComment = false
				atLineStart = true
			}
			continue
		}
		if inBlockComment {
			if c == '*' && i+1 < n && s[i+1] == '/' {
				inBlockComment = false
				i++
			} else if c == '\n' {
				atLineStart = true
			}
			continue
		}
		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '"' {
				if i+1 < n && s[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
			continue
		}
		if c == '\n' || c == '\r' {
			atLineStart = true
			continue
		}
		if c == ' ' || c == '\t' {
			continue
		}
		if atLineStart && c == '.' {
			return true
		}
		atLineStart = false
		if c == '-' && i+1 < n && s[i+1] == '-' {
			inLineComment = true
			i++
			continue
		}
		if c == '/' && i+1 < n && s[i+1] == '*' {
			inBlockComment = true
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			continue
		}
	}
	return false
}
