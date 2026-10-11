package concurrency

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// closeWithin fails the test if Close does not return within d.
func closeWithin(t *testing.T, wp *WorkerPool, d time.Duration) []error {
	t.Helper()
	done := make(chan []error, 1)
	go func() { done <- wp.Close() }()
	select {
	case errs := <-done:
		return errs
	case <-time.After(d):
		t.Fatal("Close did not return: deadlock")
		return nil
	}
}

func TestWorkerPool_ManyJobsNoDeadlock(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 2)

	var ran atomic.Int64
	for i := 0; i < 1000; i++ {
		if err := wp.Submit(func() error {
			ran.Add(1)
			return errors.New("fail")
		}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	errs := closeWithin(t, wp, 5*time.Second)
	if got := ran.Load(); got != 1000 {
		t.Errorf("ran %d jobs, want 1000", got)
	}
	if len(errs) != 1000 {
		t.Errorf("got %d errors, want 1000", len(errs))
	}
}

func TestWorkerPool_CollectsOnlyErrors(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 3)
	errBoom := errors.New("boom")

	for i := 0; i < 10; i++ {
		wp.Submit(func() error {
			if i%5 == 0 {
				return errBoom
			}
			return nil
		})
	}

	errs := closeWithin(t, wp, time.Second)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(errs), errs)
	}
	for _, err := range errs {
		if !errors.Is(err, errBoom) {
			t.Errorf("unexpected error %v", err)
		}
	}
}

func TestWorkerPool_BoundedConcurrency(t *testing.T) {
	const workers = 4
	wp := NewWorkerPool(context.Background(), workers)

	var active, peak atomic.Int64
	for i := 0; i < 50; i++ {
		wp.Submit(func() error {
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return nil
		})
	}
	closeWithin(t, wp, 5*time.Second)

	if p := peak.Load(); p > workers {
		t.Errorf("peak concurrency %d exceeds %d workers", p, workers)
	}
}

func TestWorkerPool_RecoversPanic(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 1)

	wp.Submit(func() error { panic("kaboom") })
	var after atomic.Bool
	wp.Submit(func() error { after.Store(true); return nil })

	errs := closeWithin(t, wp, time.Second)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "kaboom") {
		t.Fatalf("want one panic error, got %v", errs)
	}
	if !after.Load() {
		t.Error("worker died after panic; next job did not run")
	}
}

func TestWorkerPool_SubmitAfterClose(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 1)
	wp.Close()

	if err := wp.Submit(func() error { return nil }); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Submit after Close = %v, want ErrPoolClosed", err)
	}
}

func TestWorkerPool_CloseIdempotent(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 1)
	wp.Submit(func() error { return errors.New("x") })

	first := wp.Close()
	second := wp.Close()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("Close returned %v then %v", first, second)
	}
}

func TestWorkerPool_ContextCancelUnblocksSubmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wp := NewWorkerPool(ctx, 1)

	release := make(chan struct{})
	block := func() error { <-release; return nil }

	// 1 running + 2 queued fills the pool; the next Submit blocks.
	for i := 0; i < 3; i++ {
		if err := wp.Submit(block); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}

	submitErr := make(chan error, 1)
	go func() { submitErr <- wp.Submit(block) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-submitErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("blocked Submit = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit stayed blocked after ctx cancel")
	}

	if err := wp.Submit(block); !errors.Is(err, context.Canceled) {
		t.Errorf("Submit after cancel = %v, want context.Canceled", err)
	}

	close(release)
	errs := closeWithin(t, wp, time.Second)

	// The running job finishes; the 2 queued ones are skipped.
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) ||
		!strings.Contains(errs[0].Error(), "2 queued jobs skipped") {
		t.Fatalf("want one skipped-jobs error, got %v", errs)
	}
}

func TestWorkerPool_ConcurrentSubmitAndClose(t *testing.T) {
	wp := NewWorkerPool(context.Background(), 4)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				err := wp.Submit(func() error { return nil })
				if err != nil && !errors.Is(err, ErrPoolClosed) {
					t.Errorf("Submit: %v", err)
					return
				}
			}
		}()
	}

	time.Sleep(time.Millisecond)
	closeWithin(t, wp, 5*time.Second) // must not panic with "send on closed channel"
	wg.Wait()
}

func TestNewWorkerPool_PanicsOnZeroWorkers(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for 0 workers")
		}
	}()
	NewWorkerPool(context.Background(), 0)
}
