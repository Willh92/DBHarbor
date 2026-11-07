package sqlmeta

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

func fetchObjectDDLImpl(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, database, schema, objectType, objectName string) (string, error) {
	if sqlDrv == nil {
		return "", errors.New("SQL driver is nil")
	}
	objName := strings.TrimSpace(objectName)
	if objName == "" {
		return "", errors.New("object name is required")
	}
	eng := normalizeDBType(dbType)
	d := dialectForEngine(eng)
	ot := strings.ToUpper(strings.TrimSpace(objectType))
	if ot == "" {
		ot = "TABLE"
	}
	if ot == "MATERIALIZED VIEW" {
		ot = "MATERIALIZED_VIEW"
	} else if ot == "PACKAGE BODY" {
		ot = "PACKAGE_BODY"
	} else if ot == "DATABASE LINK" {
		ot = "DB_LINK"
	} else if ot == "TABLE MACRO" {
		ot = "TABLE_MACRO"
	}

	if ot == "TABLE" {
		st, err := sqlDrv.GetTableStructure(ctx, database, schema, objName)
		if err != nil {
			return "", err
		}
		return st.DDL, nil
	}

	switch ot {
	case "VIEW":
		return fetchViewDDL(ctx, eng, d, sqlDrv, database, schema, objName)
	case "MATERIALIZED_VIEW":
		return fetchMatViewDDL(ctx, eng, d, sqlDrv, database, schema, objName)
	case "EVENT":
		return fetchEventDDL(ctx, eng, d, sqlDrv, database, objName)
	case "SEQUENCE":
		return fetchSequenceDDL(ctx, eng, d, sqlDrv, database, schema, objName)
	case "DICTIONARY":
		return fetchDictionaryDDL(ctx, eng, d, sqlDrv, database, objName)
	case "PACKAGE", "PACKAGE_BODY":
		return fetchOracleSourceDDL(ctx, sqlDrv, database, schema, ot, objName)
	case "SYNONYM":
		return fetchSynonymDDL(ctx, eng, d, sqlDrv, database, schema, objName)
	case "DB_LINK":
		return fetchDbLinkDDL(ctx, eng, d, sqlDrv, database, schema, objName)
	case "TYPE":
		return fetchPostgresTypeDDL(ctx, d, sqlDrv, database, schema, objName)
	case "EXTENSION":
		return fetchExtensionDDL(ctx, eng, d, sqlDrv, database, objName)
	case "MACRO", "TABLE_MACRO":
		return fetchDuckDBMacroDDL(ctx, d, sqlDrv, database, schema, ot, objName)
	case "FUNCTION", "PROCEDURE", "TRIGGER":
		return fetchRoutineOrTriggerDDL(ctx, eng, d, sqlDrv, database, schema, ot, objName)
	default:
		return "", fmt.Errorf("DDL extraction for %s is not supported on %s", ot, dbType)
	}
}

func extractShowCreateStatement(qr driver.QueryResult, idx map[string]int, preferredCols ...string) string {
	if len(qr.Rows) == 0 {
		return ""
	}
	row := qr.Rows[0]
	for _, col := range preferredCols {
		if v := strings.TrimSpace(rowStr(row, idx, col, -1)); v != "" {
			return v
		}
	}
	// Scan cells for CREATE / ALTER prefix
	for _, cell := range row {
		s := strings.TrimSpace(cellStr(cell))
		upper := strings.ToUpper(s)
		if strings.HasPrefix(upper, "CREATE ") || strings.HasPrefix(upper, "ALTER ") {
			return s
		}
	}
	if len(row) >= 2 {
		return strings.TrimSpace(cellStr(row[1]))
	}
	if len(row) == 1 {
		return strings.TrimSpace(cellStr(row[0]))
	}
	return ""
}

func fetchViewDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	switch eng {
	case "mysql", "mariadb", "tidb", "oceanbase":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE VIEW "+qObj, 5)
		if err != nil {
			return "", err
		}
		if ddl := extractShowCreateStatement(qr, idx, "Create View", "Create Table"); ddl != "" {
			return ddl, nil
		}
		return "", fmt.Errorf("view %q not found", objName)

	case "clickhouse":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE TABLE "+qObj, 5)
		if err != nil {
			return "", err
		}
		if ddl := extractShowCreateStatement(qr, idx, "statement"); ddl != "" {
			return ddl, nil
		}
		return "", fmt.Errorf("view %q not found", objName)

	case "postgres", "kingbase":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "public"
		}
		q := fmt.Sprintf(
			"SELECT pg_get_viewdef(c.oid, true) AS viewdef "+
				"FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace "+
				"WHERE n.nspname = '%s' AND c.relname = '%s'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("view %q not found", objName)
		}
		def := strings.TrimSuffix(strings.TrimSpace(rowStr(qr.Rows[0], idx, "viewdef", 0)), ";")
		return fmt.Sprintf("CREATE OR REPLACE VIEW %s.%s AS\n%s;",
			sqlbase.QuoteIdent(d, sc), sqlbase.QuoteIdent(d, objName), def), nil

	case "sqlite":
		q := fmt.Sprintf("SELECT sql FROM sqlite_master WHERE type = 'view' AND name = '%s'", escapeLiteral(objName))
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("view %q not found", objName)
		}
		return strings.TrimSpace(rowStr(qr.Rows[0], idx, "sql", 0)), nil

	case "duckdb":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "main"
		}
		q := fmt.Sprintf("SELECT sql FROM duckdb_views() WHERE schema_name = '%s' AND view_name = '%s'",
			escapeLiteral(sc), escapeLiteral(objName))
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("view %q not found", objName)
		}
		return strings.TrimSpace(rowStr(qr.Rows[0], idx, "sql", 0)), nil

	case "sqlserver":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "dbo"
		}
		full := sqlbase.QuoteIdent(d, sc) + "." + sqlbase.QuoteIdent(d, objName)
		q := fmt.Sprintf("SELECT OBJECT_DEFINITION(OBJECT_ID(N'%s')) AS ddl", escapeLiteral(full))
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("view %q not found", objName)
		}
		return strings.TrimSpace(rowStr(qr.Rows[0], idx, "ddl", 0)), nil

	case "oracle", "dameng", "dm8":
		if ddl := tryOracleMetadataDDL(ctx, sqlDrv, database, schema, "VIEW", objName); ddl != "" {
			return ddl, nil
		}
		ownerCond := oracleOwnerCondition("OWNER", schema)
		q := fmt.Sprintf("SELECT OWNER, TEXT FROM ALL_VIEWS WHERE %s AND (VIEW_NAME = '%s' OR VIEW_NAME = UPPER('%s'))",
			ownerCond, escapeLiteral(objName), escapeLiteral(objName))
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("view %q not found", objName)
		}
		owner := strings.TrimSpace(rowStr(qr.Rows[0], idx, "OWNER", 0))
		text := strings.TrimSuffix(strings.TrimSpace(rowStr(qr.Rows[0], idx, "TEXT", 1)), ";")
		qView := sqlbase.QualifyTable(d, "", owner, objName)
		return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s;", qView, text), nil
	}
	return "", fmt.Errorf("VIEW DDL is not supported on %s", eng)
}

func fetchMatViewDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	switch eng {
	case "postgres", "kingbase":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "public"
		}
		q := fmt.Sprintf(
			"SELECT pg_get_viewdef(c.oid, true) AS viewdef "+
				"FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace "+
				"WHERE n.nspname = '%s' AND c.relname = '%s' AND c.relkind = 'm'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("materialized view %q not found", objName)
		}
		def := strings.TrimSuffix(strings.TrimSpace(rowStr(qr.Rows[0], idx, "viewdef", 0)), ";")
		return fmt.Sprintf("CREATE MATERIALIZED VIEW %s.%s AS\n%s;",
			sqlbase.QuoteIdent(d, sc), sqlbase.QuoteIdent(d, objName), def), nil

	case "clickhouse":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE TABLE "+qObj, 5)
		if err != nil {
			return "", err
		}
		if ddl := extractShowCreateStatement(qr, idx, "statement"); ddl != "" {
			return ddl, nil
		}
		return "", fmt.Errorf("materialized view %q not found", objName)

	case "oracle", "dameng", "dm8":
		if ddl := tryOracleMetadataDDL(ctx, sqlDrv, database, schema, "MATERIALIZED_VIEW", objName); ddl != "" {
			return ddl, nil
		}
		ownerCond := oracleOwnerCondition("OWNER", schema)
		q := fmt.Sprintf(
			"SELECT OWNER, QUERY, COALESCE(REFRESH_MODE, 'DEMAND') AS REFRESH_MODE, COALESCE(REFRESH_METHOD, 'FORCE') AS REFRESH_METHOD "+
				"FROM ALL_MVIEWS WHERE %s AND (MVIEW_NAME = '%s' OR MVIEW_NAME = UPPER('%s'))",
			ownerCond, escapeLiteral(objName), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("materialized view %q not found", objName)
		}
		owner := strings.TrimSpace(rowStr(qr.Rows[0], idx, "OWNER", 0))
		queryText := strings.TrimSuffix(strings.TrimSpace(rowStr(qr.Rows[0], idx, "QUERY", 1)), ";")
		mode := strings.TrimSpace(rowStr(qr.Rows[0], idx, "REFRESH_MODE", 2))
		method := strings.TrimSpace(rowStr(qr.Rows[0], idx, "REFRESH_METHOD", 3))
		qMV := sqlbase.QualifyTable(d, "", owner, objName)
		return fmt.Sprintf("CREATE MATERIALIZED VIEW %s\nREFRESH %s ON %s\nAS\n%s;", qMV, method, mode, queryText), nil
	}
	return "", fmt.Errorf("MATERIALIZED_VIEW DDL is not supported on %s", eng)
}

func fetchEventDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, objName string) (string, error) {
	switch eng {
	case "mysql", "mariadb", "oceanbase":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE EVENT "+qObj, 5)
		if err != nil {
			return "", err
		}
		if ddl := extractShowCreateStatement(qr, idx, "Create Event"); ddl != "" {
			return ddl, nil
		}
		return "", fmt.Errorf("event %q not found", objName)
	}
	return "", fmt.Errorf("EVENT DDL is not supported on %s", eng)
}

func fetchSequenceDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	switch eng {
	case "mariadb", "tidb":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE SEQUENCE "+qObj, 5)
		if err != nil {
			qr, idx, err = queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE TABLE "+qObj, 5)
			if err != nil {
				return "", err
			}
		}
		if ddl := extractShowCreateStatement(qr, idx, "Create Sequence", "Create Table"); ddl != "" {
			return ddl, nil
		}
		return "", fmt.Errorf("sequence %q not found", objName)

	case "postgres", "kingbase":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "public"
		}
		qSeq := sqlbase.QualifyTable(d, "", sc, objName)
		q := fmt.Sprintf(
			"SELECT COALESCE(data_type::text, 'bigint') AS data_type, COALESCE(start_value::text, '1') AS start_value, "+
				"COALESCE(increment_by::text, '1') AS increment_by, COALESCE(min_value::text, '1') AS min_value, "+
				"COALESCE(max_value::text, '') AS max_value, COALESCE(cache_size::text, '1') AS cache_size, "+
				"COALESCE(cycle::text, 'false') AS cycle "+
				"FROM pg_catalog.pg_sequences WHERE schemaname = '%s' AND sequencename = '%s'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return fmt.Sprintf("CREATE SEQUENCE %s;", qSeq), nil
		}
		r := qr.Rows[0]
		dt := strings.TrimSpace(rowStr(r, idx, "data_type", 0))
		startVal := strings.TrimSpace(rowStr(r, idx, "start_value", 1))
		incBy := strings.TrimSpace(rowStr(r, idx, "increment_by", 2))
		minVal := strings.TrimSpace(rowStr(r, idx, "min_value", 3))
		maxVal := strings.TrimSpace(rowStr(r, idx, "max_value", 4))
		cacheSz := strings.TrimSpace(rowStr(r, idx, "cache_size", 5))
		cyc := strings.ToLower(strings.TrimSpace(rowStr(r, idx, "cycle", 6)))

		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE SEQUENCE %s", qSeq)
		if dt != "" {
			fmt.Fprintf(&sb, "\n  AS %s", dt)
		}
		if incBy != "" {
			fmt.Fprintf(&sb, "\n  INCREMENT BY %s", incBy)
		}
		if minVal != "" {
			fmt.Fprintf(&sb, "\n  MINVALUE %s", minVal)
		}
		if maxVal != "" {
			fmt.Fprintf(&sb, "\n  MAXVALUE %s", maxVal)
		}
		if startVal != "" {
			fmt.Fprintf(&sb, "\n  START WITH %s", startVal)
		}
		if cacheSz != "" {
			fmt.Fprintf(&sb, "\n  CACHE %s", cacheSz)
		}
		if cyc == "true" || cyc == "t" || cyc == "1" {
			sb.WriteString("\n  CYCLE;")
		} else {
			sb.WriteString("\n  NO CYCLE;")
		}
		return sb.String(), nil

	case "oracle", "dameng", "dm8":
		if ddl := tryOracleMetadataDDL(ctx, sqlDrv, database, schema, "SEQUENCE", objName); ddl != "" {
			return ddl, nil
		}
		seqOwnerCond := oracleOwnerCondition("SEQUENCE_OWNER", schema)
		q := fmt.Sprintf(
			"SELECT SEQUENCE_OWNER, TO_CHAR(MIN_VALUE) AS MIN_VAL, TO_CHAR(MAX_VALUE) AS MAX_VAL, "+
				"TO_CHAR(INCREMENT_BY) AS INC_BY, COALESCE(CYCLE_FLAG, 'N') AS CYCLE_FLAG, "+
				"TO_CHAR(CACHE_SIZE) AS CACHE_SZ, TO_CHAR(LAST_NUMBER) AS LAST_NUM "+
				"FROM ALL_SEQUENCES WHERE %s AND (SEQUENCE_NAME = '%s' OR SEQUENCE_NAME = UPPER('%s'))",
			seqOwnerCond, escapeLiteral(objName), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("sequence %q not found", objName)
		}
		r := qr.Rows[0]
		owner := strings.TrimSpace(rowStr(r, idx, "SEQUENCE_OWNER", 0))
		qSeq := sqlbase.QualifyTable(d, "", owner, objName)
		cyc := "NOCYCLE"
		if strings.EqualFold(strings.TrimSpace(rowStr(r, idx, "CYCLE_FLAG", 4)), "Y") {
			cyc = "CYCLE"
		}
		cacheSz := strings.TrimSpace(rowStr(r, idx, "CACHE_SZ", 5))
		cacheClause := "NOCACHE"
		if cacheSz != "" && cacheSz != "0" {
			cacheClause = "CACHE " + cacheSz
		}
		return fmt.Sprintf("CREATE SEQUENCE %s MINVALUE %s MAXVALUE %s INCREMENT BY %s START WITH %s %s %s;",
			qSeq,
			rowStr(r, idx, "MIN_VAL", 1),
			rowStr(r, idx, "MAX_VAL", 2),
			rowStr(r, idx, "INC_BY", 3),
			rowStr(r, idx, "LAST_NUM", 6),
			cacheClause,
			cyc,
		), nil

	case "sqlserver":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "dbo"
		}
		q := fmt.Sprintf(
			"SELECT COALESCE(TYPE_NAME(seq.user_type_id), 'bigint') AS data_type, "+
				"CONVERT(VARCHAR(50), seq.start_value) AS start_value, "+
				"CONVERT(VARCHAR(50), seq.increment) AS increment_by, "+
				"CONVERT(VARCHAR(50), seq.minimum_value) AS min_value, "+
				"CONVERT(VARCHAR(50), seq.maximum_value) AS max_value, "+
				"CASE WHEN seq.is_cycling = 1 THEN 'CYCLE' ELSE 'NO CYCLE' END AS cycle_clause "+
				"FROM sys.sequences seq JOIN sys.schemas s ON seq.schema_id = s.schema_id "+
				"WHERE s.name = N'%s' AND seq.name = N'%s'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("sequence %q not found", objName)
		}
		r := qr.Rows[0]
		qSeq := sqlbase.QualifyTable(d, "", sc, objName)
		return fmt.Sprintf("CREATE SEQUENCE %s AS %s\n  START WITH %s\n  INCREMENT BY %s\n  MINVALUE %s\n  MAXVALUE %s\n  %s;",
			qSeq,
			rowStr(r, idx, "data_type", 0),
			rowStr(r, idx, "start_value", 1),
			rowStr(r, idx, "increment_by", 2),
			rowStr(r, idx, "min_value", 3),
			rowStr(r, idx, "max_value", 4),
			rowStr(r, idx, "cycle_clause", 5),
		), nil

	case "duckdb":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "main"
		}
		q := fmt.Sprintf(
			"SELECT COALESCE(sql, '') AS sql, CAST(start_value AS VARCHAR) AS start_value, CAST(increment_by AS VARCHAR) AS increment_by "+
				"FROM duckdb_sequences() WHERE schema_name = '%s' AND sequence_name = '%s'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("sequence %q not found", objName)
		}
		r := qr.Rows[0]
		if ddl := strings.TrimSpace(rowStr(r, idx, "sql", 0)); ddl != "" {
			return ddl, nil
		}
		qSeq := sqlbase.QualifyTable(d, "", sc, objName)
		return fmt.Sprintf("CREATE SEQUENCE %s START WITH %s INCREMENT BY %s;",
			qSeq, rowStr(r, idx, "start_value", 1), rowStr(r, idx, "increment_by", 2)), nil
	}
	return "", fmt.Errorf("SEQUENCE DDL is not supported on %s", eng)
}

func fetchDictionaryDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, objName string) (string, error) {
	if eng != "clickhouse" {
		return "", fmt.Errorf("DICTIONARY DDL is not supported on %s", eng)
	}
	qObj := sqlbase.QualifyTable(d, database, "", objName)
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE DICTIONARY "+qObj, 5)
	if err != nil {
		qr, idx, err = queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE TABLE "+qObj, 5)
		if err != nil {
			return "", err
		}
	}
	if ddl := extractShowCreateStatement(qr, idx, "statement"); ddl != "" {
		return ddl, nil
	}
	return "", fmt.Errorf("dictionary %q not found", objName)
}

func fetchRoutineOrTriggerDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objType, objName string) (string, error) {
	switch eng {
	case "clickhouse":
		if objType == "FUNCTION" {
			q := fmt.Sprintf("SELECT create_query FROM system.functions WHERE name = '%s' AND origin = 'SQLUserDefined'",
				escapeMySQLOrClickHouseLiteral(objName))
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
			if err != nil {
				return "", err
			}
			if len(qr.Rows) == 0 {
				return "", fmt.Errorf("ClickHouse SQL UDF %q not found", objName)
			}
			return strings.TrimSpace(rowStr(qr.Rows[0], idx, "create_query", 0)), nil
		}

	case "mysql", "mariadb", "tidb", "oceanbase":
		qObj := sqlbase.QualifyTable(d, database, "", objName)
		switch objType {
		case "TRIGGER":
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE TRIGGER "+qObj, 5)
			if err != nil {
				return "", err
			}
			if ddl := extractShowCreateStatement(qr, idx, "SQL Original Statement", "Create Trigger"); ddl != "" {
				return ddl, nil
			}
		case "PROCEDURE":
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE PROCEDURE "+qObj, 5)
			if err != nil {
				return "", err
			}
			if ddl := extractShowCreateStatement(qr, idx, "Create Procedure"); ddl != "" {
				return ddl, nil
			}
		default:
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, "SHOW CREATE FUNCTION "+qObj, 5)
			if err != nil {
				return "", err
			}
			if ddl := extractShowCreateStatement(qr, idx, "Create Function"); ddl != "" {
				return ddl, nil
			}
		}
		return "", fmt.Errorf("%s %q not found", strings.ToLower(objType), objName)

	case "postgres", "kingbase":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "public"
		}
		if objType == "TRIGGER" {
			q := fmt.Sprintf(
				"SELECT pg_get_triggerdef(t.oid, true) AS ddl "+
					"FROM pg_catalog.pg_trigger t "+
					"JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid "+
					"JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace "+
					"WHERE n.nspname = '%s' AND t.tgname = '%s' AND NOT t.tgisinternal LIMIT 1",
				escapeLiteral(sc), escapeLiteral(objName),
			)
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
			if err != nil {
				return "", err
			}
			if len(qr.Rows) == 0 {
				return "", fmt.Errorf("trigger %q not found", objName)
			}
			return strings.TrimSpace(rowStr(qr.Rows[0], idx, "ddl", 0)) + ";", nil
		}
		q := fmt.Sprintf(
			"SELECT pg_get_functiondef(p.oid) AS ddl "+
				"FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace "+
				"WHERE n.nspname = '%s' AND p.proname = '%s' LIMIT 1",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("routine %q not found", objName)
		}
		return strings.TrimSpace(rowStr(qr.Rows[0], idx, "ddl", 0)), nil

	case "sqlite":
		if objType == "TRIGGER" {
			q := fmt.Sprintf("SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = '%s'", escapeLiteral(objName))
			qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
			if err != nil {
				return "", err
			}
			if len(qr.Rows) == 0 {
				return "", fmt.Errorf("trigger %q not found", objName)
			}
			return strings.TrimSpace(rowStr(qr.Rows[0], idx, "sql", 0)), nil
		}

	case "sqlserver":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "dbo"
		}
		full := sqlbase.QuoteIdent(d, sc) + "." + sqlbase.QuoteIdent(d, objName)
		q := fmt.Sprintf("SELECT OBJECT_DEFINITION(OBJECT_ID(N'%s')) AS ddl", escapeLiteral(full))
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil {
			return "", err
		}
		if len(qr.Rows) == 0 {
			return "", fmt.Errorf("%s %q not found", strings.ToLower(objType), objName)
		}
		return strings.TrimSpace(rowStr(qr.Rows[0], idx, "ddl", 0)), nil

	case "oracle", "dameng", "dm8":
		return fetchOracleSourceDDL(ctx, sqlDrv, database, schema, objType, objName)
	}
	return "", fmt.Errorf("DDL extraction for %s is not supported on %s", objType, eng)
}

