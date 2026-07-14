package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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

// MergeImport merges an imported Company into the Catalog (ADR-0013). It is not a
// Sighting: last_seen is merged with GREATEST against the *file* timestamp only
// (never EXCLUDED, whose COALESCE-to-now() would re-introduce a live-sighting
// stamp), so an import with an absent or older lastSeen never advances it. now()
// is the first-insert default only. Mutable fields update only when the caller
// marks them present; an explicit empty ats_provider is stored as NULL
// (self-hosted). Writes the merged row's id back into m.ID.
func (r *CompanyRepository) MergeImport(ctx context.Context, m *crawler.CompanyMerge) error {
	var atsProvider *string
	if m.ATSProvider != "" {
		atsProvider = &m.ATSProvider
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO company
			(company_key, ats_provider, display_domain, name, first_seen, last_seen)
		VALUES
			($1, $2, $3, $4, COALESCE($5, now()), COALESCE($6, now()))
		ON CONFLICT (company_key) DO UPDATE SET
			ats_provider   = CASE WHEN $7 THEN EXCLUDED.ats_provider   ELSE company.ats_provider   END,
			display_domain = CASE WHEN $8 THEN EXCLUDED.display_domain ELSE company.display_domain END,
			name           = CASE WHEN $9 THEN EXCLUDED.name           ELSE company.name           END,
			first_seen     = LEAST(company.first_seen, $5),
			last_seen      = GREATEST(company.last_seen, $6)
		RETURNING id
		`,
		m.CompanyKey, atsProvider, m.DisplayDomain, m.Name, m.FirstSeen, m.LastSeen,
		m.ATSProviderPresent, m.DisplayDomainPresent, m.NamePresent,
	).Scan(&m.ID)
	if err != nil {
		return fmt.Errorf("postgres: error merging import company: %w", err)
	}
	return nil
}

// Delete removes the Company with the given id. Deleting a row that does not
// exist is a no-op that returns nil. The career_page.company_id foreign key has
// no ON DELETE CASCADE, so deleting a Company that still owns Career Pages is
// rejected by the database and the violation is returned wrapped; the Catalog
// Doctor must delete or re-attribute a Company's Career Pages before sweeping
// the orphaned Company.
func (r *CompanyRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM company WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: error deleting company: %w", err)
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
