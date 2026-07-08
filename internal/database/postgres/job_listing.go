package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type JobListingRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.JobListingRepository = &JobListingRepository{}

func NewJobListingRepository(pool *pgxpool.Pool) *JobListingRepository {
	return &JobListingRepository{pool: pool}
}

// Save upserts jl under definitionID, keyed (definition_id, url). On conflict
// the mutable fields and content_hash are refreshed and last_seen advanced;
// first_seen is preserved from the original insert. content_hash is derived
// from the listing's content so callers can later detect changes cheaply.
func (r *JobListingRepository) Save(ctx context.Context, definitionID uuid.UUID, jl *crawler.JobListing) error {
	// nil encodes to SQL NULL, which violates the NOT NULL tech_stack column
	// (the default only applies when the column is omitted). Coalesce to empty.
	techStack := jl.TechStack
	if techStack == nil {
		techStack = []string{}
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO job_listing
			(definition_id, url, company, title, description, location, remote, tech_stack, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (definition_id, url) DO UPDATE SET
			company = EXCLUDED.company,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			location = EXCLUDED.location,
			remote = EXCLUDED.remote,
			tech_stack = EXCLUDED.tech_stack,
			content_hash = EXCLUDED.content_hash,
			last_seen = now()
		`,
		definitionID, jl.URL, jl.Company, jl.Title, jl.Description,
		jl.Location, jl.Remote, techStack, contentHash(jl),
	)
	if err != nil {
		return fmt.Errorf("postgres: error saving job listing: %w", err)
	}

	return nil
}

func (r *JobListingRepository) Find(ctx context.Context) ([]*crawler.JobListing, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT company, url, title, description, location, remote, tech_stack
		FROM job_listing
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error finding job listings: %w", err)
	}
	defer rows.Close()

	jobListings := []*crawler.JobListing{}
	for rows.Next() {
		jl := &crawler.JobListing{}
		if err := rows.Scan(
			&jl.Company, &jl.URL, &jl.Title, &jl.Description,
			&jl.Location, &jl.Remote, &jl.TechStack,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning job listing: %w", err)
		}
		jobListings = append(jobListings, jl)
	}

	return jobListings, rows.Err()
}

// contentHash returns a hex-encoded SHA-256 over the listing's meaningful
// fields, so an upsert can record whether the extracted content changed.
func contentHash(jl *crawler.JobListing) string {
	parts := []string{
		jl.Title,
		jl.Description,
		jl.Company,
		jl.Location,
		strconv.FormatBool(jl.Remote),
		strings.Join(jl.TechStack, ","),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
