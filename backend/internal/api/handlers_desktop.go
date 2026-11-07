package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

func (s *Server) handleDesktopBoot(c *Context) {
	ticket := strings.TrimSpace(c.Query("ticket"))
	if s.desktop.BootTicket == "" || subtle.ConstantTimeCompare([]byte(ticket), []byte(s.desktop.BootTicket)) != 1 {
		c.JSON(http.StatusForbidden, H{"error": "Invalid desktop launch ticket"})
		return
	}

	const cookieMaxAge = 30 * 24 * 3600
	if portCookie := s.desktop.portScopedCookieName(); portCookie != desktopSessionCookieName {
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     portCookie,
			Value:    s.desktop.SessionCookieSecret,
			Path:     "/",
			MaxAge:   cookieMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     desktopSessionCookieName,
		Value:    s.desktop.SessionCookieSecret,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(c.Writer, c.Request, "/", http.StatusFound)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	written, err := cw.w.Write(p)
	cw.n += int64(written)
	return written, err
}

func (s *Server) handleDesktopSaveExport(c *Context) {
	sqlDrv, _, ok := s.getSQLDriver(c)
	if !ok {
		return
	}

	var body struct {
		Database     string   `json:"database"`
		Schema       string   `json:"schema"`
		Table        string   `json:"table"`
		Tables       []string `json:"tables"`
		SQL          string   `json:"sql"`
		Format       string   `json:"format"`
		IncludeDDL   bool     `json:"includeDdl"`
		DropIfExists bool     `json:"dropIfExists"`
		MaxRows      int      `json:"maxRows"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid export parameters"})
		return
	}

	format := strings.ToLower(strings.TrimSpace(body.Format))
	if format == "" {
		format = "sql"
	}
	if body.MaxRows <= 0 {
		body.MaxRows = 100000
	}

	ext := "csv"
	switch format {
	case "json":
		ext = "json"
	case "jsonl":
		ext = "jsonl"
	case "markdown":
		ext = "md"
	case "sql":
		ext = "sql"
	}

	baseName := strings.TrimSpace(body.Table)
	if baseName == "" {
		if len(body.Tables) == 1 {
			baseName = strings.TrimSpace(body.Tables[0])
		} else if len(body.Tables) > 1 {
			baseName = "selected_tables"
		} else if strings.TrimSpace(body.SQL) != "" {
			baseName = "query_export"
		} else if strings.TrimSpace(body.Database) != "" {
			baseName = strings.TrimSpace(body.Database) + "_dump"
		} else {
			baseName = "dbharbor_dump"
		}
	}
	defaultFilename := fmt.Sprintf("%s_%s.%s", sanitizeExportName(baseName), time.Now().Format("20060102_150405"), ext)

	var targetPath string
	if s.desktop.SaveFileDialog != nil {
		chosen, canceled, err := s.desktop.SaveFileDialog(defaultFilename)
		if canceled {
			c.JSON(http.StatusOK, H{"canceled": true})
			return
		}
		if err == nil && strings.TrimSpace(chosen) != "" {
			targetPath = chosen
		}
	}

	if targetPath == "" {
		home, _ := os.UserHomeDir()
		dlDir := filepath.Join(home, "Downloads")
		if err := os.MkdirAll(dlDir, 0o755); err != nil {
			dlDir = os.TempDir()
		}
		targetPath = filepath.Join(dlDir, defaultFilename)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to prepare target directory: " + err.Error()})
		return
	}

	f, err := os.Create(targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create export file: " + err.Error()})
		return
	}
	defer f.Close()

	cw := &countingWriter{w: f}
	expReq := driver.ExportRequest{
		Database:     body.Database,
		Schema:       body.Schema,
		Table:        body.Table,
		Tables:       body.Tables,
		CustomSQL:    body.SQL,
		Format:       format,
		IncludeDDL:   body.IncludeDDL,
		DropIfExists: body.DropIfExists,
		MaxRows:      body.MaxRows,
	}
	if err := sqlDrv.StreamExport(c.Request.Context(), expReq, cw); err != nil {
		_ = f.Close()
		_ = os.Remove(targetPath)
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, H{
		"canceled":  false,
		"savedPath": targetPath,
		"bytes":     cw.n,
	})
}

func (s *Server) handleDesktopSaveDocExport(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	extDrv, ok := docDrv.(driver.DocExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Streaming collection export is not supported by this driver"})
		return
	}

	var body struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		FilterJSON string `json:"filterJson"`
		SortJSON   string `json:"sortJson"`
		Format     string `json:"format"`
		MaxRows    int    `json:"maxRows"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Collection) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid MongoDB export parameters"})
		return
	}

	format := strings.ToLower(strings.TrimSpace(body.Format))
	if format == "" {
		format = "json"
	}
	ext := "json"
	switch format {
	case "jsonl":
		ext = "jsonl"
	case "csv", "csv_bom":
		ext = "csv"
	}

	defaultFilename := fmt.Sprintf("%s_%s.%s", sanitizeExportName(body.Collection), time.Now().Format("20060102_150405"), ext)
	var targetPath string
	if s.desktop.SaveFileDialog != nil {
		chosen, canceled, err := s.desktop.SaveFileDialog(defaultFilename)
		if canceled {
			c.JSON(http.StatusOK, H{"canceled": true})
			return
		}
		if err == nil && strings.TrimSpace(chosen) != "" {
			targetPath = chosen
		}
	}
	if targetPath == "" {
		home, _ := os.UserHomeDir()
		dlDir := filepath.Join(home, "Downloads")
		if err := os.MkdirAll(dlDir, 0o755); err != nil {
			dlDir = os.TempDir()
		}
		targetPath = filepath.Join(dlDir, defaultFilename)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to prepare target directory: " + err.Error()})
		return
	}
	f, err := os.Create(targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create export file: " + err.Error()})
		return
	}
	defer f.Close()

	cw := &countingWriter{w: f}
	if err := extDrv.StreamExportCollection(c.Request.Context(), body.Database, body.Collection, body.FilterJSON, body.SortJSON, format, body.MaxRows, cw); err != nil {
		_ = f.Close()
		_ = os.Remove(targetPath)
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, H{
		"canceled":  false,
		"savedPath": targetPath,
		"bytes":     cw.n,
	})
}

