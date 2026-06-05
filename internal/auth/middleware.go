package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// contextKey is the key type for auth values in request context.
type contextKey string

const (
	userKey contextKey = "user"
	roleKey contextKey = "role"
)

// UserFromContext returns the authenticated username from the request context.
func UserFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userKey).(string)
	return v
}

// RoleFromContext returns the authenticated user's role from the request context.
func RoleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(roleKey).(string)
	return v
}

// Authenticate validates the Bearer token and adds user info to context.
func Authenticate(jwtManager *JWTManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeAuthError(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		claims, err := jwtManager.ValidateToken(token)
		if err != nil {
			if errors.Is(err, ErrExpiredToken) {
				writeAuthError(w, "token expired", http.StatusUnauthorized)
				return
			}
			writeAuthError(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, claims.Username)
		ctx = context.WithValue(ctx, roleKey, claims.Role)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole creates middleware that checks if the user has the required role.
// Admin role can access everything.
func RequireRole(allowedRoles ...string) func(http.Handler) http.Handler {
	roleSet := make(map[string]bool, len(allowedRoles))
	for _, r := range allowedRoles {
		roleSet[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := RoleFromContext(r.Context())
			if role == "" {
				writeAuthError(w, "authentication required", http.StatusUnauthorized)
				return
			}

			// Admin can access everything.
			if role == RoleAdmin {
				next.ServeHTTP(w, r)
				return
			}

			if !roleSet[role] {
				writeAuthError(w, "insufficient permissions", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractBearerToken extracts the Bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}

	return strings.TrimSpace(parts[1])
}

// writeAuthError writes an authentication error response.
func writeAuthError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer realm=\"rezuscloud\"")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "failure",
		"message": message,
		"reason":  "Unauthorized",
		"code":    code,
	})
}
