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

// newSoftgardenFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a SoftgardenFetcher pointed at it. It reuses serveJSON from
// greenhouse_test.go for the common always-reply-with-a-body handler.
func newSoftgardenFetcher(t *testing.T, handler http.HandlerFunc) *ats.SoftgardenFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewSoftgardenFetcher(ats.WithSoftgardenBaseURL(srv.URL))
}

// softgardenSample is a faithful, trimmed copy of the live
// https://demo.career.softgarden.de/jobs.feed.json response (captured 2026-07-24) — a
// schema.org DataFeed whose dataFeedElement wraps each JobPosting under "item". Item 1
// is the real Communications Manager posting; item 2 is the real Senior IT Architect
// posting, which carries a present locality/country, "-" street/postal placeholders,
// and an &amp; in its description. The descriptions are trimmed, but their key text is
// preserved verbatim from the live capture.
const softgardenSample = `{
	"@context": "https://schema.org",
	"@type": "DataFeed",
	"numberOfItems": 2,
	"dataFeedElement": [
		{
			"@type": "DataFeedItem",
			"item": {
				"@type": "JobPosting",
				"title": "Communications Manager (f/m/d)",
				"url": "https://demo.career.softgarden.de/jobs/32701543/Communications-Manager-f-m-d-/",
				"datePosted": "2024-09-05T11:55:12.145+02:00",
				"identifier": {"name": "DEMO", "@type": "PropertyValue", "value": 32701543},
				"description": "<h2>Communications Manager (f/m/d) - Berlin, Deutschland</h2>\n<h3>Job Description</h3>\n<p>We are looking for a Communications Manager (f/m/d) to join our E-Commerce team in Berlin, Deutschland.</p>\n<ul>\n<li>Developing and executing a comprehensive communications strategy</li>\n</ul>",
				"employmentType": "FULL_TIME",
				"jobLocation": {"@type": "Place", "address": {"@type": "PostalAddress", "postalCode": "-", "addressRegion": "Berlin", "streetAddress": "-", "addressCountry": "Deutschland", "addressLocality": "Berlin"}}
			}
		},
		{
			"@type": "DataFeedItem",
			"item": {
				"@type": "JobPosting",
				"title": "Senior IT Architect - Infrastructure & Security Technologies (m/f/d)",
				"url": "https://demo.career.softgarden.de/jobs/32701587/Senior-IT-Architect---Infrastructure-Security-Technologies-m-f-d-/",
				"datePosted": "2024-09-05T11:55:11.872+02:00",
				"identifier": {"name": "DEMO", "@type": "PropertyValue", "value": 32701587},
				"description": "<h2>Job Description</h2>\n<p>We are looking for a Senior IT Architect - Infrastructure &amp; Security Technologies (m/f/d) to join our E-Commerce team in Prag, Tschechien.</p>\n<ul>\n<li>Designing, developing and implementing IT solutions.</li>\n</ul>",
				"employmentType": "OTHER",
				"jobLocation": {"@type": "Place", "address": {"@type": "PostalAddress", "postalCode": "-", "addressRegion": "Prag", "streetAddress": "-", "addressCountry": "Tschechien", "addressLocality": "Prag"}}
			}
		}
	]
}`

