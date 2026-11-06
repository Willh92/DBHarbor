package api

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
	"dbharbor/backend/internal/version"
	"dbharbor/backend/webdist"
)

const (
	desktopSessionCookieName = "dbharbor_desktop_session"
	desktopHeaderTokenName   = "X-DBHarbor-Desktop"
)

type cachedAutocomplete struct {
	meta      driver.AutocompleteMeta
	expiresAt time.Time
}

// DesktopConfig configures pure local desktop window mode (no user account system).
type DesktopConfig struct {
	Enabled             bool
	ListenPort          int
	DataDir             string
	DarkMode            bool
	BootTicket          string
	SessionCookieSecret string
	SaveFileDialog      func(defaultName string) (chosenPath string, canceled bool, err error)
	OpenFileDialog      func(kind string, currentPath string) (chosenPath string, canceled bool, err error)
	SetWindowDarkMode   func(dark bool)
}

func (dc DesktopConfig) portScopedCookieName() string {
	if dc.ListenPort > 0 {
		return fmt.Sprintf("%s_%d", desktopSessionCookieName, dc.ListenPort)
	}
	return desktopSessionCookieName
}

type Server struct {
	store       *store.Store
	pool        *pool.Manager
	tokens      *auth.TokenManager
	protector   *auth.LoginProtector
	updater     *version.Checker
	desktop     DesktopConfig
	startedAt   time.Time
	metaCacheMu sync.RWMutex
	metaCache   map[string]cachedAutocomplete
}

func NewRouter(st *store.Store, pm *pool.Manager, tm *auth.TokenManager) http.Handler {
	tm.SetSessionValidator(st.IsSessionActive)
	srv := &Server{
		store:     st,
		pool:      pm,
		tokens:    tm,
		protector: auth.NewLoginProtector([]byte("dbharbor-login-protector-v1")),
		updater:   version.NewChecker(),
		startedAt: time.Now(),
		metaCache: make(map[string]cachedAutocomplete),
	}
	return srv.buildHandler()
}

// NewDesktopRouter creates a pure local router without user accounts, passwords, or 2FA,
// protected against cross-site localhost requests via a one-time boot ticket + HttpOnly SameSite=Strict cookie.
func NewDesktopRouter(st *store.Store, pm *pool.Manager, dcfg DesktopConfig) http.Handler {
	dcfg.Enabled = true
	srv := &Server{
		store:     st,
		pool:      pm,
		updater:   version.NewChecker(),
		desktop:   dcfg,
		startedAt: time.Now(),
		metaCache: make(map[string]cachedAutocomplete),
	}
	return srv.buildHandler()
}

