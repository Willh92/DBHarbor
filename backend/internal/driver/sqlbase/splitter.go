package sqlbase

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// SQLSyntaxOptions controls dialect-specific SQL lexical rules for splitting and security scanning.
type SQLSyntaxOptions struct {
	// BackslashEscapes controls whether '\' escapes the next character inside '...' (and "...").
	// True for MySQL-family (mysql, mariadb, tidb, oceanbase) and ClickHouse; false for standard SQL.
	BackslashEscapes bool
	// EscapeStringPrefix controls whether PostgreSQL-style E'...' / e'...' literals (at identifier
	// boundaries) enable backslash escaping inside that literal.
	EscapeStringPrefix bool
	// NestedBlockComments controls whether /* ... */ block comments nest (e.g. /* /* */ */).
	// True for PostgreSQL, KingbaseES, DuckDB, and SQL Server; false for MySQL-family, ClickHouse, SQLite, Oracle, Dameng.
	NestedBlockComments bool
	// HashComments controls whether '#' starts a single-line comment.
	// True ONLY for MySQL-family (mysql, mariadb, tidb, oceanbase) and ClickHouse.
	HashComments bool
	// ExecComments controls whether MySQL-style /*! ... */ executable comments are unwrapped.
	// True for MySQL-family (mysql, mariadb, tidb, oceanbase) and default generic mode ("").
	ExecComments bool
	// MariaExecComments controls whether MariaDB-only /*M! ... */ and /*m! ... */ executable comments are unwrapped.
	// True ONLY for mariadb; false for mysql, tidb, oceanbase, "", and all other engines.
	MariaExecComments bool
	// DollarQuotes controls whether PostgreSQL-style $$...$$ or $tag$...$tag$ dollar-quoted string literals are recognized.
	// True ONLY for PostgreSQL, KingbaseES, and DuckDB; false for MySQL-family, ClickHouse, SQLite, SQL Server, Oracle, Dameng, and "".
	DollarQuotes bool
	// DashCommentRequiresSpace controls whether '--' starts a single-line comment ONLY when at EOF
	// or immediately followed by whitespace or an ASCII control character (b <= ' ' || b == 0x7f, i.e. 0x00..0x20 or DEL).
	// Matches MySQL/MariaDB/TiDB sql_lex.cc stateless forward lookahead (no preceding-dash suppression).
	// True ONLY for MySQL-family engines (mysql, mariadb, tidb, oceanbase); false for standard ANSI '--'.
	DashCommentRequiresSpace bool
	// BracketIdentifiers controls whether [...] is lexed as a bracket-quoted identifier.
	// True ONLY for SQL Server (sqlserver, mssql), SQLite (sqlite), and generic mode ("").
	// False for PostgreSQL, KingbaseES, DuckDB, ClickHouse, MySQL-family, Oracle, and Dameng.
	BracketIdentifiers bool
	// BracketDoubleEscape controls whether ']]' inside [...] is treated as an escaped literal ']'.
	// True ONLY for SQL Server (sqlserver, mssql) and generic mode (""); false for SQLite where '[' terminates at the first ']'.
	BracketDoubleEscape bool
	// BacktickIdentifiers controls whether `...` is lexed as a backtick-quoted identifier.
	// True ONLY for MySQL-family (mysql, mariadb, tidb, oceanbase), ClickHouse, SQLite, and generic mode ("").
	// False for PostgreSQL, KingbaseES, DuckDB, SQL Server, Oracle, and Dameng.
	BacktickIdentifiers bool
	// AlternativeQuotes controls whether Oracle / Dameng q'<delim>...<matching_delim>' (Q'...', nq'...', NQ'...')
	// alternative quoting literals are recognized at identifier boundaries.
	// True ONLY for Oracle (oracle) and Dameng (dameng, dm8); false for all other engines.
	AlternativeQuotes bool
	// skipUnclosedExecComments is an internal security-guard option that treats any executable comment
	// whose interior before the first closing */ contains an unclosed quote as a plain skipped block comment.
	skipUnclosedExecComments bool
	// skipNestedExecComments is an internal security-guard option that prevents unwrapping nested
	// /*! ... */ or /*M! ... */ comments while already inside an active executable comment, matching
	// MySQL/MariaDB servers that treat inner high-version or plain /* ... */ comments as discarded.
	skipNestedExecComments bool
	// preserveExecQuotes is an internal security-guard option that models MySQL/MariaDB PRESERVE_COMMENT
	// mode where quotes ('...', "...", `...`) and line comments ('-- ...', '#...') inside an active
	// executable comment are NOT clamped at */.
	preserveExecQuotes bool
	// hasMaxExecVersion / maxExecVersion restrict /*! ... */ unwrapping to versions <= maxExecVersion
	// (and /*M! ... */ when hasMaxMariaExecVersion is false). Unversioned /*! or /*M! has version 0.
	hasMaxExecVersion bool
	maxExecVersion    int
	// hasMaxMariaExecVersion / maxMariaExecVersion independently restrict /*M! ... */ / /*m! ... */
	// unwrapping on MariaDB to versions <= maxMariaExecVersion.
	hasMaxMariaExecVersion bool
	maxMariaExecVersion    int
}

// SyntaxOptionsForEngine returns the SQL lexical options for the given database engine or dialect name.
func SyntaxOptionsForEngine(engine string) SQLSyntaxOptions {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "mariadb":
		return SQLSyntaxOptions{BackslashEscapes: true, HashComments: true, ExecComments: true, MariaExecComments: true, DashCommentRequiresSpace: true, BacktickIdentifiers: true}
	case "mysql", "tidb", "oceanbase":
		return SQLSyntaxOptions{BackslashEscapes: true, HashComments: true, ExecComments: true, DashCommentRequiresSpace: true, BacktickIdentifiers: true}
	case "clickhouse":
		return SQLSyntaxOptions{BackslashEscapes: true, HashComments: true, BacktickIdentifiers: true}
	case "postgres", "postgresql", "kingbase", "duckdb":
		return SQLSyntaxOptions{EscapeStringPrefix: true, NestedBlockComments: true, DollarQuotes: true}
	case "sqlserver", "mssql":
		return SQLSyntaxOptions{NestedBlockComments: true, BracketIdentifiers: true, BracketDoubleEscape: true}
	case "sqlite":
		return SQLSyntaxOptions{BracketIdentifiers: true, BacktickIdentifiers: true}
	case "oracle", "dameng", "dm8":
		return SQLSyntaxOptions{AlternativeQuotes: true}
	case "":
		return SQLSyntaxOptions{ExecComments: true, BracketIdentifiers: true, BracketDoubleEscape: true, BacktickIdentifiers: true}
	default:
		return SQLSyntaxOptions{}
	}
}

// SplitStatements splits a multi-statement SQL script on top-level semicolons using standard SQL
// single-quote rules (backslash is literal inside '...'), respecting quotes, dollar-quoted blocks, and comments.
func SplitStatements(script string) []string {
	return SplitStatementsForEngine("", script)
}

// SplitStatementsForEngine splits a multi-statement SQL script using lexical rules for engine.
func SplitStatementsForEngine(engine, script string) []string {
	var stmts []string
	_ = StreamStatementsForEngine(engine, strings.NewReader(script), func(stmt string) error {
		stmts = append(stmts, stmt)
		return nil
	})
	return stmts
}

// StreamStatements incrementally parses SQL statements from r with O(statement) memory
// using standard SQL single-quote rules, invoking fn for each non-empty executable statement.
func StreamStatements(r io.Reader, fn func(stmt string) error) error {
	return StreamStatementsForEngine("", r, fn)
}

// StreamStatementsForEngine incrementally parses SQL statements from r using lexical rules for engine.
func StreamStatementsForEngine(engine string, r io.Reader, fn func(stmt string) error) error {
	return StreamStatementsWithOptions(r, SyntaxOptionsForEngine(engine), fn)
}

