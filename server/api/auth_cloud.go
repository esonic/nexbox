package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/resend/resend-go/v3"
	"golang.org/x/crypto/bcrypt"
	"gitlab.okg.com/sre_ai/boxnex/config"
	"gitlab.okg.com/sre_ai/boxnex/store"
)

// ---------------------------------------------------------------------------
// In-memory verification code store
// ---------------------------------------------------------------------------

type verifyEntry struct {
	code      string
	expiresAt time.Time
	ip        string
	sentAt    time.Time
}

var (
	verifyMu    sync.Mutex
	verifyCodes = map[string]*verifyEntry{} // key = email
)

func cleanExpiredCodes() {
	verifyMu.Lock()
	defer verifyMu.Unlock()
	now := time.Now()
	for email, e := range verifyCodes {
		if now.After(e.expiresAt) {
			delete(verifyCodes, email)
		}
	}
}

// ---------------------------------------------------------------------------
// Login brute-force protection
// ---------------------------------------------------------------------------

const (
	loginIPWindow    = time.Minute      // sliding window for IP limit
	loginIPMaxReqs   = 10              // max login attempts per IP per window
	loginAcctMaxFail = 5              // consecutive failures before account lock
	loginAcctLockDur = 15 * time.Minute // account lock duration
)

type loginIPEntry struct {
	count       int
	windowStart time.Time
}

type loginAcctEntry struct {
	failures    int
	lockedUntil time.Time
}

var (
	loginMu      sync.Mutex
	loginIPMap   = map[string]*loginIPEntry{}   // key = IP
	loginAcctMap = map[string]*loginAcctEntry{} // key = email
)

// checkLoginRateLimit checks IP and account limits before authenticating.
// Returns (429 message, true) if the request should be rejected.
func checkLoginRateLimit(ip, email string) (string, bool) {
	loginMu.Lock()
	defer loginMu.Unlock()

	now := time.Now()

	// --- IP limit ---
	ie, ok := loginIPMap[ip]
	if !ok || now.Sub(ie.windowStart) >= loginIPWindow {
		loginIPMap[ip] = &loginIPEntry{count: 1, windowStart: now}
	} else {
		ie.count++
		if ie.count > loginIPMaxReqs {
			remaining := int(loginIPWindow-now.Sub(ie.windowStart)) / int(time.Second)
			return fmt.Sprintf("too many login attempts from this IP, please wait %ds", remaining), true
		}
	}

	// --- Account lock ---
	ae, ok := loginAcctMap[email]
	if ok && now.Before(ae.lockedUntil) {
		remaining := int(ae.lockedUntil.Sub(now) / time.Second)
		return fmt.Sprintf("account temporarily locked, please try again in %ds", remaining), true
	}

	return "", false
}

// recordLoginFailure increments failure counter for the account; locks after threshold.
func recordLoginFailure(email string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	ae, ok := loginAcctMap[email]
	if !ok {
		loginAcctMap[email] = &loginAcctEntry{failures: 1}
		return
	}
	ae.failures++
	if ae.failures >= loginAcctMaxFail {
		ae.lockedUntil = time.Now().Add(loginAcctLockDur)
		ae.failures = 0
	}
}

// recordLoginSuccess clears failure state on successful login.
func recordLoginSuccess(email string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	delete(loginAcctMap, email)
}

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
		secret = "boxnex-dev-secret-change-in-production"
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

// HandleSendVerification sends a 6-digit verification code to the given email.
// Rate limit: one code per IP per 60 seconds.
// POST /api/auth/send-verification  { "email": "user@example.com" }
func (s *Server) HandleSendVerification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "email required")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}

	// Extract client IP
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.SplitN(fwd, ",", 2)[0]
	}

	cleanExpiredCodes()

	verifyMu.Lock()
	if e, ok := verifyCodes[req.Email]; ok {
		if time.Since(e.sentAt) < 60*time.Second && e.ip == ip {
			verifyMu.Unlock()
			writeError(w, http.StatusTooManyRequests, "please wait 60 seconds before requesting another code")
			return
		}
	}

	// Generate 6-digit numeric code
	b := make([]byte, 3)
	rand.Read(b)
	codeNum := (int(b[0])%10)*100000 + (int(b[1])%10)*10000 + (int(b[2])%10)*1000 + (int(b[0]/10)%10)*100 + (int(b[1]/10)%10)*10 + int(b[2]/10)%10
	code := fmt.Sprintf("%06d", codeNum%1000000)

	verifyCodes[req.Email] = &verifyEntry{
		code:      code,
		expiresAt: time.Now().Add(10 * time.Minute),
		ip:        ip,
		sentAt:    time.Now(),
	}
	verifyMu.Unlock()

	// Send email (or log if Resend not configured)
	cfg := s.Config.Get()
	if cfg.ResendAPIKey != "" {
		go sendVerificationEmail(cfg, req.Email, code)
	} else {
		// Development mode: log the code (check server/logs/boxnex.log)
		log.Printf("[auth] Verification code for %s: %s", req.Email, code)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func sendVerificationEmail(cfg config.Config, to, code string) {
	from := cfg.ResendFrom
	if from == "" {
		from = "BoxNex <noreply@box-nex.top>"
	}
	client := resend.NewClient(cfg.ResendAPIKey)
	_, err := client.Emails.Send(&resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: "BoxNex Verification Code",
		Html: fmt.Sprintf(
			`<p>Your BoxNex verification code is: <strong style="font-size:24px;letter-spacing:4px">%s</strong></p>`+
				`<p>Verification code is valid for 10 minutes. Please do not share it with others.</p>`,
			code,
		),
	})
	if err != nil {
		log.Printf("[auth] Failed to send verification email to %s: %v", to, err)
	}
}

