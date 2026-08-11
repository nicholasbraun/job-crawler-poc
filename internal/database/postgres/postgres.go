// Package postgres implements the crawl_definition, crawl_run, job_listing,
// company, and career_page repositories over a PostgreSQL database using pgx.
// It also embeds and applies the schema migrations (see migrate.go). Postgres
// holds the durable crawl-management state, the extracted job listings, and the
// discovery Catalog (companies and their career pages); the transient frontier
// and visited-URL set live in Redis.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultURL is the local Postgres DSN every binary falls back to when
// DATABASE_URL is unset or empty, so the Doctor and the bench tooling target the
// same Catalog the server migrates without extra configuration.
const DefaultURL = "postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable"

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
