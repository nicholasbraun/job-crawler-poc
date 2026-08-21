package crawler_test

import (
	"fmt"
	"reflect"
	"strings"
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
	{"several links on one line", "-\v[A](/a) and [B](/b)", "-\v[A] and [B]"},

	// Everything the model is meant to see, one case per shape.
	{"a heading's level prefix survives", "#\vOpen positions", "#\vOpen positions"},
	{"a list item's bullet survives", "-\vGo", "-\vGo"},
	{"a table row survives", "|\vBackend Engineer\tBerlin", "|\vBackend Engineer\tBerlin"},
	{"the JOIN survives", "Über Redcare\t\nNewsroom", "Über Redcare\t\nNewsroom"},
	{"a form control's marker survives", "[input checkbox: role]", "[input checkbox: role]"},
	{
		// ADR-0046's advertorial.de case: a picker of roles must not read as an index
		// of them, which it only does while the select marker is still there.
		"a select and its options survive",
		"[select: Position]\n-\vSales Manager (m/w/d)",
		"[select: Position]\n-\vSales Manager (m/w/d)",
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

// scanCases pin ScanRendering against the grammar, one case per shape the renderer
// writes. They are also the corpus TestScanRenderingReconstructsTheFlattenedText runs
// the lossless-with-respect-to-page-text property over, so a case added here is a case
// the property has to survive.
var scanCases = []struct {
	name  string
	input string
	want  []crawler.RenderingLine
}{
	{"empty", "", oneLine(crawler.RenderingParagraph, 0)},
	{
		"already-flat text is one paragraph of one text piece",
		"Open roles - Backend Engineer Apply now",
		oneLine(crawler.RenderingParagraph, 0, text("Open roles - Backend Engineer Apply now")),
	},
	{"a level-1 heading", "#\vOpen positions", oneLine(crawler.RenderingHeading, 1, text("Open positions"))},
	{"a level-6 heading", "######\vSmall print", oneLine(crawler.RenderingHeading, 6, text("Small print"))},
	{"a list item", "-\vGo", oneLine(crawler.RenderingItem, 0, text("Go"))},
	{
		// ADR-0046's advertorial.de case: a picker of roles must not read as an index
		// of them, which it only does while the select marker is still beside them.
		"a select and its options",
		"[select: Position]\n-\vSales Manager (m/w/d)",
		[]crawler.RenderingLine{
			line(crawler.RenderingParagraph, 0, false, cell(control("select: Position"))),
			line(crawler.RenderingItem, 0, false, cell(text("Sales Manager (m/w/d)"))),
		},
	},
	{
		"a table row carries one cell per cell",
		"|\vBerlin\tRemote",
		[]crawler.RenderingLine{line(crawler.RenderingRow, 0, false, cell(text("Berlin")), cell(text("Remote")))},
	},
	{
		// A boundary the PAGE ran together is invisible by construction: the renderer
		// writes nothing there, so the two cells arrive as one.
		"a table row the page ran together is one cell",
		"|\vBerlinRemote",
		oneLine(crawler.RenderingRow, 0, text("BerlinRemote")),
	},
	{
		"a link keeps its text AND its target",
		"[Apply now](/apply)",
		oneLine(crawler.RenderingParagraph, 0, link("Apply now", "/apply")),
	},
	{
		// Four of the 90 committed fixtures link text with brackets in it.
		"bracketed page text inside a link survives",
		"[Fixed Term [12 Months]](/careers/468)",
		oneLine(crawler.RenderingParagraph, 0, link("Fixed Term [12 Months]", "/careers/468")),
	},
	{
		"several links on one item, with the page text between them",
		"-\v[A](/a) and [B](/b)",
		oneLine(crawler.RenderingItem, 0, link("A", "/a"), text(" and "), link("B", "/b")),
	},
	{"a button keeps its own text", "[button: Senden]", oneLine(crawler.RenderingParagraph, 0, button("Senden"))},
	{
		"a form control carries no page words",
		"[input checkbox: role]",
		oneLine(crawler.RenderingParagraph, 0, control("input checkbox: role")),
	},
	{
		"an image carries no page words",
		"[img: 1A SMS GmbH]",
		oneLine(crawler.RenderingParagraph, 0, control("img: 1A SMS GmbH")),
	},
	{
		// The ordered alternation (#289): a control glued to a page parenthetical is
		// read as a control and the parenthetical stays page text, never as a link
		// whose "target" is the token that marks a German role.
		"a control glued to a parenthetical is not a link",
		"#\vSales Manager[img: Acme logo](m/w/d)",
		oneLine(crawler.RenderingHeading, 1, text("Sales Manager"), control("img: Acme logo"), text("(m/w/d)")),
	},
	{
		// crawler.WrappableMarkerText declines the marker rather than corrupt the text,
		// so what reaches the scanner is page text and reads as page text.
		"a declined marker is page text",
		"See ] more",
		oneLine(crawler.RenderingParagraph, 0, text("See ] more")),
	},
	{
		// An <a> around a block: the JOIN lands INSIDE the marker, and two of the 90
		// committed fixtures do it (join.com's "[\t\nWebsite](http://fugro.com)"). The
		// line the "[" sits on contributes no page text at all, and the marker is read
		// as a link rather than left on screen as its own syntax.
		"a link straddling the JOIN",
		"Read more\t\n[\t\nWebsite](http://fugro.com)",
		[]crawler.RenderingLine{
			line(crawler.RenderingParagraph, 0, true, cell(text("Read more"))),
			line(crawler.RenderingParagraph, 0, true, cell()),
			line(crawler.RenderingParagraph, 0, false, cell(link("Website", "http://fugro.com"))),
		},
	},
	{
		// A marker still spanning two lines after the JOIN is resolved gives its page
		// text to BOTH, so neither line gains or loses a byte the strip did not give it.
		"a link whose text spans an ordinary boundary",
		"[A\nB](/x)",
		[]crawler.RenderingLine{
			line(crawler.RenderingParagraph, 0, false, cell(link("A", "/x"))),
			line(crawler.RenderingParagraph, 0, false, cell(link("B", "/x"))),
		},
	},
	{
		// A line prefix inside a marker, which the strip deletes from inside it for the
		// same reason: an <a> around an <h2>.
		"a heading prefix inside a link",
		"[\t\n##\vTitle](/x)",
		[]crawler.RenderingLine{
			line(crawler.RenderingParagraph, 0, true, cell()),
			line(crawler.RenderingHeading, 2, false, cell(link("Title", "/x"))),
		},
	},
}

// TestScanRenderingReadsTheGrammar pins the third reading of the one grammar
// (ADR-0046): the lines a rendering has, and the pieces each line is made of.
func TestScanRenderingReadsTheGrammar(t *testing.T) {
	for _, tc := range scanCases {
		t.Run(tc.name, func(t *testing.T) {
			got := crawler.ScanRendering(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ScanRendering(%q) =\n %s\nwant\n %s", tc.input, describe(got), describe(tc.want))
			}
		})
	}
}

// TestScanRenderingReadsTheJoin pins the boundary the page itself ran together. A
// reader that folded it to a space would put a word boundary on screen the page never
// had -- "Über RedcareNewsroom" is 58 of the 90 committed fixtures.
func TestScanRenderingReadsTheJoin(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantJoined []bool
	}{
		{"the JOIN", "Redcare\t\nNewsroom", []bool{true, false}},
		{"an ordinary boundary", "A\nB", []bool{false, false}},
		{"a JOIN before a prefixed line", "Salary\t\n-\vBerlin", []bool{true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := crawler.ScanRendering(tt.input)
			if len(got) != len(tt.wantJoined) {
				t.Fatalf("ScanRendering(%q) read %d lines, want %d: %s", tt.input, len(got), len(tt.wantJoined), describe(got))
			}
			for i, want := range tt.wantJoined {
				if got[i].Joined != want {
					t.Errorf("line %d of %q has Joined = %v, want %v", i, tt.input, got[i].Joined, want)
				}
			}
		})
	}
}

// TestScanRenderingSurvivesAPageWordThatIsAPrefixCharacter is the #289 case read
// forwards: the VERTICAL TAB is what makes a prefix a prefix, so a page word that IS
// "-", "|" or "#" and lands at the start of a rendered line stays page text. The
// scanner has to decide that exactly the way the strip does, or a screen would delete
// a character the page really had.
func TestScanRenderingSurvivesAPageWordThatIsAPrefixCharacter(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantBlock crawler.RenderingBlock
		wantText  string
	}{
		{"a bare dash the page wrote, followed by the JOIN", "-\t\nBerlin", crawler.RenderingParagraph, "-"},
		{"a bare bar the page wrote, followed by a cell", "|\tBerlin", crawler.RenderingParagraph, "|\tBerlin"},
		{"a real bullet", "-\vBerlin", crawler.RenderingItem, "Berlin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := crawler.ScanRendering(tt.input)
			if got[0].Block != tt.wantBlock {
				t.Errorf("ScanRendering(%q) read line 0 as %q, want %q", tt.input, got[0].Block, tt.wantBlock)
			}
			if text := lineText(got[0]); text != tt.wantText {
				t.Errorf("ScanRendering(%q) kept page text %q on line 0, want %q", tt.input, text, tt.wantText)
			}
		})
	}
}

