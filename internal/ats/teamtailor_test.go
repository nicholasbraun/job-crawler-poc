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

// newTeamtailorFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a TeamtailorFetcher pointed at it. It reuses serveXML from
// personio_test.go for the common always-reply-with-an-XML-body handler (the fetcher
// does not check Content-Type, so an XML handler serves RSS fine).
func newTeamtailorFetcher(t *testing.T, handler http.HandlerFunc) *ats.TeamtailorFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewTeamtailorFetcher(ats.WithTeamtailorBaseURL(srv.URL))
}

// ttFeed wraps item XML in the /jobs.rss <rss><channel> envelope with the real
// xmlns:tt namespace declaration, so table-case bodies stay compact.
func ttFeed(items string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:tt="https://teamtailor.com/locations"><channel><title>Test</title>` + items + `</channel></rss>`
}

// teamtailorSample is a live-captured /jobs.rss payload (footasylum.teamtailor.com,
// 2026-07-23, trimmed from 65 real <item>s to 2). It preserves the exact wire shape:
// the xmlns:tt="https://teamtailor.com/locations" declaration on <rss>, entity-encoded
// HTML <description>, RFC-822 <pubDate> with a numeric zone, the /jobs/<id>-<slug>
// <link>, a UUID <guid> (not a numeric posting id, so SourceID comes from the link),
// <remoteStatus>, the tt:locations/tt:location block, and tt:department/tt:role. The
// long real descriptions are shortened; every other value is verbatim.
const teamtailorSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:tt="https://teamtailor.com/locations">
  <channel>
    <title>Footasylum</title>
    <description>Current Opportunities</description>
    <link>https://footasylum.teamtailor.com/jobs</link>
    <item>
      <title>Sales Assistant</title>
      <description>&lt;h3&gt;&lt;strong&gt;Sales Assistant (Full-Time)&lt;/strong&gt;&lt;/h3&gt;&lt;p&gt;Join the FA team and &lt;em&gt;defy ordinary.&lt;/em&gt;&lt;/p&gt;</description>
      <pubDate>Thu, 23 Jul 2026 14:25:24 +0100</pubDate>
      <link>https://footasylum.teamtailor.com/jobs/8115390-sales-assistant</link>
      <remoteStatus>none</remoteStatus>
      <guid>cbfca3ed-9ed9-4264-9b93-243b208a4441</guid>
      <tt:locations>
        <tt:location>
          <tt:name>Aberdeen Store</tt:name>
          <tt:address>Union Square</tt:address>
          <tt:zip>AB11 5RG</tt:zip>
          <tt:city>Aberdeen</tt:city>
          <tt:country>United Kingdom</tt:country>
        </tt:location>
      </tt:locations>
      <tt:department>Retail</tt:department>
      <tt:role>Full-Time Sales Assistant</tt:role>
    </item>
    <item>
      <title>Senior Sourcing and Costing Assistant</title>
      <description>&lt;p&gt;Lead sourcing &amp;amp; costing.&lt;/p&gt;</description>
      <pubDate>Thu, 23 Jul 2026 11:20:15 +0100</pubDate>
      <link>https://footasylum.teamtailor.com/jobs/8113896-senior-sourcing-and-costing-assistant</link>
      <remoteStatus>none</remoteStatus>
      <guid>2a7f1d0c-1234-4a5b-9c8d-abcdef012345</guid>
      <tt:locations>
        <tt:location>
          <tt:name>Head Office</tt:name>
          <tt:address>Peel House</tt:address>
          <tt:zip>OL16 1XX</tt:zip>
          <tt:city>Rochdale</tt:city>
          <tt:country>United Kingdom</tt:country>
        </tt:location>
      </tt:locations>
      <tt:department>TRAPSTAR</tt:department>
      <tt:role>Senior Sourcing and Costing Assistant</tt:role>
    </item>
  </channel>
</rss>`

func TestTeamtailorFetchMapsBoard(t *testing.T) {
	fetcher := newTeamtailorFetcher(t, serveXML(teamtailorSample))
	got, err := fetcher.Fetch(t.Context(), "footasylum")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Sales Assistant" {
		t.Errorf("Title = %q, want the item title", first.Title)
	}
	if first.URL != "https://footasylum.teamtailor.com/jobs/8115390-sales-assistant" {
		t.Errorf("URL = %q, want the item <link>", first.URL)
	}
	// SourceID is the numeric id parsed from the /jobs/<id>-<slug> link, not the UUID guid.
	if first.SourceID != "8115390" {
		t.Errorf("SourceID = %q, want the numeric posting id %q", first.SourceID, "8115390")
	}
	if first.Location != "Aberdeen Store" {
		t.Errorf("Location = %q, want the tt:location name", first.Location)
	}
	// CountryHint surfaces the first tt:location's country NAME for the ingest lane
	// to resolve at save (ADR-0029).
	if first.CountryHint != "United Kingdom" {
		t.Errorf("CountryHint = %q, want %q (tt:location country name)", first.CountryHint, "United Kingdom")
	}
	// Department proves the RSS-only tt:department field is captured (the reason RSS is
	// chosen over the JSON Feed).
	if first.Department != "Retail" {
		t.Errorf("Department = %q, want %q", first.Department, "Retail")
	}
	wantDesc := "Sales Assistant (Full-Time) Join the FA team and defy ordinary."
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want %q", first.Description, wantDesc)
	}
	// pubDate "Thu, 23 Jul 2026 14:25:24 +0100" == 13:25:24 UTC.
	wantTime := time.Date(2026, 7, 23, 13, 25, 24, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	second := got[1]
	if second.Title != "Senior Sourcing and Costing Assistant" || second.Department != "TRAPSTAR" || second.Location != "Head Office" {
		t.Errorf("second listing = %+v, want the Senior Sourcing / TRAPSTAR / Head Office mapping", second)
	}
	// The single-encoded &amp;amp; decodes once to "&".
	if second.Description != "Lead sourcing & costing." {
		t.Errorf("second.Description = %q, want %q", second.Description, "Lead sourcing & costing.")
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the mapper
	// must leave both empty on every returned listing. The feed's remoteStatus is not
	// mapped, so WorkArrangement is unspecified — never onsite (ADR-0030).
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

func TestTeamtailorEmptyBoard(t *testing.T) {
	fetcher := newTeamtailorFetcher(t, serveXML(ttFeed("")))

	got, err := fetcher.Fetch(t.Context(), "footasylum")
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

func TestTeamtailorSkipsItemWithoutLink(t *testing.T) {
	// <link> is the upsert key (#127); an item missing it cannot be saved and is
	// dropped rather than mapped to a keyless listing.
	body := ttFeed(`
		<item><title>Keyless</title></item>
		<item><title>Keyed</title><link>https://footasylum.teamtailor.com/jobs/42-keyed</link></item>`)
	fetcher := newTeamtailorFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "footasylum")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the linkless item is skipped)", len(got))
	}
	if got[0].URL != "https://footasylum.teamtailor.com/jobs/42-keyed" {
		t.Errorf("kept listing URL = %q, want the item that has a <link>", got[0].URL)
	}
}

