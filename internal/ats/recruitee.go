package ats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*RecruiteeFetcher)(nil)

const (
	// ProviderRecruitee is the Recruitee ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Recruitee host (<tenant>.recruitee.com),
	// so seed-time routing can resolve it against the Registry (#127). This package
	// stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderRecruitee = "recruitee"

	// recruiteeDefaultBaseURL templates the per-tenant Careers Site host. Unlike
	// Greenhouse/Lever (tenant appended to a shared board host), Recruitee puts the
	// tenant in the SUBDOMAIN, so the base interpolates the "{tenant}" placeholder.
	recruiteeDefaultBaseURL = "https://{tenant}.recruitee.com"
	recruiteeDefaultTimeout = 15 * time.Second
	// recruiteeTimeLayout parses published_at/created_at, which Recruitee serves as
	// "2026-07-17 10:29:15 UTC" — space-separated with a zone abbreviation, NOT
	// RFC3339. time.Parse resolves the "UTC" abbreviation to a zero-offset instant.
	recruiteeTimeLayout = "2006-01-02 15:04:05 MST"
)

// RecruiteeFetcher reads a Recruitee tenant's board through the public Careers
// Site API (<tenant>.recruitee.com/api/offers/) and maps its offers to Job
// Listings. It makes no LLM call: the board API supplies every field but the
// free-text description, which is the offer's own HTML body reduced to plain text
// (ADR-0022/ADR-0023).
type RecruiteeFetcher struct {
	// baseURL templates the per-tenant host via a "{tenant}" placeholder. A test
	// override with no placeholder is left untouched, so an httptest base needs no
	// real subdomain.
	baseURL    string
	httpClient *http.Client
}

// RecruiteeFetcherOption configures a RecruiteeFetcher at construction.
type RecruiteeFetcherOption func(*RecruiteeFetcher)

// WithRecruiteeBaseURL overrides the Careers-Site base URL, chiefly so tests can
// point the fetcher at an httptest server. Any "{tenant}" placeholder in the value
// is substituted with the tenant slug at fetch time; a value with no placeholder
// is used verbatim.
func WithRecruiteeBaseURL(u string) RecruiteeFetcherOption {
	return func(r *RecruiteeFetcher) {
		r.baseURL = u
	}
}

// WithRecruiteeHTTPClient injects the HTTP client used for board requests, so the
// ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithRecruiteeHTTPClient(c *http.Client) RecruiteeFetcherOption {
	return func(r *RecruiteeFetcher) {
		r.httpClient = c
	}
}

// NewRecruiteeFetcher builds a RecruiteeFetcher pointed at the public Careers Site
// API with a default-timeout HTTP client, overridable via options.
func NewRecruiteeFetcher(opts ...RecruiteeFetcherOption) *RecruiteeFetcher {
	r := &RecruiteeFetcher{
		baseURL:    recruiteeDefaultBaseURL,
		httpClient: &http.Client{Timeout: recruiteeDefaultTimeout},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Fetch returns the tenant's offers mapped to Job Listings. It reads the public
// /api/offers/ endpoint, which replies with the full offers array in one payload
// (no pagination cursor observed; very large boards are not stress-tested and are
// bounded by the shared maxBoardBytes ceiling). A non-200 response yields
// ErrBoardStatus; a decode failure is wrapped. Company and CompanyKey are left
// empty for the ingest lane to stamp from the page's Owner (ADR-0022). An empty
// board yields an empty, non-nil slice.
func (r *RecruiteeFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		// Guard before templating the host, or the default would build the bogus
		// "https://.recruitee.com".
		return nil, fmt.Errorf("ats: recruitee: empty tenant slug")
	}

	// The tenant is a trusted single DNS label (catalog.subdomainLabel) and goes
	// into the host, so it is NOT url.PathEscaped — escaping a host label is wrong.
	endpoint := strings.Replace(r.baseURL, "{tenant}", tenant, 1) + "/api/offers/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: recruitee build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: recruitee fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: recruitee tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var resp recruiteeOffersResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ats: recruitee decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, o := range resp.Offers {
		// careers_url is the canonical posting URL the lane keys upserts on (#127) —
		// often a custom domain, which is fine for the key; attribution comes from the
		// Owner/seed (ADR-0022). An offer without one has no dedup key, so skip it.
		if o.CareersURL == "" {
			continue
		}
		listings = append(listings, mapRecruiteeOffer(o))
	}
	return listings, nil
}

