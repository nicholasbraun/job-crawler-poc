package llmstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	"github.com/redis/go-redis/v9"
)

// worker consumes new (never-delivered) entries via XREADGROUP ">", processes
// each, and XACKs on success. A failed entry is left in the pending list for the
// reclaimer to redeliver. It runs until readCtx is cancelled (a stop/shutdown, or
// the readCancel a clean-finish drain fires once the backlog is empty).
func (s *Stage[T]) worker(consumer string) {
	defer s.wg.Done()
	proc := s.newProc()

	for {
		if s.readCtx.Err() != nil {
			return
		}
		streams, err := s.client.XReadGroup(s.readCtx, &redis.XReadGroupArgs{
			Group:    s.group,
			Consumer: consumer,
			Streams:  []string{s.stream, ">"},
			Count:    s.batchCount,
			Block:    s.blockDur,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // BLOCK elapsed with no new work
			}
			if s.readCtx.Err() != nil {
				return // cancelled: stop, leave the PEL for redelivery
			}
			slog.Error("llmstream: error reading from stream", "err", err, "kind", s.kind, "consumer", consumer)
			if !sleepCtx(s.readCtx, s.blockDur) {
				return
			}
			continue
		}
		for _, st := range streams {
			for _, msg := range st.Messages {
				s.handle(proc, msg)
			}
		}
	}
}

// reclaimer is the durable analog of the frontier's expired-lease reclaim: it
// scans the pending list every reclaimInterval, redelivers entries that have sat
// idle long enough, and dead-letters any that have exhausted their attempts. Two
// idle thresholds keep it both crash-safe and prompt: a first, never-reclaimed
// delivery is redelivered only once idle past the full minIdle window (it may be a
// slow-but-alive worker still in its model call), while an entry already reclaimed
// at least once is known-failed and retried on the shorter reclaimInterval cadence
// — so a poison task dead-letters in minIdle + (attempts × reclaimInterval), not
// attempts × minIdle. One goroutine per stage; retries run inline and serially.
func (s *Stage[T]) reclaimer() {
	defer s.wg.Done()
	proc := s.newProc()
	consumer := string(s.kind) + "-reclaimer"

	for {
		if s.readCtx.Err() != nil {
			return
		}
		s.reclaimOnce(proc, consumer)
		if !sleepCtx(s.readCtx, s.reclaimInterval) {
			return
		}
	}
}

// reclaimOnce redelivers or dead-letters pending entries. It lists everything idle
// past the short reclaimInterval, then applies the per-entry crash-safety rule: a
// first delivery is held until it is idle past the full minIdle window, while an
// entry already reclaimed at least once is retried right away.
func (s *Stage[T]) reclaimOnce(proc processor.Processor[T], consumer string) {
	pending, err := s.client.XPendingExt(s.readCtx, &redis.XPendingExtArgs{
		Stream: s.stream,
		Group:  s.group,
		Idle:   s.reclaimInterval,
		Start:  "-",
		End:    "+",
		Count:  s.batchCount,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) || s.readCtx.Err() != nil {
			return
		}
		slog.Error("llmstream: error listing pending entries", "err", err, "kind", s.kind)
		return
	}

	for _, p := range pending {
		if s.readCtx.Err() != nil {
			return
		}
		// RetryCount is the delivery count; once it exceeds maxAttempts the entry
		// is judged poison and moved to the dead-letter stream.
		if p.RetryCount > int64(s.maxAttempts) {
			s.deadLetterPending(p)
			continue
		}
		// A never-reclaimed first delivery (RetryCount 1) might be a slow-but-alive
		// worker still inside its model call; leave it until it is idle past the full
		// minIdle crash window. A redelivered entry (RetryCount > 1) is known-failed
		// — a live worker would have acked — so retry it on the reclaimInterval
		// cadence the XPendingExt filter already applied.
		if p.RetryCount <= 1 && p.Idle < s.minIdle {
			continue
		}
		s.reclaim(proc, consumer, p.ID)
	}
}

// reclaim XCLAIMs one pending entry idle past reclaimInterval (which increments its
// delivery count and hands ownership to this consumer) and reprocesses it. The
// caller has already applied the per-entry idle rule, and reclaimInterval is the
// smallest window it ever acts on, so XCLAIM's own MinIdle guard matches it. XCLAIM
// returns nothing if the entry was acked or is no longer idle, in which case there
// is nothing to do.
func (s *Stage[T]) reclaim(proc processor.Processor[T], consumer, id string) {
	msgs, err := s.client.XClaim(s.readCtx, &redis.XClaimArgs{
		Stream:   s.stream,
		Group:    s.group,
		Consumer: consumer,
		MinIdle:  s.reclaimInterval,
		Messages: []string{id},
	}).Result()
	if err != nil {
		if s.readCtx.Err() == nil {
			slog.Error("llmstream: error claiming pending entry", "err", err, "kind", s.kind, "id", id)
		}
		return
	}
	for _, msg := range msgs {
		s.recorder.Retry(s.readCtx, s.kind)
		s.handle(proc, msg)
	}
}

