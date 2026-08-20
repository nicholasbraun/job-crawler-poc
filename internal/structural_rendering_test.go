package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// narrowCases pin the variant the two LLM prompts read (ADR-0046): a link keeps its
// text and loses its target, and every other shape the renderer writes reaches the
// model exactly as written. Structure is the whole point of the rendering -- a case
// here that started passing "unchanged" through the strip's rules instead would
// silently take the form controls back off the prompt.
var narrowCases = []struct {
	name  string
	input string
	want  string
}{
	{"empty", "", ""},
	{"a link keeps its text and loses its target", "[Apply now](/apply)", "[Apply now]"},
	{
		// Four of the 90 committed fixtures link text with brackets in it.
		"bracketed page text inside a link survives",
		"[Fixed Term [12 Months]](/careers/468)",
		"[Fixed Term [12 Months]]",
	},
	{
		// The renderer percent-encodes ")" in an href, so the marker's own ")" is the
		// only one in it and the target still goes whole.
		"a percent-encoded target is dropped whole",
		"[Docs](/a%29b)",
		"[Docs]",
	},
	{"several links on one line", "-\t[A](/a) and [B](/b)", "-\t[A] and [B]"},

	// Everything the model is meant to see, one case per shape.
	{"a heading's level prefix survives", "#\tOpen positions", "#\tOpen positions"},
	{"a list item's bullet survives", "-\tGo", "-\tGo"},
	{"a table row survives", "|\tBackend Engineer\tBerlin", "|\tBackend Engineer\tBerlin"},
	{"the JOIN survives", "Über Redcare\t\nNewsroom", "Über Redcare\t\nNewsroom"},
	{"a form control's marker survives", "[input checkbox: role]", "[input checkbox: role]"},
	{
		// ADR-0046's advertorial.de case: a picker of roles must not read as an index
		// of them, which it only does while the select marker is still there.
		"a select and its options survive",
		"[select: Position]\n-\tSales Manager (m/w/d)",
		"[select: Position]\n-\tSales Manager (m/w/d)",
	},
	{"a button's marker survives", "[button: Senden]", "[button: Senden]"},
	{"an image's marker survives", "[img: 1A SMS GmbH]", "[img: 1A SMS GmbH]"},

	{
		// The kill-switch-off path: with PARSE_STRUCTURAL_RENDERING off the parser
		// produces exactly this, and the prompt must carry it byte for byte.
		"Flattened Text is returned byte-identical",
		"Open roles - Backend Engineer Apply now",
		"Open roles - Backend Engineer Apply now",
	},
	{
		"bracketed page text with no target is untouched",
		"contact [email protected] or [pdf]",
		"contact [email protected] or [pdf]",
	},
	{
		// The line that separates this narrowing from the strip: FlattenedText deletes
		// a control-shaped run of page text, because a control marker is attribute-built
		// and none of it is in today's flattened output. Here nothing but a link TARGET
		// is removed, so page text of that shape is page text.
		"a control-shaped run of page text is not stripped here",
		"[input]",
		"[input]",
	},
}

func TestWithoutLinkTargets(t *testing.T) {
	for _, tc := range narrowCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crawler.WithoutLinkTargets(tc.input); got != tc.want {
				t.Errorf("WithoutLinkTargets(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestWithoutLinkTargetsIsIdempotent pins what makes the narrowing safe to apply
// wherever a prompt is built: text that already carries no target reads back
// unchanged, so a hand-written Content in a test and a captured Extract Gold Set row
// reach the model as themselves.
func TestWithoutLinkTargetsIsIdempotent(t *testing.T) {
	for _, tc := range narrowCases {
		t.Run(tc.name, func(t *testing.T) {
			once := crawler.WithoutLinkTargets(tc.input)
			if twice := crawler.WithoutLinkTargets(once); twice != once {
				t.Errorf("WithoutLinkTargets is not idempotent on %q: once = %q, twice = %q", tc.input, once, twice)
			}
		})
	}
}

// TestWithoutLinkTargetsOnlyDeletes is the machine-checkable form of "the variant a
// human labeller reads is a superset of the variant the model reads" (ADR-0046). The
// narrowing may only take characters away; a rule that rewrote or inserted anything
// would make the labelling UI's rendering a second artefact to keep in step.
func TestWithoutLinkTargetsOnlyDeletes(t *testing.T) {
	for _, tc := range narrowCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crawler.WithoutLinkTargets(tc.input); len(got) > len(tc.input) {
				t.Errorf("WithoutLinkTargets(%q) grew from %d to %d bytes: %q", tc.input, len(tc.input), len(got), got)
			}
		})
	}
}
