package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlite"
	"dbharbor/backend/internal/store"
)

const (
	maxEphemeralSessionsPerUser = 8
	defaultEphemeralIdleTTL     = 12 * time.Hour
	sqliteMagicHeader           = "SQLite format 3\x00"
)

var allowedEphemeralExts = map[string]bool{
	".sqlite":  true,
	".sqlite3": true,
	".db":      true,
	".db3":     true,
	".s3db":    true,
	".duckdb":  true,
	".ddb":     true,
}

func ephemeralUserBucket(userID string) string {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		uid = "anonymous"
	}
	sum := sha256.Sum256([]byte("dbharbor-ephemeral-user:" + uid))
	return hex.EncodeToString(sum[:16])
}

func isStrictUUIDToken(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func (s *Server) ephemeralRootDir() string {
	return filepath.Join(s.resolveBaseDataDir(), "ephemeral_workspaces")
}

func (s *Server) ephemeralUserDir(userID string) string {
	return filepath.Join(s.ephemeralRootDir(), ephemeralUserBucket(userID))
}

func (s *Server) ephemeralConnDir(userID, connID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("missing user identity for ephemeral workspace")
	}
	if !isStrictUUIDToken(connID) {
		return "", errors.New("invalid ephemeral session identifier")
	}
	return filepath.Join(s.ephemeralUserDir(userID), strings.ToLower(connID)), nil
}

func detectAndValidateSingleFileDB(origFilename, explicitType string, hdr []byte, totalSize int64) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(origFilename))
	if !allowedEphemeralExts[ext] {
		return "", "", errors.New("unsupported file extension; allowed single-file databases: .sqlite, .sqlite3, .db, .db3, .s3db, .duckdb, .ddb")
	}
	reqType := strings.ToLower(strings.TrimSpace(explicitType))
	if reqType != "" && reqType != "auto" && reqType != "sqlite" && reqType != "duckdb" {
		return "", "", errors.New("unsupported single-file database driver type")
	}

	isDuckExt := ext == ".duckdb" || ext == ".ddb"
	isSQLiteOnlyExt := ext == ".sqlite" || ext == ".sqlite3" || ext == ".db3" || ext == ".s3db"

	if totalSize == 0 {
		if reqType == "duckdb" || (reqType == "" || reqType == "auto") && isDuckExt {
			if isSQLiteOnlyExt {
				return "", "", errors.New("file extension does not match DuckDB database type")
			}
			return "duckdb", ext, nil
		}
		if isDuckExt {
			return "", "", errors.New("file extension does not match SQLite 3 database type")
		}
		return "sqlite", ext, nil
	}

	hasDuckMagic := len(hdr) >= 12 && string(hdr[8:12]) == "DUCK"
	hasSQLiteMagic := len(hdr) >= 16 && string(hdr[:16]) == sqliteMagicHeader

	if hasDuckMagic {
		if reqType == "sqlite" || isSQLiteOnlyExt {
			return "", "", errors.New("file binary header is DuckDB, which conflicts with SQLite 3 file extension")
		}
		return "duckdb", ext, nil
	}
	if hasSQLiteMagic {
		if reqType == "duckdb" || isDuckExt {
			return "", "", errors.New("file binary header is SQLite 3, which conflicts with DuckDB file extension")
		}
		return "sqlite", ext, nil
	}

	return "", "", errors.New("selected file is not a valid SQLite 3 or DuckDB binary database")
}

