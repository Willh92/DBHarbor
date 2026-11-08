package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dbharbor/backend/internal/crypto"

	"golang.org/x/crypto/bcrypt"
)

type AdminRecoveryRequest struct {
	AdminUsername string
	NewPassword   string // empty = keep current password; "auto" = generate 16-char random password
	Reset2FA      bool
	Source        string // "config.json" or "cli"
}

type AdminRecoveryResult struct {
	UserID              string
	Username            string
	CreatedNew          bool
	PasswordReset       bool
	IssuedBootstrapCode string
	Reset2FA            bool
	RevokedSessions     int
	BackupPath          string
}

const recoveryAutoPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*"

func generateRandomRecoveryPassword(length int) (string, error) {
	if length < 12 {
		length = 16
	}
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range raw {
		out[i] = recoveryAutoPasswordAlphabet[int(b)%len(recoveryAutoPasswordAlphabet)]
	}
	return string(out), nil
}

func (s *Store) backupDatabaseForRecovery() (string, error) {
	srcPath := filepath.Join(s.dataDir, "dbharbor.db")
	st, err := os.Stat(srcPath)
	if err != nil || st.Size() == 0 {
		return "", nil
	}
	backupDir := filepath.Join(s.dataDir, ".backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	bakName := fmt.Sprintf("dbharbor.db.pre-recovery-%s.bak", time.Now().UTC().Format("20060102T150405Z"))
	bakPath := filepath.Join(backupDir, bakName)
	escapedDest := strings.ReplaceAll(filepath.ToSlash(bakPath), "'", "''")
	if _, vacErr := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escapedDest)); vacErr == nil {
		_ = os.Chmod(bakPath, 0o600)
		pruneOldMigrationBackups(backupDir, maxRetainedMigrationBackups+3)
		return bakPath, nil
	}
	return "", nil
}

