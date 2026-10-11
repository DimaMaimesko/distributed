// Package nats implements messaging.Publisher and messaging.Subscriber on
// top of NATS Core (at-most-once delivery, no persistence).
package nats

import (
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Connect dials url with infinite reconnects and logs connection events,
// so a NATS restart degrades to a gap in delivery rather than a crash.
// An empty url means nats.DefaultURL.
func Connect(url, name string, logger *slog.Logger) (*nats.Conn, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	return nats.Connect(url,
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
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
	)
}
