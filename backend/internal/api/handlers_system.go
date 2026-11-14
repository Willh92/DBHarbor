package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlite"
	"dbharbor/backend/internal/store"
	"dbharbor/backend/internal/version"
)

func (s *Server) handleGetVersion(c *Context) {
	info := version.Info()
	edition := info.Edition
	if s.desktop.Enabled {
		edition = "desktop"
	}
	lang := c.Query("lang")
	if lang == "" {
		lang = c.Request.Header.Get("Accept-Language")
	}
	resp := H{
		"version":           info.Version,
		"displayVersion":    info.DisplayVersion,
		"edition":           edition,
		"channel":           info.Channel,
		"authEnabled":       !s.desktop.Enabled,
		"license":           info.License,
		"gitCommit":         info.GitCommit,
		"buildTime":         info.BuildTime,
		"goVersion":         info.GoVersion,
		"os":                info.OS,
		"arch":              info.Arch,
		"repoUrl":           info.RepoURL,
		"gitUrl":            info.GitURL,
		"releasesUrl":       info.ReleasesURL,
		"embeddedChangelog": "",
	}
	if c.Query("includeChangelog") == "1" {
		resp["embeddedChangelog"] = version.EmbeddedChangelogForLocale(lang)
		resp["embeddedChangelogs"] = version.EmbeddedChangelogsMap()
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) handleGetChangelog(c *Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang == "" {
		lang = strings.TrimSpace(c.Request.Header.Get("Accept-Language"))
	}
	if lang == "" {
		lang = "en-US"
	}
	c.JSON(http.StatusOK, H{
		"lang":     lang,
		"markdown": version.GetChangelog(lang),
	})
}

func (s *Server) handleCheckUpdate(c *Context) {
	force := c.Query("force") == "true" || c.Query("force") == "1"
	lang := c.Query("lang")
	if lang == "" {
		lang = c.Request.Header.Get("Accept-Language")
	}
	channel := c.Query("channel")
	res := s.updater.CheckWithOptions(c.Request.Context(), force, lang, channel)
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleGetCaptcha(c *Context) {
	tok, img, err := s.protector.GenerateCaptcha()
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to generate CAPTCHA"})
		return
	}
	c.JSON(http.StatusOK, H{
		"captchaToken": tok,
		"captchaImage": img,
	})
}

func (s *Server) handleGetAuthStatus(c *Context) {
	needsSetup := !s.desktop.Enabled && s.store.NeedsSetup()
	c.JSON(http.StatusOK, H{
		"authEnabled": !s.desktop.Enabled,
		"initialized": !needsSetup,
		"needsSetup":  needsSetup,
		"branding":    s.store.GetLoginBranding(),
	})
}

func (s *Server) handleSetupInitialAdmin(c *Context) {
	if !s.store.NeedsSetup() {
		c.JSON(http.StatusForbidden, H{"error": "Initial administrator has already been configured"})
		return
	}
	var req struct {
		Username    string                     `json:"username"`
		DisplayName string                     `json:"displayName"`
		Password    string                     `json:"password"`
		RememberMe  bool                       `json:"rememberMe"`
		Branding    *store.LoginBrandingConfig `json:"branding"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid setup parameters"})
		return
	}

	user, err := s.store.SetupInitialAdmin(req.Username, req.DisplayName, req.Password, req.Branding)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyInitialized) {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}

	ip := c.ClientIP()
	ua := c.Request.UserAgent()
	ttl := 24 * time.Hour
	if req.RememberMe {
		ttl = 7 * 24 * time.Hour
	}
	sess, err := s.store.CreateSession(user.ID, ip, ua, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create authenticated session"})
		return
	}
	token, err := s.tokens.IssueSessionToken(user.ID, user.Username, sess.ID, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to issue session token"})
		return
	}
	s.store.AddAuditLog("ADMIN_INITIALIZED", user.Username, ip, ua, "Initialized primary administrator account via first-run setup")
	c.JSON(http.StatusOK, H{
		"token":           token,
		"username":        user.Username,
		"displayName":     user.DisplayName,
		"userId":          user.ID,
		"role":            user.Role,
		"canCreateConn":   user.CanCreateConn,
		"authEnabled":     true,
		"totpEnabled":     false,
		"passwordChanged": true,
		"branding":        s.store.GetLoginBranding(),
	})
}

func (s *Server) handleLogin(c *Context) {
	if s.store.NeedsSetup() {
		c.JSON(http.StatusForbidden, H{
			"error":      "Initial administrator setup required",
			"needsSetup": true,
		})
		return
	}
	var req struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		CaptchaToken  string `json:"captchaToken"`
		CaptchaAnswer string `json:"captchaAnswer"`
		RememberMe    bool   `json:"rememberMe"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	ip := c.ClientIP()
	ua := c.Request.UserAgent()

	chk := s.protector.Check(ip, req.Username)
	if chk.Locked {
		s.store.AddAuditLog("LOGIN_LOCKED", req.Username, ip, ua, "Blocked during temporary brute-force lockout window")
		c.Header("Retry-After", strconv.Itoa(chk.RetryAfterSec))
		c.JSON(http.StatusTooManyRequests, H{
			"error":         fmt.Sprintf("Too many attempts; please retry in %d seconds", chk.RetryAfterSec),
			"locked":        true,
			"retryAfterSec": chk.RetryAfterSec,
		})
		return
	}

	if chk.RequiresCaptcha {
		if !s.protector.VerifyCaptcha(req.CaptchaToken, req.CaptchaAnswer) {
			nextChk := s.protector.RecordFailure(ip, req.Username)
			if nextChk.Locked {
				s.store.AddAuditLog("LOGIN_LOCKED", req.Username, ip, ua, "Repeated CAPTCHA or login failures triggered security lockout")
				c.Header("Retry-After", strconv.Itoa(nextChk.RetryAfterSec))
				c.JSON(http.StatusTooManyRequests, H{
					"error":         fmt.Sprintf("Too many failed attempts; please retry in %d seconds", nextChk.RetryAfterSec),
					"locked":        true,
					"retryAfterSec": nextChk.RetryAfterSec,
				})
				return
			}
			cTok, cImg, _ := s.protector.GenerateCaptcha()
			c.JSON(http.StatusUnauthorized, H{
				"error":           "Please enter the valid verification code",
				"requiresCaptcha": true,
				"captchaToken":    cTok,
				"captchaImage":    cImg,
			})
			return
		}
	}

	user, err := s.store.Authenticate(req.Username, req.Password)
	if err != nil {
		nextChk := s.protector.RecordFailure(ip, req.Username)
		s.store.AddAuditLog("LOGIN_FAILED", req.Username, ip, ua, err.Error())
		if nextChk.Locked {
			c.Header("Retry-After", strconv.Itoa(nextChk.RetryAfterSec))
			c.JSON(http.StatusTooManyRequests, H{
				"error":         fmt.Sprintf("Too many failed attempts; please retry in %d seconds", nextChk.RetryAfterSec),
				"locked":        true,
				"retryAfterSec": nextChk.RetryAfterSec,
			})
			return
		}
		resp := H{
			"error":           err.Error(),
			"requiresCaptcha": nextChk.RequiresCaptcha,
		}
		if nextChk.RequiresCaptcha {
			cTok, cImg, _ := s.protector.GenerateCaptcha()
			resp["captchaToken"] = cTok
			resp["captchaImage"] = cImg
		}
		c.JSON(http.StatusUnauthorized, resp)
		return
	}

	if user.TOTPEnabled {
		challengeToken, err := s.tokens.Issue2FAChallengeToken(user.ID, user.Username, 5*time.Minute)
		if err != nil {
			c.JSON(http.StatusInternalServerError, H{"error": "Failed to issue 2FA challenge token"})
			return
		}
		s.store.AddAuditLog("LOGIN_2FA_CHALLENGE", user.Username, ip, ua, "Password verified; awaiting second-factor (2FA) verification")
		c.JSON(http.StatusOK, H{
			"requires2FA":    true,
			"challengeToken": challengeToken,
		})
		return
	}

	s.protector.RecordSuccess(ip, user.Username)
	ttl := 24 * time.Hour
	if req.RememberMe {
		ttl = 7 * 24 * time.Hour
	}
	sess, err := s.store.CreateSession(user.ID, ip, ua, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create authenticated session"})
		return
	}
	token, err := s.tokens.IssueSessionToken(user.ID, user.Username, sess.ID, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to issue session token"})
		return
	}
	s.store.AddAuditLog("LOGIN_SUCCESS", user.Username, ip, ua, "Signed in successfully")
	c.JSON(http.StatusOK, H{
		"token":           token,
		"username":        user.Username,
		"displayName":     user.DisplayName,
		"userId":          user.ID,
		"role":            user.Role,
		"canCreateConn":   user.CanCreateConn,
		"authEnabled":     true,
		"totpEnabled":     false,
		"passwordChanged": user.PasswordChanged,
	})
}

func (s *Server) handleLogin2FA(c *Context) {
	var req struct {
		ChallengeToken string `json:"challengeToken"`
		Code           string `json:"code"`
		RememberMe     bool   `json:"rememberMe"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ChallengeToken) == "" || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Please provide a valid 6-digit TOTP code or 8-character recovery code"})
		return
	}

	claims, err := s.tokens.ParseTokenWithPurpose(req.ChallengeToken, auth.Purpose2FAChallenge)
	if err != nil {
		c.JSON(http.StatusUnauthorized, H{"error": "2FA verification session expired; please sign in again"})
		return
	}
	if s.tokens.Is2FAChallengeConsumed(req.ChallengeToken) {
		c.JSON(http.StatusUnauthorized, H{"error": "2FA verification challenge has expired or exceeded maximum attempts; please sign in again"})
		return
	}
	if p, exists := s.store.GetPrincipal(claims.UserID); !exists || p.Disabled {
		c.JSON(http.StatusUnauthorized, H{"error": "Account has been disabled"})
		return
	}

	ip := c.ClientIP()
	ua := c.Request.UserAgent()
	chk := s.protector.Check(ip, claims.Username)
	if chk.Locked {
		c.Header("Retry-After", strconv.Itoa(chk.RetryAfterSec))
		c.JSON(http.StatusTooManyRequests, H{
			"error":         fmt.Sprintf("Too many verification code attempts; please retry in %d seconds", chk.RetryAfterSec),
			"locked":        true,
			"retryAfterSec": chk.RetryAfterSec,
		})
		return
	}

	usedRecovery, err := s.store.Verify2FA(claims.UserID, req.Code)
	if err != nil {
		exhausted := s.tokens.Record2FAChallengeAttempt(req.ChallengeToken, false)
		nextChk := s.protector.RecordFailure(ip, claims.Username)
		s.store.AddAuditLog("LOGIN_2FA_FAILED", claims.Username, ip, ua, err.Error())
		if nextChk.Locked {
			c.Header("Retry-After", strconv.Itoa(nextChk.RetryAfterSec))
			c.JSON(http.StatusTooManyRequests, H{
				"error":         fmt.Sprintf("Repeated 2FA failures; temporarily locked for %d seconds", nextChk.RetryAfterSec),
				"locked":        true,
				"retryAfterSec": nextChk.RetryAfterSec,
			})
			return
		}
		if exhausted {
			c.JSON(http.StatusUnauthorized, H{
				"error": "Too many invalid 2FA attempts for this challenge; please sign in again",
			})
			return
		}
		c.JSON(http.StatusUnauthorized, H{
			"error": err.Error(),
		})
		return
	}

	s.tokens.Record2FAChallengeAttempt(req.ChallengeToken, true)

	s.protector.RecordSuccess(ip, claims.Username)
	ttl := 24 * time.Hour
	if req.RememberMe {
		ttl = 7 * 24 * time.Hour
	}
	sess, err := s.store.CreateSession(claims.UserID, ip, ua, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create authenticated session"})
		return
	}
	token, err := s.tokens.IssueSessionToken(claims.UserID, claims.Username, sess.ID, ttl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to issue session token"})
		return
	}

	secStatus, _ := s.store.GetSecurityStatus(claims.UserID)
	detail := "TOTP second-factor verification passed"
	if usedRecovery {
		detail = fmt.Sprintf("Signed in with backup recovery code (%d remaining)", secStatus.RecoveryCodesRemaining)
	}
	s.store.AddAuditLog("LOGIN_2FA_SUCCESS", claims.Username, ip, ua, detail)

	c.JSON(http.StatusOK, H{
		"token":                  token,
		"username":               claims.Username,
		"displayName":            secStatus.DisplayName,
		"userId":                 claims.UserID,
		"role":                   secStatus.Role,
		"canCreateConn":          secStatus.CanCreateConn,
		"authEnabled":            true,
		"totpEnabled":            true,
		"usedRecoveryCode":       usedRecovery,
		"recoveryCodesRemaining": secStatus.RecoveryCodesRemaining,
		"passwordChanged":        secStatus.PasswordChanged,
	})
}

func (s *Server) handleLogout(c *Context) {
	sid := c.SessionID()
	if sid != "" {
		_ = s.store.RevokeSession(c.UserID(), sid)
		s.tokens.RevokeSessionInMemory(sid, 0)
		s.clearSessionByID(sid)
	}
	s.store.AddAuditLog("LOGOUT", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Signed out explicitly")
	c.JSON(http.StatusOK, H{"message": "Signed out successfully"})
}

func (s *Server) handleIssueExportTicket(c *Context) {
	ticket, err := s.tokens.IssueExportTicket(c.UserID(), c.Username(), c.SessionID(), 90*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to issue export download ticket"})
		return
	}
	c.JSON(http.StatusOK, H{"ticket": ticket})
}

func (s *Server) handleMe(c *Context) {
	sec, err := s.store.GetSecurityStatus(c.UserID())
	if err != nil {
		c.JSON(http.StatusOK, H{
			"userId":        c.UserID(),
			"username":      c.Username(),
			"displayName":   c.Principal.DisplayName,
			"role":          c.Role(),
			"canCreateConn": c.Principal.CanCreateConn,
			"authEnabled":   true,
		})
		return
	}
	c.JSON(http.StatusOK, H{
		"userId":                 c.UserID(),
		"username":               c.Username(),
		"displayName":            sec.DisplayName,
		"role":                   sec.Role,
		"canCreateConn":          sec.CanCreateConn,
		"authEnabled":            true,
		"totpEnabled":            sec.TOTPEnabled,
		"passwordChanged":        sec.PasswordChanged,
		"recoveryCodesRemaining": sec.RecoveryCodesRemaining,
	})
}

func (s *Server) handleGetSecurityStatus(c *Context) {
	sec, err := s.store.GetSecurityStatus(c.UserID())
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sec)
}

func (s *Server) handleChangePassword(c *Context) {
	var req struct {
		OldPassword         string `json:"oldPassword"`
		NewPassword         string `json:"newPassword"`
		RevokeOtherSessions bool   `json:"revokeOtherSessions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, H{"error": "New password must be at least 8 characters"})
		return
	}
	if err := s.store.UpdatePassword(c.UserID(), req.OldPassword, req.NewPassword); err != nil {
		s.store.AddAuditLog("PASSWORD_CHANGE_FAILED", c.Username(), c.ClientIP(), c.Request.UserAgent(), err.Error())
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if s.protector != nil {
		s.protector.ResetUser(c.Username())
	}
	revokedCnt := 0
	if req.RevokeOtherSessions {
		sids, _ := s.store.RevokeOtherSessionsWithIDs(c.UserID(), c.SessionID())
		revokedCnt = len(sids)
		for _, sid := range sids {
			if s.tokens != nil {
				s.tokens.RevokeSessionInMemory(sid, 0)
			}
			s.clearSessionByID(sid)
		}
	}
	s.store.AddAuditLog("PASSWORD_CHANGED", c.Username(), c.ClientIP(), c.Request.UserAgent(), fmt.Sprintf("Password updated; revoked %d other session(s)", revokedCnt))
	c.JSON(http.StatusOK, H{"message": "Password updated successfully", "revokedSessions": revokedCnt})
}

func (s *Server) handleBegin2FASetup(c *Context) {
	secret, uri, qrURL, err := s.store.BeginTOTPSetup(c.UserID())
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{
		"secret":        secret,
		"otpauthUri":    uri,
		"qrCodeDataUrl": qrURL,
	})
}

func (s *Server) handleEnable2FA(c *Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Please enter the 6-digit code from your authenticator app"})
		return
	}
	codes, err := s.store.ConfirmTOTPSetup(c.UserID(), req.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog("2FA_ENABLED", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Enabled TOTP 2FA and generated backup recovery codes")
	c.JSON(http.StatusOK, H{
		"message":       "Two-factor authentication (2FA) enabled successfully",
		"recoveryCodes": codes,
	})
}

func (s *Server) handleDisable2FA(c *Context) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Disabling 2FA requires both your account password and a valid TOTP/recovery code"})
		return
	}
	if err := s.store.DisableTOTP(c.UserID(), req.Password, req.Code); err != nil {
		s.store.AddAuditLog("2FA_DISABLE_FAILED", c.Username(), c.ClientIP(), c.Request.UserAgent(), err.Error())
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog("2FA_DISABLED", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Disabled TOTP two-factor authentication")
	c.JSON(http.StatusOK, H{"message": "Two-factor authentication disabled"})
}

func (s *Server) handleRegenerateRecoveryCodes(c *Context) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Regenerating recovery codes requires your current password and 2FA code"})
		return
	}
	codes, err := s.store.RegenerateRecoveryCodes(c.UserID(), req.Password, req.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddAuditLog("2FA_RECOVERY_REGEN", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Regenerated 8 single-use backup recovery codes")
	c.JSON(http.StatusOK, H{"recoveryCodes": codes})
}

func (s *Server) handleListSessions(c *Context) {
	list, err := s.store.ListSessions(c.UserID(), c.SessionID())
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"sessions": list})
}

func (s *Server) handleRevokeSession(c *Context) {
	sid := c.Param("sid")
	if sid == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Session ID is required"})
		return
	}
	if err := s.store.RevokeSession(c.UserID(), sid); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	s.tokens.RevokeSessionInMemory(sid, 0)
	s.clearSessionByID(sid)
	s.store.AddAuditLog("SESSION_REVOKED", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Revoked session "+sid)
	c.JSON(http.StatusOK, H{"message": "Session revoked"})
}

func (s *Server) handleRevokeOtherSessions(c *Context) {
	sids, err := s.store.RevokeOtherSessionsWithIDs(c.UserID(), c.SessionID())
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	for _, sid := range sids {
		if s.tokens != nil {
			s.tokens.RevokeSessionInMemory(sid, 0)
		}
		s.clearSessionByID(sid)
	}
	cnt := len(sids)
	s.store.AddAuditLog("SESSIONS_REVOKED_OTHERS", c.Username(), c.ClientIP(), c.Request.UserAgent(), fmt.Sprintf("Revoked %d other active session(s)", cnt))
	c.JSON(http.StatusOK, H{"revoked": cnt, "message": fmt.Sprintf("Revoked %d other device session(s)", cnt)})
}

func (s *Server) handleListAuditLogs(c *Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "80"))
	filter := store.AuditLogFilter{
		Username:  strings.TrimSpace(c.Query("username")),
		EventType: strings.TrimSpace(c.Query("eventType")),
		Keyword:   strings.TrimSpace(c.Query("keyword")),
		Limit:     limit,
	}
	if !c.IsAdmin() {
		filter.Username = c.Username()
	}
	logs, err := s.store.ListAuditLogsFiltered(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"logs": logs})
}

