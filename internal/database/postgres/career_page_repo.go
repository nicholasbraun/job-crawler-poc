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

type CareerPageRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.CareerPageRepository = &CareerPageRepository{}

func NewCareerPageRepository(pool *pgxpool.Pool) *CareerPageRepository {
	return &CareerPageRepository{pool: pool}
}

// Upsert inserts p keyed on (company_id, url). On conflict politeness_domain is
// refreshed and last_seen advanced; first_seen is preserved from the original
// insert. Re-discovery revives a dormant page: the conflict path resets
// consecutive_failures to 0 and stamps last_ok = now(), so a re-classified page
// reappears in ListCollectionSeeds (ADR-0035).
func (r *CareerPageRepository) Upsert(ctx context.Context, p *crawler.CareerPage) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO career_page
			(company_id, url, politeness_domain)
		VALUES ($1, $2, $3)
		ON CONFLICT (company_id, url) DO UPDATE SET
			politeness_domain   = EXCLUDED.politeness_domain,
			last_seen           = now(),
			consecutive_failures = 0,
			last_ok             = now()
		`,
		p.CompanyID, p.URL, p.PolitenessDomain,
	)
	if err != nil {
		return fmt.Errorf("postgres: error upserting career page: %w", err)
	}

	return nil
}

// MergeImport merges an imported Career Page into the Catalog (ADR-0013). Not a
// Sighting: last_seen merges with GREATEST against the file timestamp only, so an
// import never advances it to now(); now() is the first-insert default. Timestamps
// merge monotonically, and on first insert first_seen is clamped to last_seen so a
// record carrying only a past lastSeen cannot create an inverted interval.
// politeness_domain (always derived, always present) is refreshed. Re-merging the
// same page changes no data.
func (r *CareerPageRepository) MergeImport(ctx context.Context, m *crawler.CareerPageMerge) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO career_page
			(company_id, url, politeness_domain, first_seen, last_seen)
		VALUES
			($1, $2, $3, LEAST(COALESCE($4, now()), COALESCE($5, now())), COALESCE($5, now()))
		ON CONFLICT (company_id, url) DO UPDATE SET
			politeness_domain = EXCLUDED.politeness_domain,
			first_seen        = LEAST(career_page.first_seen, $4),
			last_seen         = GREATEST(career_page.last_seen, $5)
		`,
		m.CompanyID, m.URL, m.PolitenessDomain, m.FirstSeen, m.LastSeen,
	)
	if err != nil {
		return fmt.Errorf("postgres: error merging import career page: %w", err)
	}
	return nil
}

