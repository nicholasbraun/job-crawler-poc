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

var _ BoardFetcher = (*SoftgardenFetcher)(nil)

const (
	// ProviderSoftgarden is the softgarden ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a softgarden host
	// (<tenant>.career.softgarden.de), so seed-time routing can resolve it against the
	// Registry (#127). This package stays decoupled from catalog; the invariant is
	// enforced by the wiring point (pinned by TestSoftgardenCatalogRecognition).
	ProviderSoftgarden = "softgarden"

	// softgardenDefaultBaseURL templates the per-tenant board host via a "{tenant}"
	// placeholder. softgarden's tenant IS the FULL board host (a career.softgarden.de
	// subdomain OR a custom-domain CNAME), so the placeholder is the whole host — there
	// is no fixed suffix to append, unlike Recruitee's ".recruitee.com". A test override
	// with no placeholder is used verbatim.
	softgardenDefaultBaseURL = "https://{tenant}"
	softgardenDefaultTimeout = 15 * time.Second
)

// SoftgardenFetcher reads a softgarden tenant's board through the public
// /jobs.feed.json endpoint (a schema.org DataFeed) and maps its JobPostings to Job
// Listings. It makes no LLM call and sends no auth header: the public feed supplies
// every field but the free-text description, which is the posting's own single-
// encoded HTML reduced to plain text (ADR-0022/ADR-0023). The token/OAuth
// dev.softgarden.de Jobs API is deliberately NOT used.
type SoftgardenFetcher struct {
	// baseURL templates the per-tenant board host via a "{tenant}" placeholder. A test
	// override with no placeholder is left untouched, so an httptest base needs no real
	// host.
	baseURL    string
	httpClient *http.Client
}

// SoftgardenFetcherOption configures a SoftgardenFetcher at construction.
type SoftgardenFetcherOption func(*SoftgardenFetcher)

// WithSoftgardenBaseURL overrides the board base URL, chiefly so tests can point the
// fetcher at an httptest server. Any "{tenant}" placeholder is substituted with the
// board host at fetch time; a value with no placeholder is used verbatim.
func WithSoftgardenBaseURL(u string) SoftgardenFetcherOption {
	return func(s *SoftgardenFetcher) {
		s.baseURL = u
	}
}

// WithSoftgardenHTTPClient injects the HTTP client used for board requests, so the ATS
// Fetch lane can supply a rate-limited or instrumented client (#127).
func WithSoftgardenHTTPClient(c *http.Client) SoftgardenFetcherOption {
	return func(s *SoftgardenFetcher) {
		s.httpClient = c
	}
}

// NewSoftgardenFetcher builds a SoftgardenFetcher pointed at the public /jobs.feed.json
// endpoint with a default-timeout HTTP client, overridable via options.
func NewSoftgardenFetcher(opts ...SoftgardenFetcherOption) *SoftgardenFetcher {
	s := &SoftgardenFetcher{
		baseURL:    softgardenDefaultBaseURL,
		httpClient: &http.Client{Timeout: softgardenDefaultTimeout},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Fetch returns the tenant's postings mapped to Job Listings. The tenant is the FULL
// board host (a career.softgarden.de subdomain or a custom-domain CNAME), which is
// templated into the host and read from /jobs.feed.json — the whole open set in one
// payload (no pagination, no detail call; a very large board is bounded by the shared
// maxBoardBytes ceiling). No Authorization/token header is sent: the public feed is
// zero-auth, and the dev.softgarden.de token/OAuth API is deliberately avoided. A
// non-200 response yields ErrBoardStatus; a decode failure is wrapped. Company and
// CompanyKey are left empty for the ingest lane to stamp from the page's Owner
// (ADR-0022). An empty board yields an empty, non-nil slice.
//
// Completeness (ADR-0035): softgarden is single-shot, so the result is complete by
// construction — err == nil ⟹ the slice is the tenant's complete open set (safe to
// sweep). A truncated body is caught by io.LimitReader and surfaces as a decode error
// (a hard failure with a nil slice), never ErrBoardIncomplete.
func (s *SoftgardenFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		// Guard before templating the host, or the default would build the bogus
		// "https:///jobs.feed.json".
		return nil, fmt.Errorf("ats: softgarden: empty tenant slug")
	}

	// The tenant is a trusted host string that goes into the host, so it is NOT
	// url.PathEscaped — escaping a host is wrong (same reasoning as Recruitee).
	endpoint := strings.Replace(s.baseURL, "{tenant}", tenant, 1) + "/jobs.feed.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: softgarden build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")
	// No Authorization/token/X-Api-Key header: the /jobs.feed.json feed is zero-auth;
	// the token/OAuth dev.softgarden.de Jobs API is the trap we deliberately avoid.

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: softgarden fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: softgarden tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var feed softgardenFeed
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("ats: softgarden decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, el := range feed.DataFeedElement {
		// item.url is the canonical posting URL the lane keys upserts on (#127); a
		// posting without one has no dedup key and cannot be saved, so skip it.
		if el.Item.URL == "" {
			continue
		}
		listings = append(listings, mapSoftgardenItem(el.Item))
	}
	return listings, nil
}

