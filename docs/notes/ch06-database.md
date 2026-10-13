# Chapter 6: Database access and migrations

## The idea
A shared database is the most common hidden coupling between services: one team's migration or slow query breaks another team. Each service owns its schema, accesses it through a repository, and changes it only through versioned migrations that are safe to run on a live database.

## Key points
- **Use pgx, not `lib/pq`,** which is archived. pgx gives native types, typed errors (`pgconn.PgError.Code`), pooling (`pgxpool`) and `COPY`.
- **Pool size:** about `2 × CPU cores` per instance. The real limit is server-wide: `services × instances × MaxConns < max_connections` (default 100). Over budget means a smaller pool or PgBouncer. PgBouncer's transaction mode breaks session features: prepared statements, advisory locks, `SET`.
- **Set `application_name`** so `pg_stat_activity` shows which service holds which connection or lock.
- **The repository pattern:** all SQL for a domain lives in one type. It turns driver errors into domain errors (`pgx.ErrNoRows` becomes `ErrNotFound`, unique violation `23505` becomes `ErrAlreadyExists`). Handlers never import pgx.
- **Always check `rows.Err()`** after a `rows.Next()` loop, or a connection error halfway through returns a partial result silently. `pgx.CollectRows` does this for you.
- **Transactions:** `defer tx.Rollback(ctx)` right after `Begin`; after Commit it does nothing. A **conditional update** (`UPDATE ... WHERE available - reserved >= $1`) is an atomic check-and-change with no race. **Savepoints** let part of a transaction fail without losing the rest.
- **Optimistic locking:** a `version` column, and `UPDATE ... WHERE id = $1 AND version = $2`. If zero rows changed, someone else won; return 409 and let the client re-read.
- **Constraints are the last line of defence** (NOT NULL, CHECK, UNIQUE, foreign keys). Application code has bugs; the database rejects bad data anyway.
- **A partial index** (`WHERE status NOT IN ('shipped','cancelled')`) indexes only the rows you actually query, so it's smaller and faster.
- **Migrations** are numbered `NNNNNN_name.up.sql` / `.down.sql` files in git. golang-migrate takes an advisory lock, so one instance migrates at a time. In development, migrate at startup; in production, migrate as its own step (a Kubernetes Job), after review and a backup.
- **Zero-downtime migrations:**
  - **Safe:** `CREATE TABLE`; `ADD COLUMN` that's nullable or has no default; `CREATE INDEX CONCURRENTLY`; adding a foreign key `NOT VALID`, then `VALIDATE CONSTRAINT` separately.
  - **Unsafe:** adding a NOT NULL column with no default; `CREATE INDEX` without `CONCURRENTLY`; `ALTER COLUMN TYPE`; `RENAME COLUMN`, which breaks running code at once.
  - **Expand-contract**, for a new NOT NULL column:
    1. Add it as nullable.
    2. Deploy code that writes it.
    3. Backfill it in batches.
    4. Add `NOT NULL`.

    Each step is its own migration.
- **One database (or schema) per service.** An HTTP call to another service is explicit coupling you can see, test and version; a cross-service JOIN is implicit coupling that breaks at 3am.
- **sqlc** generates type-safe Go from SQL, a good middle ground between hand-written SQL and an ORM. In Go, ORMs usually hide too much.

## Rules of thumb
- Retry the database connection at startup with backoff and jitter, then fail with the real error. The database and the service often start together.
- Readiness checks the database, which gets the instance taken out of rotation. Liveness doesn't, because a restart won't fix the database.
- **Watch for connection leaks** (rows or connections never released): `pool.Stat().AcquiredConns()` grows and never comes down. Always `defer rows.Close()`.
- **Truncate `time.Now()` to microseconds** before storing. PostgreSQL keeps microseconds, so otherwise the value you return differs from the one you read back later.
- **Scope uniqueness to the owner:** `UNIQUE (user_id, idempotency_key)`, not `UNIQUE (idempotency_key)`.

## Traps (bugs in the book's code)
- **`return fmt.Errorf("commit: %w", tx.Commit(ctx))` always returns an error, even when the commit succeeded**, because `fmt.Errorf` never returns nil. Check the error first.
- **`NewDB` leaked the pool when `Ping` failed.** The pool's background goroutines kept running. Call `pool.Close()` on that path.
- **`ConnectWithRetry` slept once more after the final attempt and threw away the last error.** Return the real cause, and add jitter so many instances don't retry in lockstep.
- **golang-migrate's `postgres` driver uses `lib/pq`,** the driver the chapter tells you to avoid. Use its `pgx5://` driver.
- **A globally unique `idempotency_key`** lets user A's key collide with user B's and return B's order to A. Scope it per user.
- **The inventory example** puts an `inventory` table in the orders database, which contradicts the chapter's own "one database per service" advice. It's fine as a SQL lesson, but in our design inventory would be its own service.
- **Migrating before connecting** skips the retry: if the database is still starting, migrations fail at once. Connect with retry first, then migrate.

## In this repo
- `platform/database`: pool, retry, migrations, `WithTx`, error helpers.
- `platform/database/dbtest`: a throwaway database per test.
- `internal/orders/postgres_store.go` and `internal/orders/migrations/`.
- `internal/orders/store_test.go`: one shared set of tests that checks the memory and Postgres stores behave identically, including 20 concurrent updates where exactly one wins.

Try `make postgres`, `make test-db`, `make psql`, then `SELECT application_name, state FROM pg_stat_activity;`.

## Check yourself
1. Ten services × 3 instances × `MaxConns` 10 against `max_connections` = 100. What are your options?
2. Write the four expand-contract migrations for a new `orders.shipped_at NOT NULL` column.
3. Two clients PATCH the same order with `version: 1` at the same moment. Walk through what each one gets.
4. Why does `defer tx.Rollback(ctx)` not undo a successful commit?
5. Why should production migrations be their own step rather than run at service startup?