func fetchOracleSourceDDL(ctx context.Context, sqlDrv driver.SQLDriver, database, schema, objType, objName string) (string, error) {
	srcType := objType
	if objType == "PACKAGE_BODY" {
		srcType = "PACKAGE BODY"
	}
	ownerCond := oracleOwnerCondition("OWNER", schema)
	q := fmt.Sprintf(
		"SELECT TEXT FROM ALL_SOURCE WHERE %s AND (NAME = '%s' OR NAME = UPPER('%s')) AND TYPE = '%s' ORDER BY LINE",
		ownerCond, escapeLiteral(objName), escapeLiteral(objName), escapeLiteral(srcType),
	)
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 10000)
	if err == nil && len(qr.Rows) > 0 {
		var sb strings.Builder
		for _, r := range qr.Rows {
			sb.WriteString(rowStr(r, idx, "TEXT", 0))
		}
		src := strings.TrimSpace(sb.String())
		if src != "" {
			upper := strings.ToUpper(sqlbase.StripLeadingSQLComments(src))
			if strings.HasPrefix(upper, "CREATE ") {
				return src, nil
			}
			return "CREATE OR REPLACE " + src, nil
		}
	}
	metaType := objType
	if objType == "PACKAGE" {
		metaType = "PACKAGE_SPEC"
	}
	if ddl := tryOracleMetadataDDL(ctx, sqlDrv, database, schema, metaType, objName); ddl != "" {
		return ddl, nil
	}
	return "", fmt.Errorf("%s %q source not found", strings.ToLower(objType), objName)
}

