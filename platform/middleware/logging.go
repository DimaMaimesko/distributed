package middleware

import (
	"net/http"
	"time"
)

// Logging logs one line per request with its status and duration. Place
// it outside Recover and Timeout so it logs the 500 or 504 they write.
func Logging(opts ...Option) Middleware {
	o := newOptions(opts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := wrap(w)

			// Deferred so a request that aborts with a panic is still logged.
			defer func() {
				o.log().InfoContext(r.Context(), "request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rw.status,
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", RequestIDFromContext(r.Context()),
					"user_agent", r.UserAgent(),
					"remote_addr", r.RemoteAddr,
				)
			}()

			next.ServeHTTP(rw, r)
		})
	}
}
