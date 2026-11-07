package sqlmeta

import (
	"context"
	"fmt"
	"strings"

	"dbharbor/backend/internal/driver"
)

func enrichSchemaObjectsImpl(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	if objs == nil || sqlDrv == nil {
		return
	}
	if objs.Routines == nil {
		objs.Routines = []driver.ObjectItem{}
	}
	if objs.Triggers == nil {
		objs.Triggers = []driver.ObjectItem{}
	}

	eng := normalizeDBType(dbType)
	switch eng {
	case "mysql", "mariadb", "tidb", "oceanbase":
		enrichMySQLFamily(ctx, eng, sqlDrv, objs)
	case "clickhouse":
		enrichClickHouse(ctx, sqlDrv, objs)
	case "postgres", "kingbase":
		enrichPostgresFamily(ctx, sqlDrv, objs)
	case "oracle", "dameng", "dm8":
		enrichOracleFamily(ctx, sqlDrv, objs)
	case "sqlserver":
		enrichSQLServer(ctx, sqlDrv, objs)
	case "sqlite":
		enrichSQLite(ctx, sqlDrv, objs)
	case "duckdb":
		enrichDuckDB(ctx, sqlDrv, objs)
	}
}

func enrichMySQLFamily(ctx context.Context, eng string, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	if db == "" {
		return
	}
	escDB := escapeMySQLOrClickHouseLiteral(db)

	// 1. Routines
	if len(objs.Routines) == 0 {
		q := fmt.Sprintf(
			"SELECT ROUTINE_NAME, ROUTINE_TYPE, COALESCE(ROUTINE_COMMENT, '') AS ROUTINE_COMMENT, COALESCE(CAST(CREATED AS CHAR), '') AS CREATED_AT "+
				"FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = '%s' ORDER BY ROUTINE_NAME",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "ROUTINE_NAME", 0))
				if name == "" {
					continue
				}
				rt := strings.ToUpper(strings.TrimSpace(rowStr(r, idx, "ROUTINE_TYPE", 1)))
				objType := "FUNCTION"
				if rt == "PROCEDURE" {
					objType = "PROCEDURE"
				}
				objs.Routines = append(objs.Routines, driver.ObjectItem{
					Name:      name,
					Type:      objType,
					Comment:   rowStr(r, idx, "ROUTINE_COMMENT", 2),
					CreatedAt: rowStr(r, idx, "CREATED_AT", 3),
				})
			}
		}
	}

	// 2. Triggers (TargetName MUST be parent table)
	if len(objs.Triggers) == 0 {
		q := fmt.Sprintf(
			"SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE, COALESCE(ACTION_TIMING, '') AS ACTION_TIMING, COALESCE(EVENT_MANIPULATION, '') AS EVENT_MANIPULATION "+
				"FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = '%s' ORDER BY TRIGGER_NAME",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "TRIGGER_NAME", 0))
				if name == "" {
					continue
				}
				tbl := strings.TrimSpace(rowStr(r, idx, "EVENT_OBJECT_TABLE", 1))
				timing := strings.TrimSpace(rowStr(r, idx, "ACTION_TIMING", 2))
				manip := strings.TrimSpace(rowStr(r, idx, "EVENT_MANIPULATION", 3))
				cmt := ""
				if tbl != "" {
					cmt = "ON " + tbl
				}
				objs.Triggers = append(objs.Triggers, driver.ObjectItem{
					Name:       name,
					Type:       "TRIGGER",
					TargetName: tbl,
					Engine:     strings.TrimSpace(timing + " " + manip),
					Comment:    cmt,
				})
			}
		}
	}

	// 3. Scheduled Events (MySQL, MariaDB, OceanBase)
	if eng == "mysql" || eng == "mariadb" || eng == "oceanbase" {
		q := fmt.Sprintf(
			"SELECT EVENT_NAME, COALESCE(STATUS, '') AS STATUS, COALESCE(EVENT_TYPE, '') AS EVENT_TYPE, "+
				"COALESCE(CAST(INTERVAL_VALUE AS CHAR), '') AS INTERVAL_VALUE, COALESCE(INTERVAL_FIELD, '') AS INTERVAL_FIELD, "+
				"COALESCE(CAST(EXECUTE_AT AS CHAR), '') AS EXECUTE_AT, COALESCE(EVENT_COMMENT, '') AS EVENT_COMMENT "+
				"FROM information_schema.EVENTS WHERE EVENT_SCHEMA = '%s' ORDER BY EVENT_NAME",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "EVENT_NAME", 0))
				if name == "" {
					continue
				}
				objs.Events = append(objs.Events, driver.ObjectItem{
					Name:    name,
					Type:    "EVENT",
					Status:  strings.TrimSpace(rowStr(r, idx, "STATUS", 1)),
					Comment: rowStr(r, idx, "EVENT_COMMENT", 6),
					Extra: map[string]string{
						"eventType":     rowStr(r, idx, "EVENT_TYPE", 2),
						"intervalValue": rowStr(r, idx, "INTERVAL_VALUE", 3),
						"intervalField": rowStr(r, idx, "INTERVAL_FIELD", 4),
						"executeAt":     rowStr(r, idx, "EXECUTE_AT", 5),
					},
				})
			}
		}
	}

	// 4. Sequences (MariaDB / TiDB)
	if eng == "mariadb" && len(objs.Sequences) == 0 {
		q := fmt.Sprintf(
			"SELECT TABLE_NAME, COALESCE(TABLE_COMMENT, '') AS TABLE_COMMENT, COALESCE(CAST(CREATE_TIME AS CHAR), '') AS CREATED_AT "+
				"FROM information_schema.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'SEQUENCE' ORDER BY TABLE_NAME",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "TABLE_NAME", 0))
				if name == "" {
					continue
				}
				objs.Sequences = append(objs.Sequences, driver.ObjectItem{
					Name:      name,
					Type:      "SEQUENCE",
					Comment:   rowStr(r, idx, "TABLE_COMMENT", 1),
					CreatedAt: rowStr(r, idx, "CREATED_AT", 2),
				})
			}
		}
	} else if eng == "tidb" && len(objs.Sequences) == 0 {
		q := fmt.Sprintf(
			"SELECT SEQUENCE_NAME, COALESCE(CAST(MIN_VALUE AS CHAR), '') AS MIN_VAL, COALESCE(CAST(MAX_VALUE AS CHAR), '') AS MAX_VAL, "+
				"COALESCE(CAST(INCREMENT AS CHAR), '') AS INC_BY, COALESCE(CAST(START AS CHAR), '') AS START_VAL, "+
				"COALESCE(CAST(CYCLE AS CHAR), '') AS CYCLE_FLAG, COALESCE(SEQUENCE_COMMENT, '') AS SEQ_COMMENT "+
				"FROM information_schema.SEQUENCES WHERE SEQUENCE_SCHEMA = '%s' ORDER BY SEQUENCE_NAME",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "SEQUENCE_NAME", 0))
				if name == "" {
					continue
				}
				objs.Sequences = append(objs.Sequences, driver.ObjectItem{
					Name:    name,
					Type:    "SEQUENCE",
					Comment: rowStr(r, idx, "SEQ_COMMENT", 6),
					Extra: map[string]string{
						"minValue":    rowStr(r, idx, "MIN_VAL", 1),
						"maxValue":    rowStr(r, idx, "MAX_VAL", 2),
						"incrementBy": rowStr(r, idx, "INC_BY", 3),
						"startValue":  rowStr(r, idx, "START_VAL", 4),
						"cycle":       rowStr(r, idx, "CYCLE_FLAG", 5),
					},
				})
			}
		}
	}
}

