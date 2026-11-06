package api

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/store"
)

// H is a shorthand map alias for JSON payloads.
type H map[string]any

// HandlerFunc defines a lightweight stdlib-backed HTTP handler.
type HandlerFunc func(*Context)

// Context wraps http.ResponseWriter and *http.Request without third-party router bloat.
type Context struct {
	Writer    http.ResponseWriter
	Request   *http.Request
	Claims    *auth.Claims
	Principal store.UserPrincipal
}

func (c *Context) Param(name string) string {
	return c.Request.PathValue(name)
}

func (c *Context) Query(name string) string {
	return c.Request.URL.Query().Get(name)
}

func (c *Context) DefaultQuery(name, fallback string) string {
	if v := c.Request.URL.Query().Get(name); v != "" {
		return v
	}
	return fallback
}

func (c *Context) Header(key, val string) {
	c.Writer.Header().Set(key, val)
}

func (c *Context) Status(code int) {
	c.Writer.WriteHeader(code)
}

func (c *Context) ShouldBindJSON(dst any) error {
	if c.Request.Body == nil {
		return errors.New("empty request body")
	}
	dec := json.NewDecoder(io.LimitReader(c.Request.Body, 32<<20))
	return dec.Decode(dst)
}

func (c *Context) JSON(code int, v any) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(code)
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	if c.Request.MultipartForm == nil {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
	}
	f, fh, err := c.Request.FormFile(name)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return fh, nil
}

func (c *Context) PostForm(name string) string {
	return c.Request.FormValue(name)
}

func (c *Context) UserID() string {
	if c.Principal.ID != "" {
		return c.Principal.ID
	}
	if c.Claims != nil {
		return c.Claims.UserID
	}
	return ""
}

func (c *Context) Username() string {
	if c.Principal.Username != "" {
		return c.Principal.Username
	}
	if c.Claims != nil {
		return c.Claims.Username
	}
	return ""
}

func (c *Context) Role() string {
	if c.Principal.Role != "" {
		return c.Principal.Role
	}
	return store.RoleReadOnly
}

func (c *Context) IsAdmin() bool {
	return c.Role() == store.RoleAdmin
}

func (c *Context) CanWrite() bool {
	r := c.Role()
	return r == store.RoleAdmin || r == store.RoleReadWrite
}

func (c *Context) RequireAdmin() bool {
	if !c.IsAdmin() {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: administrator privileges required"})
		return false
	}
	return true
}

func (c *Context) RequireWrite() bool {
	if !c.CanWrite() {
		c.JSON(http.StatusForbidden, H{"error": "Permission denied: read-only role cannot perform write or modification operations"})
		return false
	}
	return true
}

func (c *Context) SessionID() string {
	if c.Claims != nil {
		return c.Claims.SessionID
	}
	return ""
}

func (c *Context) ClientIP() string {
	if xff := strings.TrimSpace(c.Request.Header.Get("X-Forwarded-For")); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = strings.TrimSpace(xff[:idx])
		}
		if xff != "" {
			return xff
		}
	}
	if xrip := strings.TrimSpace(c.Request.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	remote := strings.TrimSpace(c.Request.RemoteAddr)
	if remote == "" || remote == "@" || strings.HasPrefix(remote, "/") || strings.HasSuffix(remote, ".sock") {
		return "unix-local"
	}
	host, _, err := net.SplitHostPort(remote)
	if err == nil && host != "" {
		return host
	}
	return remote
}

// --- Pooled Gzip Response Writer for JSON API payloads ---

var apiGzipPool = sync.Pool{
	New: func() any {
		gw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gw
	},
}

type gzipAPIResponseWriter struct {
	http.ResponseWriter
	gw          *gzip.Writer
	wroteHeader bool
}

func (g *gzipAPIResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	if code == http.StatusNoContent || code == http.StatusNotModified {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.Header().Set("Vary", "Accept-Encoding")
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipAPIResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.gw == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gw.Write(b)
}

func (g *gzipAPIResponseWriter) Flush() {
	if g.gw != nil {
		_ = g.gw.Flush()
	}
	if fl, ok := g.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (g *gzipAPIResponseWriter) close() {
	if g.gw != nil {
		if g.wroteHeader {
			_ = g.gw.Close()
		}
		g.gw.Reset(io.Discard)
		apiGzipPool.Put(g.gw)
		g.gw = nil
	}
}
