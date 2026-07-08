package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestPool starts a throwaway PostgreSQL container, applies all migrations,
// and returns a connected pool. The container and pool are torn down via
// t.Cleanup. Requires a running Docker daemon; skips nothing — a missing
// daemon surfaces as a test failure so CI can't silently drop coverage.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	ctr, err := tcpostgres.Run(ctx, "postgres:17",
		tcpostgres.WithDatabase("crawler"),
		tcpostgres.WithUsername("crawler"),
		tcpostgres.WithPassword("crawler"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("error starting postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("error terminating postgres container: %v", err)
		}
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("error building connection string: %v", err)
	}

	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatalf("error applying migrations: %v", err)
	}

	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("error opening postgres pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}