func enrichClickHouse(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	if db == "" {
		db = "default"
	}
	escDB := escapeMySQLOrClickHouseLiteral(db)

	if len(objs.MaterializedViews) == 0 {
		q := fmt.Sprintf(
			"SELECT name, engine, COALESCE(total_rows, 0) AS total_rows, COALESCE(total_bytes, 0) AS total_bytes, comment "+
				"FROM system.tables WHERE database = '%s' AND engine = 'MaterializedView' ORDER BY name",
			escDB,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "name", 0))
				if name == "" {
					continue
				}
				objs.MaterializedViews = append(objs.MaterializedViews, driver.ObjectItem{
					Name:      name,
					Type:      "MATERIALIZED_VIEW",
					Engine:    rowStr(r, idx, "engine", 1),
					Rows:      rowInt64(r, idx, "total_rows", 2),
					DataBytes: rowInt64(r, idx, "total_bytes", 3),
					Comment:   rowStr(r, idx, "comment", 4),
				})
			}
		}
	}

	dictQuery := fmt.Sprintf(
		"SELECT name, COALESCE(status, '') AS status, COALESCE(type, '') AS type, "+
			"COALESCE(element_count, 0) AS element_count, COALESCE(bytes_allocated, 0) AS bytes_allocated, "+
			"COALESCE(last_exception, '') AS last_exception "+
			"FROM system.dictionaries WHERE database = '%s' ORDER BY name",
		escDB,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, dictQuery, 2000); err == nil && len(qr.Rows) > 0 {
		richDicts := make([]driver.ObjectItem, 0, len(qr.Rows))
		seen := make(map[string]struct{}, len(qr.Rows))
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
			richDicts = append(richDicts, driver.ObjectItem{
				Name:      name,
				Type:      "DICTIONARY",
				Status:    rowStr(r, idx, "status", 1),
				Engine:    rowStr(r, idx, "type", 2),
				Rows:      rowInt64(r, idx, "element_count", 3),
				DataBytes: rowInt64(r, idx, "bytes_allocated", 4),
				Comment:   rowStr(r, idx, "last_exception", 5),
			})
		}
		// Keep any unattached dictionary entries reported by system.tables
		for _, existing := range objs.Dictionaries {
			if _, ok := seen[existing.Name]; !ok {
				richDicts = append(richDicts, existing)
			}
		}
		objs.Dictionaries = richDicts
	}

	udfQuery := "SELECT name, COALESCE(create_query, '') AS create_query FROM system.functions WHERE origin = 'SQLUserDefined' ORDER BY name"
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, udfQuery, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			objs.Routines = append(objs.Routines, driver.ObjectItem{
				Name:    name,
				Type:    "FUNCTION",
				Engine:  "SQL UDF",
				Comment: rowStr(r, idx, "create_query", 1),
			})
		}
	}
}

