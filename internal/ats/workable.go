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

var _ BoardFetcher = (*WorkableFetcher)(nil)

const (
	// ProviderWorkable is the Workable ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Workable host (apply.workable.com
	// path board or <slug>.workable.com subdomain), so seed-time routing can resolve
	// it against the Registry (#127). This package stays decoupled from catalog; the
	// invariant is enforced by the wiring point.
	ProviderWorkable = "workable"

	// workableDefaultBaseURL is the public widget API's entry host. The account
	// endpoint 302-redirects to the canonical serving host
	// (apply.workable.com/api/v1/widget/accounts/<slug>); the default http.Client
	// follows that redirect transparently, so no CheckRedirect override is needed.
	workableDefaultBaseURL = "https://www.workable.com"
	workableDefaultTimeout = 15 * time.Second
	// workableDateLayout matches the date-only published_on/created_at timestamps
	// (e.g. 2026-02-12). time.Parse with this layout yields midnight UTC, so
	// FirstPublished loses time-of-day precision the board never supplies.
	workableDateLayout = "2006-01-02"
)

// WorkableFetcher reads a Workable tenant's board through the public widget API
// (www.workable.com/api/accounts/<slug>?details=true) and maps its postings to Job
// Listings. It makes no LLM call: the widget API supplies every field but the
// free-text description, which is the posting's own HTML body reduced to plain
// text (ADR-0022/ADR-0023). It deliberately never touches the neighbouring
// spi/v3/jobs API — that one is Bearer-token gated — and sends no Authorization
// header.
type WorkableFetcher struct {
	baseURL    string
	httpClient *http.Client
}

// WorkableFetcherOption configures a WorkableFetcher at construction.
type WorkableFetcherOption func(*WorkableFetcher)

// WithWorkableBaseURL overrides the widget-API base URL, chiefly so tests can
// point the fetcher at an httptest server.
func WithWorkableBaseURL(u string) WorkableFetcherOption {
	return func(w *WorkableFetcher) {
		w.baseURL = u
	}
}

// WithWorkableHTTPClient injects the HTTP client used for board requests, so the
// ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithWorkableHTTPClient(c *http.Client) WorkableFetcherOption {
	return func(w *WorkableFetcher) {
		w.httpClient = c
	}
}

// NewWorkableFetcher builds a WorkableFetcher pointed at the public widget API
// with a default-timeout HTTP client, overridable via options.
func NewWorkableFetcher(opts ...WorkableFetcherOption) *WorkableFetcher {
	w := &WorkableFetcher{
		baseURL:    workableDefaultBaseURL,
		httpClient: &http.Client{Timeout: workableDefaultTimeout},
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Fetch returns the tenant's postings mapped to Job Listings. It requests
// details=true so the widget API includes each posting's body, and lets the
// client follow the 302 to the canonical serving host. A non-200 response yields
// ErrBoardStatus; a decode failure is wrapped. Company and CompanyKey are left
// empty for the ingest lane to stamp from the page's Owner (ADR-0022). An empty
// board yields an empty, non-nil slice.
func (w *WorkableFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: workable: empty tenant slug")
	}

	endpoint := w.baseURL + "/api/accounts/" + url.PathEscape(tenant) + "?details=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: workable build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/json")
	// Never set Authorization: the public widget API needs none, and the
	// neighbouring spi/v3/jobs API is Bearer-token gated — sending a header there
	// would be the "public read vs auth-gated neighbour" trap (research §Workable).

	res, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: workable fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ats: workable tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	// Every observed board returned all jobs in one payload, so this issues a single
	// request with no paging loop; large-board pagination is unverified and deferred
	// (research open-Q). The LimitReader caps a pathological board at maxBoardBytes.
	var resp workableAccountResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ats: workable decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, j := range resp.Jobs {
		// The canonical posting URL is the key the lane upserts on (#127); a posting
		// with none of url/shortlink/application_url has no dedup key and cannot be
		// saved, so skip it. shortlink is preferred over application_url because it
		// links to the posting itself, whereas application_url is that posting's /apply/
		// subpage — the poorer dedup key.
		canonicalURL := firstNonEmpty(j.URL, j.Shortlink, j.ApplicationURL)
		if canonicalURL == "" {
			continue
		}
		listings = append(listings, mapWorkableJob(j, canonicalURL))
	}
	return listings, nil
}

// workableAccountResponse is the widget-API response envelope: postings are nested
// under the jobs key, not at the root. Only the fields the mapper reads are
// declared; any others in the JSON (name, description, …) are ignored by the decoder.
type workableAccountResponse struct {
	Jobs []workableJob `json:"jobs"`
}