func (srv *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()

	pub := func(pattern string, fn HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			fn(&Context{Writer: w, Request: r})
		})
	}

	authed := func(pattern string, fn HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			claims, principal, ok := srv.authenticateRequest(w, r)
			if !ok {
				return
			}
			fn(&Context{Writer: w, Request: r, Claims: claims, Principal: principal})
		})
	}

	// Public endpoints
	pub("GET /api/health", func(c *Context) {
		c.JSON(http.StatusOK, H{"status": "ok", "service": "DBHarbor", "version": version.DisplayTag(version.Version)})
	})
	pub("GET /api/version", srv.handleGetVersion)

	if srv.desktop.Enabled {
		pub("GET /__desktop_boot", srv.handleDesktopBoot)
		authed("GET /api/auth/me", func(c *Context) {
			c.JSON(http.StatusOK, H{
				"userId":          "local",
				"username":        "local",
				"displayName":     "Local Workspace",
				"role":            store.RoleAdmin,
				"canCreateConn":   true,
				"authEnabled":     false,
				"totpEnabled":     false,
				"passwordChanged": true,
			})
		})
		authed("POST /api/conn/{id}/export/desktop-save", srv.handleDesktopSaveExport)
		authed("POST /api/conn/{id}/doc/export/desktop-save", srv.handleDesktopSaveDocExport)
		authed("POST /api/desktop/window-theme", srv.handleDesktopSetWindowTheme)
		authed("POST /api/desktop/pick-file", srv.handleDesktopPickFile)
	} else {
		pub("GET /api/auth/captcha", srv.handleGetCaptcha)
		pub("POST /api/auth/login", srv.handleLogin)
		pub("POST /api/auth/login/2fa", srv.handleLogin2FA)

		// Authenticated User, Security & 2FA endpoints
		authed("GET /api/auth/me", srv.handleMe)
		authed("PUT /api/auth/profile", srv.handleUpdateProfile)
		authed("POST /api/auth/logout", srv.handleLogout)
		authed("POST /api/auth/ticket", srv.handleIssueExportTicket)
		authed("PUT /api/auth/password", srv.handleChangePassword)
		authed("GET /api/auth/security", srv.handleGetSecurityStatus)
		authed("POST /api/auth/2fa/setup", srv.handleBegin2FASetup)
		authed("POST /api/auth/2fa/enable", srv.handleEnable2FA)
		authed("POST /api/auth/2fa/disable", srv.handleDisable2FA)
		authed("POST /api/auth/2fa/recovery/regenerate", srv.handleRegenerateRecoveryCodes)
		authed("GET /api/auth/sessions", srv.handleListSessions)
		authed("DELETE /api/auth/sessions/{sid}", srv.handleRevokeSession)
		authed("POST /api/auth/sessions/revoke-others", srv.handleRevokeOtherSessions)
		authed("GET /api/auth/audit-logs", srv.handleListAuditLogs)

		// Administrator Team & RBAC Management endpoints
		authed("GET /api/admin/users", srv.handleAdminListUsers)
		authed("POST /api/admin/users", srv.handleAdminCreateUser)
		authed("PUT /api/admin/users/{uid}", srv.handleAdminUpdateUser)
		authed("DELETE /api/admin/users/{uid}", srv.handleAdminDeleteUser)
		authed("PUT /api/admin/users/{uid}/password", srv.handleAdminResetPassword)
		authed("POST /api/admin/users/{uid}/reset-2fa", srv.handleAdminReset2FA)
		authed("POST /api/admin/users/{uid}/revoke-sessions", srv.handleAdminRevokeUserSessions)
	}

	// System, Version Update Check & Drivers
	authed("GET /api/system/stats", srv.handleSystemStats)
	authed("POST /api/system/pools/release-idle", srv.handleReleaseIdlePools)
	authed("GET /api/system/update-check", srv.handleCheckUpdate)
	authed("GET /api/drivers", srv.handleListDrivers)
	authed("GET /api/fs/browse", srv.handleBrowseLocalFS)
	authed("POST /api/fs/upload-db", srv.handleUploadLocalDBFile)

	// Connections
	authed("GET /api/connections", srv.handleListConnections)
	authed("GET /api/connections/export", srv.handleExportConnections)
	authed("POST /api/connections/import", srv.handleImportConnections)
	authed("POST /api/connections/test", srv.handleTestConnection)
	authed("POST /api/connections", srv.handleCreateConnection)
	authed("PUT /api/connections/{id}", srv.handleUpdateConnection)
	authed("DELETE /api/connections/{id}", srv.handleDeleteConnection)
	authed("POST /api/connections/{id}/duplicate", srv.handleDuplicateConnection)
	authed("POST /api/connections/{id}/ping", srv.handlePingSavedConnection)
	authed("POST /api/connections/{id}/disconnect", srv.handleDisconnectConnection)

	// History & Snippets
	authed("GET /api/history", srv.handleListHistory)
	authed("DELETE /api/history", srv.handleClearHistory)
	authed("GET /api/snippets", srv.handleListSnippets)
	authed("POST /api/snippets", srv.handleSaveSnippet)
	authed("DELETE /api/snippets/{sid}", srv.handleDeleteSnippet)

	// Database-level & SQL / OLAP endpoints
	authed("GET /api/conn/{id}/databases", srv.handleListDatabases)
	authed("POST /api/conn/{id}/databases", srv.handleCreateDatabase)
	authed("DELETE /api/conn/{id}/databases", srv.handleDropDatabase)
	authed("GET /api/conn/{id}/schemas", srv.handleListSchemas)
	authed("POST /api/conn/{id}/schemas", srv.handleCreateSchema)
	authed("DELETE /api/conn/{id}/schemas", srv.handleDropSchema)
	authed("GET /api/conn/{id}/objects", srv.handleListObjects)
	authed("GET /api/conn/{id}/table/structure", srv.handleGetTableStructure)
	authed("GET /api/conn/{id}/object/ddl", srv.handleGetObjectDDL)
	authed("POST /api/conn/{id}/table/create", srv.handleCreateTable)
	authed("POST /api/conn/{id}/table/alter", srv.handleAlterTable)
	authed("POST /api/conn/{id}/object/action", srv.handleObjectAction)
	authed("GET /api/conn/{id}/privileges/capabilities", srv.handlePrivCapabilities)
	authed("GET /api/conn/{id}/privileges/users", srv.handlePrivListUsers)
	authed("GET /api/conn/{id}/privileges/grants", srv.handlePrivGetUserGrants)
	authed("POST /api/conn/{id}/privileges/preview", srv.handlePrivPreview)
	authed("POST /api/conn/{id}/privileges/execute", srv.handlePrivExecute)
	authed("POST /api/conn/{id}/table/browse", srv.handleBrowseData)
	authed("POST /api/conn/{id}/table/mutate/preview", srv.handlePreviewMutation)
	authed("POST /api/conn/{id}/table/mutate", srv.handleApplyMutation)
	authed("GET /api/conn/{id}/table/partitions", srv.handleListPartitions)
	authed("POST /api/conn/{id}/table/partitions/action", srv.handleManagePartition)
	authed("POST /api/conn/{id}/query", srv.handleExecuteQuery)
	authed("GET /api/conn/{id}/autocomplete", srv.handleAutocomplete)
	authed("GET /api/conn/{id}/export", srv.handleExport)
	authed("POST /api/conn/{id}/import", srv.handleImport)
	authed("POST /api/conn/{id}/import/sql", srv.handleImportSQLText)

	// Redis Key-Value endpoints
	authed("GET /api/conn/{id}/kv/scan", srv.handleKVScan)
	authed("GET /api/conn/{id}/kv/key", srv.handleKVGetKey)
	authed("POST /api/conn/{id}/kv/key", srv.handleKVSetKey)
	authed("PUT /api/conn/{id}/kv/item", srv.handleKVMutateItem)
	authed("POST /api/conn/{id}/kv/delete", srv.handleKVDeleteKeys)
	authed("PUT /api/conn/{id}/kv/ttl", srv.handleKVSetTTL)
	authed("PUT /api/conn/{id}/kv/rename", srv.handleKVRenameKey)
	authed("POST /api/conn/{id}/kv/duplicate", srv.handleKVDuplicateKey)
	authed("POST /api/conn/{id}/kv/flush", srv.handleKVFlushDB)
	authed("POST /api/conn/{id}/kv/cmd", srv.handleKVCommand)
	authed("POST /api/conn/{id}/kv/export", srv.handleKVExportKeys)
	authed("POST /api/conn/{id}/kv/import", srv.handleKVImportKeys)

	// MongoDB Document endpoints
	authed("GET /api/conn/{id}/doc/collections", srv.handleDocListCollections)
	authed("POST /api/conn/{id}/doc/collections", srv.handleDocCreateCollection)
	authed("DELETE /api/conn/{id}/doc/collections", srv.handleDocDropCollection)
	authed("PUT /api/conn/{id}/doc/collections/rename", srv.handleDocRenameCollection)
	authed("GET /api/conn/{id}/doc/export", srv.handleDocExport)
	authed("POST /api/conn/{id}/doc/import", srv.handleDocImport)
	authed("POST /api/conn/{id}/doc/find", srv.handleDocFind)
	authed("POST /api/conn/{id}/doc/insert", srv.handleDocInsert)
	authed("PUT /api/conn/{id}/doc/update", srv.handleDocUpdate)
	authed("POST /api/conn/{id}/doc/delete", srv.handleDocDelete)
	authed("GET /api/conn/{id}/doc/indexes", srv.handleDocListIndexes)
	authed("POST /api/conn/{id}/doc/indexes", srv.handleDocCreateIndex)
	authed("DELETE /api/conn/{id}/doc/indexes", srv.handleDocDropIndex)
	authed("POST /api/conn/{id}/doc/command", srv.handleDocRunCommand)

	// Live Server Monitor
	authed("GET /api/conn/{id}/monitor", srv.handleGetMonitor)
	authed("POST /api/conn/{id}/monitor/kill", srv.handleKillSession)

	// Catch-all for unknown /api/* and embedded SPA static assets
	distFS, _ := webdist.DistFS()
	assetETags := buildAssetETags(distFS)
	var desktopIndexHTML []byte
	var desktopIndexETag string
	if srv.desktop.Enabled && distFS != nil {
		desktopIndexHTML = buildDesktopIndexHTML(distFS, srv.desktop)
		if len(desktopIndexHTML) > 0 {
			desktopIndexETag = computeWeakETag(desktopIndexHTML)
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			(&Context{Writer: w, Request: r}).JSON(http.StatusNotFound, H{"error": "API endpoint not found"})
			return
		}
		if distFS == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		serveSPA(w, r, distFS, assetETags, desktopIndexHTML, desktopIndexETag)
	})

	return wrapGlobalMiddleware(mux, srv.desktop.Enabled)
}

