package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
	"dbharbor/backend/internal/store"
)

const (
	errCodeDBAuthRequired = "DB_AUTH_REQUIRED"
	errCodeDBAuthFailed   = "DB_AUTH_FAILED"

	maxSessionScopes = 4096
	maxCredsPerScope = 256
)

var errConnAuthRequired = errors.New("database password is required for this session")

type sessionConnCred struct {
	Username  string
	Password  string
	URI       string
	UpdatedAt time.Time
}

func (s *Server) credSessionKey(c *Context) string {
	if s.desktop.Enabled {
		return "desktop:local"
	}
	if c == nil {
		return ""
	}
	if sid := strings.TrimSpace(c.SessionID()); sid != "" {
		return "sess:" + sid
	}
	if uid := strings.TrimSpace(c.UserID()); uid != "" {
		return "user:" + uid
	}
	return ""
}

func (s *Server) setSessionCred(scope, connID string, cred sessionConnCred) {
	if scope == "" || connID == "" {
		return
	}
	if cred.UpdatedAt.IsZero() {
		cred.UpdatedAt = time.Now()
	}
	var evictedScope string
	var evictedPoolKey string

	s.sessionCredMu.Lock()
	if s.sessionCreds == nil {
		s.sessionCreds = make(map[string]map[string]sessionConnCred)
	}
	byConn := s.sessionCreds[scope]
	if byConn == nil {
		if len(s.sessionCreds) >= maxSessionScopes {
			var oldestScope string
			var oldestTime time.Time
			for sc, entries := range s.sessionCreds {
				var scLatest time.Time
				for _, entry := range entries {
					if entry.UpdatedAt.After(scLatest) {
						scLatest = entry.UpdatedAt
					}
				}
				if oldestScope == "" || scLatest.Before(oldestTime) {
					oldestScope = sc
					oldestTime = scLatest
				}
			}
			if oldestScope != "" {
				delete(s.sessionCreds, oldestScope)
				evictedScope = oldestScope
			}
		}
		byConn = make(map[string]sessionConnCred)
		s.sessionCreds[scope] = byConn
	}
	if _, exists := byConn[connID]; !exists && len(byConn) >= maxCredsPerScope {
		var oldestCID string
		var oldestTime time.Time
		for cid, entry := range byConn {
			if oldestCID == "" || entry.UpdatedAt.Before(oldestTime) {
				oldestCID = cid
				oldestTime = entry.UpdatedAt
			}
		}
		if oldestCID != "" {
			delete(byConn, oldestCID)
			evictedPoolKey = oldestCID + "::" + scope
		}
	}
	byConn[connID] = cred
	s.sessionCredMu.Unlock()

	if s.pool != nil {
		if evictedScope != "" {
			s.pool.EvictBySessionScope(evictedScope)
		}
		if evictedPoolKey != "" {
			s.pool.Evict(evictedPoolKey)
		}
	}
}

func (s *Server) ClearAllSessionCreds() {
	if s == nil {
		return
	}
	s.sessionCredMu.Lock()
	for scope, byConn := range s.sessionCreds {
		for cid := range byConn {
			byConn[cid] = sessionConnCred{}
			delete(byConn, cid)
		}
		delete(s.sessionCreds, scope)
	}
	s.sessionCreds = make(map[string]map[string]sessionConnCred)
	s.sessionCredMu.Unlock()
}

func (s *Server) pruneUnauthorizedUserSessionCreds(uid string) {
	uid = strings.TrimSpace(uid)
	if uid == "" || s.store == nil {
		return
	}
	p, ok := s.store.GetPrincipal(uid)
	if !ok || p.Disabled {
		s.clearSessionScope("user:" + uid)
		for _, sid := range s.store.ListActiveUserSessionIDs(uid) {
			s.clearSessionByID(sid)
		}
		return
	}
	scopes := []string{"user:" + uid}
	for _, sid := range s.store.ListActiveUserSessionIDs(uid) {
		scopes = append(scopes, "sess:"+sid)
	}
	type scopeConn struct {
		scope string
		cid   string
	}
	var candidates []scopeConn
	s.sessionCredMu.RLock()
	for _, sc := range scopes {
		for cid := range s.sessionCreds[sc] {
			candidates = append(candidates, scopeConn{scope: sc, cid: cid})
		}
	}
	s.sessionCredMu.RUnlock()
	for _, item := range candidates {
		if !s.store.CanAccessConnection(p, item.cid) {
			s.clearSessionCred(item.scope, item.cid)
		}
	}
}

