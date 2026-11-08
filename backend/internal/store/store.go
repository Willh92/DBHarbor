package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	RoleAdmin     = "admin"
	RoleReadWrite = "readwrite"
	RoleReadOnly  = "readonly"
)

type User struct {
	ID                  string   `json:"id"`
	Username            string   `json:"username"`
	DisplayName         string   `json:"displayName"`
	PasswordHash        string   `json:"-"`
	Role                string   `json:"role"`
	Disabled            bool     `json:"disabled"`
	CanCreateConn       bool     `json:"canCreateConn"`
	ConnScope           string   `json:"connScope"` // "all" or "selected"
	AllowedConnIDs      []string `json:"allowedConnIds"`
	TOTPEnabled         bool     `json:"totpEnabled"`
	PasswordChanged     bool     `json:"passwordChanged"`
	ActiveSessionsCount int      `json:"activeSessionsCount"`
	CreatedAt           string   `json:"createdAt"`
}

type UserPrincipal struct {
	ID              string
	Username        string
	DisplayName     string
	Role            string
	Disabled        bool
	PasswordChanged bool
	CanCreateConn   bool
	ConnScopeAll    bool
	AllowedConnIDs  map[string]struct{}
}

type SecurityStatus struct {
	UserID                 string `json:"userId"`
	Username               string `json:"username"`
	DisplayName            string `json:"displayName"`
	Role                   string `json:"role"`
	CanCreateConn          bool   `json:"canCreateConn"`
	TOTPEnabled            bool   `json:"totpEnabled"`
	RecoveryCodesRemaining int    `json:"recoveryCodesRemaining"`
	PasswordChanged        bool   `json:"passwordChanged"`
}

type AuthSession struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	Current    bool   `json:"current"`
}

type AuditLogEntry struct {
	ID        string `json:"id"`
	EventType string `json:"eventType"`
	Event     string `json:"event"`
	Username  string `json:"username"`
	IP        string `json:"ip"`
	UserAgent string `json:"userAgent"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"createdAt"`
}

type AuditLogFilter struct {
	Username  string
	EventType string
	Keyword   string
	Limit     int
}

type SavedConnection struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Color            string            `json:"color"`
	Host             string            `json:"host"`
	Port             int               `json:"port"`
	Username         string            `json:"username"`
	HasPassword      bool              `json:"hasPassword"`
	RememberPassword bool              `json:"rememberPassword"`
	Unlocked         bool              `json:"unlocked"`
	Database         string            `json:"database"`
	FilePath         string            `json:"filePath"`
	HasURI           bool              `json:"hasUri"`
	Options          map[string]string `json:"options"`
	OwnerID          string            `json:"ownerId,omitempty"`
	Visibility       string            `json:"visibility"`
	Editable         bool              `json:"editable"`
	Ephemeral        bool              `json:"ephemeral,omitempty"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
}

func IsEphemeralConnection(opts map[string]string) bool {
	return opts != nil && opts["__ephemeral"] == "1"
}

type HistoryEntry struct {
	ID           string `json:"id"`
	UserID       string `json:"userId,omitempty"`
	ConnectionID string `json:"connectionId"`
	ConnName     string `json:"connName"`
	DBType       string `json:"dbType"`
	Database     string `json:"database"`
	Query        string `json:"query"`
	ElapsedMs    int64  `json:"elapsedMs"`
	Status       string `json:"status"`
	ErrorMsg     string `json:"errorMsg,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type SavedSnippet struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	DBType       string `json:"dbType"`
	ConnectionID string `json:"connectionId,omitempty"`
	Database     string `json:"database,omitempty"`
	SQL          string `json:"sql"`
	OwnerID      string `json:"ownerId,omitempty"`
	OwnerName    string `json:"ownerName,omitempty"`
	IsShared     bool   `json:"isShared"`
	Editable     bool   `json:"editable"`
	CreatedAt    string `json:"createdAt"`
}

type LoginBrandingConfig struct {
	PageTitle         string `json:"pageTitle"`
	IconMode          string `json:"iconMode"`          // "shield" | "database" | "brand" | "custom" | "none"
	CustomIconDataURL string `json:"customIconDataUrl"` // validated data:image/(png|jpeg|webp|gif|svg+xml);base64,...
	Heading           string `json:"heading"`
	Subtitle          string `json:"subtitle"`
}

func DefaultLoginBranding() LoginBrandingConfig {
	return LoginBrandingConfig{
		PageTitle:         "",
		IconMode:          "shield",
		CustomIconDataURL: "",
		Heading:           "",
		Subtitle:          "",
	}
}

type Store struct {
	dataDir     string
	db          *sql.DB
	vault       *crypto.Vault
	dummyHash   []byte
	histCounter atomic.Uint64
	auditCnt    atomic.Uint64

	setupMu     sync.Mutex
	initialized atomic.Bool

	brandingMu sync.RWMutex
	branding   LoginBrandingConfig

	sessMu         sync.RWMutex
	activeSessions map[string]int64 // sessionID -> expiresAt unix

	userMu     sync.RWMutex
	principals map[string]UserPrincipal // userID -> live principal

	maintMu   sync.RWMutex
	maintHook func()

	stopOnce sync.Once
	stopCh   chan struct{}
}

func (s *Store) SetMaintenanceHook(fn func()) {
	s.maintMu.Lock()
	s.maintHook = fn
	s.maintMu.Unlock()
}

func (s *Store) DataDir() string {
	return s.dataDir
}

func Open(dataDir string, vault *crypto.Vault, adminUser, adminPass string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "dbharbor.db")
	dsn := filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-4000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open metadata sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	// Precomputed cost-10 bcrypt hash used for constant-time anti-enumeration password checks on unknown usernames.
	dummy := []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

	s := &Store{
		dataDir:        dataDir,
		db:             db,
		vault:          vault,
		dummyHash:      dummy,
		branding:       DefaultLoginBranding(),
		activeSessions: make(map[string]int64),
		principals:     make(map[string]UserPrincipal),
		stopCh:         make(chan struct{}),
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(dbPath, 0o600)
	_ = os.Chmod(dbPath+"-wal", 0o600)
	_ = os.Chmod(dbPath+"-shm", 0o600)
	if err := s.ensureAdmin(adminUser, adminPass); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.hydrateLoginBranding()
	s.hydrateUserPrincipals()
	s.hydrateActiveSessions()
	_ = s.ensureSampleConnection(dataDir)
	go s.maintenanceLoop()
	return s, nil
}

