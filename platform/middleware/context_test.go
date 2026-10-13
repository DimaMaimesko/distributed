package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureLogs points slog's default logger at a buffer for one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestWriteInternalError(t *testing.T) {
	t.Run("genuine failure", func(t *testing.T) {
		logs := captureLogs(t)
		rec := httptest.NewRecorder()
		WriteInternalError(rec, httptest.NewRequest("GET", "/", nil), "save failed", errors.New("disk full"))

		if rec.Code != 500 || !strings.Contains(logs.String(), `"level":"ERROR"`) {
			t.Errorf("status %d, logs %s", rec.Code, logs)
		}
		if strings.Contains(rec.Body.String(), "disk full") {
			t.Error("internal error detail leaked to the client")
		}
	})

	t.Run("cancelled request", func(t *testing.T) {
		logs := captureLogs(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		WriteInternalError(rec, httptest.NewRequest("GET", "/", nil).WithContext(ctx), "save failed", context.Canceled)

		if rec.Code != StatusClientClosedRequest || rec.Body.Len() != 0 {
			t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
		}
		if strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "request cancelled") {
			t.Errorf("logs = %s", logs)
		}
	})
}

func TestTimeout_SetsCause(t *testing.T) {
	cause := make(chan error, 1)
	h := Timeout(10 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		cause <- context.Cause(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if err := <-cause; !errors.Is(err, ErrRequestTimeout) {
		t.Errorf("cause = %v, want ErrRequestTimeout", err)
	}
}

func TestReadiness_ChecksRunConcurrently(t *testing.T) {
	slow := func(ctx context.Context) error {
		select {
		case <-time.After(300 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h := ReadinessHandler(Check{"a", slow}, Check{"b", slow}, Check{"c", slow})

	start := time.Now()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/healthz/ready", nil))

	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Errorf("three 300ms checks took %v; they ran one after another", d)
	}
}

func TestTimeout_ClientDisconnectIsNotATimeout(t *testing.T) {
	h := Timeout(time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client is already gone
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil).WithContext(ctx))

	if rec.Code != StatusClientClosedRequest {
		t.Errorf("status = %d, want 499 rather than a 504 timeout", rec.Code)
	}
}