func enrichPostgresFamily(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	sc := strings.TrimSpace(objs.Schema)
	if sc == "" {
		sc = "public"
	}
	escSC := escapeLiteral(sc)

	// 1. Routines
	if len(objs.Routines) == 0 {
		q := fmt.Sprintf(
			"SELECT p.proname AS routine_name, "+
				"CASE WHEN p.prokind = 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END AS routine_type, "+
				"COALESCE(obj_description(p.oid, 'pg_proc'), '') AS comment "+
				"FROM pg_catalog.pg_proc p "+
				"JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace "+
				"WHERE n.nspname = '%s' AND p.prokind IN ('f', 'p') ORDER BY p.proname",
			escSC,
		)
		qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000)
		if err != nil {
			// Fallback for older PostgreSQL / KingbaseES without prokind
			fallback := fmt.Sprintf(
				"SELECT routine_name, routine_type, '' AS comment FROM information_schema.routines WHERE routine_schema = '%s' ORDER BY routine_name",
				escSC,
			)
			qr, idx, err = queryFirstResult(ctx, sqlDrv, db, fallback, 2000)
		}
		if err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "routine_name", 0))
				if name == "" {
					continue
				}
				rt := strings.ToUpper(strings.TrimSpace(rowStr(r, idx, "routine_type", 1)))
				objType := "FUNCTION"
				if rt == "PROCEDURE" {
					objType = "PROCEDURE"
				}
				objs.Routines = append(objs.Routines, driver.ObjectItem{
					Name:    name,
					Type:    objType,
					Comment: rowStr(r, idx, "comment", 2),
				})
			}
		}
	}

	// 2. Triggers (TargetName MUST be parent table)
	if len(objs.Triggers) == 0 {
		q := fmt.Sprintf(
			"SELECT DISTINCT trigger_name, event_object_table, COALESCE(action_timing, '') AS action_timing "+
				"FROM information_schema.triggers WHERE trigger_schema = '%s' ORDER BY trigger_name",
			escSC,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "trigger_name", 0))
				if name == "" {
					continue
				}
				tbl := strings.TrimSpace(rowStr(r, idx, "event_object_table", 1))
				cmt := ""
				if tbl != "" {
					cmt = "ON " + tbl
				}
				objs.Triggers = append(objs.Triggers, driver.ObjectItem{
					Name:       name,
					Type:       "TRIGGER",
					TargetName: tbl,
					Engine:     rowStr(r, idx, "action_timing", 2),
					Comment:    cmt,
				})
			}
		}
	}

	// 3. Materialized Views (with POPULATED / UNPOPULATED status)
	mvQuery := fmt.Sprintf(
		"SELECT m.matviewname AS name, "+
			"CASE WHEN m.ispopulated THEN 'POPULATED' ELSE 'UNPOPULATED' END AS status, "+
			"GREATEST(c.reltuples::bigint, 0) AS rows_est, "+
			"pg_total_relation_size(c.oid) AS total_bytes, "+
			"COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment "+
			"FROM pg_catalog.pg_matviews m "+
			"JOIN pg_catalog.pg_namespace n ON n.nspname = m.schemaname "+
			"JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = m.matviewname AND c.relkind = 'm' "+
			"WHERE m.schemaname = '%s' ORDER BY m.matviewname",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, mvQuery, 2000); err == nil {
		mvs := make([]driver.ObjectItem, 0, len(qr.Rows))
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			mvs = append(mvs, driver.ObjectItem{
				Name:      name,
				Type:      "MATERIALIZED_VIEW",
				Status:    rowStr(r, idx, "status", 1),
				Rows:      rowInt64(r, idx, "rows_est", 2),
				DataBytes: rowInt64(r, idx, "total_bytes", 3),
				Comment:   rowStr(r, idx, "comment", 4),
			})
		}
		objs.MaterializedViews = mvs
	}

	// 4. Sequences (pg_sequences with pg_class relkind='S' fallback for older KingbaseES)
	seqQuery := fmt.Sprintf(
		"SELECT sequencename, COALESCE(data_type::text, '') AS data_type, COALESCE(start_value::text, '') AS start_value, "+
			"COALESCE(min_value::text, '') AS min_value, COALESCE(max_value::text, '') AS max_value, "+
			"COALESCE(increment_by::text, '') AS increment_by, COALESCE(cycle::text, 'false') AS cycle, "+
			"COALESCE(cache_size::text, '') AS cache_size, COALESCE(last_value::text, '') AS last_value "+
			"FROM pg_catalog.pg_sequences WHERE schemaname = '%s' ORDER BY sequencename",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, seqQuery, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "sequencename", 0))
			if name == "" {
				continue
			}
			objs.Sequences = append(objs.Sequences, driver.ObjectItem{
				Name: name,
				Type: "SEQUENCE",
				Extra: map[string]string{
					"dataType":    rowStr(r, idx, "data_type", 1),
					"startValue":  rowStr(r, idx, "start_value", 2),
					"minValue":    rowStr(r, idx, "min_value", 3),
					"maxValue":    rowStr(r, idx, "max_value", 4),
					"incrementBy": rowStr(r, idx, "increment_by", 5),
					"cycle":       rowStr(r, idx, "cycle", 6),
					"cache":       rowStr(r, idx, "cache_size", 7),
					"lastValue":   rowStr(r, idx, "last_value", 8),
				},
			})
		}
	} else {
		fallbackSeq := fmt.Sprintf(
			"SELECT c.relname AS sequencename, COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment "+
				"FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace "+
				"WHERE n.nspname = '%s' AND c.relkind = 'S' ORDER BY c.relname",
			escSC,
		)
		if qr2, idx2, err2 := queryFirstResult(ctx, sqlDrv, db, fallbackSeq, 2000); err2 == nil {
			for _, r := range qr2.Rows {
				name := strings.TrimSpace(rowStr(r, idx2, "sequencename", 0))
				if name == "" {
					continue
				}
				objs.Sequences = append(objs.Sequences, driver.ObjectItem{
					Name:    name,
					Type:    "SEQUENCE",
					Comment: rowStr(r, idx2, "comment", 1),
				})
			}
		}
	}

	// 5. Extensions (installed + available)
	instExtQuery := "SELECT e.extname AS name, COALESCE(e.extversion, '') AS extversion, COALESCE(n.nspname, '') AS schema_name, " +
		"COALESCE(obj_description(e.oid, 'pg_extension'), '') AS comment " +
		"FROM pg_catalog.pg_extension e LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace ORDER BY e.extname"
	installedNames := make(map[string]struct{})
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, instExtQuery, 500); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			installedNames[name] = struct{}{}
			ver := strings.TrimSpace(rowStr(r, idx, "extversion", 1))
			status := "INSTALLED"
			if ver != "" {
				status = "INSTALLED " + ver
			}
			objs.Extensions = append(objs.Extensions, driver.ObjectItem{
				Name:       name,
				Type:       "EXTENSION",
				Status:     status,
				TargetName: rowStr(r, idx, "schema_name", 2),
				Comment:    rowStr(r, idx, "comment", 3),
				Extra: map[string]string{
					"installedVersion": ver,
				},
			})
		}
	}

	availExtQuery := "SELECT name, COALESCE(default_version, '') AS default_version, COALESCE(comment, '') AS comment " +
		"FROM pg_catalog.pg_available_extensions WHERE installed_version IS NULL ORDER BY name LIMIT 200"
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, availExtQuery, 250); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			if _, already := installedNames[name]; already {
				continue
			}
			defVer := strings.TrimSpace(rowStr(r, idx, "default_version", 1))
			objs.Extensions = append(objs.Extensions, driver.ObjectItem{
				Name:    name,
				Type:    "EXTENSION",
				Status:  "AVAILABLE",
				Comment: rowStr(r, idx, "comment", 2),
				Extra: map[string]string{
					"defaultVersion":   defVer,
					"installedVersion": "",
				},
			})
		}
	}

	// 6. Custom Types (ENUM, DOMAIN, standalone COMPOSITE)
	typeQuery := fmt.Sprintf(
		"SELECT t.typname AS name, t.typtype::text AS typtype, COALESCE(obj_description(t.oid, 'pg_type'), '') AS comment, "+
			"CASE "+
			"WHEN t.typtype = 'e' THEN COALESCE((SELECT string_agg(quote_literal(e.enumlabel), ', ' ORDER BY e.enumsortorder) FROM pg_catalog.pg_enum e WHERE e.enumtypid = t.oid), '') "+
			"WHEN t.typtype = 'd' THEN pg_catalog.format_type(t.typbasetype, t.typtypmod) || CASE WHEN t.typnotnull THEN ' NOT NULL' ELSE '' END || COALESCE(' DEFAULT ' || t.typdefault, '') "+
			"WHEN t.typtype = 'c' THEN COALESCE((SELECT string_agg(quote_ident(a.attname) || ' ' || pg_catalog.format_type(a.atttypid, a.atttypmod), ', ' ORDER BY a.attnum) FROM pg_catalog.pg_attribute a WHERE a.attrelid = t.typrelid AND a.attnum > 0 AND NOT a.attisdropped), '') "+
			"ELSE '' END AS definition "+
			"FROM pg_catalog.pg_type t "+
			"JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace "+
			"LEFT JOIN pg_catalog.pg_class c ON c.oid = t.typrelid "+
			"WHERE n.nspname = '%s' AND t.typtype IN ('e', 'd', 'c') AND (t.typrelid = 0 OR c.relkind = 'c') "+
			"ORDER BY t.typname",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, typeQuery, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			tt := strings.TrimSpace(rowStr(r, idx, "typtype", 1))
			engine := "TYPE"
			switch tt {
			case "e":
				engine = "ENUM"
			case "d":
				engine = "DOMAIN"
			case "c":
				engine = "COMPOSITE"
			}
			cmt := rowStr(r, idx, "comment", 2)
			def := rowStr(r, idx, "definition", 3)
			if cmt == "" {
				cmt = def
			}
			objs.Types = append(objs.Types, driver.ObjectItem{
				Name:    name,
				Type:    "TYPE",
				Engine:  engine,
				Comment: cmt,
				Extra: map[string]string{
					"definition": def,
					"typeKind":   engine,
				},
			})
		}
	}
}

