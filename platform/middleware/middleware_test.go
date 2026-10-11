package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func newRequest(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, target, body)
}

func decodeAPIError(t *testing.T, body io.Reader) APIError {
	t.Helper()
	var e APIError
	if err := json.NewDecoder(body).Decode(&e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return e
}

func TestChain_FirstIsOutermost(t *testing.T) {
	var trace []string
	layer := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, name+">")
				next.ServeHTTP(w, r)
				trace = append(trace, "<"+name)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		trace = append(trace, "handler")
	}), layer("a"), layer("b"))

	h.ServeHTTP(httptest.NewRecorder(), newRequest("GET", "/", nil))

	want := []string{"a>", "b>", "handler", "<b", "<a"}
	if !slices.Equal(trace, want) {
		t.Errorf("trace = %v, want %v", trace, want)
	}
}

func TestRecover_PanicBecomes500(t *testing.T) {
	var logs bytes.Buffer
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), RequestID(), Recover(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if e := decodeAPIError(t, rec.Body); e.Code != "INTERNAL_ERROR" {
		t.Errorf("code = %q, want INTERNAL_ERROR", e.Code)
	}
	if !strings.Contains(logs.String(), `"error":"boom"`) ||
		!strings.Contains(logs.String(), rec.Header().Get(RequestIDHeader)) {
		t.Errorf("log missing panic value or request ID: %s", logs.String())
	}
}

func TestRecover_AfterResponseStartedAbortsConnection(t *testing.T) {
	h := Recover(WithLogger(slog.New(slog.DiscardHandler)))(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("partial"))
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("Flush through wrapper: %v", err)
			}
			panic("boom")
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("body read succeeded; want an error from the aborted response")
	}
}

func TestRecover_PanicInsideTimeoutLogsOriginalStack(t *testing.T) {
	var logs bytes.Buffer
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("deep")
	}), Recover(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))), Timeout(time.Second))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "TestRecover_PanicInsideTimeoutLogsOriginalStack.func1") {
		t.Errorf("logged stack does not include the panicking handler:\n%s", logs.String())
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"generated when absent", "", false},
		{"incoming is reused", "upstream-123", true},
		{"invalid incoming is replaced", "has space", false},
		{"overlong incoming is replaced", strings.Repeat("a", 129), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fromCtx string
			h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fromCtx = RequestIDFromContext(r.Context())
			}))
			req := newRequest("GET", "/", nil)
			if tt.incoming != "" {
				req.Header.Set(RequestIDHeader, tt.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(RequestIDHeader)
			if got != fromCtx {
				t.Errorf("header %q != context %q", got, fromCtx)
			}
			if tt.keep && got != tt.incoming {
				t.Errorf("id = %q, want %q", got, tt.incoming)
			}
			if !tt.keep && !uuidRE.MatchString(got) {
				t.Errorf("id = %q, want a v4 UUID", got)
			}
		})
	}
}

