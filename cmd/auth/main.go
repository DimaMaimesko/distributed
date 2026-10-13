// Command auth is the auth service: registration, login, token refresh and
// the JWKS endpoint other services fetch signing keys from.
//
// Environment:
//
//	PORT                   default 8081
//	HTTP_TIMEOUT           default 10s
//	ALLOWED_ORIGINS        comma-separated
//	AUTH_ISSUER            iss claim, default http://localhost:8081
//	AUTH_AUDIENCE          comma-separated aud claim, default "orders"
//	ACCESS_TOKEN_TTL       default 15m
//	REFRESH_TOKEN_TTL      default 720h
//	AUTH_PRIVATE_KEY_FILE  P-256 PEM key; if unset an ephemeral key is
//	                       generated and tokens die with the process.
//	                       Create one with:
//	                       openssl ecparam -name prime256v1 -genkey -noout -out key.pem
//	ADMIN_EMAIL, ADMIN_PASSWORD  optional admin user created at startup
package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"distributed/internal/authsvc"
	"distributed/platform/auth"
	"distributed/platform/concurrency"
	"distributed/platform/ctxutil"
)

func main() {
	logger := slog.New(ctxutil.LogHandler(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("auth service failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := authsvc.Config{
		Port:            envInt("PORT", 8081),
		HTTPTimeout:     envDuration("HTTP_TIMEOUT", 10*time.Second),
		AllowedOrigins:  envList("ALLOWED_ORIGINS", nil),
		RefreshTokenTTL: envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
	}
	issuer := envString("AUTH_ISSUER", "http://localhost:8081")
	audience := envList("AUTH_AUDIENCE", []string{"orders"})
	accessTTL := envDuration("ACCESS_TOKEN_TTL", 15*time.Minute)

	key, err := loadKey(logger)
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(key, issuer, audience, accessTTL)
	if err != nil {
		return err
	}

	store := authsvc.NewMemoryStore()
	if err := seedAdmin(store); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	passwords, err := authsvc.NewPasswordAgent(store)
	if err != nil {
		return err
	}
	hub := auth.NewHub()
	hub.Register(authsvc.MethodPassword, passwords)

	srv, err := authsvc.NewServer(cfg, store, hub, signer)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("auth service listening", "addr", srv.Addr, "issuer", issuer, "audience", audience)
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
	logger.Info("auth service stopped")
	return nil
}

func loadKey(logger *slog.Logger) (*ecdsa.PrivateKey, error) {
	path := os.Getenv("AUTH_PRIVATE_KEY_FILE")
	if path == "" {
		logger.Warn("AUTH_PRIVATE_KEY_FILE not set; using an ephemeral signing key")
		return auth.GenerateKey()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	return auth.ParsePrivateKeyPEM(data)
}

func seedAdmin(store authsvc.Store) error {
	email, password := os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")
	if email == "" || password == "" {
		return nil
	}
	admin, err := authsvc.NewUser(email, password, "admin", authsvc.CustomerScopes)
	if err != nil {
		return err
	}
	return store.CreateUser(context.Background(), admin)
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		invalidEnv(name, err)
	}
	return v
}

func envDuration(name string, def time.Duration) time.Duration {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		invalidEnv(name, err)
	}
	return v
}

func invalidEnv(name string, err error) {
	slog.Error("invalid config", "var", name, "error", err)
	os.Exit(2)
}

func envList(name string, def []string) []string {
	var out []string
	for s := range strings.SplitSeq(os.Getenv(name), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if out == nil {
		return def
	}
	return out
}