func enrichOracleFamily(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	ownerCond := oracleOwnerCondition("OWNER", objs.Schema)
	seqOwnerCond := oracleOwnerCondition("SEQUENCE_OWNER", objs.Schema)

	// 1. Routines
	if len(objs.Routines) == 0 {
		q := fmt.Sprintf(
			"SELECT OBJECT_NAME, OBJECT_TYPE, COALESCE(STATUS, '') AS STATUS, COALESCE(TO_CHAR(CREATED, 'YYYY-MM-DD HH24:MI:SS'), '') AS CREATED_AT "+
				"FROM ALL_OBJECTS WHERE %s AND OBJECT_TYPE IN ('PROCEDURE', 'FUNCTION') ORDER BY OBJECT_NAME",
			ownerCond,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "OBJECT_NAME", 0))
				if name == "" {
					continue
				}
				rt := strings.ToUpper(strings.TrimSpace(rowStr(r, idx, "OBJECT_TYPE", 1)))
				objType := "FUNCTION"
				if rt == "PROCEDURE" {
					objType = "PROCEDURE"
				}
				objs.Routines = append(objs.Routines, driver.ObjectItem{
					Name:      name,
					Type:      objType,
					Status:    rowStr(r, idx, "STATUS", 2),
					CreatedAt: rowStr(r, idx, "CREATED_AT", 3),
				})
			}
		}
	}

	// 2. Triggers (TargetName MUST be parent table)
	if len(objs.Triggers) == 0 {
		q := fmt.Sprintf(
			"SELECT TRIGGER_NAME, COALESCE(TABLE_NAME, '') AS TABLE_NAME, COALESCE(TRIGGER_TYPE, '') AS TRIGGER_TYPE, "+
				"COALESCE(TRIGGERING_EVENT, '') AS TRIGGERING_EVENT, COALESCE(STATUS, '') AS STATUS "+
				"FROM ALL_TRIGGERS WHERE %s ORDER BY TRIGGER_NAME",
			ownerCond,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "TRIGGER_NAME", 0))
				if name == "" {
					continue
				}
				tbl := strings.TrimSpace(rowStr(r, idx, "TABLE_NAME", 1))
				tt := strings.TrimSpace(rowStr(r, idx, "TRIGGER_TYPE", 2))
				ev := strings.TrimSpace(rowStr(r, idx, "TRIGGERING_EVENT", 3))
				cmt := ""
				if tbl != "" {
					cmt = "ON " + tbl
				}
				objs.Triggers = append(objs.Triggers, driver.ObjectItem{
					Name:       name,
					Type:       "TRIGGER",
					TargetName: tbl,
					Engine:     strings.TrimSpace(tt + " " + ev),
					Status:     rowStr(r, idx, "STATUS", 4),
					Comment:    cmt,
				})
			}
		}
	}

	// 3. Sequences
	seqQ := fmt.Sprintf(
		"SELECT SEQUENCE_NAME, TO_CHAR(MIN_VALUE) AS MIN_VAL, TO_CHAR(MAX_VALUE) AS MAX_VAL, "+
			"TO_CHAR(INCREMENT_BY) AS INC_BY, COALESCE(CYCLE_FLAG, 'N') AS CYCLE_FLAG, "+
			"TO_CHAR(CACHE_SIZE) AS CACHE_SZ, TO_CHAR(LAST_NUMBER) AS LAST_NUM "+
			"FROM ALL_SEQUENCES WHERE %s ORDER BY SEQUENCE_NAME",
		seqOwnerCond,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, seqQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "SEQUENCE_NAME", 0))
			if name == "" {
				continue
			}
			objs.Sequences = append(objs.Sequences, driver.ObjectItem{
				Name: name,
				Type: "SEQUENCE",
				Extra: map[string]string{
					"minValue":    rowStr(r, idx, "MIN_VAL", 1),
					"maxValue":    rowStr(r, idx, "MAX_VAL", 2),
					"incrementBy": rowStr(r, idx, "INC_BY", 3),
					"cycle":       rowStr(r, idx, "CYCLE_FLAG", 4),
					"cache":       rowStr(r, idx, "CACHE_SZ", 5),
					"lastValue":   rowStr(r, idx, "LAST_NUM", 6),
				},
			})
		}
	}

	// 4. Materialized Views
	mvQ := fmt.Sprintf(
		"SELECT MVIEW_NAME, COALESCE(STALENESS, '') AS STALENESS, COALESCE(REFRESH_MODE, '') AS REFRESH_MODE, "+
			"COALESCE(REFRESH_METHOD, '') AS REFRESH_METHOD, COALESCE(TO_CHAR(LAST_REFRESH_DATE, 'YYYY-MM-DD HH24:MI:SS'), '') AS LAST_REFRESH "+
			"FROM ALL_MVIEWS WHERE %s ORDER BY MVIEW_NAME",
		ownerCond,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, mvQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "MVIEW_NAME", 0))
			if name == "" {
				continue
			}
			objs.MaterializedViews = append(objs.MaterializedViews, driver.ObjectItem{
				Name:   name,
				Type:   "MATERIALIZED_VIEW",
				Status: rowStr(r, idx, "STALENESS", 1),
				Extra: map[string]string{
					"refreshMode":   rowStr(r, idx, "REFRESH_MODE", 2),
					"refreshMethod": rowStr(r, idx, "REFRESH_METHOD", 3),
					"lastRefresh":   rowStr(r, idx, "LAST_REFRESH", 4),
				},
			})
		}
	}

	// 5. Packages & Package Bodies
	pkgQ := fmt.Sprintf(
		"SELECT OBJECT_NAME, OBJECT_TYPE, COALESCE(STATUS, '') AS STATUS, COALESCE(TO_CHAR(CREATED, 'YYYY-MM-DD HH24:MI:SS'), '') AS CREATED_AT "+
			"FROM ALL_OBJECTS WHERE %s AND OBJECT_TYPE IN ('PACKAGE', 'PACKAGE BODY') ORDER BY OBJECT_NAME, OBJECT_TYPE",
		ownerCond,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, pkgQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "OBJECT_NAME", 0))
			if name == "" {
				continue
			}
			ot := strings.ToUpper(strings.TrimSpace(rowStr(r, idx, "OBJECT_TYPE", 1)))
			itemType := "PACKAGE"
			if ot == "PACKAGE BODY" {
				itemType = "PACKAGE_BODY"
			}
			objs.Packages = append(objs.Packages, driver.ObjectItem{
				Name:      name,
				Type:      itemType,
				Status:    rowStr(r, idx, "STATUS", 2),
				CreatedAt: rowStr(r, idx, "CREATED_AT", 3),
			})
		}
	}

	// 6. Synonyms
	synQ := fmt.Sprintf(
		"SELECT SYNONYM_NAME, COALESCE(TABLE_OWNER, '') AS TABLE_OWNER, COALESCE(TABLE_NAME, '') AS TABLE_NAME, COALESCE(DB_LINK, '') AS DB_LINK "+
			"FROM ALL_SYNONYMS WHERE %s ORDER BY SYNONYM_NAME",
		ownerCond,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, synQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "SYNONYM_NAME", 0))
			if name == "" {
				continue
			}
			tOwner := strings.TrimSpace(rowStr(r, idx, "TABLE_OWNER", 1))
			tName := strings.TrimSpace(rowStr(r, idx, "TABLE_NAME", 2))
			dbLink := strings.TrimSpace(rowStr(r, idx, "DB_LINK", 3))
			target := tName
			if tOwner != "" && tName != "" {
				target = tOwner + "." + tName
			}
			if dbLink != "" {
				target += "@" + dbLink
			}
			objs.Synonyms = append(objs.Synonyms, driver.ObjectItem{
				Name:       name,
				Type:       "SYNONYM",
				TargetName: target,
			})
		}
	}

	// 7. Database Links (owned by schema or PUBLIC)
	dblQ := fmt.Sprintf(
		"SELECT DB_LINK, COALESCE(USERNAME, '') AS USERNAME, COALESCE(HOST, '') AS HOST, COALESCE(TO_CHAR(CREATED, 'YYYY-MM-DD HH24:MI:SS'), '') AS CREATED_AT, COALESCE(OWNER, '') AS OWNER "+
			"FROM ALL_DB_LINKS WHERE (%s OR OWNER = 'PUBLIC') ORDER BY DB_LINK",
		ownerCond,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, dblQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "DB_LINK", 0))
			if name == "" {
				continue
			}
			objs.DbLinks = append(objs.DbLinks, driver.ObjectItem{
				Name:       name,
				Type:       "DB_LINK",
				TargetName: rowStr(r, idx, "HOST", 2),
				CreatedAt:  rowStr(r, idx, "CREATED_AT", 3),
				Extra: map[string]string{
					"username": rowStr(r, idx, "USERNAME", 1),
					"owner":    rowStr(r, idx, "OWNER", 4),
				},
			})
		}
	}
}

