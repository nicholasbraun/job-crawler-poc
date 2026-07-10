// Package llmstream is a durable, per-run LLM work stage backed by a Redis
// Stream and a consumer group. It replaces the two in-process LLM worker pools
// (career-page classify, job-listing extract) with a crash-safe queue: the crawl
// XADDs a task and, unless an optional backlog cap is reached, does not block on
// the model, while a consumer group drains the backlog into the same
// processor.Processor, XACKs on success, and redelivers (then dead-letters) on
// failure.
//
// It mirrors the Redis frontier's durability model (ADR-0003, per-run transient
// state under a run-scoped key prefix, a lease/reclaim loop) but over Redis
// Streams instead of Lua-scripted lists: a message delivered by XREADGROUP joins
// the consumer group's pending list (PEL) — the analog of the frontier's
// in-flight lease — and is either XACKed (done) or, once idle past minIdle,
// reclaimed by the reclaimer (the analog of an expired-lease reclaim). A worker
// that crashes before acking loses nothing: the entry survives in Redis and a
// resumed run (runner.Reconcile) re-attaches to the same group and reclaims it.
// This closes the extraction-loss gap on timeout/restart (#32).
//
// A successful entry is XACKed and XDELed, so XLEN tracks only outstanding work
// (undelivered backlog + in-flight PEL) and drains to zero on a clean finish.
// Re-delivery must not double-write: the processors upsert on natural keys, so
// reprocessing is idempotent at the Postgres layer.
package llmstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	"github.com/redis/go-redis/v9"
)

// group is the consumer-group name shared by every stage. It is constant because
// each run/kind already has its own stream, so the group need not be unique.
const group = "llm"

// payloadField is the stream entry field that carries the JSON-serialized task.
const payloadField = "payload"

// Stage tunable defaults, sized for the LLM stage: a handful of attempts before
// a task is judged poison, a reclaim idle window long enough that a slow model
// call is not mistaken for a crashed worker, a shorter reclaim/retry cadence so a
// known-failed task is redelivered promptly regardless of that window, a short
// blocking read so a stop is noticed promptly, and a one-at-a-time worker read so
// a worker never holds an entry it is not actively processing.
//
// defaultReadCount is deliberately 1: a worker XREADGROUPs a single entry, so the
// only entry it owns is the one in flight. This keeps a pending entry's idle time
// a sound liveness signal — the reclaimer's minIdle rule can safely treat an
// over-idle first delivery as a dead worker. A larger read count would let one
// worker prefetch a batch into its pending list; entries queued behind a slow
// model call would accrue idle time untouched and be reclaimed and re-processed
// while their owner is alive (a wasted duplicate LLM call). The reclaimer's own
// pending-list scan is separately sized by defaultBatchCount, since listing more
// idle entries per round only speeds redelivery/dead-lettering.
const (
	defaultMaxAttempts      = 5
	defaultMinIdle          = 30 * time.Second
	defaultReclaimInterval  = 30 * time.Second
	defaultBlockDur         = 5 * time.Second
	defaultDepthInterval    = 10 * time.Second
	defaultDrainPoll        = 100 * time.Millisecond
	defaultReadCount        = 1
	defaultBatchCount       = 16
	defaultBackpressurePoll = 250 * time.Millisecond

	// deadLetterMaxLen caps the per-run dead-letter stream. The main stream
	// self-trims (XACK+XDEL removes a completed entry), but dead-lettered entries
	// only clear when DeleteRun sweeps the run on a terminal status, so a
	// long-lived perpetual run that keeps dead-lettering would grow it without
	// bound. MAXLEN ~N (approximate trim) bounds it; the durable signal is the
	// DeadLetter counter, so trimming the oldest retained payloads is harmless.
	deadLetterMaxLen = 10000
)

// Option configures a Stage.
type Option[T any] func(*Stage[T])

// WithWorkers sets the number of concurrent consumer goroutines (default 1).
func WithWorkers[T any](n int) Option[T] {
	return func(s *Stage[T]) { s.workers = n }
}

// WithMaxAttempts sets how many delivery attempts a task gets before it is moved
// to the dead-letter stream (default 5).
func WithMaxAttempts[T any](n int) Option[T] {
	return func(s *Stage[T]) { s.maxAttempts = n }
}

// WithMinIdle sets how long a pending (delivered-but-unacked) entry on its FIRST,
// never-reclaimed delivery must sit idle before the reclaimer treats its worker as
// dead and redelivers it (default 30s). It must exceed the maximum time a live
// worker spends on one entry — for the LLM stage, the whole Process: one model
// call (bounded by the http client's timeout) plus the follow-on Postgres upsert —
// or a slow-but-alive worker's in-flight entry is reclaimed and processed a second
// time (a wasted, duplicate LLM call); only a truly dead worker should ever sit
// idle this long. Retries of an already-failed entry are paced by the shorter
// WithReclaimInterval, not this window. Small values make redelivery fast for tests.
func WithMinIdle[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.minIdle = d }
}

