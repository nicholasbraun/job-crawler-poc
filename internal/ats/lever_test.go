package ats_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newLeverFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a LeverFetcher pointed at it. It reuses serveJSON from
// greenhouse_test.go for the common always-reply-with-a-body handler.
func newLeverFetcher(t *testing.T, handler http.HandlerFunc) *ats.LeverFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewLeverFetcher(ats.WithLeverBaseURL(srv.URL))
}

// leverRichPosting exercises the HTML-section reduction: an opening description
// with entities and an NBSP, one titled list, and a closing additional block.
const leverRichPosting = `[
	{
		"text": "Backend Engineer",
		"hostedUrl": "https://jobs.lever.co/acme/abc",
		"categories": {"department": "Engineering", "location": "Berlin"},
		"description": "<p>Build&nbsp;things &amp; ship</p>",
		"lists": [{"text": "Requirements", "content": "<ul><li>Go</li></ul>"}],
		"additional": "<p>Perks</p>",
		"workplaceType": "remote",
		"createdAt": 1600000000000
	}
]`

func TestLeverFetchMapsBoard(t *testing.T) {
	const body = `[
		{
			"id": "abc-id",
			"text": "Backend Engineer",
			"hostedUrl": "https://jobs.lever.co/acme/abc",
			"categories": {"department": "Engineering", "location": "Berlin"},
			"description": "<p>Build services</p>",
			"workplaceType": "remote",
			"createdAt": 1600000000000
		},
		{
			"text": "Product Designer",
			"hostedUrl": "https://jobs.lever.co/acme/def",
			"categories": {"department": "Design", "location": "Lisbon"},
			"description": "Design things",
			"workplaceType": "on-site"
		}
	]`

	fetcher := newLeverFetcher(t, serveJSON(body))
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
	if first.URL != "https://jobs.lever.co/acme/abc" {
		t.Errorf("URL = %q, want the hostedUrl", first.URL)
	}
	if first.SourceID != "abc-id" {
		t.Errorf("SourceID = %q, want the posting id %q", first.SourceID, "abc-id")
	}
	if first.Location != "Berlin" {
		t.Errorf("Location = %q, want %q", first.Location, "Berlin")
	}
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want %q", first.Department, "Engineering")
	}
	if first.WorkArrangement != crawler.WorkArrangementRemote {
		t.Errorf("WorkArrangement = %q, want remote for workplaceType remote", first.WorkArrangement)
	}
	if first.Description != "Build services" {
		t.Errorf("Description = %q, want %q", first.Description, "Build services")
	}
	wantTime := time.Date(2020, 9, 13, 12, 26, 40, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	second := got[1]
	if second.Title != "Product Designer" || second.Department != "Design" || second.Location != "Lisbon" {
		t.Errorf("second listing = %+v, want the Product Designer / Design / Lisbon mapping", second)
	}
	if second.WorkArrangement != crawler.WorkArrangementOnsite {
		t.Errorf("second.WorkArrangement = %q, want onsite for workplaceType on-site", second.WorkArrangement)
	}
	if !second.FirstPublished.IsZero() {
		t.Errorf("second.FirstPublished = %v, want zero for an omitted createdAt", second.FirstPublished)
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
		// Lever exposes no structured country field, so no hint is surfaced and the
		// ingest lane resolves the Country from the composed Location (ADR-0029).
		if l.CountryHint != "" {
			t.Errorf("listing[%d].CountryHint = %q, want empty (no structured country field)", i, l.CountryHint)
		}
	}
}

func TestLeverFetchEmptyBoard(t *testing.T) {
	fetcher := newLeverFetcher(t, serveJSON(`[]`))

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

func TestLeverDescriptionStripsSectionsAndUnescapes(t *testing.T) {
	fetcher := newLeverFetcher(t, serveJSON(leverRichPosting))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}

	// Opening description, list heading, list body, and closing additional each
	// become a plain-text line: tags stripped, entities decoded, NBSP collapsed.
	want := "Build things & ship\nRequirements\nGo\nPerks"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<") {
		t.Errorf("Description = %q, still contains a raw HTML tag", got[0].Description)
	}
}

