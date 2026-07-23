package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SavedSearch is a named, stored query over the Corpus (ADR-0037 / CONTEXT
// "SavedSearch"): a persisted ListingQuery a user defines once and watches as a
// live dashboard panel. It has no owner and raises no notifications in v1 — it
// queries the Corpus, it never crawls.
type SavedSearch struct {
	ID               uuid.UUID
	Name             string
	Keywords         []string          // AND-ed free-text terms; empty = no keyword predicate
	Countries        []string          // ISO 3166-1 alpha-2, uppercase; empty = any country
	WorkArrangements []WorkArrangement // empty = any working mode
	CreatedAt        time.Time
}

// Query projects the SavedSearch's stored facets into the ListingQuery its panel
// runs against the Corpus. A SavedSearch never overrides sort or paging: it leaves
// the ListingQuery defaults (open-only, relevance-ranked with a recency tiebreak,
// default page size), so the panel shows the strongest open matches first (ADR-0037).
func (s *SavedSearch) Query() ListingQuery {
	return ListingQuery{
		Keywords:         s.Keywords,
		Countries:        s.Countries,
		WorkArrangements: s.WorkArrangements,
	}
}

// SavedSearchRepository persists SavedSearch aggregates (ADR-0037): CRUD only, no
// query execution (panels run SearchListings against the Corpus separately).
type SavedSearchRepository interface {
	// Create inserts ss, assigning its generated ID and CreatedAt back onto ss.
	Create(ctx context.Context, ss *SavedSearch) error
	// Get returns the SavedSearch with id, or ErrNotFound if none exists.
	Get(ctx context.Context, id uuid.UUID) (*SavedSearch, error)
	// List returns every SavedSearch, oldest first (a stable panel order that does
	// not reshuffle when a new search is added). Never nil.
	List(ctx context.Context) ([]*SavedSearch, error)
	// Rename sets the name of the SavedSearch with id, returning ErrNotFound when
	// no row has the id.
	Rename(ctx context.Context, id uuid.UUID, name string) error
	// Delete removes the SavedSearch with id. Idempotent: deleting a nonexistent
	// SavedSearch is not an error.
	Delete(ctx context.Context, id uuid.UUID) error
}
