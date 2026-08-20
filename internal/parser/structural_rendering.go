package parser

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// This file is one half of a pair (ADR-0046). renderStructural writes a page's
// Structural Rendering; crawler.FlattenedText reads it back as the Flattened Text
// every non-model consumer sees. The two move together or not at all, and what
// proves they still agree is TestStructuralRenderingRoundTrip, which asserts
//
//	crawler.FlattenedText(renderStructural(region)) == normalizeWS(region.Text())
//
// byte for byte over the 90 committed classifier fixtures. SourceHash is persisted
// on ~46k job_listing rows, so a derivation that is merely close re-extracts the
// whole Corpus in one wave.
//
// Two properties keep that invariant provable rather than lucky:
//
//   - Duplicating whitespace is harmless (Fields collapses it); creating or
//     destroying it is fatal. A rendering therefore records whether the PAGE had
//     whitespace at each boundary it makes visible, and the derivation deletes the
//     separators that stand for "none" instead of collapsing them. 58 of the 90
//     fixtures run words together across block boundaries -- goquery's Text()
//     concatenates text nodes with no separator, so a minified <p>A</p><p>B</p>
//     really is "AB" today -- and a renderer that inserted a newline there and let
//     it collapse to a space would re-key 64% of the Corpus.
//   - Every character written is either a source non-whitespace character, one of
//     the separators below, or a marker built ONLY from attributes. A text node is
//     never dropped and never duplicated, which is why the markers may strip to
//     nothing: href, alt, value, type, placeholder, aria-label and name are not in
//     today's flattened output, so nothing built from them may survive the strip.
//     The only two markers that WRAP page text -- [button: ...] and [text](href) --
//     both give it back.
//
// Separators, decided lazily at the moment the next word or marker is written:
//
//	block boundary, page had whitespace        "\n"      -> one space
//	block boundary, page had none              "\t\n"    -> nothing (the JOIN)
//	table-cell boundary, page had whitespace   "\t"      -> one space
//	table-cell boundary, page had none         ""        -> nothing
//	no boundary, page had whitespace           " "       -> one space
//
// Line prefixes (#..###### for a heading level, - for a list item or option, | for
// a table row) are written after the newline as part of the same lazy flush, so an
// empty block contributes nothing at all. They end in a TAB rather than a space
// because source-derived text can never contain a tab -- every tab and newline in a
// rendering is synthesized -- so the strip's anchored prefix rule can never eat a
// paragraph whose own text starts with "- " or "# ".
//
// The one thing never written is a tab immediately before a newline other than the
// JOIN itself: a cell boundary owed when a block break flushes is dropped, or it
// would delete a boundary the page really had.
const (
	// joinMarker records a block boundary the page itself ran together. The strip
	// deletes it; a bare "\n" it leaves as whitespace.
	joinMarker = "\t\n"

	// markerMaxRunes caps attribute-derived marker text. A data: URI href or a
	// paragraph-length aria-label would otherwise dominate the rendering the human
	// labeller and the extractor read, and none of it is page text.
	markerMaxRunes = 80
)

// Renderer identifiers, stamped on every extract-capture record so a captured page
// states which renderer produced it (ADR-0046). A Structural Rendering is a DERIVED
// artefact -- the Extract Gold Set stores it as the page a labeller reads and the
// extractor saw -- so a renderer change has to show up as rows produced by two
// renderers rather than mixing silently inside one drawing (#281).
//
// BUMPING THE VERSION IS PART OF CHANGING THE GRAMMAR ABOVE, in the same commit: a
// new marker, a different separator, another block or cell tag, a changed
// markerMaxRunes -- anything that moves what renderStructural writes -- takes
// RendererStructural to the next -vN. TestStructuralRendererFingerprint makes that
// mechanical rather than remembered: it hashes the rendering of all 90 committed
// classifier fixtures and fails until both the hash and the version recorded beside
// it have moved.
//
// RendererFlattened has no bump in prospect: normalizeWS's output is frozen byte for
// byte by TestFlattenedTextGolden, because a different value reaching SourceHash
// silently re-extracts ~46k job_listing rows (ADR-0046). It is versioned anyway so
// the two identifiers read the same way, and so a change nobody expects has
// somewhere to be recorded.
const (
	RendererFlattened  = "flattened-v1"
	RendererStructural = "structural-v1"
)

// RendererID names the renderer this parser writes Content.MainContent with, so the
// extract-capture tap can attribute a captured page to it (#281). It is read off the
// parser INSTANCE rather than re-derived from the kill switch at the wiring site,
// which is what keeps the stamp from disagreeing with the bytes it describes.
func (p *HTMLParser) RendererID() string {
	if p.structuralRendering {
		return RendererStructural
	}
	return RendererFlattened
}

