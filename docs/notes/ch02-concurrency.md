# Chapter 2: Goroutines, worker pools, graceful shutdown

*Reconstructed from the code in `platform/concurrency`; the chapter text wasn't part of this conversation.*

## The idea
Concurrency in Go is cheap to start and easy to get wrong. A service needs bounded concurrency (a worker pool), a way to stop work (context cancellation), and a clean exit (graceful shutdown).

## Key points
- **A worker pool** means a fixed number of goroutines reading jobs from a buffered channel. It bounds how much work runs at once, unlike `go f()` per request.
- **Closing the jobs channel** is how workers learn there's no more work: `for job := range jobs` ends.
- **After the context is cancelled**, queued jobs that haven't started are skipped and counted rather than run. Jobs already running aren't interrupted; they must check `ctx` themselves.
- **A panic in one job must not kill the process.** Recover inside the worker and turn it into an error that includes the stack.
- **`context.WithoutCancel(ctx)`** keeps the request's values, such as trace IDs, but drops its cancellation. Use it for work that must outlive the request, like publishing an event. Chapter 7 replaced the original `concurrency.Detach` with `ctxutil.Detach(ctx, timeout)`, because detached work also needs its own deadline.
- **Graceful shutdown:** on SIGTERM, stop accepting connections, let requests already in progress finish within a deadline, then exit. `http.Server.Shutdown(ctx)` does this.

## Rules of thumb
- Every goroutine needs a known way to stop.
- Protect "send on channel" against "channel closed" with a mutex and a `closed` flag. Sending on a closed channel panics.
- `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` is the standard way to start shutdown.

## In this repo
`platform/concurrency/workerpool.go` and `shutdown.go`; the demo is `main.go` (`make run-pool-demo`).

## Check yourself
1. Why can't a worker just stop when the context is cancelled, without draining the channel?
2. What does `Detach` keep from the parent context, and what does it drop?
3. What happens to requests already in progress when `Shutdown` is called?