func TestTeamtailorDescriptionStrips(t *testing.T) {
	// The <description> is entity-encoded HTML, which XML-decodes to single-encoded real
	// HTML; the mapper reduces it with htmlSingleEncodedToText. An entity-encoded angle
	// bracket in the body text (&amp;lt;10, i.e. a literal "&lt;" after XML decode) must
	// survive as a literal "<" — proving the single-encode helper (mirrors the Recruitee
	// angle-bracket test) rather than a double-encode strip that would swallow the run of
	// text after it.
	body := ttFeed(`
		<item>
			<title>Engineer</title>
			<link>https://footasylum.teamtailor.com/jobs/1-engineer</link>
			<description>&lt;p&gt;Team of &amp;lt;10 engineers.&lt;/p&gt;&lt;ul&gt;&lt;li&gt;Go&lt;/li&gt;&lt;/ul&gt;</description>
		</item>`)
	fetcher := newTeamtailorFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "footasylum")
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
	if !strings.Contains(got[0].Description, "<10") {
		t.Errorf("Description = %q, the entity-encoded angle bracket did not survive as a literal '<'", got[0].Description)
	}
}

func TestTeamtailorSourceIDFromLink(t *testing.T) {
	cases := []struct {
		name   string
		link   string
		wantID string
	}{
		{
			name:   "numeric id parsed from /jobs/<id>-<slug>",
			link:   "https://footasylum.teamtailor.com/jobs/8115390-sales-assistant",
			wantID: "8115390",
		},
		{
			name:   "numeric id with no slug suffix",
			link:   "https://acme.teamtailor.com/jobs/12345",
			wantID: "12345",
		},
		{
			name:   "bare non-numeric slug yields no id (posting still kept)",
			link:   "https://acme.teamtailor.com/jobs/senior-engineer",
			wantID: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ttFeed(`<item><title>X</title><link>` + tc.link + `</link></item>`)
			fetcher := newTeamtailorFetcher(t, serveXML(body))

			got, err := fetcher.Fetch(t.Context(), "footasylum")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			// The posting is always returned — the URL is the upsert key even when SourceID
			// is empty.
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1 (a missing SourceID must not drop the posting)", len(got))
			}
			if got[0].SourceID != tc.wantID {
				t.Errorf("SourceID = %q, want %q", got[0].SourceID, tc.wantID)
			}
			if got[0].URL != tc.link {
				t.Errorf("URL = %q, want %q", got[0].URL, tc.link)
			}
		})
	}
}