// recruiteeOffersResponse is the board-API response envelope: {"offers":[...]}.
// Only the fields the mapper reads are declared; any others in the JSON (id, slug,
// remote, company_name, …) are ignored by the decoder.
type recruiteeOffersResponse struct {
	Offers []recruiteeOffer `json:"offers"`
}

type recruiteeOffer struct {
	Title        string                   `json:"title"`
	CareersURL   string                   `json:"careers_url"` // canonical posting URL (upsert key)
	Location     string                   `json:"location"`    // flattened primary location string
	Locations    []recruiteeOfferLocation `json:"locations"`   // structured locations
	Department   string                   `json:"department"`
	PublishedAt  string                   `json:"published_at"`
	Description  string                   `json:"description"`  // single-encoded HTML
	Requirements string                   `json:"requirements"` // single-encoded HTML
}

type recruiteeOfferLocation struct {
	Name        string `json:"name"`
	City        string `json:"city"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
}

// mapRecruiteeOffer maps one board-API offer to a Job Listing. Company and
// CompanyKey are deliberately left empty: the ATS ingest lane stamps Company from
// the embedding/seed page's Owner (ADR-0022, #127) — never from the offer's own
// company_name field. The offer's remote flag is not mapped (out of ticket scope),
// so WorkArrangement is Unspecified — a silent provider is never Onsite (ADR-0030);
// TechStack is not set (dropped in #125/ADR-0023).
func mapRecruiteeOffer(o recruiteeOffer) *crawler.JobListing {
	listing := &crawler.JobListing{
		Title:           o.Title,
		URL:             o.CareersURL,
		Location:        recruiteeLocation(o),
		CountryHint:     recruiteeCountryHint(o),
		Department:      o.Department,
		Description:     recruiteeDescription(o),
		WorkArrangement: crawler.WorkArrangementUnspecified,
	}
	if t, ok := parseRecruiteeTime(o.PublishedAt); ok {
		listing.FirstPublished = t
	}
	return listing
}

// recruiteeLocation resolves an offer's Location. The flat location string (e.g.
// "Remote job") is preferred when present; otherwise the first structured
// locations[] entry supplies its name, or — when that is empty — its non-empty
// city/state/country joined with ", ". Empty when the offer carries no location.
func recruiteeLocation(o recruiteeOffer) string {
	if o.Location != "" {
		return o.Location
	}
	if len(o.Locations) > 0 {
		loc := o.Locations[0]
		if loc.Name != "" {
			return loc.Name
		}
		parts := []string{}
		for _, p := range []string{loc.City, loc.State, loc.Country} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// recruiteeCountryHint surfaces the offer's structured country signal for the
// ingest lane to resolve at save (ADR-0029): the first structured location's
// country_code (an ISO code) is preferred, falling back to its country name.
// Empty when the offer carries no structured location — the lane then resolves
// from the composed Location instead.
func recruiteeCountryHint(o recruiteeOffer) string {
	if len(o.Locations) == 0 {
		return ""
	}
	loc := o.Locations[0]
	if loc.CountryCode != "" {
		return loc.CountryCode
	}
	return loc.Country
}

// recruiteeDescription reduces an offer's HTML body (description + requirements) to
// plain text. Recruitee serves these as single-encoded real HTML, so each field is
// stripped independently via htmlSingleEncodedToText — the same shape as Lever, not
// Greenhouse's double-encoded content. The non-empty results are joined with a
// newline so the section boundary survives as a line break.
func recruiteeDescription(o recruiteeOffer) string {
	parts := []string{}
	if s := htmlSingleEncodedToText(o.Description); s != "" {
		parts = append(parts, s)
	}
	if s := htmlSingleEncodedToText(o.Requirements); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// parseRecruiteeTime parses Recruitee's published_at ("2026-07-17 10:29:15 UTC").
// ok is false (the caller keeps the zero time) on an empty or otherwise unparseable
// value — a bad timestamp must never drop a real posting.
func parseRecruiteeTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(recruiteeTimeLayout, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
