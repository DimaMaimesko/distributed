package orders

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-process Store with the same semantics as
// PostgresStore. Orders are lost on restart.
type MemoryStore struct {
	mu      sync.RWMutex
	orders  map[string]Order
	history map[string][]StatusChange
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{orders: make(map[string]Order), history: make(map[string][]StatusChange)}
}

func (s *MemoryStore) Create(_ context.Context, o Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orders[o.ID]; ok {
		return ErrAlreadyExists
	}
	if o.IdempotencyKey != "" {
		if _, err := s.byKeyLocked(o.UserID, o.IdempotencyKey); err == nil {
			return ErrIdempotencyKeyUsed
		}
	}
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

func (s *MemoryStore) GetByIdempotencyKey(_ context.Context, userID, key string) (Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byKeyLocked(userID, key)
}

func (s *MemoryStore) byKeyLocked(userID, key string) (Order, error) {
	for _, o := range s.orders {
		if o.UserID == userID && o.IdempotencyKey == key {
			return o, nil
		}
	}
	return Order{}, ErrNotFound
}

func (s *MemoryStore) ListByUser(_ context.Context, userID string, limit, offset int) ([]Order, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var mine []Order
	for _, o := range s.orders {
		if o.UserID == userID {
			mine = append(mine, o)
		}
	}
	slices.SortFunc(mine, func(a, b Order) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if offset >= len(mine) {
		return nil, nil
	}
	return mine[offset:min(offset+limit, len(mine))], nil
}

func (s *MemoryStore) UpdateStatus(_ context.Context, id, status string, version int, changedBy string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return Order{}, ErrNotFound
	}
	if o.Version != version {
		return Order{}, ErrVersionConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.history[id] = append(s.history[id], StatusChange{From: o.Status, To: status, ChangedBy: changedBy, ChangedAt: now})
	o.Status, o.Version, o.UpdatedAt = status, o.Version+1, now
	s.orders[id] = o
	return o, nil
}

func (s *MemoryStore) History(_ context.Context, id string) ([]StatusChange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.orders[id]; !ok {
		return nil, ErrNotFound
	}
	return slices.Clone(s.history[id]), nil
}
