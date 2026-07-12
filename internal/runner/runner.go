// Package runner owns the lifecycle of crawl runs: starting a run from a
// definition, stopping it via desired state, draining every active run on
// shutdown (leaving it resumable rather than terminal), and — on boot —
// adopting runs a previous process left non-terminal and resuming them from
// externalized state (Reconcile). A graceful shutdown leaves its runs running
// (Option C) with their Redis frontier intact, so a restart/redeploy continues
// them where they left off: Reconcile adopts running/stopping/pausing runs and
// resumes them, and skips paused runs (a deliberate human park it must never
// auto-resume). The actual crawl wiring is supplied by a Factory closure (owned
// by cmd/server), which keeps the runner testable and independent of the
// concrete downloader/parser/pool stack. Concurrent runs are isolated: each has
// its own engine, worker pools, per-run Redis frontier namespace, and counters —
// the only shared state is the active map, guarded by a mutex. A human-paused run
// is relaunched on demand by Resume — a live, per-run reconcile that flips the run
// back to running and rebuilds its engine through the same Factory, seeded from the
// persisted counters and re-attached to the preserved frontier by run id.
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

// ErrRunNotPaused is returned by Resume when the target run's durable status is
// not paused — it is running, pausing, stopping, terminal, or already being
// resumed/adopted (present in the active set, so a concurrent Resume or a residual
// Reconcile won the race). The API maps it to 409.
var ErrRunNotPaused = errors.New("runner: run not paused")

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
	// stopRequested is set by Stop to mark this run for a terminal stop. It lets
	// supervise tell an explicit user stop (terminate, clean the frontier) apart
	// from a shutdown drain (park as resumable) when both cancel the same
	// context. Guarded by Runner.mu.
	stopRequested bool
	// pauseRequested is set by Pause (or re-derived by the status watcher from a
	// durable pausing status) to mark this run for a park as paused: supervise
	// preserves its frontier and leaves FinishedAt unset. Guarded by Runner.mu.
	pauseRequested bool
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

// Reconcile adopts runs left non-terminal (running, stopping, or pausing) by a
// previous process and resumes them from their externalized state: the factory
// re-attaches to the run's Redis frontier by runID, and the persisted counters
// carry progress forward. A run that was stopping resumes and then immediately
// drains to stopped via the desired-state poll. A pausing run keeps its pausing
// status (it is not flipped to running) so the run's status watcher can
// re-derive the park and drive it back to paused. A paused run is a deliberate
// human park: it is NOT listed and never auto-resumed — it stays parked until a
// human Resumes it. Per-run failures (a missing definition, a factory error) are
// logged and skipped so one bad run can't block the rest; only a failure to list
// the runs is returned. Call once at boot, before serving, so it cannot race
// Start/Stop.
func (r *Runner) Reconcile(ctx context.Context) error {
	runs, err := r.runs.ListByStatus(ctx, crawler.RunStatusRunning, crawler.RunStatusStopping, crawler.RunStatusPausing)
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
		r.markFailedAndClean(run.ID, err)
		return err
	}

	ar := &activeRun{cancel: cancel}
	r.mu.Lock()
	r.active[run.ID] = ar
	r.wg.Add(1)
	r.mu.Unlock()

	go r.supervise(runCtx, cancel, run.ID, ar, engine, counters)

	return nil
}