func tryOracleMetadataDDL(ctx context.Context, sqlDrv driver.SQLDriver, database, schema, metaType, objName string) string {
	var q string
	if strings.TrimSpace(schema) != "" {
		q = fmt.Sprintf("SELECT DBMS_METADATA.GET_DDL('%s', '%s', '%s') AS DDL FROM DUAL",
			escapeLiteral(metaType), escapeLiteral(objName), escapeLiteral(strings.TrimSpace(schema)))
	} else {
		q = fmt.Sprintf("SELECT DBMS_METADATA.GET_DDL('%s', '%s') AS DDL FROM DUAL",
			escapeLiteral(metaType), escapeLiteral(objName))
	}
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
	if err != nil || len(qr.Rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rowStr(qr.Rows[0], idx, "DDL", 0))
}

func fetchSynonymDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	switch eng {
	case "oracle", "dameng", "dm8":
		ownerCond := oracleOwnerCondition("OWNER", schema)
		q := fmt.Sprintf(
			"SELECT OWNER, COALESCE(TABLE_OWNER, '') AS TABLE_OWNER, COALESCE(TABLE_NAME, '') AS TABLE_NAME, COALESCE(DB_LINK, '') AS DB_LINK "+
				"FROM ALL_SYNONYMS WHERE (%s OR OWNER = 'PUBLIC') AND (SYNONYM_NAME = '%s' OR SYNONYM_NAME = UPPER('%s'))",
			ownerCond, escapeLiteral(objName), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("synonym %q not found", objName)
		}
		r := qr.Rows[0]
		owner := strings.TrimSpace(rowStr(r, idx, "OWNER", 0))
		tOwner := strings.TrimSpace(rowStr(r, idx, "TABLE_OWNER", 1))
		tName := strings.TrimSpace(rowStr(r, idx, "TABLE_NAME", 2))
		dbLink := strings.TrimSpace(rowStr(r, idx, "DB_LINK", 3))

		synDecl := ""
		if strings.EqualFold(owner, "PUBLIC") {
			synDecl = "PUBLIC SYNONYM " + sqlbase.QuoteIdent(d, objName)
		} else {
			synDecl = "SYNONYM " + sqlbase.QualifyTable(d, "", owner, objName)
		}
		target := sqlbase.QualifyTable(d, "", tOwner, tName)
		if dbLink != "" {
			target += "@" + sqlbase.QuoteIdent(d, dbLink)
		}
		return fmt.Sprintf("CREATE OR REPLACE %s FOR %s;", synDecl, target), nil

	case "sqlserver":
		sc := strings.TrimSpace(schema)
		if sc == "" {
			sc = "dbo"
		}
		q := fmt.Sprintf(
			"SELECT syn.base_object_name FROM sys.synonyms syn JOIN sys.schemas s ON syn.schema_id = s.schema_id "+
				"WHERE s.name = N'%s' AND syn.name = N'%s'",
			escapeLiteral(sc), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("synonym %q not found", objName)
		}
		baseObj := strings.TrimSpace(rowStr(qr.Rows[0], idx, "base_object_name", 0))
		return fmt.Sprintf("CREATE SYNONYM %s FOR %s;", sqlbase.QualifyTable(d, "", sc, objName), baseObj), nil
	}
	return "", fmt.Errorf("SYNONYM DDL is not supported on %s", eng)
}

func fetchDbLinkDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	switch eng {
	case "oracle", "dameng", "dm8":
		ownerCond := oracleOwnerCondition("OWNER", schema)
		q := fmt.Sprintf(
			"SELECT OWNER, DB_LINK, COALESCE(USERNAME, '') AS USERNAME, COALESCE(HOST, '') AS HOST "+
				"FROM ALL_DB_LINKS WHERE (%s OR OWNER = 'PUBLIC') AND (DB_LINK = '%s' OR DB_LINK = UPPER('%s'))",
			ownerCond, escapeLiteral(objName), escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err != nil || len(qr.Rows) == 0 {
			return "", fmt.Errorf("database link %q not found", objName)
		}
		r := qr.Rows[0]
		owner := strings.TrimSpace(rowStr(r, idx, "OWNER", 0))
		linkName := strings.TrimSpace(rowStr(r, idx, "DB_LINK", 1))
		if linkName == "" {
			linkName = objName
		}
		user := strings.TrimSpace(rowStr(r, idx, "USERNAME", 2))
		host := strings.TrimSpace(rowStr(r, idx, "HOST", 3))
		pubPrefix := ""
		if strings.EqualFold(owner, "PUBLIC") {
			pubPrefix = "PUBLIC "
		}
		if user != "" {
			return fmt.Sprintf(`CREATE %sDATABASE LINK %s CONNECT TO %s IDENTIFIED BY "********" USING '%s';`,
				pubPrefix, sqlbase.QuoteIdent(d, linkName), sqlbase.QuoteIdent(d, user), escapeLiteral(host)), nil
		}
		return fmt.Sprintf("CREATE %sDATABASE LINK %s USING '%s';",
			pubPrefix, sqlbase.QuoteIdent(d, linkName), escapeLiteral(host)), nil
	}
	return "", fmt.Errorf("DB_LINK DDL is not supported on %s", eng)
}

func fetchPostgresTypeDDL(ctx context.Context, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objName string) (string, error) {
	sc := strings.TrimSpace(schema)
	if sc == "" {
		sc = "public"
	}
	q := fmt.Sprintf(
		"SELECT t.typtype::text AS typtype, "+
			"COALESCE((SELECT string_agg(quote_literal(e.enumlabel), ', ' ORDER BY e.enumsortorder) FROM pg_catalog.pg_enum e WHERE e.enumtypid = t.oid), '') AS enum_vals, "+
			"COALESCE(pg_catalog.format_type(t.typbasetype, t.typtypmod), '') AS base_type, "+
			"CASE WHEN t.typnotnull THEN 'true' ELSE 'false' END AS not_null, "+
			"COALESCE(t.typdefault, '') AS typdefault, "+
			"COALESCE((SELECT string_agg(pg_get_constraintdef(con.oid), ' ') FROM pg_catalog.pg_constraint con WHERE con.contypid = t.oid), '') AS domain_checks, "+
			"COALESCE((SELECT string_agg(quote_ident(a.attname) || ' ' || pg_catalog.format_type(a.atttypid, a.atttypmod), ',\n  ' ORDER BY a.attnum) FROM pg_catalog.pg_attribute a WHERE a.attrelid = t.typrelid AND a.attnum > 0 AND NOT a.attisdropped), '') AS comp_cols "+
			"FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace "+
			"WHERE n.nspname = '%s' AND t.typname = '%s'",
		escapeLiteral(sc), escapeLiteral(objName),
	)
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
	if err != nil || len(qr.Rows) == 0 {
		return "", fmt.Errorf("type %q not found", objName)
	}
	r := qr.Rows[0]
	qType := sqlbase.QualifyTable(d, "", sc, objName)
	tt := strings.TrimSpace(rowStr(r, idx, "typtype", 0))
	switch tt {
	case "e":
		return fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);", qType, rowStr(r, idx, "enum_vals", 1)), nil
	case "d":
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE DOMAIN %s AS %s", qType, rowStr(r, idx, "base_type", 2))
		if def := strings.TrimSpace(rowStr(r, idx, "typdefault", 4)); def != "" {
			sb.WriteString(" DEFAULT " + def)
		}
		if rowStr(r, idx, "not_null", 3) == "true" {
			sb.WriteString(" NOT NULL")
		}
		if checks := strings.TrimSpace(rowStr(r, idx, "domain_checks", 5)); checks != "" {
			sb.WriteString(" " + checks)
		}
		sb.WriteString(";")
		return sb.String(), nil
	case "c":
		return fmt.Sprintf("CREATE TYPE %s AS (\n  %s\n);", qType, rowStr(r, idx, "comp_cols", 6)), nil
	default:
		return fmt.Sprintf("-- Custom PostgreSQL type %s", qType), nil
	}
}

