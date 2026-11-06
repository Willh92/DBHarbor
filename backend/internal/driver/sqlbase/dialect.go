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
	if s, ok := val.(string); ok && d == DialectMySQL {
		// MySQL treats backslash as escape by default unless NO_BACKSLASH_ESCAPES is set
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, "'", "''")
		return "'" + escaped + "'"
	}
	return FormatLiteral(val)
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
		if len(SplitStatements("SELECT 1 WHERE "+f)) > 1 {
			return "", "", errors.New("multi-statement semicolons are not allowed in WHERE filter")
		}
		wherePart = " WHERE " + f
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
		if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
			continue
		}
		var sb strings.Builder
		sb.WriteString("  ")
		sb.WriteString(QuoteIdent(d, col.Name))
		sb.WriteString(" ")

		if d == DialectClickHouse {
			colType := normalizeClickHouseType(col.Type, col.Nullable)
			sb.WriteString(colType)
			if col.DefaultValue != "" {
				sb.WriteString(" DEFAULT " + col.DefaultValue)
			}
			if col.Comment != "" {
				sb.WriteString(" COMMENT " + FormatLiteral(col.Comment))
			}
		} else if d == DialectSQLite && col.IsPrimaryKey && col.IsAutoIncrement && pkCount == 1 {
			sb.WriteString("INTEGER PRIMARY KEY AUTOINCREMENT")
		} else {
			colType := col.Type
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
				sb.WriteString(" COMMENT " + FormatLiteral(col.Comment))
			}
		}

		if col.IsPrimaryKey && !(d == DialectSQLite && col.IsAutoIncrement && pkCount == 1) {
			pkCols = append(pkCols, QuoteIdent(d, col.Name))
		}
		lines = append(lines, sb.String())
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
			ddl.WriteString(" COMMENT " + FormatLiteral(req.Comment))
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
			ddl.WriteString(" COMMENT=" + FormatLiteral(req.Comment))
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
			if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
				return nil, errors.New("column name and type cannot be empty when adding a column")
			}
			nullStr := ""
			if !col.Nullable && d != DialectClickHouse {
				nullStr = " NOT NULL"
			}
			colType := col.Type
			if d == DialectClickHouse {
				colType = normalizeClickHouseType(col.Type, col.Nullable)
			}
			defStr := ""
			if col.DefaultValue != "" {
				defStr = " DEFAULT " + col.DefaultValue
			} else if d == DialectSQLite && !col.Nullable {
				// SQLite requires a DEFAULT when adding a NOT NULL column to a populated table
				defStr = " DEFAULT ''"
			}
			cmtStr := ""
			if col.Comment != "" && (d == DialectMySQL || d == DialectClickHouse) {
				cmtStr = " COMMENT " + FormatLiteral(col.Comment)
			}
			afterStr := ""
			if act.AfterColumn != "" && (d == DialectMySQL || d == DialectClickHouse) {
				afterStr = " AFTER " + QuoteIdent(d, act.AfterColumn)
			}

			if d == DialectOracle || d == DialectSQLServer {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD %s %s%s%s", tbl, QuoteIdent(d, col.Name), colType, defStr, nullStr))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s%s%s%s%s", tbl, QuoteIdent(d, col.Name), colType, nullStr, defStr, cmtStr, afterStr))
			}
			if col.Comment != "" && (d == DialectPostgres || d == DialectOracle || d == DialectDameng) {
				stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteral(col.Comment)))
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
				stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s, 'COLUMN'", FormatLiteral(target), FormatLiteral(newName)))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, newName)))
			}

		case "MODIFY_COLUMN":
			col := act.Column
			if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
				return nil, errors.New("column name and type cannot be empty when modifying a column")
			}
			oldName := strings.TrimSpace(act.OldName)
			if oldName == "" {
				oldName = col.Name
			}

			switch d {
			case DialectMySQL:
				nullStr := "NULL"
				if !col.Nullable || col.IsPrimaryKey {
					nullStr = "NOT NULL"
				}
				defStr := ""
				if col.DefaultValue != "" && !col.IsAutoIncrement {
					defStr = " DEFAULT " + col.DefaultValue
				}
				aiStr := ""
				if col.IsAutoIncrement {
					aiStr = " AUTO_INCREMENT"
				}
				cmtStr := ""
				if col.Comment != "" {
					cmtStr = " COMMENT " + FormatLiteral(col.Comment)
				}
				afterStr := ""
				if act.AfterColumn != "" {
					afterStr = " AFTER " + QuoteIdent(d, act.AfterColumn)
				}
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s CHANGE COLUMN %s %s %s %s%s%s%s%s",
						tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name), col.Type, nullStr, defStr, aiStr, cmtStr, afterStr))
				} else {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s %s%s%s%s%s",
						tbl, QuoteIdent(d, col.Name), col.Type, nullStr, defStr, aiStr, cmtStr, afterStr))
				}

			case DialectPostgres:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", tbl, QuoteIdent(d, col.Name), col.Type))
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
					stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteral(col.Comment)))
				}

			case DialectClickHouse:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				colType := normalizeClickHouseType(col.Type, col.Nullable)
				defStr := ""
				if col.DefaultValue != "" {
					defStr = " DEFAULT " + col.DefaultValue
				}
				cmtStr := ""
				if col.Comment != "" {
					cmtStr = " COMMENT " + FormatLiteral(col.Comment)
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s%s%s", tbl, QuoteIdent(d, col.Name), colType, defStr, cmtStr))

			case DialectSQLServer:
				if oldName != col.Name {
					target := req.Table + "." + oldName
					if req.Schema != "" {
						target = req.Schema + "." + target
					}
					stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s, 'COLUMN'", FormatLiteral(target), FormatLiteral(col.Name)))
				}
				nullStr := "NULL"
				if !col.Nullable || col.IsPrimaryKey {
					nullStr = "NOT NULL"
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s %s", tbl, QuoteIdent(d, col.Name), col.Type, nullStr))

			default:
				if oldName != col.Name {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", tbl, QuoteIdent(d, oldName), QuoteIdent(d, col.Name)))
				}
				defStr := ""
				if col.DefaultValue != "" {
					defStr = " DEFAULT " + col.DefaultValue
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY (%s %s%s)", tbl, QuoteIdent(d, col.Name), col.Type, defStr))
				if col.Comment != "" && (d == DialectOracle || d == DialectDameng) {
					stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", tbl, QuoteIdent(d, col.Name), FormatLiteral(col.Comment)))
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
				stmts = append(stmts, fmt.Sprintf("EXEC sp_rename %s, %s", FormatLiteral(target), FormatLiteral(newTbl)))
			default:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", tbl, QuoteIdent(d, newTbl)))
			}

		case "COMMENT_TABLE":
			switch d {
			case DialectMySQL:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s COMMENT = %s", tbl, FormatLiteral(act.Comment)))
			case DialectClickHouse:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COMMENT %s", tbl, FormatLiteral(act.Comment)))
			case DialectPostgres, DialectOracle, DialectDameng:
				stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS %s", tbl, FormatLiteral(act.Comment)))
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
	tbl := QualifyTable(d, req.Database, req.Schema, objName)
	objTypeUpper := strings.ToUpper(strings.TrimSpace(req.ObjectType))
	isView := objTypeUpper == "VIEW"

	switch action {
	case "DROP":
		switch objTypeUpper {
		case "TRIGGER":
			if d == DialectOracle {
				return []string{"DROP TRIGGER " + tbl}, nil
			}
			if d == DialectPostgres {
				// PostgreSQL DROP TRIGGER requires ON table_name if targetName is provided; otherwise quote trigger
				if strings.TrimSpace(req.TargetName) != "" {
					onTbl := QualifyTable(d, req.Database, req.Schema, strings.TrimSpace(req.TargetName))
					return []string{fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", QuoteIdent(d, objName), onTbl)}, nil
				}
			}
			return []string{"DROP TRIGGER IF EXISTS " + tbl}, nil
		case "PROCEDURE":
			if d == DialectOracle {
				return []string{"DROP PROCEDURE " + tbl}, nil
			}
			return []string{"DROP PROCEDURE IF EXISTS " + tbl}, nil
		case "FUNCTION":
			if d == DialectOracle {
				return []string{"DROP FUNCTION " + tbl}, nil
			}
			return []string{"DROP FUNCTION IF EXISTS " + tbl}, nil
		}
		if isView {
			if d == DialectOracle {
				return []string{"DROP VIEW " + tbl}, nil
			}
			return []string{"DROP VIEW IF EXISTS " + tbl}, nil
		}
		if d == DialectOracle {
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
		viewSQL := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(req.ViewSQL), ";"))
		if viewSQL == "" {
			return nil, errors.New("view SELECT query cannot be empty")
		}
		upperView := strings.ToUpper(viewSQL)
		if strings.HasPrefix(upperView, "CREATE ") || strings.HasPrefix(upperView, "ALTER ") {
			if d == DialectSQLite && strings.HasPrefix(upperView, "CREATE VIEW") {
				return []string{"DROP VIEW IF EXISTS " + tbl, viewSQL}, nil
			}
			return []string{viewSQL}, nil
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
		upperDDL := strings.ToUpper(rawDDL)
		if !strings.HasPrefix(upperDDL, "CREATE ") && !strings.HasPrefix(upperDDL, "ALTER ") {
			return nil, errors.New("routine DDL must start with CREATE or ALTER")
		}
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
		upperDDL := strings.ToUpper(rawDDL)
		if !strings.HasPrefix(upperDDL, "CREATE ") && !strings.HasPrefix(upperDDL, "ALTER ") {
			return nil, errors.New("trigger DDL must start with CREATE or ALTER")
		}
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
