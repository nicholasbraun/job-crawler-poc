// Package parser is responsible for parsing raw HTML from the http downloader
// into Content.
package parser

import (
	"bytes"
	"fmt"

	"github.com/PuerkitoBio/goquery"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

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
			html, err := selection.Html()

			if err == nil {
				return html
			}
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
