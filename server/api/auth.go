package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/esonic/nexbox/store"
)

// limiterSweepInterval bounds how often loginRateLimiter walks the whole map
// to drop fully-expired keys. Each rate-limited request opportunistically
// triggers a sweep if this much wall-time has passed since the last one.
const limiterSweepInterval = 10 * time.Minute

// authCookieName is the cookie carrying the JWT bearer token.
const authCookieName = "nexbox_auth"

// jwtTTL is the lifetime of an issued token. Independently of this, all tokens
// become unsigned-out on process restart because the signing secret is
// regenerated each boot.
const jwtTTL = 7 * 24 * time.Hour

// Login rate-limit knobs. Counters live only in process memory, so a restart
// wipes the slate clean.
const (
	loginRateWindow      = 15 * time.Minute
	loginMaxFailsPerIP   = 10
	loginMaxFailsPerUser = 5
)

// Authenticator owns the per-process JWT signing key and the in-memory login
// rate limiters. A new signing key is generated every time the server starts,
// so restarts invalidate every existing session — by design.
type Authenticator struct {
	secret      []byte
	ipLimiter   *loginRateLimiter
	userLimiter *loginRateLimiter
}

// NewAuthenticator generates a fresh signing key from crypto/rand.
func NewAuthenticator() *Authenticator {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatalf("[auth] failed to seed JWT secret: %v", err)
	}
	return &Authenticator{
		secret:      secret,
		ipLimiter:   newLoginRateLimiter(loginRateWindow, loginMaxFailsPerIP),
		userLimiter: newLoginRateLimiter(loginRateWindow, loginMaxFailsPerUser),
	}
}

// ---------------------------------------------------------------------------
// Login rate limiter (in-memory sliding window)
// ---------------------------------------------------------------------------

type loginRateLimiter struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	window    time.Duration
	maxFails  int
	lastSweep time.Time
}

func newLoginRateLimiter(window time.Duration, maxFails int) *loginRateLimiter {
	return &loginRateLimiter{
		failures:  make(map[string][]time.Time),
		window:    window,
		lastSweep: time.Now(),
		maxFails:  maxFails,
	}
}

// pruneKeyLocked filters key's failure list to entries newer than cutoff and
// deletes the map entry when nothing remains. Returns the surviving slice.
// Caller must hold l.mu.
func (l *loginRateLimiter) pruneKeyLocked(key string, cutoff time.Time) []time.Time {
	list := l.failures[key]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = kept
	}
	return kept
}

// sweepIfDueLocked walks the entire map and drops fully-expired entries when
// at least limiterSweepInterval has elapsed since the last sweep. This is
// what keeps the map from growing unbounded when many distinct attacker IPs
// or usernames hit the endpoint once and never come back. Caller must hold
// l.mu.
func (l *loginRateLimiter) sweepIfDueLocked(now time.Time) {
	if now.Sub(l.lastSweep) < limiterSweepInterval {
		return
	}
	l.lastSweep = now
	cutoff := now.Add(-l.window)
	for k := range l.failures {
		l.pruneKeyLocked(k, cutoff)
	}
}

// allowed prunes expired failures for key and reports whether another attempt
// is permitted. retryAfter is the wait until the oldest in-window failure
// expires (so the caller can hint Retry-After to the client).
func (l *loginRateLimiter) allowed(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepIfDueLocked(now)
	kept := l.pruneKeyLocked(key, now.Add(-l.window))
	if len(kept) < l.maxFails {
		return true, 0
	}
	return false, kept[0].Add(l.window).Sub(now)
}

func (l *loginRateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepIfDueLocked(now)
	l.failures[key] = append(l.failures[key], now)
}

