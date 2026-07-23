package ats_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newManatalFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a ManatalFetcher pointed at it.
func newManatalFetcher(t *testing.T, handler http.HandlerFunc) *ats.ManatalFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewManatalFetcher(ats.WithManatalBaseURL(srv.URL))
}

// manatalRecorder is an inline Open API list double. Its handler serves
// bodyByPage(page) for every /open/v1/career-pages/<slug>/job-posts request while
// recording each request's page query and Authorization header so tests can assert
// paging and the no-auth trap guard.
type manatalRecorder struct {
	mu          sync.Mutex
	bodyByPage  func(page int) string
	pages       []int
	authHeaders []string
}

// manatalConst builds a recorder whose list body is constant (ignores page).
func manatalConst(body string) *manatalRecorder {
	return &manatalRecorder{bodyByPage: func(int) string { return body }}
}

func (rec *manatalRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		rec.mu.Lock()
		rec.pages = append(rec.pages, page)
		rec.authHeaders = append(rec.authHeaders, r.Header.Get("Authorization"))
		rec.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rec.bodyByPage(page)))
	}
}

// manatalListBody is a trimmed but faithful capture of the live Open API list
// payload for tenant "manatal" (api.careers-page.com/open/v1/career-pages/manatal/
// job-posts, 2026-07-23): the {items,total,page,size,pages} envelope, a UUID id,
// a translations[] array of {language_code,name,description} (description is
// single-encoded real HTML), location.{city,state,country}, and a client whose name
// is the department. published_at is absent from index items. Extra fields (status,
// latitude, external_id, …) are kept to prove the decoder ignores them. Trimmed to
// three postings with total/pages set to match, so it is a COMPLETE board.
const manatalListBody = `{
	"items": [
		{
			"id": "324a6547-405f-4fff-af4e-79db3e0dfee2",
			"status": "published",
			"visibility": "public",
			"location": {"country": "Thailand", "city": "Bangkok", "state": "Bangkok", "latitude": 0.0, "longitude": 0.0},
			"translations": [
				{"language_code": "en", "name": "Sales Operations Internship", "description": "<p><strong>About Manatal</strong></p><p>Manatal&nbsp;is backed by Surge &amp; Sequoia Capital.</p>"}
			],
			"client": {"name": "Customer Success", "external_id": 2966, "id": "0272d31e-719e-428f-85a2-02cd6c7a15b8"}
		},
		{
			"id": "6da53b4c-ccd7-4b0f-a171-d7ea493d08c3",
			"status": "published",
			"location": {"country": "Thailand", "city": "Bangkok", "state": "Bangkok"},
			"translations": [
				{"language_code": "en", "name": "Software Engineer (Full Stack)", "description": "<p>Manatal is an HRTech SaaS company headquartered in Bangkok.</p>"}
			],
			"client": {"name": "Engineering", "external_id": 212535, "id": "3b0649c1-d72e-49ba-a78c-3911396a576b"}
		},
		{
			"id": "723bd338-9b03-4c79-af16-e8c39e8fa075",
			"status": "published",
			"location": {"country": "Thailand", "city": "Bangkok", "state": "Bangkok"},
			"translations": [
				{"language_code": "en", "name": "Senior DevOps Engineer", "description": "<p><b><strong>About Manatal</strong></b></p><p>We are hiring a Senior DevOps Engineer.</p>"}
			],
			"client": {"name": "Engineering", "external_id": 212535, "id": "3b0649c1-d72e-49ba-a78c-3911396a576b"}
		}
	],
	"total": 3,
	"page": 1,
	"size": 250,
	"pages": 1
}`

