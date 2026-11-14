package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"

	"golang.org/x/crypto/bcrypt"
)

var (
	validUsernameRe       = regexp.MustCompile(`^[a-zA-Z0-9._@-]{2,64}$`)
	ErrAlreadyInitialized = errors.New("administrator account has already been initialized")
)

// NeedsSetup returns true when no user accounts exist yet and initial administrator setup is required.
func (s *Store) NeedsSetup() bool {
	if s.initialized.Load() {
		return false
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.initialized.Load() {
		return false
	}
	var cnt int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&cnt); err == nil && cnt > 0 {
		s.initialized.Store(true)
		return false
	}
	return true
}

// SetupInitialAdmin atomically creates the very first administrator account (and optional login branding)
// when the user store is empty. Subsequent calls always return ErrAlreadyInitialized.
func (s *Store) SetupInitialAdmin(
	username string,
	displayName string,
	password string,
	branding *LoginBrandingConfig,
) (User, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()

	if s.initialized.Load() {
		return User{}, ErrAlreadyInitialized
	}

	uname := strings.TrimSpace(username)
	if !validUsernameRe.MatchString(uname) {
		return User{}, errors.New("username must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'")
	}
	if len(password) < 8 {
		return User{}, errors.New("administrator password must be at least 8 characters")
	}
	dname := strings.TrimSpace(displayName)
	if len(dname) > 64 {
		dname = dname[:64]
	}

	var validatedBranding *LoginBrandingConfig
	var brandingJSON string
	if branding != nil {
		clean, bErr := NormalizeAndValidateLoginBranding(*branding)
		if bErr != nil {
			return User{}, bErr
		}
		enc, mErr := json.Marshal(clean)
		if mErr != nil {
			return User{}, mErr
		}
		validatedBranding = &clean
		brandingJSON = string(enc)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}

	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&existing); err != nil {
		_ = tx.Rollback()
		return User{}, err
	}
	if existing > 0 {
		_ = tx.Rollback()
		s.initialized.Store(true)
		return User{}, ErrAlreadyInitialized
	}

	id := crypto.NewUUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(
		`INSERT INTO users (id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, password_changed, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, 1, 'all', 1, ?)`,
		id, uname, dname, string(hash), RoleAdmin, now,
	); err != nil {
		_ = tx.Rollback()
		return User{}, err
	}

	if validatedBranding != nil {
		if _, err := tx.Exec(
			`INSERT INTO system_settings (key, value, updated_at) VALUES ('login_branding', ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			brandingJSON, now,
		); err != nil {
			_ = tx.Rollback()
			return User{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return User{}, err
	}

	s.initialized.Store(true)
	if validatedBranding != nil {
		s.brandingMu.Lock()
		s.branding = *validatedBranding
		s.brandingMu.Unlock()
	}
	s.refreshPrincipal(id)
	u, err := s.GetUserByID(id)
	if err != nil {
		return User{}, err
	}
	return *u, nil
}

func NormalizeRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RoleAdmin:
		return RoleAdmin
	case RoleReadWrite:
		return RoleReadWrite
	case RoleReadOnly:
		return RoleReadOnly
	default:
		return RoleReadOnly
	}
}

func parseConnScope(raw string) (string, []string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "all") {
		return "all", []string{}
	}
	var ids []string
	if err := json.Unmarshal([]byte(trimmed), &ids); err != nil {
		return "all", []string{}
	}
	clean := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			clean = append(clean, id)
		}
	}
	return "selected", clean
}

func encodeConnScope(scope string, allowedIDs []string) string {
	if strings.EqualFold(strings.TrimSpace(scope), "all") {
		return "all"
	}
	clean := make([]string, 0, len(allowedIDs))
	seen := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			clean = append(clean, id)
		}
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func buildPrincipal(id, username, displayName, role string, disabled, pwdChanged, canCreate bool, rawScope string) UserPrincipal {
	normRole := NormalizeRole(role)
	scopeKind, ids := parseConnScope(rawScope)
	set := make(map[string]struct{}, len(ids))
	for _, cid := range ids {
		set[cid] = struct{}{}
	}
	return UserPrincipal{
		ID:              id,
		Username:        username,
		DisplayName:     displayName,
		Role:            normRole,
		Disabled:        disabled,
		PasswordChanged: pwdChanged,
		CanCreateConn:   normRole == RoleAdmin || (normRole == RoleReadWrite && canCreate),
		ConnScopeAll:    normRole == RoleAdmin || scopeKind == "all",
		AllowedConnIDs:  set,
	}
}

func LocalDesktopPrincipal() UserPrincipal {
	return UserPrincipal{
		ID:              "local",
		Username:        "local",
		DisplayName:     "Local Workspace",
		Role:            RoleAdmin,
		Disabled:        false,
		PasswordChanged: true,
		CanCreateConn:   true,
		ConnScopeAll:    true,
	}
}

func (s *Store) hydrateUserPrincipals() {
	rows, err := s.db.Query(`
		SELECT id, username, display_name, role, disabled, password_changed, can_create_conn, conn_scope
		FROM users
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	next := make(map[string]UserPrincipal, 8)
	for rows.Next() {
		var id, uname, dname, role, rawScope string
		var disInt, pwdInt, createInt int
		if rows.Scan(&id, &uname, &dname, &role, &disInt, &pwdInt, &createInt, &rawScope) == nil {
			next[id] = buildPrincipal(id, uname, dname, role, disInt == 1, pwdInt == 1, createInt == 1, rawScope)
		}
	}
	s.userMu.Lock()
	s.principals = next
	s.userMu.Unlock()
}

func (s *Store) refreshPrincipal(userID string) {
	if userID == "" {
		return
	}
	var id, uname, dname, role, rawScope string
	var disInt, pwdInt, createInt int
	err := s.db.QueryRow(`
		SELECT id, username, display_name, role, disabled, password_changed, can_create_conn, conn_scope
		FROM users WHERE id = ?
	`, userID).Scan(&id, &uname, &dname, &role, &disInt, &pwdInt, &createInt, &rawScope)
	s.userMu.Lock()
	if err != nil {
		delete(s.principals, userID)
	} else {
		s.principals[userID] = buildPrincipal(id, uname, dname, role, disInt == 1, pwdInt == 1, createInt == 1, rawScope)
	}
	s.userMu.Unlock()
}

func (s *Store) GetPrincipal(userID string) (UserPrincipal, bool) {
	if userID == "local" {
		return LocalDesktopPrincipal(), true
	}
	s.userMu.RLock()
	p, ok := s.principals[userID]
	s.userMu.RUnlock()
	if ok {
		return p, true
	}
	// Lazy fallback if store was modified externally
	s.refreshPrincipal(userID)
	s.userMu.RLock()
	p, ok = s.principals[userID]
	s.userMu.RUnlock()
	return p, ok
}

type CreateUserInput struct {
	Username            string   `json:"username"`
	DisplayName         string   `json:"displayName"`
	Password            string   `json:"password"`
	Role                string   `json:"role"`
	CanCreateConn       bool     `json:"canCreateConn"`
	ConnScope           string   `json:"connScope"`
	AllowedConnIDs      []string `json:"allowedConnIds"`
	ForcePasswordChange bool     `json:"forcePasswordChange"`
}

type UpdateUserInput struct {
	DisplayName    *string  `json:"displayName"`
	Role           *string  `json:"role"`
	Disabled       *bool    `json:"disabled"`
	CanCreateConn  *bool    `json:"canCreateConn"`
	ConnScope      *string  `json:"connScope"`
	AllowedConnIDs []string `json:"allowedConnIds"`
}

func (s *Store) countOtherActiveAdmins(excludeUserID string) (int, error) {
	var cnt int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id <> ?`,
		excludeUserID,
	).Scan(&cnt)
	return cnt, err
}

func (s *Store) ListUsers() ([]User, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`
		SELECT
			u.id,
			u.username,
			u.display_name,
			u.role,
			u.disabled,
			u.can_create_conn,
			u.conn_scope,
			u.totp_enabled,
			u.password_changed,
			u.created_at,
			(SELECT COUNT(*) FROM auth_sessions s WHERE s.user_id = u.id AND s.revoked = 0 AND s.expires_at > ?) AS active_cnt
		FROM users u
		ORDER BY CASE WHEN u.role = 'admin' THEN 0 WHEN u.role = 'readwrite' THEN 1 ELSE 2 END, u.created_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]User, 0, 8)
	for rows.Next() {
		var u User
		var disInt, createInt, totpInt, pwdInt int
		var rawScope string
		if err := rows.Scan(
			&u.ID, &u.Username, &u.DisplayName, &u.Role, &disInt, &createInt,
			&rawScope, &totpInt, &pwdInt, &u.CreatedAt, &u.ActiveSessionsCount,
		); err != nil {
			return nil, err
		}
		u.Role = NormalizeRole(u.Role)
		u.Disabled = disInt == 1
		u.CanCreateConn = u.Role == RoleAdmin || (u.Role == RoleReadWrite && createInt == 1)
		u.ConnScope, u.AllowedConnIDs = parseConnScope(rawScope)
		u.TOTPEnabled = totpInt == 1
		u.PasswordChanged = pwdInt == 1
		list = append(list, u)
	}
	return list, nil
}