func fetchExtensionDDL(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, objName string) (string, error) {
	switch eng {
	case "postgres", "kingbase":
		q := fmt.Sprintf(
			"SELECT e.extname, COALESCE(e.extversion, '') AS extversion, COALESCE(n.nspname, '') AS schema_name "+
				"FROM pg_catalog.pg_extension e LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace "+
				"WHERE e.extname = '%s'",
			escapeLiteral(objName),
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
		if err == nil && len(qr.Rows) > 0 {
			r := qr.Rows[0]
			sc := strings.TrimSpace(rowStr(r, idx, "schema_name", 2))
			ver := strings.TrimSpace(rowStr(r, idx, "extversion", 1))
			var sb strings.Builder
			fmt.Fprintf(&sb, "CREATE EXTENSION IF NOT EXISTS %s", sqlbase.QuoteIdent(d, objName))
			if sc != "" {
				fmt.Fprintf(&sb, " SCHEMA %s", sqlbase.QuoteIdent(d, sc))
			}
			if ver != "" {
				fmt.Fprintf(&sb, " VERSION '%s'", escapeLiteral(ver))
			}
			sb.WriteString(";")
			return sb.String(), nil
		}
		return fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s;", sqlbase.QuoteIdent(d, objName)), nil

	case "duckdb":
		if !reValidExtensionName.MatchString(objName) {
			return "", fmt.Errorf("invalid extension name %q", objName)
		}
		extRef := formatDuckDBExtensionRef(objName)
		return fmt.Sprintf("INSTALL %s;\nLOAD %s;", extRef, extRef), nil
	}
	return "", fmt.Errorf("EXTENSION DDL is not supported on %s", eng)
}

func fetchDuckDBMacroDDL(ctx context.Context, d sqlbase.Dialect, sqlDrv driver.SQLDriver, database, schema, objType, objName string) (string, error) {
	sc := strings.TrimSpace(schema)
	if sc == "" {
		sc = "main"
	}
	q := fmt.Sprintf(
		"SELECT function_type, COALESCE(CAST(parameters AS VARCHAR), '') AS parameters, COALESCE(macro_definition, '') AS definition "+
			"FROM duckdb_functions() WHERE schema_name = '%s' AND function_name = '%s' AND function_type IN ('macro', 'table_macro') LIMIT 1",
		escapeLiteral(sc), escapeLiteral(objName),
	)
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 5)
	if err != nil || len(qr.Rows) == 0 {
		return "", fmt.Errorf("macro %q not found", objName)
	}
	r := qr.Rows[0]
	ft := strings.ToLower(strings.TrimSpace(rowStr(r, idx, "function_type", 0)))
	rawParams := strings.Trim(strings.TrimSpace(rowStr(r, idx, "parameters", 1)), "[]")
	def := strings.TrimSuffix(strings.TrimSpace(rowStr(r, idx, "definition", 2)), ";")
	qName := sqlbase.QualifyTable(d, "", sc, objName)
	if ft == "table_macro" || objType == "TABLE_MACRO" {
		return fmt.Sprintf("CREATE OR REPLACE MACRO %s(%s) AS TABLE %s;", qName, rawParams, def), nil
	}
	return fmt.Sprintf("CREATE OR REPLACE MACRO %s(%s) AS %s;", qName, rawParams, def), nil
}
