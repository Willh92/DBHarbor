package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
	"dbharbor/backend/internal/driver/sqlmeta"
	"dbharbor/backend/internal/store"
)

func (s *Server) requestExecContext(c *Context) context.Context {
	ctx := c.Request.Context()
	if !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin {
		ctx = sqlbase.WithRestrictedWebContext(ctx)
	}
	return ctx
}

const autocompleteCacheTTL = 20 * time.Second

func (s *Server) invalidateConnCache(connID string) {
	if connID == "" {
		return
	}
	prefix := connID + "|"
	s.metaCacheMu.Lock()
	for k := range s.metaCache {
		if strings.HasPrefix(k, prefix) {
			delete(s.metaCache, k)
		}
	}
	s.metaCacheMu.Unlock()
}

func (s *Server) getSQLDriver(c *Context) (driver.SQLDriver, driver.ConnectionConfig, bool) {
	return s.getSQLDriverAccess(c, false)
}

func (s *Server) getSQLDriverAccess(c *Context, requireWrite bool) (driver.SQLDriver, driver.ConnectionConfig, bool) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, requireWrite) {
		return nil, driver.ConnectionConfig{}, false
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return nil, cfg, false
	}
	sqlDrv, ok := drv.(driver.SQLDriver)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Data source %s is not a relational or OLAP SQL engine", cfg.Type)})
		return nil, cfg, false
	}
	return sqlDrv, cfg, true
}

func (s *Server) handleListDatabases(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	dbs, err := drv.ListDatabases(c.Request.Context())
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	if dbs == nil {
		dbs = []driver.DatabaseSummary{}
	}
	c.JSON(http.StatusOK, H{"databases": dbs})
}

func (s *Server) handleCreateDatabase(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Charset string `json:"charset"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Database name is required"})
		return
	}
	dbName := strings.TrimSpace(req.Name)
	start := time.Now()
	if err := drv.CreateDatabase(c.Request.Context(), dbName, req.Charset); err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	s.invalidateConnCache(connID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: dbName, Query: fmt.Sprintf("/* CREATE DATABASE %s */", dbName),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": "Database created successfully"})
}

func (s *Server) handleDropDatabase(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		var body struct {
			Name string `json:"name"`
		}
		_ = c.ShouldBindJSON(&body)
		name = strings.TrimSpace(body.Name)
	}
	if name == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Database name is required"})
		return
	}
	if sqlbase.IsProtectedSystemDatabase(cfg.Type, name) {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("System database %q is protected and cannot be dropped", name)})
		return
	}
	dropper, ok := drv.(driver.DatabaseDropper)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Dropping databases is not supported by %s connections", cfg.Type)})
		return
	}
	start := time.Now()
	if err := dropper.DropDatabase(c.Request.Context(), name); err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	s.invalidateConnCache(connID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: name, Query: fmt.Sprintf("/* DROP DATABASE %s */", name),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": fmt.Sprintf("Database %q dropped successfully", name)})
}

func (s *Server) handleListSchemas(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, false)
	if !ok {
		return
	}
	schemas, err := sqlDrv.ListSchemas(c.Request.Context(), c.Query("database"))
	if err != nil {
		s.respondConnError(c, cfg.ID, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, H{"schemas": schemas})
}

func (s *Server) handleCreateSchema(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	sm, ok := drv.(driver.SchemaManager)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Schema creation is not supported by %s", cfg.Type)})
		return
	}
	var req struct {
		Database string `json:"database"`
		Schema   string `json:"schema"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Schema) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Schema name is required"})
		return
	}
	schemaName := strings.TrimSpace(req.Schema)
	start := time.Now()
	if err := sm.CreateSchema(c.Request.Context(), strings.TrimSpace(req.Database), schemaName); err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	s.invalidateConnCache(connID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: req.Database, Query: fmt.Sprintf("/* CREATE SCHEMA %s */", schemaName),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": "Schema created successfully"})
}

