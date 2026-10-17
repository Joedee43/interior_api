// Package pgtest gives integration tests a migrated PostgreSQL pool.
//
// It uses TEST_DATABASE_URL and skips the test when that is unset. Tests
// share one database, so each test must create uniquely-named data (see
// Unique) instead of truncating tables, which would break tests running
// in parallel in other packages.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

// New returns a pool connected to the test database with all migrations
// applied. The pool is closed automatically when the test ends.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	migrateOnce.Do(func() { migrateErr = migrate(ctx, pool) })
	if migrateErr != nil {
		t.Fatalf("migrate test database: %v", migrateErr)
	}
	return pool
}

// migrate applies ./migrations. The Postgres session lock stops two test
// packages (which `go test ./...` runs in parallel processes) from
// migrating at the same time.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(migrationsDir()),
		goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// migrationsDir finds <module root>/migrations relative to this source
// file, so tests work no matter which package directory they run from.
func migrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}

// Unique returns a short random suffix for test data, e.g. "a1b2c3d4".
func Unique() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
