package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const UserContextKey contextKey = "auth_user"

// UserContext holds the parsed JWT claims stored in the request context.
type UserContext struct {
	ID    string
	Email string
	Role  string
}

// RequireAuth is a strict middleware — rejects the request with 401 if no
// valid JWT is present.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := extractClaims(r)
		if err != nil {
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), UserContextKey, &UserContext{
			ID:    claims.UserID,
			Email: claims.Email,
			Role:  claims.Role,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth is a soft middleware — attaches user context if a valid JWT is
// present, but does not reject requests without one (used for checkout).
func OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := extractClaims(r)
		if err == nil {
			ctx := context.WithValue(r.Context(), UserContextKey, &UserContext{
				ID:    claims.UserID,
				Email: claims.Email,
				Role:  claims.Role,
			})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole wraps RequireAuth and additionally checks that the authenticated
// user holds the expected role (e.g. "admin").
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := GetUserFromContext(r)
			if user == nil || user.Role != role {
				http.Error(w, "forbidden: insufficient permissions", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// GetUserFromContext retrieves the UserContext stored by RequireAuth /
// OptionalAuth. Returns nil if the request is unauthenticated.
func GetUserFromContext(r *http.Request) *UserContext {
	u, _ := r.Context().Value(UserContextKey).(*UserContext)
	return u
}

// extractClaims reads the Bearer token from the Authorization header or the
// "token" HttpOnly cookie and parses the JWT claims.
func extractClaims(r *http.Request) (*Claims, error) {
	// Prefer Authorization header (mobile / API clients).
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return ParseToken(parts[1])
		}
	}

	// Fallback: HttpOnly cookie (browser clients).
	if cookie, err := r.Cookie("token"); err == nil {
		return ParseToken(cookie.Value)
	}

	return nil, ErrInvalidToken
}
