package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/mongodb"
	"dbharbor/backend/internal/driver/redis"
	"dbharbor/backend/internal/store"
)

func (s *Server) getKVDriverAccess(c *Context, requireWrite bool) (driver.KeyValueDriver, driver.ConnectionConfig, bool) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, requireWrite) {
		return nil, driver.ConnectionConfig{}, false
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return nil, cfg, false
	}
	kvDrv, ok := drv.(driver.KeyValueDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Data source %s is not a Key-Value engine", cfg.Type)})
		return nil, cfg, false
	}
	return kvDrv, cfg, true
}

func (s *Server) getDocDriverAccess(c *Context, requireWrite bool) (driver.DocumentDriver, driver.ConnectionConfig, bool) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, requireWrite) {
		return nil, driver.ConnectionConfig{}, false
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return nil, cfg, false
	}
	docDrv, ok := drv.(driver.DocumentDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Data source %s is not a Document database engine", cfg.Type)})
		return nil, cfg, false
	}
	return docDrv, cfg, true
}

// Redis Handlers

func (s *Server) handleKVScan(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, false)
	if !ok {
		return
	}
	dbIdx, _ := strconv.Atoi(c.DefaultQuery("db", "0"))
	cursor, _ := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
	count, _ := strconv.ParseInt(c.DefaultQuery("count", "200"), 10, 64)
	res, err := kvDrv.ScanKeys(c.Request.Context(), dbIdx, c.DefaultQuery("pattern", "*"), cursor, count)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleKVGetKey(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, false)
	if !ok {
		return
	}
	dbIdx, _ := strconv.Atoi(c.DefaultQuery("db", "0"))
	detail, err := kvDrv.GetKey(c.Request.Context(), dbIdx, c.Query("key"))
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleKVSetKey(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		DB int `json:"db"`
		driver.KVSetRequest
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	if err := kvDrv.SetKey(c.Request.Context(), req.DB, req.KVSetRequest); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Key saved successfully"})
}

func (s *Server) handleKVMutateItem(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		DB int `json:"db"`
		driver.KVItemMutationRequest
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	if err := kvDrv.MutateCollectionItem(c.Request.Context(), req.DB, req.KVItemMutationRequest); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Operation completed successfully"})
}

func (s *Server) handleKVDeleteKeys(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		DB   int      `json:"db"`
		Keys []string `json:"keys"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	deleted, err := kvDrv.DeleteKeys(c.Request.Context(), req.DB, req.Keys)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"deleted": deleted})
}

func (s *Server) handleKVSetTTL(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		DB  int    `json:"db"`
		Key string `json:"key"`
		TTL int64  `json:"ttl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	if err := kvDrv.SetTTL(c.Request.Context(), req.DB, req.Key, req.TTL); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "TTL updated successfully"})
}