// TestScanRenderingReconstructsTheFlattenedText is the property the whole reader
// rests on: the pieces put back together ARE the Flattened Text. A rendering on
// screen can therefore never show the labeller words the text the gate keyed on does
// not have. internal/parser asserts the same property over the 90 committed page
// fixtures, which is the corpus-scale half of it.
func TestScanRenderingReconstructsTheFlattenedText(t *testing.T) {
	inputs := []string{
		// ADR-0046's own examples, which is where the grammar is written down.
		"#\vOpen positions", "-\vGo", "|\vBerlin", "Redcare\t\nNewsroom",
		"[input text: Vorname]", "[select: Position]", "[textarea: message]",
		"[img: 1A SMS GmbH]", "[button: Senden]", "[Apply now](/apply)",
		"#\vUnternehmen\n-\v[Firma Alpha](/organization/1) Musterstrasse 1\n-\v[Firma Beta](/organization/2)",
		"Salary-Berlin", "-\t\nBerlin", "|\vA\tB\t\n|\vC\tD",
	}
	for _, tc := range scanCases {
		inputs = append(inputs, tc.input)
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			if got, want := reconstruct(crawler.ScanRendering(in)), crawler.FlattenedText(in); got != want {
				t.Errorf("reconstructing the scan of %q gives %q, want the Flattened Text %q", in, got, want)
			}
		})
	}
}

