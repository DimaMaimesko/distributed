package nats

import (
	"context"

	"github.com/nats-io/nats.go"

	"distributed/platform/messaging"
)

var _ messaging.Publisher = (*Publisher)(nil)

// Publisher publishes over an existing connection. It does not own the
// connection: Close flushes pending messages but leaves nc open.
type Publisher struct {
	nc *nats.Conn
}

func NewPublisher(nc *nats.Conn) *Publisher {
	return &Publisher{nc: nc}
}

// Publish buffers data for delivery to subject. In NATS Core a nil error
// means the message was handed to the client, not that anyone received it.
func (p *Publisher) Publish(ctx context.Context, subject string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.nc.Publish(subject, data)
}

// Close flushes buffered messages to the server.
func (p *Publisher) Close() error {
	return p.nc.Flush()
}
