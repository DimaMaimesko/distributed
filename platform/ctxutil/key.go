// Package ctxutil holds the small helpers that keep context.Context flowing
// correctly through every layer: typed keys for request-scoped values,
// the request and user IDs every log line should carry, detaching work
// that must outlive a request, and a slog handler that adds those IDs to
// logs automatically.
//
// The rule it supports: every function that does I/O takes ctx as its
// first parameter and passes it on. context.Background() belongs only in
// main, tests and initialization, the roots of context trees.
package ctxutil

import "context"

// Key is a typed context key. The value type is part of the key, so code
// that stores a value and code that reads it cannot disagree about its
// type, and each key is a distinct pointer, so two packages using the same
// name never collide.
//
//	var tenantKey = ctxutil.NewKey[string]("tenant")
//	ctx = tenantKey.With(ctx, "acme")
//	tenant, ok := tenantKey.Value(ctx)
//
// Store only immutable, request-scoped metadata: who is calling, which
// request this is. Not dependencies (pass those explicitly) and not
// mutable state shared between goroutines.
type Key[T any] struct {
	name string
}

func NewKey[T any](name string) *Key[T] {
	return &Key[T]{name: name}
}

// With returns a copy of ctx carrying v.
func (k *Key[T]) With(ctx context.Context, v T) context.Context {
	return context.WithValue(ctx, k, v)
}

// Value returns the value stored under k, if any.
func (k *Key[T]) Value(ctx context.Context) (T, bool) {
	v, ok := ctx.Value(k).(T)
	return v, ok
}

func (k *Key[T]) String() string { return k.name }
