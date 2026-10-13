package database

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes, see
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	codeUniqueViolation      = "23505"
	codeForeignKeyViolation  = "23503"
	codeCheckViolation       = "23514"
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
)

// IsNotFound reports whether a single-row query found no row.
func IsNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// IsDuplicate reports whether a unique constraint was violated.
// ConstraintName tells you which one.
func IsDuplicate(err error) bool { return hasCode(err, codeUniqueViolation) }

// IsForeignKeyViolation reports whether a referenced row is missing.
func IsForeignKeyViolation(err error) bool { return hasCode(err, codeForeignKeyViolation) }

// IsCheckViolation reports whether a CHECK constraint rejected the data.
func IsCheckViolation(err error) bool { return hasCode(err, codeCheckViolation) }

// IsRetryable reports whether the transaction failed because of
// concurrency (serialization failure or deadlock) and may succeed if
// retried from the start.
func IsRetryable(err error) bool {
	return hasCode(err, codeSerializationFailure) || hasCode(err, codeDeadlockDetected)
}

// ConstraintName returns the name of the violated constraint, or "".
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
