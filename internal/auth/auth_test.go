package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

const (
	testSessionSecret = "auth-test-session-secret-0123456789abcdef"
	testPassword      = "correct-horse-battery-staple"
)

// The bcrypt hash is computed once: bcrypt is deliberately slow and these tests
// only need one valid hash to compare against.
var (
	testHashOnce sync.Once
	testHash     string
	testHashErr  error
)

func testPasswordHash(t *testing.T) string {
	t.Helper()
	testHashOnce.Do(func() {
		testHash, testHashErr = HashPassword(testPassword)
	})
	if testHashErr != nil {
		t.Fatalf("HashPassword: %v", testHashErr)
	}
	return testHash
}

func newAuthForTest(t *testing.T) (*Service, *store.Store, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "auth.db")
	st, err := store.Open(dbPath+"?_busy_timeout=5000&_foreign_keys=on", dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := config.Default()
	cfg.App.DataDir = dir
	cfg.Database.Path = dbPath
	cfg.HTTP.SessionSecret = testSessionSecret
	cfg.SetAuthEnabled(true)
	cfg.Auth.RequireForWeb = true
	cfg.Auth.SessionTTLHours = 72
	cfg.Auth.LockoutAttempts = 8
	cfg.Auth.LockoutMinutes = 15
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}
	return New(st, cfg), st, cfg
}

func createUserForTest(t *testing.T, st *store.Store, username, role string, enabled bool) *model.User {
	t.Helper()
	u := &model.User{
		Username:     username,
		DisplayName:  strings.ToUpper(username[:1]) + username[1:],
		Role:         role,
		Enabled:      enabled,
		PasswordHash: testPasswordHash(t),
	}
	id, err := st.CreateUser(u)
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", username, err)
	}
	u.ID = id
	return u
}

func setUserEnabledForTest(t *testing.T, st *store.Store, id int64, enabled bool) {
	t.Helper()
	u, err := st.GetUser(id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	if u == nil {
		t.Fatalf("GetUser(%d) returned nil", id)
	}
	u.Enabled = enabled
	if err := st.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser(%d): %v", id, err)
	}
}

func mintToken(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

// tamperSignature rewrites the last segment so the signature no longer matches.
func tamperSignature(t *testing.T, raw string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT %q", raw)
	}
	parts[2] = strings.Repeat("A", len(parts[2]))
	return strings.Join(parts, ".")
}

// tamperClaims re-encodes the payload without re-signing it.
func tamperClaims(t *testing.T, raw string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT %q", raw)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	claims := jwt.MapClaims{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	mutate(claims)
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(encoded)
	return strings.Join(parts, ".")
}

func TestHashPasswordVerify(t *testing.T) {
	hash, err := HashPassword("s3cret-value")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || hash == "s3cret-value" {
		t.Fatalf("HashPassword did not produce a hash: %q", hash)
	}
	if !CheckPassword(hash, "s3cret-value") {
		t.Error("CheckPassword rejected the correct password")
	}
	if CheckPassword(hash, "s3cret-valuE") {
		t.Error("CheckPassword accepted a wrong password")
	}
	if CheckPassword("", "anything") {
		t.Error("CheckPassword accepted an empty hash")
	}

	// Two hashes of the same password must differ (salted) yet both verify.
	second, err := HashPassword("s3cret-value")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if second == hash {
		t.Error("bcrypt produced two identical hashes for the same password")
	}
	if !CheckPassword(second, "s3cret-value") {
		t.Error("second hash does not verify")
	}
}

