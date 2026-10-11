package concurrency

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
)

// ErrPoolClosed is returned by Submit after Close has been called.
var ErrPoolClosed = errors.New("concurrency: worker pool is closed")

// WorkerPool runs submitted jobs on a fixed number of goroutines.
//
// Once ctx is cancelled, Submit stops accepting jobs and queued jobs that
// have not started yet are skipped; Close reports how many were skipped.
// Jobs that are already running are not interrupted — pass ctx into them
// if they should stop early.
type WorkerPool struct {
	ctx  context.Context
	jobs chan func() error
	wg   sync.WaitGroup

	// mu guards closed and the send on jobs, so Submit never sends on a
	// closed channel.
	mu     sync.RWMutex
	closed bool

	errMu   sync.Mutex
	errs    []error
	skipped int

	closeOnce sync.Once
}

// NewWorkerPool starts workers goroutines. It panics if workers < 1.
func NewWorkerPool(ctx context.Context, workers int) *WorkerPool {
	if workers < 1 {
		panic(fmt.Sprintf("concurrency: NewWorkerPool workers must be >= 1, got %d", workers))
	}

	wp := &WorkerPool{
		ctx:  ctx,
		jobs: make(chan func() error, workers*2),
	}

	for range workers {
		wp.wg.Go(func() {
			// Keep draining jobs even after ctx is cancelled, so Close
			// never waits on a full queue.
			for fn := range wp.jobs {
				if ctx.Err() != nil {
					wp.recordSkip()
					continue
				}
				if err := run(fn); err != nil {
					wp.recordErr(err)
				}
			}
		})
	}

	return wp
}

// Submit queues fn, blocking while the queue is full. It returns
// ErrPoolClosed after Close, or ctx.Err() if ctx is cancelled first.
func (wp *WorkerPool) Submit(fn func() error) error {
	wp.mu.RLock()
	defer wp.mu.RUnlock()

	if wp.closed {
		return ErrPoolClosed
	}
	if err := wp.ctx.Err(); err != nil {
		return err
	}

	select {
	case wp.jobs <- fn:
		return nil
	case <-wp.ctx.Done():
		return wp.ctx.Err()
	}
}

// Close stops accepting jobs, waits for queued jobs to finish and returns
// all non-nil errors they produced. Panics in jobs are returned as errors.
// Close is safe to call more than once; later calls return the same errors.
func (wp *WorkerPool) Close() []error {
	wp.closeOnce.Do(func() {
		wp.mu.Lock()
		wp.closed = true
		close(wp.jobs)
		wp.mu.Unlock()

		wp.wg.Wait()

		if wp.skipped > 0 {
			wp.errs = append(wp.errs,
				fmt.Errorf("concurrency: %d queued jobs skipped: %w", wp.skipped, context.Cause(wp.ctx)))
		}
	})
	return slices.Clone(wp.errs)
}

func (wp *WorkerPool) recordErr(err error) {
	wp.errMu.Lock()
	wp.errs = append(wp.errs, err)
	wp.errMu.Unlock()
}

func (wp *WorkerPool) recordSkip() {
	wp.errMu.Lock()
	wp.skipped++
	wp.errMu.Unlock()
}

// run executes fn, turning a panic into an error so one bad job cannot
// crash the process.
func run(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("concurrency: job panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}
