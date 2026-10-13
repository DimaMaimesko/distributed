package database

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"distributed/platform/ctxutil"
)

const (
	connectAttempts = 6
	initialBackoff  = 250 * time.Millisecond
	maxBackoff      = 5 * time.Second
)

// ConnectWithRetry calls NewDB until it succeeds, backing off
// exponentially (with jitter) between attempts. In an orchestrator the
// database often starts at the same time as the service; this rides out
// that race instead of crash-looping.
func ConnectWithRetry(ctx context.Context, connString string, opts ...Option) (*pgxpool.Pool, error) {
	backoff := initialBackoff
	var lastErr error

	for attempt := 1; attempt <= connectAttempts; attempt++ {
		pool, err := NewDB(ctx, connString, opts...)
		if err == nil {
			return pool, nil
		}
		lastErr = err
		if attempt == connectAttempts {
			break // no point sleeping before giving up
		}

		// Jitter spreads out retries from many instances starting together.
		wait := backoff/2 + rand.N(backoff/2+1)
		slog.WarnContext(ctx, "database connection failed, retrying",
			"attempt", attempt, "retry_in", wait.String(), "error", err)

		if err := ctxutil.Sleep(ctx, wait); err != nil {
			return nil, err
		}
		backoff = min(2*backoff, maxBackoff)
	}
	return nil, fmt.Errorf("database connection failed after %d attempts: %w", connectAttempts, lastErr)
}
