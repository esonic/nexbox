package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// Context key type to avoid collisions
type contextKey string

const (
	ContextKeyUserID   contextKey = "userID"
	ContextKeyRole     contextKey = "role"
	ContextKeyFeatures contextKey = "features"
	ContextKeyUsername contextKey = "username"
)

// jwtClaims holds the JWT payload
type jwtClaims struct {
	UserID   string `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Features int64  `json:"features"`
	jwt.RegisteredClaims
}

// jwtSecret returns the configured secret, or a hard-coded fallback for dev mode.
func (s *Server) jwtSecret() []byte {
	secret := s.Config.Get().JWTSecret
	if secret == "" {
		secret = "nexbox-dev-secret-change-in-production"
	}
	return []byte(secret)
}

// jwtExpDays returns configured token expiry in days (default 7).
func (s *Server) jwtExpDays() int {
	d := s.Config.Get().JWTExpDays
	if d <= 0 {
		d = 7
	}
	return d
}

// generateToken creates a signed JWT for the given user.
func (s *Server) generateToken(u *store.User) (string, error) {
	expiry := time.Now().Add(time.Duration(s.jwtExpDays()) * 24 * time.Hour)
	claims := jwtClaims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		Features: u.Features,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiry),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   u.ID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret())
}

// HandleRegister POST /api/auth/register
func (s *Server) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, "username and password are required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	// Check username not taken
	existing, err := s.Store.GetUserByUsername(req.Username)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	u, err := s.Store.CreateUser(store.CreateUserRequest{
		Username: req.Username,
		Password: string(hash), // store expects already-hashed value
		Email:    req.Email,
		Role:     "user",
	})
	if err != nil {
		http.Error(w, "failed to create user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// If this is the first user ever registered, promote them to admin
	// automatically so no manual SQL is required after first deploy.
	allUsers, err := s.Store.ListUsers(2, 0)
	if err == nil && len(allUsers) == 1 {
		// Only one user exists — must be the one we just created.
		if roleErr := s.Store.UpdateUserRole(u.ID, "admin"); roleErr == nil {
			u.Role = "admin"
		}
	}

	token, err := s.generateToken(u)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token": token,
		"user":  u,
	})
}

// HandleLogin POST /api/auth/login
func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	u, err := s.Store.GetUserByUsername(req.Username)
	if err != nil || u == nil {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}

	token, err := s.generateToken(u)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token": token,
		"user":  u,
	})
}

// HandleMe GET /api/auth/me
func (s *Server) HandleMe(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(ContextKeyUserID).(string)
	u, err := s.Store.GetUser(userID)
	if err != nil || u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}

// HandleAdminUpdateUserFeatures PATCH /api/admin/users/{userID}/features
// Admin-only: update a user's feature flags.
func (s *Server) HandleAdminUpdateUserFeatures(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	var req struct {
		Features int64 `json:"features"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdateUserFeatures(targetID, req.Features); err != nil {
		http.Error(w, "failed to update features: "+err.Error(), http.StatusInternalServerError)
		return
	}
	u, err := s.Store.GetUser(targetID)
	if err != nil || u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}

// HandleAdminListUsers GET /api/admin/users
func (s *Server) HandleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(100, 0)
	if err != nil {
		http.Error(w, "failed to list users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}
