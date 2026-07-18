package ats_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newPersonioFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a PersonioFetcher pointed at it plus the server's base URL
// (needed to assert the synthesized /job/<id> canonical URLs).
func newPersonioFetcher(t *testing.T, handler http.HandlerFunc) (*ats.PersonioFetcher, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewPersonioFetcher(ats.WithPersonioBaseURL(srv.URL)), srv.URL
}

// serveXML returns a handler that always replies with body as XML.
func serveXML(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(body))
	}
}

// personioBoard is a captured-real-shape /xml feed: the UTF-8 prolog, the
// <workzag-jobs> root wrapping two <position> elements, the full field set
// (including unmapped <subcompany>/<recruitingCategory>/enum fields the decoder
// must ignore), CDATA-wrapped HTML <jobDescription> sections, <additionalOffices>,
// and both <createdAt> forms — RFC3339-with-colon (position 101, the live 2026
// shape) and the documented no-colon +0200 (position 202).
const personioBoard = `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <id>101</id>
    <subcompany>ACME Robotics GmbH</subcompany>
    <office>Munich</office>
    <additionalOffices>
      <office>Berlin</office>
      <office>Remote</office>
    </additionalOffices>
    <department>Engineering</department>
    <recruitingCategory>Software</recruitingCategory>
    <name>Senior Backend Engineer (m/f/d)</name>
    <jobDescriptions>
      <jobDescription>
        <name>Your Tasks</name>
        <value><![CDATA[<span style="font-weight: 400;">Build &amp; ship Go services.</span>]]></value>
      </jobDescription>
      <jobDescription>
        <name>Your Profile</name>
        <value><![CDATA[<ul><li>5+ years Go</li><li>REST APIs</li></ul>]]></value>
      </jobDescription>
    </jobDescriptions>
    <employmentType>permanent</employmentType>
    <seniority>senior</seniority>
    <schedule>full-time</schedule>
    <yearsOfExperience>5-10</yearsOfExperience>
    <keywords>go,backend,api</keywords>
    <occupation>software_development</occupation>
    <occupationCategory>it_software</occupationCategory>
    <createdAt>2026-07-02T15:34:32+00:00</createdAt>
  </position>
  <position>
    <id>202</id>
    <subcompany>ACME Robotics GmbH</subcompany>
    <office>Cologne</office>
    <department>Design</department>
    <recruitingCategory>Design</recruitingCategory>
    <name>Product Designer</name>
    <jobDescriptions>
      <jobDescription>
        <name>About the Role</name>
        <value><![CDATA[<p>Design delightful things.</p>]]></value>
      </jobDescription>
    </jobDescriptions>
    <employmentType>permanent</employmentType>
    <createdAt>2016-05-31T12:14:07+0200</createdAt>
  </position>
</workzag-jobs>`