// OpenLocal opens the metadata store in pure local desktop mode without creating any user accounts or bcrypt dummy hashes.
func OpenLocal(dataDir string, vault *crypto.Vault) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "dbharbor.db")
	dsn := filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-4000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open metadata sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{
		dataDir:        dataDir,
		db:             db,
		vault:          vault,
		branding:       DefaultLoginBranding(),
		activeSessions: make(map[string]int64),
		principals:     make(map[string]UserPrincipal),
		stopCh:         make(chan struct{}),
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.initialized.Store(true)
	s.hydrateLoginBranding()
	_ = s.ensureSampleConnection(dataDir)
	go s.maintenanceLoop()
	return s, nil
}

func (s *Store) Close() error {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	return s.db.Close()
}

func NormalizeAndValidateLoginBranding(in LoginBrandingConfig) (LoginBrandingConfig, error) {
	out := LoginBrandingConfig{
		PageTitle:         strings.TrimSpace(in.PageTitle),
		IconMode:          strings.ToLower(strings.TrimSpace(in.IconMode)),
		CustomIconDataURL: strings.TrimSpace(in.CustomIconDataURL),
		Heading:           strings.TrimSpace(in.Heading),
		Subtitle:          strings.TrimSpace(in.Subtitle),
	}
	if utf8.RuneCountInString(out.PageTitle) > 80 {
		return LoginBrandingConfig{}, errors.New("browser page title must be at most 80 characters")
	}
	if utf8.RuneCountInString(out.Heading) > 80 {
		return LoginBrandingConfig{}, errors.New("login heading must be at most 80 characters")
	}
	if utf8.RuneCountInString(out.Subtitle) > 160 {
		return LoginBrandingConfig{}, errors.New("login subtitle must be at most 160 characters")
	}
	switch out.IconMode {
	case "", "shield":
		out.IconMode = "shield"
	case "database", "brand", "custom", "none":
	default:
		return LoginBrandingConfig{}, errors.New("invalid login icon mode")
	}

	if out.CustomIconDataURL != "" {
		if len(out.CustomIconDataURL) > 200_000 {
			return LoginBrandingConfig{}, errors.New("custom icon image exceeds maximum size (128 KB)")
		}
		lower := strings.ToLower(out.CustomIconDataURL)
		allowedPrefixes := []string{
			"data:image/png;base64,",
			"data:image/jpeg;base64,",
			"data:image/jpg;base64,",
			"data:image/webp;base64,",
			"data:image/gif;base64,",
			"data:image/svg+xml;base64,",
			"data:image/x-icon;base64,",
		}
		matchedPrefix := ""
		for _, pfx := range allowedPrefixes {
			if strings.HasPrefix(lower, pfx) {
				matchedPrefix = pfx
				break
			}
		}
		if matchedPrefix == "" {
			return LoginBrandingConfig{}, errors.New("custom icon must be a valid PNG, JPEG, WebP, GIF, ICO, or SVG image")
		}
		rawB64 := out.CustomIconDataURL[len(matchedPrefix):]
		decoded, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil || len(decoded) == 0 {
			return LoginBrandingConfig{}, errors.New("invalid base64 image payload for custom icon")
		}
		if strings.Contains(matchedPrefix, "svg+xml") {
			svgLower := strings.ToLower(string(decoded))
			if strings.Contains(svgLower, "<script") ||
				strings.Contains(svgLower, "javascript:") ||
				strings.Contains(svgLower, "onload=") ||
				strings.Contains(svgLower, "onerror=") ||
				strings.Contains(svgLower, "<foreignobject") {
				return LoginBrandingConfig{}, errors.New("SVG custom icon contains disallowed active script elements")
			}
		}
	} else if out.IconMode == "custom" {
		return LoginBrandingConfig{}, errors.New("please upload a custom icon image or select another icon style")
	}

	return out, nil
}

func (s *Store) hydrateLoginBranding() {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = 'login_branding'`).Scan(&raw)
	if err != nil || strings.TrimSpace(raw) == "" {
		return
	}
	var parsed LoginBrandingConfig
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		if valid, vErr := NormalizeAndValidateLoginBranding(parsed); vErr == nil {
			s.brandingMu.Lock()
			s.branding = valid
			s.brandingMu.Unlock()
		}
	}
}

func (s *Store) GetLoginBranding() LoginBrandingConfig {
	s.brandingMu.RLock()
	defer s.brandingMu.RUnlock()
	return s.branding
}

func (s *Store) SaveLoginBranding(in LoginBrandingConfig) (LoginBrandingConfig, error) {
	clean, err := NormalizeAndValidateLoginBranding(in)
	if err != nil {
		return LoginBrandingConfig{}, err
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return LoginBrandingConfig{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(
		`INSERT INTO system_settings (key, value, updated_at) VALUES ('login_branding', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		string(encoded), now,
	); err != nil {
		return LoginBrandingConfig{}, err
	}
	s.brandingMu.Lock()
	s.branding = clean
	s.brandingMu.Unlock()
	return clean, nil
}

func (s *Store) hydrateActiveSessions() {
	now := time.Now().Unix()
	_, _ = s.db.Exec(`DELETE FROM auth_sessions WHERE expires_at < ?`, now)
	rows, err := s.db.Query(`SELECT id, expires_at FROM auth_sessions WHERE revoked = 0 AND expires_at > ?`, now)
	if err != nil {
		return
	}
	defer rows.Close()
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	for rows.Next() {
		var id string
		var exp int64
		if rows.Scan(&id, &exp) == nil {
			s.activeSessions[id] = exp
		}
	}
}

