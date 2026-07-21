package ats_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// newRecruiteeFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a RecruiteeFetcher pointed at it. It reuses serveJSON from
// greenhouse_test.go for the common always-reply-with-a-body handler.
func newRecruiteeFetcher(t *testing.T, handler http.HandlerFunc) *ats.RecruiteeFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewRecruiteeFetcher(ats.WithRecruiteeBaseURL(srv.URL))
}

// recruiteeSample is a faithful captured /api/offers/ payload: the {"offers":[...]}
// envelope, a custom-domain careers_url, the "YYYY-MM-DD HH:MM:SS UTC" published_at
// timestamp, a stray company_name that must be ignored, a flat location string plus
// structured locations[], and single-encoded HTML in description/requirements.
const recruiteeSample = `{
	"offers": [
		{
			"id": 100,
			"slug": "senior-frontend-engineer-100-remote-emea-1",
			"title": "Senior Frontend Engineer - 100% Remote - EMEA",
			"careers_url": "https://careers.hostaway.com/o/senior-frontend-engineer-100-remote-emea-1",
			"published_at": "2026-07-17 10:29:15 UTC",
			"created_at": "2026-07-15 09:00:00 UTC",
			"company_name": "Hostaway",
			"department": "Engineering",
			"location": "Remote job",
			"locations": [
				{"name": "Remote job", "city": "Lisbon", "state": "", "country": "Portugal", "country_code": "PT"}
			],
			"remote": true,
			"description": "<p>We are looking for a <strong>Senior Frontend Engineer</strong> to join us.</p>",
			"requirements": "<ul><li>5+ years experience</li><li>React</li></ul>"
		},
		{
			"id": 101,
			"slug": "product-manager",
			"title": "Product Manager",
			"careers_url": "https://careers.hostaway.com/o/product-manager",
			"published_at": "2026-07-16 08:00:00 UTC",
			"company_name": "Hostaway",
			"department": "Product",
			"location": "Barcelona",
			"locations": [],
			"description": "<p>Own the roadmap.</p>",
			"requirements": ""
		}
	]
}`

func TestRecruiteeFetchMapsBoard(t *testing.T) {
	fetcher := newRecruiteeFetcher(t, serveJSON(recruiteeSample))
	got, err := fetcher.Fetch(t.Context(), "hostaway")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Senior Frontend Engineer - 100% Remote - EMEA" {
		t.Errorf("Title = %q, want the offer title", first.Title)
	}
	// careers_url is a custom domain (not *.recruitee.com); it is still the key.
	if first.URL != "https://careers.hostaway.com/o/senior-frontend-engineer-100-remote-emea-1" {
		t.Errorf("URL = %q, want the custom-domain careers_url", first.URL)
	}
	if first.Location != "Remote job" {
		t.Errorf("Location = %q, want the flat location string", first.Location)
	}
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want %q", first.Department, "Engineering")
	}
	wantDesc := "We are looking for a Senior Frontend Engineer to join us.\n5+ years experience React"
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want %q", first.Description, wantDesc)
	}
	wantTime := time.Date(2026, 7, 17, 10, 29, 15, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	if got[1].Title != "Product Manager" || got[1].Department != "Product" || got[1].Location != "Barcelona" {
		t.Errorf("second listing = %+v, want the Product Manager / Product / Barcelona mapping", got[1])
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the
	// mapper must leave both empty on every returned listing. The offer's remote flag
	// is not mapped, so WorkArrangement is unspecified — never onsite (ADR-0030).
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
	}
}

func TestRecruiteeFetchEmptyBoard(t *testing.T) {
	fetcher := newRecruiteeFetcher(t, serveJSON(`{"offers":[]}`))

	got, err := fetcher.Fetch(t.Context(), "hostaway")
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

func TestRecruiteeSkipsOfferWithoutCareersURL(t *testing.T) {
	// careers_url is the upsert key (#127); an offer missing it cannot be saved and
	// is dropped rather than mapped to a keyless listing.
	body := `{"offers":[
		{"title":"Keyless"},
		{"title":"Keyed","careers_url":"https://careers.hostaway.com/o/keyed"}
	]}`
	fetcher := newRecruiteeFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "hostaway")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless offer is skipped)", len(got))
	}
	if got[0].URL != "https://careers.hostaway.com/o/keyed" {
		t.Errorf("kept listing URL = %q, want the offer that has a careers_url", got[0].URL)
	}
}