func TestLeverDescriptionPreservesEntityEncodedAngleBrackets(t *testing.T) {
	// Lever's sections are single-encoded real HTML: text-level angle brackets
	// arrive as &lt;/&gt;. They must survive the reduction. A double-encoded
	// calibration would decode &lt; to a literal "<" before stripping tags and then
	// swallow the following text as a bogus tag, silently dropping "<10 engineers. "
	// and "<algorithm>" here.
	body := `[
		{
			"text": "Systems Engineer",
			"hostedUrl": "https://jobs.lever.co/acme/xyz",
			"description": "<p>Team of &lt;10 engineers. <strong>Apply.</strong></p>",
			"lists": [{"text": "Stack", "content": "<ul><li>C++ &amp; templates &lt;algorithm&gt; required</li></ul>"}],
			"additional": "<p>Response time &lt;2s</p>"
		}
	]`
	fetcher := newLeverFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}

	want := "Team of <10 engineers. Apply.\nStack\nC++ & templates <algorithm> required\nResponse time <2s"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
}

func TestLeverWorkplaceTypeArrangement(t *testing.T) {
	// workplaceType is Lever's positive signal, folded onto the enum: "on-site" -> onsite
	// (a positive on-site signal, not discarded), "hybrid" -> hybrid, and an absent or
	// unrecognized value -> unspecified, never onsite (ADR-0030).
	cases := []struct {
		workplaceType string
		want          crawler.WorkArrangement
	}{
		{"remote", crawler.WorkArrangementRemote},
		{"Remote", crawler.WorkArrangementRemote}, // folded case-insensitively
		{"on-site", crawler.WorkArrangementOnsite},
		{"hybrid", crawler.WorkArrangementHybrid},
		{"unspecified", crawler.WorkArrangementUnspecified},
		{"", crawler.WorkArrangementUnspecified},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.workplaceType), func(t *testing.T) {
			body := fmt.Sprintf(`[{"hostedUrl":"https://jobs.lever.co/acme/1","workplaceType":%q}]`, tc.workplaceType)
			fetcher := newLeverFetcher(t, serveJSON(body))

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].WorkArrangement != tc.want {
				t.Errorf("WorkArrangement = %q for workplaceType %q, want %q", got[0].WorkArrangement, tc.workplaceType, tc.want)
			}
		})
	}
}

func TestLeverBuildsBoardURL(t *testing.T) {
	var gotPath, gotMode string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMode = r.URL.Query().Get("mode")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}
	fetcher := newLeverFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/acme" {
		t.Errorf("request path = %q, want %q", gotPath, "/acme")
	}
	if gotMode != "json" {
		t.Errorf("mode query = %q, want %q", gotMode, "json")
	}
}

func TestLeverNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newLeverFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestLeverEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewLeverFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestLeverIgnoresProviderCompanyField(t *testing.T) {
	// A stray company/companyName blob in the board JSON must never populate
	// Company: the mapper reads no provider company field.
	body := `[{"text":"X","hostedUrl":"https://jobs.lever.co/acme/1","company":"SomeCorp","companyName":"SomeCorp"}]`
	fetcher := newLeverFetcher(t, serveJSON(body))

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

func TestLeverSkipsPostingWithoutHostedURL(t *testing.T) {
	// hostedUrl is the upsert key (#127); a posting missing it cannot be saved and
	// is dropped rather than mapped to a keyless listing.
	body := `[
		{"text":"Keyless","categories":{"location":"Berlin"}},
		{"text":"Keyed","hostedUrl":"https://jobs.lever.co/acme/1"}
	]`
	fetcher := newLeverFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless posting is skipped)", len(got))
	}
	if got[0].URL != "https://jobs.lever.co/acme/1" {
		t.Errorf("kept listing URL = %q, want the posting that has a hostedUrl", got[0].URL)
	}
}
