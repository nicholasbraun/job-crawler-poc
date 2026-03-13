package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestURLs(t *testing.T) {
	t.Run("Test URL Parse (absolute)", func(t *testing.T) {
		url, err := crawler.ParseURL("https://google.com", "https://google.com/jobs")
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
		url, err := crawler.ParseURL("https://google.com", "/jobs")
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
		url, err := crawler.ParseURL("https://google.com", "")
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
		url, err := crawler.ParseURL("https://google.com/jobs", "")
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

	t.Run("Test URL Parse (different absolute url)", func(t *testing.T) {
		url, err := crawler.ParseURL("https://google.com/jobs", "https://netflix.com/jobs")
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

	t.Run("Test URL Parse (invalid, no base)", func(t *testing.T) {
		url, err := crawler.ParseURL("", "/jobs")
		if err == nil {
			t.Fatalf("expected parsing url without base to fail, got: %+v", url)
		}
	})

	t.Run("Test URL Parse (invalid, fragment)", func(t *testing.T) {
		url, err := crawler.ParseURL("https://google.com/jobs", "mailto@google.com")
		if err == nil {
			t.Fatalf("expected parsing url fragmet to fail, got: %+v", url)
		}
	})
}