func verifyBinarySnapshotMagic(dbType, snapPath string) (int64, error) {
	fi, err := os.Lstat(snapPath)
	if err != nil {
		return 0, errors.New("exported database snapshot is unavailable")
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return 0, errors.New("refusing to serve non-regular snapshot file")
	}
	sz := fi.Size()
	if sz == 0 {
		return 0, errors.New("exported database snapshot is empty")
	}
	f, err := os.Open(snapPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var hdr [16]byte
	n, _ := io.ReadFull(f, hdr[:])
	switch dbType {
	case "sqlite":
		if n < 16 || string(hdr[:16]) != sqliteMagicHeader {
			return 0, errors.New("snapshot failed SQLite 3 binary header verification")
		}
	case "duckdb":
		if n < 12 || string(hdr[8:12]) != "DUCK" {
			return 0, errors.New("snapshot failed DuckDB binary header verification")
		}
	default:
		return 0, errors.New("unsupported single-file database engine")
	}
	return sz, nil
}

func sanitizeDisplayDownloadFilename(rawName, dbType string) string {
	base := filepath.Base(strings.TrimSpace(rawName))
	ext := strings.ToLower(filepath.Ext(base))
	defaultExt := ".sqlite"
	if dbType == "duckdb" {
		defaultExt = ".duckdb"
	}
	if !allowedEphemeralExts[ext] {
		ext = defaultExt
	} else {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	cleanStem := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' || r == ':' || r == '*' || r == '?' || r == '<' || r == '>' || r == '|' || r == ';' {
			return '_'
		}
		return r
	}, strings.TrimSpace(base))
	cleanStem = strings.Trim(cleanStem, ". ")
	if cleanStem == "" {
		cleanStem = "database"
	}
	return cleanStem + ext
}

func buildAttachmentContentDisposition(filename string) string {
	asciiFallback := sanitizeExportName(strings.TrimSuffix(filename, filepath.Ext(filename))) + filepath.Ext(filename)
	if asciiFallback == "" || asciiFallback == filepath.Ext(filename) {
		asciiFallback = "database" + filepath.Ext(filename)
	}
	encodedUTF8 := url.PathEscape(filename)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", asciiFallback, encodedUTF8)
}

func (s *Server) reserveEphemeralSlot(userID, connDir string) (func(), bool) {
	cleanDir := filepath.Clean(connDir)
	s.ephemeralMu.Lock()
	if s.inFlightEphemeralByUser == nil {
		s.inFlightEphemeralByUser = make(map[string]int)
	}
	if s.inFlightEphemeralDirs == nil {
		s.inFlightEphemeralDirs = make(map[string]bool)
	}
	activeSaved := 0
	if s.store != nil {
		activeSaved = s.store.CountUserEphemeralConnections(userID)
	}
	if activeSaved+s.inFlightEphemeralByUser[userID] >= maxEphemeralSessionsPerUser {
		s.ephemeralMu.Unlock()
		return nil, false
	}
	s.inFlightEphemeralByUser[userID]++
	s.inFlightEphemeralDirs[cleanDir] = true
	s.ephemeralMu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			s.ephemeralMu.Lock()
			if n := s.inFlightEphemeralByUser[userID] - 1; n <= 0 {
				delete(s.inFlightEphemeralByUser, userID)
			} else {
				s.inFlightEphemeralByUser[userID] = n
			}
			delete(s.inFlightEphemeralDirs, cleanDir)
			s.ephemeralMu.Unlock()
		})
	}
	return release, true
}

func (s *Server) isEphemeralDirInFlight(dirPath string) bool {
	clean := filepath.Clean(dirPath)
	s.ephemeralMu.Lock()
	inFlight := s.inFlightEphemeralDirs != nil && s.inFlightEphemeralDirs[clean]
	s.ephemeralMu.Unlock()
	return inFlight
}

