package crawler

import (
	"regexp"
	"strings"
)

// WithoutLinkTargets narrows a page's Structural Rendering to the variant the two
// LLM prompts read: every link keeps its text, none keeps its target (ADR-0046).
// One renderer produces one rendering; a human labeller reads it whole, and the
// model reads this narrowing of it.
//
// It is a pure DELETION -- the result is always a subsequence of the input -- which
// is what makes "the labeller's variant is a superset of the prompt's" a property
// rather than a claim:
//
//	"[Apply now](/apply)"                    -> "[Apply now]"
//	"[Fixed Term [12 Months]](/careers/468)" -> "[Fixed Term [12 Months]]"
//	"#\tOpen positions"                      -> unchanged, a heading's level prefix
//	"-\tGo"                                  -> unchanged, a list item's bullet
//	"|\tBerlin"                              -> unchanged, a table row's bar
//	"Redcare\t\nNewsroom"                    -> unchanged, the JOIN
//	"[input checkbox: role]"                 -> unchanged, a form control
//	"[button: Senden]"                       -> unchanged
//	"[img: 1A SMS GmbH]"                     -> unchanged
//	"already flat page text"                 -> unchanged, byte for byte
//
// Why the target goes: over the 90 committed classifier fixtures the rendering
// measures 1.253x the Flattened Text with targets and 1.081x without (ADR-0046
// measured 1.43x / 1.10x over 36 live pages). LLM_EXTRACT_MAX_CHARS is 8000, so at
// an unchanged cap the targets-carrying form would show the model roughly a quarter
// less page than today, where the narrowed form costs about a tenth -- "keep the
// targets" and "raise the cap" compound rather than compose.
//
// Why the brackets stay: the marking is the signal. A rail of "similar jobs" links
// beside a posting is only distinguishable from the posting's own prose while the
// link text is still marked as link text, and dropping the brackets buys nothing.
//
// Why it is the identity while the kill switch is off: every link marker the
// renderer writes carries "](", and zero of the 526 KB of page text in the 90
// committed fixtures contains that pair. With PARSE_STRUCTURAL_RENDERING off the
// parser's output has no markers at all, so the prompt carries exactly the bytes it
// carried before the renderer existed -- asserted over all 90 fixtures by
// TestPromptVariantIsIdentityOnFlattenedText.
//
// Only a LINK's target goes. A link rule on its own cannot express that, because
// every marker ends in "]" and so satisfies the left half of linkMarkerPattern: an
// image or a control glued to page text that opens with a parenthesis reads as a
// link with a target, and the page's own parenthetical is what gets deleted.
//
//	"#\vSales Manager[img: Acme logo](m/w/d)"  ->  "(m/w/d)" deleted, the exact
//	                                               token that marks a German role
//	"[input text: Vorname](optional) Nachname" ->  "(optional)" deleted
//
// So the scan recognises an attribute-only marker and a button FIRST and leaves
// them alone, and only what is left is read as a link (#289). Nothing persisted was
// ever at risk -- FlattenedText strips control and button markers before the link
// rule and so never had the ambiguity -- but the loss landed in the two prompts,
// which is the one place this whole change exists to improve.
//
// The residual, stated rather than hidden: page text that literally contains "]("
// loses the run between them. The empty "[]" marker of a link with no text and the
// "[[img: ...]]" of an image inside a link are left exactly as the renderer wrote
// them -- 138 and 100 occurrences across the 90 fixtures, ~4% of 3443 markers --
// because one rule with no special cases is what keeps this a deletion.
//
// Pure: no model, no network, no database.
func WithoutLinkTargets(rendering string) string {
	// Fast path, and an exact one: a link marker cannot exist without "](", so text
	// carrying none is already the narrowing of itself. It is what puts today's
	// flattened parser output on the identical path it was on before the rendering
	// existed.
	if !strings.Contains(rendering, "](") {
		return rendering
	}
	return anyMarkerOrLink.ReplaceAllStringFunc(rendering, func(marker string) string {
		groups := linkMarkerPattern.FindStringSubmatch(marker)
		if groups == nil {
			return marker // an image, a control or a button: not a link, keep it whole
		}
		return "[" + groups[1] + "]"
	})
}

