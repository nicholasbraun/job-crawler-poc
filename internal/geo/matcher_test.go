package geo_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

// These are the data-independent matcher invariants: properties of the matching
// algorithm asserted through the public Resolve, independent of which specific
// cities the generated gazetteer happens to contain. #174 owns the complementary
// full-gazetteer data-invariant test.

// TestResolveOutputAlwaysValidOrEmpty is the core keep-on-doubt safety property:
// Resolve returns either a real ISO code or the empty Country, never garbage. It
// holds over adversarial inputs so the Country Constraint can trust the output.
func TestResolveOutputAlwaysValidOrEmpty(t *testing.T) {
	inputs := []string{
		"", "   ", "Remote", "Remote - EU", "Europe", "EMEA", "APAC",
		"Latin America", "Berlin, Germany", "Atlanta, Georgia, USA",
		"New York, NY", "Austin, TX", "Wilmington, DE", "join us",
		"US", "U.S.", "USA", "Georgian", "berlinX", "Xberlin", "campus",
		"???", "123", "München", "Zürich", "San Francisco, CA",
	}
	for _, in := range inputs {
		out := geo.Resolve(in)
		if !geo.Valid(out) && out != "" {
			t.Errorf("Resolve(%q) = %q, which is neither Valid nor empty", in, out)
		}
	}
}

// TestResolveWordBoundary asserts a gazetteer token only matches as a whole word,
// never nested inside a longer word — the tokenizer's job.
func TestResolveWordBoundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"georgia nested in georgian", "Georgian"},
		{"suffix noise on berlin", "berlinX"},
		{"prefix noise on berlin", "Xberlin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.Resolve(tt.in); got != "" {
				t.Errorf("Resolve(%q) = %q, want %q (token must not match inside a word)", tt.in, got, "")
			}
		})
	}
}

// TestMatchUSToken pins the case- and boundary-sensitivity of the bare-US pass
// through Resolve: uppercase standalone US tokens resolve to US, while the
// lowercase pronoun and US substrings do not.
func TestMatchUSToken(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare us", "US", "US"},
		{"dotted us", "U.S.", "US"},
		{"usa", "USA", "US"},
		{"remote us", "Remote, US", "US"},
		{"city then us", "Austin, US", "US"},

		{"lowercase pronoun", "join us", ""},
		{"campus substring", "campus", ""},
		{"amadeus substring", "Amadeus", ""},
		{"status substring", "status", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.Resolve(tt.in); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExcludedCodesNeverMisResolve guards the two-letter code collisions the
// generator excludes (ADR-0031): a bare code that also names a foreign ISO
// country or a stoplisted US state must resolve to nothing — never to that
// foreign country and never to US.
func TestExcludedCodesNeverMisResolve(t *testing.T) {
	for _, code := range []string{"DE", "CA", "IN", "GA"} {
		if got := geo.Resolve(code); got != "" {
			t.Errorf("Resolve(%q) = %q, want %q (bare colliding code must not resolve)", code, got, "")
		}
	}
	// But the city name behind the collision still resolves: Wilmington, DE is the
	// US city (the barred "de" state code cannot steer it to Germany).
	if got := geo.Resolve("Wilmington, DE"); got != "US" {
		t.Errorf("Resolve(%q) = %q, want %q", "Wilmington, DE", got, "US")
	}
}

// TestKeepOnDoubtRegions asserts that region and aggregate words resolve to the
// empty Country, so a region-only location is kept rather than mis-assigned.
func TestKeepOnDoubtRegions(t *testing.T) {
	for _, in := range []string{"Europe", "EMEA", "Latin America", "Remote", "Remote - EU"} {
		if got := geo.Resolve(in); got != "" {
			t.Errorf("Resolve(%q) = %q, want %q (region word must not resolve)", in, got, "")
		}
	}
}
