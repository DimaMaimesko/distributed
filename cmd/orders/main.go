// Command orders serves the orders HTTP API and publishes an OrderCreated
// event for every new order.
//
// Environment: PORT (default 8080), HTTP_TIMEOUT (default 10s),
// ALLOWED_ORIGINS (comma-separated), NATS_URL.
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

	srv := orders.NewServer(cfg, orders.NewMemoryStore(), pub,
		middleware.Check{Name: "nats", Func: natsmsg.HealthCheck(nc)},
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

func loadConfig() (orders.Config, error) {
	cfg := orders.Config{Port: 8080, HTTPTimeout: 10 * time.Second}

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
