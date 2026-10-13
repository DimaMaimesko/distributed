# Chapter 7: Context propagation

## The idea
`context.Context` is the only way Go carries **cancellation, deadlines and request metadata** across a call stack. It works only if every layer passes it on. One `context.Background()` in the middle cuts the chain, and everything below it keeps working for a client that has already left. Under load that looks like a memory leak.

## Key points
- **The discipline:** every function that does I/O takes `ctx` first and passes it to every I/O call. `context.Background()` belongs only at the **roots**: `main`, tests, initialization.
- **Cancellation travels down.** When the client disconnects, `r.Context()` is cancelled, and so is every context derived from it. pgx then sends PostgreSQL a cancel request, so **the server stops the query** too, not just the Go code (we checked this in a test).
- **Deadlines are a budget.** Each layer inherits whatever time is left. If an early step is slow, later steps time out quickly instead of hanging. `context.WithTimeout(ctx, 500ms)` can only *shorten* the parent's deadline, never extend it.
- **Values are for request-scoped, immutable metadata:** who is calling, which request this is. Values travel **down**, never up: the access-log middleware can't see `user_id`, because auth adds it to a context further in.
- **`errgroup.WithContext`:** the first error cancels the shared context, so sibling goroutines stop. Use a plain `WaitGroup` when you want every result even if some fail (the dashboard's probes, readiness checks).
- **Detach with `context.WithoutCancel(ctx)`** for work that serves correctness rather than the caller: compensation, cleanup, an event for a write that already committed, a rollback. It keeps the values (request ID, user) but not the cancellation. **Always add your own timeout.**
- **`context.WithTimeoutCause` and `context.Cause`** say *why* a context ended: our deadline, or the client leaving.

## Rules of thumb
- If the client is gone, don't log ERROR or send 500. Check `ctx.Err() != nil` and record **499** (client closed request).
- **Shared work triggered by one request** (a cache refill, a key fetch) must not be cancelled by that request: other requests are waiting on it.
- Always `defer cancel()` after `WithCancel` or `WithTimeout`; a forgotten `cancel` leaks a timer until the parent ends.
- In long loops, check `ctx.Done()`, or use `ctxutil.Sleep(ctx, d)` instead of `time.Sleep` in retries.
- For a library without context support, `ctxutil.Do(ctx, fn)` stops *waiting*, but `fn` keeps running. It's a workaround, not a fix.

## Anti-patterns
- `context.Background()` inside a handler or any function that was given a ctx.
- Dependencies in context (`ctx.Value(dbKey)`): pass them in structs or signatures instead.
- Mutable state in context (`&Metrics{}`): it becomes a data race.
- A context stored in a struct field: structs outlive requests.

## Traps (what we found applying this to our code)
- **Our Timeout middleware reported a client disconnect as `504 TIMEOUT`.** Its context derives from the request's, so the client leaving also ends it. Fixed with `WithTimeoutCause`: a 504 only for our own deadline, otherwise 499. A test of the chapter's opening scenario found this.
- **The JWKS key refresh used the triggering request's context.** One impatient client could fail the fetch for everyone waiting. It now detaches.
- **`defer tx.Rollback(ctx)` with a cancelled ctx never sends `ROLLBACK`,** so the pool discards the connection. We now roll back on a detached context with a timeout. A test proves the connection gets reused, and fails with the old code.
- **The event publish detached with no timeout** (Chapter 2's `Detach`), so detached work could hang forever. It's replaced by `ctxutil.Detach(ctx, timeout)`.
- **Readiness checks ran one after another,** so one hanging dependency used up the others' budget. They now run concurrently on a shared deadline.
- **In the book, `Value[T](ctx, key)` lets every reader pick `T`.** Ask for the wrong type and you silently get nothing. We put the type in the key instead (`Key[T]`), so the compiler catches a mismatch.
- **The book wants all keys in one package,** but the claims key can't live in `ctxutil`: claims are an auth type, and `ctxutil` importing `auth` would create an import cycle. So auth also stores the plain `user_id` string in `ctxutil` for logs.
- **The book's Q&A cleanup example uses `context.Background()`,** which loses the request ID in cleanup logs. Its own detach section gets this right: `WithoutCancel` plus a timeout.

## In this repo
- `platform/ctxutil`: typed `Key[T]`, `RequestID` and `UserID`, `Detach`, `Sleep`, `Do`, `IsCancelled`, and `LogHandler`, which adds `request_id` and `user_id` to every `slog.*Context` call in every service.
- `middleware.WriteInternalError` (500 vs 499) and `middleware.ErrRequestTimeout`.
- Tests:
  - `internal/orders/cancel_test.go`: a client disconnect cancels the store call; nothing is published; no ERROR is logged; 499 is recorded. A row-lock wait **disappears from `pg_stat_activity`** once the client hangs up.
  - `platform/database/cancel_test.go`: the server really stops `pg_sleep(30)`; the rollback survives cancellation.

## Check yourself
1. A handler calls `store.Create(context.Background(), o)`. The client disconnects. What keeps running, and for how long?
2. The parent has 200ms left, and you call `WithTimeout(ctx, 500ms)`. What deadline do you get?
3. When should work detach from the request, and what must you add when it does?
4. Why can't the logging middleware log the `user_id` that auth puts in the context?
5. errgroup or WaitGroup for readiness checks? Why?