func (s *Server) handleKVRenameKey(c *Context) {
	kvDrv, cfg, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	ext, ok := kvDrv.(driver.KVExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Key rename is not supported by this engine"})
		return
	}
	var req struct {
		DB     int    `json:"db"`
		OldKey string `json:"oldKey"`
		NewKey string `json:"newKey"`
		NXOnly bool   `json:"nxOnly"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	start := time.Now()
	if err := ext.RenameKey(c.Request.Context(), req.DB, req.OldKey, req.NewKey, req.NXOnly); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: fmt.Sprintf("db%d", req.DB), Query: fmt.Sprintf("RENAME %q %q", req.OldKey, req.NewKey),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": "Key renamed successfully"})
}

func (s *Server) handleKVDuplicateKey(c *Context) {
	kvDrv, cfg, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	ext, ok := kvDrv.(driver.KVExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Key duplication is not supported by this engine"})
		return
	}
	var req struct {
		DB      int    `json:"db"`
		SrcKey  string `json:"srcKey"`
		DstKey  string `json:"dstKey"`
		Replace bool   `json:"replace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	start := time.Now()
	if err := ext.DuplicateKey(c.Request.Context(), req.DB, req.SrcKey, req.DstKey, req.Replace); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: fmt.Sprintf("db%d", req.DB), Query: fmt.Sprintf("COPY %q %q", req.SrcKey, req.DstKey),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": "Key duplicated successfully"})
}

func (s *Server) handleKVFlushDB(c *Context) {
	kvDrv, cfg, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	ext, ok := kvDrv.(driver.KVExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Database flush is not supported by this engine"})
		return
	}
	var req struct {
		DB int `json:"db"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	start := time.Now()
	if err := ext.FlushDB(c.Request.Context(), req.DB); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: fmt.Sprintf("db%d", req.DB), Query: "FLUSHDB",
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": fmt.Sprintf("Logical database db%d flushed successfully", req.DB)})
}

func (s *Server) handleKVCommand(c *Context) {
	kvDrv, cfg, ok := s.getKVDriverAccess(c, false)
	if !ok {
		return
	}
	var req struct {
		DB      int    `json:"db"`
		Command string `json:"command"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request payload"})
		return
	}
	if !c.CanWrite() {
		if safe, reason := redis.IsReadOnlyCommand(req.Command); !safe {
			c.JSON(http.StatusForbidden, H{"error": "Read-only mode: " + reason})
			return
		}
	}
	res, err := kvDrv.ExecuteCommand(c.Request.Context(), req.DB, req.Command)
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: fmt.Sprintf("db%d", req.DB), Query: req.Command,
		ElapsedMs: res.ElapsedMs, Status: status, ErrorMsg: errMsg,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

type kvBackupPayload struct {
	Service    string                 `json:"service"`
	ExportedAt string                 `json:"exportedAt"`
	DB         int                    `json:"db"`
	Entries    []driver.KVEntryDetail `json:"entries"`
}

func (s *Server) handleKVExportKeys(c *Context) {
	kvDrv, _, ok := s.getKVDriverAccess(c, false)
	if !ok {
		return
	}
	var req struct {
		DB      int      `json:"db"`
		Pattern string   `json:"pattern"`
		Keys    []string `json:"keys"`
		MaxKeys int      `json:"maxKeys"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.MaxKeys <= 0 {
		req.MaxKeys = 500
	} else if req.MaxKeys > 2000 {
		req.MaxKeys = 2000
	}

	ctx := c.Request.Context()
	keys := req.Keys
	if len(keys) == 0 {
		pattern := strings.TrimSpace(req.Pattern)
		if pattern == "" {
			pattern = "*"
		}
		var cursor uint64
		seen := map[string]bool{}
		for len(keys) < req.MaxKeys {
			scanRes, err := kvDrv.ScanKeys(ctx, req.DB, pattern, cursor, 200)
			if err != nil {
				c.JSON(http.StatusBadRequest, H{"error": err.Error()})
				return
			}
			for _, ks := range scanRes.Keys {
				if !seen[ks.Key] {
					seen[ks.Key] = true
					keys = append(keys, ks.Key)
					if len(keys) >= req.MaxKeys {
						break
					}
				}
			}
			if scanRes.Cursor == 0 {
				break
			}
			cursor = scanRes.Cursor
		}
	}

	entries := make([]driver.KVEntryDetail, 0, len(keys))
	for _, k := range keys {
		if det, err := kvDrv.GetKey(ctx, req.DB, k); err == nil {
			entries = append(entries, det)
		}
	}

	c.JSON(http.StatusOK, kvBackupPayload{
		Service:    "DBHarbor-Redis-Backup",
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		DB:         req.DB,
		Entries:    entries,
	})
}

func (s *Server) handleKVImportKeys(c *Context) {
	kvDrv, cfg, ok := s.getKVDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		DB        int                  `json:"db"`
		Overwrite bool                 `json:"overwrite"`
		Entries   []driver.KVSetRequest `json:"entries"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Entries) == 0 {
		c.JSON(http.StatusBadRequest, H{"error": "Please provide at least one key entry to import"})
		return
	}

	ctx := c.Request.Context()
	start := time.Now()
	imported := 0
	skipped := 0
	for _, entry := range req.Entries {
		k := strings.TrimSpace(entry.Key)
		if k == "" {
			continue
		}
		if req.Overwrite {
			_, _ = kvDrv.DeleteKeys(ctx, req.DB, []string{k})
		} else {
			if _, existsErr := kvDrv.GetKey(ctx, req.DB, k); existsErr == nil {
				skipped++
				continue
			}
		}
		if err := kvDrv.SetKey(ctx, req.DB, entry); err != nil {
			skipped++
			continue
		}
		imported++
	}

	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: fmt.Sprintf("db%d", req.DB), Query: fmt.Sprintf("/* IMPORT %d KEYS */", imported),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{
		"imported": imported,
		"skipped":  skipped,
	})
}

// MongoDB Handlers

func (s *Server) handleDocListCollections(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	colls, err := docDrv.ListCollections(c.Request.Context(), c.Query("database"))
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"collections": colls})
}