func (s *Server) handleDropSchema(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	sm, ok := drv.(driver.SchemaManager)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": fmt.Sprintf("Schema deletion is not supported by %s", cfg.Type)})
		return
	}
	database := strings.TrimSpace(c.Query("database"))
	schemaName := strings.TrimSpace(c.Query("schema"))
	cascade := c.Query("cascade") == "true" || c.Query("cascade") == "1"
	if schemaName == "" {
		var body struct {
			Database string `json:"database"`
			Schema   string `json:"schema"`
			Cascade  bool   `json:"cascade"`
		}
		if c.ShouldBindJSON(&body) == nil {
			if database == "" {
				database = strings.TrimSpace(body.Database)
			}
			schemaName = strings.TrimSpace(body.Schema)
			if body.Cascade {
				cascade = true
			}
		}
	}
	if schemaName == "" {
		c.JSON(http.StatusBadRequest, H{"error": "Schema name is required"})
		return
	}
	start := time.Now()
	if err := sm.DropSchema(c.Request.Context(), database, schemaName, cascade); err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	s.invalidateConnCache(connID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: database, Query: fmt.Sprintf("/* DROP SCHEMA %s */", schemaName),
		ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
	})
	c.JSON(http.StatusOK, H{"message": fmt.Sprintf("Schema %q dropped successfully", schemaName)})
}

func (s *Server) handleListObjects(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriver(c)
	if !ok {
		return
	}
	objs, err := sqlDrv.ListObjects(c.Request.Context(), c.Query("database"), c.Query("schema"))
	if err != nil {
		s.respondConnError(c, cfg.ID, cfg.Type, err)
		return
	}
	sqlmeta.EnrichSchemaObjects(c.Request.Context(), cfg.Type, sqlDrv, &objs)
	c.JSON(http.StatusOK, objs)
}

func (s *Server) handleGetTableStructure(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriver(c)
	if !ok {
		return
	}
	st, err := sqlDrv.GetTableStructure(c.Request.Context(), c.Query("database"), c.Query("schema"), c.Query("table"))
	if err != nil {
		s.respondConnError(c, cfg.ID, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

func (s *Server) handleCreateTable(c *Context) {
	var req driver.CreateTableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, !req.PreviewOnly)
	if !ok {
		return
	}
	if !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin && strings.EqualFold(cfg.Type, "clickhouse") {
		if err := sqlbase.ValidateClickHouseWebEngineClause(req.Engine); err != nil {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
	}
	start := time.Now()
	ddl, err := sqlDrv.CreateTable(s.requestExecContext(c), req)
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"ddl": ddl})
		return
	}
	if !req.PreviewOnly && ddl != "" {
		s.invalidateConnCache(cfg.ID)
		s.store.AddHistory(store.HistoryEntry{
			UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
			Database: req.Database, Query: ddl, ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
		})
	}
	c.JSON(http.StatusOK, H{"ddl": ddl})
}

func (s *Server) handleAlterTable(c *Context) {
	var req driver.AlterTableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, !req.PreviewOnly)
	if !ok {
		return
	}
	if !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin && strings.EqualFold(cfg.Type, "clickhouse") {
		for _, act := range req.Actions {
			if strings.TrimSpace(act.Projection.Query) != "" {
				if err := sqlbase.ValidateClickHouseRestrictedWebSQL(act.Projection.Query); err != nil {
					c.JSON(http.StatusForbidden, H{"error": err.Error()})
					return
				}
			}
		}
	}
	start := time.Now()
	stmts, err := sqlDrv.AlterTable(s.requestExecContext(c), req)
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"sqls": stmts})
		return
	}
	if !req.PreviewOnly && len(stmts) > 0 {
		s.invalidateConnCache(cfg.ID)
		s.store.AddHistory(store.HistoryEntry{
			UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
			Database: req.Database, Query: strings.Join(stmts, ";\n"),
			ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
		})
	}
	c.JSON(http.StatusOK, H{"sqls": stmts})
}

func (s *Server) handleObjectAction(c *Context) {
	var req driver.ObjectActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, !req.PreviewOnly)
	if !ok {
		return
	}
	req.WebMode = !s.desktop.Enabled
	req.IsAdmin = s.desktop.Enabled || c.Principal.Role == store.RoleAdmin
	req.AllowPrivIP = s.desktop.Enabled || c.Principal.Role == store.RoleAdmin || s.opts.AllowPrivateNetForNonAdmin

	start := time.Now()
	sqls, err := sqlmeta.ExecuteObjectAction(s.requestExecContext(c), cfg.Type, sqlDrv, req)
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"sqls": sqls})
		return
	}
	if !req.PreviewOnly {
		s.invalidateConnCache(cfg.ID)
		histQuery := fmt.Sprintf("/* %s %s %s */", req.Action, req.ObjectType, req.ObjectName)
		if len(sqls) > 0 {
			histQuery = strings.Join(sqls, ";\n")
		}
		s.store.AddHistory(store.HistoryEntry{
			UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
			Database: req.Database, Query: histQuery,
			ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
		})
	}
	c.JSON(http.StatusOK, H{"message": "Operation completed successfully", "sqls": sqls})
}

