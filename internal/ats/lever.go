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

var _ BoardFetcher = (*LeverFetcher)(nil)

const (
	// ProviderLever is the Lever ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Lever host (jobs.lever.co), so
	// seed-time routing can resolve it against the Registry (#127). This package
	// stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderLever = "lever"

	leverDefaultBaseURL = "https://api.lever.co/v0/postings"
	leverDefaultTimeout = 15 * time.Second
)

// LeverFetcher reads a Lever tenant's board through the public postings API
// (api.lever.co/v0/postings) and maps its postings to Job Listings. It makes no
// LLM call: the board API supplies every field but the free-text description,
// which is the posting's own HTML sections reduced to plain text (ADR-0022/ADR-0023).
type LeverFetcher struct {
	baseURL    string
	httpClient *http.Client
}

// LeverFetcherOption configures a LeverFetcher at construction.
type LeverFetcherOption func(*LeverFetcher)

// WithLeverBaseURL overrides the postings-API base URL, chiefly so tests can
// point the fetcher at an httptest server.
func WithLeverBaseURL(u string) LeverFetcherOption {
	return func(l *LeverFetcher) {
		l.baseURL = u
	}
}

// WithLeverHTTPClient injects the HTTP client used for board requests, so the ATS
// Fetch lane can supply a rate-limited or instrumented client (#127).
func WithLeverHTTPClient(c *http.Client) LeverFetcherOption {
	return func(l *LeverFetcher) {
		l.httpClient = c
	}
}

// NewLeverFetcher builds a LeverFetcher pointed at the public postings API with a
// default-timeout HTTP client, overridable via options.
func NewLeverFetcher(opts ...LeverFetcherOption) *LeverFetcher {
	l := &LeverFetcher{
		baseURL:    leverDefaultBaseURL,
		httpClient: &http.Client{Timeout: leverDefaultTimeout},
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Fetch returns the tenant's postings mapped to Job Listings. It requests
// mode=json so the postings API replies with JSON rather than HTML. A non-200
// response yields ErrBoardStatus; a decode failure is wrapped. Company and
// CompanyKey are left empty for the ingest lane to stamp from the page's Owner
// (ADR-0022). An empty board yields an empty, non-nil slice.
func (l *LeverFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: lever: empty tenant slug")
	}

	endpoint := l.baseURL + "/" + url.PathEscape(tenant) + "?mode=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: lever build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: lever fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: lever tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var postings []leverPosting
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&postings); err != nil {
		return nil, fmt.Errorf("ats: lever decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, p := range postings {
		// hostedUrl is the canonical posting URL the lane keys upserts on (#127);
		// a posting without one has no dedup key and cannot be saved, so skip it.
		if p.HostedURL == "" {
			continue
		}
		listings = append(listings, mapLeverPosting(p))
	}
	return listings, nil
}

// leverPosting is one entry in the postings-API JSON array. Only the fields the
// mapper reads are declared; any others in the JSON are ignored by the decoder.
type leverPosting struct {
	Text          string          `json:"text"`      // posting title
	HostedURL     string          `json:"hostedUrl"` // canonical posting URL (upsert key)
	Categories    leverCategories `json:"categories"`
	Description   string          `json:"description"` // opening HTML section
	Lists         []leverList     `json:"lists"`       // titled HTML sections (requirements, …)
	Additional    string          `json:"additional"`  // closing HTML section
	CreatedAt     int64           `json:"createdAt"`   // first published, ms since epoch
	WorkplaceType string          `json:"workplaceType"`
}

type leverCategories struct {
	Department string `json:"department"`
	Location   string `json:"location"`
}

type leverList struct {
	Text    string `json:"text"`    // section heading
	Content string `json:"content"` // section body HTML
}

// mapLeverPosting maps one postings-API entry to a Job Listing. Company and
// CompanyKey are deliberately left empty: the ATS ingest lane stamps Company from
// the embedding/seed page's Owner (ADR-0022, #127) — never from a Lever field.
func mapLeverPosting(p leverPosting) *crawler.JobListing {
	return &crawler.JobListing{
		Title:          p.Text,
		URL:            p.HostedURL,
		Location:       p.Categories.Location,
		Description:    leverDescription(p),
		Remote:         strings.EqualFold(p.WorkplaceType, "remote"),
		Department:     p.Categories.Department,
		FirstPublished: leverFirstPublished(p.CreatedAt),
	}
}

// leverDescription reduces a posting's HTML sections (opening description, each
// titled list's heading and body, closing additional) to plain text. Lever serves
// these as single-encoded real HTML, so each section is stripped independently via
// htmlSingleEncodedToText — not Greenhouse's double-encoded htmlDoubleEncodedToText,
// whose leading unescape would decode an entity-encoded angle bracket in the text
// (e.g. "team of &lt;10 engineers") into a literal "<" and then swallow the run of
// text after it as a bogus tag. The non-empty results are joined with newlines, so
// section boundaries survive as line breaks rather than collapsing into one run of
// text.
func leverDescription(p leverPosting) string {
	parts := []string{}
	if s := htmlSingleEncodedToText(p.Description); s != "" {
		parts = append(parts, s)
	}
	for _, list := range p.Lists {
		if s := htmlSingleEncodedToText(list.Text); s != "" {
			parts = append(parts, s)
		}
		if s := htmlSingleEncodedToText(list.Content); s != "" {
			parts = append(parts, s)
		}
	}
	if s := htmlSingleEncodedToText(p.Additional); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// leverFirstPublished converts Lever's createdAt (milliseconds since the Unix
// epoch) to a UTC time. A non-positive value means the board omitted the field;
// the caller keeps the zero time so a missing timestamp never drops a real
// posting (parity with the Greenhouse mapper's fail-safe).
func leverFirstPublished(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
