package llmstream

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/redis/go-redis/v9"
)

// DeleteRun removes every stream a run staged (both kinds and their dead-letter
// streams, all under the llmstream:{runID}:* namespace). It mirrors
// redisfrontier.DeleteRun: a package function (there is no live Stage to hang it
// on at cleanup time) that uses SCAN, not KEYS, so it never blocks Redis on a
// large keyspace, and is a no-op for a run with no keys. Wired to the same
// terminal/factory-error lifecycle points as the frontier cleaner, so a paused
// run's streams survive for a resumed run to redeliver.
func DeleteRun(ctx context.Context, client *redis.Client, runID uuid.UUID) error {
	pattern := "llmstream:" + runID.String() + ":*"
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("llmstream: scanning keys to delete: %w", err)
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("llmstream: deleting run keys: %w", err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// Depth reports a run/kind stage's backlog and pending counts for observability.
// backlog is the total outstanding entries in the stream (XLEN — undelivered plus
// delivered-but-unacked, since acked entries are deleted); pending is the subset
// currently in the consumer group's pending list (delivered, in-flight or
// awaiting retry). A missing stream/group reports zero.
func Depth(ctx context.Context, client *redis.Client, runID uuid.UUID, kind llmobs.Kind) (backlog, pending int64, err error) {
	return streamDepth(ctx, client, streamKey(runID, kind), group)
}

// streamDepth is the shared backlog/pending measurement used by Depth, the drain
// loop, and the depth reporter.
func streamDepth(ctx context.Context, client *redis.Client, stream, grp string) (backlog, pending int64, err error) {
	backlog, err = client.XLen(ctx, stream).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("llmstream: measuring stream length: %w", err)
	}
	p, err := client.XPending(ctx, stream, grp).Result()
	if err != nil {
		// A stream that exists but has no group yet (nothing consumed) has nothing
		// pending; go-redis surfaces this as NOGROUP or a nil reply.
		if isNoGroup(err) || errors.Is(err, redis.Nil) {
			return backlog, 0, nil
		}
		return 0, 0, fmt.Errorf("llmstream: measuring pending: %w", err)
	}
	return backlog, p.Count, nil
}
