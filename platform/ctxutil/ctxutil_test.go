package ctxutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestKey_IsTypedAndCollisionFree(t *testing.T) {
	a := NewKey[string]("tenant")
	b := NewKey[string]("tenant") // same name, different key
	ctx := a.With(context.Background(), "acme")

	if v, ok := a.Value(ctx); !ok || v != "acme" {
		t.Errorf("a = %q, %v", v, ok)
	}
	if _, ok := b.Value(ctx); ok {
		t.Error("a key with the same name read another key's value")
	}
	if _, ok := a.Value(context.Background()); ok {
		t.Error("empty context returned a value")
	}
}

func TestDetach(t *testing.T) {
	parent, cancel := context.WithCancel(WithRequestID(context.Background(), "req-1"))
	detached, cancelDetached := Detach(parent, 50*time.Millisecond)
	defer cancelDetached()

	cancel()
	if detached.Err() != nil {
		t.Fatal("detached context was cancelled with its parent")
	}
	if RequestID(detached) != "req-1" {
		t.Error("detached context lost the parent's values")
	}
	select {
	case <-detached.Done():
	case <-time.After(time.Second):
		t.Fatal("detached context has no deadline of its own")
	}
}

func TestSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Sleep ignored cancellation")
	}
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("uncancelled Sleep: %v", err)
	}
}

func TestDo(t *testing.T) {
	v, err := Do(context.Background(), func() (int, error) { return 42, nil })
	if v != 42 || err != nil {
		t.Errorf("got %d, %v", v, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	_, err = Do(ctx, func() (int, error) { <-release; return 0, nil })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %v; want to stop waiting at the deadline", err, time.Since(start))
	}
}

func TestLogHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewJSONHandler(&buf, nil)))
	ctx := WithUserID(WithRequestID(context.Background(), "req-1"), "usr-1")

	lines := func() []map[string]any {
		var out []map[string]any
		for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var m map[string]any
			json.Unmarshal([]byte(l), &m)
			out = append(out, m)
		}
		buf.Reset()
		return out
	}

	log.InfoContext(ctx, "deep in a repository", "order_id", "ord-1")
	if l := lines()[0]; l["request_id"] != "req-1" || l["user_id"] != "usr-1" || l["order_id"] != "ord-1" {
		t.Errorf("context IDs not added: %v", l)
	}

	// An explicit value wins and is not duplicated.
	log.InfoContext(ctx, "explicit", "request_id", "req-explicit")
	if raw := buf.String(); strings.Count(raw, "request_id") != 1 || !strings.Contains(raw, "req-explicit") {
		t.Errorf("duplicate or overridden request_id: %s", raw)
	}
	buf.Reset()

	// Loggers derived with With keep the behaviour.
	log.With("component", "store").InfoContext(ctx, "derived")
	if l := lines()[0]; l["request_id"] != "req-1" || l["component"] != "store" {
		t.Errorf("derived logger: %v", l)
	}

	// No context values: nothing added.
	log.Info("startup")
	if l := lines()[0]; l["request_id"] != nil {
		t.Errorf("request_id without a context: %v", l)
	}
}