// softgardenFeed is the /jobs.feed.json envelope: a schema.org DataFeed whose
// dataFeedElement wraps each JobPosting under an "item" key. Only the fields the
// mapper reads are declared; any others in the JSON are ignored by the decoder.
type softgardenFeed struct {
	DataFeedElement []softgardenElement `json:"dataFeedElement"`
}

type softgardenElement struct {
	Item softgardenItem `json:"item"`
}

type softgardenItem struct {
	Title       string                `json:"title"`
	URL         string                `json:"url"`         // canonical posting URL (upsert key)
	DatePosted  string                `json:"datePosted"`  // RFC3339 with fractional seconds + offset
	Description string                `json:"description"` // single-encoded HTML
	Identifier  softgardenIdentifier  `json:"identifier"`  // PropertyValue carrying the stable posting id
	JobLocation softgardenJobLocation `json:"jobLocation"`
}

// softgardenIdentifier is the schema.org PropertyValue. Value is the stable numeric
// posting id; json.Number renders the integer exactly and yields "" when absent.
type softgardenIdentifier struct {
	Value json.Number `json:"value"`
}

type softgardenJobLocation struct {
	Address softgardenAddress `json:"address"`
}

type softgardenAddress struct {
	StreetAddress   string `json:"streetAddress"`
	PostalCode      string `json:"postalCode"`
	AddressLocality string `json:"addressLocality"`
	AddressRegion   string `json:"addressRegion"`
	AddressCountry  string `json:"addressCountry"`
}

// mapSoftgardenItem maps one feed JobPosting to a Job Listing. Company and CompanyKey
// are deliberately left empty: the ATS ingest lane stamps Company from the
// embedding/seed page's Owner (ADR-0022, #127). softgarden has no department field, so
// Department stays empty (not invented). employmentType is an employment type
// (FULL_TIME/PART_TIME), not a work arrangement, and the "hybrid/remote" wording only
// appears inside the free-text description HTML, so WorkArrangement stays Unspecified —
// a silent provider is never Onsite (ADR-0030); TechStack is not set (ADR-0023).
//
// SourceID maps identifier.value — the stable numeric posting id — even though it is
// not in the ticket's literal field map: the canonical item.url embeds a mutable title
// slug, so without a stable SourceID a retitle would re-slug into a "new" listing under
// listingid.FromURL and the original would be absence-swept-closed (ADR-0034/ADR-0035).
func mapSoftgardenItem(item softgardenItem) *crawler.JobListing {
	listing := &crawler.JobListing{
		Title:           item.Title,
		URL:             item.URL,                       // canonical; the lane keys upserts on it (#127)
		SourceID:        item.Identifier.Value.String(), // stable numeric posting id (see doc)
		Location:        softgardenLocation(item.JobLocation.Address),
		CountryHint:     softgardenPlaceholder(item.JobLocation.Address.AddressCountry),
		Description:     htmlSingleEncodedToText(item.Description),
		WorkArrangement: crawler.WorkArrangementUnspecified,
	}
	if t, ok := parseSoftgardenTime(item.DatePosted); ok {
		listing.FirstPublished = t
	}
	return listing
}

// softgardenLocation composes a readable Location from an address's non-placeholder
// fields in the order [street, postal, locality, region, country], joined with ", "
// and de-duplicated case-insensitively so the common addressLocality == addressRegion
// case (e.g. Berlin/Berlin) does not render "Berlin, Berlin, Deutschland". Empty when
// every field is a placeholder.
func softgardenLocation(addr softgardenAddress) string {
	parts := []string{}
	for _, raw := range []string{
		addr.StreetAddress,
		addr.PostalCode,
		addr.AddressLocality,
		addr.AddressRegion,
		addr.AddressCountry,
	} {
		v := softgardenPlaceholder(raw)
		if v == "" {
			continue
		}
		dup := false
		for _, existing := range parts {
			if strings.EqualFold(existing, v) {
				dup = true
				break
			}
		}
		if !dup {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// softgardenPlaceholder normalizes a softgarden address field: it returns "" when the
// trimmed value is empty or the "-" placeholder softgarden emits for an absent field,
// and the trimmed value otherwise.
func softgardenPlaceholder(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return ""
	}
	return s
}

// parseSoftgardenTime parses softgarden's datePosted, which carries fractional seconds
// and a colon offset (e.g. "2024-09-05T11:55:12.145+02:00"); the parsed instant keeps
// its offset. ok is false (the caller keeps the zero time) on an empty or otherwise
// unparseable value — a bad timestamp must never drop a real posting.
func parseSoftgardenTime(s string) (time.Time, bool) {
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
