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
			(id, name, kind, seed_urls, keywords, max_depth, max_domains, url_filter)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
		`,
		def.ID, def.Name, string(def.Kind), seedURLs, keywords,
		def.MaxDepth, def.MaxDomains, filterJSON,
	).Scan(&def.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: error creating crawl definition: %w", err)
	}

	return nil
}

func (r *CrawlDefinitionRepository) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, kind, seed_urls, keywords, max_depth, max_domains, url_filter, created_at
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
		SELECT id, name, kind, seed_urls, keywords, max_depth, max_domains, url_filter, created_at
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
		&def.MaxDepth, &def.MaxDomains, &filterJSON, &def.CreatedAt,
	); err != nil {
		return nil, err
	}

	def.Kind = crawler.CrawlKind(kind)
	if err := json.Unmarshal(filterJSON, &def.URLFilter); err != nil {
		return nil, fmt.Errorf("postgres: error unmarshalling url filter: %w", err)
	}

	return def, nil
}
