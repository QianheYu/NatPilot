// Package api exposes the HTTP interface used by the web UI, the desktop shell
// and the CLI. The same handlers serve every deployment mode; authentication is
// only enforced in web/server modes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/auth"
	"github.com/ekko-studio/natpilot/internal/bus"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/mapper"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/scanner"
	"github.com/ekko-studio/natpilot/internal/scheduler"
	"github.com/ekko-studio/natpilot/internal/store"
	"github.com/ekko-studio/natpilot/internal/webui"
)

// App is the assembled application handed to the HTTP layer.
type App struct {
	Store     *store.Store
	Config    *config.Config
	Mapper    *mapper.Mapper
	Scanner   *scanner.Scanner
	Auth      *auth.Service
	Bus       *bus.Bus
	Scheduler *scheduler.Scheduler

	Version   string
	StartedAt time.Time
}

// Server implements http.Handler.
type Server struct {
	app *App
	mux *http.ServeMux
}

// NewServer builds the HTTP handler tree.
func NewServer(app *App) *Server {
	s := &Server{app: app, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler with CORS, logging and recovery.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Error("api", "panic in handler", "path", r.URL.Path, "panic", fmt.Sprint(rec))
			writeError(w, http.StatusInternalServerError, "internal error")
		}
	}()
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: 200}
	s.applyCORS(sw, r)
	if r.Method == http.MethodOptions {
		sw.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(sw, r)
	if !strings.HasPrefix(r.URL.Path, "/api/logs/stream") {
		logging.Debug("api", "request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it supports flushing (SSE).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	allowed := false
	for _, o := range s.app.Config.HTTP.CORSOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			allowed = true
			break
		}
	}
	if !allowed && len(s.app.Config.HTTP.CORSOrigins) == 0 {
		// Same-origin deployment (the SPA is served by this server); allow the
		// echo so the desktop shell (file://) can still call the API.
		allowed = strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1") ||
			strings.HasPrefix(origin, "file://") ||
			strings.HasPrefix(origin, "app://")
	}
	if !allowed {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-API-Key,X-Hermes-Profile")
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg, "status": code})
}

func writeOK(w http.ResponseWriter, extra map[string]any) {
	out := map[string]any{"ok": true}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeJSON reads a JSON body with a size limit.
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// Retry without strict field checking so newer clients keep working.
		if r.Body != nil {
			return err
		}
		return err
	}
	return nil
}

func decodeJSONLoose(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return errors.New("empty request body")
	}
	return json.Unmarshal(b, dst)
}

func pathInt(r *http.Request, name string) (int64, error) {
	v := r.PathValue(name)
	return strconv.ParseInt(v, 10, 64)
}

func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryBool(r *http.Request, name string) (bool, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return false, false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, true
	default:
		return false, true
	}
}

// ---- middleware ----

type ctxKey string

const identityKey ctxKey = "identity"

// identity extracts the caller identity.
func (s *Server) identity(r *http.Request) auth.Identity {
	if v := r.Context().Value(identityKey); v != nil {
		if id, ok := v.(auth.Identity); ok {
			return id
		}
	}
	return s.app.Auth.IdentityForRequest(r)
}

// authMiddleware resolves the identity and enforces authentication.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident := s.app.Auth.IdentityForRequest(r)
		if s.app.Auth.Enabled() && !ident.Auth {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, ident)
		next(w, r.WithContext(ctx))
	}
}

// adminMiddleware enforces the admin role (open mode always passes).
func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ident := s.identity(r)
		if ident.Auth && !ident.IsAdmin() {
			writeError(w, http.StatusForbidden, "administrator privileges required")
			return
		}
		next(w, r)
	})
}

// writeMiddleware rejects mutations from viewer accounts.
func (s *Server) writeMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ident := s.identity(r)
		if ident.Auth && !ident.CanWrite() {
			writeError(w, http.StatusForbidden, "this account has read-only access")
			return
		}
		next(w, r)
	})
}

// audit records a mutation.
func (s *Server) audit(r *http.Request, action, object, detail string, ok bool) {
	ident := s.identity(r)
	_ = s.app.Store.AddAudit(&model.AuditEntry{
		Actor:     ident.Username,
		Action:    action,
		Object:    object,
		Detail:    detail,
		IP:        s.clientIP(r),
		UserAgent: r.UserAgent(),
		Success:   ok,
	})
}

// clientIP returns the caller address used for audit logs and login lockout.
//
// X-Forwarded-For is honoured only when the operator declares that the process sits
// behind a trusted reverse proxy (http.trust_proxy). Trusting it unconditionally let
// any client pick its own address, which also defeated the per-IP login lockout.
func (s *Server) clientIP(r *http.Request) string {
	if s.app != nil && s.app.Config != nil && s.app.Config.HTTP.TrustProxy {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			for _, part := range strings.Split(v, ",") {
				if ip := strings.TrimSpace(part); ip != "" {
					return ip
				}
			}
		}
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
			return v
		}
	}
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	return addr[:i], addr[i+1:], nil
}

// ---- static frontend ----

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if strings.HasPrefix(path, "api/") {
		writeError(w, http.StatusNotFound, "unknown API endpoint")
		return
	}
	if err := webui.Serve(w, r, path); err != nil {
		logging.Debug("api", "static serve failed", "path", path, "error", err.Error())
		writeError(w, http.StatusNotFound, "not found")
	}
}
