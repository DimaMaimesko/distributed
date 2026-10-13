-- One row per status change, written in the same transaction as the
-- change, so the history can never disagree with the order.
CREATE TABLE order_status_history (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id    TEXT NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    from_status TEXT NOT NULL,
    to_status   TEXT NOT NULL,
    changed_by  TEXT NOT NULL,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX order_status_history_order_idx ON order_status_history (order_id, id);
