package ats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*GreenhouseFetcher)(nil)

const (
	// ProviderGreenhouse is the Greenhouse ATS provider family key. It MUST equal
	// the provider string catalog.Identify emits for a Greenhouse host, so
	// seed-time routing can resolve it against the Registry (#127). This package
	// stays decoupled from catalog; the invariant is enforced by the wiring point.
	ProviderGreenhouse = "greenhouse"

	greenhouseDefaultBaseURL = "https://boards-api.greenhouse.io"
	greenhouseDefaultTimeout = 15 * time.Second
	// maxBoardBytes caps the decoded board JSON. With content=true a large tenant's
	// full posting bodies run to several MB, so the ceiling is generous.
	maxBoardBytes = 10 << 20
)

// GreenhouseFetcher reads a Greenhouse tenant's board through the public board
// API (boards-api.greenhouse.io) and maps its postings to Job Listings. It makes
// no LLM call: the board API supplies every field but the free-text description,
// which is the posting's own HTML body reduced to plain text (ADR-0022/ADR-0023).
type GreenhouseFetcher struct {
	baseURL    string
	httpClient *http.Client
}

// GreenhouseFetcherOption configures a GreenhouseFetcher at construction.
type GreenhouseFetcherOption func(*GreenhouseFetcher)

// WithGreenhouseBaseURL overrides the board-API base URL, chiefly so tests can
// point the fetcher at an httptest server.
func WithGreenhouseBaseURL(u string) GreenhouseFetcherOption {
	return func(g *GreenhouseFetcher) {
		g.baseURL = u
	}
}

// WithGreenhouseHTTPClient injects the HTTP client used for board requests, so
// the ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithGreenhouseHTTPClient(c *http.Client) GreenhouseFetcherOption {
	return func(g *GreenhouseFetcher) {
		g.httpClient = c
	}
}

// NewGreenhouseFetcher builds a GreenhouseFetcher pointed at the public board API
// with a default-timeout HTTP client, overridable via options.
func NewGreenhouseFetcher(opts ...GreenhouseFetcherOption) *GreenhouseFetcher {
	g := &GreenhouseFetcher{
		baseURL:    greenhouseDefaultBaseURL,
		httpClient: &http.Client{Timeout: greenhouseDefaultTimeout},
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Fetch returns the tenant's postings mapped to Job Listings. It requests
// content=true so the board API includes each posting's HTML body and
// first_published. A non-200 response yields ErrBoardStatus; a decode failure is
// wrapped. Company and CompanyKey are left empty for the ingest lane to stamp
// from the page's Owner (ADR-0022). An empty board yields an empty, non-nil slice.
func (g *GreenhouseFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: greenhouse: empty tenant slug")
	}

	endpoint := g.baseURL + "/v1/boards/" + url.PathEscape(tenant) + "/jobs?content=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: greenhouse build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: greenhouse fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: greenhouse tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var resp greenhouseJobsResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ats: greenhouse decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, j := range resp.Jobs {
		// absolute_url is the canonical posting URL the lane keys upserts on (#127);
		// a posting without one has no dedup key and cannot be saved, so skip it.
		if j.AbsoluteURL == "" {
			continue
		}
		listings = append(listings, mapGreenhouseJob(j))
	}
	return listings, nil
}

// greenhouseJobsResponse is the board-API response envelope. Only the fields the
// mapper reads are declared; any others in the JSON are ignored by the decoder.
type greenhouseJobsResponse struct {
	Jobs []greenhouseJob `json:"jobs"`
}

type greenhouseJob struct {
	ID             int64                  `json:"id"` // stable posting id (Corpus SourceID)
	Title          string                 `json:"title"`
	AbsoluteURL    string                 `json:"absolute_url"`
	Content        string                 `json:"content"` // HTML-entity-encoded HTML
	FirstPublished string                 `json:"first_published"`
	Location       greenhouseLocation     `json:"location"`
	Departments    []greenhouseDepartment `json:"departments"`
}

type greenhouseLocation struct {
	Name string `json:"name"`
}

type greenhouseDepartment struct {
	Name string `json:"name"`
}

// mapGreenhouseJob maps one board-API posting to a Job Listing. Company and
// CompanyKey are deliberately left empty: the ATS ingest lane stamps Company from
// the embedding/seed page's Owner (ADR-0022, #127). The board API exposes no
// working mode, so WorkArrangement is Unspecified — a silent provider is never
// Onsite (ADR-0030); TechStack is not set (dropped in #125/ADR-0023).
func mapGreenhouseJob(j greenhouseJob) *crawler.JobListing {
	listing := &crawler.JobListing{
		Title:           j.Title,
		URL:             j.AbsoluteURL, // canonical posting URL; the lane keys upserts on it (#127)
		SourceID:        strconv.FormatInt(j.ID, 10),
		Location:        j.Location.Name,
		Description:     htmlDoubleEncodedToText(j.Content),
		WorkArrangement: crawler.WorkArrangementUnspecified,
	}
	if len(j.Departments) > 0 {
		listing.Department = j.Departments[0].Name
	}
	if t, ok := parseGreenhouseTime(j.FirstPublished); ok {
		listing.FirstPublished = t
	}
	return listing
}

// parseGreenhouseTime parses Greenhouse's RFC3339 first_published, tolerating a
// fractional-second variant. ok is false (the caller keeps the zero time) on an
// empty or otherwise unparseable value — a bad timestamp must never drop a real
// posting.
func parseGreenhouseTime(s string) (time.Time, bool) {
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
