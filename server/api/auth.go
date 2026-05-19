package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/esonic/nexbox/store"
)

// authCookieName is the cookie carrying the JWT bearer token.
const authCookieName = "nexbox_auth"

// jwtTTL is the lifetime of an issued token. Independently of this, all tokens
// become unsigned-out on process restart because the signing secret is
// regenerated each boot.
const jwtTTL = 7 * 24 * time.Hour

// Authenticator owns the per-process JWT signing key. A new random key is
// generated every time the server starts, so server restarts invalidate every
// existing session — by design (see nexbox setup notes).
type Authenticator struct {
	secret []byte
}

// NewAuthenticator generates a fresh signing key from crypto/rand.
func NewAuthenticator() *Authenticator {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatalf("[auth] failed to seed JWT secret: %v", err)
	}
	return &Authenticator{secret: secret}
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
	if a := AuthFromContext(r.Context()); a != nil {
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

// RequireAuth blocks requests that don't carry a valid JWT cookie/Authorization
// header. Login/health endpoints are skipped via the public-paths set.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
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
		u, err := s.Store.GetUserByID(claims.UserID)
		if err != nil || u == nil {
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
	Username string `json:"username"`
	UserID   int64  `json:"user_id"`
}

// HandleLogin verifies the (username, password) pair and sets the auth cookie.
// POST /api/login
func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	u, err := s.Store.GetUserByUsername(req.Username)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			// Use the same error message for "no such user" and "wrong password"
			// to avoid leaking which usernames exist.
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up user")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := s.Auth.issue(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	setAuthCookie(w, token)

	// Phase 5: ensure the user has at least the seed Default project on first
	// login. If they later delete it we don't recreate.
	if err := s.ensureDefaultProject(u); err != nil {
		log.Printf("[auth] ensureDefaultProject for %q: %v", u.Username, err)
	}

	writeJSON(w, http.StatusOK, loginResponse{Username: u.Username, UserID: u.ID})
}

// HandleLogout clears the auth cookie. Tokens in flight remain technically
// valid until expiry, but the browser will stop sending them.
// POST /api/logout
func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// authorizeWS validates the JWT for a WebSocket upgrade request before the
// connection is upgraded. It writes a 401 and returns false when auth fails,
// so the caller should `return` immediately on a false result.
func (s *Server) authorizeWS(w http.ResponseWriter, r *http.Request) bool {
	raw := readAuthToken(r)
	if raw == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return false
	}
	claims, err := s.Auth.parse(raw)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return false
	}
	if u, err := s.Store.GetUserByID(claims.UserID); err != nil || u == nil {
		http.Error(w, "user no longer exists", http.StatusUnauthorized)
		return false
	}
	return true
}

// HandleAuthMe returns the current user if authenticated, or 401 when not.
// Used by the frontend on startup to decide whether to show the login page.
// GET /api/auth/me
func (s *Server) HandleAuthMe(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || u == nil {
		writeError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Username: u.Username, UserID: u.ID})
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
