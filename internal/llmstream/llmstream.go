// Package llmstream is a durable, per-run LLM work stage backed by a Redis
// Stream and a consumer group. It replaces the two in-process LLM worker pools
// (career-page classify, job-listing extract) with a crash-safe queue: the crawl
// XADDs a task and never blocks on the model, while a consumer group drains the
// backlog into the same processor.Processor, XACKs on success, and redelivers
// (then dead-letters) on failure.
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
// call is not mistaken for a crashed worker, a short blocking read so a stop is
// noticed promptly, and a small batch so one worker does not hoard the backlog.
const (
	defaultMaxAttempts   = 5
	defaultMinIdle       = 30 * time.Second
	defaultBlockDur      = 5 * time.Second
	defaultDepthInterval = 10 * time.Second
	defaultDrainPoll     = 100 * time.Millisecond
	defaultBatchCount    = 16
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

// WithMinIdle sets how long a pending (delivered-but-unacked) entry must sit idle
// before the reclaimer redelivers it, and thus the reclaim poll cadence (default
// 30s). It must exceed the maximum time a live worker spends on one entry — for
// the LLM stage, one model call's timeout — or a slow-but-alive worker's in-flight
// entry is reclaimed and processed a second time (a wasted, duplicate LLM call);
// only a truly dead worker should ever sit idle this long. Small values make
// redelivery fast for tests.
func WithMinIdle[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.minIdle = d }
}

// WithBlockDuration sets how long a worker blocks on XREADGROUP waiting for new
// work before looping (default 5s).
func WithBlockDuration[T any](d time.Duration) Option[T] {
	return func(s *Stage[T]) { s.blockDur = d }
}

// WithBatchCount sets how many entries a worker or the reclaimer reads per round
// trip (default 16).
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
// group. Enqueue is a non-blocking producer (XADD); Start launches the consumers;
// Close either drains to empty (clean finish) or stops promptly leaving the PEL
// for redelivery (stop/shutdown).
type Stage[T any] struct {
	client   *redis.Client
	stream   string
	dead     string
	group    string
	kind     llmobs.Kind
	newProc  func() processor.Processor[T]
	recorder llmobs.Recorder

	workers       int
	maxAttempts   int
	minIdle       time.Duration
	blockDur      time.Duration
	depthInterval time.Duration
	drainPoll     time.Duration
	batchCount    int64

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
		client:        client,
		stream:        stream,
		dead:          stream + ":dead",
		group:         group,
		kind:          kind,
		newProc:       newProc,
		recorder:      llmobs.Nop(),
		workers:       1,
		maxAttempts:   defaultMaxAttempts,
		minIdle:       defaultMinIdle,
		blockDur:      defaultBlockDur,
		depthInterval: defaultDepthInterval,
		drainPoll:     defaultDrainPoll,
		batchCount:    defaultBatchCount,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.workers < 1 {
		s.workers = 1
	}
	return s
}

// Enqueue XADDs a task onto the stream. It is a non-blocking producer: the crawl
// never waits on the model. Safe to call before Start; XADD creates the stream.
func (s *Stage[T]) Enqueue(ctx context.Context, task *T) error {
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
