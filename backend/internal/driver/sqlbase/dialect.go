package sqlbase

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"dbharbor/backend/internal/driver"
)

type Dialect string

const (
	DialectMySQL      Dialect = "mysql"
	DialectPostgres   Dialect = "postgres"
	DialectClickHouse Dialect = "clickhouse"
	DialectSQLite     Dialect = "sqlite"
	DialectSQLServer  Dialect = "sqlserver"
	DialectOracle     Dialect = "oracle"
	DialectDameng     Dialect = "dameng"
)

func QuoteIdent(d Dialect, ident string) string {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return ""
	}
	switch d {
	case DialectMySQL, DialectClickHouse:
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	case DialectSQLServer:
		return "[" + strings.ReplaceAll(ident, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
	}
}

func QualifyTable(d Dialect, database, schema, table string) string {
	switch d {
	case DialectMySQL, DialectClickHouse:
		if database != "" {
			return QuoteIdent(d, database) + "." + QuoteIdent(d, table)
		}
		return QuoteIdent(d, table)
	case DialectPostgres, DialectSQLServer, DialectOracle, DialectDameng:
		if schema != "" {
			return QuoteIdent(d, schema) + "." + QuoteIdent(d, table)
		}
		return QuoteIdent(d, table)
	default:
		return QuoteIdent(d, table)
	}
}

func FormatLiteral(val any) string {
	if val == nil {
		return "NULL"
	}
	switch v := val.(type) {
	case bool:
		if v {
			return "1"
		}
		return "0"
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		escaped := strings.ReplaceAll(v, "'", "''")
		return "'" + escaped + "'"
	default:
		s := fmt.Sprintf("%v", v)
		escaped := strings.ReplaceAll(s, "'", "''")
		return "'" + escaped + "'"
	}
}

func FormatLiteralForDialect(d Dialect, val any) string {
	if b, ok := val.(bool); ok && (d == DialectPostgres || d == DialectClickHouse || d == DialectSQLite) {
		if b {
			return "TRUE"
		}
		return "FALSE"
	}
	if s, ok := val.(string); ok && (d == DialectMySQL || d == DialectClickHouse) {
		// MySQL and ClickHouse treat backslash as escape inside single-quoted string literals
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, "'", "''")
		escaped = strings.ReplaceAll(escaped, "\x00", `\0`)
		escaped = strings.ReplaceAll(escaped, "\r", `\r`)
		escaped = strings.ReplaceAll(escaped, "\n", `\n`)
		return "'" + escaped + "'"
	}
	return FormatLiteral(val)
}

// ContainsUnquotedSQLMeta reports whether expr contains unquoted ';', '#', '--', '/*',
// or unclosed single/double/backtick/bracket quotes.
func ContainsUnquotedSQLMeta(expr string) bool {
	return containsUnquotedSQLMeta(expr)
}

