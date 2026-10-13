// Command dashboard serves a status page at http://localhost:8090 showing
// the health of each service, the NATS server and its clients, and events
// as they are published.
//
// Environment: PORT (default 8090), AUTH_URL (default
// http://localhost:8081), ORDERS_URL (default http://localhost:8080),
// NATS_URL, NATS_MONITOR_URL (default http://localhost:8222; NATS must run
// with -m 8222).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"distributed/internal/dashboard"
	"distributed/platform/concurrency"
	natsmsg "distributed/platform/messaging/nats"
)

// eventSubject catches every order event, not just orders.created.
const eventSubject = "orders.>"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := 8090
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid PORT", "error", err)
			os.Exit(2)
		}
		port = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events := dashboard.NewEventLog(50)

	// The dashboard is an ordinary subscriber without a queue group, so it
	// receives a copy of every event alongside the real consumers.
	nc, err := natsmsg.Connect(os.Getenv("NATS_URL"), "dashboard", logger, nats.RetryOnFailedConnect(true))
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	sub := natsmsg.NewSubscriber(nc)
	defer sub.Close()
	msgs, err := sub.Subscribe(ctx, eventSubject)
	if err != nil {
		logger.Error("failed to subscribe", "error", err)
		os.Exit(1)
	}
	go func() {
		for m := range msgs {
			events.Add(m.Subject, m.Data)
		}
	}()

	collector := dashboard.NewCollector(
		[]dashboard.Target{
			{Name: "auth", URL: envString("AUTH_URL", "http://localhost:8081")},
			{Name: "orders", URL: envString("ORDERS_URL", "http://localhost:8080")},
		},
		envString("NATS_MONITOR_URL", "http://localhost:8222"),
		events,
	)
	srv := dashboard.NewServer(dashboard.Config{Port: port}, collector)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("dashboard listening", "url", "http://localhost"+srv.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		logger.Error("server failed", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	if err := concurrency.GracefulShutdown(ctx, 10*time.Second, srv); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("dashboard stopped")
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