func (s *Store) CreateUser(in CreateUserInput) (User, error) {
	uname := strings.TrimSpace(in.Username)
	if !validUsernameRe.MatchString(uname) {
		return User{}, errors.New("username must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'")
	}
	if len(in.Password) < 6 {
		return User{}, errors.New("initial password must be at least 6 characters")
	}
	role := NormalizeRole(in.Role)
	dname := strings.TrimSpace(in.DisplayName)
	if len(dname) > 64 {
		dname = dname[:64]
	}

	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE LOWER(username) = LOWER(?)`, uname).Scan(&exists); err != nil {
		return User{}, err
	}
	if exists > 0 {
		return User{}, fmt.Errorf("username %q already exists", uname)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	canCreate := 0
	if role == RoleAdmin || (role == RoleReadWrite && in.CanCreateConn) {
		canCreate = 1
	}
	rawScope := encodeConnScope(in.ConnScope, in.AllowedConnIDs)
	if role == RoleAdmin {
		rawScope = "all"
	}
	pwdChanged := 1
	if in.ForcePasswordChange {
		pwdChanged = 0
	}
	id := crypto.NewUUID()
	createdAt := time.Now().UTC().Format(time.RFC3339)

	_, err = s.db.Exec(`
		INSERT INTO users (id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, password_changed, created_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?)
	`, id, uname, dname, string(hash), role, canCreate, rawScope, pwdChanged, createdAt)
	if err != nil {
		return User{}, err
	}
	s.initialized.Store(true)
	s.refreshPrincipal(id)
	u, err := s.GetUserByID(id)
	if err != nil {
		return User{}, err
	}
	return *u, nil
}

func (s *Store) UpdateUser(userID string, in UpdateUserInput) (User, error) {
	cur, err := s.GetUserByID(userID)
	if err != nil {
		return User{}, errors.New("user not found")
	}

	nextDisplayName := cur.DisplayName
	if in.DisplayName != nil {
		nextDisplayName = strings.TrimSpace(*in.DisplayName)
		if len(nextDisplayName) > 64 {
			nextDisplayName = nextDisplayName[:64]
		}
	}

	nextRole := cur.Role
	if in.Role != nil {
		nextRole = NormalizeRole(*in.Role)
	}

	nextDisabled := cur.Disabled
	if in.Disabled != nil {
		nextDisabled = *in.Disabled
	}

	// Guard against locking out or demoting the last active administrator
	if cur.Role == RoleAdmin && !cur.Disabled && (nextRole != RoleAdmin || nextDisabled) {
		otherAdmins, err := s.countOtherActiveAdmins(userID)
		if err != nil {
			return User{}, err
		}
		if otherAdmins == 0 {
			return User{}, errors.New("cannot disable or demote the last active administrator account")
		}
	}

	nextCanCreate := cur.CanCreateConn
	if in.CanCreateConn != nil {
		nextCanCreate = *in.CanCreateConn
	}
	if nextRole == RoleAdmin {
		nextCanCreate = true
	} else if nextRole == RoleReadOnly {
		nextCanCreate = false
	}

	nextScope := cur.ConnScope
	nextAllowed := cur.AllowedConnIDs
	if in.ConnScope != nil {
		nextScope = *in.ConnScope
		nextAllowed = in.AllowedConnIDs
	} else if in.AllowedConnIDs != nil {
		nextAllowed = in.AllowedConnIDs
	}
	rawScope := encodeConnScope(nextScope, nextAllowed)
	if nextRole == RoleAdmin {
		rawScope = "all"
	}

	disInt := 0
	if nextDisabled {
		disInt = 1
	}
	createInt := 0
	if nextCanCreate {
		createInt = 1
	}

	if _, err := s.db.Exec(`
		UPDATE users
		SET display_name = ?, role = ?, disabled = ?, can_create_conn = ?, conn_scope = ?
		WHERE id = ?
	`, nextDisplayName, nextRole, disInt, createInt, rawScope, userID); err != nil {
		return User{}, err
	}

	s.refreshPrincipal(userID)
	if nextDisabled {
		_, _ = s.RevokeAllUserSessions(userID)
	}

	u, err := s.GetUserByID(userID)
	if err != nil {
		return User{}, err
	}
	return *u, nil
}

func (s *Store) UpdateUserProfile(userID, displayName string) error {
	dname := strings.TrimSpace(displayName)
	if len(dname) > 64 {
		dname = dname[:64]
	}
	res, err := s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, dname, userID)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return errors.New("user not found")
	}
	s.refreshPrincipal(userID)
	return nil
}

func (s *Store) AdminResetPassword(userID, newPass string, forceChange bool) ([]string, error) {
	if len(newPass) < 6 {
		return nil, errors.New("new password must be at least 6 characters")
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil || exists == 0 {
		return nil, errors.New("user not found")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	pwdChanged := 1
	if forceChange {
		pwdChanged = 0
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed = ? WHERE id = ?`, string(hash), pwdChanged, userID); err != nil {
		return nil, err
	}
	s.refreshPrincipal(userID)
	return s.RevokeAllUserSessions(userID)
}

