package sqlmeta

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

var (
	reValidExtensionName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	reValidExtensionVer  = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	reValidParamName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reValidTimestamp     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2})?)?$`)
	reTNSHostExtract     = regexp.MustCompile(`(?i)\bHOST\s*=\s*([^\)\s]+)`)
	reTNSForbiddenProto  = regexp.MustCompile(`(?i)\b(PROTOCOL\s*=\s*(IPC|BEQ|NMP|PIPE)|PROGRAM\s*=|EXTPROC)`)
)

var blockedPGWebExtensions = map[string]struct{}{
	"adminpack":    {},
	"file_fdw":     {},
	"dblink":       {},
	"postgres_fdw": {},
	"plperlu":      {},
	"plpython3u":   {},
	"plpythonu":    {},
	"plpython2u":   {},
	"pltclu":       {},
	"plsh":         {},
}

var allowedEventUnits = map[string]struct{}{
	"SECOND": {},
	"MINUTE": {},
	"HOUR":   {},
	"DAY":    {},
	"WEEK":   {},
	"MONTH":  {},
}

func formatDuckDBExtensionRef(name string) string {
	if strings.Contains(name, "-") {
		return "'" + escapeLiteral(name) + "'"
	}
	return name
}

func validateDuckDBSandboxSQL(sqlText string) error {
	return sqlbase.ValidateDuckDBRestrictedWebSQL(sqlText)
}

func validateNoDefinerForNonAdminWeb(req driver.ObjectActionRequest, texts ...string) error {
	if !req.WebMode || req.IsAdmin {
		return nil
	}
	for _, t := range texts {
		if sqlbase.HasSQLDefinerClause(t) {
			return errors.New("DEFINER and SQL SECURITY DEFINER clauses are not permitted for non-administrator accounts in Web mode")
		}
	}
	return nil
}

func validateDbLinkHostSSRF(ctx context.Context, allowPrivIP bool, rawHost string) error {
	h := strings.TrimSpace(rawHost)
	if h == "" {
		return errors.New("database link host cannot be empty")
	}
	if strings.ContainsAny(h, "'\r\n\x00") {
		return errors.New("database link host contains disallowed characters")
	}
	if allowPrivIP {
		return nil
	}
	if strings.Contains(h, "(") {
		if reTNSForbiddenProto.MatchString(h) {
			return errors.New("TNS local IPC/BEQ/PROGRAM descriptors are forbidden in restricted mode")
		}
		matches := reTNSHostExtract.FindAllStringSubmatch(h, -1)
		if len(matches) == 0 {
			return errors.New("TNS descriptor must contain an explicit HOST=... address for security validation")
		}
		for _, m := range matches {
			if err := sqlbase.ValidateRemoteHost(ctx, m[1]); err != nil {
				return err
			}
		}
		return nil
	}
	return sqlbase.ValidateRemoteHost(ctx, h)
}

func executeObjectActionImpl(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, req driver.ObjectActionRequest) ([]string, error) {
	eng := normalizeDBType(dbType)
	d := dialectForEngine(eng)
	action := strings.ToUpper(strings.TrimSpace(req.Action))
	objType := strings.ToUpper(strings.TrimSpace(req.ObjectType))
	if objType == "MATERIALIZED VIEW" {
		objType = "MATERIALIZED_VIEW"
	} else if objType == "PACKAGE BODY" {
		objType = "PACKAGE_BODY"
	} else if objType == "DATABASE LINK" {
		objType = "DB_LINK"
	} else if objType == "TABLE MACRO" {
		objType = "TABLE_MACRO"
	}
	req.ObjectType = objType
	req.Action = action

	// Global DEFINER check for non-admin Web requests
	var specBody, specSelect, specMacro string
	if req.Spec != nil {
		specBody = req.Spec.BodySQL
		specSelect = req.Spec.SelectSQL
		specMacro = req.Spec.MacroExpr
	}
	if err := validateNoDefinerForNonAdminWeb(req, req.RoutineSQL, req.ViewSQL, specBody, specSelect, specMacro); err != nil {
		return nil, err
	}

	// 1. SQLite HEALTH_CHECK (does not require ObjectName)
	if action == "HEALTH_CHECK" {
		return handleSQLiteHealthCheck(ctx, eng, sqlDrv, req)
	}

	objName := strings.TrimSpace(req.ObjectName)
	if objName == "" {
		return nil, errors.New("object name cannot be empty")
	}
	if strings.ContainsRune(objName, 0) {
		return nil, errors.New("object name contains null byte")
	}

	// DuckDB WebMode sandbox check on any raw SQL payloads
	if eng == "duckdb" && req.WebMode {
		for _, raw := range []string{req.RoutineSQL, req.ViewSQL, specBody, specSelect, specMacro} {
			if strings.TrimSpace(raw) != "" {
				if err := validateDuckDBSandboxSQL(raw); err != nil {
					return nil, err
				}
			}
		}
	}

	// Normalize CREATE_OR_REPLACE on legacy VIEW / ROUTINE / TRIGGER when Spec == nil
	if action == "CREATE_OR_REPLACE" && req.Spec == nil {
		switch objType {
		case "VIEW":
			req.Action = "CREATE_VIEW"
			req.ReplaceExisting = true
			action = "CREATE_VIEW"
		case "PROCEDURE":
			req.Action = "CREATE_ROUTINE"
			req.ReplaceExisting = true
			action = "CREATE_ROUTINE"
		case "FUNCTION":
			if eng != "clickhouse" {
				req.Action = "CREATE_ROUTINE"
				req.ReplaceExisting = true
				action = "CREATE_ROUTINE"
			}
		case "TRIGGER":
			req.Action = "CREATE_TRIGGER"
			req.ReplaceExisting = true
			action = "CREATE_TRIGGER"
		}
	}

	// Dispatch specialized Phase 2 object types & lifecycle actions
	var (
		execSQLs    []string
		displaySQLs []string
		handled     bool
		buildErr    error
	)

	switch objType {
	case "EVENT":
		execSQLs, buildErr = buildEventActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "SEQUENCE":
		execSQLs, buildErr = buildSequenceActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "MATERIALIZED_VIEW":
		execSQLs, buildErr = buildMatViewActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "DICTIONARY":
		execSQLs, buildErr = buildDictionaryActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "PACKAGE", "PACKAGE_BODY":
		execSQLs, buildErr = buildPackageActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "SYNONYM":
		execSQLs, buildErr = buildSynonymActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "DB_LINK":
		execSQLs, displaySQLs, buildErr = buildDbLinkActionSQLs(ctx, eng, d, req)
		handled = true

	case "TYPE":
		execSQLs, buildErr = buildPostgresTypeActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "EXTENSION":
		execSQLs, buildErr = buildExtensionActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "MACRO", "TABLE_MACRO":
		execSQLs, buildErr = buildDuckDBMacroActionSQLs(eng, d, req)
		displaySQLs = execSQLs
		handled = true

	case "FUNCTION":
		if eng == "clickhouse" {
			execSQLs, buildErr = buildClickHouseUDFActionSQLs(d, req)
			displaySQLs = execSQLs
			handled = true
		}

	case "TRIGGER":
		if action == "ENABLE" || action == "DISABLE" {
			execSQLs, buildErr = buildTriggerToggleSQLs(ctx, eng, d, sqlDrv, req)
			displaySQLs = execSQLs
			handled = true
		}
	}

	if handled {
		if buildErr != nil {
			return nil, buildErr
		}
		if req.PreviewOnly {
			return displaySQLs, nil
		}
		if sqlDrv == nil {
			return nil, errors.New("SQL driver is required for non-preview execution")
		}
		for _, stmt := range execSQLs {
			if _, err := sqlDrv.ExecuteSQL(ctx, req.Database, stmt, 0); err != nil {
				return nil, err
			}
		}
		return displaySQLs, nil
	}

	// Legacy TABLE / VIEW / FUNCTION / PROCEDURE / TRIGGER actions via BuildObjectActionSQLs + ManageObject
	if (eng == "postgres" || eng == "kingbase") && objType == "TRIGGER" && strings.TrimSpace(req.TargetName) == "" && !req.PreviewOnly && sqlDrv != nil {
		if tbl := lookupPostgresTriggerTable(ctx, sqlDrv, req.Database, req.Schema, objName); tbl != "" {
			req.TargetName = tbl
		}
	}

	previewSQLs, err := sqlbase.BuildObjectActionSQLs(d, req)
	if err != nil {
		return nil, err
	}
	if req.PreviewOnly {
		return previewSQLs, nil
	}
	if sqlDrv == nil {
		return nil, errors.New("SQL driver is required for non-preview execution")
	}
	if err := sqlDrv.ManageObject(ctx, req); err != nil {
		return nil, err
	}
	return previewSQLs, nil
}

func lookupPostgresTriggerTable(ctx context.Context, sqlDrv driver.SQLDriver, database, schema, trigName string) string {
	sc := strings.TrimSpace(schema)
	if sc == "" {
		sc = "public"
	}
	q := fmt.Sprintf(
		"SELECT event_object_table FROM information_schema.triggers WHERE trigger_schema = '%s' AND trigger_name = '%s' LIMIT 1",
		escapeLiteral(sc), escapeLiteral(trigName),
	)
	qr, idx, err := queryFirstResult(ctx, sqlDrv, database, q, 1)
	if err != nil || len(qr.Rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rowStr(qr.Rows[0], idx, "event_object_table", 0))
}

func handleSQLiteHealthCheck(ctx context.Context, eng string, sqlDrv driver.SQLDriver, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "sqlite" {
		return nil, fmt.Errorf("HEALTH_CHECK is only supported on SQLite (got %s)", eng)
	}
	checkKind := strings.ToLower(strings.TrimSpace(req.TargetName))
	if checkKind == "" {
		checkKind = strings.ToLower(strings.TrimSpace(req.ObjectName))
	}
	pragmaSQL := "PRAGMA quick_check"
	switch checkKind {
	case "integrity_check":
		pragmaSQL = "PRAGMA integrity_check"
	case "foreign_key_check":
		pragmaSQL = "PRAGMA foreign_key_check"
	case "", "quick_check", "database", "main":
		pragmaSQL = "PRAGMA quick_check"
	default:
		return nil, fmt.Errorf("unsupported SQLite health check pragma %q", checkKind)
	}
	if req.PreviewOnly {
		return []string{pragmaSQL}, nil
	}
	if sqlDrv == nil {
		return nil, errors.New("SQL driver is required for HEALTH_CHECK execution")
	}
	qr, _, err := queryFirstResult(ctx, sqlDrv, req.Database, pragmaSQL, 100)
	if err != nil {
		return nil, err
	}
	summary := "ok"
	if len(qr.Rows) == 0 {
		if pragmaSQL == "PRAGMA foreign_key_check" {
			summary = "0 foreign key violations"
		}
	} else {
		var msgs []string
		for _, r := range qr.Rows {
			if len(r) > 0 {
				msgs = append(msgs, cellStr(r[0]))
			}
		}
		if len(msgs) > 0 {
			summary = strings.Join(msgs, "; ")
		}
	}
	return []string{fmt.Sprintf("%s /* result: %s */", pragmaSQL, strings.ReplaceAll(summary, "*/", "* /"))}, nil
}

func buildEventActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "mysql" && eng != "mariadb" && eng != "oceanbase" {
		return nil, fmt.Errorf("scheduled events are not supported on %s", eng)
	}
	qEv := sqlbase.QualifyTable(d, req.Database, "", req.ObjectName)
	switch req.Action {
	case "DROP":
		return []string{"DROP EVENT IF EXISTS " + qEv}, nil
	case "ENABLE":
		return []string{fmt.Sprintf("ALTER EVENT %s ENABLE", qEv)}, nil
	case "DISABLE":
		return []string{fmt.Sprintf("ALTER EVENT %s DISABLE", qEv)}, nil
	case "CREATE_OR_REPLACE", "ALTER":
		if req.Spec != nil {
			spec := req.Spec
			var sched strings.Builder
			switch strings.ToUpper(strings.TrimSpace(spec.ScheduleKind)) {
			case "EVERY", "":
				if spec.EveryValue <= 0 {
					return nil, errors.New("event EVERY interval value must be a positive integer")
				}
				unit := strings.ToUpper(strings.TrimSpace(spec.EveryUnit))
				if _, ok := allowedEventUnits[unit]; !ok {
					return nil, fmt.Errorf("invalid event interval unit %q", spec.EveryUnit)
				}
				fmt.Fprintf(&sched, "ON SCHEDULE EVERY %d %s", spec.EveryValue, unit)
				if s := strings.TrimSpace(spec.StartsAt); s != "" {
					if !reValidTimestamp.MatchString(s) {
						return nil, fmt.Errorf("invalid event STARTS timestamp %q", s)
					}
					fmt.Fprintf(&sched, " STARTS '%s'", s)
				}
				if e := strings.TrimSpace(spec.EndsAt); e != "" {
					if !reValidTimestamp.MatchString(e) {
						return nil, fmt.Errorf("invalid event ENDS timestamp %q", e)
					}
					fmt.Fprintf(&sched, " ENDS '%s'", e)
				}
			case "AT":
				at := strings.TrimSpace(spec.ExecuteAt)
				if !reValidTimestamp.MatchString(at) {
					return nil, fmt.Errorf("invalid event AT timestamp %q", at)
				}
				fmt.Fprintf(&sched, "ON SCHEDULE AT '%s'", at)
			default:
				return nil, fmt.Errorf("unsupported event schedule kind %q", spec.ScheduleKind)
			}

			comp := "ON COMPLETION NOT PRESERVE"
			if spec.Preserve {
				comp = "ON COMPLETION PRESERVE"
			}
			state := "ENABLE"
			if spec.Enabled != nil && !*spec.Enabled {
				state = "DISABLE"
			}
			cmt := ""
			if strings.TrimSpace(req.Comment) != "" {
				cmt = " COMMENT " + sqlbase.FormatLiteralForDialect(d, req.Comment)
			}
			body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(spec.BodySQL), ";"))
			if body == "" {
				return nil, errors.New("event DO body SQL cannot be empty")
			}
			probe := fmt.Sprintf("CREATE EVENT `probe` ON SCHEDULE EVERY 1 DAY DO %s", body)
			if len(sqlbase.SplitStatements(probe)) != 1 {
				return nil, errors.New("event DO body must be a single SQL statement or BEGIN ... END compound block")
			}

			if req.Action == "ALTER" {
				return []string{fmt.Sprintf("ALTER EVENT %s %s %s %s%s DO %s", qEv, sched.String(), comp, state, cmt, body)}, nil
			}
			if eng == "mariadb" {
				return []string{fmt.Sprintf("CREATE OR REPLACE EVENT %s %s %s %s%s DO %s", qEv, sched.String(), comp, state, cmt, body)}, nil
			}
			createStmt := fmt.Sprintf("CREATE EVENT %s %s %s %s%s DO %s", qEv, sched.String(), comp, state, cmt, body)
			if req.ReplaceExisting || req.Action == "CREATE_OR_REPLACE" {
				return []string{"DROP EVENT IF EXISTS " + qEv, createStmt}, nil
			}
			return []string{createStmt}, nil
		}

		// Raw DDL fallback
		raw := strings.TrimSpace(req.RoutineSQL)
		if raw == "" {
			return nil, errors.New("event specification or DDL SQL is required")
		}
		stmts := sqlbase.SplitStatements(raw)
		if len(stmts) != 1 {
			return nil, errors.New("event DDL must contain exactly one statement")
		}
		if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "ALTER"}, "EVENT"); err != nil {
			return nil, err
		}
		upper := strings.ToUpper(sqlbase.StripLeadingSQLComments(stmts[0]))
		if (req.ReplaceExisting || req.Action == "CREATE_OR_REPLACE") && eng != "mariadb" && !strings.Contains(upper, "CREATE OR REPLACE") && strings.HasPrefix(upper, "CREATE") {
			return []string{"DROP EVENT IF EXISTS " + qEv, stmts[0]}, nil
		}
		return stmts, nil
	default:
		return nil, fmt.Errorf("unsupported EVENT action %q", req.Action)
	}
}

func buildSequenceActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	sc := strings.TrimSpace(req.Schema)
	if sc == "" {
		switch eng {
		case "postgres", "kingbase":
			sc = "public"
		case "sqlserver":
			sc = "dbo"
		case "duckdb":
			sc = "main"
		}
	}
	qSeq := sqlbase.QualifyTable(d, req.Database, sc, req.ObjectName)

	switch req.Action {
	case "DROP":
		if eng == "oracle" || eng == "dameng" || eng == "dm8" {
			return []string{"DROP SEQUENCE " + qSeq}, nil
		}
		return []string{"DROP SEQUENCE IF EXISTS " + qSeq}, nil

	case "RESTART":
		val := int64(1)
		if req.Spec != nil && req.Spec.RestartWith != nil {
			val = *req.Spec.RestartWith
		}
		if eng == "oracle" || eng == "dameng" || eng == "dm8" {
			return []string{fmt.Sprintf("ALTER SEQUENCE %s RESTART START WITH %d", qSeq, val)}, nil
		}
		return []string{fmt.Sprintf("ALTER SEQUENCE %s RESTART WITH %d", qSeq, val)}, nil

	case "CREATE_OR_REPLACE", "ALTER":
		spec := req.Spec
		if spec == nil {
			if strings.TrimSpace(req.RoutineSQL) != "" {
				stmts := sqlbase.SplitStatements(req.RoutineSQL)
				if len(stmts) != 1 {
					return nil, errors.New("sequence DDL must be a single statement")
				}
				if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "ALTER"}, "SEQUENCE"); err != nil {
					return nil, err
				}
				return stmts, nil
			}
			spec = &driver.ObjectSpec{}
		}

		if dt := strings.TrimSpace(spec.DataType); dt != "" {
			if err := sqlbase.ValidateColumnType(dt); err != nil {
				return nil, err
			}
		}

		var parts []string
		isOracleLike := eng == "oracle" || eng == "dameng" || eng == "dm8"
		if dt := strings.TrimSpace(spec.DataType); dt != "" && !isOracleLike && eng != "duckdb" && eng != "tidb" && eng != "mariadb" {
			parts = append(parts, "AS "+dt)
		}
		if spec.IncrementBy != nil {
			parts = append(parts, fmt.Sprintf("INCREMENT BY %d", *spec.IncrementBy))
		}
		if spec.MinValue != nil {
			parts = append(parts, fmt.Sprintf("MINVALUE %d", *spec.MinValue))
		}
		if spec.MaxValue != nil {
			parts = append(parts, fmt.Sprintf("MAXVALUE %d", *spec.MaxValue))
		}
		if req.Action == "ALTER" {
			if spec.RestartWith != nil {
				if isOracleLike {
					parts = append(parts, fmt.Sprintf("RESTART START WITH %d", *spec.RestartWith))
				} else {
					parts = append(parts, fmt.Sprintf("RESTART WITH %d", *spec.RestartWith))
				}
			}
		} else if spec.StartWith != nil {
			parts = append(parts, fmt.Sprintf("START WITH %d", *spec.StartWith))
		}
		if spec.Cache != nil && eng != "duckdb" {
			if isOracleLike && *spec.Cache <= 1 {
				parts = append(parts, "NOCACHE")
			} else if *spec.Cache > 0 {
				parts = append(parts, fmt.Sprintf("CACHE %d", *spec.Cache))
			}
		}
		if spec.Cycle {
			parts = append(parts, "CYCLE")
		} else if isOracleLike {
			parts = append(parts, "NOCYCLE")
		} else {
			parts = append(parts, "NO CYCLE")
		}

		suffix := ""
		if len(parts) > 0 {
			suffix = " " + strings.Join(parts, " ")
		}
		if req.Action == "ALTER" {
			return []string{fmt.Sprintf("ALTER SEQUENCE %s%s", qSeq, suffix)}, nil
		}
		if eng == "mariadb" {
			return []string{fmt.Sprintf("CREATE OR REPLACE SEQUENCE %s%s", qSeq, suffix)}, nil
		}
		if req.ReplaceExisting {
			dropStmt := "DROP SEQUENCE IF EXISTS " + qSeq
			if isOracleLike {
				dropStmt = "DROP SEQUENCE " + qSeq
			}
			return []string{dropStmt, fmt.Sprintf("CREATE SEQUENCE %s%s", qSeq, suffix)}, nil
		}
		return []string{fmt.Sprintf("CREATE SEQUENCE %s%s", qSeq, suffix)}, nil

	default:
		return nil, fmt.Errorf("unsupported SEQUENCE action %q", req.Action)
	}
}

func buildMatViewActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	sc := strings.TrimSpace(req.Schema)
	if sc == "" && (eng == "postgres" || eng == "kingbase") {
		sc = "public"
	}
	qMV := sqlbase.QualifyTable(d, req.Database, sc, req.ObjectName)

	switch req.Action {
	case "DROP":
		switch eng {
		case "clickhouse":
			return []string{"DROP TABLE IF EXISTS " + qMV}, nil
		case "oracle", "dameng", "dm8":
			return []string{"DROP MATERIALIZED VIEW " + qMV}, nil
		default:
			cascade := ""
			if req.Spec != nil && req.Spec.Cascade {
				cascade = " CASCADE"
			}
			return []string{"DROP MATERIALIZED VIEW IF EXISTS " + qMV + cascade}, nil
		}

	case "REFRESH", "REFRESH_CONCURRENTLY":
		concurrently := req.Action == "REFRESH_CONCURRENTLY" || (req.Spec != nil && strings.EqualFold(strings.TrimSpace(req.Spec.RefreshMode), "CONCURRENTLY"))
		switch eng {
		case "postgres", "kingbase":
			if concurrently {
				return []string{"REFRESH MATERIALIZED VIEW CONCURRENTLY " + qMV}, nil
			}
			return []string{"REFRESH MATERIALIZED VIEW " + qMV}, nil
		case "clickhouse":
			return []string{"SYSTEM REFRESH VIEW " + qMV}, nil
		case "dameng", "dm8":
			return []string{"REFRESH MATERIALIZED VIEW " + qMV}, nil
		case "oracle":
			method := "?"
			if req.Spec != nil {
				switch strings.ToUpper(strings.TrimSpace(req.Spec.RefreshMode)) {
				case "COMPLETE", "C":
					method = "C"
				case "FAST", "F":
					method = "F"
				case "", "FORCE", "?":
					method = "?"
				default:
					return nil, fmt.Errorf("invalid Oracle materialized view refresh mode %q", req.Spec.RefreshMode)
				}
			}
			target := escapeLiteral(qMV)
			return []string{fmt.Sprintf("BEGIN DBMS_MVIEW.REFRESH('%s', '%s'); END;", target, method)}, nil
		default:
			return nil, fmt.Errorf("materialized view refresh is not supported on %s", eng)
		}

	case "CREATE_OR_REPLACE":
		rawSelect := ""
		if req.Spec != nil && strings.TrimSpace(req.Spec.SelectSQL) != "" {
			rawSelect = req.Spec.SelectSQL
		} else if strings.TrimSpace(req.ViewSQL) != "" {
			rawSelect = req.ViewSQL
		} else {
			rawSelect = req.RoutineSQL
		}
		stmts := sqlbase.SplitStatements(rawSelect)
		if len(stmts) != 1 {
			return nil, errors.New("materialized view definition must contain exactly one SQL statement")
		}
		stmt := strings.TrimSpace(stmts[0])
		if eng == "clickhouse" && req.WebMode && !req.IsAdmin && !req.AllowPrivIP {
			if err := sqlbase.ValidateClickHouseRestrictedWebSQL(stmt); err != nil {
				return nil, err
			}
		}
		upper := strings.ToUpper(sqlbase.StripLeadingSQLComments(stmt))
		if strings.HasPrefix(upper, "CREATE") || strings.HasPrefix(upper, "ALTER") || strings.HasPrefix(upper, "REPLACE") {
			if err := sqlbase.ValidateExpectedDDLTarget(stmt, []string{"CREATE", "ALTER", "REPLACE"}, "MATERIALIZED VIEW"); err != nil {
				return nil, err
			}
			return []string{stmt}, nil
		}
		if err := sqlbase.ValidateReadOnlyScript(stmt); err != nil {
			return nil, fmt.Errorf("materialized view query must be read-only: %w", err)
		}

		switch eng {
		case "postgres", "kingbase":
			var out []string
			if req.ReplaceExisting {
				out = append(out, "DROP MATERIALIZED VIEW IF EXISTS "+qMV)
			}
			out = append(out, fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n%s", qMV, stmt))
			return out, nil

		case "clickhouse":
			var out []string
			if req.ReplaceExisting {
				out = append(out, "DROP TABLE IF EXISTS "+qMV)
			}
			clause := ""
			if req.Spec != nil && strings.TrimSpace(req.Spec.TargetTable) != "" {
				clause = " TO " + sqlbase.QualifyTable(d, req.Database, "", strings.TrimSpace(req.Spec.TargetTable))
			} else {
				engClause := "MergeTree() ORDER BY tuple()"
				if req.Spec != nil && strings.TrimSpace(req.Spec.EngineClause) != "" {
					if sqlbase.ContainsUnquotedSQLMeta(req.Spec.EngineClause) {
						return nil, errors.New("invalid ClickHouse ENGINE clause")
					}
					if req.WebMode && !req.IsAdmin && !req.AllowPrivIP {
						if err := sqlbase.ValidateClickHouseWebEngineClause(req.Spec.EngineClause); err != nil {
							return nil, err
						}
					}
					engClause = strings.TrimSpace(req.Spec.EngineClause)
				}
				clause = " ENGINE = " + engClause
			}
			pop := ""
			if req.Spec != nil && req.Spec.Populate {
				pop = " POPULATE"
			}
			out = append(out, fmt.Sprintf("CREATE MATERIALIZED VIEW %s%s%s AS\n%s", qMV, clause, pop, stmt))
			return out, nil

		case "oracle", "dameng", "dm8":
			var out []string
			if req.ReplaceExisting {
				out = append(out, "DROP MATERIALIZED VIEW "+qMV)
			}
			out = append(out, fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n%s", qMV, stmt))
			return out, nil

		default:
			return nil, fmt.Errorf("MATERIALIZED_VIEW creation is not supported on %s", eng)
		}

	default:
		return nil, fmt.Errorf("unsupported MATERIALIZED_VIEW action %q", req.Action)
	}
}

func buildDictionaryActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "clickhouse" {
		return nil, fmt.Errorf("DICTIONARY actions are only supported on ClickHouse (got %s)", eng)
	}
	qDict := sqlbase.QualifyTable(d, req.Database, "", req.ObjectName)
	switch req.Action {
	case "DROP":
		return []string{"DROP DICTIONARY IF EXISTS " + qDict}, nil
	case "RELOAD", "REFRESH":
		return []string{"SYSTEM RELOAD DICTIONARY " + qDict}, nil
	case "CREATE_OR_REPLACE":
		raw := strings.TrimSpace(req.RoutineSQL)
		if raw == "" {
			raw = strings.TrimSpace(req.ViewSQL)
		}
		stmts := sqlbase.SplitStatements(raw)
		if len(stmts) != 1 {
			return nil, errors.New("ClickHouse DICTIONARY DDL must contain a single statement")
		}
		if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "REPLACE"}, "DICTIONARY"); err != nil {
			return nil, err
		}
		if req.WebMode && !req.IsAdmin {
			if err := sqlbase.ValidateClickHouseDictionaryDDL(stmts[0], req.AllowPrivIP); err != nil {
				return nil, err
			}
		}
		return stmts, nil
	default:
		return nil, fmt.Errorf("unsupported DICTIONARY action %q", req.Action)
	}
}

func buildClickHouseUDFActionSQLs(d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	qFn := sqlbase.QuoteIdent(d, req.ObjectName)
	switch req.Action {
	case "DROP":
		return []string{"DROP FUNCTION IF EXISTS " + qFn}, nil
	case "CREATE_OR_REPLACE", "CREATE_ROUTINE":
		if req.Spec != nil && strings.TrimSpace(req.Spec.MacroExpr) != "" {
			params := make([]string, 0, len(req.Spec.Parameters))
			for _, p := range req.Spec.Parameters {
				pt := strings.TrimSpace(p)
				if pt == "" {
					continue
				}
				if !reValidParamName.MatchString(pt) {
					return nil, fmt.Errorf("invalid UDF parameter name %q", pt)
				}
				params = append(params, pt)
			}
			if err := sqlbase.ValidateSQLExpression("UDF expression", req.Spec.MacroExpr); err != nil {
				return nil, err
			}
			if req.WebMode {
				if err := sqlbase.ValidateReadOnlyScript("SELECT " + req.Spec.MacroExpr); err != nil {
					return nil, err
				}
				if !req.IsAdmin && !req.AllowPrivIP {
					if err := sqlbase.ValidateClickHouseRestrictedWebSQL("SELECT " + req.Spec.MacroExpr); err != nil {
						return nil, err
					}
				}
			}
			paramClause := "(" + strings.Join(params, ", ") + ")"
			return []string{fmt.Sprintf("CREATE OR REPLACE FUNCTION %s AS %s -> %s", qFn, paramClause, strings.TrimSpace(req.Spec.MacroExpr))}, nil
		}
		raw := strings.TrimSpace(req.RoutineSQL)
		stmts := sqlbase.SplitStatements(raw)
		if len(stmts) != 1 {
			return nil, errors.New("ClickHouse FUNCTION DDL must contain a single statement")
		}
		if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "REPLACE"}, "FUNCTION"); err != nil {
			return nil, err
		}
		if req.WebMode && !req.IsAdmin && !req.AllowPrivIP {
			if err := sqlbase.ValidateClickHouseRestrictedWebSQL(stmts[0]); err != nil {
				return nil, err
			}
		}
		return stmts, nil
	default:
		return nil, fmt.Errorf("unsupported ClickHouse FUNCTION action %q", req.Action)
	}
}

func buildPackageActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "oracle" && eng != "dameng" && eng != "dm8" {
		return nil, fmt.Errorf("packages are not supported on %s", eng)
	}
	qPkg := sqlbase.QualifyTable(d, "", req.Schema, req.ObjectName)
	switch req.Action {
	case "DROP":
		if req.ObjectType == "PACKAGE_BODY" {
			return []string{"DROP PACKAGE BODY " + qPkg}, nil
		}
		return []string{"DROP PACKAGE " + qPkg}, nil
	case "COMPILE":
		if req.ObjectType == "PACKAGE_BODY" {
			return []string{fmt.Sprintf("ALTER PACKAGE %s COMPILE BODY", qPkg)}, nil
		}
		return []string{fmt.Sprintf("ALTER PACKAGE %s COMPILE", qPkg)}, nil
	case "COMPILE_BODY":
		return []string{fmt.Sprintf("ALTER PACKAGE %s COMPILE BODY", qPkg)}, nil
	case "CREATE_OR_REPLACE":
		raw := strings.TrimSpace(req.RoutineSQL)
		if raw == "" {
			return nil, errors.New("package DDL source cannot be empty")
		}
		stmts := sqlbase.SplitStatements(raw)
		if len(stmts) != 1 {
			return nil, errors.New("package DDL must be a single CREATE [OR REPLACE] PACKAGE statement")
		}
		if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "ALTER"}, "PACKAGE BODY", "PACKAGE"); err != nil {
			return nil, err
		}
		return stmts, nil
	default:
		return nil, fmt.Errorf("unsupported PACKAGE action %q", req.Action)
	}
}

func buildSynonymActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	switch eng {
	case "oracle", "dameng", "dm8":
		isPub := req.Spec != nil && req.Spec.Public
		synRef := sqlbase.QualifyTable(d, "", req.Schema, req.ObjectName)
		pubKw := ""
		if isPub {
			pubKw = "PUBLIC "
			synRef = sqlbase.QuoteIdent(d, req.ObjectName)
		}
		switch req.Action {
		case "DROP":
			return []string{fmt.Sprintf("DROP %sSYNONYM %s", pubKw, synRef)}, nil
		case "CREATE_OR_REPLACE":
			if req.Spec == nil || strings.TrimSpace(req.Spec.TargetObject) == "" {
				return nil, errors.New("synonym targetObject is required")
			}
			target := sqlbase.QualifyTable(d, "", strings.TrimSpace(req.Spec.TargetOwner), strings.TrimSpace(req.Spec.TargetObject))
			return []string{fmt.Sprintf("CREATE OR REPLACE %sSYNONYM %s FOR %s", pubKw, synRef, target)}, nil
		}

	case "sqlserver":
		sc := strings.TrimSpace(req.Schema)
		if sc == "" {
			sc = "dbo"
		}
		qSyn := sqlbase.QualifyTable(d, "", sc, req.ObjectName)
		switch req.Action {
		case "DROP":
			return []string{"DROP SYNONYM IF EXISTS " + qSyn}, nil
		case "CREATE_OR_REPLACE":
			if req.Spec == nil || strings.TrimSpace(req.Spec.TargetObject) == "" {
				return nil, errors.New("synonym targetObject is required")
			}
			tOwner := strings.TrimSpace(req.Spec.TargetOwner)
			if tOwner == "" {
				tOwner = sc
			}
			target := sqlbase.QualifyTable(d, "", tOwner, strings.TrimSpace(req.Spec.TargetObject))
			return []string{
				"DROP SYNONYM IF EXISTS " + qSyn,
				fmt.Sprintf("CREATE SYNONYM %s FOR %s", qSyn, target),
			}, nil
		}
	}
	return nil, fmt.Errorf("SYNONYM action %q is not supported on %s", req.Action, eng)
}

func buildDbLinkActionSQLs(ctx context.Context, eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) (execSQLs []string, displaySQLs []string, err error) {
	if eng != "oracle" && eng != "dameng" && eng != "dm8" {
		return nil, nil, fmt.Errorf("DB_LINK actions are only supported on Oracle and Dameng (got %s)", eng)
	}
	isPub := req.Spec != nil && req.Spec.Public
	if isPub && !req.IsAdmin {
		return nil, nil, errors.New("PUBLIC database links require administrator privileges")
	}
	if req.WebMode && !req.AllowPrivIP && !req.IsAdmin {
		return nil, nil, errors.New("database link operations in restricted Web mode require administrator privileges")
	}
	pubKw := ""
	if isPub {
		pubKw = "PUBLIC "
	}
	qLink := sqlbase.QuoteIdent(d, req.ObjectName)

	switch req.Action {
	case "DROP":
		stmt := fmt.Sprintf("DROP %sDATABASE LINK %s", pubKw, qLink)
		return []string{stmt}, []string{stmt}, nil

	case "CREATE_OR_REPLACE":
		if req.Spec == nil || strings.TrimSpace(req.Spec.DbLinkHost) == "" {
			return nil, nil, errors.New("database link host (dbLinkHost) is required")
		}
		if err := validateDbLinkHostSSRF(ctx, req.AllowPrivIP, req.Spec.DbLinkHost); err != nil {
			return nil, nil, err
		}
		pass := req.Spec.DbLinkPass
		if strings.ContainsAny(pass, "\"\r\n\x00") {
			return nil, nil, errors.New("database link password contains disallowed quote or control characters")
		}
		hostEsc := escapeLiteral(strings.TrimSpace(req.Spec.DbLinkHost))
		user := strings.TrimSpace(req.Spec.DbLinkUser)

		var execOut, dispOut []string
		if req.ReplaceExisting {
			dropStmt := fmt.Sprintf("DROP %sDATABASE LINK %s", pubKw, qLink)
			execOut = append(execOut, dropStmt)
			dispOut = append(dispOut, dropStmt)
		}
		if user != "" {
			if pass == "" {
				return nil, nil, errors.New("database link password is required when username is specified")
			}
			qUser := sqlbase.QuoteIdent(d, user)
			execOut = append(execOut, fmt.Sprintf(`CREATE %sDATABASE LINK %s CONNECT TO %s IDENTIFIED BY "%s" USING '%s'`,
				pubKw, qLink, qUser, pass, hostEsc))
			dispOut = append(dispOut, fmt.Sprintf(`CREATE %sDATABASE LINK %s CONNECT TO %s IDENTIFIED BY "********" USING '%s'`,
				pubKw, qLink, qUser, hostEsc))
		} else {
			stmt := fmt.Sprintf("CREATE %sDATABASE LINK %s USING '%s'", pubKw, qLink, hostEsc)
			execOut = append(execOut, stmt)
			dispOut = append(dispOut, stmt)
		}
		return execOut, dispOut, nil

	default:
		return nil, nil, fmt.Errorf("unsupported DB_LINK action %q", req.Action)
	}
}

func buildPostgresTypeActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "postgres" && eng != "kingbase" {
		return nil, fmt.Errorf("custom TYPE management is only supported on PostgreSQL / KingbaseES (got %s)", eng)
	}
	sc := strings.TrimSpace(req.Schema)
	if sc == "" {
		sc = "public"
	}
	qType := sqlbase.QualifyTable(d, "", sc, req.ObjectName)

	switch req.Action {
	case "DROP":
		cascade := ""
		if req.Spec != nil && req.Spec.Cascade {
			cascade = " CASCADE"
		}
		return []string{"DROP TYPE IF EXISTS " + qType + cascade}, nil

	case "ADD_ENUM_VALUE":
		if req.Spec == nil || strings.TrimSpace(req.Spec.NewEnumValue) == "" {
			return nil, errors.New("newEnumValue is required for ADD_ENUM_VALUE")
		}
		return []string{fmt.Sprintf("ALTER TYPE %s ADD VALUE IF NOT EXISTS %s",
			qType, sqlbase.FormatLiteralForDialect(d, req.Spec.NewEnumValue))}, nil

	case "CREATE_OR_REPLACE":
		if req.Spec != nil {
			switch strings.ToUpper(strings.TrimSpace(req.Spec.TypeKind)) {
			case "ENUM", "":
				if len(req.Spec.EnumValues) == 0 {
					return nil, errors.New("at least one enum value is required")
				}
				vals := make([]string, len(req.Spec.EnumValues))
				for i, v := range req.Spec.EnumValues {
					vals[i] = sqlbase.FormatLiteralForDialect(d, v)
				}
				var out []string
				if req.ReplaceExisting {
					out = append(out, "DROP TYPE IF EXISTS "+qType)
				}
				out = append(out, fmt.Sprintf("CREATE TYPE %s AS ENUM (%s)", qType, strings.Join(vals, ", ")))
				return out, nil

			case "DOMAIN":
				if err := sqlbase.ValidateColumnType(req.Spec.BaseType); err != nil {
					return nil, fmt.Errorf("invalid domain base type: %w", err)
				}
				chkClause := ""
				if strings.TrimSpace(req.Spec.CheckExpr) != "" {
					if err := sqlbase.ValidateSQLExpression("domain CHECK expression", req.Spec.CheckExpr); err != nil {
						return nil, err
					}
					chkClause = fmt.Sprintf(" CHECK (%s)", strings.TrimSpace(req.Spec.CheckExpr))
				}
				var out []string
				if req.ReplaceExisting {
					out = append(out, "DROP DOMAIN IF EXISTS "+qType)
				}
				out = append(out, fmt.Sprintf("CREATE DOMAIN %s AS %s%s", qType, strings.TrimSpace(req.Spec.BaseType), chkClause))
				return out, nil
			}
		}
		if strings.TrimSpace(req.RoutineSQL) != "" {
			stmts := sqlbase.SplitStatements(req.RoutineSQL)
			if len(stmts) != 1 {
				return nil, errors.New("TYPE DDL must contain a single statement")
			}
			if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE", "ALTER"}, "TYPE", "DOMAIN"); err != nil {
				return nil, err
			}
			return stmts, nil
		}
		return nil, errors.New("type specification or DDL SQL is required")

	default:
		return nil, fmt.Errorf("unsupported TYPE action %q", req.Action)
	}
}

func buildExtensionActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	extName := strings.TrimSpace(req.ObjectName)
	if !reValidExtensionName.MatchString(extName) {
		return nil, fmt.Errorf("invalid extension name %q: only alphanumeric, underscore, and hyphen characters are allowed", extName)
	}

	switch eng {
	case "postgres", "kingbase":
		if req.WebMode && !req.IsAdmin {
			if _, blocked := blockedPGWebExtensions[strings.ToLower(extName)]; blocked {
				return nil, fmt.Errorf("extension %q is restricted for non-administrator accounts in Web mode", extName)
			}
		}
		qExt := sqlbase.QuoteIdent(d, extName)
		switch req.Action {
		case "INSTALL", "CREATE_OR_REPLACE":
			var sb strings.Builder
			fmt.Fprintf(&sb, "CREATE EXTENSION IF NOT EXISTS %s", qExt)
			if sc := strings.TrimSpace(req.Schema); sc != "" {
				fmt.Fprintf(&sb, " SCHEMA %s", sqlbase.QuoteIdent(d, sc))
			}
			if req.Spec != nil {
				if ver := strings.TrimSpace(req.Spec.ExtensionVer); ver != "" {
					if !reValidExtensionVer.MatchString(ver) {
						return nil, fmt.Errorf("invalid extension version %q", ver)
					}
					fmt.Fprintf(&sb, " VERSION '%s'", ver)
				}
				if req.Spec.Cascade {
					sb.WriteString(" CASCADE")
				}
			}
			return []string{sb.String()}, nil

		case "ALTER":
			if req.Spec != nil && strings.TrimSpace(req.Spec.ExtensionVer) != "" {
				ver := strings.TrimSpace(req.Spec.ExtensionVer)
				if !reValidExtensionVer.MatchString(ver) {
					return nil, fmt.Errorf("invalid extension version %q", ver)
				}
				return []string{fmt.Sprintf("ALTER EXTENSION %s UPDATE TO '%s'", qExt, ver)}, nil
			}
			return []string{fmt.Sprintf("ALTER EXTENSION %s UPDATE", qExt)}, nil

		case "DROP":
			cascade := ""
			if req.Spec != nil && req.Spec.Cascade {
				cascade = " CASCADE"
			}
			return []string{"DROP EXTENSION IF EXISTS " + qExt + cascade}, nil
		}

	case "duckdb":
		if req.WebMode && !req.IsAdmin && (req.Action == "INSTALL" || req.Action == "LOAD" || req.Action == "CREATE_OR_REPLACE") {
			return nil, errors.New("DuckDB extension INSTALL and LOAD are restricted for non-administrator accounts in Web mode")
		}
		extRef := formatDuckDBExtensionRef(extName)
		switch req.Action {
		case "INSTALL":
			return []string{"INSTALL " + extRef}, nil
		case "LOAD":
			return []string{"LOAD " + extRef}, nil
		case "CREATE_OR_REPLACE":
			return []string{"INSTALL " + extRef, "LOAD " + extRef}, nil
		}
	}
	return nil, fmt.Errorf("EXTENSION action %q is not supported on %s", req.Action, eng)
}

func buildDuckDBMacroActionSQLs(eng string, d sqlbase.Dialect, req driver.ObjectActionRequest) ([]string, error) {
	if eng != "duckdb" {
		return nil, fmt.Errorf("MACRO actions are only supported on DuckDB (got %s)", eng)
	}
	sc := strings.TrimSpace(req.Schema)
	if sc == "" {
		sc = "main"
	}
	qMacro := sqlbase.QualifyTable(d, "", sc, req.ObjectName)
	isTable := req.ObjectType == "TABLE_MACRO" || (req.Spec != nil && req.Spec.IsTableMacro)

	switch req.Action {
	case "DROP":
		if isTable {
			return []string{"DROP MACRO TABLE IF EXISTS " + qMacro}, nil
		}
		return []string{"DROP MACRO IF EXISTS " + qMacro}, nil

	case "CREATE_OR_REPLACE":
		if req.Spec != nil && strings.TrimSpace(req.Spec.MacroExpr) != "" {
			if req.WebMode {
				if err := validateDuckDBSandboxSQL(req.Spec.MacroExpr); err != nil {
					return nil, err
				}
			}
			params := make([]string, 0, len(req.Spec.Parameters))
			for _, p := range req.Spec.Parameters {
				pt := strings.TrimSpace(p)
				if pt == "" {
					continue
				}
				if !reValidParamName.MatchString(pt) {
					return nil, fmt.Errorf("invalid macro parameter name %q", pt)
				}
				params = append(params, pt)
			}
			expr := strings.TrimSuffix(strings.TrimSpace(req.Spec.MacroExpr), ";")
			if isTable {
				stmts := sqlbase.SplitStatements(expr)
				if len(stmts) != 1 {
					return nil, errors.New("table macro query must contain a single SQL statement")
				}
				if err := sqlbase.ValidateReadOnlyScript(stmts[0]); err != nil {
					return nil, fmt.Errorf("table macro query must be read-only: %w", err)
				}
				return []string{fmt.Sprintf("CREATE OR REPLACE MACRO %s(%s) AS TABLE %s", qMacro, strings.Join(params, ", "), stmts[0])}, nil
			}
			if err := sqlbase.ValidateSQLExpression("macro expression", expr); err != nil {
				return nil, err
			}
			return []string{fmt.Sprintf("CREATE OR REPLACE MACRO %s(%s) AS %s", qMacro, strings.Join(params, ", "), expr)}, nil
		}

		raw := strings.TrimSpace(req.RoutineSQL)
		if raw == "" {
			return nil, errors.New("macro specification or DDL SQL is required")
		}
		if req.WebMode {
			if err := validateDuckDBSandboxSQL(raw); err != nil {
				return nil, err
			}
		}
		stmts := sqlbase.SplitStatements(raw)
		if len(stmts) != 1 {
			return nil, errors.New("macro DDL must contain a single SQL statement")
		}
		if err := sqlbase.ValidateExpectedDDLTarget(stmts[0], []string{"CREATE"}, "MACRO"); err != nil {
			return nil, err
		}
		return stmts, nil

	default:
		return nil, fmt.Errorf("unsupported MACRO action %q", req.Action)
	}
}

func buildTriggerToggleSQLs(ctx context.Context, eng string, d sqlbase.Dialect, sqlDrv driver.SQLDriver, req driver.ObjectActionRequest) ([]string, error) {
	verb := req.Action // ENABLE or DISABLE
	switch eng {
	case "postgres", "kingbase":
		tbl := strings.TrimSpace(req.TargetName)
		if tbl == "" && !req.PreviewOnly && sqlDrv != nil {
			tbl = lookupPostgresTriggerTable(ctx, sqlDrv, req.Database, req.Schema, req.ObjectName)
		}
		if tbl == "" {
			return nil, errors.New("target table name (targetName) is required to toggle a PostgreSQL trigger")
		}
		sc := strings.TrimSpace(req.Schema)
		if sc == "" {
			sc = "public"
		}
		onTbl := sqlbase.QualifyTable(d, "", sc, tbl)
		return []string{fmt.Sprintf("ALTER TABLE %s %s TRIGGER %s", onTbl, verb, sqlbase.QuoteIdent(d, req.ObjectName))}, nil

	case "oracle", "dameng", "dm8":
		qTrg := sqlbase.QualifyTable(d, "", req.Schema, req.ObjectName)
		return []string{fmt.Sprintf("ALTER TRIGGER %s %s", qTrg, verb)}, nil

	case "sqlserver":
		if strings.TrimSpace(req.TargetName) == "" {
			return nil, errors.New("target table name (targetName) is required to toggle a SQL Server trigger")
		}
		sc := strings.TrimSpace(req.Schema)
		if sc == "" {
			sc = "dbo"
		}
		qTrg := sqlbase.QualifyTable(d, "", sc, req.ObjectName)
		onTbl := sqlbase.QualifyTable(d, "", sc, strings.TrimSpace(req.TargetName))
		return []string{fmt.Sprintf("%s TRIGGER %s ON %s", verb, qTrg, onTbl)}, nil

	default:
		return nil, fmt.Errorf("trigger %s is not supported on %s", verb, eng)
	}
}
