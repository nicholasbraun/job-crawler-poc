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

type CorpusRepository struct {
	pool *pgxpool.Pool
}

var (
	_ crawler.CorpusRepository         = &CorpusRepository{}
	_ crawler.CorpusLivenessRepository = &CorpusRepository{}
)

func NewCorpusRepository(pool *pgxpool.Pool) *CorpusRepository {
	return &CorpusRepository{pool: pool}
}

// Save upserts jl into the Corpus keyed on canonical_url (ADR-0034). On conflict
// the mutable fields + identity/lane/hash are refreshed, last_seen advances, and
// closed_at is cleared so a returning posting reopens in place (ADR-0035);
// first_seen is preserved. A re-seen posting is confirmed alive, so the crawl-lane
// inconclusive_streak is reset to 0. career_page_id is written NULL when unknown
// (uuid.Nil).
func (r *CorpusRepository) Save(ctx context.Context, jl *crawler.JobListing) error {
	var careerPageID any
	if jl.CareerPageID != uuid.Nil {
		careerPageID = jl.CareerPageID
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO job_listing
			(canonical_url, url, source, source_id, source_hash, career_page_id,
			 company, title, description, location, work_arrangement, company_key, country)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (canonical_url) DO UPDATE SET
			url              = EXCLUDED.url,
			source           = EXCLUDED.source,
			source_id        = EXCLUDED.source_id,
			source_hash      = EXCLUDED.source_hash,
			career_page_id   = EXCLUDED.career_page_id,
			company          = EXCLUDED.company,
			title            = EXCLUDED.title,
			description      = EXCLUDED.description,
			location         = EXCLUDED.location,
			work_arrangement = EXCLUDED.work_arrangement,
			company_key         = EXCLUDED.company_key,
			country             = EXCLUDED.country,
			last_seen           = now(),
			closed_at           = NULL,
			inconclusive_streak = 0
		`,
		// Pass the underlying strings, not the named Source/WorkArrangement types, to
		// avoid any pgx encode ambiguity for a named string type.
		jl.CanonicalURL, jl.URL, string(jl.Source), jl.SourceID, jl.SourceHash, careerPageID,
		jl.Company, jl.Title, jl.Description, jl.Location, string(jl.WorkArrangement),
		jl.CompanyKey, jl.Country,
	)
	if err != nil {
		return fmt.Errorf("postgres: error saving job listing: %w", err)
	}

	return nil
}

// ListOpen returns every Open (closed_at IS NULL) listing under careerPageID,
// ordered by first_seen, carrying the identity/hash fields a liveness refetch needs.
// Never returns nil — an empty board yields an empty slice.
func (r *CorpusRepository) ListOpen(ctx context.Context, careerPageID uuid.UUID) ([]*crawler.JobListing, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT canonical_url, url, source, source_id, source_hash
		FROM job_listing
		WHERE career_page_id = $1 AND closed_at IS NULL
		ORDER BY first_seen`,
		careerPageID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing open listings: %w", err)
	}
	defer rows.Close()

	listings := []*crawler.JobListing{}
	for rows.Next() {
		jl := &crawler.JobListing{CareerPageID: careerPageID}
		var source string
		if err := rows.Scan(&jl.CanonicalURL, &jl.URL, &source, &jl.SourceID, &jl.SourceHash); err != nil {
			return nil, fmt.Errorf("postgres: error scanning open listing: %w", err)
		}
		jl.Source = crawler.SourceLane(source)
		listings = append(listings, jl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: error listing open listings: %w", err)
	}

	return listings, nil
}

// CloseAbsent runs the ATS absence-sweep for one board (ADR-0035). A partial or
// failed fetch (boardComplete=false) closes nothing — save-presence, skip-sweep —
// so a down provider never mass-closes a live board. On a complete fetch it closes
// every Open ATS listing under careerPageID not re-seen this Cycle (last_seen strictly
// before the Cycle watermark notSeenSince); source='ats' keeps the sweep from ever
// touching a crawl-lane listing sharing the page, and the careerPageID scope keeps it
// from touching a sibling board of the same Company. Returns the count closed.
func (r *CorpusRepository) CloseAbsent(ctx context.Context, careerPageID uuid.UUID, notSeenSince time.Time, boardComplete bool) (int, error) {
	if !boardComplete {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE job_listing
		SET closed_at = now()
		WHERE career_page_id = $1
		  AND source = 'ats'
		  AND closed_at IS NULL
		  AND last_seen < $2`,
		careerPageID, notSeenSince,
	)
	if err != nil {
		return 0, fmt.Errorf("postgres: error closing absent listings: %w", err)
	}

	return int(tag.RowsAffected()), nil
}

// ApplyCrawlProbe applies one crawl-lane refetch Outcome to the listing keyed on
// canonicalURL, driving the pure NextLiveness reducer so the SQL never re-derives the
// lifecycle rules. A direct crawl refetch is always authoritative, so boardComplete is
// true here; the ATS interlock is exercised by CloseAbsent's gate, never this path.
// The read-modify-write runs in one transaction under FOR UPDATE so concurrent probes
// of the same listing serialize. Only a probed listing is touched — an unprobed listing
// is never opened here, so a down collector closes nothing. Returns the resulting state.
func (r *CorpusRepository) ApplyCrawlProbe(ctx context.Context, canonicalURL string, outcome crawler.ProbeOutcome, staleThreshold int) (crawler.LifecycleState, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return crawler.LifecycleState{}, fmt.Errorf("postgres: error beginning crawl-probe tx: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded; the error is not actionable.
	defer func() { _ = tx.Rollback(ctx) }()

	var current crawler.LifecycleState
	err = tx.QueryRow(ctx, `
		SELECT (closed_at IS NULL), inconclusive_streak
		FROM job_listing WHERE canonical_url = $1 FOR UPDATE`,
		canonicalURL,
	).Scan(&current.Open, &current.InconclusiveStreak)
	if errors.Is(err, pgx.ErrNoRows) {
		return crawler.LifecycleState{}, fmt.Errorf("postgres: crawl probe for unknown listing %q", canonicalURL)
	}
	if err != nil {
		return crawler.LifecycleState{}, fmt.Errorf("postgres: error reading listing for crawl probe: %w", err)
	}

	next := crawler.NextLiveness(current, outcome, true, staleThreshold)

	if _, err = tx.Exec(ctx, `
		UPDATE job_listing SET
			inconclusive_streak = $2,
			closed_at           = CASE WHEN $3 THEN NULL ELSE COALESCE(closed_at, now()) END,
			last_seen           = CASE WHEN $4 THEN now() ELSE last_seen END
		WHERE canonical_url = $1`,
		canonicalURL, next.InconclusiveStreak, next.Open, outcome == crawler.ProbeAlive,
	); err != nil {
		return crawler.LifecycleState{}, fmt.Errorf("postgres: error applying crawl probe: %w", err)
	}

	if err = tx.Commit(ctx); err != nil {
		return crawler.LifecycleState{}, fmt.Errorf("postgres: error committing crawl probe: %w", err)
	}

	return next, nil
}