// RenderingBlock names what one line of a Structural Rendering is: its line prefix,
// read forwards (the grammar table in internal/parser/structural_rendering.go).
type RenderingBlock string

const (
	RenderingParagraph RenderingBlock = "paragraph"
	RenderingHeading   RenderingBlock = "heading"
	// RenderingItem is a list item or a <select> option; the renderer writes one
	// bullet for both, and the marker beside a <select> is what tells them apart.
	RenderingItem RenderingBlock = "item"
	// RenderingRow is a table row. Its Cells are the page's own cells.
	RenderingRow RenderingBlock = "row"
)

// RenderingMark names what one piece of a rendered line is.
type RenderingMark string

const (
	RenderingTextMark    RenderingMark = "text"    // page text, verbatim
	RenderingLinkMark    RenderingMark = "link"    // [text](target)
	RenderingButtonMark  RenderingMark = "button"  // [button: text]
	RenderingControlMark RenderingMark = "control" // [img|input|select|textarea ...]
)

// RenderingPiece is one piece of a rendered line.
type RenderingPiece struct {
	Mark RenderingMark
	// Text is PAGE text and nothing else: verbatim for a text piece, the wrapped text
	// for a link or a button, and EMPTY for a control -- a control marker is built
	// only from attributes, so it contributes no page words at all (FlattenedText
	// deletes it whole).
	Text string
	// Target is a link's target as the RENDERER recorded it: sanitized, its ")"
	// percent-encoded and the whole capped at 80 runes (parser.markerHref). A long
	// target is therefore truncated here, and a reader that turns one into a click
	// has to say so.
	Target string
	// Label is a control marker's whole inner text ("input checkbox: role", "img:
	// Acme logo"). It is display material built from attributes, never page text.
	Label string
}

// RenderingLine is one line of a Structural Rendering.
type RenderingLine struct {
	Block RenderingBlock
	// Level is a heading's level, 1..6; 0 for every other block.
	Level int
	// Cells holds ONE entry for every block but a table row, which carries one per
	// cell. A cell boundary the page itself ran together is invisible by construction
	// -- the renderer writes nothing there -- so those two cells arrive as one,
	// exactly as FlattenedText reads them.
	Cells [][]RenderingPiece
	// Joined reports the JOIN: the page ran this line together with the next with no
	// whitespace at all. It is the "Über RedcareNewsroom" case, and a reader that
	// folded it to a space would put a word boundary on screen the page never had.
	Joined bool
}

// ScanRendering reads a Structural Rendering back as its lines and their pieces --
// the third reading of the one grammar (ADR-0046), beside FlattenedText's strip and
// WithoutLinkTargets' narrowing. The labelling UI is what needs it: a human decides
// "is this ONE Job Listing?" from the structure, and the tool turns these lines into
// headings, list items, table rows and links on screen (#288).
//
// It is TOTAL, and lossless with respect to PAGE TEXT: concatenating every piece's
// Text -- cells joined by a tab, lines joined by nothing where Joined and by a space
// elsewhere -- and collapsing whitespace reproduces FlattenedText(rendering) exactly.
// That property is the test (TestScanRenderingReconstructsTheFlattenedText here, and
// TestScanRenderingAgreesWithFlattenedTextOverTheFixtures over all 90 committed page
// fixtures in internal/parser), and it is what keeps a rendering on screen from ever
// showing words the Flattened Text the gate actually keyed on does not have.
//
// It resolves the line prefixes and the JOIN BEFORE it reads the markers, in exactly
// FlattenedText's order, and that order is not a detail. A marker can straddle a line
// boundary -- an <a> around a block renders as "[\t\nWebsite](http://fugro.com)", and
// two of the 90 committed fixtures do it -- so a scanner that split the lines first
// would leave the marker's own syntax on screen as page text. It can carry a line
// prefix too, which the strip deletes from inside the marker for the same reason. A
// marker that still spans two lines after that contributes its page text to each of
// them, so no line ever gains or loses a byte the strip did not give it.
//
// Already-flat text scans as one paragraph of one text piece, so a row captured
// before the renderer existed reads through this unchanged.
//
// It is a READER of the grammar and not the renderer, so it does not move
// parser.RendererStructural: nothing about what is written down changes here.
//
// Pure: no model, no network, no database.
func ScanRendering(rendering string) []RenderingLine {
	resolved, bounds := scanLines(rendering)
	scan := &renderingScan{text: resolved, spans: scanSpans(resolved)}

	lines := make([]RenderingLine, 0, len(bounds))
	for _, bound := range bounds {
		line := RenderingLine{Block: bound.block, Level: bound.level, Joined: bound.joined}
		for _, cut := range scanCells(resolved, bound) {
			line.Cells = append(line.Cells, scan.pieces(cut.lo, cut.hi))
		}
		// Never dropped, not even an empty one: the reconstruction property is stated
		// over the line count, and a line the scanner swallowed is a boundary the screen
		// would put back as nothing.
		lines = append(lines, line)
	}
	return lines
}

