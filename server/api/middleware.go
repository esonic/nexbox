package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// AuthMiddleware validates the Bearer token and injects user claims into context.
// In local mode (no JWTSecret configured), all requests pass through with
// default local-user context so that the app works without authentication.
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local mode: no JWT secret configured → skip auth, inject default user.
		if s.Config.Get().JWTSecret == "" {
			ctx := r.Context()
			ctx = context.WithValue(ctx, ContextKeyUserID, "local")
			ctx = context.WithValue(ctx, ContextKeyUsername, "local")
			ctx = context.WithValue(ctx, ContextKeyRole, "admin")
			ctx = context.WithValue(ctx, ContextKeyFeatures, int64(0xFF))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

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

// requireProjectAccess ensures the authenticated user owns the project identified
// by the {projectId} URL parameter. Admins bypass the check.
// Apply to any route group nested under /{projectId}.
func (s *Server) requireProjectAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUserRole(r) == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		projectID := chi.URLParam(r, "projectId")
		project, err := s.Store.GetProject(projectID)
		if err != nil || project == nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if project.OwnerID != GetUserID(r) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireTaskAccess ensures the authenticated user owns the project that
// contains the task identified by the {taskId} URL parameter. Admins bypass.
func (s *Server) requireTaskAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUserRole(r) == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		taskID := chi.URLParam(r, "taskId")
		task, err := s.Store.GetTask(taskID)
		if err != nil || task == nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		if task.ProjectID != "" {
			project, err := s.Store.GetProject(task.ProjectID)
			if err != nil || project == nil || project.OwnerID != GetUserID(r) {
				writeError(w, http.StatusForbidden, "access denied")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAssetAccess ensures the authenticated user owns the project that
// contains the asset identified by the {assetId} URL parameter. Admins bypass.
func (s *Server) requireAssetAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUserRole(r) == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		assetID := chi.URLParam(r, "assetId")
		asset, err := s.Store.GetAsset(assetID)
		if err != nil || asset == nil {
			writeError(w, http.StatusNotFound, "asset not found")
			return
		}
		project, err := s.Store.GetProject(asset.ProjectID)
		if err != nil || project == nil || project.OwnerID != GetUserID(r) {
			writeError(w, http.StatusForbidden, "access denied")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WSAuthMiddleware is like AuthMiddleware but also accepts the JWT via the
// "token" query parameter. Browsers cannot set custom headers on WebSocket
// connections, so the token must be passed as ?token=<jwt> instead.
func (s *Server) WSAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local mode: skip auth, inject default user.
		if s.Config.Get().JWTSecret == "" {
			ctx := r.Context()
			ctx = context.WithValue(ctx, ContextKeyUserID, "local")
			ctx = context.WithValue(ctx, ContextKeyUsername, "local")
			ctx = context.WithValue(ctx, ContextKeyRole, "admin")
			ctx = context.WithValue(ctx, ContextKeyFeatures, int64(0xFF))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

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
