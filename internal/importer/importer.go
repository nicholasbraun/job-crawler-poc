// Package importer runs Catalog Imports as asynchronous, durable Import Jobs
// (ADR-0014). An uploaded NDJSON file is buffered in memory and processed off the
// request goroutine; the job record tracks its lifecycle (pending -> running ->
// completed/failed). Execution is serialized through a size-1 semaphore so
// concurrent uploads never contend. Every line is validated down the Identity
// Ladder (ADR-0013); NewMergeExecutor then lands the resolved records into the
// Catalog for a non-dry-run job, injected at the server via WithExecutor. The
// zero-dependency default (executeImport) only validates and counts.
package importer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// counts would-upserts; the server injects NewMergeExecutor via WithExecutor to
// land the merges for real imports.
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

// fingerprintRequest is the SHA-256 (hex) of a submission's dry-run flag and
// payload — the "same request" identity an Idempotency-Key replay is checked
// against (ADR-0014). The flag is mixed in as a leading byte so a dry run and a
// real run of the same file fingerprint differently, forcing a fresh key for the
// "import for real" step after a dry run. Filename is deliberately excluded: a
// retry may re-pick the same bytes under a different local name.
func fingerprintRequest(payload []byte, dryRun bool) string {
	h := sha256.New()
	var flag byte
	if dryRun {
		flag = 1
	}
	h.Write([]byte{flag})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// Submit creates a pending Import Job for the buffered payload and starts
// processing it asynchronously, returning the job in its initial pending state
// and whether it was an idempotent replay of a prior submission (replay=true
// means the caller should answer 200 instead of 202). A non-empty
// idempotencyKey makes the submission retriable: a later submission with the
// same key returns the original job (replay), unless the request fingerprint
// differs, which yields crawler.ErrIdempotencyKeyConflict. The returned pointer
// is a snapshot the caller owns; the background goroutine mutates its own copy,
// so the two never race. Submit uses the request ctx only for the initial
// create — a cancelled request before creation aborts; once launched, the job
// detaches and outlives the request.
func (im *Importer) Submit(ctx context.Context, filename string, payload []byte, dryRun bool, idempotencyKey string) (*crawler.ImportJob, bool, error) {
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

	// Keyless submission: always a fresh job. No fingerprint is stored — a keyless
	// job can never be replayed, so it has nothing to be checked against.
	if idempotencyKey == "" {
		if err := im.jobs.Create(ctx, job); err != nil {
			return nil, false, fmt.Errorf("importer: creating import job: %w", err)
		}
		im.launch(*job, payload)
		return job, false, nil
	}

	// Keyed submission: the fingerprint lets the repository tell a safe retry
	// (same key + same request -> replay) from a dangerous key reuse (same key,
	// different request -> conflict).
	job.IdempotencyKey = idempotencyKey
	job.RequestFingerprint = fingerprintRequest(payload, dryRun)
	stored, replay, err := im.jobs.CreateWithKey(ctx, job)
	if err != nil {
		if errors.Is(err, crawler.ErrIdempotencyKeyConflict) {
			return nil, false, err // surfaced verbatim so the API answers 422
		}
		return nil, false, fmt.Errorf("importer: creating import job with key: %w", err)
	}
	if replay {
		// The original submission already owns this job's execution; do not launch
		// a second run. Return the stored job so the API answers 200.
		return stored, true, nil
	}
	im.launch(*stored, payload)
	return stored, false, nil
}

// launch starts the async execution of a freshly-created job, pairing the
// wait-group increment with the goroutine so Shutdown can drain it. The job is
// passed by value: the goroutine owns its copy and never races the caller's.
func (im *Importer) launch(job crawler.ImportJob, payload []byte) {
	im.wg.Add(1)
	go im.run(job, payload)
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

	// The merge executor gates its Catalog writes on job.DryRun: a dry run runs
	// the identical validation loop and counts would-upserts without writing,
	// while a real job lands the merges. The default executor ignores dryRun and
	// only validates.
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

// executeImport is the validate-only default Executor: it counts would-upserts
// and collects per-line data errors without any Catalog write path. The server
// injects NewMergeExecutor instead; this remains the zero-dependency default
// (used by tests and any Importer constructed without WithExecutor). It ignores
// ctx and dryRun — validation is identical for dry-run and real jobs.
func executeImport(_ context.Context, payload []byte, _ bool) (crawler.ImportResult, error) {
	return runImportLoop(payload, func(rec catalog.ResolvedRecord) (int, error) {
		return len(rec.Pages), nil
	})
}

// NewMergeExecutor builds the Catalog-merging Executor for real imports
// (ADR-0013). For a non-dry-run job it merges each resolved line's Company and
// then its valid Career Pages into the Catalog; for a dry run it runs the
// identical validation loop and counts would-upserts without writing. A merge
// error is an infrastructure failure that fails the whole job (ADR-0013's
// monotone merges make the partially-applied file a converging state — a
// re-upload completes it). Injected via WithExecutor at the server wiring site.
func NewMergeExecutor(companies crawler.CompanyRepository, pages crawler.CareerPageRepository) Executor {
	return func(ctx context.Context, payload []byte, dryRun bool) (crawler.ImportResult, error) {
		return runImportLoop(payload, func(rec catalog.ResolvedRecord) (int, error) {
			if dryRun {
				return len(rec.Pages), nil // validate only; no writes
			}
			company := toCompanyMerge(rec.Company)
			if err := companies.MergeImport(ctx, company); err != nil {
				return 0, fmt.Errorf("importer: merging company %q: %w", rec.Company.CompanyKey, err)
			}
			for _, p := range rec.Pages {
				if err := pages.MergeImport(ctx, toPageMerge(company.ID, p)); err != nil {
					return 0, fmt.Errorf("importer: merging career page %q: %w", p.URL, err)
				}
			}
			return len(rec.Pages), nil
		})
	}
}

// toCompanyMerge renders a resolved import Company as a merge instruction.
// Website / WebsitePresent are deliberately omitted: the company.website column
// and its presence-wins write are #88's scope.
func toCompanyMerge(c catalog.ResolvedCompany) *crawler.CompanyMerge {
	return &crawler.CompanyMerge{
		CompanyKey:           c.CompanyKey,
		ATSProvider:          c.ATSProvider,
		ATSProviderPresent:   c.ATSProviderPresent,
		DisplayDomain:        c.DisplayDomain,
		DisplayDomainPresent: c.DisplayDomainPresent,
		Name:                 c.Name,
		NamePresent:          c.NamePresent,
		FirstSeen:            c.FirstSeen,
		LastSeen:             c.LastSeen,
	}
}

// toPageMerge renders a resolved import Career Page as a merge instruction under
// its (already-merged) Company's id.
func toPageMerge(companyID uuid.UUID, p catalog.ResolvedPage) *crawler.CareerPageMerge {
	return &crawler.CareerPageMerge{
		CompanyID:        companyID,
		URL:              p.URL,
		PolitenessDomain: p.PolitenessDomain,
		FirstSeen:        p.FirstSeen,
		LastSeen:         p.LastSeen,
	}
}

// runImportLoop scans an NDJSON payload line by line (ADR-0015), decodes and
// resolves each record down the Identity Ladder (ADR-0013), and calls apply to
// land it. apply returns the number of Career Pages that landed, or a non-nil
// error for an infrastructure failure that fails the whole job. Decode/resolve
// data errors and a resolved record's per-page (sub-line best-effort) errors are
// collected and capped; blank lines are skipped without counting.
func runImportLoop(payload []byte, apply func(catalog.ResolvedRecord) (int, error)) (crawler.ImportResult, error) {
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
		pagesUpserted, err := apply(resolved)
		if err != nil {
			return crawler.ImportResult{}, err // infrastructure error fails the whole job
		}
		result.CompaniesUpserted++
		result.PagesUpserted += pagesUpserted
		for _, perr := range resolved.PageErrors {
			addErr(perr.Error()) // sub-line best-effort: bad page, company + valid pages still landed
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
