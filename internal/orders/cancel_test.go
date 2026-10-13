package orders

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"distributed/platform/database/dbtest"
)

// captureLogs points slog's default logger at a buffer for one test.
func captureLogs(t *testing.T) *safeBuffer {
	t.Helper()
	buf := &safeBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// safeBuffer collects log output written from the server's goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// blockingStore blocks Create until the request's context is cancelled,
// like a database call stuck behind a slow query.
type blockingStore struct {
	*MemoryStore
	sawCancel chan error
}

func (s *blockingStore) Create(ctx context.Context, o Order) error {
	<-ctx.Done()
	s.sawCancel <- ctx.Err()
	return ctx.Err()
}

func clientThatGivesUp(after time.Duration) *http.Client {
	return &http.Client{Timeout: after}
}

func TestClientDisconnect_CancelsTheWork(t *testing.T) {
	logs := captureLogs(t)
	store := &blockingStore{MemoryStore: NewMemoryStore(), sawCancel: make(chan error, 1)}
	pub := &fakePublisher{}
	srv := newTestServer(t, store, pub)

	req, _ := http.NewRequest("POST", srv.URL+"/orders", strings.NewReader(`{"product_id":"p1","quantity":1}`))
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, "user-001", "customer"))
	req.Header.Set("Content-Type", "application/json")
	if _, err := clientThatGivesUp(100 * time.Millisecond).Do(req); err == nil {
		t.Fatal("request succeeded; the store should still have been blocked")
	}

	// The store saw the cancellation: the work stopped, it did not run to
	// completion for a client that was gone.
	select {
	case err := <-store.sawCancel:
		if err != context.Canceled {
			t.Errorf("store saw %v, want context.Canceled (client disconnect)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store never saw the client's disconnect")
	}
	srv.Close() // wait for the handler to finish before reading logs

	if len(pub.msgs) != 0 {
		t.Errorf("published an event for an order that was never created")
	}
	out := logs.String()
	if strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("client disconnect logged as an error:\n%s", out)
	}
	if !strings.Contains(out, `"status":499`) {
		t.Errorf("access log does not record 499:\n%s", out)
	}
}

func TestClientDisconnect_ReleasesTheDatabase(t *testing.T) {
	pool, _ := dbtest.New(t, Migrations, MigrationsDir)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Create(ctx, newTestOrder("ord-1", "user-001", base)); err != nil {
		t.Fatal(err)
	}

	// Another transaction holds the row lock, so the request's UPDATE has
	// to wait: the "slow downstream" from the chapter.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM orders WHERE id = 'ord-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	lockWaiters := func() int {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n)
		return n
	}

	srv := newTestServer(t, store, &fakePublisher{})
	req, _ := http.NewRequest("PATCH", srv.URL+"/orders/ord-1", strings.NewReader(`{"status":"cancelled","version":1}`))
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, "user-001", "customer"))
	req.Header.Set("Content-Type", "application/json")

	done := make(chan error, 1)
	go func() {
		_, err := clientThatGivesUp(500 * time.Millisecond).Do(req)
		done <- err
	}()

	// While the client waits, PostgreSQL shows the UPDATE waiting on the lock.
	if !eventually(func() bool { return lockWaiters() == 1 }) {
		t.Fatal("the UPDATE never started waiting on the lock")
	}
	if err := <-done; err == nil {
		t.Fatal("request succeeded despite the lock")
	}
	// The client is gone: the cancellation must reach PostgreSQL and end
	// the wait, freeing the connection and the backend.
	if !eventually(func() bool { return lockWaiters() == 0 }) {
		t.Error("the UPDATE is still waiting on the server after the client disconnected")
	}

	// And nothing changed: the order is still pending at version 1.
	blocker.Rollback(ctx)
	if o, _ := store.GetByID(ctx, "ord-1"); o.Status != StatusPending || o.Version != 1 {
		t.Errorf("order = %+v", o)
	}
}

func eventually(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
