package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ListingQuery is the concrete, engine-agnostic query a SavedSearch (or an ad-hoc
// search) runs against the Corpus (ADR-0037). It is a plain filter struct — keywords,
// countries, work arrangements, open-only, paging, sort — deliberately NOT a
// search-engine DSL, so the Postgres FTS backend can be swapped without touching
// callers. Every field is optional; the zero value means "every Open listing, newest
// activity first". Multiple keywords are AND-ed; multiple values within Countries /
// WorkArrangements are OR-ed.
type ListingQuery struct {
	Keywords         []string          // AND-ed free-text terms; fuzzy-tolerant (pg_trgm)
	Countries        []string          // ISO-3166-1 alpha-2; matched case-insensitively; empty = any
	WorkArrangements []WorkArrangement // empty = any working mode
	IncludeClosed    bool              // false (default) restricts to Open (closed_at IS NULL)
	Sort             ListingSort       // zero value = relevance
	Limit            int               // <= 0 applies the default page size
	Offset           int               // < 0 treated as 0
}

// ListingSort selects the result ordering of a ListingQuery.
type ListingSort string

const (
	// SortRelevance ranks by keyword-match strength (ts_rank over the weighted
	// tsvector) with a last_seen recency tiebreak; the default. With no Keywords it
	// degrades to pure recency.
	SortRelevance ListingSort = ""
	// SortRecent orders strictly by last_seen descending, ignoring relevance.
	SortRecent ListingSort = "recent"
	// SortFound orders strictly by first_seen descending — the newly-discovered
	// postings, ignoring relevance. Distinct from SortRecent: last_seen bumps on
	// every re-verification, so "recently found" (first_seen) is what a live
	// collection feed wants, not "recently re-seen".
	SortFound ListingSort = "found"
)

// CorpusListing is a persisted Corpus Job Listing projected for reading — what a
// SearchListings query returns and a SavedSearch panel renders (ADR-0037). It is
// distinct from JobListing (the extraction/ingest write DTO): it carries the row's
// persistence identity and Listing-Lifecycle timestamps a reader needs, and none of
// the LLM-unmarshaling json tags.
type CorpusListing struct {
	ID              uuid.UUID
	CanonicalURL    string
	URL             string // the source posting link, for provenance
	Title           string
	Description     string
	Company         string
	CompanyKey      string
	Department      string // team/department from the ATS board API; "" on the crawl lane (ADR-0022)
	Location        string // raw, unresolved location text (kept for display)
	Country         string // resolved ISO-3166-1 alpha-2; "" when unresolved
	WorkArrangement WorkArrangement
	Source          SourceLane
	CareerPageID    uuid.UUID // uuid.Nil when the row has no Career Page
	FirstSeen       time.Time
	LastSeen        time.Time
	ClosedAt        *time.Time // nil while Open (Listing Lifecycle, ADR-0035)
}

// CorpusSearchRepository is the read port over the Corpus (ADR-0037): it answers a
// ListingQuery with the matching Job Listings, ranked and paged. It is implemented by
// the same Postgres store as CorpusRepository / CorpusLivenessRepository, kept a
// separate interface so a query caller depends only on the read surface.
type CorpusSearchRepository interface {
	// SearchListings returns the Corpus listings matching q — keyword match over the
	// weighted title/description/company tsvector with a pg_trgm fuzzy tail, composed
	// with the country / work-arrangement / open-closed structured filters — ordered
	// per q.Sort (relevance with a recency tiebreak by default) and paged by
	// q.Limit/q.Offset. Open-only unless q.IncludeClosed. Never returns nil; no match
	// yields an empty slice.
	SearchListings(ctx context.Context, q ListingQuery) ([]*CorpusListing, error)
}
