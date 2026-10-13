// Package dbtest gives each test its own throwaway PostgreSQL database.
//
// Tests using it are skipped unless TEST_DATABASE_URL points at a server
// where the user may CREATE DATABASE, e.g. the one `make postgres` starts:
//
//	TEST_DATABASE_URL=postgres://orders:orders@localhost:5434/postgres?sslmode=disable
package dbtest

import (
	"context"
	"crypto/rand"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"distributed/platform/database"
)

const envVar = "TEST_DATABASE_URL"

// AdminURL returns TEST_DATABASE_URL, or skips the test if it is unset.
func AdminURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(envVar)
	if u == "" {
		t.Skipf("%s not set; run `make test-db` for database tests", envVar)
	}
	return u
}

// New creates an empty database, applies migrations (dir inside fsys)
// and returns a pool plus the database URL. The database is dropped when
// the test ends. Pass a nil fsys for an empty database.
func New(t testing.TB, fsys fs.FS, dir string) (*pgxpool.Pool, string) {
	t.Helper()
	admin := AdminURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminConn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to %s: %v", envVar, err)
	}
	defer adminConn.Close(ctx)

	name := "test_" + strings.ToLower(rand.Text())
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dbURL := withDatabase(t, admin, name)
	if fsys != nil {
		if err := database.RunMigrations(dbURL, fsys, dir); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	pool, err := database.NewDB(ctx, dbURL, database.WithAppName("dbtest"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return pool, dbURL
}

func withDatabase(t testing.TB, rawURL, name string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", envVar, err)
	}
	u.Path = "/" + name
	return u.String()
}
