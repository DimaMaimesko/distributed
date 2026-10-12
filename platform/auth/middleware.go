package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"distributed/platform/middleware"
)

// Authenticate requires a valid "Authorization: Bearer <token>" header and
// stores the claims and raw token in the request context. Apply it per
// route, so public routes (health, login) stay public by construction.
func Authenticate(v *Validator) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				unauthorized(w, "", "MISSING_TOKEN", "authorization required")
				return
			}

			scheme, token, ok := strings.Cut(header, " ")
			token = strings.TrimSpace(token)
			if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
				unauthorized(w, "invalid_request", "INVALID_TOKEN_FORMAT", "expected Bearer token")
				return
			}

			claims, err := v.Validate(r.Context(), token)
			if err != nil {
				// Expired and malformed tokens are routine and not logged.
				// A correctly signed token from the wrong issuer or for
				// another audience is not: it points at a token leaking
				// between environments or services.
				if errors.Is(err, jwt.ErrTokenInvalidIssuer) || errors.Is(err, jwt.ErrTokenInvalidAudience) {
					slog.WarnContext(r.Context(), "token rejected: wrong issuer or audience",
						"error", err,
						"request_id", middleware.RequestIDFromContext(r.Context()),
						"remote_addr", r.RemoteAddr,
					)
				}
				unauthorized(w, "invalid_token", "INVALID_TOKEN", "token is invalid or expired")
				return
			}

			next.ServeHTTP(w, r.WithContext(ContextWithClaims(r.Context(), claims, token)))
		})
	}
}

// unauthorized writes a 401 with the WWW-Authenticate header RFC 6750
// requires for bearer-token endpoints.
func unauthorized(w http.ResponseWriter, oauthErr, code, message string) {
	challenge := "Bearer"
	if oauthErr != "" {
		challenge += fmt.Sprintf(` error=%q`, oauthErr)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	middleware.WriteError(w, http.StatusUnauthorized, code, message)
}

// RequireScope allows the request only if the token has scope. Scopes are
// matched exactly: "orders:write" does not imply "orders:read", so issue
// both when both are meant.
func RequireScope(scope string) middleware.Middleware {
	return require(func(c *Claims) bool { return c.Scope.Has(scope) },
		"INSUFFICIENT_SCOPE", fmt.Sprintf("scope %q required", scope))
}

// RequireRole allows the request only if the token has one of roles.
func RequireRole(roles ...string) middleware.Middleware {
	return require(func(c *Claims) bool { return slices.Contains(roles, c.Role) },
		"FORBIDDEN", "access denied")
}

func require(allowed func(*Claims) bool, code, message string) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				unauthorized(w, "", "UNAUTHENTICATED", "not authenticated")
				return
			}
			if !allowed(claims) {
				middleware.WriteError(w, http.StatusForbidden, code, message)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
