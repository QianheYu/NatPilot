package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/auth"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/proto"
	"github.com/ekko-studio/natpilot/internal/webui"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dbErr := ""
	if err := s.app.Store.Ping(r.Context()); err != nil {
		status = "degraded"
		dbErr = err.Error()
	}
	out := map[string]any{
		"status":      status,
		"version":     s.app.Version,
		"uptime_s":    int(time.Since(s.app.StartedAt).Seconds()),
		"db":          dbErr,
		"mode":        s.app.Config.EffectiveMode(),
		"server_time": time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config
	writeJSON(w, http.StatusOK, map[string]any{
		"product":         "NatPilot",
		"version":         s.app.Version,
		"mode":            cfg.EffectiveMode(),
		"auth_enabled":    s.app.Auth.Enabled(),
		"auth_required":   cfg.AuthRequired(),
		// 桌面/免登录模式只有一个内置管理员，不支持多用户与权限管理。
		"user_management": cfg.AuthRequired(),
		"frontend_built":  webui.HasFrontend(),
		"protocols":       cfg.Mapper.Protocols,
		"protocol_labels": protocolLabels(),
		"started_at":      s.app.StartedAt,
		"base_path":       cfg.HTTP.BasePath,
	})
}

func protocolLabels() []map[string]string {
	return []map[string]string{
		{"id": proto.KindIGD2, "name": "UPnP IGD v2", "note": "AddAnyPortMapping + leases (preferred)"},
		{"id": proto.KindIGD1, "name": "UPnP IGD v1", "note": "legacy WANIPConnection / WANPPPConnection"},
		{"id": proto.KindPCP, "name": "PCP (RFC 6887)", "note": "port control protocol"},
		{"id": proto.KindNATPMP, "name": "NAT-PMP (RFC 6886)", "note": "legacy compatibility"},
	}
}

// ---- auth ----

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.app.Auth.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"auth":     false,
			"identity": auth.Identity{Username: "local", Name: "Local operator", Role: "admin"},
			"message":  "authentication is disabled in this deployment mode",
		})
		return
	}
	var req loginRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	token, ident, err := s.app.Auth.Login(r.Context(), req.Username, req.Password, s.clientIP(r), r.UserAgent())
	if err != nil {
		code := http.StatusUnauthorized
		switch err {
		case auth.ErrLockedOut:
			code = http.StatusTooManyRequests
		case auth.ErrAccountDisabled:
			code = http.StatusForbidden
		}
		writeError(w, code, err.Error())
		return
	}
	s.app.Auth.SetSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auth": true, "identity": ident})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromCookie(r)
	ident := s.identity(r)
	s.app.Auth.Logout(token, ident.Username, s.clientIP(r), r.UserAgent())
	s.app.Auth.ClearSessionCookie(w)
	writeOK(w, nil)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ident := s.app.Auth.IdentityForRequest(r)
	out := map[string]any{
		"authenticated": ident.Auth,
		"identity":      ident,
		"auth_enabled":  s.app.Auth.Enabled(),
		"mode":          s.app.Config.EffectiveMode(),
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- users ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageArgs(q)
	users, total, err := s.app.Store.ListUsersPage(strings.TrimSpace(q.Get("q")), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total, "limit": limit, "offset": offset})
}

type userRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Enabled     *bool  `json:"enabled"`
	Password    string `json:"password"`
}

// userManagementDisabled reports whether multi-user management is unavailable.
//
// Desktop (免登录) mode runs as a single built-in administrator: there is nobody to
// log in as anyone else and no password to protect, so creating or editing accounts
// would only produce unreachable logins. The API rejects those calls instead of
// silently accepting them.
func (s *Server) userManagementDisabled() bool {
	return !s.app.Config.AuthRequired()
}

func (s *Server) rejectUserManagement(w http.ResponseWriter) bool {
	if !s.userManagementDisabled() {
		return false
	}
	writeError(w, http.StatusForbidden,
		"桌面模式（免登录）只有一个内置管理员且拥有全部权限，不支持用户与权限管理")
	return true
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.rejectUserManagement(w) {
		return
	}
	var req userRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if existing, err := s.app.Store.GetUserByName(req.Username); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	role := req.Role
	if role == "" {
		role = "operator"
	}
	if !validRole(role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	u := &model.User{
		Username: req.Username, DisplayName: req.DisplayName, Email: req.Email,
		Role: role, Enabled: enabled, PasswordHash: hash,
	}
	if _, err := s.app.Store.CreateUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.create", req.Username, "", true)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "user": u})
}

func validRole(role string) bool {
	switch role {
	case "admin", "operator", "viewer":
		return true
	}
	return false
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if s.rejectUserManagement(w) {
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	u, err := s.app.Store.GetUser(id)
	if err != nil || u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	var req userRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DisplayName != "" {
		u.DisplayName = req.DisplayName
	}
	if req.Email != "" {
		u.Email = req.Email
	}
	if req.Role != "" {
		if !validRole(req.Role) {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		u.Role = req.Role
	}
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	if req.Password != "" {
		if len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		hash, herr := auth.HashPassword(req.Password)
		if herr != nil {
			writeError(w, http.StatusInternalServerError, herr.Error())
			return
		}
		u.PasswordHash = hash
	}
	if err := s.app.Store.UpdateUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !u.Enabled {
		_ = s.app.Store.DeleteUserSessions(u.ID)
	}
	s.audit(r, "user.update", u.Username, "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if s.rejectUserManagement(w) {
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	ident := s.identity(r)
	if ident.UserID == id && ident.Auth {
		writeError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	u, err := s.app.Store.GetUser(id)
	if err != nil || u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := s.app.Store.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.app.Store.DeleteUserSessions(id)
	s.audit(r, "user.delete", u.Username, "", true)
	writeOK(w, nil)
}

func (s *Server) handleUserToken(w http.ResponseWriter, r *http.Request) {
	if s.rejectUserManagement(w) {
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	u, err := s.app.Store.GetUser(id)
	if err != nil || u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	token, err := s.app.Auth.SignToken(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.token", u.Username, "", true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"token":      token,
		"expires_in": s.app.Config.Auth.TokenTTLHours,
	})
}

type passwordRequest struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	ident := s.identity(r)
	if !ident.Auth {
		writeError(w, http.StatusBadRequest, "authentication is disabled; password changes are not applicable")
		return
	}
	var req passwordRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := s.app.Store.GetUser(ident.UserID)
	if err != nil || u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Current) {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	if len(req.New) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u.PasswordHash = hash
	if err := s.app.Store.UpdateUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "account.password", u.Username, "", true)
	writeOK(w, nil)
}

var _ = config.ModeDesktop