// StreamStatementsWithOptions incrementally parses SQL statements from r using opts.
func StreamStatementsWithOptions(r io.Reader, opts SQLSyntaxOptions, fn func(stmt string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var cur strings.Builder

	inSingle := false
	singleBackslashEsc := false
	inDouble := false
	inBacktick := false
	inBracket := false
	dollarTag := ""
	altQuoteClose := ""
	inLineComment := false
	blockCommentDepth := 0
	inExecComment := false
	firstByte := true
	prevIsIdentChar := false

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
			if !opts.preserveExecQuotes && inExecComment && ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inLineComment = false
				inExecComment = false
				prevIsIdentChar = false
				continue
			}
			if ch == '\n' {
				inLineComment = false
				prevIsIdentChar = false
			}
			continue
		}

		if blockCommentDepth > 0 {
			cur.WriteByte(ch)
			if opts.NestedBlockComments && ch == '/' && next == '*' {
				_, _ = br.Discard(1)
				cur.WriteByte('*')
				blockCommentDepth++
				prevIsIdentChar = false
				continue
			}
			if ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				blockCommentDepth--
				if blockCommentDepth == 0 && !opts.NestedBlockComments && !opts.skipNestedExecComments {
					inExecComment = false
				}
				prevIsIdentChar = false
			}
			continue
		}

		if dollarTag != "" {
			if ch == '$' {
				want := dollarTag[1:]
				if len(want) <= 32*1024 {
					ahead := peekBytes(len(want))
					if len(ahead) == len(want) && string(ahead) == want {
						_, _ = br.Discard(len(want))
						cur.WriteString(dollarTag)
						dollarTag = ""
						prevIsIdentChar = false
						continue
					}
				} else {
					cur.WriteByte('$')
					matchedIdx := 0
					for matchedIdx < len(want) {
						b, pErr := br.Peek(1)
						if pErr != nil || len(b) == 0 {
							break
						}
						c := b[0]
						if c == want[matchedIdx] {
							_, _ = br.Discard(1)
							cur.WriteByte(c)
							matchedIdx++
							continue
						}
						if c == '$' {
							_, _ = br.Discard(1)
							cur.WriteByte('$')
							matchedIdx = 0
							continue
						}
						break
					}
					if matchedIdx == len(want) {
						dollarTag = ""
						prevIsIdentChar = false
					}
					continue
				}
			}
			cur.WriteByte(ch)
			continue
		}

		if altQuoteClose != "" {
			if ch == altQuoteClose[0] {
				need := len(altQuoteClose) - 1
				ahead := peekBytes(need)
				if len(ahead) == need && string(ahead) == altQuoteClose[1:] {
					_, _ = br.Discard(need)
					cur.WriteString(altQuoteClose)
					altQuoteClose = ""
					prevIsIdentChar = false
					continue
				}
			}
			cur.WriteByte(ch)
			continue
		}

		if inSingle {
			cur.WriteByte(ch)
			if singleBackslashEsc && ch == '\\' {
				ahead := peekBytes(2)
				if len(ahead) > 0 && !(!opts.preserveExecQuotes && inExecComment && len(ahead) == 2 && ahead[0] == '*' && ahead[1] == '/') {
					esc, _ := br.ReadByte()
					cur.WriteByte(esc)
					continue
				}
			}
			if !opts.preserveExecQuotes && inExecComment && ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inSingle = false
				singleBackslashEsc = false
				inExecComment = false
				prevIsIdentChar = false
				continue
			}
			if ch == '\'' {
				if next == '\'' {
					_, _ = br.Discard(1)
					cur.WriteByte('\'')
				} else {
					inSingle = false
					singleBackslashEsc = false
					prevIsIdentChar = false
				}
			}
			continue
		}

		if inDouble {
			cur.WriteByte(ch)
			if opts.BackslashEscapes && ch == '\\' {
				ahead := peekBytes(2)
				if len(ahead) > 0 && !(!opts.preserveExecQuotes && inExecComment && len(ahead) == 2 && ahead[0] == '*' && ahead[1] == '/') {
					esc, _ := br.ReadByte()
					cur.WriteByte(esc)
					continue
				}
			}
			if !opts.preserveExecQuotes && inExecComment && ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inDouble = false
				inExecComment = false
				prevIsIdentChar = false
				continue
			}
			if ch == '"' {
				if next == '"' {
					_, _ = br.Discard(1)
					cur.WriteByte('"')
				} else {
					inDouble = false
					prevIsIdentChar = false
				}
			}
			continue
		}

		if inBacktick {
			cur.WriteByte(ch)
			if !opts.preserveExecQuotes && inExecComment && ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inBacktick = false
				inExecComment = false
				prevIsIdentChar = false
				continue
			}
			if ch == '`' {
				if next == '`' {
					_, _ = br.Discard(1)
					cur.WriteByte('`')
				} else {
					inBacktick = false
					prevIsIdentChar = false
				}
			}
			continue
		}

		if inBracket {
			cur.WriteByte(ch)
			if !opts.preserveExecQuotes && inExecComment && ch == '*' && next == '/' {
				_, _ = br.Discard(1)
				cur.WriteByte('/')
				inBracket = false
				inExecComment = false
				prevIsIdentChar = false
				continue
			}
			if ch == ']' {
				if opts.BracketDoubleEscape && next == ']' {
					_, _ = br.Discard(1)
					cur.WriteByte(']')
				} else {
					inBracket = false
					prevIsIdentChar = false
				}
			}
			continue
		}

		// Top-level comment checks
		if (opts.ExecComments || opts.MariaExecComments) && inExecComment && ch == '*' && next == '/' {
			flushWord()
			_, _ = br.Discard(1)
			cur.WriteString("*/")
			inExecComment = false
			prevIsIdentChar = false
			continue
		}
		if ch == '-' && next == '-' {
			isDashComment := !opts.DashCommentRequiresSpace
			if !isDashComment {
				ahead2 := peekBytes(2)
				isDashComment = len(ahead2) < 2 || isMySQLDashCommentFollowByte(ahead2[1])
			}
			if isDashComment {
				flushWord()
				_, _ = br.Discard(1)
				cur.WriteString("--")
				inLineComment = true
				prevIsIdentChar = false
				continue
			}
		}
		if opts.HashComments && ch == '#' {
			flushWord()
			cur.WriteByte(ch)
			inLineComment = true
			prevIsIdentChar = false
			continue
		}
		if ch == '/' && next == '*' {
			flushWord()
			if (!inExecComment || !opts.skipNestedExecComments) && (opts.ExecComments || opts.MariaExecComments) {
				ahead := peekBytes(12)
				if adv := matchExecCommentAheadBytesWithOpts(ahead, opts); adv > 0 {
					if opts.skipUnclosedExecComments {
						win := peekBytes(32 * 1024)
						if execCommentAheadHasUnclosedQuoteBeforeClose(win, adv, opts) {
							_, _ = br.Discard(1)
							cur.WriteString("/*")
							blockCommentDepth = 1
							prevIsIdentChar = false
							continue
						}
					}
					_, _ = br.Discard(adv)
					cur.WriteByte('/')
					cur.Write(ahead[:adv])
					inExecComment = true
					prevIsIdentChar = false
					continue
				}
			}
			_, _ = br.Discard(1)
			cur.WriteString("/*")
			blockCommentDepth = 1
			prevIsIdentChar = false
			continue
		}

		// Quote openings
		if opts.EscapeStringPrefix && !prevIsIdentChar && (ch == 'E' || ch == 'e') && next == '\'' {
			flushWord()
			_, _ = br.Discard(1)
			inSingle = true
			singleBackslashEsc = true
			prevIsIdentChar = false
			cur.WriteByte(ch)
			cur.WriteByte('\'')
			continue
		}
		if opts.AlternativeQuotes && !prevIsIdentChar && (ch == 'q' || ch == 'Q' || ch == 'n' || ch == 'N') {
			var candBuf [7]byte
			candBuf[0] = ch
			ahead := peekBytes(6)
			nCand := 1 + copy(candBuf[1:], ahead)
			if prefixLen, closeSeq, ok := matchAltQuoteOpenBytes(candBuf[:nCand]); ok {
				flushWord()
				_, _ = br.Discard(prefixLen - 1)
				altQuoteClose = closeSeq
				prevIsIdentChar = false
				cur.Write(candBuf[:prefixLen])
				continue
			}
		}
		if ch == '\'' {
			flushWord()
			inSingle = true
			singleBackslashEsc = opts.BackslashEscapes
			prevIsIdentChar = false
			cur.WriteByte(ch)
			continue
		}
		if ch == '"' {
			flushWord()
			inDouble = true
			prevIsIdentChar = false
			cur.WriteByte(ch)
			continue
		}
		if opts.BacktickIdentifiers && ch == '`' {
			flushWord()
			inBacktick = true
			prevIsIdentChar = false
			cur.WriteByte(ch)
			continue
		}
		if opts.BracketIdentifiers && ch == '[' {
			flushWord()
			inBracket = true
			prevIsIdentChar = false
			cur.WriteByte(ch)
			continue
		}
		if opts.DollarQuotes && !prevIsIdentChar && ch == '$' {
			nb := peekBytes(1)
			if len(nb) > 0 {
				if nb[0] == '$' {
					flushWord()
					_, _ = br.Discard(1)
					dollarTag = "$$"
					prevIsIdentChar = false
					cur.WriteString("$$")
					continue
				}
				if isDollarTagStartByte(nb[0]) {
					var tagBuf strings.Builder
					tagBuf.WriteByte('$')
					matched := false
					for {
						b, pErr := br.Peek(1)
						if pErr != nil || len(b) == 0 {
							break
						}
						c := b[0]
						if c == '$' {
							_, _ = br.Discard(1)
							tagBuf.WriteByte('$')
							matched = true
							break
						}
						if !isDollarTagContByte(c) {
							break
						}
						_, _ = br.Discard(1)
						tagBuf.WriteByte(c)
					}
					flushWord()
					tagStr := tagBuf.String()
					cur.WriteString(tagStr)
					if matched {
						dollarTag = tagStr
						prevIsIdentChar = false
					} else {
						prevIsIdentChar = true
					}
					continue
				}
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
		prevIsIdentChar = (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' || ch >= 0x80 || (opts.AlternativeQuotes && ch == '#')

		if ch == ';' {
			prevIsIdentChar = false
			if stmtAllowsCompound && beginDepth > 0 {
				cur.WriteByte(ch)
				continue
			}
			trimmed := strings.TrimSpace(cur.String())
			cur.Reset()
			resetStmtState()
			if hasExecutableContentWithOpts(trimmed, opts) {
				if err := fn(trimmed); err != nil {
					return err
				}
			}
			continue
		}

		cur.WriteByte(ch)
	}

	flushWord()

	if trimmed := strings.TrimSpace(cur.String()); hasExecutableContentWithOpts(trimmed, opts) {
		if err := fn(trimmed); err != nil {
			return err
		}
	}
	return nil
}

func isDollarTagStartByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_' || b >= 0x80
}

