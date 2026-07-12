package postgres

import (
	"context"
	"fmt"

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
// insert.
func (r *CareerPageRepository) Upsert(ctx context.Context, p *crawler.CareerPage) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO career_page
			(company_id, url, politeness_domain)
		VALUES ($1, $2, $3)
		ON CONFLICT (company_id, url) DO UPDATE SET
			politeness_domain = EXCLUDED.politeness_domain,
			last_seen = now()
		`,
		p.CompanyID, p.URL, p.PolitenessDomain,
	)
	if err != nil {
		return fmt.Errorf("postgres: error upserting career page: %w", err)
	}

	return nil
}

// ListURLs returns every catalogued Career Page URL, most-recently-seen first.
// It never returns nil; an empty Catalog yields an empty slice.
func (r *CareerPageRepository) ListURLs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT url FROM career_page ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing career page urls: %w", err)
	}

	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing career page urls: %w", err)
	}

	return urls, nil
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

// List returns every catalogued Career Page as a full entity, most-recently-seen
// first. CompanyID is included so the dashboard can group pages under their
// Company.
func (r *CareerPageRepository) List(ctx context.Context) ([]*crawler.CareerPage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, company_id, url, politeness_domain, first_seen, last_seen
		FROM career_page ORDER BY last_seen DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing career pages: %w", err)
	}
	defer rows.Close()

	pages := []*crawler.CareerPage{}
	for rows.Next() {
		p := &crawler.CareerPage{}
		if err := rows.Scan(
			&p.ID, &p.CompanyID, &p.URL, &p.PolitenessDomain,
			&p.FirstSeen, &p.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning career page: %w", err)
		}
		pages = append(pages, p)
	}

	return pages, rows.Err()
}
