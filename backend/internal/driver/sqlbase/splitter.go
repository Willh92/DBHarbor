package sqlbase

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SplitStatements splits a multi-statement SQL script on top-level semicolons,
// respecting single/double/backtick quotes, dollar-quoted blocks ($$...$$), and line/block comments.
func SplitStatements(script string) []string {
	var stmts []string
	_ = StreamStatements(strings.NewReader(script), func(stmt string) error {
		stmts = append(stmts, stmt)
		return nil
	})
	return stmts
}

// StreamStatements incrementally parses SQL statements from r with O(statement) memory,
// invoking fn for each non-empty executable statement.
func StreamStatements(r io.Reader, fn func(stmt string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var cur strings.Builder

	inSingle := false
	inDouble := false
	inBacktick := false
	dollarTag := ""
	inLineComment := false
	inBlockComment := false
	inExecComment := false
	firstByte := true

	var wordBuf [32]byte
	wordLen := 0
	stmtWordIdx := 0
	sawCreate := false
	stmtAllowsCompound := false
	isPackage := false
	packageOpened := false
	beginDepth := 0
	caseDepth := 0

	peekBytes := func(n int) []byte {
		b, _ := br.Peek(n)
		return b
	}

	peekNextWordUpper := func() string {
		b := peekBytes(24)
		i := 0
		for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
			i++
		}
		start := i
		for i < len(b) && ((b[i] >= 'a' && b[i] <= 'z') || (b[i] >= 'A' && b[i] <= 'Z') || b[i] == '_') {
			i++
		}
		if i == start {
			return ""
		}
		return strings.ToUpper(string(b[start:i]))
	}

	flushWord := func() {
		if wordLen == 0 {
			return
		}
		w := strings.ToUpper(string(wordBuf[:wordLen]))
		wordLen = 0
		stmtWordIdx++

		if stmtWordIdx == 1 {
			if w == "CREATE" || w == "ALTER" {
				sawCreate = true
			} else if w == "DECLARE" {
				stmtAllowsCompound = true
			}
			return
		}
		if sawCreate && !stmtAllowsCompound && stmtWordIdx <= 10 {
			switch w {
			case "PACKAGE":
				stmtAllowsCompound = true
				isPackage = true
			case "TRIGGER", "PROCEDURE", "FUNCTION", "EVENT":
				stmtAllowsCompound = true
			case "TABLE", "INDEX", "VIEW", "DATABASE", "SCHEMA", "USER", "ROLE", "SEQUENCE", "TYPE":
				sawCreate = false
			}
		}
		if !stmtAllowsCompound {
			return
		}
		if isPackage && !packageOpened && (w == "IS" || w == "AS") {
			packageOpened = true
			beginDepth++
			return
		}
		switch w {
		case "BEGIN":
			beginDepth++
		case "CASE":
			caseDepth++
		case "END":
			nw := peekNextWordUpper()
			switch nw {
			case "IF", "LOOP", "WHILE", "REPEAT":
				// Control-flow block terminator inside routine/trigger; beginDepth unchanged
			case "CASE":
				if caseDepth > 0 {
					caseDepth--
				}
			default:
				if caseDepth > 0 {
					caseDepth--
				} else if beginDepth > 0 {
					beginDepth--
				}
			}
		}
	}

	resetStmtState := func() {
		wordLen = 0
		stmtWordIdx = 0
		sawCreate = false
		stmtAllowsCompound = false
		isPackage = false
		packageOpened = false
		beginDepth = 0
		caseDepth = 0
	}

	for {
		ch, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Strip UTF-8 BOM at the very start of stream
		if firstByte {
			firstByte = false
			if ch == 0xEF {
				if next2 := peekBytes(2); len(next2) == 2 && next2[0] == 0xBB && next2[1] == 0xBF {
					_, _ = br.Discard(2)
					continue
				}
			}
		}

		var next byte
		if nb := peekBytes(1); len(nb) > 0 {
			next = nb[0]
		}

		if inLineComment {
			cur.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}

		if inBlockComment {
			cur.WriteByte(ch)
			if ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inBlockComment = false
			}
			continue
		}

		if dollarTag != "" {
			if ch == '$' {
				need := len(dollarTag) - 1
				ahead := peekBytes(need)
				if len(ahead) == need && string(ahead) == dollarTag[1:] {
					_, _ = br.Discard(need)
					cur.WriteString(dollarTag)
					dollarTag = ""
					continue
				}
			}
			cur.WriteByte(ch)
			continue
		}

		if inSingle {
			cur.WriteByte(ch)
			if ch == '\\' && len(peekBytes(1)) > 0 {
				esc, _ := br.ReadByte()
				cur.WriteByte(esc)
				continue
			}
			if ch == '\'' {
				if next == '\'' {
					_, _ = br.Discard(1)
					cur.WriteByte('\'')
				} else {
					inSingle = false
				}
			}
			continue
		}

		if inDouble {
			cur.WriteByte(ch)
			if ch == '\\' && len(peekBytes(1)) > 0 {
				esc, _ := br.ReadByte()
				cur.WriteByte(esc)
				continue
			}
			if ch == '"' {
				if next == '"' {
					_, _ = br.Discard(1)
					cur.WriteByte('"')
				} else {
					inDouble = false
				}
			}
			continue
		}

		if inBacktick {
			cur.WriteByte(ch)
			if ch == '`' {
				if next == '`' {
					_, _ = br.Discard(1)
					cur.WriteByte('`')
				} else {
					inBacktick = false
				}
			}
			continue
		}

		// Top-level comment checks
		if inExecComment && ch == '*' && next == '/' {
			flushWord()
			_, _ = br.Discard(1)
			cur.WriteString("*/")
			inExecComment = false
			continue
		}
		if ch == '-' && next == '-' {
			flushWord()
			_, _ = br.Discard(1)
			cur.WriteString("--")
			inLineComment = true
			continue
		}
		if ch == '#' {
			flushWord()
			cur.WriteByte(ch)
			inLineComment = true
			continue
		}
		if ch == '/' && next == '*' {
			flushWord()
			ahead := peekBytes(12)
			if adv := matchExecCommentAheadBytes(ahead); adv > 0 {
				_, _ = br.Discard(adv)
				cur.WriteByte('/')
				cur.Write(ahead[:adv])
				inExecComment = true
				continue
			}
			_, _ = br.Discard(1)
			cur.WriteString("/*")
			inBlockComment = true
			continue
		}

		// Quote openings
		if ch == '\'' {
			flushWord()
			inSingle = true
			cur.WriteByte(ch)
			continue
		}
		if ch == '"' {
			flushWord()
			inDouble = true
			cur.WriteByte(ch)
			continue
		}
		if ch == '`' {
			flushWord()
			inBacktick = true
			cur.WriteByte(ch)
			continue
		}
		if ch == '$' {
			flushWord()
			ahead := peekBytes(33)
			if tag := matchDollarTagBytes(ahead); tag != "" {
				_, _ = br.Discard(len(tag) - 1)
				dollarTag = tag
				cur.WriteString(tag)
				continue
			}
		}

		isWordChar := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9' && wordLen > 0) || ch == '_'
		if isWordChar {
			if wordLen < len(wordBuf) {
				wordBuf[wordLen] = ch
				wordLen++
			}
		} else {
			flushWord()
		}

		if ch == ';' {
			if stmtAllowsCompound && beginDepth > 0 {
				cur.WriteByte(ch)
				continue
			}
			trimmed := strings.TrimSpace(cur.String())
			cur.Reset()
			resetStmtState()
			if hasExecutableContent(trimmed) {
				if err := fn(trimmed); err != nil {
					return err
				}
			}
			continue
		}

		cur.WriteByte(ch)
	}

	flushWord()

	if trimmed := strings.TrimSpace(cur.String()); hasExecutableContent(trimmed) {
		if err := fn(trimmed); err != nil {
			return err
		}
	}
	return nil
}