func isDollarTagContByte(b byte) bool {
	return isDollarTagStartByte(b) || (b >= '0' && b <= '9')
}

func altQuoteClosingDelimiter(delim []byte) string {
	if len(delim) == 1 {
		switch delim[0] {
		case '[':
			return "]"
		case '{':
			return "}"
		case '(':
			return ")"
		case '<':
			return ">"
		}
	}
	return string(delim)
}

func matchAltQuoteOpenBytes(b []byte) (prefixLen int, closeSeq string, ok bool) {
	if len(b) < 3 {
		return 0, "", false
	}
	idx := 0
	if b[0] == 'n' || b[0] == 'N' {
		idx = 1
		if len(b) < 4 {
			return 0, "", false
		}
	}
	if (b[idx] != 'q' && b[idx] != 'Q') || b[idx+1] != '\'' {
		return 0, "", false
	}
	delimStart := idx + 2
	if delimStart >= len(b) {
		return 0, "", false
	}
	d := b[delimStart]
	if d <= ' ' || d == 0x7f {
		return 0, "", false
	}
	delimLen := 1
	if d >= 0x80 {
		if r, size := utf8.DecodeRune(b[delimStart:]); r != utf8.RuneError && size > 0 {
			delimLen = size
		}
	}
	delimEnd := delimStart + delimLen
	return delimEnd, altQuoteClosingDelimiter(b[delimStart:delimEnd]) + "'", true
}

func matchAltQuoteOpen(s string) (prefixLen int, closeSeq string, ok bool) {
	if len(s) < 3 {
		return 0, "", false
	}
	idx := 0
	if s[0] == 'n' || s[0] == 'N' {
		idx = 1
		if len(s) < 4 {
			return 0, "", false
		}
	}
	if (s[idx] != 'q' && s[idx] != 'Q') || s[idx+1] != '\'' {
		return 0, "", false
	}
	delimStart := idx + 2
	if delimStart >= len(s) {
		return 0, "", false
	}
	d := s[delimStart]
	if d <= ' ' || d == 0x7f {
		return 0, "", false
	}
	delimLen := 1
	if d >= 0x80 {
		if r, size := utf8.DecodeRuneInString(s[delimStart:]); r != utf8.RuneError && size > 0 {
			delimLen = size
		}
	}
	delimEnd := delimStart + delimLen
	closeDelim := s[delimStart:delimEnd]
	if delimLen == 1 {
		switch d {
		case '[':
			closeDelim = "]"
		case '{':
			closeDelim = "}"
		case '(':
			closeDelim = ")"
		case '<':
			closeDelim = ">"
		}
	}
	return delimEnd, closeDelim + "'", true
}

// parseExecCommentHeader inspects bytes immediately following an already-read '/'
// and returns (advanceLenAfterSlash, parsedVersion, isMaria, ok).
// Unversioned `/*!` or `/*M!` / `/*m!` returns version = 0.
// MySQL `/*!` consumes at most 5 version digits; MariaDB `/*M!` / `/*m!` consumes at most 6.
func parseExecCommentHeader(ahead []byte) (adv int, version int, isMaria bool, ok bool) {
	if len(ahead) < 2 || ahead[0] != '*' {
		return 0, 0, false, false
	}
	idx := 0
	if ahead[1] == '!' {
		idx = 2
	} else if len(ahead) >= 3 && (ahead[1] == 'M' || ahead[1] == 'm') && ahead[2] == '!' {
		idx = 3
		isMaria = true
	} else {
		return 0, 0, false, false
	}
	maxDigits := 5
	if isMaria {
		maxDigits = 6
	}
	digits := 0
	ver := 0
	for idx < len(ahead) && digits < maxDigits && ahead[idx] >= '0' && ahead[idx] <= '9' {
		ver = ver*10 + int(ahead[idx]-'0')
		idx++
		digits++
	}
	return idx, ver, isMaria, true
}

// matchExecCommentAheadBytesWithOpts inspects bytes immediately following an already-read '/'
// and returns the number of bytes in ahead belonging to an enabled MySQL (`/*!...`) or
// MariaDB (`/*M!...` / `/*m!...`) executable comment opening prefix respecting version cutoffs in opts.
func matchExecCommentAheadBytesWithOpts(ahead []byte, opts SQLSyntaxOptions) int {
	adv, ver, isMaria, ok := parseExecCommentHeader(ahead)
	if !ok {
		return 0
	}
	if isMaria {
		if !opts.MariaExecComments {
			return 0
		}
		if opts.hasMaxMariaExecVersion && ver > opts.maxMariaExecVersion {
			return 0
		}
		if !opts.hasMaxMariaExecVersion && opts.hasMaxExecVersion && ver > opts.maxExecVersion {
			return 0
		}
		return adv
	}
	if !opts.ExecComments {
		return 0
	}
	if opts.hasMaxExecVersion && ver > opts.maxExecVersion {
		return 0
	}
	return adv
}

// execCommentPrefixInfoWithOpts returns (prefixLen, version, isMaria) for a leading MySQL (`/*!...`)
// or MariaDB (`/*M!...` / `/*m!...`) executable comment opening prefix enabled by opts, or (0, 0, false).
func execCommentPrefixInfoWithOpts(s string, opts SQLSyntaxOptions) (plen int, ver int, isMaria bool) {
	if len(s) < 3 || s[0] != '/' {
		return 0, 0, false
	}
	var buf [10]byte
	n := len(s) - 1
	if n > len(buf) {
		n = len(buf)
	}
	for i := 0; i < n; i++ {
		buf[i] = s[i+1]
	}
	adv, parsedVer, maria, ok := parseExecCommentHeader(buf[:n])
	if !ok {
		return 0, 0, false
	}
	if matchExecCommentAheadBytesWithOpts(buf[:n], opts) == 0 {
		return 0, 0, false
	}
	return adv + 1, parsedVer, maria
}

// execCommentPrefixLenWithOpts returns the length of a leading MySQL (`/*!...`) or MariaDB (`/*M!...` / `/*m!...`)
// executable comment opening prefix enabled by opts (including optional 5-6 digit version), or 0.
func execCommentPrefixLenWithOpts(s string, opts SQLSyntaxOptions) int {
	plen, _, _ := execCommentPrefixInfoWithOpts(s, opts)
	return plen
}

func isMySQLDashCommentFollowByte(b byte) bool {
	return b <= ' ' || b == 0x7f
}

// isDashLineCommentAt checks whether s[i:] (starting at "--") begins a single-line comment under opts.
// When opts.DashCommentRequiresSpace is true (MySQL/MariaDB/TiDB/OceanBase), "--" starts a comment
// only when followed by EOF or a whitespace/ASCII control character (b <= ' ' || b == 0x7f).
func isDashLineCommentAt(s string, i int, opts SQLSyntaxOptions) bool {
	if i+1 >= len(s) || s[i] != '-' || s[i+1] != '-' {
		return false
	}
	if !opts.DashCommentRequiresSpace {
		return true
	}
	return i+2 >= len(s) || isMySQLDashCommentFollowByte(s[i+2])
}

func isStructuralExecWord(w string) bool {
	switch w {
	case "BEGIN", "END", "CASE",
		"CREATE", "ALTER", "DECLARE",
		"PACKAGE", "TRIGGER", "PROCEDURE", "FUNCTION", "EVENT",
		"TABLE", "INDEX", "VIEW", "DATABASE", "SCHEMA", "USER", "ROLE", "SEQUENCE", "TYPE",
		"IS", "AS",
		"IF", "LOOP", "WHILE", "REPEAT",
		"DELIMITER":
		return true
	}
	return false
}

