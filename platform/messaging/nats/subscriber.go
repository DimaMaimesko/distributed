package nats

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go"

	"distributed/platform/messaging"
)

var _ messaging.Subscriber = (*Subscriber)(nil)

// bufferSize is how many messages may wait between the NATS client and the
// application before the client starts reporting slow-consumer errors.
const bufferSize = 64

// Subscriber subscribes over an existing connection. It does not own the
// connection: Close ends all subscriptions but leaves nc open.
type Subscriber struct {
	nc         *nats.Conn
	queueGroup string

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type Option func(*Subscriber)

// WithQueueGroup makes every Subscribe call join the named queue group, so
// each message goes to exactly one subscriber in the group. Run several
// consumers with the same group to scale horizontally.
func WithQueueGroup(name string) Option {
	return func(s *Subscriber) { s.queueGroup = name }
}

func NewSubscriber(nc *nats.Conn, opts ...Option) *Subscriber {
	s := &Subscriber{nc: nc, done: make(chan struct{})}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func noAck() error { return nil }

// Subscribe starts receiving messages on subject. The returned channel is
// closed after ctx is cancelled or Close is called.
func (s *Subscriber) Subscribe(ctx context.Context, subject string) (<-chan messaging.Message, error) {
	in := make(chan *nats.Msg, bufferSize)

	var (
		sub *nats.Subscription
		err error
	)
	if s.queueGroup != "" {
		sub, err = s.nc.ChanQueueSubscribe(subject, s.queueGroup, in)
	} else {
		sub, err = s.nc.ChanSubscribe(subject, in)
	}
	if err != nil {
		return nil, err
	}

	// The goroutine is the only writer to out, so it alone may close it.
	out := make(chan messaging.Message)
	s.wg.Go(func() {
		defer close(out)
		defer sub.Unsubscribe()

		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case m := <-in:
				msg := messaging.Message{
					Subject: m.Subject,
					Data:    m.Data,
					Reply:   m.Reply,
					Ack:     noAck,
				}
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				case <-s.done:
					return
				}
			}
		}
	})

	return out, nil
}

// Close ends all subscriptions and waits until their channels are closed.
// Messages still buffered are dropped, consistent with at-most-once
// delivery. Close is safe to call more than once.
func (s *Subscriber) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	s.wg.Wait()
	return nil
}
