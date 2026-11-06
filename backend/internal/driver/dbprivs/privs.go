package dbprivs

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"dbharbor/backend/internal/driver"
)

type AccountInfo struct {
	Username   string   `json:"username"`
	Host       string   `json:"host,omitempty"`
	Identity   string   `json:"identity"`
	AuthPlugin string   `json:"authPlugin,omitempty"`
	Locked     bool     `json:"locked,omitempty"`
	IsSuper    bool     `json:"isSuper,omitempty"`
	CanLogin   bool     `json:"canLogin,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	Extra      string   `json:"extra,omitempty"`
}

type GrantEntry struct {
	Statement  string `json:"statement"`
	Scope      string `json:"scope,omitempty"`
	Privileges string `json:"privileges,omitempty"`
	Grantable  bool   `json:"grantable,omitempty"`
}

type Capabilities struct {
	Supported           bool     `json:"supported"`
	Engine              string   `json:"engine"`
	SupportsHost        bool     `json:"supportsHost"`
	SupportsLock        bool     `json:"supportsLock"`
	SupportsRoles       bool     `json:"supportsRoles"`
	SupportsTableScope  bool     `json:"supportsTableScope"`
	SupportsGlobalScope bool     `json:"supportsGlobalScope"`
	AvailablePrivileges []string `json:"availablePrivileges"`
	AvailableRoles      []string `json:"availableRoles,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

type ActionRequest struct {
	Action          string   `json:"action"` // CREATE_USER, ALTER_USER, DROP_USER, LOCK_USER, UNLOCK_USER, GRANT, REVOKE
	Username        string   `json:"username"`
	Host            string   `json:"host,omitempty"`
	Database        string   `json:"database,omitempty"`
	Schema          string   `json:"schema,omitempty"`
	Table           string   `json:"table,omitempty"`
	ScopeType       string   `json:"scopeType,omitempty"` // global, database, schema, table
	Password        string   `json:"password,omitempty"`
	Privileges      []string `json:"privileges,omitempty"`
	Roles           []string `json:"roles,omitempty"`
	WithGrantOption bool     `json:"withGrantOption,omitempty"`
	IsSuper         bool     `json:"isSuper,omitempty"`
	CanCreateDB     bool     `json:"canCreateDB,omitempty"`
	CanCreateRole   bool     `json:"canCreateRole,omitempty"`
	ACLPreset       string   `json:"aclPreset,omitempty"` // readonly, readwrite, admin, custom
	ACLRules        string   `json:"aclRules,omitempty"`
}

var safePrivTokenRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ ]*$`)

func sqlStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteBacktick(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}

func quoteDouble(id string) string {
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

func quoteBracket(id string) string {
	return "[" + strings.ReplaceAll(id, "]", "]]") + "]"
}

func normalizeMySQLHost(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return "%"
	}
	return h
}

func formatMySQLUser(username, host string) string {
	return fmt.Sprintf("%s@%s", sqlStr(strings.TrimSpace(username)), sqlStr(normalizeMySQLHost(host)))
}

func sanitizePrivileges(items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		p := strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(raw)), " "))
		if p == "" {
			continue
		}
		if !safePrivTokenRe.MatchString(p) {
			return nil, fmt.Errorf("invalid privilege token %q", raw)
		}
		if _, exists := seen[p]; !exists {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

func cellStr(row []any, idx int) string {
	if idx < 0 || idx >= len(row) || row[idx] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(row[idx]))
}

func cellTruthy(row []any, idx int) bool {
	s := strings.ToUpper(cellStr(row, idx))
	return s == "Y" || s == "YES" || s == "TRUE" || s == "T" || s == "1" || s == "OPEN"
}

func isMySQLFamily(dbType string) bool {
	switch strings.ToLower(dbType) {
	case "mysql", "mariadb", "tidb", "oceanbase":
		return true
	default:
		return false
	}
}

func isPGFamily(dbType string) bool {
	switch strings.ToLower(dbType) {
	case "postgres", "kingbase":
		return true
	default:
		return false
	}
}

// GetCapabilities returns privilege management capabilities for a given database engine.
func GetCapabilities(dbType string) Capabilities {
	t := strings.ToLower(strings.TrimSpace(dbType))
	switch {
	case isMySQLFamily(t):
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        true,
			SupportsLock:        t != "tidb",
			SupportsRoles:       false,
			SupportsTableScope:  true,
			SupportsGlobalScope: true,
			AvailablePrivileges: []string{
				"ALL PRIVILEGES", "SELECT", "INSERT", "UPDATE", "DELETE",
				"CREATE", "ALTER", "DROP", "INDEX", "REFERENCES",
				"CREATE VIEW", "SHOW VIEW", "CREATE ROUTINE", "ALTER ROUTINE", "EXECUTE",
				"TRIGGER", "EVENT", "LOCK TABLES", "PROCESS", "RELOAD", "REPLICATION CLIENT", "REPLICATION SLAVE",
			},
		}
	case isPGFamily(t):
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        true,
			SupportsRoles:       true,
			SupportsTableScope:  true,
			SupportsGlobalScope: false,
			AvailablePrivileges: []string{
				"ALL PRIVILEGES", "SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE",
				"REFERENCES", "TRIGGER", "CONNECT", "TEMPORARY", "CREATE", "USAGE", "EXECUTE",
			},
			AvailableRoles: []string{"pg_read_all_data", "pg_write_all_data", "pg_monitor"},
		}
	case t == "clickhouse":
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        false,
			SupportsRoles:       true,
			SupportsTableScope:  true,
			SupportsGlobalScope: true,
			AvailablePrivileges: []string{
				"ALL", "SELECT", "INSERT", "ALTER", "CREATE", "DROP", "TRUNCATE", "OPTIMIZE", "SHOW",
			},
		}
	case t == "sqlserver":
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        false,
			SupportsRoles:       true,
			SupportsTableScope:  true,
			SupportsGlobalScope: false,
			AvailablePrivileges: []string{
				"SELECT", "INSERT", "UPDATE", "DELETE", "EXECUTE", "ALTER", "CONTROL", "VIEW DEFINITION",
			},
			AvailableRoles: []string{
				"db_owner", "db_datareader", "db_datawriter", "db_ddladmin", "db_securityadmin", "db_backupoperator",
			},
		}
	case t == "oracle" || t == "dm8" || t == "dameng":
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        true,
			SupportsRoles:       true,
			SupportsTableScope:  true,
			SupportsGlobalScope: true,
			AvailablePrivileges: []string{
				"CREATE SESSION", "CREATE TABLE", "CREATE VIEW", "CREATE SEQUENCE", "CREATE PROCEDURE", "CREATE TRIGGER",
				"UNLIMITED TABLESPACE", "SELECT", "INSERT", "UPDATE", "DELETE", "ALTER", "ALL PRIVILEGES",
			},
			AvailableRoles: []string{"CONNECT", "RESOURCE", "DBA"},
		}
	case t == "mongodb":
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        false,
			SupportsRoles:       true,
			SupportsTableScope:  false,
			SupportsGlobalScope: true,
			AvailablePrivileges: []string{},
			AvailableRoles: []string{
				"read", "readWrite", "dbAdmin", "userAdmin",
				"readAnyDatabase", "readWriteAnyDatabase", "dbAdminAnyDatabase", "userAdminAnyDatabase", "clusterMonitor", "root",
			},
		}
	case t == "redis":
		return Capabilities{
			Supported:           true,
			Engine:              t,
			SupportsHost:        false,
			SupportsLock:        true,
			SupportsRoles:       false,
			SupportsTableScope:  false,
			SupportsGlobalScope: true,
			AvailablePrivileges: []string{
				"+@read", "+@write", "+@all", "-@dangerous", "~*", "&*",
			},
		}
	default:
		return Capabilities{
			Supported:           false,
			Engine:              t,
			AvailablePrivileges: []string{},
			Reason:              "Embedded file-based database engines do not use server-level user accounts or SQL GRANT tables.",
		}
	}
}

// EnrichRoutinesAndTriggers populates Routines and Triggers on SchemaObjects when a driver returns empty slices.
func EnrichRoutinesAndTriggers(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, objs *driver.SchemaObjects) {
	if objs == nil || sqlDrv == nil {
		return
	}
	if objs.Tables == nil {
		objs.Tables = []driver.ObjectItem{}
	}
	if objs.Views == nil {
		objs.Views = []driver.ObjectItem{}
	}
	if objs.Routines == nil {
		objs.Routines = []driver.ObjectItem{}
	}
	if objs.Triggers == nil {
		objs.Triggers = []driver.ObjectItem{}
	}

	t := strings.ToLower(strings.TrimSpace(dbType))
	db := strings.TrimSpace(objs.Database)
	schema := strings.TrimSpace(objs.Schema)

	switch {
	case isMySQLFamily(t):
		if db == "" {
			return
		}
		if len(objs.Routines) == 0 {
			q := fmt.Sprintf(
				"SELECT ROUTINE_NAME, UPPER(COALESCE(ROUTINE_TYPE, 'FUNCTION')), COALESCE(ROUTINE_COMMENT, ''), COALESCE(CAST(CREATED AS CHAR), '') FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = %s ORDER BY ROUTINE_NAME",
				sqlStr(db),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name == "" {
						continue
					}
					rtype := cellStr(r, 1)
					if rtype == "" {
						rtype = "FUNCTION"
					}
					objs.Routines = append(objs.Routines, driver.ObjectItem{
						Name:      name,
						Type:      rtype,
						Comment:   cellStr(r, 2),
						CreatedAt: cellStr(r, 3),
					})
				}
			}
		}
		if len(objs.Triggers) == 0 {
			q := fmt.Sprintf(
				"SELECT TRIGGER_NAME, CONCAT(COALESCE(ACTION_TIMING,''), ' ', COALESCE(EVENT_MANIPULATION,''), ' ON ', COALESCE(EVENT_OBJECT_TABLE,'')), COALESCE(CAST(CREATED AS CHAR), '') FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = %s ORDER BY TRIGGER_NAME",
				sqlStr(db),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name == "" {
						continue
					}
					objs.Triggers = append(objs.Triggers, driver.ObjectItem{
						Name:      name,
						Type:      "TRIGGER",
						Comment:   cellStr(r, 1),
						CreatedAt: cellStr(r, 2),
					})
				}
			}
		}

	case isPGFamily(t):
		if schema == "" {
			schema = "public"
		}
		if len(objs.Routines) == 0 {
			q := fmt.Sprintf(
				"SELECT DISTINCT routine_name, UPPER(COALESCE(routine_type, 'FUNCTION')) FROM information_schema.routines WHERE routine_schema = %s ORDER BY routine_name",
				sqlStr(schema),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name == "" {
						continue
					}
					objs.Routines = append(objs.Routines, driver.ObjectItem{
						Name: name,
						Type: cellStr(r, 1),
					})
				}
			}
		}
		if len(objs.Triggers) == 0 {
			q := fmt.Sprintf(
				"SELECT DISTINCT trigger_name, COALESCE(event_object_table, '') FROM information_schema.triggers WHERE trigger_schema = %s ORDER BY trigger_name",
				sqlStr(schema),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name == "" {
						continue
					}
					tbl := cellStr(r, 1)
					comment := ""
					if tbl != "" {
						comment = "ON " + tbl
					}
					objs.Triggers = append(objs.Triggers, driver.ObjectItem{
						Name:    name,
						Type:    "TRIGGER",
						Comment: comment,
					})
				}
			}
		}

	case t == "sqlite":
		if len(objs.Triggers) == 0 {
			q := "SELECT name, COALESCE(tbl_name, '') FROM sqlite_master WHERE type = 'trigger' ORDER BY name"
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name == "" {
						continue
					}
					tbl := cellStr(r, 1)
					comment := ""
					if tbl != "" {
						comment = "ON " + tbl
					}
					objs.Triggers = append(objs.Triggers, driver.ObjectItem{
						Name:    name,
						Type:    "TRIGGER",
						Comment: comment,
					})
				}
			}
		}

	case t == "sqlserver":
		if schema == "" {
			schema = "dbo"
		}
		if len(objs.Routines) == 0 {
			q := fmt.Sprintf(
				"SELECT o.name, CASE WHEN o.type = 'P' THEN 'PROCEDURE' ELSE 'FUNCTION' END FROM sys.objects o INNER JOIN sys.schemas s ON o.schema_id = s.schema_id WHERE s.name = %s AND o.type IN ('P','FN','IF','TF') ORDER BY o.name",
				sqlStr(schema),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name != "" {
						objs.Routines = append(objs.Routines, driver.ObjectItem{Name: name, Type: cellStr(r, 1)})
					}
				}
			}
		}
		if len(objs.Triggers) == 0 {
			q := fmt.Sprintf(
				"SELECT tr.name, tb.name FROM sys.triggers tr INNER JOIN sys.tables tb ON tr.parent_id = tb.object_id INNER JOIN sys.schemas s ON tb.schema_id = s.schema_id WHERE s.name = %s AND tr.is_ms_shipped = 0 ORDER BY tr.name",
				sqlStr(schema),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name != "" {
						tbl := cellStr(r, 1)
						comment := ""
						if tbl != "" {
							comment = "ON " + tbl
						}
						objs.Triggers = append(objs.Triggers, driver.ObjectItem{Name: name, Type: "TRIGGER", Comment: comment})
					}
				}
			}
		}

	case t == "oracle" || t == "dm8" || t == "dameng":
		owner := schema
		if owner == "" {
			owner = db
		}
		if owner == "" {
			return
		}
		if len(objs.Routines) == 0 {
			q := fmt.Sprintf(
				"SELECT OBJECT_NAME, OBJECT_TYPE FROM ALL_OBJECTS WHERE OWNER = %s AND OBJECT_TYPE IN ('PROCEDURE','FUNCTION') ORDER BY OBJECT_NAME",
				sqlStr(strings.ToUpper(owner)),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name != "" {
						objs.Routines = append(objs.Routines, driver.ObjectItem{Name: name, Type: cellStr(r, 1)})
					}
				}
			}
		}
		if len(objs.Triggers) == 0 {
			q := fmt.Sprintf(
				"SELECT TRIGGER_NAME, TABLE_NAME FROM ALL_TRIGGERS WHERE OWNER = %s ORDER BY TRIGGER_NAME",
				sqlStr(strings.ToUpper(owner)),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 500); err == nil && len(res) > 0 {
				for _, r := range res[0].Rows {
					name := cellStr(r, 0)
					if name != "" {
						tbl := cellStr(r, 1)
						comment := ""
						if tbl != "" {
							comment = "ON " + tbl
						}
						objs.Triggers = append(objs.Triggers, driver.ObjectItem{Name: name, Type: "TRIGGER", Comment: comment})
					}
				}
			}
		}
	}
}

// FetchObjectDDL retrieves DDL source text for VIEW, TRIGGER, PROCEDURE, FUNCTION, or TABLE objects.
func FetchObjectDDL(ctx context.Context, dbType string, sqlDrv driver.SQLDriver, database, schema, objectType, objectName string) (string, error) {
	name := strings.TrimSpace(objectName)
	if name == "" {
		return "", fmt.Errorf("object name is required")
	}
	objType := strings.ToUpper(strings.TrimSpace(objectType))
	if objType == "" || objType == "TABLE" {
		st, err := sqlDrv.GetTableStructure(ctx, database, schema, name)
		if err != nil {
			return "", err
		}
		return st.DDL, nil
	}

	t := strings.ToLower(strings.TrimSpace(dbType))
	db := strings.TrimSpace(database)
	sc := strings.TrimSpace(schema)

	switch {
	case isMySQLFamily(t):
		target := quoteBacktick(name)
		if db != "" {
			target = quoteBacktick(db) + "." + quoteBacktick(name)
		}
		showKind := "VIEW"
		colIdx := 1
		switch objType {
		case "VIEW", "MATERIALIZED VIEW":
			showKind = "VIEW"
			colIdx = 1
		case "TRIGGER":
			showKind = "TRIGGER"
			colIdx = 2
		case "PROCEDURE":
			showKind = "PROCEDURE"
			colIdx = 2
		case "FUNCTION":
			showKind = "FUNCTION"
			colIdx = 2
		}
		if res, err := sqlDrv.ExecuteSQL(ctx, db, fmt.Sprintf("SHOW CREATE %s %s", showKind, target), 5); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
			row := res[0].Rows[0]
			if ddl := cellStr(row, colIdx); ddl != "" {
				return ddl, nil
			}
			for i := len(row) - 1; i >= 1; i-- {
				if ddl := cellStr(row, i); strings.Contains(strings.ToUpper(ddl), "CREATE") || strings.Contains(strings.ToUpper(ddl), "SELECT") {
					return ddl, nil
				}
			}
		}
		if objType == "VIEW" && db != "" {
			q := fmt.Sprintf("SELECT VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = %s AND TABLE_NAME = %s", sqlStr(db), sqlStr(name))
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				if def := cellStr(res[0].Rows[0], 0); def != "" {
					return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s", target, def), nil
				}
			}
		}

	case isPGFamily(t):
		if sc == "" {
			sc = "public"
		}
		qual := quoteDouble(sc) + "." + quoteDouble(name)
		switch objType {
		case "VIEW", "MATERIALIZED VIEW":
			q := fmt.Sprintf("SELECT pg_get_viewdef(%s::regclass, true)", sqlStr(qual))
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				if def := cellStr(res[0].Rows[0], 0); def != "" {
					return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s", qual, def), nil
				}
			}
		case "FUNCTION", "PROCEDURE":
			q := fmt.Sprintf(
				"SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = %s AND p.proname = %s LIMIT 1",
				sqlStr(sc), sqlStr(name),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				if def := cellStr(res[0].Rows[0], 0); def != "" {
					return def, nil
				}
			}
		case "TRIGGER":
			q := fmt.Sprintf(
				"SELECT pg_get_triggerdef(tr.oid, true) FROM pg_trigger tr JOIN pg_class c ON c.oid = tr.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = %s AND tr.tgname = %s AND NOT tr.tgisinternal LIMIT 1",
				sqlStr(sc), sqlStr(name),
			)
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				if def := cellStr(res[0].Rows[0], 0); def != "" {
					return def + ";", nil
				}
			}
		}

	case t == "sqlite":
		q := fmt.Sprintf("SELECT sql FROM sqlite_master WHERE name = %s AND sql IS NOT NULL LIMIT 1", sqlStr(name))
		if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
			if def := cellStr(res[0].Rows[0], 0); def != "" {
				return def + ";", nil
			}
		}

	case t == "duckdb":
		q := fmt.Sprintf("SELECT sql FROM duckdb_views() WHERE view_name = %s LIMIT 1", sqlStr(name))
		if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
			if def := cellStr(res[0].Rows[0], 0); def != "" {
				return def, nil
			}
		}

	case t == "clickhouse":
		target := quoteBacktick(name)
		if db != "" {
			target = quoteBacktick(db) + "." + quoteBacktick(name)
		}
		if res, err := sqlDrv.ExecuteSQL(ctx, db, "SHOW CREATE TABLE "+target, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
			if def := cellStr(res[0].Rows[0], 0); def != "" {
				return def, nil
			}
		}

	case t == "sqlserver":
		if sc == "" {
			sc = "dbo"
		}
		qual := sc + "." + name
		q := fmt.Sprintf("SELECT OBJECT_DEFINITION(OBJECT_ID(%s))", sqlStr(qual))
		if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
			if def := cellStr(res[0].Rows[0], 0); def != "" {
				return def, nil
			}
		}

	case t == "oracle" || t == "dm8" || t == "dameng":
		owner := sc
		if owner == "" {
			owner = db
		}
		if objType == "VIEW" {
			q := fmt.Sprintf("SELECT TEXT FROM ALL_VIEWS WHERE VIEW_NAME = %s", sqlStr(name))
			if owner != "" {
				q += fmt.Sprintf(" AND OWNER = %s", sqlStr(strings.ToUpper(owner)))
			}
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 1); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				if def := cellStr(res[0].Rows[0], 0); def != "" {
					return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s", quoteDouble(name), def), nil
				}
			}
		} else {
			q := fmt.Sprintf("SELECT TEXT FROM ALL_SOURCE WHERE NAME = %s", sqlStr(name))
			if owner != "" {
				q += fmt.Sprintf(" AND OWNER = %s", sqlStr(strings.ToUpper(owner)))
			}
			q += " ORDER BY LINE"
			if res, err := sqlDrv.ExecuteSQL(ctx, db, q, 2000); err == nil && len(res) > 0 && len(res[0].Rows) > 0 {
				var sb strings.Builder
				sb.WriteString("CREATE OR REPLACE ")
				for _, r := range res[0].Rows {
					if r[0] != nil {
						sb.WriteString(fmt.Sprint(r[0]))
					}
				}
				return sb.String(), nil
			}
		}
	}

	return "", fmt.Errorf("DDL definition is unavailable or insufficient privileges for %s %s", objType, name)
}

// ListUsers retrieves database engine accounts, roles, or ACL users across supported SQL & NoSQL drivers.
func ListUsers(ctx context.Context, dbType string, drv driver.Driver, database string) ([]AccountInfo, error) {
	t := strings.ToLower(strings.TrimSpace(dbType))
	caps := GetCapabilities(t)
	if !caps.Supported {
		return []AccountInfo{}, nil
	}

	if t == "redis" {
		kv, ok := drv.(driver.KeyValueDriver)
		if !ok {
			return nil, fmt.Errorf("driver is not a Redis KeyValueDriver")
		}
		res, err := kv.ExecuteCommand(ctx, 0, "ACL LIST")
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(res.Output), "\n")
		users := make([]AccountInfo, 0, len(lines))
		for _, raw := range lines {
			line := strings.Trim(strings.TrimSpace(raw), `"`)
			if idx := strings.Index(line, "user "); idx > 0 {
				line = line[idx:]
			}
			if !strings.HasPrefix(line, "user ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			uname := fields[1]
			state := "on"
			if len(fields) >= 3 {
				state = fields[2]
			}
			rules := ""
			if len(fields) > 3 {
				rules = strings.Join(fields[3:], " ")
			}
			users = append(users, AccountInfo{
				Username: uname,
				Identity: uname,
				Locked:   state == "off",
				CanLogin: state != "off",
				IsSuper:  strings.Contains(rules, "+@all"),
				Extra:    rules,
			})
		}
		return users, nil
	}

	if t == "mongodb" {
		docDrv, ok := drv.(driver.DocumentDriver)
		if !ok {
			return nil, fmt.Errorf("driver is not a MongoDB DocumentDriver")
		}
		targetDB := strings.TrimSpace(database)
		if targetDB == "" {
			targetDB = "admin"
		}
		raw, err := docDrv.RunCommand(ctx, targetDB, `{"usersInfo": 1}`)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(raw)
		var parsed struct {
			Users []struct {
				User  string `json:"user"`
				DB    string `json:"db"`
				Roles []struct {
					Role string `json:"role"`
					DB   string `json:"db"`
				} `json:"roles"`
			} `json:"users"`
		}
		_ = json.Unmarshal(b, &parsed)
		users := make([]AccountInfo, 0, len(parsed.Users))
		for _, u := range parsed.Users {
			roles := make([]string, 0, len(u.Roles))
			isSuper := false
			for _, r := range u.Roles {
				label := r.Role
				if r.DB != "" && r.DB != targetDB {
					label = fmt.Sprintf("%s@%s", r.Role, r.DB)
				}
				roles = append(roles, label)
				if r.Role == "root" || r.Role == "userAdminAnyDatabase" {
					isSuper = true
				}
			}
			users = append(users, AccountInfo{
				Username: u.User,
				Host:     u.DB,
				Identity: fmt.Sprintf("%s@%s", u.User, u.DB),
				CanLogin: true,
				IsSuper:  isSuper,
				Roles:    roles,
			})
		}
		return users, nil
	}

	sqlDrv, ok := drv.(driver.SQLDriver)
	if !ok {
		return nil, fmt.Errorf("driver does not implement SQLDriver")
	}

	switch {
	case isMySQLFamily(t):
		queries := []string{
			"SELECT User, Host, COALESCE(plugin, ''), COALESCE(account_locked, 'N'), COALESCE(Super_priv, 'N') FROM mysql.user ORDER BY User, Host",
			"SELECT User, Host, '', 'N', COALESCE(Super_priv, 'N') FROM mysql.user ORDER BY User, Host",
		}
		for _, q := range queries {
			res, err := sqlDrv.ExecuteSQL(ctx, database, q, 500)
			if err == nil && len(res) > 0 {
				users := make([]AccountInfo, 0, len(res[0].Rows))
				for _, r := range res[0].Rows {
					u := cellStr(r, 0)
					h := cellStr(r, 1)
					if u == "" && h == "" {
						continue
					}
					locked := cellTruthy(r, 3)
					users = append(users, AccountInfo{
						Username:   u,
						Host:       h,
						Identity:   fmt.Sprintf("'%s'@'%s'", u, h),
						AuthPlugin: cellStr(r, 2),
						Locked:     locked,
						CanLogin:   !locked,
						IsSuper:    cellTruthy(r, 4),
					})
				}
				return users, nil
			}
		}
		// Fallback when current account lacks SELECT on mysql.user: return CURRENT_USER()
		res, err := sqlDrv.ExecuteSQL(ctx, database, "SELECT CURRENT_USER()", 1)
		if err != nil {
			return nil, err
		}
		if len(res) > 0 && len(res[0].Rows) > 0 {
			cu := cellStr(res[0].Rows[0], 0)
			u, h := cu, "%"
			if idx := strings.LastIndex(cu, "@"); idx > 0 {
				u = cu[:idx]
				h = cu[idx+1:]
			}
			return []AccountInfo{{
				Username: u,
				Host:     h,
				Identity: fmt.Sprintf("'%s'@'%s'", u, h),
				CanLogin: true,
				Extra:    "Current session account (SELECT on mysql.user restricted)",
			}}, nil
		}
		return []AccountInfo{}, nil

	case isPGFamily(t):
		q := `SELECT r.rolname, r.rolsuper, r.rolcanlogin, r.rolcreatedb, r.rolcreaterole,
			COALESCE((SELECT string_agg(m.rolname, ',') FROM pg_auth_members am JOIN pg_roles m ON m.oid = am.roleid WHERE am.member = r.oid), '')
			FROM pg_roles r WHERE r.rolname NOT LIKE 'pg_%' ORDER BY r.rolname`
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 500)
		if err != nil {
			return nil, err
		}
		users := []AccountInfo{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				uname := cellStr(r, 0)
				if uname == "" {
					continue
				}
				canLogin := cellTruthy(r, 2)
				var roles []string
				if rawRoles := cellStr(r, 5); rawRoles != "" {
					roles = strings.Split(rawRoles, ",")
				}
				var flags []string
				if cellTruthy(r, 3) {
					flags = append(flags, "CREATEDB")
				}
				if cellTruthy(r, 4) {
					flags = append(flags, "CREATEROLE")
				}
				users = append(users, AccountInfo{
					Username: uname,
					Identity: uname,
					IsSuper:  cellTruthy(r, 1),
					CanLogin: canLogin,
					Locked:   !canLogin,
					Roles:    roles,
					Extra:    strings.Join(flags, ", "),
				})
			}
		}
		return users, nil

	case t == "clickhouse":
		q := "SELECT name, storage, auth_type FROM system.users ORDER BY name"
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 500)
		if err != nil {
			res, err = sqlDrv.ExecuteSQL(ctx, database, "SHOW USERS", 500)
			if err != nil {
				return nil, err
			}
		}
		users := []AccountInfo{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				uname := cellStr(r, 0)
				if uname == "" {
					continue
				}
				users = append(users, AccountInfo{
					Username:   uname,
					Identity:   uname,
					CanLogin:   true,
					AuthPlugin: cellStr(r, 2),
					Extra:      cellStr(r, 1),
				})
			}
		}
		return users, nil

	case t == "sqlserver":
		q := `SELECT dp.name, dp.type_desc, COALESCE(dp.authentication_type_desc, ''),
			COALESCE(STRING_AGG(rp.name, ','), '')
			FROM sys.database_principals dp
			LEFT JOIN sys.database_role_members drm ON drm.member_principal_id = dp.principal_id
			LEFT JOIN sys.database_principals rp ON rp.principal_id = drm.role_principal_id
			WHERE dp.type IN ('S','U','G') AND dp.name NOT IN ('INFORMATION_SCHEMA','sys','guest')
			GROUP BY dp.name, dp.type_desc, dp.authentication_type_desc
			ORDER BY dp.name`
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 500)
		if err != nil {
			// Fallback for SQL Server older than 2017 without STRING_AGG
			qFallback := "SELECT name, type_desc, '' FROM sys.database_principals WHERE type IN ('S','U','G') AND name NOT IN ('INFORMATION_SCHEMA','sys','guest') ORDER BY name"
			res, err = sqlDrv.ExecuteSQL(ctx, database, qFallback, 500)
			if err != nil {
				return nil, err
			}
		}
		users := []AccountInfo{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				uname := cellStr(r, 0)
				if uname == "" {
					continue
				}
				var roles []string
				if rawRoles := cellStr(r, 3); rawRoles != "" {
					roles = strings.Split(rawRoles, ",")
				}
				isSuper := uname == "dbo"
				for _, rl := range roles {
					if rl == "db_owner" {
						isSuper = true
					}
				}
				users = append(users, AccountInfo{
					Username:   uname,
					Identity:   uname,
					AuthPlugin: cellStr(r, 2),
					CanLogin:   true,
					IsSuper:    isSuper,
					Roles:      roles,
					Extra:      cellStr(r, 1),
				})
			}
		}
		return users, nil

	case t == "oracle" || t == "dm8" || t == "dameng":
		res, err := sqlDrv.ExecuteSQL(ctx, database, "SELECT USERNAME, ACCOUNT_STATUS FROM DBA_USERS ORDER BY USERNAME", 500)
		if err != nil {
			res, err = sqlDrv.ExecuteSQL(ctx, database, "SELECT USERNAME, 'OPEN' FROM ALL_USERS ORDER BY USERNAME", 500)
			if err != nil {
				return nil, err
			}
		}
		users := []AccountInfo{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				uname := cellStr(r, 0)
				if uname == "" {
					continue
				}
				status := strings.ToUpper(cellStr(r, 1))
				locked := strings.Contains(status, "LOCK")
				users = append(users, AccountInfo{
					Username: uname,
					Identity: uname,
					Locked:   locked,
					CanLogin: !locked,
					Extra:    status,
				})
			}
		}
		return users, nil
	}

	return []AccountInfo{}, nil
}

