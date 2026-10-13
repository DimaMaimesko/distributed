// Package orders is the orders HTTP service: create, list, read and
// update orders, and announce changes on the message queue.
package orders

import (
	"context"
	"errors"
	"slices"
	"time"
)

type Order struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ProductID string    `json:"product_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Version increases on every change. Clients send back the version
	// they saw; a mismatch means someone else changed the order meanwhile.
	Version int `json:"version"`

	IdempotencyKey string `json:"-"`
}

type StatusChange struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	ChangedBy string    `json:"changed_by"`
	ChangedAt time.Time `json:"changed_at"`
}

const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusShipped   = "shipped"
	StatusCancelled = "cancelled"
)

// transitions lists the allowed next statuses. shipped and cancelled are
// final.
var transitions = map[string][]string{
	StatusPending: {StatusPaid, StatusCancelled},
	StatusPaid:    {StatusShipped, StatusCancelled},
}

func CanTransition(from, to string) bool {
	return slices.Contains(transitions[from], to)
}

var (
	ErrNotFound      = errors.New("order not found")
	ErrAlreadyExists = errors.New("order already exists")
	// ErrIdempotencyKeyUsed means this user already created an order with
	// the same idempotency key.
	ErrIdempotencyKeyUsed = errors.New("idempotency key already used")
	// ErrVersionConflict means the order changed since the caller read it.
	ErrVersionConflict = errors.New("order was modified concurrently")
)

// Store persists orders. Implementations return the errors above, never
// driver-specific ones.
type Store interface {
	Create(ctx context.Context, o Order) error
	GetByID(ctx context.Context, id string) (Order, error)
	GetByIdempotencyKey(ctx context.Context, userID, key string) (Order, error)
	// ListByUser returns a user's orders, newest first.
	ListByUser(ctx context.Context, userID string, limit, offset int) ([]Order, error)
	// UpdateStatus sets the status if the order is still at version,
	// increments the version and records the change in the history, all
	// atomically. It returns the updated order.
	UpdateStatus(ctx context.Context, id, status string, version int, changedBy string) (Order, error)
	// History returns an order's status changes, oldest first.
	History(ctx context.Context, id string) ([]StatusChange, error)
}
