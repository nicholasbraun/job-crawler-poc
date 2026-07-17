package openrouter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{
			// tech_stack was dropped from the struct (ADR-0023); a stray key the
			// model still emits must be ignored, not error the decode.
			name:        "stray tech_stack key is ignored",
			content:     `{"title":"Backend Engineer","tech_stack":["Go"],"is_job_posting":true}`,
			wantPosting: true,
			wantTitle:   "Backend Engineer",
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
				if got.Listing.Company != "" || got.Listing.Location != "" || got.Listing.Remote {
					t.Errorf("want empty listing fields on abstain, got %+v", got.Listing)
				}
			}
		})
	}
}

// TestExtractPromptOmitsTechStack asserts the request the extractor sends the LLM
// no longer mentions tech_stack anywhere -- neither the system-prompt field list
// nor the closing "leave every other field empty" instruction (ADR-0023). The
// shared newExtractorServer discards the request, so this uses a local server
// that records the raw request body.
func TestExtractPromptOmitsTechStack(t *testing.T) {
	var captured string

	var env chatEnvelope
	env.Choices = make([]struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}, 1)
	env.Choices[0].Message.Content = `{"title":"X","is_job_posting":true}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured = string(body)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(env); err != nil {
			t.Errorf("encode envelope: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	ext := openrouter.NewJobListingExtractor(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
	raw := crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "some page text"},
	}

	if _, err := ext.Extract(t.Context(), raw); err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if strings.Contains(captured, "tech_stack") {
		t.Errorf("extractor request should not mention tech_stack, got:\n%s", captured)
	}
}