func sanitizeExportName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "export"
	}
	return out
}

func (s *Server) handleDesktopSetWindowTheme(c *Context) {
	var body struct {
		DarkMode bool `json:"darkMode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid window theme payload"})
		return
	}
	s.desktop.DarkMode = body.DarkMode
	if s.desktop.SetWindowDarkMode != nil {
		s.desktop.SetWindowDarkMode(body.DarkMode)
	}
	c.JSON(http.StatusOK, H{"darkMode": body.DarkMode})
}

func (s *Server) handleDesktopPickFile(c *Context) {
	var body struct {
		Kind        string `json:"kind"`
		CurrentPath string `json:"currentPath"`
	}
	_ = c.ShouldBindJSON(&body)
	if s.desktop.OpenFileDialog == nil {
		c.JSON(http.StatusOK, H{"canceled": true, "unsupported": true})
		return
	}
	chosen, canceled, err := s.desktop.OpenFileDialog(body.Kind, body.CurrentPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if canceled || strings.TrimSpace(chosen) == "" {
		c.JSON(http.StatusOK, H{"canceled": true})
		return
	}
	c.JSON(http.StatusOK, H{
		"canceled": false,
		"filePath": chosen,
	})
}

type fsBrowseEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	SizeBytes int64  `json:"sizeBytes"`
	UpdatedAt string `json:"updatedAt"`
}

var allowedLocalDBExts = map[string]bool{
	".db":      true,
	".sqlite":  true,
	".sqlite3": true,
	".s3db":    true,
	".db3":     true,
	".duckdb":  true,
	".ddb":     true,
	".parquet": true,
	".csv":     true,
}

func (s *Server) resolveBaseDataDir() string {
	if s.desktop.DataDir != "" {
		return s.desktop.DataDir
	}
	if s.store != nil && s.store.DataDir() != "" {
		return s.store.DataDir()
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return os.TempDir()
}

var protectedInternalFiles = map[string]bool{
	"dbharbor.db":         true,
	"dbharbor.db-wal":     true,
	"dbharbor.db-shm":     true,
	"dbharbor.db-journal": true,
	".secret.key":         true,
	".master.key":         true,
	"ui_prefs.json":       true,
	"config.json":         true,
}

func hasWindowsADS(s string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'a' && trimmed[0] <= 'z') || (trimmed[0] >= 'A' && trimmed[0] <= 'Z')) {
		return strings.IndexByte(trimmed[2:], ':') >= 0
	}
	return strings.IndexByte(trimmed, ':') >= 0
}

func equalCleanPaths(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

func hasInvalidDBFilePathChars(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == 0x7f || b == '?' || b == '#' || b == '%' {
			return true
		}
	}
	return false
}

func hasControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func cleanDBFilePathCandidate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == ":memory:" || strings.HasPrefix(strings.ToLower(s), "file::memory:") {
		return ":memory:"
	}
	if strings.HasPrefix(strings.ToLower(s), "file:") {
		s = s[5:]
		s = strings.TrimPrefix(s, "//")
	}
	if qIdx := strings.IndexAny(s, "?#"); qIdx >= 0 {
		s = s[:qIdx]
	}
	return strings.TrimSpace(s)
}

func resolveCandidateAbsPath(baseDataDir, cleaned string) (string, error) {
	if filepath.IsAbs(cleaned) {
		return filepath.Abs(cleaned)
	}
	if baseDataDir != "" {
		return filepath.Abs(filepath.Join(baseDataDir, cleaned))
	}
	return filepath.Abs(cleaned)
}

func evalExistingSymlinkPath(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil && resolved != "" {
		return resolved
	}
	parent := filepath.Dir(absPath)
	if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil && resolvedParent != "" {
		return filepath.Join(resolvedParent, filepath.Base(absPath))
	}
	return absPath
}

func isPathWithinDir(targetPath, baseDir string) bool {
	absTarget, err1 := filepath.Abs(targetPath)
	absBase, err2 := filepath.Abs(baseDir)
	if err1 != nil || err2 != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		absTarget = strings.ToLower(absTarget)
		absBase = strings.ToLower(absBase)
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isProtectedMatch(absTarget, absBase string) bool {
	if isPathWithinDir(absTarget, filepath.Join(absBase, ".backups")) ||
		isPathWithinDir(absTarget, filepath.Join(absBase, "backups")) ||
		isPathWithinDir(absTarget, filepath.Join(absBase, "ephemeral_workspaces")) {
		return true
	}
	baseName := strings.ToLower(filepath.Base(absTarget))
	// Strip any trailing Windows ADS or dots/spaces just in case
	if cIdx := strings.IndexByte(baseName, ':'); cIdx >= 0 {
		baseName = baseName[:cIdx]
	}
	baseName = strings.TrimRight(baseName, ". ")
	if !protectedInternalFiles[baseName] && !strings.HasPrefix(baseName, "dbharbor.db") {
		return false
	}
	targetParent := filepath.Clean(filepath.Dir(absTarget))
	cleanBase := filepath.Clean(absBase)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(targetParent, cleanBase)
	}
	return targetParent == cleanBase
}

func (s *Server) isProtectedInternalPath(candidatePath string) bool {
	cleaned := cleanDBFilePathCandidate(candidatePath)
	if cleaned == "" || cleaned == ":memory:" {
		return false
	}
	baseDataDir := s.resolveBaseDataDir()
	absBase, err := filepath.Abs(baseDataDir)
	if err != nil {
		return false
	}
	absTarget, err := resolveCandidateAbsPath(baseDataDir, cleaned)
	if err != nil {
		return false
	}
	if isProtectedMatch(absTarget, absBase) {
		return true
	}
	// Also test against CWD-relative interpretation if cleaned was relative
	if !filepath.IsAbs(cleaned) {
		if cwdAbs, cErr := filepath.Abs(cleaned); cErr == nil && isProtectedMatch(cwdAbs, absBase) {
			return true
		}
	}
	realTarget := evalExistingSymlinkPath(absTarget)
	realBase := evalExistingSymlinkPath(absBase)
	return isProtectedMatch(realTarget, realBase)
}

func userScopedLocalDBBucket(userID string) string {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		uid = "anonymous"
	}
	sum := sha256.Sum256([]byte("dbharbor-localdb-user:" + uid))
	return "u_" + hex.EncodeToString(sum[:12])
}

func isUserScopedLocalDBBucketName(name string) bool {
	if len(name) != 26 || !strings.HasPrefix(strings.ToLower(name), "u_") {
		return false
	}
	for i := 2; i < len(name); i++ {
		ch := name[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

func isOtherUserLocalDBPath(targetPath, localDBDir, myBucket string) bool {
	absTarget, err1 := filepath.Abs(targetPath)
	absLocal, err2 := filepath.Abs(localDBDir)
	if err1 != nil || err2 != nil {
		return true
	}
	rel, err := filepath.Rel(absLocal, absTarget)
	if err != nil || rel == "." {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	firstSeg := rel
	if sepIdx := strings.IndexByte(rel, filepath.Separator); sepIdx >= 0 {
		firstSeg = rel[:sepIdx]
	}
	return isUserScopedLocalDBBucketName(firstSeg) && !strings.EqualFold(firstSeg, myBucket)
}

func verifyUploadedSingleFileDB(origBase, filePath string, written int64) error {
	ext := strings.ToLower(filepath.Ext(origBase))
	if allowedEphemeralExts[ext] {
		if written < 12 {
			return errors.New("uploaded file is not a valid SQLite 3 or DuckDB binary database")
		}
		f, err := os.Open(filePath)
		if err != nil {
			return errors.New("failed to inspect uploaded database file")
		}
		var hdr [32]byte
		n, _ := io.ReadFull(f, hdr[:])
		_ = f.Close()
		if _, _, vErr := detectAndValidateSingleFileDB(origBase, "auto", hdr[:n], written); vErr != nil {
			return vErr
		}
		return nil
	}
	if ext == ".parquet" {
		if written < 4 {
			return errors.New("uploaded file is not a valid Apache Parquet file")
		}
		f, err := os.Open(filePath)
		if err != nil {
			return errors.New("failed to inspect uploaded Parquet file")
		}
		var hdr [4]byte
		n, _ := io.ReadFull(f, hdr[:])
		_ = f.Close()
		if n < 4 || string(hdr[:4]) != "PAR1" {
			return errors.New("uploaded file header does not match Apache Parquet magic (PAR1)")
		}
	}
	return nil
}

func (s *Server) normalizeConnectionFilePathForWebUser(c *Context, rawFilePath string) string {
	trimmed := strings.TrimSpace(rawFilePath)
	if s.desktop.Enabled || c.IsAdmin() || trimmed == "" || trimmed == ":memory:" {
		return rawFilePath
	}
	if hasInvalidDBFilePathChars(trimmed) {
		return rawFilePath
	}
	low := strings.ToLower(trimmed)
	if strings.HasPrefix(low, "file:") || filepath.IsAbs(trimmed) || strings.ContainsAny(trimmed, `/\:`) || trimmed == "." || trimmed == ".." {
		return rawFilePath
	}
	if s.isProtectedInternalPath(trimmed) {
		return rawFilePath
	}
	return filepath.ToSlash(filepath.Join("local_databases", userScopedLocalDBBucket(c.UserID()), trimmed))
}

func (s *Server) validateConnectionFilePath(c *Context, dbType, rawFilePath string) (int, string) {
	trimmed := strings.TrimSpace(rawFilePath)
	if trimmed == "" || trimmed == ":memory:" {
		return http.StatusOK, ""
	}
	if hasInvalidDBFilePathChars(trimmed) || strings.HasPrefix(strings.ToLower(trimmed), "file:") || hasWindowsADS(trimmed) {
		return http.StatusBadRequest, "Invalid database file path: query parameters, URI schemes, percent-encoding, and control characters are not allowed"
	}
	if s.isProtectedInternalPath(trimmed) {
		return http.StatusForbidden, "Access denied: connecting directly to DBHarbor's internal metadata or configuration files is prohibited"
	}
	if !s.desktop.Enabled && !c.IsAdmin() {
		baseDataDir := s.resolveBaseDataDir()
		localDBDir := filepath.Join(baseDataDir, "local_databases")
		myBucket := userScopedLocalDBBucket(c.UserID())
		userDir := filepath.Join(localDBDir, myBucket)
		_ = os.MkdirAll(userDir, 0o700)

		absTarget, err := resolveCandidateAbsPath(baseDataDir, trimmed)
		if err != nil || !isPathWithinDir(absTarget, userDir) || equalCleanPaths(absTarget, userDir) {
			return http.StatusForbidden, "Permission denied: non-admin accounts can only open database files inside their own isolated local_databases user directory"
		}
		realTarget := evalExistingSymlinkPath(absTarget)
		realUserDir := evalExistingSymlinkPath(userDir)
		if !isPathWithinDir(realTarget, realUserDir) || equalCleanPaths(realTarget, realUserDir) {
			return http.StatusForbidden, "Permission denied: symbolic links escaping your isolated local_databases directory are prohibited"
		}
		ext := strings.ToLower(filepath.Ext(absTarget))
		if !allowedLocalDBExts[ext] {
			return http.StatusForbidden, "Permission denied: unsupported local database file extension"
		}
	}
	return http.StatusOK, ""
}

func extractHostsFromURI(rawURI string) (scheme string, hosts []string, ok bool) {
	uStr := strings.TrimSpace(rawURI)
	if uStr == "" {
		return "", nil, true
	}
	parsed, err := url.Parse(uStr)
	if err != nil || parsed.Host == "" {
		return "", nil, false
	}
	scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	for _, part := range strings.Split(parsed.Host, ",") {
		hp := strings.TrimSpace(part)
		if atIdx := strings.LastIndexByte(hp, '@'); atIdx >= 0 {
			hp = strings.TrimSpace(hp[atIdx+1:])
		}
		if hp == "" {
			return scheme, nil, false
		}
		if h, _, sErr := net.SplitHostPort(hp); sErr == nil {
			hosts = append(hosts, strings.Trim(h, "[]"))
		} else {
			hosts = append(hosts, strings.Trim(hp, "[]"))
		}
	}
	if len(hosts) == 0 {
		return scheme, nil, false
	}
	return scheme, hosts, true
}

var restrictedNonAdminOptions = map[string]bool{
	"tlscafile":                   true,
	"tlscertificatekeyfile":       true,
	"sslclientcertificatekeyfile": true,
	"sslcertificateauthorityfile": true,
	"sslrootcert":                 true,
	"sslcert":                     true,
	"sslkey":                      true,
}

func (s *Server) validateRemoteEndpointForWebUser(c *Context, cfg driver.ConnectionConfig) (int, string) {
	if s.desktop.Enabled || c.IsAdmin() {
		return http.StatusOK, ""
	}
	dbType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if dbType == "sqlite" || dbType == "duckdb" {
		return http.StatusOK, ""
	}

	for k, v := range cfg.Options {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if restrictedNonAdminOptions[strings.ToLower(strings.TrimSpace(k))] {
			return http.StatusForbidden, fmt.Sprintf("Permission denied: connection option %q is restricted for non-administrator accounts", k)
		}
	}

	rawURI := strings.TrimSpace(cfg.URI)
	if rawURI != "" && (dbType == "mongodb" || strings.HasPrefix(strings.ToLower(rawURI), "mongodb")) {
		if err := sqlbase.ValidateMongoURIForRestrictedWeb(rawURI); err != nil {
			return http.StatusForbidden, "Permission denied: " + err.Error()
		}
	}

	rawHost := strings.TrimSpace(cfg.Host)
	if rawHost != "" {
		lowHost := strings.ToLower(rawHost)
		if strings.Contains(lowHost, "/") || strings.Contains(lowHost, `\`) || strings.Contains(lowHost, ".sock") {
			return http.StatusForbidden, "Permission denied: Unix domain socket hosts are restricted for non-administrator accounts"
		}
	}

	if s.opts.AllowPrivateNetForNonAdmin {
		return http.StatusOK, ""
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	if rawURI != "" {
		scheme, uriHosts, ok := extractHostsFromURI(rawURI)
		if !ok {
			return http.StatusForbidden, "Permission denied: invalid or unverifiable connection URI for non-administrator account"
		}
		for _, h := range uriHosts {
			var err error
			if scheme == "mongodb+srv" {
				err = sqlbase.ValidateMongoSRVHost(ctx, h)
			} else {
				err = sqlbase.ValidateRemoteHost(ctx, h)
			}
			if err != nil {
				return http.StatusForbidden, "Permission denied: " + err.Error()
			}
		}
	}

	if rawHost == "" && rawURI == "" {
		rawHost = "127.0.0.1"
	}
	if rawHost != "" {
		for _, part := range strings.Split(rawHost, ",") {
			hp := strings.TrimSpace(part)
			if hp == "" {
				continue
			}
			if err := sqlbase.ValidateRemoteHost(ctx, hp); err != nil {
				return http.StatusForbidden, "Permission denied: " + err.Error()
			}
		}
	}
	return http.StatusOK, ""
}

func listFilesystemRoots(baseDataDir string, sandboxed bool, userBucket string) []H {
	var roots []H
	seen := map[string]bool{}
	addRoot := func(label, p string) {
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		if seen[clean] {
			return
		}
		if st, err := os.Stat(clean); err == nil && st.IsDir() {
			seen[clean] = true
			roots = append(roots, H{"label": label, "path": clean})
		}
	}

	if baseDataDir != "" {
		localDBDir := filepath.Join(baseDataDir, "local_databases")
		_ = os.MkdirAll(localDBDir, 0o700)
		if sandboxed {
			if userBucket != "" {
				userDir := filepath.Join(localDBDir, userBucket)
				_ = os.MkdirAll(userDir, 0o700)
				addRoot("My Uploads", userDir)
			}
			return roots
		}
		addRoot("Local DBs", localDBDir)
		addRoot("Data Dir", baseDataDir)
	}
	if sandboxed {
		return roots
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		addRoot("Home", home)
	}
	if runtime.GOOS == "windows" {
		for ch := 'C'; ch <= 'Z'; ch++ {
			drv := fmt.Sprintf("%c:\\", ch)
			addRoot(fmt.Sprintf("%c:", ch), drv)
		}
	} else {
		addRoot("/", "/")
	}
	return roots
}

func (s *Server) handleBrowseLocalFS(c *Context) {
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to browse server database files"})
		return
	}
	baseDataDir := s.resolveBaseDataDir()
	localDBDir := filepath.Join(baseDataDir, "local_databases")
	_ = os.MkdirAll(localDBDir, 0o700)
	sandboxed := !s.desktop.Enabled && !c.IsAdmin()
	myBucket := ""
	userDir := ""
	realUserDir := ""
	if sandboxed {
		myBucket = userScopedLocalDBBucket(c.UserID())
		userDir = filepath.Join(localDBDir, myBucket)
		_ = os.MkdirAll(userDir, 0o700)
		realUserDir = evalExistingSymlinkPath(userDir)
	}

	rawPath := strings.TrimSpace(c.Query("path"))
	if hasControlChars(rawPath) || (sandboxed && rawPath != "" && hasInvalidDBFilePathChars(rawPath)) {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid directory path"})
		return
	}
	showAll := (c.Query("all") == "1" || c.Query("all") == "true") && !sandboxed

	defaultDir := baseDataDir
	if sandboxed {
		defaultDir = userDir
	}

	targetDir := rawPath
	if targetDir == "" || targetDir == ":memory:" {
		targetDir = defaultDir
	}
	if abs, err := resolveCandidateAbsPath(baseDataDir, targetDir); err == nil {
		targetDir = abs
	}
	if sandboxed {
		evalTarget := evalExistingSymlinkPath(targetDir)
		if !isPathWithinDir(targetDir, userDir) || !isPathWithinDir(evalTarget, realUserDir) {
			c.JSON(http.StatusForbidden, H{"error": "Permission denied: non-admin accounts can only browse inside their own isolated local_databases user directory"})
			return
		}
	}
	if s.isProtectedInternalPath(targetDir) {
		c.JSON(http.StatusForbidden, H{"error": "Access denied: browsing internal protected directories is prohibited"})
		return
	}
	if st, err := os.Stat(targetDir); err != nil {
		targetDir = defaultDir
	} else if !st.IsDir() {
		targetDir = filepath.Dir(targetDir)
		if sandboxed {
			evalTarget := evalExistingSymlinkPath(targetDir)
			if !isPathWithinDir(targetDir, userDir) || !isPathWithinDir(evalTarget, realUserDir) {
				targetDir = defaultDir
			}
		}
	}

	dirEntries, err := os.ReadDir(targetDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Unable to read directory: " + err.Error()})
		return
	}

	entries := make([]fsBrowseEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "ephemeral_workspaces") || strings.EqualFold(name, "backups") {
			continue
		}
		fullEntryPath := filepath.Join(targetDir, name)
		if sandboxed {
			evalEntry := evalExistingSymlinkPath(fullEntryPath)
			if !isPathWithinDir(fullEntryPath, userDir) || !isPathWithinDir(evalEntry, realUserDir) {
				continue
			}
		}
		isDir := de.IsDir()
		if !isDir && s.isProtectedInternalPath(fullEntryPath) {
			continue
		}
		if !isDir && !showAll {
			ext := strings.ToLower(filepath.Ext(name))
			if !allowedLocalDBExts[ext] {
				continue
			}
		}
		var sz int64
		var modStr string
		if info, iErr := de.Info(); iErr == nil {
			if !isDir {
				sz = info.Size()
			}
			modStr = info.ModTime().Format("2006-01-02 15:04")
		}
		entries = append(entries, fsBrowseEntry{
			Name:      name,
			Path:      fullEntryPath,
			IsDir:     isDir,
			SizeBytes: sz,
			UpdatedAt: modStr,
		})
		if len(entries) >= 500 {
			break
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parentDir := filepath.Dir(targetDir)
	if parentDir == targetDir || (sandboxed && !isPathWithinDir(parentDir, userDir)) {
		parentDir = ""
	}
	reportedDataDir := baseDataDir
	if sandboxed {
		reportedDataDir = userDir
	}

	c.JSON(http.StatusOK, H{
		"currentDir": targetDir,
		"parentDir":  parentDir,
		"dataDir":    reportedDataDir,
		"roots":      listFilesystemRoots(baseDataDir, sandboxed, myBucket),
		"entries":    entries,
	})
}

func (s *Server) evictConnectionsMatchingFilePath(targetPath string, maxWait time.Duration) {
	if s.store == nil || s.pool == nil {
		return
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return
	}
	baseDataDir := s.resolveBaseDataDir()
	conns, err := s.store.ListConnections()
	if err != nil {
		return
	}
	for _, conn := range conns {
		fp := strings.TrimSpace(conn.FilePath)
		if fp == "" || fp == ":memory:" {
			continue
		}
		connAbs, cErr := resolveCandidateAbsPath(baseDataDir, fp)
		if cErr != nil {
			continue
		}
		if equalCleanPaths(connAbs, absTarget) {
			s.pool.EvictAndWait(conn.ID, maxWait)
			s.invalidateConnCache(conn.ID)
		}
	}
}

func (s *Server) cleanupUserLocalDBDir(userID string) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return
	}
	localDBDir := filepath.Join(s.resolveBaseDataDir(), "local_databases")
	uDir := filepath.Join(localDBDir, userScopedLocalDBBucket(uid))
	if !isPathWithinDir(uDir, localDBDir) || equalCleanPaths(uDir, localDBDir) {
		return
	}
	_ = os.RemoveAll(uDir)
}

func (s *Server) handleUploadLocalDBFile(c *Context) {
	if !c.RequireWrite() {
		return
	}
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to upload database files"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.maxImportBytes())
	part, rawFilename, fields, err := c.StreamFormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Please select a local database file to upload"})
		return
	}
	defer part.Close()

	origBase := filepath.Base(rawFilename)
	ext := strings.ToLower(filepath.Ext(origBase))
	if !allowedLocalDBExts[ext] {
		c.JSON(http.StatusBadRequest, H{"error": "Unsupported database file extension; allowed: .db, .sqlite, .sqlite3, .duckdb, .ddb, .parquet, .csv"})
		return
	}

	destDir := filepath.Join(s.resolveBaseDataDir(), "local_databases")
	if !s.desktop.Enabled && !c.IsAdmin() {
		destDir = filepath.Join(destDir, userScopedLocalDBBucket(c.UserID()))
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create local_databases directory: " + err.Error()})
		return
	}

	tmpFile, err := os.CreateTemp(destDir, ".upload_*"+ext)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to save database file: " + err.Error()})
		return
	}
	tmpPath := tmpFile.Name()
	written, copyErr := io.Copy(tmpFile, part)
	_ = tmpFile.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, H{"error": "Failed while writing database file: " + copyErr.Error()})
		return
	}

	// Drain any trailing form fields (e.g., overwrite appended after file in FormData)
	c.DrainMultipartFields(fields)

	if vErr := verifyUploadedSingleFileDB(origBase, tmpPath, written); vErr != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusBadRequest, H{"error": vErr.Error()})
		return
	}

	overwrite := strings.EqualFold(strings.TrimSpace(fields["overwrite"]), "true") || strings.EqualFold(strings.TrimSpace(c.Query("overwrite")), "true")
	stem := strings.TrimSuffix(origBase, filepath.Ext(origBase))
	safeName := sanitizeExportName(stem) + ext
	destPath := filepath.Join(destDir, safeName)
	if !overwrite {
		if _, err := os.Stat(destPath); err == nil {
			safeName = fmt.Sprintf("%s_%s%s", sanitizeExportName(stem), time.Now().Format("20060102_150405"), ext)
			destPath = filepath.Join(destDir, safeName)
		}
	}
	if overwrite {
		s.evictConnectionsMatchingFilePath(destPath, 1500*time.Millisecond)
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(destPath + suffix)
		}
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to finalize uploaded database file: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, H{
		"filePath":  destPath,
		"fileName":  safeName,
		"sizeBytes": written,
	})
}