// GetUserGrants retrieves privilege grants, roles, or ACL rules for a specific database account.
func GetUserGrants(ctx context.Context, dbType string, drv driver.Driver, username, host, database string) ([]GrantEntry, error) {
	t := strings.ToLower(strings.TrimSpace(dbType))
	uname := strings.TrimSpace(username)
	if uname == "" {
		return nil, fmt.Errorf("username is required")
	}

	if t == "redis" {
		kv, ok := drv.(driver.KeyValueDriver)
		if !ok {
			return nil, fmt.Errorf("driver is not a Redis KeyValueDriver")
		}
		res, err := kv.ExecuteCommand(ctx, 0, "ACL LIST")
		if err != nil {
			return nil, err
		}
		for _, raw := range strings.Split(strings.TrimSpace(res.Output), "\n") {
			line := strings.Trim(strings.TrimSpace(raw), `"`)
			if idx := strings.Index(line, "user "); idx > 0 {
				line = line[idx:]
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == uname {
				return []GrantEntry{{
					Statement:  "ACL SETUSER " + strings.TrimPrefix(line, "user "),
					Scope:      "global",
					Privileges: strings.Join(fields[2:], " "),
				}}, nil
			}
		}
		return []GrantEntry{}, nil
	}

	if t == "mongodb" {
		docDrv, ok := drv.(driver.DocumentDriver)
		if !ok {
			return nil, fmt.Errorf("driver is not a MongoDB DocumentDriver")
		}
		targetDB := strings.TrimSpace(host)
		if targetDB == "" {
			targetDB = strings.TrimSpace(database)
		}
		if targetDB == "" {
			targetDB = "admin"
		}
		cmd, _ := json.Marshal(map[string]any{"usersInfo": uname})
		raw, err := docDrv.RunCommand(ctx, targetDB, string(cmd))
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(raw)
		var parsed struct {
			Users []struct {
				Roles []struct {
					Role string `json:"role"`
					DB   string `json:"db"`
				} `json:"roles"`
			} `json:"users"`
		}
		_ = json.Unmarshal(b, &parsed)
		grants := []GrantEntry{}
		for _, u := range parsed.Users {
			for _, r := range u.Roles {
				grants = append(grants, GrantEntry{
					Statement:  fmt.Sprintf("Role %s on database %s", r.Role, r.DB),
					Scope:      r.DB,
					Privileges: r.Role,
				})
			}
		}
		return grants, nil
	}

	sqlDrv, ok := drv.(driver.SQLDriver)
	if !ok {
		return nil, fmt.Errorf("driver does not implement SQLDriver")
	}

	switch {
	case isMySQLFamily(t):
		q := fmt.Sprintf("SHOW GRANTS FOR %s", formatMySQLUser(uname, host))
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 200)
		if err != nil {
			return nil, err
		}
		grants := []GrantEntry{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				stmt := cellStr(r, 0)
				if stmt == "" {
					continue
				}
				privs, scope := parseMySQLGrantStatement(stmt)
				grants = append(grants, GrantEntry{
					Statement:  stmt,
					Scope:      scope,
					Privileges: privs,
					Grantable:  strings.Contains(strings.ToUpper(stmt), "WITH GRANT OPTION"),
				})
			}
		}
		return grants, nil

	case isPGFamily(t):
		grants := []GrantEntry{}
		qRoles := fmt.Sprintf(
			"SELECT m.rolname, am.admin_option FROM pg_auth_members am JOIN pg_roles r ON r.oid = am.member JOIN pg_roles m ON m.oid = am.roleid WHERE r.rolname = %s ORDER BY m.rolname",
			sqlStr(uname),
		)
		if res, err := sqlDrv.ExecuteSQL(ctx, database, qRoles, 100); err == nil && len(res) > 0 {
			for _, r := range res[0].Rows {
				roleName := cellStr(r, 0)
				if roleName != "" {
					grants = append(grants, GrantEntry{
						Statement:  fmt.Sprintf("GRANT %s TO %s", quoteDouble(roleName), quoteDouble(uname)),
						Scope:      "ROLE",
						Privileges: roleName,
						Grantable:  cellTruthy(r, 1),
					})
				}
			}
		}
		qTbl := fmt.Sprintf(
			"SELECT table_schema || '.' || table_name, string_agg(privilege_type, ', ' ORDER BY privilege_type), bool_or(is_grantable = 'YES') FROM information_schema.role_table_grants WHERE grantee = %s GROUP BY table_schema, table_name ORDER BY table_schema, table_name",
			sqlStr(uname),
		)
		if res, err := sqlDrv.ExecuteSQL(ctx, database, qTbl, 300); err == nil && len(res) > 0 {
			for _, r := range res[0].Rows {
				scope := cellStr(r, 0)
				privs := cellStr(r, 1)
				grants = append(grants, GrantEntry{
					Statement:  fmt.Sprintf("GRANT %s ON TABLE %s TO %s", privs, scope, quoteDouble(uname)),
					Scope:      scope,
					Privileges: privs,
					Grantable:  cellTruthy(r, 2),
				})
			}
		}
		return grants, nil

	case t == "clickhouse":
		q := fmt.Sprintf("SHOW GRANTS FOR %s", quoteBacktick(uname))
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 200)
		if err != nil {
			return nil, err
		}
		grants := []GrantEntry{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				stmt := cellStr(r, 0)
				if stmt != "" {
					grants = append(grants, GrantEntry{
						Statement: stmt,
						Grantable: strings.Contains(strings.ToUpper(stmt), "WITH GRANT OPTION"),
					})
				}
			}
		}
		return grants, nil

	case t == "sqlserver":
		q := fmt.Sprintf(
			`SELECT dp.state_desc, dp.permission_name, dp.class_desc, COALESCE(OBJECT_NAME(dp.major_id), '')
			FROM sys.database_permissions dp
			JOIN sys.database_principals pr ON pr.principal_id = dp.grantee_principal_id
			WHERE pr.name = %s ORDER BY dp.class_desc, dp.permission_name`,
			sqlStr(uname),
		)
		res, err := sqlDrv.ExecuteSQL(ctx, database, q, 300)
		if err != nil {
			return nil, err
		}
		grants := []GrantEntry{}
		if len(res) > 0 {
			for _, r := range res[0].Rows {
				state := cellStr(r, 0)
				perm := cellStr(r, 1)
				cls := cellStr(r, 2)
				obj := cellStr(r, 3)
				scope := cls
				if obj != "" {
					scope = obj
				}
				grants = append(grants, GrantEntry{
					Statement:  fmt.Sprintf("%s %s ON %s TO %s", state, perm, scope, quoteBracket(uname)),
					Scope:      scope,
					Privileges: perm,
					Grantable:  strings.Contains(strings.ToUpper(state), "WITH_GRANT_OPTION"),
				})
			}
		}
		return grants, nil

	case t == "oracle" || t == "dm8" || t == "dameng":
		grants := []GrantEntry{}
		qSys := fmt.Sprintf("SELECT PRIVILEGE, ADMIN_OPTION FROM DBA_SYS_PRIVS WHERE GRANTEE = %s ORDER BY PRIVILEGE", sqlStr(strings.ToUpper(uname)))
		if res, err := sqlDrv.ExecuteSQL(ctx, database, qSys, 200); err == nil && len(res) > 0 {
			for _, r := range res[0].Rows {
				priv := cellStr(r, 0)
				if priv != "" {
					grants = append(grants, GrantEntry{
						Statement:  fmt.Sprintf("GRANT %s TO %s", priv, quoteDouble(uname)),
						Scope:      "SYSTEM",
						Privileges: priv,
						Grantable:  cellTruthy(r, 1),
					})
				}
			}
		}
		qRole := fmt.Sprintf("SELECT GRANTED_ROLE, ADMIN_OPTION FROM DBA_ROLE_PRIVS WHERE GRANTEE = %s ORDER BY GRANTED_ROLE", sqlStr(strings.ToUpper(uname)))
		if res, err := sqlDrv.ExecuteSQL(ctx, database, qRole, 100); err == nil && len(res) > 0 {
			for _, r := range res[0].Rows {
				rl := cellStr(r, 0)
				if rl != "" {
					grants = append(grants, GrantEntry{
						Statement:  fmt.Sprintf("GRANT %s TO %s", rl, quoteDouble(uname)),
						Scope:      "ROLE",
						Privileges: rl,
						Grantable:  cellTruthy(r, 1),
					})
				}
			}
		}
		return grants, nil
	}

	return []GrantEntry{}, nil
}