// blockTags are the elements whose boundaries start a line, mapped to the prefix
// that line carries. A prefix is set on enter and cleared on leave, so a <p> nested
// in an <li> keeps the bullet while the <div> after the list does not.
var blockTags = map[string]string{
	"address": "", "article": "", "aside": "", "blockquote": "", "br": "",
	"dd": "", "div": "", "dl": "", "dt": "", "fieldset": "", "figcaption": "",
	"figure": "", "footer": "", "form": "", "header": "", "hr": "", "main": "",
	"nav": "", "ol": "", "p": "", "pre": "", "section": "", "select": "",
	"table": "", "tbody": "", "textarea": "", "tfoot": "", "thead": "", "ul": "",

	"h1": "#\t", "h2": "##\t", "h3": "###\t", "h4": "####\t", "h5": "#####\t", "h6": "######\t",

	"li":     "-\t",
	"option": "-\t",
	"tr":     "|\t",
}

// cellTags are the elements that separate cells within a rendered table row,
// rather than starting a line of their own.
var cellTags = map[string]bool{"td": true, "th": true}

// renderStructural renders the parser's main region as a Structural Rendering.
// The region is chosen by mainRegion in both modes, so this never changes WHICH
// part of a page becomes content -- only how it is written down (ADR-0046).
func renderStructural(sel *goquery.Selection) string {
	r := &renderer{}
	for _, n := range sel.Nodes {
		r.node(n)
	}
	return r.buf.String()
}

// renderer accumulates a rendering. Every separator is owed rather than written:
// the flags say what the page put between what is already written and whatever
// comes next, and flush turns them into characters only once there is a next.
type renderer struct {
	buf strings.Builder

	pendingSpace  bool   // the page had whitespace at this boundary
	pendingBreak  bool   // a block boundary is owed
	pendingPrefix string // the line prefix that boundary carries
	pendingCell   bool   // a table-cell boundary is owed
	wrote         bool   // anything at all is in buf, so a separator has two sides

	// inWrapper counts the markers currently wrapping page text ([button: ...],
	// [text](href)). Nested wrappers are rendered as their text alone: a marker
	// inside another marker's brackets would make the strip order-dependent, and
	// the nesting only arises from invalid markup anyway.
	inWrapper int
}

// node renders one DOM node, mirroring exactly what goquery's Text() traverses:
// text nodes contribute, element nodes recurse, and anything else (comments, the
// document node) contributes nothing but its children.
func (r *renderer) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		r.text(n.Data)
	case html.ElementNode:
		r.element(n)
	default:
		r.children(n)
	}
}

func (r *renderer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.node(c)
	}
}

func (r *renderer) element(n *html.Node) {
	tag := n.Data
	prefix, isBlock := blockTags[tag]
	switch {
	case isBlock:
		r.openBlock(prefix)
	case cellTags[tag]:
		r.pendingCell = true
	}

	switch tag {
	case "img":
		// Void element: the alternative text is an attribute, so the marker must
		// strip to nothing -- today's flattened output carries no alt text. It is
		// spelled like the other attribute-only controls rather than markdown's
		// "![alt]": page text ending in "!" directly before a link or a button ran
		// the two together into a forged image marker on three of the 90 fixtures
		// ("...Vielfalt!" before a "Mehr lesen" button), and the strip then ate the
		// text the page really had.
		if alt := markerLabel(attr(n, "alt")); alt != "" {
			r.write("[img: " + alt + "]")
		}
	case "input":
		if marker := inputMarker(n); marker != "" {
			r.write(marker)
		}
	case "select", "textarea":
		// The marker sits BESIDE the control's text, never around it: <option> and
		// <textarea> text IS in today's output and has to survive the strip.
		r.write("[" + tag + labelSuffix(n) + "]")
		r.children(n)
	case "a":
		href := attr(n, "href")
		if href == "" || r.inWrapper > 0 {
			r.children(n)
			break
		}
		r.write("[")
		r.inWrapper++
		r.children(n)
		r.inWrapper--
		// Written raw: a trailing space inside the link is still owed to whatever
		// follows the marker, so the closing bracket must not consume it.
		r.writeRaw("](" + markerHref(href) + ")")
	case "button":
		if r.inWrapper > 0 {
			r.children(n)
			break
		}
		r.write("[button: ")
		r.inWrapper++
		r.children(n)
		r.inWrapper--
		r.writeRaw("]")
	default:
		r.children(n)
	}

	switch {
	case isBlock:
		r.closeBlock(prefix)
	case cellTags[tag]:
		r.pendingCell = true
	}
}