func (s *Server) authorizeConn(c *Context, connID string, requireWrite bool) bool {
	if !s.store.CanAccessConnection(c.Principal, connID) {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: you do not have access to this data source connection"})
		return false
	}
	if requireWrite && !c.RequireWrite() {
		return false
	}
	return true
}

func (s *Server) resolveWebPrincipal(ctx *Context, claims *auth.Claims) (store.UserPrincipal, bool) {
	p, exists := s.store.GetPrincipal(claims.UserID)
	if !exists || p.Disabled {
		if claims.SessionID != "" {
			_ = s.store.RevokeSession(claims.UserID, claims.SessionID)
			if s.tokens != nil {
				s.tokens.RevokeSessionInMemory(claims.SessionID, 0)
			}
		}
		ctx.JSON(http.StatusUnauthorized, H{"error": "Account disabled or no longer active; please contact an administrator"})
		return store.UserPrincipal{}, false
	}
	return p, true
}

func (s *Server) authenticateRequest(w http.ResponseWriter, r *http.Request) (*auth.Claims, store.UserPrincipal, bool) {
	ctx := &Context{Writer: w, Request: r}

	if s.desktop.Enabled {
		localP := store.LocalDesktopPrincipal()
		if s.desktop.SessionCookieSecret == "" {
			return &auth.Claims{UserID: "local", Username: "local"}, localP, true
		}
		if hdrTok := strings.TrimSpace(r.Header.Get(desktopHeaderTokenName)); hdrTok != "" {
			if subtle.ConstantTimeCompare([]byte(hdrTok), []byte(s.desktop.SessionCookieSecret)) == 1 {
				return &auth.Claims{UserID: "local", Username: "local"}, localP, true
			}
		}
		if portCookieName := s.desktop.portScopedCookieName(); portCookieName != desktopSessionCookieName {
			if ck, err := r.Cookie(portCookieName); err == nil {
				if subtle.ConstantTimeCompare([]byte(ck.Value), []byte(s.desktop.SessionCookieSecret)) == 1 {
					return &auth.Claims{UserID: "local", Username: "local"}, localP, true
				}
			}
		}
		if ck, err := r.Cookie(desktopSessionCookieName); err == nil {
			if subtle.ConstantTimeCompare([]byte(ck.Value), []byte(s.desktop.SessionCookieSecret)) == 1 {
				return &auth.Claims{UserID: "local", Username: "local"}, localP, true
			}
		}
		ctx.JSON(http.StatusUnauthorized, H{"error": "Unauthorized local desktop session"})
		return nil, store.UserPrincipal{}, false
	}

	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		tokenStr := strings.TrimSpace(authHeader[7:])
		claims, err := s.tokens.ParseTokenWithPurpose(tokenStr, auth.PurposeSession)
		if err != nil {
			ctx.JSON(http.StatusUnauthorized, H{"error": err.Error()})
			return nil, store.UserPrincipal{}, false
		}
		p, ok := s.resolveWebPrincipal(ctx, claims)
		if !ok {
			return nil, store.UserPrincipal{}, false
		}
		return claims, p, true
	}

	// Allow single-use export tickets (preferred) or legacy query token on streaming GET /export
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/export") {
		if ticket := strings.TrimSpace(r.URL.Query().Get("ticket")); ticket != "" {
			claims, err := s.tokens.ParseTokenWithPurpose(ticket, auth.PurposeExportTicket)
			if err != nil {
				ctx.JSON(http.StatusUnauthorized, H{"error": err.Error()})
				return nil, store.UserPrincipal{}, false
			}
			p, ok := s.resolveWebPrincipal(ctx, claims)
			if !ok {
				return nil, store.UserPrincipal{}, false
			}
			return claims, p, true
		}
		if tok := strings.TrimSpace(r.URL.Query().Get("token")); tok != "" {
			claims, err := s.tokens.ParseTokenWithPurpose(tok, auth.PurposeSession)
			if err != nil {
				ctx.JSON(http.StatusUnauthorized, H{"error": err.Error()})
				return nil, store.UserPrincipal{}, false
			}
			p, ok := s.resolveWebPrincipal(ctx, claims)
			if !ok {
				return nil, store.UserPrincipal{}, false
			}
			return claims, p, true
		}
	}

	ctx.JSON(http.StatusUnauthorized, H{"error": "Authentication required or missing credentials"})
	return nil, store.UserPrincipal{}, false
}