// matchDollarTagBytes matches the remainder after an already-read leading '$'.
func matchDollarTagBytes(ahead []byte) string {
	for i := 0; i < len(ahead) && i < 32; i++ {
		c := ahead[i]
		if c == '$' {
			return "$" + string(ahead[:i+1])
		}
		isIdent := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) || c == '_'
		if !isIdent {
			return ""
		}
	}
	return ""
}

// matchExecCommentAheadBytes inspects bytes immediately following an already-read '/'
// and returns the number of bytes in ahead belonging to a MySQL (`/*!` / `/*!50110`)
// or MariaDB (`/*M!` / `/*m!` / `/*M!100100`) executable comment opening prefix, or 0 if regular `/*`.
func matchExecCommentAheadBytes(ahead []byte) int {
	if len(ahead) < 2 || ahead[0] != '*' {
		return 0
	}
	idx := 0
	if ahead[1] == '!' {
		idx = 2
	} else if len(ahead) >= 3 && (ahead[1] == 'M' || ahead[1] == 'm') && ahead[2] == '!' {
		idx = 3
	} else {
		return 0
	}
	digits := 0
	for idx < len(ahead) && digits < 6 && ahead[idx] >= '0' && ahead[idx] <= '9' {
		idx++
		digits++
	}
	return idx
}

