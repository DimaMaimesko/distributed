# Chapter 4: HTTP services and middleware chains

## The idea
A framework is someone else's middleware. With Go 1.22+ `net/http` (method and path patterns, `r.PathValue`), a composable chain of `func(http.Handler) http.Handler` gives you everything a framework does for most services.

## Key points
- **Middleware** wraps `next.ServeHTTP`: code before it runs on the way in, code after it on the way out.
- **`Chain(h, a, b, c)`** makes `a` the outermost layer.
- **Order is behaviour, not style.** Ours is `RequestID → Logging → CORS → Recover → Timeout → BodyClose`:
  - RequestID comes first, so even the panic log has the ID.
  - Logging sits outside Recover and Timeout, so it logs the 500 and 504 they write.
  - CORS sits outside them too, so browsers can read those errors.
- **Liveness vs readiness:**
  - Liveness ("am I alive?") checks no dependencies; failing it means a restart.
  - Readiness ("can I serve now?") checks dependencies; failing it takes the instance out of load balancing without restarting it.
- **One error shape everywhere:** `{"code","message"}`. Log internal details; never send them to the client.
- **Server timeouts** (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) protect against slow clients. A handler timeout protects against slow dependencies. You need both, and `WriteTimeout` must be longer than the handler timeout.

## Rules of thumb
- Handlers with dependencies are constructors: `getOrderHandler(store) http.HandlerFunc`.
- Map domain errors to status codes with `switch { case errors.Is(...) }`.
- Apply auth **per route**, so a new route is public only if you register it without auth.
- Reject unknown JSON fields (`DisallowUnknownFields`) and limit body size.

## Traps (bugs in the book's code)
- **Timeout middleware race:** the book's handler goroutine and the 504 path both write to the same `ResponseWriter`. The fix is to buffer the handler's output, and only the winner writes, like the standard library's `http.TimeoutHandler`.
- **The book's chain order** loses the request ID in panic logs, logs a timeout as 200, and strips CORS headers from error responses.
- **Our own Timeout middleware**, found in Chapter 7: it reported a client disconnect as a 504, because its context ends both on its deadline and when the parent is cancelled. Use `context.WithTimeoutCause` to tell them apart.
- **Recover after headers are sent** can't send a 500. Abort the connection with `panic(http.ErrAbortHandler)`, so the client doesn't take a truncated response as complete.
- **`DecodeJSON`:**
  - `Content-Type != "application/json"` rejects `application/json; charset=utf-8`; parse it with `mime.ParseMediaType`.
  - `dec.More()` misses trailing garbage; decode a second time and expect `io.EOF`.
- **`BodyClose` with an unbounded `io.Copy(io.Discard, body)`** lets a client keep your handler busy. Bound it. `net/http` already does a bounded drain itself.
- **CORS:** add `Vary: Origin`, and only short-circuit real preflights (OPTIONS with `Access-Control-Request-Method`).
- **The book's handler test** served the handler without the router, so `r.PathValue("id")` was empty. Test through the real routes.

## In this repo
`platform/middleware`; routes are in `internal/orders/server.go`.

## Check yourself
1. Why must Logging wrap Timeout, not the other way round?
2. Liveness checks the database. The database goes down. What does Kubernetes do, and why is that bad?
3. What's the difference between `WriteTimeout` and the Timeout middleware?
