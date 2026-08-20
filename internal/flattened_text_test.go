package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// flattenCases pins the whitespace semantics every content consumer now inherits.
// They are the parser's normalizeWS semantics exactly, which is why the derivation is
// the identity on today's output (ADR-0046) -- #279 must not change any of them by
// accident while it adds the stripping.
var flattenCases = []struct {
	name  string
	input string
	want  string
}{
	{"empty", "", ""},
	{"already flat text is unchanged", "already flat text", "already flat text"},
	{"the ends are trimmed", "  leading and trailing  ", "leading and trailing"},
	{
		"a rendering's line structure collapses to single spaces",
		"Backend Engineer\n\n- Go\n- Postgres",
		"Backend Engineer - Go - Postgres",
	},
	{"tabs and carriage returns are whitespace", "tabs\tand\r\nnewlines", "tabs and newlines"},
	{"a non-breaking space folds", "a\u00a0b", "a b"},
	{"a zero-width space is page text, not whitespace", "a\u200bb", "a\u200bb"},
	{
		// PostingBody owns dropping these (validUTF8), and SourceHash must never see a
		// different byte string than it saw before -- ~46k persisted rows depend on it.
		"invalid UTF-8 bytes survive",
		"Qualit\xe4tssicherung engineer",
		"Qualit\xe4tssicherung engineer",
	},
	{"whitespace only flattens to empty", " \n\t ", ""},
}

func TestFlattenedText(t *testing.T) {
	for _, tc := range flattenCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crawler.FlattenedText(tc.input); got != tc.want {
				t.Errorf("FlattenedText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestFlattenedTextIsIdempotent pins the property the routing rests on: a consumer
// handed text that is already flat -- a captured Extract Gold Set row, a hand-written
// Content in a test, or today's parser output -- reads it back unchanged, so routing a
// consumer through the derivation can never change what it sees twice over.
func TestFlattenedTextIsIdempotent(t *testing.T) {
	for _, tc := range flattenCases {
		t.Run(tc.name, func(t *testing.T) {
			once := crawler.FlattenedText(tc.input)
			if twice := crawler.FlattenedText(once); twice != once {
				t.Errorf("FlattenedText is not idempotent on %q: once = %q, twice = %q", tc.input, once, twice)
			}
		})
	}
}
