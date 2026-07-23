package ats_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newGreenhouseFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a GreenhouseFetcher pointed at it.
func newGreenhouseFetcher(t *testing.T, handler http.HandlerFunc) *ats.GreenhouseFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewGreenhouseFetcher(ats.WithGreenhouseBaseURL(srv.URL))
}

// serveJSON returns a handler that always replies with body as JSON.
func serveJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestGreenhouseFetchMapsBoard(t *testing.T) {
	const body = `{
		"jobs": [
			{
				"id": 12345,
				"title": "Backend Engineer",
				"absolute_url": "https://boards.greenhouse.io/acme/jobs/1",
				"content": "&lt;p&gt;Build &lt;strong&gt;Go&lt;/strong&gt; services&lt;/p&gt;",
				"first_published": "2026-01-02T15:04:05Z",
				"location": {"name": "Berlin"},
				"departments": [{"name": "Engineering"}, {"name": "Platform"}]
			},
			{
				"title": "Product Designer",
				"absolute_url": "https://boards.greenhouse.io/acme/jobs/2",
				"content": "Design things",
				"first_published": "2026-02-03T09:00:00Z",
				"location": {"name": "Remote"},
				"departments": [{"name": "Design"}]
			}
		]
	}`

	fetcher := newGreenhouseFetcher(t, serveJSON(body))
	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Backend Engineer" {
		t.Errorf("Title = %q, want %q", first.Title, "Backend Engineer")
	}
	if first.URL != "https://boards.greenhouse.io/acme/jobs/1" {
		t.Errorf("URL = %q, want the absolute_url", first.URL)
	}
	if first.SourceID != "12345" {
		t.Errorf("SourceID = %q, want the stringified board id %q", first.SourceID, "12345")
	}
	if first.Location != "Berlin" {
		t.Errorf("Location = %q, want %q", first.Location, "Berlin")
	}
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want the first department %q", first.Department, "Engineering")
	}
	if first.Description != "Build Go services" {
		t.Errorf("Description = %q, want %q", first.Description, "Build Go services")
	}
	wantTime := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	if got[1].Title != "Product Designer" || got[1].Department != "Design" {
		t.Errorf("second listing = %+v, want the Product Designer / Design mapping", got[1])
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the
	// mapper must leave both empty on every returned listing. The board API exposes
	// no working mode, so WorkArrangement is unspecified — never onsite (ADR-0030).
	for i, l := range got {
		if l.Company != "" {
			t.Errorf("listing[%d].Company = %q, want empty (lane stamps it)", i, l.Company)
		}
		if l.CompanyKey != "" {
			t.Errorf("listing[%d].CompanyKey = %q, want empty (lane stamps it)", i, l.CompanyKey)
		}
		if l.WorkArrangement != crawler.WorkArrangementUnspecified {
			t.Errorf("listing[%d].WorkArrangement = %q, want unspecified (silent provider, never onsite)", i, l.WorkArrangement)
		}
		// Greenhouse exposes no structured country field, so no hint is surfaced and
		// the ingest lane resolves the Country from the composed Location (ADR-0029).
		if l.CountryHint != "" {
			t.Errorf("listing[%d].CountryHint = %q, want empty (no structured country field)", i, l.CountryHint)
		}
	}
}

func TestGreenhouseFetchEmptyBoard(t *testing.T) {
	fetcher := newGreenhouseFetcher(t, serveJSON(`{"jobs":[]}`))

	got, err := fetcher.Fetch(t.Context(), "acme")
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

func TestGreenhouseDescriptionStrippedAndUnescaped(t *testing.T) {
	body := `{"jobs":[{"absolute_url":"https://boards.greenhouse.io/acme/jobs/1","content":"&lt;p&gt;Build &lt;strong&gt;Go&lt;/strong&gt; services&lt;/p&gt;"}]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Description != "Build Go services" {
		t.Errorf("Description = %q, want %q", got[0].Description, "Build Go services")
	}
}

func TestGreenhouseDescriptionDecodesTextNodeEntities(t *testing.T) {
	// The board content is entity-encoded HTML, so its text nodes stay
	// entity-encoded after one unescape (e.g. &amp;nbsp;, &amp;amp;). The stored
	// description must be fully decoded plain text, not literal &amp;/&nbsp;, or a
	// keyword search for "R&D" would never match a stored "R&amp;D".
	body := `{"jobs":[{"absolute_url":"https://boards.greenhouse.io/acme/jobs/1","content":"&lt;p&gt;Join the R&amp;amp;D&amp;nbsp;team&lt;/p&gt;"}]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Description != "Join the R&D team" {
		t.Errorf("Description = %q, want %q", got[0].Description, "Join the R&D team")
	}
}

func TestGreenhouseMissingDepartment(t *testing.T) {
	body := `{"jobs":[{"title":"X","absolute_url":"https://boards.greenhouse.io/acme/jobs/1","departments":[]}]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Department != "" {
		t.Errorf("Department = %q, want empty for an absent department array", got[0].Department)
	}
}

func TestGreenhouseIgnoresProviderCompanyField(t *testing.T) {
	// A stray company/companies blob in the board JSON must never populate
	// Company: the mapper reads no provider company field.
	body := `{"jobs":[{"title":"X","absolute_url":"https://boards.greenhouse.io/acme/jobs/1","company":"SomeCorp","companies":[{"name":"SomeCorp"}]}]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Company != "" {
		t.Errorf("Company = %q, want empty; the mapper must not read a provider company field", got[0].Company)
	}
}

func TestGreenhouseSkipsPostingWithoutAbsoluteURL(t *testing.T) {
	// absolute_url is the upsert key (#127); a posting missing it cannot be saved
	// and is dropped rather than mapped to a keyless listing (mirrors Lever).
	body := `{"jobs":[
		{"title":"Keyless"},
		{"title":"Keyed","absolute_url":"https://boards.greenhouse.io/acme/jobs/1"}
	]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless posting is skipped)", len(got))
	}
	if got[0].URL != "https://boards.greenhouse.io/acme/jobs/1" {
		t.Errorf("kept listing URL = %q, want the posting that has an absolute_url", got[0].URL)
	}
}

func TestGreenhouseBuildsBoardURL(t *testing.T) {
	var gotPath, gotContent string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContent = r.URL.Query().Get("content")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}
	fetcher := newGreenhouseFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/v1/boards/acme/jobs" {
		t.Errorf("request path = %q, want %q", gotPath, "/v1/boards/acme/jobs")
	}
	if gotContent != "true" {
		t.Errorf("content query = %q, want %q", gotContent, "true")
	}
}

func TestGreenhouseNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newGreenhouseFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestGreenhouseMalformedFirstPublished(t *testing.T) {
	body := `{"jobs":[{"title":"X","absolute_url":"https://boards.greenhouse.io/acme/jobs/1","first_published":"not-a-date"}]}`
	fetcher := newGreenhouseFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if !got[0].FirstPublished.IsZero() {
		t.Errorf("FirstPublished = %v, want zero for a malformed timestamp (fail-safe)", got[0].FirstPublished)
	}
}

func TestGreenhouseEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewGreenhouseFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}