func (s *Server) handleDocCreateCollection(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Collection) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Collection name is required"})
		return
	}
	if err := docDrv.CreateCollection(c.Request.Context(), req.Database, strings.TrimSpace(req.Collection)); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Collection created successfully"})
}

func (s *Server) handleDocDropCollection(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	if err := docDrv.DropCollection(c.Request.Context(), c.Query("database"), c.Query("collection")); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Collection dropped"})
}

func (s *Server) handleDocRenameCollection(c *Context) {
	docDrv, cfg, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	ext, ok := docDrv.(driver.DocExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Collection rename is not supported by this engine"})
		return
	}
	var req struct {
		Database string `json:"database"`
		OldName  string `json:"oldName"`
		NewName  string `json:"newName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	start := time.Now()
	if err := ext.RenameCollection(c.Request.Context(), req.Database, req.OldName, req.NewName); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: req.Database, Query: fmt.Sprintf("/* renameCollection %s -> %s */", req.OldName, req.NewName),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": "Collection renamed successfully"})
}

func (s *Server) handleDocExport(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	ext, ok := docDrv.(driver.DocExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Streaming collection export is not supported by this engine"})
		return
	}
	database := strings.TrimSpace(c.Query("database"))
	collection := strings.TrimSpace(c.Query("collection"))
	if database == "" || collection == "" {
		c.JSON(http.StatusBadRequest, H{"error": "database and collection query parameters are required"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	maxRows, _ := strconv.Atoi(c.DefaultQuery("maxRows", "50000"))

	extName := "json"
	contentType := "application/json; charset=utf-8"
	switch format {
	case "jsonl":
		extName = "jsonl"
		contentType = "application/x-ndjson; charset=utf-8"
	case "csv", "csv_bom":
		extName = "csv"
		contentType = "text/csv; charset=utf-8"
	}
	filename := fmt.Sprintf("%s_%s.%s", sanitizeExportName(collection), time.Now().Format("20060102_150405"), extName)
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if err := ext.StreamExportCollection(c.Request.Context(), database, collection, c.Query("filterJson"), c.Query("sortJson"), format, maxRows, c.Writer); err != nil {
		_, _ = fmt.Fprintf(c.Writer, "\n/* Export Error: %s */\n", err.Error())
	}
}

func (s *Server) handleDocFind(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	var req driver.DocFindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	res, err := docDrv.FindDocuments(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleDocInsert(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		Document   string `json:"document"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	id, err := docDrv.InsertDocument(c.Request.Context(), req.Database, req.Collection, req.Document)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"insertedId": id})
}

