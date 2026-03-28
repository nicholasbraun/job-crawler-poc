package joblistingfilter_test

import (
	"errors"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	joblistingfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job_listing_filter"
)

func TestTitleContains(t *testing.T) {
	checkFn := joblistingfilter.TitleContains(filter.Contains("developer", "engineer"))

	tests := []struct {
		name        string
		content     *crawler.Content
		expectedErr error
	}{
		{"match", &crawler.Content{
			Title: "Senior Software Engineer",
		}, filter.ErrPass},
		{"no match", &crawler.Content{
			Title: "Senior Something Else",
		}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFn(tt.content)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("expected %v, got: %v", tt.expectedErr, err)
			}
		})
	}
}

func TestMainContentContains(t *testing.T) {
	checkFn := joblistingfilter.MainContentContains(
		filter.Contains("apply", "requirements"),
		filter.Contains("golang"),
	)

	tests := []struct {
		name        string
		content     *crawler.Content
		expectedErr error
	}{
		{"match", &crawler.Content{
			MainContent: "apply for this golang role",
		}, filter.ErrPass},
		{"single match", &crawler.Content{
			MainContent: "only and article about golang",
		}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFn(tt.content)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("expected %v, got: %v", tt.expectedErr, err)
			}
		})
	}
}

func TestRelevanceFilterComposition(t *testing.T) {
	chainFn := filter.Chain(
		filter.Every(joblistingfilter.TitleContains(
			filter.Contains("developer", "engineer", "entwickler"),
			filter.Contains("golang", "go", "backend", "software"),
		),
			joblistingfilter.MainContentContains(
				filter.Contains("apply", "bewerben"),
				filter.Contains("golang", "go"),
				filter.Contains("experience"),
			),
		),
		filter.Reject[*crawler.Content](),
	)

	tests := []struct {
		name     string
		content  *crawler.Content
		wantPass bool
	}{
		{"should pass", &crawler.Content{
			Title:       "senior software engineer",
			MainContent: "you need 20 years of experience writing go. apply now",
		}, true},
		{"should not match", &crawler.Content{
			Title:       "Blog post about a software engineer",
			MainContent: "only and article about golang engineer with a lot of experience.",
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := chainFn(tt.content)
			if err != nil && tt.wantPass {
				t.Errorf("expected to pass, got error: %v", err)
			}
			if err == nil && !tt.wantPass {
				t.Errorf("expected not to pass, but got no error")
			}
		})
	}
}
