package database_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"distributed/platform/database"
	"distributed/platform/database/dbtest"
)

var testMigrations = fstest.MapFS{
	"m/000001_accounts.up.sql": {Data: []byte(`
		CREATE TABLE accounts (
			id      INTEGER PRIMARY KEY,
			email   TEXT NOT NULL CONSTRAINT accounts_email_key UNIQUE,
			balance INTEGER NOT NULL CHECK (balance >= 0)
		);`)},
	"m/000001_accounts.down.sql": {Data: []byte(`DROP TABLE accounts;`)},
	"m/000002_transfers.up.sql": {Data: []byte(`
		CREATE TABLE transfers (
			id         INTEGER PRIMARY KEY,
			account_id INTEGER NOT NULL REFERENCES accounts (id)
		);`)},
	"m/000002_transfers.down.sql": {Data: []byte(`DROP TABLE transfers;`)},
}

func TestErrorHelpers(t *testing.T) {
	pool, _ := dbtest.New(t, testMigrations, "m")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO accounts VALUES (1, 'a@x', 10)`); err != nil {
		t.Fatal(err)
	}

	err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE id = 99`).Scan(new(int))
	if !database.IsNotFound(err) {
		t.Errorf("no rows: IsNotFound = false for %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO accounts VALUES (2, 'a@x', 10)`)
	if !database.IsDuplicate(err) || database.ConstraintName(err) != "accounts_email_key" {
		t.Errorf("duplicate email: IsDuplicate=%v constraint=%q", database.IsDuplicate(err), database.ConstraintName(err))
	}

	_, err = pool.Exec(ctx, `INSERT INTO accounts VALUES (3, 'b@x', -1)`)
	if !database.IsCheckViolation(err) {
		t.Errorf("negative balance: IsCheckViolation = false for %v", err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO transfers VALUES (1, 42)`)
	if !database.IsForeignKeyViolation(err) {
		t.Errorf("missing account: IsForeignKeyViolation = false for %v", err)
	}

	if database.IsDuplicate(errors.New("plain")) || database.ConstraintName(nil) != "" {
		t.Error("helpers matched a non-PostgreSQL error")
	}
}

func TestWithTx(t *testing.T) {
	pool, _ := dbtest.New(t, testMigrations, "m")
	ctx := context.Background()
	count := func() int {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&n)
		return n
	}
	insert := func(tx pgx.Tx, id int) error {
		_, err := tx.Exec(ctx, `INSERT INTO accounts VALUES ($1, $2, 0)`, id, fmt.Sprintf("user%d@x", id))
		return err
	}

	err := database.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := insert(tx, 1); err != nil {
			return err
		}
		return insert(tx, 2)
	})
	if err != nil || count() != 2 {
		t.Fatalf("commit: err=%v count=%d", err, count())
	}

	boom := errors.New("boom")
	err = database.WithTx(ctx, pool, func(tx pgx.Tx) error {
		insert(tx, 3)
		return boom
	})
	if !errors.Is(err, boom) || count() != 2 {
		t.Errorf("rollback on error: err=%v count=%d", err, count())
	}

	func() {
		defer func() { recover() }()
		database.WithTx(ctx, pool, func(tx pgx.Tx) error {
			insert(tx, 4)
			panic("boom")
		})
	}()
	if count() != 2 {
		t.Errorf("rollback on panic: count=%d", count())
	}
	if s := pool.Stat(); s.AcquiredConns() != 0 {
		t.Errorf("%d connections still acquired after transactions: leak", s.AcquiredConns())
	}
}

func TestMigrations_UpDownUp(t *testing.T) {
	pool, dbURL := dbtest.New(t, nil, "")
	ctx := context.Background()
	tables := func() int {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('accounts', 'transfers')`).Scan(&n)
		return n
	}

	if err := database.RunMigrations(dbURL, testMigrations, "m"); err != nil {
		t.Fatal(err)
	}
	if tables() != 2 {
		t.Fatalf("after up: %d tables", tables())
	}
	// Running again is a no-op, not an error.
	if err := database.RunMigrations(dbURL, testMigrations, "m"); err != nil {
		t.Errorf("second up: %v", err)
	}
	// Down migrations must actually undo the up migrations.
	if err := database.MigrateDown(dbURL, testMigrations, "m"); err != nil || tables() != 0 {
		t.Fatalf("down: err=%v tables=%d", err, tables())
	}
	if err := database.RunMigrations(dbURL, testMigrations, "m"); err != nil || tables() != 2 {
		t.Errorf("up again: err=%v tables=%d", err, tables())
	}
}

func TestRunMigrations_RejectsNonPostgresURL(t *testing.T) {
	err := database.RunMigrations("mysql://localhost/x", testMigrations, "m")
	if err == nil {
		t.Fatal("accepted a mysql:// URL")
	}
}

func TestConnectWithRetry_GivesUpOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Port 1 refuses connections immediately.
	_, err := database.ConnectWithRetry(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err == nil {
		t.Fatal("connected to nothing")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v to give up after the context was cancelled", d)
	}
}

func TestNewDB_PoolSettings(t *testing.T) {
	_, dbURL := dbtest.New(t, nil, "")
	pool, err := database.NewDB(context.Background(), dbURL,
		database.WithAppName("pool-test"), database.WithMaxConns(3))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if pool.Config().MaxConns != 3 {
		t.Errorf("MaxConns = %d", pool.Config().MaxConns)
	}
	var app string
	pool.QueryRow(context.Background(), `SELECT current_setting('application_name')`).Scan(&app)
	if app != "pool-test" {
		t.Errorf("application_name = %q", app)
	}
	if err := database.Check(pool)(context.Background()); err != nil {
		t.Errorf("Check: %v", err)
	}
}
