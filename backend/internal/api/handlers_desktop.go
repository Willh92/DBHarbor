package api

import (
	"context"
	"crypto/subtle"
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

func cleanDBFilePathCandidate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == ":memory:" || strings.HasPrefix(strings.ToLower(s), "file::memory:") {
		return ":memory:"
	}
	if strings.HasPrefix(strings.ToLower(s), "file:") {
		s = s[5:]
		s = strings.TrimPrefix(s, "//")
	}
	if qIdx := strings.IndexByte(s, '?'); qIdx >= 0 {
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
		isPathWithinDir(absTarget, filepath.Join(absBase, "backups")) {
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

func (s *Server) normalizeConnectionFilePathForWebUser(c *Context, rawFilePath string) string {
	trimmed := strings.TrimSpace(rawFilePath)
	if s.desktop.Enabled || c.IsAdmin() || trimmed == "" || trimmed == ":memory:" {
		return rawFilePath
	}
	low := strings.ToLower(trimmed)
	if strings.HasPrefix(low, "file:") || filepath.IsAbs(trimmed) || strings.ContainsAny(trimmed, `/\:`) || trimmed == "." || trimmed == ".." {
		return rawFilePath
	}
	if s.isProtectedInternalPath(trimmed) {
		return rawFilePath
	}
	return filepath.ToSlash(filepath.Join("local_databases", trimmed))
}

func (s *Server) validateConnectionFilePath(c *Context, dbType, rawFilePath string) (int, string) {
	cleaned := cleanDBFilePathCandidate(rawFilePath)
	if cleaned == "" || cleaned == ":memory:" {
		return http.StatusOK, ""
	}
	if hasWindowsADS(cleaned) {
		return http.StatusBadRequest, "Invalid database file path"
	}
	if s.isProtectedInternalPath(cleaned) {
		return http.StatusForbidden, "Access denied: connecting directly to DBHarbor's internal metadata or configuration files is prohibited"
	}
	if !s.desktop.Enabled && !c.IsAdmin() {
		baseDataDir := s.resolveBaseDataDir()
		localDBDir := filepath.Join(baseDataDir, "local_databases")
		_ = os.MkdirAll(localDBDir, 0o700)
		absTarget, err := resolveCandidateAbsPath(baseDataDir, cleaned)
		if err != nil || !isPathWithinDir(absTarget, localDBDir) {
			return http.StatusForbidden, "Permission denied: non-admin accounts can only open database files inside the server local_databases directory"
		}
		realTarget := evalExistingSymlinkPath(absTarget)
		realLocal := evalExistingSymlinkPath(localDBDir)
		if !isPathWithinDir(realTarget, realLocal) {
			return http.StatusForbidden, "Permission denied: symbolic links escaping the local_databases sandbox are prohibited"
		}
	}
	return http.StatusOK, ""
}

func isRestrictedInternalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
}

func extractHostsFromMongoURI(rawURI string) []string {
	uStr := strings.TrimSpace(rawURI)
	if uStr == "" {
		return nil
	}
	if parsed, err := url.Parse(uStr); err == nil && parsed.Host != "" {
		var hosts []string
		for _, part := range strings.Split(parsed.Host, ",") {
			hp := strings.TrimSpace(part)
			if hp == "" {
				continue
			}
			if h, _, sErr := net.SplitHostPort(hp); sErr == nil {
				hosts = append(hosts, strings.Trim(h, "[]"))
			} else {
				hosts = append(hosts, strings.Trim(hp, "[]"))
			}
		}
		return hosts
	}
	return nil
}

func (s *Server) validateRemoteEndpointForWebUser(c *Context, cfg driver.ConnectionConfig) (int, string) {
	if s.desktop.Enabled || c.IsAdmin() || s.opts.AllowPrivateNetForNonAdmin {
		return http.StatusOK, ""
	}
	dbType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if dbType == "sqlite" || dbType == "duckdb" {
		return http.StatusOK, ""
	}

	var candidates []string
	if h := strings.TrimSpace(cfg.Host); h != "" {
		candidates = append(candidates, strings.Trim(h, "[]"))
	}
	if strings.TrimSpace(cfg.URI) != "" {
		candidates = append(candidates, extractHostsFromMongoURI(cfg.URI)...)
	}

	for _, host := range candidates {
		if host == "" {
			continue
		}
		if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
			return http.StatusForbidden, "Permission denied: non-admin accounts cannot connect to loopback, link-local, or private internal network addresses unless allowed by an administrator"
		}
		if ip := net.ParseIP(host); ip != nil {
			if isRestrictedInternalIP(ip) {
				return http.StatusForbidden, "Permission denied: non-admin accounts cannot connect to loopback, link-local, or private internal network addresses unless allowed by an administrator"
			}
			continue
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		cancel()
		if err == nil {
			for _, addr := range addrs {
				if isRestrictedInternalIP(addr.IP) {
					return http.StatusForbidden, "Permission denied: target hostname resolves to a restricted internal or loopback address"
				}
			}
		}
	}
	return http.StatusOK, ""
}

func listFilesystemRoots(baseDataDir string, sandboxed bool) []H {
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
		addRoot("Local DBs", localDBDir)
		if !sandboxed {
			addRoot("Data Dir", baseDataDir)
		}
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

	rawPath := strings.TrimSpace(c.Query("path"))
	showAll := (c.Query("all") == "1" || c.Query("all") == "true") && !sandboxed

	defaultDir := baseDataDir
	if sandboxed {
		defaultDir = localDBDir
	}

	targetDir := rawPath
	if targetDir == "" || targetDir == ":memory:" {
		targetDir = defaultDir
	}
	if abs, err := resolveCandidateAbsPath(baseDataDir, targetDir); err == nil {
		targetDir = abs
	}
	realLocalDBDir := evalExistingSymlinkPath(localDBDir)
	if sandboxed {
		if !isPathWithinDir(targetDir, localDBDir) || !isPathWithinDir(evalExistingSymlinkPath(targetDir), realLocalDBDir) {
			c.JSON(http.StatusForbidden, H{"error": "Permission denied: non-admin accounts can only browse inside the server local_databases directory"})
			return
		}
	}
	if st, err := os.Stat(targetDir); err != nil {
		targetDir = defaultDir
	} else if !st.IsDir() {
		targetDir = filepath.Dir(targetDir)
		if sandboxed && (!isPathWithinDir(targetDir, localDBDir) || !isPathWithinDir(evalExistingSymlinkPath(targetDir), realLocalDBDir)) {
			targetDir = defaultDir
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
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullEntryPath := filepath.Join(targetDir, name)
		if sandboxed && !isPathWithinDir(evalExistingSymlinkPath(fullEntryPath), realLocalDBDir) {
			continue
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
	if parentDir == targetDir || (sandboxed && !isPathWithinDir(parentDir, localDBDir)) {
		parentDir = ""
	}
	reportedDataDir := baseDataDir
	if sandboxed {
		reportedDataDir = localDBDir
	}

	c.JSON(http.StatusOK, H{
		"currentDir": targetDir,
		"parentDir":  parentDir,
		"dataDir":    reportedDataDir,
		"roots":      listFilesystemRoots(baseDataDir, sandboxed),
		"entries":    entries,
	})
}

func (s *Server) handleUploadLocalDBFile(c *Context) {
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to upload database files"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.maxImportBytes())
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Please select a local database file to upload"})
		return
	}

	origBase := filepath.Base(fileHeader.Filename)
	ext := strings.ToLower(filepath.Ext(origBase))
	if !allowedLocalDBExts[ext] {
		c.JSON(http.StatusBadRequest, H{"error": "Unsupported database file extension; allowed: .db, .sqlite, .sqlite3, .duckdb, .ddb, .parquet, .csv"})
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Failed to open uploaded file"})
		return
	}
	defer src.Close()

	destDir := filepath.Join(s.resolveBaseDataDir(), "local_databases")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create local_databases directory: " + err.Error()})
		return
	}

	stem := strings.TrimSuffix(origBase, filepath.Ext(origBase))
	safeName := sanitizeExportName(stem) + ext
	destPath := filepath.Join(destDir, safeName)
	if c.PostForm("overwrite") != "true" {
		if _, err := os.Stat(destPath); err == nil {
			safeName = fmt.Sprintf("%s_%s%s", sanitizeExportName(stem), time.Now().Format("20060102_150405"), ext)
			destPath = filepath.Join(destDir, safeName)
		}
	}

	dst, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to save database file: " + err.Error()})
		return
	}
	written, copyErr := io.Copy(dst, src)
	_ = dst.Close()
	if copyErr != nil {
		_ = os.Remove(destPath)
		c.JSON(http.StatusInternalServerError, H{"error": "Failed while writing database file: " + copyErr.Error()})
		return
	}

	c.JSON(http.StatusOK, H{
		"filePath":  destPath,
		"fileName":  safeName,
		"sizeBytes": written,
	})
}