func enrichSQLServer(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	sc := strings.TrimSpace(objs.Schema)
	if sc == "" {
		sc = "dbo"
	}
	escSC := escapeLiteral(sc)

	// 1. Routines
	if len(objs.Routines) == 0 {
		q := fmt.Sprintf(
			"SELECT ROUTINE_NAME, ROUTINE_TYPE, COALESCE(CONVERT(VARCHAR(19), CREATED, 120), '') AS CREATED_AT "+
				"FROM INFORMATION_SCHEMA.ROUTINES WHERE ROUTINE_SCHEMA = N'%s' ORDER BY ROUTINE_NAME",
			escSC,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "ROUTINE_NAME", 0))
				if name == "" {
					continue
				}
				rt := strings.ToUpper(strings.TrimSpace(rowStr(r, idx, "ROUTINE_TYPE", 1)))
				objType := "FUNCTION"
				if rt == "PROCEDURE" {
					objType = "PROCEDURE"
				}
				objs.Routines = append(objs.Routines, driver.ObjectItem{
					Name:      name,
					Type:      objType,
					CreatedAt: rowStr(r, idx, "CREATED_AT", 2),
				})
			}
		}
	}

	// 2. Triggers (TargetName MUST be parent table)
	if len(objs.Triggers) == 0 {
		q := fmt.Sprintf(
			"SELECT tr.name AS trigger_name, tb.name AS table_name, "+
				"CASE WHEN tr.is_disabled = 1 THEN 'DISABLED' ELSE 'ENABLED' END AS status, "+
				"COALESCE(CONVERT(VARCHAR(19), tr.create_date, 120), '') AS created_at "+
				"FROM sys.triggers tr "+
				"JOIN sys.tables tb ON tr.parent_id = tb.object_id "+
				"JOIN sys.schemas s ON tb.schema_id = s.schema_id "+
				"WHERE s.name = N'%s' ORDER BY tr.name",
			escSC,
		)
		if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, q, 2000); err == nil {
			for _, r := range qr.Rows {
				name := strings.TrimSpace(rowStr(r, idx, "trigger_name", 0))
				if name == "" {
					continue
				}
				tbl := strings.TrimSpace(rowStr(r, idx, "table_name", 1))
				cmt := ""
				if tbl != "" {
					cmt = "ON " + tbl
				}
				objs.Triggers = append(objs.Triggers, driver.ObjectItem{
					Name:       name,
					Type:       "TRIGGER",
					TargetName: tbl,
					Status:     rowStr(r, idx, "status", 2),
					CreatedAt:  rowStr(r, idx, "created_at", 3),
					Comment:    cmt,
				})
			}
		}
	}

	// 3. Sequences
	seqQ := fmt.Sprintf(
		"SELECT seq.name AS sequence_name, COALESCE(TYPE_NAME(seq.user_type_id), '') AS data_type, "+
			"COALESCE(CONVERT(VARCHAR(50), seq.start_value), '') AS start_value, "+
			"COALESCE(CONVERT(VARCHAR(50), seq.increment), '') AS increment_by, "+
			"COALESCE(CONVERT(VARCHAR(50), seq.minimum_value), '') AS min_value, "+
			"COALESCE(CONVERT(VARCHAR(50), seq.maximum_value), '') AS max_value, "+
			"CASE WHEN seq.is_cycling = 1 THEN 'true' ELSE 'false' END AS cycle, "+
			"COALESCE(CONVERT(VARCHAR(50), seq.current_value), '') AS current_value "+
			"FROM sys.sequences seq JOIN sys.schemas s ON seq.schema_id = s.schema_id "+
			"WHERE s.name = N'%s' ORDER BY seq.name",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, seqQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "sequence_name", 0))
			if name == "" {
				continue
			}
			objs.Sequences = append(objs.Sequences, driver.ObjectItem{
				Name: name,
				Type: "SEQUENCE",
				Extra: map[string]string{
					"dataType":    rowStr(r, idx, "data_type", 1),
					"startValue":  rowStr(r, idx, "start_value", 2),
					"incrementBy": rowStr(r, idx, "increment_by", 3),
					"minValue":    rowStr(r, idx, "min_value", 4),
					"maxValue":    rowStr(r, idx, "max_value", 5),
					"cycle":       rowStr(r, idx, "cycle", 6),
					"lastValue":   rowStr(r, idx, "current_value", 7),
				},
			})
		}
	}

	// 4. Synonyms
	synQ := fmt.Sprintf(
		"SELECT syn.name AS synonym_name, COALESCE(syn.base_object_name, '') AS base_object_name, "+
			"COALESCE(CONVERT(VARCHAR(19), syn.create_date, 120), '') AS created_at "+
			"FROM sys.synonyms syn JOIN sys.schemas s ON syn.schema_id = s.schema_id "+
			"WHERE s.name = N'%s' ORDER BY syn.name",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, synQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "synonym_name", 0))
			if name == "" {
				continue
			}
			objs.Synonyms = append(objs.Synonyms, driver.ObjectItem{
				Name:       name,
				Type:       "SYNONYM",
				TargetName: rowStr(r, idx, "base_object_name", 1),
				CreatedAt:  rowStr(r, idx, "created_at", 2),
			})
		}
	}
}