func (s *Store) ensureAdmin(username, password string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		s.initialized.Store(true)
		return nil
	}
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	// No CLI/env bootstrap credentials supplied: defer administrator creation to Web Initial Setup
	if username == "" && password == "" {
		s.initialized.Store(false)
		return nil
	}
	if password == "" {
		return errors.New("initial admin password (-admin-pass or DBHARBOR_ADMIN_PASS) is required when admin username is specified")
	}
	if username == "" {
		username = "admin"
	}
	if !validUsernameRe.MatchString(username) {
		return errors.New("admin username must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'")
	}
	if len(password) < 8 {
		return errors.New("initial admin password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	pwdChangedInt := 1
	if password == "admin123" {
		pwdChangedInt = 0
	}
	_, err = s.db.Exec(
		`INSERT INTO users (id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, password_changed, created_at) VALUES (?, ?, ?, ?, ?, 0, 1, 'all', ?, ?)`,
		crypto.NewUUID(), username, "", string(hash), RoleAdmin, pwdChangedInt, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	s.initialized.Store(true)
	return nil
}

func (s *Store) ensureSampleConnection(dataDir string) error {
	localDBDir := filepath.Join(dataDir, "local_databases")
	_ = os.MkdirAll(localDBDir, 0o755)
	samplePath := filepath.Join(localDBDir, "sample_playground.sqlite")
	legacySamplePath := filepath.Join(dataDir, "sample_playground.sqlite")

	// Migrate legacy sample_playground.sqlite out of dataDir root into local_databases/
	if _, err := os.Stat(legacySamplePath); err == nil {
		if _, destErr := os.Stat(samplePath); os.IsNotExist(destErr) {
			if renErr := os.Rename(legacySamplePath, samplePath); renErr == nil {
				_, _ = s.db.Exec(`UPDATE connections SET file_path = ? WHERE file_path = ?`, samplePath, legacySamplePath)
			}
		}
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM connections`).Scan(&count); err != nil || count > 0 {
		return err
	}
	sampleDSN := filepath.ToSlash(samplePath)
	sdb, err := sql.Open("sqlite", sampleDSN)
	if err != nil {
		return err
	}
	defer sdb.Close()

	_, _ = sdb.Exec(`
CREATE TABLE IF NOT EXISTS customers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	email TEXT NOT NULL UNIQUE,
	tier TEXT NOT NULL DEFAULT 'PRO',
	balance REAL NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	customer_id INTEGER NOT NULL,
	product TEXT NOT NULL,
	amount REAL NOT NULL,
	status TEXT NOT NULL DEFAULT 'PAID',
	ordered_at TEXT NOT NULL,
	FOREIGN KEY(customer_id) REFERENCES customers(id)
);
CREATE VIEW IF NOT EXISTS v_customer_spending AS
SELECT c.id, c.name, c.tier, COUNT(o.id) AS order_count, COALESCE(SUM(o.amount), 0) AS total_spent
FROM customers c LEFT JOIN orders o ON c.id = o.customer_id
GROUP BY c.id, c.name, c.tier;
`)
	var cCount int
	_ = sdb.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&cCount)
	if cCount == 0 {
		now := time.Now().Format("2006-01-02 15:04:05")
		_, _ = sdb.Exec(`INSERT INTO customers (name, email, tier, balance, created_at) VALUES
			('StarSea Tech', 'ops@starsea.io', 'ENTERPRISE', 128500.50, ?),
			('CloudSail Data', 'dba@cloudsail.io', 'PRO', 34200.00, ?),
			('Aurora Lab', 'dev@auroralab.org', 'STARTER', 5600.80, ?)`, now, now, now)
		_, _ = sdb.Exec(`INSERT INTO orders (customer_id, product, amount, status, ordered_at) VALUES
			(1, 'ClickHouse Enterprise Cluster', 68000.00, 'PAID', ?),
			(1, 'PostgreSQL HA Add-on', 19800.00, 'PAID', ?),
			(2, 'Redis Memory Grid', 12500.00, 'PAID', ?),
			(3, 'SQLite Edge Sync', 1999.00, 'PENDING', ?)`, now, now, now, now)
	}

	_, err = s.SaveConnection(driver.ConnectionConfig{
		Name:     "Sample Playground (Local SQLite)",
		Type:     "sqlite",
		Color:    "#13c2c2",
		FilePath: samplePath,
	}, false)
	return err
}

func (s *Store) Authenticate(username, password string) (*User, error) {
	var u User
	var totpInt, pwdChangedInt, disabledInt, canCreateInt int
	var rawScope string
	err := s.db.QueryRow(
		`SELECT id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, totp_enabled, password_changed, created_at FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &disabledInt, &canCreateInt, &rawScope, &totpInt, &pwdChangedInt, &u.CreatedAt)
	if err != nil {
		// Equalize timing against username enumeration
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("invalid username or password")
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, errors.New("invalid username or password")
	}
	if disabledInt == 1 {
		return nil, errors.New("account has been disabled; please contact your administrator")
	}
	u.Role = NormalizeRole(u.Role)
	u.Disabled = false
	u.CanCreateConn = u.Role == RoleAdmin || canCreateInt == 1
	u.ConnScope, u.AllowedConnIDs = parseConnScope(rawScope)
	u.TOTPEnabled = totpInt == 1
	u.PasswordChanged = pwdChangedInt == 1
	return &u, nil
}

func (s *Store) GetUserByID(userID string) (*User, error) {
	var u User
	var totpInt, pwdChangedInt, disabledInt, canCreateInt int
	var rawScope string
	err := s.db.QueryRow(
		`SELECT id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, totp_enabled, password_changed, created_at FROM users WHERE id = ?`,
		userID,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &disabledInt, &canCreateInt, &rawScope, &totpInt, &pwdChangedInt, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.Role = NormalizeRole(u.Role)
	u.Disabled = disabledInt == 1
	u.CanCreateConn = u.Role == RoleAdmin || canCreateInt == 1
	u.ConnScope, u.AllowedConnIDs = parseConnScope(rawScope)
	u.TOTPEnabled = totpInt == 1
	u.PasswordChanged = pwdChangedInt == 1
	return &u, nil
}

func (s *Store) GetSecurityStatus(userID string) (SecurityStatus, error) {
	var st SecurityStatus
	var totpInt, pwdChangedInt, canCreateInt int
	var recJSON string
	err := s.db.QueryRow(
		`SELECT id, username, display_name, role, can_create_conn, totp_enabled, recovery_hashes_json, password_changed FROM users WHERE id = ?`,
		userID,
	).Scan(&st.UserID, &st.Username, &st.DisplayName, &st.Role, &canCreateInt, &totpInt, &recJSON, &pwdChangedInt)
	if err != nil {
		return st, err
	}
	st.Role = NormalizeRole(st.Role)
	st.CanCreateConn = st.Role == RoleAdmin || canCreateInt == 1
	st.TOTPEnabled = totpInt == 1
	st.PasswordChanged = pwdChangedInt == 1
	var hashes []string
	_ = json.Unmarshal([]byte(recJSON), &hashes)
	st.RecoveryCodesRemaining = len(hashes)
	return st, nil
}

func (s *Store) UpdatePassword(userID, oldPass, newPass string) error {
	if len(newPass) < 6 {
		return errors.New("new password must be at least 6 characters")
	}
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return errors.New("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPass)); err != nil {
		return errors.New("current password is incorrect")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed = 1 WHERE id = ?`, string(newHash), userID); err != nil {
		return err
	}
	s.refreshPrincipal(userID)
	return nil
}

// --- 2FA (TOTP + Backup Recovery Codes) ---

func (s *Store) BeginTOTPSetup(userID string) (secret, otpauthURI, qrDataURL string, err error) {
	var username string
	if err := s.db.QueryRow(`SELECT username FROM users WHERE id = ?`, userID).Scan(&username); err != nil {
		return "", "", "", errors.New("user not found")
	}
	secret, err = auth.GenerateTOTPSecret()
	if err != nil {
		return "", "", "", err
	}
	enc, err := s.vault.Encrypt(secret)
	if err != nil {
		return "", "", "", err
	}
	if _, err := s.db.Exec(`UPDATE users SET totp_pending_enc = ? WHERE id = ?`, enc, userID); err != nil {
		return "", "", "", err
	}
	otpauthURI = auth.BuildOTPAuthURI("DBHarbor", username, secret)
	qrDataURL, err = auth.GenerateQRCodeDataURL(otpauthURI)
	if err != nil {
		return "", "", "", err
	}
	return secret, otpauthURI, qrDataURL, nil
}

func (s *Store) ConfirmTOTPSetup(userID, code string) ([]string, error) {
	var pendingEnc string
	if err := s.db.QueryRow(`SELECT totp_pending_enc FROM users WHERE id = ?`, userID).Scan(&pendingEnc); err != nil {
		return nil, errors.New("user not found")
	}
	if pendingEnc == "" {
		return nil, errors.New("please initiate 2FA setup first")
	}
	secret, err := s.vault.Decrypt(pendingEnc)
	if err != nil {
		return nil, errors.New("failed to decrypt pending 2FA secret")
	}
	step, ok := auth.VerifyTOTP(secret, code, time.Now(), 0)
	if !ok {
		return nil, errors.New("invalid or expired 6-digit verification code; please verify your device clock")
	}
	plainCodes, hashedCodes, err := auth.GenerateRecoveryCodes(8)
	if err != nil {
		return nil, err
	}
	recBytes, _ := json.Marshal(hashedCodes)
	_, err = s.db.Exec(`
		UPDATE users
		SET totp_enabled = 1, totp_secret_enc = ?, totp_pending_enc = '', recovery_hashes_json = ?, last_totp_step = ?
		WHERE id = ?
	`, pendingEnc, string(recBytes), step, userID)
	if err != nil {
		return nil, err
	}
	return plainCodes, nil
}

func (s *Store) Verify2FA(userID, inputCode string) (usedRecovery bool, err error) {
	var totpInt int
	var secEnc, recJSON string
	var lastStep int64
	err = s.db.QueryRow(`
		SELECT totp_enabled, totp_secret_enc, recovery_hashes_json, last_totp_step
		FROM users WHERE id = ?
	`, userID).Scan(&totpInt, &secEnc, &recJSON, &lastStep)
	if err != nil {
		return false, errors.New("user not found")
	}
	if totpInt != 1 || secEnc == "" {
		return false, errors.New("two-factor authentication is not enabled for this account")
	}

	secret, err := s.vault.Decrypt(secEnc)
	if err != nil {
		return false, errors.New("failed to decrypt 2FA secret")
	}

	clean := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(inputCode), "-", ""), " ", "")
	// Try 6-digit TOTP first
	if len(clean) == 6 {
		if matchedStep, ok := auth.VerifyTOTP(secret, clean, time.Now(), lastStep); ok {
			_, _ = s.db.Exec(`UPDATE users SET last_totp_step = ? WHERE id = ?`, matchedStep, userID)
			return false, nil
		}
	}

	// Fallback: check 8-char backup recovery code
	if len(clean) >= 8 {
		targetHash := auth.HashRecoveryCode(clean)
		var hashes []string
		_ = json.Unmarshal([]byte(recJSON), &hashes)
		matchedIdx := -1
		for i, h := range hashes {
			if h == targetHash {
				matchedIdx = i
				break
			}
		}
		if matchedIdx >= 0 {
			remaining := append(hashes[:matchedIdx], hashes[matchedIdx+1:]...)
			remBytes, _ := json.Marshal(remaining)
			_, _ = s.db.Exec(`UPDATE users SET recovery_hashes_json = ? WHERE id = ?`, string(remBytes), userID)
			return true, nil
		}
	}

	return false, errors.New("invalid two-factor verification code or backup recovery code")
}

func (s *Store) DisableTOTP(userID, password, code string) error {
	var pwdHash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&pwdHash); err != nil {
		return errors.New("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(pwdHash), []byte(password)); err != nil {
		return errors.New("current password is incorrect")
	}
	if _, err := s.Verify2FA(userID, code); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		UPDATE users
		SET totp_enabled = 0, totp_secret_enc = '', totp_pending_enc = '', recovery_hashes_json = '[]', last_totp_step = 0
		WHERE id = ?
	`, userID)
	return err
}

func (s *Store) RegenerateRecoveryCodes(userID, password, code string) ([]string, error) {
	var pwdHash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&pwdHash); err != nil {
		return nil, errors.New("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(pwdHash), []byte(password)); err != nil {
		return nil, errors.New("current password is incorrect")
	}
	if _, err := s.Verify2FA(userID, code); err != nil {
		return nil, err
	}
	plainCodes, hashedCodes, err := auth.GenerateRecoveryCodes(8)
	if err != nil {
		return nil, err
	}
	recBytes, _ := json.Marshal(hashedCodes)
	if _, err := s.db.Exec(`UPDATE users SET recovery_hashes_json = ? WHERE id = ?`, string(recBytes), userID); err != nil {
		return nil, err
	}
	return plainCodes, nil
}

// --- Revocable Active Sessions ---

func (s *Store) CreateSession(userID, ip, userAgent string, ttl time.Duration) (AuthSession, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	exp := now.Add(ttl).Unix()
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	sess := AuthSession{
		ID:         crypto.NewUUID(),
		UserID:     userID,
		IP:         ip,
		UserAgent:  userAgent,
		CreatedAt:  nowStr,
		LastSeenAt: nowStr,
		ExpiresAt:  exp,
	}
	_, err := s.db.Exec(`
		INSERT INTO auth_sessions (id, user_id, ip, user_agent, created_at, last_seen_at, expires_at, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
	`, sess.ID, sess.UserID, sess.IP, sess.UserAgent, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt)
	if err != nil {
		return sess, err
	}
	s.sessMu.Lock()
	s.activeSessions[sess.ID] = exp
	s.sessMu.Unlock()
	return sess, nil
}

func (s *Store) IsSessionActive(sessionID string) bool {
	if sessionID == "" {
		return true
	}
	now := time.Now().Unix()
	s.sessMu.RLock()
	exp, ok := s.activeSessions[sessionID]
	s.sessMu.RUnlock()
	return ok && exp > now
}

func (s *Store) ListSessions(userID, currentSessionID string) ([]AuthSession, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`
		SELECT id, user_id, ip, user_agent, created_at, last_seen_at, expires_at
		FROM auth_sessions
		WHERE user_id = ? AND revoked = 0 AND expires_at > ?
		ORDER BY created_at DESC LIMIT 50
	`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]AuthSession, 0, 8)
	for rows.Next() {
		var item AuthSession
		if err := rows.Scan(&item.ID, &item.UserID, &item.IP, &item.UserAgent, &item.CreatedAt, &item.LastSeenAt, &item.ExpiresAt); err == nil {
			item.Current = item.ID == currentSessionID
			list = append(list, item)
		}
	}
	return list, nil
}

