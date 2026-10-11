package middleware

import (
	"io"
	"net/http"
)

// maxDrain bounds how much unread body BodyClose discards. A client that
// sends more than this to a handler that ignored the body is not worth
// reading; the connection is closed instead of reused.
const maxDrain = 256 << 10

// BodyClose drains and closes the request body after the handler returns,
// so the keep-alive connection can be reused.
//
// net/http's server already does a bounded drain and close on its own;
// this middleware makes the behaviour explicit and independent of the
// server. Unlike io.Copy(io.Discard, r.Body), it is bounded, so a client
// cannot keep the handler busy by streaming a huge body.
func BodyClose() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if r.Body != nil {
					io.CopyN(io.Discard, r.Body, maxDrain)
					r.Body.Close()
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
