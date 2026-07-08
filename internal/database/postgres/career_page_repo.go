package postgres

import (
	"context"
	"fmt"

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