func TestPersonioFetchMapsBoard(t *testing.T) {
	fetcher, baseURL := newPersonioFetcher(t, serveXML(personioBoard))
	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Senior Backend Engineer (m/f/d)" {
		t.Errorf("Title = %q, want %q", first.Title, "Senior Backend Engineer (m/f/d)")
	}
	// Primary <office> first, then each <additionalOffices>/<office>, joined ", ".
	if first.Location != "Munich, Berlin, Remote" {
		t.Errorf("Location = %q, want %q", first.Location, "Munich, Berlin, Remote")
	}
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want %q", first.Department, "Engineering")
	}
	// The feed carries no per-posting URL; the mapper synthesizes base + /job/<id>.
	if want := baseURL + "/job/101"; first.URL != want {
		t.Errorf("URL = %q, want the synthesized %q", first.URL, want)
	}
	// Each <jobDescription> section's heading and CDATA-HTML body become a
	// plain-text line: tags stripped, &amp; decoded, list items space-joined.
	wantDesc := "Your Tasks\nBuild & ship Go services.\nYour Profile\n5+ years Go REST APIs"
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want %q", first.Description, wantDesc)
	}
	// RFC3339 with a colon offset (the live 2026 shape) parses.
	wantFirst := time.Date(2026, 7, 2, 15, 34, 32, 0, time.UTC)
	if !first.FirstPublished.Equal(wantFirst) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantFirst)
	}

	second := got[1]
	if second.Title != "Product Designer" || second.Department != "Design" || second.Location != "Cologne" {
		t.Errorf("second listing = %+v, want the Product Designer / Design / Cologne mapping", second)
	}
	if want := baseURL + "/job/202"; second.URL != want {
		t.Errorf("second.URL = %q, want %q", second.URL, want)
	}
	// The documented no-colon +0200 offset parses via the basic fallback layout;
	// 12:14:07 +0200 is 10:14:07 UTC.
	wantSecond := time.Date(2016, 5, 31, 10, 14, 7, 0, time.UTC)
	if !second.FirstPublished.Equal(wantSecond) {
		t.Errorf("second.FirstPublished = %v, want %v", second.FirstPublished, wantSecond)
	}

	// The ingest lane (#127) stamps Company/CompanyKey from the page Owner; the
	// mapper must leave both empty on every returned listing — never from <subcompany>.
	for i, l := range got {
		if l.Company != "" {
			t.Errorf("listing[%d].Company = %q, want empty (lane stamps it)", i, l.Company)
		}
		if l.CompanyKey != "" {
			t.Errorf("listing[%d].CompanyKey = %q, want empty (lane stamps it)", i, l.CompanyKey)
		}
	}
}

func TestPersonioFetchEmptyBoard(t *testing.T) {
	fetcher, _ := newPersonioFetcher(t, serveXML(`<?xml version="1.0" encoding="UTF-8"?><workzag-jobs></workzag-jobs>`))

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

func TestPersonioOptInNotFoundReturnsEmpty(t *testing.T) {
	// The XML feed is opt-in per tenant: a recognized-but-opted-out tenant 404s.
	// That is "no open roles" — an empty non-nil slice and a nil error, NOT
	// ErrBoardStatus. This is the key Personio-specific divergence.
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher, _ := newPersonioFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "optedout")
	if err != nil {
		t.Fatalf("Fetch on a 404 opt-in board: err = %v, want nil", err)
	}
	if errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err wraps ErrBoardStatus, want nil for the opt-in 404")
	}
	if got == nil {
		t.Fatal("Fetch returned a nil slice on a 404, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d listings on a 404, want 0", len(got))
	}
}

func TestPersonioNon200ReturnsErrBoardStatus(t *testing.T) {
	// Any non-200 other than the opt-in 404 is a real failure.
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	fetcher, _ := newPersonioFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestPersonioDescriptionConcatenatesSectionsAndStrips(t *testing.T) {
	// Multiple CDATA-wrapped HTML sections concatenate into one plain-text
	// Description, each stripped of its tags with entities decoded.
	body := `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <id>1</id>
    <name>Engineer</name>
    <jobDescriptions>
      <jobDescription>
        <name>Intro</name>
        <value><![CDATA[<p>Join us &amp; grow.</p>]]></value>
      </jobDescription>
      <jobDescription>
        <name>Requirements</name>
        <value><![CDATA[<ul><li>Go</li><li>SQL</li></ul>]]></value>
      </jobDescription>
      <jobDescription>
        <name>Benefits</name>
        <value><![CDATA[<div>30 days &amp; more</div>]]></value>
      </jobDescription>
    </jobDescriptions>
    <createdAt>2026-07-02T15:34:32+00:00</createdAt>
  </position>
</workzag-jobs>`
	fetcher, _ := newPersonioFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Intro\nJoin us & grow.\nRequirements\nGo SQL\nBenefits\n30 days & more"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<") {
		t.Errorf("Description = %q, still contains a raw HTML tag", got[0].Description)
	}
}

