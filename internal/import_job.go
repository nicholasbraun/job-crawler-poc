package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ImportJobStatus is the lifecycle state of an Import Job.
//
//	pending    created, waiting for the single execution slot
//	running    the line loop is validating/merging the uploaded file
//	completed  the file was processed; Result holds the report
//	failed     an infrastructure error aborted the job; Error holds the detail
type ImportJobStatus string

const (
	ImportJobStatusPending   ImportJobStatus = "pending"
	ImportJobStatusRunning   ImportJobStatus = "running"
	ImportJobStatusCompleted ImportJobStatus = "completed"
	ImportJobStatusFailed    ImportJobStatus = "failed"
)

// ImportError is one line-tagged data error in an Import Job's result. Line is
// the 1-indexed file line the error occurred on; Message is the user-facing
// per-line report entry (lowercase, no package prefix), so an operator can jump
// to the offending line and fix it.
type ImportError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ImportResult is the terminal report of a completed Import Job: the would-upsert
// (dry run) or upserted (real) counts, the first capped batch of line-tagged data
// errors, and ErrorCount — the true total, which may exceed len(Errors) when the
// file had more errors than the report cap.
type ImportResult struct {
	CompaniesUpserted int           `json:"companiesUpserted"`
	PagesUpserted     int           `json:"pagesUpserted"`
	Errors            []ImportError `json:"errors"`
	ErrorCount        int           `json:"errorCount"`
}

// ImportJob is one asynchronous, durable execution of a Catalog Import (ADR-0014).
// The uploaded payload is buffered in memory for the job's lifetime and never
// persisted; only this record is durable. A dry-run Job validates and reports
// without writing to the Catalog. Used as a pointer type.
type ImportJob struct {
	ID       uuid.UUID
	Status   ImportJobStatus
	DryRun   bool
	Filename string
	FileSize int64
	// Result is the terminal report; nil until the job completes (and stays nil
	// for a pending, running, or failed job).
	Result *ImportResult
	// Error is the infrastructure-failure detail for a failed job; empty otherwise.
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ImportJobRepository persists Import Job records (ADR-0014). The uploaded file
// is deliberately not stored — a job interrupted by a restart is swept to failed
// and the operator re-uploads (idempotent merges make that a complete recovery).
type ImportJobRepository interface {
	// Create inserts job. If job.ID is nil a fresh ID is assigned. The
	// idempotency-key and request-fingerprint columns are left NULL in this
	// milestone (populated by the idempotent-submission work, #87).
	Create(ctx context.Context, job *ImportJob) error

	// Get returns the job with id, or ErrNotFound if none exists.
	Get(ctx context.Context, id uuid.UUID) (*ImportJob, error)

	// List returns every Import Job, newest first. Never nil.
	List(ctx context.Context) ([]*ImportJob, error)

	// Update writes job's status, result, error, and updated_at by id. Used for
	// the pending->running and running->completed/failed transitions.
	Update(ctx context.Context, job *ImportJob) error

	// SweepInterrupted marks every job still pending or running — left behind by a
	// dead process — as failed with msg and updated_at = at, returning how many
	// rows it changed. Called once at boot (ADR-0014).
	SweepInterrupted(ctx context.Context, msg string, at time.Time) (int64, error)
}
