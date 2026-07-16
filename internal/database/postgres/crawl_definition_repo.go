package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type CrawlDefinitionRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.CrawlDefinitionRepository = &CrawlDefinitionRepository{}

func NewCrawlDefinitionRepository(pool *pgxpool.Pool) *CrawlDefinitionRepository {
	return &CrawlDefinitionRepository{pool: pool}
}

// Create inserts def, assigning it a fresh ID and created-at timestamp which
// are written back into def.
func (r *CrawlDefinitionRepository) Create(ctx context.Context, def *crawler.CrawlDefinition) error {
	if def.ID == uuid.Nil {
		def.ID = uuid.New()
	}

	// A nil slice encodes to SQL NULL, which violates the NOT NULL columns
	// (the default only applies when the column is omitted). Coalesce to empty.
	seedURLs := def.SeedURLs
	if seedURLs == nil {
		seedURLs = []string{}
	}
	keywords := def.Keywords
	if keywords == nil {
		keywords = []string{}
	}

	filterJSON, err := json.Marshal(def.URLFilter)
	if err != nil {
		return fmt.Errorf("postgres: error marshalling url filter: %w", err)
	}

	err = r.pool.QueryRow(ctx, `
		INSERT INTO crawl_definition
			(id, name, kind, seed_urls, keywords, max_depth, url_filter)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at
		`,
		def.ID, def.Name, string(def.Kind), seedURLs, keywords,
		def.MaxDepth, filterJSON,
	).Scan(&def.CreatedAt)
	if err != nil {
		if isUniqueViolation(err, "crawl_definition_single_discovery_idx") {
			return crawler.ErrDiscoveryDefinitionExists
		}
		return fmt.Errorf("postgres: error creating crawl definition: %w", err)
	}

	return nil
}

// Delete removes the definition by ID. Deleting a row that is not present is
// not an error, so the fused createCrawl rollback can call it unconditionally.
func (r *CrawlDefinitionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM crawl_definition WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: error deleting crawl definition: %w", err)
	}
	return nil
}

// AppendSeedURL idempotently adds url to the definition's seed_urls (ADR-0018).
// The CASE keeps it a single statement: a url already present leaves the array
// unchanged, so the row is still matched (RowsAffected == 1) and a missing
// definition (RowsAffected == 0) maps to ErrNotFound — distinguishing "already
// present" from "no such definition", which a bare WHERE-guarded append cannot.
func (r *CrawlDefinitionRepository) AppendSeedURL(ctx context.Context, id uuid.UUID, url string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE crawl_definition
		SET seed_urls = CASE
			WHEN $2 = ANY(seed_urls) THEN seed_urls
			ELSE array_append(seed_urls, $2)
		END
		WHERE id = $1
		`, id, url)
	if err != nil {
		return fmt.Errorf("postgres: error appending seed url: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return crawler.ErrNotFound
	}
	return nil
}

func (r *CrawlDefinitionRepository) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, kind, seed_urls, keywords, max_depth, url_filter, created_at
		FROM crawl_definition WHERE id = $1
		`, id)

	def, err := scanDefinition(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, crawler.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: error getting crawl definition: %w", err)
	}

	return def, nil
}

func (r *CrawlDefinitionRepository) List(ctx context.Context) ([]*crawler.CrawlDefinition, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, kind, seed_urls, keywords, max_depth, url_filter, created_at
		FROM crawl_definition ORDER BY created_at DESC
		`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing crawl definitions: %w", err)
	}
	defer rows.Close()

	defs := []*crawler.CrawlDefinition{}
	for rows.Next() {
		def, err := scanDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: error scanning crawl definition: %w", err)
		}
		defs = append(defs, def)
	}

	return defs, rows.Err()
}

// scanRow abstracts over *pgxpool.Row and pgx.Rows so a single scan helper
// serves both Get and List.
type scanRow interface {
	Scan(dest ...any) error
}

func scanDefinition(row scanRow) (*crawler.CrawlDefinition, error) {
	def := &crawler.CrawlDefinition{}
	var kind string
	var filterJSON []byte

	if err := row.Scan(
		&def.ID, &def.Name, &kind, &def.SeedURLs, &def.Keywords,
		&def.MaxDepth, &filterJSON, &def.CreatedAt,
	); err != nil {
		return nil, err
	}

	def.Kind = crawler.CrawlKind(kind)
	if err := json.Unmarshal(filterJSON, &def.URLFilter); err != nil {
		return nil, fmt.Errorf("postgres: error unmarshalling url filter: %w", err)
	}

	return def, nil
}
