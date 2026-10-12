package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// GenerateKey creates a P-256 signing key. Use it for tests and local
// development; in production load a persistent key with ParsePrivateKeyPEM,
// or every restart invalidates all issued tokens.
func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// ParsePrivateKeyPEM parses a P-256 private key in SEC 1 ("EC PRIVATE
// KEY", as written by `openssl ecparam -genkey`) or PKCS #8 ("PRIVATE
// KEY") PEM form.
func ParsePrivateKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("auth: no PEM block found")
	}

	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse EC private key: %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse PKCS #8 private key: %w", err)
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("auth: PKCS #8 key is %T, want ECDSA", k)
		}
		key = ek
	default:
		return nil, fmt.Errorf("auth: unsupported PEM block %q", block.Type)
	}

	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("auth: key curve is %s, want P-256", key.Curve.Params().Name)
	}
	return key, nil
}
