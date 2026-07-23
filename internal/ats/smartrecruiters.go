package ats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*SmartRecruitersFetcher)(nil)

const (
	// ProviderSmartRecruiters is the SmartRecruiters ATS provider family key. It
	// MUST equal the provider string catalog.Identify emits for a SmartRecruiters
	// host (careers/jobs.smartrecruiters.com), so seed-time routing can resolve it
	// against the Registry (#127). This package stays decoupled from catalog; the
	// invariant is enforced by the wiring point.
	ProviderSmartRecruiters = "smartrecruiters"

	smartRecruitersDefaultBaseURL = "https://api.smartrecruiters.com"
	smartRecruitersDefaultTimeout = 15 * time.Second
	// smartRecruitersPageSize is the list window per request. The Posting API caps
	// the effective limit at 100 (a larger request is silently clamped), so the
	// fetcher pages at the ceiling.
	smartRecruitersPageSize = 100
	// smartRecruitersMaxPages bounds the pagination loop so a misbehaving board that
	// never advances past totalFound cannot spin forever. 1000 pages * 100 = 100k
	// postings, far beyond any real tenant.
	smartRecruitersMaxPages = 1000
)

// SmartRecruitersFetcher reads a SmartRecruiters tenant's board through the public
// Posting API (api.smartrecruiters.com/v1/companies/<id>/postings) and maps its
// postings to Job Listings. It makes no LLM call. The list endpoint returns only
// posting summaries — the canonical postingUrl and the jobAd description live on
// the per-posting detail object — so the fetcher pages the list for ids, then
// resolves each id with a follow-up detail call (ADR-0022/ADR-0023).
//
// As the only provider that paginates and reports a total, it is the only one that
// computes completeness at runtime (ADR-0035): Fetch returns ErrBoardIncomplete
// alongside the partial slice when the list did not enumerate the whole reported
// board (a short/empty-before-total page or the pagination cap), a posting failed
// to resolve to a detail, or the final mapped count falls short of totalFound.
type SmartRecruitersFetcher struct {
	baseURL    string
	httpClient *http.Client
	// maxPages bounds the pagination loop; exceeding it without reaching totalFound
	// marks the fetch ErrBoardIncomplete. Defaults to smartRecruitersMaxPages.
	maxPages int
}

// SmartRecruitersFetcherOption configures a SmartRecruitersFetcher at construction.
type SmartRecruitersFetcherOption func(*SmartRecruitersFetcher)

// WithSmartRecruitersBaseURL overrides the Posting-API base URL, chiefly so tests
// can point the fetcher at an httptest server.
func WithSmartRecruitersBaseURL(u string) SmartRecruitersFetcherOption {
	return func(s *SmartRecruitersFetcher) {
		s.baseURL = u
	}
}

// WithSmartRecruitersHTTPClient injects the HTTP client used for board requests, so
// the ATS Fetch lane can supply a rate-limited or instrumented client (#127).
func WithSmartRecruitersHTTPClient(c *http.Client) SmartRecruitersFetcherOption {
	return func(s *SmartRecruitersFetcher) {
		s.httpClient = c
	}
}

// WithSmartRecruitersMaxPages overrides the pagination cap (default
// smartRecruitersMaxPages). Exceeding the cap without reaching totalFound marks the
// fetch ErrBoardIncomplete; chiefly a test knob for the cap path.
func WithSmartRecruitersMaxPages(n int) SmartRecruitersFetcherOption {
	return func(s *SmartRecruitersFetcher) {
		s.maxPages = n
	}
}