type workableJob struct {
	Title          string                  `json:"title"`
	Shortcode      string                  `json:"shortcode"` // stable posting id (Corpus SourceID)
	Department     string                  `json:"department"`
	Country        string                  `json:"country"`
	State          string                  `json:"state"`
	City           string                  `json:"city"`
	Locations      []workableLocationEntry `json:"locations"`
	Telecommuting  bool                    `json:"telecommuting"`
	WorkplaceType  string                  `json:"workplace_type"`
	PublishedOn    string                  `json:"published_on"` // date-only YYYY-MM-DD
	CreatedAt      string                  `json:"created_at"`   // date-only fallback
	URL            string                  `json:"url"`          // canonical posting URL (upsert key)
	ApplicationURL string                  `json:"application_url"`
	Shortlink      string                  `json:"shortlink"`
	Description    string                  `json:"description"` // single-encoded HTML
}

// workableLocationEntry is one entry of the jobs[].locations[] array, the fallback
// when the top-level country/state/city trio is empty. The state component is keyed
// "region" here, unlike the top-level posting fields which key it "state" — mapping
// the wrong key silently drops the region from the composed location line.
type workableLocationEntry struct {
	Country string `json:"country"`
	State   string `json:"region"`
	City    string `json:"city"`
}

// mapWorkableJob maps one widget-API posting to a Job Listing. Company and
// CompanyKey are deliberately left empty: the ATS ingest lane stamps Company from
// the embedding/seed page's Owner (ADR-0022, #127) — never from a Workable field.
// TechStack is not set (dropped in #125/ADR-0023).
func mapWorkableJob(j workableJob, canonicalURL string) *crawler.JobListing {
	// workplace_type is Workable's positive signal (Lever-style enum: remote/on-site/
	// hybrid), so it maps straight through the normalizer. telecommuting is a bare
	// remote boolean that only fills in when workplace_type says nothing; its absence
	// degrades to Unspecified, never Onsite (ADR-0030).
	arrangement := crawler.NormalizeWorkArrangement(j.WorkplaceType)
	if arrangement == crawler.WorkArrangementUnspecified && j.Telecommuting {
		arrangement = crawler.WorkArrangementRemote
	}
	listing := &crawler.JobListing{
		Title:           j.Title,
		URL:             canonicalURL,
		SourceID:        j.Shortcode,
		Location:        workableLocation(j),
		CountryHint:     workableCountryHint(j),
		Description:     htmlSingleEncodedToText(j.Description),
		WorkArrangement: arrangement,
		Department:      j.Department,
	}
	// published_on is preferred; created_at is the fallback. A malformed or absent
	// pair keeps the zero time so a bad timestamp never drops a real posting (parity
	// with the Greenhouse/Lever mappers' fail-safe).
	if t, ok := parseWorkableDate(j.PublishedOn, j.CreatedAt); ok {
		listing.FirstPublished = t
	}
	return listing
}

// workableCountryHint surfaces the posting's structured country name for the
// ingest lane to resolve at save (ADR-0029): the top-level country is preferred,
// falling back to the first locations[] entry's country. Empty when neither is
// present — the lane then resolves from the composed Location instead.
func workableCountryHint(j workableJob) string {
	if j.Country != "" {
		return j.Country
	}
	if len(j.Locations) > 0 {
		return j.Locations[0].Country
	}
	return ""
}

// workableLocation composes a single human-readable location line from the
// top-level city/state/country trio, falling back to the first locations[] entry
// composed the same way, else the empty string. It keeps no synthetic "Remote"
// token — the WorkArrangement enum is the domain's working-mode signal.
func workableLocation(j workableJob) string {
	if s := joinNonEmpty(j.City, j.State, j.Country); s != "" {
		return s
	}
	if len(j.Locations) > 0 {
		loc := j.Locations[0]
		return joinNonEmpty(loc.City, loc.State, loc.Country)
	}
	return ""
}

// parseWorkableDate parses the first candidate that is a valid date-only
// timestamp, so a missing or malformed published_on falls through to created_at.
// ok is false (the caller keeps the zero time) when no candidate parses.
func parseWorkableDate(candidates ...string) (time.Time, bool) {
	for _, s := range candidates {
		if s == "" {
			continue
		}
		if t, err := time.Parse(workableDateLayout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// joinNonEmpty joins the trimmed, non-empty parts with ", ", dropping blanks so no
// stray ", ," survives when a location component is absent.
func joinNonEmpty(parts ...string) string {
	kept := []string{}
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, ", ")
}

// firstNonEmpty returns the first non-empty value, or "" when all are empty. It
// backs the url -> shortlink -> application_url canonical-URL precedence.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