func (s *Server) handleBrowseData(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, false)
	if !ok {
		return
	}
	var req driver.BrowseDataRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	if !c.CanWrite() && strings.TrimSpace(req.Filter) != "" {
		if err := sqlbase.ValidateReadOnlyScript("SELECT 1 WHERE " + req.Filter); err != nil {
			c.JSON(http.StatusForbidden, H{"error": "Read-only mode: " + err.Error()})
			return
		}
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 100
	} else if req.PageSize > 1000 {
		req.PageSize = 1000
	}
	res, err := sqlDrv.BrowseData(c.Request.Context(), req)
	if err != nil {
		s.respondConnError(c, cfg.ID, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handlePreviewMutation(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, true)
	if !ok {
		return
	}
	var req driver.RowMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	sqls, err := sqlDrv.BuildMutationPreviewSQL(req)
	if err != nil {
		s.respondConnError(c, cfg.ID, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, H{"sqls": sqls})
}

func (s *Server) handleApplyMutation(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, true)
	if !ok {
		return
	}
	var req driver.RowMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	start := time.Now()
	res, err := sqlDrv.ApplyRowMutations(c.Request.Context(), req)
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"executedSql": res.ExecutedSQL})
		return
	}
	if len(res.ExecutedSQL) > 0 {
		s.store.AddHistory(store.HistoryEntry{
			UserID:       c.UserID(),
			ConnectionID: cfg.ID,
			ConnName:     cfg.Name,
			DBType:       cfg.Type,
			Database:     req.Database,
			Query:        strings.Join(res.ExecutedSQL, ";\n"),
			ElapsedMs:    time.Since(start).Milliseconds(),
			Status:       "ok",
		})
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleListPartitions(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	pm, ok := drv.(driver.PartitionManager)
	if !ok {
		c.JSON(http.StatusOK, H{"partitions": []driver.PartitionInfo{}})
		return
	}
	parts, err := pm.ListPartitions(c.Request.Context(), c.Query("database"), c.Query("schema"), c.Query("table"))
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	c.JSON(http.StatusOK, H{"partitions": parts})
}

func (s *Server) handleManagePartition(c *Context) {
	connID := c.Param("id")
	var req driver.PartitionActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid request parameters"})
		return
	}
	if !s.authorizeConn(c, connID, !req.PreviewOnly) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		s.respondConnError(c, connID, cfg.Type, err)
		return
	}
	pm, ok := drv.(driver.PartitionManager)
	if !ok {
		c.JSON(http.StatusBadRequest, H{"error": "Partition management is not supported by this database engine"})
		return
	}
	start := time.Now()
	stmt, err := pm.ManagePartition(c.Request.Context(), req)
	if err != nil {
		s.respondConnErrorWithFields(c, connID, cfg.Type, err, H{"sql": stmt})
		return
	}
	if !req.PreviewOnly && stmt != "" {
		s.invalidateConnCache(connID)
		s.store.AddHistory(store.HistoryEntry{
			UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
			Database: req.Database, Query: stmt, ElapsedMs: time.Since(start).Milliseconds(), Status: "ok",
		})
	}
	c.JSON(http.StatusOK, H{"message": "Partition operation completed successfully", "sql": stmt})
}

