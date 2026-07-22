package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	var nameSource *string
	if c.NameSource != "" {
		s := string(c.NameSource)
		nameSource = &s
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO company
			(company_key, ats_provider, display_domain, name, name_source)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (company_key) DO UPDATE SET
			ats_provider = EXCLUDED.ats_provider,
			display_domain = EXCLUDED.display_domain,
			name = EXCLUDED.name,
			name_source = EXCLUDED.name_source,
			last_seen = now()
		RETURNING id
		`,
		c.CompanyKey, atsProvider, c.DisplayDomain, c.Name, nameSource,
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
// (self-hosted), and an explicit empty website is stored as NULL. On first
// insert first_seen is clamped to last_seen, so a record carrying only a past
// lastSeen (first_seen would default to now()) cannot create an inverted
// first_seen > last_seen interval. When a present name actually changes the
// stored value, name_source is reset to NULL (ADR-0025): the import carries no
// ladder rung, so a stale Discovery-derived source must not outlive the name it
// described; a same-valued or absent name leaves the source intact. Writes the
// merged row's id back into m.ID.
func (r *CompanyRepository) MergeImport(ctx context.Context, m *crawler.CompanyMerge) error {
	var atsProvider *string
	if m.ATSProvider != "" {
		atsProvider = &m.ATSProvider
	}
	var website *string
	if m.Website != "" {
		website = &m.Website
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO company
			(company_key, ats_provider, display_domain, name, website, first_seen, last_seen)
		VALUES
			($1, $2, $3, $4, $5, LEAST(COALESCE($6, now()), COALESCE($7, now())), COALESCE($7, now()))
		ON CONFLICT (company_key) DO UPDATE SET
			ats_provider   = CASE WHEN $8  THEN EXCLUDED.ats_provider   ELSE company.ats_provider   END,
			display_domain = CASE WHEN $9  THEN EXCLUDED.display_domain ELSE company.display_domain END,
			name           = CASE WHEN $10 THEN EXCLUDED.name           ELSE company.name           END,
			name_source    = CASE WHEN $10 AND EXCLUDED.name IS DISTINCT FROM company.name THEN NULL ELSE company.name_source END,
			website        = CASE WHEN $11 THEN EXCLUDED.website        ELSE company.website        END,
			first_seen     = LEAST(company.first_seen, $6),
			last_seen      = GREATEST(company.last_seen, $7)
		RETURNING id
		`,
		m.CompanyKey, atsProvider, m.DisplayDomain, m.Name, website, m.FirstSeen, m.LastSeen,
		m.ATSProviderPresent, m.DisplayDomainPresent, m.NamePresent, m.WebsitePresent,
	).Scan(&m.ID)
	if err != nil {
		return fmt.Errorf("postgres: error merging import company: %w", err)
	}
	return nil
}

// ListPagelessSeeds returns each Pageless Company's Website paired with its
// stored CompanyKey (the seed's Owner): a company row with a non-NULL website
// and no career_page. Ordered most-recently-seen first to mirror ListSeeds --
// ordering is not load-bearing, since the Frontier dedups and does not depend on
// seed order. The (website, company_key) column order matches the
// crawler.CatalogSeed (URL, CompanyKey) field order the positional collector
// binds by. Because the write path stores an empty Website as SQL NULL (the
// ats_provider idiom), `website IS NOT NULL` alone excludes the without-website
// case. Never returns nil; an empty result yields an empty slice.
func (r *CompanyRepository) ListPagelessSeeds(ctx context.Context) ([]crawler.CatalogSeed, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.website, c.company_key
		FROM company c
		WHERE c.website IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM career_page p WHERE p.company_id = c.id)
		ORDER BY c.last_seen DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing pageless company seeds: %w", err)
	}

	seeds, err := pgx.CollectRows(rows, pgx.RowToStructByPos[crawler.CatalogSeed])
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing pageless company seeds: %w", err)
	}

	return seeds, nil
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

// List returns every catalogued Company, most-recently-seen first. ats_provider,
// website, and name_source are nullable in the schema (self-hosted companies store
// a NULL ats_provider; a Company whose homepage is unknown stores a NULL website;
// a legacy/imported row stores a NULL name_source); each NULL is surfaced as the
// empty string, matching how the write path encodes it.
func (r *CompanyRepository) List(ctx context.Context) ([]*crawler.Company, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, company_key, ats_provider, display_domain, name, name_source, website, first_seen, last_seen
		FROM company ORDER BY last_seen DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing companies: %w", err)
	}
	defer rows.Close()

	companies := []*crawler.Company{}
	for rows.Next() {
		c := &crawler.Company{}
		var atsProvider, nameSource, website *string
		if err := rows.Scan(
			&c.ID, &c.CompanyKey, &atsProvider, &c.DisplayDomain, &c.Name, &nameSource, &website,
			&c.FirstSeen, &c.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning company: %w", err)
		}
		if atsProvider != nil {
			c.ATSProvider = *atsProvider
		}
		if nameSource != nil {
			c.NameSource = crawler.NameSource(*nameSource)
		}
		if website != nil {
			c.Website = *website
		}
		companies = append(companies, c)
	}

	return companies, rows.Err()
}