func (s *Store) AdminReset2FA(userID string) error {
	res, err := s.db.Exec(`
		UPDATE users
		SET totp_enabled = 0, totp_secret_enc = '', totp_pending_enc = '', recovery_hashes_json = '[]', last_totp_step = 0
		WHERE id = ?
	`, userID)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return errors.New("user not found")
	}
	return nil
}

func (s *Store) ListActiveUserSessionIDs(userID string) []string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	rows, err := s.db.Query(`SELECT id FROM auth_sessions WHERE user_id = ? AND revoked = 0`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var sids []string
	for rows.Next() {
		var sid string
		if rows.Scan(&sid) == nil && sid != "" {
			sids = append(sids, sid)
		}
	}
	return sids
}

func (s *Store) RevokeAllUserSessions(userID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM auth_sessions WHERE user_id = ? AND revoked = 0`, userID)
	var sids []string
	if err == nil {
		for rows.Next() {
			var sid string
			if rows.Scan(&sid) == nil {
				sids = append(sids, sid)
			}
		}
		_ = rows.Close()
	}
	s.sessMu.Lock()
	for _, sid := range sids {
		delete(s.activeSessions, sid)
	}
	s.sessMu.Unlock()

	_, err = s.db.Exec(`UPDATE auth_sessions SET revoked = 1 WHERE user_id = ? AND revoked = 0`, userID)
	return sids, err
}

func (s *Store) DeleteUser(targetUserID, actorUserID string) ([]string, []string, error) {
	if targetUserID == actorUserID {
		return nil, nil, errors.New("cannot delete your own currently signed-in account")
	}
	target, err := s.GetUserByID(targetUserID)
	if err != nil {
		return nil, nil, errors.New("user not found")
	}
	if target.Role == RoleAdmin && !target.Disabled {
		otherAdmins, err := s.countOtherActiveAdmins(targetUserID)
		if err != nil {
			return nil, nil, err
		}
		if otherAdmins == 0 {
			return nil, nil, errors.New("cannot delete the last active administrator account")
		}
	}

	sids, _ := s.RevokeAllUserSessions(targetUserID)

	// Collect private connection IDs owned by this user so caller can evict pools
	var privateConnIDs []string
	cRows, cErr := s.db.Query(`SELECT id FROM connections WHERE owner_id = ? AND visibility = 'private'`, targetUserID)
	if cErr == nil {
		for cRows.Next() {
			var cid string
			if cRows.Scan(&cid) == nil {
				privateConnIDs = append(privateConnIDs, cid)
			}
		}
		_ = cRows.Close()
	}

	_, _ = s.db.Exec(`DELETE FROM connections WHERE owner_id = ? AND visibility = 'private'`, targetUserID)
	_, _ = s.db.Exec(`UPDATE connections SET owner_id = '' WHERE owner_id = ?`, targetUserID)
	_, _ = s.db.Exec(`DELETE FROM saved_snippets WHERE owner_id = ? AND is_shared = 0`, targetUserID)
	_, _ = s.db.Exec(`DELETE FROM query_history WHERE user_id = ?`, targetUserID)
	_, _ = s.db.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, targetUserID)

	if _, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, targetUserID); err != nil {
		return sids, privateConnIDs, err
	}

	s.userMu.Lock()
	delete(s.principals, targetUserID)
	s.userMu.Unlock()
	return sids, privateConnIDs, nil
}

// --- Connection Access Control, Duplication & Sanitized Import/Export ---

func redactHostDataDirPrefix(filePath, dataDir string) string {
	fp := strings.TrimSpace(filePath)
	if fp == "" || fp == ":memory:" || strings.TrimSpace(dataDir) == "" {
		return fp
	}
	absData, err1 := filepath.Abs(dataDir)
	absFile, err2 := filepath.Abs(fp)
	if err1 != nil || err2 != nil {
		return filepath.Base(fp)
	}
	rel, err := filepath.Rel(absData, absFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(fp)
	}
	return filepath.ToSlash(rel)
}

func sanitizeClientVisibleConnection(c SavedConnection, dataDir string, maskHostDir bool) SavedConnection {
	if IsEphemeralConnection(c.Options) {
		c.Ephemeral = true
		origName := strings.TrimSpace(c.Options["__orig_filename"])
		if origName == "" {
			origName = filepath.Base(c.FilePath)
		}
		c.FilePath = origName
	} else if maskHostDir {
		c.FilePath = redactHostDataDirPrefix(c.FilePath, dataDir)
	}
	if len(c.Options) > 0 {
		cleaned := make(map[string]string, len(c.Options))
		for k, v := range c.Options {
			if strings.HasPrefix(k, "__") {
				continue
			}
			cleaned[k] = v
		}
		c.Options = cleaned
	}
	return c
}

func StripReservedConnectionOptions(opts map[string]string) map[string]string {
	if len(opts) == 0 {
		return opts
	}
	out := make(map[string]string, len(opts))
	for k, v := range opts {
		if strings.HasPrefix(k, "__") {
			continue
		}
		out[k] = v
	}
	return out
}

func (s *Store) CountUserEphemeralConnections(userID string) int {
	if strings.TrimSpace(userID) == "" {
		return 0
	}
	var cnt int
	_ = s.db.QueryRow(
		`SELECT COUNT(1) FROM connections WHERE owner_id = ? AND options_json LIKE '%"__ephemeral":"1"%'`,
		userID,
	).Scan(&cnt)
	return cnt
}

// IsConnectionOwnedByAdmin reports whether connID is a system-bootstrapped connection (empty owner_id)
// or is owned by an active user whose role is RoleAdmin.
func (s *Store) IsConnectionOwnedByAdmin(connID string) bool {
	if strings.TrimSpace(connID) == "" {
		return false
	}
	var ownerID string
	if err := s.db.QueryRow(`SELECT owner_id FROM connections WHERE id = ?`, connID).Scan(&ownerID); err != nil {
		return false
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return true
	}
	if p, ok := s.GetPrincipal(ownerID); ok {
		return p.Role == RoleAdmin
	}
	var role string
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id = ? AND disabled = 0`, ownerID).Scan(&role); err == nil {
		return role == RoleAdmin
	}
	return false
}

