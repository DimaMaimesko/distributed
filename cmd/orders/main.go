// Command orders serves the orders HTTP API and publishes an OrderCreated
// event for every new order.
//
// Environment: PORT (default 8080), HTTP_TIMEOUT (default 10s),
// ALLOWED_ORIGINS (comma-separated), NATS_URL,
// AUTH_JWKS_URL (default http://localhost:8081/.well-known/jwks.json),
// AUTH_ISSUER (default http://localhost:8081).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"distributed/internal/orders"
	"distributed/platform/auth"
	"distributed/platform/concurrency"
	natsmsg "distributed/platform/messaging/nats"
	"distributed/platform/middleware"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	// Start even if NATS is down: readiness reports it until it connects.
	nc, err := natsmsg.Connect(os.Getenv("NATS_URL"), "orders", logger, nats.RetryOnFailedConnect(true))
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	pub := natsmsg.NewPublisher(nc)
	defer pub.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start even if the auth service is down: keys are fetched again on
	// the first token, and readiness reports them missing until then.
	keys := auth.NewJWKSClient(cfg.JWKSURL)
	if err := keys.Refresh(ctx); err != nil {
		logger.Warn("could not fetch signing keys yet", "url", cfg.JWKSURL, "error", err)
	}
	tokens := auth.NewValidator(keys, cfg.Issuer, orders.Audience)

	srv := orders.NewServer(cfg.Config, orders.NewMemoryStore(), pub, tokens,
		middleware.Check{Name: "nats", Func: natsmsg.HealthCheck(nc)},
		middleware.Check{Name: "auth-keys", Func: keys.Ready},
	)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("orders service listening", "addr", srv.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		logger.Error("server failed", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	if err := concurrency.GracefulShutdown(ctx, 30*time.Second, srv); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("orders service stopped")
}

type config struct {
	orders.Config
	JWKSURL string
	Issuer  string
}

func loadConfig() (config, error) {
	cfg := config{
		Config:  orders.Config{Port: 8080, HTTPTimeout: 10 * time.Second},
		JWKSURL: "http://localhost:8081/.well-known/jwks.json",
		Issuer:  "http://localhost:8081",
	}
	if v := os.Getenv("AUTH_JWKS_URL"); v != "" {
		cfg.JWKSURL = v
	}
	if v := os.Getenv("AUTH_ISSUER"); v != "" {
		cfg.Issuer = v
	}

	if v := os.Getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("PORT: %w", err)
		}
		cfg.Port = port
	}
	if v := os.Getenv("HTTP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("HTTP_TIMEOUT: %w", err)
		}
		cfg.HTTPTimeout = d
	}
	for o := range strings.SplitSeq(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}
	return cfg, nil
}
