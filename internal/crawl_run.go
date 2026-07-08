package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RunStatus is the lifecycle state of a CrawlRun.
//
//	running    the crawl loop is active
//	stopping   a stop was requested; the loop is draining (desired state)
//	paused     the run was parked by a graceful shutdown; non-terminal and
//	           resumable — boot-time reconcile re-adopts it (finished_at stays null)
//	stopped    the run halted on request
//	completed  the run drained the frontier and finished on its own
//	failed     the run ended on an unexpected error (see CrawlRun.Error)
type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusStopping  RunStatus = "stopping"
	RunStatusPaused    RunStatus = "paused"
	RunStatusStopped   RunStatus = "stopped"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
)

// RunCounters is a point-in-time snapshot of a run's progress metrics.
type RunCounters struct {
	PagesCrawled  int64
	ListingsFound int64
}

// CrawlRun is a single execution of a CrawlDefinition and the source of truth
// for its desired state: setting Status to stopping is how a stop is requested.
type CrawlRun struct {
	ID           uuid.UUID
	DefinitionID uuid.UUID
	Status       RunStatus
	Counters     RunCounters
	StartedAt    time.Time
	// FinishedAt is set once the run reaches a terminal status. Nil while active.
	FinishedAt *time.Time
	// Error holds the failure detail for a failed run; empty otherwise.
	Error string
}

// CrawlRunRepository persists and retrieves crawl runs and their live state.
type CrawlRunRepository interface {
	Create(ctx context.Context, run *CrawlRun) error
	Get(ctx context.Context, id uuid.UUID) (*CrawlRun, error)
	List(ctx context.Context) ([]*CrawlRun, error)
	// ListByStatus returns every run whose status is one of statuses. Used by
	// the boot-time reconcile loop to find runs (running, stopping, or paused)
	// left non-terminal by a previous process, so they can be adopted and
	// resumed.
	ListByStatus(ctx context.Context, statuses ...RunStatus) ([]*CrawlRun, error)
	// GetStatus reads just the status column — the hot path polled by the
	// crawl loop to detect a desired-state stop.
	GetStatus(ctx context.Context, id uuid.UUID) (RunStatus, error)
	// UpdateStatus sets the status and, for a terminal status, finishedAt and
	// errMsg. finishedAt is nil for non-terminal transitions.
	UpdateStatus(ctx context.Context, id uuid.UUID, status RunStatus, finishedAt *time.Time, errMsg string) error
	UpdateCounters(ctx context.Context, id uuid.UUID, counters RunCounters) error
}
