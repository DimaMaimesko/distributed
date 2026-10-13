package orders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"distributed/platform/database/dbtest"
)

// Both stores must behave identically; the handler tests run on the
// memory store and trust that this holds.

func TestMemoryStore(t *testing.T) {
	testStore(t, func(t *testing.T) Store { return NewMemoryStore() })
}

func TestPostgresStore(t *testing.T) {
	testStore(t, func(t *testing.T) Store {
		pool, _ := dbtest.New(t, Migrations, MigrationsDir)
		return NewPostgresStore(pool)
	})
}

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func newTestOrder(id, user string, created time.Time) Order {
	return Order{
		ID: id, UserID: user, ProductID: "prod-1", Quantity: 1,
		Status: StatusPending, CreatedAt: created, UpdatedAt: created, Version: 1,
	}
}

func testStore(t *testing.T, newStore func(*testing.T) Store) {
	ctx := context.Background()

	t.Run("create and get", func(t *testing.T) {
		s := newStore(t)
		want := newTestOrder("ord-1", "user-1", base)
		if err := s.Create(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetByID(ctx, "ord-1")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("got  %+v\nwant %+v", got, want)
		}
		if _, err := s.GetByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing order: %v", err)
		}
	})

	t.Run("duplicate id", func(t *testing.T) {
		s := newStore(t)
		o := newTestOrder("ord-1", "user-1", base)
		s.Create(ctx, o)
		if err := s.Create(ctx, o); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("err = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("idempotency keys are per user", func(t *testing.T) {
		s := newStore(t)
		a := newTestOrder("ord-a", "user-1", base)
		a.IdempotencyKey = "key-1"
		if err := s.Create(ctx, a); err != nil {
			t.Fatal(err)
		}

		retry := newTestOrder("ord-b", "user-1", base)
		retry.IdempotencyKey = "key-1"
		if err := s.Create(ctx, retry); !errors.Is(err, ErrIdempotencyKeyUsed) {
			t.Errorf("same user, same key: %v", err)
		}

		other := newTestOrder("ord-c", "user-2", base)
		other.IdempotencyKey = "key-1"
		if err := s.Create(ctx, other); err != nil {
			t.Errorf("other user, same key: %v", err)
		}

		got, err := s.GetByIdempotencyKey(ctx, "user-1", "key-1")
		if err != nil || got.ID != "ord-a" {
			t.Errorf("GetByIdempotencyKey = %+v, %v", got, err)
		}
		if _, err := s.GetByIdempotencyKey(ctx, "user-3", "key-1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown key: %v", err)
		}

		// Orders without a key never collide.
		for i := range 2 {
			if err := s.Create(ctx, newTestOrder(fmt.Sprintf("ord-nokey-%d", i), "user-1", base)); err != nil {
				t.Errorf("order without key: %v", err)
			}
		}
	})

	t.Run("list by user", func(t *testing.T) {
		s := newStore(t)
		for i := range 5 {
			s.Create(ctx, newTestOrder(fmt.Sprintf("ord-%d", i), "user-1", base.Add(time.Duration(i)*time.Minute)))
		}
		s.Create(ctx, newTestOrder("ord-other", "user-2", base))

		page, err := s.ListByUser(ctx, "user-1", 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 2 || page[0].ID != "ord-4" || page[1].ID != "ord-3" {
			t.Errorf("first page = %v", ids(page))
		}
		page, _ = s.ListByUser(ctx, "user-1", 2, 4)
		if len(page) != 1 || page[0].ID != "ord-0" {
			t.Errorf("last page = %v", ids(page))
		}
		if page, _ := s.ListByUser(ctx, "user-1", 2, 10); len(page) != 0 {
			t.Errorf("past the end = %v", ids(page))
		}
	})

	t.Run("update status", func(t *testing.T) {
		s := newStore(t)
		s.Create(ctx, newTestOrder("ord-1", "user-1", base))

		updated, err := s.UpdateStatus(ctx, "ord-1", StatusPaid, 1, "admin-1")
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status != StatusPaid || updated.Version != 2 || !updated.UpdatedAt.After(base) {
			t.Errorf("updated = %+v", updated)
		}
		if got, _ := s.GetByID(ctx, "ord-1"); got != updated {
			t.Errorf("stored %+v, returned %+v", got, updated)
		}

		// A stale version is rejected and changes nothing.
		if _, err := s.UpdateStatus(ctx, "ord-1", StatusCancelled, 1, "user-1"); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("stale version: %v", err)
		}
		if _, err := s.UpdateStatus(ctx, "missing", StatusPaid, 1, "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing order: %v", err)
		}

		s.UpdateStatus(ctx, "ord-1", StatusShipped, 2, "admin-1")
		history, err := s.History(ctx, "ord-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 ||
			history[0].From != StatusPending || history[0].To != StatusPaid || history[0].ChangedBy != "admin-1" ||
			history[1].From != StatusPaid || history[1].To != StatusShipped {
			t.Errorf("history = %+v", history)
		}
		if _, err := s.History(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("history of missing order: %v", err)
		}
	})

	t.Run("concurrent updates: exactly one wins", func(t *testing.T) {
		s := newStore(t)
		s.Create(ctx, newTestOrder("ord-1", "user-1", base))

		var wg sync.WaitGroup
		var won, conflicts atomic.Int32
		for i := range 20 {
			wg.Go(func() {
				_, err := s.UpdateStatus(ctx, "ord-1", StatusCancelled, 1, fmt.Sprintf("user-%d", i))
				switch {
				case err == nil:
					won.Add(1)
				case errors.Is(err, ErrVersionConflict):
					conflicts.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			})
		}
		wg.Wait()

		if won.Load() != 1 || conflicts.Load() != 19 {
			t.Errorf("won=%d conflicts=%d, want 1 and 19", won.Load(), conflicts.Load())
		}
		if h, _ := s.History(ctx, "ord-1"); len(h) != 1 {
			t.Errorf("history has %d entries, want 1", len(h))
		}
	})
}

func ids(orders []Order) []string {
	var out []string
	for _, o := range orders {
		out = append(out, o.ID)
	}
	return out
}