func wrapGlobalMiddleware(next http.Handler, isDesktop bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hardened HTTP Security Headers
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		if !isDesktop {
			// CORS headers for web server mode
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, Accept-Language")
			h.Set("Access-Control-Expose-Headers", "Content-Disposition")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[DBHarbor] HTTP panic recovered: %v", rec)
				(&Context{Writer: w, Request: r}).JSON(http.StatusInternalServerError, H{"error": "Internal server error"})
			}
		}()

		// Dynamic Gzip compression for API responses when accepted by client
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			!strings.HasSuffix(r.URL.Path, "/export") &&
			strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gw := apiGzipPool.Get().(*gzip.Writer)
			gw.Reset(w)
			gzw := &gzipAPIResponseWriter{ResponseWriter: w, gw: gw}
			defer gzw.close()
			next.ServeHTTP(gzw, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func computeWeakETag(data []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return fmt.Sprintf(`W/"%x-%x"`, h.Sum64(), len(data))
}

func buildAssetETags(distFS fs.FS) map[string]string {
	if distFS == nil {
		return nil
	}
	etags := make(map[string]string, 32)
	h := fnv.New64a()
	_ = fs.WalkDir(distFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		f, oErr := distFS.Open(p)
		if oErr != nil {
			return nil
		}
		h.Reset()
		n, rErr := io.Copy(h, f)
		_ = f.Close()
		if rErr != nil {
			return nil
		}
		logical := strings.TrimSuffix(p, ".gz")
		etags[logical] = fmt.Sprintf(`W/"%x-%x"`, h.Sum64(), n)
		return nil
	})
	return etags
}

