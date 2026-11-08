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

const maxMultipartFieldBytes = 64 << 10

// Context wraps http.ResponseWriter and *http.Request without third-party router bloat.
type Context struct {
	Writer              http.ResponseWriter
	Request             *http.Request
	Claims              *auth.Claims
	Principal           store.UserPrincipal
	TrustedProxies      []*net.IPNet
	cleanups            []func()
	multipartReader     *multipart.Reader
	multipartFieldBytes int
}

func (c *Context) Defer(fn func()) {
	if fn != nil {
		c.cleanups = append(c.cleanups, fn)
	}
}

func (c *Context) RunCleanups() {
	for i := len(c.cleanups) - 1; i >= 0; i-- {
		c.cleanups[i]()
	}
	c.cleanups = nil
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
	maxBytes := int64(4 << 20)
	if c.Request.URL != nil {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/auth/") {
			maxBytes = 512 << 10
		} else if strings.Contains(p, "/import") {
			maxBytes = 16 << 20
		}
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	dec := json.NewDecoder(c.Request.Body)
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

// StreamFormFile streams a multipart file part directly from the HTTP socket without buffering into RAM or temp disk,
// collecting any preceding text form fields (capped at 64 KiB total).
func (c *Context) StreamFormFile(fieldName string) (*multipart.Part, string, map[string]string, error) {
	mr, err := c.Request.MultipartReader()
	if err != nil {
		return nil, "", nil, err
	}
	c.multipartReader = mr
	c.multipartFieldBytes = 0
	fields := make(map[string]string, 8)
	for {
		part, pErr := mr.NextPart()
		if pErr != nil {
			if errors.Is(pErr, io.EOF) {
				return nil, "", fields, errors.New("multipart file field not found: " + fieldName)
			}
			return nil, "", fields, pErr
		}
		formName := part.FormName()
		fileName := part.FileName()
		if formName == fieldName && fileName != "" {
			return part, fileName, fields, nil
		}
		if fileName == "" && formName != "" && c.multipartFieldBytes < maxMultipartFieldBytes {
			rem := maxMultipartFieldBytes - c.multipartFieldBytes
			b, _ := io.ReadAll(io.LimitReader(part, int64(rem)))
			c.multipartFieldBytes += len(b)
			fields[formName] = string(b)
		}
		_ = part.Close()
	}
}

// DrainMultipartFields reads any trailing non-file form fields emitted after the streamed file part (within the 64 KiB cap).
func (c *Context) DrainMultipartFields(fields map[string]string) {
	if c.multipartReader == nil || fields == nil {
		return
	}
	for c.multipartFieldBytes < maxMultipartFieldBytes {
		part, err := c.multipartReader.NextPart()
		if err != nil {
			break
		}
		formName := part.FormName()
		if part.FileName() == "" && formName != "" {
			rem := maxMultipartFieldBytes - c.multipartFieldBytes
			b, _ := io.ReadAll(io.LimitReader(part, int64(rem)))
			c.multipartFieldBytes += len(b)
			if _, exists := fields[formName]; !exists {
				fields[formName] = string(b)
			}
		}
		_ = part.Close()
	}
}

func isTrustedProxyIP(ip net.IP, trusted []*net.IPNet) bool {
	if ip == nil || len(trusted) == 0 {
		return false
	}
	for _, ipNet := range trusted {
		if ipNet != nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *Context) ClientIP() string {
	remote := strings.TrimSpace(c.Request.RemoteAddr)
	isUnix := remote == "" || remote == "@" || strings.HasPrefix(remote, "/") || strings.HasSuffix(remote, ".sock")
	peerHost := remote
	if isUnix {
		peerHost = "unix-local"
	} else if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		peerHost = host
	}
	peerHost = strings.Trim(peerHost, "[]")
	peerIP := net.ParseIP(peerHost)

	trustProxyHeaders := isUnix || (len(c.TrustedProxies) > 0 && isTrustedProxyIP(peerIP, c.TrustedProxies))
	if trustProxyHeaders {
		if xff := strings.TrimSpace(c.Request.Header.Get("X-Forwarded-For")); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				cand := strings.Trim(strings.TrimSpace(parts[i]), "[]")
				if cand == "" {
					continue
				}
				ip := net.ParseIP(cand)
				if ip == nil {
					continue
				}
				if isTrustedProxyIP(ip, c.TrustedProxies) {
					continue
				}
				return ip.String()
			}
		}
		if xrip := strings.Trim(strings.TrimSpace(c.Request.Header.Get("X-Real-IP")), "[]"); xrip != "" {
			if ip := net.ParseIP(xrip); ip != nil {
				return ip.String()
			}
		}
	}
	if peerIP != nil {
		return peerIP.String()
	}
	return peerHost
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

func (g *gzipAPIResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

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

type delayedAttachmentWriter struct {
	w           http.ResponseWriter
	contentType string
	disposition string
	wroteHeader bool
}

func (d *delayedAttachmentWriter) commitHeaders() {
	if d.wroteHeader {
		return
	}
	d.wroteHeader = true
	d.w.Header().Set("Content-Type", d.contentType)
	d.w.Header().Set("Content-Disposition", d.disposition)
	d.w.WriteHeader(http.StatusOK)
}

func (d *delayedAttachmentWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	d.commitHeaders()
	return d.w.Write(p)
}

func (d *delayedAttachmentWriter) Flush() {
	if d.wroteHeader {
		if fl, ok := d.w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}
