package openrouter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
)

// chatEnvelope mirrors the OpenAI-compatible chat-completions response the
// extractor decodes: the LLM's answer is a JSON string in choices[0].message.content.
type chatEnvelope struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// newExtractorServer stands up a server that always replies with content as the
// LLM message body, and returns an extractor pointed at it.
func newExtractorServer(t *testing.T, content string) *openrouter.JobListingExtractor {
	t.Helper()
	var env chatEnvelope
	env.Choices = make([]struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}, 1)
	env.Choices[0].Message.Content = content

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(env); err != nil {
			t.Errorf("encode envelope: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return openrouter.NewJobListingExtractor(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
}

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	return u
}

func TestExtractParsesVerdict(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantPosting  bool
		wantTitle    string
		wantEmptyish bool
	}{
		{
			name:        "true verdict",
			content:     `{"title":"Backend Engineer","company":"Acme","is_job_posting":true}`,
			wantPosting: true,
			wantTitle:   "Backend Engineer",
		},
		{
			name:         "false verdict leaves fields empty",
			content:      `{"is_job_posting":false}`,
			wantPosting:  false,
			wantTitle:    "",
			wantEmptyish: true,
		},
		{
			name:        "omitted field defaults to posting (recall-safe)",
			content:     `{"title":"X"}`,
			wantPosting: true,
			wantTitle:   "X",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := newExtractorServer(t, tc.content)
			raw := crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: crawler.Content{MainContent: "some page text"},
			}

			got, err := ext.Extract(t.Context(), raw)
			if err != nil {
				t.Fatalf("Extract returned error: %v", err)
			}

			if got.IsJobPosting != tc.wantPosting {
				t.Errorf("IsJobPosting = %v, want %v", got.IsJobPosting, tc.wantPosting)
			}
			if got.Listing.Title != tc.wantTitle {
				t.Errorf("Listing.Title = %q, want %q", got.Listing.Title, tc.wantTitle)
			}
			if got.Listing.URL != raw.URL.RawURL {
				t.Errorf("Listing.URL = %q, want %q", got.Listing.URL, raw.URL.RawURL)
			}
			if tc.wantEmptyish {
				if got.Listing.Company != "" || got.Listing.Location != "" || got.Listing.Remote || len(got.Listing.TechStack) != 0 {
					t.Errorf("want empty listing fields on abstain, got %+v", got.Listing)
				}
			}
		})
	}
}