// inspectExecCommentInterior scans s starting at start (after the opening /*! or /*M! header)
// up to the first closing */ (or EOF) and reports whether the executable comment contains
// structural splitting/nesting constructs (;, /*, --, #, compound/DDL keywords, or EOF without */)
// and whether a single/double/backtick quote is still open when */ (or EOF) is reached.
func inspectExecCommentInterior(s string, start int, opts SQLSyntaxOptions) (structural bool, unclosedQuote bool) {
	inSingle := false
	singleBackslashEsc := false
	inDouble := false
	inBacktick := false
	n := len(s)
	var wordBuf [16]byte
	wordLen := 0
	flushWord := func() {
		if wordLen == 0 {
			return
		}
		if isStructuralExecWord(strings.ToUpper(string(wordBuf[:wordLen]))) {
			structural = true
		}
		wordLen = 0
	}
	for j := start; j < n; j++ {
		ch := s[j]
		if inSingle {
			if ch == '*' && j+1 < n && s[j+1] == '/' {
				return true, true
			}
			if singleBackslashEsc && ch == '\\' && j+1 < n {
				if j+2 < n && s[j+1] == '*' && s[j+2] == '/' {
					return true, true
				}
				j++
				continue
			}
			if ch == '\'' {
				if j+1 < n && s[j+1] == '\'' {
					j++
				} else {
					inSingle = false
					singleBackslashEsc = false
				}
			}
			continue
		}
		if inDouble {
			if ch == '*' && j+1 < n && s[j+1] == '/' {
				return true, true
			}
			if opts.BackslashEscapes && ch == '\\' && j+1 < n {
				if j+2 < n && s[j+1] == '*' && s[j+2] == '/' {
					return true, true
				}
				j++
				continue
			}
			if ch == '"' {
				if j+1 < n && s[j+1] == '"' {
					j++
				} else {
					inDouble = false
				}
			}
			continue
		}
		if inBacktick {
			if ch == '*' && j+1 < n && s[j+1] == '/' {
				return true, true
			}
			if ch == '`' {
				if j+1 < n && s[j+1] == '`' {
					j++
				} else {
					inBacktick = false
				}
			}
			continue
		}
		if ch == '*' && j+1 < n && s[j+1] == '/' {
			flushWord()
			return structural, false
		}
		if (opts.HashComments && ch == '#') || isDashLineCommentAt(s, j, opts) {
			flushWord()
			structural = true
			for j < n && s[j] != '\n' {
				if !opts.preserveExecQuotes && s[j] == '*' && j+1 < n && s[j+1] == '/' {
					return true, false
				}
				j++
			}
			continue
		}
		if ch == '/' && j+1 < n && s[j+1] == '*' {
			flushWord()
			return true, false
		}
		if ch == ';' {
			flushWord()
			structural = true
			continue
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9' && wordLen > 0) || ch == '_' {
			if wordLen < len(wordBuf) {
				wordBuf[wordLen] = ch
				wordLen++
			}
			continue
		}
		flushWord()
		switch ch {
		case '\'':
			inSingle = true
			singleBackslashEsc = opts.BackslashEscapes
		case '"':
			inDouble = true
		case '`':
			if opts.BacktickIdentifiers {
				inBacktick = true
			}
		}
	}
	flushWord()
	if inSingle || inDouble || inBacktick {
		return true, true
	}
	return true, false
}

// execCommentInteriorHasUnclosedQuote scans s starting at start up to the first closing */
// and reports whether a single/double/backtick quote is still open when */ (or EOF) is reached.
func execCommentInteriorHasUnclosedQuote(s string, start int, opts SQLSyntaxOptions) bool {
	_, unclosed := inspectExecCommentInterior(s, start, opts)
	return unclosed
}

func execCommentAheadHasUnclosedQuoteBeforeClose(win []byte, adv int, opts SQLSyntaxOptions) bool {
	if adv >= len(win) {
		return false
	}
	return execCommentInteriorHasUnclosedQuote(string(win[adv:]), 0, opts)
}

func hasExecCommentCandidate(s string, opts SQLSyntaxOptions) bool {
	if opts.ExecComments && strings.Contains(s, "/*!") {
		return true
	}
	if opts.MariaExecComments && (strings.Contains(s, "/*M!") || strings.Contains(s, "/*m!")) {
		return true
	}
	return false
}

func splitStatementsWithOptions(script string, opts SQLSyntaxOptions) []string {
	var stmts []string
	_ = StreamStatementsWithOptions(strings.NewReader(script), opts, func(stmt string) error {
		stmts = append(stmts, stmt)
		return nil
	})
	return stmts
}

const (
	maxExecVersionCutoffs = 64
	maxMariaCrossPairs    = 64
	maxExtractPasses      = 64
)

func extractExecCommentVersions(s string, opts SQLSyntaxOptions) (mysqlVers []int, mariaVers []int, overflowed bool) {
	seenMySQL := make(map[int]struct{})
	seenMaria := make(map[int]struct{})

	recordVersion := func(isMaria bool, ver int) bool {
		if isMaria {
			if _, exists := seenMaria[ver]; !exists {
				if len(mariaVers) >= maxExecVersionCutoffs {
					overflowed = true
					return false
				}
				seenMaria[ver] = struct{}{}
				mariaVers = append(mariaVers, ver)
			}
			return true
		}
		if _, exists := seenMySQL[ver]; !exists {
			if len(mysqlVers) >= maxExecVersionCutoffs {
				overflowed = true
				return false
			}
			seenMySQL[ver] = struct{}{}
			mysqlVers = append(mysqlVers, ver)
		}
		return true
	}

	seenPasses := make(map[SQLSyntaxOptions]struct{})
	queue := make([]SQLSyntaxOptions, 0, 4)
	enqueue := func(p SQLSyntaxOptions) {
		if _, exists := seenPasses[p]; exists {
			return
		}
		seenPasses[p] = struct{}{}
		queue = append(queue, p)
	}

	baseNoPreserve := opts
	baseNoPreserve.preserveExecQuotes = false
	baseNoPreserve.hasMaxExecVersion = false
	baseNoPreserve.maxExecVersion = 0
	baseNoPreserve.hasMaxMariaExecVersion = false
	baseNoPreserve.maxMariaExecVersion = 0
	enqueue(baseNoPreserve)

	basePreserve := baseNoPreserve
	basePreserve.preserveExecQuotes = true
	enqueue(basePreserve)

	n := len(s)
	for passIdx := 0; passIdx < len(queue); passIdx++ {
		if passIdx >= maxExtractPasses {
			overflowed = true
			return nil, nil, true
		}
		scanOpts := queue[passIdx]
		inSingle := false
		singleBackslashEsc := false
		inDouble := false
		inBacktick := false
		inExec := false
		prevIsIdentChar := false

		for i := 0; i < n; i++ {
			ch := s[i]
			if inSingle {
				if singleBackslashEsc && ch == '\\' {
					if !scanOpts.preserveExecQuotes && inExec && i+2 < n && s[i+1] == '*' && s[i+2] == '/' {
						inSingle = false
						singleBackslashEsc = false
						inExec = false
						prevIsIdentChar = false
						i += 2
						continue
					}
					if i+1 < n {
						i++
					}
					continue
				}
				if !scanOpts.preserveExecQuotes && inExec && ch == '*' && i+1 < n && s[i+1] == '/' {
					inSingle = false
					singleBackslashEsc = false
					inExec = false
					prevIsIdentChar = false
					i++
					continue
				}
				if ch == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i++
					} else {
						inSingle = false
						singleBackslashEsc = false
					}
				}
				prevIsIdentChar = false
				continue
			}
			if inDouble {
				if scanOpts.BackslashEscapes && ch == '\\' {
					if !scanOpts.preserveExecQuotes && inExec && i+2 < n && s[i+1] == '*' && s[i+2] == '/' {
						inDouble = false
						inExec = false
						prevIsIdentChar = false
						i += 2
						continue
					}
					if i+1 < n {
						i++
					}
					continue
				}
				if !scanOpts.preserveExecQuotes && inExec && ch == '*' && i+1 < n && s[i+1] == '/' {
					inDouble = false
					inExec = false
					prevIsIdentChar = false
					i++
					continue
				}
				if ch == '"' {
					if i+1 < n && s[i+1] == '"' {
						i++
					} else {
						inDouble = false
					}
				}
				prevIsIdentChar = false
				continue
			}
			if inBacktick {
				if !scanOpts.preserveExecQuotes && inExec && ch == '*' && i+1 < n && s[i+1] == '/' {
					inBacktick = false
					inExec = false
					prevIsIdentChar = false
					i++
					continue
				}
				if ch == '`' {
					if i+1 < n && s[i+1] == '`' {
						i++
					} else {
						inBacktick = false
					}
				}
				prevIsIdentChar = false
				continue
			}
			if (scanOpts.HashComments && ch == '#') || isDashLineCommentAt(s, i, scanOpts) {
				for i < n && s[i] != '\n' {
					if !scanOpts.preserveExecQuotes && inExec && i+1 < n && s[i] == '*' && s[i+1] == '/' {
						inExec = false
						i++
						break
					}
					i++
				}
				prevIsIdentChar = false
				continue
			}
			if inExec && ch == '*' && i+1 < n && s[i+1] == '/' {
				inExec = false
				prevIsIdentChar = false
				i++
				continue
			}
			if ch == '/' && i+1 < n && s[i+1] == '*' {
				end := i + 10
				if end > n {
					end = n
				}
				var buf [9]byte
				m := copy(buf[:], s[i+1:end])
				adv, ver, isMaria, ok := parseExecCommentHeader(buf[:m])
				enabled := false
				if ok {
					if isMaria {
						enabled = scanOpts.MariaExecComments &&
							(!scanOpts.hasMaxMariaExecVersion || ver <= scanOpts.maxMariaExecVersion) &&
							(scanOpts.hasMaxMariaExecVersion || !scanOpts.hasMaxExecVersion || ver <= scanOpts.maxExecVersion)
					} else {
						enabled = scanOpts.ExecComments &&
							(!scanOpts.hasMaxExecVersion || ver <= scanOpts.maxExecVersion)
					}
				}
				if enabled {
					structural, unclosed := inspectExecCommentInterior(s, i+adv+1, scanOpts)
					if structural || unclosed {
						if !recordVersion(isMaria, ver) {
							return nil, nil, true
						}
						if scanOpts.preserveExecQuotes && unclosed && ver > 0 {
							if !recordVersion(isMaria, ver-1) {
								return nil, nil, true
							}
							nextPass := scanOpts
							if isMaria {
								if !nextPass.hasMaxMariaExecVersion || ver-1 < nextPass.maxMariaExecVersion {
									nextPass.hasMaxMariaExecVersion = true
									nextPass.maxMariaExecVersion = ver - 1
								}
							} else {
								if !nextPass.hasMaxExecVersion || ver-1 < nextPass.maxExecVersion {
									nextPass.hasMaxExecVersion = true
									nextPass.maxExecVersion = ver - 1
								}
							}
							enqueue(nextPass)
						}
					}
					inExec = true
					prevIsIdentChar = false
					i += adv
					continue
				}
				j := i + 2
				for j+1 < n && !(s[j] == '*' && s[j+1] == '/') {
					j++
				}
				if j+1 < n {
					i = j + 1
				} else {
					i = n - 1
				}
				if !scanOpts.skipNestedExecComments {
					inExec = false
				}
				prevIsIdentChar = false
				continue
			}
			if scanOpts.EscapeStringPrefix && !prevIsIdentChar && (ch == 'E' || ch == 'e') && i+1 < n && s[i+1] == '\'' {
				inSingle = true
				singleBackslashEsc = true
				prevIsIdentChar = false
				i++
				continue
			}
			switch ch {
			case '\'':
				inSingle = true
				singleBackslashEsc = scanOpts.BackslashEscapes
				prevIsIdentChar = false
			case '"':
				inDouble = true
				prevIsIdentChar = false
			case '`':
				if scanOpts.BacktickIdentifiers {
					inBacktick = true
				}
				prevIsIdentChar = false
			default:
				prevIsIdentChar = (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' || ch >= 0x80
			}
		}
	}

	sort.Ints(mysqlVers)
	sort.Ints(mariaVers)
	if opts.ExecComments && opts.MariaExecComments && len(mysqlVers)*len(mariaVers) > maxMariaCrossPairs {
		return nil, nil, true
	}
	return mysqlVers, mariaVers, overflowed
}

