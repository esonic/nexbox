package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// AuthMiddleware validates the Bearer token and injects user claims into context.
// Returns 401 if the token is missing or invalid.
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "missing or invalid authorization header", http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		claims := &jwtClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return s.jwtSecret(), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, ContextKeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ContextKeyUsername, claims.Username)
		ctx = context.WithValue(ctx, ContextKeyRole, claims.Role)
		ctx = context.WithValue(ctx, ContextKeyFeatures, claims.Features)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminMiddleware requires the requesting user to have role "admin".
// Must be used after AuthMiddleware.
func AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ContextKeyRole).(string)
		if role != "admin" {
			http.Error(w, "forbidden: admin role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GetUserID retrieves the authenticated user's ID from context.
// Returns "" if not set (should not happen after AuthMiddleware).
func GetUserID(r *http.Request) string {
	v, _ := r.Context().Value(ContextKeyUserID).(string)
	return v
}

// GetUserRole retrieves the authenticated user's role from context.
func GetUserRole(r *http.Request) string {
	v, _ := r.Context().Value(ContextKeyRole).(string)
	return v
}

// GetUserFeatures retrieves the authenticated user's feature flags from context.
func GetUserFeatures(r *http.Request) int64 {
	v, _ := r.Context().Value(ContextKeyFeatures).(int64)
	return v
}

// WSAuthMiddleware is like AuthMiddleware but also accepts the JWT via the
// "token" query parameter. Browsers cannot set custom headers on WebSocket
// connections, so the token must be passed as ?token=<jwt> instead.
func (s *Server) WSAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prefer Authorization header; fall back to query param.
		tokenStr := ""
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			tokenStr = r.URL.Query().Get("token")
		}

		if tokenStr == "" {
			http.Error(w, "missing or invalid authorization", http.StatusUnauthorized)
			return
		}

		claims := &jwtClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return s.jwtSecret(), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, ContextKeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ContextKeyUsername, claims.Username)
		ctx = context.WithValue(ctx, ContextKeyRole, claims.Role)
		ctx = context.WithValue(ctx, ContextKeyFeatures, claims.Features)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
