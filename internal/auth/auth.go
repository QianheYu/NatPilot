// Package auth implements login, session and API-token authentication for the
// web/GUI deployments. Headless deployments run without authentication.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

// Errors returned by the service.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrAccountDisabled    = errors.New("account is disabled")
	ErrLockedOut          = errors.New("too many failed attempts, try again later")
	ErrNoSession          = errors.New("not authenticated")
	ErrForbidden          = errors.New("insufficient permissions")
)

// SessionCookie is the cookie name for browser sessions.
const SessionCookie = "natpilot_session"

// tokenIssuer is the "iss" claim this service stamps on API tokens and requires
// when verifying one.
const tokenIssuer = "natpilot"

// Identity is the authenticated principal.
type Identity struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Auth     bool   `json:"authenticated"` // false in open (desktop) mode
}

// IsAdmin reports whether the identity may manage users and settings.
func (i Identity) IsAdmin() bool { return i.Role == "admin" || !i.Auth }

// CanWrite reports whether the identity may mutate state.
func (i Identity) CanWrite() bool {
	switch i.Role {
	case "viewer":
		return false
	default:
		return true
	}
}

// Service provides authentication operations.
type Service struct {
	store  *store.Store
	cfg    *config.Config
	secret []byte

	mu       sync.Mutex
	attempts map[string]*attempt
}

type attempt struct {
	count int
	until time.Time
}

// New builds the authentication service.
func New(st *store.Store, cfg *config.Config) *Service {
	secret := []byte(cfg.SessionSecretOrGenerate())
	return &Service{
		store:    st,
		cfg:      cfg,
		secret:   secret,
		attempts: map[string]*attempt{},
	}
}

// Enabled reports whether authentication is enforced.
func (s *Service) Enabled() bool { return s.cfg.AuthRequired() }