func TestVerifyToken(t *testing.T) {
	svc, st, _ := newAuthForTest(t)
	u := createUserForTest(t, st, "alice", "operator", true)

	valid, err := svc.SignToken(u)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if strings.Count(valid, ".") != 2 {
		t.Fatalf("SignToken returned a malformed JWT: %q", valid)
	}

	now := time.Now()
	claims := func() jwt.MapClaims {
		return jwt.MapClaims{
			"sub":  u.ID,
			"usr":  u.Username,
			"role": u.Role,
			"iat":  now.Unix(),
			"exp":  now.Add(time.Hour).Unix(),
			"iss":  "natpilot",
		}
	}
	expired := claims()
	expired["exp"] = now.Add(-time.Hour).Unix()
	noExpiry := claims()
	delete(noExpiry, "exp")
	stringSubject := claims()
	stringSubject["sub"] = "1"
	unknownSubject := claims()
	unknownSubject["sub"] = int64(987654)
	staleRole := claims()
	staleRole["role"] = "admin" // the database, not the claim, decides the role
	wrongIssuer := claims()
	wrongIssuer["iss"] = "somebody-else"

	cases := []struct {
		name     string
		token    string
		wantOK   bool
		wantRole string
	}{
		{"valid token", valid, true, "operator"},
		{"empty token", "", false, ""},
		{"garbage token", "not-a-jwt", false, ""},
		{"tampered signature", tamperSignature(t, valid), false, ""},
		{"tampered claims", tamperClaims(t, valid, func(c jwt.MapClaims) { c["role"] = "admin" }), false, ""},
		{"signed with another secret", mintToken(t, jwt.SigningMethodHS256, []byte("another-secret"), claims()), false, ""},
		{"expired token", mintToken(t, jwt.SigningMethodHS256, svc.secret, expired), false, ""},
		{"token without exp", mintToken(t, jwt.SigningMethodHS256, svc.secret, noExpiry), false, ""},
		{"wrong issuer", mintToken(t, jwt.SigningMethodHS256, svc.secret, wrongIssuer), false, ""},
		{"no signature (alg none)", mintToken(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims()), false, ""},
		{"non numeric subject", mintToken(t, jwt.SigningMethodHS256, svc.secret, stringSubject), false, ""},
		{"unknown subject", mintToken(t, jwt.SigningMethodHS256, svc.secret, unknownSubject), false, ""},
		{"role claim is ignored", mintToken(t, jwt.SigningMethodHS256, svc.secret, staleRole), true, "operator"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ident, ok := svc.VerifyToken(c.token)
			if ok != c.wantOK {
				t.Fatalf("VerifyToken(%q) ok = %v, want %v", c.token, ok, c.wantOK)
			}
			if !ok {
				if ident != (Identity{}) {
					t.Fatalf("rejected token returned a non-zero identity: %+v", ident)
				}
				return
			}
			if ident.UserID != u.ID || ident.Username != u.Username || ident.Name != u.DisplayName {
				t.Fatalf("identity = %+v, want user %d/%q/%q", ident, u.ID, u.Username, u.DisplayName)
			}
			if ident.Role != c.wantRole {
				t.Errorf("role = %q, want %q", ident.Role, c.wantRole)
			}
			if !ident.Auth {
				t.Error("verified token produced an unauthenticated identity")
			}
		})
	}
}

// Regression test for the security defect in VerifyToken: store.GetUser returns
// (nil, nil) for a missing row, and the previous "err == nil && u != nil" guard
// skipped the account check entirely, so the token of a deleted user stayed valid
// until it expired on its own.
func TestVerifyTokenRejectsDeletedUser(t *testing.T) {
	svc, st, _ := newAuthForTest(t)
	u := createUserForTest(t, st, "bob", "admin", true)

	token, err := svc.SignToken(u)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, ok := svc.VerifyToken(token); !ok {
		t.Fatal("token of an existing user must verify")
	}

	if err := st.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	gone, err := st.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser of a deleted user must not error (store contract is nil, nil): %v", err)
	}
	if gone != nil {
		t.Fatalf("GetUser of a deleted user = %+v, want nil", gone)
	}

	if ident, ok := svc.VerifyToken(token); ok {
		t.Fatalf("token of a deleted user must be rejected, got identity %+v", ident)
	}
}

