package ats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*AshbyFetcher)(nil)

const (
	// ProviderAshby is the Ashby ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for an Ashby host (jobs.ashbyhq.com),
	// so seed-time routing can resolve it against the Registry (#127). This package
	// stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderAshby = "ashby"

	ashbyDefaultBaseURL = "https://api.ashbyhq.com"
	ashbyDefaultTimeout = 15 * time.Second
)

// AshbyFetcher reads an Ashby tenant's board through the public job-board API
// (api.ashbyhq.com/posting-api) and maps its postings to Job Listings. It makes
// no LLM call, and — unlike Greenhouse/Lever — needs no HTML-strip pass: Ashby
// ships a ready-made descriptionPlain alongside descriptionHtml, so the plain
// field maps straight to Description (ADR-0022/ADR-0023).
type AshbyFetcher struct {
	baseURL    string
	httpClient *http.Client
}

// AshbyFetcherOption configures an AshbyFetcher at construction.
type AshbyFetcherOption func(*AshbyFetcher)

// WithAshbyBaseURL overrides the job-board-API base URL, chiefly so tests can
// point the fetcher at an httptest server.
func WithAshbyBaseURL(u string) AshbyFetcherOption {
	return func(a *AshbyFetcher) {
		a.baseURL = u
	}
}

// WithAshbyHTTPClient injects the HTTP client used for board requests, so the ATS
// Fetch lane can supply a rate-limited or instrumented client (#127).
func WithAshbyHTTPClient(c *http.Client) AshbyFetcherOption {
	return func(a *AshbyFetcher) {
		a.httpClient = c
	}
}

// NewAshbyFetcher builds an AshbyFetcher pointed at the public job-board API with
// a default-timeout HTTP client, overridable via options.
func NewAshbyFetcher(opts ...AshbyFetcherOption) *AshbyFetcher {
	a := &AshbyFetcher{
		baseURL:    ashbyDefaultBaseURL,
		httpClient: &http.Client{Timeout: ashbyDefaultTimeout},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Fetch returns the tenant's postings mapped to Job Listings. It requests the
// public GET job-board endpoint (the live-verified no-auth request shape) and
// sends NO Authorization header: the private jobPosting.list endpoint is
// deliberately avoided. It does not request includeCompensation: the mapper
// reads only descriptionPlain, so pulling compensation blocks would only inflate
// the response toward the shared size cap. A non-200 response yields
// ErrBoardStatus; a decode failure is wrapped. Company and CompanyKey are left
// empty for the ingest lane to stamp from the page's Owner (ADR-0022). An empty
// board yields an empty, non-nil slice.
func (a *AshbyFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: ashby: empty tenant slug")
	}

	endpoint := a.baseURL + "/posting-api/job-board/" + url.PathEscape(tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: ashby build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: ashby fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: ashby tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var resp ashbyJobsResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ats: ashby decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, j := range resp.Jobs {
		// jobUrl is the canonical posting URL the lane keys upserts on (#127); a
		// posting without one has no dedup key and cannot be saved, so skip it.
		if j.JobURL == "" {
			continue
		}
		listings = append(listings, mapAshbyJob(j))
	}
	return listings, nil
}

// ashbyJobsResponse is the job-board-API response envelope. Only the fields the
// mapper reads are declared; any others in the JSON are ignored by the decoder.
type ashbyJobsResponse struct {
	Jobs []ashbyJob `json:"jobs"`
}

type ashbyJob struct {
	Title            string       `json:"title"`
	Location         string       `json:"location"` // human-readable string, e.g. "Europe"
	Address          ashbyAddress `json:"address"`  // structured fallback when location is empty
	Department       string       `json:"department"`
	IsRemote         bool         `json:"isRemote"`
	WorkplaceType    string       `json:"workplaceType"` // "Remote"/"Hybrid"/"Onsite"; fallback for isRemote
	PublishedAt      string       `json:"publishedAt"`   // RFC3339 (with fractional seconds)
	JobURL           string       `json:"jobUrl"`        // canonical posting URL (upsert key)
	DescriptionPlain string       `json:"descriptionPlain"`
}

// ashbyAddress is the structured location Ashby ships alongside the free-text
// location string. Only the country is read, as the fallback Location component
// when the top-level location string is empty (docs/research/ats-providers.md
// §Ashby field map: location/address→Location).
type ashbyAddress struct {
	PostalAddress struct {
		AddressCountry string `json:"addressCountry"`
	} `json:"postalAddress"`
}

// mapAshbyJob maps one job-board-API posting to a Job Listing. descriptionPlain
// is stored verbatim: Ashby already ships it as plain text (with meaningful
// paragraph newlines), so it needs neither an HTML strip nor a whitespace
// collapse — either would silently reformat the body. Company and CompanyKey are
// deliberately left empty: the ATS ingest lane stamps Company from the
// embedding/seed page's Owner (ADR-0022, #127) — never from an Ashby field.
func mapAshbyJob(j ashbyJob) *crawler.JobListing {
	// location is the primary human-readable string; the structured
	// address.postalAddress.addressCountry is the documented fallback when it is
	// empty.
	location := j.Location
	if location == "" {
		location = j.Address.PostalAddress.AddressCountry
	}
	// workplaceType is Ashby's positive signal ("Remote"/"Hybrid"/"Onsite"), so it
	// maps straight through the normalizer. isRemote only fills in when
	// workplaceType is absent: a bare isRemote never overrides a stated Onsite/Hybrid,
	// and its absence degrades to Unspecified, never Onsite (ADR-0030).
	arrangement := crawler.NormalizeWorkArrangement(j.WorkplaceType)
	if arrangement == crawler.WorkArrangementUnspecified && j.IsRemote {
		arrangement = crawler.WorkArrangementRemote
	}
	listing := &crawler.JobListing{
		Title:    j.Title,
		URL:      j.JobURL,
		Location: location,
		// address.postalAddress.addressCountry is the structured country signal; the
		// ingest lane resolves it to an ISO Country at save in preference to the
		// composed Location (ADR-0029). A region like "European Union" resolves to the
		// empty Country and is kept — the safe under-filtering direction.
		CountryHint:     j.Address.PostalAddress.AddressCountry,
		Description:     j.DescriptionPlain,
		WorkArrangement: arrangement,
		Department:      j.Department,
	}
	if t, ok := parseAshbyTime(j.PublishedAt); ok {
		listing.FirstPublished = t
	}
	return listing
}

// parseAshbyTime parses Ashby's RFC3339 publishedAt, tolerating the fractional-
// second variant the live board emits (e.g. "2021-04-27T20:13:45.158+00:00").
// ok is false (the caller keeps the zero time) on an empty or otherwise
// unparseable value — a bad timestamp must never drop a real posting.
func parseAshbyTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
