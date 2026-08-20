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

	// The rendering's own syntax, one case per rule the strip owes ADR-0046. Each is
	// a shape the renderer synthesizes, so each must come back out exactly as the
	// page would have read without it.
	{"a heading's level prefix is dropped", "#\tOpen positions", "Open positions"},
	{"a sub-heading's prefix is dropped too", "##\tSub", "Sub"},
	{"a list item's bullet is dropped", "-\tGo", "Go"},
	{"a table row's bar is dropped", "|\tBerlin", "Berlin"},
	{"a table cell separator is whitespace like any other", "|\tName\tBerlin", "Name Berlin"},
	{
		// The 56-of-90 case: the page itself ran the two blocks together, so the
		// boundary the rendering makes visible must vanish rather than fold.
		"the JOIN deletes a boundary the page ran together",
		"Über Redcare\t\nNewsroom",
		"Über RedcareNewsroom",
	},
	{
		"a plain newline is an ordinary boundary and folds",
		"Über Redcare \nNewsroom",
		"Über Redcare Newsroom",
	},
	{"a form input contributes nothing", "[input text: Vorname]", ""},
	{"a select contributes nothing", "[select: Position]", ""},
	{"a textarea's marker contributes nothing", "[textarea: message]", ""},
	{"a submit input's caption is an attribute and vanishes", "[input submit: Senden]", ""},
	{"a button's own text survives", "[button: Senden]", "Senden"},
	{"an image's alternative text vanishes", "[img: 1A SMS GmbH]", ""},
	{"a link keeps its text and drops its target", "[Apply now](/apply)", "Apply now"},
	{
		// Four of the 90 fixtures link text with brackets in it.
		"bracketed page text inside a link survives",
		"[Fixed Term [12 Months]](/careers/468)",
		"Fixed Term [12 Months]",
	},
	{
		// Prefixes are tab-anchored precisely so page text of this shape is safe.
		"page text that starts like a bullet is untouched",
		"- not a marker",
		"- not a marker",
	},
	{"page text that starts like a heading is untouched", "# 1 in Germany", "# 1 in Germany"},
	{
		// news.ycombinator.com's real page text: no rule may key on a bare pipe.
		"page text full of pipes is untouched",
		"new | past | comments | ask",
		"new | past | comments | ask",
	},
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
