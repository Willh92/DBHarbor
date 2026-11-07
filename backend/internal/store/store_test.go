package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbharbor/backend/internal/crypto"
)

func TestOpenCustomAdminBootstrapAndValidation(t *testing.T) {
	invalidDir := t.TempDir()
	vault1, err := crypto.NewVaultFromDataDir(invalidDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	if _, err := Open(invalidDir, vault1, "a", "StrongPass123"); err == nil {
		t.Fatal("expected 1-char admin username to fail validation")
	}
	if _, err := Open(invalidDir, vault1, "ops_admin", "123"); err == nil {
		t.Fatal("expected <6 char custom admin password to fail validation")
	}

	validDir := t.TempDir()
	vault2, err := crypto.NewVaultFromDataDir(validDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	st, err := Open(validDir, vault2, "ops.admin@corp", "Secr3t!2026")
	if err != nil {
		t.Fatalf("Open with custom admin failed: %v", err)
	}

	u, err := st.Authenticate("ops.admin@corp", "Secr3t!2026")
	if err != nil {
		_ = st.Close()
		t.Fatalf("Authenticate custom admin failed: %v", err)
	}
	if u.Role != RoleAdmin || !u.PasswordChanged {
		_ = st.Close()
		t.Fatalf("expected RoleAdmin with PasswordChanged=true, got role=%q passwordChanged=%v", u.Role, u.PasswordChanged)
	}
	if _, err := st.Authenticate("admin", "admin123"); err == nil {
		_ = st.Close()
		t.Fatal("default admin account should not exist when custom admin was initialized")
	}
	_ = st.Close()

	// Re-opening existing DB with different bootstrap params must NOT overwrite existing users
	st2, err := Open(validDir, vault2, "another_admin", "AnotherPass99")
	if err != nil {
		t.Fatalf("Re-open existing store failed: %v", err)
	}
	defer st2.Close()
	if _, err := st2.Authenticate("ops.admin@corp", "Secr3t!2026"); err != nil {
		t.Fatalf("expected original custom admin to remain intact after re-open: %v", err)
	}
	if _, err := st2.Authenticate("another_admin", "AnotherPass99"); err == nil {
		t.Fatal("expected ensureAdmin to be a no-op once users table is already populated")
	}
}

func TestDeferredWebSetupAndLoginBranding(t *testing.T) {
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}

	// Open with empty bootstrap credentials -> deferred Web setup mode
	st, err := Open(tmpDir, vault, "", "")
	if err != nil {
		t.Fatalf("Open deferred setup failed: %v", err)
	}
	defer st.Close()

	if !st.NeedsSetup() {
		t.Fatal("expected NeedsSetup() == true on freshly initialized empty store")
	}
	if _, err := st.Authenticate("admin", "admin123"); err == nil {
		t.Fatal("built-in default admin/admin123 must NOT exist")
	}

	// Reject weak initial password (< 8 chars)
	if _, err := st.SetupInitialAdmin("sec_ops", "SecOps", "short7", nil); err == nil {
		t.Fatal("expected <8 char password to be rejected by SetupInitialAdmin")
	}

	// Valid SVG icon without scripts
	safeSVG := `data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="#2563eb"/></svg>`))
	initialBranding := &LoginBrandingConfig{
		PageTitle:         "Internal Data Console",
		IconMode:          "custom",
		CustomIconDataURL: safeSVG,
		Heading:           "Data Gateway",
		Subtitle:          "Authorized personnel only",
	}

	u, err := st.SetupInitialAdmin("sec_ops", "Security Ops", "Sup3rStrong!Pass", initialBranding)
	if err != nil {
		t.Fatalf("SetupInitialAdmin failed: %v", err)
	}
	if u.Username != "sec_ops" || u.Role != RoleAdmin || !u.PasswordChanged {
		t.Fatalf("unexpected initial admin user: %+v", u)
	}
	if st.NeedsSetup() {
		t.Fatal("expected NeedsSetup() == false after SetupInitialAdmin")
	}

	// Second call to SetupInitialAdmin must be rejected permanently
	if _, err := st.SetupInitialAdmin("attacker", "Attacker", "AnotherPass123!", nil); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("expected ErrAlreadyInitialized on second SetupInitialAdmin, got %v", err)
	}

	gotBranding := st.GetLoginBranding()
	if gotBranding.PageTitle != "Internal Data Console" || gotBranding.IconMode != "custom" || gotBranding.Heading != "Data Gateway" {
		t.Fatalf("unexpected saved branding: %+v", gotBranding)
	}

	// Reject malicious SVG XSS payloads in SaveLoginBranding
	evilSVG := `data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString([]byte(`<svg onload="alert(1)"><script>alert(1)</script></svg>`))
	if _, err := st.SaveLoginBranding(LoginBrandingConfig{
		IconMode:          "custom",
		CustomIconDataURL: evilSVG,
	}); err == nil {
		t.Fatal("expected SVG containing <script>/onload to be rejected")
	}
}

func TestAutomaticSchemaMigrationFromLegacyV1(t *testing.T) {
	legacyDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(legacyDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}

	// Simulate an existing v1.0.0 metadata database (no schema_migrations table, PRAGMA user_version = 0, older columns)
	dbPath := filepath.Join(legacyDir, "dbharbor.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open legacy db failed: %v", err)
	}
	legacyDDL := `
