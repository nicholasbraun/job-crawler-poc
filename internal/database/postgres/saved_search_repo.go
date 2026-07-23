package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type SavedSearchRepository struct {
	pool *pgxpool.Pool
}

var _ crawler.SavedSearchRepository = &SavedSearchRepository{}

func NewSavedSearchRepository(pool *pgxpool.Pool) *SavedSearchRepository {
	return &SavedSearchRepository{pool: pool}
}

// Create inserts ss, letting the DB own the id and created_at (RETURNING them onto
// ss). The three facet arrays bind natively to text[]; nil is coalesced to an empty
// slice first so a nil facet never becomes SQL NULL (the columns are NOT NULL).
func (r *SavedSearchRepository) Create(ctx context.Context, ss *crawler.SavedSearch) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO saved_search (name, keywords, countries, work_arrangements)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		ss.Name, coalesceStrings(ss.Keywords), coalesceStrings(ss.Countries),
		arrangementsToStrings(ss.WorkArrangements),
	).Scan(&ss.ID, &ss.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: error creating saved search: %w", err)
	}
	return nil
}

func (r *SavedSearchRepository) Get(ctx context.Context, id uuid.UUID) (*crawler.SavedSearch, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, keywords, countries, work_arrangements, created_at
		FROM saved_search WHERE id = $1`, id)

	ss, err := scanSavedSearch(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, crawler.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: error getting saved search: %w", err)
	}
	return ss, nil
}

// List returns every SavedSearch oldest-first (created_at, id tiebreak) so the
// dashboard panel order is stable and does not reshuffle when one is added. Never nil.
func (r *SavedSearchRepository) List(ctx context.Context) ([]*crawler.SavedSearch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, keywords, countries, work_arrangements, created_at
		FROM saved_search ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: error listing saved searches: %w", err)
	}
	defer rows.Close()

	searches := []*crawler.SavedSearch{}
	for rows.Next() {
		ss, err := scanSavedSearch(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: error scanning saved search: %w", err)
		}
		searches = append(searches, ss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: error listing saved searches: %w", err)
	}
	return searches, nil
}

// Rename sets a SavedSearch's name by id, returning ErrNotFound when no row matches
// (the query is fixed at creation; only the name is mutable in v1).
func (r *SavedSearchRepository) Rename(ctx context.Context, id uuid.UUID, name string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE saved_search SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return fmt.Errorf("postgres: error renaming saved search: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return crawler.ErrNotFound
	}
	return nil
}

// Delete removes a SavedSearch by id. Idempotent: deleting an already-absent row is
// not an error, so RowsAffected is intentionally ignored.
func (r *SavedSearchRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM saved_search WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: error deleting saved search: %w", err)
	}
	return nil
}

func scanSavedSearch(row scanRow) (*crawler.SavedSearch, error) {
	ss := &crawler.SavedSearch{}
	var arrangements []string
	if err := row.Scan(
		&ss.ID, &ss.Name, &ss.Keywords, &ss.Countries, &arrangements, &ss.CreatedAt,
	); err != nil {
		return nil, err
	}
	ss.WorkArrangements = stringsToArrangements(arrangements)
	return ss, nil
}

// coalesceStrings returns s, or an empty (non-nil) slice when s is nil, so a nil
// facet binds as an empty text[] rather than SQL NULL.
func coalesceStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// arrangementsToStrings converts a WorkArrangement slice to the underlying strings
// pgx binds to text[] (mirroring corpus.go's "pass the underlying strings" note).
func arrangementsToStrings(ws []crawler.WorkArrangement) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = string(w)
	}
	return out
}

// stringsToArrangements maps stored text[] values back to WorkArrangement. Never
// nil: an empty column yields an empty slice.
func stringsToArrangements(ss []string) []crawler.WorkArrangement {
	out := make([]crawler.WorkArrangement, len(ss))
	for i, s := range ss {
		out[i] = crawler.WorkArrangement(s)
	}
	return out
}