func (s *Store) RevokeSession(userID, sessionID string) error {
	s.sessMu.Lock()
	delete(s.activeSessions, sessionID)
	s.sessMu.Unlock()
	_, err := s.db.Exec(`UPDATE auth_sessions SET revoked = 1 WHERE id = ? AND user_id = ?`, sessionID, userID)
	return err
}

func (s *Store) RevokeOtherSessions(userID, keepSessionID string) (int, error) {
	ids, err := s.RevokeOtherSessionsWithIDs(userID, keepSessionID)
	return len(ids), err
}

func (s *Store) RevokeOtherSessionsWithIDs(userID, keepSessionID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM auth_sessions WHERE user_id = ? AND id <> ? AND revoked = 0`, userID, keepSessionID)
	var ids []string
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		_ = rows.Close()
	}
	s.sessMu.Lock()
	for _, id := range ids {
		delete(s.activeSessions, id)
	}
	s.sessMu.Unlock()

	_, err = s.db.Exec(`UPDATE auth_sessions SET revoked = 1 WHERE user_id = ? AND id <> ? AND revoked = 0`, userID, keepSessionID)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// --- Security Audit Logs ---

func (s *Store) AddAuditLog(event, username, ip, userAgent, detail string) {
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	_, _ = s.db.Exec(`
		INSERT INTO security_audit_logs (id, event, username, ip, user_agent, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, crypto.NewUUID(), event, username, ip, userAgent, detail, time.Now().UTC().Format(time.RFC3339))

	if s.auditCnt.Add(1)%64 == 0 {
		_, _ = s.db.Exec(`
			DELETE FROM security_audit_logs
			WHERE created_at < COALESCE(
				(SELECT created_at FROM security_audit_logs ORDER BY created_at DESC LIMIT 1 OFFSET 5000),
				''
			)
		`)
	}
}

