// Package events holds the event schemas shared by services in this
// repository. Subject names and payload shapes are a public contract:
// change them by adding a new versioned subject, not by editing in place.
package events

import "time"

// SubjectOrderCreated carries OrderCreated events.
const SubjectOrderCreated = "orders.created"

type OrderCreated struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id,omitempty"`
	ProductID string    `json:"product_id"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
}

// SubjectOrderStatusChanged carries OrderStatusChanged events.
const SubjectOrderStatusChanged = "orders.status_changed"

type OrderStatusChanged struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Version   int       `json:"version"`
	ChangedBy string    `json:"changed_by"`
	ChangedAt time.Time `json:"changed_at"`
}