func TestVerifyTokenRejectsDisabledUser(t *testing.T) {
	svc, st, _ := newAuthForTest(t)
	u := createUserForTest(t, st, "carol", "operator", true)

	token, err := svc.SignToken(u)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, ok := svc.VerifyToken(token); !ok {
		t.Fatal("token of an enabled user must verify")
	}

	setUserEnabledForTest(t, st, u.ID, false)
	if ident, ok := svc.VerifyToken(token); ok {
		t.Fatalf("token of a disabled user must be rejected, got identity %+v", ident)
	}

	// The check is live account state, not a snapshot: re-enabling the account makes
	// the very same token valid again.
	setUserEnabledForTest(t, st, u.ID, true)
	if _, ok := svc.VerifyToken(token); !ok {
		t.Error("token must verify again once the account is re-enabled")
	}
}

func TestResolveSession(t *testing.T) {
	svc, st, _ := newAuthForTest(t)
	u := createUserForTest(t, st, "dave", "operator", true)

	const token = "session-token-for-dave"
	if err := st.CreateSession(token, u.ID, time.Hour, "127.0.0.1", "test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ident, ok := svc.ResolveSession(token)
	if !ok {
		t.Fatal("ResolveSession rejected a valid session")
	}
	if ident.UserID != u.ID || ident.Username != u.Username || ident.Role != "operator" || !ident.Auth {
		t.Fatalf("identity = %+v", ident)
	}
	if ident.IsAdmin() {
		t.Error("an operator must not be treated as an admin")
	}
	if !ident.CanWrite() {
		t.Error("an operator must be allowed to write")
	}

	if _, ok := svc.ResolveSession(""); ok {
		t.Error("ResolveSession accepted an empty token")
	}
	if _, ok := svc.ResolveSession("never-issued"); ok {
		t.Error("ResolveSession accepted an unknown token")
	}

	// Deleted user: the session row is gone with the account, but even a surviving
	// row must not authenticate.
	if err := st.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := svc.ResolveSession(token); ok {
		t.Error("ResolveSession accepted a session of a deleted user")
	}

	viewer := createUserForTest(t, st, "erin", "viewer", true)
	const viewerToken = "session-token-for-erin"
	if err := st.CreateSession(viewerToken, viewer.ID, time.Hour, "127.0.0.1", "test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	vident, ok := svc.ResolveSession(viewerToken)
	if !ok {
		t.Fatal("ResolveSession rejected the viewer session")
	}
	if vident.CanWrite() {
		t.Error("a viewer must not be allowed to write")
	}

	setUserEnabledForTest(t, st, viewer.ID, false)
	if _, ok := svc.ResolveSession(viewerToken); ok {
		t.Error("ResolveSession accepted a session of a disabled user")
	}

	// Expired sessions are rejected as well.
	setUserEnabledForTest(t, st, viewer.ID, true)
	const expiredToken = "session-token-expired"
	if err := st.CreateSession(expiredToken, viewer.ID, -time.Minute, "127.0.0.1", "test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, ok := svc.ResolveSession(expiredToken); ok {
		t.Error("ResolveSession accepted an expired session")
	}
}

func TestLoginAndLogout(t *testing.T) {
	svc, st, _ := newAuthForTest(t)
	u := createUserForTest(t, st, "grace", "operator", true)
	ctx := context.Background()

	if !svc.Enabled() {
		t.Fatal("service must report authentication as enabled")
	}

	token, ident, err := svc.Login(ctx, "grace", testPassword, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("Login returned an empty session token")
	}
	if strings.Contains(token, ".") {
		t.Errorf("session token must be opaque, got a JWT-like value: %q", token)
	}
	if len(token) != 64 {
		t.Errorf("session token length = %d, want 64 hex characters", len(token))
	}
	if ident.UserID != u.ID || ident.Username != u.Username || !ident.Auth {
		t.Fatalf("identity = %+v", ident)
	}

	// A session token is not a JWT, and a JWT is not a session.
	if _, ok := svc.VerifyToken(token); ok {
		t.Error("opaque session token must not verify as an API token")
	}
	apiToken, err := svc.SignToken(u)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, ok := svc.ResolveSession(apiToken); ok {
		t.Error("API token must not resolve as a session")
	}

	svc.Logout(token, ident.Username, "127.0.0.1", "test-agent")
	if _, ok := svc.ResolveSession(token); ok {
		t.Error("session survived Logout")
	}

	if _, _, err := svc.Login(ctx, "grace", "wrong-password", "127.0.0.1", "test-agent"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, "nobody", testPassword, "127.0.0.1", "test-agent"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user error = %v, want ErrInvalidCredentials", err)
	}

	createUserForTest(t, st, "heidi", "viewer", false)
	if _, _, err := svc.Login(ctx, "heidi", testPassword, "127.0.0.1", "test-agent"); !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("disabled account error = %v, want ErrAccountDisabled", err)
	}
}

func TestLoginLockout(t *testing.T) {
	svc, st, cfg := newAuthForTest(t)
	cfg.Auth.LockoutAttempts = 3
	cfg.Auth.LockoutMinutes = 15
	createUserForTest(t, st, "alice", "operator", true)

	ctx := context.Background()
	const ip = "203.0.113.7"
	const ua = "lockout-test/1.0"

	for i := 1; i <= 3; i++ {
		token, ident, err := svc.Login(ctx, "alice", "wrong-password", ip, ua)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v, want ErrInvalidCredentials", i, err)
		}
		if token != "" || ident != (Identity{}) {
			t.Fatalf("attempt %d returned credentials despite failing: %q %+v", i, token, ident)
		}
	}

	// The limit is reached: the next attempt is answered as locked out, even with
	// the correct password.
	if _, _, err := svc.Login(ctx, "alice", "wrong-password", ip, ua); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("error = %v, want ErrLockedOut", err)
	}
	if _, _, err := svc.Login(ctx, "alice", testPassword, ip, ua); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("correct password during lockout: error = %v, want ErrLockedOut", err)
	}

	// The lockout key is lower-cased, so changing the username case must not bypass it.
	if _, _, err := svc.Login(ctx, "ALICE", "wrong-password", ip, ua); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("upper case username bypassed the lockout: %v", err)
	}

	// Another client address keeps its own counter.
	if _, _, err := svc.Login(ctx, "alice", "wrong-password", "198.51.100.9", ua); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("a different IP was locked out too: %v", err)
	}

	// Rather than sleeping through lockout_minutes, rewind the window.
	key := "alice|" + ip
	svc.mu.Lock()
	entry, ok := svc.attempts[key]
	svc.mu.Unlock()
	if !ok {
		t.Fatalf("no lockout entry for %q", key)
	}
	svc.mu.Lock()
	entry.until = time.Now().Add(-time.Second)
	svc.mu.Unlock()

	token, ident, err := svc.Login(ctx, "alice", testPassword, ip, ua)
	if err != nil {
		t.Fatalf("Login after the lock window expired: %v", err)
	}
	if token == "" || ident.Username != "alice" {
		t.Fatalf("Login after expiry = %q %+v", token, ident)
	}
}

