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

// DayCount is a single UTC-day bucket of newly-catalogued Career Pages: Count
// pages whose FirstSeen falls on Day (truncated to UTC midnight). It is the raw
// per-day tally the Catalog History sparkline is reconstructed from; the
// cumulation and gap-filling live in a pure transform, not the query.
type DayCount struct {
	Day   time.Time
	Count int
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

	// FirstSeenByDay returns how many Career Pages were first catalogued on each
	// UTC day, ascending by day, with days that catalogued nothing omitted. It
	// backs the Catalog History sparkline (see the Catalog History term in
	// CONTEXT.md): because it derives from surviving rows' FirstSeen, the trend
	// it reconstructs is revisionist — pages the Catalog Doctor later removes
	// drop out of the whole history. It never returns nil; an empty Catalog
	// yields an empty slice.
	FirstSeenByDay(ctx context.Context) ([]DayCount, error)
}

// RawCareerPage is a candidate Career Page emitted by the discovery pool for
// the career-page pool to confirm and catalogue. It carries the parsed page so
// the downstream worker can identify the company. Certain reports whether the
// candidate is a structurally-confirmed Career Page (an ATS board root); when
// true the career-page pool skips the LLM confirmation, bounding cost at
// perpetual scale. A false Certain (a content-heuristic match on an unrecognized
// host) is confirmed by the LLM before it is catalogued -- including a page
// carrying a schema.org JobPosting JSON-LD, which marks a single posting rather
// than a hub and so must clear the confirmer like any other candidate.
type RawCareerPage struct {
	URL     URL
	Content Content
	Certain bool
}
