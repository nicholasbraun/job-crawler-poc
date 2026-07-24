package ats

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*TeamtailorFetcher)(nil)

const (
	// ProviderTeamtailor is the Teamtailor ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a Teamtailor host (<tenant>.teamtailor.com),
	// so seed-time routing can resolve it against the Registry (#127). This package stays
	// decoupled from catalog; the invariant is enforced by the wiring point (and pinned by
	// TestTeamtailorCatalogRecognition).
	ProviderTeamtailor = "teamtailor"

	// teamtailorDefaultBaseURL templates the per-tenant board host via a "{tenant}"
	// placeholder. The tenant is the FULL host label-prefix left of ".teamtailor.com"
	// (e.g. "acme", or "thestudio.na" for a regional host), so the fetcher reconstructs
	// the exact board host rather than re-slugging a single label.
	teamtailorDefaultBaseURL = "https://{tenant}.teamtailor.com"
	teamtailorDefaultTimeout = 15 * time.Second
)

// TeamtailorFetcher reads a Teamtailor tenant's board through the public career-site
// RSS feed (<tenant>.teamtailor.com/jobs.rss) and maps its <item>s to Job Listings.
// It makes no LLM call and sends no auth header: the public feed supplies every field
// but the free-text description, which is the item's own entity-encoded HTML reduced
// to plain text (ADR-0022/ADR-0023). The RSS feed is chosen over the leaner JSON Feed
// (/jobs.json) because only RSS carries tt:department and the structured
// tt:locations, both of which the family populates. Teamtailor is one of the ATS
// Fetch lane's XML providers — it uses encoding/xml, like Personio.
type TeamtailorFetcher struct {
	// baseURL templates the per-tenant host via a "{tenant}" placeholder. A test
	// override with no placeholder is left untouched, so an httptest base needs no
	// real subdomain.
	baseURL    string
	httpClient *http.Client
}

// TeamtailorFetcherOption configures a TeamtailorFetcher at construction.
type TeamtailorFetcherOption func(*TeamtailorFetcher)

// WithTeamtailorBaseURL overrides the career-site base URL, chiefly so tests can
// point the fetcher at an httptest server. Any "{tenant}" placeholder in the value
// is substituted with the tenant slug at fetch time; a value with no placeholder is
// used verbatim.
func WithTeamtailorBaseURL(u string) TeamtailorFetcherOption {
	return func(t *TeamtailorFetcher) {
		t.baseURL = u
	}
}

// WithTeamtailorHTTPClient injects the HTTP client used for board requests, so the
// ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithTeamtailorHTTPClient(c *http.Client) TeamtailorFetcherOption {
	return func(t *TeamtailorFetcher) {
		t.httpClient = c
	}
}

// NewTeamtailorFetcher builds a TeamtailorFetcher pointed at the public /jobs.rss
// feed with a default-timeout HTTP client, overridable via options.
func NewTeamtailorFetcher(opts ...TeamtailorFetcherOption) *TeamtailorFetcher {
	t := &TeamtailorFetcher{
		baseURL:    teamtailorDefaultBaseURL,
		httpClient: &http.Client{Timeout: teamtailorDefaultTimeout},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Fetch returns the tenant's open roles mapped to Job Listings. It reads the public
// /jobs.rss feed, which replies with the whole board in one payload (no pagination
// cursor observed; very large boards are not stress-tested and are bounded by the
// shared maxBoardBytes ceiling; completeness on 100+ boards is unconfirmed — see
// ADR-0035). A non-200 response yields ErrBoardStatus; a decode failure is wrapped.
// Company and CompanyKey are left empty for the ingest lane to stamp from the page's
// Owner (ADR-0022). An empty board yields an empty, non-nil slice.
//
// Completeness (ADR-0035): Teamtailor is single-shot, so the result is complete by
// construction — err == nil ⟹ the slice is the tenant's complete open set (safe to
// sweep). The fetcher never returns ErrBoardIncomplete: there is no partial-page
// state to signal, and a body cut mid-<item> is caught by io.LimitReader and surfaces
// as a decode error (a hard failure with a nil slice), never a silent partial.
func (t *TeamtailorFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		// Guard before templating the host, or the default would build the bogus
		// "https://.teamtailor.com".
		return nil, fmt.Errorf("ats: teamtailor: empty tenant slug")
	}

	// The tenant is a trusted host label-prefix (catalog.subdomainLabel) and goes into
	// the host, so it is NOT url.PathEscaped — escaping a host label is wrong (same
	// reasoning as Recruitee).
	endpoint := strings.Replace(t.baseURL, "{tenant}", tenant, 1) + "/jobs.rss"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ats: teamtailor build request for tenant %q: %w", tenant, err)
	}
	req.Header.Set("Accept", "application/rss+xml")
	// Send no Authorization and no X-Api-Version header: api.teamtailor.com is the
	// key-gated REST API (401/406 without them), but the public tenant /jobs.rss feed
	// needs no auth. Asserted by TestTeamtailorNoAuthHeader.

	res, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ats: teamtailor fetch tenant %q: %w", tenant, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		// Unlike Personio's opt-in feed, Teamtailor's /jobs.rss is not opt-in, so a 404
		// is a genuinely missing/wrong board (not "no open roles") and surfaces as
		// ErrBoardStatus rather than an empty slice.
		return nil, fmt.Errorf("ats: teamtailor tenant %q: status %d: %w", tenant, res.StatusCode, ErrBoardStatus)
	}

	var feed teamtailorFeed
	if err := xml.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("ats: teamtailor decode tenant %q: %w", tenant, err)
	}

	listings := []*crawler.JobListing{}
	for _, item := range feed.Channel.Items {
		// <link> is the canonical posting URL the lane keys upserts on (#127); an item
		// without one has no dedup key and cannot be saved, so skip it.
		if item.Link == "" {
			continue
		}
		listings = append(listings, mapTeamtailorItem(item))
	}
	return listings, nil
}

