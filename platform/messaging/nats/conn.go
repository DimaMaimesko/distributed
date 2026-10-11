// Package nats implements messaging.Publisher and messaging.Subscriber on
// top of NATS Core (at-most-once delivery, no persistence).
package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Connect dials url with infinite reconnects and logs connection events,
// so a NATS restart degrades to a gap in delivery rather than a crash.
// An empty url means nats.DefaultURL. opts are applied after the defaults.
func Connect(url, name string, logger *slog.Logger, opts ...nats.Option) (*nats.Conn, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	defaults := []nats.Option{
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil { // nil on a deliberate Close
				logger.Warn("disconnected from NATS", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("reconnected to NATS", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			logger.Info("NATS connection closed")
		}),
	}
	return nats.Connect(url, append(defaults, opts...)...)
}

// HealthCheck returns a readiness check that fails while nc is not
// connected, e.g. during a reconnect.
func HealthCheck(nc *nats.Conn) func(context.Context) error {
	return func(context.Context) error {
		if !nc.IsConnected() {
			return fmt.Errorf("nats: connection is %v", nc.Status())
		}
		return nil
	}
}
