package orders

import (
	"fmt"
	"net/http"
	"time"

	"distributed/platform/middleware"
)

type Config struct {
	Port           int
	HTTPTimeout    time.Duration
	AllowedOrigins []string
}

// NewServer wires routes and the middleware chain. ready are the
// dependencies checked by the readiness probe.
func NewServer(cfg Config, store Store, pub publisher, ready ...middleware.Check) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz/live", middleware.LivenessHandler)
	mux.HandleFunc("GET /healthz/ready", middleware.ReadinessHandler(ready...))

	mux.HandleFunc("GET /orders/{id}", getOrderHandler(store))
	mux.HandleFunc("POST /orders", createOrderHandler(store, pub))

	handler := middleware.Chain(
		mux,
		middleware.RequestID(), // first, so every layer below can log the ID
		middleware.Logging(),   // outside Recover/Timeout: logs the 500/504 they write
		middleware.CORS(cfg.AllowedOrigins),
		middleware.Recover(),
		middleware.Timeout(cfg.HTTPTimeout),
		middleware.BodyClose(),
	)

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Must outlast the handler timeout, or the connection is cut
		// before the 504 is written.
		WriteTimeout:   cfg.HTTPTimeout + 5*time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}
