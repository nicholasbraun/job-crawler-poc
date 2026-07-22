package geo

import (
	"strings"
	"unicode"
)

// This file holds the single normalization surface shared by the runtime
// resolver (Resolve folds and tokenizes each lookup) and the offline gazetteer
// generator (internal/geo/gen keys every stored row through NormalizeKey). Both
// must fold identically or a stored key never matches the input the resolver
// builds from it, so there is exactly one copy here — the two cannot drift by
// construction. The round-trip known_test and the gazetteer data-invariant test
// are the tripwires that fail if a change to this file silently shifts a key.

// Fold normalizes a string for gazetteer lookup: lowercased, with the common
// European diacritics that appear in place names mapped to ASCII (so "München"
// and "Munchen" both become "munchen"). Gazetteer keys are stored folded, so
// this is applied identically to inputs and to the curated data.
func Fold(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case 'ä', 'à', 'á', 'â', 'ã', 'å':
			b.WriteByte('a')
		case 'æ':
			b.WriteString("ae")
		case 'ç':
			b.WriteByte('c')
		case 'é', 'è', 'ê', 'ë':
			b.WriteByte('e')
		case 'í', 'ì', 'î', 'ï':
			b.WriteByte('i')
		case 'ñ':
			b.WriteByte('n')
		case 'ö', 'ò', 'ó', 'ô', 'õ', 'ø':
			b.WriteByte('o')
		case 'ü', 'ù', 'ú', 'û':
			b.WriteByte('u')
		case 'ß':
			b.WriteString("ss")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tokenize splits a folded string into word tokens on runs of non-alphanumeric
// characters (commas, spaces, hyphens, slashes, parentheses). Splitting on word
// boundaries is what keeps "Georgia" from matching inside "Georgian".
func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// NormalizeKey is the single function every stored gazetteer key passes through:
// it folds then joins word tokens with single spaces, yielding the exact phrase
// form the resolver builds from an input at lookup time (strings.Join over
// tokenize(Fold(s))). So "Côte d'Ivoire" -> "cote d ivoire", "St. Louis" ->
// "st louis", "München" -> "munchen", "United States" -> "united states".
func NormalizeKey(s string) string {
	return strings.Join(tokenize(Fold(s)), " ")
}
