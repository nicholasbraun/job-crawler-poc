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
		{
			// A reasoning model may prepend a <think> block despite the non-reasoning
			// prompt; it is stripped before decode.
			name:        "leading think block is stripped before decode",
			content:     "<think>weighing whether this is one role</think>\n{\"title\":\"Backend Engineer\",\"is_job_posting\":true}",
			wantPosting: true,
			wantTitle:   "Backend Engineer",
		},
		{
			// A server that ignores response_format may wrap the object in prose; the
			// outermost-brace fallback salvages it.
			name:        "prose-wrapped json salvaged by brace fallback",
			content:     "Here you go: {\"title\":\"Backend Engineer\",\"is_job_posting\":true} — done.",
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
				if got.Listing.Company != "" || got.Listing.Location != "" ||
					got.Listing.WorkArrangement != crawler.WorkArrangementUnspecified {
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

// TestExtractParsesWorkArrangement asserts the LLM's work_arrangement string is
// unmarshaled and folded onto the enum (ADR-0030): the four canonical values pass
// through, case/separator variants normalize, and an off-enum or omitted value
// degrades to Unspecified — never Onsite.
func TestExtractParsesWorkArrangement(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    crawler.WorkArrangement
	}{
		{"remote", `{"title":"X","work_arrangement":"remote","is_job_posting":true}`, crawler.WorkArrangementRemote},
		{"onsite", `{"title":"X","work_arrangement":"onsite","is_job_posting":true}`, crawler.WorkArrangementOnsite},
		{"hybrid", `{"title":"X","work_arrangement":"hybrid","is_job_posting":true}`, crawler.WorkArrangementHybrid},
		{"unspecified", `{"title":"X","work_arrangement":"unspecified","is_job_posting":true}`, crawler.WorkArrangementUnspecified},
		{"uppercase folds", `{"title":"X","work_arrangement":"REMOTE","is_job_posting":true}`, crawler.WorkArrangementRemote},
		{"separator folds on-site", `{"title":"X","work_arrangement":"on-site","is_job_posting":true}`, crawler.WorkArrangementOnsite},
		{"off-enum degrades to unspecified", `{"title":"X","work_arrangement":"office","is_job_posting":true}`, crawler.WorkArrangementUnspecified},
		{"omitted degrades to unspecified", `{"title":"X","is_job_posting":true}`, crawler.WorkArrangementUnspecified},
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
			if got.Listing.WorkArrangement != tc.want {
				t.Errorf("WorkArrangement = %q, want %q", got.Listing.WorkArrangement, tc.want)
			}
		})
	}
}

// TestExtractPromptMentionsWorkArrangement asserts the request the extractor sends
// names work_arrangement (the enum field) and no longer carries the old "remote"
// boolean bullet. It uses a local server that records the raw request body.
func TestExtractPromptMentionsWorkArrangement(t *testing.T) {
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

	if !strings.Contains(captured, "work_arrangement") {
		t.Errorf("extractor request should name work_arrangement, got:\n%s", captured)
	}
	// The old boolean bullet ("remote": if this job is available remotely) must be
	// gone; a bare mention of the word "remote" as an enum value is expected.
	if strings.Contains(captured, "available remotely") {
		t.Errorf("extractor request should not carry the old remote boolean bullet, got:\n%s", captured)
	}
}

// TestExtractPromptNudgesCountryName asserts the location bullet nudges the model
// to name the country in the location text and forbids emitting a code, keeping the
// deterministic resolver the sole normalization authority (ADR-0029). The match is
// deliberately loose to avoid coupling to the prompt's exact wording.
func TestExtractPromptNudgesCountryName(t *testing.T) {
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

	lower := strings.ToLower(captured)
	if !strings.Contains(lower, "country") {
		t.Errorf("extractor prompt should nudge the model to name the country, got:\n%s", captured)
	}
	if !strings.Contains(lower, "code") {
		t.Errorf("extractor prompt should mention not emitting a country code, got:\n%s", captured)
	}
}

// TestExtractReturnsJudgmentOnly asserts the port answers judgment only
// (ADR-0041): the title is still parsed, but a response that STILL carries a
// description cannot leak it into the listing, and the extractor no longer stamps
// the extraction-cache key — the save processor derives both from the page. The
// key's own contract stays pinned by internal/source_hash_test.go.
func TestExtractReturnsJudgmentOnly(t *testing.T) {
	ext := newExtractorServer(t, `{"title":"Backend Engineer","description":"a model-written summary","is_job_posting":true}`)
	raw := crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "some page text"},
	}

	got, err := ext.Extract(t.Context(), raw)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if got.Listing.Title != "Backend Engineer" {
		t.Errorf("Listing.Title = %q, want %q", got.Listing.Title, "Backend Engineer")
	}
	if got.Listing.Description != "" {
		t.Errorf("Listing.Description = %q, want empty (a model summary must not leak)", got.Listing.Description)
	}
	if got.Listing.SourceHash != "" {
		t.Errorf("Listing.SourceHash = %q, want empty (the save processor stamps the cache key)", got.Listing.SourceHash)
	}
}

// TestExtractPromptOmitsDescription asserts the SYSTEM PROMPT the extractor sends no
// longer asks the model to write a description (ADR-0041) — that is what shrinks the
// generated response from a summary plus four fields to four short fields. The shared
// newExtractorServer discards the request, so this uses a local server that records the
// raw request body.
//
// It asserts on the system message alone, not the whole serialized request: the user
// message carries the page text, and "description" is ordinary English that a realistic
// page fixture would contain ("job description"), which would turn a fixture change into
// a false failure. It also rejects "summary", so rewording the prompt to ask for one
// under a different name cannot slip past.
func TestExtractPromptOmitsDescription(t *testing.T) {
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
		URL: newURL(t, "https://careers.acme.com/jobs/1"),
		// Deliberately contains the words the prompt must not: the assertion reads the
		// system message only, so page text can say anything a real posting would.
		Content: crawler.Content{MainContent: "Job description: build things. Summary of the role follows."},
	}

	if _, err := ext.Extract(t.Context(), raw); err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(captured), &sent); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}

	var systemPrompt string
	for _, m := range sent.Messages {
		if m.Role == "system" {
			systemPrompt = m.Content
		}
	}
	if systemPrompt == "" {
		t.Fatalf("captured request carries no system message: %s", captured)
	}

	for _, banned := range []string{"description", "summary"} {
		if strings.Contains(strings.ToLower(systemPrompt), banned) {
			t.Errorf("system prompt should not ask the model for a %s, got:\n%s", banned, systemPrompt)
		}
	}
}
