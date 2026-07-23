package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type CorpusRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.CorpusRepository = &CorpusRepository{}

func NewCorpusRepository(pool *pgxpool.Pool) *CorpusRepository {
	return &CorpusRepository{pool: pool}
}

// Save upserts jl into the Corpus keyed on canonical_url (ADR-0034). On conflict
// the mutable fields + identity/lane/hash are refreshed, last_seen advances, and
// closed_at is cleared so a returning posting reopens in place (ADR-0035);
// first_seen is preserved. career_page_id is written NULL when unknown (uuid.Nil).
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
			company_key      = EXCLUDED.company_key,
			country          = EXCLUDED.country,
			last_seen        = now(),
			closed_at        = NULL
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
