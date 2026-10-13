package ctxutil

import (
	"context"
	"log/slog"
)

// LogHandler wraps a slog.Handler so that every record logged with a
// context (slog.InfoContext(ctx, ...)) carries request_id and user_id from
// that context. A repository three layers below the HTTP handler gets
// correlated logs without knowing anything about HTTP.
//
//	slog.SetDefault(slog.New(ctxutil.LogHandler(slog.NewJSONHandler(os.Stdout, nil))))
//
// Values travel down the call tree, never up: a log line written by an
// outer layer cannot see a value an inner layer added to its own context.
func LogHandler(next slog.Handler) slog.Handler {
	return logHandler{next: next}
}

type logHandler struct {
	next slog.Handler
}

func (h logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil {
		return h.next.Handle(ctx, r)
	}
	var add []slog.Attr
	if id := RequestID(ctx); id != "" && !hasAttr(r, "request_id") {
		add = append(add, slog.String("request_id", id))
	}
	if id := UserID(ctx); id != "" && !hasAttr(r, "user_id") {
		add = append(add, slog.String("user_id", id))
	}
	if len(add) > 0 {
		// Records share attribute storage between copies; clone before
		// modifying.
		r = r.Clone()
		r.AddAttrs(add...)
	}
	return h.next.Handle(ctx, r)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{next: h.next.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{next: h.next.WithGroup(name)}
}

// hasAttr reports whether the record already has key at the top level, so
// a caller that logs request_id explicitly does not get it twice.
func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