// execCommentPrefixLen returns the length of a leading MySQL (`/*!...`) or MariaDB (`/*M!...` / `/*m!...`)
// executable comment opening prefix (including optional 5-6 digit version), or 0 if s does not start with one.
func execCommentPrefixLen(s string) int {
	if len(s) < 3 || s[0] != '/' || s[1] != '*' {
		return 0
	}
	idx := 0
	if s[2] == '!' {
		idx = 3
	} else if len(s) >= 4 && (s[2] == 'M' || s[2] == 'm') && s[3] == '!' {
		idx = 4
	} else {
		return 0
	}
	digits := 0
	for idx < len(s) && digits < 6 && s[idx] >= '0' && s[idx] <= '9' {
		idx++
		digits++
	}
	return idx
}

// StripLeadingSQLComments removes leading whitespace, line comments (-- and #), and regular block comments (/* ... */),
// while unwrapping MySQL/MariaDB executable comment prefixes (/*! ... */ and /*M! ... */) so security guards
// inspect the true leading SQL keyword.
func StripLeadingSQLComments(s string) string {
	for len(s) > 0 {
		i := 0
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
		s = s[i:]
		if strings.HasPrefix(s, "--") || strings.HasPrefix(s, "#") {
			if nl := strings.IndexByte(s, '\n'); nl >= 0 {
				s = s[nl+1:]
				continue
			}
			return ""
		}
		if plen := execCommentPrefixLen(s); plen > 0 {
			s = s[plen:]
			continue
		}
		if strings.HasPrefix(s, "*/") {
			s = s[2:]
			continue
		}
		if strings.HasPrefix(s, "/*") {
			if end := strings.Index(s[2:], "*/"); end >= 0 {
				s = s[2+end+2:]
				continue
			}
			return ""
		}
		break
	}
	return s
}

func hasExecutableContent(s string) bool {
	if s == "" {
		return false
	}
	inBlock := false
	inExec := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		var next byte
		if i+1 < len(s) {
			next = s[i+1]
		}
		if inBlock {
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			continue
		}
		if inExec && ch == '*' && next == '/' {
			inExec = false
			i++
			continue
		}
		if ch == '-' && next == '-' {
			if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
				i += nl
				continue
			}
			return false
		}
		if ch == '#' {
			if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
				i += nl
				continue
			}
			return false
		}
		if ch == '/' && next == '*' {
			if plen := execCommentPrefixLen(s[i:]); plen > 0 {
				inExec = true
				i += plen - 1
				continue
			}
			inBlock = true
			i++
			continue
		}
		return true
	}
	return false
}

var forbiddenReadOnlyKeywords = map[string]struct{}{
	"INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {}, "UPSERT": {}, "REPLACE": {},
	"CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {}, "RENAME": {},
	"GRANT": {}, "REVOKE": {}, "CALL": {}, "EXEC": {}, "EXECUTE": {},
	"COPY": {}, "ATTACH": {}, "DETACH": {}, "VACUUM": {}, "INTO": {},
}