func TestManatalFetchMapsBoard(t *testing.T) {
	fetcher := newManatalFetcher(t, manatalConst(manatalListBody).handler())

	got, err := fetcher.Fetch(t.Context(), "manatal")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3", len(got))
	}

	first := got[0]
	if first.Title != "Sales Operations Internship" {
		t.Errorf("Title = %q, want the first translation name", first.Title)
	}
	// The canonical posting URL is constructed on the REAL board host even though the
	// list is served from httptest (WithManatalBaseURL overrides only the API base).
	if first.URL != "https://manatal.careers-page.com/jobs/324a6547-405f-4fff-af4e-79db3e0dfee2" {
		t.Errorf("URL = %q, want the constructed <tenant>.careers-page.com/jobs/<id>", first.URL)
	}
	if first.SourceID != "324a6547-405f-4fff-af4e-79db3e0dfee2" {
		t.Errorf("SourceID = %q, want the item id (URL re-slug stable, ADR-0034)", first.SourceID)
	}
	if first.Location != "Bangkok, Bangkok, Thailand" {
		t.Errorf("Location = %q, want %q (city, state, country)", first.Location, "Bangkok, Bangkok, Thailand")
	}
	// CountryHint surfaces the structured location.country for the ingest lane (ADR-0029).
	if first.CountryHint != "Thailand" {
		t.Errorf("CountryHint = %q, want %q (location.country)", first.CountryHint, "Thailand")
	}
	// client.name is the department per the verified field map, NOT the company.
	if first.Department != "Customer Success" {
		t.Errorf("Department = %q, want the client.name %q", first.Department, "Customer Success")
	}
	if first.WorkArrangement != crawler.WorkArrangementUnspecified {
		t.Errorf("WorkArrangement = %q, want unspecified (index has no working-mode field)", first.WorkArrangement)
	}
	if first.Description == "" {
		t.Error("Description is empty, want the stripped translation description")
	}
	if strings.Contains(first.Description, "<p>") || strings.Contains(first.Description, "&amp;") || strings.Contains(first.Description, "&nbsp;") {
		t.Errorf("Description = %q, still contains a raw tag or entity", first.Description)
	}
	// Decision A: the index omits published_at and no detail call is made, so
	// FirstPublished is left zero.
	if !first.FirstPublished.IsZero() {
		t.Errorf("FirstPublished = %v, want zero (index omits published_at)", first.FirstPublished)
	}

	second := got[1]
	if second.Title != "Software Engineer (Full Stack)" || second.Department != "Engineering" {
		t.Errorf("second listing = %+v, want the Software Engineer / Engineering mapping", second)
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the mapper
	// must leave both empty on every returned listing (ADR-0022).
	for i, l := range got {
		if l.Company != "" {
			t.Errorf("listing[%d].Company = %q, want empty (lane stamps it)", i, l.Company)
		}
		if l.CompanyKey != "" {
			t.Errorf("listing[%d].CompanyKey = %q, want empty (lane stamps it)", i, l.CompanyKey)
		}
	}
}

func TestManatalEmptyBoard(t *testing.T) {
	fetcher := newManatalFetcher(t, manatalConst(`{"items":[],"total":0,"page":1,"size":250,"pages":0}`).handler())

	got, err := fetcher.Fetch(t.Context(), "manatal")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got == nil {
		t.Fatal("Fetch returned a nil slice, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d listings, want 0", len(got))
	}
}

func TestManatalPagination(t *testing.T) {
	page1 := `{"items":[
		{"id":"id-1","translations":[{"name":"Role 1"}],"location":{},"client":{}},
		{"id":"id-2","translations":[{"name":"Role 2"}],"location":{},"client":{}}
	],"total":3,"page":1,"size":250,"pages":2}`
	page2 := `{"items":[
		{"id":"id-3","translations":[{"name":"Role 3"}],"location":{},"client":{}}
	],"total":3,"page":2,"size":250,"pages":2}`
	rec := &manatalRecorder{
		bodyByPage: func(page int) string {
			if page <= 1 {
				return page1
			}
			return page2
		},
	}
	fetcher := newManatalFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 across two pages", len(got))
	}
	// The pager is 1-based and advances by one page per request.
	wantPages := []int{1, 2}
	if len(rec.pages) != len(wantPages) {
		t.Fatalf("list requests = %v, want pages %v", rec.pages, wantPages)
	}
	for i, p := range wantPages {
		if rec.pages[i] != p {
			t.Errorf("list request[%d] page = %d, want %d", i, rec.pages[i], p)
		}
	}
}