func (s *Server) handleOpenEphemeralDBFile(c *Context) {
	if !c.Principal.CanCreateConn {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: your account is not authorized to open local database files"})
		return
	}
	if !c.RequireWrite() {
		return
	}
	userID := strings.TrimSpace(c.UserID())
	if userID == "" {
		c.JSON(http.StatusUnauthorized, H{"error": "Authentication required"})
		return
	}

	// Reap expired ephemeral sessions first before atomically reserving per-user quota + workspace directory
	s.sweepEphemeralWorkspaces(defaultEphemeralIdleTTL)

	connID := strings.ToLower(crypto.NewUUID())
	connDir, err := s.ephemeralConnDir(userID, connID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	releaseSlot, ok := s.reserveEphemeralSlot(userID, connDir)
	if !ok {
		c.JSON(http.StatusTooManyRequests, H{
			"error": fmt.Sprintf("You already have %d active temporary database sessions open; please close an unused temporary session first", maxEphemeralSessionsPerUser),
		})
		return
	}
	defer releaseSlot()

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.maxImportBytes())
	part, fileName, fields, err := c.StreamFormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Please select a local SQLite 3 or DuckDB file to open"})
		return
	}
	defer part.Close()

	origBase := filepath.Base(strings.TrimSpace(fileName))
	ext := strings.ToLower(filepath.Ext(origBase))
	if !allowedEphemeralExts[ext] {
		c.JSON(http.StatusBadRequest, H{"error": "unsupported file extension; allowed single-file databases: .sqlite, .sqlite3, .db, .db3, .s3db, .duckdb, .ddb"})
		return
	}

	var magicBuf [16]byte
	magicN, readErr := io.ReadFull(part, magicBuf[:])
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		c.JSON(http.StatusBadRequest, H{"error": "Failed to inspect database file header"})
		return
	}

	if err := os.MkdirAll(connDir, 0o700); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to initialize isolated workspace directory"})
		return
	}
	_ = os.Chmod(s.ephemeralRootDir(), 0o700)
	_ = os.Chmod(s.ephemeralUserDir(userID), 0o700)
	_ = os.Chmod(connDir, 0o700)

	stem := sanitizeExportName(strings.TrimSuffix(origBase, filepath.Ext(origBase)))
	if stem == "" {
		stem = "workspace"
	}
	safeDiskName := stem + ext
	destPath := filepath.Join(connDir, safeDiskName)

	dst, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to create isolated database file"})
		return
	}

	combinedReader := io.MultiReader(bytes.NewReader(magicBuf[:magicN]), part)
	written, copyErr := io.Copy(dst, combinedReader)
	closeErr := dst.Close()
	_ = part.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusBadRequest, H{"error": "Failed while receiving uploaded database file"})
		return
	}

	// Drain any trailing text fields (e.g. when client appends file before driverType)
	c.DrainMultipartFields(fields)
	explicitDriver := strings.TrimSpace(fields["driverType"])
	if explicitDriver == "" {
		explicitDriver = strings.TrimSpace(fields["engine"])
	}
	if explicitDriver == "" {
		explicitDriver = strings.TrimSpace(c.Query("driverType"))
	}

	// Verify extension, explicit driverType, and binary magic header with exact final byte count
	dbType, _, vErr := detectAndValidateSingleFileDB(origBase, explicitDriver, magicBuf[:magicN], written)
	if vErr != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusBadRequest, H{"error": vErr.Error()})
		return
	}

	displayOrig := sanitizeDisplayDownloadFilename(origBase, dbType)
	badgeColor := "#13c2c2"
	if dbType == "duckdb" {
		badgeColor = "#d4b106"
	}

	connCfg := driver.ConnectionConfig{
		ID:       connID,
		Name:     displayOrig,
		Type:     dbType,
		Color:    badgeColor,
		Database: "main",
		FilePath: destPath,
		Options: map[string]string{
			"__ephemeral":       "1",
			"__ephemeral_owner": userID,
			"__orig_filename":   displayOrig,
		},
	}

	// Validate engine connectivity & enumerate initial tables inside Web sandbox BEFORE persisting metadata
	probeCfg := connCfg
	if !s.desktop.Enabled {
		probeCfg = s.prepareWebConnectionConfig(probeCfg)
	}
	drv, err := driver.Create(probeCfg)
	if err != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	_, pingErr := drv.Ping(ctx)
	var firstTable string
	var tablesCount int
	if pingErr == nil {
		if sqlDrv, ok := drv.(driver.SQLDriver); ok {
			if objs, oErr := sqlDrv.ListObjects(ctx, "main", ""); oErr == nil {
				tablesCount = len(objs.Tables)
				if tablesCount > 0 {
					firstTable = objs.Tables[0].Name
				}
			}
		}
	}
	cancel()
	_ = drv.Close()
	if pingErr != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusBadRequest, H{"error": "Failed to open database file: " + pingErr.Error()})
		return
	}

	vis := "private"
	ownerID := userID
	if s.desktop.Enabled {
		vis = "shared"
	}
	saved, err := s.store.InsertConnectionWithMeta(connCfg, ownerID, vis)
	if err != nil {
		_ = os.RemoveAll(connDir)
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	saved.Ephemeral = true
	saved.FilePath = displayOrig
	saved.Options = store.StripReservedConnectionOptions(saved.Options)

	c.JSON(http.StatusOK, H{
		"connection":  saved,
		"database":    "main",
		"firstTable":  firstTable,
		"tablesCount": tablesCount,
		"sizeBytes":   written,
	})
}

