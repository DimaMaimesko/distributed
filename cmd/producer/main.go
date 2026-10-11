// Command producer publishes an OrderCreated event every two seconds.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"distributed/events"
	"distributed/platform/messaging"
	natsmsg "distributed/platform/messaging/nats"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	natsURL := os.Getenv("NATS_URL")
	nc, err := natsmsg.Connect(natsURL, "producer", logger)
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	pub := natsmsg.NewPublisher(nc)
	defer pub.Close()

	logger.Info("producer started", "nats_url", nc.ConnectedUrl())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run(ctx, logger, pub, 2*time.Second)
	logger.Info("producer shutting down")
}

func run(ctx context.Context, logger *slog.Logger, pub messaging.Publisher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	orderNum := 1
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			event := events.OrderCreated{
				ID:        fmt.Sprintf("order-%04d", orderNum),
				ProductID: "prod-001",
				Quantity:  orderNum%5 + 1,
				CreatedAt: time.Now().UTC(),
			}

			data, err := json.Marshal(event)
			if err != nil {
				logger.Error("failed to marshal event", "error", err)
				continue
			}

			if err := pub.Publish(ctx, events.SubjectOrderCreated, data); err != nil {
				logger.Error("failed to publish", "error", err)
				continue
			}

			logger.Info("published order", "order_id", event.ID, "quantity", event.Quantity)
			orderNum++
		}
	}
}