// scanBound is one line of a rendering after its prefix and its JOIN are resolved:
// what the line IS, and the half-open range of the resolved text it occupies.
type scanBound struct {
	block  RenderingBlock
	level  int
	joined bool
	lo, hi int
}

// scanLines resolves the two shapes FlattenedText resolves first -- the line prefixes
// and the JOIN -- and returns the text that leaves behind together with each line's
// range in it. The resolved text is byte for byte what FlattenedText holds after its
// first two steps, which is what lets the markers be read over it exactly once.
func scanLines(rendering string) (string, []scanBound) {
	raws := strings.Split(rendering, "\n")
	var b strings.Builder
	b.Grow(len(rendering))
	bounds := make([]scanBound, 0, len(raws))
	for i, raw := range raws {
		bound := scanBound{block: RenderingParagraph}
		// A tab immediately before a newline is only ever the JOIN: the renderer drops a
		// cell boundary owed at a block break rather than write one there.
		if strings.HasSuffix(raw, "\t") {
			bound.joined = true
			raw = raw[:len(raw)-1]
		}
		rest := raw
		if m := scanLinePrefixPattern.FindStringSubmatch(raw); m != nil {
			switch prefix := m[1]; prefix[0] {
			case '#':
				bound.block, bound.level = RenderingHeading, len(prefix)
			case '-':
				bound.block = RenderingItem
			case '|':
				bound.block = RenderingRow
			}
			rest = raw[len(m[0]):]
		}
		// The JOIN is deleted rather than folded; every other boundary stays whitespace,
		// which is the difference between "RedcareNewsroom" and a word boundary the page
		// never had.
		if i > 0 && !bounds[i-1].joined {
			b.WriteString("\n")
		}
		bound.lo = b.Len()
		b.WriteString(rest)
		bound.hi = b.Len()
		bounds = append(bounds, bound)
	}
	return b.String(), bounds
}

// scanCut is one cell's range within the resolved text.
type scanCut struct{ lo, hi int }

// scanCells cuts a line into its cells. Only a table row has more than one: every
// other block is one cell by construction, and a cell boundary the page ran together
// is invisible because the renderer wrote nothing there.
func scanCells(text string, bound scanBound) []scanCut {
	if bound.block != RenderingRow {
		return []scanCut{{bound.lo, bound.hi}}
	}
	cuts := []scanCut{}
	lo := bound.lo
	for i := lo; i < bound.hi; i++ {
		if text[i] != '\t' {
			continue
		}
		cuts = append(cuts, scanCut{lo, i})
		lo = i + 1
	}
	return append(cuts, scanCut{lo, bound.hi})
}

// renderingSpan is one run of the resolved text and what the grammar says it is. A
// marker's own syntax -- its brackets, its "button: ", its target -- is covered by NO
// span, which is precisely how it is dropped: FlattenedText deletes exactly those
// bytes. A control marker is the one zero-width span, because it contributes a piece
// to the screen and no page words at all.
type renderingSpan struct {
	lo, hi int
	mark   RenderingMark
	target string
	label  string
}

