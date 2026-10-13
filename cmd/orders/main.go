// Command orders serves the orders HTTP API and publishes an event for
// every new order and status change.
//
// Environment:
//
//	PORT             default 8080
//	HTTP_TIMEOUT     default 10s
//	ALLOWED_ORIGINS  comma-separated
//	NATS_URL
//	AUTH_JWKS_URL    default http://localhost:8081/.well-known/jwks.json
//	AUTH_ISSUER      default http://localhost:8081
//	DATABASE_URL     postgres://...; without it orders are kept in memory
//	DB_MIGRATE       "true" applies migrations at startup (development);
//	                 in production run `orders -migrate` as its own step
//	DB_MAX_CONNS     pool size, default 2 × CPUs (min 4)
//
// Flags:
//
//	-migrate  apply database migrations and exit
package main

import (
	"context"
	"errors"
	"flag"
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
	"distributed/platform/ctxutil"
	"distributed/platform/database"
	natsmsg "distributed/platform/messaging/nats"
	"distributed/platform/middleware"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "apply database migrations and exit")
	flag.Parse()

	logger := slog.New(ctxutil.LogHandler(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(2)
	}

	if *migrateOnly {
		err = migrate(cfg)
	} else {
		err = run(logger, cfg)
	}
	if err != nil {
		logger.Error("orders failed", "error", err)
		os.Exit(1)
	}
}

func migrate(cfg config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("-migrate needs DATABASE_URL")
	}
	return database.RunMigrations(cfg.DatabaseURL, orders.Migrations, orders.MigrationsDir)
}

func run(logger *slog.Logger, cfg config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var ready []middleware.Check

	store, closeStore, err := openStore(ctx, logger, cfg, &ready)
	if err != nil {
		return err
	}
	defer closeStore()

	// Start even if NATS is down: readiness reports it until it connects.
	nc, err := natsmsg.Connect(cfg.NATSURL, "orders", logger, nats.RetryOnFailedConnect(true))
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()
	pub := natsmsg.NewPublisher(nc)
	defer pub.Close()
	ready = append(ready, middleware.Check{Name: "nats", Func: natsmsg.HealthCheck(nc)})

	// Start even if the auth service is down: keys are fetched again on
	// the first token, and readiness reports them missing until then.
	keys := auth.NewJWKSClient(cfg.JWKSURL)
	if err := keys.Refresh(ctx); err != nil {
		logger.Warn("could not fetch signing keys yet", "url", cfg.JWKSURL, "error", err)
	}
	tokens := auth.NewValidator(keys, cfg.Issuer, orders.Audience)
	ready = append(ready, middleware.Check{Name: "auth-keys", Func: keys.Ready})

	srv := orders.NewServer(cfg.Config, store, pub, tokens, ready...)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("orders service listening", "addr", srv.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	if err := concurrency.GracefulShutdown(ctx, 30*time.Second, srv); err != nil {
		return err
	}
	logger.Info("orders service stopped")
	return nil
}

// openStore returns the PostgreSQL store when DATABASE_URL is set and the
// in-memory store otherwise. It adds the database to the readiness checks.
func openStore(ctx context.Context, logger *slog.Logger, cfg config, ready *[]middleware.Check) (orders.Store, func(), error) {
	if cfg.DatabaseURL == "" {
		logger.Warn("DATABASE_URL not set; orders are kept in memory and lost on restart")
		return orders.NewMemoryStore(), func() {}, nil
	}

	opts := []database.Option{database.WithAppName("orders")}
	if cfg.DBMaxConns > 0 {
		opts = append(opts, database.WithMaxConns(cfg.DBMaxConns))
	}
	// The database may still be starting (e.g. both just scheduled), so
	// retry with backoff rather than crash on the first refusal.
	pool, err := database.ConnectWithRetry(ctx, cfg.DatabaseURL, opts...)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("connected to database", "max_conns", pool.Config().MaxConns)

	// Migrate after connecting, so the retry above has already waited
	// for the database to come up.
	if cfg.Migrate {
		if err := migrate(cfg); err != nil {
			pool.Close()
			return nil, nil, err
		}
	}

	*ready = append(*ready, middleware.Check{Name: "postgres", Func: database.Check(pool)})
	return orders.NewPostgresStore(pool), pool.Close, nil
}

type config struct {
	orders.Config
	JWKSURL     string
	Issuer      string
	NATSURL     string
	DatabaseURL string
	Migrate     bool
	DBMaxConns  int32
}

func loadConfig() (config, error) {
	cfg := config{
		Config:      orders.Config{Port: 8080, HTTPTimeout: 10 * time.Second},
		JWKSURL:     envString("AUTH_JWKS_URL", "http://localhost:8081/.well-known/jwks.json"),
		Issuer:      envString("AUTH_ISSUER", "http://localhost:8081"),
		NATSURL:     os.Getenv("NATS_URL"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Migrate:     os.Getenv("DB_MIGRATE") == "true",
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
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("DB_MAX_CONNS: must be a positive integer")
		}
		cfg.DBMaxConns = int32(n)
	}
	for o := range strings.SplitSeq(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}
	return cfg, nil
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
