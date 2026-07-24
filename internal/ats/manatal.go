package ats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*ManatalFetcher)(nil)

const (
	// ProviderManatal is the Manatal ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Manatal host (a tenant on
	// <slug>.careers-page.com or the legacy www.careers-page.com/<slug> path), so
	// seed-time routing can resolve it against the Registry (#127). This package
	// stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderManatal = "manatal"

	// manatalDefaultBaseURL is the PUBLIC Open API base. The /open/v1 endpoints are
	// unauthenticated; never target /api/v1 — that is the JWT-Bearer recruiter API
	// and answers 403 without a token (docs/research/ats-providers.md §Manatal trap).
	manatalDefaultBaseURL = "https://api.careers-page.com"
	manatalDefaultTimeout = 15 * time.Second
	// manatalPageSize is the list window per request; the Open API documents size ≤ 250,
	// so the fetcher pages at the ceiling.
	manatalPageSize = 250
	// manatalFirstPage is the first page index. The Open API paginates 1-based: a
	// page=1 request echoes "page":1 (verified live), so enumeration starts at 1.
	manatalFirstPage = 1
	// manatalMaxPages bounds the pagination loop so a board that never advances past
	// its reported total cannot spin forever. 1000 pages * 250 = 250k postings, far
	// beyond any real tenant.
	manatalMaxPages = 1000
	// manatalPostingHost is the real-world board host the canonical posting URL is
	// built on. Kept a named const so the constructed upsert key is auditable and is
	// independent of the API baseURL (which a test overrides).
	manatalPostingHost = "careers-page.com"
)

// ManatalFetcher reads a Manatal tenant's board through the public Open API
// (api.careers-page.com/open/v1/career-pages/<slug>/job-posts) and maps its
// job-posts to Job Listings. It makes no LLM call: the paginated list inlines the
// title, location, department, and the id from which the canonical posting URL is
// built — everything needed to identify, dedup, save, and keyword-filter a posting
// (ADR-0022/ADR-0023).
//
// No detail call is made. The index omits only published_at, a soft enrichment
// field; fetching the per-posting detail (/open/v1/job-posts/<id>) for it alone is
// the unpaced #140 N+1 antipattern, worse here because the board's HTML host 429s
// under load, and buys nothing for identity/dedup/liveness — ADR-0035 keys liveness
// on first_seen/closed_at, not FirstPublished. So FirstPublished is left zero and
// the detail endpoint is a documented future enrichment, not built (Decision A).
//
// As a paginated board that reports a total, it computes completeness at runtime
// (ADR-0035): Fetch returns ErrBoardIncomplete alongside the partial slice when the
// list did not enumerate the whole reported total (a short/empty-before-total page
// or the pagination cap), or the final mapped count falls short of the total (also
// catching a posting dropped for an empty id).
type ManatalFetcher struct {
	baseURL    string
	httpClient *http.Client
	// maxPages bounds the pagination loop; exceeding it without reaching the reported
	// total marks the fetch ErrBoardIncomplete. Defaults to manatalMaxPages.
	maxPages int
}

// ManatalFetcherOption configures a ManatalFetcher at construction.
type ManatalFetcherOption func(*ManatalFetcher)

// WithManatalBaseURL overrides the Open-API base URL, chiefly so tests can point
// the fetcher at an httptest server. It overrides only the API base; the
// constructed canonical posting URL always uses the real careers-page.com host —
// that URL is the real-world upsert key, not an API address.
func WithManatalBaseURL(u string) ManatalFetcherOption {
	return func(m *ManatalFetcher) {
		m.baseURL = u
	}
}

// WithManatalHTTPClient injects the HTTP client used for board requests, so the
// ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithManatalHTTPClient(c *http.Client) ManatalFetcherOption {
	return func(m *ManatalFetcher) {
		m.httpClient = c
	}
}

// WithManatalMaxPages overrides the pagination cap (default manatalMaxPages).
// Exceeding the cap without reaching the reported total marks the fetch
// ErrBoardIncomplete; chiefly a test knob for the cap path.
func WithManatalMaxPages(n int) ManatalFetcherOption {
	return func(m *ManatalFetcher) {
		m.maxPages = n
	}
}