// WithReclaimInterval sets how often the reclaimer scans the pending list, and the
// idle window an already-failed entry (one reclaimed at least once) must sit before
// it is retried (default 30s). It is deliberately decoupled from WithMinIdle so
// redelivery and dead-lettering stay prompt even when minIdle must be large
// (longer than a model call): minIdle gates only the first reclaim of a
// possibly-still-live worker's entry, while every subsequent retry of a
// known-failed entry proceeds on this shorter cadence. Clamped to minIdle when
// larger. Small values make retries fast for tests.
func WithReclaimInterval[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.reclaimInterval = d }
}

// WithMaxBacklog caps the stream's outstanding entries: once the backlog reaches n,
// Enqueue blocks (honouring its context) until a consumer drains it below n,
// applying backpressure so a crawl that outruns the LLM stage slows instead of
// growing Redis without bound (each entry carries the full page content). Zero (the
// default) disables the cap for pure producer/consumer decoupling. Sized as a
// high-water safety valve, so normal operation never touches it.
func WithMaxBacklog[T any](n int64) Option[T] {
	return func(s *Stage[T]) { s.maxBacklog = n }
}

// WithBlockDuration sets how long a worker blocks on XREADGROUP waiting for new
// work before looping (default 5s).
func WithBlockDuration[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.blockDur = d }
}

// WithReadCount sets how many new entries a worker claims per XREADGROUP (default
// 1). Keep it at 1 unless per-entry processing is cheap: a worker owns every entry
// it reads until it acks, so a prefetched entry queued behind a slow model call
// sits idle and can be reclaimed and double-processed by the reclaimer while its
// worker is still alive. Clamped to at least 1.
func WithReadCount[T any](n int64) Option[T] {
	return func(s *Stage[T]) { s.readCount = n }
}

// WithBatchCount sets how many pending entries the reclaimer lists and acts on per
// scan (default 16). Unlike the worker read count it does not affect double-process
// safety — the reclaimer only ever touches entries already idle past their reclaim
// window — so a larger value just redelivers/dead-letters a backlog faster.
func WithBatchCount[T any](n int64) Option[T] {
	return func(s *Stage[T]) { s.batchCount = n }
}

// WithDepthInterval sets how often queue depth + pending are sampled and recorded
// as gauges (default 10s).
func WithDepthInterval[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.depthInterval = d }
}

// WithDrainPoll sets how often a clean-finish drain re-measures depth while
// waiting for the backlog to empty (default 100ms).
func WithDrainPoll[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.drainPoll = d }
}

// WithRecorder sets the llmobs Recorder used to emit retry/dead-letter counters
// and queue-depth gauges. A nil recorder leaves the no-op default.
func WithRecorder[T any](r llmobs.Recorder) Option[T] {
	return func(s *Stage[T]) {
		if r != nil {
			s.recorder = r
		}
	}
}

// Stage is a durable per-run LLM work queue over a Redis Stream and a consumer
// group. Enqueue is a producer (XADD) that only blocks when a backlog cap is
// reached; Start launches the consumers; Close either drains to empty (clean
// finish) or stops promptly leaving the PEL for redelivery (stop/shutdown).
type Stage[T any] struct {
	client   *redis.Client
	stream   string
	dead     string
	group    string
	kind     llmobs.Kind
	newProc  func() processor.Processor[T]
	recorder llmobs.Recorder

	workers         int
	maxAttempts     int
	minIdle         time.Duration
	reclaimInterval time.Duration
	blockDur        time.Duration
	depthInterval   time.Duration
	drainPoll       time.Duration
	readCount       int64
	batchCount      int64
	maxBacklog      int64

	// runCtx is the run context handed to Start; its liveness at Close time tells
	// a clean finish (drain) apart from a stop/shutdown (leave the PEL). readCtx
	// derives from it and is cancelled by readCancel to stop the consumers.
	runCtx     context.Context
	readCtx    context.Context
	readCancel context.CancelFunc

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewStage builds a stage for runID and kind over the shared client. kind names
// the stream (llmstream:{runID}:{kind}); newProc mints one processor per consumer
// goroutine, mirroring the worker pool it replaces.
func NewStage[T any](client *redis.Client, runID uuid.UUID, kind llmobs.Kind, newProc func() processor.Processor[T], opts ...Option[T]) *Stage[T] {
	stream := streamKey(runID, kind)
	s := &Stage[T]{
		client:          client,
		stream:          stream,
		dead:            stream + ":dead",
		group:           group,
		kind:            kind,
		newProc:         newProc,
		recorder:        llmobs.Nop(),
		workers:         1,
		maxAttempts:     defaultMaxAttempts,
		minIdle:         defaultMinIdle,
		reclaimInterval: defaultReclaimInterval,
		blockDur:        defaultBlockDur,
		depthInterval:   defaultDepthInterval,
		drainPoll:       defaultDrainPoll,
		readCount:       defaultReadCount,
		batchCount:      defaultBatchCount,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.workers < 1 {
		s.workers = 1
	}
	if s.readCount < 1 {
		s.readCount = 1
	}
	if s.reclaimInterval <= 0 {
		s.reclaimInterval = defaultReclaimInterval
	}
	if s.reclaimInterval > s.minIdle {
		// The retry cadence can never outrun the first-reclaim crash window: an
		// entry can't be retried faster than a live worker's entry is protected.
		s.reclaimInterval = s.minIdle
	}
	return s
}

// Enqueue XADDs a task onto the stream. It normally does not wait on the model —
// the crawl and the LLM stage are decoupled — but when a backlog cap is set
// (WithMaxBacklog) it first blocks until the stream has capacity, so a fast crawl
// cannot grow Redis without bound. Safe to call before Start; XADD creates the
// stream.
func (s *Stage[T]) Enqueue(ctx context.Context, task *T) error {
	if err := s.awaitCapacity(ctx); err != nil {
		return err
	}
	b, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("llmstream: marshaling task: %w", err)
	}
	if err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.stream,
		Values: map[string]interface{}{payloadField: string(b)},
	}).Err(); err != nil {
		return fmt.Errorf("llmstream: enqueueing task: %w", err)
	}
	return nil
}

