package orders

import (
	"fmt"
	"net/http"
	"time"

	"distributed/platform/auth"
	"distributed/platform/middleware"
)

// Audience is this service's name in the aud claim of access tokens.
const Audience = "orders"

type Config struct {
	Port           int
	HTTPTimeout    time.Duration
	AllowedOrigins []string
}

// NewServer wires routes and the middleware chain. tokens validates access
// tokens; ready are the dependencies checked by the readiness probe.
func NewServer(cfg Config, store Store, pub publisher, tokens *auth.Validator, ready ...middleware.Check) *http.Server {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /healthz/live", middleware.LivenessHandler)
	mux.HandleFunc("GET /healthz/ready", middleware.ReadinessHandler(ready...))

	// Protected routes: auth is applied per route, so a new route is
	// public only if it is registered without it.
	authn := auth.Authenticate(tokens)
	read := func(h http.Handler) http.Handler {
		return middleware.Chain(h, authn, auth.RequireScope("orders:read"))
	}
	write := func(h http.Handler) http.Handler {
		return middleware.Chain(h, authn, auth.RequireScope("orders:write"))
	}
	mux.Handle("GET /orders", read(listOrdersHandler(store)))
	mux.Handle("GET /orders/{id}", read(getOrderHandler(store)))
	mux.Handle("GET /orders/{id}/history", read(historyHandler(store)))
	mux.Handle("POST /orders", write(createOrderHandler(store, pub)))
	mux.Handle("PATCH /orders/{id}", write(updateStatusHandler(store, pub)))

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
