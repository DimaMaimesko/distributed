package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost makes one hash take roughly 250-300ms, which makes guessing
// expensive. It is a variable only so tests can lower it.
var bcryptCost = 12

const (
	MinPasswordLength = 8
	// MaxPasswordBytes is bcrypt's input limit; longer inputs are rejected
	// rather than silently truncated.
	MaxPasswordBytes = 72
)

var ErrWeakPassword = fmt.Errorf("password must be %d to %d bytes", MinPasswordLength, MaxPasswordBytes)

// HashPassword hashes a password for storage.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordBytes {
		return "", ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches hash.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// SetPasswordCostForTesting lowers the bcrypt cost so tests that hash
// passwords run fast. It returns a function that restores the old cost.
// Never call it outside tests.
func SetPasswordCostForTesting() (restore func()) {
	old := bcryptCost
	bcryptCost = bcrypt.MinCost
	return func() { bcryptCost = old }
}