// ExecuteAdminRecovery performs an atomic emergency administrator recovery (password reset, 2FA reset,
// account unlock, and full session revocation) after snapshotting dbharbor.db into <DataDir>/.backups/.
func (s *Store) ExecuteAdminRecovery(req AdminRecoveryRequest) (AdminRecoveryResult, error) {
	var res AdminRecoveryResult

	targetUser := strings.TrimSpace(req.AdminUsername)
	rawPass := req.NewPassword
	trimmedPass := strings.TrimSpace(rawPass)

	if trimmedPass == "" && !req.Reset2FA {
		return res, errors.New("emergency recovery requires a new password (min 8 chars or 'auto') and/or reset2FA=true")
	}
	if targetUser != "" && !validUsernameRe.MatchString(targetUser) {
		return res, errors.New("recovery adminUsername must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'")
	}

	effectivePass := rawPass
	if strings.EqualFold(trimmedPass, "auto") {
		gen, err := generateRandomRecoveryPassword(16)
		if err != nil {
			return res, fmt.Errorf("failed to generate recovery password: %w", err)
		}
		effectivePass = gen
		res.IssuedBootstrapCode = gen
	} else if effectivePass != "" && len(effectivePass) < 8 {
		return res, errors.New("emergency recovery password must be at least 8 characters")
	}

	var passHash string
	if effectivePass != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(effectivePass), bcrypt.DefaultCost)
		if err != nil {
			return res, err
		}
		passHash = string(h)
	}

	if bak, err := s.backupDatabaseForRecovery(); err == nil {
		res.BackupPath = bak
	}

	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}

	var userID, resolvedUsername string
	if targetUser != "" {
		err = tx.QueryRow(`SELECT id, username FROM users WHERE LOWER(username) = LOWER(?) LIMIT 1`, targetUser).
			Scan(&userID, &resolvedUsername)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return res, err
		}
	} else {
		err = tx.QueryRow(`SELECT id, username FROM users WHERE role = ? ORDER BY created_at ASC LIMIT 1`, RoleAdmin).
			Scan(&userID, &resolvedUsername)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow(`SELECT id, username FROM users ORDER BY created_at ASC LIMIT 1`).
				Scan(&userID, &resolvedUsername)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return res, err
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if userID == "" {
		// No matching user exists yet — create a fresh primary administrator
		if passHash == "" {
			_ = tx.Rollback()
			return res, errors.New("no administrator account exists yet; please provide a new password (or 'auto') to create one")
		}
		if targetUser == "" {
			targetUser = "admin"
		}
		userID = crypto.NewUUID()
		resolvedUsername = targetUser
		if _, err := tx.Exec(
			`INSERT INTO users (id, username, display_name, password_hash, role, disabled, can_create_conn, conn_scope, totp_enabled, totp_secret_enc, totp_pending_enc, recovery_hashes_json, last_totp_step, password_changed, created_at)
			 VALUES (?, ?, '', ?, ?, 0, 1, 'all', 0, '', '', '[]', 0, 1, ?)`,
			userID, resolvedUsername, passHash, RoleAdmin, now,
		); err != nil {
			_ = tx.Rollback()
			return res, err
		}
		res.CreatedNew = true
		res.PasswordReset = true
		res.Reset2FA = req.Reset2FA
	} else {
		if passHash != "" && req.Reset2FA {
			_, err = tx.Exec(
				`UPDATE users SET password_hash = ?, password_changed = 1, role = ?, disabled = 0, can_create_conn = 1, conn_scope = 'all',
				 totp_enabled = 0, totp_secret_enc = '', totp_pending_enc = '', recovery_hashes_json = '[]', last_totp_step = 0
				 WHERE id = ?`,
				passHash, RoleAdmin, userID,
			)
			res.PasswordReset = true
			res.Reset2FA = true
		} else if passHash != "" {
			_, err = tx.Exec(
				`UPDATE users SET password_hash = ?, password_changed = 1, role = ?, disabled = 0, can_create_conn = 1, conn_scope = 'all'
				 WHERE id = ?`,
				passHash, RoleAdmin, userID,
			)
			res.PasswordReset = true
		} else if req.Reset2FA {
			_, err = tx.Exec(
				`UPDATE users SET role = ?, disabled = 0, can_create_conn = 1, conn_scope = 'all',
				 totp_enabled = 0, totp_secret_enc = '', totp_pending_enc = '', recovery_hashes_json = '[]', last_totp_step = 0
				 WHERE id = ?`,
				RoleAdmin, userID,
			)
			res.Reset2FA = true
		}
		if err != nil {
			_ = tx.Rollback()
			return res, err
		}
	}

	// Collect and revoke all active sessions for the recovered administrator
	var sessionIDs []string
	sRows, sErr := tx.Query(`SELECT id FROM auth_sessions WHERE user_id = ? AND revoked = 0`, userID)
	if sErr == nil {
		for sRows.Next() {
			var sid string
			if sRows.Scan(&sid) == nil {
				sessionIDs = append(sessionIDs, sid)
			}
		}
		_ = sRows.Close()
	}
	if revRes, revErr := tx.Exec(`UPDATE auth_sessions SET revoked = 1 WHERE user_id = ? AND revoked = 0`, userID); revErr == nil {
		aff, _ := revRes.RowsAffected()
		res.RevokedSessions = int(aff)
	}

	srcLabel := strings.TrimSpace(req.Source)
	if srcLabel == "" {
		srcLabel = "local-console"
	}
	var actions []string
	if res.CreatedNew {
		actions = append(actions, "created-admin")
	} else if res.PasswordReset {
		actions = append(actions, "password-reset")
	}
	if res.Reset2FA {
		actions = append(actions, "2fa-reset")
	}
	actions = append(actions, "unlocked", fmt.Sprintf("revoked-sessions=%d", res.RevokedSessions))

	_, _ = tx.Exec(
		`INSERT INTO security_audit_logs (id, event, username, ip, user_agent, detail, created_at)
		 VALUES (?, 'ADMIN_EMERGENCY_RECOVERY', ?, '127.0.0.1', ?, ?, ?)`,
		crypto.NewUUID(),
		resolvedUsername,
		"dbharbor-recovery/"+srcLabel,
		fmt.Sprintf("Emergency administrator recovery via %s (%s)", srcLabel, strings.Join(actions, ", ")),
		now,
	)

	if err := tx.Commit(); err != nil {
		return res, err
	}

	s.initialized.Store(true)
	s.refreshPrincipal(userID)
	if len(sessionIDs) > 0 {
		s.sessMu.Lock()
		for _, sid := range sessionIDs {
			delete(s.activeSessions, sid)
		}
		s.sessMu.Unlock()
	}

	res.UserID = userID
	res.Username = resolvedUsername
	return res, nil
}
