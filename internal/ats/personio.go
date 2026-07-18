package ats

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*PersonioFetcher)(nil)

const (
	// ProviderPersonio is the Personio ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Personio host (<slug>.jobs.personio.de
	// / .com), so seed-time routing can resolve it against the Registry (#127). This
	// package stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderPersonio = "personio"

	personioDefaultTimeout = 15 * time.Second
	// personioTimeLayoutBasic is Personio's documented no-colon ISO-8601 offset
	// (e.g. 2016-05-31T12:14:07+0200), the fallback when RFC3339 (the colon form
	// every live 2026 tenant emits) does not parse.
	personioTimeLayoutBasic = "2006-01-02T15:04:05-0700"
)

// PersonioFetcher reads a Personio tenant's board through the public career-site
// XML feed (<slug>.jobs.personio.de/xml) and maps its positions to Job Listings.
// It makes no LLM call: the feed supplies every field but the free-text
// description, which is the position's own CDATA-wrapped HTML sections reduced to
// plain text (ADR-0022/ADR-0023). Personio is the ATS Fetch lane's one XML
// provider — it uses encoding/xml rather than the Greenhouse/Lever JSON plumbing.
type PersonioFetcher struct {
	// baseURL, when non-empty, repoints BOTH the /xml fetch endpoint and the
	// synthesized canonical posting URLs (chiefly so tests can target an httptest
	// server). Empty is the sentinel for "derive the per-tenant .de host".
	baseURL    string
	httpClient *http.Client
}

// PersonioFetcherOption configures a PersonioFetcher at construction.
type PersonioFetcherOption func(*PersonioFetcher)

// WithPersonioBaseURL overrides the career-site base URL, chiefly so tests can
// point the fetcher at an httptest server. It repoints both the /xml fetch
// endpoint and the synthesized /job/<id> canonical URLs, so a test server needs
// no per-tenant subdomain.
func WithPersonioBaseURL(u string) PersonioFetcherOption {
	return func(p *PersonioFetcher) {
		p.baseURL = u
	}
}

// WithPersonioHTTPClient injects the HTTP client used for board requests, so the
// ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithPersonioHTTPClient(c *http.Client) PersonioFetcherOption {
	return func(p *PersonioFetcher) {
		p.httpClient = c
	}
}

