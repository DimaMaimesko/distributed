package concurrency

import "context"

// Detach returns a context that keeps parent's values but is never
// cancelled when parent is. Use it for work that must outlive a request
// (audit logs, async notifications) while preserving trace IDs etc.
func Detach(parent context.Context) context.Context {
	return context.WithoutCancel(parent)
}
