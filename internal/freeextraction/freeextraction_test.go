package freeextraction_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/freeextraction"
)

// spyExtractor stands in for the model-backed extractor: it records whether the
// decorator delegated, and with what, so "the model was never called" is assertable
// without a model.
type spyExtractor struct {
	calls   int
	lastRaw crawler.RawJobListing
	result  crawler.Extraction
	err     error
}

func (s *spyExtractor) Extract(_ context.Context, raw crawler.RawJobListing) (crawler.Extraction, error) {
	s.calls++
	s.lastRaw = raw
	return s.result, s.err
}

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	return u
}

// newSpy returns a spy whose extraction is recognizably the MODEL's, so a delegated
// page's result can be told apart from a Free Extraction's by its title alone.
func newSpy() *spyExtractor {
	return &spyExtractor{result: crawler.Extraction{
		Listing:      crawler.JobListing{Title: "model-extracted"},
		IsJobPosting: true,
	}}
}

// TestExtractorFiresOnUnambiguousStructuredData asserts the headline behaviour: a
// page whose structured data publishes exactly one titled posting is resolved from
// that data, with the wrapped extractor never called.
func TestExtractorFiresOnUnambiguousStructuredData(t *testing.T) {
	tests := []struct {
		name            string
		jsonld          string
		wantTitle       string
		wantLocation    string
		wantArrangement crawler.WorkArrangement
	}{
		{
			name: "lone posting yields the page's declared fields",
			jsonld: `{"@type":"JobPosting","title":"Backend Engineer",
				"jobLocation":{"@type":"Place","address":{"addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"DE"}}}`,
			wantTitle:    "Backend Engineer",
			wantLocation: "Berlin, Berlin, DE",
			// Nothing declared: never guessed Onsite (ADR-0030).
			wantArrangement: crawler.WorkArrangementUnspecified,
		},
		{
			name: "a remote marker is tagged Remote",
			jsonld: `{"@type":"JobPosting","title":"Backend Engineer","jobLocationType":"TELECOMMUTE",
				"jobLocation":{"address":{"addressLocality":"Berlin","addressCountry":"DE"}}}`,
			wantTitle:       "Backend Engineer",
			wantLocation:    "Berlin, DE",
			wantArrangement: crawler.WorkArrangementRemote,
		},
		{
			name: "a posting nested in @graph beside an Organization",
			jsonld: `{"@context":"https://schema.org","@graph":[
				{"@type":"Organization","name":"Acme"},
				{"@type":"JobPosting","title":"Data Engineer","jobLocation":{"address":{"addressLocality":"Hamburg"}}}]}`,
			wantTitle:       "Data Engineer",
			wantLocation:    "Hamburg",
			wantArrangement: crawler.WorkArrangementUnspecified,
		},
		{
			name:            "a posting with no address has an empty Location",
			jsonld:          `{"@type":"JobPosting","title":"Platform Engineer"}`,
			wantTitle:       "Platform Engineer",
			wantLocation:    "",
			wantArrangement: crawler.WorkArrangementUnspecified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newSpy()
			raw := crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: crawler.Content{MainContent: "site chrome and nav", JSONLD: []string{tt.jsonld}},
			}

			got, err := freeextraction.NewExtractor(spy).Extract(t.Context(), raw)
			if err != nil {
				t.Fatalf("Extract returned error: %v", err)
			}

			if spy.calls != 0 {
				t.Errorf("wrapped extractor called %d times, want 0 (no model call)", spy.calls)
			}
			if !got.Free {
				t.Error("Free = false, want true (the accounting marker the save processor reads)")
			}
			if !got.IsJobPosting {
				t.Error("IsJobPosting = false, want true")
			}
			if got.Listing.URL != raw.URL.RawURL {
				t.Errorf("URL = %q, want the source page %q", got.Listing.URL, raw.URL.RawURL)
			}
			if got.Listing.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Listing.Title, tt.wantTitle)
			}
			if got.Listing.Location != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got.Listing.Location, tt.wantLocation)
			}
			if got.Listing.WorkArrangement != tt.wantArrangement {
				t.Errorf("WorkArrangement = %q, want %q", got.Listing.WorkArrangement, tt.wantArrangement)
			}
		})
	}
}

