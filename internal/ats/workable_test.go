package ats_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// newWorkableFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a WorkableFetcher pointed at it. It reuses serveJSON from
// greenhouse_test.go for the common always-reply-with-a-body handler.
func newWorkableFetcher(t *testing.T, handler http.HandlerFunc) *ats.WorkableFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewWorkableFetcher(ats.WithWorkableBaseURL(srv.URL))
}

// workableSampleBoard is a faithful trim of a captured live widget-API payload:
// the {name, description, jobs:[…]} envelope with postings nested under jobs, the
// date-only published_on/created_at timestamps, single-encoded HTML descriptions,
// and the extra fields (code, shortcode, employment_type, industry, function,
// experience, education) the decoder must ignore.
const workableSampleBoard = `{
	"name": "Acme",
	"description": "<p>We build things.</p>",
	"jobs": [
		{
			"id": 1122334,
			"shortcode": "ABC123DEF",
			"code": null,
			"title": "Senior Backend Engineer",
			"full_title": "Senior Backend Engineer",
			"country": "United States",
			"country_code": "US",
			"state": "California",
			"city": "San Francisco",
			"region": "",
			"education": "",
			"experience": "Mid-Senior level",
			"function": "Software Development",
			"industry": "",
			"department": "Engineering",
			"employment_type": "Full-time",
			"telecommuting": false,
			"workplace_type": "on-site",
			"locations": [{"country": "United States", "countryCode": "US", "region": "California", "city": "San Francisco"}],
			"published_on": "2026-02-12",
			"created_at": "2026-02-10",
			"url": "https://apply.workable.com/acme/j/ABC123DEF/",
			"application_url": "https://apply.workable.com/acme/j/ABC123DEF/apply/",
			"shortlink": "https://apply.workable.com/j/ABC123DEF",
			"description": "<p>Build <strong>Go</strong> services &amp; ship.</p>"
		},
		{
			"id": 4455667,
			"shortcode": "XYZ789GHI",
			"title": "Product Designer",
			"country": "Germany",
			"state": "",
			"city": "Berlin",
			"department": "Design",
			"employment_type": "Full-time",
			"telecommuting": true,
			"workplace_type": "remote",
			"locations": [{"country": "Germany", "city": "Berlin"}],
			"published_on": "2026-03-01",
			"created_at": "2026-02-25",
			"url": "https://apply.workable.com/acme/j/XYZ789GHI/",
			"application_url": "https://apply.workable.com/acme/j/XYZ789GHI/apply/",
			"shortlink": "https://apply.workable.com/j/XYZ789GHI",
			"description": "Design things"
		}
	]
}`

func TestWorkableFetchMapsBoard(t *testing.T) {
	fetcher := newWorkableFetcher(t, serveJSON(workableSampleBoard))
	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	first := got[0]
	if first.Title != "Senior Backend Engineer" {
		t.Errorf("Title = %q, want %q", first.Title, "Senior Backend Engineer")
	}
	if first.URL != "https://apply.workable.com/acme/j/ABC123DEF/" {
		t.Errorf("URL = %q, want the canonical url", first.URL)
	}
	if first.Location != "San Francisco, California, United States" {
		t.Errorf("Location = %q, want %q", first.Location, "San Francisco, California, United States")
	}
	if first.Department != "Engineering" {
		t.Errorf("Department = %q, want %q", first.Department, "Engineering")
	}
	if first.Remote {
		t.Errorf("Remote = true, want false for telecommuting=false workplace_type=on-site")
	}
	if first.Description != "Build Go services & ship." {
		t.Errorf("Description = %q, want %q", first.Description, "Build Go services & ship.")
	}
	wantTime := time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v", first.FirstPublished, wantTime)
	}

	second := got[1]
	if second.Title != "Product Designer" || second.Department != "Design" || second.Location != "Berlin, Germany" {
		t.Errorf("second listing = %+v, want the Product Designer / Design / Berlin, Germany mapping", second)
	}
	if !second.Remote {
		t.Errorf("second.Remote = false, want true for telecommuting=true workplace_type=remote")
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

func TestWorkableFetchEmptyBoard(t *testing.T) {
	fetcher := newWorkableFetcher(t, serveJSON(`{"name":"Acme","jobs":[]}`))

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

func TestWorkableSkipsPostingWithoutCanonicalURL(t *testing.T) {
	// A posting with none of url/shortlink/application_url has no upsert key (#127)
	// and is dropped rather than mapped to a keyless listing.
	body := `{"jobs":[
		{"title":"Keyless","city":"Berlin"},
		{"title":"Keyed","url":"https://apply.workable.com/acme/j/K/"}
	]}`
	fetcher := newWorkableFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the keyless posting is skipped)", len(got))
	}
	if got[0].URL != "https://apply.workable.com/acme/j/K/" {
		t.Errorf("kept listing URL = %q, want the posting that has a canonical url", got[0].URL)
	}
}

