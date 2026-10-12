package authsvc

import (
	"fmt"
	"net/http"
	"time"

	"distributed/platform/auth"
	"distributed/platform/middleware"
)

type Config struct {
	Port            int
	HTTPTimeout     time.Duration
	AllowedOrigins  []string
	RefreshTokenTTL time.Duration
}

// NewServer wires the auth routes. All of them are public: this is where
// callers get their tokens.
func NewServer(cfg Config, store Store, hub *auth.Hub, signer *auth.Signer) (*http.Server, error) {
	jwks, err := auth.JWKSHandler(signer.PublicKey())
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz/live", middleware.LivenessHandler)
	mux.HandleFunc("GET /healthz/ready", middleware.ReadinessHandler())
	mux.HandleFunc("GET /.well-known/jwks.json", jwks)

	mux.HandleFunc("POST /auth/register", registerHandler(store))
	mux.HandleFunc("POST /auth/login", loginHandler(store, hub, signer, cfg.RefreshTokenTTL))
	mux.HandleFunc("POST /auth/refresh", refreshHandler(store, signer, cfg.RefreshTokenTTL))
	mux.HandleFunc("POST /auth/logout", logoutHandler(store))

	handler := middleware.Chain(
		mux,
		middleware.RequestID(),
		middleware.Logging(),
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
		WriteTimeout:      cfg.HTTPTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}, nil
}