var forbiddenReadOnlyFunctions = map[string]struct{}{
	// PostgreSQL administrative, filesystem, locking & network functions
	"PG_TERMINATE_BACKEND":      {},
	"PG_CANCEL_BACKEND":         {},
	"PG_RELOAD_CONF":            {},
	"PG_ROTATE_LOGFILE":         {},
	"PG_READ_FILE":              {},
	"PG_READ_BINARY_FILE":       {},
	"PG_LS_DIR":                 {},
	"PG_STAT_FILE":              {},
	"PG_ADVISORY_LOCK":          {},
	"PG_ADVISORY_XACT_LOCK":     {},
	"PG_TRY_ADVISORY_LOCK":      {},
	"PG_TRY_ADVISORY_XACT_LOCK": {},
	"SET_CONFIG":                {},
	"DBLINK":                    {},
	"DBLINK_EXEC":               {},
	"DBLINK_CONNECT":            {},
	"LO_IMPORT":                 {},
	"LO_EXPORT":                 {},
	"LO_UNLINK":                 {},
	"LO_CREATE":                 {},
	"LO_FROM_BYTEA":             {},
	"LO_PUT":                    {},

	// MySQL / SQLite extension & file I/O functions
	"LOAD_FILE":      {},
	"LOAD_EXTENSION": {},
	"READFILE":       {},
	"WRITEFILE":      {},
	"FTS3_TOKENIZER": {},

	// ClickHouse table / network / file I/O functions
	"URL":              {},
	"URLCLUSTER":       {},
	"REMOTE":           {},
	"REMOTESECURE":     {},
	"FILE":             {},
	"FILECLUSTER":      {},
	"S3":               {},
	"S3CLUSTER":        {},
	"HDFS":             {},
	"HDFSCLUSTER":      {},
	"MYSQL":            {},
	"POSTGRESQL":       {},
	"MONGODB":          {},
	"REDIS":            {},
	"SQLITE":           {},
	"ODBC":             {},
	"JDBC":             {},
	"EXECUTABLE":       {},
	"EXECUTABLE_POOL":  {},
	"EXECUTABLEPOOL":   {},
	"AZUREBLOBSTORAGE": {},
	"INPUT":            {},

	// SQL Server ad-hoc remote / file functions
	"OPENROWSET":                  {},
	"OPENDATASOURCE":              {},
	"OPENQUERY":                   {},
	"FN_XE_FILE_TARGET_READ_FILE": {},

	// Oracle network, filesystem & directory packages/types
	"UTL_HTTP":    {},
	"UTL_INADDR":  {},
	"UTL_TCP":     {},
	"UTL_SMTP":    {},
	"UTL_FILE":    {},
	"DBMS_LDAP":   {},
	"HTTPURITYPE": {},
}

// callOnlyReadOnlyFunctions are ClickHouse table/IO function names that can legitimately
// appear as ordinary column or table names when NOT invoked as a function call f(...).
var callOnlyReadOnlyFunctions = map[string]struct{}{
	"URL":              {},
	"URLCLUSTER":       {},
	"REMOTE":           {},
	"REMOTESECURE":     {},
	"FILE":             {},
	"FILECLUSTER":      {},
	"S3":               {},
	"S3CLUSTER":        {},
	"HDFS":             {},
	"HDFSCLUSTER":      {},
	"MYSQL":            {},
	"POSTGRESQL":       {},
	"MONGODB":          {},
	"REDIS":            {},
	"SQLITE":           {},
	"ODBC":             {},
	"JDBC":             {},
	"EXECUTABLE":       {},
	"EXECUTABLE_POOL":  {},
	"EXECUTABLEPOOL":   {},
	"AZUREBLOBSTORAGE": {},
	"INPUT":            {},
}

var forbiddenPragmaNames = map[string]struct{}{
	"WAL_CHECKPOINT": {}, "SHRINK_MEMORY": {}, "OPTIMIZE": {},
	"INCREMENTAL_VACUUM": {}, "WRITABLE_SCHEMA": {},
	"KEY": {}, "REKEY": {}, "HEXKEY": {}, "ACTIVATE_EXTENSIONS": {},
}

type roTokKind uint8

const (
	roTokWord roTokKind = iota
	roTokQuotedIdent
	roTokString
	roTokDot
	roTokLParen
	roTokRParen
	roTokAt
	roTokEq
	roTokOther
)

