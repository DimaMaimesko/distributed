package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// Recover turns a panic in any layer below it into a logged error and a
// 500 response. If the response had already started, a 500 can no longer
// be sent, so the connection is aborted instead and the client sees a
// broken response rather than a truncated one that looks complete.
func Recover(opts ...Option) Middleware {
	o := newOptions(opts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := wrap(w)
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				stack := debug.Stack()
				if ps, ok := p.(*panicWithStack); ok {
					p, stack = ps.value, ps.stack
				}
				if p == http.ErrAbortHandler {
					panic(p) // deliberate abort; net/http handles it quietly
				}

				o.log().ErrorContext(r.Context(), "panic recovered",
					"error", p,
					"stack", string(stack),
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", RequestIDFromContext(r.Context()),
				)

				if rw.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				w.Header().Del("Content-Length")
				WriteError(rw, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// panicWithStack carries a panic from another goroutine (see Timeout)
// together with that goroutine's stack, so Recover logs the line that
// actually panicked rather than the line that re-panicked.
type panicWithStack struct {
	value any
	stack []byte
}

func (p *panicWithStack) Error() string {
	return fmt.Sprintf("%v\n%s", p.value, p.stack)
}