func (s *Server) handleSystemStats(c *Context) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	pStats := s.pool.Stats()
	c.JSON(http.StatusOK, H{
		"version":     version.DisplayTag(version.Version),
		"allocMB":     float64(ms.Alloc) / 1024 / 1024,
		"sysMB":       float64(ms.Sys) / 1024 / 1024,
		"heapObjects": ms.HeapObjects,
		"goroutines":  runtime.NumGoroutine(),
		"uptimeSec":   int64(time.Since(s.startedAt).Seconds()),
		"goVersion":   runtime.Version(),
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"activePools": pStats.ActivePools,
		"maxPools":    pStats.MaxPools,
	})
}

func (s *Server) handleReleaseIdlePools(c *Context) {
	if !c.RequireAdmin() {
		return
	}
	released := s.pool.ReleaseIdle()
	s.metaCacheMu.Lock()
	s.metaCache = make(map[string]cachedAutocomplete)
	s.metaCacheMu.Unlock()
	runtime.GC()
	c.JSON(http.StatusOK, H{
		"released": released,
		"message":  fmt.Sprintf("Released %d active connection pool(s)", released),
	})
}

func (s *Server) handleListDrivers(c *Context) {
	metas := driver.ListMetas()
	if s.desktop.Enabled {
		c.JSON(http.StatusOK, H{"drivers": metas})
		return
	}
	filtered := make([]driver.DriverMeta, len(metas))
	copy(filtered, metas)
	for i := range filtered {
		if filtered[i].Type == "duckdb" && len(filtered[i].Fields) > 0 {
			fields := make([]driver.ConnFieldSchema, 0, len(filtered[i].Fields))
			for _, f := range filtered[i].Fields {
				if f.Key == "cliPath" {
					continue
				}
				fields = append(fields, f)
			}
			filtered[i].Fields = fields
		}
	}
	c.JSON(http.StatusOK, H{"drivers": filtered})
}

