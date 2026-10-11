package middleware

import (
	"bytes"
	"context"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// Timeout gives each request a deadline. If the handler has not finished
// when it expires, the client gets a 504 immediately, even if the handler
// ignores its context.
//
// The handler runs in its own goroutine and writes into a buffer; only
// the winner of "handler finished" vs "deadline expired" writes to the
// real ResponseWriter, so the two never race. A handler that ignores ctx
// keeps running in the background until it returns, and its writes after
// the deadline fail with http.ErrHandlerTimeout. Because the response is
// buffered, streaming (Flush) is not supported below this middleware.
// Panics in the handler are re-raised here for Recover to catch.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			tw := &timeoutWriter{header: make(http.Header)}
			done := make(chan struct{})
			panicChan := make(chan any, 1)

			go func() {
				defer func() {
					if p := recover(); p != nil {
						panicChan <- &panicWithStack{value: p, stack: debug.Stack()}
					}
				}()
				next.ServeHTTP(tw, r.WithContext(ctx))
				close(done)
			}()

			select {
			case p := <-panicChan:
				panic(p)
			case <-done:
				tw.mu.Lock()
				defer tw.mu.Unlock()
				dst := w.Header()
				for k, v := range tw.header {
					dst[k] = v
				}
				if !tw.wroteHeader {
					tw.status = http.StatusOK
				}
				w.WriteHeader(tw.status)
				w.Write(tw.buf.Bytes())
			case <-ctx.Done():
				tw.mu.Lock()
				defer tw.mu.Unlock()
				tw.timedOut = true
				WriteError(w, http.StatusGatewayTimeout, "TIMEOUT", "request timed out")
			}
		})
	}
}

// timeoutWriter buffers the handler's response until Timeout decides
// whether to send it.
type timeoutWriter struct {
	header http.Header // only touched by the handler goroutine until done

	mu          sync.Mutex
	buf         bytes.Buffer
	status      int
	wroteHeader bool
	timedOut    bool
}

func (tw *timeoutWriter) Header() http.Header { return tw.header }

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut || tw.wroteHeader {
		return
	}
	tw.status = code
	tw.wroteHeader = true
}

func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	if !tw.wroteHeader {
		tw.status = http.StatusOK
		tw.wroteHeader = true
	}
	return tw.buf.Write(b)
}