func (s *Store) ListConnectionsForPrincipal(p UserPrincipal) ([]SavedConnection, error) {
	all, err := s.ListConnections()
	if err != nil {
		return nil, err
	}
	if p.ID == "local" {
		for i := range all {
			all[i].Editable = true
			all[i] = sanitizeClientVisibleConnection(all[i], s.dataDir, false)
		}
		return all, nil
	}
	if p.Role == RoleAdmin {
		filtered := make([]SavedConnection, 0, len(all))
		for _, c := range all {
			if IsEphemeralConnection(c.Options) {
				if c.OwnerID == "" || c.OwnerID != p.ID {
					continue
				}
			}
			c.Editable = true
			filtered = append(filtered, sanitizeClientVisibleConnection(c, s.dataDir, false))
		}
		return filtered, nil
	}

	filtered := make([]SavedConnection, 0, len(all))
	for _, c := range all {
		if IsEphemeralConnection(c.Options) {
			if c.OwnerID != "" && c.OwnerID == p.ID {
				c.Editable = true
				filtered = append(filtered, sanitizeClientVisibleConnection(c, s.dataDir, true))
			}
			continue
		}
		isOwner := c.OwnerID != "" && c.OwnerID == p.ID
		if isOwner {
			c.Editable = p.Role != RoleReadOnly
			filtered = append(filtered, sanitizeClientVisibleConnection(c, s.dataDir, true))
			continue
		}
		if c.Visibility == "shared" || c.Visibility == "" {
			if p.ConnScopeAll {
				c.Editable = false
				filtered = append(filtered, sanitizeClientVisibleConnection(c, s.dataDir, true))
			} else if _, allowed := p.AllowedConnIDs[c.ID]; allowed {
				c.Editable = false
				filtered = append(filtered, sanitizeClientVisibleConnection(c, s.dataDir, true))
			}
		}
	}
	return filtered, nil
}