func execCommentOptsVariants(opts SQLSyntaxOptions, sql string) (variants []SQLSyntaxOptions, overflowed bool) {
	if !opts.ExecComments && !opts.MariaExecComments {
		return []SQLSyntaxOptions{opts}, false
	}
	variants = make([]SQLSyntaxOptions, 0, 16)
	seen := make(map[SQLSyntaxOptions]struct{}, 32)
	add := func(v SQLSyntaxOptions) {
		if _, exists := seen[v]; exists {
			return
		}
		seen[v] = struct{}{}
		variants = append(variants, v)
	}
	addFamily := func(base SQLSyntaxOptions) {
		add(base)
		nested := base
		nested.skipNestedExecComments = true
		add(nested)
		preserve := base
		preserve.preserveExecQuotes = true
		add(preserve)
		preserveNested := preserve
		preserveNested.skipNestedExecComments = true
		add(preserveNested)
	}

	addFamily(opts)
	skipUnclosed := opts
	skipUnclosed.skipUnclosedExecComments = true
	add(skipUnclosed)

	noExec := opts
	noExec.ExecComments = false
	noExec.MariaExecComments = false
	noExec.skipUnclosedExecComments = false
	noExec.skipNestedExecComments = false
	noExec.preserveExecQuotes = false
	noExec.hasMaxExecVersion = false
	noExec.maxExecVersion = 0
	noExec.hasMaxMariaExecVersion = false
	noExec.maxMariaExecVersion = 0
	add(noExec)

	if opts.ExecComments && opts.MariaExecComments {
		onlyMaria := opts
		onlyMaria.ExecComments = false
		addFamily(onlyMaria)

		onlyMySQL := opts
		onlyMySQL.MariaExecComments = false
		addFamily(onlyMySQL)
	}

	mysqlVers, mariaVers, overflowed := extractExecCommentVersions(sql, opts)
	if overflowed {
		return nil, true
	}
	if len(mysqlVers) == 0 && len(mariaVers) == 0 {
		return variants, false
	}

	jointVers := make([]int, 0, len(mysqlVers)+len(mariaVers))
	jointVers = append(jointVers, mysqlVers...)
	jointVers = append(jointVers, mariaVers...)
	sort.Ints(jointVers)
	prev := -1
	for _, ver := range jointVers {
		if ver == prev {
			continue
		}
		prev = ver
		v := opts
		if opts.ExecComments {
			v.hasMaxExecVersion = true
			v.maxExecVersion = ver
		}
		if opts.MariaExecComments {
			v.hasMaxMariaExecVersion = true
			v.maxMariaExecVersion = ver
		}
		addFamily(v)
	}

	if opts.ExecComments && opts.MariaExecComments {
		onlyMaria := opts
		onlyMaria.ExecComments = false
		for _, mv := range mariaVers {
			v := onlyMaria
			v.hasMaxMariaExecVersion = true
			v.maxMariaExecVersion = mv
			addFamily(v)
		}

		onlyMySQL := opts
		onlyMySQL.MariaExecComments = false
		for _, xv := range mysqlVers {
			v := onlyMySQL
			v.hasMaxExecVersion = true
			v.maxExecVersion = xv
			addFamily(v)
		}

		if len(mysqlVers) > 0 && len(mariaVers) > 0 {
			for _, xv := range mysqlVers {
				for _, mv := range mariaVers {
					if xv == mv {
						continue
					}
					v := opts
					v.hasMaxExecVersion = true
					v.maxExecVersion = xv
					v.hasMaxMariaExecVersion = true
					v.maxMariaExecVersion = mv
					addFamily(v)
				}
			}
		}
	}

	return variants, false
}

var errEmptySQLStatement = errors.New("empty SQL statement")

// SplitSingleStatementForEngine splits script using engine's syntax rules and verifies that
// every executable-comment evaluation variant agrees on exactly one non-empty statement.
func SplitSingleStatementForEngine(engine, script string) (string, error) {
	opts := SyntaxOptionsForEngine(engine)
	stmts := splitStatementsWithOptions(script, opts)
	if len(stmts) == 0 {
		return "", errEmptySQLStatement
	}
	if len(stmts) > 1 {
		return "", errors.New("multiple SQL statements are not allowed")
	}
	if hasExecCommentCandidate(script, opts) {
		variants, overflowed := execCommentOptsVariants(opts, script)
		if overflowed {
			return "", errors.New("statement exceeds executable-comment complexity limit")
		}
		for _, variant := range variants {
			if variant == opts {
				continue
			}
			vStmts := splitStatementsWithOptions(script, variant)
			if len(vStmts) != 1 {
				return "", errors.New("multiple SQL statements are not allowed")
			}
		}
	}
	return strings.TrimSpace(stmts[0]), nil
}

// StripLeadingSQLComments removes leading whitespace, line comments (-- and #), and regular block comments (/* ... */),
// while unwrapping MySQL/MariaDB executable comment prefixes (/*! ... */ and /*M! ... */) so security guards
// inspect the true leading SQL keyword.
func StripLeadingSQLComments(s string) string {
	return StripLeadingSQLCommentsWithOpts(s, SQLSyntaxOptions{HashComments: true, ExecComments: true, MariaExecComments: true})
}

// StripLeadingSQLCommentsForEngine behaves like StripLeadingSQLComments using lexical rules for engine.
func StripLeadingSQLCommentsForEngine(engine, s string) string {
	return StripLeadingSQLCommentsWithOpts(s, SyntaxOptionsForEngine(engine))
}

