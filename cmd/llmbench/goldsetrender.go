// This file is the rendering the labelling tool puts beside a row's captured text
// (ADR-0047, #288): a Structural Rendering turned into a small self-contained HTML
// document, which the tool serves from its OWN loopback origin. The origin is never
// framed -- a site's framing headers would refuse it, and a browser running the site's
// own CSS and scripts would show a RICHER page than the crawler ever saw, because the
// crawler executes no JavaScript and what the Extract Gate read is the parse rather
// than the painted page.
//
// What is on screen is therefore the crawler's own parse, drawn as structure:
// headings, list items, table rows, link text WITH its target (ADR-0046 --
// "/organization/..." against "/jobs/..." is what settles an employer directory), and
// the form controls that make an apply form's role picker read as a picker instead of
// as an index of three roles.
//
// It is pure: a rendering and a page URL in, bytes out. Nothing here fetches, and
// nothing here decides WHETHER a row may be rendered -- goldsetui.go owns that, from
// the Capture Fidelity #287 measured.
package main

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// goldRenderStyle is the whole document's presentation. Inline, because the document
// is served under "default-src 'none'" and may not reach the network for anything --
// and because a rendering that needed a second request would be a rendering that can
// fail halfway.
const goldRenderStyle = `
:root { color-scheme: dark; --bg:#10131a; --ink:#e6e8ee; --dim:#9aa3b2; --edge:#2b303b; --key:#7aa2f7; }
* { box-sizing: border-box; }
body { margin:0; padding:1rem 1.1rem; background:var(--bg); color:var(--ink);
  font:15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
article { max-width:62rem; }
h1,h2,h3,h4,h5,h6 { line-height:1.25; margin:1.1rem 0 .5rem; }
h1 { font-size:1.6rem; } h2 { font-size:1.35rem; } h3 { font-size:1.15rem; }
h4,h5,h6 { font-size:1rem; }
p { margin:.55rem 0; }
ul { margin:.55rem 0; padding-left:1.4rem; list-style:disc; }
li { margin:.2rem 0; }
table { border-collapse:collapse; margin:.6rem 0; font-size:.92rem; }
td { border:1px solid var(--edge); padding:.2rem .5rem; vertical-align:top; }
a { color:var(--key); }
.target { font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size:.8rem; color:var(--dim); word-break:break-all; }
.link { color:var(--key); text-decoration:underline; }
.button { border:1px solid var(--edge); border-radius:4px; padding:0 .35rem; }
.control { border:1px dashed var(--edge); border-radius:4px; padding:0 .25rem;
  color:var(--dim); font-size:.85rem; }
.empty { color:var(--dim); font-style:italic; }
.refusal { border-left:3px solid #d0553a; padding-left:.7rem; color:#f0d6cf; }
`

// goldRenderDocument renders a Structural Rendering as the document GET /render/{id}
// serves. pageURL becomes the injected <base>, so a link target the page wrote
// relative ("/organization/123") resolves to the site it came from rather than to this
// tool.
//
// Every page-derived byte is escaped, and there is no <script> anywhere: the document
// is generated, so there is nothing in it to execute, and the frame's sandbox and the
// response's CSP are the wire-level statement of that.
func goldRenderDocument(rendering, pageURL string) []byte {
	var b strings.Builder
	goldRenderHead(&b, pageURL)
	b.WriteString("<article>")
	goldRenderBody(&b, crawler.ScanRendering(rendering))
	b.WriteString("</article></body></html>")
	return []byte(b.String())
}

// goldRenderRefusal is the document served in the frame's place when a row may not be
// rendered. It is HTML rather than JSON because the consumer is an iframe, and a JSON
// body in a frame is a download prompt rather than a sentence.
func goldRenderRefusal(msg string) []byte {
	var b strings.Builder
	goldRenderHead(&b, "")
	b.WriteString(`<article><p class="refusal">`)
	b.WriteString(html.EscapeString(msg))
	b.WriteString("</p></article></body></html>")
	return []byte(b.String())
}

// goldRenderHead writes the document up to <body>, injecting the page's own <base>
// when it has one that a browser may resolve against.
func goldRenderHead(b *strings.Builder, pageURL string) {
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="referrer" content="no-referrer">`)
	if base := goldRenderBase(pageURL); base != "" {
		// target="_blank" on the base: a click inside the frame opens a real tab rather
		// than replacing the rendering with the live site inside the labelling surface.
		fmt.Fprintf(b, `<base href="%s" target="_blank">`, html.EscapeString(base))
	}
	b.WriteString("<style>" + goldRenderStyle + "</style></head><body>")
}

// goldRenderBody writes the scanned lines as blocks. Consecutive items become one
// list and consecutive rows one table, because a list drawn as fifteen one-item lists
// is a list a labeller cannot count.
func goldRenderBody(b *strings.Builder, lines []crawler.RenderingLine) {
	drawn := 0
	for i := 0; i < len(lines); {
		line := lines[i]
		if goldRenderLineEmpty(line) {
			// A rendering does not emit empty blocks; a line whose only piece was a
			// straddling marker's syntax is real, and an empty <p> for it is clutter.
			i++
			continue
		}
		drawn++
		switch line.Block {
		case crawler.RenderingItem:
			i = goldRenderGroup(b, lines, i, crawler.RenderingItem, "ul", "li", "")
		case crawler.RenderingRow:
			// A table row is the one block whose cells are the page's own, so it is the
			// one that draws them.
			i = goldRenderGroup(b, lines, i, crawler.RenderingRow, "table", "tr", "td")
		case crawler.RenderingHeading:
			tag := fmt.Sprintf("h%d", min(max(line.Level, 1), 6))
			b.WriteString("<" + tag + ">")
			goldRenderCells(b, line, "")
			b.WriteString("</" + tag + ">")
			i++
		default:
			b.WriteString("<p>")
			goldRenderCells(b, line, "")
			b.WriteString("</p>")
			i++
		}
	}
	if drawn == 0 {
		// The JS-shell case ADR-0047 names: the crawler runs no JavaScript, so a shell
		// renders empty here exactly as it did for the gate. Saying so is the point --
		// an empty frame with no sentence in it reads as a broken tool.
		b.WriteString(`<p class="empty">This page carried no text at all -- the crawler's parse of it is empty.</p>`)
	}
}

