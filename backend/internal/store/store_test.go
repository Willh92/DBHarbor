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
	"dbharbor/backend/internal/driver"
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

func TestConnectionRememberPasswordPersistenceAndWipe(t *testing.T) {
	// 1. URI credential helpers (standard + special characters)
	rawURI := "mongodb://app_user:p%40ss%3Aw%23rd@mongo.internal:27017/orders?authSource=admin"
	stripped := StripPasswordFromURI(rawURI)
	if strings.Contains(stripped, "p%40ss") || strings.Contains(stripped, "w%23rd") {
		t.Fatalf("expected StripPasswordFromURI to remove password, got %q", stripped)
	}
	if gotUser := ExtractUsernameFromURI(stripped); gotUser != "app_user" {
		t.Fatalf("expected ExtractUsernameFromURI=%q, got %q", "app_user", gotUser)
	}
	reinjected := InjectCredentialsIntoURI(stripped, "app_user", "p@ss:w#rd")
	if !strings.Contains(reinjected, "app_user:") || StripPasswordFromURI(reinjected) != stripped {
		t.Fatalf("unexpected InjectCredentialsIntoURI round-trip: %q", reinjected)
	}

	// Fallback unescaped special char password in URI
	unescapedURI := "mongodb://root:raw@pass:123@127.0.0.1:27017/admin"
	strippedUnescaped := StripPasswordFromURI(unescapedURI)
	if strings.Contains(strippedUnescaped, "raw@pass") {
		t.Fatalf("expected fallback StripPasswordFromURI to scrub unescaped password, got %q", strippedUnescaped)
	}

	// 2. Store persistence when RememberPassword == false (zero secret on disk)
	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir: %v", err)
	}
	st, err := Open(tmpDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()

	savedUnremembered, err := st.InsertConnectionWithMeta(driver.ConnectionConfig{
		ID:               "conn-no-remember",
		Name:             "Prod MySQL (Session Only)",
		Type:             "mysql",
		Host:             "10.0.0.10",
		Port:             3306,
		Username:         "dba",
		Password:         "TopSecret!999",
		Database:         "app",
		RememberPassword: driver.BoolPtr(false),
	}, "u-1", "private")
	if err != nil {
		t.Fatalf("InsertConnectionWithMeta(unremembered): %v", err)
	}
	if savedUnremembered.RememberPassword {
		t.Fatalf("expected RememberPassword=false on SavedConnection, got true")
	}

	var rawPassEnc, rawURIEnc string
	var rawRememberInt int
	if err := st.db.QueryRow(`SELECT password_enc, uri_enc, remember_password FROM connections WHERE id = ?`, "conn-no-remember").
		Scan(&rawPassEnc, &rawURIEnc, &rawRememberInt); err != nil {
		t.Fatalf("query raw connection row: %v", err)
	}
	if rawPassEnc != "" || rawRememberInt != 0 {
		t.Fatalf("expected zero password_enc on disk and remember_password=0, got password_enc=%q remember_password=%d", rawPassEnc, rawRememberInt)
	}
	decUnremembered, err := st.GetDecryptedConnection("conn-no-remember")
	if err != nil {
		t.Fatalf("GetDecryptedConnection(unremembered): %v", err)
	}
	if decUnremembered.Password != "" || decUnremembered.ShouldRememberPassword() {
		t.Fatalf("expected decrypted unremembered config to have empty password and ShouldRememberPassword()=false, got %+v", decUnremembered)
	}

	// 3. Store persistence when RememberPassword == true -> encrypted on disk, then turning OFF wipes disk secret!
	savedRemembered, err := st.InsertConnectionWithMeta(driver.ConnectionConfig{
		ID:               "conn-mongo-remember",
		Name:             "Analytics Mongo",
		Type:             "mongodb",
		Host:             "10.0.0.20",
		Port:             27017,
		Username:         "mongo_admin",
		Password:         "MongoSecret!888",
		URI:              "mongodb://mongo_admin:MongoSecret!888@10.0.0.20:27017/analytics",
		RememberPassword: driver.BoolPtr(true),
	}, "u-1", "private")
	if err != nil {
		t.Fatalf("InsertConnectionWithMeta(remembered): %v", err)
	}
	if !savedRemembered.RememberPassword {
		t.Fatalf("expected RememberPassword=true on SavedConnection")
	}
	decRemembered, err := st.GetDecryptedConnection("conn-mongo-remember")
	if err != nil {
		t.Fatalf("GetDecryptedConnection(remembered): %v", err)
	}
	if decRemembered.Password != "MongoSecret!888" || !strings.Contains(decRemembered.URI, "MongoSecret!888") {
		t.Fatalf("expected remembered password & URI to decrypt intact, got pass=%q uri=%q", decRemembered.Password, decRemembered.URI)
	}

	// Edit existing remembered connection to turn OFF RememberPassword without re-typing password (keepExistingSecret=true)
	toggledOff, err := st.SaveConnectionWithMeta(driver.ConnectionConfig{
		ID:               "conn-mongo-remember",
		Name:             "Analytics Mongo",
		Type:             "mongodb",
		Host:             "10.0.0.20",
		Port:             27017,
		Username:         "mongo_admin",
		Password:         "", // left blank while unchecking Remember password
		URI:              "",
		RememberPassword: driver.BoolPtr(false),
	}, true, "u-1", "private")
	if err != nil {
		t.Fatalf("SaveConnectionWithMeta(turn off remember): %v", err)
	}
	if toggledOff.RememberPassword {
		t.Fatalf("expected RememberPassword=false after turning off remember")
	}
	if err := st.db.QueryRow(`SELECT password_enc, remember_password FROM connections WHERE id = ?`, "conn-mongo-remember").
		Scan(&rawPassEnc, &rawRememberInt); err != nil {
		t.Fatalf("query raw toggled connection row: %v", err)
	}
	if rawPassEnc != "" || rawRememberInt != 0 {
		t.Fatalf("expected turning off RememberPassword to wipe password_enc on disk immediately, got password_enc=%q remember=%d", rawPassEnc, rawRememberInt)
	}
	decToggled, err := st.GetDecryptedConnection("conn-mongo-remember")
	if err != nil {
		t.Fatalf("GetDecryptedConnection(toggled off): %v", err)
	}
	if decToggled.Password != "" || strings.Contains(decToggled.URI, "MongoSecret!888") {
		t.Fatalf("expected wiped password & stripped URI after turning off remember, got pass=%q uri=%q", decToggled.Password, decToggled.URI)
	}

	// 4. ImportConnections defaults credentialed drivers to RememberPassword=false and file drivers to true
	if _, ok := driver.GetMeta("postgres"); !ok {
		driver.Register(driver.DriverMeta{Type: "postgres", Name: "PostgreSQL"}, nil)
	}
	if _, ok := driver.GetMeta("sqlite"); !ok {
		driver.Register(driver.DriverMeta{Type: "sqlite", Name: "SQLite"}, nil)
	}
	imported, err := st.ImportConnections([]ExportedConnectionProfile{
		{Name: "Imported PG", Type: "postgres", Host: "10.0.0.30", Port: 5432, Username: "pguser"},
		{Name: "Imported SQLite", Type: "sqlite", FilePath: filepath.Join(tmpDir, "local.db")},
	}, UserPrincipal{ID: "u-1", Role: RoleAdmin})
	if err != nil || imported != 2 {
		t.Fatalf("ImportConnections failed: imported=%d err=%v", imported, err)
	}
	allConns, err := st.ListConnections()
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	var foundPG, foundSQLite bool
	for _, c := range allConns {
		if c.Name == "Imported PG" {
			foundPG = true
			if c.RememberPassword {
				t.Fatalf("expected Imported PG to default to RememberPassword=false")
			}
		}
		if c.Name == "Imported SQLite" {
			foundSQLite = true
			if !c.RememberPassword {
				t.Fatalf("expected Imported SQLite (passwordless file engine) to have RememberPassword=true")
			}
		}
	}
	if !foundPG || !foundSQLite {
		t.Fatalf("expected both imported profiles in ListConnections")
	}
}

