package ctxutil

import "context"

// The IDs every log line about a request should carry. They live here,
// not in the packages that set them, so any layer (a repository, a
// publisher) can read them without importing HTTP or auth code.
var (
	requestIDKey = NewKey[string]("request_id")
	userIDKey    = NewKey[string]("user_id")
)

// WithRequestID stores the request ID. The RequestID middleware calls it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return requestIDKey.With(ctx, id)
}

// RequestID returns the request ID, or "".
func RequestID(ctx context.Context) string {
	id, _ := requestIDKey.Value(ctx)
	return id
}

// WithUserID stores the authenticated user's ID. auth.Authenticate calls it.
func WithUserID(ctx context.Context, id string) context.Context {
	return userIDKey.With(ctx, id)
}

// UserID returns the authenticated user's ID, or "".
func UserID(ctx context.Context) string {
	id, _ := userIDKey.Value(ctx)
	return id
}