func TestManatalIncompleteOnCountMismatch(t *testing.T) {
	// The board claims total:3 but delivers two items then an empty page — it returned
	// fewer job-posts than it claimed. Both are mapped; the fetch is still
	// ErrBoardIncomplete because the enumerated count fell short (ADR-0035 cross-check).
	page1 := `{"items":[
		{"id":"id-1","translations":[{"name":"Role 1"}],"location":{},"client":{}},
		{"id":"id-2","translations":[{"name":"Role 2"}],"location":{},"client":{}}
	],"total":3,"page":1,"size":250,"pages":2}`
	page2 := `{"items":[],"total":3,"page":2,"size":250,"pages":2}`
	rec := &manatalRecorder{
		bodyByPage: func(page int) string {
			if page <= 1 {
				return page1
			}
			return page2
		},
	}
	fetcher := newManatalFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want it to wrap ErrBoardIncomplete (delivered 2 of a claimed 3)", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2 (the partial slice the fetcher did collect)", len(got))
	}
	// The partial slice is real data, not a nil placeholder: each listing is mapped.
	for i, l := range got {
		if l.Title == "" || l.URL == "" {
			t.Errorf("listing[%d] = %+v, want a fully mapped posting in the partial slice", i, l)
		}
	}
}

func TestManatalIncompleteOnPaginationCap(t *testing.T) {
	// With the pagination cap set to 1 page, the first page (total:3, two items) stops
	// the loop before the reported total is reached, so the fetch is ErrBoardIncomplete
	// even though both items map (ADR-0035).
	page1 := `{"items":[
		{"id":"id-1","translations":[{"name":"Role 1"}],"location":{},"client":{}},
		{"id":"id-2","translations":[{"name":"Role 2"}],"location":{},"client":{}}
	],"total":3,"page":1,"size":250,"pages":2}`
	srv := httptest.NewServer(manatalConst(page1).handler())
	t.Cleanup(srv.Close)
	fetcher := ats.NewManatalFetcher(
		ats.WithManatalBaseURL(srv.URL),
		ats.WithManatalMaxPages(1),
	)

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want it to wrap ErrBoardIncomplete (pagination cap hit before total)", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2 (the partial slice collected before the cap)", len(got))
	}
}

func TestManatalNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newManatalFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestManatalSkipsPostingWithoutURL(t *testing.T) {
	// The canonical URL is constructed from the item id, so an item with an empty id
	// has no upsert key and is dropped rather than mapped to a keyless listing. Dropping
	// it makes the collected count fall short of total, so the fetch is
	// ErrBoardIncomplete (ADR-0035 count cross-check); the id-bearing posting still rides.
	body := `{"items":[
		{"id":"","translations":[{"name":"No id"}],"location":{},"client":{}},
		{"id":"keeps","translations":[{"name":"Kept"}],"location":{},"client":{}}
	],"total":2,"page":1,"size":250,"pages":1}`
	fetcher := newManatalFetcher(t, manatalConst(body).handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want it to wrap ErrBoardIncomplete (a dropped id-less posting)", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the id-less posting is skipped)", len(got))
	}
	if got[0].URL != "https://acme.careers-page.com/jobs/keeps" {
		t.Errorf("kept listing URL = %q, want the constructed URL for the id-bearing posting", got[0].URL)
	}
}

func TestManatalDescriptionStripsHTML(t *testing.T) {
	// A translation description is single-encoded real HTML: literal tags plus text-level
	// entities. The reduction must strip the tags, decode &amp; to "&", and preserve an
	// entity-encoded angle bracket (&lt;C++&gt; -> <C++>) — proving the single-encode
	// helper, not the double-encode one, is applied.
	body := `{"items":[
		{"id":"one","translations":[{"name":"Systems Engineer","description":"<p>Research &amp; dev &lt;C++&gt;.</p>"}],"location":{},"client":{}}
	],"total":1,"page":1,"size":250,"pages":1}`
	fetcher := newManatalFetcher(t, manatalConst(body).handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Research & dev <C++>."
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<p>") || strings.Contains(got[0].Description, "&amp;") {
		t.Errorf("Description = %q, still contains a raw tag or entity", got[0].Description)
	}
}

func TestManatalEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewManatalFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestManatalSendsNoAuthHeader(t *testing.T) {
	// Guards the dual-API trap: the /open/v1 API is public, so the fetcher must send no
	// Authorization header. The /api/v1 recruiter API is JWT-Bearer-gated and avoided.
	rec := manatalConst(manatalListBody)
	fetcher := newManatalFetcher(t, rec.handler())

	if _, err := fetcher.Fetch(t.Context(), "manatal"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rec.authHeaders) == 0 {
		t.Fatal("recorded 0 requests, want at least 1")
	}
	for i, h := range rec.authHeaders {
		if h != "" {
			t.Errorf("request[%d] Authorization = %q, want none sent", i, h)
		}
	}
}