func TestSoftgardenFetchMapsBoard(t *testing.T) {
	fetcher := newSoftgardenFetcher(t, serveJSON(softgardenSample))
	got, err := fetcher.Fetch(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Communications Manager (f/m/d)" {
		t.Errorf("Title = %q, want the posting title", first.Title)
	}
	if first.URL != "https://demo.career.softgarden.de/jobs/32701543/Communications-Manager-f-m-d-/" {
		t.Errorf("URL = %q, want the canonical item.url", first.URL)
	}
	// SourceID comes from identifier.value — the stable numeric posting id — not the
	// url, whose slug is mutable (ADR-0034).
	if first.SourceID != "32701543" {
		t.Errorf("SourceID = %q, want the stringified identifier.value %q", first.SourceID, "32701543")
	}
	// "-" street/postal placeholders are dropped and the duplicate locality/region
	// (Berlin/Berlin) is deduped, leaving "Berlin, Deutschland".
	if first.Location != "Berlin, Deutschland" {
		t.Errorf("Location = %q, want %q", first.Location, "Berlin, Deutschland")
	}
	if first.CountryHint != "Deutschland" {
		t.Errorf("CountryHint = %q, want %q (structured addressCountry)", first.CountryHint, "Deutschland")
	}
	// The description is single-encoded HTML: tags stripped, entities decoded. The h2
	// text survives and no raw markup remains.
	if !strings.Contains(first.Description, "Communications Manager (f/m/d) - Berlin, Deutschland") {
		t.Errorf("Description = %q, want it to contain the h2 heading text", first.Description)
	}
	if strings.Contains(first.Description, "<") {
		t.Errorf("Description = %q, still contains a raw angle bracket", first.Description)
	}
	// Item 2 carries an &amp; in its body, which must decode to a literal ampersand so
	// a keyword search for "Infrastructure & Security" matches.
	if !strings.Contains(got[1].Description, "Infrastructure & Security") {
		t.Errorf("got[1].Description = %q, want the de-entity'd %q", got[1].Description, "Infrastructure & Security")
	}
	// 11:55:12.145 +02:00 == 09:55:12.145Z; .Equal compares instants across zones.
	wantTime := time.Date(2024, 9, 5, 9, 55, 12, 145000000, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the mapper
	// leaves both empty. softgarden has no department field, so Department is empty. The
	// employmentType is not a work arrangement and the remote/hybrid wording lives only
	// in free-text HTML, so WorkArrangement is unspecified — never onsite (ADR-0030).
	for i, l := range got {
		if l.Company != "" {
			t.Errorf("listing[%d].Company = %q, want empty (lane stamps it)", i, l.Company)
		}
		if l.CompanyKey != "" {
			t.Errorf("listing[%d].CompanyKey = %q, want empty (lane stamps it)", i, l.CompanyKey)
		}
		if l.Department != "" {
			t.Errorf("listing[%d].Department = %q, want empty (softgarden has no department field)", i, l.Department)
		}
		if l.WorkArrangement != crawler.WorkArrangementUnspecified {
			t.Errorf("listing[%d].WorkArrangement = %q, want unspecified (silent provider, never onsite)", i, l.WorkArrangement)
		}
	}
}

func TestSoftgardenFetchEmptyBoard(t *testing.T) {
	fetcher := newSoftgardenFetcher(t, serveJSON(`{"dataFeedElement":[]}`))

	got, err := fetcher.Fetch(t.Context(), "demo")
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

func TestSoftgardenSkipsItemWithoutURL(t *testing.T) {
	// item.url is the upsert key (#127); an element missing it cannot be saved and is
	// dropped rather than mapped to a keyless listing.
	body := `{"dataFeedElement":[
		{"item":{"title":"Keyless"}},
		{"item":{"title":"Keyed","url":"https://demo.career.softgarden.de/jobs/1/keyed/"}}
	]}`
	fetcher := newSoftgardenFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless item is skipped)", len(got))
	}
	if got[0].URL != "https://demo.career.softgarden.de/jobs/1/keyed/" {
		t.Errorf("kept listing URL = %q, want the item that has a url", got[0].URL)
	}
}

func TestSoftgardenDescriptionStrips(t *testing.T) {
	// The description is single-encoded real HTML: tags are stripped and text-level
	// entities are unescaped exactly once, so an entity-encoded angle bracket in the
	// body (&lt;10) survives as a literal "<" rather than being swallowed by a
	// double-encode strip.
	body := `{"dataFeedElement":[{"item":{
		"url":"https://demo.career.softgarden.de/jobs/1/x/",
		"description":"<p>Team of &lt;10 engineers.</p><ul><li>Go</li></ul>"
	}}]}`
	fetcher := newSoftgardenFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Team of <10 engineers. Go"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<p>") || strings.Contains(got[0].Description, "<li>") {
		t.Errorf("Description = %q, still contains a raw HTML tag", got[0].Description)
	}
}