func (s *Store) GetSavedConnectionForPrincipal(p UserPrincipal, connID string) (SavedConnection, bool) {
	if connID == "" {
		return SavedConnection{}, false
	}
	list, err := s.ListConnectionsForPrincipal(p)
	if err != nil {
		return SavedConnection{}, false
	}
	for _, item := range list {
		if item.ID == connID {
			return item, true
		}
	}
	return SavedConnection{}, false
}

func (s *Store) CanAccessConnection(p UserPrincipal, connID string) bool {
	if connID == "" {
		return false
	}
	var ownerID, vis, optJSON string
	err := s.db.QueryRow(`SELECT owner_id, visibility, options_json FROM connections WHERE id = ?`, connID).Scan(&ownerID, &vis, &optJSON)
	if err != nil {
		return false
	}
	if p.ID == "local" {
		return true
	}
	var opts map[string]string
	_ = json.Unmarshal([]byte(optJSON), &opts)
	if IsEphemeralConnection(opts) {
		ephOwner := opts["__ephemeral_owner"]
		return ownerID != "" && ownerID == p.ID && (ephOwner == "" || ephOwner == p.ID)
	}
	if p.Role == RoleAdmin {
		return true
	}
	if ownerID != "" && ownerID == p.ID {
		return true
	}
	if vis == "shared" || vis == "" {
		if p.ConnScopeAll {
			return true
		}
		_, ok := p.AllowedConnIDs[connID]
		return ok
	}
	return false
}

