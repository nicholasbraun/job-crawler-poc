package llmstream_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmstream"
	"github.com/redis/go-redis/v9"
)

// eventually is the standard timeout for the durable behaviors, generous enough
// to absorb a couple of reclaim cycles at the fast minIdle below.
const eventually = 10 * time.Second

// fastOpts tunes a stage for tests: prompt reclaim/redelivery and short blocking
// reads, with depth sampling parked so it does not add noise.
func fastOpts() []llmstream.Option[task] {
	return []llmstream.Option[task]{
		llmstream.WithWorkers[task](2),
		llmstream.WithMinIdle[task](100 * time.Millisecond),
		llmstream.WithBlockDuration[task](100 * time.Millisecond),
		llmstream.WithDrainPoll[task](20 * time.Millisecond),
		llmstream.WithDepthInterval[task](time.Hour),
	}
}

// depthIsZero polls Depth for a run/kind and reports whether the stream has fully
// drained (no backlog, no pending).
func depthIsZero(t *testing.T, client *redis.Client, runID uuid.UUID, kind llmobs.Kind) func() bool {
	return func() bool {
		backlog, pending, err := llmstream.Depth(t.Context(), client, runID, kind)
		if err != nil {
			t.Fatalf("Depth: %v", err)
		}
		return backlog == 0 && pending == 0
	}
}

func deadLen(t *testing.T, client *redis.Client, runID uuid.UUID, kind llmobs.Kind) int64 {
	t.Helper()
	n, err := client.XLen(t.Context(), fmt.Sprintf("llmstream:%s:%s:dead", runID, kind)).Result()
	if err != nil {
		t.Fatalf("XLen dead: %v", err)
	}
	return n
}

