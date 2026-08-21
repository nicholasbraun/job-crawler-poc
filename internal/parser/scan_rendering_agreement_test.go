package parser_test

import (
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// TestScanRenderingAgreesWithFlattenedTextOverTheFixtures is the corpus-scale half of
// ScanRendering's property (ADR-0046, #288): putting a scanned rendering back together
// reproduces its Flattened Text byte for byte, over every committed page fixture.
//
// It is what makes "the rendering a labeller reads can never show words the text the
// gate keyed on does not have" a property rather than a claim. internal/ asserts the
// same thing over hand-written cases; this asserts it over 90 real pages, which is
// where a marker glued to a parenthetical or a page word that is a prefix character
// actually occurs.
func TestScanRenderingAgreesWithFlattenedTextOverTheFixtures(t *testing.T) {
	rendering := parser.NewHTMLParser(parser.WithStructuralRendering(true))

	scored := 0
	for _, name := range goldenFixtureNames(t) {
		scored++
		t.Run(name, func(t *testing.T) {
			content, err := rendering.Parse(readFixture(t, name))
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			got := reconstruct(crawler.ScanRendering(content.MainContent))
			want := crawler.FlattenedText(content.MainContent)
			if got == want {
				return
			}
			offset, gotWindow, wantWindow := firstDiff(got, want)
			t.Errorf("the scan of %s reconstructs %d bytes, its Flattened Text has %d, first difference at byte %d\n got: %s\nwant: %s\n"+
				"a rendering on screen may not carry page words the text the gate keyed on does not have (ADR-0046)",
				name, len(got), len(want), offset, gotWindow, wantWindow)
		})
	}
	if scored < roundTripFixturesMin {
		t.Fatalf("the scan was asserted over only %d fixtures, want at least %d -- an invariant this narrow proves nothing", scored, roundTripFixturesMin)
	}
}

// TestScanRenderingReadsTheFixturesAsStructure is the other half: the reconstruction
// above would also hold for a scanner that read every page as one flat paragraph. The
// fixtures really do carry headings, list items, table rows and links, and a scan that
// stopped seeing them would leave the labelling UI drawing a wall of text again.
func TestScanRenderingReadsTheFixturesAsStructure(t *testing.T) {
	rendering := parser.NewHTMLParser(parser.WithStructuralRendering(true))

	blocks := map[crawler.RenderingBlock]int{}
	marks := map[crawler.RenderingMark]int{}
	targets := 0
	for _, name := range goldenFixtureNames(t) {
		content, err := rendering.Parse(readFixture(t, name))
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, line := range crawler.ScanRendering(content.MainContent) {
			blocks[line.Block]++
			for _, cell := range line.Cells {
				for _, piece := range cell {
					marks[piece.Mark]++
					if piece.Mark == crawler.RenderingLinkMark && piece.Target != "" {
						targets++
					}
				}
			}
		}
	}

	for _, block := range []crawler.RenderingBlock{
		crawler.RenderingParagraph, crawler.RenderingHeading, crawler.RenderingItem, crawler.RenderingRow,
	} {
		if blocks[block] == 0 {
			t.Errorf("no fixture line scanned as %q; the scanner has stopped reading a shape the renderer writes", block)
		}
	}
	for _, mark := range []crawler.RenderingMark{
		crawler.RenderingTextMark, crawler.RenderingLinkMark, crawler.RenderingButtonMark, crawler.RenderingControlMark,
	} {
		if marks[mark] == 0 {
			t.Errorf("no fixture piece scanned as %q; the scanner has stopped reading a marker the renderer writes", mark)
		}
	}
	// The whole reason the labeller's variant keeps targets (ADR-0046): "/organization/..."
	// versus "/jobs/..." is what settles an employer directory.
	if targets == 0 {
		t.Error("not one link in 90 fixtures carried a target; the labeller's variant of the rendering exists to show them")
	}
	t.Logf("blocks %v, marks %v, links carrying a target %d", blocks, marks, targets)
}

// reconstruct puts a scan back together as page text: pieces in order, cells joined by
// the tab they were split on, lines joined by NOTHING where the page ran them together
// and by a space elsewhere, then collapsed exactly as FlattenedText collapses.
//
// internal/structural_rendering_test.go carries the same lines for the hand-written
// cases, because a test package cannot import another test package. The two are one
// statement of the property and move together.
func reconstruct(lines []crawler.RenderingLine) string {
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
	return strings.Join(strings.Fields(b.String()), " ")
}
