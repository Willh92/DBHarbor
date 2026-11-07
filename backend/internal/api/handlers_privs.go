package api

import (
	"net/http"
	"strings"
	"time"

	"dbharbor/backend/internal/driver/dbprivs"
	"dbharbor/backend/internal/driver/sqlmeta"
	"dbharbor/backend/internal/store"
)

func (s *Server) handleGetObjectDDL(c *Context) {
	sqlDrv, cfg, ok := s.getSQLDriverAccess(c, false)
	if !ok {
		return
	}
	ddl, err := sqlmeta.FetchObjectDDL(
		c.Request.Context(),
		cfg.Type,
		sqlDrv,
		c.Query("database"),
		c.Query("schema"),
		c.Query("objectType"),
		c.Query("objectName"),
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"ddl": ddl})
}

func (s *Server) handlePrivCapabilities(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	sc, err := s.store.GetDecryptedConnection(connID)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": "Connection not found"})
		return
	}
	c.JSON(http.StatusOK, dbprivs.GetCapabilities(sc.Type))
}

func (s *Server) handlePrivListUsers(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	users, err := dbprivs.ListUsers(c.Request.Context(), cfg.Type, drv, c.Query("database"))
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if users == nil {
		users = []dbprivs.AccountInfo{}
	}
	c.JSON(http.StatusOK, H{"users": users})
}

func (s *Server) handlePrivGetUserGrants(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	grants, err := dbprivs.GetUserGrants(
		c.Request.Context(),
		cfg.Type,
		drv,
		c.Query("username"),
		c.Query("host"),
		c.Query("database"),
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	if grants == nil {
		grants = []dbprivs.GrantEntry{}
	}
	c.JSON(http.StatusOK, H{"grants": grants})
}

func (s *Server) handlePrivPreview(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, false) {
		return
	}
	sc, err := s.store.GetDecryptedConnection(connID)
	if err != nil {
		c.JSON(http.StatusNotFound, H{"error": "Connection not found"})
		return
	}
	var req dbprivs.ActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid privilege action parameters"})
		return
	}
	preview, err := dbprivs.BuildRedactedStatements(sc.Type, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"statements": preview})
}

func (s *Server) handlePrivExecute(c *Context) {
	connID := c.Param("id")
	if !s.authorizeConn(c, connID, true) {
		return
	}
	drv, cfg, err := s.acquireConnDriver(c, connID)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error()})
		return
	}
	var req dbprivs.ActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "Invalid privilege action parameters"})
		return
	}
	start := time.Now()
	redacted, _ := dbprivs.BuildRedactedStatements(cfg.Type, req)
	_, err = dbprivs.ExecuteAction(c.Request.Context(), cfg.Type, drv, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": err.Error(), "statements": redacted})
		return
	}
	s.store.AddHistory(store.HistoryEntry{
		UserID:       c.UserID(),
		ConnectionID: cfg.ID,
		ConnName:     cfg.Name,
		DBType:       cfg.Type,
		Database:     req.Database,
		Query:        strings.Join(redacted, ";\n"),
		ElapsedMs:    time.Since(start).Milliseconds(),
		Status:       "ok",
	})
	c.JSON(http.StatusOK, H{
		"message":    "Database privilege action executed successfully",
		"statements": redacted,
	})
}