func (s *Server) handleExportDatabaseFile(c *Context) {
	connID := strings.TrimSpace(c.Param("id"))
	if !isStrictUUIDToken(connID) {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid connection identifier"})
		return
	}
	if !s.authorizeConn(c, connID, false) {
		return
	}
	userID := strings.TrimSpace(c.UserID())
	if userID == "" {
		c.JSON(http.StatusUnauthorized, H{"error": "Authentication required"})
		return
	}

	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}

	// Gate 1: Strictly restrict raw binary database downloads to ephemeral browser-uploaded sessions ONLY
	if !store.IsEphemeralConnection(cfg.Options) {
		c.JSON(http.StatusForbidden, H{
			"error": "Security policy: raw binary file download is restricted exclusively to browser-loaded temporary sessions",
		})
		return
	}
	if cfg.Options["__ephemeral_owner"] == "" || cfg.Options["__ephemeral_owner"] != userID {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: temporary database session belongs to another user"})
		return
	}
	if cfg.Type != "sqlite" && cfg.Type != "duckdb" {
		c.JSON(http.StatusBadRequest, H{"error": "Binary database file export is supported only for SQLite 3 and DuckDB"})
		return
	}

	// Gate 2: Canonical sandbox directory & symlink verification
	expectedDir, err := s.ephemeralConnDir(userID, connID)
	if err != nil {
		c.JSON(http.StatusForbidden, H{"error": "Invalid ephemeral workspace binding"})
		return
	}
	if !isPathWithinDir(cfg.FilePath, expectedDir) {
		c.JSON(http.StatusForbidden, H{"error": "Security policy violation: database file path is outside isolated workspace"})
		return
	}
	fi, statErr := os.Lstat(cfg.FilePath)
	if statErr != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		c.JSON(http.StatusForbidden, H{"error": "Security policy violation: workspace database file is missing or not a regular file"})
		return
	}
	realFile := evalExistingSymlinkPath(cfg.FilePath)
	realExpectedDir := evalExistingSymlinkPath(expectedDir)
	if !isPathWithinDir(realFile, realExpectedDir) {
		c.JSON(http.StatusForbidden, H{"error": "Security policy violation: symlink escape detected"})
		return
	}

	exporter, ok := drv.(driver.FileExporter)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Database driver does not support binary file export"})
		return
	}

	// Gate 3: Export consistent snapshot inside the same isolated directory & verify binary DB magic header
	snapPath := filepath.Join(expectedDir, fmt.Sprintf(".snapshot_%s.tmp", crypto.NewUUID()))
	defer func() {
		_ = os.Remove(snapPath)
	}()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	if err := exporter.ExportDatabaseFile(ctx, snapPath); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}

	snapSize, err := verifyBinarySnapshotMagic(cfg.Type, snapPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}

	snapFile, err := os.Open(snapPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "Failed to open exported snapshot"})
		return
	}
	defer snapFile.Close()

	downloadName := sanitizeDisplayDownloadFilename(cfg.Options["__orig_filename"], cfg.Type)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", buildAttachmentContentDisposition(downloadName))
	c.Header("Content-Length", fmt.Sprintf("%d", snapSize))
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, snapFile)
}

func (s *Server) cleanupEphemeralWorkspace(cfg driver.ConnectionConfig) {
	if !store.IsEphemeralConnection(cfg.Options) {
		return
	}
	if cfg.FilePath != "" {
		sqlite.ReleaseDuckDBFileLock(cfg.FilePath)
	}
	ownerID := strings.TrimSpace(cfg.Options["__ephemeral_owner"])
	if ownerID == "" || !isStrictUUIDToken(cfg.ID) {
		return
	}
	connDir, err := s.ephemeralConnDir(ownerID, cfg.ID)
	if err != nil {
		return
	}
	if !isPathWithinDir(connDir, s.ephemeralRootDir()) {
		return
	}
	_ = os.RemoveAll(connDir)
}

