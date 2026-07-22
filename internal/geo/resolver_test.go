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

		// Bare names, endonyms, synonyms.
		{"english name", "Germany", "DE"},
		{"endonym", "Deutschland", "DE"},
		{"synonym usa", "USA", "US"},
		{"uk synonym resolves to gb", "United Kingdom", "GB"},

		// City layer via English exonyms (the gazetteer stores the ASCII endonym,
		// so the exonym is the reliable spelling). Umlaut endonyms like "München"
		// and "Zürich" are an accepted keep-on-doubt gap: fold maps ü->u but the
		// data holds "muenchen"/"zuerich", so the endonym resolves to "" (kept),
		// while the English exonym resolves.
		{"city english exonym munich", "Munich", "DE"},
		{"city english exonym cologne", "Cologne", "DE"},

		// Ambiguity trap: Georgia the country vs. the US state.
		{"georgia alone is the country", "Georgia", "GE"},
		{"rightmost country wins", "Atlanta, Georgia, USA", "US"},

		// Overlapping-token disambiguation: a multi-word name beats the bare
		// token nested inside it (rightmost-ending match wins, not rightmost-start).
		{"north korea beats bare korea", "North Korea", "KP"},
		{"south korea", "Seoul, South Korea", "KR"},

		// The bare-US pass is case- and boundary-aware: a lowercase "us" pronoun
		// never resolves and never overrides a named or city-derived country, but
		// an uppercase standalone "US" is a country signal (ADR-0028/0029).
		{"stray us pronoun keeps city country", "Munich — join us!", "DE"},
		{"stray us pronoun keeps named country", "Vienna, Austria — join us", "AT"},
		{"lowercase us pronoun alone", "join us", ""},
		{"bare us token", "Remote, US", "US"},

		// US states and cities now resolve through the generated gazetteer.
		{"us state code", "Austin, TX", "US"},
		{"us city and state code", "San Francisco, CA", "US"},
		{"us city outside eu net", "New York, NY", "US"},
		{"single-country town", "Walldorf", "DE"},
		// State code "de" is barred (collides with Germany's ISO code), so the
		// city name wins: Wilmington, DE is the US city, never Germany.
		{"delaware state code never germany", "Wilmington, DE", "US"},

		// Region/aggregate words are not places (barred from the city layer), so a
		// region-only location resolves to the empty Country — keep-on-doubt.
		{"continent america is not a country", "Latin America", ""},
		{"region with remote prefix", "Remote - EU", ""},
		{"region name", "Europe", ""},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"remote alone", "Remote", ""},
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