func (s *Server) getSessionCred(scope, connID string) (sessionConnCred, bool) {
	if scope == "" || connID == "" {
		return sessionConnCred{}, false
	}
	s.sessionCredMu.RLock()
	var cred sessionConnCred
	var ok bool
	if byConn := s.sessionCreds[scope]; byConn != nil {
		cred, ok = byConn[connID]
	}
	s.sessionCredMu.RUnlock()
	return cred, ok
}

func (s *Server) hasSessionCred(scope, connID string) bool {
	_, ok := s.getSessionCred(scope, connID)
	return ok
}

func (s *Server) clearSessionCred(scope, connID string) {
	if scope == "" || connID == "" {
		return
	}
	s.sessionCredMu.Lock()
	if byConn := s.sessionCreds[scope]; byConn != nil {
		delete(byConn, connID)
		if len(byConn) == 0 {
			delete(s.sessionCreds, scope)
		}
	}
	s.sessionCredMu.Unlock()
	if s.pool != nil {
		s.pool.Evict(connID + "::" + scope)
	}
}

func (s *Server) clearSessionScope(scope string) {
	if scope == "" {
		return
	}
	s.sessionCredMu.Lock()
	delete(s.sessionCreds, scope)
	s.sessionCredMu.Unlock()
	if s.pool != nil {
		s.pool.EvictBySessionScope(scope)
	}
}

func (s *Server) clearSessionByID(sid string) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return
	}
	s.clearSessionScope("sess:" + sid)
}

func (s *Server) clearConnectionSessionCreds(connID string) {
	if connID == "" {
		return
	}
	s.sessionCredMu.Lock()
	for scope, byConn := range s.sessionCreds {
		delete(byConn, connID)
		if len(byConn) == 0 {
			delete(s.sessionCreds, scope)
		}
	}
	s.sessionCredMu.Unlock()
}

func (s *Server) sweepStaleSessionCreds() {
	if s.desktop.Enabled || s.store == nil {
		return
	}
	cutoff := time.Now().Add(-8 * 24 * time.Hour)
	var staleScopes []string
	var stalePoolKeys []string

	s.sessionCredMu.Lock()
	for scope, byConn := range s.sessionCreds {
		if strings.HasPrefix(scope, "sess:") {
			sid := strings.TrimPrefix(scope, "sess:")
			if !s.store.IsSessionActive(sid) {
				delete(s.sessionCreds, scope)
				staleScopes = append(staleScopes, scope)
				continue
			}
		} else if strings.HasPrefix(scope, "user:") {
			uid := strings.TrimPrefix(scope, "user:")
			if p, ok := s.store.GetPrincipal(uid); !ok || p.Disabled {
				delete(s.sessionCreds, scope)
				staleScopes = append(staleScopes, scope)
				continue
			}
		}
		for cid, cred := range byConn {
			if cred.UpdatedAt.Before(cutoff) {
				delete(byConn, cid)
				stalePoolKeys = append(stalePoolKeys, cid+"::"+scope)
			}
		}
		if len(byConn) == 0 {
			delete(s.sessionCreds, scope)
			staleScopes = append(staleScopes, scope)
		}
	}
	s.sessionCredMu.Unlock()

	if s.pool != nil {
		for _, scope := range staleScopes {
			s.pool.EvictBySessionScope(scope)
		}
		for _, pk := range stalePoolKeys {
			s.pool.Evict(pk)
		}
	}
}

func (s *Server) annotateSavedConnectionUnlock(c *Context, item store.SavedConnection) store.SavedConnection {
	if !driver.IsCredentialedDriver(item.Type) || item.RememberPassword {
		item.Unlocked = true
		return item
	}
	item.Unlocked = s.hasSessionCred(s.credSessionKey(c), item.ID)
	return item
}

func (s *Server) annotateConnectionsUnlockState(c *Context, list []store.SavedConnection) []store.SavedConnection {
	scope := s.credSessionKey(c)
	for i := range list {
		if !driver.IsCredentialedDriver(list[i].Type) || list[i].RememberPassword {
			list[i].Unlocked = true
		} else {
			list[i].Unlocked = s.hasSessionCred(scope, list[i].ID)
		}
	}
	return list
}

