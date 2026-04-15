package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestURLs(t *testing.T) {
	t.Run("Test URL Parse (absolute)", func(t *testing.T) {
		base, _ := crawler.NewURL("https://google.com")
		url, err := base.Parse("https://google.com/jobs")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "google.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://google.com/jobs"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})

	t.Run("Test URL Parse (relative)", func(t *testing.T) {
		base, _ := crawler.NewURL("https://google.com")
		url, err := base.Parse("/jobs")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "google.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://google.com/jobs"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})

	t.Run("Test URL Parse (seed)", func(t *testing.T) {
		url, err := crawler.NewURL("https://google.com")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "google.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://google.com"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})

	t.Run("Test URL Parse (seed with path)", func(t *testing.T) {
		url, err := crawler.NewURL("https://google.com/jobs")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "google.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://google.com/jobs"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})

	t.Run("Test URL Parse (different absolute url with subdomain)", func(t *testing.T) {
		base, _ := crawler.NewURL("https://google.com")

		url, err := base.Parse("https://jobs.google.com")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "jobs.google.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://jobs.google.com"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})

	t.Run("Test URL Parse (different absolute url)", func(t *testing.T) {
		base, _ := crawler.NewURL("https://google.com")

		url, err := base.Parse("https://netflix.com/jobs")
		if err != nil {
			t.Fatalf("error parsing url %v", err)
		}

		wantHostname := "netflix.com"
		gotHostname := url.Hostname

		assertStrings(t, wantHostname, gotHostname)

		wantRawURL := "https://netflix.com/jobs"
		gotRawURL := url.RawURL

		assertStrings(t, wantRawURL, gotRawURL)
	})
}

func TestURLNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase scheme", "HTTPS://example.com/jobs", "https://example.com/jobs"},
		{"lowercase host", "https://Example.COM/jobs", "https://example.com/jobs"},
		{"strip fragment", "https://example.com/jobs#apply", "https://example.com/jobs"},
		{"sort query params", "https://example.com/jobs?b=2&a=1", "https://example.com/jobs?a=1&b=2"},
		{"strip trailing slash", "https://example.com/jobs/", "https://example.com/jobs"},
		{"strip trailing slash deep path", "https://example.com/jobs/golang/", "https://example.com/jobs/golang"},
		{"keep root path", "https://example.com/", "https://example.com/"},
		{"combined", "HTTPS://Example.COM/Jobs/?b=2&a=1#apply", "https://example.com/Jobs?a=1&b=2"},
	}

	for _, tc := range cases {
		t.Run("NewURL: "+tc.name, func(t *testing.T) {
			got, err := crawler.NewURL(tc.in)
			if err != nil {
				t.Fatalf("error parsing url %v", err)
			}
			assertStrings(t, tc.want, got.RawURL)
		})

		t.Run("Parse: "+tc.name, func(t *testing.T) {
			base, _ := crawler.NewURL("https://seed.example/")
			got, err := base.Parse(tc.in)
			if err != nil {
				t.Fatalf("error parsing url %v", err)
			}
			assertStrings(t, tc.want, got.RawURL)
		})
	}
}
