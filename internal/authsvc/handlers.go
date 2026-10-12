package authsvc

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"distributed/platform/auth"
	"distributed/platform/middleware"
)

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type userResponse struct {
	ID     string   `json:"id"`
	Email  string   `json:"email"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

// RoleCustomer and CustomerScopes are given to every self-registered user.
const RoleCustomer = "customer"

var CustomerScopes = []string{"orders:read", "orders:write"}

func registerHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}
		if addr, err := mail.ParseAddress(req.Email); err != nil || addr.Address != strings.TrimSpace(req.Email) {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_FIELD", "email is invalid")
			return
		}

		user, err := NewUser(req.Email, req.Password, RoleCustomer, CustomerScopes)
		if errors.Is(err, auth.ErrWeakPassword) {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_FIELD", err.Error())
			return
		}
		if err == nil {
			err = store.CreateUser(r.Context(), user)
		}
		switch {
		case errors.Is(err, ErrEmailTaken):
			middleware.WriteError(w, http.StatusConflict, "EMAIL_TAKEN", "email already registered")
			return
		case err != nil:
			internalError(w, r, "register user failed", err)
			return
		}

		middleware.WriteJSON(w, http.StatusCreated, userResponse{
			ID: user.ID, Email: user.Email, Role: user.Role, Scopes: user.Scopes,
		})
	}
}

// NewUser builds a User with a hashed password and a fresh ID.
func NewUser(email, password, role string, scopes []string) (User, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return User{}, err
	}
	return User{
		ID:           "usr_" + strings.ToLower(rand.Text()),
		Email:        strings.TrimSpace(email),
		PasswordHash: hash,
		Role:         role,
		Scopes:       scopes,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func loginHandler(store Store, hub *auth.Hub, signer *auth.Signer, refreshTTL time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method      string          `json:"method"`
			Credentials json.RawMessage `json:"credentials"`
		}
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}

		userID, err := hub.Authenticate(r.Context(), req.Method, req.Credentials)
		switch {
		case errors.Is(err, auth.ErrUnknownMethod):
			middleware.WriteError(w, http.StatusBadRequest, "UNKNOWN_METHOD", err.Error())
			return
		case errors.Is(err, auth.ErrInvalidCredentials):
			// Same response for unknown user and wrong password.
			middleware.WriteError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid credentials")
			return
		case err != nil:
			internalError(w, r, "authenticate failed", err)
			return
		}

		user, err := store.FindByID(r.Context(), userID)
		if err != nil {
			internalError(w, r, "load authenticated user failed", err)
			return
		}
		refresh, err := store.CreateRefreshToken(r.Context(), user.ID, refreshTTL)
		if err != nil {
			internalError(w, r, "create refresh token failed", err)
			return
		}
		writeTokenPair(w, r, signer, user, refresh)
	}
}

func refreshHandler(store Store, signer *auth.Signer, refreshTTL time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}

		userID, refresh, err := store.RotateRefreshToken(r.Context(), req.RefreshToken, refreshTTL)
		if err != nil {
			if errors.Is(err, ErrTokenNotFound) || errors.Is(err, ErrTokenExpired) {
				middleware.WriteError(w, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token invalid or expired")
				return
			}
			internalError(w, r, "rotate refresh token failed", err)
			return
		}

		user, err := store.FindByID(r.Context(), userID)
		if errors.Is(err, ErrUserNotFound) {
			// The account was deleted after the token was issued.
			middleware.WriteError(w, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", "refresh token invalid or expired")
			return
		}
		if err != nil {
			internalError(w, r, "load user failed", err)
			return
		}
		writeTokenPair(w, r, signer, user, refresh)
	}
}

// logoutHandler revokes a refresh token. The access token stays valid
// until it expires, which is why access tokens are short-lived.
func logoutHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}
		if err := store.RevokeRefreshToken(r.Context(), req.RefreshToken); err != nil {
			internalError(w, r, "revoke refresh token failed", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeTokenPair(w http.ResponseWriter, r *http.Request, signer *auth.Signer, user User, refresh string) {
	access, err := signer.Issue(auth.Identity{
		UserID: user.ID,
		Email:  user.Email,
		Role:   user.Role,
		Scopes: user.Scopes,
	})
	if err != nil {
		internalError(w, r, "issue access token failed", err)
		return
	}
	// Tokens must never be cached by proxies or the browser.
	w.Header().Set("Cache-Control", "no-store")
	middleware.WriteJSON(w, http.StatusOK, TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(signer.TTL().Seconds()),
	})
}

func internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.ErrorContext(r.Context(), msg, "error", err)
	middleware.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
}
