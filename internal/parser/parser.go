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

type HTMLParser struct{}

func (p *HTMLParser) Parse(b []byte) (*crawler.Content, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("error parsing reader %w ", err)
	}

	content := &crawler.Content{
		Title:       getTitle(doc),
		MainContent: getMainContent(doc),
		URLs:        getUrls(doc),
		JSONLD:      getJSONLD(doc),
		SiteName:    getSiteName(doc),
		Embeds:      getEmbeds(doc),
		ElementIDs:  getElementIDs(doc),
	}
	return content, nil
}

func NewHTMLParser() *HTMLParser {
	return &HTMLParser{}
}

func getMainContent(doc *goquery.Document) string {
	matchers := []goquery.Matcher{
		goquery.Single("main"),
		goquery.Single("div[role=main]"),
		goquery.Single("div#content"),
		goquery.Single("article"),
		goquery.Single("body"),
	}

	for _, m := range matchers {
		selection := doc.FindMatcher(m)
		if selection.Length() == 1 {
			// Clone before stripping: Remove detaches nodes from the shared doc,
			// which would delete the ld+json <script> blocks getJSONLD reads next.
			clone := selection.Clone()
			clone.Find("script, style, noscript, svg, template").Remove()
			return normalizeWS(clone.Text())
		}
	}

	return ""
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
		url, exists := s.Attr("href")
		if exists {
			urls = append(urls, url)
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
		if src, ok := s.Attr("src"); ok {
			embeds = append(embeds, crawler.Embed{Src: src, IsFrame: true})
		}
	})
	doc.Find("script[src]").Each(func(i int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok {
			embeds = append(embeds, crawler.Embed{Src: src, IsFrame: false})
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