// TestSoftgardenLocationComposition drives softgardenLocation + the "-" placeholder
// handling: absent-field placeholders are dropped, a duplicate locality/region is
// deduped, and the surviving fields join in [street, postal, locality, region,
// country] order.
func TestSoftgardenLocationComposition(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "all placeholders yield empty",
			addr: `{"streetAddress":"-","postalCode":"-","addressLocality":"-","addressRegion":"-","addressCountry":"-"}`,
			want: "",
		},
		{
			name: "duplicate locality/region deduped",
			addr: `{"streetAddress":"-","postalCode":"-","addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"Deutschland"}`,
			want: "Berlin, Deutschland",
		},
		{
			name: "country only",
			addr: `{"addressCountry":"Deutschland"}`,
			want: "Deutschland",
		},
		{
			name: "street and postal present keep order",
			addr: `{"streetAddress":"Hauptstr 1","postalCode":"10115","addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"Deutschland"}`,
			want: "Hauptstr 1, 10115, Berlin, Deutschland",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"dataFeedElement":[{"item":{"url":"https://demo.career.softgarden.de/jobs/1/x/","jobLocation":{"address":` + tc.addr + `}}}]}`
			fetcher := newSoftgardenFetcher(t, serveJSON(body))

			got, err := fetcher.Fetch(t.Context(), "demo")
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

// TestSoftgardenCountryHint asserts the structured country signal the mapper surfaces
// for the ingest lane to resolve at save (ADR-0029): addressCountry when present, and
// empty when it is the "-" placeholder or absent.
func TestSoftgardenCountryHint(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "country present",
			addr: `{"addressCountry":"Deutschland"}`,
			want: "Deutschland",
		},
		{
			name: "placeholder country yields empty",
			addr: `{"addressCountry":"-"}`,
			want: "",
		},
		{
			name: "absent country yields empty",
			addr: `{"addressLocality":"Berlin"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"dataFeedElement":[{"item":{"url":"https://demo.career.softgarden.de/jobs/1/x/","jobLocation":{"address":` + tc.addr + `}}}]}`
			fetcher := newSoftgardenFetcher(t, serveJSON(body))

			got, err := fetcher.Fetch(t.Context(), "demo")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].CountryHint != tc.want {
				t.Errorf("CountryHint = %q, want %q", got[0].CountryHint, tc.want)
			}
		})
	}
}

func TestSoftgardenMalformedDatePosted(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unparseable timestamp",
			body: `{"dataFeedElement":[{"item":{"url":"https://demo.career.softgarden.de/jobs/1/x/","datePosted":"05.09.2024"}}]}`,
		},
		{
			name: "absent timestamp",
			body: `{"dataFeedElement":[{"item":{"url":"https://demo.career.softgarden.de/jobs/1/x/"}}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := newSoftgardenFetcher(t, serveJSON(tc.body))

			got, err := fetcher.Fetch(t.Context(), "demo")
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

func TestSoftgardenTolerantIdentifier(t *testing.T) {
	// identifier.value is normally a number but is a string (or absent) on some tenants.
	// A non-numeric value must degrade to a stringified SourceID and NEVER fail the
	// whole-feed decode (ADR-0035) — the same fail-soft as a bad datePosted, so one odd
	// posting can't wipe an entire board.
	body := `{"dataFeedElement":[
		{"item":{"url":"https://demo.career.softgarden.de/jobs/1/a/","identifier":{"@type":"PropertyValue","value":"REQ-ABC"}}},
		{"item":{"url":"https://demo.career.softgarden.de/jobs/2/b/","identifier":{"@type":"PropertyValue","value":42}}},
		{"item":{"url":"https://demo.career.softgarden.de/jobs/3/c/"}}
	]}`
	fetcher := newSoftgardenFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Fetch: %v (a string identifier must not fail the board)", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 (no posting dropped by an odd identifier)", len(got))
	}
	if got[0].SourceID != "REQ-ABC" {
		t.Errorf("SourceID[0] = %q, want the stringified value %q", got[0].SourceID, "REQ-ABC")
	}
	if got[1].SourceID != "42" {
		t.Errorf("SourceID[1] = %q, want the numeric value %q", got[1].SourceID, "42")
	}
	if got[2].SourceID != "" {
		t.Errorf("SourceID[2] = %q, want empty for an absent identifier", got[2].SourceID)
	}
}

