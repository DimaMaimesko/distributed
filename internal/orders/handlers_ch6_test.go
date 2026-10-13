package orders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"distributed/events"
	"distributed/platform/middleware"
)

func decodeBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func wantCode(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d", resp.StatusCode, status)
	}
	if code != "" {
		if e := decodeBody[middleware.APIError](t, resp); e.Code != code {
			t.Errorf("code = %q, want %q", e.Code, code)
		}
	}
}

// postWithKey creates an order as user-001 with an Idempotency-Key.
func postWithKey(t *testing.T, srv *httptest.Server, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/orders", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, "user-001", "customer"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(IdempotencyKeyHeader, key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestCreateOrder_IdempotencyKey(t *testing.T) {
	pub := &fakePublisher{}
	srv := newTestServer(t, NewMemoryStore(), pub)
	body := `{"product_id":"p1","quantity":2}`

	first := postWithKey(t, srv, "checkout-42", body)
	wantCode(t, first, http.StatusCreated, "")
	created := decodeBody[Order](t, first)

	// A network retry with the same key returns the same order...
	retry := postWithKey(t, srv, "checkout-42", body)
	wantCode(t, retry, http.StatusOK, "")
	if replayed := decodeBody[Order](t, retry); replayed.ID != created.ID {
		t.Errorf("retry created %s, original %s", replayed.ID, created.ID)
	}
	if retry.Header.Get("Idempotent-Replayed") != "true" {
		t.Error("retry not marked as replayed")
	}
	// ...and announces the order only once.
	if len(pub.msgs) != 1 {
		t.Errorf("published %d events, want 1", len(pub.msgs))
	}

	// The same key with a different body is a client bug, not a retry.
	wantCode(t, postWithKey(t, srv, "checkout-42", `{"product_id":"p1","quantity":5}`),
		http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED")

	// A new key creates a new order.
	wantCode(t, postWithKey(t, srv, "checkout-43", body), http.StatusCreated, "")
}

func TestListOrders(t *testing.T) {
	store := NewMemoryStore()
	for i, id := range []string{"a", "b", "c"} {
		store.Create(context.Background(), newTestOrder(id, "user-001", base.Add(time.Duration(i)*time.Hour)))
	}
	store.Create(context.Background(), newTestOrder("theirs", "user-002", base))
	srv := newTestServer(t, store, &fakePublisher{})
	tok := tokenFor(t, "user-001", "customer")

	resp := do(t, "GET", srv.URL+"/orders?limit=2", tok, "", "")
	wantCode(t, resp, 200, "")
	page := decodeBody[listResponse](t, resp)
	if got := ids(page.Orders); len(got) != 2 || got[0] != "c" || got[1] != "b" || page.Limit != 2 {
		t.Errorf("page = %v (limit %d)", got, page.Limit)
	}

	resp = do(t, "GET", srv.URL+"/orders?limit=2&offset=2", tok, "", "")
	if got := ids(decodeBody[listResponse](t, resp).Orders); len(got) != 1 || got[0] != "a" {
		t.Errorf("second page = %v", got)
	}

	// Another user's orders never appear; an empty list is [] not null.
	resp = do(t, "GET", srv.URL+"/orders", tokenFor(t, "user-003", "customer"), "", "")
	var raw map[string]json.RawMessage
	json.NewDecoder(resp.Body).Decode(&raw)
	if string(raw["orders"]) != "[]" {
		t.Errorf("empty list = %s", raw["orders"])
	}

	for _, q := range []string{"limit=0", "limit=101", "limit=x", "offset=-1"} {
		wantCode(t, do(t, "GET", srv.URL+"/orders?"+q, tok, "", ""), 400, "INVALID_QUERY")
	}
}

func TestUpdateStatus(t *testing.T) {
	newEnv := func(t *testing.T) (*httptest.Server, *fakePublisher) {
		store := NewMemoryStore()
		store.Create(context.Background(), newTestOrder("ord-1", "user-001", base))
		pub := &fakePublisher{}
		return newTestServer(t, store, pub), pub
	}
	patch := func(t *testing.T, srv *httptest.Server, token, body string) *http.Response {
		return do(t, "PATCH", srv.URL+"/orders/ord-1", token, "application/json", body)
	}
	owner := tokenFor(t, "user-001", "customer")
	admin := tokenFor(t, "admin-1", RoleAdmin)

	t.Run("owner cancels", func(t *testing.T) {
		srv, pub := newEnv(t)
		resp := patch(t, srv, owner, `{"status":"cancelled","version":1}`)
		wantCode(t, resp, 200, "")
		if o := decodeBody[Order](t, resp); o.Status != StatusCancelled || o.Version != 2 {
			t.Errorf("order = %+v", o)
		}
		if len(pub.msgs) != 1 || pub.msgs[0].subject != events.SubjectOrderStatusChanged {
			t.Fatalf("published = %+v", pub.msgs)
		}
		var ev events.OrderStatusChanged
		json.Unmarshal(pub.msgs[0].data, &ev)
		if ev.From != StatusPending || ev.To != StatusCancelled || ev.ChangedBy != "user-001" {
			t.Errorf("event = %+v", ev)
		}

		hist := do(t, "GET", srv.URL+"/orders/ord-1/history", owner, "", "")
		wantCode(t, hist, 200, "")
		var h struct{ Changes []StatusChange }
		json.NewDecoder(hist.Body).Decode(&h)
		if len(h.Changes) != 1 || h.Changes[0].To != StatusCancelled {
			t.Errorf("history = %+v", h.Changes)
		}
	})

	t.Run("stale version conflicts", func(t *testing.T) {
		srv, pub := newEnv(t)
		wantCode(t, patch(t, srv, admin, `{"status":"paid","version":1}`), 200, "")
		// The owner still holds version 1.
		wantCode(t, patch(t, srv, owner, `{"status":"cancelled","version":1}`), 409, "VERSION_CONFLICT")
		if len(pub.msgs) != 1 {
			t.Errorf("published %d events, want 1", len(pub.msgs))
		}
	})

	t.Run("admin follows the lifecycle", func(t *testing.T) {
		srv, _ := newEnv(t)
		wantCode(t, patch(t, srv, admin, `{"status":"paid","version":1}`), 200, "")
		wantCode(t, patch(t, srv, admin, `{"status":"shipped","version":2}`), 200, "")
		wantCode(t, patch(t, srv, admin, `{"status":"cancelled","version":3}`), 422, "INVALID_TRANSITION")
	})

	t.Run("rejected requests", func(t *testing.T) {
		srv, _ := newEnv(t)
		wantCode(t, patch(t, srv, owner, `{"status":"shipped","version":1}`), 403, "FORBIDDEN")
		wantCode(t, patch(t, srv, admin, `{"status":"pending","version":1}`), 422, "INVALID_TRANSITION")
		wantCode(t, patch(t, srv, admin, `{"status":"paid"}`), 400, "MISSING_FIELD")
		wantCode(t, patch(t, srv, tokenFor(t, "user-002", "customer"), `{"status":"cancelled","version":1}`), 404, "ORDER_NOT_FOUND")
		wantCode(t, patch(t, srv, tokenFor(t, "user-001", "customer", "orders:read"), `{"status":"cancelled","version":1}`), 403, "INSUFFICIENT_SCOPE")
	})
}
