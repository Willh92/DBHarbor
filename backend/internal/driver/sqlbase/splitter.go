package sqlbase

import (
	"bufio"
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
	firstByte := true

	var wordBuf [32]byte
	wordLen := 0
	stmtWordIdx := 0
	sawCreate := false
	stmtAllowsCompound := false
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
			case "TRIGGER", "PROCEDURE", "FUNCTION", "PACKAGE", "EVENT":
				stmtAllowsCompound = true
			case "TABLE", "INDEX", "VIEW", "DATABASE", "SCHEMA", "USER", "ROLE", "SEQUENCE", "TYPE":
				sawCreate = false
			}
		}
		if !stmtAllowsCompound {
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

// StripLeadingSQLComments removes leading whitespace, line comments (-- and #), and block comments (/* ... */)
// from a SQL statement so security guards can inspect the true leading SQL keyword.
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
			inBlock = true
			i++
			continue
		}
		return true
	}
	return false
}