// StripLeadingSQLCommentsWithOpts behaves like StripLeadingSQLComments using opts.
func StripLeadingSQLCommentsWithOpts(s string, opts SQLSyntaxOptions) string {
	inExecComment := false
	for len(s) > 0 {
		i := 0
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' || s[i] == '\v' || s[i] == '\f') {
			i++
		}
		s = s[i:]
		if isDashLineCommentAt(s, 0, opts) || (opts.HashComments && strings.HasPrefix(s, "#")) {
			nl := strings.IndexByte(s, '\n')
			if !opts.preserveExecQuotes && inExecComment {
				if end := strings.Index(s, "*/"); end >= 0 && (nl < 0 || end < nl) {
					inExecComment = false
					s = s[end+2:]
					continue
				}
			}
			if nl >= 0 {
				s = s[nl+1:]
				continue
			}
			return ""
		}
		if opts.ExecComments || opts.MariaExecComments {
			if plen := execCommentPrefixLenWithOpts(s, opts); plen > 0 {
				if !execCommentInteriorHasUnclosedQuote(s, plen, opts) {
					inExecComment = true
					s = s[plen:]
					continue
				}
			}
			if inExecComment && strings.HasPrefix(s, "*/") {
				inExecComment = false
				s = s[2:]
				continue
			}
		}
		if strings.HasPrefix(s, "/*") {
			if opts.NestedBlockComments {
				depth := 1
				j := 2
				for j < len(s) && depth > 0 {
					if j+1 < len(s) && s[j] == '/' && s[j+1] == '*' {
						depth++
						j += 2
						continue
					}
					if j+1 < len(s) && s[j] == '*' && s[j+1] == '/' {
						depth--
						j += 2
						continue
					}
					j++
				}
				if depth == 0 {
					s = s[j:]
					continue
				}
				return ""
			}
			if end := strings.Index(s[2:], "*/"); end >= 0 {
				if !opts.NestedBlockComments {
					inExecComment = false
				}
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
	return hasExecutableContentWithOpts(s, SQLSyntaxOptions{})
}

func hasExecutableContentWithOpts(s string, opts SQLSyntaxOptions) bool {
	if s == "" {
		return false
	}
	blockDepth := 0
	inExec := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		var next byte
		if i+1 < len(s) {
			next = s[i+1]
		}
		if blockDepth > 0 {
			if opts.NestedBlockComments && ch == '/' && next == '*' {
				blockDepth++
				i++
				continue
			}
			if ch == '*' && next == '/' {
				blockDepth--
				if blockDepth == 0 && !opts.NestedBlockComments && !opts.skipNestedExecComments {
					inExec = false
				}
				i++
			}
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == '\v' || ch == '\f' {
			continue
		}
		if inExec && ch == '*' && next == '/' {
			inExec = false
			i++
			continue
		}
		if isDashLineCommentAt(s, i, opts) || (opts.HashComments && ch == '#') {
			nl := strings.IndexByte(s[i:], '\n')
			if !opts.preserveExecQuotes && inExec {
				if end := strings.Index(s[i:], "*/"); end >= 0 && (nl < 0 || end < nl) {
					inExec = false
					i += end + 1
					continue
				}
			}
			if nl >= 0 {
				i += nl
				continue
			}
			return false
		}
		if ch == '/' && next == '*' {
			if (!inExec || !opts.skipNestedExecComments) && (opts.ExecComments || opts.MariaExecComments) {
				if plen := execCommentPrefixLenWithOpts(s[i:], opts); plen > 0 {
					if opts.skipUnclosedExecComments && execCommentInteriorHasUnclosedQuote(s[i:], plen, opts) {
						blockDepth = 1
						i++
						continue
					}
					inExec = true
					i += plen - 1
					continue
				}
			}
			blockDepth = 1
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

// readOnlyArgPragmas are read-only diagnostic PRAGMAs that may be invoked with 0 args or a parenthesized argument.
var readOnlyArgPragmas = map[string]struct{}{
	"TABLE_INFO": {}, "TABLE_XINFO": {}, "INDEX_LIST": {}, "INDEX_INFO": {}, "INDEX_XINFO": {},
	"FOREIGN_KEY_LIST": {}, "TABLE_LIST": {}, "DATABASE_LIST": {}, "COLLATION_LIST": {},
	"MODULE_LIST": {}, "FUNCTION_LIST": {}, "PRAGMA_LIST": {}, "COMPILE_OPTIONS": {},
	"QUICK_CHECK": {}, "INTEGRITY_CHECK": {}, "FOREIGN_KEY_CHECK": {},
	"STATS": {}, "VERSION": {}, "DATABASE_SIZE": {}, "STORAGE_INFO": {},
	"SHOW": {}, "SHOW_TABLES": {}, "SHOW_TABLES_EXPANDED": {},
	"FUNCTIONS": {}, "COLLATIONS": {}, "PLATFORM": {},
}

// readOnlyNoArgPragmas are inspection PRAGMAs that are strictly read-only ONLY when called without arguments or '='.
var readOnlyNoArgPragmas = map[string]struct{}{
	"ENCODING": {}, "USER_VERSION": {}, "APPLICATION_ID": {}, "SCHEMA_VERSION": {}, "DATA_VERSION": {},
	"JOURNAL_MODE": {}, "SYNCHRONOUS": {}, "FOREIGN_KEYS": {}, "AUTO_VACUUM": {},
	"PAGE_SIZE": {}, "PAGE_COUNT": {}, "FREELIST_COUNT": {}, "CACHE_SIZE": {},
	"TEMP_STORE": {}, "LOCKING_MODE": {}, "BUSY_TIMEOUT": {}, "MAX_PAGE_COUNT": {},
	"SECURE_DELETE": {}, "RECURSIVE_TRIGGERS": {}, "READ_UNCOMMITTED": {},
}

type roTokKind uint8

const (
	roTokWord roTokKind = iota
	roTokQuotedIdent
	roTokString
	roTokDot
	roTokComma
	roTokSemi
	roTokLParen
	roTokRParen
	roTokAt
	roTokEq
	roTokOther
)

type execContext struct {
	active    bool
	needMySQL bool
	minMySQL  int
	needMaria bool
	minMaria  int
}

type roLexToken struct {
	kind      roTokKind
	val       string
	needMySQL bool
	minMySQL  int
	needMaria bool
	minMaria  int
}

func lexSQLForReadOnlyGuard(stmt string) (words []string, lexTokens []roLexToken, hasAssignment bool) {
	return lexSQLForReadOnlyGuardWithOpts(stmt, SQLSyntaxOptions{})
}

func lexSQLForReadOnlyGuardWithOpts(stmt string, opts SQLSyntaxOptions) (words []string, lexTokens []roLexToken, hasAssignment bool) {
	n := len(stmt)
	i := 0
	prevIsIdentChar := false
	execCtx := execContext{}
	emitTok := func(tok roLexToken) {
		if execCtx.active {
			tok.needMySQL = execCtx.needMySQL
			tok.minMySQL = execCtx.minMySQL
			tok.needMaria = execCtx.needMaria
			tok.minMaria = execCtx.minMaria
		}
		lexTokens = append(lexTokens, tok)
	}
	isIdentChar := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$'
	}
	for i < n {
		c := stmt[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f' {
			prevIsIdentChar = false
			i++
			continue
		}
		// Line comments: -- (and # when opts.HashComments is true)
		if (opts.HashComments && c == '#') || isDashLineCommentAt(stmt, i, opts) {
			for i < n && stmt[i] != '\n' {
				if !opts.preserveExecQuotes && execCtx.active && i+1 < n && stmt[i] == '*' && stmt[i+1] == '/' {
					execCtx = execContext{}
					i += 2
					break
				}
				i++
			}
			prevIsIdentChar = false
			continue
		}
		// Block comments: /* ... */ (with optional nesting and MySQL/MariaDB /*! ... */ unwrapping)
		if c == '/' && i+1 < n && stmt[i+1] == '*' {
			if (!execCtx.active || !opts.skipNestedExecComments) && (opts.ExecComments || opts.MariaExecComments) {
				if plen, ver, isMaria := execCommentPrefixInfoWithOpts(stmt[i:], opts); plen > 0 {
					if !(opts.skipUnclosedExecComments && execCommentInteriorHasUnclosedQuote(stmt[i:], plen, opts)) {
						if !execCtx.active {
							execCtx = execContext{active: true}
						}
						if isMaria {
							if !execCtx.needMaria || ver > execCtx.minMaria {
								execCtx.minMaria = ver
							}
							execCtx.needMaria = true
						} else {
							if !execCtx.needMySQL || ver > execCtx.minMySQL {
								execCtx.minMySQL = ver
							}
							execCtx.needMySQL = true
						}
						prevIsIdentChar = false
						i += plen
						continue
					}
				}
			}
			i += 2
			if opts.NestedBlockComments {
				depth := 1
				for i < n && depth > 0 {
					if i+1 < n && stmt[i] == '/' && stmt[i+1] == '*' {
						depth++
						i += 2
						continue
					}
					if i+1 < n && stmt[i] == '*' && stmt[i+1] == '/' {
						depth--
						i += 2
						continue
					}
					i++
				}
			} else {
				for i+1 < n && !(stmt[i] == '*' && stmt[i+1] == '/') {
					i++
				}
				if i+1 < n {
					i += 2
				} else {
					i = n
				}
				if !opts.skipNestedExecComments {
					execCtx = execContext{}
				}
			}
			prevIsIdentChar = false
			continue
		}
		if execCtx.active && c == '*' && i+1 < n && stmt[i+1] == '/' {
			execCtx = execContext{}
			prevIsIdentChar = false
			i += 2
			continue
		}
		// Single-quoted string literals '...' (and PostgreSQL/DuckDB E'...' / e'...')
		if c == '\'' || (opts.EscapeStringPrefix && !prevIsIdentChar && (c == 'E' || c == 'e') && i+1 < n && stmt[i+1] == '\'') {
			escMode := opts.BackslashEscapes
			if c != '\'' {
				escMode = true
				i++ // consume E/e
			}
			i++ // consume opening '
			clamped := false
			tokCtx := execCtx
			for i < n {
				if escMode && stmt[i] == '\\' {
					if !opts.preserveExecQuotes && execCtx.active && i+2 < n && stmt[i+1] == '*' && stmt[i+2] == '/' {
						i++
						continue
					}
					if i+1 < n {
						i += 2
					} else {
						i++
					}
					continue
				}
				if !opts.preserveExecQuotes && execCtx.active && stmt[i] == '*' && i+1 < n && stmt[i+1] == '/' {
					execCtx = execContext{}
					clamped = true
					i += 2
					break
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
			if !clamped {
				tok := roLexToken{kind: roTokString}
				if tokCtx.active {
					tok.needMySQL = tokCtx.needMySQL
					tok.minMySQL = tokCtx.minMySQL
					tok.needMaria = tokCtx.needMaria
					tok.minMaria = tokCtx.minMaria
				}
				lexTokens = append(lexTokens, tok)
			}
			prevIsIdentChar = false
			continue
		}
		// Quoted identifiers: "..." (and `...` when opts.BacktickIdentifiers is true)
		if c == '"' || (opts.BacktickIdentifiers && c == '`') {
			quote := c
			i++
			clamped := false
			tokCtx := execCtx
			var identBuf strings.Builder
			for i < n {
				if opts.BackslashEscapes && quote == '"' && stmt[i] == '\\' && i+1 < n {
					if !opts.preserveExecQuotes && execCtx.active && i+2 < n && stmt[i+1] == '*' && stmt[i+2] == '/' {
						i++
						continue
					}
					identBuf.WriteByte(stmt[i+1])
					i += 2
					continue
				}
				if !opts.preserveExecQuotes && execCtx.active && stmt[i] == '*' && i+1 < n && stmt[i+1] == '/' {
					execCtx = execContext{}
					clamped = true
					i += 2
					break
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
			if !clamped {
				tok := roLexToken{kind: roTokQuotedIdent, val: strings.ToUpper(identBuf.String())}
				if tokCtx.active {
					tok.needMySQL = tokCtx.needMySQL
					tok.minMySQL = tokCtx.minMySQL
					tok.needMaria = tokCtx.needMaria
					tok.minMaria = tokCtx.minMaria
				}
				lexTokens = append(lexTokens, tok)
			}
			prevIsIdentChar = false
			continue
		}
		// Bracket-quoted identifiers: [...] (SQL Server / SQLite / generic only)
		if c == '[' && opts.BracketIdentifiers {
			i++
			var identBuf strings.Builder
			for i < n {
				if stmt[i] == ']' {
					if opts.BracketDoubleEscape && i+1 < n && stmt[i+1] == ']' {
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
			emitTok(roLexToken{kind: roTokQuotedIdent, val: strings.ToUpper(identBuf.String())})
			prevIsIdentChar = false
			continue
		}
		// Oracle / Dameng alternative quoting literals: q'<d>...<close_d>' / Q'...' / nq'...' / NQ'...'
		if opts.AlternativeQuotes && !prevIsIdentChar && (c == 'q' || c == 'Q' || c == 'n' || c == 'N') {
			if prefixLen, closeSeq, ok := matchAltQuoteOpen(stmt[i:]); ok {
				closeIdx := strings.Index(stmt[i+prefixLen:], closeSeq)
				if closeIdx >= 0 {
					i += prefixLen + closeIdx + len(closeSeq)
					emitTok(roLexToken{kind: roTokString})
					prevIsIdentChar = false
					continue
				}
			}
		}
		// Dollar-quoted strings in PostgreSQL/KingbaseES/DuckDB: $$...$$ or $tag$...$tag$
		if opts.DollarQuotes && !prevIsIdentChar && c == '$' {
			if tag, ok := matchSQLDollarTag(stmt[i:]); ok {
				closeIdx := strings.Index(stmt[i+len(tag):], tag)
				if closeIdx >= 0 {
					i += len(tag) + closeIdx + len(tag)
					emitTok(roLexToken{kind: roTokString})
					prevIsIdentChar = false
					continue
				}
			}
		}
		if c == '=' {
			hasAssignment = true
			emitTok(roLexToken{kind: roTokEq})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == '@' {
			emitTok(roLexToken{kind: roTokAt})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == '.' {
			emitTok(roLexToken{kind: roTokDot})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == ',' {
			emitTok(roLexToken{kind: roTokComma})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == ';' {
			emitTok(roLexToken{kind: roTokSemi})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == '(' {
			emitTok(roLexToken{kind: roTokLParen})
			prevIsIdentChar = false
			i++
			continue
		}
		if c == ')' {
			emitTok(roLexToken{kind: roTokRParen})
			prevIsIdentChar = false
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
				emitTok(roLexToken{kind: roTokWord, val: up})
			} else {
				emitTok(roLexToken{kind: roTokOther})
			}
			prevIsIdentChar = true
			continue
		}
		emitTok(roLexToken{kind: roTokOther})
		prevIsIdentChar = c >= 0x80 || (opts.AlternativeQuotes && c == '#')
		i++
	}
	return words, lexTokens, hasAssignment
}

func execTokenSkippable(u roLexToken, minX int, minM int) bool {
	if u.needMySQL && u.minMySQL > minX {
		return true
	}
	if u.needMaria && u.minMaria > minM {
		return true
	}
	return false
}

type bJumpInfo struct {
	valid    bool
	stopIdx  int
	minX     int
	minM     int
	hasMixed bool
}

// hasMonotonicTokenPair checks in O(N) time whether any token matching matchA followed by a token
// matching matchB can become adjacent under some monotonic MySQL/MariaDB server version pair (X, M),
// where every intervening token lies inside an executable comment skipped at (X, M).
func hasMonotonicTokenPair(lexTokens []roLexToken, matchA, matchB func(roLexToken) bool) (roLexToken, bool) {
	if len(lexTokens) < 2 {
		return roLexToken{}, false
	}
	jumps := make([]bJumpInfo, len(lexTokens))
	for j := 0; j < len(lexTokens); j++ {
		b := lexTokens[j]
		if !matchB(b) {
			continue
		}
		curMinX := -1
		if b.needMySQL {
			curMinX = b.minMySQL
		}
		curMinM := -1
		if b.needMaria {
			curMinM = b.minMaria
		}
		minBlockedX := math.MaxInt
		minBlockedM := math.MaxInt
		var mixedBlockers []roLexToken
		stopIdx := -1

		for k := j - 1; k >= 0; k-- {
			u := lexTokens[k]
			if matchA(u) {
				reqX := curMinX
				if u.needMySQL && u.minMySQL > reqX {
					reqX = u.minMySQL
				}
				reqM := curMinM
				if u.needMaria && u.minMaria > reqM {
					reqM = u.minMaria
				}
				if reqX < minBlockedX && reqM < minBlockedM {
					allSkipped := true
					for _, mb := range mixedBlockers {
						if !execTokenSkippable(mb, reqX, reqM) {
							allSkipped = false
							break
						}
					}
					if allSkipped {
						return u, true
					}
				}
			}

			if jumps[k].valid && !jumps[k].hasMixed {
				if b.needMySQL && !b.needMaria && u.needMySQL && !u.needMaria && u.minMySQL > b.minMySQL {
					if u.minMySQL < minBlockedX {
						minBlockedX = u.minMySQL
					}
					if jumps[k].minX < minBlockedX {
						minBlockedX = jumps[k].minX
					}
					if jumps[k].minM < minBlockedM {
						minBlockedM = jumps[k].minM
					}
					if jumps[k].stopIdx < 0 {
						stopIdx = -1
						break
					}
					k = jumps[k].stopIdx + 1
					continue
				}
				if b.needMaria && !b.needMySQL && u.needMaria && !u.needMySQL && u.minMaria > b.minMaria {
					if u.minMaria < minBlockedM {
						minBlockedM = u.minMaria
					}
					if jumps[k].minX < minBlockedX {
						minBlockedX = jumps[k].minX
					}
					if jumps[k].minM < minBlockedM {
						minBlockedM = jumps[k].minM
					}
					if jumps[k].stopIdx < 0 {
						stopIdx = -1
						break
					}
					k = jumps[k].stopIdx + 1
					continue
				}
			}

			if !execTokenSkippable(u, curMinX, curMinM) {
				stopIdx = k
				break
			}
			if u.needMySQL && !u.needMaria {
				if u.minMySQL < minBlockedX {
					minBlockedX = u.minMySQL
				}
			} else if u.needMaria && !u.needMySQL {
				if u.minMaria < minBlockedM {
					minBlockedM = u.minMaria
				}
			} else {
				mixedBlockers = append(mixedBlockers, u)
			}
		}

		if (b.needMySQL && !b.needMaria) || (b.needMaria && !b.needMySQL) {
			jumps[j] = bJumpInfo{
				valid:    true,
				stopIdx:  stopIdx,
				minX:     minBlockedX,
				minM:     minBlockedM,
				hasMixed: len(mixedBlockers) > 0,
			}
		}
	}
	return roLexToken{}, false
}

var allowedReadOnlyFirstWords = map[string]struct{}{
	"SELECT":   {},
	"SHOW":     {},
	"DESCRIBE": {},
	"DESC":     {},
	"VALUES":   {},
	"TABLE":    {},
	"WITH":     {},
	"EXPLAIN":  {},
	"PRAGMA":   {},
}

func hasMonotonicDisallowedFirstWord(lexTokens []roLexToken) (bool, string) {
	minPrevX := math.MaxInt
	minPrevM := math.MaxInt
	var mixedPrev []roLexToken
	for _, tok := range lexTokens {
		if tok.kind != roTokWord {
			continue
		}
		if _, allowed := allowedReadOnlyFirstWords[tok.val]; !allowed {
			reqX := -1
			if tok.needMySQL {
				reqX = tok.minMySQL
			}
			reqM := -1
			if tok.needMaria {
				reqM = tok.minMaria
			}
			if reqX < minPrevX && reqM < minPrevM {
				allSkipped := true
				for _, mb := range mixedPrev {
					if !execTokenSkippable(mb, reqX, reqM) {
						allSkipped = false
						break
					}
				}
				if allSkipped {
					return true, tok.val
				}
			}
		}
		if !tok.needMySQL && !tok.needMaria {
			return false, ""
		}
		if tok.needMySQL && !tok.needMaria {
			if tok.minMySQL < minPrevX {
				minPrevX = tok.minMySQL
			}
		} else if tok.needMaria && !tok.needMySQL {
			if tok.minMaria < minPrevM {
				minPrevM = tok.minMaria
			}
		} else {
			mixedPrev = append(mixedPrev, tok)
		}
	}
	return false, ""
}

// IsReadOnlySafeStatement validates that a single SQL statement is strictly read-only
// using standard SQL single-quote rules.
func IsReadOnlySafeStatement(stmt string) (bool, string) {
	return IsReadOnlySafeStatementForEngine("", stmt)
}

// IsReadOnlySafeStatementForEngine validates that a single SQL statement is strictly read-only
// using lexical rules for engine.
func IsReadOnlySafeStatementForEngine(engine, stmt string) (bool, string) {
	opts := SyntaxOptionsForEngine(engine)
	if !hasExecCommentCandidate(stmt, opts) {
		return isReadOnlySafeStatementWithOpts(stmt, opts)
	}
	variants, overflowed := execCommentOptsVariants(opts, stmt)
	if overflowed {
		return false, "statement exceeds executable-comment complexity limit"
	}
	for _, variant := range variants {
		if ok, reason := isReadOnlySafeStatementWithOpts(stmt, variant); !ok {
			return false, reason
		}
		if variant != opts {
			subStmts := splitStatementsWithOptions(stmt, variant)
			for _, sub := range subStmts {
				if ok, reason := isReadOnlySafeStatementWithOpts(sub, variant); !ok {
					return false, reason
				}
			}
		}
	}
	return true, ""
}

func isReadOnlySafeStatementWithOpts(stmt string, opts SQLSyntaxOptions) (bool, string) {
	tokens, lexTokens, hasAssign := lexSQLForReadOnlyGuardWithOpts(stmt, opts)
	if len(tokens) == 0 {
		return true, ""
	}
	if opts.ExecComments || opts.MariaExecComments {
		if badFirst, word := hasMonotonicDisallowedFirstWord(lexTokens); badFirst {
			return false, fmt.Sprintf("statement %q is not permitted in read-only mode", word)
		}
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

		if opts.ExecComments || opts.MariaExecComments {
			if _, locked := hasMonotonicTokenPair(
				lexTokens,
				func(t roLexToken) bool { return t.kind == roTokWord && t.val == "FOR" },
				func(t roLexToken) bool {
					return t.kind == roTokWord && (t.val == "UPDATE" || t.val == "SHARE" || t.val == "NO" || t.val == "KEY")
				},
			); locked {
				return false, "locking SELECT clauses (FOR UPDATE / FOR SHARE) are not permitted in read-only mode"
			}
			if fnTok, badCall := hasMonotonicTokenPair(
				lexTokens,
				func(t roLexToken) bool {
					if t.kind != roTokWord && t.kind != roTokQuotedIdent {
						return false
					}
					_, callOnly := callOnlyReadOnlyFunctions[t.val]
					return callOnly
				},
				func(t roLexToken) bool { return t.kind == roTokLParen },
			); badCall {
				return false, fmt.Sprintf("function %q is not permitted in read-only mode", fnTok.val)
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
		if len(lexTokens) < 2 || lexTokens[0].kind != roTokWord || lexTokens[0].val != "PRAGMA" {
			return false, "invalid PRAGMA syntax in read-only mode"
		}
		if lexTokens[1].kind != roTokWord && lexTokens[1].kind != roTokQuotedIdent {
			return false, "invalid PRAGMA target in read-only mode"
		}
		pragmaName := lexTokens[1].val
		argStart := 2
		if len(lexTokens) > 2 && lexTokens[2].kind == roTokDot {
			if len(lexTokens) < 4 || (lexTokens[3].kind != roTokWord && lexTokens[3].kind != roTokQuotedIdent) {
				return false, "invalid schema-qualified PRAGMA syntax in read-only mode"
			}
			if len(lexTokens) > 4 && lexTokens[4].kind == roTokDot {
				return false, "multi-dot PRAGMA identifiers are not permitted in read-only mode"
			}
			pragmaName = lexTokens[3].val
			argStart = 4
		}
		if argStart < len(lexTokens) {
			if lexTokens[argStart].kind != roTokLParen || lexTokens[len(lexTokens)-1].kind != roTokRParen {
				return false, fmt.Sprintf("PRAGMA %s has invalid syntax for read-only mode", pragmaName)
			}
			depth := 0
			for idx := argStart; idx < len(lexTokens); idx++ {
				switch lexTokens[idx].kind {
				case roTokLParen:
					depth++
				case roTokRParen:
					depth--
					if depth == 0 && idx != len(lexTokens)-1 {
						return false, fmt.Sprintf("PRAGMA %s has unexpected trailing tokens in read-only mode", pragmaName)
					}
					if depth < 0 {
						return false, fmt.Sprintf("PRAGMA %s has invalid syntax for read-only mode", pragmaName)
					}
				}
			}
			if depth != 0 {
				return false, fmt.Sprintf("PRAGMA %s has unbalanced parentheses in read-only mode", pragmaName)
			}
		}
		if _, ok := readOnlyArgPragmas[pragmaName]; ok {
			return checkNoForbidden()
		}
		if _, ok := readOnlyNoArgPragmas[pragmaName]; ok {
			if argStart != len(lexTokens) {
				return false, fmt.Sprintf("PRAGMA %s with arguments is not permitted in read-only mode", pragmaName)
			}
			return checkNoForbidden()
		}
		return false, fmt.Sprintf("PRAGMA %s is not permitted in read-only mode", pragmaName)
	default:
		return false, fmt.Sprintf("statement %q is not permitted in read-only mode", first)
	}
}

// ValidateReadOnlyScript validates every statement in a potentially multi-statement SQL script
// using standard SQL single-quote rules.
func ValidateReadOnlyScript(script string) error {
	return ValidateReadOnlyScriptForEngine("", script)
}

// ValidateReadOnlyScriptForEngine validates every statement in script using lexical rules for engine.
func ValidateReadOnlyScriptForEngine(engine, script string) error {
	opts := SyntaxOptionsForEngine(engine)
	if !hasExecCommentCandidate(script, opts) {
		return validateReadOnlyScriptWithOpts(script, opts)
	}
	variants, overflowed := execCommentOptsVariants(opts, script)
	if overflowed {
		return errors.New("statement exceeds executable-comment complexity limit")
	}
	for _, variant := range variants {
		if err := validateReadOnlyScriptWithOpts(script, variant); err != nil {
			return err
		}
	}
	return nil
}

func validateReadOnlyScriptWithOpts(script string, opts SQLSyntaxOptions) error {
	stmts := splitStatementsWithOptions(script, opts)
	if len(stmts) == 0 {
		if ok, reason := isReadOnlySafeStatementWithOpts(script, opts); !ok {
			return errors.New(reason)
		}
		return nil
	}
	for _, stmt := range stmts {
		if ok, reason := isReadOnlySafeStatementWithOpts(stmt, opts); !ok {
			return errors.New(reason)
		}
	}
	return nil
}
