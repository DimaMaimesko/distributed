package authsvc

import (
	"context"
	"encoding/json"
	"errors"

	"distributed/platform/auth"
)

// MethodPassword is the login method for email and password.
const MethodPassword = "password"

// PasswordAgent is the built-in auth.Agent for email and password.
type PasswordAgent struct {
	store     Store
	dummyHash string
}

func NewPasswordAgent(store Store) (*PasswordAgent, error) {
	// Hashed once at startup; see Authenticate.
	dummy, err := auth.HashPassword("not-a-real-password")
	if err != nil {
		return nil, err
	}
	return &PasswordAgent{store: store, dummyHash: dummy}, nil
}

type passwordCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *PasswordAgent) Authenticate(ctx context.Context, raw json.RawMessage) (string, error) {
	var c passwordCredentials
	if err := json.Unmarshal(raw, &c); err != nil || c.Email == "" || c.Password == "" {
		return "", auth.ErrInvalidCredentials
	}

	user, err := a.store.FindByEmail(ctx, c.Email)
	if errors.Is(err, ErrUserNotFound) {
		// Spend the same ~300ms as a real check, so response time does
		// not reveal which emails are registered.
		auth.VerifyPassword(a.dummyHash, c.Password)
		return "", auth.ErrInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	if !auth.VerifyPassword(user.PasswordHash, c.Password) {
		return "", auth.ErrInvalidCredentials
	}
	return user.ID, nil
}