// teamtailorFeed is the /jobs.rss document envelope: <rss><channel> wrapping repeated
// <item> elements. Only the fields the mapper reads are declared; any others in the
// XML are ignored by the decoder.
type teamtailorFeed struct {
	XMLName xml.Name          `xml:"rss"`
	Channel teamtailorChannel `xml:"channel"`
}

type teamtailorChannel struct {
	Items []teamtailorItem `xml:"item"`
}

// teamtailorItem is one <item> in the feed. The tt:-namespaced elements are matched
// by LOCAL NAME only (no namespace in the struct tag): an unqualified tag matches the
// element's local name regardless of its namespace prefix, so xml:"department" matches
// <tt:department>. This is robust to the exact tt namespace URI (which the struct does
// not pin).
type teamtailorItem struct {
	Title       string               `xml:"title"`
	Link        string               `xml:"link"`               // canonical posting URL /jobs/<id>-<slug> (upsert key)
	Description string               `xml:"description"`        // entity-encoded HTML
	PubDate     string               `xml:"pubDate"`            // RFC-822 / RFC-1123 with numeric zone
	Department  string               `xml:"department"`         // tt:department (RSS-only; the reason RSS is chosen)
	Locations   []teamtailorLocation `xml:"locations>location"` // tt:locations/tt:location
}

type teamtailorLocation struct {
	Name    string `xml:"name"`
	City    string `xml:"city"`
	Country string `xml:"country"` // country NAME (no ISO code in the feed)
}

// mapTeamtailorItem maps one <item> to a Job Listing. Company and CompanyKey are
// deliberately left empty: the ATS ingest lane stamps Company from the embedding/seed
// page's Owner (ADR-0022, #127) — never from a provider field. WorkArrangement is
// Unspecified — a silent provider is never Onsite (ADR-0030): the feed's tt:remoteStatus
// enum (none/hybrid/fully/temporary) does not map cleanly onto the remote/onsite/hybrid
// vocabulary (a fully-remote posting would fold to Unspecified) and is absent from the
// ticket's field map, so mapping it is out of scope for v1 (matches the Greenhouse/
// Recruitee/Personio precedent). TechStack is not set (dropped in #125/ADR-0023).
func mapTeamtailorItem(item teamtailorItem) *crawler.JobListing {
	listing := &crawler.JobListing{
		Title:           item.Title,
		URL:             item.Link, // canonical posting URL; the lane keys upserts on it (#127)
		SourceID:        teamtailorSourceID(item.Link),
		Location:        teamtailorLocationString(item),
		CountryHint:     teamtailorCountryHint(item),
		Department:      item.Department,
		Description:     htmlSingleEncodedToText(item.Description),
		WorkArrangement: crawler.WorkArrangementUnspecified,
	}
	if t, ok := parseTeamtailorTime(item.PubDate); ok {
		listing.FirstPublished = t
	}
	return listing
}

// teamtailorSourceID extracts the stable posting id from a /jobs/<id>-<slug> link, so
// a slug re-word never forges a new posting (ADR-0034, parity with the siblings that
// set SourceID). It takes the leading run before the first "-" of the link's last path
// segment and accepts it only when all-digit (a bare-slug path yields ""). Returns ""
// on any parse failure; a missing/unparseable SourceID must NOT drop the posting — the
// URL is still the upsert key.
func teamtailorSourceID(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	seg := path.Base(u.Path)
	if i := strings.IndexByte(seg, '-'); i >= 0 {
		seg = seg[:i]
	}
	if seg == "" || strings.TrimLeft(seg, "0123456789") != "" {
		return ""
	}
	return seg
}

// teamtailorLocationString resolves an item's Location from its first tt:location:
// the tt:name is preferred when present, else the non-empty of [city, country] joined
// with ", ". Empty when the item carries no structured location. Only the first
// location is used (Recruitee precedent) so the Location string stays unambiguous;
// joining multiple tt:locations is a reasonable future enhancement.
func teamtailorLocationString(item teamtailorItem) string {
	if len(item.Locations) == 0 {
		return ""
	}
	loc := item.Locations[0]
	if loc.Name != "" {
		return loc.Name
	}
	parts := []string{}
	for _, p := range []string{loc.City, loc.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// teamtailorCountryHint surfaces the first tt:location's country for the ingest lane
// to resolve at save (ADR-0029). The feed carries a country NAME (no ISO code), which
// the Country Resolver resolves. Empty when the item carries no structured location.
func teamtailorCountryHint(item teamtailorItem) string {
	if len(item.Locations) == 0 {
		return ""
	}
	return item.Locations[0].Country
}

// parseTeamtailorTime parses Teamtailor's <pubDate>, an RFC-822 date. The live feed
// emits a numeric zone (RFC1123Z, e.g. "Thu, 23 Jul 2026 14:25:24 +0100"); a named
// zone (RFC1123) is tried as a fallback. ok is false (the caller keeps the zero time)
// on an empty or otherwise unparseable value — a bad timestamp must never drop a real
// posting.
func parseTeamtailorTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC1123Z, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC1123, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