// EnsureBootstrapUser creates the initial administrator when no user exists.
func (s *Service) EnsureBootstrapUser() error {
	n, err := s.store.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	username := orDefault(s.cfg.Auth.BootstrapUser, "admin")
	password := strings.TrimSpace(s.cfg.Auth.BootstrapPass)
	generated := false
	if password == "" {
		password = randomPassword(16)
		generated = true
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	u := &model.User{
		Username:     username,
		DisplayName:  orDefault(s.cfg.Auth.BootstrapName, "Administrator"),
		Role:         "admin",
		Enabled:      true,
		PasswordHash: hash,
	}
	if _, err := s.store.CreateUser(u); err != nil {
		return err
	}
	logging.Info("auth", "bootstrap administrator created", "username", username)
	if generated {
		// The generated password is surfaced once, on stderr, so the operator
		// can log in; it is never written to the database in clear text.
		fmt.Printf("\n  NatPilot: generated administrator password for %q: %s\n  Change it after the first login.\n\n", username, password)
	}
	return nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifies a password against a stored hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// randomPassword returns a readable random password.
func randomPassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "changeme-" + hex.EncodeToString([]byte(time.Now().String()))[:8]
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

// Login validates credentials, creates a session and returns its token plus the
// identity.
func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (string, Identity, error) {
	key := strings.ToLower(username) + "|" + ip
	if st := s.lockState(key); st != nil {
		return "", Identity{}, ErrLockedOut
	}
	u, err := s.store.GetUserByName(username)
	if err != nil {
		return "", Identity{}, err
	}
	if u == nil || !CheckPassword(u.PasswordHash, password) {
		s.recordFailure(key)
		s.audit(username, "login", "user", "invalid credentials", ip, ua, false)
		return "", Identity{}, ErrInvalidCredentials
	}
	if !u.Enabled {
		s.audit(username, "login", "user", "disabled account", ip, ua, false)
		return "", Identity{}, ErrAccountDisabled
	}
	s.clearFailures(key)

	token, err := s.newSessionToken(u)
	if err != nil {
		return "", Identity{}, err
	}
	ttl := time.Duration(s.sessionTTLHours()) * time.Hour
	if err := s.store.CreateSession(token, u.ID, ttl, ip, ua); err != nil {
		return "", Identity{}, err
	}
	_ = s.store.TouchLogin(u.ID)
	s.audit(username, "login", "user", "success", ip, ua, true)
	return token, Identity{UserID: u.ID, Username: u.Username, Name: u.DisplayName, Role: u.Role, Auth: true}, nil
}

// Logout destroys a session.
func (s *Service) Logout(token, actor, ip, ua string) {
	if token != "" {
		_ = s.store.DeleteSession(token)
	}
	s.audit(actor, "logout", "user", "", ip, ua, true)
}

// sessionTTLHours returns the configured session lifetime.
func (s *Service) sessionTTLHours() int {
	if s.cfg.Auth.SessionTTLHours <= 0 {
		return 72
	}
	return s.cfg.Auth.SessionTTLHours
}

// newSessionToken generates an opaque random session token.
func (s *Service) newSessionToken(u *model.User) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ResolveSession resolves a session token to an identity.
func (s *Service) ResolveSession(token string) (Identity, bool) {
	if token == "" {
		return Identity{}, false
	}
	uid, ok, err := s.store.GetSession(token)
	if err != nil || !ok {
		return Identity{}, false
	}
	u, err := s.store.GetUser(uid)
	if err != nil || u == nil || !u.Enabled {
		return Identity{}, false
	}
	return Identity{UserID: u.ID, Username: u.Username, Name: u.DisplayName, Role: u.Role, Auth: true}, true
}

// SignToken issues a JWT API token for a user.
func (s *Service) SignToken(u *model.User) (string, error) {
	ttl := time.Duration(s.cfg.Auth.TokenTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	claims := jwt.MapClaims{
		"sub":  u.ID,
		"usr":  u.Username,
		"role": u.Role,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(ttl).Unix(),
		"iss":  tokenIssuer,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// VerifyToken validates a JWT API token.
func (s *Service) VerifyToken(raw string) (Identity, bool) {
	// An API token must always carry an expiry and must be signed with the single
	// algorithm this service issues; without WithExpirationRequired a token signed
	// elsewhere with the same secret would never expire.
	parsed, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(tokenIssuer),
	)
	if err != nil || !parsed.Valid {
		return Identity{}, false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Identity{}, false
	}
	sub, _ := claims["sub"].(float64)
	ident := Identity{UserID: int64(sub), Auth: true}
	if v, ok := claims["usr"].(string); ok {
		ident.Username = v
	}
	if v, ok := claims["role"].(string); ok {
		ident.Role = v
	}
	// A token must not outlive the account it was issued for: reject it when the user
	// has been deleted or disabled, even though the signature is still valid.
	u, err := s.store.GetUser(ident.UserID)
	if err != nil {
		return Identity{}, false
	}
	if u == nil || !u.Enabled {
		return Identity{}, false
	}
	ident.Name = u.DisplayName
	ident.Role = u.Role
	return ident, true
}

// IdentityForRequest extracts the caller identity from a request: the session
// cookie, a Bearer session/token, or the open (unauthenticated) identity.
func (s *Service) IdentityForRequest(r *http.Request) Identity {
	if !s.Enabled() {
		return Identity{Username: "local", Name: "Local operator", Role: "admin", Auth: false}
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if ident, ok := s.ResolveSession(c.Value); ok {
			return ident
		}
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		raw := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if raw == "" {
			return Identity{}
		}
		if ident, ok := s.ResolveSession(raw); ok {
			return ident
		}
		if ident, ok := s.VerifyToken(raw); ok {
			return ident
		}
	}
	if v := r.Header.Get("X-API-Key"); v != "" {
		if ident, ok := s.VerifyToken(v); ok {
			return ident
		}
		if ident, ok := s.ResolveSession(v); ok {
			return ident
		}
	}
	return Identity{}
}

// TokenFromCookie returns the raw session cookie value.
func TokenFromCookie(r *http.Request) string {
	if c, err := r.Cookie(SessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// SetSessionCookie writes the session cookie on the response.
func (s *Service) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	ttl := time.Duration(s.sessionTTLHours()) * time.Hour
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(r),
		MaxAge:   int(ttl.Seconds()),
	})
}

// cookieSecure reports whether the session cookie must be TLS-only.
//
// Looking at r.TLS alone silently downgrades the cookie to plaintext behind a
// TLS-terminating reverse proxy, so the forwarded scheme is trusted as well once
// the operator declares the process sits behind such a proxy.
func (s *Service) cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg != nil && s.cfg.HTTP.TrustProxy {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
			return true
		}
	}
	return s.cfg != nil && s.cfg.HTTP.TLS.Enabled
}

// ClearSessionCookie removes the session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// ---- lockout ----

func (s *Service) lockState(key string) *attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[key]
	if !ok {
		return nil
	}
	if time.Now().After(a.until) {
		delete(s.attempts, key)
		return nil
	}
	if a.count >= s.maxAttempts() {
		return a
	}
	return nil
}

func (s *Service) maxAttempts() int {
	if s.cfg.Auth.LockoutAttempts <= 0 {
		return 8
	}
	return s.cfg.Auth.LockoutAttempts
}

func (s *Service) recordFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[key]
	if !ok {
		a = &attempt{}
		s.attempts[key] = a
	}
	a.count++
	minutes := s.cfg.Auth.LockoutMinutes
	if minutes <= 0 {
		minutes = 15
	}
	a.until = time.Now().Add(time.Duration(minutes) * time.Minute)
}

func (s *Service) clearFailures(key string) {
	s.mu.Lock()
	delete(s.attempts, key)
	s.mu.Unlock()
}

func (s *Service) audit(actor, action, object, detail, ip, ua string, ok bool) {
	_ = s.store.AddAudit(&model.AuditEntry{
		Actor: actor, Action: action, Object: object, Detail: detail, IP: ip, UserAgent: ua, Success: ok,
	})
}