func (s *Store) CanEditConnection(p UserPrincipal, connID string) bool {
	if connID == "" {
		return false
	}
	var ownerID, vis, optJSON string
	if err := s.db.QueryRow(`SELECT owner_id, visibility, options_json FROM connections WHERE id = ?`, connID).Scan(&ownerID, &vis, &optJSON); err != nil {
		return false
	}
	if p.ID == "local" {
		return true
	}
	var opts map[string]string
	_ = json.Unmarshal([]byte(optJSON), &opts)
	if IsEphemeralConnection(opts) {
		ephOwner := opts["__ephemeral_owner"]
		return ownerID != "" && ownerID == p.ID && (ephOwner == "" || ephOwner == p.ID)
	}
	if p.Role == RoleAdmin {
		return true
	}
	if p.Role == RoleReadOnly || vis == "shared" {
		return false
	}
	return ownerID != "" && ownerID == p.ID
}

func (s *Store) DuplicateConnection(connID string, p UserPrincipal) (SavedConnection, error) {
	cfg, err := s.GetDecryptedConnection(connID)
	if err != nil {
		return SavedConnection{}, err
	}
	if IsEphemeralConnection(cfg.Options) {
		return SavedConnection{}, errors.New("temporary browser-loaded database sessions cannot be duplicated")
	}
	var origOwner, origVis string
	_ = s.db.QueryRow(`SELECT owner_id, visibility FROM connections WHERE id = ?`, connID).Scan(&origOwner, &origVis)

	if p.ID != "local" && origOwner != "" && origOwner != p.ID {
		cfg.Password = ""
		cfg.URI = StripPasswordFromURI(cfg.URI)
		cfg.RememberPassword = driver.BoolPtr(!driver.IsCredentialedDriver(cfg.Type))
	}

	cfg.ID = ""
	cfg.Name = strings.TrimSpace(cfg.Name) + " (Copy)"
	cfg.Options = StripReservedConnectionOptions(cfg.Options)
	ownerID := p.ID
	vis := "private"
	if p.ID == "local" {
		ownerID = ""
		vis = "shared"
	} else if p.Role == RoleAdmin {
		if origVis == "private" {
			vis = "private"
		} else {
			vis = "shared"
		}
	}
	return s.SaveConnectionWithMeta(cfg, false, ownerID, vis)
}

