package collection

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// defaultPollInterval is how often the scheduler re-checks due-ness. It is far
// finer than the (default daily) collection cadence but coarse enough to be
// cheap: a Cycle that overruns its interval resumes within one poll of finishing
// (ADR-0036), not at the next aligned cadence tick. Internal, not env-tunable --
// the spec exposes only the interval and the disable flag.
const defaultPollInterval = time.Minute

// Starter starts a run for a definition. Satisfied by *runner.Runner; the
// scheduler depends on this narrow surface, not the whole runner.
type Starter interface {
	Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error)
}

// LatestRunLookup returns the most-recently-started run for a definition, or nil
// (no error) when it has never run. It is the persisted state the due-check
// derives from, so scheduling survives restarts (ADR-0036). Satisfied by
// *postgres.CrawlRunRepository.
type LatestRunLookup interface {
	LatestByDefinition(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error)
}

// Due reports whether a new Collection Cycle should start at now (ADR-0036): due
// when none is active and now is at or past lastStart+interval. A collector that
// has never run (lastStart zero) is due immediately, so the first Cycle after
// cutover repopulates the Corpus. Due never counts missed windows -- a long
// outage yields exactly one Cycle, not a catch-up burst -- because lastStart is
// the last ACTUAL start, not an aligned schedule tick.
func Due(now, lastStart time.Time, active bool, interval time.Duration) bool {
	if active {
		return false
	}
	if lastStart.IsZero() {
		return true
	}
	return !now.Before(lastStart.Add(interval))
}

// Config wires a Scheduler. Interval is the collection cadence (caller-validated
// > 0). Poll defaults to defaultPollInterval when non-positive.
type Config struct {
	Runs         LatestRunLookup
	Starter      Starter
	DefinitionID uuid.UUID
	Interval     time.Duration
	Poll         time.Duration
}

// Scheduler starts Collection Cycles on a cadence by polling persisted run rows
// (ADR-0036). It owns no timers of record: every due decision is recomputed from
// the database each poll, so a restart resumes the cadence with no lost or
// duplicated Cycle. Overlap is prevented by the one-active-run invariant -- a
// Start that races an active Cycle gets ErrActiveRunExists and is ignored.
type Scheduler struct {
	runs     LatestRunLookup
	starter  Starter
	defID    uuid.UUID
	interval time.Duration
	poll     time.Duration
}

func NewScheduler(cfg Config) *Scheduler {
	poll := cfg.Poll
	if poll <= 0 {
		poll = defaultPollInterval
	}
	return &Scheduler{
		runs:     cfg.Runs,
		starter:  cfg.Starter,
		defID:    cfg.DefinitionID,
		interval: cfg.Interval,
		poll:     poll,
	}
}

// Run polls until ctx is cancelled, starting a Cycle whenever one is due. It
// checks once immediately (so a due Cycle -- e.g. the first after cutover, or an
// overdue one after a restart -- starts without waiting a full poll), then every
// poll interval. It blocks; run it in a goroutine.
func (s *Scheduler) Run(ctx context.Context) {
	s.tick(ctx)
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick recomputes due-state from the latest persisted run and starts a Cycle if
// due. Given the one-active-run invariant, the active run (if any) is always the
// latest-started, so the latest run alone yields both the cadence anchor
// (StartedAt) and the active flag (non-terminal status).
func (s *Scheduler) tick(ctx context.Context) {
	latest, err := s.runs.LatestByDefinition(ctx, s.defID)
	if err != nil {
		slog.Error("collection scheduler: reading latest run", "err", err)
		return
	}
	var lastStart time.Time
	active := false
	if latest != nil {
		lastStart = latest.StartedAt
		active = !latest.Status.Terminal()
	}
	if !Due(time.Now(), lastStart, active, s.interval) {
		return
	}
	if _, err := s.starter.Start(ctx, s.defID); err != nil {
		if errors.Is(err, crawler.ErrActiveRunExists) {
			return // a Cycle is already active (raced the due-check); expected
		}
		if ctx.Err() != nil {
			return // shutting down: the Start failure is the drain, not a fault
		}
		slog.Error("collection scheduler: starting cycle", "err", err)
	}
}
