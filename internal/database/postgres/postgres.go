// Package postgres implements the crawl_definition, crawl_run, and job_listing
// repositories over a PostgreSQL database using pgx. It also embeds and applies
// the schema migrations (see migrate.go). SQLite still backs only the
// visited-URL set (moving to Redis in Step 3); Postgres holds the
// crawl-management state and the extracted job listings.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates a connection pool for the given DSN and verifies connectivity
// with a Ping. The caller owns the returned pool and must Close it.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: error creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: error pinging database: %w", err)
	}

	return pool, nil
}
