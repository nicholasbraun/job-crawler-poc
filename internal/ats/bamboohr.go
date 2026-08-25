package ats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

var _ BoardFetcher = (*BambooHRFetcher)(nil)

const (
	// ProviderBambooHR is the BambooHR ATS provider family key. It MUST equal the
	// provider string catalog.Identify emits for a BambooHR host
	// (<tenant>.bamboohr.com), so seed-time routing can resolve it against the
	// Registry (#127). This package stays decoupled from catalog; the invariant is
	// enforced by the wiring point (pinned by TestBambooHRCatalogRecognition).
	ProviderBambooHR = "bamboohr"

	// bambooHRDefaultBaseURL templates the tenant slug into the board host via a
	// "{tenant}" placeholder. The ATS Fetch lane hands the fetcher the leftmost
	// catalog label (e.g. "giottoai" for giottoai.bamboohr.com), which is templated
	// into the fixed .bamboohr.com board host — mirroring softgarden/Recruitee/
	// Teamtailor's "{tenant}.<suffix>" bases. Dropping the suffix here would build the
	// unresolvable host "https://giottoai/careers/list" while every unit test (which
	// overrides the base) stayed green; collection.TestSubdomainProviderHostContract
	// is the end-to-end guard. A test override with no placeholder is used verbatim.
	bambooHRDefaultBaseURL = "https://{tenant}.bamboohr.com"
	bambooHRDefaultTimeout = 15 * time.Second

	// bambooHRPostingHost is the real-world board host the canonical posting URL is
	// built on. Kept a named const so the constructed upsert key is auditable and is
	// independent of the API baseURL (which a test overrides) — the manatalPostingHost
	// rule: the URL is a real-world identity, not an API address.
	bambooHRPostingHost = "bamboohr.com"

	// bambooHRDefaultDetailDelay paces successive per-posting detail calls (#140).
	// atsingest.HostLimiter cannot do this job — it paces once per TASK, before
	// Fetch, and internal/atsingest already imports internal/ats — so the N+1 owns its
	// own spacing. 250ms is headroom rather than a measured requirement: the largest
	// live board found is 23 postings (~5.75s of pacing) and BambooHR served 23
	// unpaced sequential details at 200 with no throttling (verified 2026-08-25).
	bambooHRDefaultDetailDelay = 250 * time.Millisecond

	// bambooHRDefaultMaxDetails bounds the per-posting detail calls one board may
	// spend. At bambooHRDefaultDetailDelay a 5,000-posting tenant would hold a pool
	// worker for ~21 minutes; the budget caps that at ~2 minutes. Exceeding it never
	// loses a posting — the list-derived listing (identity, URL, title, location) is
	// still emitted, only its Posting Body is missing — but it does mark the fetch
	// ErrBoardIncomplete so the absence-sweep is skipped (ADR-0035).
	bambooHRDefaultMaxDetails = 500

	// bambooHRRemoteLocationType / bambooHRHybridLocationType are the two locationType
	// codes BambooHR positively characterises. See bambooHRWorkArrangement.
	bambooHRRemoteLocationType = "1"
	bambooHRHybridLocationType = "2"
)

// BambooHRFetcher reads a BambooHR tenant's board through the hosted careers site's
// public JSON endpoints (<tenant>.bamboohr.com/careers/list and
// /careers/<id>/detail) and maps its job openings to Job Listings. It makes no LLM
// call and sends no auth header (ADR-0022/ADR-0023).
//
// The list enumerates the whole open set in one unpaginated response and already
// carries everything the posting's identity needs — the id the canonical URL is
// built from, the title, department, location and locationType — but NOT the
// Posting Body, and no country. Only the per-posting detail carries `description`,
// `datePosted`, and `location.addressCountry`. Since a Job Listing without a Posting
// Body is thin (ADR-0041) and the Country drives SavedSearch filtering (ADR-0029),
// the fetcher does the N+1: list first, then one detail per posting. Identity is
// always taken from the LIST row, so a failed detail costs enrichment, never the
// posting's URL or dedup key.
//
// The N+1 is the #140 shape, so it is both paced (detailDelay between successive
// detail calls, context-aware) and budgeted (maxDetails), and it distinguishes a
// genuine 404/410 — that posting is gone, drop it rather than upsert it back Open
// past ADR-0035's reopen-in-place rule — from a 429/5xx/transport/decode failure,
// where the posting is NOT known gone and its list-derived form is kept.
type BambooHRFetcher struct {
	// baseURL templates the per-tenant board host via a "{tenant}" placeholder. A test
	// override with no placeholder is left untouched, so an httptest base needs no real
	// host.
	baseURL    string
	httpClient *http.Client
	// detailDelay is the pause between successive detail calls (none before the
	// first). Zero disables pacing. Defaults to bambooHRDefaultDetailDelay.
	detailDelay time.Duration
	// maxDetails is the per-board detail-call budget; once spent, the remaining
	// postings are emitted list-derived and the fetch is marked ErrBoardIncomplete.
	// Defaults to bambooHRDefaultMaxDetails.
	maxDetails int
}

