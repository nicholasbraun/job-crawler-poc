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

func MainContentContains(checks ...filter.CheckFn[string]) filter.CheckFn[*crawler.Content] {
	checkFn := filter.Every(checks...)

	return func(c *crawler.Content) error {
		return checkFn(c.MainContent)
	}
}
