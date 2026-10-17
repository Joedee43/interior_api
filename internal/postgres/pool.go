// Package postgres owns the PostgreSQL connection pool. Domain packages
// (account, company, catalog, ...) receive a *pgxpool.Pool from cmd/api and
// write their own queries; this package does not hold business SQL.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOptions are deliberately small. Defaults suit local development;
// tune MaxConns against PostgreSQL's max_connections when deploying.
type PoolOptions struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

func DefaultPoolOptions() PoolOptions {
	return PoolOptions{
		MaxConns:        10,
		MinConns:        0,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 15 * time.Minute,
		ConnectTimeout:  5 * time.Second,
	}
}

// NewPool parses the URL, opens a pool and pings once so a bad URL or
// password fails at startup instead of on the first request.
//
// The returned pool is safe for concurrent use by many goroutines; create
// one per process and Close it on shutdown.
func NewPool(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Do not wrap err with the URL: it would contain the password.
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = opts.MaxConns
	cfg.MinConns = opts.MinConns
	cfg.MaxConnLifetime = opts.MaxConnLifetime
	cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
