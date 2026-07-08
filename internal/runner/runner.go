// Package runner owns the lifecycle of crawl runs: starting a run from a
// definition, stopping it via desired state, and draining every active run on
// shutdown. The actual crawl wiring is supplied by a Factory closure (owned by
// cmd/server), which keeps the runner testable and independent of the concrete
// downloader/parser/pool stack. Shaped so multi-run (a later step) is a map
// insert rather than a rewrite.
package runner

import (
	"context"
	"errors"
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

// FrontierCleaner drops a run's transient frontier state (e.g. its Redis keys)
// once the run is permanently done. Injected by the caller so the runner stays
// decoupled from the concrete frontier implementation.
type FrontierCleaner func(ctx context.Context, runID uuid.UUID) error

// Option configures a Runner.
type Option func(*Runner)

// WithFrontierCleaner sets the hook used to discard a finished run's frontier
// state. Without it, that state is left in place.
func WithFrontierCleaner(c FrontierCleaner) Option {
	return func(r *Runner) { r.frontierCleaner = c }
}

// Runner starts, stops, and drains crawl runs.
type Runner struct {
	runs            crawler.CrawlRunRepository
	defs            crawler.CrawlDefinitionRepository
	factory         Factory
	frontierCleaner FrontierCleaner

	mu     sync.Mutex
	active map[uuid.UUID]*activeRun
	wg     sync.WaitGroup
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

// Reconcile fails any run left non-terminal by a previous process. A freshly
// started process supervises no runs, so a running or stopping row is an orphan
// whose process crashed or was killed mid-run; it can never resume and its stop
// endpoint would 409 forever. Marking it failed clears it from the API and
// dashboard. Call once at startup before serving.
func (r *Runner) Reconcile(ctx context.Context) error {
	ids, err := r.runs.FailInterrupted(ctx, "interrupted by server restart")
	if err != nil {
		return err
	}
	// A failed orphan can never resume, so its frontier state is dead weight;
	// drop it. Cleanup failure is logged, not fatal — the run is already failed.
	for _, id := range ids {
		if r.frontierCleaner == nil {
			break
		}
		if err := r.frontierCleaner(ctx, id); err != nil {
			slog.Error("runner: error cleaning frontier state for orphaned run", "err", err, "run_id", id)
		}
	}
	if len(ids) > 0 {
		slog.Info("runner: reconciled orphaned runs on startup", "count", len(ids))
	}
	return nil
}

// Start looks up the definition, records a running run, wires the engine, and
// launches it in the background. The returned run reflects the just-created
// row. The crawl outlives the calling request: its context derives from
// context.Background, so only Stop/Shutdown can cancel it.
func (r *Runner) Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error) {
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

	runCtx, cancel := context.WithCancel(context.Background())
	counters := &Counters{}
	shouldStop := r.newStopPoller(run.ID)

	engine, err := r.factory(runCtx, run.ID, *def, counters, shouldStop)
	if err != nil {
		cancel()
		finishedAt := time.Now()
		if uerr := r.runs.UpdateStatus(ctx, run.ID, crawler.RunStatusFailed, &finishedAt, err.Error()); uerr != nil {
			slog.Error("runner: error marking run failed after factory error", "err", uerr, "run_id", run.ID)
		}
		return nil, err
	}

	r.mu.Lock()
	r.active[run.ID] = &activeRun{cancel: cancel}
	r.wg.Add(1)
	r.mu.Unlock()

	go r.supervise(runCtx, run.ID, engine, counters)

	return run, nil
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

// Shutdown requests every active run to stop, then blocks until all have
// drained and flipped to a terminal status. It never calls os.Exit; the caller
// exits only after this returns.
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	for runID, active := range r.active {
		if err := r.runs.UpdateStatus(ctx, runID, crawler.RunStatusStopping, nil, ""); err != nil {
			slog.Error("runner: error marking run stopping on shutdown", "err", err, "run_id", runID)
		}
		active.cancel()
	}
	r.mu.Unlock()

	r.wg.Wait()
}

// supervise blocks on the crawl, flushing counters periodically, then drains
// the engine and records the terminal status. Terminal DB writes use
// context.Background because the run context is cancelled on stop.
func (r *Runner) supervise(runCtx context.Context, runID uuid.UUID, engine *Engine, counters *Counters) {
	defer r.wg.Done()

	tickerDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(counterFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := r.runs.UpdateCounters(context.Background(), runID, counters.Snapshot()); err != nil {
					slog.Error("runner: error flushing counters", "err", err, "run_id", runID)
				}
			case <-tickerDone:
				return
			}
		}
	}()

	runErr := engine.Orchestrator.Run(runCtx, engine.SeedURLs)

	close(tickerDone)
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
