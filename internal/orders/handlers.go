package orders

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"distributed/events"
	"distributed/platform/concurrency"
	"distributed/platform/middleware"
)

// publisher is the part of messaging.Publisher the handlers need.
type publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

func getOrderHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			middleware.WriteError(w, http.StatusBadRequest, "MISSING_ID", "order ID is required")
			return
		}

		order, err := store.GetByID(r.Context(), id)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				middleware.WriteError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
			default:
				slog.ErrorContext(r.Context(), "failed to get order", "order_id", id, "error", err)
				middleware.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}
			return
		}

		middleware.WriteJSON(w, http.StatusOK, order)
	}
}

type createOrderRequest struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func createOrderHandler(store Store, pub publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createOrderRequest
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}
		if req.ProductID == "" {
			middleware.WriteError(w, http.StatusBadRequest, "MISSING_FIELD", "product_id is required")
			return
		}
		if req.Quantity < 1 {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_FIELD", "quantity must be >= 1")
			return
		}

		order := Order{
			ID:        newOrderID(),
			ProductID: req.ProductID,
			Quantity:  req.Quantity,
			Status:    "pending",
			CreatedAt: time.Now().UTC(),
		}
		if err := store.Create(r.Context(), order); err != nil {
			slog.ErrorContext(r.Context(), "failed to create order", "error", err)
			middleware.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			return
		}

		publishCreated(r.Context(), pub, order)

		w.Header().Set("Location", "/orders/"+order.ID)
		middleware.WriteJSON(w, http.StatusCreated, order)
	}
}

// publishCreated announces a new order. The order is already stored, so a
// failed publish is logged but does not fail the request: with NATS Core
// the event is simply lost. Making "store + publish" atomic needs the
// outbox pattern.
func publishCreated(ctx context.Context, pub publisher, o Order) {
	data, err := json.Marshal(events.OrderCreated{
		ID:        o.ID,
		ProductID: o.ProductID,
		Quantity:  o.Quantity,
		CreatedAt: o.CreatedAt,
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal event", "order_id", o.ID, "error", err)
		return
	}
	// Detached: the event must go out even if the client has disconnected.
	if err := pub.Publish(concurrency.Detach(ctx), events.SubjectOrderCreated, data); err != nil {
		slog.WarnContext(ctx, "failed to publish order created", "order_id", o.ID, "error", err)
	}
}

func newOrderID() string {
	return "ord_" + strings.ToLower(rand.Text())
}