func (s *Store) ListAuditLogs(limit int) ([]AuditLogEntry, error) {
	return s.ListAuditLogsFiltered(AuditLogFilter{Limit: limit})
}

func (s *Store) ListAuditLogsFiltered(filter AuditLogFilter) ([]AuditLogEntry, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 300 {
		limit = 60
	}
	var conds []string
	var args []any
	if u := strings.TrimSpace(filter.Username); u != "" {
		conds = append(conds, "username = ?")
		args = append(args, u)
	}
	if ev := strings.TrimSpace(filter.EventType); ev != "" {
		conds = append(conds, "event = ?")
		args = append(args, ev)
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds, "(username LIKE ? OR event LIKE ? OR ip LIKE ? OR detail LIKE ?)")
		args = append(args, like, like, like, like)
	}
	query := `SELECT id, event, username, ip, user_agent, detail, created_at FROM security_audit_logs`
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]AuditLogEntry, 0, limit)
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Event, &e.Username, &e.IP, &e.UserAgent, &e.Detail, &e.CreatedAt); err == nil {
			e.EventType = e.Event
			list = append(list, e)
		}
	}
	return list, nil
}

// --- Connections, History & Snippets ---

func isPortDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitURIUserinfoFallback(raw string) (schemePrefix, user, pass, hostPart, tail string, hasPass, ok bool) {
	schemeIdx := strings.Index(raw, "://")
	if schemeIdx <= 0 {
		return "", "", "", "", "", false, false
	}
	schemePrefix = raw[:schemeIdx+3]
	rest := raw[schemeIdx+3:]

	var authority string
	if firstSlash := strings.IndexByte(rest, '/'); firstSlash >= 0 {
		authority = rest[:firstSlash]
		tail = rest[firstSlash:]
	} else if qIdx := strings.IndexAny(rest, "?#"); qIdx >= 0 {
		if strings.Contains(rest[:qIdx], "@") {
			authority = rest[:qIdx]
			tail = rest[qIdx:]
		} else if atIdx := strings.LastIndex(rest, "@"); atIdx > qIdx {
			prefix := rest[:qIdx]
			colonIdx := strings.Index(prefix, ":")
			if colonIdx >= 0 && !isPortDigits(prefix[colonIdx+1:]) {
				postAt := rest[atIdx+1:]
				if postTailIdx := strings.IndexAny(postAt, "?#"); postTailIdx >= 0 {
					authority = rest[:atIdx+1] + postAt[:postTailIdx]
					tail = postAt[postTailIdx:]
				} else {
					authority = rest
				}
			} else {
				authority = rest[:qIdx]
				tail = rest[qIdx:]
			}
		} else {
			authority = rest[:qIdx]
			tail = rest[qIdx:]
		}
	} else {
		authority = rest
	}

	atIdx := strings.LastIndex(authority, "@")
	if atIdx < 0 {
		return "", "", "", "", "", false, false
	}
	userinfo := authority[:atIdx]
	hostPart = authority[atIdx+1:]
	if colonIdx := strings.Index(userinfo, ":"); colonIdx >= 0 {
		return schemePrefix, userinfo[:colonIdx], userinfo[colonIdx+1:], hostPart, tail, true, true
	}
	return schemePrefix, userinfo, "", hostPart, tail, false, true
}

// StripPasswordFromURI removes any embedded password from a connection URI (e.g. mongodb://user:pass@host/db -> mongodb://user@host/db)
// so unremembered URI connections never persist database credentials on disk.
func StripPasswordFromURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		username := u.User.Username()
		if username != "" {
			u.User = url.User(username)
		} else {
			u.User = nil
		}
		return u.String()
	}
	// Fallback for URIs containing unescaped special characters in userinfo (scheme://user:pass@host/...)
	if schemePrefix, userOnly, _, hostPart, tail, hasPass, ok := splitURIUserinfoFallback(raw); ok && hasPass {
		if userOnly != "" {
			return schemePrefix + userOnly + "@" + hostPart + tail
		}
		return schemePrefix + hostPart + tail
	}
	return raw
}

// ExtractUsernameFromURI extracts the username component from a connection URI if present.
func ExtractUsernameFromURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		return u.User.Username()
	}
	if _, user, _, _, _, _, ok := splitURIUserinfoFallback(raw); ok {
		if unesc, err := url.PathUnescape(user); err == nil {
			return unesc
		}
		return user
	}
	return ""
}