// TestExtractorDelegates asserts the delegation boundary: everything that is not an
// unambiguous, titled lone posting reaches the wrapped extractor unchanged and comes
// back verbatim, unmarked. Presence of structured data is not the signal;
// unambiguity is (ADR-0042).
func TestExtractorDelegates(t *testing.T) {
	tests := []struct {
		name    string
		content crawler.Content
	}{
		{
			name: "two posting nodes",
			content: crawler.Content{JSONLD: []string{
				`{"@type":"JobPosting","title":"Backend Engineer"}`,
				`{"@type":"JobPosting","title":"Frontend Engineer"}`,
			}},
		},
		{
			name: "an ItemList wrapping one posting",
			content: crawler.Content{JSONLD: []string{
				`{"@type":"ItemList","itemListElement":[{"@type":"ListItem","item":{"@type":"JobPosting","title":"Backend Engineer"}}]}`,
			}},
		},
		{
			name:    "a title-less posting node",
			content: crawler.Content{JSONLD: []string{`{"@type":"JobPosting","description":"We are hiring."}`}},
		},
		{
			name:    "a whitespace-only title",
			content: crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"   \n  "}`}},
		},
		{
			name:    "an unparseable block",
			content: crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":`}},
		},
		{
			name:    "no structured data at all",
			content: crawler.Content{MainContent: "we are hiring a backend engineer"},
		},
		{
			name:    "structured data with no posting node",
			content: crawler.Content{JSONLD: []string{`{"@type":"Organization","name":"Acme"}`}},
		},
		{
			// A filled or expired ad still served in full as structured data, above a
			// body that is only a notice. Saving it would put a job that does not exist
			// into the Corpus with no model in the loop, and the refetch lane could never
			// close it: the page keeps serving this same body, so it reads as unchanged
			// forever. The model reads the notice and abstains.
			name: "a withdrawal notice under a full declared ad",
			content: crawler.Content{
				MainContent: "Operations Team Lead - NYC This position is no longer active. " +
					"Either the position was filled, or the ad has expired.",
				JSONLD: []string{`{"@type":"JobPosting","title":"Operations Team Lead - NYC",
					"description":"We are hiring an Operations Team Lead for our New York office. You will own the daily running of the site, lead a team of five, report to the GM, and design the processes the region scales on. We offer a competitive salary, equity, health cover, and a hybrid schedule out of Manhattan. Previous operations leadership experience in a high-growth environment is required."}`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newSpy()
			raw := crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs"),
				Content: tt.content,
			}

			got, err := freeextraction.NewExtractor(spy).Extract(t.Context(), raw)
			if err != nil {
				t.Fatalf("Extract returned error: %v", err)
			}

			if spy.calls != 1 {
				t.Fatalf("wrapped extractor called %d times, want 1 (the page must reach the model)", spy.calls)
			}
			if spy.lastRaw.URL.RawURL != raw.URL.RawURL {
				t.Errorf("delegated URL = %q, want the workload's %q unchanged", spy.lastRaw.URL.RawURL, raw.URL.RawURL)
			}
			if got.Listing.Title != "model-extracted" {
				t.Errorf("Title = %q, want the wrapped extractor's result verbatim", got.Listing.Title)
			}
			if got.Free {
				t.Error("Free = true on a delegated page, want false (a model call was made)")
			}
		})
	}
}

// TestExtractorReturnsJudgmentFieldsOnly pins that the free path emits judgment
// fields and nothing else: the Posting Body, its Description Source and the
// extraction-cache key are the save processor's to stamp for BOTH paths (ADR-0041 /
// ADR-0035), and Company is attributed from the run's Catalog snapshot (ADR-0021).
// Emitting any of them here is what would make the two paths' cache keys differ.
func TestExtractorReturnsJudgmentFieldsOnly(t *testing.T) {
	raw := crawler.RawJobListing{
		URL: newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{
			MainContent: "site chrome and nav",
			JSONLD: []string{`{"@type":"JobPosting","title":"Backend Engineer","description":"` +
				strings.Repeat("a very long posting body. ", 200) + `","hiringOrganization":{"@type":"Organization","name":"Acme"}}`},
		},
	}

	got, err := freeextraction.NewExtractor(newSpy()).Extract(t.Context(), raw)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if got.Listing.Description != "" {
		t.Errorf("Description = %q, want empty (the save processor derives the Posting Body)", got.Listing.Description)
	}
	if got.Listing.DescriptionSource != "" {
		t.Errorf("DescriptionSource = %q, want empty (stamped at save for both paths)", got.Listing.DescriptionSource)
	}
	if got.Listing.SourceHash != "" {
		t.Errorf("SourceHash = %q, want empty (the save processor stamps the extraction-cache key)", got.Listing.SourceHash)
	}
	if got.Listing.Company != "" {
		t.Errorf("Company = %q, want empty (attributed from the Catalog snapshot at save)", got.Listing.Company)
	}
}

// TestExtractorPropagatesInnerError asserts a delegated page's failure is the
// wrapped extractor's, verbatim: the decorator neither swallows it nor invents a
// Free Extraction to paper over it.
func TestExtractorPropagatesInnerError(t *testing.T) {
	wantErr := errors.New("openrouter: status 500: oops")
	spy := &spyExtractor{err: wantErr}
	raw := crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs"),
		Content: crawler.Content{MainContent: "browse our open roles"},
	}

	got, err := freeextraction.NewExtractor(spy).Extract(t.Context(), raw)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Extract error = %v, want %v", err, wantErr)
	}
	if got != (crawler.Extraction{}) {
		t.Errorf("Extraction = %+v, want the zero value on the inner error", got)
	}
}
