// Package geo is the deterministic Country Resolver: the sole authority on the
// ISO 3166-1 alpha-2 Country a Job Listing's free-text location resolves to
// (ADR-0029). It is hand-rolled over a curated gazetteer — no geo dependency —
// and it never guesses: a location it cannot place resolves to the empty
// Country, which the Country Constraint keeps rather than drops (ADR-0028).
package geo

import (
	"strings"
	"unicode"
)

// Resolve maps a Job Listing's raw, free-text location to its ISO 3166-1
// alpha-2 Country code (e.g. "Berlin, Germany" -> "DE"). It returns "" (the
// empty Country) when it cannot confidently place the location — never a wrong
// guess. The crawl keeps the empty Country rather than dropping the listing
// (ADR-0028, ADR-0029).
//
// Matching is on whole word tokens, so "Georgia" resolves to the country GE
// only as a standalone token, never inside "Georgian". A country token is
// preferred over a city, and when several countries appear the rightmost wins —
// encoding the real-world "City, Region, Country" convention, so
// "Atlanta, Georgia, USA" resolves to US (the US-state reading) while a bare
// "Georgia" resolves to GE.
func Resolve(location string) string {
	tokens := tokenize(fold(location))
	if len(tokens) == 0 {
		return ""
	}
	// Country pass first (an explicit country token beats a city), then the
	// city safety-net; both return the rightmost match.
	if code := matchRightmost(tokens, countryByName); code != "" {
		return code
	}
	if code := matchRightmost(tokens, countryByCity); code != "" {
		return code
	}
	return ""
}

// Valid reports whether code is a real, officially assigned ISO 3166-1 alpha-2
// code. It is case-insensitive and trims surrounding space. Reserved and
// user-assigned codes (e.g. "EU", "UK") are not valid — GB is the assigned code
// for the United Kingdom — even though Resolve maps the synonym "UK" to GB.
// Reused by API country-code validation.
func Valid(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 'A' || code[i] > 'Z' {
			return false
		}
	}
	_, ok := validCodes[code]
	return ok
}

// matchRightmost slides an n-gram window (widest phrase first, from each start)
// over tokens and returns the code of the rightmost-ending match in table,
// tie-broken toward the longer phrase. Ranking by end position (not start) lets
// a multi-word name beat a bare token nested inside it — "north korea" ends
// where "korea" does but is longer, so it wins (KP, not KR) — while still
// preferring the last country in the "City, Region, Country" convention (e.g.
// "Atlanta, Georgia, USA" -> US, whose token ends furthest right). "" means no
// window matched — table values are never empty, so "" is unambiguously
// "not found".
func matchRightmost(tokens []string, table map[string]string) string {
	bestEnd, bestLen, bestCode := -1, 0, ""
	n := len(tokens)
	for start := 0; start < n; start++ {
		width := maxPhraseWords
		if start+width > n {
			width = n - start
		}
		for w := width; w >= 1; w-- {
			phrase := strings.Join(tokens[start:start+w], " ")
			if code, ok := table[phrase]; ok {
				if end := start + w; end > bestEnd || (end == bestEnd && w > bestLen) {
					bestEnd, bestLen, bestCode = end, w, code
				}
				break // longest phrase from this start found; advance the start
			}
		}
	}
	return bestCode
}

// fold normalizes a string for gazetteer lookup: lowercased, with the common
// European diacritics that appear in place names mapped to ASCII (so "München"
// and "Munchen" both become "munchen"). Gazetteer keys are stored folded, so
// this is applied identically to inputs and to the curated data.
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
// characters (commas, spaces, hyphens, slashes, parentheses). Splitting on word
// boundaries is what keeps "Georgia" from matching inside "Georgian".
func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
