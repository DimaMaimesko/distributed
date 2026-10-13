// Command consumer logs every OrderCreated event it receives.
//
// Set QUEUE_GROUP to run several consumers that share the load: each
// message then goes to exactly one member of the group.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"distributed/events"
	"distributed/platform/ctxutil"
	"distributed/platform/messaging"
	natsmsg "distributed/platform/messaging/nats"
)

func main() {
	logger := slog.New(ctxutil.LogHandler(slog.NewJSONHandler(os.Stdout, nil)))

	natsURL := os.Getenv("NATS_URL")
	nc, err := natsmsg.Connect(natsURL, "consumer", logger)
	if err != nil {
		logger.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var opts []natsmsg.Option
	queueGroup := os.Getenv("QUEUE_GROUP")
	if queueGroup != "" {
		opts = append(opts, natsmsg.WithQueueGroup(queueGroup))
	}
	sub := natsmsg.NewSubscriber(nc, opts...)
	defer sub.Close()

	msgs, err := sub.Subscribe(ctx, events.SubjectOrderCreated)
	if err != nil {
		logger.Error("failed to subscribe", "error", err)
		os.Exit(1)
	}

	logger.Info("consumer started, waiting for orders", "queue_group", queueGroup)
	run(ctx, logger, msgs)
	logger.Info("consumer shutting down")
}

func run(ctx context.Context, logger *slog.Logger, msgs <-chan messaging.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}

			var event events.OrderCreated
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				logger.Error("failed to unmarshal event", "error", err)
				continue
			}

			logger.Info("received order",
				"order_id", event.ID,
				"user_id", event.UserID,
				"product_id", event.ProductID,
				"quantity", event.Quantity,
				"age_ms", time.Since(event.CreatedAt).Milliseconds(),
			)

			if err := msg.Ack(); err != nil {
				logger.Error("failed to ack", "error", err)
			}
		}
	}
}