// scanSpans reads the markers over the whole resolved text. The walk is
// anyMarkerOrLink's -- the SAME ordered alternation WithoutLinkTargets uses, so a
// control or a button can never be claimed by the link rule (#289) -- and each match
// is re-read in that same order to say which of the three it was.
func scanSpans(text string) []renderingSpan {
	spans := []renderingSpan{}
	last := 0
	for _, loc := range anyMarkerOrLink.FindAllStringIndex(text, -1) {
		if loc[0] > last {
			spans = append(spans, renderingSpan{lo: last, hi: loc[0], mark: RenderingTextMark})
		}
		spans = append(spans, markerSpan(text, loc[0], loc[1]))
		last = loc[1]
	}
	if last < len(text) {
		spans = append(spans, renderingSpan{lo: last, hi: len(text), mark: RenderingTextMark})
	}
	return spans
}

// markerSpan reads one whole marker and returns the span its PAGE TEXT occupies. The
// order is the alternation's own, which is what makes it exact.
func markerSpan(text string, lo, hi int) renderingSpan {
	marker := text[lo:hi]
	if wholeControlMarker.MatchString(marker) {
		// Zero width: attribute-built, so no part of it is page text. The label is the
		// marker minus its outer brackets, which is display material and nothing else.
		return renderingSpan{lo: lo, hi: lo, mark: RenderingControlMark, label: marker[1 : len(marker)-1]}
	}
	if g := wholeButtonMarker.FindStringSubmatchIndex(marker); g != nil {
		return renderingSpan{lo: lo + g[2], hi: lo + g[3], mark: RenderingButtonMark}
	}
	if g := wholeLinkMarker.FindStringSubmatchIndex(marker); g != nil {
		return renderingSpan{lo: lo + g[2], hi: lo + g[3], mark: RenderingLinkMark, target: marker[g[4]:g[5]]}
	}
	// Unreachable: anyMarkerOrLink is the alternation of exactly these three rules.
	// Falling back to page text rather than panicking is the safe residual -- the words
	// on screen are still the words the strip keeps.
	return renderingSpan{lo: lo, hi: hi, mark: RenderingTextMark}
}

// renderingScan cuts the spans into the pieces one cell holds. The cursor is what
// keeps a page with thousands of markers from costing a pass over all of them per
// line; it only ever advances past a span no later cell can reach.
type renderingScan struct {
	text   string
	spans  []renderingSpan
	cursor int
}

// pieces returns the pieces covering [lo, hi), clipped to it. A span straddling the
// range contributes its own bytes to each side, so no cell gains or loses a byte.
func (s *renderingScan) pieces(lo, hi int) []RenderingPiece {
	for s.cursor < len(s.spans) && s.spans[s.cursor].hi < lo {
		s.cursor++
	}
	pieces := []RenderingPiece{}
	for i := s.cursor; i < len(s.spans) && s.spans[i].lo < hi; i++ {
		span := s.spans[i]
		if span.mark == RenderingControlMark {
			if span.lo >= lo {
				pieces = append(pieces, RenderingPiece{Mark: RenderingControlMark, Label: span.label})
			}
			continue
		}
		from, to := max(span.lo, lo), min(span.hi, hi)
		if from >= to {
			continue
		}
		pieces = append(pieces, RenderingPiece{Mark: span.mark, Text: s.text[from:to], Target: span.target})
	}
	return pieces
}

// The three anchored copies ScanRendering re-reads a marker with. They are derived
// from the patterns themselves rather than rewritten, so there is still exactly one
// grammar: a change to a marker rule reaches the scanner the same commit it lands.
var (
	wholeControlMarker = regexp.MustCompile(`\A(?:` + controlMarkerPattern.String() + `)\z`)
	wholeButtonMarker  = regexp.MustCompile(`\A(?:` + buttonMarkerPattern.String() + `)\z`)
	wholeLinkMarker    = regexp.MustCompile(`\A(?:` + linkMarkerPattern.String() + `)\z`)
)