// NewSmartRecruitersFetcher builds a SmartRecruitersFetcher pointed at the public
// Posting API with a default-timeout HTTP client, overridable via options.
func NewSmartRecruitersFetcher(opts ...SmartRecruitersFetcherOption) *SmartRecruitersFetcher {
	s := &SmartRecruitersFetcher{
		baseURL:    smartRecruitersDefaultBaseURL,
		httpClient: &http.Client{Timeout: smartRecruitersDefaultTimeout},
		maxPages:   smartRecruitersMaxPages,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Fetch returns the tenant's postings mapped to Job Listings. It pages the list
// endpoint to enumerate posting ids, then resolves each id with a detail call
// (the list summary carries neither the canonical URL nor the description). A
// non-200 on the list is board-level and yields ErrBoardStatus; a non-200 or
// transport error on a single detail skips just that posting so one flaky posting
// never nukes the whole board. Company and CompanyKey are left empty for the
// ingest lane to stamp from the page's Owner (ADR-0022). An empty board yields an
// empty, non-nil slice.
//
// Completeness (ADR-0035): Fetch returns the collected slice with ErrBoardIncomplete
// — never a nil slice — when the fetch cannot be proven to be the whole open board.
// Three triggers: the list did not enumerate the reported total (a short/empty page
// before totalFound or the pagination cap), a posting failed to resolve to a detail
// (a skipped detail), or the final mapped count falls short of totalFound (also
// catching a posting dropped for an empty postingUrl). Any shortfall biases to
// keep-stale-open (skip-sweep), never a mass-close. A cancelled context still
// surfaces as a hard context error with a nil slice, ahead of the incomplete verdict.
func (s *SmartRecruitersFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		return nil, fmt.Errorf("ats: smartrecruiters: empty tenant slug")
	}

	ids, totalFound, listComplete, err := s.collectPostingIDs(ctx, tenant)
	if err != nil {
		return nil, err
	}

	// detailSkipped guards against a lying/low totalFound: a per-posting failure alone
	// makes the fetch untrustworthy even if the count still happens to reconcile.
	detailSkipped := false
	listings := []*crawler.JobListing{}
	for _, id := range ids {
		// Abort (rather than skip every remaining posting) on a cancelled context, so
		// a cancellation surfaces as an error instead of a silently partial board.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		detail, ok := s.fetchPostingDetail(ctx, tenant, id)
		if !ok {
			// fetchPostingDetail swallows the underlying error to skip a single flaky
			// posting, but a cancelled context is board-level, not posting-level. Surface
			// it as an error rather than a silently partial board — the top-of-loop guard
			// misses a cancellation that lands during this iteration's detail call
			// (notably the final id, where there is no next iteration to catch it).
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			detailSkipped = true
			continue
		}
		// postingUrl is the canonical posting URL the lane keys upserts on (#127); a
		// posting whose detail lacks one has no dedup key and cannot be saved, so skip it.
		if detail.PostingURL == "" {
			continue
		}
		listings = append(listings, mapSmartRecruitersPosting(detail))
	}

	// Completeness contract (ADR-0035): the sweep may run only on a provably complete
	// snapshot. Complete iff every page was enumerated up to the reported total, no
	// posting failed to resolve, and the collected count matches the board's total.
	// The count cross-check (len(listings) < totalFound) uniformly catches a transient
	// detail skip and a posting dropped for an empty postingUrl.
	if !listComplete || detailSkipped || len(listings) < totalFound {
		return listings, fmt.Errorf("ats: smartrecruiters tenant %q: %w", tenant, ErrBoardIncomplete)
	}
	return listings, nil
}

// collectPostingIDs pages the list endpoint and returns every posting id in board
// order, the board's reported totalFound, and whether the list was enumerated in
// full. It advances offset by the actual page length (tolerating a server-side
// page-size clamp) and is hard-capped at s.maxPages. listComplete is true only when
// offset reaches totalFound; a page emptied before the total (a short/mismatched
// board) or the cap being hit both yield listComplete=false so the caller can mark
// the fetch incomplete. A non-200 aborts the whole fetch with ErrBoardStatus.
func (s *SmartRecruitersFetcher) collectPostingIDs(ctx context.Context, tenant string) (ids []string, totalFound int, listComplete bool, err error) {
	ids = []string{}
	offset := 0
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		endpoint := fmt.Sprintf("%s/v1/companies/%s/postings?limit=%d&offset=%d",
			s.baseURL, url.PathEscape(tenant), smartRecruitersPageSize, offset)
		var resp smartRecruitersListResponse
		if err := s.getInto(ctx, endpoint, &resp); err != nil {
			return nil, 0, false, fmt.Errorf("ats: smartrecruiters list tenant %q: %w", tenant, err)
		}
		totalFound = resp.TotalFound
		for _, item := range resp.Content {
			if item.ID != "" {
				ids = append(ids, item.ID)
			}
		}
		offset += len(resp.Content)
		if offset >= resp.TotalFound {
			// The whole reported board was enumerated.
			return ids, totalFound, true, nil
		}
		if len(resp.Content) == 0 {
			// An empty page before reaching the total: the board delivered fewer postings
			// than it claimed (short/mismatch). Stop and report the shortfall.
			return ids, totalFound, false, nil
		}
	}
	// The pagination cap was hit without reaching totalFound.
	return ids, totalFound, false, nil
}