func checkIfNoneMatch(r *http.Request, etag string) bool {
	if etag == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	inm := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if inm == "" {
		return false
	}
	if inm == "*" {
		return true
	}
	targetCore := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(inm, ",") {
		cand := strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if cand == targetCore {
			return true
		}
	}
	return false
}

func serveDesktopIndex(w http.ResponseWriter, r *http.Request, desktopIndexHTML []byte, desktopIndexETag string) {
	setStaticCacheHeaders(w, "index.html")
	if desktopIndexETag != "" {
		w.Header().Set("ETag", desktopIndexETag)
		if checkIfNoneMatch(r, desktopIndexETag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(desktopIndexHTML)
	}
}

func serveSPA(
	w http.ResponseWriter,
	r *http.Request,
	distFS fs.FS,
	assetETags map[string]string,
	desktopIndexHTML []byte,
	desktopIndexETag string,
) {
	cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if cleanPath == "" || cleanPath == "." {
		cleanPath = "index.html"
	}

	if cleanPath == "index.html" && len(desktopIndexHTML) > 0 {
		serveDesktopIndex(w, r, desktopIndexHTML, desktopIndexETag)
		return
	}

	if tryServeEmbeddedAsset(w, r, distFS, assetETags, cleanPath) {
		return
	}
	if cleanPath != "index.html" {
		if len(desktopIndexHTML) > 0 {
			serveDesktopIndex(w, r, desktopIndexHTML, desktopIndexETag)
			return
		}
		if tryServeEmbeddedAsset(w, r, distFS, assetETags, "index.html") {
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func buildDesktopIndexHTML(distFS fs.FS, dcfg DesktopConfig) []byte {
	var raw []byte
	if gzFile, err := distFS.Open("index.html.gz"); err == nil {
		defer gzFile.Close()
		if gr, gErr := gzip.NewReader(gzFile); gErr == nil {
			raw, _ = io.ReadAll(gr)
			_ = gr.Close()
		}
	}
	if len(raw) == 0 {
		if f, err := distFS.Open("index.html"); err == nil {
			raw, _ = io.ReadAll(f)
			_ = f.Close()
		}
	}
	if len(raw) == 0 {
		return nil
	}
	bootTag := []byte(fmt.Sprintf(
		`<script>window.__DBHARBOR_BOOT__={"edition":"desktop","authEnabled":false,"desktopToken":%q,"darkMode":%v};</script>`,
		dcfg.SessionCookieSecret,
		dcfg.DarkMode,
	))
	if idx := bytes.Index(raw, []byte("</head>")); idx >= 0 {
		out := make([]byte, 0, len(raw)+len(bootTag))
		out = append(out, raw[:idx]...)
		out = append(out, bootTag...)
		out = append(out, raw[idx:]...)
		return out
	}
	return append(bootTag, raw...)
}

func tryServeEmbeddedAsset(
	w http.ResponseWriter,
	r *http.Request,
	distFS fs.FS,
	assetETags map[string]string,
	cleanPath string,
) bool {
	ctype := contentTypeForExt(filepath.Ext(cleanPath))
	etag := assetETags[cleanPath]

	// 1. Prefer pre-compressed .gz asset (zero-CPU streaming for browsers, transparent gunzip fallback otherwise)
	if gzFile, err := distFS.Open(cleanPath + ".gz"); err == nil {
		defer gzFile.Close()
		if stat, sErr := gzFile.Stat(); sErr == nil && !stat.IsDir() {
			setStaticCacheHeaders(w, cleanPath)
			w.Header().Set("Vary", "Accept-Encoding")
			if etag != "" {
				w.Header().Set("ETag", etag)
				if checkIfNoneMatch(r, etag) {
					w.WriteHeader(http.StatusNotModified)
					return true
				}
			}
			w.Header().Set("Content-Type", ctype)
			if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = io.Copy(w, gzFile)
				}
				return true
			}
			gr, gErr := gzip.NewReader(gzFile)
			if gErr == nil {
				defer gr.Close()
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = io.Copy(w, gr)
				}
				return true
			}
		}
	}

	// 2. Fallback to uncompressed asset (e.g. icons, woff2, or dev builds)
	f, err := distFS.Open(cleanPath)
	if err != nil {
		return false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}
	setStaticCacheHeaders(w, cleanPath)
	if etag != "" {
		w.Header().Set("ETag", etag)
		if checkIfNoneMatch(r, etag) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
	return true
}

func setStaticCacheHeaders(w http.ResponseWriter, cleanPath string) {
	if strings.HasPrefix(cleanPath, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
}

func contentTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".ttf":
		return "font/ttf"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".wasm":
		return "application/wasm"
	default:
		if c := mime.TypeByExtension(ext); c != "" {
			return c
		}
		return "application/octet-stream"
	}
}