// ListCollectionSeeds returns every non-dormant catalogued Career Page as a
// CollectionSeed (id + url + owning CompanyKey), most-recently-seen first, for a
// Collection Cycle to seed from (ADR-0036). Dormant is derived: a page with
// consecutive_failures at or above dormancyThreshold is excluded, so re-discovery
// (which resets the counter via Upsert) revives it. Never returns nil; an empty
// Catalog yields an empty slice.
func (r *CareerPageRepository) ListCollectionSeeds(ctx context.Context, dormancyThreshold int) ([]crawler.CollectionSeed, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.url, c.company_key
		FROM career_page p JOIN company c ON c.id = p.company_id
		WHERE p.consecutive_failures < $1
		ORDER BY p.last_seen DESC
		`, dormancyThreshold)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing collection seeds: %w", err)
	}
	defer rows.Close()

	seeds := []crawler.CollectionSeed{}
	for rows.Next() {
		var s crawler.CollectionSeed
		if err := rows.Scan(&s.CareerPageID, &s.URL, &s.CompanyKey); err != nil {
			return nil, fmt.Errorf("postgres: error scanning collection seed: %w", err)
		}
		seeds = append(seeds, s)
	}

	return seeds, rows.Err()
}

// RecordProbe folds one dormancy ProbeOutcome into a Career Page's counters via
// the pure NextDormancy reducer (ADR-0035). The read-modify-write runs in one
// transaction under FOR UPDATE so concurrent probes of the same page serialize.
// When the probe crosses the page into dormant on THIS call, it Closes the page's
// remaining Open Job Listings (both lanes — no source filter) in the same tx and
// reports the count; a page already dormant re-closes nothing. Returns the
// resulting DormancyResult. Mirrors CorpusRepository.ApplyCrawlProbe's tx shape.
func (r *CareerPageRepository) RecordProbe(ctx context.Context, careerPageID uuid.UUID, outcome crawler.ProbeOutcome, threshold int) (crawler.DormancyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return crawler.DormancyResult{}, fmt.Errorf("postgres: error beginning dormancy-probe tx: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded; the error is not actionable.
	defer func() { _ = tx.Rollback(ctx) }()

	var failures int
	var lastOK *time.Time
	err = tx.QueryRow(ctx, `
		SELECT consecutive_failures, last_ok
		FROM career_page WHERE id = $1 FOR UPDATE`,
		careerPageID,
	).Scan(&failures, &lastOK)
	if errors.Is(err, pgx.ErrNoRows) {
		return crawler.DormancyResult{}, fmt.Errorf("postgres: dormancy probe for unknown career page %q", careerPageID)
	}
	if err != nil {
		return crawler.DormancyResult{}, fmt.Errorf("postgres: error reading career page for dormancy probe: %w", err)
	}

	prevLastOK := time.Time{}
	if lastOK != nil {
		prevLastOK = *lastOK
	}
	nextFailures, nextLastOK := crawler.NextDormancy(failures, prevLastOK, outcome, time.Now())
	becameDormant := !crawler.Dormant(failures, threshold) && crawler.Dormant(nextFailures, threshold)

	if _, err = tx.Exec(ctx, `
		UPDATE career_page SET
			consecutive_failures = $2,
			last_ok              = $3
		WHERE id = $1`,
		careerPageID, nextFailures, nullTime(nextLastOK),
	); err != nil {
		return crawler.DormancyResult{}, fmt.Errorf("postgres: error applying dormancy probe: %w", err)
	}

	closed := 0
	if becameDormant {
		tag, cerr := tx.Exec(ctx, `
			UPDATE job_listing SET closed_at = now()
			WHERE career_page_id = $1 AND closed_at IS NULL`,
			careerPageID,
		)
		if cerr != nil {
			return crawler.DormancyResult{}, fmt.Errorf("postgres: error closing dormant page listings: %w", cerr)
		}
		closed = int(tag.RowsAffected())
	}

	if err = tx.Commit(ctx); err != nil {
		return crawler.DormancyResult{}, fmt.Errorf("postgres: error committing dormancy probe: %w", err)
	}

	return crawler.DormancyResult{
		ConsecutiveFailures: nextFailures,
		BecameDormant:       becameDormant,
		ClosedListings:      closed,
	}, nil
}

// nullTime maps a zero time.Time to a nil *time.Time so it writes SQL NULL, and a
// set time to itself. Keeps last_ok NULL until the first Alive probe stamps it.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Delete removes the Career Page with the given id. Deleting a row that does
// not exist is a no-op that returns nil, so a repeated Catalog Doctor pass over
// an already-cleaned Catalog stays idempotent.
func (r *CareerPageRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM career_page WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: error deleting career page: %w", err)
	}
	return nil
}

// Reattribute re-points the Career Page with the given id to companyID,
// correcting a mis-attributed owner (e.g. the join.com host-Company split). It
// changes only company_id; first_seen and last_seen are left untouched because
// a re-attribution is a correction, not a fresh sighting. Re-pointing a row that
// does not exist is a no-op that returns nil. If a (companyID, url) row already
// exists the UNIQUE(company_id, url) constraint rejects the move and the
// violation is returned wrapped — collapsing such duplicates into one row is the
// Catalog Doctor's merge step, not this primitive's responsibility.
func (r *CareerPageRepository) Reattribute(ctx context.Context, id, companyID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE career_page SET company_id = $2 WHERE id = $1`, id, companyID)
	if err != nil {
		return fmt.Errorf("postgres: error re-attributing career page: %w", err)
	}
	return nil
}

// FirstSeenByDay returns the per-UTC-day count of newly-catalogued Career Pages,
// ascending by day, days with no new pages omitted. It buckets on
// first_seen AT TIME ZONE 'UTC' so the day boundaries are fixed at UTC midnight
// regardless of the connection's session TimeZone — matching the pure transform
// that gap-fills and cumulates these counts into the sparkline. Cumulation is
// deliberately kept out of SQL (a plain GROUP BY here) so the fiddly logic stays
// unit-testable without a database.
func (r *CareerPageRepository) FirstSeenByDay(ctx context.Context) ([]crawler.DayCount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT date_trunc('day', first_seen AT TIME ZONE 'UTC') AS day, count(*)
		FROM career_page
		GROUP BY day
		ORDER BY day
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error counting career pages by day: %w", err)
	}
	defer rows.Close()

	counts := []crawler.DayCount{}
	for rows.Next() {
		var dc crawler.DayCount
		if err := rows.Scan(&dc.Day, &dc.Count); err != nil {
			return nil, fmt.Errorf("postgres: error scanning career page day count: %w", err)
		}
		counts = append(counts, dc)
	}

	return counts, rows.Err()
}

// List returns every catalogued Career Page as a full entity, most-recently-seen
// first. CompanyID is included so the dashboard can group pages under their
// Company; the dormancy counters (ConsecutiveFailures/LastOK) are surfaced so the
// dashboard can flag a stalling page (ADR-0035).
func (r *CareerPageRepository) List(ctx context.Context) ([]*crawler.CareerPage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, company_id, url, politeness_domain, first_seen, last_seen,
		       consecutive_failures, last_ok
		FROM career_page ORDER BY last_seen DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing career pages: %w", err)
	}
	defer rows.Close()

	pages := []*crawler.CareerPage{}
	for rows.Next() {
		p := &crawler.CareerPage{}
		var lastOK *time.Time
		if err := rows.Scan(
			&p.ID, &p.CompanyID, &p.URL, &p.PolitenessDomain,
			&p.FirstSeen, &p.LastSeen, &p.ConsecutiveFailures, &lastOK,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning career page: %w", err)
		}
		if lastOK != nil {
			p.LastOK = *lastOK
		}
		pages = append(pages, p)
	}

	return pages, rows.Err()
}
