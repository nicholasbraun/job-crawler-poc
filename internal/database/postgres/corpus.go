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
	_ crawler.CorpusRepository            = &CorpusRepository{}
	_ crawler.CorpusLivenessRepository    = &CorpusRepository{}
	_ crawler.CorpusSearchRepository      = &CorpusRepository{}
	_ crawler.CorpusDescriptionRepository = &CorpusRepository{}
)

func NewCorpusRepository(pool *pgxpool.Pool) *CorpusRepository {
	return &CorpusRepository{pool: pool}
}

// Save upserts jl into the Corpus keyed on canonical_url (ADR-0034). On conflict
// the mutable fields + identity/lane/hash are refreshed, last_seen advances, and
// closed_at is cleared so a returning posting reopens in place (ADR-0035);
// first_seen is preserved. A re-seen posting is confirmed alive, so the crawl-lane
// inconclusive_streak is reset to 0. career_page_id is written NULL when unknown
// (uuid.Nil). department is a display-only ATS-lane attribute ("" on the crawl
// lane, ADR-0022) — stamped here but deliberately absent from search_tsv, as is
// description_source, which records where the stored description came from
// (ADR-0041) and IS refreshed on re-save: a re-extraction re-derives the body, so
// its provenance must follow. An unset marker fails the column's CHECK rather than
// writing a silent third state.
func (r *CorpusRepository) Save(ctx context.Context, jl *crawler.JobListing) error {
	var careerPageID any
	if jl.CareerPageID != uuid.Nil {
		careerPageID = jl.CareerPageID
	}
	// discovered_depth is crawl-lane-only instrumentation (migration 0024): NULL on
	// the ATS lane (no crawl depth), and never overwritten on re-save so it records
	// the depth at first discovery.
	var discoveredDepth any
	if jl.Source == crawler.SourceLaneCrawl {
		discoveredDepth = jl.DiscoveredDepth
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO job_listing
			(canonical_url, url, source, source_id, source_hash, career_page_id,
			 company, title, description, location, work_arrangement, company_key, country, department,
			 discovered_depth, description_source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
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
			department          = EXCLUDED.department,
			description_source  = EXCLUDED.description_source,
			last_seen           = now(),
			closed_at           = NULL,
			inconclusive_streak = 0
		`,
		// Pass the underlying strings, not the named Source/WorkArrangement/
		// DescriptionSource types, to avoid any pgx encode ambiguity for a named string
		// type.
		jl.CanonicalURL, jl.URL, string(jl.Source), jl.SourceID, jl.SourceHash, careerPageID,
		jl.Company, jl.Title, jl.Description, jl.Location, string(jl.WorkArrangement),
		jl.CompanyKey, jl.Country, jl.Department, discoveredDepth, string(jl.DescriptionSource),
	)
	if err != nil {
		return fmt.Errorf("postgres: error saving job listing: %w", err)
	}

	return nil
}

// ListOpen returns every Open (closed_at IS NULL) listing under careerPageID,
// ordered by first_seen, carrying the identity/hash fields a liveness refetch needs.
// company_key is included so a changed-page re-extraction can rebuild the Owner
// attribution (ADR-0021) from the listing's stored key, and description_source so the
// refetch heal (ADR-0041) can tell a legacy model-authored body from a real Posting
// Body without a second query per listing. The description TEXT is deliberately not
// projected — the heal rewrites it from the page it already fetched. Never returns
// nil — an empty board yields an empty slice.
func (r *CorpusRepository) ListOpen(ctx context.Context, careerPageID uuid.UUID) ([]*crawler.JobListing, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT canonical_url, url, source, source_id, source_hash, company_key, description_source
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
		var source, descriptionSource string
		if err := rows.Scan(&jl.CanonicalURL, &jl.URL, &source, &jl.SourceID, &jl.SourceHash,
			&jl.CompanyKey, &descriptionSource); err != nil {
			return nil, fmt.Errorf("postgres: error scanning open listing: %w", err)
		}
		jl.Source = crawler.SourceLane(source)
		jl.DescriptionSource = crawler.DescriptionSource(descriptionSource)
		listings = append(listings, jl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: error listing open listings: %w", err)
	}

	return listings, nil
}

// DeleteByCareerPage removes every Job Listing under careerPageID and returns the
// count. It serves the Catalog Doctor's Delete disposition (ADR-0011): a page the
// Doctor rejects was never a valid Career Page, so the crawl-lane listings beneath
// it are mis-attributed by construction and go with it. This is why the
// career_page_id FK is deliberately NOT ON DELETE SET NULL (migration 0027) --
// orphaning would leave them unreachable by ListOpen and therefore Open forever.
// Deleting under a page with no listings is a no-op returning 0.
func (r *CorpusRepository) DeleteByCareerPage(ctx context.Context, careerPageID uuid.UUID) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM job_listing WHERE career_page_id = $1`, careerPageID)
	if err != nil {
		return 0, fmt.Errorf("postgres: error deleting listings for career page: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RepointCareerPage moves every Job Listing under fromID to toID and returns the
// count. It serves the Catalog Doctor's Merge disposition: the loser row is the
// same page as the survivor, so its listings are VALID and must follow it rather
// than be deleted with it. Only career_page_id changes -- not closed_at, last_seen
// or the lifecycle counters -- because a merge is a correction, not a sighting.
//
// A listing whose canonical_url already exists under toID would violate the
// canonical_url UNIQUE constraint; the violation is returned wrapped rather than
// swallowed, since collapsing two Corpus rows is a lifecycle decision this
// primitive must not make silently.
func (r *CorpusRepository) RepointCareerPage(ctx context.Context, fromID, toID uuid.UUID) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE job_listing SET career_page_id = $2 WHERE career_page_id = $1`, fromID, toID)
	if err != nil {
		return 0, fmt.Errorf("postgres: error re-pointing listings to merge survivor: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CountByCareerPages returns the number of Job Listings under each of ids, keyed by
// Career Page id. Pages with no listings are absent from the map rather than zero.
// It backs the Catalog Doctor's dry-run report, so the blast radius of a plan is
// visible BEFORE --apply: a plan that deletes a page owning listings is exactly the
// case worth inspecting by hand. Returns an empty map for no ids.
func (r *CorpusRepository) CountByCareerPages(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	counts := map[uuid.UUID]int{}
	if len(ids) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT career_page_id, count(*)
		FROM job_listing
		WHERE career_page_id = ANY($1)
		GROUP BY career_page_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: error counting listings by career page: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("postgres: error scanning listing count: %w", err)
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: error counting listings by career page: %w", err)
	}
	return counts, nil
}

// UpdateDescription rewrites one listing's stored Posting Body and its Description
// Source marker, and touches nothing else (ADR-0041): not closed_at, last_seen or
// inconclusive_streak, so a heal can never resurrect a Closed posting nor forge a
// sighting. The STORED generated search_tsv is recomputed by Postgres from the new
// description, so a healed body becomes searchable with no extra work and no index
// change (migrations 0025/0026). Deliberately unguarded SQL — the heal decision (which
// markers are legacy) belongs to the refetch processor, the same policy/SQL split
// CloseAbsent and ApplyCrawlProbe follow. Reports ErrNotFound when no row carries
// canonicalURL, mirroring ApplyCrawlProbe.
func (r *CorpusRepository) UpdateDescription(ctx context.Context, canonicalURL, description string, source crawler.DescriptionSource) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE job_listing
		SET description = $2, description_source = $3
		WHERE canonical_url = $1`,
		// Pass the underlying string, not the named DescriptionSource type, to avoid
		// any pgx encode ambiguity for a named string type (as Save does).
		canonicalURL, description, string(source),
	)
	if err != nil {
		return fmt.Errorf("postgres: error updating listing description: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: description update for unknown listing %q: %w", canonicalURL, crawler.ErrNotFound)
	}

	return nil
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
		return crawler.LifecycleState{}, fmt.Errorf("postgres: crawl probe for unknown listing %q: %w", canonicalURL, crawler.ErrNotFound)
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
