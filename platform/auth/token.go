package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Identity is who a token is issued to.
type Identity struct {
	UserID string
	Email  string
	Role   string
	Scopes []string
}

// Signer issues access tokens. Only the auth service has one.
type Signer struct {
	key      *ecdsa.PrivateKey
	keyID    string
	issuer   string
	audience []string
	ttl      time.Duration
}

// NewSigner returns a Signer whose tokens carry iss=issuer, aud=audience
// and expire after ttl. The key ID is the key's RFC 7638 thumbprint, so it
// is stable across restarts and matches the published JWKS.
func NewSigner(key *ecdsa.PrivateKey, issuer string, audience []string, ttl time.Duration) (*Signer, error) {
	kid, err := Thumbprint(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, keyID: kid, issuer: issuer, audience: audience, ttl: ttl}, nil
}

func (s *Signer) PublicKey() *ecdsa.PublicKey { return &s.key.PublicKey }
func (s *Signer) TTL() time.Duration          { return s.ttl }

// Issue signs an access token for id.
func (s *Signer) Issue(id Identity) (string, error) {
	now := time.Now()
	claims := Claims{
		Email: id.Email,
		Role:  id.Role,
		Scope: id.Scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   id.UserID,
			Audience:  s.audience,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
			ID:        rand.Text(), // jti, for blocklisting a single token
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = s.keyID
	return token.SignedString(s.key)
}

// ErrUnknownKey is returned by a KeySet that has no key with the given ID.
var ErrUnknownKey = errors.New("auth: unknown signing key")

// KeySet resolves the key ID in a token header to a verification key.
type KeySet interface {
	Key(ctx context.Context, kid string) (*ecdsa.PublicKey, error)
}

// StaticKeys is a fixed KeySet keyed by thumbprint.
type StaticKeys map[string]*ecdsa.PublicKey

func NewStaticKeys(keys ...*ecdsa.PublicKey) (StaticKeys, error) {
	ks := make(StaticKeys, len(keys))
	for _, k := range keys {
		kid, err := Thumbprint(k)
		if err != nil {
			return nil, err
		}
		ks[kid] = k
	}
	return ks, nil
}

func (ks StaticKeys) Key(_ context.Context, kid string) (*ecdsa.PublicKey, error) {
	if k, ok := ks[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

// clockSkew is how far apart the issuer's and the validator's clocks may be.
const clockSkew = 30 * time.Second

// Validator checks access tokens locally, with no call to the auth service.
type Validator struct {
	keys     KeySet
	issuer   string
	audience string
}

// NewValidator accepts only ES256 tokens signed by a key in keys, issued
// by issuer and addressed to audience (this service's name). Checking iss
// and aud stops a token minted for another environment or another service
// from being accepted here.
func NewValidator(keys KeySet, issuer, audience string) *Validator {
	return &Validator{keys: keys, issuer: issuer, audience: audience}
}

func (v *Validator) Validate(ctx context.Context, tokenString string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims,
		func(t *jwt.Token) (any, error) {
			// Check the algorithm before handing out a key. This, together
			// with WithValidMethods, is what rejects "alg": "none" and
			// HS256-signed-with-the-public-key forgeries.
			if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
				return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
			}
			kid, _ := t.Header["kid"].(string)
			if kid == "" {
				return nil, errors.New("token has no kid")
			}
			return v.keys.Key(ctx, kid)
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(clockSkew),
		// Reject non-canonical base64. Otherwise the unused low bits of the
		// last signature character can be flipped, giving one token several
		// valid spellings and defeating anything keyed on the token string.
		jwt.WithStrictDecoding(),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid token: %w", err)
	}
	return claims, nil
}
