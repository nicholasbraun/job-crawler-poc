package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	_, err := r.pool.Exec(ctx, `
		INSERT INTO job_listing
			(definition_id, url, company, title, description, location, work_arrangement, content_hash, company_key, country)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (definition_id, url) DO UPDATE SET
			company = EXCLUDED.company,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			location = EXCLUDED.location,
			work_arrangement = EXCLUDED.work_arrangement,
			content_hash = EXCLUDED.content_hash,
			company_key = EXCLUDED.company_key,
			country = EXCLUDED.country,
			last_seen = now()
		`,
		definitionID, jl.URL, jl.Company, jl.Title, jl.Description,
		// Pass the underlying string, not the named WorkArrangement type, to avoid
		// any pgx encode ambiguity for a named string type.
		jl.Location, string(jl.WorkArrangement), contentHash(jl), jl.CompanyKey, jl.Country,
	)
	if err != nil {
		return fmt.Errorf("postgres: error saving job listing: %w", err)
	}

	return nil
}

func (r *JobListingRepository) Find(ctx context.Context) ([]*crawler.JobListing, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT company, url, title, description, location, work_arrangement, company_key, country
		FROM job_listing
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error finding job listings: %w", err)
	}
	defer rows.Close()

	jobListings := []*crawler.JobListing{}
	for rows.Next() {
		jl := &crawler.JobListing{}
		// Scan the column into an intermediate string, then normalize: this sidesteps
		// pgx named-type scan questions and defensively folds any legacy DB value onto
		// the enum (ADR-0030).
		var wa string
		if err := rows.Scan(
			&jl.Company, &jl.URL, &jl.Title, &jl.Description,
			&jl.Location, &wa, &jl.CompanyKey, &jl.Country,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning job listing: %w", err)
		}
		jl.WorkArrangement = crawler.NormalizeWorkArrangement(wa)
		jobListings = append(jobListings, jl)
	}

	return jobListings, rows.Err()
}

// FindByDefinition returns the listings saved under definitionID, most-recently
// -seen first. A non-empty keyword filters to listings whose title or
// description contains it, case-insensitively (an ILIKE substring match); an
// empty keyword returns all of the definition's listings.
func (r *JobListingRepository) FindByDefinition(ctx context.Context, definitionID uuid.UUID, keyword string) ([]*crawler.JobListing, error) {
	// The keyword branches the WHERE clause rather than always binding a
	// pattern, so the common "no keyword" case is a plain definition_id scan.
	query := `
		SELECT company, url, title, description, location, work_arrangement, company_key, country
		FROM job_listing
		WHERE definition_id = $1`
	args := []any{definitionID}
	if keyword != "" {
		// Escape LIKE metacharacters so a keyword's % and _ match literally
		// rather than as wildcards; the pattern still binds as a parameter.
		query += ` AND (title ILIKE $2 ESCAPE '\' OR description ILIKE $2 ESCAPE '\')`
		args = append(args, "%"+escapeLike(keyword)+"%")
	}
	query += ` ORDER BY last_seen DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: error finding job listings by definition: %w", err)
	}
	defer rows.Close()

	jobListings := []*crawler.JobListing{}
	for rows.Next() {
		jl := &crawler.JobListing{}
		// See Find: scan through an intermediate string, then normalize onto the enum.
		var wa string
		if err := rows.Scan(
			&jl.Company, &jl.URL, &jl.Title, &jl.Description,
			&jl.Location, &wa, &jl.CompanyKey, &jl.Country,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning job listing: %w", err)
		}
		jl.WorkArrangement = crawler.NormalizeWorkArrangement(wa)
		jobListings = append(jobListings, jl)
	}

	return jobListings, rows.Err()
}

// likeEscape rewrites the LIKE metacharacters \, % and _ into their escaped
// forms so a caller's keyword is matched literally under a `LIKE ... ESCAPE '\'`
// clause. The backslash must be replaced first, or it would double-escape the
// backslashes introduced for % and _.
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike escapes the LIKE metacharacters in keyword. See likeEscape.
func escapeLike(keyword string) string {
	return likeEscape.Replace(keyword)
}

// contentHash returns a hex-encoded SHA-256 over the listing's meaningful
// fields, so an upsert can record whether the extracted content changed.
func contentHash(jl *crawler.JobListing) string {
	parts := []string{
		jl.Title,
		jl.Description,
		jl.Company,
		jl.Location,
		string(jl.WorkArrangement),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