CREATE TABLE users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	created_at TEXT NOT NULL
);
INSERT INTO users (id, username, display_name, password_hash, role, created_at)
VALUES ('u-legacy-1', 'legacy_admin', 'Legacy Admin', '$2a$10$abcdefghijklmnopqrstuv', 'admin', '2026-01-01T00:00:00Z');

CREATE TABLE connections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	file_path TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
INSERT INTO connections (id, name, type, file_path, created_at, updated_at)
VALUES ('c-legacy-1', 'Legacy SQLite', 'sqlite', '/tmp/legacy.db', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`
	if _, err := rawDB.Exec(legacyDDL); err != nil {
		_ = rawDB.Close()
		t.Fatalf("seed legacy schema failed: %v", err)
	}
	_ = rawDB.Close()

	// Open with v1.0.1 Store -> must automatically backup & migrate to LatestSchemaVersion
	st, err := Open(legacyDir, vault, "", "")
	if err != nil {
		t.Fatalf("Open on legacy v1.0.0 DB failed: %v", err)
	}
	defer st.Close()

	ver, err := st.SchemaVersion()
	if err != nil || ver != LatestSchemaVersion {
		t.Fatalf("expected SchemaVersion=%d after auto-upgrade, got ver=%d err=%v", LatestSchemaVersion, ver, err)
	}
	if st.NeedsSetup() {
		t.Fatal("expected NeedsSetup() == false when upgrading existing populated v1.0.0 database")
	}

	// Verify automatic backup file was created under .backups/
	backupEntries, err := os.ReadDir(filepath.Join(legacyDir, ".backups"))
	if err != nil || len(backupEntries) == 0 {
		t.Fatalf("expected automatic pre-upgrade backup in .backups/, got entries=%d err=%v", len(backupEntries), err)
	}
	foundBak := false
	for _, e := range backupEntries {
		if strings.HasSuffix(e.Name(), ".bak") && strings.Contains(e.Name(), "dbharbor.db.") {
			foundBak = true
			break
		}
	}
	if !foundBak {
		t.Fatalf("expected dbharbor.db.*.bak in .backups/")
	}

	// Verify existing user & connection survived migration and new branding table works
	u, err := st.GetUserByID("u-legacy-1")
	if err != nil || u.Username != "legacy_admin" {
		t.Fatalf("expected legacy_admin to be preserved across migration, got user=%+v err=%v", u, err)
	}
	conns, err := st.ListConnections()
	if err != nil || len(conns) != 1 || conns[0].Name != "Legacy SQLite" {
		t.Fatalf("expected Legacy SQLite connection to be preserved, got conns=%+v err=%v", conns, err)
	}
	if _, err := st.SaveLoginBranding(LoginBrandingConfig{
		PageTitle: "Upgraded Portal",
		IconMode:  "database",
		Heading:   "Enterprise Data",
	}); err != nil {
		t.Fatalf("expected system_settings branding save to work after migration: %v", err)
	}
}
