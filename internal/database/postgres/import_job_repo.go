package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type ImportJobRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.ImportJobRepository = &ImportJobRepository{}

func NewImportJobRepository(pool *pgxpool.Pool) *ImportJobRepository {
	return &ImportJobRepository{pool: pool}
}

// Create inserts a keyless job, assigning a fresh ID when job.ID is nil. The
// idempotency-key and request-fingerprint columns are deliberately omitted so
// they default to SQL NULL (keyed submission uses CreateWithKey).
func (r *ImportJobRepository) Create(ctx context.Context, job *crawler.ImportJob) error {
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}

	resultJSON, err := marshalResult(job.Result)
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO import_job
			(id, status, dry_run, filename, file_size, result, error, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`,
		job.ID, string(job.Status), job.DryRun, job.Filename, job.FileSize,
		resultJSON, job.Error, job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: error creating import job: %w", err)
	}

	return nil
}

// CreateWithKey inserts job keyed by its Idempotency-Key, arbitrating key reuse
// atomically via ON CONFLICT DO NOTHING (ADR-0014). Under concurrent same-key
// submissions exactly one INSERT lands (RowsAffected 1); the losers observe the
// conflict, load the winner, and return it as a replay when the fingerprints
// match or ErrIdempotencyKeyConflict when they differ. job.IdempotencyKey must
// be non-empty — the keyless path uses Create.
func (r *ImportJobRepository) CreateWithKey(ctx context.Context, job *crawler.ImportJob) (*crawler.ImportJob, bool, error) {
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}

	resultJSON, err := marshalResult(job.Result)
	if err != nil {
		return nil, false, err
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO import_job
			(id, status, dry_run, filename, file_size, result, error,
			 idempotency_key, request_fingerprint, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (idempotency_key) DO NOTHING
		`,
		job.ID, string(job.Status), job.DryRun, job.Filename, job.FileSize,
		resultJSON, job.Error, job.IdempotencyKey, job.RequestFingerprint,
		job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: error creating import job with key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return job, false, nil // this INSERT won: a fresh job
	}

	// The key already exists. Load the winner to tell a safe replay (same request
	// fingerprint) from a dangerous key reuse (different fingerprint).
	existing, err := r.getByKey(ctx, job.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if existing.RequestFingerprint != job.RequestFingerprint {
		return nil, false, crawler.ErrIdempotencyKeyConflict
	}
	return existing, true, nil
}

// getByKey loads the job carrying idempotencyKey (with its stored fingerprint),
// for CreateWithKey's conflict arbitration.
func (r *ImportJobRepository) getByKey(ctx context.Context, key string) (*crawler.ImportJob, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, status, dry_run, filename, file_size, result, error,
		       COALESCE(idempotency_key, ''), COALESCE(request_fingerprint, ''),
		       created_at, updated_at
		FROM import_job WHERE idempotency_key = $1
		`, key)
	job, err := scanImportJob(row)
	if err != nil {
		return nil, fmt.Errorf("postgres: error loading import job by key: %w", err)
	}
	return job, nil
}

func (r *ImportJobRepository) Get(ctx context.Context, id uuid.UUID) (*crawler.ImportJob, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, status, dry_run, filename, file_size, result, error,
		       COALESCE(idempotency_key, ''), COALESCE(request_fingerprint, ''),
		       created_at, updated_at
		FROM import_job WHERE id = $1
		`, id)

	job, err := scanImportJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, crawler.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: error getting import job: %w", err)
	}

	return job, nil
}

func (r *ImportJobRepository) List(ctx context.Context) ([]*crawler.ImportJob, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, status, dry_run, filename, file_size, result, error,
		       COALESCE(idempotency_key, ''), COALESCE(request_fingerprint, ''),
		       created_at, updated_at
		FROM import_job ORDER BY created_at DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing import jobs: %w", err)
	}
	defer rows.Close()

	jobs := []*crawler.ImportJob{}
	for rows.Next() {
		job, err := scanImportJob(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: error scanning import job: %w", err)
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// Update writes the mutable columns of a job (status, result, error, updated_at)
// by id — the pending->running and running->completed/failed transitions.
func (r *ImportJobRepository) Update(ctx context.Context, job *crawler.ImportJob) error {
	resultJSON, err := marshalResult(job.Result)
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		UPDATE import_job SET status = $2, result = $3, error = $4, updated_at = $5 WHERE id = $1
		`, job.ID, string(job.Status), resultJSON, job.Error, job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: error updating import job: %w", err)
	}

	return nil
}

// SweepInterrupted fails every job a dead process left pending or running,
// stamping msg and updated_at = at, and returns the number of rows changed.
func (r *ImportJobRepository) SweepInterrupted(ctx context.Context, msg string, at time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE import_job SET status = 'failed', error = $1, updated_at = $2
		WHERE status IN ('pending', 'running')
		`, msg, at)
	if err != nil {
		return 0, fmt.Errorf("postgres: error sweeping interrupted import jobs: %w", err)
	}

	return tag.RowsAffected(), nil
}

// marshalResult encodes an ImportResult as jsonb bytes, or nil (SQL NULL) when
// the job has no result yet.
func marshalResult(res *crawler.ImportResult) ([]byte, error) {
	if res == nil {
		return nil, nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("postgres: error encoding import result: %w", err)
	}
	return b, nil
}

func scanImportJob(row scanRow) (*crawler.ImportJob, error) {
	job := &crawler.ImportJob{}
	var status string
	var resultJSON []byte // a SQL NULL result scans to nil

	if err := row.Scan(
		&job.ID, &status, &job.DryRun, &job.Filename, &job.FileSize,
		&resultJSON, &job.Error, &job.IdempotencyKey, &job.RequestFingerprint,
		&job.CreatedAt, &job.UpdatedAt,
	); err != nil {
		return nil, err
	}

	job.Status = crawler.ImportJobStatus(status)
	if resultJSON != nil {
		var res crawler.ImportResult
		if err := json.Unmarshal(resultJSON, &res); err != nil {
			return nil, fmt.Errorf("postgres: error decoding import result: %w", err)
		}
		job.Result = &res
	}

	return job, nil
}