func TestTeamtailorLocation(t *testing.T) {
	cases := []struct {
		name string
		loc  string
		want string
	}{
		{
			name: "prefers tt:name",
			loc:  `<tt:locations><tt:location><tt:name>Aberdeen Store</tt:name><tt:city>Aberdeen</tt:city><tt:country>United Kingdom</tt:country></tt:location></tt:locations>`,
			want: "Aberdeen Store",
		},
		{
			name: "falls back to city, country when name empty",
			loc:  `<tt:locations><tt:location><tt:name></tt:name><tt:city>Berlin</tt:city><tt:country>Germany</tt:country></tt:location></tt:locations>`,
			want: "Berlin, Germany",
		},
		{
			name: "empty when no tt:location",
			loc:  ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ttFeed(`<item><title>X</title><link>https://acme.teamtailor.com/jobs/1-x</link>` + tc.loc + `</item>`)
			fetcher := newTeamtailorFetcher(t, serveXML(body))

			got, err := fetcher.Fetch(t.Context(), "acme")
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

func TestTeamtailorCountryHint(t *testing.T) {
	cases := []struct {
		name string
		loc  string
		want string
	}{
		{
			name: "first location's country name",
			loc:  `<tt:locations><tt:location><tt:city>Aberdeen</tt:city><tt:country>United Kingdom</tt:country></tt:location></tt:locations>`,
			want: "United Kingdom",
		},
		{
			name: "empty when no structured location",
			loc:  ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ttFeed(`<item><title>X</title><link>https://acme.teamtailor.com/jobs/1-x</link>` + tc.loc + `</item>`)
			fetcher := newTeamtailorFetcher(t, serveXML(body))

			got, err := fetcher.Fetch(t.Context(), "acme")
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

func TestTeamtailorMalformedPubDate(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unparseable timestamp",
			body: ttFeed(`<item><title>X</title><link>https://acme.teamtailor.com/jobs/1-x</link><pubDate>not-a-date</pubDate></item>`),
		},
		{
			name: "absent timestamp",
			body: ttFeed(`<item><title>X</title><link>https://acme.teamtailor.com/jobs/1-x</link></item>`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := newTeamtailorFetcher(t, serveXML(tc.body))

			got, err := fetcher.Fetch(t.Context(), "acme")
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

func TestTeamtailorBuildsBoardURL(t *testing.T) {
	var gotPath string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(ttFeed("")))
	}
	fetcher := newTeamtailorFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "footasylum"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/jobs.rss" {
		t.Errorf("request path = %q, want %q", gotPath, "/jobs.rss")
	}
}

func TestTeamtailorFullHostTemplating(t *testing.T) {
	// The fetcher templates the FULL host label-prefix verbatim, so a regional host
	// like thestudio.na.teamtailor.com (prefix "thestudio.na") reconstructs correctly.
	// A leftmost-label slugger would build "na.teamtailor.com" — the wrong board. A test
	// base carrying the placeholder in the path proves the full multi-label prefix is
	// substituted without needing real per-tenant subdomains.
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(ttFeed("")))
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	fetcher := ats.NewTeamtailorFetcher(ats.WithTeamtailorBaseURL(srv.URL + "/{tenant}"))

	if _, err := fetcher.Fetch(t.Context(), "thestudio.na"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/thestudio.na/jobs.rss" {
		t.Errorf("request path = %q, want %q (full multi-label prefix substituted)", gotPath, "/thestudio.na/jobs.rss")
	}
}

func TestTeamtailorNoAuthHeader(t *testing.T) {
	// api.teamtailor.com is the key-gated REST API; the public tenant /jobs.rss feed
	// needs no auth. The fetcher must never send Authorization or X-Api-Version.
	var gotAuth, gotAPIVersion string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIVersion = r.Header.Get("X-Api-Version")
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(ttFeed("")))
	}
	fetcher := newTeamtailorFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "footasylum"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want it never sent", gotAuth)
	}
	if gotAPIVersion != "" {
		t.Errorf("X-Api-Version header = %q, want it never sent", gotAPIVersion)
	}
}

func TestTeamtailorNon200ReturnsErrBoardStatus(t *testing.T) {
	// Teamtailor's /jobs.rss is not opt-in, so a 404 is a genuinely missing/wrong board
	// and surfaces as ErrBoardStatus (unlike Personio's opt-in 404-is-empty).
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newTeamtailorFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestTeamtailorTruncatedBodyIsHardError(t *testing.T) {
	// Teamtailor decodes the whole feed in one request, so completeness is structural
	// (ADR-0035): a body cut mid-<item> surfaces as a decode error, never a silent
	// partial. A single-shot provider returns a hard error, NOT ErrBoardIncomplete.
	fetcher := newTeamtailorFetcher(t, serveXML(`<rss version="2.0"><channel><item><title>x</title>`))

	_, err := fetcher.Fetch(t.Context(), "footasylum")
	if err == nil {
		t.Fatal("want a decode error for a truncated body")
	}
	if errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatal("single-shot provider must never emit ErrBoardIncomplete; a partial read is a hard error")
	}
}

func TestTeamtailorEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request (and
	// before templating the bogus "https://.teamtailor.com" host).
	fetcher := ats.NewTeamtailorFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestTeamtailorCatalogRecognition(t *testing.T) {
	// Pins the ProviderTeamtailor const to what catalog.Identify emits for a
	// <tenant>.teamtailor.com host — the invariant seed-time routing relies on — and
	// documents the accept-imperfect regional-host slug (out of ticket scope to fix).
	cases := []struct {
		name           string
		rawURL         string
		wantProvider   string
		wantCompanyKey string
	}{
		{
			name:           "common single-label host",
			rawURL:         "https://acme.teamtailor.com",
			wantProvider:   ats.ProviderTeamtailor,
			wantCompanyKey: "teamtailor:acme",
		},
		{
			// A numeric-suffix tenant is still a single host label, so it slugs correctly.
			name:           "numeric-suffix host",
			rawURL:         "https://asklocala-1671530238.teamtailor.com",
			wantProvider:   ats.ProviderTeamtailor,
			wantCompanyKey: "teamtailor:asklocala-1671530238",
		},
		{
			// Accept-imperfect: catalog.subdomainLabel emits the leftmost label ("na") for
			// the rare regional multi-label prefix, so the CompanyKey is imperfect — but the
			// provider still resolves to this fetcher, and the fetcher handles the full host
			// when handed the full prefix (TestTeamtailorFullHostTemplating). Fixing the
			// catalog slug is deliberately out of scope (it would change every subdomain-rule
			// provider's CompanyKey — huge blast radius).
			name:           "regional multi-label host (accept-imperfect slug)",
			rawURL:         "https://thestudio.na.teamtailor.com",
			wantProvider:   ats.ProviderTeamtailor,
			wantCompanyKey: "teamtailor:na",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := crawler.NewURL(tc.rawURL)
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			id := catalog.Identify(u)
			if id.ATSProvider != tc.wantProvider {
				t.Errorf("ATSProvider = %q, want %q", id.ATSProvider, tc.wantProvider)
			}
			if id.CompanyKey != tc.wantCompanyKey {
				t.Errorf("CompanyKey = %q, want %q", id.CompanyKey, tc.wantCompanyKey)
			}
		})
	}

	// Pin the embed-detection path (ATSProviderForHost) too: a bare Teamtailor host
	// resolves to the same provider family the Registry routes on.
	if provider, ok := catalog.ATSProviderForHost("acme.teamtailor.com"); !ok || provider != ats.ProviderTeamtailor {
		t.Errorf("ATSProviderForHost(acme.teamtailor.com) = (%q, %v), want (%q, true)", provider, ok, ats.ProviderTeamtailor)
	}
}
