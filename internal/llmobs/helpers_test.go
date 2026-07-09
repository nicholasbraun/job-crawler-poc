package llmobs_test

import (
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// newTestClient starts a throwaway Redis container and returns a connected
// client. The container and client are torn down via t.Cleanup. Requires a
// running Docker daemon; a missing daemon surfaces as a test failure so CI can't
// silently drop coverage.
func newTestClient(t *testing.T) *redis.Client {
	t.Helper()
	ctx := t.Context()

	ctr, err := tcredis.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("error starting redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("error terminating redis container: %v", err)
		}
	})

	connStr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("error building connection string: %v", err)
	}

	opts, err := redis.ParseURL(connStr)
	if err != nil {
		t.Fatalf("error parsing redis url %q: %v", connStr, err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("error pinging redis: %v", err)
	}

	return client
}
