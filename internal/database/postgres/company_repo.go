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

// List returns every catalogued Company, most-recently-seen first. ats_provider
// is nullable in the schema (self-hosted companies store NULL); a NULL is
// surfaced as the empty string, matching how Upsert encodes it.
func (r *CompanyRepository) List(ctx context.Context) ([]*crawler.Company, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, company_key, ats_provider, display_domain, name, first_seen, last_seen
		FROM company ORDER BY last_seen DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing companies: %w", err)
	}
	defer rows.Close()

	companies := []*crawler.Company{}
	for rows.Next() {
		c := &crawler.Company{}
		var atsProvider *string
		if err := rows.Scan(
			&c.ID, &c.CompanyKey, &atsProvider, &c.DisplayDomain, &c.Name,
			&c.FirstSeen, &c.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning company: %w", err)
		}
		if atsProvider != nil {
			c.ATSProvider = *atsProvider
		}
		companies = append(companies, c)
	}

	return companies, rows.Err()
}
