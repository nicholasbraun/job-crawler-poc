package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type CompanyRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.CompanyRepository = &CompanyRepository{}

func NewCompanyRepository(pool *pgxpool.Pool) *CompanyRepository {
	return &CompanyRepository{pool: pool}
}

// Upsert inserts c keyed on company_key. On conflict the mutable fields are
// refreshed and last_seen advanced; first_seen is preserved from the original
// insert. The row's id (existing or freshly generated) is written back into
// c.ID. ats_provider is nullable — the empty string (self-hosted) is stored as
// SQL NULL so it stays queryable as "no ATS".
func (r *CompanyRepository) Upsert(ctx context.Context, c *crawler.Company) error {
	var atsProvider *string
	if c.ATSProvider != "" {
		atsProvider = &c.ATSProvider
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO company
			(company_key, ats_provider, display_domain, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (company_key) DO UPDATE SET
			ats_provider = EXCLUDED.ats_provider,
			display_domain = EXCLUDED.display_domain,
			name = EXCLUDED.name,
			last_seen = now()
		RETURNING id
		`,
		c.CompanyKey, atsProvider, c.DisplayDomain, c.Name,
	).Scan(&c.ID)
	if err != nil {
		return fmt.Errorf("postgres: error upserting company: %w", err)
	}

	return nil
}
