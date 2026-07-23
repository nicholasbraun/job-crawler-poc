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

// newAshbyFetcher stands up an httptest server with handler, registers its
// cleanup, and returns an AshbyFetcher pointed at it.
func newAshbyFetcher(t *testing.T, handler http.HandlerFunc) *ats.AshbyFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewAshbyFetcher(ats.WithAshbyBaseURL(srv.URL))
}

// ashbyBoardSample is a faithful, trimmed capture of the public Ashby job-board
// API response for the Linear tenant (api.ashbyhq.com/posting-api/job-board/linear,
// 2026-07-18). It preserves the exact wire shape: the {"jobs":[…]} envelope, the
// per-job key set (including the nested address object and both descriptionHtml
// and descriptionPlain), and Ashby's RFC3339-with-fractional-seconds publishedAt
// format ("…T20:13:45.158+00:00"). Only descriptionPlain/descriptionHtml bodies
// are shortened; every mapped field carries the real captured value.
const ashbyBoardSample = `{
	"jobs": [
		{
			"id": "d3bc1ced-3ce4-4086-a050-555055dbb1ff",
			"title": "Senior / Staff Fullstack Engineer",
			"department": "Product",
			"team": "Engineering",
			"location": "Europe",
			"address": {"postalAddress": {"addressCountry": "European Union"}},
			"isRemote": true,
			"workplaceType": "Remote",
			"employmentType": "FullTime",
			"publishedAt": "2021-04-27T20:13:45.158+00:00",
			"jobUrl": "https://jobs.ashbyhq.com/linear/d3bc1ced-3ce4-4086-a050-555055dbb1ff",
			"applyUrl": "https://jobs.ashbyhq.com/linear/d3bc1ced-3ce4-4086-a050-555055dbb1ff/application",
			"descriptionHtml": "<p style=\"min-height:1.5em\">At Linear, we're building the product development system for teams and agents.</p>",
			"descriptionPlain": "At Linear, we're building the product development system for teams and agents.\n\nFounded in 2019, Linear has become the platform of choice for modern software teams."
		},
		{
			"id": "cd5ae036-0223-427a-b038-ba16ef9dcb32",
			"title": "Senior / Staff Fullstack Engineer",
			"department": "Product",
			"team": "Engineering",
			"location": "North America",
			"address": {"postalAddress": {"addressCountry": "United States"}},
			"isRemote": true,
			"workplaceType": "Remote",
			"employmentType": "FullTime",
			"publishedAt": "2021-08-18T20:48:26.891+00:00",
			"jobUrl": "https://jobs.ashbyhq.com/linear/cd5ae036-0223-427a-b038-ba16ef9dcb32",
			"applyUrl": "https://jobs.ashbyhq.com/linear/cd5ae036-0223-427a-b038-ba16ef9dcb32/application",
			"descriptionHtml": "<p>We're hiring across the Americas.</p>",
			"descriptionPlain": "We're hiring across the Americas."
		},
		{
			"id": "069c4628-88d7-4e4d-b393-c996fc7f3076",
			"title": "Senior / Staff Product Engineer",
			"department": "Product",
			"team": "Engineering",
			"location": "Europe",
			"address": {"postalAddress": {"addressCountry": "European Union"}},
			"isRemote": true,
			"workplaceType": "Remote",
			"employmentType": "FullTime",
			"publishedAt": "2022-01-22T08:49:37.626+00:00",
			"jobUrl": "https://jobs.ashbyhq.com/linear/069c4628-88d7-4e4d-b393-c996fc7f3076",
			"applyUrl": "https://jobs.ashbyhq.com/linear/069c4628-88d7-4e4d-b393-c996fc7f3076/application",
			"descriptionHtml": "<p>Build the product.</p>",
			"descriptionPlain": "Build the product."
		}
	]
}`

