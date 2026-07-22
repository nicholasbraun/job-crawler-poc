// These tests live in package main (not an external _test package): a main
// package cannot be imported, which is the standard, justified exception for a
// build tool. They feed synthetic inputs only — never the embedded reference
// data — so they stay fast and hermetic.
package main

import "testing"

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
			if got := normalizeKey(tt.in); got != tt.want {
				t.Errorf("normalizeKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