// fetchPostingDetail resolves one posting id to its detail object. ok is false when
// the detail call fails (a transient non-200 or transport error): the caller skips
// just that posting and keeps going, so a single flaky detail never fails the board.
func (s *SmartRecruitersFetcher) fetchPostingDetail(ctx context.Context, tenant, id string) (smartRecruitersPosting, bool) {
	endpoint := s.baseURL + "/v1/companies/" + url.PathEscape(tenant) + "/postings/" + url.PathEscape(id)
	var detail smartRecruitersPosting
	if err := s.getInto(ctx, endpoint, &detail); err != nil {
		slog.Warn("ats: smartrecruiters skipping posting with unresolvable detail",
			"tenant", tenant, "postingID", id, "err", err)
		return smartRecruitersPosting{}, false
	}
	return detail, true
}

// getInto issues a GET for endpoint and decodes a size-capped, non-200-guarded body
// into dst. It sends only an Accept header and NO Authorization/token header: the
// public Posting API is unauthenticated, and the partner Job Board API and the
// feed/publications (X-SmartToken) endpoint are deliberately avoided
// (docs/research/ats-providers.md §SmartRecruiters trap). A non-200 wraps
// ErrBoardStatus so callers can errors.Is it.
func (s *SmartRecruitersFetcher) getInto(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("posting api status %d: %w", res.StatusCode, ErrBoardStatus)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// smartRecruitersListResponse is one page of the Posting API list endpoint. Only
// the fields the pager reads are declared: totalFound bounds the loop and each
// content item supplies the posting id (the summary carries neither postingUrl nor
// jobAd, which is why a detail call follows).
type smartRecruitersListResponse struct {
	TotalFound int                       `json:"totalFound"`
	Content    []smartRecruitersListItem `json:"content"`
}

type smartRecruitersListItem struct {
	ID string `json:"id"`
}

// smartRecruitersPosting is a single posting's detail object. Only the fields the
// mapper reads are declared; any others in the JSON are ignored by the decoder.
type smartRecruitersPosting struct {
	ID           string                  `json:"id"` // stable posting id (Corpus SourceID)
	Name         string                  `json:"name"`
	PostingURL   string                  `json:"postingUrl"`   // canonical posting URL (upsert key)
	ReleasedDate string                  `json:"releasedDate"` // RFC3339 (with fractional seconds)
	Location     smartRecruitersLocation `json:"location"`
	Department   smartRecruitersLabel    `json:"department"`
	Function     smartRecruitersLabel    `json:"function"`
	JobAd        smartRecruitersJobAd    `json:"jobAd"`
}

type smartRecruitersLocation struct {
	City    string `json:"city"`
	Country string `json:"country"`
	Remote  bool   `json:"remote"`
}

type smartRecruitersLabel struct {
	Label string `json:"label"`
}

type smartRecruitersJobAd struct {
	Sections smartRecruitersSections `json:"sections"`
}

// smartRecruitersSections models the jobAd's four documented sections as named
// fields rather than a map: the mapper concatenates them in a fixed order, and a
// map's randomized iteration would make the stored Description non-deterministic. A
// posting that ever grows a fifth section silently drops it — a missed field, never
// a crash.
type smartRecruitersSections struct {
	CompanyDescription    smartRecruitersSection `json:"companyDescription"`
	JobDescription        smartRecruitersSection `json:"jobDescription"`
	Qualifications        smartRecruitersSection `json:"qualifications"`
	AdditionalInformation smartRecruitersSection `json:"additionalInformation"`
}

type smartRecruitersSection struct {
	Title string `json:"title"`
	Text  string `json:"text"` // single-encoded real HTML (verified live)
}

// mapSmartRecruitersPosting maps one detail object to a Job Listing. Company and
// CompanyKey are deliberately left empty: the ATS ingest lane stamps Company from
// the embedding/seed page's Owner (ADR-0022, #127) — never from a SmartRecruiters
// field.
func mapSmartRecruitersPosting(p smartRecruitersPosting) *crawler.JobListing {
	// location.remote is a bare boolean, so only remote:true is a positive signal.
	// remote:false is NOT an on-site signal — it degrades to Unspecified, the ADR's
	// headline case (ADR-0030).
	arrangement := crawler.WorkArrangementUnspecified
	if p.Location.Remote {
		arrangement = crawler.WorkArrangementRemote
	}
	listing := &crawler.JobListing{
		Title:    p.Name,
		URL:      p.PostingURL, // canonical posting URL; the lane keys upserts on it (#127)
		SourceID: p.ID,         // the re-slug-stable posting id (ADR-0034)
		Location: smartRecruitersLocationText(p.Location),
		// location.country is a structured ISO alpha-2 country code (e.g. "de"), not a
		// name; the ingest lane treats it as a valid code at save (uppercased) in
		// preference to resolving the composed Location (ADR-0029).
		CountryHint:     p.Location.Country,
		Description:     smartRecruitersDescription(p.JobAd.Sections),
		WorkArrangement: arrangement,
		Department:      smartRecruitersDepartment(p),
	}
	if t, ok := parseSmartRecruitersTime(p.ReleasedDate); ok {
		listing.FirstPublished = t
	}
	return listing
}

// smartRecruitersLocationText joins the posting's city and country with ", ",
// dropping either when empty. The board's own fullLocation string is not used: it
// is malformed for postings with a blank region (e.g. "Changsha, , China").
func smartRecruitersLocationText(loc smartRecruitersLocation) string {
	parts := []string{}
	if loc.City != "" {
		parts = append(parts, loc.City)
	}
	if loc.Country != "" {
		parts = append(parts, loc.Country)
	}
	return strings.Join(parts, ", ")
}

// smartRecruitersDepartment prefers the posting's department label, falling back to
// its function label. department is often an empty object on the live board, so
// function.label ("Engineering", "Design", …) is the reliable value.
func smartRecruitersDepartment(p smartRecruitersPosting) string {
	if p.Department.Label != "" {
		return p.Department.Label
	}
	return p.Function.Label
}

// smartRecruitersDescription reduces the jobAd's four sections to plain text in a
// fixed order: company description, job description, qualifications, additional
// information. Each section's heading and body are single-encoded real HTML, so both
// are stripped independently via htmlSingleEncodedToText (mirroring the Lever
// reduction), and the non-empty results are joined with newlines so section
// boundaries survive as line breaks.
func smartRecruitersDescription(s smartRecruitersSections) string {
	parts := []string{}
	for _, sec := range []smartRecruitersSection{
		s.CompanyDescription,
		s.JobDescription,
		s.Qualifications,
		s.AdditionalInformation,
	} {
		if t := htmlSingleEncodedToText(sec.Title); t != "" {
			parts = append(parts, t)
		}
		if t := htmlSingleEncodedToText(sec.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// parseSmartRecruitersTime parses SmartRecruiters' releasedDate (RFC3339 with a
// trailing Z and fractional seconds, e.g. "2026-07-18T07:50:54.778Z"). ok is false
// (the caller keeps the zero time) on an empty or otherwise unparseable value — a
// bad timestamp must never drop a real posting (parity with the sibling mappers).
func parseSmartRecruitersTime(s string) (time.Time, bool) {
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
