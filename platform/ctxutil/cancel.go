package ctxutil

import (
	"context"
	"time"
)

// IsCancelled reports whether ctx is done: the client went away or the
// deadline passed. Handlers use it to tell "the request was abandoned"
// apart from a genuine failure.
func IsCancelled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// Detach returns a context for work that must finish even if the request
// is cancelled: compensation, cleanup, an event for a write that already
// committed. It keeps ctx's values (request ID, user, later trace ID) but
// not its cancellation, and adds its own timeout so the work can never
// hang forever. Always call the returned cancel.
//
// Do not detach work the caller is waiting for: if they have gone, that
// work should stop.
func Detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// Sleep waits for d, or returns ctx.Err() as soon as ctx is done. Use it
// in retry loops instead of time.Sleep, which cannot be interrupted.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do calls fn, which cannot be cancelled (a library without context
// support), but stops waiting for it when ctx is done.
//
// fn keeps running in the background until it returns; only the caller
// stops waiting. Prefer libraries that accept a context.
func Do[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	// Buffered, so fn's goroutine can exit even if nobody receives.
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()

	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