// NewPersonioFetcher builds a PersonioFetcher with a default-timeout HTTP client.
// The base URL is left empty so the tenant's .de career-site host is derived per
// fetch; both are overridable via options.
func NewPersonioFetcher(opts ...PersonioFetcherOption) *PersonioFetcher {
	p := &PersonioFetcher{
		baseURL:    "",
		httpClient: &http.Client{Timeout: personioDefaultTimeout},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// tenantBaseURL returns the configured base URL when one is set, else the tenant's
// default .de career-site host. It backs both the /xml endpoint and the
// synthesized posting URLs so a test override keeps the two consistent. The .com
// twin serves identical content; there is deliberately no .com fallback (see Fetch).
func (p *PersonioFetcher) tenantBaseURL(tenant string) string {
	if p.baseURL != "" {
		return p.baseURL
	}
	return "https://" + tenant + ".jobs.personio.de"
}

// Fetch returns the tenant's positions mapped to Job Listings. It reads the public
// /xml career-site feed. Company and CompanyKey are left empty for the ingest lane
// to stamp from the page's Owner (ADR-0022). An empty board yields an empty,
// non-nil slice.
//
// Personio's feed is opt-in per tenant (Settings -> Recruiting -> Career page), so
// a recognized-but-opted-out tenant legitimately answers 404 — that is treated as
// "no open roles" (empty non-nil slice), not an error. Any OTHER non-200 yields
// ErrBoardStatus. This 404 handling is the one deliberate divergence from the
// Greenhouse/Lever fetchers.
func (p *PersonioFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		// Guard before deriving the host, or the default would build the bogus
		// "https://.jobs.personio.de".
		return nil, fmt.Errorf("ats: personio: empty tenant slug")
	}

	base := p.tenantBaseURL(tenant)
	endpoint := base + "/xml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: personio build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/xml")
	// Never send X-Company-ID: the ReadMe get_xml reference lists it as "required",
	// but that is a doc auto-gen artifact disproven by zero-header 200s (research §Personio).

	res, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: personio fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusNotFound {
		// Opt-in feed: a recognized tenant with the career page disabled 404s. That
		// is "no open roles", not a failure — return the empty-board result.
		return []*crawler.JobListing{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: personio tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var doc personioJobs
	if err := xml.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("ats: personio decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, pos := range doc.Positions {
		// The synthesized /job/<id> URL is the canonical posting URL the lane keys
		// upserts on (#127); a position without an id has no dedup key and cannot
		// be saved, so skip it.
		if pos.ID == "" {
			continue
		}
		listings = append(listings, mapPersonioPosition(base, pos))
	}
	return listings, nil
}

// personioJobs is the /xml feed's document envelope: root <workzag-jobs> wrapping
// repeated <position> elements. Only the fields the mapper reads are declared; any
// others in the XML are ignored by the decoder.
type personioJobs struct {
	XMLName   xml.Name           `xml:"workzag-jobs"`
	Positions []personioPosition `xml:"position"`
}

// personioPosition is one <position> in the feed. The wire element is <position>,
// not <posting> (the OpenAPI "posting" is only the array property name).
type personioPosition struct {
	ID   string `xml:"id"`
	Name string `xml:"name"` // position title
	// Office is the primary site; AdditionalOffices holds the <additionalOffices>
	// container's <office> children.
	Office            string                   `xml:"office"`
	AdditionalOffices []string                 `xml:"additionalOffices>office"`
	Department        string                   `xml:"department"`
	CreatedAt         string                   `xml:"createdAt"`
	Descriptions      []personioJobDescription `xml:"jobDescriptions>jobDescription"`
}

// personioJobDescription is one named section under <jobDescriptions>. Value is
// CDATA-wrapped HTML, which encoding/xml unwraps into a raw single-encoded HTML
// string (the same shape as Lever's sections).
type personioJobDescription struct {
	Name  string `xml:"name"`  // section heading
	Value string `xml:"value"` // section body, CDATA-wrapped HTML
}

// mapPersonioPosition maps one <position> to a Job Listing. Company and CompanyKey
// are deliberately left empty: the ATS ingest lane stamps Company from the
// embedding/seed page's Owner (ADR-0022, #127) — never from a Personio field such
// as <subcompany>. Remote is not exposed by the feed and stays false; TechStack is
// not set (dropped in #125/ADR-0023). The URL is synthesized from base + id, since
// the feed carries no per-posting URL.
func mapPersonioPosition(base string, pos personioPosition) *crawler.JobListing {
	listing := &crawler.JobListing{
		Title:       pos.Name,
		URL:         base + "/job/" + pos.ID,
		Location:    personioLocation(pos),
		Department:  pos.Department,
		Description: personioDescription(pos),
	}
	if t, ok := parsePersonioTime(pos.CreatedAt); ok {
		listing.FirstPublished = t
	}
	return listing
}

// personioLocation joins a position's sites into one string: the primary <office>
// first, then each non-empty <additionalOffices>/<office>, separated by ", ". A
// single Location string that names every site aids location/keyword matching.
func personioLocation(pos personioPosition) string {
	parts := []string{}
	if s := strings.TrimSpace(pos.Office); s != "" {
		parts = append(parts, s)
	}
	for _, office := range pos.AdditionalOffices {
		if s := strings.TrimSpace(office); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// personioDescription reduces a position's <jobDescription> sections to plain text.
// Each section's heading and CDATA-HTML body are stripped independently via
// htmlSingleEncodedToText (the CDATA unwraps to single-encoded real HTML, Lever's
// shape — not Greenhouse's double-encoded content). The non-empty results are
// joined with newlines so section boundaries survive as line breaks.
func personioDescription(pos personioPosition) string {
	parts := []string{}
	for _, d := range pos.Descriptions {
		if s := htmlSingleEncodedToText(d.Name); s != "" {
			parts = append(parts, s)
		}
		if s := htmlSingleEncodedToText(d.Value); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// parsePersonioTime parses Personio's <createdAt>, which is ambiguous across
// tenants: live 2026 tenants emit RFC3339 with a colon offset (2026-07-02T15:34:32+00:00),
// while the documented example uses a no-colon offset (2016-05-31T12:14:07+0200).
// RFC3339 is tried first, then the basic no-colon layout. ok is false (the caller
// keeps the zero time) on an empty or otherwise unparseable value — a bad timestamp
// must never drop a real posting.
func parsePersonioTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(personioTimeLayoutBasic, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
