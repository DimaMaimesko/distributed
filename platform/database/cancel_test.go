package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"distributed/platform/database"
	"distributed/platform/database/dbtest"
)

// activeSleeps counts other sessions in this database still running
// pg_sleep, as the server sees them.
func activeSleeps(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid()
		  AND state = 'active' AND query LIKE 'SELECT pg_sleep%'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitFor polls cond for up to 3s.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

func TestCancellationStopsTheQueryOnTheServer(t *testing.T) {
	pool, _ := dbtest.New(t, nil, "")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := pool.Exec(ctx, `SELECT pg_sleep(30)`)

	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("query returned %v after %v; want a prompt error", err, time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v; want it to wrap context.DeadlineExceeded", err)
	}
	// The important part: not just the Go side giving up, the server
	// actually stopped the query.
	if !waitFor(func() bool { return activeSleeps(t, pool) == 0 }) {
		t.Errorf("pg_sleep still running on the server after the context was cancelled")
	}
}

func TestWithTx_RollbackSurvivesCancellation(t *testing.T) {
	_, dbURL := dbtest.New(t, testMigrations, "m")
	pool, err := database.NewDB(context.Background(), dbURL, database.WithMaxConns(1))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.Exec(context.Background(), `SELECT 1`) // open the one connection
	before := pool.Stat().NewConnsCount()

	ctx, cancel := context.WithCancel(context.Background())
	err = database.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO accounts VALUES (1, 'a@x', 0)`); err != nil {
			return err
		}
		cancel() // the client goes away mid-transaction
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM accounts`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows committed by a cancelled transaction", n)
	}
	// The ROLLBACK reached the server, so the connection went back to the
	// pool clean and was reused rather than thrown away.
	if got := pool.Stat().NewConnsCount(); got != before {
		t.Errorf("pool opened %d new connections; the rolled-back one was discarded", got-before)
	}
}