func containsUnquotedSQLMeta(expr string) bool {
	inSingle := false
	inDouble := false
	inBacktick := false
	inBracket := false
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if ch == 0 {
			return true
		}
		if inSingle {
			if ch == '\\' && i+1 < len(expr) {
				i++
				continue
			}
			if ch == '\'' {
				if i+1 < len(expr) && expr[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				if i+1 < len(expr) && expr[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
			continue
		}
		if inBacktick {
			if ch == '`' {
				if i+1 < len(expr) && expr[i+1] == '`' {
					i++
				} else {
					inBacktick = false
				}
			}
			continue
		}
		if inBracket {
			if ch == ']' {
				if i+1 < len(expr) && expr[i+1] == ']' {
					i++
				} else {
					inBracket = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '[':
			inBracket = true
		case ';', '#':
			return true
		case '-':
			if i+1 < len(expr) && expr[i+1] == '-' {
				return true
			}
		case '/':
			if i+1 < len(expr) && expr[i+1] == '*' {
				return true
			}
		}
	}
	return inSingle || inDouble || inBacktick || inBracket
}

func validateDDLFragment(label, val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return nil
	}
	if containsUnquotedSQLMeta(trimmed) {
		return fmt.Errorf("invalid %s: semicolons, unclosed quotes, or SQL comments are not allowed", label)
	}
	return nil
}

// ValidateColumnType strictly validates a SQL column data type specification.
// Only alphanumeric characters, spaces, underscores, commas, periods, quotes, and balanced
// parentheses/brackets are allowed; SQL comments (--, /*, #) and semicolons are rejected.
func ValidateColumnType(colType string) error {
	trimmed := strings.TrimSpace(colType)
	if trimmed == "" {
		return errors.New("column type cannot be empty")
	}
	if containsUnquotedSQLMeta(trimmed) {
		return errors.New("invalid column type: semicolons, unclosed quotes, or SQL comments are not allowed")
	}
	parenDepth := 0
	bracketDepth := 0
	inSingle := false
	inDouble := false
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if ch == '\r' || ch == '\n' || ch == 0 {
			return errors.New("invalid column type: control characters are not allowed")
		}
		if inSingle {
			if ch == '\\' && i+1 < len(trimmed) {
				i++
				continue
			}
			if ch == '\'' {
				if i+1 < len(trimmed) && trimmed[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				if i+1 < len(trimmed) && trimmed[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
			continue
		}
		switch {
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'):
		case ch == ' ' || ch == '_' || ch == ',' || ch == '.':
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == '(':
			parenDepth++
		case ch == ')':
			parenDepth--
			if parenDepth < 0 {
				return errors.New("invalid column type: unbalanced parentheses")
			}
		case ch == '[':
			bracketDepth++
		case ch == ']':
			bracketDepth--
			if bracketDepth < 0 {
				return errors.New("invalid column type: unbalanced brackets")
			}
		default:
			return fmt.Errorf("invalid column type %q: disallowed character %q", colType, ch)
		}
	}
	if inSingle || inDouble || parenDepth != 0 || bracketDepth != 0 {
		return errors.New("invalid column type: unclosed quotes or unbalanced parentheses")
	}
	return nil
}

// ValidateSQLExpression ensures expr is non-empty, contains no semicolons or SQL comments,
// has no unclosed quotes, and has strictly balanced parentheses.
func ValidateSQLExpression(label, expr string) error {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if containsUnquotedSQLMeta(trimmed) {
		return fmt.Errorf("invalid %s: semicolons, unclosed quotes, or SQL comments are not allowed", label)
	}
	parenDepth := 0
	inSingle := false
	inDouble := false
	inBacktick := false
	inBracket := false
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if ch == 0 {
			return fmt.Errorf("invalid %s: null byte is not allowed", label)
		}
		if inSingle {
			if ch == '\\' && i+1 < len(trimmed) {
				i++
				continue
			}
			if ch == '\'' {
				if i+1 < len(trimmed) && trimmed[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				if i+1 < len(trimmed) && trimmed[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
			continue
		}
		if inBacktick {
			if ch == '`' {
				if i+1 < len(trimmed) && trimmed[i+1] == '`' {
					i++
				} else {
					inBacktick = false
				}
			}
			continue
		}
		if inBracket {
			if ch == ']' {
				if i+1 < len(trimmed) && trimmed[i+1] == ']' {
					i++
				} else {
					inBracket = false
				}
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '[':
			inBracket = true
		case '(':
			parenDepth++
		case ')':
			parenDepth--
			if parenDepth < 0 {
				return fmt.Errorf("invalid %s: unbalanced parentheses", label)
			}
		}
	}
	if inSingle || inDouble || inBacktick || inBracket || parenDepth != 0 {
		return fmt.Errorf("invalid %s: unclosed quotes or unbalanced parentheses", label)
	}
	if len(SplitStatements("SELECT "+trimmed)) > 1 {
		return fmt.Errorf("invalid %s: multiple SQL statements are not allowed", label)
	}
	return nil
}

func stripOuterParens(expr string) string {
	s := strings.TrimSpace(expr)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth := 0
		inSingle := false
		inDouble := false
		wrapsAll := true
		for i := 0; i < len(s); i++ {
			ch := s[i]
			if inSingle {
				if ch == '\\' && i+1 < len(s) {
					i++
					continue
				}
				if ch == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i++
					} else {
						inSingle = false
					}
				}
				continue
			}
			if inDouble {
				if ch == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						i++
					} else {
						inDouble = false
					}
				}
				continue
			}
			switch ch {
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i < len(s)-1 {
					wrapsAll = false
				}
			}
			if !wrapsAll {
				break
			}
		}
		if !wrapsAll || depth != 0 {
			break
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func formatGeneratedColumnClause(d Dialect, col driver.ColumnDef) (clause string, err error) {
	if err := ValidateSQLExpression("generated column expression", col.GeneratedExpr); err != nil {
		return "", err
	}
	inner := stripOuterParens(col.GeneratedExpr)
	storage := strings.ToUpper(strings.TrimSpace(col.GeneratedStorage))
	if d == DialectSQLServer {
		if storage == "STORED" || storage == "PERSISTED" {
			return fmt.Sprintf("AS (%s) PERSISTED", inner), nil
		}
		return fmt.Sprintf("AS (%s)", inner), nil
	}
	if d == DialectPostgres {
		if storage != "VIRTUAL" {
			storage = "STORED"
		}
		return fmt.Sprintf("GENERATED ALWAYS AS (%s) %s", inner, storage), nil
	}
	if storage == "STORED" {
		return fmt.Sprintf("GENERATED ALWAYS AS (%s) STORED", inner), nil
	}
	return fmt.Sprintf("GENERATED ALWAYS AS (%s) VIRTUAL", inner), nil
}

func buildCheckConstraintClause(d Dialect, chk driver.CheckConstraintDef) (string, error) {
	if err := ValidateSQLExpression("CHECK constraint expression", chk.Expression); err != nil {
		return "", err
	}
	inner := stripOuterParens(chk.Expression)
	if strings.TrimSpace(chk.Name) != "" {
		return fmt.Sprintf("CONSTRAINT %s CHECK (%s)", QuoteIdent(d, chk.Name), inner), nil
	}
	return fmt.Sprintf("CHECK (%s)", inner), nil
}

// BuildSQLServerExtendedPropertySQL builds an idempotent T-SQL statement (without internal semicolons)
// that updates or adds the MS_Description extended property on a table or column.
func BuildSQLServerExtendedPropertySQL(schema, table, column, comment string) string {
	sc := strings.TrimSpace(schema)
	if sc == "" {
		sc = "dbo"
	}
	nv := func(s string) string {
		clean := strings.ReplaceAll(s, "\x00", "")
		return "N'" + strings.ReplaceAll(clean, "'", "''") + "'"
	}
	if strings.TrimSpace(column) == "" {
		return fmt.Sprintf(
			"IF EXISTS (SELECT 1 FROM sys.fn_listextendedproperty(N'MS_Description', N'SCHEMA', %s, N'TABLE', %s, NULL, NULL)) "+
				"EXEC sp_updateextendedproperty @name = N'MS_Description', @value = %s, @level0type = N'SCHEMA', @level0name = %s, @level1type = N'TABLE', @level1name = %s "+
				"ELSE EXEC sp_addextendedproperty @name = N'MS_Description', @value = %s, @level0type = N'SCHEMA', @level0name = %s, @level1type = N'TABLE', @level1name = %s",
			nv(sc), nv(table),
			nv(comment), nv(sc), nv(table),
			nv(comment), nv(sc), nv(table),
		)
	}
	return fmt.Sprintf(
		"IF EXISTS (SELECT 1 FROM sys.fn_listextendedproperty(N'MS_Description', N'SCHEMA', %s, N'TABLE', %s, N'COLUMN', %s)) "+
			"EXEC sp_updateextendedproperty @name = N'MS_Description', @value = %s, @level0type = N'SCHEMA', @level0name = %s, @level1type = N'TABLE', @level1name = %s, @level2type = N'COLUMN', @level2name = %s "+
			"ELSE EXEC sp_addextendedproperty @name = N'MS_Description', @value = %s, @level0type = N'SCHEMA', @level0name = %s, @level1type = N'TABLE', @level1name = %s, @level2type = N'COLUMN', @level2name = %s",
		nv(sc), nv(table), nv(column),
		nv(comment), nv(sc), nv(table), nv(column),
		nv(comment), nv(sc), nv(table), nv(column),
	)
}

func BuildBrowseQuery(d Dialect, req driver.BrowseDataRequest) (dataSQL string, countSQL string, err error) {
	if strings.TrimSpace(req.Table) == "" {
		return "", "", errors.New("table name is required")
	}
	page := req.Page
	if page < 1 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 2000 {
		pageSize = 2000
	}
	offset := (page - 1) * pageSize

	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	wherePart := ""
	if f := strings.TrimSpace(req.Filter); f != "" {
		if containsUnquotedSQLMeta(f) || len(SplitStatements("SELECT 1 WHERE "+f)) > 1 {
			return "", "", errors.New("semicolons, SQL comments, or unclosed quotes are not allowed in WHERE filter")
		}
		wherePart = " WHERE (" + f + ")"
	}

	orderParts := make([]string, 0, len(req.Sorts))
	for _, s := range req.Sorts {
		if strings.TrimSpace(s.Column) == "" {
			continue
		}
		dir := "ASC"
		if strings.EqualFold(s.Direction, "DESC") {
			dir = "DESC"
		}
		orderParts = append(orderParts, QuoteIdent(d, s.Column)+" "+dir)
	}
	orderClause := ""
	if len(orderParts) > 0 {
		orderClause = " ORDER BY " + strings.Join(orderParts, ", ")
	}

	countSQL = fmt.Sprintf("SELECT COUNT(*) FROM %s%s", tbl, wherePart)

	switch d {
	case DialectSQLServer:
		if orderClause == "" {
			orderClause = " ORDER BY (SELECT NULL)"
		}
		dataSQL = fmt.Sprintf("SELECT * FROM %s%s%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", tbl, wherePart, orderClause, offset, pageSize)
	case DialectOracle:
		dataSQL = fmt.Sprintf("SELECT * FROM %s%s%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", tbl, wherePart, orderClause, offset, pageSize)
	default:
		selectCols := "*"
		if d == DialectSQLite {
			selectCols = "rowid AS __dbharbor_rowid__, *"
		}
		dataSQL = fmt.Sprintf("SELECT %s FROM %s%s%s LIMIT %d OFFSET %d", selectCols, tbl, wherePart, orderClause, pageSize, offset)
	}
	return dataSQL, countSQL, nil
}

func BuildWhereFromLocator(d Dialect, locator map[string]any) (string, error) {
	if len(locator) == 0 {
		return "", errors.New("missing row locator condition (primary key recommended)")
	}
	keys := make([]string, 0, len(locator))
	for k := range locator {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	conds := make([]string, 0, len(keys))
	for _, k := range keys {
		v := locator[k]
		col := QuoteIdent(d, k)
		if k == "__dbharbor_rowid__" && d == DialectSQLite {
			col = "rowid"
		}
		if v == nil {
			conds = append(conds, col+" IS NULL")
		} else {
			conds = append(conds, fmt.Sprintf("%s = %s", col, FormatLiteralForDialect(d, v)))
		}
	}
	return strings.Join(conds, " AND "), nil
}

func BuildMutationSQLs(d Dialect, req driver.RowMutationRequest) ([]string, error) {
	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	var sqls []string

	// 1. Inserts
	for _, row := range req.Inserts {
		if len(row) == 0 {
			continue
		}
		cols := make([]string, 0, len(row))
		for k := range row {
			if k == "__dbharbor_rowid__" {
				continue
			}
			cols = append(cols, k)
		}
		sort.Strings(cols)
		if len(cols) == 0 {
			continue
		}
		quotedCols := make([]string, len(cols))
		vals := make([]string, len(cols))
		for i, c := range cols {
			quotedCols[i] = QuoteIdent(d, c)
			vals[i] = FormatLiteralForDialect(d, row[c])
		}
		sqls = append(sqls, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, strings.Join(quotedCols, ", "), strings.Join(vals, ", ")))
	}

	// 2. Updates
	for _, upd := range req.Updates {
		if len(upd.Changes) == 0 {
			continue
		}
		whereExpr, err := BuildWhereFromLocator(d, upd.Locator)
		if err != nil {
			return nil, err
		}
		cols := make([]string, 0, len(upd.Changes))
		for k := range upd.Changes {
			if k == "__dbharbor_rowid__" {
				continue
			}
			cols = append(cols, k)
		}
		sort.Strings(cols)
		setParts := make([]string, len(cols))
		for i, c := range cols {
			setParts[i] = fmt.Sprintf("%s = %s", QuoteIdent(d, c), FormatLiteralForDialect(d, upd.Changes[c]))
		}
		if d == DialectClickHouse {
			sqls = append(sqls, fmt.Sprintf("ALTER TABLE %s UPDATE %s WHERE %s", tbl, strings.Join(setParts, ", "), whereExpr))
		} else {
			sqls = append(sqls, fmt.Sprintf("UPDATE %s SET %s WHERE %s", tbl, strings.Join(setParts, ", "), whereExpr))
		}
	}

	// 3. Deletes
	for _, loc := range req.Deletes {
		whereExpr, err := BuildWhereFromLocator(d, loc)
		if err != nil {
			return nil, err
		}
		if d == DialectClickHouse {
			sqls = append(sqls, fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", tbl, whereExpr))
		} else {
			sqls = append(sqls, fmt.Sprintf("DELETE FROM %s WHERE %s", tbl, whereExpr))
		}
	}

	return sqls, nil
}

func BuildCreateTableDDL(d Dialect, req driver.CreateTableRequest) (string, error) {
	if strings.TrimSpace(req.Table) == "" {
		return "", errors.New("table name cannot be empty")
	}
	if len(req.Columns) == 0 {
		return "", errors.New("at least one column must be defined")
	}

	// Detect if single PK auto-increment in SQLite
	pkCount := 0
	for _, c := range req.Columns {
		if c.IsPrimaryKey && strings.TrimSpace(c.Name) != "" {
			pkCount++
		}
	}

	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	var lines []string
	var pkCols []string

	for _, col := range req.Columns {
		if strings.TrimSpace(col.Name) == "" {
			continue
		}
		hasGen := strings.TrimSpace(col.GeneratedExpr) != ""
		if strings.TrimSpace(col.Type) == "" && !(d == DialectSQLServer && hasGen) {
			continue
		}
		if strings.TrimSpace(col.Type) != "" {
			if err := ValidateColumnType(col.Type); err != nil {
				return "", err
			}
		}
		if err := validateDDLFragment("column default value", col.DefaultValue); err != nil {
			return "", err
		}
		var sb strings.Builder
		sb.WriteString("  ")
		sb.WriteString(QuoteIdent(d, col.Name))
		sb.WriteString(" ")

		if hasGen && d != DialectClickHouse {
			genClause, err := formatGeneratedColumnClause(d, col)
			if err != nil {
				return "", err
			}
			if d == DialectSQLServer {
				sb.WriteString(genClause)
				if (!col.Nullable || col.IsPrimaryKey) && strings.HasSuffix(genClause, "PERSISTED") {
					sb.WriteString(" NOT NULL")
				}
			} else {
				sb.WriteString(strings.TrimSpace(col.Type))
				sb.WriteString(" ")
				sb.WriteString(genClause)
				if !col.Nullable || col.IsPrimaryKey {
					sb.WriteString(" NOT NULL")
				}
				if d == DialectMySQL && col.Comment != "" {
					sb.WriteString(" COMMENT " + FormatLiteralForDialect(d, col.Comment))
				}
			}
		} else if d == DialectClickHouse {
			colType := normalizeClickHouseType(col.Type, col.Nullable)
			sb.WriteString(colType)
			if col.DefaultValue != "" {
				sb.WriteString(" DEFAULT " + col.DefaultValue)
			}
			if col.Comment != "" {
				sb.WriteString(" COMMENT " + FormatLiteralForDialect(d, col.Comment))
			}
		} else if d == DialectSQLite && col.IsPrimaryKey && col.IsAutoIncrement && pkCount == 1 {
			sb.WriteString("INTEGER PRIMARY KEY AUTOINCREMENT")
		} else {
			colType := strings.TrimSpace(col.Type)
			if d == DialectPostgres && col.IsAutoIncrement {
				if strings.Contains(strings.ToUpper(colType), "BIG") {
					colType = "BIGSERIAL"
				} else {
					colType = "SERIAL"
				}
			}
			sb.WriteString(colType)
			if d == DialectSQLServer && col.IsAutoIncrement {
				sb.WriteString(" IDENTITY(1,1)")
			}
			if !col.Nullable || col.IsPrimaryKey {
				sb.WriteString(" NOT NULL")
			}
			if col.DefaultValue != "" && !col.IsAutoIncrement {
				sb.WriteString(" DEFAULT " + col.DefaultValue)
			}
			if d == DialectMySQL && col.IsAutoIncrement {
				sb.WriteString(" AUTO_INCREMENT")
			}
			if d == DialectMySQL && col.Comment != "" {
				sb.WriteString(" COMMENT " + FormatLiteralForDialect(d, col.Comment))
			}
		}

		if col.IsPrimaryKey && !(d == DialectSQLite && col.IsAutoIncrement && pkCount == 1) {
			pkCols = append(pkCols, QuoteIdent(d, col.Name))
		}
		lines = append(lines, sb.String())
	}

	if len(lines) == 0 {
		return "", errors.New("at least one valid column must be defined")
	}

	if len(pkCols) > 0 && d != DialectClickHouse {
		lines = append(lines, fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(pkCols, ", ")))
	}

	for _, idx := range req.Indexes {
		if len(idx.Columns) == 0 || d != DialectMySQL {
			continue
		}
		qCols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			qCols[i] = QuoteIdent(d, c)
		}
		prefix := "INDEX"
		if idx.Unique {
			prefix = "UNIQUE INDEX"
		}
		idxName := idx.Name
		if idxName == "" {
			idxName = "idx_" + strings.Join(idx.Columns, "_")
		}
		lines = append(lines, fmt.Sprintf("  %s %s (%s)", prefix, QuoteIdent(d, idxName), strings.Join(qCols, ", ")))
	}

	if d != DialectClickHouse {
		for _, fk := range req.ForeignKeys {
			if clause := buildForeignKeyConstraintClause(d, fk); clause != "" {
				lines = append(lines, "  "+clause)
			}
		}
		for _, chk := range req.CheckConstraints {
			if strings.TrimSpace(chk.Expression) == "" {
				continue
			}
			clause, err := buildCheckConstraintClause(d, chk)
			if err != nil {
				return "", err
			}
			lines = append(lines, "  "+clause)
		}
	}

	if err := validateDDLFragment("table engine", req.Engine); err != nil {
		return "", err
	}
	if err := validateDDLFragment("table charset", req.Charset); err != nil {
		return "", err
	}
	if err := validateDDLFragment("ORDER BY expression", req.OrderBy); err != nil {
		return "", err
	}
	if err := validateDDLFragment("PARTITION BY expression", req.PartitionBy); err != nil {
		return "", err
	}

	var ddl strings.Builder
	ddl.WriteString(fmt.Sprintf("CREATE TABLE %s (\n%s\n)", tbl, strings.Join(lines, ",\n")))

	switch d {
	case DialectClickHouse:
		engine := req.Engine
		if engine == "" {
			engine = "MergeTree()"
		} else if !strings.Contains(engine, "(") {
			engine += "()"
		}
		ddl.WriteString(" ENGINE = " + engine)
		orderBy := strings.TrimSpace(req.OrderBy)
		if orderBy == "" {
			if len(pkCols) > 0 {
				orderBy = "(" + strings.Join(pkCols, ", ") + ")"
			} else {
				orderBy = "tuple()"
			}
		}
		ddl.WriteString(" ORDER BY " + orderBy)
		if strings.TrimSpace(req.PartitionBy) != "" {
			ddl.WriteString(" PARTITION BY " + strings.TrimSpace(req.PartitionBy))
		}
		if req.Comment != "" {
			ddl.WriteString(" COMMENT " + FormatLiteralForDialect(d, req.Comment))
		}
	case DialectMySQL:
		engine := req.Engine
		if engine == "" {
			engine = "InnoDB"
		}
		charset := req.Charset
		if charset == "" {
			charset = "utf8mb4"
		}
		ddl.WriteString(fmt.Sprintf(" ENGINE=%s DEFAULT CHARSET=%s", engine, charset))
		if req.Comment != "" {
			ddl.WriteString(" COMMENT=" + FormatLiteralForDialect(d, req.Comment))
		}
	case DialectSQLite:
		var opts []string
		if req.WithoutRowID {
			opts = append(opts, "WITHOUT ROWID")
		}
		if req.Strict {
			opts = append(opts, "STRICT")
		}
		if len(opts) > 0 {
			ddl.WriteString(" " + strings.Join(opts, ", "))
		}
	}

	return ddl.String(), nil
}

func normalizeClickHouseType(colType string, nullable bool) string {
	t := strings.TrimSpace(colType)
	if nullable {
		if !strings.HasPrefix(t, "Nullable(") && !strings.HasPrefix(t, "Array(") && !strings.HasPrefix(t, "Map(") && !strings.HasPrefix(t, "Tuple(") {
			return "Nullable(" + t + ")"
		}
		return t
	}
	if strings.HasPrefix(t, "Nullable(") && strings.HasSuffix(t, ")") {
		return strings.TrimSuffix(strings.TrimPrefix(t, "Nullable("), ")")
	}
	return t
}

func buildForeignKeyConstraintClause(d Dialect, fk driver.ForeignKeyDef) string {
	if len(fk.Columns) == 0 || strings.TrimSpace(fk.RefTable) == "" || len(fk.RefColumns) == 0 {
		return ""
	}
	qCols := make([]string, len(fk.Columns))
	for i, c := range fk.Columns {
		qCols[i] = QuoteIdent(d, c)
	}
	qRefs := make([]string, len(fk.RefColumns))
	for i, c := range fk.RefColumns {
		qRefs[i] = QuoteIdent(d, c)
	}
	var sb strings.Builder
	if strings.TrimSpace(fk.Name) != "" {
		sb.WriteString("CONSTRAINT " + QuoteIdent(d, fk.Name) + " ")
	}
	sb.WriteString(fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
		strings.Join(qCols, ", "),
		QuoteIdent(d, fk.RefTable),
		strings.Join(qRefs, ", "),
	))
	if act := sanitizeFKAction(fk.OnDelete); act != "" {
		sb.WriteString(" ON DELETE " + act)
	}
	if act := sanitizeFKAction(fk.OnUpdate); act != "" {
		sb.WriteString(" ON UPDATE " + act)
	}
	return sb.String()
}

func sanitizeFKAction(act string) string {
	upper := strings.ToUpper(strings.TrimSpace(act))
	switch upper {
	case "CASCADE", "RESTRICT", "SET NULL", "SET DEFAULT", "NO ACTION":
		return upper
	default:
		return ""
	}
}

// BuildAlterTableSQLsForEngine builds dialect-specific ALTER TABLE statements, accounting for engine
// sub-variants such as MariaDB using DROP CONSTRAINT instead of MySQL 8.0's DROP CHECK.
func BuildAlterTableSQLsForEngine(dbType string, d Dialect, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := BuildAlterTableSQLs(d, req)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(dbType), "mariadb") && d == DialectMySQL {
		tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
		prefix := "ALTER TABLE " + tbl + " DROP CHECK "
		replacement := "ALTER TABLE " + tbl + " DROP CONSTRAINT "
		for i, s := range stmts {
			if strings.HasPrefix(s, prefix) {
				stmts[i] = replacement + strings.TrimPrefix(s, prefix)
			}
		}
	}
	return stmts, nil
}

func BuildAlterTableSQLs(d Dialect, req driver.AlterTableRequest) ([]string, error) {
	if strings.TrimSpace(req.Table) == "" {
		return nil, errors.New("table name cannot be empty")
	}
	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	var stmts []string

	for _, act := range req.Actions {
		switch strings.ToUpper(strings.TrimSpace(act.Action)) {
		case "ADD_COLUMN":
			col := act.Column
			hasGen := strings.TrimSpace(col.GeneratedExpr) != ""
			if strings.TrimSpace(col.Name) == "" || (strings.TrimSpace(col.Type) == "" && !(d == DialectSQLServer && hasGen)) {
				return nil, errors.New("column name and type cannot be empty when adding a column")
			}
			if strings.TrimSpace(col.Type) != "" {
				if err := ValidateColumnType(col.Type); err != nil {
					return nil, err
				}
			}
			if err := validateDDLFragment("column default value", col.DefaultValue); err != nil {
				return nil, err
			}
			nullStr := ""
			if !col.Nullable && d != DialectClickHouse {
				nullStr = " NOT NULL"
			}
			colType := strings.TrimSpace(col.Type)
			if d == DialectClickHouse {
				colType = normalizeClickHouseType(colType, col.Nullable)
			}
			cmtStr := ""
			if col.Comment != "" && (d == DialectMySQL || d == DialectClickHouse) {
				cmtStr = " COMMENT " + FormatLiteralForDialect(d, col.Comment)
			}
			afterStr := ""
			if act.AfterColumn != "" && (d == DialectMySQL || d == DialectClickHouse) {
				afterStr = " AFTER " + QuoteIdent(d, act.AfterColumn)
			}

			if hasGen && d != DialectClickHouse {
				genClause, err := formatGeneratedColumnClause(d, col)
				if err != nil {
					return nil, err
				}
				if d == DialectSQLServer {
					if !col.Nullable && strings.HasSuffix(genClause, "PERSISTED") {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s %s NOT NULL", tbl, QuoteIdent(d, col.Name), genClause))
					} else {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s %s", tbl, QuoteIdent(d, col.Name), genClause))
					}
				} else if d == DialectOracle || d == DialectDameng {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s %s %s%s", tbl, QuoteIdent(d, col.Name), colType, genClause, nullStr))
				} else {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s %s%s%s%s", tbl, QuoteIdent(d, col.Name), colType, genClause, nullStr, cmtStr, afterStr))
				}
			} else {
				defStr := ""
				if col.DefaultValue != "" {
					defStr = " DEFAULT " + col.DefaultValue
				} else if d == DialectSQLite && !col.Nullable {
					// SQLite requires a DEFAULT when adding a NOT NULL column to a populated table
					defStr = " DEFAULT ''"
				}
				if d == DialectOracle || d == DialectDameng || d == DialectSQLServer {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s %s%s%s", tbl, QuoteIdent(d, col.Name), colType, defStr, nullStr))
				} else {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s%s%s%s%s", tbl, QuoteIdent(d, col.Name), colType, nullStr, defStr, cmtStr, afterStr))
				}
			}
			if col.Comment != "" && (d == DialectPostgres || d == DialectOracle || d == DialectDameng) {
				stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteralForDialect(d, col.Comment)))
			}
			if col.Comment != "" && d == DialectSQLServer {
				stmts = append(stmts, BuildSQLServerExtendedPropertySQL(req.Schema, req.Table, col.Name, col.Comment))
			}

		case "DROP_COLUMN":
			colName := act.Column.Name
			if colName == "" {
				colName = act.OldName
			}
			if strings.TrimSpace(colName) == "" {
				return nil, errors.New("column name cannot be empty when dropping a column")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", tbl, QuoteIdent(d, colName)))

		case "RENAME_COLUMN":
			oldName := strings.TrimSpace(act.OldName)
			newName := strings.TrimSpace(act.NewName)
			if newName == "" {
				newName = strings.TrimSpace(act.Column.Name)
			}
			if oldName == "" || newName == "" {
				return nil, errors.New("both old and new column names are required when renaming a column")
			}
			if oldName == newName {
				continue
			}
			if d == DialectSQLServer {
				target := req.Table + "." + oldName
				if req.Schema != "" {
					target = req.Schema + "." + target
				}
				stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s, 'COLUMN'", FormatLiteralForDialect(d, target), FormatLiteralForDialect(d, newName)))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, newName)))
			}

		case "MODIFY_COLUMN":
			col := act.Column
			if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
				return nil, errors.New("column name and type cannot be empty when modifying a column")
			}
			if err := ValidateColumnType(col.Type); err != nil {
				return nil, err
			}
			if err := validateDDLFragment("column default value", col.DefaultValue); err != nil {
				return nil, err
			}
			oldName := strings.TrimSpace(act.OldName)
			if oldName == "" {
				oldName = col.Name
			}
			colType := strings.TrimSpace(col.Type)

			switch d {
			case DialectMySQL:
				nullStr := "NULL"
				if !col.Nullable || col.IsPrimaryKey {
					nullStr = "NOT NULL"
				}
				cmtStr := ""
				if col.Comment != "" {
					cmtStr = " COMMENT " + FormatLiteralForDialect(d, col.Comment)
				}
				afterStr := ""
				if act.AfterColumn != "" {
					afterStr = " AFTER " + QuoteIdent(d, act.AfterColumn)
				}
				if strings.TrimSpace(col.GeneratedExpr) != "" {
					genClause, err := formatGeneratedColumnClause(d, col)
					if err != nil {
						return nil, err
					}
					if oldName != col.Name {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s CHANGE COLUMN %s %s %s %s %s%s%s",
							tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name), colType, genClause, nullStr, cmtStr, afterStr))
					} else {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s %s %s%s%s",
							tbl, QuoteIdent(d, col.Name), colType, genClause, nullStr, cmtStr, afterStr))
					}
				} else {
					defStr := ""
					if col.DefaultValue != "" && !col.IsAutoIncrement {
						defStr = " DEFAULT " + col.DefaultValue
					}
					aiStr := ""
					if col.IsAutoIncrement {
						aiStr = " AUTO_INCREMENT"
					}
					if oldName != col.Name {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s CHANGE COLUMN %s %s %s %s%s%s%s%s",
							tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name), colType, nullStr, defStr, aiStr, cmtStr, afterStr))
					} else {
						stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s %s%s%s%s%s",
							tbl, QuoteIdent(d, col.Name), colType, nullStr, defStr, aiStr, cmtStr, afterStr))
					}
				}

			case DialectPostgres:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", tbl, QuoteIdent(d, col.Name), colType))
				if col.Nullable && !col.IsPrimaryKey {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL", tbl, QuoteIdent(d, col.Name)))
				} else {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", tbl, QuoteIdent(d, col.Name)))
				}
				if col.DefaultValue != "" {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", tbl, QuoteIdent(d, col.Name), col.DefaultValue))
				} else if !col.IsAutoIncrement {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", tbl, QuoteIdent(d, col.Name)))
				}
				if col.Comment != "" {
					stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteralForDialect(d, col.Comment)))
				}

			case DialectClickHouse:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				normType := normalizeClickHouseType(colType, col.Nullable)
				defStr := ""
				if col.DefaultValue != "" {
					defStr = " DEFAULT " + col.DefaultValue
				}
				cmtStr := ""
				if col.Comment != "" {
					cmtStr = " COMMENT " + FormatLiteralForDialect(d, col.Comment)
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s%s%s", tbl, QuoteIdent(d, col.Name), normType, defStr, cmtStr))

			case DialectSQLServer:
				if oldName != col.Name {
					target := req.Table + "." + oldName
					if req.Schema != "" {
						target = req.Schema + "." + target
					}
					stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s, 'COLUMN'", FormatLiteralForDialect(d, target), FormatLiteralForDialect(d, col.Name)))
				}
				nullStr := "NULL"
				if !col.Nullable || col.IsPrimaryKey {
					nullStr = "NOT NULL"
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s %s", tbl, QuoteIdent(d, col.Name), colType, nullStr))
				if col.Comment != "" {
					stmts = append(stmts, BuildSQLServerExtendedPropertySQL(req.Schema, req.Table, col.Name, col.Comment))
				}

			default:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				defStr := ""
				if col.DefaultValue != "" {
					defStr = " DEFAULT " + col.DefaultValue
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY (%s %s%s)", tbl, QuoteIdent(d, col.Name), colType, defStr))
				if col.Comment != "" && (d == DialectOracle || d == DialectDameng) {
					stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteralForDialect(d, col.Comment)))
				}
			}

		case "ADD_INDEX":
			idx := act.Index
			if len(idx.Columns) == 0 {
				return nil, errors.New("index must include at least one column")
			}
			idxName := strings.TrimSpace(idx.Name)
			if idxName == "" {
				idxName = "idx_" + req.Table + "_" + strings.Join(idx.Columns, "_")
			}
			qCols := make([]string, len(idx.Columns))
			for i, c := range idx.Columns {
				qCols[i] = QuoteIdent(d, c)
			}
			uniq := ""
			if idx.Unique {
				uniq = "UNIQUE "
			}
			stmts = append(stmts, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", uniq, QuoteIdent(d, idxName), tbl, strings.Join(qCols, ", ")))

		case "DROP_INDEX":
			idxName := strings.TrimSpace(act.Index.Name)
			if idxName == "" {
				return nil, errors.New("index name cannot be empty")
			}
			if d == DialectMySQL || d == DialectSQLServer {
				stmts = append(stmts, fmt.Sprintf("DROP INDEX %s ON %s", QuoteIdent(d, idxName), tbl))
			} else if d == DialectPostgres && req.Schema != "" {
				stmts = append(stmts, fmt.Sprintf("DROP INDEX IF EXISTS %s.%s", QuoteIdent(d, req.Schema), QuoteIdent(d, idxName)))
			} else {
				stmts = append(stmts, fmt.Sprintf("DROP INDEX %s", QuoteIdent(d, idxName)))
			}

		case "ADD_FOREIGN_KEY":
			fk := act.ForeignKey
			if fk.Name == "" && len(fk.Columns) > 0 {
				fk.Name = fmt.Sprintf("fk_%s_%s", req.Table, strings.Join(fk.Columns, "_"))
			}
			clause := buildForeignKeyConstraintClause(d, fk)
			if clause == "" {
				return nil, errors.New("foreign key columns and referenced table/columns cannot be empty")
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s", tbl, clause))

		case "DROP_FOREIGN_KEY":
			fkName := strings.TrimSpace(act.ForeignKey.Name)
			if fkName == "" {
				fkName = strings.TrimSpace(act.OldName)
			}
			if fkName == "" {
				return nil, errors.New("foreign key constraint name cannot be empty")
			}
			if d == DialectMySQL {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s", tbl, QuoteIdent(d, fkName)))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", tbl, QuoteIdent(d, fkName)))
			}

		case "ADD_CHECK":
			chk := act.Check
			clause, err := buildCheckConstraintClause(d, chk)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s", tbl, clause))

		case "DROP_CHECK":
			chkName := strings.TrimSpace(act.Check.Name)
			if chkName == "" {
				chkName = strings.TrimSpace(act.OldName)
			}
			if chkName == "" {
				return nil, errors.New("check constraint name cannot be empty")
			}
			if d == DialectMySQL {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", tbl, QuoteIdent(d, chkName)))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", tbl, QuoteIdent(d, chkName)))
			}

		case "RENAME_TABLE":
			newTbl := strings.TrimSpace(act.NewName)
			if newTbl == "" {
				return nil, errors.New("new table name cannot be empty")
			}
			switch d {
			case DialectMySQL, DialectClickHouse:
				stmts = append(stmts, fmt.Sprintf("RENAME TABLE %s TO %s", tbl, QualifyTable(d, req.Database, req.Schema, newTbl)))
			case DialectSQLServer:
				target := req.Table
				if req.Schema != "" {
					target = req.Schema + "." + target
				}
				stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s", FormatLiteralForDialect(d, target), FormatLiteralForDialect(d, newTbl)))
			default:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, QuoteIdent(d, newTbl)))
			}

		case "COMMENT_TABLE":
			switch d {
			case DialectMySQL:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s COMMENT = %s", tbl, FormatLiteralForDialect(d, act.Comment)))
			case DialectClickHouse:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COMMENT %s", tbl, FormatLiteralForDialect(d, act.Comment)))
			case DialectPostgres, DialectOracle, DialectDameng:
				stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS %s", tbl, FormatLiteralForDialect(d, act.Comment)))
			case DialectSQLServer:
				stmts = append(stmts, BuildSQLServerExtendedPropertySQL(req.Schema, req.Table, "", act.Comment))
			default:
				return nil, fmt.Errorf("table comments are not supported by the current database engine")
			}
		}
	}
	return stmts, nil
}