// BambooHRFetcherOption configures a BambooHRFetcher at construction.
type BambooHRFetcherOption func(*BambooHRFetcher)

// WithBambooHRBaseURL overrides the board base URL, chiefly so tests can point the
// fetcher at an httptest server. Any "{tenant}" placeholder is substituted with the
// tenant slug at fetch time; a value with no placeholder is used verbatim. It
// overrides only the API base; the constructed canonical posting URL always uses the
// real bamboohr.com host.
func WithBambooHRBaseURL(u string) BambooHRFetcherOption {
	return func(b *BambooHRFetcher) {
		b.baseURL = u
	}
}

// WithBambooHRHTTPClient injects the HTTP client used for board requests, so the ATS
// Fetch lane can supply a rate-limited or instrumented client (#127).
func WithBambooHRHTTPClient(c *http.Client) BambooHRFetcherOption {
	return func(b *BambooHRFetcher) {
		b.httpClient = c
	}
}

// WithBambooHRDetailDelay overrides the pause between successive per-posting detail
// calls (default bambooHRDefaultDetailDelay). Zero disables pacing — used by tests
// that are not exercising the pacing itself.
func WithBambooHRDetailDelay(d time.Duration) BambooHRFetcherOption {
	return func(b *BambooHRFetcher) {
		b.detailDelay = d
	}
}

// WithBambooHRMaxDetails overrides the per-board detail-call budget (default
// bambooHRDefaultMaxDetails). Exceeding it marks the fetch ErrBoardIncomplete;
// chiefly a test knob for the budget path.
func WithBambooHRMaxDetails(n int) BambooHRFetcherOption {
	return func(b *BambooHRFetcher) {
		b.maxDetails = n
	}
}