// TestLinePrefixPatternsAgree pins the strip and the scanner to one rule: whatever
// linePrefixPattern DELETES from a line, scanLinePrefixPattern has to read as that
// line's prefix and leave the same remainder. A grammar change that moved one and not
// the other would be a scanner reading a rendering the strip does not.
func TestLinePrefixPatternsAgree(t *testing.T) {
	lines := []string{
		"#\vh1", "##\vh2", "###\vh3", "####\vh4", "#####\vh5", "######\vh6",
		"#######\vseven hashes are not a prefix", "-\vitem", "|\vrow", "|\vA\tB",
		"plain", "", "-", "-\tnot a prefix", "- not a prefix", "\vno prefix character",
		"-\v", "-\v-\vonly the first is a prefix",
	}
	for _, raw := range lines {
		t.Run(raw, func(t *testing.T) {
			stripped := crawler.FlattenedText(raw)
			scanned := reconstruct(crawler.ScanRendering(raw))
			if stripped != scanned {
				t.Errorf("the strip reads %q as %q but the scanner reconstructs %q", raw, stripped, scanned)
			}
		})
	}
}

// reconstruct puts a scan back together as page text: pieces in order, cells joined by
// the tab they were split on, lines joined by NOTHING where the page ran them together
// and by a space elsewhere, then collapsed exactly as FlattenedText collapses.
//
// internal/parser carries the same 15 lines for the corpus-scale assertion, because a
// test package cannot import another test package. The two are one statement of the
// property and move together.
func reconstruct(lines []crawler.RenderingLine) string {
	return strings.Join(strings.Fields(reconstructRaw(lines)), " ")
}

// lineText is one line's page text, uncollapsed, so a test can assert on the bytes
// that line actually kept.
func lineText(line crawler.RenderingLine) string {
	return reconstructRaw([]crawler.RenderingLine{line})
}

// reconstructRaw is reconstruct without the whitespace collapse.
func reconstructRaw(lines []crawler.RenderingLine) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 && !lines[i-1].Joined {
			b.WriteString(" ")
		}
		for c, cell := range line.Cells {
			if c > 0 {
				b.WriteString("\t")
			}
			for _, piece := range cell {
				b.WriteString(piece.Text)
			}
		}
	}
	return b.String()
}

// describe renders a scan for a failure message: the shape, not 500 bytes of struct.
func describe(lines []crawler.RenderingLine) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		var b strings.Builder
		fmt.Fprintf(&b, "%s(level %d, joined %v)", line.Block, line.Level, line.Joined)
		for _, cell := range line.Cells {
			b.WriteString(" |")
			for _, piece := range cell {
				fmt.Fprintf(&b, " %s(%q,%q,%q)", piece.Mark, piece.Text, piece.Target, piece.Label)
			}
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n ")
}

// The piece and line constructors the table reads with, so a case says what it means.
func text(s string) crawler.RenderingPiece {
	return crawler.RenderingPiece{Mark: crawler.RenderingTextMark, Text: s}
}

func link(s, target string) crawler.RenderingPiece {
	return crawler.RenderingPiece{Mark: crawler.RenderingLinkMark, Text: s, Target: target}
}

func button(s string) crawler.RenderingPiece {
	return crawler.RenderingPiece{Mark: crawler.RenderingButtonMark, Text: s}
}

func control(label string) crawler.RenderingPiece {
	return crawler.RenderingPiece{Mark: crawler.RenderingControlMark, Label: label}
}

func cell(pieces ...crawler.RenderingPiece) []crawler.RenderingPiece {
	if len(pieces) == 0 {
		return []crawler.RenderingPiece{}
	}
	return pieces
}

func line(block crawler.RenderingBlock, level int, joined bool, cells ...[]crawler.RenderingPiece) crawler.RenderingLine {
	return crawler.RenderingLine{Block: block, Level: level, Joined: joined, Cells: cells}
}

// oneLine is the common case: one line, one cell, the pieces given.
func oneLine(block crawler.RenderingBlock, level int, pieces ...crawler.RenderingPiece) []crawler.RenderingLine {
	return []crawler.RenderingLine{line(block, level, false, cell(pieces...))}
}
