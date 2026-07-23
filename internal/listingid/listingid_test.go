package listingid_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/listingid"
)

func TestFromURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"http forced to https", "http://example.com/jobs/1", "https://example.com/jobs/1"},
		{
			"host lowercased and www stripped, path case preserved",
			"https://WWW.Example.com/Jobs/1",
			"https://example.com/Jobs/1",
		},
		{"fragment dropped", "https://example.com/jobs/1#apply", "https://example.com/jobs/1"},
		{"non-root trailing slash stripped", "https://example.com/jobs/1/", "https://example.com/jobs/1"},
		{"root path kept", "https://example.com/", "https://example.com/"},
		{"query params sorted", "https://example.com/jobs?b=2&a=1", "https://example.com/jobs?a=1&b=2"},
		{
			"known tracker stripped, unknown param kept",
			"https://example.com/jobs?utm_source=x&jobId=9",
			"https://example.com/jobs?jobId=9",
		},
		{
			"unlisted param survives",
			"https://example.com/jobs?ref=twitter",
			"https://example.com/jobs?ref=twitter",
		},
		{"unparseable input returned unchanged", "http://%zz", "http://%zz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := listingid.FromURL(tc.raw); got != tc.want {
				t.Errorf("FromURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestFromURLKeepsDistinctOnMeaningfulParam is the load-bearing keep-distinct
// boundary (ADR-0034): two posting URLs differing only by a kept (non-tracker)
// param must NOT collapse to one identity — some boards carry the posting id in
// the query, so a false-merge there silently loses a real posting.
func TestFromURLKeepsDistinctOnMeaningfulParam(t *testing.T) {
	one := listingid.FromURL("https://board.example.com/j?jobId=1")
	two := listingid.FromURL("https://board.example.com/j?jobId=2")
	if one == two {
		t.Errorf("URLs differing only by a kept param collapsed: both %q", one)
	}
}

func TestFromATS(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		tenant   string
		sourceID string
		want     string
	}{
		{"basic triple", "greenhouse", "acme", "123", "greenhouse:acme:123"},
		{"provider and tenant case-folded", "Greenhouse", "Acme", "123", "greenhouse:acme:123"},
		{"sourceID kept verbatim (case preserved)", "lever", "acme", "AbC-9", "lever:acme:AbC-9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := listingid.FromATS(tc.provider, tc.tenant, tc.sourceID); got != tc.want {
				t.Errorf("FromATS(%q,%q,%q) = %q, want %q", tc.provider, tc.tenant, tc.sourceID, got, tc.want)
			}
		})
	}
}

// TestFromATSKeepsDistinctOnSourceID asserts the re-slug-stability / keep-distinct
// boundary: two postings of the same tenant with different provider ids get
// distinct identities, never folded together (ADR-0034).
func TestFromATSKeepsDistinctOnSourceID(t *testing.T) {
	one := listingid.FromATS("greenhouse", "acme", "1")
	two := listingid.FromATS("greenhouse", "acme", "2")
	if one == two {
		t.Errorf("distinct sourceIDs collapsed: both %q", one)
	}
}
