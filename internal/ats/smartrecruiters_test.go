package ats_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newSmartRecruitersFetcher stands up an httptest server with handler, registers
// its cleanup, and returns a SmartRecruitersFetcher pointed at it.
func newSmartRecruitersFetcher(t *testing.T, handler http.HandlerFunc) *ats.SmartRecruitersFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewSmartRecruitersFetcher(ats.WithSmartRecruitersBaseURL(srv.URL))
}

// srRecorder is an inline SmartRecruiters Posting API double. Its handler serves
// listByOffset(offset) for the list path and details[id] for each detail path (an
// id missing from details → 404, exercising the skip-on-unresolvable path), while
// recording every request's offset query, Authorization header, and the number of
// detail calls so tests can assert paging, no-auth, and no-detail-on-empty.
type srRecorder struct {
	mu           sync.Mutex
	listByOffset func(offset int) string
	details      map[string]string
	listOffsets  []int
	authHeaders  []string
	detailCalls  int
}

// srConst builds a recorder whose list body is constant (ignores offset).
func srConst(listBody string, details map[string]string) *srRecorder {
	return &srRecorder{
		listByOffset: func(int) string { return listBody },
		details:      details,
	}
}

func (rec *srRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.authHeaders = append(rec.authHeaders, r.Header.Get("Authorization"))
		rec.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		id := srDetailID(r.URL.Path)
		if id == "" {
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			rec.mu.Lock()
			rec.listOffsets = append(rec.listOffsets, offset)
			rec.mu.Unlock()
			_, _ = w.Write([]byte(rec.listByOffset(offset)))
			return
		}
		rec.mu.Lock()
		rec.detailCalls++
		rec.mu.Unlock()
		body, ok := rec.details[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

// srDetailID returns the posting id from a Posting API path, or "" for the list
// path (.../postings) which has no trailing id segment.
func srDetailID(path string) string {
	const marker = "/postings"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	return strings.TrimPrefix(path[i+len(marker):], "/")
}

// A captured-real Posting API sample (tenant BoschGroup): the list carries only
// posting summaries (id/name/ref) — NO postingUrl and NO jobAd — while each detail
// object carries postingUrl, an RFC3339 releasedDate with fractional seconds and a
// trailing Z, a structured location, function/department labels, and jobAd.sections
// whose text is single-encoded real HTML.
const srListBody = `{
	"offset": 0,
	"limit": 100,
	"totalFound": 2,
	"content": [
		{
			"id": "744000138449489",
			"uuid": "5c5c0b6e-0000-0000-0000-000000000001",
			"name": "Process Development Engineer (m/f/div.)",
			"ref": "https://api.smartrecruiters.com/v1/companies/BoschGroup/postings/744000138449489"
		},
		{
			"id": "744000138449490",
			"uuid": "5c5c0b6e-0000-0000-0000-000000000002",
			"name": "Product Designer",
			"ref": "https://api.smartrecruiters.com/v1/companies/BoschGroup/postings/744000138449490"
		}
	]
}`

const srDetailEngineer = `{
	"id": "744000138449489",
	"uuid": "5c5c0b6e-0000-0000-0000-000000000001",
	"name": "Process Development Engineer (m/f/div.)",
	"refNumber": "REF12345A",
	"company": {"identifier": "BoschGroup", "name": "Bosch Group"},
	"releasedDate": "2026-07-18T07:50:54.778Z",
	"location": {"city": "Changsha", "country": "cn", "region": "43", "remote": false},
	"function": {"id": "engineering", "label": "Engineering"},
	"department": {},
	"typeOfEmployment": {"label": "Regular"},
	"postingUrl": "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em",
	"applyUrl": "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em/apply",
	"jobAd": {
		"sections": {
			"companyDescription": {"title": "Company Description", "text": "<p>The Bosch Group is a leading global supplier.</p>"},
			"jobDescription": {"title": "Job Description", "text": "<p>Develop <strong>semiconductor</strong> process technology.</p>"},
			"qualifications": {"title": "Qualifications", "text": "<ul><li>Degree in Engineering</li></ul>"},
			"additionalInformation": {"title": "Additional Information", "text": "<p>Need more information? Contact us.</p>"}
		}
	}
}`

const srDetailDesigner = `{
	"id": "744000138449490",
	"name": "Product Designer",
	"releasedDate": "2026-06-01T09:00:00.000Z",
	"location": {"city": "Berlin", "country": "de", "remote": true},
	"function": {"id": "design", "label": "Design"},
	"department": {"label": "Design Team"},
	"postingUrl": "https://jobs.smartrecruiters.com/BoschGroup/744000138449490--product-designer",
	"jobAd": {
		"sections": {
			"jobDescription": {"title": "Job Description", "text": "<p>Shape the product experience.</p>"}
		}
	}
}`

func TestSmartRecruitersFetchMapsBoard(t *testing.T) {
	rec := srConst(srListBody, map[string]string{
		"744000138449489": srDetailEngineer,
		"744000138449490": srDetailDesigner,
	})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "boschgroup")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Process Development Engineer (m/f/div.)" {
		t.Errorf("Title = %q, want the posting name", first.Title)
	}
	if first.URL != "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em" {
		t.Errorf("URL = %q, want the detail postingUrl", first.URL)
	}
	if first.SourceID != "744000138449489" {
		t.Errorf("SourceID = %q, want the posting id (URL re-slug stable, ADR-0034)", first.SourceID)
	}
	if first.Location != "Changsha, cn" {
		t.Errorf("Location = %q, want %q (city, country)", first.Location, "Changsha, cn")
	}
	// CountryHint surfaces the structured location.country for the ingest lane to
	// resolve at save (ADR-0029).
	if first.CountryHint != "cn" {
		t.Errorf("CountryHint = %q, want %q (location.country)", first.CountryHint, "cn")
	}
	// department is an empty object on this posting, so function.label is the value.
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want the function label %q", first.Department, "Engineering")
	}
	// location.remote:false is NOT an on-site signal — it degrades to unspecified.
	if first.WorkArrangement != crawler.WorkArrangementUnspecified {
		t.Errorf("WorkArrangement = %q, want unspecified for location.remote=false (never onsite)", first.WorkArrangement)
	}
	wantDesc := "Company Description\nThe Bosch Group is a leading global supplier.\n" +
		"Job Description\nDevelop semiconductor process technology.\n" +
		"Qualifications\nDegree in Engineering\n" +
		"Additional Information\nNeed more information? Contact us."
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want %q", first.Description, wantDesc)
	}
	wantTime := time.Date(2026, 7, 18, 7, 50, 54, 778000000, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	second := got[1]
	if second.Title != "Product Designer" || second.Location != "Berlin, de" {
		t.Errorf("second listing = %+v, want the Product Designer / Berlin, de mapping", second)
	}
	if second.CountryHint != "de" {
		t.Errorf("second.CountryHint = %q, want %q (location.country)", second.CountryHint, "de")
	}
	// department.label is present here, so it wins over the function label.
	if second.Department != "Design Team" {
		t.Errorf("second.Department = %q, want the department label %q", second.Department, "Design Team")
	}
	if second.WorkArrangement != crawler.WorkArrangementRemote {
		t.Errorf("second.WorkArrangement = %q, want remote for location.remote=true", second.WorkArrangement)
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the
	// mapper must leave both empty on every returned listing.
	for i, l := range got {
		if l.Company != "" {
			t.Errorf("listing[%d].Company = %q, want empty (lane stamps it)", i, l.Company)
		}
		if l.CompanyKey != "" {
			t.Errorf("listing[%d].CompanyKey = %q, want empty (lane stamps it)", i, l.CompanyKey)
		}
	}
}

func TestSmartRecruitersEmptyBoard(t *testing.T) {
	rec := srConst(`{"offset":0,"limit":100,"totalFound":0,"content":[]}`, nil)
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "boschgroup")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got == nil {
		t.Fatal("Fetch returned a nil slice, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d listings, want 0", len(got))
	}
	if rec.detailCalls != 0 {
		t.Errorf("detail endpoint called %d times, want 0 for an empty board", rec.detailCalls)
	}
}