func parseMySQLGrantStatement(stmt string) (privs string, scope string) {
	upper := strings.ToUpper(stmt)
	if strings.HasPrefix(upper, "GRANT ") {
		onIdx := strings.Index(upper, " ON ")
		toIdx := strings.Index(upper, " TO ")
		if onIdx > 6 && toIdx > onIdx+4 {
			privs = strings.TrimSpace(stmt[6:onIdx])
			scope = strings.TrimSpace(stmt[onIdx+4 : toIdx])
		}
	}
	return privs, scope
}

// BuildStatements generates previewable SQL statements, Redis ACL commands, or MongoDB JSON admin commands for an action.
func BuildStatements(dbType string, req ActionRequest) ([]string, error) {
	t := strings.ToLower(strings.TrimSpace(dbType))
	caps := GetCapabilities(t)
	if !caps.Supported {
		return nil, fmt.Errorf("database user & privilege governance is not supported for %s", dbType)
	}

	action := strings.ToUpper(strings.TrimSpace(req.Action))
	uname := strings.TrimSpace(req.Username)
	if uname == "" {
		return nil, fmt.Errorf("username is required")
	}

	if t == "redis" {
		return buildRedisACLCommands(action, uname, req)
	}
	if t == "mongodb" {
		return buildMongoPrivilegeCommands(action, uname, req)
	}

	privs, err := sanitizePrivileges(req.Privileges)
	if err != nil {
		return nil, err
	}
	roles, err := sanitizePrivileges(req.Roles)
	if err != nil {
		return nil, fmt.Errorf("invalid role name: %w", err)
	}
	// Preserve original role case from req.Roles when valid
	cleanRoles := make([]string, 0, len(roles))
	for _, raw := range req.Roles {
		r := strings.TrimSpace(raw)
		if r != "" {
			cleanRoles = append(cleanRoles, r)
		}
	}

	switch {
	case isMySQLFamily(t):
		userSpec := formatMySQLUser(uname, req.Host)
		switch action {
		case "CREATE_USER":
			stmt := fmt.Sprintf("CREATE USER %s", userSpec)
			if req.Password != "" {
				stmt += fmt.Sprintf(" IDENTIFIED BY %s", sqlStr(req.Password))
			}
			stmts := []string{stmt}
			if len(privs) > 0 {
				grantStmts, gErr := BuildStatements(dbType, ActionRequest{
					Action:          "GRANT",
					Username:        uname,
					Host:            req.Host,
					Database:        req.Database,
					Table:           req.Table,
					ScopeType:       req.ScopeType,
					Privileges:      req.Privileges,
					WithGrantOption: req.WithGrantOption,
				})
				if gErr != nil {
					return nil, gErr
				}
				stmts = append(stmts, grantStmts...)
			}
			return stmts, nil
		case "ALTER_USER":
			if req.Password == "" {
				return nil, fmt.Errorf("new password is required")
			}
			return []string{fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s", userSpec, sqlStr(req.Password))}, nil
		case "LOCK_USER":
			return []string{fmt.Sprintf("ALTER USER %s ACCOUNT LOCK", userSpec)}, nil
		case "UNLOCK_USER":
			return []string{fmt.Sprintf("ALTER USER %s ACCOUNT UNLOCK", userSpec)}, nil
		case "DROP_USER":
			return []string{fmt.Sprintf("DROP USER %s", userSpec)}, nil
		case "GRANT", "REVOKE":
			if len(privs) == 0 {
				return nil, fmt.Errorf("at least one privilege must be selected")
			}
			target := "*.*"
			scope := strings.ToLower(strings.TrimSpace(req.ScopeType))
			db := strings.TrimSpace(req.Database)
			tbl := strings.TrimSpace(req.Table)
			if scope == "database" || (scope == "" && db != "" && tbl == "") {
				if db == "" {
					return nil, fmt.Errorf("database name is required for database-scope privileges")
				}
				target = quoteBacktick(db) + ".*"
			} else if scope == "table" || tbl != "" {
				if db == "" || tbl == "" {
					return nil, fmt.Errorf("database and table names are required for table-scope privileges")
				}
				target = quoteBacktick(db) + "." + quoteBacktick(tbl)
			}
			privList := strings.Join(privs, ", ")
			if action == "GRANT" {
				stmt := fmt.Sprintf("GRANT %s ON %s TO %s", privList, target, userSpec)
				if req.WithGrantOption {
					stmt += " WITH GRANT OPTION"
				}
				return []string{stmt}, nil
			}
			return []string{fmt.Sprintf("REVOKE %s ON %s FROM %s", privList, target, userSpec)}, nil
		}

	case isPGFamily(t):
		qu := quoteDouble(uname)
		switch action {
		case "CREATE_USER":
			opts := []string{"LOGIN"}
			if req.IsSuper {
				opts = append(opts, "SUPERUSER")
			}
			if req.CanCreateDB {
				opts = append(opts, "CREATEDB")
			}
			if req.CanCreateRole {
				opts = append(opts, "CREATEROLE")
			}
			if req.Password != "" {
				opts = append(opts, "PASSWORD "+sqlStr(req.Password))
			}
			stmts := []string{fmt.Sprintf("CREATE ROLE %s WITH %s", qu, strings.Join(opts, " "))}
			if len(privs) > 0 || len(cleanRoles) > 0 {
				grantStmts, gErr := BuildStatements(dbType, ActionRequest{
					Action:          "GRANT",
					Username:        uname,
					Database:        req.Database,
					Schema:          req.Schema,
					Table:           req.Table,
					ScopeType:       req.ScopeType,
					Privileges:      req.Privileges,
					Roles:           req.Roles,
					WithGrantOption: req.WithGrantOption,
				})
				if gErr != nil {
					return nil, gErr
				}
				stmts = append(stmts, grantStmts...)
			}
			return stmts, nil
		case "ALTER_USER":
			if req.Password == "" {
				return nil, fmt.Errorf("new password is required")
			}
			return []string{fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s", qu, sqlStr(req.Password))}, nil
		case "LOCK_USER":
			return []string{fmt.Sprintf("ALTER ROLE %s NOLOGIN", qu)}, nil
		case "UNLOCK_USER":
			return []string{fmt.Sprintf("ALTER ROLE %s LOGIN", qu)}, nil
		case "DROP_USER":
			return []string{fmt.Sprintf("DROP ROLE %s", qu)}, nil
		case "GRANT", "REVOKE":
			var stmts []string
			for _, rl := range cleanRoles {
				if action == "GRANT" {
					stmt := fmt.Sprintf("GRANT %s TO %s", quoteDouble(rl), qu)
					if req.WithGrantOption {
						stmt += " WITH ADMIN OPTION"
					}
					stmts = append(stmts, stmt)
				} else {
					stmts = append(stmts, fmt.Sprintf("REVOKE %s FROM %s", quoteDouble(rl), qu))
				}
			}
			if len(privs) > 0 {
				sc := strings.TrimSpace(req.Schema)
				if sc == "" {
					sc = "public"
				}
				db := strings.TrimSpace(req.Database)
				tbl := strings.TrimSpace(req.Table)
				scope := strings.ToLower(strings.TrimSpace(req.ScopeType))

				var dbPrivs, schemaPrivs, tablePrivs []string
				for _, p := range privs {
					switch p {
					case "CONNECT", "TEMPORARY":
						dbPrivs = append(dbPrivs, p)
					case "USAGE":
						schemaPrivs = append(schemaPrivs, p)
					case "CREATE":
						if scope == "database" {
							dbPrivs = append(dbPrivs, p)
						} else {
							schemaPrivs = append(schemaPrivs, p)
						}
					default:
						tablePrivs = append(tablePrivs, p)
					}
				}
				sort.Strings(dbPrivs)
				sort.Strings(schemaPrivs)
				prep := "TO"
				suffix := ""
				if action == "REVOKE" {
					prep = "FROM"
				} else if req.WithGrantOption {
					suffix = " WITH GRANT OPTION"
				}
				if len(dbPrivs) > 0 && db != "" {
					stmts = append(stmts, fmt.Sprintf("%s %s ON DATABASE %s %s %s%s", action, strings.Join(dbPrivs, ", "), quoteDouble(db), prep, qu, suffix))
				}
				if len(schemaPrivs) > 0 {
					stmts = append(stmts, fmt.Sprintf("%s %s ON SCHEMA %s %s %s%s", action, strings.Join(schemaPrivs, ", "), quoteDouble(sc), prep, qu, suffix))
				}
				if len(tablePrivs) > 0 {
					if scope == "table" && tbl != "" {
						stmts = append(stmts, fmt.Sprintf("%s %s ON TABLE %s.%s %s %s%s", action, strings.Join(tablePrivs, ", "), quoteDouble(sc), quoteDouble(tbl), prep, qu, suffix))
					} else {
						stmts = append(stmts, fmt.Sprintf("%s %s ON ALL TABLES IN SCHEMA %s %s %s%s", action, strings.Join(tablePrivs, ", "), quoteDouble(sc), prep, qu, suffix))
					}
				}
			}
			if len(stmts) == 0 {
				return nil, fmt.Errorf("select at least one privilege or role")
			}
			return stmts, nil
		}

	case t == "clickhouse":
		qu := quoteBacktick(uname)
		switch action {
		case "CREATE_USER":
			stmt := fmt.Sprintf("CREATE USER %s", qu)
			if req.Password != "" {
				stmt += fmt.Sprintf(" IDENTIFIED BY %s", sqlStr(req.Password))
			}
			return []string{stmt}, nil
		case "ALTER_USER":
			return []string{fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s", qu, sqlStr(req.Password))}, nil
		case "DROP_USER":
			return []string{fmt.Sprintf("DROP USER %s", qu)}, nil
		case "GRANT", "REVOKE":
			if len(privs) == 0 {
				return nil, fmt.Errorf("select at least one privilege")
			}
			target := "*.*"
			db := strings.TrimSpace(req.Database)
			tbl := strings.TrimSpace(req.Table)
			scope := strings.ToLower(strings.TrimSpace(req.ScopeType))
			if scope == "database" && db != "" {
				target = quoteBacktick(db) + ".*"
			} else if scope == "table" && db != "" && tbl != "" {
				target = quoteBacktick(db) + "." + quoteBacktick(tbl)
			}
			if action == "GRANT" {
				stmt := fmt.Sprintf("GRANT %s ON %s TO %s", strings.Join(privs, ", "), target, qu)
				if req.WithGrantOption {
					stmt += " WITH GRANT OPTION"
				}
				return []string{stmt}, nil
			}
			return []string{fmt.Sprintf("REVOKE %s ON %s FROM %s", strings.Join(privs, ", "), target, qu)}, nil
		}

	case t == "sqlserver":
		qu := quoteBracket(uname)
		switch action {
		case "CREATE_USER":
			if req.Password != "" {
				return []string{
					fmt.Sprintf("CREATE LOGIN %s WITH PASSWORD = %s", qu, sqlStr(req.Password)),
					fmt.Sprintf("CREATE USER %s FOR LOGIN %s", qu, qu),
				}, nil
			}
			return []string{fmt.Sprintf("CREATE USER %s WITHOUT LOGIN", qu)}, nil
		case "ALTER_USER":
			return []string{fmt.Sprintf("ALTER LOGIN %s WITH PASSWORD = %s", qu, sqlStr(req.Password))}, nil
		case "DROP_USER":
			return []string{fmt.Sprintf("DROP USER %s", qu)}, nil
		case "GRANT", "REVOKE":
			var stmts []string
			for _, rl := range cleanRoles {
				if action == "GRANT" {
					stmts = append(stmts, fmt.Sprintf("ALTER ROLE %s ADD MEMBER %s", quoteBracket(rl), qu))
				} else {
					stmts = append(stmts, fmt.Sprintf("ALTER ROLE %s DROP MEMBER %s", quoteBracket(rl), qu))
				}
			}
			if len(privs) > 0 {
				sc := strings.TrimSpace(req.Schema)
				if sc == "" {
					sc = "dbo"
				}
				tbl := strings.TrimSpace(req.Table)
				prep := "TO"
				if action == "REVOKE" {
					prep = "FROM"
				}
				if strings.ToLower(req.ScopeType) == "table" && tbl != "" {
					stmts = append(stmts, fmt.Sprintf("%s %s ON %s.%s %s %s", action, strings.Join(privs, ", "), quoteBracket(sc), quoteBracket(tbl), prep, qu))
				} else {
					stmts = append(stmts, fmt.Sprintf("%s %s ON SCHEMA::%s %s %s", action, strings.Join(privs, ", "), quoteBracket(sc), prep, qu))
				}
			}
			if len(stmts) == 0 {
				return nil, fmt.Errorf("select at least one privilege or database role")
			}
			return stmts, nil
		}

	case t == "oracle" || t == "dm8" || t == "dameng":
		qu := quoteDouble(uname)
		switch action {
		case "CREATE_USER":
			if req.Password == "" {
				return nil, fmt.Errorf("password is required")
			}
			return []string{fmt.Sprintf("CREATE USER %s IDENTIFIED BY %s", qu, quoteDouble(req.Password))}, nil
		case "ALTER_USER":
			return []string{fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s", qu, quoteDouble(req.Password))}, nil
		case "LOCK_USER":
			return []string{fmt.Sprintf("ALTER USER %s ACCOUNT LOCK", qu)}, nil
		case "UNLOCK_USER":
			return []string{fmt.Sprintf("ALTER USER %s ACCOUNT UNLOCK", qu)}, nil
		case "DROP_USER":
			return []string{fmt.Sprintf("DROP USER %s CASCADE", qu)}, nil
		case "GRANT", "REVOKE":
			var stmts []string
			prep := "TO"
			if action == "REVOKE" {
				prep = "FROM"
			}
			for _, rl := range cleanRoles {
				stmts = append(stmts, fmt.Sprintf("%s %s %s %s", action, rl, prep, qu))
			}
			if len(privs) > 0 {
				tbl := strings.TrimSpace(req.Table)
				sc := strings.TrimSpace(req.Schema)
				if strings.ToLower(req.ScopeType) == "table" && tbl != "" {
					obj := quoteDouble(tbl)
					if sc != "" {
						obj = quoteDouble(sc) + "." + quoteDouble(tbl)
					}
					stmts = append(stmts, fmt.Sprintf("%s %s ON %s %s %s", action, strings.Join(privs, ", "), obj, prep, qu))
				} else {
					stmts = append(stmts, fmt.Sprintf("%s %s %s %s", action, strings.Join(privs, ", "), prep, qu))
				}
			}
			if len(stmts) == 0 {
				return nil, fmt.Errorf("select at least one privilege or role")
			}
			return stmts, nil
		}
	}

	return nil, fmt.Errorf("unsupported action %q for engine %s", action, dbType)
}

func buildRedisACLCommands(action, uname string, req ActionRequest) ([]string, error) {
	if strings.ContainsAny(uname, " \t\r\n\"'") {
		return nil, fmt.Errorf("Redis ACL username cannot contain whitespace or quotes")
	}
	switch action {
	case "CREATE_USER", "GRANT":
		rules := strings.TrimSpace(req.ACLRules)
		if rules == "" {
			switch strings.ToLower(strings.TrimSpace(req.ACLPreset)) {
			case "readonly":
				rules = "~* +@read -@write -@admin -@dangerous"
			case "admin":
				rules = "~* &* +@all"
			default:
				rules = "~* +@read +@write -@dangerous"
			}
		}
		if len(req.Privileges) > 0 && req.ACLRules == "" && req.ACLPreset == "" {
			rules = strings.Join(req.Privileges, " ")
		}
		pwdPart := ""
		if req.Password != "" {
			if strings.ContainsAny(req.Password, " \t\r\n") {
				return nil, fmt.Errorf("Redis ACL password token cannot contain whitespace")
			}
			pwdPart = " >" + req.Password
		} else if action == "CREATE_USER" {
			pwdPart = " nopass"
		}
		return []string{fmt.Sprintf("ACL SETUSER %s on%s %s", uname, pwdPart, rules)}, nil
	case "ALTER_USER":
		if req.Password == "" {
			return nil, fmt.Errorf("new password is required")
		}
		return []string{fmt.Sprintf("ACL SETUSER %s resetpass >%s", uname, req.Password)}, nil
	case "LOCK_USER":
		return []string{fmt.Sprintf("ACL SETUSER %s off", uname)}, nil
	case "UNLOCK_USER":
		return []string{fmt.Sprintf("ACL SETUSER %s on", uname)}, nil
	case "DROP_USER":
		if uname == "default" {
			return nil, fmt.Errorf("the built-in Redis 'default' ACL user cannot be deleted")
		}
		return []string{fmt.Sprintf("ACL DELUSER %s", uname)}, nil
	case "REVOKE":
		return []string{fmt.Sprintf("ACL SETUSER %s resetkeys resetchannels -@all", uname)}, nil
	default:
		return nil, fmt.Errorf("unsupported Redis ACL action %q", action)
	}
}

func buildMongoPrivilegeCommands(action, uname string, req ActionRequest) ([]string, error) {
	targetDB := strings.TrimSpace(req.Database)
	if targetDB == "" {
		targetDB = strings.TrimSpace(req.Host)
	}
	if targetDB == "" {
		targetDB = "admin"
	}

	toRoleDocs := func(roles []string) []map[string]string {
		out := make([]map[string]string, 0, len(roles))
		for _, raw := range roles {
			r := strings.TrimSpace(raw)
			if r == "" {
				continue
			}
			roleName, roleDB := r, targetDB
			if idx := strings.LastIndex(r, "@"); idx > 0 {
				roleName = r[:idx]
				roleDB = r[idx+1:]
			}
			out = append(out, map[string]string{"role": roleName, "db": roleDB})
		}
		return out
	}

	var cmd map[string]any
	switch action {
	case "CREATE_USER":
		if req.Password == "" {
			return nil, fmt.Errorf("password is required to create a MongoDB user")
		}
		roles := toRoleDocs(req.Roles)
		if len(roles) == 0 {
			roles = []map[string]string{{"role": "read", "db": targetDB}}
		}
		cmd = map[string]any{
			"createUser": uname,
			"pwd":        req.Password,
			"roles":      roles,
		}
	case "ALTER_USER":
		if req.Password == "" {
			return nil, fmt.Errorf("new password is required")
		}
		cmd = map[string]any{
			"updateUser": uname,
			"pwd":        req.Password,
		}
	case "DROP_USER":
		cmd = map[string]any{
			"dropUser": uname,
		}
	case "GRANT":
		roles := toRoleDocs(req.Roles)
		if len(roles) == 0 {
			return nil, fmt.Errorf("at least one MongoDB role is required")
		}
		cmd = map[string]any{
			"grantRolesToUser": uname,
			"roles":            roles,
		}
	case "REVOKE":
		roles := toRoleDocs(req.Roles)
		if len(roles) == 0 {
			return nil, fmt.Errorf("at least one MongoDB role is required")
		}
		cmd = map[string]any{
			"revokeRolesFromUser": uname,
			"roles":               roles,
		}
	default:
		return nil, fmt.Errorf("unsupported MongoDB user action %q", action)
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return []string{string(b)}, nil
}

// ExecuteAction builds and executes user/privilege governance statements against the target database driver.
func ExecuteAction(ctx context.Context, dbType string, drv driver.Driver, req ActionRequest) ([]string, error) {
	stmts, err := BuildStatements(dbType, req)
	if err != nil {
		return nil, err
	}
	t := strings.ToLower(strings.TrimSpace(dbType))

	if t == "redis" {
		kv, ok := drv.(driver.KeyValueDriver)
		if !ok {
			return stmts, fmt.Errorf("driver is not a Redis KeyValueDriver")
		}
		for _, cmd := range stmts {
			if _, err := kv.ExecuteCommand(ctx, 0, cmd); err != nil {
				return stmts, err
			}
		}
		return stmts, nil
	}

	if t == "mongodb" {
		docDrv, ok := drv.(driver.DocumentDriver)
		if !ok {
			return stmts, fmt.Errorf("driver is not a MongoDB DocumentDriver")
		}
		targetDB := strings.TrimSpace(req.Database)
		if targetDB == "" {
			targetDB = strings.TrimSpace(req.Host)
		}
		if targetDB == "" {
			targetDB = "admin"
		}
		for _, cmdJSON := range stmts {
			if _, err := docDrv.RunCommand(ctx, targetDB, cmdJSON); err != nil {
				return stmts, err
			}
		}
		return stmts, nil
	}

	sqlDrv, ok := drv.(driver.SQLDriver)
	if !ok {
		return stmts, fmt.Errorf("driver does not implement SQLDriver")
	}
	for _, stmt := range stmts {
		if _, err := sqlDrv.ExecuteSQL(ctx, req.Database, stmt, 10); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}
