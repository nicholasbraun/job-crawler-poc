package geo_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

func TestNormalizeKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"diacritic fold", "München", "munchen"},
		{"apostrophe and diacritic split into tokens", "Côte d'Ivoire", "cote d ivoire"},
		{"punctuation collapses", "St. Louis", "st louis"},
		{"phrase joins with single space", "United States", "united states"},
		{"comma and extra space", "Korea, Republic of", "korea republic of"},
		{"hyphen is a token boundary", "Guinea-Bissau", "guinea bissau"},
		{"trailing space trimmed", "Sweden ", "sweden"},
		{"ss expansion", "Preußen", "preussen"},
		{"already normalized", "berlin", "berlin"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.NormalizeKey(tt.in); got != tt.want {
				t.Errorf("NormalizeKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFold(t *testing.T) {
	// One case per diacritic switch arm, so a wrong edit to a single arm fails
	// here directly rather than only transitively through NormalizeKey, the
	// gazetteer round-trip, or the data-invariant test.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases ascii", "BERLIN", "berlin"},
		{"a-group", "Åàáâã", "aaaaa"},
		{"ae ligature and o-slash", "Ærø", "aero"},
		{"c cedilla", "Curaçao", "curacao"},
		{"e-group", "Évêché", "eveche"},
		{"i-group", "Reykjavík", "reykjavik"},
		{"n tilde", "Ñuñoa", "nunoa"},
		{"o-group", "Córdoba", "cordoba"},
		{"u-group", "Zürich", "zurich"},
		{"eszett", "Preußen", "preussen"},
		{"unmapped rune passes through", "北京", "北京"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geo.Fold(tt.in); got != tt.want {
				t.Errorf("Fold(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
