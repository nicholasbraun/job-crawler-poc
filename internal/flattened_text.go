package crawler

import (
	"regexp"
	"strings"
)

// FlattenedText derives a page's Flattened Text from the page content the parser
// stores in Content.MainContent (ADR-0046): the plain run of words every non-model
// consumer reads -- the Posting Body and the Corpus search index it feeds, the
// extraction-cache key, the Extract Gate's phrase marks, the keyword filters, the
// duplication probe.
//
// It is the inverse of what a Structural Rendering adds: whatever the renderer
// synthesized comes back out, and what the page itself said stays. It is DERIVED on
// demand rather than stored beside the rendering, so the two can never disagree about
// what a page says.
//
// The invariant the whole change rests on:
//
//	FlattenedText(a page's Structural Rendering) == the parser's pre-rendering output
//
// byte for byte, over the 90 committed classifier fixtures frozen in
// internal/parser/testdata/flattened_text.golden.jsonl. SourceHash is persisted on
// ~46k job_listing rows and a different value there silently re-extracts the whole
// Corpus, so the derivation is exact or the rendering does not ship (ADR-0046).
//
// What the renderer synthesizes, and what comes back out here (the grammar itself
// is written down in internal/parser/structural_rendering.go, and the two files
// move together or not at all):
//
//	"#\vOpen positions"           -> "Open positions"   a heading's level prefix
//	"-\vGo"                       -> "Go"               a list item's or option's bullet
//	"|\vBerlin"                   -> "Berlin"           a table row's bar
//	"Redcare\t\nNewsroom"         -> "RedcareNewsroom"  a boundary the PAGE ran together
//	"[input text: Vorname]"       -> ""                 the <label> already contributed the word
//	"[select: Position]"          -> ""                 likewise
//	"[textarea: message]"         -> ""                 likewise; its own text survives
//	"[img: 1A SMS GmbH]"          -> ""                 today's output carries no alt text
//	"[button: Senden]"            -> "Senden"           a button's own text IS in today's output
//	"[Apply now](/apply)"         -> "Apply now"        the link text, never its target
//
// Two shapes carry the whole derivation. Line prefixes are anchored to a VERTICAL
// TAB, which the renderer writes to end a prefix and for nothing else, and which can
// never appear in source-derived text -- the renderer collapses every run of page
// whitespace into a separator -- so neither page text that starts "- " nor a page
// word that IS "-" is ever eaten. And "\t\n" is the JOIN: goquery's
// Text() concatenates text nodes with no separator at all, so 58 of the 90 committed
// fixtures really do run words together across block boundaries ("Über
// RedcareNewsroom"), and a rendering that made those boundaries visible has to
// record that the page had nothing there. A bare newline is an ordinary boundary and
// folds to a space, which is also what keeps hand-written "...\n\n- Go" test content
// reading exactly as it always did.
//
// Order matters: prefixes and the JOIN are resolved BEFORE the bracket markers, so a
// marker that strips to nothing can never leave a tab beside a newline and forge a
// JOIN the page never had.
//
// Bracketed page text inside a marker is expected rather than assumed away -- four of
// the 90 fixtures link text like "[email protected]", "[pdf]" or "Fixed Term [12
// Months]" -- so the two markers that wrap page text tolerate one level of brackets
// in it.
//
// The residual, stated rather than hidden: page text that literally contains "](",
// "[img: ...]", "[input ...]", "[select ...]", "[textarea ...]" or "[button: ...]"
// is rewritten by the strip. There are zero occurrences across the 526 KB of page
// text in the 90 committed fixtures, the round-trip test is the standing check, and
// the rendering is off by default until it has been scored.
//
// Whitespace is not an optional half of that: the line structure a rendering
// introduces IS whitespace, so every run of it collapses to a single space and the
// ends are trimmed, matching the parser's normalizeWS exactly. A non-breaking space
// counts as whitespace and folds; a zero-width space does not and is page text.
// Bytes that are not valid UTF-8 pass through unchanged -- a mis-encoded page must
// reach SourceHash as the same bytes it always did, and PostingBody owns dropping
// them (see validUTF8).
//
// Idempotent -- FlattenedText(FlattenedText(s)) == FlattenedText(s) -- so a consumer
// handed already-flattened text (a captured Extract Gold Set row, a hand-written
// Content in a test) reads it unchanged.
//
// Pure: no model, no network, no database.
func FlattenedText(rendering string) string {
	// Fast path, and an exact one: every synthesized shape needs a "[", a tab, a
	// newline or a vertical tab, so text carrying none of the four is already flat.
	// It keeps today's parser output and every pre-rendering captured row on the
	// identical path they were on before the derivation existed.
	//
	// The vertical tab is not optional here. A rendering whose first block carries a
	// prefix has no separator in front of it -- a lone <h1> renders as "#\vTitle",
	// with no tab and no newline anywhere -- so a fast path blind to "\v" would fold
	// the prefix to a space and hand back "# Title".
	if !strings.ContainsAny(rendering, "[\t\n\v") {
		return strings.Join(strings.Fields(rendering), " ")
	}

	s := linePrefixPattern.ReplaceAllString(rendering, "")
	s = strings.ReplaceAll(s, joinMarker, "")
	s = controlMarkerPattern.ReplaceAllString(s, "")
	s = buttonMarkerPattern.ReplaceAllString(s, "$1")
	s = linkMarkerPattern.ReplaceAllString(s, "$1")
	return strings.Join(strings.Fields(s), " ")
}

