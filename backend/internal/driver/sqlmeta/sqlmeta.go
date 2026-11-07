package sqlmeta

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

// EnrichSchemaObjects populates engine-specific object slices (Routines, Triggers, Sequences,
// MaterializedViews, Events, Packages, Types, Synonyms, Extensions, Dictionaries, DbLinks, Macros).
func EnrichSchemaObjects(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	enrichSchemaObjectsImpl(ctx, dbType, sqlDrv, objs)
}

// FetchObjectDDL returns the DDL source or CREATE statement for any supported database object type.
func FetchObjectDDL(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, database, schema, objectType, objectName string) (string, error) {
	return fetchObjectDDLImpl(ctx, dbType, sqlDrv, database, schema, objectType, objectName)
}

// ExecuteObjectAction builds (and executes unless req.PreviewOnly is true) object CRUD/lifecycle
// statements, returning redacted SQL statements for live preview and history logging.
func ExecuteObjectAction(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, req driver.ObjectActionRequest) ([]string, error) {
	return executeObjectActionImpl(ctx, dbType, sqlDrv, req)
}

func normalizeDBType(dbType string) string {
	return strings.ToLower(strings.TrimSpace(dbType))
}

func dialectForEngine(dbType string) sqlbase.Dialect {
	switch normalizeDBType(dbType) {
	case "mysql", "mariadb", "tidb", "oceanbase":
		return sqlbase.DialectMySQL
	case "postgres", "kingbase", "duckdb":
		return sqlbase.DialectPostgres
	case "clickhouse":
		return sqlbase.DialectClickHouse
	case "sqlite":
		return sqlbase.DialectSQLite
	case "sqlserver":
		return sqlbase.DialectSQLServer
	case "oracle":
		return sqlbase.DialectOracle
	case "dameng", "dm8":
		return sqlbase.DialectDameng
	default:
		return sqlbase.DialectPostgres
	}
}

func escapeLiteral(s string) string {
	clean := strings.ReplaceAll(s, "\x00", "")
	return strings.ReplaceAll(clean, "'", "''")
}

func escapeMySQLOrClickHouseLiteral(s string) string {
	clean := strings.ReplaceAll(s, "\x00", "")
	clean = strings.ReplaceAll(clean, `\`, `\\`)
	return strings.ReplaceAll(clean, "'", "''")
}

func oracleOwnerCondition(col, schema string) string {
	sc := strings.TrimSpace(schema)
	if sc != "" {
		esc := escapeLiteral(sc)
		return fmt.Sprintf("(%s = '%s' OR %s = UPPER('%s'))", col, esc, col, esc)
	}
	return fmt.Sprintf("%s = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')", col)
}

func colIndexMap(cols []driver.QueryColumn) map[string]int {
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		k := strings.ToLower(strings.TrimSpace(c.Name))
		if k != "" {
			if _, exists := m[k]; !exists {
				m[k] = i
			}
		}
	}
	return m
}

func cellStr(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func rowStr(row []any, idxMap map[string]int, colName string, fallbackPos int) string {
	if idx, ok := idxMap[strings.ToLower(colName)]; ok && idx >= 0 && idx < len(row) {
		return cellStr(row[idx])
	}
	if fallbackPos >= 0 && fallbackPos < len(row) {
		return cellStr(row[fallbackPos])
	}
	return ""
}

func rowInt64(row []any, idxMap map[string]int, colName string, fallbackPos int) int64 {
	s := strings.TrimSpace(rowStr(row, idxMap, colName, fallbackPos))
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

func queryFirstResult(ctx context.Context, sqlDrv driver.SQLDriver, database, sqlText string, maxRows int) (driver.QueryResult, map[string]int, error) {
	res, err := sqlDrv.ExecuteSQL(ctx, database, sqlText, maxRows)
	if err != nil {
		return driver.QueryResult{}, nil, err
	}
	if len(res) == 0 {
		return driver.QueryResult{}, map[string]int{}, nil
	}
	return res[0], colIndexMap(res[0].Columns), nil
}