type ExportedConnectionProfile struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Color      string            `json:"color,omitempty"`
	Host       string            `json:"host,omitempty"`
	Port       int               `json:"port,omitempty"`
	Username   string            `json:"username,omitempty"`
	Database   string            `json:"database,omitempty"`
	FilePath   string            `json:"filePath,omitempty"`
	URI        string            `json:"uri,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
	Visibility string            `json:"visibility,omitempty"`
}

func (s *Store) ExportConnectionsForPrincipal(p UserPrincipal) ([]ExportedConnectionProfile, error) {
	list, err := s.ListConnectionsForPrincipal(p)
	if err != nil {
		return nil, err
	}
	out := make([]ExportedConnectionProfile, 0, len(list))
	for _, c := range list {
		if c.Ephemeral {
			continue
		}
		out = append(out, ExportedConnectionProfile{
			Name:       c.Name,
			Type:       c.Type,
			Color:      c.Color,
			Host:       c.Host,
			Port:       c.Port,
			Username:   c.Username,
			Database:   c.Database,
			FilePath:   c.FilePath,
			Options:    c.Options,
			Visibility: c.Visibility,
		})
	}
	return out, nil
}

const MaxImportConnectionProfiles = 500

func (s *Store) ImportConnections(profiles []ExportedConnectionProfile, p UserPrincipal) (int, error) {
	if len(profiles) == 0 {
		return 0, errors.New("no connection profiles found in import payload")
	}
	if len(profiles) > MaxImportConnectionProfiles {
		return 0, fmt.Errorf("cannot import more than %d connection profiles at once", MaxImportConnectionProfiles)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO connections (id, name, type, color, host, port, username, password_enc, database_name, file_path, uri_enc, options_json, owner_id, visibility, remember_password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	imported := 0
	for _, item := range profiles {
		name := strings.TrimSpace(item.Name)
		tp := strings.TrimSpace(item.Type)
		if name == "" || tp == "" {
			continue
		}
		if _, ok := driver.GetMeta(tp); !ok {
			continue
		}
		vis := "private"
		ownerID := p.ID
		if p.ID == "local" {
			ownerID = ""
			vis = "shared"
		} else if p.Role == RoleAdmin {
			if item.Visibility == "private" {
				vis = "private"
			} else {
				vis = "shared"
			}
		}
		rememberInt := 0
		if !driver.IsCredentialedDriver(tp) {
			rememberInt = 1
		}
		cleanOpts := StripReservedConnectionOptions(item.Options)
		if cleanOpts == nil {
			cleanOpts = map[string]string{}
		}
		optBytes, _ := json.Marshal(cleanOpts)
		uriEnc := ""
		if cleanURI := StripPasswordFromURI(strings.TrimSpace(item.URI)); cleanURI != "" {
			if enc, encErr := s.vault.Encrypt(cleanURI); encErr == nil {
				uriEnc = enc
			}
		}
		connID := crypto.NewUUID()
		if _, execErr := stmt.Exec(
			connID, name, tp, strings.TrimSpace(item.Color),
			strings.TrimSpace(item.Host), item.Port, strings.TrimSpace(item.Username),
			strings.TrimSpace(item.Database), strings.TrimSpace(item.FilePath),
			uriEnc, string(optBytes), ownerID, vis, rememberInt, now, now,
		); execErr == nil {
			imported++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return imported, nil
}

func (s *Store) maintenanceLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			now := time.Now().Unix()
			_, _ = s.db.Exec(`DELETE FROM auth_sessions WHERE expires_at < ? OR revoked = 1`, now)
			s.sessMu.Lock()
			for sid, exp := range s.activeSessions {
				if exp <= now {
					delete(s.activeSessions, sid)
				}
			}
			s.sessMu.Unlock()
			s.maintMu.RLock()
			hook := s.maintHook
			s.maintMu.RUnlock()
			if hook != nil {
				hook()
			}
			_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
			_, _ = s.db.Exec(`PRAGMA optimize`)
		}
	}
}