// markFailedAndClean records a run as terminally failed and drops its transient
// frontier state. It is the shared cleanup for a factory error on a run that never
// reached supervise: the factory may already have created the run's Redis frontier
// keys before failing, so they must be cleaned here or they leak with no owner
// (see #24). Terminal DB writes use context.Background because the failure often
// coincides with a cancelled request/run context.
func (r *Runner) markFailedAndClean(runID uuid.UUID, cause error) {
	finishedAt := time.Now()
	if uerr := r.runs.UpdateStatus(context.Background(), runID, crawler.RunStatusFailed, &finishedAt, cause.Error()); uerr != nil {
		slog.Error("runner: error marking run failed after factory error", "err", uerr, "run_id", runID)
	}
	if r.frontierCleaner != nil {
		if cerr := r.frontierCleaner(context.Background(), runID); cerr != nil {
			slog.Error("runner: error cleaning up frontier after factory error", "err", cerr, "run_id", runID)
		}
	}
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

	// Mark this as an explicit stop so a concurrent Shutdown drain terminates it
	// (status stopped, frontier cleaned) rather than parking it for resume.
	active.stopRequested = true
	if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusStopping, nil, ""); err != nil {
		return err
	}
	active.cancel()

	return nil
}

// Pause requests a desired-state pause of a running run: it marks an in-memory
// pause-requested flag, writes status=pausing (the durable desired state honored
// by the run's status watcher), and cancels the run context to unblock a
// frontier that may be sleeping or parked on a perpetual Next. Unlike Stop, this
// parks the run as paused — non-terminal, FinishedAt unset, frontier preserved
// for a later Resume. Returns ErrRunNotActive if the run is not the live running
// run, or if it is already pausing or stopping (a redundant/invalid request the
// API maps to 409).
func (r *Runner) Pause(ctx context.Context, runID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	active, ok := r.active[runID]
	if !ok {
		return ErrRunNotActive
	}
	if active.pauseRequested || active.stopRequested {
		return ErrRunNotActive
	}

	active.pauseRequested = true
	if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusPausing, nil, ""); err != nil {
		return err
	}
	active.cancel()

	return nil
}

// Resume relaunches a human-paused run from exactly where it left off: it verifies
// the run's durable status is paused, flips it to running, and rebuilds the engine
// through the same Factory used by Start and Reconcile — seeded from the run's
// persisted counters (so counters continue rather than reset) and re-attached to
// the preserved frontier by run id. The frontier is deliberately NOT cleaned.
//
// Resume is a live, per-run reconcile and must be race-safe: the run's active-set
// slot is the mutual-exclusion mechanism. Under a single r.mu critical section it
// rejects a run already in the active set (a concurrent Resume or a residual
// boot-time Reconcile won), verifies the durable status is paused (read under the
// same lock so it is atomic against a racing flip), flips it to running, and
// reserves the slot — only then, outside the lock, is the Factory invoked exactly
// once. It cannot reuse launch, which invokes the Factory before reserving the slot
// and would let two concurrent Resumes each build an engine against one frontier.
//
// Returns ErrRunNotPaused when the run is not paused (running/pausing/stopping/
// terminal, or already being resumed), ErrShuttingDown during a drain, or the
// repository's error for an unknown id.
func (r *Runner) Resume(ctx context.Context, runID uuid.UUID) error {
	// Load the run (for its persisted counters) and definition before taking the
	// lock, so only the cheap reserve-and-flip happens under r.mu. An unknown id
	// surfaces the repository error here, before any state is touched.
	run, err := r.runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	def, err := r.defs.Get(ctx, run.DefinitionID)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	shouldStop := r.newStopPoller(runID)
	ar := &activeRun{cancel: cancel}

	r.mu.Lock()
	// Never wg.Add after Shutdown began waiting on it (see #14); the lock also
	// serializes this reservation against Shutdown's cancel-all loop.
	if r.draining {
		r.mu.Unlock()
		cancel()
		return ErrShuttingDown
	}
	// The slot is the true mutual exclusion: if the run is already active a
	// concurrent Resume or a residual Reconcile is already driving it.
	if _, exists := r.active[runID]; exists {
		r.mu.Unlock()
		cancel()
		return ErrRunNotPaused
	}
	// Read the durable status under the lock so the paused→running flip is atomic
	// against a racing transition.
	status, err := r.runs.GetStatus(ctx, runID)
	if err != nil {
		r.mu.Unlock()
		cancel()
		return err
	}
	if status != crawler.RunStatusPaused {
		r.mu.Unlock()
		cancel()
		return ErrRunNotPaused
	}
	if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusRunning, nil, ""); err != nil {
		r.mu.Unlock()
		cancel()
		return err
	}
	r.active[runID] = ar
	r.wg.Add(1)
	r.mu.Unlock()

	// Heavy per-run I/O (rebuilding the engine, re-attaching Redis state) runs
	// outside the lock. The reservation above guarantees this factory call is the
	// only one for this run.
	counters := countersFrom(run.Counters)
	engine, err := r.factory(runCtx, runID, *def, counters, shouldStop)
	if err != nil {
		cancel()
		// Unwind the reservation made above before recording the failure (unlike
		// launch, which had not yet reserved when its factory failed): a leaked
		// wg count hangs Shutdown, a stranded active entry blocks a later Resume.
		r.mu.Lock()
		delete(r.active, runID)
		r.wg.Done()
		r.mu.Unlock()
		r.markFailedAndClean(runID, err)
		return err
	}

	go r.supervise(runCtx, cancel, runID, ar, engine, counters)

	return nil
}

