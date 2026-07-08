package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CareerPage is a durable Catalog entry: a page that lists or links to a
// company's job openings. It is attributed to a Company (CompanyID) via
// ATS-aware identity, while its PolitenessDomain (the host) may be shared with
// other tenants on the same ATS. Used as a pointer type.
type CareerPage struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
	URL       string
	// PolitenessDomain is the host used for rate limiting (= URL.Hostname). It
	// can be shared across tenants of a multi-tenant ATS.
	PolitenessDomain string
	FirstSeen        time.Time
	LastSeen         time.Time
}

// CareerPageRepository persists Career Pages in the Catalog.
type CareerPageRepository interface {
	// Upsert inserts p or, when a row with the same (CompanyID, URL) already
	// exists, refreshes its mutable fields and advances last_seen while
	// preserving first_seen.
	Upsert(ctx context.Context, p *CareerPage) error

	// ListURLs returns every catalogued Career Page URL. A Keyword Crawl calls
	// this at run start to seed the Frontier from the Catalog.
	ListURLs(ctx context.Context) ([]string, error)

	// List returns every catalogued Career Page as a full entity (including
	// CompanyID so the dashboard can group pages under their Company),
	// most-recently-seen first. It never returns nil; an empty Catalog yields
	// an empty slice.
	List(ctx context.Context) ([]*CareerPage, error)
}

// RawCareerPage is a candidate Career Page emitted by the discovery pool for
// the career-page pool to confirm and catalogue. It carries the parsed page so
// the downstream worker can identify the company. Certain reports whether the
// candidate is a structurally-confirmed Career Page (an ATS board root); when
// true the career-page pool skips the LLM confirmation, bounding cost at
// perpetual scale. A false Certain (a content-heuristic match on an unrecognized
// host) is confirmed by the LLM before it is catalogued -- unless the page
// carries a schema.org JobPosting JSON-LD block, which is itself a definitive
// accept that also bypasses the LLM.
type RawCareerPage struct {
	URL     URL
	Content Content
	Certain bool
}