func (s *Server) handleDocImport(c *Context) {
	docDrv, cfg, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	extDrv, ok := docDrv.(driver.DocExtendedDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Batch document import is not supported by this driver"})
		return
	}

	ct := strings.ToLower(c.Request.Header.Get("Content-Type"))
	var (
		database        string
		collection      string
		format          string
		continueOnError bool
		reader          io.Reader
	)

	if strings.Contains(ct, "multipart/form-data") {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.maxImportBytes())
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, H{"error": "Please select a .json or .jsonl file to import"})
			return
		}
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, H{"error": "Failed to read uploaded file"})
			return
		}
		defer f.Close()
		reader = f
		database = strings.TrimSpace(c.PostForm("database"))
		collection = strings.TrimSpace(c.PostForm("collection"))
		format = strings.TrimSpace(c.PostForm("format"))
		continueOnError = c.PostForm("continueOnError") == "true"
		if format == "" {
			lower := strings.ToLower(fh.Filename)
			if strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".ndjson") {
				format = "jsonl"
			} else {
				format = "json"
			}
		}
	} else {
		var body struct {
			Database        string `json:"database"`
			Collection      string `json:"collection"`
			Format          string `json:"format"`
			Content         string `json:"content"`
			ContinueOnError bool   `json:"continueOnError"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, H{"error": "Invalid import request parameters"})
			return
		}
		database = strings.TrimSpace(body.Database)
		collection = strings.TrimSpace(body.Collection)
		format = strings.TrimSpace(body.Format)
		continueOnError = body.ContinueOnError
		reader = strings.NewReader(body.Content)
	}

	res, err := extDrv.ImportDocuments(c.Request.Context(), database, collection, reader, format, continueOnError)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error(), "importedRows": res.ImportedRows})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: database, Query: fmt.Sprintf("/* IMPORT %d DOCS INTO %s */", res.ImportedRows, collection),
		ElapsedMs: res.ElapsedMs, Status: "ok",
	})
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleDocUpdate(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		ID         string `json:"id"`
		Document   string `json:"document"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	if err := docDrv.UpdateDocument(c.Request.Context(), req.Database, req.Collection, req.ID, req.Document); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Document updated successfully"})
}

func (s *Server) handleDocDelete(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database   string   `json:"database"`
		Collection string   `json:"collection"`
		IDs        []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	cnt, err := docDrv.DeleteDocuments(c.Request.Context(), req.Database, req.Collection, req.IDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"deleted": cnt})
}

func (s *Server) handleDocListIndexes(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	idxs, err := docDrv.ListIndexes(c.Request.Context(), c.Query("database"), c.Query("collection"))
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"indexes": idxs})
}

func (s *Server) handleDocCreateIndex(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		KeysJSON   string `json:"keysJson"`
		Unique     bool   `json:"unique"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	if err := docDrv.CreateIndex(c.Request.Context(), req.Database, req.Collection, req.KeysJSON, req.Unique); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Index created successfully"})
}

func (s *Server) handleDocDropIndex(c *Context) {
	docDrv, _, ok := s.getDocDriverAccess(c, true)
	if !ok {
		return
	}
	if err := docDrv.DropIndex(c.Request.Context(), c.Query("database"), c.Query("collection"), c.Query("name")); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Index dropped"})
}

func (s *Server) handleDocRunCommand(c *Context) {
	docDrv, cfg, ok := s.getDocDriverAccess(c, false)
	if !ok {
		return
	}
	var req struct {
		Database    string `json:"database"`
		CommandJSON string `json:"commandJson"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	if !c.CanWrite() {
		if safe, reason := mongodb.IsReadOnlyCommand(req.CommandJSON); !safe {
			c.JSON(http.StatusForbidden, H{"error": "Read-only mode: " + reason})
			return
		}
	}
	start := time.Now()
	out, err := docDrv.RunCommand(c.Request.Context(), req.Database, req.CommandJSON)
	elapsed := time.Since(start).Milliseconds()
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: req.Database, Query: req.CommandJSON, ElapsedMs: elapsed, Status: status, ErrorMsg: errMsg,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"result": out, "elapsedMs": elapsed})
}

// Monitor Handlers

func (s *Server) handleGetMonitor(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	drv, _, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	mp, ok := drv.(driver.MonitorProvider)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Runtime monitoring is not supported by this driver"})
		return
	}
	snap, err := mp.GetMonitorSnapshot(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if !c.CanWrite() {
		snap.CanKillSession = false
	}
	c.JSON(http.StatusOK, snap)
}

func (s *Server) handleKillSession(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, _, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	mp, ok := drv.(driver.MonitorProvider)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Session termination is not supported by this driver"})
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Session ID is required"})
		return
	}
	if err := mp.KillSession(c.Request.Context(), req.SessionID); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"message": "Session termination command sent"})
}
