package main

import (
	"strings"
	"unicode"
)

// fold, tokenize, and normalizeKey are copied verbatim from
// internal/geo/resolver.go (the source of truth) so the generated gazetteer
// keys are normalized identically to how the runtime matcher folds a lookup.
// The two copies must not drift: #173's round-trip known_test and #174's
// data-invariant test are the tripwires that fail if they ever diverge. A
// future cleanup could export a shared geo.NormalizeKey; it is deferred so this
// generator stays additive and #173 owns the resolver rework.

// fold normalizes a string for gazetteer lookup: lowercased, with the common
// European diacritics that appear in place names mapped to ASCII (so "München"
// and "Munchen" both become "munchen").
func fold(s string) string {
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
// characters (commas, spaces, hyphens, slashes, parentheses).
func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// normalizeKey is the single function every stored key passes through: it folds
// then joins word tokens with single spaces, yielding the exact phrase form the
// resolver builds from an input at lookup time (strings.Join over tokenize).
// So "Côte d'Ivoire" -> "cote d ivoire", "St. Louis" -> "st louis",
// "München" -> "munchen", "United States" -> "united states".
func normalizeKey(s string) string {
	return strings.Join(tokenize(fold(s)), " ")
}
