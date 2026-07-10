package llmstream_test

import (
	"context"
	"errors"
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

	t.Run("resume redelivers a pending entry orphaned by a crashed process", func(t *testing.T) {
		runID := uuid.New()
		kind := llmobs.KindExtract
		stream := fmt.Sprintf("llmstream:%s:extract", runID)

		// Model a previous process that created the group, delivered an entry to a
		// worker, then died before acking: the entry is stuck in the PEL, owned by a
		// consumer that no longer exists.
		prior := llmstream.NewStage(client, runID, kind, procOf(newSpy()))
		if err := prior.Enqueue(t.Context(), &task{Key: "orphan"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if err := client.XGroupCreateMkStream(t.Context(), stream, "llm", "0").Err(); err != nil {
			t.Fatalf("XGroupCreateMkStream: %v", err)
		}
		if _, err := client.XReadGroup(t.Context(), &redis.XReadGroupArgs{
			Group: "llm", Consumer: "extract-0-deadproc", Streams: []string{stream, ">"}, Count: 1,
		}).Result(); err != nil {
			t.Fatalf("XReadGroup: %v", err)
		}

		// The new process adopts the run: a fresh Stage on the same runID/kind
		// re-attaches to the group (BUSYGROUP) and its reclaimer must redeliver the
		// orphaned entry. Workers only read never-delivered entries, so only the
		// reclaim path can recover it — this is the #32 restart guarantee.
		spy := newSpy()
		stage := llmstream.NewStage(client, runID, kind, procOf(spy), fastOpts()...)
		if err := stage.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		waitFor(t, eventually, "orphaned pending entry redelivered and processed",
			func() bool { return spy.seenCount("orphan") == 1 })
		waitFor(t, eventually, "pending list clears after reclaim",
			depthIsZero(t, client, runID, kind))
	})

	t.Run("clean finish drains a queued backlog before Close returns", func(t *testing.T) {
		runID := uuid.New()
		kind := llmobs.KindClassify
		spy := newSpy()
		// A single worker so the backlog cannot drain instantly on Enqueue; Close's
		// clean-finish drain must process the remainder before it returns.
		opts := []llmstream.Option[task]{
			llmstream.WithWorkers[task](1),
			llmstream.WithMinIdle[task](100 * time.Millisecond),
			llmstream.WithBlockDuration[task](50 * time.Millisecond),
			llmstream.WithDrainPoll[task](20 * time.Millisecond),
			llmstream.WithDepthInterval[task](time.Hour),
		}
		stage := llmstream.NewStage(client, runID, kind, procOf(spy), opts...)
		if err := stage.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}

		const n = 25
		for i := range n {
			if err := stage.Enqueue(t.Context(), &task{Key: fmt.Sprintf("k%d", i)}); err != nil {
				t.Fatalf("Enqueue %d: %v", i, err)
			}
		}

		// Clean finish: the run context is still alive, so Close drains to empty
		// before returning rather than leaving the backlog for a resume.
		stage.Close()

		if got := spy.distinctSeen(); got != n {
			t.Errorf("processed %d distinct tasks after drain, want %d", got, n)
		}
		if !depthIsZero(t, client, runID, kind)() {
			t.Error("stream not fully drained after clean-finish Close")
		}
	})

	t.Run("first delivery is not reclaimed before minIdle", func(t *testing.T) {
		runID := uuid.New()
		kind := llmobs.KindExtract
		spy := newSpy()
		spy.gate = make(chan struct{}) // hold the first (and only) Process call open

		// A large minIdle with a much shorter reclaim interval: the reclaimer scans
		// often, but a first delivery still in flight (a slow-but-alive worker) must
		// not be reclaimed and double-processed until minIdle elapses.
		opts := []llmstream.Option[task]{
			llmstream.WithWorkers[task](2),
			llmstream.WithMinIdle[task](2 * time.Second),
			llmstream.WithReclaimInterval[task](50 * time.Millisecond),
			llmstream.WithBlockDuration[task](50 * time.Millisecond),
			llmstream.WithDrainPoll[task](20 * time.Millisecond),
			llmstream.WithDepthInterval[task](time.Hour),
		}
		stage := llmstream.NewStage(client, runID, kind, procOf(spy), opts...)
		if err := stage.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer stage.Close()

		if err := stage.Enqueue(t.Context(), &task{Key: "slow"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		// Wait for a worker to pick it up and enter Process (now blocked on the gate).
		waitFor(t, eventually, "task delivered to a worker",
			func() bool { return spy.callCount() >= 1 })

		// Well within minIdle but many reclaim intervals later, the in-flight entry
		// must not have been reclaimed and re-processed.
		time.Sleep(700 * time.Millisecond)
		if got := spy.callCount(); got != 1 {
			t.Fatalf("Process called %d times; a live first delivery was reclaimed before minIdle", got)
		}

		// Let the worker finish: the single delivery succeeds and acks, no redelivery.
		close(spy.gate)
		waitFor(t, eventually, "in-flight task completes on its single delivery",
			func() bool { return spy.seenCount("slow") == 1 && depthIsZero(t, client, runID, kind)() })
		if got := spy.callCount(); got != 1 {
			t.Errorf("Process called %d times total, want 1 (no reclaim of a live entry)", got)
		}
	})

	t.Run("backpressure blocks enqueue at the cap and is ctx-cancellable", func(t *testing.T) {
		runID := uuid.New()
		kind := llmobs.KindExtract
		const capacity = 3
		stage := llmstream.NewStage(client, runID, kind, procOf(newSpy()),
			llmstream.WithMaxBacklog[task](capacity))
		// Deliberately not Started: nothing drains, so the backlog only grows.

		for i := range capacity {
			if err := stage.Enqueue(t.Context(), &task{Key: fmt.Sprintf("k%d", i)}); err != nil {
				t.Fatalf("Enqueue %d: %v", i, err)
			}
		}

		// At the cap, the next Enqueue must block until capacity frees; since nothing
		// drains, it returns only when its context is cancelled.
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- stage.Enqueue(ctx, &task{Key: "blocked"}) }()

		select {
		case err := <-errCh:
			t.Fatalf("Enqueue returned %v at capacity; expected it to block", err)
		case <-time.After(400 * time.Millisecond):
		}

		cancel()
		select {
		case err := <-errCh:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("blocked Enqueue returned %v, want context.Canceled", err)
			}
		case <-time.After(eventually):
			t.Fatal("cancelled Enqueue never returned")
		}

		n, err := client.XLen(t.Context(), fmt.Sprintf("llmstream:%s:extract", runID)).Result()
		if err != nil {
			t.Fatalf("XLen: %v", err)
		}
		if n != capacity {
			t.Errorf("backlog = %d, want %d (a blocked enqueue must not add past the cap)", n, capacity)
		}
	})
}