func TestWorkableCanonicalURLPrecedence(t *testing.T) {
	// url wins over shortlink wins over application_url; the fetcher keeps the first
	// non-empty of that ordered trio as the upsert key.
	cases := []struct {
		name    string
		fields  string
		wantURL string
	}{
		{"url only", `"url":"https://u"`, "https://u"},
		{"shortlink only", `"shortlink":"https://s"`, "https://s"},
		{"application_url only", `"application_url":"https://a"`, "https://a"},
		{"all three prefers url", `"url":"https://u","shortlink":"https://s","application_url":"https://a"`, "https://u"},
		{"shortlink over application_url", `"shortlink":"https://s","application_url":"https://a"`, "https://s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"jobs":[{"title":"X",%s}]}`, tc.fields)
			fetcher := newWorkableFetcher(t, serveJSON(body))

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].URL != tc.wantURL {
				t.Errorf("URL = %q, want %q", got[0].URL, tc.wantURL)
			}
		})
	}
}

func TestWorkableFirstPublishedDateOnly(t *testing.T) {
	t.Run("published_on parses to midnight UTC", func(t *testing.T) {
		body := `{"jobs":[{"url":"https://u","published_on":"2026-02-12"}]}`
		fetcher := newWorkableFetcher(t, serveJSON(body))

		got, err := fetcher.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		want := time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC)
		if !got[0].FirstPublished.Equal(want) {
			t.Errorf("FirstPublished = %v, want %v", got[0].FirstPublished, want)
		}
	})

	t.Run("empty published_on falls back to created_at", func(t *testing.T) {
		body := `{"jobs":[{"url":"https://u","published_on":"","created_at":"2026-01-05"}]}`
		fetcher := newWorkableFetcher(t, serveJSON(body))

		got, err := fetcher.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		want := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
		if !got[0].FirstPublished.Equal(want) {
			t.Errorf("FirstPublished = %v, want the created_at fallback %v", got[0].FirstPublished, want)
		}
	})

	t.Run("malformed published_on falls through to created_at", func(t *testing.T) {
		body := `{"jobs":[{"url":"https://u","published_on":"not-a-date","created_at":"2026-03-03"}]}`
		fetcher := newWorkableFetcher(t, serveJSON(body))

		got, err := fetcher.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		want := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
		if !got[0].FirstPublished.Equal(want) {
			t.Errorf("FirstPublished = %v, want the created_at fallback %v", got[0].FirstPublished, want)
		}
	})

	t.Run("both absent keeps zero time and the posting", func(t *testing.T) {
		body := `{"jobs":[{"url":"https://u","title":"X"}]}`
		fetcher := newWorkableFetcher(t, serveJSON(body))

		got, err := fetcher.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d listings, want 1 (a missing timestamp must not drop the posting)", len(got))
		}
		if !got[0].FirstPublished.IsZero() {
			t.Errorf("FirstPublished = %v, want zero for absent timestamps (fail-safe)", got[0].FirstPublished)
		}
	})

	t.Run("both malformed keeps zero time and the posting", func(t *testing.T) {
		body := `{"jobs":[{"url":"https://u","published_on":"2026/02/12","created_at":"garbage"}]}`
		fetcher := newWorkableFetcher(t, serveJSON(body))

		got, err := fetcher.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d listings, want 1 (a malformed timestamp must not drop the posting)", len(got))
		}
		if !got[0].FirstPublished.IsZero() {
			t.Errorf("FirstPublished = %v, want zero for malformed timestamps (fail-safe)", got[0].FirstPublished)
		}
	})
}