// WrappableMarkerText reports whether page text can sit inside one of the two
// markers that WRAP page text -- [text](href) and [button: text] -- and still be
// given back by FlattenedText verbatim. The renderer asks before it commits to a
// marker, and writes the text bare when the answer is no (parser.wrapper).
//
// It lives here, beside the patterns, because the question is entirely about how
// the strip reads a marker; a copy of these rules in the renderer would be a second
// grammar free to drift from this one.
//
// Two ways page text makes a marker unreadable, both found reviewing #279 and both
// landing on the persisted path -- the raw marker and its href reach the Posting
// Body, the Corpus search index and SourceHash:
//
//	"See ] more"   [See ] more](/x)      linkMarkerPattern cannot match: the "]"
//	                                     closes the marker early, so the whole
//	                                     marker survives the strip, target included
//	"input your details"
//	               [input your details]  controlMarkerPattern claims it as a form
//	                                     control and deletes the page's own words
//
// The probe is "["+text+"]" for both wrappers, which is exactly right for a link
// and deliberately conservative for a button: "[button: input foo]" is in fact
// unambiguous, but declining it costs one button's marking and keeps one rule
// instead of two. Text always survives either way.
func WrappableMarkerText(text string) bool {
	if !wholeMarkedText.MatchString(text) {
		return false
	}
	probe := "[" + text + "]"
	return !controlMarkerPattern.MatchString(probe) && !buttonMarkerPattern.MatchString(probe)
}

// joinMarker is the rendering's "the page had no whitespace at this boundary" mark
// (parser.joinMarker). It is deleted rather than folded, which is the difference
// between "Über RedcareNewsroom" and a new SourceHash for 64% of the Corpus.
const joinMarker = "\t\n"

// markedText is page text inside a wrapping marker: anything but brackets, or one
// level of them. Link text really does carry brackets -- "[email protected]",
// "Fixed Term [12 Months]", "(1965) [pdf]" all appear in the committed fixtures --
// and a rule that stopped at the first "]" would leave the marker's own syntax in
// the Flattened Text.
const markedText = `(?:[^\[\]]|\[[^\[\]]*\])*`

var (
	// linePrefixPattern matches a heading level, a list bullet or a table-row bar at
	// the start of a rendered line. The trailing VERTICAL TAB is what makes it
	// unambiguous: the renderer writes "\v" to terminate a prefix and for nothing
	// else, and page text cannot contain one, so this rule has exactly one reading.
	//
	// It was anchored on a plain tab until #289, on the reasoning that page text can
	// hold "- " but never "-\t". That guarded the wrong side: joinMarker is "\t\n"
	// and a cell separator is "\t", so a page word that is exactly "-", "|" or "#"
	// at the start of a rendered line was followed by a synthesized tab and read as
	// a bullet -- deleting the page's own character and the boundary behind it.
	linePrefixPattern = regexp.MustCompile(`(?m)^(?:#{1,6}|-|\|)\v`)

	// controlMarkerPattern matches an image or a form control -- everything built
	// only from attributes, which today's flattened output carries none of. The ":"
	// or " " after the tag name keeps page text like "[inputs are welcome]" out of
	// it.
	controlMarkerPattern = regexp.MustCompile(`\[(?:img|input|select|textarea)(?:[: ][^\]]*)?\]`)

	// buttonMarkerPattern keeps a button's text: a <button>'s own words are page
	// text and have always been in the flattened output. An <input type="submit">
	// is NOT this shape -- its caption is an attribute and must vanish.
	buttonMarkerPattern = regexp.MustCompile(`\[button: (` + markedText + `)\]`)

	// linkMarkerPattern keeps a link's text and drops its target. It has two
	// readers with two replacements: FlattenedText keeps the text alone, and
	// WithoutLinkTargets keeps it still bracketed (see structural_rendering.go).
	// One grammar, so the two readings can never disagree about what a link is.
	linkMarkerPattern = regexp.MustCompile(`\[(` + markedText + `)\]\([^)]*\)`)

	// wholeMarkedText is markedText anchored over an entire string, which is what
	// WrappableMarkerText needs: not "does a marker appear somewhere" but "is all
	// of this text something a marker can hold".
	wholeMarkedText = regexp.MustCompile(`\A` + markedText + `\z`)

	// anyMarkerOrLink drives WithoutLinkTargets. The alternation is ordered, and
	// the order is the point: an attribute-only marker or a button is recognised
	// FIRST, so a link rule can never claim its brackets (see WithoutLinkTargets).
	anyMarkerOrLink = regexp.MustCompile(
		controlMarkerPattern.String() + `|` + buttonMarkerPattern.String() + `|` + linkMarkerPattern.String())
)
