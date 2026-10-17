package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests run against a real PostgreSQL database named by
// TEST_DATABASE_URL (e.g. the local interior_test database). They skip
// rather than fail when it is unset so `go test ./...` works anywhere.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return url
}

func TestNewPool_Connects(t *testing.T) {
	url := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, url, DefaultPoolOptions())
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}
}

func TestNewPool_InvalidURL(t *testing.T) {
	_, err := NewPool(context.Background(), "not a url ://", DefaultPoolOptions())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestNewPool_Unreachable(t *testing.T) {
	opts := DefaultPoolOptions()
	opts.ConnectTimeout = 500 * time.Millisecond

	// Port 1 is reserved and nothing listens there, so the ping must fail.
	_, err := NewPool(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable", opts)
	if err == nil {
		t.Fatal("expected error for unreachable database")
	}
}
