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
	// ConsecutiveFailures is how many consecutive Cycles found this page hard-dead
	// (a 404 board, or a page that no longer classifies); at or above the dormancy
	// threshold the page is dormant (ADR-0035). Reset to 0 by re-discovery (Upsert).
	ConsecutiveFailures int
	// LastOK is when the page last probed reachable/alive. Zero until the first
	// successful Cycle reach.
	LastOK time.Time
}

// DormancyResult is the outcome of folding one dormancy probe into a Career Page
// (ADR-0035): the resulting consecutive-failure count, whether this probe crossed
// the page into dormant, and how many Open listings the dormancy cascade closed
// (0 unless BecameDormant).
type DormancyResult struct {
	ConsecutiveFailures int
	BecameDormant       bool
	ClosedListings      int
}

// DayCount is a single UTC-day bucket of newly-catalogued Career Pages: Count
// pages whose FirstSeen falls on Day (truncated to UTC midnight). It is the raw
// per-day tally the Catalog History sparkline is reconstructed from; the
// cumulation and gap-filling live in a pure transform, not the query.
type DayCount struct {
	Day   time.Time
	Count int
}

// CareerPageMerge is one import line's Career Page rendered as a merge
// instruction (ADR-0013), keyed on (CompanyID, URL). URL is the storage form and
// PolitenessDomain is derived from the URL host (never carried by the file), so
// both are always present. FirstSeen/LastSeen are nil when the file omitted them.
type CareerPageMerge struct {
	CompanyID        uuid.UUID
	URL              string
	PolitenessDomain string
	FirstSeen        *time.Time
	LastSeen         *time.Time
}

// CareerPageRepository persists Career Pages in the Catalog.
type CareerPageRepository interface {
	// Upsert inserts p or, when a row with the same (CompanyID, URL) already
	// exists, refreshes its mutable fields and advances last_seen while
	// preserving first_seen. Re-discovery revives a dormant page: the conflict
	// path resets consecutive_failures to 0 and stamps last_ok, so a re-classified
	// page reappears in ListCollectionSeeds (ADR-0035).
	Upsert(ctx context.Context, p *CareerPage) error

	// MergeImport lands an imported Career Page keyed on (CompanyID, URL)
	// (ADR-0013). Like the Company merge it is not a Sighting: last_seen never
	// advances to now on update; timestamps merge monotonically (LEAST/GREATEST)
	// and default to now() on first insert only, with first_seen clamped to
	// last_seen so a record carrying only a past lastSeen cannot create an
	// inverted interval. politeness_domain (always
	// present, derived from the URL host) is refreshed. Re-merging changes no data.
	MergeImport(ctx context.Context, m *CareerPageMerge) error

	// ListCollectionSeeds returns every NON-dormant Career Page as a CollectionSeed
	// (id + url + owning CompanyKey), for a Collection Cycle to seed from (ADR-0036).
	// Dormant is derived, not stored: a page with consecutive_failures at or above
	// dormancyThreshold is excluded, so re-discovery (which resets the counter) revives
	// it. Never returns nil; an empty Catalog yields an empty slice.
	ListCollectionSeeds(ctx context.Context, dormancyThreshold int) ([]CollectionSeed, error)

	// RecordProbe folds one dormancy ProbeOutcome into a Career Page's counters via
	// the pure NextDormancy reducer under FOR UPDATE (ADR-0035). When the probe crosses
	// the page into dormant on THIS call, it Closes the page's remaining Open Job
	// Listings (both lanes) in the SAME transaction and reports the count. A page
	// already dormant re-closes nothing. Returns the resulting DormancyResult.
	RecordProbe(ctx context.Context, careerPageID uuid.UUID, outcome ProbeOutcome, threshold int) (DormancyResult, error)

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