func TestLogging_RecordsStatus(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"implicit 200", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }, 200},
		{"explicit status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }, 418},
		{"nothing written", func(http.ResponseWriter, *http.Request) {}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := Chain(tt.handler, RequestID(), Logging(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newRequest("GET", "/x", nil))

			var line struct {
				Status    int    `json:"status"`
				Path      string `json:"path"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatalf("parse log %q: %v", logs.String(), err)
			}
			if line.Status != tt.want || rec.Code != tt.want {
				t.Errorf("logged %d, sent %d, want %d", line.Status, rec.Code, tt.want)
			}
			if line.Path != "/x" || line.RequestID != rec.Header().Get(RequestIDHeader) {
				t.Errorf("log line = %+v", line)
			}
		})
	}
}

func TestTimeout_FastHandlerPassesThrough(t *testing.T) {
	h := Timeout(time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("handler context has no deadline")
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("made"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/", nil))

	if rec.Code != http.StatusCreated || rec.Body.String() != "made" || rec.Header().Get("X-Test") != "yes" {
		t.Errorf("got %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestTimeout_SlowHandlerGets504AndLateWritesFail(t *testing.T) {
	release := make(chan struct{})
	lateErr := make(chan error, 1)
	h := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // ignores ctx, like a badly behaved handler
		_, err := w.Write([]byte("too late"))
		lateErr <- err
	}))

	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, newRequest("GET", "/", nil))
	if d := time.Since(start); d > time.Second {
		t.Errorf("Timeout returned after %v", d)
	}
	close(release)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	if e := decodeAPIError(t, rec.Body); e.Code != "TIMEOUT" {
		t.Errorf("code = %q, want TIMEOUT", e.Code)
	}
	if err := <-lateErr; !errors.Is(err, http.ErrHandlerTimeout) {
		t.Errorf("late write err = %v, want ErrHandlerTimeout", err)
	}
}

func TestCORS(t *testing.T) {
	cors := CORS([]string{"https://app.example"})

	t.Run("preflight from allowed origin", func(t *testing.T) {
		called := false
		h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		req := newRequest("OPTIONS", "/orders", nil)
		req.Header.Set("Origin", "https://app.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if called {
			t.Error("preflight reached the handler")
		}
		if rec.Code != http.StatusNoContent ||
			rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" ||
			rec.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Errorf("got %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("preflight from other origin gets no CORS headers", func(t *testing.T) {
		called := false
		h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		req := newRequest("OPTIONS", "/orders", nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if !called || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("called=%v headers=%v", called, rec.Header())
		}
	})

	t.Run("actual request from allowed origin", func(t *testing.T) {
		h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := newRequest("GET", "/orders/1", nil)
		req.Header.Set("Origin", "https://app.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		hdr := rec.Header()
		if hdr.Get("Access-Control-Allow-Origin") != "https://app.example" ||
			!slices.Contains(hdr.Values("Vary"), "Origin") ||
			hdr.Get("Access-Control-Expose-Headers") != RequestIDHeader {
			t.Errorf("headers = %v", hdr)
		}
	})
}

func TestDecodeJSON(t *testing.T) {
	type target struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int // 0 means success
	}{
		{"valid", "application/json", `{"name":"a"}`, 0},
		{"charset parameter", "application/json; charset=utf-8", `{"name":"a"}`, 0},
		{"trailing whitespace", "application/json", "{\"name\":\"a\"}\n\n", 0},
		{"wrong content type", "text/plain", `{"name":"a"}`, http.StatusUnsupportedMediaType},
		{"missing content type", "", `{"name":"a"}`, http.StatusUnsupportedMediaType},
		{"unknown field", "application/json", `{"name":"a","priority":"high"}`, http.StatusBadRequest},
		{"trailing object", "application/json", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest},
		{"trailing garbage", "application/json", `{"name":"a"} x`, http.StatusBadRequest},
		{"malformed", "application/json", `{"name":`, http.StatusBadRequest},
		{"too large", "application/json", `{"name":"` + strings.Repeat("a", MaxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newRequest("POST", "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()

			var v target
			err := DecodeJSON(rec, req, &v)
			if tt.wantStatus == 0 {
				if err != nil || v.Name != "a" {
					t.Fatalf("err = %v, v = %+v", err, v)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
			WriteDecodeError(rec, err)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (err: %v)", rec.Code, tt.wantStatus, err)
			}
		})
	}
}

func TestReadinessHandler(t *testing.T) {
	ok := Check{Name: "db", Func: func(context.Context) error { return nil }}
	down := Check{Name: "nats", Func: func(context.Context) error { return errors.New("disconnected") }}

	tests := []struct {
		name        string
		checks      []Check
		wantStatus  int
		wantFailing []string
	}{
		{"no checks", nil, 200, nil},
		{"all pass", []Check{ok}, 200, nil},
		{"one fails", []Check{ok, down}, 503, []string{"nats"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ReadinessHandler(tt.checks...)(rec, newRequest("GET", "/healthz/ready", nil))

			var body readiness
			json.NewDecoder(rec.Body).Decode(&body)
			if rec.Code != tt.wantStatus || !slices.Equal(body.Failing, tt.wantFailing) {
				t.Errorf("got %d %+v", rec.Code, body)
			}
		})
	}
}
