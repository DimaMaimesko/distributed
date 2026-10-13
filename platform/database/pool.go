// Package database is the PostgreSQL toolkit every service uses: a tuned
// pgx connection pool, connect-with-retry for startup races, embedded
// schema migrations, transactions, readiness checks and helpers that turn
// PostgreSQL error codes into questions like "was this a duplicate?".
package database

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"distributed/platform/ctxutil"
)

type options struct {
	appName  string
	maxConns int32
}

type Option func(*options)

// WithAppName sets application_name, which shows up in pg_stat_activity.
// With many services on one server, it is how you find out who holds a
// long-running connection or a lock.
func WithAppName(name string) Option {
	return func(o *options) { o.appName = name }
}

// WithMaxConns overrides the pool size. Remember the server-wide budget:
// services × instances × MaxConns must stay below max_connections.
func WithMaxConns(n int32) Option {
	return func(o *options) { o.maxConns = n }
}

// defaultMaxConns follows the 2 × CPU guideline, with a floor of 4.
func defaultMaxConns() int32 {
	return int32(max(4, 2*runtime.GOMAXPROCS(0)))
}

// NewDB creates a connection pool and checks that the database answers.
func NewDB(ctx context.Context, connString string, opts ...Option) (*pgxpool.Pool, error) {
	o := options{maxConns: defaultMaxConns()}
	for _, opt := range opts {
		opt(&o)
	}

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	config.MaxConns = o.maxConns
	config.MinConns = min(2, o.maxConns)
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute
	config.HealthCheckPeriod = time.Minute
	if o.appName != "" {
		config.ConnConfig.RuntimeParams["application_name"] = o.appName
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		// The pool already runs background goroutines; close it or they leak.
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// Check returns a readiness check that pings the database.
func Check(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		return pool.Ping(ctx)
	}
}

// WithTx runs fn in a transaction. It commits if fn returns nil and rolls
// back otherwise, including when fn panics.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// After a successful Commit this is a no-op. Detached, because the
	// most common reason to roll back is that ctx was cancelled: with a
	// cancelled ctx the ROLLBACK is never sent, and the pool has to throw
	// the connection away instead of reusing it.
	defer func() {
		rbCtx, cancel := ctxutil.Detach(ctx, 5*time.Second)
		defer cancel()
		tx.Rollback(rbCtx)
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
