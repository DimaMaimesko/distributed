package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrInvalidCredentials is returned by an Agent when the credentials
	// are wrong or malformed.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUnknownMethod      = errors.New("auth: unknown authentication method")
)

// Agent authenticates one kind of credential (password, API key, OAuth,
// ...) and returns the user ID it belongs to.
type Agent interface {
	Authenticate(ctx context.Context, credentials json.RawMessage) (userID string, err error)
}

// Hub routes a login to the Agent registered for its method. Adding an
// authentication method means registering a new Agent; the login handler
// and token issuing do not change.
type Hub struct {
	mu     sync.RWMutex
	agents map[string]Agent
}

func NewHub() *Hub {
	return &Hub{agents: make(map[string]Agent)}
}

// Register makes agent handle method, replacing any previous agent.
func (h *Hub) Register(method string, agent Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[method] = agent
}

func (h *Hub) Authenticate(ctx context.Context, method string, credentials json.RawMessage) (string, error) {
	h.mu.RLock()
	agent, ok := h.agents[method]
	h.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownMethod, method)
	}
	return agent.Authenticate(ctx, credentials)
}