func TestURICredentialHelpersSyncAndCrossOwnerDuplicateScrub(t *testing.T) {
	if got := ExtractPasswordFromURI("mongodb://alice:S3cr3t%21@10.0.0.5:27017/app"); got != "S3cr3t!" {
		t.Fatalf("ExtractPasswordFromURI escaped = %q, want %q", got, "S3cr3t!")
	}
	if got := ExtractPasswordFromURI("mongodb://alice@10.0.0.5:27017/app"); got != "" {
		t.Fatalf("ExtractPasswordFromURI passwordless = %q, want empty", got)
	}
	if got := ExtractPasswordFromURI("mongodb://alice:raw@pass@10.0.0.5:27017/app"); got != "raw@pass" {
		t.Fatalf("ExtractPasswordFromURI fallback = %q, want %q", got, "raw@pass")
	}

	// InjectCredentialsIntoURI retries StripPasswordFromURI on malformed unescaped userinfo and strips stale :oldpass when empty
	injectedFallback := InjectCredentialsIntoURI("mongodb://alice:old@pass@10.0.0.5:27017/app", "alice", "newpass")
	if injectedFallback != "mongodb://alice:newpass@10.0.0.5:27017/app" {
		t.Fatalf("InjectCredentialsIntoURI fallback = %q", injectedFallback)
	}
	strippedNoUser := InjectCredentialsIntoURI("mongodb://:oldpass@10.0.0.5:27017/app", "", "")
	if strippedNoUser != "mongodb://10.0.0.5:27017/app" {
		t.Fatalf("InjectCredentialsIntoURI empty user/pass = %q, want stale password stripped", strippedNoUser)
	}

	tmpDir := t.TempDir()
	vault, err := crypto.NewVaultFromDataDir(tmpDir, "test-uri-sync-key")
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(tmpDir, vault, "admin", "AdminPass123!")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, ok := driver.GetMeta("mongodb"); !ok {
		driver.Register(driver.DriverMeta{Type: "mongodb", Name: "MongoDB"}, nil)
	}

	// SaveConnectionWithMeta keeps oldURIEnc in sync when cfg.URI == "" && keepExistingSecret && password/username changes
	saved, err := st.SaveConnectionWithMeta(driver.ConnectionConfig{
		Name:             "Synced Mongo",
		Type:             "mongodb",
		Username:         "alice",
		Password:         "PassV1",
		URI:              "mongodb://alice:PassV1@10.0.0.9:27017/prod?authSource=admin",
		RememberPassword: driver.BoolPtr(true),
	}, false, "user-owner", "private")
	if err != nil {
		t.Fatalf("initial SaveConnectionWithMeta: %v", err)
	}

	_, err = st.SaveConnectionWithMeta(driver.ConnectionConfig{
		ID:               saved.ID,
		Name:             "Synced Mongo",
		Type:             "mongodb",
		Username:         "bob",
		Password:         "PassV2",
		URI:              "",
		RememberPassword: driver.BoolPtr(true),
	}, true, "user-owner", "private")
	if err != nil {
		t.Fatalf("update SaveConnectionWithMeta: %v", err)
	}

	dec, err := st.GetDecryptedConnection(saved.ID)
	if err != nil {
		t.Fatalf("GetDecryptedConnection: %v", err)
	}
	if dec.Password != "PassV2" || !strings.Contains(dec.URI, "bob:PassV2@10.0.0.9:27017") {
		t.Fatalf("expected decrypted URI to stay synced with updated username/password, got pass=%q uri=%q", dec.Password, dec.URI)
	}

	// Cross-owner DuplicateConnection (e.g. Admin duplicating another user's private connection) must scrub secrets
	dup, err := st.DuplicateConnection(saved.ID, UserPrincipal{
		ID:            "admin-other",
		Username:      "admin",
		Role:          RoleAdmin,
		CanCreateConn: true,
	})
	if err != nil {
		t.Fatalf("DuplicateConnection cross-owner: %v", err)
	}
	if dup.RememberPassword {
		t.Fatalf("expected cross-owner duplicated credentialed connection to force RememberPassword=false")
	}
	dupDec, err := st.GetDecryptedConnection(dup.ID)
	if err != nil {
		t.Fatalf("GetDecryptedConnection(dup): %v", err)
	}
	if dupDec.Password != "" || strings.Contains(dupDec.URI, "PassV2") {
		t.Fatalf("expected cross-owner duplicate to scrub private secret, got pass=%q uri=%q", dupDec.Password, dupDec.URI)
	}
}
