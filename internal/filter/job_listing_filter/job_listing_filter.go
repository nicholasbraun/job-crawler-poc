// Package joblistingfilter implements logic to filter Content for relevant job postings
package joblistingfilter

import (
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
)

func TitleContains(checks ...filter.CheckFn[string]) filter.CheckFn[*crawler.Content] {
	checkFn := filter.Every(checks...)

	return func(c *crawler.Content) error {
		return checkFn(c.Title)
	}
}

// MainContentContains runs the keyword checks over the page's Flattened Text
// (ADR-0046) rather than the content field raw, so a keyword phrase split across a
// heading, a list item or a table row still matches once the parser renders structure.
// A check's word boundaries cannot span a newline, so reading the field raw would
// silently drop matches the moment the rendering lands.
func MainContentContains(checks ...filter.CheckFn[string]) filter.CheckFn[*crawler.Content] {
	checkFn := filter.Every(checks...)

	return func(c *crawler.Content) error {
		return checkFn(crawler.FlattenedText(c.MainContent))
	}
}