func enrichSQLite(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	if len(objs.Triggers) > 0 {
		return
	}
	q := "SELECT name, COALESCE(tbl_name, '') AS tbl_name FROM sqlite_master WHERE type = 'trigger' ORDER BY name"
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, objs.Database, q, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "name", 0))
			if name == "" {
				continue
			}
			tbl := strings.TrimSpace(rowStr(r, idx, "tbl_name", 1))
			cmt := ""
			if tbl != "" {
				cmt = "ON " + tbl
			}
			objs.Triggers = append(objs.Triggers, driver.ObjectItem{
				Name:       name,
				Type:       "TRIGGER",
				TargetName: tbl,
				Comment:    cmt,
			})
		}
	}
}

func enrichDuckDB(ctx context.Context, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	db := strings.TrimSpace(objs.Database)
	sc := strings.TrimSpace(objs.Schema)
	if sc == "" {
		sc = "main"
	}
	escSC := escapeLiteral(sc)

	// 1. Sequences
	seqQ := fmt.Sprintf(
		"SELECT sequence_name, CAST(start_value AS VARCHAR) AS start_value, CAST(increment_by AS VARCHAR) AS increment_by, "+
			"CAST(min_value AS VARCHAR) AS min_value, CAST(max_value AS VARCHAR) AS max_value, "+
			"CAST(cycle AS VARCHAR) AS cycle, COALESCE(CAST(last_value AS VARCHAR), '') AS last_value "+
			"FROM duckdb_sequences() WHERE schema_name = '%s' ORDER BY sequence_name",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, seqQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "sequence_name", 0))
			if name == "" {
				continue
			}
			objs.Sequences = append(objs.Sequences, driver.ObjectItem{
				Name: name,
				Type: "SEQUENCE",
				Extra: map[string]string{
					"startValue":  rowStr(r, idx, "start_value", 1),
					"incrementBy": rowStr(r, idx, "increment_by", 2),
					"minValue":    rowStr(r, idx, "min_value", 3),
					"maxValue":    rowStr(r, idx, "max_value", 4),
					"cycle":       rowStr(r, idx, "cycle", 5),
					"lastValue":   rowStr(r, idx, "last_value", 6),
				},
			})
		}
	}

	// 2. Macros & Table Macros
	macroQ := fmt.Sprintf(
		"SELECT function_name, function_type, COALESCE(CAST(parameters AS VARCHAR), '') AS parameters, "+
			"COALESCE(macro_definition, '') AS definition, COALESCE(comment, '') AS comment "+
			"FROM duckdb_functions() WHERE schema_name = '%s' AND function_type IN ('macro', 'table_macro') AND NOT internal "+
			"ORDER BY function_name",
		escSC,
	)
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, macroQ, 2000); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "function_name", 0))
			if name == "" {
				continue
			}
			ft := strings.ToLower(strings.TrimSpace(rowStr(r, idx, "function_type", 1)))
			itemType := "MACRO"
			if ft == "table_macro" {
				itemType = "TABLE_MACRO"
			}
			objs.Macros = append(objs.Macros, driver.ObjectItem{
				Name:    name,
				Type:    itemType,
				Comment: rowStr(r, idx, "comment", 4),
				Extra: map[string]string{
					"parameters": rowStr(r, idx, "parameters", 2),
					"definition": rowStr(r, idx, "definition", 3),
				},
			})
		}
	}

	// 3. Extensions
	extQ := "SELECT extension_name, CAST(loaded AS VARCHAR) AS loaded, CAST(installed AS VARCHAR) AS installed, " +
		"COALESCE(description, '') AS description, COALESCE(extension_version, '') AS extension_version " +
		"FROM duckdb_extensions() ORDER BY extension_name"
	if qr, idx, err := queryFirstResult(ctx, sqlDrv, db, extQ, 500); err == nil {
		for _, r := range qr.Rows {
			name := strings.TrimSpace(rowStr(r, idx, "extension_name", 0))
			if name == "" {
				continue
			}
			loaded := strings.ToLower(strings.TrimSpace(rowStr(r, idx, "loaded", 1)))
			installed := strings.ToLower(strings.TrimSpace(rowStr(r, idx, "installed", 2)))
			status := "AVAILABLE"
			if loaded == "true" || loaded == "1" {
				status = "LOADED"
			} else if installed == "true" || installed == "1" {
				status = "INSTALLED"
			}
			objs.Extensions = append(objs.Extensions, driver.ObjectItem{
				Name:    name,
				Type:    "EXTENSION",
				Status:  status,
				Comment: rowStr(r, idx, "description", 3),
				Extra: map[string]string{
					"version": rowStr(r, idx, "extension_version", 4),
				},
			})
		}
	}
}
