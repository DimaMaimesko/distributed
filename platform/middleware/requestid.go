package middleware

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"

	"distributed/platform/ctxutil"
)

// RequestIDHeader carries the request ID in both directions.
const RequestIDHeader = "X-Request-ID"

// RequestID gives every request an ID, stored in the context and echoed
// in the response header. An incoming X-Request-ID (from a gateway or an
// upstream service) is reused so log lines correlate across services.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID(id) {
				id = newRequestID()
			}

			ctx := ctxutil.WithRequestID(r.Context(), id)
			w.Header().Set(RequestIDHeader, id)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext returns the request ID, or "" if there is none.
// It is ctxutil.RequestID, kept here for callers that already import
// middleware.
func RequestIDFromContext(ctx context.Context) string {
	return ctxutil.RequestID(ctx)
}

// validRequestID accepts short, printable, space-free IDs. Anything else
// is replaced, so clients cannot push junk into every log line.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// newRequestID returns a random (version 4) UUID.
func newRequestID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
