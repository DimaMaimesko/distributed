// Package orders is the orders HTTP service: create and fetch orders, and
// announce new ones on the message queue.
package orders

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Order struct {
	ID        string    `json:"id"`
	ProductID string    `json:"product_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

var ErrNotFound = errors.New("order not found")

// Store persists orders.
type Store interface {
	Create(ctx context.Context, o Order) error
	GetByID(ctx context.Context, id string) (Order, error)
}

// MemoryStore is an in-process Store. Orders are lost on restart.
type MemoryStore struct {
	mu     sync.RWMutex
	orders map[string]Order
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{orders: make(map[string]Order)}
}

func (s *MemoryStore) Create(_ context.Context, o Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[o.ID] = o
	return nil
}

func (s *MemoryStore) GetByID(_ context.Context, id string) (Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orders[id]
	if !ok {
		return Order{}, ErrNotFound
	}
	return o, nil
}
