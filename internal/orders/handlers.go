package orders

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"distributed/events"
	"distributed/platform/auth"
	"distributed/platform/concurrency"
	"distributed/platform/middleware"
)

// publisher is the part of messaging.Publisher the handlers need.
type publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// RoleAdmin may read every order and make any valid status change.
const RoleAdmin = "admin"

const (
	defaultPageSize = 20
	maxPageSize     = 100
	// IdempotencyKeyHeader lets a client retry POST /orders safely: the
	// same key from the same user returns the original order instead of
	// creating a second one.
	IdempotencyKeyHeader = "Idempotency-Key"
	maxIdempotencyKeyLen = 255
)

func internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.ErrorContext(r.Context(), msg, "error", err)
	middleware.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
}

func notFound(w http.ResponseWriter) {
	middleware.WriteError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
}

func isAdmin(c *auth.Claims) bool { return c.Role == RoleAdmin }

// loadVisibleOrder fetches an order the caller may see. Someone else's
// order is reported as not found, so its existence is not revealed.
func loadVisibleOrder(w http.ResponseWriter, r *http.Request, store Store) (Order, *auth.Claims, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		middleware.WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "not authenticated")
		return Order{}, nil, false
	}
	order, err := store.GetByID(r.Context(), r.PathValue("id"))
	if err == nil && order.UserID != claims.UserID() && !isAdmin(claims) {
		err = ErrNotFound
	}
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return Order{}, nil, false
	case err != nil:
		internalError(w, r, "failed to get order", err)
		return Order{}, nil, false
	}
	return order, claims, true
}

func getOrderHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if order, _, ok := loadVisibleOrder(w, r, store); ok {
			middleware.WriteJSON(w, http.StatusOK, order)
		}
	}
}

func historyHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		order, _, ok := loadVisibleOrder(w, r, store)
		if !ok {
			return
		}
		changes, err := store.History(r.Context(), order.ID)
		if err != nil {
			internalError(w, r, "failed to load history", err)
			return
		}
		if changes == nil {
			changes = []StatusChange{}
		}
		middleware.WriteJSON(w, http.StatusOK, map[string]any{"order_id": order.ID, "changes": changes})
	}
}