// handle decodes and processes one message. On success it acks (and deletes) the
// entry; on a processing error it leaves the entry pending for the reclaimer; on
// an undecodable payload — which can never succeed — it dead-letters immediately.
func (s *Stage[T]) handle(proc processor.Processor[T], msg redis.XMessage) {
	raw, ok := msg.Values[payloadField].(string)
	if !ok {
		slog.Error("llmstream: entry missing payload field, dead-lettering", "kind", s.kind, "id", msg.ID)
		s.moveToDeadLetter(msg, "missing payload")
		return
	}
	var task T
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		slog.Error("llmstream: error decoding task, dead-lettering", "err", err, "kind", s.kind, "id", msg.ID)
		s.moveToDeadLetter(msg, "decode error")
		return
	}
	if err := s.runProcess(proc, &task, msg.ID); err != nil {
		// A cancelled run context surfaces here too; leaving the entry pending is
		// correct in both cases (transient error → retry; shutdown → resume).
		slog.Error("llmstream: error processing task, leaving pending", "err", err, "kind", s.kind, "id", msg.ID)
		return
	}
	s.ack(msg.ID)
}

// runProcess runs the processor, converting a panic into an error so one poison
// item can neither crash the goroutine nor the process (mirrors pool.process):
// the entry is left pending and, if it keeps panicking, dead-lettered.
func (s *Stage[T]) runProcess(proc processor.Processor[T], task *T, id string) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("llmstream: recovered from panic in Process",
				"kind", s.kind, "id", id, "panic", rec, "stack", string(debug.Stack()))
			err = fmt.Errorf("llmstream: panic in process: %v", rec)
		}
	}()
	return proc.Process(s.readCtx, task)
}

// ack removes a completed entry from the pending list (XACK) and deletes it from
// the stream (XDEL) so the backlog reflects only outstanding work. The two run in
// one MULTI/EXEC so a crash cannot leave an entry acked-but-undeleted: such a
// straggler still counts toward XLEN yet is no longer pending, which would stall a
// resumed run's clean-finish drain (it waits for backlog==0 && pending==0)
// forever. It uses a background context: a completed unit of work must be
// acknowledged even as the run context cancels (an un-acked but done entry would
// be needlessly reprocessed after a resume — harmless, since the processors are
// idempotent, but wasteful).
func (s *Stage[T]) ack(id string) {
	ctx := context.Background()
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.XAck(ctx, s.stream, s.group, id)
		pipe.XDel(ctx, s.stream, id)
		return nil
	})
	if err != nil {
		slog.Error("llmstream: error acking entry", "err", err, "kind", s.kind, "id", id)
	}
}

// deadLetterPending moves a poison pending entry to the dead-letter stream. It
// reads the payload back to preserve it; if the entry is already gone it just
// clears the pending list so it stops being scanned.
func (s *Stage[T]) deadLetterPending(p redis.XPendingExt) {
	msgs, err := s.client.XRange(context.Background(), s.stream, p.ID, p.ID).Result()
	if err != nil || len(msgs) == 0 {
		if err != nil {
			slog.Error("llmstream: error reading poison entry for dead-letter", "err", err, "kind", s.kind, "id", p.ID)
		}
		s.ack(p.ID)
		return
	}
	s.moveToDeadLetter(msgs[0], fmt.Sprintf("exceeded max attempts (%d)", s.maxAttempts))
}

// moveToDeadLetter copies an entry to the dead-letter stream, acks+deletes it off
// the main stream, and records the dead-letter. The dead-letter stream is a
// diagnostic sink only: it lives under the run's llmstream:{runID}:* namespace and
// is swept by DeleteRun when the run ends, so the retained payload is for in-run
// inspection while the durable signal is the DeadLetter counter, not the entry.
func (s *Stage[T]) moveToDeadLetter(msg redis.XMessage, reason string) {
	payload, _ := msg.Values[payloadField].(string)
	if err := s.client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: s.dead,
		Values: map[string]interface{}{
			payloadField: payload,
			"reason":     reason,
			"origin_id":  msg.ID,
		},
	}).Err(); err != nil {
		slog.Error("llmstream: error writing to dead-letter stream", "err", err, "kind", s.kind, "id", msg.ID)
	}
	s.ack(msg.ID)
	s.recorder.DeadLetter(context.Background(), s.kind)
}

// drain blocks until the backlog and pending list are both empty, so a clean
// finish processes every staged task (and every retry) before Close returns. A
// stop/shutdown landing mid-drain cancels the run context, which abandons the
// drain and leaves the remaining entries for a resumed run to redeliver.
func (s *Stage[T]) drain() {
	ticker := time.NewTicker(s.drainPoll)
	defer ticker.Stop()

	for {
		if s.runCtx.Err() != nil {
			return
		}
		backlog, pending, err := streamDepth(context.Background(), s.client, s.stream, s.group)
		if err != nil {
			slog.Error("llmstream: error measuring depth during drain", "err", err, "kind", s.kind)
			return
		}
		if backlog == 0 && pending == 0 {
			return
		}
		select {
		case <-s.runCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

// depthReporter periodically samples the queue depth + pending count and records
// them as gauges, so queue depth and lag are observable on the metrics endpoint.
func (s *Stage[T]) depthReporter() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.depthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.readCtx.Done():
			return
		case <-ticker.C:
			backlog, pending, err := streamDepth(s.readCtx, s.client, s.stream, s.group)
			if err != nil {
				if s.readCtx.Err() != nil {
					return
				}
				slog.Error("llmstream: error sampling queue depth", "err", err, "kind", s.kind)
				continue
			}
			s.recorder.QueueDepth(s.readCtx, s.kind, backlog, pending)
		}
	}
}

// sleepCtx sleeps for d, returning false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