func TestRecruiteeDescriptionConcatenatesAndStrips(t *testing.T) {
	// description + requirements are single-encoded real HTML: tags are stripped and
	// the two non-empty parts join with a newline. An entity-encoded angle bracket in
	// the body text (&lt;10) must survive as a literal "<", proving the single-encode
	// helper (mirrors the Lever angle-bracket test) rather than a double-encode strip
	// that would swallow the run of text after it.
	body := `{"offers":[{
		"careers_url":"https://careers.hostaway.com/o/1",
		"description":"<p>Team of &lt;10 engineers.</p>",
		"requirements":"<ul><li>Go</li></ul>"
	}]}`
	fetcher := newRecruiteeFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "hostaway")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Team of <10 engineers.\nGo"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<p>") || strings.Contains(got[0].Description, "<li>") {
		t.Errorf("Description = %q, still contains a raw HTML tag", got[0].Description)
	}
}

func TestRecruiteeLocationFallback(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "flat location string preferred",
			body: `{"offers":[{"careers_url":"https://careers.hostaway.com/o/1","location":"Remote job","locations":[{"name":"Ignored"}]}]}`,
			want: "Remote job",
		},
		{
			name: "falls back to locations[0].name",
			body: `{"offers":[{"careers_url":"https://careers.hostaway.com/o/1","location":"","locations":[{"name":"Berlin Office","city":"Berlin","country":"Germany"}]}]}`,
			want: "Berlin Office",
		},
		{
			name: "falls back to city/state/country when name empty",
			body: `{"offers":[{"careers_url":"https://careers.hostaway.com/o/1","location":"","locations":[{"name":"","city":"Berlin","state":"","country":"Germany"}]}]}`,
			want: "Berlin, Germany",
		},
		{
			name: "empty when no location at all",
			body: `{"offers":[{"careers_url":"https://careers.hostaway.com/o/1","location":"","locations":[]}]}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := newRecruiteeFetcher(t, serveJSON(tc.body))

			got, err := fetcher.Fetch(t.Context(), "hostaway")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].Location != tc.want {
				t.Errorf("Location = %q, want %q", got[0].Location, tc.want)
			}
		})
	}
}

func TestRecruiteeMalformedPublishedAt(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unparseable timestamp",
			body: `{"offers":[{"title":"X","careers_url":"https://careers.hostaway.com/o/1","published_at":"2026-07-17T10:29:15Z"}]}`,
		},
		{
			name: "absent timestamp",
			body: `{"offers":[{"title":"X","careers_url":"https://careers.hostaway.com/o/1"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := newRecruiteeFetcher(t, serveJSON(tc.body))

			got, err := fetcher.Fetch(t.Context(), "hostaway")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1 (a bad timestamp must not drop the posting)", len(got))
			}
			if !got[0].FirstPublished.IsZero() {
				t.Errorf("FirstPublished = %v, want zero for a %s (fail-safe)", got[0].FirstPublished, tc.name)
			}
		})
	}
}

func TestRecruiteeBuildsBoardURL(t *testing.T) {
	var gotPath string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}
	fetcher := newRecruiteeFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "hostaway"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/api/offers/" {
		t.Errorf("request path = %q, want %q", gotPath, "/api/offers/")
	}
}

func TestRecruiteeTenantInHostSubstitution(t *testing.T) {
	// Recruitee puts the tenant in the subdomain, so the base templates a {tenant}
	// placeholder. A test base carrying the placeholder in the path proves the
	// substitution without needing real per-tenant subdomains.
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"offers":[]}`))
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	fetcher := ats.NewRecruiteeFetcher(ats.WithRecruiteeBaseURL(srv.URL + "/{tenant}"))

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/acme/api/offers/" {
		t.Errorf("request path = %q, want %q ({tenant} substituted)", gotPath, "/acme/api/offers/")
	}
}

func TestRecruiteeNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newRecruiteeFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestRecruiteeEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewRecruiteeFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestRecruiteeIgnoresProviderCompanyField(t *testing.T) {
	// The offer's own company_name must never populate Company: the mapper reads no
	// provider company field (the lane stamps it from the Owner).
	body := `{"offers":[{"title":"X","careers_url":"https://careers.hostaway.com/o/1","company_name":"Hostaway"}]}`
	fetcher := newRecruiteeFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "hostaway")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Company != "" {
		t.Errorf("Company = %q, want empty; the mapper must not read the offer's company_name", got[0].Company)
	}
}

func TestRecruiteeCatalogRecognition(t *testing.T) {
	// Pins the ProviderRecruitee const to what catalog.Identify emits for a
	// <tenant>.recruitee.com host — the invariant seed-time routing relies on.
	u, err := crawler.NewURL("https://acme.recruitee.com")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	id := catalog.Identify(u)
	if id.ATSProvider != ats.ProviderRecruitee {
		t.Errorf("ATSProvider = %q, want %q", id.ATSProvider, ats.ProviderRecruitee)
	}
	if id.CompanyKey != "recruitee:acme" {
		t.Errorf("CompanyKey = %q, want %q", id.CompanyKey, "recruitee:acme")
	}
}
