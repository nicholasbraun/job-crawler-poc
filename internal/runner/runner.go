// Package runner owns the lifecycle of crawl runs: starting a run from a
// definition, stopping it via desired state, draining every active run on
// shutdown, and — on boot — adopting runs a previous process left non-terminal
// and resuming them from externalized state (Reconcile). The actual crawl
// wiring is supplied by a Factory closure (owned by cmd/server), which keeps
// the runner testable and independent of the concrete downloader/parser/pool
// stack. Concurrent runs are isolated: each has its own engine, worker pools,
// per-run Redis frontier namespace, and counters — the only shared state is the
// active map, guarded by a mutex.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
)

// ErrRunNotActive is returned by Stop when the target run is not currently
// running (already finished, or never started in this process).
var ErrRunNotActive = errors.New("runner: run not active")

// ErrShuttingDown is returned by Start once Shutdown has begun draining: the
// intake path is closed so a new run cannot be launched into a WaitGroup that
// is already being waited on.
var ErrShuttingDown = errors.New("runner: shutting down")

// counterFlushInterval is how often live counters are flushed to the run row
// so the dashboard sees progress while a crawl is in flight.
const counterFlushInterval = 1500 * time.Millisecond

// statusPollInterval throttles the desired-state stop poll: GetStatus is hit at
// most once per interval, since the orchestrator loop polls very frequently.
const statusPollInterval = time.Second

// Counters holds a run's live progress metrics. Many url workers increment
// concurrently, so the fields are atomic.
type Counters struct {
	PagesCrawled  atomic.Int64
	ListingsFound atomic.Int64
}

// Snapshot reads the counters into a plain value for persistence.
func (c *Counters) Snapshot() crawler.RunCounters {
	return crawler.RunCounters{
		PagesCrawled:  c.PagesCrawled.Load(),
		ListingsFound: c.ListingsFound.Load(),
	}
}

// Engine is a fully-wired crawl ready to run. Close drains the run's worker
// pools and must be called once Run returns; it drains the url pool before the
// job_listing pool (reverse order would enqueue into a closed pool and lose
// listings).
type Engine struct {
	Orchestrator *orchestrator.Orchestrator
	SeedURLs     []string
	Close        func()
}

// Factory builds an Engine for a single run. runID is the just-created run's
// id, used to namespace per-run state (e.g. the Redis frontier keys). counters
// are the taps the wiring increments; shouldStop is the desired-state poll to
// hand to the orchestrator. ctx is the run context (derived from
// context.Background, not a request).
type Factory func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error)

type activeRun struct {
	cancel context.CancelFunc
}

// FrontierCleaner drops a run's externalized frontier state once the run reaches
// a terminal status, so its transient Redis keys don't leak. cmd/server wires
// this to redisfrontier.DeleteRun; when left nil (e.g. in tests) terminal
// cleanup is simply skipped.
type FrontierCleaner func(ctx context.Context, runID uuid.UUID) error

// Option configures a Runner.
type Option func(*Runner)

// WithFrontierCleaner sets the cleaner invoked after a run reaches a terminal
// status.
func WithFrontierCleaner(c FrontierCleaner) Option {
	return func(r *Runner) { r.frontierCleaner = c }
}

// Runner starts, stops, and drains crawl runs.
type Runner struct {
	runs            crawler.CrawlRunRepository
	defs            crawler.CrawlDefinitionRepository
	factory         Factory
	frontierCleaner FrontierCleaner

	mu       sync.Mutex
	active   map[uuid.UUID]*activeRun
	wg       sync.WaitGroup
	draining bool
}

