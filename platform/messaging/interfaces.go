// Package messaging defines the contract between application code and the
// messaging layer. Services depend on these interfaces, never on a concrete
// broker client, so NATS can be swapped for Kafka (or an in-memory fake in
// tests) without touching business logic.
package messaging

import "context"

// Publisher sends messages to a subject.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
	Close() error
}

// Subscriber delivers messages published to a subject. The returned
// channel is closed when ctx is cancelled or the Subscriber is closed.
type Subscriber interface {
	Subscribe(ctx context.Context, subject string) (<-chan Message, error)
	Close() error
}

// Message is a broker-independent message.
type Message struct {
	Subject string
	Data    []byte
	Reply   string
	Ack     func() error // no-op in NATS Core; commits offset in Kafka
}