// awaitCapacity applies the backlog safety valve (WithMaxBacklog): when a cap is
// set it blocks until the stream's outstanding entries fall below it, so a fast
// crawl feeding a slow LLM stage slows rather than growing Redis unbounded. A zero
// cap is a no-op (pure decoupling). It honours ctx, so a stop/shutdown unblocks a
// producer parked on capacity, and reports that cancellation to the caller.
func (s *Stage[T]) awaitCapacity(ctx context.Context) error {
	if s.maxBacklog <= 0 {
		return nil
	}
	for {
		n, err := s.client.XLen(ctx, s.stream).Result()
		if err != nil {
			return fmt.Errorf("llmstream: measuring backlog for backpressure: %w", err)
		}
		if n < s.maxBacklog {
			return nil
		}
		if !sleepCtx(ctx, defaultBackpressurePoll) {
			return ctx.Err()
		}
	}
}

// Start creates the consumer group (idempotently — a group left by a previous
// process is reused) and launches the consumer, reclaimer, and depth-reporter
// goroutines. runCtx is the run context; cancelling it (a stop/shutdown) unblocks
// the consumers, which then leave any in-flight entries in the PEL. Call once.
func (s *Stage[T]) Start(runCtx context.Context) error {
	if err := s.ensureGroup(runCtx); err != nil {
		return err
	}

	s.runCtx = runCtx
	s.readCtx, s.readCancel = context.WithCancel(runCtx)

	for i := 0; i < s.workers; i++ {
		consumer := fmt.Sprintf("%s-%d", s.kind, i)
		s.wg.Add(1)
		go s.worker(consumer)
	}
	s.wg.Add(1)
	go s.reclaimer()
	s.wg.Add(1)
	go s.depthReporter()

	return nil
}

// Close stops the consumers. If the run context is still alive (a clean finish —
// the frontier drained) it processes the remaining backlog and pending entries to
// completion before returning. If the run context was cancelled (a stop or
// shutdown) it stops promptly and leaves un-acked entries in the PEL so a resumed
// run redelivers them. Safe to call multiple times, and a no-op if Start never
// ran.
func (s *Stage[T]) Close() {
	s.closeOnce.Do(func() {
		if s.readCancel == nil {
			return
		}
		if s.runCtx.Err() == nil {
			s.drain()
		}
		s.readCancel()
		s.wg.Wait()
		// The depth gauges are last-writer-wins per run; record a terminal zero so
		// this ended run stops contributing to sum by (kind). Background context:
		// readCtx is now cancelled. A clean finish drained to zero anyway; a
		// stop/shutdown is process-exit or headed to a terminal DeleteRun, so
		// reporting zero for this run's live backlog is correct in every real case.
		s.recorder.QueueDepth(context.Background(), s.kind, 0, 0)
	})
}

// ensureGroup creates the stream (MKSTREAM) and the consumer group at the stream
// origin. A BUSYGROUP error means a previous process already created the group
// (its last-delivered id and PEL survive in Redis), so it is treated as success.
func (s *Stage[T]) ensureGroup(ctx context.Context) error {
	if err := s.client.XGroupCreateMkStream(ctx, s.stream, s.group, "0").Err(); err != nil && !isBusyGroup(err) {
		return fmt.Errorf("llmstream: creating consumer group: %w", err)
	}
	return nil
}

// streamKey is the per-run, per-kind stream name. Everything a run stages lives
// under llmstream:{runID}:* so DeleteRun can sweep it (mirrors the frontier).
func streamKey(runID uuid.UUID, kind llmobs.Kind) string {
	return "llmstream:" + runID.String() + ":" + string(kind)
}

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

func isNoGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NOGROUP")
}