func (s *Server) handleListConnections(c *Context) {
	list, err := s.store.ListConnectionsForPrincipal(c.Principal)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	list = s.annotateConnectionsUnlockState(c, list)
	c.JSON(http.StatusOK, H{"connections": list})
}

// tlsSecurityLevel grades transport security from 0 (plaintext) -> 1 (opportunistic) ->
// 2 (encrypted without strict certificate/hostname verification) -> 3 (strictly verified TLS),
// ensuring any downgrade requires re-entering connection credentials.
func tlsSecurityLevel(cfg driver.ConnectionConfig) int {
	level := 0
	promote := func(v int) {
		if v > level {
			level = v
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Option("tls", ""))) {
	case "true", "1", "custom", "verified":
		promote(3)
	case "skip-verify":
		promote(2)
	case "preferred":
		promote(1)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Option("sslmode", ""))) {
	case "verify-full", "verify-ca":
		promote(3)
	case "require":
		promote(2)
	case "prefer", "allow":
		promote(1)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Option("encrypt", ""))) {
	case "verified", "strict":
		promote(3)
	case "true", "1", "mandatory":
		promote(2)
	}

	rawURI := strings.TrimSpace(cfg.URI)
	if rawURI != "" {
		lowURI := strings.ToLower(rawURI)
		uriLevel := 0
		if strings.HasPrefix(lowURI, "mongodb+srv://") || strings.HasPrefix(lowURI, "rediss://") {
			if !strings.Contains(lowURI, "tls=false") && !strings.Contains(lowURI, "ssl=false") {
				uriLevel = 3
			}
		} else if strings.Contains(lowURI, "tls=true") || strings.Contains(lowURI, "ssl=true") ||
			strings.Contains(lowURI, "tls=1") || strings.Contains(lowURI, "ssl=1") {
			uriLevel = 3
		}
		if u, err := url.Parse(store.StripPasswordFromURI(rawURI)); err == nil {
			q := u.Query()
			for _, k := range []string{"tls", "ssl"} {
				switch strings.ToLower(strings.TrimSpace(q.Get(k))) {
				case "true", "1":
					if uriLevel < 3 {
						uriLevel = 3
					}
				case "false", "0":
					uriLevel = 0
				}
			}
			switch strings.ToLower(strings.TrimSpace(q.Get("sslmode"))) {
			case "verify-full", "verify-ca":
				if uriLevel < 3 {
					uriLevel = 3
				}
			case "require":
				if uriLevel < 2 {
					uriLevel = 2
				}
			case "prefer", "allow":
				if uriLevel < 1 {
					uriLevel = 1
				}
			}
			for _, k := range []string{"tlsinsecure", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames", "insecure"} {
				for qk, qvs := range q {
					if strings.EqualFold(strings.TrimSpace(qk), k) && len(qvs) > 0 {
						v := strings.ToLower(strings.TrimSpace(qvs[0]))
						if (v == "true" || v == "1") && uriLevel > 2 {
							uriLevel = 2
						}
					}
				}
			}
		}
		promote(uriLevel)
	}
	return level
}