func TestSoftgardenBuildsBoardURL(t *testing.T) {
	var gotPath string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dataFeedElement":[]}`))
	}
	fetcher := newSoftgardenFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "demo"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/jobs.feed.json" {
		t.Errorf("request path = %q, want %q", gotPath, "/jobs.feed.json")
	}
}

func TestSoftgardenTemplatesTenantSlug(t *testing.T) {
	// The ATS Fetch lane passes the leftmost catalog label (the tenant slug), which the
	// base URL templates into the board host. A test base carrying the {tenant}
	// placeholder in the path proves the substitution without needing a real host; that
	// the DEFAULT base's .career.softgarden.de suffix reconstructs the real board host
	// from the slug is pinned end-to-end by collection.TestSubdomainProviderHostContract.
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dataFeedElement":[]}`))
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	fetcher := ats.NewSoftgardenFetcher(ats.WithSoftgardenBaseURL(srv.URL + "/{tenant}"))

	if _, err := fetcher.Fetch(t.Context(), "demo"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/demo/jobs.feed.json" {
		t.Errorf("request path = %q, want %q (tenant slug substituted)", gotPath, "/demo/jobs.feed.json")
	}
}

func TestSoftgardenSendsNoAuthHeader(t *testing.T) {
	// The public /jobs.feed.json feed is zero-auth; the token/OAuth dev.softgarden.de
	// Jobs API is the trap. The fetcher must send no Authorization header.
	var gotAuth string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dataFeedElement":[]}`))
	}
	fetcher := newSoftgardenFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "demo"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want it unset (public zero-auth feed)", gotAuth)
	}
}

func TestSoftgardenNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newSoftgardenFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestSoftgardenTruncatedBodyIsHardError(t *testing.T) {
	// softgarden decodes the whole feed in one request, so completeness is structural
	// (ADR-0035): a body cut mid-array surfaces as a decode error, never a silent
	// partial. A single-shot provider returns a hard error, NOT ErrBoardIncomplete.
	fetcher := newSoftgardenFetcher(t, serveJSON(`{"dataFeedElement":[{"item":{"url":"https://x/a"`))

	_, err := fetcher.Fetch(t.Context(), "demo")
	if err == nil {
		t.Fatal("want a decode error for a truncated body")
	}
	if errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatal("single-shot provider must never emit ErrBoardIncomplete; a partial read is a hard error")
	}
}

func TestSoftgardenEmptyTenant(t *testing.T) {
	// An empty tenant is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewSoftgardenFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant", got)
	}
}

func TestSoftgardenCatalogRecognition(t *testing.T) {
	// Pins the ProviderSoftgarden const to what catalog.Identify emits for a
	// <tenant>.career.softgarden.de host — the invariant seed-time routing relies on.
	u, err := crawler.NewURL("https://demo.career.softgarden.de")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	id := catalog.Identify(u)
	if id.ATSProvider != ats.ProviderSoftgarden {
		t.Errorf("ATSProvider = %q, want %q", id.ATSProvider, ats.ProviderSoftgarden)
	}
	if id.CompanyKey != "softgarden:demo" {
		t.Errorf("CompanyKey = %q, want %q", id.CompanyKey, "softgarden:demo")
	}
}
