package geo_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

// TestKnownCountries keeps the curated picklist in sync with the gazetteer: the
// set is non-empty, sorted by Name, free of duplicate codes, and every entry is a
// real ISO code (Valid) whose Name the Resolver maps back to that same code. If
// someone adds a Country here without a matching gazetteer entry (or vice versa),
// this fails rather than silently offering a code the constraint can never match.
func TestKnownCountries(t *testing.T) {
	got := geo.KnownCountries()

	if len(got) == 0 {
		t.Fatal("KnownCountries() is empty, want the curated set")
	}

	seen := map[string]bool{}
	for i, c := range got {
		if i > 0 && got[i-1].Name > c.Name {
			t.Errorf("not sorted by Name: %q precedes %q", got[i-1].Name, c.Name)
		}
		if seen[c.Code] {
			t.Errorf("duplicate code %q in known set", c.Code)
		}
		seen[c.Code] = true

		if !geo.Valid(c.Code) {
			t.Errorf("%q (%s): Code is not a valid ISO alpha-2 code", c.Code, c.Name)
		}
		if resolved := geo.Resolve(c.Name); resolved != c.Code {
			t.Errorf("Resolve(%q) = %q, want %q (name must round-trip to its code)", c.Name, resolved, c.Code)
		}
	}
}