// TestStage exercises the durable stage against a real Redis (Streams + consumer
// groups). Each subtest uses a fresh runID so key namespaces don't collide on the
// shared container. Stages are Started with a background context (their run
// context) so Close exercises the clean-finish drain; the context is cancelled
// only by Close's internal readCancel.
func TestStage(t *testing.T) {
	client := newTestClient(t)
	runCtx := context.Background()

	t.Run("enqueue consume ack", func(t *testing.T) {
		runID := uuid.New()
		spy := newSpy()
		stage := llmstream.NewStage(client, runID, llmobs.KindClassify, procOf(spy), fastOpts()...)
		if err := stage.Start(runCtx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		if err := stage.Enqueue(t.Context(), &task{Key: "k1"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		waitFor(t, eventually, "task consumed and acked",
			func() bool { return spy.seenCount("k1") == 1 && depthIsZero(t, client, runID, llmobs.KindClassify)() })

		if got := spy.callCount(); got != 1 {
			t.Errorf("processed %d times, want exactly 1 (no redelivery on success)", got)
		}
	})

	t.Run("crash before ack redelivers", func(t *testing.T) {
		runID := uuid.New()
		spy := newSpy()
		spy.failFor["k"] = 1 // fail the first delivery, succeed on the redelivery

		opts := append(fastOpts(), llmstream.WithMaxAttempts[task](5))
		stage := llmstream.NewStage(client, runID, llmobs.KindExtract, procOf(spy), opts...)
		if err := stage.Start(runCtx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		if err := stage.Enqueue(t.Context(), &task{Key: "k"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		// The first attempt errors and leaves the entry pending; the reclaimer
		// redelivers it after minIdle and the retry succeeds and acks.
		waitFor(t, eventually, "redelivered task eventually succeeds",
			func() bool { return spy.seenCount("k") == 1 })
		waitFor(t, eventually, "pending list clears after successful retry",
			depthIsZero(t, client, runID, llmobs.KindExtract))

		if got := spy.callCount(); got != 2 {
			t.Errorf("processed %d times, want 2 (one failed delivery + one successful redelivery)", got)
		}
	})

	t.Run("idempotent reprocessing", func(t *testing.T) {
		runID := uuid.New()
		spy := newSpy()
		stage := llmstream.NewStage(client, runID, llmobs.KindExtract, procOf(spy), fastOpts()...)
		if err := stage.Start(runCtx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		// The same logical task staged twice: both are delivered and processed, but
		// the durable sink (a set) records one distinct effect — the property the
		// real processors' upserts guarantee, so redelivery never double-writes.
		for range 2 {
			if err := stage.Enqueue(t.Context(), &task{Key: "dup"}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
		}

		waitFor(t, eventually, "both copies processed and acked",
			func() bool { return spy.callCount() == 2 && depthIsZero(t, client, runID, llmobs.KindExtract)() })

		if got := spy.distinctSeen(); got != 1 {
			t.Errorf("distinct durable effects = %d, want 1 (idempotent sink)", got)
		}
	})

	t.Run("dead-letter after max attempts", func(t *testing.T) {
		runID := uuid.New()
		spy := newSpy()
		spy.alwaysFail = true

		opts := append(fastOpts(), llmstream.WithMaxAttempts[task](1))
		stage := llmstream.NewStage(client, runID, llmobs.KindClassify, procOf(spy), opts...)
		if err := stage.Start(runCtx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		if err := stage.Enqueue(t.Context(), &task{Key: "poison"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		// After exhausting its attempts the poison task lands in the dead-letter
		// stream and is acked off the main pending list.
		waitFor(t, eventually, "poison task moved to dead-letter stream",
			func() bool { return deadLen(t, client, runID, llmobs.KindClassify) == 1 })
		waitFor(t, eventually, "poison task acked off the main pending list",
			depthIsZero(t, client, runID, llmobs.KindClassify))

		if got := spy.seenCount("poison"); got != 0 {
			t.Errorf("poison task recorded %d successes, want 0", got)
		}
	})

	t.Run("DeleteRun sweeps run keys", func(t *testing.T) {
		runID := uuid.New()
		stage := llmstream.NewStage(client, runID, llmobs.KindClassify, procOf(newSpy()))

		// Enqueue (creates the main stream) and write directly to the dead-letter
		// stream, so both keys under the run's namespace exist.
		if err := stage.Enqueue(t.Context(), &task{Key: "k"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if err := client.XAdd(t.Context(), &redis.XAddArgs{
			Stream: fmt.Sprintf("llmstream:%s:classify:dead", runID),
			Values: map[string]interface{}{"payload": "{}"},
		}).Err(); err != nil {
			t.Fatalf("XAdd dead: %v", err)
		}

		pattern := fmt.Sprintf("llmstream:%s:*", runID)
		before, err := client.Keys(t.Context(), pattern).Result()
		if err != nil {
			t.Fatalf("Keys before: %v", err)
		}
		if len(before) == 0 {
			t.Fatal("expected stream keys to exist before delete")
		}

		if err := llmstream.DeleteRun(t.Context(), client, runID); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}

		after, err := client.Keys(t.Context(), pattern).Result()
		if err != nil {
			t.Fatalf("Keys after: %v", err)
		}
		if len(after) != 0 {
			t.Errorf("expected no stream keys after delete, got %v", after)
		}
	})

	t.Run("Depth reports backlog and pending", func(t *testing.T) {
		runID := uuid.New()
		kind := llmobs.KindExtract
		stream := fmt.Sprintf("llmstream:%s:extract", runID)
		stage := llmstream.NewStage(client, runID, kind, procOf(newSpy()))

		// Three staged, none consumed: backlog 3, pending 0 (no group yet).
		for i := range 3 {
			if err := stage.Enqueue(t.Context(), &task{Key: fmt.Sprintf("k%d", i)}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
		}
		backlog, pending, err := llmstream.Depth(t.Context(), client, runID, kind)
		if err != nil {
			t.Fatalf("Depth: %v", err)
		}
		if backlog != 3 || pending != 0 {
			t.Errorf("Depth before consume: got (backlog=%d, pending=%d), want (3, 0)", backlog, pending)
		}

		// Create the group and deliver one entry (leaving it pending, not acked):
		// backlog stays 3 (nothing deleted), pending becomes 1.
		if err := client.XGroupCreate(t.Context(), stream, "llm", "0").Err(); err != nil {
			t.Fatalf("XGroupCreate: %v", err)
		}
		if _, err := client.XReadGroup(t.Context(), &redis.XReadGroupArgs{
			Group:    "llm",
			Consumer: "probe",
			Streams:  []string{stream, ">"},
			Count:    1,
		}).Result(); err != nil {
			t.Fatalf("XReadGroup: %v", err)
		}

		backlog, pending, err = llmstream.Depth(t.Context(), client, runID, kind)
		if err != nil {
			t.Fatalf("Depth: %v", err)
		}
		if backlog != 3 || pending != 1 {
			t.Errorf("Depth with one in-flight: got (backlog=%d, pending=%d), want (3, 1)", backlog, pending)
		}
	})
}