func TestSmartRecruitersPagination(t *testing.T) {
	page0 := `{"offset":0,"limit":100,"totalFound":3,"content":[{"id":"id-1"},{"id":"id-2"}]}`
	page2 := `{"offset":2,"limit":100,"totalFound":3,"content":[{"id":"id-3"}]}`
	detail := func(id string) string {
		return `{"name":"Role ` + id + `","postingUrl":"https://jobs.smartrecruiters.com/acme/` + id + `"}`
	}
	rec := &srRecorder{
		listByOffset: func(offset int) string {
			if offset == 0 {
				return page0
			}
			return page2
		},
		details: map[string]string{
			"id-1": detail("id-1"),
			"id-2": detail("id-2"),
			"id-3": detail("id-3"),
		},
	}
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 across two pages", len(got))
	}
	// The pager must advance offset by the first page's length, so the second list
	// request carries offset=2 (not a fixed page-size stride).
	wantOffsets := []int{0, 2}
	if len(rec.listOffsets) != len(wantOffsets) {
		t.Fatalf("list requests = %v, want offsets %v", rec.listOffsets, wantOffsets)
	}
	for i, o := range wantOffsets {
		if rec.listOffsets[i] != o {
			t.Errorf("list request[%d] offset = %d, want %d", i, rec.listOffsets[i], o)
		}
	}
}

func TestSmartRecruitersListNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newSmartRecruitersFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 list response", got)
	}
}

func TestSmartRecruitersSkipsPostingWithoutResolvableURL(t *testing.T) {
	// Two ids: one detail 404s (no resolvable URL), the other is valid. The board
	// still succeeds and returns only the resolvable posting.
	list := `{"offset":0,"limit":100,"totalFound":2,"content":[{"id":"gone"},{"id":"keeps"}]}`
	rec := srConst(list, map[string]string{
		"keeps": `{"name":"Kept","postingUrl":"https://jobs.smartrecruiters.com/acme/keeps"}`,
		// "gone" is absent → the double serves 404 for it.
	})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the unresolvable posting is skipped)", len(got))
	}
	if got[0].URL != "https://jobs.smartrecruiters.com/acme/keeps" {
		t.Errorf("kept listing URL = %q, want the posting whose detail resolved", got[0].URL)
	}
}

func TestSmartRecruitersSkipsPostingWithEmptyPostingURL(t *testing.T) {
	// A detail that returns 200 but omits postingUrl has no upsert key and is
	// dropped rather than mapped to a keyless listing (mirrors Greenhouse/Lever).
	list := `{"offset":0,"limit":100,"totalFound":2,"content":[{"id":"blank"},{"id":"keeps"}]}`
	rec := srConst(list, map[string]string{
		"blank": `{"name":"No URL","postingUrl":""}`,
		"keeps": `{"name":"Kept","postingUrl":"https://jobs.smartrecruiters.com/acme/keeps"}`,
	})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the URL-less posting is skipped)", len(got))
	}
	if got[0].URL != "https://jobs.smartrecruiters.com/acme/keeps" {
		t.Errorf("kept listing URL = %q, want the posting that has a postingUrl", got[0].URL)
	}
}

func TestSmartRecruitersCancelDuringFinalDetailReturnsError(t *testing.T) {
	// A context cancelled while the LAST posting's detail call is in flight must
	// surface as an error, not a silently partial board. The detail handler cancels
	// the context and then holds the connection open, so the in-flight GET fails with
	// context.Canceled; the fetcher must propagate that rather than skip the posting
	// and return (partial, nil).
	list := `{"offset":0,"limit":100,"totalFound":1,"content":[{"id":"only"}]}`
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if srDetailID(r.URL.Path) == "" {
			_, _ = w.Write([]byte(list))
			return
		}
		// Cancel the run while this detail request is in flight, then wait for the
		// client to drop the connection (r.Context() fires) so the response never
		// completes and the client's GET fails with context.Canceled.
		cancel()
		<-r.Context().Done()
	}
	fetcher := newSmartRecruitersFetcher(t, handler)

	got, err := fetcher.Fetch(ctx, "acme")
	if err == nil {
		t.Fatalf("Fetch err = nil, want a context error for cancellation during the final detail call; got %d listings", len(got))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil (no silently partial board)", got)
	}
}