func applySessionCredToConfig(cfg driver.ConnectionConfig, cred sessionConnCred) driver.ConnectionConfig {
	if cred.Username != "" {
		cfg.Username = cred.Username
	}
	cfg.Password = cred.Password
	if cred.URI != "" {
		cfg.URI = cred.URI
	} else if cfg.URI != "" {
		cfg.URI = store.InjectCredentialsIntoURI(cfg.URI, cfg.Username, cred.Password)
	}
	return cfg
}

func (s *Server) acquireConnDriver(c *Context, connID string) (driver.Driver, driver.ConnectionConfig, error) {
	baseCfg, err := s.store.GetDecryptedConnection(connID)
	if err != nil {
		return nil, baseCfg, err
	}
	if !driver.IsCredentialedDriver(baseCfg.Type) {
		drv, cfg, release, aErr := s.pool.Acquire(connID)
		if aErr != nil {
			return nil, cfg, aErr
		}
		c.Defer(release)
		return drv, cfg, nil
	}

	scope := s.credSessionKey(c)
	hasOverride := s.hasSessionCred(scope, connID)
	if !baseCfg.ShouldRememberPassword() && !hasOverride {
		return nil, baseCfg, errConnAuthRequired
	}

	if hasOverride {
		poolKey := connID + "::" + scope
		drv, cfg, release, aErr := s.pool.AcquireWithConfig(poolKey, func() (driver.ConnectionConfig, error) {
			fresh, gErr := s.store.GetDecryptedConnection(connID)
			if gErr != nil {
				return fresh, gErr
			}
			latestCred, ok := s.getSessionCred(scope, connID)
			if !ok {
				if !fresh.ShouldRememberPassword() {
					return fresh, errConnAuthRequired
				}
				return fresh, nil
			}
			return applySessionCredToConfig(fresh, latestCred), nil
		})
		if aErr != nil {
			return nil, baseCfg, aErr
		}
		c.Defer(release)
		return drv, cfg, nil
	}

	drv, cfg, release, err := s.pool.Acquire(connID)
	if err != nil {
		return nil, baseCfg, err
	}
	c.Defer(release)
	return drv, cfg, nil
}

func (s *Server) evictCallerConnPool(c *Context, connID string) {
	scope := s.credSessionKey(c)
	hadOverride := scope != "" && s.hasSessionCred(scope, connID)
	if scope != "" {
		s.clearSessionCred(scope, connID)
	}
	canEdit := c != nil && s.store != nil && s.store.CanEditConnection(c.Principal, connID)
	if s.pool != nil && (!hadOverride || canEdit) {
		s.pool.Evict(connID)
	}
	s.invalidateConnCache(connID)
}

func (s *Server) respondConnError(c *Context, connID, dbType string, err error) {
	s.respondConnErrorWithFields(c, connID, dbType, err, nil)
}

func (s *Server) respondConnErrorWithFields(c *Context, connID, dbType string, err error, extra H) {
	if err == nil {
		return
	}
	if dbType == "" && connID != "" && s.store != nil {
		if sc, ok := s.store.GetSavedConnectionForPrincipal(c.Principal, connID); ok {
			dbType = sc.Type
		}
	}
	body := H{}
	for k, v := range extra {
		body[k] = v
	}
	if errors.Is(err, errConnAuthRequired) {
		body["error"] = "Database password is required for this session"
		body["code"] = errCodeDBAuthRequired
		body["connectionId"] = connID
		c.JSON(http.StatusBadRequest, body)
		return
	}
	if sqlbase.IsAuthError(dbType, err) {
		s.evictCallerConnPool(c, connID)
		body["error"] = err.Error()
		body["code"] = errCodeDBAuthFailed
		body["connectionId"] = connID
		c.JSON(http.StatusBadRequest, body)
		return
	}
	body["error"] = err.Error()
	c.JSON(http.StatusBadRequest, body)
}