func (s *Server) cleanupUserEphemeralWorkspaces(userID string) {
	if strings.TrimSpace(userID) == "" {
		return
	}
	uDir := s.ephemeralUserDir(userID)
	if !isPathWithinDir(uDir, s.ephemeralRootDir()) {
		return
	}
	_ = os.RemoveAll(uDir)
}

func latestDirModTime(dirPath string) time.Time {
	var latest time.Time
	if st, err := os.Stat(dirPath); err == nil {
		latest = st.ModTime()
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return latest
	}
	for _, de := range entries {
		if info, iErr := de.Info(); iErr == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

func (s *Server) sweepEphemeralWorkspaces(idleTTL time.Duration) {
	if s.store == nil {
		return
	}
	if idleTTL <= 0 {
		idleTTL = defaultEphemeralIdleTTL
	}
	now := time.Now()

	allConns, err := s.store.ListConnections()
	if err != nil {
		return
	}

	const orphanGracePeriod = 15 * time.Minute

	validDirs := make(map[string]bool)
	for _, c := range allConns {
		if !store.IsEphemeralConnection(c.Options) {
			continue
		}
		ownerID := strings.TrimSpace(c.Options["__ephemeral_owner"])
		connDir, dErr := s.ephemeralConnDir(ownerID, c.ID)
		if dErr != nil {
			if s.pool != nil {
				s.pool.EvictAndWait(c.ID, 1500*time.Millisecond)
			}
			s.invalidateConnCache(c.ID)
			_ = s.store.DeleteConnection(c.ID)
			continue
		}

		// Keep if active in pool within idleTTL
		if s.pool != nil && s.pool.IsConnectionActiveWithin(c.ID, idleTTL) {
			validDirs[filepath.Clean(connDir)] = true
			continue
		}

		lastTouch := latestDirModTime(connDir)
		if ts, pErr := time.Parse(time.RFC3339, c.UpdatedAt); pErr == nil && ts.After(lastTouch) {
			lastTouch = ts
		}
		if lastTouch.IsZero() || now.Sub(lastTouch) >= idleTTL {
			if s.pool != nil {
				s.pool.EvictAndWait(c.ID, 1500*time.Millisecond)
			}
			s.invalidateConnCache(c.ID)
			_ = s.store.DeleteConnection(c.ID)
			_ = s.store.ClearHistoryForUser("", c.ID)
			if c.FilePath != "" {
				sqlite.ReleaseDuckDBFileLock(c.FilePath)
			}
			_ = os.RemoveAll(connDir)
			continue
		}
		validDirs[filepath.Clean(connDir)] = true
	}

	// Sweep any orphaned directories inside ephemeral_workspaces not backed by a live connection record,
	// preserving any directory currently in-flight or modified within the 15-minute grace window.
	rootDir := s.ephemeralRootDir()
	userBuckets, err := os.ReadDir(rootDir)
	if err != nil {
		return
	}
	for _, uEntry := range userBuckets {
		if !uEntry.IsDir() {
			continue
		}
		uPath := filepath.Join(rootDir, uEntry.Name())
		connEntries, rErr := os.ReadDir(uPath)
		if rErr != nil {
			continue
		}
		remaining := 0
		for _, cEntry := range connEntries {
			cPath := filepath.Clean(filepath.Join(uPath, cEntry.Name()))
			if validDirs[cPath] || s.isEphemeralDirInFlight(cPath) {
				remaining++
				continue
			}
			if modTime := latestDirModTime(cPath); !modTime.IsZero() && now.Sub(modTime) < orphanGracePeriod {
				remaining++
				continue
			}
			_ = os.RemoveAll(cPath)
		}
		if remaining == 0 {
			_ = os.Remove(uPath)
		}
	}
}
