// Package middleware is the HTTP middleware chain shared by every service:
// recovery, request IDs, logging, timeouts, CORS, plus JSON error and
// health-check helpers. It depends only on the standard library.
package middleware

import (
	"log/slog"
	"net/http"
)

// Middleware wraps a handler, running code before and after it.
type Middleware func(http.Handler) http.Handler

// Chain wraps h so that middleware[0] is the outermost layer: a request
// passes through the middleware in argument order on the way in, and in
// reverse order on the way out.
func Chain(h http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// Option configures middleware that accept options.
type Option func(*options)

type options struct {
	logger *slog.Logger
}

// WithLogger sets the logger. The default is slog.Default() at the time
// each request is handled, so slog.SetDefault after building the chain
// still takes effect.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func (o options) log() *slog.Logger {
	if o.logger != nil {
		return o.logger
	}
	return slog.Default()
}