func (r *renderer) openBlock(prefix string) {
	r.pendingBreak = true
	if prefix != "" {
		r.pendingPrefix = prefix
	}
}

func (r *renderer) closeBlock(prefix string) {
	r.pendingBreak = true
	if prefix != "" {
		r.pendingPrefix = ""
	}
}

// text contributes a text node: its words verbatim, its whitespace as owed
// separators. Word splitting is strings.Fields' own, so the words are exactly the
// ones normalizeWS produces -- including across bytes that are not valid UTF-8,
// which must reach SourceHash unchanged.
func (r *renderer) text(s string) {
	if s == "" {
		return
	}
	body := strings.TrimLeftFunc(s, unicode.IsSpace)
	if len(body) != len(s) {
		r.pendingSpace = true
	}
	if body == "" {
		return
	}
	trimmed := strings.TrimRightFunc(body, unicode.IsSpace)
	trailing := len(trimmed) != len(body)

	for i, word := range strings.Fields(trimmed) {
		if i > 0 {
			r.pendingSpace = true
		}
		r.write(word)
	}
	if trailing {
		r.pendingSpace = true
	}
}

// write emits page text or a marker, paying whatever separator is owed first.
func (r *renderer) write(s string) {
	r.flush()
	r.buf.WriteString(s)
	r.wrote = true
}

// writeRaw closes a wrapping marker without paying a separator, leaving what the
// page owed to be paid after the marker instead of inside it.
func (r *renderer) writeRaw(s string) {
	r.buf.WriteString(s)
}

func (r *renderer) flush() {
	switch {
	case r.pendingBreak:
		if r.wrote {
			if r.pendingSpace {
				r.buf.WriteString("\n")
			} else {
				r.buf.WriteString(joinMarker)
			}
		}
		// A cell boundary owed at the same moment is dropped rather than written:
		// its tab would land immediately before the newline and read as a JOIN,
		// deleting a boundary the page really had.
		r.buf.WriteString(r.pendingPrefix)
	case r.pendingCell:
		if r.pendingSpace && r.wrote {
			r.buf.WriteString("\t")
		}
	case r.pendingSpace:
		if r.wrote {
			r.buf.WriteString(" ")
		}
	}
	r.pendingBreak, r.pendingCell, r.pendingSpace = false, false, false
	r.pendingPrefix = ""
}

// inputMarker renders one <input> as a control marker, or "" for the controls a
// reader never sees. The type is part of the marker because it is what tells a
// checkbox list of roles (ADR-0046's advertorial.de case) from an index of them.
func inputMarker(n *html.Node) string {
	kind := markerLabel(strings.ToLower(attr(n, "type")))
	if kind == "" {
		kind = "text" // HTML's own default
	}
	switch kind {
	case "hidden":
		// A CSRF token or tracking field: invisible on the page, noise in the
		// rendering, and its name carries nothing about the posting.
		return ""
	case "submit", "button", "reset":
		// The caption lives in value=, an attribute -- so unlike a <button>'s own
		// text it is NOT in today's flattened output and must strip to nothing.
		if value := markerLabel(attr(n, "value")); value != "" {
			return "[input " + kind + ": " + value + "]"
		}
		return "[input " + kind + "]"
	}
	return "[input " + kind + labelSuffix(n) + "]"
}

// labelSuffix names a control from its attributes, most human-readable first, or
// returns "" when the markup names it nowhere. The order is fixed so a rendering
// is reproducible from the same HTML.
func labelSuffix(n *html.Node) string {
	for _, key := range []string{"aria-label", "name", "placeholder", "id"} {
		if label := markerLabel(attr(n, key)); label != "" {
			return ": " + label
		}
	}
	return ""
}

// markerLabel sanitizes attribute text for use inside a marker. It is synthetic
// text, never page text, so this is free: whitespace collapses, brackets become
// parentheses so the strip's bracket rules cannot be confused by an attribute, and
// the result is capped.
func markerLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("[", "(", "]", ")").Replace(s)
	return truncateRunes(s, markerMaxRunes)
}

// markerHref sanitizes a link target. On top of markerLabel it percent-encodes the
// closing parenthesis, so the ")" that ends the marker is the only one in it.
func markerHref(s string) string {
	return strings.ReplaceAll(markerLabel(s), ")", "%29")
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