func (s *Server) handleUnlockConnection(c *Context) {
	id := c.Param("id")
	if !s.authorizeConn(c, id, false) {
		return
	}
	baseCfg, err := s.store.GetDecryptedConnection(id)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": err.Error()})
		return
	}
	var req struct {
		Username         *string `json:"username"`
		Password         string  `json:"password"`
		URI              string  `json:"uri"`
		RememberPassword *bool   `json:"rememberPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid unlock credentials payload"})
		return
	}
	if len(req.Password) > 4096 || len(req.URI) > 8192 {
		c.JSON(http.StatusBadRequest, H{"error": "Unlock credentials exceed maximum allowed length"})
		return
	}

	canEdit := s.store.CanEditConnection(c.Principal, id) && !store.IsEphemeralConnection(baseCfg.Options)
	rawURI := strings.TrimSpace(req.URI)
	if rawURI != "" && !canEdit {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: only an administrator or the connection owner can modify the connection URI"})
		return
	}

	testCfg := baseCfg
	if req.Username != nil {
		testCfg.Username = strings.TrimSpace(*req.Username)
	}
	testCfg.Password = req.Password
	if rawURI != "" {
		testCfg.URI = rawURI
	} else if baseCfg.URI != "" {
		testCfg.URI = store.InjectCredentialsIntoURI(baseCfg.URI, testCfg.Username, req.Password)
	}

	if rawURI != "" || !s.store.IsConnectionOwnedByAdmin(id) {
		if status, errMsg := s.validateRemoteEndpointForWebUser(c, testCfg); status != http.StatusOK {
			c.JSON(status, H{"error": errMsg})
			return
		}
	}

	pingCfg := testCfg
	if !s.desktop.Enabled {
		pingCfg = s.prepareWebConnectionConfig(pingCfg)
	}
	drv, err := driver.Create(pingCfg)
	if err != nil {
		if sqlbase.IsAuthError(pingCfg.Type, err) {
			c.JSON(http.StatusBadRequest, H{"error": err.Error(), "code": errCodeDBAuthFailed, "connectionId": id})
			return
		}
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	defer drv.Close()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := drv.Ping(ctx)
	if err != nil {
		if sqlbase.IsAuthError(pingCfg.Type, err) {
			c.JSON(http.StatusBadRequest, H{"error": "Connection failed: " + err.Error(), "code": errCodeDBAuthFailed, "connectionId": id})
			return
		}
		c.JSON(http.StatusBadRequest, H{"error": "Connection failed: " + err.Error()})
		return
	}

	scope := s.credSessionKey(c)
	wantRemember := req.RememberPassword != nil && *req.RememberPassword

	if wantRemember && canEdit {
		saveCfg := baseCfg
		saveCfg.Username = testCfg.Username
		saveCfg.Password = testCfg.Password
		saveCfg.URI = testCfg.URI
		saveCfg.RememberPassword = driver.BoolPtr(true)
		if _, sErr := s.store.SaveConnectionWithMeta(saveCfg, false, "", ""); sErr != nil {
			c.JSON(http.StatusInternalServerError, H{"error": sErr.Error()})
			return
		}
		s.clearConnectionSessionCreds(id)
		s.pool.EvictConnectionAll(id)
		s.invalidateConnCache(id)
	} else {
		effPass := testCfg.Password
		if effPass == "" {
			effPass = store.ExtractPasswordFromURI(testCfg.URI)
		}
		s.setSessionCred(scope, id, sessionConnCred{
			Username:  testCfg.Username,
			Password:  effPass,
			URI:       testCfg.URI,
			UpdatedAt: time.Now(),
		})
		if s.pool != nil && scope != "" {
			s.pool.Evict(id + "::" + scope)
		}
		if canEdit && req.RememberPassword != nil && !*req.RememberPassword && baseCfg.ShouldRememberPassword() {
			// Owner explicitly unchecked Remember Password while re-authenticating: wipe stale disk secret.
			saveCfg := baseCfg
			saveCfg.Username = testCfg.Username
			saveCfg.Password = ""
			saveCfg.URI = store.StripPasswordFromURI(testCfg.URI)
			saveCfg.RememberPassword = driver.BoolPtr(false)
			if _, sErr := s.store.SaveConnectionWithMeta(saveCfg, false, "", ""); sErr != nil {
				c.JSON(http.StatusInternalServerError, H{"error": sErr.Error()})
				return
			}
			s.pool.Evict(id)
		}
		s.invalidateConnCache(id)
	}

	savedConn, _ := s.store.GetSavedConnectionForPrincipal(c.Principal, id)
	savedConn = s.annotateSavedConnectionUnlock(c, savedConn)
	c.JSON(http.StatusOK, H{
		"connection": savedConn,
		"serverInfo": info,
	})
}
