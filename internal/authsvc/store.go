// Package authsvc is the auth service: it registers users, verifies their
// credentials and issues access and refresh tokens. No other service
// stores credentials or issues tokens.
package authsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	Scopes       []string
	CreatedAt    time.Time
}

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrEmailTaken    = errors.New("email already registered")
	ErrTokenNotFound = errors.New("refresh token not found")
	ErrTokenExpired  = errors.New("refresh token expired")
)

type Store interface {
	CreateUser(ctx context.Context, u User) error
	FindByEmail(ctx context.Context, email string) (User, error)
	FindByID(ctx context.Context, id string) (User, error)

	// CreateRefreshToken returns a new opaque refresh token for userID.
	CreateRefreshToken(ctx context.Context, userID string, ttl time.Duration) (string, error)
	// RotateRefreshToken consumes token and returns its user and a
	// replacement, atomically: of two concurrent calls with the same
	// token, only one succeeds.
	RotateRefreshToken(ctx context.Context, token string, ttl time.Duration) (userID, newToken string, err error)
	// RevokeRefreshToken deletes token. Unknown tokens are not an error.
	RevokeRefreshToken(ctx context.Context, token string) error
}

// MemoryStore is an in-process Store. Users and tokens are lost on restart.
type MemoryStore struct {
	mu      sync.Mutex
	users   map[string]User   // by ID
	byEmail map[string]string // normalized email -> ID
	refresh map[string]refreshRecord
}

type refreshRecord struct {
	userID    string
	expiresAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:   make(map[string]User),
		byEmail: make(map[string]string),
		refresh: make(map[string]refreshRecord),
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *MemoryStore) CreateUser(_ context.Context, u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	email := normalizeEmail(u.Email)
	if _, ok := s.byEmail[email]; ok {
		return ErrEmailTaken
	}
	s.users[u.ID] = u
	s.byEmail[email] = u.ID
	return nil
}

func (s *MemoryStore) FindByEmail(_ context.Context, email string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byEmail[normalizeEmail(email)]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return s.users[id], nil
}

func (s *MemoryStore) FindByID(_ context.Context, id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (s *MemoryStore) CreateRefreshToken(_ context.Context, userID string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newRefreshTokenLocked(userID, ttl), nil
}

func (s *MemoryStore) RotateRefreshToken(_ context.Context, token string, ttl time.Duration) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := hashToken(token)
	rec, ok := s.refresh[key]
	if !ok {
		return "", "", ErrTokenNotFound
	}
	delete(s.refresh, key)
	if time.Now().After(rec.expiresAt) {
		return "", "", ErrTokenExpired
	}
	return rec.userID, s.newRefreshTokenLocked(rec.userID, ttl), nil
}

func (s *MemoryStore) RevokeRefreshToken(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refresh, hashToken(token))
	return nil
}

func (s *MemoryStore) newRefreshTokenLocked(userID string, ttl time.Duration) string {
	token := rand.Text() // 130 random bits
	s.refresh[hashToken(token)] = refreshRecord{userID: userID, expiresAt: time.Now().Add(ttl)}
	return token
}

// hashToken is how refresh tokens are stored: a leaked store does not
// yield usable tokens. A fast hash is fine because the token is random,
// not a guessable password.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