// BuildObjectActionSQLs builds dialect-specific DDL/DML statements for table & view management operations.
func BuildObjectActionSQLs(d Dialect, req driver.ObjectActionRequest) ([]string, error) {
	action := strings.ToUpper(strings.TrimSpace(req.Action))
	objName := strings.TrimSpace(req.ObjectName)
	if objName == "" {
		return nil, errors.New("target object name cannot be empty")
	}
	if req.WebMode && !req.IsAdmin {
		if HasSQLDefinerClause(req.RoutineSQL) || HasSQLDefinerClause(req.ViewSQL) {
			return nil, errors.New("DEFINER and SQL SECURITY DEFINER clauses are not permitted for non-administrator accounts in Web mode")
		}
	}
	tbl := QualifyTable(d, req.Database, req.Schema, objName)
	objTypeUpper := strings.ToUpper(strings.TrimSpace(req.ObjectType))
	isView := objTypeUpper == "VIEW"

	switch action {
	case "DROP":
		switch objTypeUpper {
		case "TRIGGER":
			if d == DialectOracle || d == DialectDameng {
				return []string{"DROP TRIGGER " + tbl}, nil
			}
			if d == DialectPostgres {
				if strings.TrimSpace(req.TargetName) != "" {
					onTbl := QualifyTable(d, req.Database, req.Schema, strings.TrimSpace(req.TargetName))
					return []string{fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", QuoteIdent(d, objName), onTbl)}, nil
				}
				return nil, errors.New("target table name (targetName) is required to drop a PostgreSQL trigger")
			}
			return []string{"DROP TRIGGER IF EXISTS " + tbl}, nil
		case "PROCEDURE":
			if d == DialectOracle || d == DialectDameng {
				return []string{"DROP PROCEDURE " + tbl}, nil
			}
			return []string{"DROP PROCEDURE IF EXISTS " + tbl}, nil
		case "FUNCTION":
			if d == DialectOracle || d == DialectDameng {
				return []string{"DROP FUNCTION " + tbl}, nil
			}
			return []string{"DROP FUNCTION IF EXISTS " + tbl}, nil
		case "MATERIALIZED_VIEW", "MATERIALIZED VIEW":
			if d == DialectOracle || d == DialectDameng {
				return []string{"DROP MATERIALIZED VIEW " + tbl}, nil
			}
			if d == DialectClickHouse {
				return []string{"DROP TABLE IF EXISTS " + tbl}, nil
			}
			return []string{"DROP MATERIALIZED VIEW IF EXISTS " + tbl}, nil
		}
		if isView {
			if d == DialectOracle || d == DialectDameng {
				return []string{"DROP VIEW " + tbl}, nil
			}
			return []string{"DROP VIEW IF EXISTS " + tbl}, nil
		}
		if d == DialectOracle || d == DialectDameng {
			return []string{"DROP TABLE " + tbl}, nil
		}
		return []string{"DROP TABLE IF EXISTS " + tbl}, nil

	case "TRUNCATE":
		if d == DialectSQLite {
			return []string{"DELETE FROM " + tbl}, nil
		}
		return []string{"TRUNCATE TABLE " + tbl}, nil

	case "OPTIMIZE":
		switch d {
		case DialectSQLite:
			return []string{"VACUUM"}, nil
		case DialectMySQL:
			return []string{"OPTIMIZE TABLE " + tbl}, nil
		case DialectClickHouse:
			return []string{"OPTIMIZE TABLE " + tbl + " FINAL"}, nil
		case DialectPostgres:
			return []string{"VACUUM ANALYZE " + tbl}, nil
		default:
			return nil, fmt.Errorf("OPTIMIZE is not supported by the current database engine")
		}

	case "ANALYZE":
		switch d {
		case DialectSQLite, DialectPostgres:
			return []string{"ANALYZE " + tbl}, nil
		case DialectMySQL:
			return []string{"ANALYZE TABLE " + tbl}, nil
		case DialectSQLServer:
			return []string{"UPDATE STATISTICS " + tbl}, nil
		default:
			return nil, fmt.Errorf("ANALYZE is not supported by the current database engine")
		}

	case "RENAME":
		target := strings.TrimSpace(req.TargetName)
		if target == "" {
			return nil, errors.New("target name for rename cannot be empty")
		}
		if isView && d == DialectPostgres {
			return []string{fmt.Sprintf("ALTER VIEW %s RENAME TO %s", tbl, QuoteIdent(d, target))}, nil
		}
		return BuildAlterTableSQLs(d, driver.AlterTableRequest{
			Database: req.Database,
			Schema:   req.Schema,
			Table:    objName,
			Actions:  []driver.AlterAction{{Action: "RENAME_TABLE", NewName: target}},
		})

	case "COPY_STRUCTURE":
		target := strings.TrimSpace(req.TargetName)
		if target == "" {
			return nil, errors.New("target table name for structure copy cannot be empty")
		}
		dst := QualifyTable(d, req.Database, req.Schema, target)
		switch d {
		case DialectMySQL:
			return []string{fmt.Sprintf("CREATE TABLE %s LIKE %s", dst, tbl)}, nil
		case DialectClickHouse:
			return []string{fmt.Sprintf("CREATE TABLE %s AS %s", dst, tbl)}, nil
		case DialectSQLServer:
			return []string{fmt.Sprintf("SELECT TOP 0 * INTO %s FROM %s", dst, tbl)}, nil
		default:
			return []string{fmt.Sprintf("CREATE TABLE %s AS SELECT * FROM %s WHERE 1 = 0", dst, tbl)}, nil
		}

	case "COPY_DATA":
		target := strings.TrimSpace(req.TargetName)
		if target == "" {
			return nil, errors.New("target table name for data clone cannot be empty")
		}
		dst := QualifyTable(d, req.Database, req.Schema, target)
		switch d {
		case DialectMySQL, DialectClickHouse:
			createSQL := fmt.Sprintf("CREATE TABLE %s LIKE %s", dst, tbl)
			if d == DialectClickHouse {
				createSQL = fmt.Sprintf("CREATE TABLE %s AS %s", dst, tbl)
			}
			return []string{
				createSQL,
				fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", dst, tbl),
			}, nil
		case DialectSQLServer:
			return []string{fmt.Sprintf("SELECT * INTO %s FROM %s", dst, tbl)}, nil
		default:
			return []string{fmt.Sprintf("CREATE TABLE %s AS SELECT * FROM %s", dst, tbl)}, nil
		}

	case "CREATE_VIEW":
		splitView := SplitStatements(req.ViewSQL)
		if len(splitView) == 0 {
			return nil, errors.New("view SELECT query cannot be empty")
		}
		if len(splitView) > 1 {
			return nil, errors.New("view definition must contain a single SQL statement")
		}
		viewSQL := strings.TrimSpace(splitView[0])
		if d == DialectClickHouse && req.WebMode && !req.IsAdmin && !req.AllowPrivIP {
			if err := ValidateClickHouseRestrictedWebSQL(viewSQL); err != nil {
				return nil, err
			}
		}
		leadKw := leadingSQLKeyword(viewSQL)
		if leadKw == "CREATE" || leadKw == "ALTER" || leadKw == "REPLACE" {
			if err := ValidateExpectedDDLTarget(viewSQL, []string{"CREATE", "ALTER", "REPLACE"}, "VIEW"); err != nil {
				return nil, err
			}
			upperView := strings.ToUpper(StripLeadingSQLComments(viewSQL))
			if d == DialectSQLite && strings.HasPrefix(upperView, "CREATE") && !strings.Contains(upperView, "IF NOT EXISTS") {
				return []string{"DROP VIEW IF EXISTS " + tbl, viewSQL}, nil
			}
			return []string{viewSQL}, nil
		}
		if leadKw != "SELECT" && leadKw != "WITH" && leadKw != "VALUES" && leadKw != "TABLE" {
			return nil, errors.New("view definition must be a SELECT query or CREATE VIEW statement")
		}
		if d == DialectSQLite {
			return []string{
				"DROP VIEW IF EXISTS " + tbl,
				fmt.Sprintf("CREATE VIEW %s AS %s", tbl, viewSQL),
			}, nil
		}
		if d == DialectSQLServer {
			return []string{fmt.Sprintf("CREATE OR ALTER VIEW %s AS %s", tbl, viewSQL)}, nil
		}
		return []string{fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", tbl, viewSQL)}, nil

	case "CREATE_ROUTINE":
		rawDDL := cleanCompoundRoutineSQL(req.RoutineSQL)
		if rawDDL == "" {
			rawDDL = cleanCompoundRoutineSQL(req.ViewSQL)
		}
		if rawDDL == "" {
			return nil, errors.New("routine DDL statement cannot be empty")
		}
		splitRoutine := SplitStatements(rawDDL)
		if len(splitRoutine) != 1 {
			return nil, errors.New("routine DDL must contain a single statement")
		}
		rawDDL = strings.TrimSpace(splitRoutine[0])
		if err := ValidateExpectedDDLTarget(rawDDL, []string{"CREATE", "ALTER", "REPLACE"}, "FUNCTION", "PROCEDURE"); err != nil {
			return nil, err
		}
		if d == DialectClickHouse && req.WebMode && !req.IsAdmin && !req.AllowPrivIP {
			if err := ValidateClickHouseRestrictedWebSQL(rawDDL); err != nil {
				return nil, err
			}
		}
		upperDDL := strings.ToUpper(StripLeadingSQLComments(rawDDL))
		var stmts []string
		if req.ReplaceExisting && (d == DialectMySQL || d == DialectSQLite) && !strings.Contains(upperDDL, "CREATE OR REPLACE") {
			routineKind := "FUNCTION"
			if objTypeUpper == "PROCEDURE" || strings.Contains(upperDDL, "PROCEDURE ") {
				routineKind = "PROCEDURE"
			}
			stmts = append(stmts, fmt.Sprintf("DROP %s IF EXISTS %s", routineKind, tbl))
		}
		stmts = append(stmts, rawDDL)
		return stmts, nil

	case "CREATE_TRIGGER":
		rawDDL := cleanCompoundRoutineSQL(req.RoutineSQL)
		if rawDDL == "" {
			rawDDL = cleanCompoundRoutineSQL(req.ViewSQL)
		}
		if rawDDL == "" {
			return nil, errors.New("trigger DDL statement cannot be empty")
		}
		splitTrig := SplitStatements(rawDDL)
		if len(splitTrig) != 1 {
			return nil, errors.New("trigger DDL must contain a single statement")
		}
		rawDDL = strings.TrimSpace(splitTrig[0])
		if err := ValidateExpectedDDLTarget(rawDDL, []string{"CREATE", "ALTER", "REPLACE"}, "TRIGGER"); err != nil {
			return nil, err
		}
		upperDDL := strings.ToUpper(StripLeadingSQLComments(rawDDL))
		var stmts []string
		if req.ReplaceExisting || d == DialectSQLite {
			switch d {
			case DialectMySQL, DialectSQLite:
				if !strings.Contains(upperDDL, "CREATE OR REPLACE") {
					stmts = append(stmts, "DROP TRIGGER IF EXISTS "+tbl)
				}
			case DialectPostgres:
				if !strings.Contains(upperDDL, "CREATE OR REPLACE") && strings.TrimSpace(req.TargetName) != "" {
					onTbl := QualifyTable(d, req.Database, req.Schema, strings.TrimSpace(req.TargetName))
					stmts = append(stmts, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", QuoteIdent(d, objName), onTbl))
				}
			}
		}
		stmts = append(stmts, rawDDL)
		return stmts, nil

	case "COMMENT":
		return BuildAlterTableSQLs(d, driver.AlterTableRequest{
			Database: req.Database,
			Schema:   req.Schema,
			Table:    objName,
			Actions:  []driver.AlterAction{{Action: "COMMENT_TABLE", Comment: req.Comment}},
		})

	default:
		return nil, fmt.Errorf("unsupported object action: %s", req.Action)
	}
}

func cleanCompoundRoutineSQL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	customDelim := ""
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		lt := strings.TrimSpace(line)
		upper := strings.ToUpper(lt)
		if strings.HasPrefix(upper, "DELIMITER ") {
			tok := strings.TrimSpace(lt[len("DELIMITER "):])
			if tok != "" && tok != ";" {
				customDelim = tok
			}
			continue
		}
		cleaned = append(cleaned, line)
	}
	out := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if customDelim != "" && strings.HasSuffix(out, customDelim) {
		out = strings.TrimSpace(strings.TrimSuffix(out, customDelim))
	}
	return out
}