func (s *Server) handleExecuteQuery(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, false)
	if !ok {
		return
	}
	var req struct {
		Database string `json:"database"`
		Query    string `json:"query"`
		MaxRows  int    `json:"maxRows"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "SQL query cannot be empty"})
		return
	}
	if !c.CanWrite() {
		if err := sqlbase.ValidateReadOnlyScript(req.Query); err != nil {
			c.JSON(http.StatusForbidden, H{"error": "Read-only mode: " + err.Error()})
			return
		}
	}
	if !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin && strings.EqualFold(cfg.Type, "clickhouse") {
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(req.Query); err != nil {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
	}
	if req.MaxRows <= 0 {
		req.MaxRows = 1000
	} else if req.MaxRows > 5000 {
		req.MaxRows = 5000
	}
	start := time.Now()
	results, err := sqlDrv.ExecuteSQL(s.requestExecContext(c), req.Database, req.Query, req.MaxRows)
	elapsed := time.Since(start).Milliseconds()

	for _, r := range results {
		if !r.IsSelect {
			s.invalidateConnCache(cfg.ID)
			break
		}
	}

	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID:       c.UserID(),
		ConnectionID: cfg.ID,
		ConnName:     cfg.Name,
		DBType:       cfg.Type,
		Database:     req.Database,
		Query:        req.Query,
		ElapsedMs:    elapsed,
		Status:       status,
		ErrorMsg:     errMsg,
	})

	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"partialResults": results})
		return
	}
	c.JSON(http.StatusOK, H{"results": results, "totalElapsedMs": elapsed})
}

func (s *Server) handleAutocomplete(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriver(c)
	if !ok {
		return
	}
	dbName := c.Query("database")
	force := c.Query("force") == "true" || c.Query("force") == "1"
	cacheKey := cfg.ID + "|" + dbName
	if !s.desktop.Enabled && !cfg.ShouldRememberPassword() {
		cacheKey = cfg.ID + "|" + s.credSessionKey(c) + "|" + dbName
	}

	if !force {
		s.metaCacheMu.RLock()
		entry, hit := s.metaCache[cacheKey]
		s.metaCacheMu.RUnlock()
		if hit && time.Now().Before(entry.expiresAt) {
			c.JSON(http.StatusOK, entry.meta)
			return
		}
	}

	meta, err := sqlDrv.GetAutocompleteMeta(c.Request.Context(), dbName)
	if err != nil {
		if sqlbase.IsAuthError(cfg.Type, err) {
			s.evictCallerConnPool(c, cfg.ID)
		}
		c.JSON(http.StatusOK, driver.AutocompleteMeta{Tables: map[string][]string{}})
		return
	}

	s.metaCacheMu.Lock()
	if len(s.metaCache) >= 64 {
		now := time.Now()
		var oldestKey string
		var oldestExp time.Time
		for k, v := range s.metaCache {
			if now.After(v.expiresAt) {
				delete(s.metaCache, k)
			} else if oldestKey == "" || v.expiresAt.Before(oldestExp) {
				oldestKey = k
				oldestExp = v.expiresAt
			}
		}
		if len(s.metaCache) >= 64 && oldestKey != "" {
			delete(s.metaCache, oldestKey)
		}
	}
	s.metaCache[cacheKey] = cachedAutocomplete{
		meta:      meta,
		expiresAt: time.Now().Add(autocompleteCacheTTL),
	}
	s.metaCacheMu.Unlock()

	c.JSON(http.StatusOK, meta)
}

func (s *Server) handleExport(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, false)
	if !ok {
		return
	}
	maxRows, _ := strconv.Atoi(c.DefaultQuery("maxRows", "50000"))
	table := strings.TrimSpace(c.Query("table"))
	customSQL := strings.TrimSpace(c.Query("sql"))
	if customSQL != "" && !c.CanWrite() {
		if err := sqlbase.ValidateReadOnlyScript(customSQL); err != nil {
			c.JSON(http.StatusForbidden, H{"error": "Read-only mode: " + err.Error()})
			return
		}
	}
	if customSQL != "" && !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin && strings.EqualFold(cfg.Type, "clickhouse") {
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(customSQL); err != nil {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
	}
	var tables []string
	if rawTables := strings.TrimSpace(c.Query("tables")); rawTables != "" {
		for _, part := range strings.Split(rawTables, ",") {
			if t := strings.TrimSpace(part); t != "" {
				tables = append(tables, t)
			}
		}
	}

	defaultFmt := "csv"
	if table == "" && customSQL == "" {
		defaultFmt = "sql"
	}
	format := strings.ToLower(c.DefaultQuery("format", defaultFmt))

	req := driver.ExportRequest{
		Database:     c.Query("database"),
		Schema:       c.Query("schema"),
		Table:        table,
		Tables:       tables,
		CustomSQL:    customSQL,
		Format:       format,
		IncludeDDL:   c.Query("includeDdl") == "true",
		DropIfExists: c.Query("dropIfExists") == "true",
		MaxRows:      maxRows,
	}

	ext := "csv"
	contentType := "text/csv; charset=utf-8"
	switch format {
	case "json":
		ext = "json"
		contentType = "application/json; charset=utf-8"
	case "jsonl":
		ext = "jsonl"
		contentType = "application/x-ndjson; charset=utf-8"
	case "markdown":
		ext = "md"
		contentType = "text/markdown; charset=utf-8"
	case "sql":
		ext = "sql"
		contentType = "application/sql; charset=utf-8"
	}

	baseName := table
	if baseName == "" {
		if len(tables) == 1 {
			baseName = tables[0]
		} else if len(tables) > 1 {
			baseName = "selected_tables"
		} else if customSQL != "" {
			baseName = "query_export"
		} else if req.Database != "" {
			baseName = req.Database + "_dump"
		} else {
			baseName = "dbharbor_dump"
		}
	}
	filename := fmt.Sprintf("%s_%s.%s", sanitizeExportName(baseName), time.Now().Format("20060102_150405"), ext)

	dw := &delayedAttachmentWriter{
		w:           c.Writer,
		contentType: contentType,
		disposition: fmt.Sprintf("attachment; filename=%q", filename),
	}
	if err := sqlDrv.StreamExport(s.requestExecContext(c), req, dw); err != nil {
		if !dw.wroteHeader {
			s.respondConnError(c, cfg.ID, cfg.Type, err)
			return
		}
		if sqlbase.IsAuthError(cfg.Type, err) {
			s.evictCallerConnPool(c, cfg.ID)
		}
		if format == "sql" {
			_, _ = fmt.Fprintf(dw, "\n/* Export Error: %s */\n", err.Error())
		}
		return
	}
	dw.commitHeaders()
}

func (s *Server) handleImport(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, true)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.maxImportBytes())
	part, origFilename, fields, err := c.StreamFormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Please upload a data file to import"})
		return
	}
	defer part.Close()

	fieldOrQuery := func(key string) string {
		if v := strings.TrimSpace(fields[key]); v != "" {
			return v
		}
		return strings.TrimSpace(c.Query(key))
	}

	format := strings.ToLower(fieldOrQuery("format"))
	if format == "" {
		lower := strings.ToLower(origFilename)
		switch {
		case strings.HasSuffix(lower, ".sql"):
			format = "sql"
		case strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".ndjson"):
			format = "jsonl"
		case strings.HasSuffix(lower, ".json"):
			format = "json"
		default:
			format = "csv"
		}
	}

	database := fieldOrQuery("database")
	table := fieldOrQuery("table")
	res, err := sqlDrv.BatchImport(s.requestExecContext(c), driver.ImportRequest{
		Database:        database,
		Schema:          fieldOrQuery("schema"),
		Table:           table,
		Format:          format,
		ContinueOnError: strings.EqualFold(fieldOrQuery("continueOnError"), "true"),
		Reader:          part,
	})
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	s.invalidateConnCache(cfg.ID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database:  database,
		Query:     fmt.Sprintf("/* IMPORT %s file=%s rows=%d */", strings.ToUpper(format), origFilename, res.ImportedRows),
		ElapsedMs: res.ElapsedMs, Status: status, ErrorMsg: errMsg,
	})
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"importedRows": res.ImportedRows, "warnings": res.Warnings})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleImportSQLText(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, true)
	if !ok {
		return
	}
	var req struct {
		Database        string `json:"database"`
		Schema          string `json:"schema"`
		SQL             string `json:"sql"`
		ContinueOnError bool   `json:"continueOnError"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SQL) == "" {
		c.JSON(http.StatusBadRequest, H{"error": "SQL script content cannot be empty"})
		return
	}
	if !s.desktop.Enabled && !c.IsAdmin() && !s.opts.AllowPrivateNetForNonAdmin && strings.EqualFold(cfg.Type, "clickhouse") {
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL(req.SQL); err != nil {
			c.JSON(http.StatusForbidden, H{"error": err.Error()})
			return
		}
	}

	res, err := sqlDrv.BatchImport(s.requestExecContext(c), driver.ImportRequest{
		Database:        req.Database,
		Schema:          req.Schema,
		Format:          "sql",
		ContinueOnError: req.ContinueOnError,
		Reader:          strings.NewReader(req.SQL),
	})
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	s.invalidateConnCache(cfg.ID)
	s.store.AddHistory(store.HistoryEntry{
		UserID: c.UserID(), ConnectionID: cfg.ID, ConnName: cfg.Name, DBType: cfg.Type,
		Database: req.Database, Query: req.SQL, ElapsedMs: res.ElapsedMs, Status: status, ErrorMsg: errMsg,
	})
	if err != nil {
		s.respondConnErrorWithFields(c, cfg.ID, cfg.Type, err, H{"importedRows": res.ImportedRows, "warnings": res.Warnings})
		return
	}
	c.JSON(http.StatusOK, res)
}
