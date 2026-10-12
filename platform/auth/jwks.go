package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"distributed/platform/middleware"
)

// JWKS is a JSON Web Key Set (RFC 7517).
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK is a P-256 public key in JSON Web Key form.
type JWK struct {
	KeyType   string `json:"kty"`
	Algorithm string `json:"alg,omitempty"`
	Use       string `json:"use,omitempty"`
	KeyID     string `json:"kid,omitempty"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

// p256CoordSize is the fixed size of a P-256 coordinate. JWK requires the
// full width; big.Int.Bytes() would drop leading zero bytes.
const p256CoordSize = 32

// NewJWK encodes a P-256 public key. Its key ID is the RFC 7638 thumbprint.
func NewJWK(pub *ecdsa.PublicKey) (JWK, error) {
	if pub.Curve != elliptic.P256() {
		return JWK{}, errors.New("auth: only P-256 keys are supported")
	}
	b, err := pub.Bytes() // 0x04 || X || Y
	if err != nil {
		return JWK{}, fmt.Errorf("auth: encode public key: %w", err)
	}
	enc := base64.RawURLEncoding
	jwk := JWK{
		KeyType:   "EC",
		Algorithm: "ES256",
		Use:       "sig",
		Curve:     "P-256",
		X:         enc.EncodeToString(b[1 : 1+p256CoordSize]),
		Y:         enc.EncodeToString(b[1+p256CoordSize:]),
	}
	jwk.KeyID = jwk.thumbprint()
	return jwk, nil
}

// PublicKey decodes the JWK, rejecting anything but an ES256 P-256 key.
func (k JWK) PublicKey() (*ecdsa.PublicKey, error) {
	if k.KeyType != "EC" || k.Curve != "P-256" {
		return nil, fmt.Errorf("auth: unsupported key type %s/%s", k.KeyType, k.Curve)
	}
	if k.Algorithm != "" && k.Algorithm != "ES256" {
		return nil, fmt.Errorf("auth: unsupported key algorithm %s", k.Algorithm)
	}
	x, errX := base64.RawURLEncoding.DecodeString(k.X)
	y, errY := base64.RawURLEncoding.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != p256CoordSize || len(y) != p256CoordSize {
		return nil, errors.New("auth: malformed key coordinates")
	}
	point := append(append([]byte{4}, x...), y...)
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
}

// thumbprint is the RFC 7638 JWK thumbprint: SHA-256 over the required
// members in lexicographic order with no whitespace.
func (k JWK) thumbprint() string {
	canonical := fmt.Sprintf(`{"crv":%q,"kty":%q,"x":%q,"y":%q}`, k.Curve, k.KeyType, k.X, k.Y)
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Thumbprint returns the key ID Signer and JWKSHandler use for pub.
func Thumbprint(pub *ecdsa.PublicKey) (string, error) {
	jwk, err := NewJWK(pub)
	if err != nil {
		return "", err
	}
	return jwk.KeyID, nil
}

// jwksMaxAge is how long clients may cache the key set. A new key must be
// published at least this long before tokens are signed with it.
const jwksMaxAge = "public, max-age=900"

// JWKSHandler serves keys at GET /.well-known/jwks.json. During rotation,
// pass both the new and the old key until tokens signed with the old one
// have expired.
func JWKSHandler(keys ...*ecdsa.PublicKey) (http.HandlerFunc, error) {
	set := JWKS{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		jwk, err := NewJWK(k)
		if err != nil {
			return nil, err
		}
		set.Keys = append(set.Keys, jwk)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", jwksMaxAge)
		middleware.WriteJSON(w, http.StatusOK, set)
	}, nil
}