func New(runs crawler.CrawlRunRepository, defs crawler.CrawlDefinitionRepository, factory Factory, opts ...Option) *Runner {
	r := &Runner{
		runs:    runs,
		defs:    defs,
		factory: factory,
		active:  map[uuid.UUID]*activeRun{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start looks up the definition, records a running run, wires the engine, and
// launches it in the background. The returned run reflects the just-created
// row. The crawl outlives the calling request: its context derives from
// context.Background, so only Stop/Shutdown can cancel it.
func (r *Runner) Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error) {
	// Refuse intake once shutdown has begun so we never Add to r.wg after
	// Shutdown has started waiting on it (see #14).
	r.mu.Lock()
	draining := r.draining
	r.mu.Unlock()
	if draining {
		return nil, ErrShuttingDown
	}

	def, err := r.defs.Get(ctx, definitionID)
	if err != nil {
		return nil, err
	}

	run := &crawler.CrawlRun{
		ID:           uuid.New(),
		DefinitionID: def.ID,
		Status:       crawler.RunStatusRunning,
		StartedAt:    time.Now(),
	}
	if err := r.runs.Create(ctx, run); err != nil {
		return nil, err
	}

	if err := r.launch(run, def, &Counters{}); err != nil {
		return nil, err
	}

	return run, nil
}

// Reconcile adopts runs left non-terminal (running or stopping) by a previous
// process and resumes them from their externalized state: the factory
// re-attaches to the run's Redis frontier by runID, and the persisted counters
// carry progress forward. A run that was stopping resumes and then immediately
// drains to stopped via the desired-state poll. Per-run failures (a missing
// definition, a factory error) are logged and skipped so one bad run can't
// block the rest; only a failure to list the runs is returned. Call once at
// boot, before serving, so it cannot race Start/Stop.
func (r *Runner) Reconcile(ctx context.Context) error {
	runs, err := r.runs.ListByStatus(ctx, crawler.RunStatusRunning, crawler.RunStatusStopping)
	if err != nil {
		return fmt.Errorf("runner: listing interrupted runs: %w", err)
	}

	adopted := 0
	for _, run := range runs {
		def, err := r.defs.Get(ctx, run.DefinitionID)
		if err != nil {
			slog.Error("runner: error loading definition for interrupted run, skipping", "err", err, "run_id", run.ID)
			continue
		}
		// Seed live counters from the persisted snapshot so the first flush does
		// not regress the row toward zero.
		if err := r.launch(run, def, countersFrom(run.Counters)); err != nil {
			slog.Error("runner: error adopting interrupted run, skipping", "err", err, "run_id", run.ID)
			continue
		}
		adopted++
		slog.Info("runner: adopted interrupted run", "run_id", run.ID, "status", run.Status)
	}

	if adopted > 0 {
		slog.Info("runner: reconcile complete", "adopted", adopted)
	}
	return nil
}

// launch wires an engine for an already-persisted run and supervises it in the
// background. Shared by Start (a fresh run, zero counters) and Reconcile (an
// adopted run, counters seeded from Postgres). The run context derives from
// context.Background so the crawl outlives the calling request/boot. On a
// factory error the run is marked failed and the error is returned; the caller
// decides whether that is fatal.
func (r *Runner) launch(run *crawler.CrawlRun, def *crawler.CrawlDefinition, counters *Counters) error {
	runCtx, cancel := context.WithCancel(context.Background())
	shouldStop := r.newStopPoller(run.ID)

	engine, err := r.factory(runCtx, run.ID, *def, counters, shouldStop)
	if err != nil {
		cancel()
		finishedAt := time.Now()
		if uerr := r.runs.UpdateStatus(context.Background(), run.ID, crawler.RunStatusFailed, &finishedAt, err.Error()); uerr != nil {
			slog.Error("runner: error marking run failed after factory error", "err", uerr, "run_id", run.ID)
		}
		// The factory may already have created the run's Redis frontier keys
		// before failing; this run never reaches supervise, so clean them up
		// here or they leak with no owner (see #24).
		if r.frontierCleaner != nil {
			if cerr := r.frontierCleaner(context.Background(), run.ID); cerr != nil {
				slog.Error("runner: error cleaning up frontier after factory error", "err", cerr, "run_id", run.ID)
			}
		}
		return err
	}

	r.mu.Lock()
	r.active[run.ID] = &activeRun{cancel: cancel}
	r.wg.Add(1)
	r.mu.Unlock()

	go r.supervise(runCtx, cancel, run.ID, engine, counters)

	return nil
}

// Stop requests a desired-state stop: it writes status=stopping (the source of
// truth polled by the loop) and then cancels the run context to unblock a
// frontier that may be sleeping on a cooldown. Returns ErrRunNotActive if the
// run already finished.
func (r *Runner) Stop(ctx context.Context, runID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	active, ok := r.active[runID]
	if !ok {
		return ErrRunNotActive
	}

	if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusStopping, nil, ""); err != nil {
		return err
	}
	active.cancel()

	return nil
}

// Shutdown closes intake, requests every active run to stop, then blocks until
// all have drained or the context deadline elapses. It never calls os.Exit; the
// caller exits only after this returns. The drain is bounded by ctx: if a
// worker is stuck in a call that ignores cancellation, Shutdown returns once ctx
// is done rather than hanging forever (see #15).
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	// Latch draining so Start rejects any new run for the rest of the process
	// lifetime; a run added after this point would race r.wg.Wait (see #14).
	r.draining = true
	for runID, active := range r.active {
		if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusStopping, nil, ""); err != nil {
			slog.Error("runner: error marking run stopping on shutdown", "err", err, "run_id", runID)
		}
		active.cancel()
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("runner: shutdown timed out, exiting with runs still draining")
	}
}

