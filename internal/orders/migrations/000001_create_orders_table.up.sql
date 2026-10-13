CREATE TABLE orders (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    product_id      TEXT NOT NULL,
    quantity        INTEGER NOT NULL CHECK (quantity > 0),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    version         INTEGER NOT NULL DEFAULT 1,

    -- Unique per user, not globally: otherwise one user's key could
    -- collide with another's and return them someone else's order.
    -- NULL keys never conflict, so orders without a key are unaffected.
    CONSTRAINT orders_user_idempotency_key UNIQUE (user_id, idempotency_key)
);

-- "My orders, newest first": the index matches both the filter and the
-- sort, so PostgreSQL reads rows in order instead of sorting them.
CREATE INDEX orders_user_created_idx ON orders (user_id, created_at DESC);

-- Partial index: only orders still being worked on, which is what
-- status queries look for. Smaller and faster than indexing every row.
CREATE INDEX orders_open_status_idx ON orders (status)
    WHERE status NOT IN ('shipped', 'cancelled');