func TestLoginSuccessClearsFailureCounter(t *testing.T) {
	svc, st, cfg := newAuthForTest(t)
	cfg.Auth.LockoutAttempts = 3
	createUserForTest(t, st, "bob", "admin", true)

	ctx := context.Background()
	const ip = "192.0.2.44"
	const ua = "reset-test/1.0"

	for i := 0; i < 2; i++ {
		if _, _, err := svc.Login(ctx, "bob", "wrong-password", ip, ua); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", i+1, err)
		}
	}
	if _, _, err := svc.Login(ctx, "bob", testPassword, ip, ua); err != nil {
		t.Fatalf("correct password after 2 failures: %v", err)
	}

	key := "bob|" + ip
	svc.mu.Lock()
	_, still := svc.attempts[key]
	svc.mu.Unlock()
	if still {
		t.Error("failure counter survived a successful login")
	}

	// The counter really starts from zero again: two more failures must not lock.
	for i := 0; i < 2; i++ {
		if _, _, err := svc.Login(ctx, "bob", "wrong-password", ip, ua); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("post-reset attempt %d error = %v, want ErrInvalidCredentials", i+1, err)
		}
	}
	if _, _, err := svc.Login(ctx, "bob", testPassword, ip, ua); err != nil {
		t.Fatalf("counter was not reset by the earlier success: %v", err)
	}
}

