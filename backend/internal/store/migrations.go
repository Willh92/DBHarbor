package store

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dbharbor/backend/internal/version"
)

// LatestSchemaVersion is the target internal SQLite schema version for this DBHarbor release.
// Increment this constant and append a new migration step whenever a future version alters internal tables.
const LatestSchemaVersion = 3

const maxRetainedMigrationBackups = 5

type schemaMigration struct {
	Version int
	Name    string
	Up      func(tx *sql.Tx) error
}

var orderedMigrations = []schemaMigration{
	{
		Version: 1,
		Name:    "v1_0_0_core_tables_rbac_and_2fa",
		Up: func(tx *sql.Tx) error {
			coreDDL := `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	disabled INTEGER NOT NULL DEFAULT 0,
	can_create_conn INTEGER NOT NULL DEFAULT 1,
	conn_scope TEXT NOT NULL DEFAULT 'all',
	totp_enabled INTEGER NOT NULL DEFAULT 0,
	totp_secret_enc TEXT NOT NULL DEFAULT '',
	totp_pending_enc TEXT NOT NULL DEFAULT '',
	recovery_hashes_json TEXT NOT NULL DEFAULT '[]',
	last_totp_step INTEGER NOT NULL DEFAULT 0,
	password_changed INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON auth_sessions(user_id, revoked);

CREATE TABLE IF NOT EXISTS security_audit_logs (
	id TEXT PRIMARY KEY,
	event TEXT NOT NULL,
	username TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON security_audit_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS connections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	color TEXT NOT NULL DEFAULT '#1677ff',
	host TEXT NOT NULL DEFAULT '',
	port INTEGER NOT NULL DEFAULT 0,
	username TEXT NOT NULL DEFAULT '',
	password_enc TEXT NOT NULL DEFAULT '',
	database_name TEXT NOT NULL DEFAULT '',
	file_path TEXT NOT NULL DEFAULT '',
	uri_enc TEXT NOT NULL DEFAULT '',
	options_json TEXT NOT NULL DEFAULT '{}',
	owner_id TEXT NOT NULL DEFAULT '',
	visibility TEXT NOT NULL DEFAULT 'shared',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS query_history (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	connection_id TEXT NOT NULL,
	conn_name TEXT NOT NULL DEFAULT '',
	db_type TEXT NOT NULL DEFAULT '',
	database_name TEXT NOT NULL DEFAULT '',
	query_text TEXT NOT NULL,
	elapsed_ms INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'ok',
	error_msg TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_conn_created ON query_history(connection_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_history_created ON query_history(created_at DESC);

CREATE TABLE IF NOT EXISTS saved_snippets (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	db_type TEXT NOT NULL DEFAULT '',
	connection_id TEXT NOT NULL DEFAULT '',
	database_name TEXT NOT NULL DEFAULT '',
	sql_text TEXT NOT NULL,
	owner_id TEXT NOT NULL DEFAULT '',
	owner_name TEXT NOT NULL DEFAULT '',
	is_shared INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL
);
`
			if _, err := tx.Exec(coreDDL); err != nil {
				return err
			}

			// Idempotent column upgrades for legacy databases created prior to v1.0.0
			colSpecs := []struct {
				table string
				col   string
				spec  string
			}{
				{"users", "totp_enabled", "INTEGER NOT NULL DEFAULT 0"},
				{"users", "totp_secret_enc", "TEXT NOT NULL DEFAULT ''"},
				{"users", "totp_pending_enc", "TEXT NOT NULL DEFAULT ''"},
				{"users", "recovery_hashes_json", "TEXT NOT NULL DEFAULT '[]'"},
				{"users", "last_totp_step", "INTEGER NOT NULL DEFAULT 0"},
				{"users", "password_changed", "INTEGER NOT NULL DEFAULT 0"},
				{"users", "display_name", "TEXT NOT NULL DEFAULT ''"},
				{"users", "role", "TEXT NOT NULL DEFAULT 'admin'"},
				{"users", "disabled", "INTEGER NOT NULL DEFAULT 0"},
				{"users", "can_create_conn", "INTEGER NOT NULL DEFAULT 1"},
				{"users", "conn_scope", "TEXT NOT NULL DEFAULT 'all'"},
				{"connections", "color", "TEXT NOT NULL DEFAULT '#1677ff'"},
				{"connections", "host", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "port", "INTEGER NOT NULL DEFAULT 0"},
				{"connections", "username", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "password_enc", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "database_name", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "file_path", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "uri_enc", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "options_json", "TEXT NOT NULL DEFAULT '{}'"},
				{"connections", "owner_id", "TEXT NOT NULL DEFAULT ''"},
				{"connections", "visibility", "TEXT NOT NULL DEFAULT 'shared'"},
				{"query_history", "user_id", "TEXT NOT NULL DEFAULT ''"},
				{"saved_snippets", "owner_id", "TEXT NOT NULL DEFAULT ''"},
				{"saved_snippets", "owner_name", "TEXT NOT NULL DEFAULT ''"},
				{"saved_snippets", "is_shared", "INTEGER NOT NULL DEFAULT 1"},
			}
			for _, cs := range colSpecs {
				if err := ensureColumnExists(tx, cs.table, cs.col, cs.spec); err != nil {
					return err
				}
			}

			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_history_user_created ON query_history(user_id, created_at DESC)`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		Version: 2,
		Name:    "v1_0_1_system_settings_and_login_branding",
		Up: func(tx *sql.Tx) error {
			ddl := `
CREATE TABLE IF NOT EXISTS system_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_connections_owner_vis ON connections(owner_id, visibility);
`
			_, err := tx.Exec(ddl)
			return err
		},
	},
	{
		Version: 3,
		Name:    "v1_0_3_connection_remember_password",
		Up: func(tx *sql.Tx) error {
			return ensureColumnExists(tx, "connections", "remember_password", "INTEGER NOT NULL DEFAULT 1")
		},
	},
}

// SchemaVersion returns the currently applied internal metadata database schema version.
func (s *Store) SchemaVersion() (int, error) {
	var ver int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&ver)
	return ver, err
}

func (s *Store) migrate() error {
	// Check whether pre-existing user data tables exist before bootstrapping schema_migrations
	hasExistingData, err := s.hasExistingMetadataTables()
	if err != nil {
		return fmt.Errorf("inspect metadata schema state: %w", err)
	}

	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	app_version TEXT NOT NULL DEFAULT '',
	applied_at TEXT NOT NULL
);
`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	currentVer, err := s.detectCurrentSchemaVersion()
	if err != nil {
		return err
	}
	if currentVer >= LatestSchemaVersion {
		return nil
	}

	// Automatically snapshot existing metadata DB before applying forward schema upgrades
	if hasExistingData && s.dataDir != "" {
		if bakPath, bakErr := s.backupDatabaseBeforeUpgrade(currentVer, LatestSchemaVersion); bakErr != nil {
			log.Printf("[DBHarbor] Warning: pre-upgrade metadata backup skipped: %v", bakErr)
		} else if bakPath != "" {
			log.Printf("[DBHarbor] Created automatic pre-upgrade metadata backup: %s", bakPath)
		}
	}

	appVer := version.NormalizeTag(version.Version)
	for _, m := range orderedMigrations {
		if m.Version <= currentVer {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration v%d (%s): %w", m.Version, m.Name, err)
		}
		if err := m.Up(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration v%d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO schema_migrations (version, name, app_version, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version, m.Name, appVer, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration v%d: %w", m.Version, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set user_version=%d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration v%d (%s): %w", m.Version, m.Name, err)
		}
		if hasExistingData {
			log.Printf("[DBHarbor] Auto-migrated metadata database schema -> v%d (%s)", m.Version, m.Name)
		}
	}
	return nil
}

func (s *Store) hasExistingMetadataTables() (bool, error) {
	var cnt int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'connections')`,
	).Scan(&cnt)
	return cnt > 0, err
}

func (s *Store) detectCurrentSchemaVersion() (int, error) {
	var tableVer int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&tableVer); err != nil {
		return 0, fmt.Errorf("query schema_migrations version: %w", err)
	}
	var pragmaVer int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&pragmaVer); err == nil && pragmaVer > tableVer {
		tableVer = pragmaVer
	}
	return tableVer, nil
}

func ensureColumnExists(tx *sql.Tx, table, column, columnSpec string) error {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspect table_info(%s): %w", table, err)
	}
	exists := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if scanErr := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		if strings.EqualFold(name, column) {
			exists = true
		}
	}
	if closeErr := rows.Close(); closeErr != nil {
		return closeErr
	}
	if exists {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, columnSpec)
	if _, err := tx.Exec(stmt); err != nil {
		return fmt.Errorf("alter %s add column %s: %w", table, column, err)
	}
	return nil
}

func (s *Store) backupDatabaseBeforeUpgrade(fromVer, toVer int) (string, error) {
	srcPath := filepath.Join(s.dataDir, "dbharbor.db")
	st, err := os.Stat(srcPath)
	if err != nil || st.Size() == 0 {
		return "", nil
	}

	backupDir := filepath.Join(s.dataDir, ".backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}

	bakName := fmt.Sprintf(
		"dbharbor.db.v%d-to-v%d-%s.bak",
		fromVer,
		toVer,
		time.Now().UTC().Format("20060102T150405Z"),
	)
	bakPath := filepath.Join(backupDir, bakName)
	escapedDest := strings.ReplaceAll(filepath.ToSlash(bakPath), "'", "''")

	// Prefer atomic SQLite VACUUM INTO for a consistent single-file snapshot even in WAL mode
	if _, vacErr := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escapedDest)); vacErr == nil {
		_ = os.Chmod(bakPath, 0o600)
		pruneOldMigrationBackups(backupDir, maxRetainedMigrationBackups)
		return bakPath, nil
	}

	// Fallback: checkpoint WAL and copy dbharbor.db directly
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	in, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer in.Close()

	out, err := os.OpenFile(bakPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(bakPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(bakPath)
		return "", closeErr
	}
	pruneOldMigrationBackups(backupDir, maxRetainedMigrationBackups)
	return bakPath, nil
}

func pruneOldMigrationBackups(backupDir string, keep int) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bak") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, old := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(backupDir, old))
	}
}