func TestPersonioDescriptionUsesSingleEncodedStrip(t *testing.T) {
	// Personio's CDATA sections unwrap to single-encoded HTML (Lever's shape), so
	// the mapper must reduce them with htmlSingleEncodedToText, never the double-
	// encoded helper. This fixture discriminates the two — the plain-text bodies in
	// the other tests reduce identically under both, so they cannot catch a regression
	// to the wrong helper. A double-encoded entity ("&amp;lt;") unescapes exactly once
	// under the single-encoded reduction, yielding the literal four-character text
	// "&lt;" (no raw '<'); the double-encoded reduction would unescape it twice into a
	// raw '<' and then treat that '<' as a tag opener. If personioDescription regressed
	// to htmlDoubleEncodedToText, both assertions below would fail.
	body := `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <id>1</id>
    <name>Engineer</name>
    <jobDescriptions>
      <jobDescription>
        <name>Stack</name>
        <value><![CDATA[<p>We escape &amp;lt; in docs.</p>]]></value>
      </jobDescription>
    </jobDescriptions>
  </position>
</workzag-jobs>`
	fetcher, _ := newPersonioFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	// Single-encoded strip: the outer &amp; is decoded once, leaving the literal
	// entity text "&lt;". The double-encoded strip would emit "We escape < in docs."
	want := "Stack\nWe escape &lt; in docs."
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q (single-encoded strip)", got[0].Description, want)
	}
	if strings.Contains(got[0].Description, "<") {
		t.Errorf("Description = %q contains a raw '<'; the double-encoded strip was used", got[0].Description)
	}
}

func TestPersonioMalformedCreatedAt(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <id>1</id>
    <name>Engineer</name>
    <createdAt>not-a-date</createdAt>
  </position>
</workzag-jobs>`
	fetcher, _ := newPersonioFetcher(t, serveXML(body))

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

func TestPersonioSkipsPositionWithoutID(t *testing.T) {
	// The synthesized /job/<id> URL is the upsert key; a position with no <id> has
	// no dedup key and is dropped rather than mapped to a keyless listing.
	body := `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <name>Keyless</name>
    <office>Berlin</office>
  </position>
  <position>
    <id>42</id>
    <name>Keyed</name>
  </position>
</workzag-jobs>`
	fetcher, baseURL := newPersonioFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the id-less position is skipped)", len(got))
	}
	if want := baseURL + "/job/42"; got[0].URL != want {
		t.Errorf("kept listing URL = %q, want the position that has an id %q", got[0].URL, want)
	}
}

func TestPersonioBuildsBoardURL(t *testing.T) {
	var gotPath, gotCompanyID string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCompanyID = r.Header.Get("X-Company-ID")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><workzag-jobs></workzag-jobs>`))
	}
	fetcher, _ := newPersonioFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/xml" {
		t.Errorf("request path = %q, want %q", gotPath, "/xml")
	}
	// X-Company-ID is a disproven ReadMe artifact — the fetcher must never send it.
	if gotCompanyID != "" {
		t.Errorf("X-Company-ID header = %q, want it never sent", gotCompanyID)
	}
}

func TestPersonioEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request
	// (and before deriving the bogus "https://.jobs.personio.de" host).
	fetcher := ats.NewPersonioFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}

func TestPersonioIgnoresProviderCompanyField(t *testing.T) {
	// <subcompany> is the employer, but the mapper reads no provider company field:
	// Company must stay empty for the lane to stamp from Owner (ADR-0022).
	body := `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
  <position>
    <id>1</id>
    <name>Engineer</name>
    <subcompany>Employer GmbH</subcompany>
  </position>
</workzag-jobs>`
	fetcher, _ := newPersonioFetcher(t, serveXML(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	if got[0].Company != "" {
		t.Errorf("Company = %q, want empty; the mapper must not read <subcompany>", got[0].Company)
	}
}