func TestCookieSecure(t *testing.T) {
	svc, _, cfg := newAuthForTest(t)

	cases := []struct {
		name       string
		directTLS  bool
		trustProxy bool
		tlsEnabled bool
		forwarded  string
		want       bool
	}{
		{"direct TLS", true, false, false, "", true},
		{"direct TLS wins over a plaintext forward", true, false, false, "http", true},
		{"forwarded https with trust_proxy", false, true, false, "https", true},
		{"forwarded https is case and space insensitive", false, true, false, "  HTTPS ", true},
		{"forwarded https without trust_proxy", false, false, false, "https", false},
		{"forwarded http with trust_proxy", false, true, false, "http", false},
		{"no TLS and no forward header", false, true, false, "", false},
		{"plain http", false, false, false, "", false},
		{"configured TLS listener", false, false, true, "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg.HTTP.TrustProxy = c.trustProxy
			cfg.HTTP.TLS.Enabled = c.tlsEnabled

			req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
			if c.directTLS {
				req.TLS = &tls.ConnectionState{}
			}
			if c.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", c.forwarded)
			}

			if got := svc.cookieSecure(req); got != c.want {
				t.Fatalf("cookieSecure = %v, want %v (tls=%v trust_proxy=%v tls.enabled=%v forward=%q)",
					got, c.want, c.directTLS, c.trustProxy, c.tlsEnabled, c.forwarded)
			}

			rec := httptest.NewRecorder()
			svc.SetSessionCookie(rec, req, "token-value")
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("SetSessionCookie wrote %d cookies, want 1", len(cookies))
			}
			ck := cookies[0]
			if ck.Name != SessionCookie {
				t.Errorf("cookie name = %q, want %q", ck.Name, SessionCookie)
			}
			if ck.Value != "token-value" {
				t.Errorf("cookie value = %q", ck.Value)
			}
			if ck.Secure != c.want {
				t.Errorf("cookie Secure = %v, want %v", ck.Secure, c.want)
			}
			if !ck.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
			if ck.Path != "/" {
				t.Errorf("cookie path = %q, want /", ck.Path)
			}
			if ck.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie SameSite = %v, want Lax", ck.SameSite)
			}
			if ck.MaxAge != 72*3600 {
				t.Errorf("cookie MaxAge = %d, want %d", ck.MaxAge, 72*3600)
			}
		})
	}

	rec := httptest.NewRecorder()
	svc.ClearSessionCookie(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("ClearSessionCookie wrote %d cookies, want 1", len(cookies))
	}
	if cookies[0].MaxAge != -1 || cookies[0].Value != "" {
		t.Errorf("clearing cookie = %+v, want an empty cookie with MaxAge -1", cookies[0])
	}
}

