// Package parser is responsible for parsing raw HTML from the http downloader
// into Content.
package parser

import (
	"bytes"
	"fmt"

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
			text := selection.Text()
			return text
		}
	}

	return ""
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

// getJSONLD returns the raw text of every <script type="application/ld+json">
// block, in document order. Contents are not parsed or validated here.
func getJSONLD(doc *goquery.Document) []string {
	blocks := []string{}

	doc.Find(`script[type="application/ld+json"]`).Each(func(i int, s *goquery.Selection) {
		blocks = append(blocks, s.Text())
	})

	return blocks
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