// NewManatalFetcher builds a ManatalFetcher pointed at the public Open API with a
// default-timeout HTTP client, overridable via options.
func NewManatalFetcher(opts ...ManatalFetcherOption) *ManatalFetcher {
	m := &ManatalFetcher{
		baseURL:    manatalDefaultBaseURL,
		httpClient: &http.Client{Timeout: manatalDefaultTimeout},
		maxPages:   manatalMaxPages,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Fetch returns the tenant's job-posts mapped to Job Listings. It pages the Open
// API list endpoint, which inlines every field the mapper needs, so no detail call
// follows. A non-200 on any page is board-level and yields ErrBoardStatus with a
// nil slice. Company and CompanyKey are left empty for the ingest lane to stamp
// from the page's Owner (ADR-0022). An empty board yields an empty, non-nil slice.
//
// Completeness (ADR-0035): Fetch returns the collected slice with ErrBoardIncomplete
// — never a nil slice — when the fetch cannot be proven to be the whole open board:
// the list did not enumerate the reported total (a short/empty page before the total
// or the pagination cap), or the final mapped count falls short of the total (also
// catching a posting dropped for an empty id). Any shortfall biases to
// keep-stale-open (skip-sweep), never a mass-close. A cancelled context still
// surfaces as a hard context error with a nil slice, ahead of the incomplete verdict.
func (m *ManatalFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: manatal: empty tenant slug")
	}

	items, total, listComplete, err := m.collectJobPosts(ctx, tenant)
	if err != nil {
		return nil, err
	}

	listings := []*crawler.JobListing{}
	for _, item := range items {
		// The canonical posting URL is CONSTRUCTED from the item id, so an item with
		// no id has no URL / dedup key and cannot be saved — skip it (mirrors the
		// greenhouse/SmartRecruiters "no upsert key → skip" rule).
		if item.ID == "" {
			continue
		}
		listings = append(listings, mapManatalJobPost(item, tenant))
	}

	// Completeness contract (ADR-0035): the sweep may run only on a provably complete
	// snapshot. The count cross-check (len(listings) < total) uniformly catches both a
	// short board and any item dropped for an empty id.
	if !listComplete || len(listings) < total {
		return listings, fmt.Errorf("ats: manatal tenant %q: %w", tenant, ErrBoardIncomplete)
	}
	return listings, nil
}

// collectJobPosts pages the list endpoint and returns every job-post in board
// order, the board's reported total, and whether the list was enumerated in full.
// It is hard-capped at m.maxPages. listComplete is true only when the accumulated
// count reaches the reported total; an empty page before the total (a short/
// mismatched board) or the cap being hit both yield listComplete=false so the
// caller can mark the fetch incomplete. The completeness oracle is `total`, not the
// echoed `pages`, so a lying `pages` cannot truncate enumeration. A non-200 aborts
// the whole fetch with ErrBoardStatus.
func (m *ManatalFetcher) collectJobPosts(ctx context.Context, tenant string) (items []manatalJobPost, total int, listComplete bool, err error) {
	items = []manatalJobPost{}
	page := manatalFirstPage
	for i := 0; i < m.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		// tenant is a slug in a PATH segment, so it is url.PathEscaped (as greenhouse does).
		endpoint := fmt.Sprintf("%s/open/v1/career-pages/%s/job-posts?page=%d&size=%d",
			m.baseURL, url.PathEscape(tenant), page, manatalPageSize)
		var resp manatalListResponse
		if err := m.getInto(ctx, endpoint, &resp); err != nil {
			return nil, 0, false, fmt.Errorf("ats: manatal list tenant %q: %w", tenant, err)
		}
		total = resp.Total
		items = append(items, resp.Items...)
		if len(items) >= resp.Total {
			// The whole reported board was enumerated (the common single-page case).
			return items, total, true, nil
		}
		if len(resp.Items) == 0 {
			// An empty page before reaching the total: the board delivered fewer job-posts
			// than it claimed (short/mismatch). Stop and report the shortfall.
			return items, total, false, nil
		}
		page++
	}
	// The pagination cap was hit without reaching total.
	return items, total, false, nil
}