// supervise blocks on the crawl, flushing counters periodically, then drains
// the engine and records the terminal status. Terminal DB writes use
// context.Background because the run context is cancelled on stop. cancel is the
// run context's cancel, invoked by the desired-state watcher when it observes a
// stop request that the orchestrator loop cannot see (see #16).
func (r *Runner) supervise(runCtx context.Context, cancel context.CancelFunc, runID uuid.UUID, engine *Engine, counters *Counters) {
	defer r.wg.Done()

	// Both helper goroutines are joined before the final flush so no late write
	// can land after it or after the engine's pools close (see #23), and the
	// watcher is part of drain accounting (see #16).
	var helpers sync.WaitGroup

	flushDone := make(chan struct{})
	helpers.Add(1)
	go func() {
		defer helpers.Done()
		ticker := time.NewTicker(counterFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := r.runs.UpdateCounters(context.Background(), runID, counters.Snapshot()); err != nil {
					slog.Error("runner: error flushing counters", "err", err, "run_id", runID)
				}
			case <-flushDone:
				return
			}
		}
	}()

	// Desired-state stop watcher: polls the run's status independently of the
	// orchestrator loop, so a parked perpetual run whose frontier.Next never
	// returns still honors a stopping status by cancelling the run context.
	watchDone := make(chan struct{})
	helpers.Add(1)
	go func() {
		defer helpers.Done()
		ticker := time.NewTicker(statusPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				status, err := r.runs.GetStatus(context.Background(), runID)
				if err != nil {
					slog.Error("runner: error polling run status in watcher", "err", err, "run_id", runID)
					continue
				}
				if status == crawler.RunStatusStopping {
					cancel()
					return
				}
			}
		}
	}()

	runErr := engine.Orchestrator.Run(runCtx, engine.SeedURLs)

	close(flushDone)
	close(watchDone)
	helpers.Wait()

	engine.Close()

	if err := r.runs.UpdateCounters(context.Background(), runID, counters.Snapshot()); err != nil {
		slog.Error("runner: error flushing final counters", "err", err, "run_id", runID)
	}

	finishedAt := time.Now()
	status, errMsg := terminalStatus(runErr)

	// Serialize the terminal transition + deregistration against Stop so a run
	// that finishes just as Stop fires cannot be left stuck in 'stopping'.
	r.mu.Lock()
	if err := r.runs.UpdateStatus(context.Background(), runID, status, &finishedAt, errMsg); err != nil {
		slog.Error("runner: error setting terminal status", "err", err, "run_id", runID)
	}
	delete(r.active, runID)
	r.mu.Unlock()

	// Drop the run's transient frontier state now that it has ended. Done
	// outside the lock: it is Redis I/O and must not block a concurrent
	// Stop/Shutdown holding mu.
	if r.frontierCleaner != nil {
		if err := r.frontierCleaner(context.Background(), runID); err != nil {
			slog.Error("runner: error cleaning up frontier for finished run", "err", err, "run_id", runID)
		}
	}
}

// newStopPoller returns a throttled desired-state poll: it reads the run's
// status at most once per statusPollInterval and latches true once it observes
// stopping. Called only from the orchestrator's single loop goroutine.
func (r *Runner) newStopPoller(runID uuid.UUID) func(context.Context) bool {
	var (
		mu       sync.Mutex
		lastPoll time.Time
		latched  bool
	)

	return func(ctx context.Context) bool {
		mu.Lock()
		defer mu.Unlock()

		if latched {
			return true
		}
		if !lastPoll.IsZero() && time.Since(lastPoll) < statusPollInterval {
			return false
		}
		lastPoll = time.Now()

		status, err := r.runs.GetStatus(ctx, runID)
		if err != nil {
			slog.Error("runner: error polling run status", "err", err, "run_id", runID)
			return false
		}
		if status == crawler.RunStatusStopping {
			latched = true
			return true
		}
		return false
	}
}

// countersFrom builds live Counters initialized to a persisted snapshot, so an
// adopted run continues accumulating from where the previous process left off
// rather than restarting at zero.
func countersFrom(rc crawler.RunCounters) *Counters {
	c := &Counters{}
	c.PagesCrawled.Store(rc.PagesCrawled)
	c.ListingsFound.Store(rc.ListingsFound)
	return c
}

// terminalStatus maps a run's final error to its terminal status. A nil error
// is a clean completion; a requested stop or a cancelled context is stopped
// (not a failure); anything else is a failure carrying the error text.
func terminalStatus(err error) (crawler.RunStatus, string) {
	switch {
	case err == nil:
		return crawler.RunStatusCompleted, ""
	case errors.Is(err, orchestrator.ErrStopRequested), errors.Is(err, context.Canceled):
		return crawler.RunStatusStopped, ""
	default:
		return crawler.RunStatusFailed, err.Error()
	}
}