// Shutdown closes intake, cancels every active run, then blocks until all have
// drained or the context deadline elapses. It never calls os.Exit; the caller
// exits only after this returns. The drain is bounded by ctx: if a worker is
// stuck in a call that ignores cancellation, Shutdown returns once ctx is done
// rather than hanging forever (see #15).
//
// Unlike Stop, Shutdown does NOT write a stopping desired-state: a drain is not
// a stop. It only cancels each run's context to unblock it; supervise then
// leaves the run running (Option C — keeping its Redis frontier intact) so the
// next process adopts and resumes it via Reconcile, exactly like a crash. It is
// deliberately not parked as paused: paused is reserved for a human Pause that
// Reconcile must never auto-resume. Writing stopping here would instead make
// Reconcile resume the run only to immediately drain it to stopped.
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	// Latch draining so Start rejects any new run for the rest of the process
	// lifetime; a run added after this point would race r.wg.Wait (see #14).
	// draining also tells supervise to park (rather than terminate) any run it
	// finds cancelled without an explicit stop.
	r.draining = true
	for _, active := range r.active {
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
// the engine and either leaves the run running for resume (a shutdown drain) or
// records its terminal status. Terminal DB writes use context.Background because the run context is
// cancelled on stop. cancel is the run context's cancel, invoked by the
// desired-state watcher when it observes a stop request that the orchestrator
// loop cannot see (see #16). ar is this run's active entry, read to tell a
// user-requested stop apart from a shutdown drain.
func (r *Runner) supervise(runCtx context.Context, cancel context.CancelFunc, runID uuid.UUID, ar *activeRun, engine *Engine, counters *Counters) {
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
				switch status {
				case crawler.RunStatusStopping:
					cancel()
					return
				case crawler.RunStatusPausing:
					// Re-derive a pause whose in-memory flag was lost across a
					// restart (a reconcile-adopted crash-mid-pause run): mark the
					// flag under the lock so supervise's fate decision parks it as
					// paused, then cancel to unblock the loop. The exact analog of
					// how a persisted stopping is re-honored above.
					r.mu.Lock()
					ar.pauseRequested = true
					r.mu.Unlock()
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

	// Serialize the fate decision + deregistration against Stop so a run that
	// finishes just as Stop fires cannot be left stuck in 'stopping'.
	r.mu.Lock()
	// A pause-requested run parks as paused: non-terminal (FinishedAt stays nil),
	// frontier preserved (this returns before the cleaner call below), so a later
	// Resume continues it. This MUST precede the shutdown-drain branch: when a
	// pause races a graceful shutdown, the deliberate human park must win over the
	// auto-resumable "leave running" park, and a pause-requested run must never
	// fall through to a terminal status. The `!ar.stopRequested` guard yields to
	// an explicit Stop, which still terminates the run as stopped (frontier
	// cleaned) in the branches below. The `runErr != nil && runCtx cancelled`
	// guards mirror the shutdown branch: a run that hit ErrDone (→ nil) exactly as
	// a pause fired is recorded on its own terms below, not resurrected as paused.
	if ar.pauseRequested && !ar.stopRequested && runErr != nil && errors.Is(runCtx.Err(), context.Canceled) {
		if err := r.runs.UpdateStatus(context.Background(), runID, crawler.RunStatusPaused, nil, ""); err != nil {
			slog.Error("runner: error parking run as paused", "err", err, "run_id", runID)
		}
		delete(r.active, runID)
		r.mu.Unlock()
		slog.Info("runner: parked run as paused", "run_id", runID)
		return
	}

	// A shutdown drain that cancelled a run the user did not explicitly Stop is
	// a resumable interruption, not a completion: leave it running (Option C —
	// non-terminal, finishedAt stays nil) and keep its Redis frontier intact so
	// the next process re-adopts it (Reconcile) exactly like a crash-interrupted
	// run. Leaving it running (rather than parking it paused) reserves paused for
	// a deliberate human Pause, which reconcile must never auto-resume. A user
	// Stop, a natural completion (nil), or any exit while not draining falls
	// through to a terminal status below.
	//
	// The fate is keyed off the run context, not the error shape. During a drain
	// runCtx is always cancelled, but a cancellation that lands mid-read surfaces
	// as a net i/o timeout (go-redis aborts the in-flight socket read via a past
	// deadline → os.ErrDeadlineExceeded), not a wrapped context.Canceled — so
	// errors.Is(runErr, context.Canceled) would miss any busy crawl (see #32).
	// runCtx.Err() is reliable regardless of how the frontier's underlying read
	// failed. Because the cancelled context is the dominant signal, ANY non-nil
	// error during a drain parks the run: at that moment nearly every error is
	// cancellation-induced, and favouring resume over a terminal failure is the
	// whole point of a graceful shutdown (a genuinely broken run just re-fails
	// after Reconcile). The runErr != nil guard still lets a run that hit ErrDone
	// (→ nil) just as shutdown fired be recorded completed rather than
	// resurrected as paused.
	if r.draining && !ar.stopRequested && runErr != nil && errors.Is(runCtx.Err(), context.Canceled) {
		if err := r.runs.UpdateStatus(context.Background(), runID, crawler.RunStatusRunning, nil, ""); err != nil {
			slog.Error("runner: error leaving run running on shutdown", "err", err, "run_id", runID)
		}
		delete(r.active, runID)
		r.mu.Unlock()
		slog.Info("runner: left run running for resume on shutdown", "run_id", runID)
		return
	}

	finishedAt := time.Now()
	status, errMsg := terminalStatus(runErr, runCtx.Err())
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
// is a clean completion; a requested stop or a cancelled context is stopped (not
// a failure); anything else is a failure carrying the error text. ctxErr is the
// run context's error (runCtx.Err()): when it is non-nil the run was
// intentionally Stopped or Shutdown, so any error is downgraded to stopped even
// if the frontier surfaced a net i/o timeout (from a cancellation landing
// mid-read) rather than a wrapped context.Canceled. A transient frontier I/O
// error during normal operation happens with a live (non-cancelled) context, so
// it still fails.
func terminalStatus(err error, ctxErr error) (crawler.RunStatus, string) {
	switch {
	case err == nil:
		return crawler.RunStatusCompleted, ""
	case errors.Is(err, orchestrator.ErrStopRequested), errors.Is(err, context.Canceled):
		return crawler.RunStatusStopped, ""
	case ctxErr != nil: // run context cancelled → intentional stop, even if the
		// frontier surfaced a net i/o timeout instead of context.Canceled
		return crawler.RunStatusStopped, ""
	default:
		return crawler.RunStatusFailed, err.Error()
	}
}