// HandleRegister POST /api/auth/register
func (s *Server) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Code = strings.TrimSpace(req.Code)

	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "verification code required")
		return
	}

	// Validate verification code
	verifyMu.Lock()
	entry, ok := verifyCodes[req.Email]
	if !ok || time.Now().After(entry.expiresAt) || entry.code != req.Code {
		verifyMu.Unlock()
		writeError(w, http.StatusBadRequest, "invalid or expired verification code")
		return
	}
	delete(verifyCodes, req.Email)
	verifyMu.Unlock()

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	// Create user (email stored as Username)
	u, err := s.Store.CreateUser(store.CreateUserRequest{
		Username: req.Email,
		Email:    req.Email,
		Password: string(hash),
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// First user becomes admin
	users, _ := s.Store.ListUsers(2, 0)
	if len(users) == 1 {
		if err := s.Store.UpdateUserRole(u.ID, "admin"); err == nil {
			u.Role = "admin"
		}
	}

	token, err := s.generateToken(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token": token,
		"user":  u,
	})
}

// HandleLogin POST /api/auth/login
func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`    // primary field
		Username string `json:"username"` // backward compat
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Use Email if provided, fall back to Username
	if req.Email != "" {
		req.Username = strings.ToLower(strings.TrimSpace(req.Email))
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "email required")
		return
	}

	// Extract client IP (same logic as send-verification)
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.SplitN(fwd, ",", 2)[0]
	}

	// Check rate limits before touching the DB
	if msg, limited := checkLoginRateLimit(ip, req.Username); limited {
		writeError(w, http.StatusTooManyRequests, msg)
		return
	}

	u, err := s.Store.GetUserByUsername(req.Username)
	if err != nil || u == nil {
		recordLoginFailure(req.Username)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		recordLoginFailure(req.Username)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	recordLoginSuccess(req.Username)

	token, err := s.generateToken(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"user":  u,
	})
}

// HandleMe GET /api/auth/me
func (s *Server) HandleMe(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(ContextKeyUserID).(string)

	// Local mode (no JWT): return user context from middleware directly.
	if s.Config.Get().JWTSecret == "" {
		features, _ := r.Context().Value(ContextKeyFeatures).(int64)
		role, _ := r.Context().Value(ContextKeyRole).(string)
		username, _ := r.Context().Value(ContextKeyUsername).(string)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":       userID,
			"username": username,
			"role":     role,
			"features": features,
		})
		return
	}

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

// HandleAdminUpdateUserQuota PATCH /api/admin/users/{userID}/quota
// Admin-only: update a user's container resource quota.
func (s *Server) HandleAdminUpdateUserQuota(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "userID")
	var req struct {
		Quotas string `json:"quotas"` // JSON string e.g. {"cpu":"1.0","memory":"2g","pids":200}
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Quotas == "" {
		req.Quotas = "{}"
	}
	if !json.Valid([]byte(req.Quotas)) {
		http.Error(w, "quotas must be valid JSON", http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdateUserQuotas(targetID, req.Quotas); err != nil {
		http.Error(w, "failed to update quota: "+err.Error(), http.StatusInternalServerError)
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

// HandleGetMyQuota GET /api/quota
// Returns the authenticated user's container resource quota.
func (s *Server) HandleGetMyQuota(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)
	u, err := s.Store.GetUser(userID)
	if err != nil || u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"quotas": u.Quotas})
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
