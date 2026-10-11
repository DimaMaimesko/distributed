package orders

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"distributed/events"
	"distributed/platform/middleware"
)

type published struct {
	subject string
	data    []byte
}

type fakePublisher struct {
	mu   sync.Mutex
	msgs []published
	err  error
}

func (p *fakePublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, published{subject, data})
	return nil
}

func newTestServer(t *testing.T, store Store, pub publisher, ready ...middleware.Check) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewServer(Config{HTTPTimeout: time.Second}, store, pub, ready...).Handler)
	t.Cleanup(srv.Close)
	return srv
}

func postOrder(t *testing.T, srv *httptest.Server, contentType, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/orders", contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestGetOrderHandler(t *testing.T) {
	store := NewMemoryStore()
	store.Create(context.Background(), Order{ID: "order-001", ProductID: "prod-001", Quantity: 2, Status: "pending"})
	srv := newTestServer(t, store, &fakePublisher{})

	resp, err := http.Get(srv.URL + "/orders/order-001")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var order Order
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if order.ID != "order-001" {
		t.Errorf("ID: got %q, want order-001", order.ID)
	}
}

func TestGetOrderHandler_NotFound(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore(), &fakePublisher{})

	resp, err := http.Get(srv.URL + "/orders/nonexistent")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
	var apiErr middleware.APIError
	json.NewDecoder(resp.Body).Decode(&apiErr)
	if apiErr.Code != "ORDER_NOT_FOUND" {
		t.Errorf("code: got %q, want ORDER_NOT_FOUND", apiErr.Code)
	}
}

func TestCreateOrder_StoresAndPublishes(t *testing.T) {
	store := NewMemoryStore()
	pub := &fakePublisher{}
	srv := newTestServer(t, store, pub)

	resp := postOrder(t, srv, "application/json", `{"product_id":"prod-001","quantity":3}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	var created Order
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := resp.Header.Get("Location"), "/orders/"+created.ID; got != want {
		t.Errorf("Location: got %q, want %q", got, want)
	}
	if _, err := store.GetByID(context.Background(), created.ID); err != nil {
		t.Errorf("order not stored: %v", err)
	}

	if len(pub.msgs) != 1 || pub.msgs[0].subject != events.SubjectOrderCreated {
		t.Fatalf("published = %+v, want one %s", pub.msgs, events.SubjectOrderCreated)
	}
	var ev events.OrderCreated
	json.Unmarshal(pub.msgs[0].data, &ev)
	if ev.ID != created.ID || ev.Quantity != 3 || ev.ProductID != "prod-001" {
		t.Errorf("event = %+v, order = %+v", ev, created)
	}
}

func TestCreateOrder_PublishFailureStillCreates(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore(), &fakePublisher{err: errors.New("nats down")})

	resp := postOrder(t, srv, "application/json", `{"product_id":"prod-001","quantity":1}`)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
}

func TestCreateOrder_Invalid(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{"missing product", "application/json", `{"quantity":1}`, 400, "MISSING_FIELD"},
		{"zero quantity", "application/json", `{"product_id":"p","quantity":0}`, 400, "INVALID_FIELD"},
		{"unknown field", "application/json", `{"product_id":"p","quantity":1,"priority":"high"}`, 400, "INVALID_REQUEST"},
		{"not JSON", "text/plain", `product_id=p`, 415, "UNSUPPORTED_MEDIA_TYPE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &fakePublisher{}
			srv := newTestServer(t, NewMemoryStore(), pub)

			resp := postOrder(t, srv, tt.contentType, tt.body)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status: got %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			var apiErr middleware.APIError
			json.NewDecoder(resp.Body).Decode(&apiErr)
			if apiErr.Code != tt.wantCode {
				t.Errorf("code: got %q, want %q", apiErr.Code, tt.wantCode)
			}
			if len(pub.msgs) != 0 {
				t.Errorf("published %d events for an invalid request", len(pub.msgs))
			}
		})
	}
}

func TestHealth(t *testing.T) {
	down := middleware.Check{Name: "nats", Func: func(context.Context) error { return errors.New("down") }}
	srv := newTestServer(t, NewMemoryStore(), &fakePublisher{}, down)

	for path, want := range map[string]int{
		"/healthz/live":  http.StatusOK,
		"/healthz/ready": http.StatusServiceUnavailable,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: got %d, want %d", path, resp.StatusCode, want)
		}
	}
}
