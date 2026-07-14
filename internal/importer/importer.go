// Package importer runs Catalog Imports as asynchronous, durable Import Jobs
// (ADR-0014). An uploaded NDJSON file is buffered in memory and processed off the
// request goroutine; the job record tracks its lifecycle (pending -> running ->
// completed/failed). Execution is serialized through a size-1 semaphore so
// concurrent uploads never contend. This milestone validates every line down the
// Identity Ladder and counts would-upserts (ADR-0013); the Catalog-merging
// Executor for non-dry-run jobs is injected by later work.
package importer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// maxReportedErrors caps how many line-tagged data errors an ImportResult carries
// while ErrorCount still reports the true total, so a file with thousands of bad
// lines yields a bounded report the dashboard can render.
const maxReportedErrors = 100

// interruptedMessage is the failure text stamped on a job a dead process left
// mid-flight; re-uploading is a complete recovery because imports merge
// idempotently (ADR-0013/0014).
const interruptedMessage = "interrupted by server restart; re-upload the file"

// Executor validates and applies one buffered import payload, returning the
// result report, or a non-nil error for an infrastructure failure that fails the
// whole job. dryRun selects validate-only vs. Catalog-merging behavior. This is
// the extension seam: the default executor (executeImport) only validates and
// counts would-upserts because no Catalog write path exists yet; a merging
// executor is injected via WithExecutor once merge writes land (#86).
type Executor func(ctx context.Context, payload []byte, dryRun bool) (crawler.ImportResult, error)

// Importer runs Catalog Imports as asynchronous, durable Import Jobs (ADR-0014).
type Importer struct {
	jobs    crawler.ImportJobRepository
	execute Executor
	sem     chan struct{}  // size-1 semaphore: FIFO-serialize job execution (ADR-0014)
	wg      sync.WaitGroup // tracks in-flight jobs for graceful Shutdown
}

// Option configures an Importer.
type Option func(*Importer)

// WithExecutor overrides the import execution step. Tests inject failures with
// it; later work injects the Catalog-merging executor for real imports.
func WithExecutor(e Executor) Option { return func(im *Importer) { im.execute = e } }

func New(jobs crawler.ImportJobRepository, opts ...Option) *Importer {
	im := &Importer{jobs: jobs, execute: executeImport, sem: make(chan struct{}, 1)}
	for _, opt := range opts {
		opt(im)
	}
	return im
}

// Submit creates a pending Import Job for the buffered payload and starts
// processing it asynchronously, returning the job in its initial pending state.
// The returned pointer is a snapshot the caller owns; the background goroutine
// mutates its own copy, so the two never race. Submit uses the request ctx only
// for the initial Create — a cancelled request before creation aborts; once
// launched, the job detaches and outlives the request.
func (im *Importer) Submit(ctx context.Context, filename string, payload []byte, dryRun bool) (*crawler.ImportJob, error) {
	now := time.Now().UTC()
	job := &crawler.ImportJob{
		ID:        uuid.New(),
		Status:    crawler.ImportJobStatusPending,
		DryRun:    dryRun,
		Filename:  filename,
		FileSize:  int64(len(payload)),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := im.jobs.Create(ctx, job); err != nil {
		return nil, fmt.Errorf("importer: creating import job: %w", err)
	}

	im.wg.Add(1)
	go im.run(*job, payload) // pass by value: the goroutine owns its copy

	return job, nil
}

// run drives one job through running -> completed/failed. It acquires the single
// execution slot first, so at most one job runs at a time (FIFO among waiters).
func (im *Importer) run(job crawler.ImportJob, payload []byte) {
	defer im.wg.Done()

	im.sem <- struct{}{} // acquire the single execution slot (FIFO among waiters)
	defer func() { <-im.sem }()

	ctx := context.Background() // detached: the import outlives its request

	job.Status = crawler.ImportJobStatusRunning
	job.UpdatedAt = time.Now().UTC()
	if err := im.jobs.Update(ctx, &job); err != nil {
		// Leave the job pending; the next boot's sweep will fail it. Nothing wrote
		// to the Catalog, so this is safe.
		slog.Error("importer: error marking import job running", "err", err, "job_id", job.ID)
		return
	}

	// job.DryRun currently changes nothing observable: there is no Catalog write
	// path in this milestone, so dry-run and real jobs both only validate and
	// count. The merging executor (#86) gates its writes on dryRun.
	result, execErr := im.execute(ctx, payload, job.DryRun)

	job.UpdatedAt = time.Now().UTC()
	if execErr != nil {
		job.Status, job.Error, job.Result = crawler.ImportJobStatusFailed, execErr.Error(), nil
	} else {
		job.Status, job.Error, job.Result = crawler.ImportJobStatusCompleted, "", &result
	}
	if err := im.jobs.Update(ctx, &job); err != nil {
		slog.Error("importer: error recording terminal import job", "err", err, "job_id", job.ID, "status", job.Status)
	}
}

// executeImport validates every line of an NDJSON catalog file down the Identity
// Ladder and counts would-upserts, collecting per-line data errors best-effort
// (ADR-0013). It ignores ctx and dryRun: this milestone has no Catalog write
// path, so validation is identical for dry-run and real jobs. It returns a
// non-nil error only for an infrastructure failure (an unreadable stream).
func executeImport(_ context.Context, payload []byte, _ bool) (crawler.ImportResult, error) {
	result := crawler.ImportResult{Errors: []crawler.ImportError{}}

	scanner := bufio.NewScanner(bytes.NewReader(payload))
	// One company line (with many nested pages) may exceed bufio's 64KB default
	// token size; a single record can never exceed the whole buffered upload, so
	// bound the token at the payload length + 1.
	scanner.Buffer(make([]byte, 0, 64*1024), len(payload)+1)

	line := 0
	addErr := func(msg string) {
		result.ErrorCount++
		if len(result.Errors) < maxReportedErrors {
			result.Errors = append(result.Errors, crawler.ImportError{Line: line, Message: msg})
		}
	}

	for scanner.Scan() {
		line++ // 1-indexed file line, counting blanks, so it matches the operator's editor
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue // skip blank lines (e.g. a trailing newline) without counting or erroring
		}
		rec, err := catalog.DecodeImportRecord(raw)
		if err != nil {
			addErr(err.Error())
			continue
		}
		resolved, err := catalog.ResolveImportRecord(rec)
		if err != nil {
			addErr(err.Error()) // whole-line failure (no identity, bad website, keyless ambiguity, ...)
			continue
		}
		// #86 merges resolved.Company + resolved.Pages into the Catalog here for a
		// non-dry-run job. This milestone only counts would-upserts.
		result.CompaniesUpserted++
		result.PagesUpserted += len(resolved.Pages)
		for _, perr := range resolved.PageErrors {
			addErr(perr.Error()) // sub-line best-effort: bad page, company + valid pages still counted
		}
	}
	if err := scanner.Err(); err != nil {
		return crawler.ImportResult{}, fmt.Errorf("importer: reading upload: %w", err)
	}

	return result, nil
}

// Sweep marks every job left pending or running by a previous process as failed.
// Call once at boot, before serving, so it cannot race a live Submit.
func (im *Importer) Sweep(ctx context.Context) error {
	n, err := im.jobs.SweepInterrupted(ctx, interruptedMessage, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("importer: sweeping interrupted import jobs: %w", err)
	}
	if n > 0 {
		slog.Info("importer: swept interrupted import jobs", "count", n)
	}
	return nil
}

// Shutdown blocks until in-flight jobs finish or ctx is done, so a graceful stop
// does not abandon a running import. Submit must not be called after Shutdown.
func (im *Importer) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		im.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