func TestSmartRecruitersDescriptionStripsSingleEncodedHTML(t *testing.T) {
	// jobAd.sections[].text is single-encoded real HTML: literal tags plus text-level
	// entities. The reduction must strip the tags, decode &amp; to "&", and preserve
	// an entity-encoded angle bracket (&lt;algorithm&gt; -> <algorithm>) — proving the
	// single-encode helper, not the double-encode one, is applied.
	list := `{"offset":0,"limit":100,"totalFound":1,"content":[{"id":"one"}]}`
	detail := `{
		"name": "Systems Engineer",
		"postingUrl": "https://jobs.smartrecruiters.com/acme/one",
		"jobAd": {"sections": {"jobDescription": {"title": "Job Description", "text": "<p>Research &amp; development with C++ &lt;algorithm&gt;.</p>"}}}
	}`
	rec := srConst(list, map[string]string{"one": detail})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Job Description\nResearch & development with C++ <algorithm>."
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<p>") || strings.Contains(got[0].Description, "&amp;") {
		t.Errorf("Description = %q, still contains a raw tag or entity", got[0].Description)
	}
}

func TestSmartRecruitersMalformedReleasedDate(t *testing.T) {
	list := `{"offset":0,"limit":100,"totalFound":1,"content":[{"id":"one"}]}`
	detail := `{"name":"X","postingUrl":"https://jobs.smartrecruiters.com/acme/one","releasedDate":"not-a-date"}`
	rec := srConst(list, map[string]string{"one": detail})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (a bad timestamp must not drop the posting)", len(got))
	}
	if !got[0].FirstPublished.IsZero() {
		t.Errorf("FirstPublished = %v, want zero for a malformed timestamp (fail-safe)", got[0].FirstPublished)
	}
}

func TestSmartRecruitersWorkArrangement(t *testing.T) {
	// location.remote is a bare boolean, so only remote:true is a positive signal.
	// remote:false and an omitted remote both degrade to unspecified — a bare
	// "not remote" is never an on-site signal (ADR-0030's headline case).
	cases := []struct {
		name   string
		detail string
		want   crawler.WorkArrangement
	}{
		{"remote true", `{"name":"X","postingUrl":"https://jobs.smartrecruiters.com/acme/one","location":{"remote":true}}`, crawler.WorkArrangementRemote},
		{"remote false is unspecified not onsite", `{"name":"X","postingUrl":"https://jobs.smartrecruiters.com/acme/one","location":{"remote":false}}`, crawler.WorkArrangementUnspecified},
		{"remote omitted is unspecified", `{"name":"X","postingUrl":"https://jobs.smartrecruiters.com/acme/one","location":{"city":"Berlin"}}`, crawler.WorkArrangementUnspecified},
	}
	list := `{"offset":0,"limit":100,"totalFound":1,"content":[{"id":"one"}]}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := srConst(list, map[string]string{"one": tc.detail})
			fetcher := newSmartRecruitersFetcher(t, rec.handler())

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].WorkArrangement != tc.want {
				t.Errorf("WorkArrangement = %q, want %q", got[0].WorkArrangement, tc.want)
			}
		})
	}
}

func TestSmartRecruitersEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewSmartRecruitersFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestSmartRecruitersSendsNoAuthHeader(t *testing.T) {
	// Guards the dual-API trap: the public Posting API is unauthenticated, so the
	// fetcher must send no Authorization header on the list OR the detail call. The
	// partner Job Board API / feed/publications (X-SmartToken) are token-gated.
	rec := srConst(srListBody, map[string]string{
		"744000138449489": srDetailEngineer,
		"744000138449490": srDetailDesigner,
	})
	fetcher := newSmartRecruitersFetcher(t, rec.handler())

	if _, err := fetcher.Fetch(t.Context(), "boschgroup"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rec.authHeaders) < 3 {
		t.Fatalf("recorded %d requests, want at least 3 (1 list + 2 detail)", len(rec.authHeaders))
	}
	for i, h := range rec.authHeaders {
		if h != "" {
			t.Errorf("request[%d] Authorization = %q, want none sent", i, h)
		}
	}
}
