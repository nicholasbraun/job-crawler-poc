package geo_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Country-bearing strings.
		{"city and country", "Berlin, Germany", "DE"},
		{"multi-word country name", "New York, United States", "US"},
		{"city and country austria", "Vienna, Austria", "AT"},

		// Bare names, endonyms, synonyms, demonyms.
		{"english name", "Germany", "DE"},
		{"endonym", "Deutschland", "DE"},
		{"demonym", "German", "DE"},
		{"synonym usa", "USA", "US"},
		{"uk synonym resolves to gb", "United Kingdom", "GB"},

		// Curated city safety-net, including diacritic folding and alt-spellings.
		{"city diacritic fold", "München", "DE"},
		{"city english exonym", "Munich", "DE"},
		{"city ascii alt-spelling", "Muenchen", "DE"},
		{"swiss city diacritic", "Zürich", "CH"},

		// Ambiguity trap: Georgia the country vs. the US state.
		{"georgia alone is the country", "Georgia", "GE"},
		{"rightmost country wins", "Atlanta, Georgia, USA", "US"},

		// Region-only and undeterminable strings resolve to the empty Country.
		{"region with remote prefix", "Remote - EU", ""},
		{"region name", "Europe", ""},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"remote alone", "Remote", ""},

		// A US state/city outside the EU-weighted safety-net is a deliberate
		// gap: kept as unresolved rather than guessed.
		{"uncovered us locale", "New York, NY", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.Resolve(tt.in); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"assigned code", "DE", true},
		{"case insensitive", "de", true},
		{"us assigned", "US", true},
		{"gb assigned", "GB", true},
		{"trims space", " FR ", true},

		{"unassigned xx", "XX", false},
		{"unassigned zz", "ZZ", false},
		{"reserved eu", "EU", false},
		{"reserved uk not assigned", "UK", false},
		{"empty", "", false},
		{"too short", "D", false},
		{"too long", "USA", false},
		{"non-letters", "D1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.Valid(tt.code); got != tt.want {
				t.Errorf("Valid(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}
