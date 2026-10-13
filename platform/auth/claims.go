// Package auth issues and validates ES256 JWT access tokens, publishes and
// fetches signing keys as a JWKS, and provides the HTTP middleware that
// turns a bearer token into claims on the request context.
//
// Services that only validate tokens need a Validator backed by a
// JWKSClient; only the auth service holds a Signer.
package auth

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"distributed/platform/ctxutil"
)

// Claims is the payload of an access token. The user ID is the standard
// "sub" claim.
type Claims struct {
	Email string `json:"email,omitempty"`
	Role  string `json:"role,omitempty"`
	Scope Scopes `json:"scope,omitempty"`
	jwt.RegisteredClaims
}

// UserID returns the authenticated user's ID.
func (c *Claims) UserID() string { return c.Subject }

// Scopes is a list of permissions such as "orders:read". In a token it is
// encoded as one space-separated string, as in OAuth 2.0.
type Scopes []string

func (s Scopes) MarshalJSON() ([]byte, error) {
	return json.Marshal(strings.Join(s, " "))
}

func (s *Scopes) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	*s = strings.Fields(str)
	return nil
}

func (s Scopes) Has(scope string) bool { return slices.Contains(s, scope) }

// The keys are defined here rather than in ctxutil because Claims is an
// auth type; ctxutil importing auth would create an import cycle.
var (
	claimsKey = ctxutil.NewKey[*Claims]("claims")
	tokenKey  = ctxutil.NewKey[string]("bearer_token")
)

// ClaimsFromContext returns the claims stored by Authenticate.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	return claimsKey.Value(ctx)
}

// TokenFromContext returns the raw bearer token stored by Authenticate,
// for forwarding to downstream services.
func TokenFromContext(ctx context.Context) (string, bool) {
	return tokenKey.Value(ctx)
}

// ContextWithClaims returns ctx carrying claims, the token they came from,
// and the user ID (ctxutil.UserID) that log lines pick up. Authenticate
// calls it; tests can use it to fake an authenticated request.
func ContextWithClaims(ctx context.Context, c *Claims, token string) context.Context {
	ctx = claimsKey.With(ctx, c)
	ctx = tokenKey.With(ctx, token)
	return ctxutil.WithUserID(ctx, c.UserID())
}