type listResponse struct {
	Orders []Order `json:"orders"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// listOrdersHandler returns the caller's own orders, newest first.
func listOrdersHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			middleware.WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "not authenticated")
			return
		}
		limit, err := queryInt(r, "limit", defaultPageSize, 1, maxPageSize)
		if err != nil {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
			return
		}
		offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
		if err != nil {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
			return
		}

		orders, err := store.ListByUser(r.Context(), claims.UserID(), limit, offset)
		if err != nil {
			internalError(w, r, "failed to list orders", err)
			return
		}
		if orders == nil {
			orders = []Order{}
		}
		middleware.WriteJSON(w, http.StatusOK, listResponse{Orders: orders, Limit: limit, Offset: offset})
	}
}

func queryInt(r *http.Request, name string, def, lo, hi int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, errors.New(name + " must be an integer from " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi))
	}
	return v, nil
}

type createOrderRequest struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func createOrderHandler(store Store, pub publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			middleware.WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "not authenticated")
			return
		}

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
		key := r.Header.Get(IdempotencyKeyHeader)
		if len(key) > maxIdempotencyKeyLen {
			middleware.WriteError(w, http.StatusBadRequest, "INVALID_HEADER", "Idempotency-Key is too long")
			return
		}

		// PostgreSQL stores microseconds; truncating here keeps the value
		// returned now equal to the one read back later.
		now := time.Now().UTC().Truncate(time.Microsecond)
		order := Order{
			ID:             newOrderID(),
			UserID:         claims.UserID(),
			ProductID:      req.ProductID,
			Quantity:       req.Quantity,
			Status:         StatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
			Version:        1,
			IdempotencyKey: key,
		}

		err := store.Create(r.Context(), order)
		if errors.Is(err, ErrIdempotencyKeyUsed) {
			replayCreate(w, r, store, claims.UserID(), key, req)
			return
		}
		if err != nil {
			internalError(w, r, "failed to create order", err)
			return
		}

		publishCreated(r.Context(), pub, order)

		w.Header().Set("Location", "/orders/"+order.ID)
		middleware.WriteJSON(w, http.StatusCreated, order)
	}
}

// replayCreate answers a retried POST /orders: the first request with this
// key already created the order, so return it instead of a duplicate. The
// unique constraint in the database decides who was first, so this is
// safe even when two retries arrive at the same moment.
func replayCreate(w http.ResponseWriter, r *http.Request, store Store, userID, key string, req createOrderRequest) {
	existing, err := store.GetByIdempotencyKey(r.Context(), userID, key)
	if err != nil {
		internalError(w, r, "failed to load order for idempotency key", err)
		return
	}
	if existing.ProductID != req.ProductID || existing.Quantity != req.Quantity {
		middleware.WriteError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"this Idempotency-Key was already used for a different request")
		return
	}
	w.Header().Set("Location", "/orders/"+existing.ID)
	w.Header().Set("Idempotent-Replayed", "true")
	middleware.WriteJSON(w, http.StatusOK, existing)
}

type updateStatusRequest struct {
	Status  string `json:"status"`
	Version int    `json:"version"`
}

// updateStatusHandler changes an order's status. The client sends the
// version it last saw; if the order changed since, it gets 409 and must
// re-read and decide again, instead of silently overwriting someone
// else's change.
func updateStatusHandler(store Store, pub publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		order, claims, ok := loadVisibleOrder(w, r, store)
		if !ok {
			return
		}

		var req updateStatusRequest
		if err := middleware.DecodeJSON(w, r, &req); err != nil {
			middleware.WriteDecodeError(w, err)
			return
		}
		if req.Version < 1 {
			middleware.WriteError(w, http.StatusBadRequest, "MISSING_FIELD", "version is required")
			return
		}
		if !isAdmin(claims) && req.Status != StatusCancelled {
			middleware.WriteError(w, http.StatusForbidden, "FORBIDDEN", "customers may only cancel orders")
			return
		}
		if req.Version != order.Version {
			versionConflict(w)
			return
		}
		if !CanTransition(order.Status, req.Status) {
			middleware.WriteError(w, http.StatusUnprocessableEntity, "INVALID_TRANSITION",
				"cannot change status from "+order.Status+" to "+req.Status)
			return
		}

		updated, err := store.UpdateStatus(r.Context(), order.ID, req.Status, req.Version, claims.UserID())
		switch {
		case errors.Is(err, ErrVersionConflict):
			versionConflict(w)
			return
		case errors.Is(err, ErrNotFound):
			notFound(w)
			return
		case err != nil:
			internalError(w, r, "failed to update status", err)
			return
		}

		publishStatusChanged(r.Context(), pub, order.Status, updated, claims.UserID())
		middleware.WriteJSON(w, http.StatusOK, updated)
	}
}

func versionConflict(w http.ResponseWriter) {
	middleware.WriteError(w, http.StatusConflict, "VERSION_CONFLICT",
		"the order was changed by someone else; fetch it again and retry")
}

// publishCreated announces a new order. The order is already stored, so a
// failed publish is logged but does not fail the request: with NATS Core
// the event is simply lost. Making "store + publish" atomic needs the
// outbox pattern.
func publishCreated(ctx context.Context, pub publisher, o Order) {
	publish(ctx, pub, events.SubjectOrderCreated, o.ID, events.OrderCreated{
		ID:        o.ID,
		UserID:    o.UserID,
		ProductID: o.ProductID,
		Quantity:  o.Quantity,
		CreatedAt: o.CreatedAt,
	})
}

func publishStatusChanged(ctx context.Context, pub publisher, from string, o Order, changedBy string) {
	publish(ctx, pub, events.SubjectOrderStatusChanged, o.ID, events.OrderStatusChanged{
		ID:        o.ID,
		UserID:    o.UserID,
		From:      from,
		To:        o.Status,
		Version:   o.Version,
		ChangedBy: changedBy,
		ChangedAt: o.UpdatedAt,
	})
}

func publish(ctx context.Context, pub publisher, subject, orderID string, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal event", "subject", subject, "order_id", orderID, "error", err)
		return
	}
	// Detached: the event must go out even if the client has disconnected.
	if err := pub.Publish(concurrency.Detach(ctx), subject, data); err != nil {
		slog.WarnContext(ctx, "failed to publish event", "subject", subject, "order_id", orderID, "error", err)
	}
}

func newOrderID() string {
	return "ord_" + strings.ToLower(rand.Text())
}
