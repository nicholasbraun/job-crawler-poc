// Package parser is responsible for parsing raw HTML from the http downloader
// into Content.
package parser

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// Parser extracts structured content (title, main text, links) from raw HTML.
type Parser interface {
	Parse(b []byte) (*crawler.Content, error)
}

var _ Parser = &HTMLParser{}

// DefaultStructuralRendering is the parser's shipped behavior: the Flattened Text
// it has always produced (ADR-0046). Owned here, so cmd/server's knob and the code
// it configures cannot drift apart (ADR-0045).
const DefaultStructuralRendering = false

type HTMLParser struct {
	// structuralRendering makes MainContent a Structural Rendering instead of one
	// flat run of words. Off by default: rendering walks the DOM a second time on
	// every page the Discovery Crawl fetches, and every consumer reads the same
	// bytes either way (crawler.FlattenedText), so nothing downstream moves.
	structuralRendering bool
}

// HTMLParserOption configures an HTMLParser.
type HTMLParserOption func(*HTMLParser)

// WithStructuralRendering turns the Structural Rendering on or off (ADR-0046).
// RendererID (structural_rendering.go) names the renderer this switch selects, so
// a captured page can say which one produced it (#281).
func WithStructuralRendering(on bool) HTMLParserOption {
	return func(p *HTMLParser) { p.structuralRendering = on }
}

func (p *HTMLParser) Parse(b []byte) (*crawler.Content, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("error parsing reader %w ", err)
	}

	content := &crawler.Content{
		Title:       getTitle(doc),
		MainContent: p.mainContent(doc),
		URLs:        getUrls(doc),
		JSONLD:      getJSONLD(doc),
		SiteName:    getSiteName(doc),
		Embeds:      getEmbeds(doc),
		ElementIDs:  getElementIDs(doc),
	}
	return content, nil
}

func NewHTMLParser(opts ...HTMLParserOption) *HTMLParser {
	p := &HTMLParser{structuralRendering: DefaultStructuralRendering}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// mainContent renders the page's main region. Both modes take the SAME region from
// mainRegion and differ only in how it is written down, so turning the Structural
// Rendering on can never change which part of a page becomes content (ADR-0046).
// With it off there is no second walk and no rendering built and thrown away: the
// parser costs exactly what it cost before.
func (p *HTMLParser) mainContent(doc *goquery.Document) string {
	sel := mainRegion(doc)
	if sel == nil {
		return ""
	}
	if p.structuralRendering {
		return renderStructural(sel)
	}
	return normalizeWS(sel.Text())
}

// mainRegion returns the cloned, script-stripped region the parser has always
// picked, or nil for a document with no body. The chrome decision is taken on the
// region's plain text in BOTH modes: deciding "did stripping chrome empty this
// page?" on a rendering would let a page of images and form controls keep furniture
// that today it drops.
func mainRegion(doc *goquery.Document) *goquery.Selection {
	// A page that declares a semantic container has already told us where its content
	// is, so that region is taken as-is. Chrome INSIDE it is left alone deliberately:
	// on a compact career page the surrounding nav often carries the load-bearing
	// signal ("Open positions", "Career at X"), and removing it flipped a real Career
	// Page to a false verdict when #270 first stripped unconditionally.
	semantic := []goquery.Matcher{
		goquery.Single("main"),
		goquery.Single("div[role=main]"),
		goquery.Single("div#content"),
		goquery.Single("article"),
	}
	for _, m := range semantic {
		selection := doc.FindMatcher(m)
		if selection.Length() == 1 {
			// Clone before stripping: Remove detaches nodes from the shared doc,
			// which would delete the ld+json <script> blocks getJSONLD reads next.
			return stripNonContent(selection.Clone())
		}
	}

	// No semantic container: everything, chrome included, would otherwise become
	// content. This is the only case where site furniture is dropped (#270).
	if body := doc.FindMatcher(goquery.Single("body")); body.Length() == 1 {
		return withoutChrome(body)
	}

	return nil
}

// stripNonContent removes the elements whose text is never page content — scripts,
// styles, noscript fallbacks, inline SVG and inert templates — from a CLONE, and
// returns it. Callers must clone first: Remove detaches nodes from the shared
// document, which would delete the ld+json blocks getJSONLD reads next.
func stripNonContent(clone *goquery.Selection) *goquery.Selection {
	clone.Find("script, style, noscript, svg, template").Remove()
	return clone
}

// withoutChrome returns the page's <body> as a stripped clone with the site
// furniture — nav, header, footer, aside — dropped (#270). Callers with a semantic
// container must not use it; see mainRegion for why.
//
// A page with no semantic container otherwise contributes its whole navigation menu as
// content. A site that lists its openings in a global nav then puts a job-openings list
// on EVERY page, which measurably breaks three consumers at once: the career-page
// classifier reads a cheese shop's contact page as a careers hub, the extractor is handed
// a menu instead of a posting, and the Corpus stores and indexes the menu as a Posting
// Body (ADR-0041).
//
// A page whose only text lives inside that furniture keeps it, rather than being reduced
// to nothing: dropping to empty would lose the page entirely at the Extract Gate, which is
// a worse failure than a noisy body. So this never returns less than the unstripped text
// would have.
func withoutChrome(body *goquery.Selection) *goquery.Selection {
	clone := stripNonContent(body.Clone())
	chrome := clone.Find("nav, header, footer, aside")
	if chrome.Length() == 0 {
		return clone
	}
	chrome.Remove()
	if normalizeWS(clone.Text()) != "" {
		return clone
	}
	// Rare: the page's only text lives in its furniture, so the unstripped clone is
	// rebuilt here rather than kept alongside the stripped one on every page.
	return stripNonContent(body.Clone())
}

// normalizeWS collapses every run of whitespace (including the newlines and tabs
// that block elements emit) to a single space and trims the ends. Page layout is
// irrelevant to the downstream LLM, so this trades it for a denser, smaller input.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func getUrls(doc *goquery.Document) []string {
	urls := []string{}

	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		if url, exists := s.Attr("href"); exists {
			// Trim surrounding whitespace/newlines: a padded href fails url.Parse
			// ("first path segment ... cannot contain colon") downstream, so the
			// link would be dropped and ERROR-logged instead of enqueued.
			if url = strings.TrimSpace(url); url != "" {
				urls = append(urls, url)
			}
		}
	})

	return urls
}

