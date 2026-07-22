// Package geo is the deterministic Country Resolver: the sole authority on the
// ISO 3166-1 alpha-2 Country a Job Listing's free-text location resolves to
// (ADR-0029). It matches free text against a generated gazetteer — built offline
// from GeoNames + ISO 3166 (internal/geo/gen) and embedded, parsed once at init —
// so it carries no runtime geo dependency and stays byte-deterministic (ADR-0031).
// It never guesses: a location it cannot place resolves to the empty Country,
// which the Country Constraint keeps rather than drops (ADR-0028).
package geo

import (
	"regexp"
	"strings"
)

// Resolve maps a Job Listing's raw, free-text location to its ISO 3166-1
// alpha-2 Country code (e.g. "Berlin, Germany" -> "DE"). It returns "" (the
// empty Country) when it cannot confidently place the location — never a wrong
// guess. The crawl keeps the empty Country rather than dropping the listing
// (ADR-0028, ADR-0029).
//
// Matching is on whole word tokens, so "Georgia" resolves to the country GE
// only as a standalone token, never inside "Georgian". The gazetteer's three
// layers are tried in descending precedence and the rightmost match within a
// layer wins, encoding the real-world "City, State, Country" convention: an
// explicit country token beats a US-state or city reading, so "Vienna, Austria"
// resolves to AT and "Atlanta, Georgia, USA" resolves to US (the rightmost
// country) while a bare "Georgia" resolves to GE.
//
// The bare-US pass runs second, between the country and state/city layers. A
// standalone "US"/"U.S."/"USA" is a country-level signal the country layer
// deliberately omits (folding corrupts "US" into the pronoun "us"), so it must
// outrank a state or city reading — yet it must not override an explicitly named
// rightmost country, which the country layer, running first, has already placed.
func Resolve(location string) string {
	tokens := tokenize(Fold(location))
	if len(tokens) == 0 {
		return ""
	}
	if code := matchRightmost(tokens, gaz.country, gaz.maxWords); code != "" {
		return code
	}
	if matchUSToken(location) {
		return "US"
	}
	if code := matchRightmost(tokens, gaz.state, gaz.maxWords); code != "" {
		return code
	}
	if code := matchRightmost(tokens, gaz.city, gaz.maxWords); code != "" {
		return code
	}
	return ""
}

// usToken matches a bare United States token — "US", "U.S.", "USA", "U.S.A." —
// as a standalone, UPPERCASE token. matchUSToken runs it on the original,
// unfolded string on purpose: folding lowercases "US" into the English pronoun
// "us" ("join us"), which must never resolve. The required uppercasing plus the
// non-letter boundaries are what separate the country signal from the pronoun
// ("join us") and from substrings ("Belarus", "campus", "AUSTIN").
var usToken = regexp.MustCompile(`(?:^|[^A-Za-z])(?:USA|U\.S\.A\.|U\.S\.|US)(?:[^A-Za-z]|$)`)

// matchUSToken reports whether location contains a bare United States token as an
// uppercase, boundary-delimited word (see usToken). Accepted edges, both
// improbable in a location field: an all-caps CTA ("JOIN US") matches, and a
// bare rightmost "US" after an already-named country yields the earlier country
// because the country layer resolves first.
func matchUSToken(location string) bool {
	return usToken.MatchString(location)
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

// matchRightmost slides an n-gram window up to maxWords wide (widest phrase
// first, from each start) over tokens and returns the code of the
// rightmost-ending match in table, tie-broken toward the longer phrase. Ranking
// by end position (not start) lets a multi-word name beat a bare token nested
// inside it — "north korea" ends where "korea" does but is longer, so it wins
// (KP, not KR) — while still preferring the last country in the
// "City, Region, Country" convention (e.g. "Atlanta, Georgia, USA" -> US, whose
// token ends furthest right). "" means no window matched — table values are
// never empty, so "" is unambiguously "not found".
func matchRightmost(tokens []string, table map[string]string, maxWords int) string {
	bestEnd, bestLen, bestCode := -1, 0, ""
	n := len(tokens)
	for start := 0; start < n; start++ {
		width := maxWords
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

// Fold and tokenize (the normalization surface shared with the gazetteer
// generator) live in normalize.go.