// NewBambooHRFetcher builds a BambooHRFetcher pointed at the hosted careers site's
// public JSON endpoints with a default-timeout HTTP client, overridable via options.
func NewBambooHRFetcher(opts ...BambooHRFetcherOption) *BambooHRFetcher {
	b := &BambooHRFetcher{
		baseURL:     bambooHRDefaultBaseURL,
		httpClient:  &http.Client{Timeout: bambooHRDefaultTimeout},
		detailDelay: bambooHRDefaultDetailDelay,
		maxDetails:  bambooHRDefaultMaxDetails,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Fetch returns the tenant's job openings mapped to Job Listings. The tenant is the
// leftmost catalog label (the slug, e.g. "giottoai"), templated into the fixed
// .bamboohr.com board host. It reads /careers/list — one unpaginated response
// carrying the whole open set — then enriches each posting from
// /careers/<id>/detail. A non-200 on the list is board-level and yields
// ErrBoardStatus with a nil slice. Company and CompanyKey are left empty for the
// ingest lane to stamp from the page's Owner (ADR-0022). An empty board yields an
// empty, non-nil slice with no detail call at all.
//
// Completeness (ADR-0035): Fetch returns the collected slice with ErrBoardIncomplete
// — never a nil slice — whenever the result cannot be proven to be the whole open
// board: the mapped count falls short of meta.totalCount (also catching a row
// dropped for a missing id), any detail call failed, or the detail budget was spent.
// A truncated body is caught by io.LimitReader and surfaces as a decode error (a
// hard failure with a nil slice), never a silent partial. A cancelled context
// surfaces as a hard context error with a nil slice, ahead of the incomplete verdict.
func (b *BambooHRFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	if tenant == "" {
		// Guard before templating the host, or the default would build the bogus
		// "https://.bamboohr.com/careers/list".
		return nil, fmt.Errorf("ats: bamboohr: empty tenant slug")
	}

	// The tenant slug is a trusted host label that goes into the host, so it is NOT
	// url.PathEscaped — escaping a host label is wrong (same reasoning as softgarden).
	board := strings.Replace(b.baseURL, "{tenant}", tenant, 1)

	var list bambooHRListResponse
	if err := b.getInto(ctx, board+"/careers/list", &list); err != nil {
		return nil, fmt.Errorf("ats: bamboohr list tenant %q: %w", tenant, err)
	}

	// detailFailed guards against a lying/low totalCount: a per-posting failure alone
	// makes the fetch untrustworthy even if the count still happens to reconcile.
	detailFailed := false
	budgetSpent := false
	detailCalls := 0
	listings := []*crawler.JobListing{}
	for _, item := range list.Result {
		// Abort (rather than skip every remaining posting) on a cancelled context, so a
		// cancellation surfaces as an error instead of a silently partial board.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The canonical posting URL is CONSTRUCTED from the row's id, so a row with no
		// id has no URL / dedup key and cannot be saved — skip it (the greenhouse/
		// SmartRecruiters/Manatal "no upsert key → skip" rule). The count cross-check
		// below reports the resulting shortfall.
		if item.ID == "" {
			continue
		}
		// Build from the LIST row first: identity, URL, title, department, location and
		// work arrangement never depend on the detail call succeeding.
		listing := mapBambooHRListItem(item, tenant)

		if detailCalls >= b.maxDetails {
			budgetSpent = true
			listings = append(listings, listing)
			continue
		}
		if detailCalls > 0 {
			// Pace between successive detail calls, never before the first (#140). The wait
			// is context-aware so a shutdown is not delayed by the full interval.
			if err := bambooHRWait(ctx, b.detailDelay); err != nil {
				return nil, err
			}
		}
		detailCalls++

		var detail bambooHRDetailResponse
		if err := b.getInto(ctx, board+"/careers/"+url.PathEscape(item.ID)+"/detail", &detail); err != nil {
			// A failed detail is posting-level, but a cancelled context is board-level:
			// surface it rather than degrade the run into a silently partial board. The
			// top-of-loop guard misses a cancellation landing during THIS iteration's call
			// (notably the final posting, which has no next iteration to catch it).
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			detailFailed = true
			slog.Warn("ats: bamboohr posting detail failed",
				"tenant", tenant, "postingID", item.ID, "err", err)
			if bambooHRPostingGone(err) {
				// 404/410: the posting went away between the list and the detail. DROP it
				// rather than save the list-derived form — upserting a gone posting would
				// reopen it in place under ADR-0035's reopen rule.
				continue
			}
			// 429/5xx/transport/decode: upstream is degraded and the posting is NOT known
			// gone, so keep its list-derived form (with no Posting Body). Collapsing this
			// into the 404 bucket is exactly the #140 bug.
			listings = append(listings, listing)
			continue
		}
		bambooHROverlayDetail(listing, detail.Result.JobOpening)
		listings = append(listings, listing)
	}

	// Completeness contract (ADR-0035): the sweep may run only on a provably complete
	// snapshot. The count cross-check (len(listings) < meta.totalCount) uniformly
	// catches a row dropped for a missing id, a posting dropped for a 404 detail, and
	// any server-side list cap should one ever appear (none observed live, and
	// limit/offset/page/size/perPage are all ignored — so the cap, if it exists,
	// degrades to skip-the-sweep rather than mass-closing a board).
	if len(listings) < list.Meta.TotalCount || detailFailed || budgetSpent {
		return listings, fmt.Errorf("ats: bamboohr tenant %q: %w", tenant, ErrBoardIncomplete)
	}
	return listings, nil
}

// getInto issues a GET for endpoint and decodes a size-capped, non-200-guarded,
// on-host body into dst. It sends only an Accept header and NO Authorization/token
// header: /careers/list and /careers/<id>/detail are the zero-auth endpoints the
// hosted careers site itself reads, while the credentialed
// api.bamboohr.com/api/gateway.php/<company>/v1/... (HTTP Basic, API key) is the
// dual-API trap this fetcher deliberately avoids
// (docs/research/ats-providers.md §BambooHR). A non-200 wraps ErrBoardStatus so
// callers can errors.Is it. A truncated body surfaces as a decode error via
// io.LimitReader (the ADR-0035 truncation-as-hard-error guarantee).
func (b *BambooHRFetcher) getInto(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	res, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	// Off-host guard — how BambooHR says "no such tenant". A nonexistent or
	// deactivated tenant does NOT 404: it 302s to https://www.bamboohr.com/, which
	// answers a Go client 403 text/html (verified live 2026-08-25). Left alone that
	// would reach classifyBoard as a status/decode error and read Inconclusive
	// forever, so a genuinely dead board would never tip its Career Page dormant and
	// its Open Job Listings would never Close (ADR-0035). Landing on a different host
	// than we asked for is therefore reported as a dead board (404). res.Request is
	// set by the real transport; a test RoundTripper that leaves it nil simply skips
	// the check.
	if res.Request != nil && res.Request.URL != nil && !strings.EqualFold(res.Request.URL.Host, req.URL.Host) {
		return fmt.Errorf("careers api: redirected off the board host to %q (no such tenant): %w",
			res.Request.URL.Host, &BoardStatusError{StatusCode: http.StatusNotFound})
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("careers api: %w", &BoardStatusError{StatusCode: res.StatusCode})
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBoardBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// bambooHRPostingGone reports whether a detail-call error means the posting itself is
// gone (404/410) rather than that the board is momentarily degraded (429/5xx, a
// transport blip, a decode failure). The two must not share a bucket: only the first
// justifies dropping the posting from the fetch (#140).
func bambooHRPostingGone(err error) bool {
	var se *BoardStatusError
	return errors.As(err, &se) && (se.StatusCode == http.StatusNotFound || se.StatusCode == http.StatusGone)
}

// bambooHRWait pauses for d, returning early with the context's error if it is
// cancelled first. A non-positive d does not pause at all.
func bambooHRWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bambooHRListResponse is the /careers/list envelope: {meta:{totalCount}, result:[…]}.
// Only the fields the mapper reads are declared; any others in the JSON are ignored
// by the decoder. TotalCount is the completeness oracle the count cross-check runs
// against (ADR-0035).
type bambooHRListResponse struct {
	Meta   bambooHRListMeta   `json:"meta"`
	Result []bambooHRListItem `json:"result"`
}

type bambooHRListMeta struct {
	TotalCount int `json:"totalCount"`
}

// bambooHRListItem is one open job opening as the list reports it. Its id is a
// STRING on the live board ("36"), not a number. Deliberately absent: isRemote (null
// on every list row and gone from the detail — a dead field), employmentStatusLabel
// (an employment type — Full-Time/Contractor — not a work arrangement, and
// JobListing has no field for it: the softgarden precedent), and departmentId (an
// opaque numeric that must never stand in for departmentLabel).
type bambooHRListItem struct {
	ID              string              `json:"id"` // stable posting id; builds the canonical URL and the Corpus SourceID (ADR-0034)
	JobOpeningName  string              `json:"jobOpeningName"`
	DepartmentLabel string              `json:"departmentLabel"`
	LocationType    string              `json:"locationType"` // "0" unstated / "1" remote / "2" hybrid
	Location        bambooHRLocation    `json:"location"`
	ATSLocation     bambooHRATSLocation `json:"atsLocation"`
}

// bambooHRDetailResponse is the /careers/<id>/detail envelope. Note the extra
// nesting the list does not have: result.jobOpening, not result.
type bambooHRDetailResponse struct {
	Result bambooHRDetailResult `json:"result"`
}

type bambooHRDetailResult struct {
	JobOpening bambooHRJobOpening `json:"jobOpening"`
}

// bambooHRJobOpening is one job opening's detail object — the only place the Posting
// Body, the publish date, and a country live. jobOpeningShareUrl is deliberately not
// declared: it equals the URL built from the tenant and the id (verified across
// giottoai/birdbuddy/vendasta), and constructing it keeps the upsert key derivable
// from the list alone, so a failed detail can never change a posting's identity.
// jobOpeningStatus is not declared either: the list IS the board's open set (the same
// set BambooHR's own renderer prints under "Open Positions"), so gating openness on a
// detail-only field would make it depend on the enrichment call succeeding.
type bambooHRJobOpening struct {
	JobOpeningName  string              `json:"jobOpeningName"`
	DepartmentLabel string              `json:"departmentLabel"`
	Description     string              `json:"description"` // single-encoded real HTML (verified live)
	DatePosted      string              `json:"datePosted"`  // date-only, "2006-01-02"
	LocationType    string              `json:"locationType"`
	Location        bambooHRLocation    `json:"location"`
	ATSLocation     bambooHRATSLocation `json:"atsLocation"`
}

// bambooHRLocation is the physical-address location object. postalCode and
// addressCountry exist only on the DETAIL — the list's copy carries city/state alone,
// which is why the detail call also buys Country resolution and not just the Posting
// Body. Every field is frequently JSON null, which decodes into a string as "" with
// no error, so no custom unmarshaler is needed.
type bambooHRLocation struct {
	City           string `json:"city"`
	State          string `json:"state"`
	PostalCode     string `json:"postalCode"`
	AddressCountry string `json:"addressCountry"`
}

// bambooHRATSLocation is the coarse alternate location object. province exists only
// on the LIST and countryId only on the detail, so this declares the union; an absent
// key decodes to the zero value.
type bambooHRATSLocation struct {
	City     string `json:"city"`
	State    string `json:"state"`
	Province string `json:"province"`
	Country  string `json:"country"`
}

// mapBambooHRListItem maps one list row to a Job Listing. Company and CompanyKey are
// deliberately left empty: the ATS ingest lane stamps Company from the embedding/seed
// page's Owner (ADR-0021/ADR-0022, #127) — BambooHR's board carries no company field
// worth trusting anyway. TechStack is not set (dropped in #125/ADR-0023). Description
// and FirstPublished stay empty here and are filled by the detail overlay; a posting
// whose detail failed transiently keeps this form.
func mapBambooHRListItem(item bambooHRListItem, tenant string) *crawler.JobListing {
	return &crawler.JobListing{
		// Live titles carry trailing spaces ("Machine Learning Engineer "), so trim.
		Title:           strings.TrimSpace(item.JobOpeningName),
		URL:             bambooHRPostingURL(tenant, item.ID),
		SourceID:        item.ID,
		Location:        bambooHRLocationText(item.Location, item.ATSLocation),
		CountryHint:     bambooHRCountryHint(item.Location, item.ATSLocation),
		Department:      strings.TrimSpace(item.DepartmentLabel),
		WorkArrangement: bambooHRWorkArrangement(item.LocationType),
	}
}

// bambooHROverlayDetail folds a posting's detail object onto its list-derived Job
// Listing, field by field and ONLY where the detail states something, so a sparser
// detail can never erase richer list data. The detail adds the Posting Body and
// datePosted outright, and upgrades Location/CountryHint (its location object carries
// addressCountry, which the list's does not).
func bambooHROverlayDetail(listing *crawler.JobListing, jo bambooHRJobOpening) {
	if title := strings.TrimSpace(jo.JobOpeningName); title != "" {
		listing.Title = title
	}
	if dept := strings.TrimSpace(jo.DepartmentLabel); dept != "" {
		listing.Department = dept
	}
	if loc := bambooHRLocationText(jo.Location, jo.ATSLocation); loc != "" {
		listing.Location = loc
	}
	if hint := bambooHRCountryHint(jo.Location, jo.ATSLocation); hint != "" {
		listing.CountryHint = hint
	}
	// The Posting Body (ADR-0041). BambooHR serves single-encoded real HTML — literal
	// tags, entities escaped exactly once ("Weights &amp; Biases"), literal NBSP — so
	// it reduces like Lever/Manatal/softgarden, not like Greenhouse's double-encoded
	// content.
	if desc := htmlSingleEncodedToText(jo.Description); desc != "" {
		listing.Description = desc
	}
	if t, ok := parseBambooHRDate(jo.DatePosted); ok {
		listing.FirstPublished = t
	}
	// Only ever an upgrade: an absent locationType on the detail must not downgrade a
	// positive one the list stated.
	if a := bambooHRWorkArrangement(jo.LocationType); a != crawler.WorkArrangementUnspecified {
		listing.WorkArrangement = a
	}
}

// bambooHRPostingURL builds the canonical posting URL on the REAL board host,
// regardless of any API baseURL override — the URL is the real-world upsert key, not
// an API address (the Manatal rule). It reproduces the board's own
// jobOpeningShareUrl exactly (verified across giottoai/birdbuddy/vendasta), so the
// upsert key is derivable from the list alone. Neither part is escaped: tenant is a
// trusted single DNS label (escaping a host label is wrong) and the id is a bare
// numeric string, and the stored URL must match the board's own share URL byte for
// byte (the detail ENDPOINT does escape the id — that one is an API address).
func bambooHRPostingURL(tenant, id string) string {
	return "https://" + tenant + "." + bambooHRPostingHost + "/careers/" + id
}

// bambooHRWorkArrangement maps the board's locationType to a Work Arrangement:
// "1" → remote, "2" → hybrid, everything else — "0", empty, or an unknown code —
// → unspecified (ADR-0030).
//
// The evidence is BambooHR's own board renderer (/jobs/embed2.php), checked
// posting-by-posting against the API on four tenants: it appends "Remote" for 1 and
// "(Hybrid)" for 2, and prints a bare city with NO arrangement word for 0. So the
// provider itself declines to characterise 0, and ADR-0030 is explicit that a source
// which does not POSITIVELY state the mode maps to unspecified, never onsite — a
// false onsite is a wrong answer in a SavedSearch's Work Arrangement filter, an
// unspecified is a visible gap. Reading 0 as a positive on-site selection is arguable
// (hybrid has its own code, and every observed locationType:"0" posting carries a
// full physical address) but cannot be settled from outside: 0 is also what an
// untouched form field would carry. Because the field decodes as a string, an absent
// or null locationType yields "" → unspecified, so no zero-value accident can produce
// onsite.
func bambooHRWorkArrangement(locationType string) crawler.WorkArrangement {
	switch locationType {
	case bambooHRRemoteLocationType:
		return crawler.WorkArrangementRemote
	case bambooHRHybridLocationType:
		return crawler.WorkArrangementHybrid
	default:
		return crawler.WorkArrangementUnspecified
	}
}

// bambooHRLocationText composes a readable Location, preferring the physical
// `location` object and FALLING BACK to the coarse `atsLocation` when it says
// nothing. The two really are alternates rather than parts of one address: across 51
// live postings exactly one of them is ever populated, keyed by locationType. So they
// are never merged — merging would invent a location for a posting the provider
// deliberately left coarse. postalCode is deliberately excluded: a bare postal code is
// noise in a displayed Location and buys the Country Resolver nothing, which reads the
// city/country tokens (ADR-0029). Parts are de-duplicated case-insensitively so a
// row whose city equals its state does not render "Berlin, Berlin". Empty when both
// objects are empty — such a posting is kept, never dropped (ADR-0028).
func bambooHRLocationText(loc bambooHRLocation, ats bambooHRATSLocation) string {
	if text := bambooHRJoin(loc.City, loc.State, loc.AddressCountry); text != "" {
		return text
	}
	return bambooHRJoin(ats.City, ats.State, ats.Province, ats.Country)
}

// bambooHRCountryHint returns the provider's structured country signal for the ingest
// lane to resolve at save (ADR-0029), preferring the detail's location.addressCountry
// and falling back to atsLocation.country. Both are country NAMES ("Switzerland",
// "Slovenia", "Canada"), which the Resolver handles; the list carries neither, so a
// list-only posting has no hint and falls back to its composed Location.
func bambooHRCountryHint(loc bambooHRLocation, ats bambooHRATSLocation) string {
	if c := strings.TrimSpace(loc.AddressCountry); c != "" {
		return c
	}
	return strings.TrimSpace(ats.Country)
}

// bambooHRJoin trims each part, drops the empty ones and any case-insensitive
// duplicate, and joins the rest with ", ".
func bambooHRJoin(parts ...string) string {
	kept := []string{}
	for _, raw := range parts {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		dup := false
		for _, existing := range kept {
			if strings.EqualFold(existing, v) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, v)
		}
	}
	return strings.Join(kept, ", ")
}

// parseBambooHRDate parses the board's datePosted, which is date-only
// ("2026-07-21") and so lands at midnight UTC — the same precision loss as Workable.
// ok is false (the caller keeps the zero time) on an empty or otherwise unparseable
// value: a bad timestamp must never drop a real posting.
func parseBambooHRDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