func sameEndpointAndSecurity(oldCfg, newCfg driver.ConnectionConfig) bool {
	if !strings.EqualFold(strings.TrimSpace(oldCfg.Type), strings.TrimSpace(newCfg.Type)) {
		return false
	}
	if strings.TrimSpace(oldCfg.Host) != strings.TrimSpace(newCfg.Host) || oldCfg.Port != newCfg.Port {
		return false
	}
	oldURI := strings.TrimSpace(oldCfg.URI)
	newURI := strings.TrimSpace(newCfg.URI)
	if oldURI == "" && newURI != "" {
		return false
	}
	if oldURI != "" && newURI != "" {
		if store.StripPasswordFromURI(oldURI) != store.StripPasswordFromURI(newURI) {
			return false
		}
	}
	effNew := newCfg
	if strings.TrimSpace(effNew.URI) == "" && oldURI != "" {
		effNew.URI = oldURI
	}
	if tlsSecurityLevel(effNew) < tlsSecurityLevel(oldCfg) {
		return false
	}
	return true
}

func (s *Server) handleTestConnection(c *Context) {
	var cfg driver.ConnectionConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid connection parameters"})
		return
	}
	if cfg.ID != "" {
		if !s.store.CanEditConnection(c.Principal, cfg.ID) {
			c.JSON(http.StatusForbidden, H{"error": "Permission denied: cannot test or modify this connection"})
			return
		}
		if cfg.Password == "" || strings.TrimSpace(cfg.URI) == "" {
			if existing, err := s.store.GetDecryptedConnection(cfg.ID); err == nil {
				// Prevent stored credential exfiltration if target host/port/URI was altered or TLS was downgraded without re-entering password
				if sameEndpointAndSecurity(existing, cfg) {
					cred, hasCred := s.getSessionCred(s.credSessionKey(c), cfg.ID)
					typedNewPass := cfg.Password != ""
					typedRawURI := strings.TrimSpace(cfg.URI)
					typedURIHasSecret := typedRawURI != "" && typedRawURI != store.StripPasswordFromURI(typedRawURI)

					if !typedNewPass && !typedURIHasSecret {
						if hasCred && cred.Password != "" {
							cfg.Password = cred.Password
						} else if hasCred && cred.URI != "" {
							cfg.Password = store.ExtractPasswordFromURI(cred.URI)
						} else if existing.Password != "" {
							cfg.Password = existing.Password
						} else if existing.URI != "" {
							cfg.Password = store.ExtractPasswordFromURI(existing.URI)
						}
						if typedRawURI != "" {
							cfg.URI = store.InjectCredentialsIntoURI(typedRawURI, cfg.Username, cfg.Password)
						} else {
							baseURI := ""
							if hasCred && cred.URI != "" {
								baseURI = cred.URI
							} else if existing.URI != "" {
								baseURI = existing.URI
							}
							if baseURI != "" {
								cfg.URI = store.InjectCredentialsIntoURI(baseURI, cfg.Username, cfg.Password)
							}
						}
					} else if typedNewPass {
						if typedRawURI == "" {
							baseURI := existing.URI
							if baseURI == "" && hasCred && cred.URI != "" {
								baseURI = cred.URI
							}
							if baseURI != "" {
								cfg.URI = store.InjectCredentialsIntoURI(store.StripPasswordFromURI(baseURI), cfg.Username, cfg.Password)
							}
						} else if !typedURIHasSecret {
							cfg.URI = store.InjectCredentialsIntoURI(typedRawURI, cfg.Username, cfg.Password)
						}
					}
				}
			}
		}
	} else if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to create new connections"})
		return
	}
	cfg.Options = store.StripReservedConnectionOptions(cfg.Options)
	if !s.desktop.Enabled && !s.opts.AllowPrivateNetForNonAdmin && !c.IsAdmin() {
		if cfg.Options == nil {
			cfg.Options = make(map[string]string, 1)
		}
		cfg.Options["__restrict_internal_ip"] = "1"
	}
	cfg.FilePath = s.normalizeConnectionFilePathForWebUser(c, cfg.FilePath)
	if status, errMsg := s.validateConnectionFilePath(c, cfg.Type, cfg.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if status, errMsg := s.validateRemoteEndpointForWebUser(c, cfg); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if !s.desktop.Enabled {
		cfg = s.prepareWebConnectionConfig(cfg)
	}

	drv, err := driver.Create(cfg)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	defer drv.Close()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := drv.Ping(ctx)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Connection failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

func (s *Server) handlePingSavedConnection(c *Context) {
	id := c.Param("id")
	if !s.authorizeConn(c, id, false) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, id)
	if err != nil {
		s.respondConnError(c, id, cfg.Type, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := drv.Ping(ctx)
	if err != nil {
		if scope := s.credSessionKey(c); scope != "" && s.hasSessionCred(scope, id) {
			s.pool.Evict(id + "::" + scope)
		} else {
			s.pool.Evict(id)
		}
		s.respondConnError(c, id, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, info)
}

func (s *Server) handleCreateConnection(c *Context) {
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to create new connections"})
		return
	}
	var req struct {
		driver.ConnectionConfig
		Visibility string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid connection payload"})
		return
	}
	if req.RememberPassword == nil && driver.IsCredentialedDriver(req.Type) {
		// New credentialed connections default to RememberPassword = false (session-only password).
		req.RememberPassword = driver.BoolPtr(false)
	}
	req.Options = store.StripReservedConnectionOptions(req.Options)
	req.FilePath = s.normalizeConnectionFilePathForWebUser(c, req.FilePath)
	if status, errMsg := s.validateConnectionFilePath(c, req.Type, req.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if status, errMsg := s.validateRemoteEndpointForWebUser(c, req.ConnectionConfig); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if !s.desktop.Enabled && req.Options != nil {
		delete(req.Options, "cliPath")
	}
	req.ConnectionConfig.ID = ""
	rawPassword := req.Password
	rawURI := strings.TrimSpace(req.URI)
	ownerID := c.UserID()
	vis := "private"
	if s.desktop.Enabled {
		ownerID = ""
		vis = "shared"
	} else if c.IsAdmin() {
		if req.Visibility == "private" {
			vis = "private"
		} else {
			vis = "shared"
		}
	}
	saved, err := s.store.SaveConnectionWithMeta(req.ConnectionConfig, false, ownerID, vis)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	if !saved.RememberPassword && (rawPassword != "" || (rawURI != "" && rawURI != store.StripPasswordFromURI(rawURI))) {
		fullURI := rawURI
		if fullURI != "" && rawPassword != "" && fullURI == store.StripPasswordFromURI(fullURI) {
			fullURI = store.InjectCredentialsIntoURI(fullURI, saved.Username, rawPassword)
		}
		effPass := rawPassword
		if effPass == "" {
			effPass = store.ExtractPasswordFromURI(fullURI)
		}
		s.setSessionCred(s.credSessionKey(c), saved.ID, sessionConnCred{
			Username:  saved.Username,
			Password:  effPass,
			URI:       fullURI,
			UpdatedAt: time.Now(),
		})
	}
	saved = s.annotateSavedConnectionUnlock(c, saved)
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleUpdateConnection(c *Context) {
	id := c.Param("id")
	if !s.store.CanEditConnection(c.Principal, id) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can modify this connection"})
		return
	}
	existingCfg, existingErr := s.store.GetDecryptedConnection(id)
	if existingErr != nil {
		c.JSON(http.StatusNotFound, H{"error": existingErr.Error()})
		return
	}
	if store.IsEphemeralConnection(existingCfg.Options) {
		c.JSON(http.StatusForbidden, H{"error": "Temporary browser-loaded database sessions cannot be reconfigured"})
		return
	}
	var req struct {
		driver.ConnectionConfig
		KeepExistingSecret bool   `json:"keepExistingSecret"`
		Visibility         string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid connection payload"})
		return
	}
	req.Options = store.StripReservedConnectionOptions(req.Options)
	req.FilePath = s.normalizeConnectionFilePathForWebUser(c, req.FilePath)
	if status, errMsg := s.validateConnectionFilePath(c, req.Type, req.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if status, errMsg := s.validateRemoteEndpointForWebUser(c, req.ConnectionConfig); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	if !s.desktop.Enabled && req.Options != nil {
		delete(req.Options, "cliPath")
	}

	rawPassword := req.Password
	rawURI := strings.TrimSpace(req.URI)
	uriHasEmbeddedSecret := rawURI != "" && rawURI != store.StripPasswordFromURI(rawURI)

	sameEndpoint := sameEndpointAndSecurity(existingCfg, req.ConnectionConfig)
	if !sameEndpoint {
		// Prevent exfiltrating stored credentials by editing host/port/URI or downgrading TLS without re-typing credentials.
		req.KeepExistingSecret = false
	}

	scope := s.credSessionKey(c)
	prevSessionCred, hadSessionCred := s.getSessionCred(scope, id)

	wantRemember := true
	if driver.IsCredentialedDriver(req.Type) {
		if req.RememberPassword != nil {
			wantRemember = *req.RememberPassword
		} else {
			wantRemember = existingCfg.ShouldRememberPassword()
		}
	}
	req.RememberPassword = driver.BoolPtr(wantRemember)

	// If switching RememberPassword false -> true while the current session already holds the unlocked secret,
	// promote the active session secret onto disk when the user left the password field blank.
	if wantRemember && rawPassword == "" && !uriHasEmbeddedSecret && req.KeepExistingSecret && sameEndpoint && !existingCfg.ShouldRememberPassword() && hadSessionCred {
		effPass := prevSessionCred.Password
		if effPass == "" && prevSessionCred.URI != "" {
			effPass = store.ExtractPasswordFromURI(prevSessionCred.URI)
		}
		req.Password = effPass
		if rawURI != "" {
			req.URI = store.InjectCredentialsIntoURI(rawURI, req.Username, effPass)
		} else if prevSessionCred.URI != "" {
			req.URI = store.InjectCredentialsIntoURI(prevSessionCred.URI, req.Username, effPass)
		}
	} else if wantRemember && rawURI != "" && !uriHasEmbeddedSecret {
		if rawPassword != "" {
			req.URI = store.InjectCredentialsIntoURI(rawURI, req.Username, rawPassword)
		} else if req.KeepExistingSecret && sameEndpoint && existingCfg.ShouldRememberPassword() {
			effOldPass := existingCfg.Password
			if effOldPass == "" && existingCfg.URI != "" {
				effOldPass = store.ExtractPasswordFromURI(existingCfg.URI)
			}
			if effOldPass != "" {
				req.URI = store.InjectCredentialsIntoURI(rawURI, req.Username, effOldPass)
			}
		}
	}

	req.ConnectionConfig.ID = id
	vis := ""
	ownerOverride := ""
	if s.desktop.Enabled {
		vis = "shared"
	} else if c.IsAdmin() {
		if req.Visibility == "private" || req.Visibility == "shared" {
			vis = req.Visibility
		}
		if vis == "shared" && !s.store.IsConnectionOwnedByAdmin(id) {
			ownerOverride = c.UserID()
		}
	} else {
		vis = "private"
	}
	saved, err := s.store.SaveConnectionWithMeta(req.ConnectionConfig, req.KeepExistingSecret, ownerOverride, vis)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}

	if saved.RememberPassword {
		s.clearConnectionSessionCreds(id)
	} else {
		if !sameEndpoint {
			s.clearConnectionSessionCreds(id)
		}
		if rawPassword != "" || uriHasEmbeddedSecret {
			fullURI := rawURI
			if fullURI == "" && existingCfg.URI != "" && sameEndpoint {
				fullURI = store.InjectCredentialsIntoURI(existingCfg.URI, saved.Username, rawPassword)
			} else if fullURI != "" && rawPassword != "" && !uriHasEmbeddedSecret {
				fullURI = store.InjectCredentialsIntoURI(fullURI, saved.Username, rawPassword)
			}
			effPass := rawPassword
			if effPass == "" {
				effPass = store.ExtractPasswordFromURI(fullURI)
			}
			s.setSessionCred(scope, id, sessionConnCred{
				Username:  saved.Username,
				Password:  effPass,
				URI:       fullURI,
				UpdatedAt: time.Now(),
			})
		} else if req.KeepExistingSecret && sameEndpoint {
			if hadSessionCred {
				updatedCred := prevSessionCred
				updatedCred.Username = saved.Username
				if updatedCred.Password == "" && updatedCred.URI != "" {
					updatedCred.Password = store.ExtractPasswordFromURI(updatedCred.URI)
				}
				if rawURI != "" {
					updatedCred.URI = store.InjectCredentialsIntoURI(rawURI, saved.Username, updatedCred.Password)
				} else if updatedCred.URI != "" {
					updatedCred.URI = store.InjectCredentialsIntoURI(updatedCred.URI, saved.Username, updatedCred.Password)
				}
				updatedCred.UpdatedAt = time.Now()
				s.setSessionCred(scope, id, updatedCred)
			} else if existingCfg.ShouldRememberPassword() && (existingCfg.Password != "" || (existingCfg.URI != "" && existingCfg.URI != store.StripPasswordFromURI(existingCfg.URI))) {
				// Turning OFF Remember Password on an existing remembered connection wipes disk secret immediately
				// while preserving the active session's credential until logout/next login.
				effPass := existingCfg.Password
				if effPass == "" {
					effPass = store.ExtractPasswordFromURI(existingCfg.URI)
				}
				fullURI := existingCfg.URI
				if rawURI != "" {
					fullURI = store.InjectCredentialsIntoURI(rawURI, saved.Username, effPass)
				} else if fullURI != "" && effPass != "" && fullURI == store.StripPasswordFromURI(fullURI) {
					fullURI = store.InjectCredentialsIntoURI(fullURI, saved.Username, effPass)
				}
				s.setSessionCred(scope, id, sessionConnCred{
					Username:  saved.Username,
					Password:  effPass,
					URI:       fullURI,
					UpdatedAt: time.Now(),
				})
			}
		} else {
			s.clearSessionCred(scope, id)
		}
	}

	s.pool.EvictConnectionAll(id)
	s.invalidateConnCache(id)
	saved = s.annotateSavedConnectionUnlock(c, saved)
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleDuplicateConnection(c *Context) {
	id := c.Param("id")
	if !s.authorizeConn(c, id, false) {
		return
	}
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to duplicate or create connections"})
		return
	}
	if !s.store.CanEditConnection(c.Principal, id) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can duplicate this connection"})
		return
	}
	srcCfg, srcErr := s.store.GetDecryptedConnection(id)
	if srcErr != nil {
		c.JSON(http.StatusNotFound, H{"error": srcErr.Error()})
		return
	}
	srcSaved, hasSrcSaved := s.store.GetSavedConnectionForPrincipal(c.Principal, id)
	if !s.desktop.Enabled && !c.IsAdmin() {
		srcFilePath := s.normalizeConnectionFilePathForWebUser(c, srcCfg.FilePath)
		if status, errMsg := s.validateConnectionFilePath(c, srcCfg.Type, srcFilePath); status != http.StatusOK {
			c.JSON(status, H{"error": errMsg})
			return
		}
		checkCfg := srcCfg
		checkCfg.Options = store.StripReservedConnectionOptions(checkCfg.Options)
		if status, errMsg := s.validateRemoteEndpointForWebUser(c, checkCfg); status != http.StatusOK {
			c.JSON(status, H{"error": errMsg})
			return
		}
	}
	saved, err := s.store.DuplicateConnection(id, c.Principal)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	sameOwner := s.desktop.Enabled || (hasSrcSaved && (srcSaved.OwnerID == c.UserID() || (c.IsAdmin() && srcSaved.OwnerID == "")))
	if !saved.RememberPassword && sameOwner {
		scope := s.credSessionKey(c)
		if cred, ok := s.getSessionCred(scope, id); ok {
			cred.UpdatedAt = time.Now()
			s.setSessionCred(scope, saved.ID, cred)
		}
	}
	saved = s.annotateSavedConnectionUnlock(c, saved)
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleExportConnections(c *Context) {
	profiles, err := s.store.ExportConnectionsForPrincipal(c.Principal)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	payload := H{
		"service":     "DBHarbor",
		"exportedAt":  time.Now().UTC().Format(time.RFC3339),
		"connections": profiles,
	}
	if c.Query("download") == "1" || c.Query("download") == "true" {
		filename := fmt.Sprintf("dbharbor_connections_%s.json", time.Now().Format("20060102_150405"))
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		enc := json.NewEncoder(c.Writer)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(payload)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Server) handleImportConnections(c *Context) {
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to import connections"})
		return
	}
	var raw json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid JSON payload for connection import"})
		return
	}
	var profiles []store.ExportedConnectionProfile
	var wrapped struct {
		Connections []store.ExportedConnectionProfile `json:"connections"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Connections) > 0 {
		profiles = wrapped.Connections
	} else if err := json.Unmarshal(raw, &profiles); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Unrecognized connection profile format"})
		return
	}
	if len(profiles) > store.MaxImportConnectionProfiles {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Cannot import more than %d connection profiles in a single batch", store.MaxImportConnectionProfiles)})
		return
	}
	validatedEndpoints := make(map[string]struct{}, len(profiles))
	for i := range profiles {
		profiles[i].Options = store.StripReservedConnectionOptions(profiles[i].Options)
		profiles[i].FilePath = s.normalizeConnectionFilePathForWebUser(c, profiles[i].FilePath)
		if status, errMsg := s.validateConnectionFilePath(c, profiles[i].Type, profiles[i].FilePath); status != http.StatusOK {
			c.JSON(status, H{"error": errMsg})
			return
		}
		optKeys := make([]string, 0, len(profiles[i].Options))
		for k := range profiles[i].Options {
			optKeys = append(optKeys, k)
		}
		sort.Strings(optKeys)
		var optSig strings.Builder
		for _, k := range optKeys {
			optSig.WriteString(k)
			optSig.WriteByte('=')
			optSig.WriteString(profiles[i].Options[k])
			optSig.WriteByte('&')
		}
		epKey := strings.ToLower(strings.TrimSpace(profiles[i].Type)) + "|" +
			strings.TrimSpace(profiles[i].Host) + "|" +
			strconv.Itoa(profiles[i].Port) + "|" +
			strings.TrimSpace(profiles[i].URI) + "|" +
			optSig.String()
		if _, alreadyOK := validatedEndpoints[epKey]; !alreadyOK {
			if status, errMsg := s.validateRemoteEndpointForWebUser(c, driver.ConnectionConfig{
				Type:    profiles[i].Type,
				Host:    profiles[i].Host,
				Port:    profiles[i].Port,
				URI:     profiles[i].URI,
				Options: profiles[i].Options,
			}); status != http.StatusOK {
				c.JSON(status, H{"error": errMsg})
				return
			}
			validatedEndpoints[epKey] = struct{}{}
		}
		if !s.desktop.Enabled && profiles[i].Options != nil {
			delete(profiles[i].Options, "cliPath")
		}
	}
	imported, err := s.store.ImportConnections(profiles, c.Principal)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"imported": imported, "message": fmt.Sprintf("Imported %d connection profile(s)", imported)})
}

func (s *Server) handleDisconnectConnection(c *Context) {
	id := c.Param("id")
	if !s.authorizeConn(c, id, false) {
		return
	}
	s.clearSessionCred(s.credSessionKey(c), id)
	if s.store.CanEditConnection(c.Principal, id) {
		s.pool.EvictConnectionAll(id)
		s.invalidateConnCache(id)
	}
	c.JSON(http.StatusOK, H{"message": "Connection pool released"})
}

func (s *Server) handleDeleteConnection(c *Context) {
	id := c.Param("id")
	if !s.store.CanEditConnection(c.Principal, id) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can delete this connection"})
		return
	}
	existingCfg, _ := s.store.GetDecryptedConnection(id)
	s.clearConnectionSessionCreds(id)
	s.pool.EvictAndWait(id, 1500*time.Millisecond)
	s.invalidateConnCache(id)
	if err := s.store.DeleteConnection(id); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	if strings.EqualFold(existingCfg.Type, "duckdb") && strings.TrimSpace(existingCfg.FilePath) != "" {
		sqlite.ReleaseDuckDBFileLock(existingCfg.FilePath)
	}
	if store.IsEphemeralConnection(existingCfg.Options) {
		s.cleanupEphemeralWorkspace(existingCfg)
		_ = s.store.ClearHistoryForUser("", id)
	}
	c.JSON(http.StatusOK, H{"message": "Connection deleted"})
}

func (s *Server) handleListHistory(c *Context) {
	connID := c.Query("connectionId")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	list, err := s.store.ListHistoryForUser(c.UserID(), connID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"history": list})
}

func (s *Server) handleClearHistory(c *Context) {
	connID := c.Query("connectionId")
	if err := s.store.ClearHistoryForUser(c.UserID(), connID); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Query history cleared"})
}

func (s *Server) handleListSnippets(c *Context) {
	list, err := s.store.ListSnippetsForUser(c.UserID(), c.IsAdmin())
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"snippets": list})
}

func (s *Server) handleSaveSnippet(c *Context) {
	var sn store.SavedSnippet
	if err := c.ShouldBindJSON(&sn); err != nil || strings.TrimSpace(sn.Title) == "" || strings.TrimSpace(sn.SQL) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Snippet title and SQL content cannot be empty"})
		return
	}
	// Read-only users can save personal query snippets, while team-shared snippets require write/admin permission
	if !c.CanWrite() {
		sn.IsShared = false
	}
	if s.desktop.Enabled {
		sn.IsShared = true
	}
	saved, err := s.store.SaveSnippetForUser(sn, c.UserID(), c.Username(), c.IsAdmin())
	if err != nil {
		if strings.HasPrefix(err.Error(), "permission denied") {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleDeleteSnippet(c *Context) {
	if err := s.store.DeleteSnippetForUser(c.Param("sid"), c.UserID(), c.IsAdmin()); err != nil {
		if strings.HasPrefix(err.Error(), "permission denied") {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "SQL snippet deleted"})
}