func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// clientIPFromRequest extracts the remote IP for rate-limit keying. We
// intentionally use r.RemoteAddr and do NOT trust X-Forwarded-For: in a
// direct-listen deploy that header is attacker-controlled and would let a
// scripted brute-force trivially bypass per-IP limits by rotating the
// header. If you put nexbox behind a trusted reverse proxy, strip XFF at
// the proxy and let it rewrite RemoteAddr (or extend this helper to honor
// a configured trusted-proxy CIDR).
func clientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authClaims is the payload embedded in every issued JWT.
type authClaims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"u"`
	jwt.RegisteredClaims
}

// issue returns a signed JWT string for the given user.
func (a *Authenticator) issue(u *store.User) (string, error) {
	now := time.Now()
	claims := authClaims{
		UserID:   u.ID,
		Username: u.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtTTL)),
			Subject:   u.Username,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(a.secret)
}

// parse validates a token string and returns the embedded claims.
func (a *Authenticator) parse(raw string) (*authClaims, error) {
	tok, err := jwt.ParseWithClaims(raw, &authClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return a.secret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := tok.Claims.(*authClaims)
	if !ok || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Auth context
// ---------------------------------------------------------------------------

type authCtxKey struct{}

// AuthInfo is what gets stashed in the request context after a successful
// auth middleware run.
type AuthInfo struct {
	UserID   int64
	Username string
	Local    bool // internal passwordless workspace, not a signed-in identity
}

// localWorkspaceUser returns the internal owner only while no login accounts
// exist. Query errors never silently disable authentication.
func (s *Server) localWorkspaceUser() (*store.User, error) {
	required, err := s.Store.HasLoginUsers()
	if err != nil || required {
		return nil, err
	}
	return s.Store.EnsureLocalUser()
}

func (s *Server) serveLocalAuthStatus(w http.ResponseWriter) bool {
	u, err := s.localWorkspaceUser()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read authentication mode")
		return true
	}
	if u == nil {
		return false
	}
	if err := s.ensureDefaultProject(u); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to initialize local workspace")
		return true
	}
	if s.Hub != nil {
		s.Hub.ownerCache.Clear()
	}
	writeJSON(w, http.StatusOK, loginResponse{AuthRequired: false})
	return true
}

// AuthFromContext returns the authenticated user, or nil when none is present
// (i.e. the request was served by a route outside the auth middleware).
func AuthFromContext(ctx context.Context) *AuthInfo {
	v, _ := ctx.Value(authCtxKey{}).(*AuthInfo)
	return v
}

// currentUser returns the username of the authenticated caller of the given
// request, or "system" when none is present. Use this for fields like
// comment.author or task.assigner where you want the human's name.
func (s *Server) currentUser(r *http.Request) string {
	if a := AuthFromContext(r.Context()); a != nil && !a.Local {
		return a.Username
	}
	return "system"
}

// requireAuthUser fetches the authenticated user from the request context;
// returns nil + writes 401 when missing. Callers that need the user id should
// use this in handler bodies.
func (s *Server) requireAuthUser(w http.ResponseWriter, r *http.Request) *AuthInfo {
	a := AuthFromContext(r.Context())
	if a == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil
	}
	return a
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// RequireAuth supplies the internal owner in passwordless mode, or requires a
// valid JWT when login accounts exist. Public endpoints bypass this middleware.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		u, err := s.localWorkspaceUser()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read authentication mode")
			return
		}
		if u != nil {
			info := &AuthInfo{UserID: u.ID, Username: u.Username, Local: true}
			ctx := context.WithValue(r.Context(), authCtxKey{}, info)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		raw := readAuthToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		claims, err := s.Auth.parse(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		// Confirm the user still exists; revoke-by-deletion semantics.
		u, err = s.Store.GetUserByID(claims.UserID)
		if err != nil || u == nil || u.PasswordHash == "" {
			writeError(w, http.StatusUnauthorized, "user no longer exists")
			return
		}
		info := &AuthInfo{UserID: u.ID, Username: u.Username}
		ctx := context.WithValue(r.Context(), authCtxKey{}, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isPublicPath returns true for endpoints that must work without auth.
// Anything not listed here is gated by the auth middleware.
func isPublicPath(path string) bool {
	switch path {
	case "/api/login", "/api/logout", "/api/auth/me":
		return true
	}
	return false
}

// readAuthToken extracts the bearer token from either the auth cookie or the
// `Authorization: Bearer <token>` header.
func readAuthToken(r *http.Request) string {
	if c, err := r.Cookie(authCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
		return strings.TrimPrefix(v, "Bearer ")
	}
	// WebSocket upgrades pass the token via query string because browsers
	// can't set Authorization on the upgrade request.
	if v := r.URL.Query().Get("auth"); v != "" {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AuthRequired bool   `json:"auth_required"`
	Username     string `json:"username,omitempty"`
	UserID       int64  `json:"user_id,omitempty"`
}

// HandleLogin verifies the (username, password) pair and sets the auth cookie.
// POST /api/login
func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if s.serveLocalAuthStatus(w) {
		return
	}
	// Cap the request body so a malicious caller can't ship a huge JSON blob
	// just to make the parser allocate.
	r.Body = http.MaxBytesReader(w, r.Body, 4096)

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	ip := clientIPFromRequest(r)
	// Username lookup is case-sensitive in the DB; normalize for limiter keying
	// so per-user limits can't be bypassed by varying the case in repeated
	// attempts against the same account.
	userKey := strings.ToLower(req.Username)

	if ok, retry := s.Auth.ipLimiter.allowed(ip); !ok {
		writeRateLimited(w, retry, "too many login attempts from this address, try again later")
		return
	}
	if ok, retry := s.Auth.userLimiter.allowed(userKey); !ok {
		writeRateLimited(w, retry, "too many failed attempts for this account, try again later")
		return
	}

	u, err := s.Store.GetUserByUsername(req.Username)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			// Use the same error message for "no such user" and "wrong
			// password" so attackers can't enumerate usernames from the
			// response text. Response timing still differs (this branch
			// skips bcrypt), but the rate limiter caps how quickly that
			// signal can be sampled.
			s.Auth.ipLimiter.recordFailure(ip)
			s.Auth.userLimiter.recordFailure(userKey)
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up user")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		s.Auth.ipLimiter.recordFailure(ip)
		s.Auth.userLimiter.recordFailure(userKey)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	// Successful login: clear this account's counter so a legit user isn't
	// punished for typos. Leave the per-IP counter alone — it expires by
	// window — so a single guessed password on a shared NAT can't unlock
	// unlimited attempts against other usernames.
	s.Auth.userLimiter.reset(userKey)

	token, err := s.Auth.issue(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	setAuthCookie(w, token)

	// Initialize the default project for accounts with no existing projects.
	if err := s.ensureDefaultProject(u); err != nil {
		log.Printf("[auth] ensureDefaultProject for %q: %v", u.Username, err)
	}

	if s.Hub != nil {
		s.Hub.ownerCache.Clear()
	}
	writeJSON(w, http.StatusOK, loginResponse{AuthRequired: true, Username: u.Username, UserID: u.ID})
}

// HandleLogout clears the auth cookie. Tokens in flight remain technically
// valid until expiry, but the browser will stop sending them.
// POST /api/logout
func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// authorizeWS resolves the local workspace owner or validates the login before
// upgrading. The returned mode is also used to invalidate existing sockets.
func (s *Server) authorizeWS(w http.ResponseWriter, r *http.Request) (*AuthInfo, bool) {
	u, err := s.localWorkspaceUser()
	if err != nil {
		http.Error(w, "failed to read authentication mode", http.StatusInternalServerError)
		return nil, false
	}
	if s.Hub != nil {
		s.Hub.ownerCache.Clear()
	}
	if u != nil {
		return &AuthInfo{UserID: u.ID, Username: u.Username, Local: true}, true
	}
	raw := readAuthToken(r)
	if raw == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return nil, false
	}
	claims, err := s.Auth.parse(raw)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return nil, false
	}
	u, err = s.Store.GetUserByID(claims.UserID)
	if err != nil || u == nil || u.PasswordHash == "" {
		http.Error(w, "user no longer exists", http.StatusUnauthorized)
		return nil, false
	}
	return &AuthInfo{UserID: u.ID, Username: u.Username}, true
}

// Close existing chat and terminal sockets when account commands switch modes.
// This also covers idle connections, without requiring a server restart.
func (s *Server) watchWSAuthMode(conn *websocket.Conn, local bool) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !s.checkWSAuthMode(conn, local) {
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s *Server) checkWSAuthMode(conn *websocket.Conn, local bool) bool {
	required, err := s.Store.HasLoginUsers()
	if err == nil && required != local {
		return true
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Authentication mode changed"), time.Now().Add(time.Second))
	_ = conn.Close()
	return false
}

// writeRateLimited responds with 429 + a Retry-After hint (in seconds).
func writeRateLimited(w http.ResponseWriter, retry time.Duration, message string) {
	secs := int64(retry.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	writeError(w, http.StatusTooManyRequests, fmt.Sprintf("%s (retry in %ds)", message, secs))
}

// HandleAuthMe returns passwordless mode, the signed-in account, or 401 when
// login is required and no valid credentials are supplied.
// Used by the frontend on startup to decide whether to show the login page.
// GET /api/auth/me
func (s *Server) HandleAuthMe(w http.ResponseWriter, r *http.Request) {
	if s.serveLocalAuthStatus(w) {
		return
	}
	raw := readAuthToken(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	claims, err := s.Auth.parse(raw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	u, err := s.Store.GetUserByID(claims.UserID)
	if err != nil || u == nil || u.PasswordHash == "" {
		writeError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{AuthRequired: true, Username: u.Username, UserID: u.ID})
}

// ---------------------------------------------------------------------------
// Cookie helpers
// ---------------------------------------------------------------------------

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(jwtTTL.Seconds()),
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
