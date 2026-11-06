package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"

	"golang.org/x/crypto/bcrypt"
)

var validUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9._@-]{2,64}$`)

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

func (s *Store) ListConnectionsForPrincipal(p UserPrincipal) ([]SavedConnection, error) {
	all, err := s.ListConnections()
	if err != nil {
		return nil, err
	}
	if p.ID == "local" || p.Role == RoleAdmin {
		for i := range all {
			all[i].Editable = true
		}
		return all, nil
	}

	filtered := make([]SavedConnection, 0, len(all))
	for _, c := range all {
		isOwner := c.OwnerID != "" && c.OwnerID == p.ID
		if isOwner {
			c.Editable = p.Role != RoleReadOnly
			filtered = append(filtered, c)
			continue
		}
		if c.Visibility == "shared" || c.Visibility == "" {
			if p.ConnScopeAll {
				c.Editable = false
				filtered = append(filtered, c)
			} else if _, allowed := p.AllowedConnIDs[c.ID]; allowed {
				c.Editable = false
				filtered = append(filtered, c)
			}
		}
	}
	return filtered, nil
}

func (s *Store) CanAccessConnection(p UserPrincipal, connID string) bool {
	if connID == "" {
		return false
	}
	if p.ID == "local" || p.Role == RoleAdmin {
		var cnt int
		return s.db.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = ?`, connID).Scan(&cnt) == nil && cnt > 0
	}
	var ownerID, vis string
	err := s.db.QueryRow(`SELECT owner_id, visibility FROM connections WHERE id = ?`, connID).Scan(&ownerID, &vis)
	if err != nil {
		return false
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
	if p.ID == "local" || p.Role == RoleAdmin {
		return true
	}
	if p.Role == RoleReadOnly {
		return false
	}
	var ownerID string
	if err := s.db.QueryRow(`SELECT owner_id FROM connections WHERE id = ?`, connID).Scan(&ownerID); err != nil {
		return false
	}
	return ownerID != "" && ownerID == p.ID
}

func (s *Store) DuplicateConnection(connID string, p UserPrincipal) (SavedConnection, error) {
	cfg, err := s.GetDecryptedConnection(connID)
	if err != nil {
		return SavedConnection{}, err
	}
	var origVis string
	_ = s.db.QueryRow(`SELECT visibility FROM connections WHERE id = ?`, connID).Scan(&origVis)

	cfg.ID = ""
	cfg.Name = strings.TrimSpace(cfg.Name) + " (Copy)"
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

func (s *Store) ImportConnections(profiles []ExportedConnectionProfile, p UserPrincipal) (int, error) {
	if len(profiles) == 0 {
		return 0, errors.New("no connection profiles found in import payload")
	}
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
		_, err := s.SaveConnectionWithMeta(driver.ConnectionConfig{
			Name:     name,
			Type:     tp,
			Color:    item.Color,
			Host:     item.Host,
			Port:     item.Port,
			Username: item.Username,
			Database: item.Database,
			FilePath: item.FilePath,
			Options:  item.Options,
		}, false, ownerID, vis)
		if err == nil {
			imported++
		}
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
			_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
			_, _ = s.db.Exec(`PRAGMA optimize`)
		}
	}
}