func TestAshbyFetchMapsBoard(t *testing.T) {
	fetcher := newAshbyFetcher(t, serveJSON(ashbyBoardSample))

	got, err := fetcher.Fetch(t.Context(), "linear")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3", len(got))
	}

	// descriptionPlain maps verbatim — its paragraph newlines must survive, proving
	// no HTML-strip or whitespace-collapse pass ran on the Ashby body.
	const wantDesc = "At Linear, we're building the product development system for teams and agents.\n\nFounded in 2019, Linear has become the platform of choice for modern software teams."

	first := got[0]
	if first.Title != "Senior / Staff Fullstack Engineer" {
		t.Errorf("Title = %q, want %q", first.Title, "Senior / Staff Fullstack Engineer")
	}
	if first.URL != "https://jobs.ashbyhq.com/linear/d3bc1ced-3ce4-4086-a050-555055dbb1ff" {
		t.Errorf("URL = %q, want the jobUrl", first.URL)
	}
	if first.SourceID != "d3bc1ced-3ce4-4086-a050-555055dbb1ff" {
		t.Errorf("SourceID = %q, want the posting id", first.SourceID)
	}
	if first.Location != "Europe" {
		t.Errorf("Location = %q, want %q (the location string, not the address object)", first.Location, "Europe")
	}
	// CountryHint surfaces the structured address.postalAddress.addressCountry for the
	// ingest lane to resolve at save (ADR-0029), independent of the Location string.
	// "European Union" is a region the resolver will keep as the empty Country.
	if first.CountryHint != "European Union" {
		t.Errorf("CountryHint = %q, want %q (addressCountry)", first.CountryHint, "European Union")
	}
	if first.Department != "Product" {
		t.Errorf("Department = %q, want %q", first.Department, "Product")
	}
	if first.WorkArrangement != crawler.WorkArrangementRemote {
		t.Errorf("WorkArrangement = %q, want remote for isRemote:true/workplaceType:Remote", first.WorkArrangement)
	}
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want the verbatim descriptionPlain %q", first.Description, wantDesc)
	}
	// publishedAt carries fractional seconds; FirstPublished must retain them.
	wantTime := time.Date(2021, 4, 27, 20, 13, 45, 158000000, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	// A second posting distinguishes the location mapping (North America, not Europe).
	if got[1].Location != "North America" {
		t.Errorf("got[1].Location = %q, want %q", got[1].Location, "North America")
	}
	if got[1].URL != "https://jobs.ashbyhq.com/linear/cd5ae036-0223-427a-b038-ba16ef9dcb32" {
		t.Errorf("got[1].URL = %q, want its jobUrl", got[1].URL)
	}
	// A third posting distinguishes the title mapping.
	if got[2].Title != "Senior / Staff Product Engineer" {
		t.Errorf("got[2].Title = %q, want %q", got[2].Title, "Senior / Staff Product Engineer")
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

func TestAshbyDescriptionIsPlainNotStripped(t *testing.T) {
	// descriptionPlain is stored byte-for-byte. This body carries characters an
	// HTML strip would mangle: a bare "<" the tag regex would swallow the trailing
	// text with, a literal "&" a decode pass would touch, and a newline a collapse
	// would flatten. Equality proves the mapper runs no such pass on Ashby.
	const plain = "Team of <10 engineers.\nApply now & win."
	body := `{"jobs":[{"jobUrl":"https://jobs.ashbyhq.com/acme/1","descriptionPlain":"Team of <10 engineers.\nApply now & win."}]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Description != plain {
		t.Errorf("Description = %q, want the verbatim plain text %q (no strip/collapse)", got[0].Description, plain)
	}
}

func TestAshbyFetchEmptyBoard(t *testing.T) {
	fetcher := newAshbyFetcher(t, serveJSON(`{"jobs":[]}`))

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

func TestAshbyLocationFallsBackToAddressCountry(t *testing.T) {
	// When the top-level location string is absent, the mapper falls back to the
	// structured address.postalAddress.addressCountry (docs §Ashby field map:
	// location/address→Location).
	body := `{"jobs":[{"jobUrl":"https://jobs.ashbyhq.com/acme/1","address":{"postalAddress":{"addressCountry":"United States"}}}]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Location != "United States" {
		t.Errorf("Location = %q, want %q (address.postalAddress.addressCountry fallback)", got[0].Location, "United States")
	}
	// The same addressCountry is surfaced as the country hint (ADR-0029).
	if got[0].CountryHint != "United States" {
		t.Errorf("CountryHint = %q, want %q (addressCountry)", got[0].CountryHint, "United States")
	}
}

func TestAshbyWorkArrangement(t *testing.T) {
	// workplaceType is Ashby's positive signal and maps straight onto the enum:
	// "Remote"/"Onsite"/"Hybrid" pass through, so an explicit Onsite reads onsite (not
	// discarded). isRemote only fills in when workplaceType is absent; when both are
	// absent the arrangement degrades to unspecified, never onsite (ADR-0030).
	cases := []struct {
		name string
		job  string
		want crawler.WorkArrangement
	}{
		{"workplaceType Remote", `{"jobUrl":"https://jobs.ashbyhq.com/acme/1","workplaceType":"Remote"}`, crawler.WorkArrangementRemote},
		{"workplaceType Onsite", `{"jobUrl":"https://jobs.ashbyhq.com/acme/1","workplaceType":"Onsite"}`, crawler.WorkArrangementOnsite},
		{"workplaceType Hybrid", `{"jobUrl":"https://jobs.ashbyhq.com/acme/1","workplaceType":"Hybrid"}`, crawler.WorkArrangementHybrid},
		{"isRemote fills in when workplaceType absent", `{"jobUrl":"https://jobs.ashbyhq.com/acme/1","isRemote":true}`, crawler.WorkArrangementRemote},
		{"neither signal is unspecified", `{"jobUrl":"https://jobs.ashbyhq.com/acme/1"}`, crawler.WorkArrangementUnspecified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := newAshbyFetcher(t, serveJSON(`{"jobs":[`+tc.job+`]}`))

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

func TestAshbySkipsPostingWithoutJobURL(t *testing.T) {
	// jobUrl is the upsert key (#127); a posting missing it cannot be saved and is
	// dropped rather than mapped to a keyless listing (mirrors Greenhouse/Lever).
	body := `{"jobs":[
		{"title":"Keyless"},
		{"title":"Keyed","jobUrl":"https://jobs.ashbyhq.com/acme/1"}
	]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless posting is skipped)", len(got))
	}
	if got[0].URL != "https://jobs.ashbyhq.com/acme/1" {
		t.Errorf("kept listing URL = %q, want the posting that has a jobUrl", got[0].URL)
	}
}

func TestAshbyBuildsBoardURL(t *testing.T) {
	var gotPath, gotCompensation, gotAuth string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCompensation = r.URL.Query().Get("includeCompensation")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}
	fetcher := newAshbyFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/posting-api/job-board/acme" {
		t.Errorf("request path = %q, want %q", gotPath, "/posting-api/job-board/acme")
	}
	// The mapper reads only descriptionPlain, so the fetcher must not request
	// compensation blocks it would only discard (they count against the size cap).
	if gotCompensation != "" {
		t.Errorf("includeCompensation query = %q, want it unset", gotCompensation)
	}
	// The gotcha guard: the fetcher uses the public GET job-board endpoint, never
	// the HTTP-Basic-auth jobPosting.list endpoint, so it must send no credential.
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (public endpoint, no auth)", gotAuth)
	}
}

func TestAshbyNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newAshbyFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestAshbyMalformedPublishedAt(t *testing.T) {
	body := `{"jobs":[{"title":"X","jobUrl":"https://jobs.ashbyhq.com/acme/1","publishedAt":"not-a-date"}]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

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

func TestAshbyAbsentPublishedAt(t *testing.T) {
	body := `{"jobs":[{"title":"X","jobUrl":"https://jobs.ashbyhq.com/acme/1"}]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if !got[0].FirstPublished.IsZero() {
		t.Errorf("FirstPublished = %v, want zero for an absent timestamp", got[0].FirstPublished)
	}
}

func TestAshbyIgnoresProviderCompanyField(t *testing.T) {
	// A stray company/organization blob in the board JSON must never populate
	// Company: the mapper reads no provider company field (mirrors Greenhouse/Lever).
	body := `{"jobs":[{"title":"X","jobUrl":"https://jobs.ashbyhq.com/acme/1","company":"SomeCorp","organization":{"name":"SomeCorp"}}]}`
	fetcher := newAshbyFetcher(t, serveJSON(body))

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

func TestAshbyEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewAshbyFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}