func TestWorkableDescriptionEncoding(t *testing.T) {
	// Workable's job description is single-encoded real HTML: text-level angle
	// brackets arrive as &lt;/&gt; and must survive the reduction. A double-encoded
	// calibration would decode &lt; to a literal "<" before stripping tags and then
	// swallow the following text as a bogus tag, silently dropping "<10 engineers."
	// here. Mirrors TestLeverDescriptionPreservesEntityEncodedAngleBrackets.
	body := `{"jobs":[{"url":"https://u","description":"<p>Team of &lt;10 engineers. <strong>Apply</strong> for R&amp;D.</p>"}]}`
	fetcher := newWorkableFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "Team of <10 engineers. Apply for R&D."
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
}

func TestWorkableRemote(t *testing.T) {
	cases := []struct {
		telecommuting bool
		workplaceType string
		wantRemote    bool
	}{
		{false, "on-site", false},
		{false, "", false},
		{false, "hybrid", false},
		{true, "on-site", true}, // telecommuting alone flips it
		{false, "remote", true}, // workplace_type alone flips it
		{false, "Remote", true}, // matched case-insensitively
		{true, "remote", true},  // both agree
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("telecommuting=%v/workplace_type=%q", tc.telecommuting, tc.workplaceType), func(t *testing.T) {
			body := fmt.Sprintf(`{"jobs":[{"url":"https://u","telecommuting":%v,"workplace_type":%q}]}`, tc.telecommuting, tc.workplaceType)
			fetcher := newWorkableFetcher(t, serveJSON(body))

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].Remote != tc.wantRemote {
				t.Errorf("Remote = %v for telecommuting=%v workplace_type=%q, want %v", got[0].Remote, tc.telecommuting, tc.workplaceType, tc.wantRemote)
			}
		})
	}
}

func TestWorkableLocationFallsBackToLocationsArray(t *testing.T) {
	// When the top-level city/state/country trio is empty, the first locations[]
	// entry supplies the location line. The array keys its state component "region"
	// (unlike the top-level posting fields, which key it "state"), so the region must
	// survive into the composed line rather than being silently dropped.
	body := `{"jobs":[{"url":"https://u","city":"","state":"","country":"","locations":[{"city":"San Francisco","region":"California","country":"United States"}]}]}`
	fetcher := newWorkableFetcher(t, serveJSON(body))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1", len(got))
	}
	want := "San Francisco, California, United States"
	if got[0].Location != want {
		t.Errorf("Location = %q, want the locations[] fallback %q", got[0].Location, want)
	}
}

func TestWorkableBuildsBoardURL(t *testing.T) {
	var gotPath, gotDetails string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDetails = r.URL.Query().Get("details")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}
	fetcher := newWorkableFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/api/accounts/acme" {
		t.Errorf("request path = %q, want %q", gotPath, "/api/accounts/acme")
	}
	if gotDetails != "true" {
		t.Errorf("details query = %q, want %q", gotDetails, "true")
	}
}

func TestWorkableSendsNoAuthHeader(t *testing.T) {
	// The public widget API needs no auth; the neighbouring spi/v3/jobs API is
	// Bearer-token gated. The fetcher must never send an Authorization header (the
	// "public read vs auth-gated neighbour" trap, research §Workable).
	var gotAuth string
	handler := func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}
	fetcher := newWorkableFetcher(t, handler)

	if _, err := fetcher.Fetch(t.Context(), "acme"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty", gotAuth)
	}
}

func TestWorkableFollowsRedirect(t *testing.T) {
	// The account endpoint 302-redirects to the canonical widget host; the default
	// http.Client follows it. Two routes on one server prove the fetcher lands on the
	// widget path and returns its jobs without any CheckRedirect override.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/api/accounts/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", srv.URL+"/api/v1/widget/accounts/acme")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/api/v1/widget/accounts/acme", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[{"title":"Redirected Role","url":"https://apply.workable.com/acme/j/R/"}]}`))
	})

	fetcher := ats.NewWorkableFetcher(ats.WithWorkableBaseURL(srv.URL))
	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Redirected Role" {
		t.Fatalf("got %+v, want the single job served after the 302 redirect", got)
	}
}

func TestWorkableNon200ReturnsErrBoardStatus(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	fetcher := newWorkableFetcher(t, handler)

	got, err := fetcher.Fetch(t.Context(), "missing")
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Fatalf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a non-200 response", got)
	}
}

func TestWorkableEmptyTenant(t *testing.T) {
	// An empty tenant slug is a caller bug the fetcher rejects before any request.
	fetcher := ats.NewWorkableFetcher()

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant slug")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant slug", got)
	}
}
