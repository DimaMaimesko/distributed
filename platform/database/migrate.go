package database

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers pgx5://
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// RunMigrations applies every pending "up" migration in dir of migrations,
// usually an embed.FS compiled into the service, so the binary always
// carries the schema it expects.
//
// Files are named NNNNNN_description.up.sql / .down.sql. golang-migrate
// takes a PostgreSQL advisory lock, so several instances starting at once
// apply each migration exactly once.
func RunMigrations(databaseURL string, migrations fs.FS, dir string) error {
	m, err := newMigrate(databaseURL, migrations, dir)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		var dirty migrate.ErrDirty
		if errors.As(err, &dirty) {
			return fmt.Errorf("migration %d failed earlier and left the schema dirty; "+
				"fix it by hand, then run `migrate force %d`: %w", dirty.Version, dirty.Version-1, err)
		}
		return fmt.Errorf("run migrations: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("read migration version: %w", err)
	}
	slog.Info("migrations applied", "version", version, "dirty", dirty)
	return nil
}

// MigrateDown reverts every migration. Only for tests and local resets.
func MigrateDown(databaseURL string, migrations fs.FS, dir string) error {
	m, err := newMigrate(databaseURL, migrations, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

func newMigrate(databaseURL string, migrations fs.FS, dir string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations, dir)
	if err != nil {
		return nil, fmt.Errorf("open migrations: %w", err)
	}
	dbURL, err := pgx5URL(databaseURL)
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return m, nil
}

// pgx5URL rewrites a postgres:// URL to the pgx5:// scheme golang-migrate
// uses for its pgx v5 driver (its plain "postgres" driver uses lib/pq).
func pgx5URL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		u.Scheme = "pgx5"
	case "pgx5":
	default:
		return "", fmt.Errorf("database URL must start with postgres://, got %q", u.Scheme+"://")
	}
	return u.String(), nil
}