// goldRenderGroup writes one run of consecutive lines of the same block as a single
// container, and returns the index it stopped at.
func goldRenderGroup(b *strings.Builder, lines []crawler.RenderingLine, i int, block crawler.RenderingBlock, container, item, cellTag string) int {
	b.WriteString("<" + container + ">")
	for ; i < len(lines) && lines[i].Block == block; i++ {
		if goldRenderLineEmpty(lines[i]) {
			continue
		}
		b.WriteString("<" + item + ">")
		goldRenderCells(b, lines[i], cellTag)
		b.WriteString("</" + item + ">")
	}
	b.WriteString("</" + container + ">")
	return i
}

// goldRenderCells writes a line's cells. cellTag is empty for every block but a table
// row, which is the only one whose cells are the page's own.
func goldRenderCells(b *strings.Builder, line crawler.RenderingLine, cellTag string) {
	for _, cell := range line.Cells {
		if cellTag != "" {
			b.WriteString("<" + cellTag + ">")
		}
		for _, piece := range cell {
			goldRenderPiece(b, piece)
		}
		if cellTag != "" {
			b.WriteString("</" + cellTag + ">")
		}
	}
}

// goldRenderPiece writes one piece. A link's TARGET is always shown beside its text,
// whether or not the click is offered: the target is what settles a page like an
// employer directory, and withholding it would take away the very thing this rendering
// exists to put on screen.
func goldRenderPiece(b *strings.Builder, piece crawler.RenderingPiece) {
	switch piece.Mark {
	case crawler.RenderingLinkMark:
		text := html.EscapeString(piece.Text)
		if strings.TrimSpace(piece.Text) == "" {
			// A link with no text -- an icon, an empty anchor. Its target is all it has,
			// so the target is what stands in for the text rather than nothing at all.
			text = html.EscapeString(piece.Target)
		}
		if href := goldRenderHref(piece.Target); href != "" {
			fmt.Fprintf(b, `<a href="%s" rel="noreferrer noopener" target="_blank">%s</a>`, html.EscapeString(href), text)
		} else {
			// Only the CLICK is withheld: a "javascript:" href off a crawled page must
			// never become a click target, and the page still gets to say what it wrote.
			fmt.Fprintf(b, `<span class="link">%s</span>`, text)
		}
		fmt.Fprintf(b, ` <span class="target">(%s)</span>`, html.EscapeString(piece.Target))
	case crawler.RenderingButtonMark:
		fmt.Fprintf(b, `<span class="button">%s</span>`, html.EscapeString(piece.Text))
	case crawler.RenderingControlMark:
		fmt.Fprintf(b, `<span class="control">%s</span>`, html.EscapeString(piece.Label))
	default:
		b.WriteString(html.EscapeString(piece.Text))
	}
}

// goldRenderLineEmpty reports that a line has nothing to draw: no page words and no
// marker. It is not the same as "no text" -- a line holding only a form control is
// worth drawing, because the control is exactly what tells a picker from an index.
func goldRenderLineEmpty(line crawler.RenderingLine) bool {
	for _, cell := range line.Cells {
		for _, piece := range cell {
			if piece.Mark != crawler.RenderingTextMark || strings.TrimSpace(piece.Text) != "" {
				return false
			}
		}
	}
	return true
}

// goldRenderBase returns the reference a relative link target resolves against, or ""
// when the row's URL is not one a browser could resolve against. It is STRICTER than
// goldRenderHref: a base has to be absolute and http(s), because a relative base would
// resolve "/organization/123" against this tool rather than against the site the page
// came from -- which is the one thing the reference exists to prevent.
func goldRenderBase(pageURL string) string {
	trimmed := strings.TrimSpace(pageURL)
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return trimmed
}

// goldRenderHref returns the href a target may be given, or "" when it may never
// become a click target. Only http(s), scheme-relative and path-relative targets
// qualify -- the same rule the labelling page's own safeURL applies to a row URL,
// because a "javascript:" href off a crawled page is a crawled page's text and not a
// destination.
//
// A fragment is refused for a different reason: inside a sandboxed frame it navigates
// nowhere a labeller can see, so it is a click that does nothing.
func goldRenderHref(target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return trimmed
}
