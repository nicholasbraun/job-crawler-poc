package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type CrawlRunRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.CrawlRunRepository = &CrawlRunRepository{}

func NewCrawlRunRepository(pool *pgxpool.Pool) *CrawlRunRepository {
	return &CrawlRunRepository{pool: pool}
}

// Create inserts run. If run.ID is nil a fresh ID is assigned; started_at and
// the initial status/counters are written from the struct.
func (r *CrawlRunRepository) Create(ctx context.Context, run *crawler.CrawlRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO crawl_run
			(id, definition_id, status, pages_crawled, listings_found, started_at, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
		run.ID, run.DefinitionID, string(run.Status),
		run.Counters.PagesCrawled, run.Counters.ListingsFound,
		run.StartedAt, run.Error,
	)
	if err != nil {
		return fmt.Errorf("postgres: error creating crawl run: %w", err)
	}

	return nil
}

func (r *CrawlRunRepository) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlRun, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, definition_id, status, pages_crawled, listings_found, started_at, finished_at, error
		FROM crawl_run WHERE id = $1
		`, id)

	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, crawler.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: error getting crawl run: %w", err)
	}

	return run, nil
}

func (r *CrawlRunRepository) List(ctx context.Context) ([]*crawler.CrawlRun, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, definition_id, status, pages_crawled, listings_found, started_at, finished_at, error
		FROM crawl_run ORDER BY started_at DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing crawl runs: %w", err)
	}
	defer rows.Close()

	runs := []*crawler.CrawlRun{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: error scanning crawl run: %w", err)
		}
		runs = append(runs, run)
	}

	return runs, rows.Err()
}

func (r *CrawlRunRepository) GetStatus(ctx context.Context, id uuid.UUID) (crawler.RunStatus, error) {
	var status string
	err := r.pool.QueryRow(ctx, `SELECT status FROM crawl_run WHERE id = $1`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", crawler.ErrNotFound
		}
		return "", fmt.Errorf("postgres: error getting crawl run status: %w", err)
	}

	return crawler.RunStatus(status), nil
}

func (r *CrawlRunRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status crawler.RunStatus, finishedAt *time.Time, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE crawl_run SET status = $2, finished_at = $3, error = $4 WHERE id = $1
		`, id, string(status), finishedAt, errMsg)
	if err != nil {
		return fmt.Errorf("postgres: error updating crawl run status: %w", err)
	}

	return nil
}

// FailInterrupted marks every non-terminal run (running or stopping) as failed
// with errMsg and finished_at = now. On a single-server deployment a process
// that just started supervises no runs, so any such row is necessarily an
// orphan left by a previous process that crashed or was killed mid-run.
func (r *CrawlRunRepository) FailInterrupted(ctx context.Context, errMsg string) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE crawl_run
		SET status = $1, finished_at = now(), error = $2
		WHERE status IN ($3, $4)
		RETURNING id
		`,
		string(crawler.RunStatusFailed), errMsg,
		string(crawler.RunStatusRunning), string(crawler.RunStatusStopping),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: error failing interrupted runs: %w", err)
	}
	defer rows.Close()

	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: error scanning failed run id: %w", err)
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

func (r *CrawlRunRepository) UpdateCounters(ctx context.Context, id uuid.UUID, counters crawler.RunCounters) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE crawl_run SET pages_crawled = $2, listings_found = $3 WHERE id = $1
		`, id, counters.PagesCrawled, counters.ListingsFound)
	if err != nil {
		return fmt.Errorf("postgres: error updating crawl run counters: %w", err)
	}

	return nil
}

func scanRun(row scanRow) (*crawler.CrawlRun, error) {
	run := &crawler.CrawlRun{}
	var status string

	if err := row.Scan(
		&run.ID, &run.DefinitionID, &status,
		&run.Counters.PagesCrawled, &run.Counters.ListingsFound,
		&run.StartedAt, &run.FinishedAt, &run.Error,
	); err != nil {
		return nil, err
	}

	run.Status = crawler.RunStatus(status)
	return run, nil
}
