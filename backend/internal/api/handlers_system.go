package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/driver"
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
	c.JSON(http.StatusOK, H{
		"version":            info.Version,
		"displayVersion":     info.DisplayVersion,
		"edition":            edition,
		"channel":            info.Channel,
		"authEnabled":        !s.desktop.Enabled,
		"license":            info.License,
		"gitCommit":          info.GitCommit,
		"buildTime":          info.BuildTime,
		"goVersion":          info.GoVersion,
		"os":                 info.OS,
		"arch":               info.Arch,
		"repoUrl":            info.RepoURL,
		"gitUrl":             info.GitURL,
		"releasesUrl":        info.ReleasesURL,
		"embeddedChangelog":  version.EmbeddedChangelogForLocale(lang),
		"embeddedChangelogs": version.EmbeddedChangelogsMap(),
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

func (s *Server) handleLogin(c *Context) {
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
			"error":         fmt.Sprintf("Too many login attempts; account or IP is temporarily locked. Please retry in %d seconds", chk.RetryAfterSec),
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
					"error":         fmt.Sprintf("Too many failed verification attempts; security lockout active (%d seconds)", nextChk.RetryAfterSec),
					"locked":        true,
					"retryAfterSec": nextChk.RetryAfterSec,
				})
				return
			}
			cTok, cImg, _ := s.protector.GenerateCaptcha()
			c.JSON(http.StatusUnauthorized, H{
				"error":             "Please enter the valid security CAPTCHA code",
				"requiresCaptcha":   true,
				"remainingAttempts": nextChk.RemainingAttempts,
				"captchaToken":      cTok,
				"captchaImage":      cImg,
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
				"error":         fmt.Sprintf("Too many failed verification attempts; security lockout active (%d seconds)", nextChk.RetryAfterSec),
				"locked":        true,
				"retryAfterSec": nextChk.RetryAfterSec,
			})
			return
		}
		resp := H{
			"error":             err.Error(),
			"remainingAttempts": nextChk.RemainingAttempts,
			"requiresCaptcha":   nextChk.RequiresCaptcha,
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
			"username":       user.Username,
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
		c.JSON(http.StatusUnauthorized, H{
			"error":             err.Error(),
			"remainingAttempts": nextChk.RemainingAttempts,
		})
		return
	}

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
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, H{"error": "New password must be at least 6 characters"})
		return
	}
	if err := s.store.UpdatePassword(c.UserID(), req.OldPassword, req.NewPassword); err != nil {
		s.store.AddAuditLog("PASSWORD_CHANGE_FAILED", c.Username(), c.ClientIP(), c.Request.UserAgent(), err.Error())
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	revokedCnt := 0
	if req.RevokeOtherSessions {
		revokedCnt, _ = s.store.RevokeOtherSessions(c.UserID(), c.SessionID())
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
	s.store.AddAuditLog("SESSION_REVOKED", c.Username(), c.ClientIP(), c.Request.UserAgent(), "Revoked session "+sid)
	c.JSON(http.StatusOK, H{"message": "Session revoked"})
}

func (s *Server) handleRevokeOtherSessions(c *Context) {
	cnt, err := s.store.RevokeOtherSessions(c.UserID(), c.SessionID())
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
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
	c.JSON(http.StatusOK, H{"drivers": driver.ListMetas()})
}

func (s *Server) handleListConnections(c *Context) {
	list, err := s.store.ListConnectionsForPrincipal(c.Principal)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"connections": list})
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
		if cfg.Password == "" && cfg.URI == "" {
			if existing, err := s.store.GetDecryptedConnection(cfg.ID); err == nil {
				// Prevent stored credential exfiltration if target host/port was altered without re-entering password
				if existing.Host == cfg.Host && existing.Port == cfg.Port && existing.Type == cfg.Type {
					if cfg.Password == "" {
						cfg.Password = existing.Password
					}
					if cfg.URI == "" {
						cfg.URI = existing.URI
					}
				}
			}
		}
	} else if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to create new connections"})
		return
	}
	if status, errMsg := s.validateConnectionFilePath(c, cfg.Type, cfg.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
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
	drv, _, err := s.pool.Get(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := drv.Ping(ctx)
	if err != nil {
		s.pool.Evict(id)
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
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
	if status, errMsg := s.validateConnectionFilePath(c, req.Type, req.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	req.ConnectionConfig.ID = ""
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
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleUpdateConnection(c *Context) {
	id := c.Param("id")
	if !s.store.CanEditConnection(c.Principal, id) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can modify this connection"})
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
	if status, errMsg := s.validateConnectionFilePath(c, req.Type, req.FilePath); status != http.StatusOK {
		c.JSON(status, H{"error": errMsg})
		return
	}
	req.ConnectionConfig.ID = id
	vis := ""
	if s.desktop.Enabled {
		vis = "shared"
	} else if c.IsAdmin() {
		if req.Visibility == "private" || req.Visibility == "shared" {
			vis = req.Visibility
		}
	} else {
		vis = "private"
	}
	saved, err := s.store.SaveConnectionWithMeta(req.ConnectionConfig, req.KeepExistingSecret, "", vis)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	s.pool.Evict(id)
	s.invalidateConnCache(id)
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
	saved, err := s.store.DuplicateConnection(id, c.Principal)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
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
	for _, prof := range profiles {
		if status, errMsg := s.validateConnectionFilePath(c, prof.Type, prof.FilePath); status != http.StatusOK {
			c.JSON(status, H{"error": errMsg})
			return
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
	s.pool.Evict(id)
	s.invalidateConnCache(id)
	c.JSON(http.StatusOK, H{"message": "Connection pool released"})
}

func (s *Server) handleDeleteConnection(c *Context) {
	id := c.Param("id")
	if !s.store.CanEditConnection(c.Principal, id) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can delete this connection"})
		return
	}
	s.pool.Evict(id)
	s.invalidateConnCache(id)
	if err := s.store.DeleteConnection(id); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
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
