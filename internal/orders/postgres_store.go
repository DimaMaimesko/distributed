package orders

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"distributed/platform/database"
)

// PostgresStore is the Store backed by PostgreSQL. All SQL for orders
// lives here; callers only ever see the domain errors from order.go.
type PostgresStore struct {
	db *pgxpool.Pool
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: db}
}

const orderColumns = `id, user_id, product_id, quantity, status, created_at, updated_at, version, COALESCE(idempotency_key, '')`

// scanOrder reads one row selected with orderColumns.
func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.UserID, &o.ProductID, &o.Quantity, &o.Status,
		&o.CreatedAt, &o.UpdatedAt, &o.Version, &o.IdempotencyKey)
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	return o, err
}

func (s *PostgresStore) Create(ctx context.Context, o Order) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO orders (id, user_id, product_id, quantity, status, idempotency_key, created_at, updated_at, version)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $7, 1)`,
		o.ID, o.UserID, o.ProductID, o.Quantity, o.Status, o.IdempotencyKey, o.CreatedAt,
	)
	if database.IsDuplicate(err) {
		if database.ConstraintName(err) == "orders_user_idempotency_key" {
			return ErrIdempotencyKeyUsed
		}
		return ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("create order: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetByID(ctx context.Context, id string) (Order, error) {
	o, err := scanOrder(s.db.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE id = $1`, id))
	if database.IsNotFound(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("get order %s: %w", id, err)
	}
	return o, nil
}

func (s *PostgresStore) GetByIdempotencyKey(ctx context.Context, userID, key string) (Order, error) {
	o, err := scanOrder(s.db.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE user_id = $1 AND idempotency_key = $2`, userID, key))
	if database.IsNotFound(err) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("get order by idempotency key: %w", err)
	}
	return o, nil
}

func (s *PostgresStore) ListByUser(ctx context.Context, userID string, limit, offset int) ([]Order, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+orderColumns+`
		 FROM orders
		 WHERE user_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list orders for user %s: %w", userID, err)
	}
	// pgx.CollectRows closes rows and checks rows.Err(), so a connection
	// error halfway through cannot silently return a partial list.
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Order, error) {
		return scanOrder(row)
	})
	if err != nil {
		return nil, fmt.Errorf("list orders for user %s: %w", userID, err)
	}
	return orders, nil
}

func (s *PostgresStore) UpdateStatus(ctx context.Context, id, status string, version int, changedBy string) (Order, error) {
	var updated Order
	err := database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		// Read the current status for the history row. Checking the version
		// here gives a precise error; the UPDATE below re-checks it, which
		// is what actually guards against a concurrent change.
		var from string
		var current int
		err := tx.QueryRow(ctx, `SELECT status, version FROM orders WHERE id = $1`, id).Scan(&from, &current)
		if database.IsNotFound(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read order: %w", err)
		}
		if current != version {
			return ErrVersionConflict
		}

		// Optimistic locking: the WHERE version = $3 makes this a no-op if
		// someone else updated the order after our read.
		updated, err = scanOrder(tx.QueryRow(ctx,
			`UPDATE orders SET status = $2, version = version + 1, updated_at = NOW()
			 WHERE id = $1 AND version = $3
			 RETURNING `+orderColumns,
			id, status, version))
		if database.IsNotFound(err) {
			return ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update status: %w", err)
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO order_status_history (order_id, from_status, to_status, changed_by, changed_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			id, from, status, changedBy, updated.UpdatedAt)
		if err != nil {
			return fmt.Errorf("record status history: %w", err)
		}
		return nil
	})
	if err != nil {
		return Order{}, err
	}
	return updated, nil
}

func (s *PostgresStore) History(ctx context.Context, id string) ([]StatusChange, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check order: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	rows, err := s.db.Query(ctx,
		`SELECT from_status, to_status, changed_by, changed_at
		 FROM order_status_history WHERE order_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("list status history: %w", err)
	}
	changes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StatusChange, error) {
		var c StatusChange
		err := row.Scan(&c.From, &c.To, &c.ChangedBy, &c.ChangedAt)
		c.ChangedAt = c.ChangedAt.UTC()
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("list status history: %w", err)
	}
	return changes, nil
}

var _ Store = (*PostgresStore)(nil)
var _ Store = (*MemoryStore)(nil)
