package atsingest

import "github.com/google/uuid"

// FetchTask is one unit of ATS-Fetch work: pull a single tenant's board from an
// ATS provider's board API and attribute its postings to an Owner Company
// (ADR-0022). It is the ATS Fetch lane's pool work item. Collection routing
// produces one per routed Career-Page Seed (carrying its CareerPageID); the embed
// trigger (#129) produces more from boards embedded on crawled pages (with a Nil
// CareerPageID). Both submit through Lane.Submit, which dedups by (Provider,
// TenantSlug) so a tenant is fetched at most once a run.
type FetchTask struct {
	// Provider is the ATS provider family key (e.g. "greenhouse"), equal to the
	// value catalog.Identify emits and the Registry resolves a BoardFetcher by.
	Provider string
	// TenantSlug is the provider-scoped tenant identifier (e.g. "acme"): the board
	// API path segment the fetcher reads.
	TenantSlug string
	// Owner is the ADR-0021 Owner CompanyKey the fetched postings are attributed
	// to — the Catalog key of the Company whose Seed (or, in #129, embedding page)
	// produced this task. The lane stamps it onto every saved Job Listing; the
	// provider board's own company field is never used.
	Owner string
	// CareerPageID is the career_page.id this board was seeded from (ADR-0035):
	// stamped onto every saved posting, and the scope for the absence-sweep and the
	// dormancy probe. uuid.Nil for an embed-discovered board (no owning Career Page),
	// which is saved-only — no sweep, no dormancy.
	CareerPageID uuid.UUID
}