// ExtractPasswordFromURI extracts the password component from a connection URI if present.
func ExtractPasswordFromURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if pass, ok := u.User.Password(); ok {
			return pass
		}
	}
	if _, _, rawPass, _, _, hasPass, ok := splitURIUserinfoFallback(raw); ok && hasPass {
		if unesc, err := url.PathUnescape(rawPass); err == nil {
			return unesc
		}
		return rawPass
	}
	return ""
}

// InjectCredentialsIntoURI merges runtime username/password credentials into a connection URI.
func InjectCredentialsIntoURI(rawURI, username, password string) string {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return ""
	}
	stripped := StripPasswordFromURI(rawURI)
	u, err := url.Parse(rawURI)
	if err != nil || (u.User == nil && stripped != rawURI) {
		u, err = url.Parse(stripped)
		if err != nil {
			return rawURI
		}
	}
	effUser := strings.TrimSpace(username)
	if effUser == "" && u.User != nil {
		effUser = u.User.Username()
	}
	if effUser == "" && password == "" {
		u.User = nil
	} else if password == "" {
		u.User = url.User(effUser)
	} else {
		u.User = url.UserPassword(effUser, password)
	}
	return u.String()
}

func (s *Store) ListConnections() ([]SavedConnection, error) {
	rows, err := s.db.Query(`
		SELECT id, name, type, color, host, port, username, password_enc, database_name, file_path, uri_enc, options_json, owner_id, visibility, remember_password, created_at, updated_at
		FROM connections ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]SavedConnection, 0, 8)
	for rows.Next() {
		var item SavedConnection
		var passEnc, uriEnc, optJSON string
		var rememberInt int
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Type, &item.Color, &item.Host, &item.Port,
			&item.Username, &passEnc, &item.Database, &item.FilePath, &uriEnc, &optJSON,
			&item.OwnerID, &item.Visibility, &rememberInt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if item.Visibility == "" {
			item.Visibility = "shared"
		}
		item.Editable = true
		item.HasPassword = passEnc != ""
		item.HasURI = uriEnc != ""
		if !driver.IsCredentialedDriver(item.Type) {
			item.RememberPassword = true
			item.Unlocked = true
		} else {
			item.RememberPassword = rememberInt != 0
			item.Unlocked = item.RememberPassword
		}
		item.Options = make(map[string]string)
		_ = json.Unmarshal([]byte(optJSON), &item.Options)
		item.Ephemeral = IsEphemeralConnection(item.Options)
		list = append(list, item)
	}
	return list, nil
}

func (s *Store) GetDecryptedConnection(id string) (driver.ConnectionConfig, error) {
	var cfg driver.ConnectionConfig
	var passEnc, uriEnc, optJSON string
	var rememberInt int
	err := s.db.QueryRow(`
		SELECT id, name, type, color, host, port, username, password_enc, database_name, file_path, uri_enc, options_json, remember_password
		FROM connections WHERE id = ?
	`, id).Scan(
		&cfg.ID, &cfg.Name, &cfg.Type, &cfg.Color, &cfg.Host, &cfg.Port,
		&cfg.Username, &passEnc, &cfg.Database, &cfg.FilePath, &uriEnc, &optJSON, &rememberInt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cfg, fmt.Errorf("connection not found (%s)", id)
		}
		return cfg, err
	}
	remember := !driver.IsCredentialedDriver(cfg.Type) || rememberInt != 0
	cfg.RememberPassword = driver.BoolPtr(remember)
	if passEnc != "" {
		dec, err := s.vault.Decrypt(passEnc)
		if err != nil {
			return cfg, fmt.Errorf("failed to decrypt database password: %w", err)
		}
		cfg.Password = dec
	}
	if uriEnc != "" {
		dec, err := s.vault.Decrypt(uriEnc)
		if err != nil {
			return cfg, fmt.Errorf("failed to decrypt connection URI: %w", err)
		}
		cfg.URI = dec
	}
	cfg.Options = make(map[string]string)
	_ = json.Unmarshal([]byte(optJSON), &cfg.Options)
	return cfg, nil
}

func (s *Store) SaveConnection(cfg driver.ConnectionConfig, keepExistingSecret bool) (SavedConnection, error) {
	return s.SaveConnectionWithMeta(cfg, keepExistingSecret, "", "shared")
}

func (s *Store) InsertConnectionWithMeta(cfg driver.ConnectionConfig, ownerID, visibility string) (SavedConnection, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(cfg.ID) == "" {
		cfg.ID = crypto.NewUUID()
	}
	if cfg.Color == "" {
		cfg.Color = "#1677ff"
	}
	if cfg.Options == nil {
		cfg.Options = map[string]string{}
	}
	if visibility != "private" && visibility != "shared" {
		visibility = "shared"
	}
	remember := cfg.ShouldRememberPassword()
	rememberInt := 0
	if remember {
		rememberInt = 1
	}
	passPlain := cfg.Password
	uriPlain := cfg.URI
	if !remember {
		if strings.TrimSpace(cfg.Username) == "" && uriPlain != "" {
			if extracted := ExtractUsernameFromURI(uriPlain); extracted != "" {
				cfg.Username = extracted
			}
		}
		passPlain = ""
		uriPlain = StripPasswordFromURI(uriPlain)
	}
	optBytes, _ := json.Marshal(cfg.Options)
	passEnc, err := s.vault.Encrypt(passPlain)
	if err != nil {
		return SavedConnection{}, err
	}
	uriEnc, err := s.vault.Encrypt(uriPlain)
	if err != nil {
		return SavedConnection{}, err
	}
	_, err = s.db.Exec(`
		INSERT INTO connections (id, name, type, color, host, port, username, password_enc, database_name, file_path, uri_enc, options_json, owner_id, visibility, remember_password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, cfg.ID, cfg.Name, cfg.Type, cfg.Color, cfg.Host, cfg.Port, cfg.Username, passEnc, cfg.Database, cfg.FilePath, uriEnc, string(optBytes), ownerID, visibility, rememberInt, now, now)
	if err != nil {
		return SavedConnection{}, err
	}
	return SavedConnection{
		ID:               cfg.ID,
		Name:             cfg.Name,
		Type:             cfg.Type,
		Color:            cfg.Color,
		Host:             cfg.Host,
		Port:             cfg.Port,
		Username:         cfg.Username,
		HasPassword:      passEnc != "",
		RememberPassword: remember,
		Unlocked:         remember,
		Database:         cfg.Database,
		FilePath:         cfg.FilePath,
		HasURI:           uriEnc != "",
		Options:          cfg.Options,
		OwnerID:          ownerID,
		Visibility:       visibility,
		Editable:         true,
		Ephemeral:        IsEphemeralConnection(cfg.Options),
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

func (s *Store) SaveConnectionWithMeta(cfg driver.ConnectionConfig, keepExistingSecret bool, ownerID, visibility string) (SavedConnection, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if cfg.Color == "" {
		cfg.Color = "#1677ff"
	}
	if cfg.Options == nil {
		cfg.Options = map[string]string{}
	}
	if visibility != "private" && visibility != "shared" {
		visibility = "shared"
	}
	optBytes, _ := json.Marshal(cfg.Options)

	var passEnc, uriEnc, createdAt string
	var remember bool
	var err error

	if cfg.ID == "" {
		cfg.ID = crypto.NewUUID()
		createdAt = now
		remember = cfg.ShouldRememberPassword()
		rememberInt := 0
		if remember {
			rememberInt = 1
		}
		passPlain := cfg.Password
		uriPlain := cfg.URI
		if !remember {
			if strings.TrimSpace(cfg.Username) == "" && uriPlain != "" {
				if extracted := ExtractUsernameFromURI(uriPlain); extracted != "" {
					cfg.Username = extracted
				}
			}
			passPlain = ""
			uriPlain = StripPasswordFromURI(uriPlain)
		}
		passEnc, err = s.vault.Encrypt(passPlain)
		if err != nil {
			return SavedConnection{}, err
		}
		uriEnc, err = s.vault.Encrypt(uriPlain)
		if err != nil {
			return SavedConnection{}, err
		}
		_, err = s.db.Exec(`
			INSERT INTO connections (id, name, type, color, host, port, username, password_enc, database_name, file_path, uri_enc, options_json, owner_id, visibility, remember_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, cfg.ID, cfg.Name, cfg.Type, cfg.Color, cfg.Host, cfg.Port, cfg.Username, passEnc, cfg.Database, cfg.FilePath, uriEnc, string(optBytes), ownerID, visibility, rememberInt, now, now)
		if err != nil {
			return SavedConnection{}, err
		}
	} else {
		var oldUsername, oldPassEnc, oldURIEnc, oldOwner, oldVis string
		var oldRememberInt int
		err = s.db.QueryRow(`SELECT username, password_enc, uri_enc, owner_id, visibility, remember_password, created_at FROM connections WHERE id = ?`, cfg.ID).
			Scan(&oldUsername, &oldPassEnc, &oldURIEnc, &oldOwner, &oldVis, &oldRememberInt, &createdAt)
		if err != nil {
			return SavedConnection{}, fmt.Errorf("connection not found: %w", err)
		}
		if ownerID == "" {
			ownerID = oldOwner
		}
		if visibility == "" {
			visibility = oldVis
		}
		if !driver.IsCredentialedDriver(cfg.Type) {
			remember = true
		} else if cfg.RememberPassword != nil {
			remember = *cfg.RememberPassword
		} else {
			remember = oldRememberInt != 0
		}
		rememberInt := 0
		if remember {
			rememberInt = 1
		}

		if !remember {
			// Turning off or keeping RememberPassword=false guarantees zero password stored on disk.
			passEnc = ""
			rawURI := cfg.URI
			if rawURI == "" && keepExistingSecret && oldURIEnc != "" {
				if decURI, decErr := s.vault.Decrypt(oldURIEnc); decErr == nil {
					rawURI = decURI
				}
			}
			if strings.TrimSpace(cfg.Username) == "" && rawURI != "" {
				if extracted := ExtractUsernameFromURI(rawURI); extracted != "" {
					cfg.Username = extracted
				}
			}
			uriEnc, err = s.vault.Encrypt(StripPasswordFromURI(rawURI))
			if err != nil {
				return SavedConnection{}, err
			}
		} else {
			var effectivePassword string
			if cfg.Password == "" && keepExistingSecret {
				passEnc = oldPassEnc
				if oldPassEnc != "" {
					effectivePassword, _ = s.vault.Decrypt(oldPassEnc)
				}
			} else {
				effectivePassword = cfg.Password
				passEnc, err = s.vault.Encrypt(cfg.Password)
				if err != nil {
					return SavedConnection{}, err
				}
			}
			if cfg.URI == "" && keepExistingSecret {
				uriEnc = oldURIEnc
				if oldURIEnc != "" && (cfg.Password != "" || strings.TrimSpace(cfg.Username) != strings.TrimSpace(oldUsername)) {
					if decURI, decErr := s.vault.Decrypt(oldURIEnc); decErr == nil && decURI != "" {
						passForURI := effectivePassword
						if passForURI == "" && cfg.Password == "" {
							passForURI = ExtractPasswordFromURI(decURI)
						}
						updatedURI := InjectCredentialsIntoURI(decURI, cfg.Username, passForURI)
						uriEnc, err = s.vault.Encrypt(updatedURI)
						if err != nil {
							return SavedConnection{}, err
						}
					}
				}
			} else {
				uriEnc, err = s.vault.Encrypt(cfg.URI)
				if err != nil {
					return SavedConnection{}, err
				}
			}
		}
		_, err = s.db.Exec(`
			UPDATE connections
			SET name=?, type=?, color=?, host=?, port=?, username=?, password_enc=?, database_name=?, file_path=?, uri_enc=?, options_json=?, visibility=?, remember_password=?, updated_at=?
			WHERE id=?
		`, cfg.Name, cfg.Type, cfg.Color, cfg.Host, cfg.Port, cfg.Username, passEnc, cfg.Database, cfg.FilePath, uriEnc, string(optBytes), visibility, rememberInt, now, cfg.ID)
		if err != nil {
			return SavedConnection{}, err
		}
	}

	return SavedConnection{
		ID:               cfg.ID,
		Name:             cfg.Name,
		Type:             cfg.Type,
		Color:            cfg.Color,
		Host:             cfg.Host,
		Port:             cfg.Port,
		Username:         cfg.Username,
		HasPassword:      passEnc != "",
		RememberPassword: remember,
		Unlocked:         remember,
		Database:         cfg.Database,
		FilePath:         cfg.FilePath,
		HasURI:           uriEnc != "",
		Options:          cfg.Options,
		OwnerID:          ownerID,
		Visibility:       visibility,
		Editable:         true,
		Ephemeral:        IsEphemeralConnection(cfg.Options),
		CreatedAt:        createdAt,
		UpdatedAt:        now,
	}, nil
}

func (s *Store) DeleteConnection(id string) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE id = ?`, id)
	return err
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + " ...[truncated]"
}

func (s *Store) AddHistory(h HistoryEntry) {
	if h.ID == "" {
		h.ID = crypto.NewUUID()
	}
	if h.CreatedAt == "" {
		h.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	h.Query = truncateUTF8Bytes(h.Query, 8192)
	_, _ = s.db.Exec(`
		INSERT INTO query_history (id, user_id, connection_id, conn_name, db_type, database_name, query_text, elapsed_ms, status, error_msg, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, h.ID, h.UserID, h.ConnectionID, h.ConnName, h.DBType, h.Database, h.Query, h.ElapsedMs, h.Status, h.ErrorMsg, h.CreatedAt)

	if s.histCounter.Add(1)%64 == 0 {
		if h.UserID != "" {
			_, _ = s.db.Exec(`
				DELETE FROM query_history
				WHERE user_id = ? AND created_at < COALESCE(
					(SELECT created_at FROM query_history WHERE user_id = ? ORDER BY created_at DESC LIMIT 1 OFFSET 2000),
					''
				)
			`, h.UserID, h.UserID)
		}
		_, _ = s.db.Exec(`
			DELETE FROM query_history
			WHERE created_at < COALESCE(
				(SELECT created_at FROM query_history ORDER BY created_at DESC LIMIT 1 OFFSET 10000),
				''
			)
		`)
	}
}

func (s *Store) ClearHistory(connectionID string) error {
	return s.ClearHistoryForUser("", connectionID)
}

func (s *Store) ClearHistoryForUser(userID, connectionID string) error {
	switch {
	case userID == "local" && connectionID != "":
		_, err := s.db.Exec(`DELETE FROM query_history WHERE (user_id = 'local' OR user_id = '') AND connection_id = ?`, connectionID)
		return err
	case userID == "local":
		_, err := s.db.Exec(`DELETE FROM query_history WHERE user_id = 'local' OR user_id = ''`)
		return err
	case userID != "" && connectionID != "":
		_, err := s.db.Exec(`DELETE FROM query_history WHERE user_id = ? AND connection_id = ?`, userID, connectionID)
		return err
	case userID != "":
		_, err := s.db.Exec(`DELETE FROM query_history WHERE user_id = ?`, userID)
		return err
	case connectionID != "":
		_, err := s.db.Exec(`DELETE FROM query_history WHERE connection_id = ?`, connectionID)
		return err
	default:
		_, err := s.db.Exec(`DELETE FROM query_history`)
		return err
	}
}

func (s *Store) ListHistory(connectionID string, limit int) ([]HistoryEntry, error) {
	return s.ListHistoryForUser("", connectionID, limit)
}

func (s *Store) ListHistoryForUser(userID, connectionID string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var conds []string
	var args []any
	if userID == "local" {
		conds = append(conds, "(user_id = 'local' OR user_id = '')")
	} else if userID != "" {
		conds = append(conds, "user_id = ?")
		args = append(args, userID)
	}
	if connectionID != "" {
		conds = append(conds, "connection_id = ?")
		args = append(args, connectionID)
	}
	query := `SELECT id, user_id, connection_id, conn_name, db_type, database_name, query_text, elapsed_ms, status, error_msg, created_at FROM query_history`
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]HistoryEntry, 0, 32)
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.UserID, &h.ConnectionID, &h.ConnName, &h.DBType, &h.Database, &h.Query, &h.ElapsedMs, &h.Status, &h.ErrorMsg, &h.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, h)
	}
	return list, nil
}

func (s *Store) SaveSnippet(sn SavedSnippet) (SavedSnippet, error) {
	sn.IsShared = true
	return s.SaveSnippetForUser(sn, "", "", true)
}

func (s *Store) SaveSnippetForUser(sn SavedSnippet, userID, username string, isAdmin bool) (SavedSnippet, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	isSharedInt := 0
	if sn.IsShared {
		isSharedInt = 1
	}

	if sn.ID == "" {
		sn.ID = crypto.NewUUID()
		sn.OwnerID = userID
		sn.OwnerName = username
		sn.CreatedAt = now
	} else {
		var existingOwner, existingOwnerName, existingCreated string
		err := s.db.QueryRow(`SELECT owner_id, owner_name, created_at FROM saved_snippets WHERE id = ?`, sn.ID).
			Scan(&existingOwner, &existingOwnerName, &existingCreated)
		if err == nil {
			if !isAdmin && existingOwner != "" && existingOwner != userID {
				return SavedSnippet{}, errors.New("permission denied: only the snippet owner or an administrator can update this snippet")
			}
			sn.OwnerID = existingOwner
			sn.OwnerName = existingOwnerName
			sn.CreatedAt = existingCreated
		} else {
			sn.OwnerID = userID
			sn.OwnerName = username
			sn.CreatedAt = now
		}
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO saved_snippets (id, title, db_type, connection_id, database_name, sql_text, owner_id, owner_name, is_shared, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sn.ID, sn.Title, sn.DBType, sn.ConnectionID, sn.Database, sn.SQL, sn.OwnerID, sn.OwnerName, isSharedInt, sn.CreatedAt)
	sn.Editable = true
	return sn, err
}

func (s *Store) ListSnippets() ([]SavedSnippet, error) {
	return s.ListSnippetsForUser("", true)
}

func (s *Store) ListSnippetsForUser(userID string, isAdmin bool) ([]SavedSnippet, error) {
	var rows *sql.Rows
	var err error
	if isAdmin || userID == "" || userID == "local" {
		rows, err = s.db.Query(`
			SELECT id, title, db_type, connection_id, database_name, sql_text, owner_id, owner_name, is_shared, created_at
			FROM saved_snippets ORDER BY created_at DESC
		`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, title, db_type, connection_id, database_name, sql_text, owner_id, owner_name, is_shared, created_at
			FROM saved_snippets
			WHERE is_shared = 1 OR owner_id = ? OR owner_id = ''
			ORDER BY created_at DESC
		`, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]SavedSnippet, 0, 16)
	for rows.Next() {
		var sn SavedSnippet
		var sharedInt int
		if err := rows.Scan(&sn.ID, &sn.Title, &sn.DBType, &sn.ConnectionID, &sn.Database, &sn.SQL, &sn.OwnerID, &sn.OwnerName, &sharedInt, &sn.CreatedAt); err != nil {
			return nil, err
		}
		sn.IsShared = sharedInt == 1
		sn.Editable = isAdmin || userID == "local" || sn.OwnerID == "" || sn.OwnerID == userID
		list = append(list, sn)
	}
	return list, nil
}

func (s *Store) DeleteSnippet(id string) error {
	return s.DeleteSnippetForUser(id, "", true)
}

func (s *Store) DeleteSnippetForUser(id, userID string, isAdmin bool) error {
	if !isAdmin && userID != "" && userID != "local" {
		var ownerID string
		err := s.db.QueryRow(`SELECT owner_id FROM saved_snippets WHERE id = ?`, id).Scan(&ownerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if ownerID != "" && ownerID != userID {
			return errors.New("permission denied: only the snippet owner or an administrator can delete this snippet")
		}
	}
	_, err := s.db.Exec(`DELETE FROM saved_snippets WHERE id = ?`, id)
	return err
}