func TestIdentityForRequest(t *testing.T) {
	svc, st, cfg := newAuthForTest(t)
	u := createUserForTest(t, st, "ivan", "operator", true)

	const sessionToken = "session-token-for-ivan"
	if err := st.CreateSession(sessionToken, u.ID, time.Hour, "127.0.0.1", "test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	apiToken, err := svc.SignToken(u)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	// Open (desktop) mode: no credentials are required and the caller is the local
	// operator.
	cfg.SetAuthEnabled(false)
	open := svc.IdentityForRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	if open.Auth {
		t.Errorf("open mode identity must not be authenticated: %+v", open)
	}
	if open.Username != "local" || open.Role != "admin" || !open.IsAdmin() {
		t.Errorf("open mode identity = %+v, want the local admin", open)
	}
	cfg.SetAuthEnabled(true)

	cases := []struct {
		name     string
		prepare  func(*http.Request)
		wantAuth bool
	}{
		{"session cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: sessionToken})
		}, true},
		{"bearer session token", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+sessionToken)
		}, true},
		{"bearer api token", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+apiToken)
		}, true},
		{"api key header", func(r *http.Request) { r.Header.Set("X-API-Key", apiToken) }, true},
		{"no credentials", func(*http.Request) {}, false},
		{"invalid bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope.nope.nope") }, false},
		{"empty bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer   ") }, false},
		{"invalid api key", func(r *http.Request) { r.Header.Set("X-API-Key", "garbage") }, false},
		{"unknown session cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "never-issued"})
		}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
			c.prepare(req)
			ident := svc.IdentityForRequest(req)
			if ident.Auth != c.wantAuth {
				t.Fatalf("Auth = %v, want %v (identity %+v)", ident.Auth, c.wantAuth, ident)
			}
			if !c.wantAuth {
				if ident != (Identity{}) {
					t.Fatalf("unauthenticated request resolved to %+v", ident)
				}
				return
			}
			if ident.UserID != u.ID || ident.Username != u.Username || ident.Role != u.Role || ident.Name != u.DisplayName {
				t.Fatalf("identity = %+v, want user %d/%q", ident, u.ID, u.Username)
			}
			if !ident.CanWrite() {
				t.Error("an operator must be allowed to write")
			}
		})
	}

	// A cookie from a deleted user must not authenticate.
	if err := st.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: sessionToken})
	if ident := svc.IdentityForRequest(req); ident.Auth {
		t.Errorf("session cookie of a deleted user authenticated: %+v", ident)
	}
}

func TestTokenFromCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := TokenFromCookie(req); got != "" {
		t.Errorf("TokenFromCookie without a cookie = %q, want empty", got)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "abc123"})
	if got := TokenFromCookie(req); got != "abc123" {
		t.Errorf("TokenFromCookie = %q, want abc123", got)
	}
	// A cookie with another name must be ignored.
	other := httptest.NewRequest(http.MethodGet, "/", nil)
	other.AddCookie(&http.Cookie{Name: "something_else", Value: "zzz"})
	if got := TokenFromCookie(other); got != "" {
		t.Errorf("TokenFromCookie picked up an unrelated cookie: %q", got)
	}
}

func TestEnsureBootstrapUser(t *testing.T) {
	_, st, cfg := newAuthForTest(t)
	cfg.Auth.BootstrapUser = "root"
	cfg.Auth.BootstrapPass = "bootstrap-password"
	cfg.Auth.BootstrapName = "Root Operator"
	svc := New(st, cfg)

	if n, err := st.CountUsers(); err != nil || n != 0 {
		t.Fatalf("CountUsers = %d, %v; want 0, nil", n, err)
	}

	if err := svc.EnsureBootstrapUser(); err != nil {
		t.Fatalf("EnsureBootstrapUser: %v", err)
	}

	u, err := st.GetUserByName("root")
	if err != nil {
		t.Fatalf("GetUserByName: %v", err)
	}
	if u == nil {
		t.Fatal("bootstrap user was not created")
	}
	if u.Role != "admin" || !u.Enabled {
		t.Errorf("bootstrap user = %+v, want an enabled admin", u)
	}
	if u.DisplayName != "Root Operator" {
		t.Errorf("display name = %q, want %q", u.DisplayName, "Root Operator")
	}
	if !CheckPassword(u.PasswordHash, "bootstrap-password") {
		t.Error("bootstrap password does not verify against the stored hash")
	}
	if strings.Contains(u.PasswordHash, "bootstrap-password") {
		t.Error("password was stored in clear text")
	}

	// Idempotent: a second call must not create or overwrite anything.
	if err := svc.EnsureBootstrapUser(); err != nil {
		t.Fatalf("second EnsureBootstrapUser: %v", err)
	}
	if n, err := st.CountUsers(); err != nil || n != 1 {
		t.Fatalf("CountUsers after the second call = %d, %v; want 1, nil", n, err)
	}
}