// getInto issues a GET for endpoint and decodes a size-capped, non-200-guarded body
// into dst. It sends only an Accept header and NO Authorization/token header: the
// /open/v1 API is public, and the /api/v1 JWT-Bearer recruiter API is deliberately
// avoided (docs/research/ats-providers.md §Manatal trap). A non-200 wraps
// ErrBoardStatus so callers can errors.Is it. A truncated body surfaces as a decode
// error via io.LimitReader (the ADR-0035 truncation-as-decode-error guarantee).
func (m *ManatalFetcher) getInto(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("open/v1 status %d: %w", res.StatusCode, ErrBoardStatus)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// manatalListResponse is one page of the Open API list endpoint:
// {items, total, page, size, pages}. Total is the completeness oracle that bounds
// the loop; Pages is decoded only as a documented secondary cross-check and is not
// driven on. Any other envelope field is ignored by the decoder.
type manatalListResponse struct {
	Items []manatalJobPost `json:"items"`
	Total int              `json:"total"`
	Pages int              `json:"pages"` // secondary cross-check only; loop drives on Total
}

// manatalJobPost is a single job-post from the index. Only the fields the mapper
// reads are declared; any others in the JSON (status, salary, …) are ignored by the
// decoder. Deliberately no published_at field: the index omits it and no detail
// call is made (Decision A).
type manatalJobPost struct {
	ID           string               `json:"id"`           // stable posting id / UUID (Corpus SourceID, ADR-0034); also builds the URL
	Translations []manatalTranslation `json:"translations"` // localized name/description
	Location     manatalLocation      `json:"location"`
	Client       manatalClient        `json:"client"`
}

type manatalTranslation struct {
	Name        string `json:"name"`
	Description string `json:"description"` // single-encoded real HTML (verified live)
}

type manatalLocation struct {
	City    string `json:"city"`
	State   string `json:"state"`
	Country string `json:"country"`
}

type manatalClient struct {
	Name string `json:"name"`
}

// mapManatalJobPost maps one index job-post to a Job Listing. Company and CompanyKey
// are deliberately left empty: the ATS ingest lane stamps Company from the
// embedding/seed page's Owner (ADR-0022, #127) — never from client.name, which per
// the verified field map is the posting's department/team (re-confirmed live).
// TechStack is not set (dropped in #125/ADR-0023). FirstPublished is left zero — the
// index omits published_at and no detail call is made (Decision A). WorkArrangement
// is Unspecified: the index carries no working-mode field, and a silent provider is
// never Onsite (ADR-0030).
func mapManatalJobPost(item manatalJobPost, tenant string) *crawler.JobListing {
	return &crawler.JobListing{
		Title:    manatalTitle(item),
		URL:      manatalPostingURL(tenant, item.ID), // https://<tenant>.careers-page.com/jobs/<id> — canonical upsert key
		SourceID: item.ID,                            // re-slug-stable posting id (ADR-0034)
		Location: manatalLocationText(item.Location),
		// location.country is a structured country signal (a name, e.g. "Thailand");
		// the ingest lane resolves it at save (ADR-0029).
		CountryHint:     item.Location.Country,
		Department:      item.Client.Name, // per the verified field map (client.name → Department); NOT used as Company
		Description:     manatalDescription(item),
		WorkArrangement: crawler.WorkArrangementUnspecified,
	}
}

// manatalPostingURL builds the canonical posting URL on the REAL board host,
// regardless of any API baseURL override — the URL is the real-world upsert key, not
// an API address. tenant is a trusted single DNS label and id a UUID, so neither is
// url.PathEscaped (escaping a host label is wrong; mirrors the Recruitee reasoning).
func manatalPostingURL(tenant, id string) string {
	return "https://" + tenant + "." + manatalPostingHost + "/jobs/" + id
}

// manatalTitle returns the first non-empty translation name, or "" when none is
// present. A missing title never drops a posting — only a missing id does.
func manatalTitle(item manatalJobPost) string {
	for _, tr := range item.Translations {
		if tr.Name != "" {
			return tr.Name
		}
	}
	return ""
}

// manatalLocationText joins the location's non-empty city, state, and country with
// ", " (the sibling location-join idiom). Empty when the post carries no location.
func manatalLocationText(loc manatalLocation) string {
	parts := []string{}
	for _, p := range []string{loc.City, loc.State, loc.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// manatalDescription reduces the first non-empty translation description to plain
// text. Manatal serves it as single-encoded real HTML (literal tags plus text-level
// entities, verified live), so it is reduced via htmlSingleEncodedToText — the same
// shape as Lever/Recruitee, not Greenhouse's double-encoded content.
func manatalDescription(item manatalJobPost) string {
	for _, tr := range item.Translations {
		if tr.Description != "" {
			return htmlSingleEncodedToText(tr.Description)
		}
	}
	return ""
}