type roLexToken struct {
	kind roTokKind
	val  string
}

func lexSQLForReadOnlyGuard(stmt string) (words []string, lexTokens []roLexToken, hasAssignment bool) {
	n := len(stmt)
	i := 0
	isIdentChar := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$'
	}
	for i < n {
		c := stmt[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		// Line comments: -- or #
		if c == '#' || (c == '-' && i+1 < n && stmt[i+1] == '-') {
			for i < n && stmt[i] != '\n' {
				i++
			}
			continue
		}
		// Block comments: /* ... */ (step inside MySQL /*! ... */ and MariaDB /*M! ... */ executable comments)
		if c == '/' && i+1 < n && stmt[i+1] == '*' {
			if plen := execCommentPrefixLen(stmt[i:]); plen > 0 {
				i += plen
				continue
			}
			i += 2
			for i+1 < n && !(stmt[i] == '*' && stmt[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		if c == '*' && i+1 < n && stmt[i+1] == '/' {
			i += 2
			continue
		}
		// Single-quoted string literals '...'
		if c == '\'' {
			i++
			for i < n {
				if stmt[i] == '\\' {
					i += 2
					continue
				}
				if stmt[i] == '\'' {
					if i+1 < n && stmt[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			lexTokens = append(lexTokens, roLexToken{kind: roTokString})
			continue
		}
		// Quoted identifiers: "...", `...`
		if c == '"' || c == '`' {
			quote := c
			i++
			var identBuf strings.Builder
			for i < n {
				if stmt[i] == '\\' && quote != '`' && i+1 < n {
					identBuf.WriteByte(stmt[i+1])
					i += 2
					continue
				}
				if stmt[i] == quote {
					if i+1 < n && stmt[i+1] == quote {
						identBuf.WriteByte(quote)
						i += 2
						continue
					}
					i++
					break
				}
				identBuf.WriteByte(stmt[i])
				i++
			}
			lexTokens = append(lexTokens, roLexToken{kind: roTokQuotedIdent, val: strings.ToUpper(identBuf.String())})
			continue
		}
		// Bracket-quoted identifiers: [...]
		if c == '[' {
			i++
			var identBuf strings.Builder
			for i < n {
				if stmt[i] == ']' {
					if i+1 < n && stmt[i+1] == ']' {
						identBuf.WriteByte(']')
						i += 2
						continue
					}
					i++
					break
				}
				identBuf.WriteByte(stmt[i])
				i++
			}
			lexTokens = append(lexTokens, roLexToken{kind: roTokQuotedIdent, val: strings.ToUpper(identBuf.String())})
			continue
		}
		// Dollar-quoted strings in PostgreSQL: $$...$$ or $tag$...$tag$
		if c == '$' && (i+1 < n && (stmt[i+1] == '$' || (stmt[i+1] >= 'a' && stmt[i+1] <= 'z') || (stmt[i+1] >= 'A' && stmt[i+1] <= 'Z') || stmt[i+1] == '_')) {
			endTag := strings.IndexByte(stmt[i+1:], '$')
			if endTag >= 0 {
				tag := stmt[i : i+endTag+2]
				closeIdx := strings.Index(stmt[i+len(tag):], tag)
				if closeIdx >= 0 {
					i += len(tag) + closeIdx + len(tag)
					lexTokens = append(lexTokens, roLexToken{kind: roTokString})
					continue
				}
			}
		}
		if c == '=' {
			hasAssignment = true
			lexTokens = append(lexTokens, roLexToken{kind: roTokEq})
			i++
			continue
		}
		if c == '@' {
			lexTokens = append(lexTokens, roLexToken{kind: roTokAt})
			i++
			continue
		}
		if c == '.' {
			lexTokens = append(lexTokens, roLexToken{kind: roTokDot})
			i++
			continue
		}
		if c == '(' {
			lexTokens = append(lexTokens, roLexToken{kind: roTokLParen})
			i++
			continue
		}
		if c == ')' {
			lexTokens = append(lexTokens, roLexToken{kind: roTokRParen})
			i++
			continue
		}
		if isIdentChar(c) {
			start := i
			for i < n && isIdentChar(stmt[i]) {
				i++
			}
			if !(c >= '0' && c <= '9') {
				up := strings.ToUpper(stmt[start:i])
				words = append(words, up)
				lexTokens = append(lexTokens, roLexToken{kind: roTokWord, val: up})
			} else {
				lexTokens = append(lexTokens, roLexToken{kind: roTokOther})
			}
			continue
		}
		lexTokens = append(lexTokens, roLexToken{kind: roTokOther})
		i++
	}
	return words, lexTokens, hasAssignment
}

// IsReadOnlySafeStatement validates that a single SQL statement is strictly read-only.
func IsReadOnlySafeStatement(stmt string) (bool, string) {
	tokens, lexTokens, hasAssign := lexSQLForReadOnlyGuard(stmt)
	if len(tokens) == 0 {
		return true, ""
	}
	first := tokens[0]

	checkNoForbidden := func() (bool, string) {
		for idx, tok := range tokens {
			if _, bad := forbiddenReadOnlyKeywords[tok]; bad {
				return false, fmt.Sprintf("keyword %q is not permitted in read-only mode", tok)
			}
			if _, badFn := forbiddenReadOnlyFunctions[tok]; badFn {
				if _, callOnly := callOnlyReadOnlyFunctions[tok]; !callOnly {
					return false, fmt.Sprintf("function %q is not permitted in read-only mode", tok)
				}
			}
			// Block locking reads: FOR UPDATE / FOR SHARE / FOR NO KEY UPDATE / FOR KEY SHARE
			if tok == "FOR" && idx+1 < len(tokens) {
				next := tokens[idx+1]
				if next == "UPDATE" || next == "SHARE" || next == "NO" || next == "KEY" {
					return false, "locking SELECT clauses (FOR UPDATE / FOR SHARE) are not permitted in read-only mode"
				}
			}
		}

		// Inspect all function/package calls preceding '(' (including PKG.FUNC(...) and quoted identifiers)
		for idx, lt := range lexTokens {
			if lt.kind != roTokLParen || idx == 0 {
				continue
			}
			pos := idx - 1
			for pos >= 0 {
				t := lexTokens[pos]
				if t.kind != roTokWord && t.kind != roTokQuotedIdent {
					break
				}
				if _, badFn := forbiddenReadOnlyFunctions[t.val]; badFn {
					return false, fmt.Sprintf("function %q is not permitted in read-only mode", t.val)
				}
				if pos >= 2 && lexTokens[pos-1].kind == roTokDot &&
					(lexTokens[pos-2].kind == roTokWord || lexTokens[pos-2].kind == roTokQuotedIdent) {
					pos -= 2
					continue
				}
				break
			}
		}
		return true, ""
	}

	switch first {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "VALUES", "TABLE":
		return checkNoForbidden()
	case "WITH":
		hasSelect := false
		for _, tok := range tokens {
			if tok == "SELECT" || tok == "VALUES" || tok == "TABLE" {
				hasSelect = true
				break
			}
		}
		if !hasSelect {
			return false, "WITH statement must end in a read-only SELECT query"
		}
		return checkNoForbidden()
	case "EXPLAIN":
		return checkNoForbidden()
	case "PRAGMA":
		if hasAssign {
			return false, "mutating PRAGMA assignments are not permitted in read-only mode"
		}
		if len(tokens) >= 2 {
			if _, bad := forbiddenPragmaNames[tokens[1]]; bad {
				return false, fmt.Sprintf("PRAGMA %s is not permitted in read-only mode", tokens[1])
			}
		}
		return checkNoForbidden()
	default:
		return false, fmt.Sprintf("statement %q is not permitted in read-only mode", first)
	}
}

// ValidateReadOnlyScript validates every statement in a potentially multi-statement SQL script.
func ValidateReadOnlyScript(script string) error {
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		if ok, reason := IsReadOnlySafeStatement(script); !ok {
			return errors.New(reason)
		}
		return nil
	}
	for _, stmt := range stmts {
		if ok, reason := IsReadOnlySafeStatement(stmt); !ok {
			return errors.New(reason)
		}
	}
	return nil
}