// getEmbeds returns every <iframe> and <script> that carries a src, tagged by
// element kind — all iframes first, then all scripts. These are the page's
// third-party board embed candidates; the Gate filters them to known ATS hosts.
// Kept out of getUrls (the frontier link set) so an embed/tracker/CDN src is
// never enqueued.
func getEmbeds(doc *goquery.Document) []crawler.Embed {
	embeds := []crawler.Embed{}
	doc.Find("iframe[src]").Each(func(i int, s *goquery.Selection) {
		// Trim surrounding whitespace: a padded src fails url.Parse in the Gate's
		// embedHost, silently dropping an otherwise-matching ATS board host.
		if src, ok := s.Attr("src"); ok {
			if src = strings.TrimSpace(src); src != "" {
				embeds = append(embeds, crawler.Embed{Src: src, IsFrame: true})
			}
		}
	})
	doc.Find("script[src]").Each(func(i int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok {
			if src = strings.TrimSpace(src); src != "" {
				embeds = append(embeds, crawler.Embed{Src: src, IsFrame: false})
			}
		}
	})
	return embeds
}

// getElementIDs returns the id attribute of every element that has one, in
// document order. The Gate's ATS-embed signal checks these for a provider's
// board-container marker (e.g. Greenhouse "grnhse_app").
func getElementIDs(doc *goquery.Document) []string {
	ids := []string{}
	doc.Find("[id]").Each(func(i int, s *goquery.Selection) {
		if id, ok := s.Attr("id"); ok && id != "" {
			ids = append(ids, id)
		}
	})
	return ids
}

// getJSONLD returns the raw text of every <script type="application/ld+json">
// block, in document order. Contents are not parsed or validated here.
func getJSONLD(doc *goquery.Document) []string {
	blocks := []string{}

	doc.Find(`script[type="application/ld+json"]`).Each(func(i int, s *goquery.Selection) {
		blocks = append(blocks, s.Text())
	})

	return blocks
}

// getSiteName returns the page's og:site_name meta content, or "" when absent.
// The Name Ladder's metadata rung (ADR-0025) reads it for a self-hosted Company;
// other pipelines ignore it. Only surrounding whitespace is trimmed here; full
// normalization is left to the consumer (metaName), matching getTitle's raw-text
// behavior.
func getSiteName(doc *goquery.Document) string {
	sel := doc.FindMatcher(goquery.Single(`meta[property="og:site_name"]`))
	if sel.Length() == 1 {
		if content, ok := sel.Attr("content"); ok {
			return strings.TrimSpace(content)
		}
	}
	return ""
}

func getTitle(doc *goquery.Document) string {
	matchers := []goquery.Matcher{
		goquery.Single("title"),
		goquery.Single("h1"),
	}

	for _, m := range matchers {
		selection := doc.FindMatcher(m)
		if selection.Length() == 1 {
			return selection.Text()
		}
	}

	return ""
}
