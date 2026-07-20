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

// newClassifierServer stands up a server that always replies with content as the
// LLM message body, and returns a classifier pointed at it. chatEnvelope and
// newURL are shared with job_listing_extractor_test.go in this package.
func newClassifierServer(t *testing.T, content string) *openrouter.CareerPageClassifier {
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

	return openrouter.NewCareerPageClassifier(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
}

// TestConfirmDecodesVerdict pins the real classifier's decode of the widened
// two-field response at the public Confirm seam: a named employer surfaces on the
// Verdict, while a null, absent, or whitespace-only company_name collapses to ""
// so the Name Ladder reads it as abstain (ADR-0025).
func TestConfirmDecodesVerdict(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantCareer  bool
		wantCompany string
	}{
		{
			name:        "career page names employer",
			content:     `{"is_career_page":true,"company_name":"Süddeutsche Zeitung"}`,
			wantCareer:  true,
			wantCompany: "Süddeutsche Zeitung",
		},
		{
			name:        "career page abstains on null name",
			content:     `{"is_career_page":true,"company_name":null}`,
			wantCareer:  true,
			wantCompany: "",
		},
		{
			name:        "not a career page",
			content:     `{"is_career_page":false,"company_name":null}`,
			wantCareer:  false,
			wantCompany: "",
		},
		{
			name:        "absent company_name field abstains",
			content:     `{"is_career_page":true}`,
			wantCareer:  true,
			wantCompany: "",
		},
		{
			name:        "surrounding whitespace is trimmed",
			content:     `{"is_career_page":true,"company_name":"  Acme  "}`,
			wantCareer:  true,
			wantCompany: "Acme",
		},
		{
			name:        "fenced json body still decodes",
			content:     "```json\n{\"is_career_page\":true,\"company_name\":\"Acme\"}\n```",
			wantCareer:  true,
			wantCompany: "Acme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			classifier := newClassifierServer(t, tc.content)
			content := &crawler.Content{Title: "x", MainContent: "some page text"}

			verdict, err := classifier.Confirm(t.Context(), "https://careers.acme.com/jobs", content)
			if err != nil {
				t.Fatalf("Confirm returned error: %v", err)
			}

			if verdict.IsCareerPage != tc.wantCareer {
				t.Errorf("IsCareerPage = %v, want %v", verdict.IsCareerPage, tc.wantCareer)
			}
			if verdict.CompanyName != tc.wantCompany {
				t.Errorf("CompanyName = %q, want %q", verdict.CompanyName, tc.wantCompany)
			}
		})
	}
}

// TestConfirmPromptRequestsCompanyName proves the widened, null-biased name
// instruction is actually sent to the LLM, so the llm rung has something to
// decode. The shared newClassifierServer discards the request, so this uses a
// local server that records the raw request body.
func TestConfirmPromptRequestsCompanyName(t *testing.T) {
	var captured string

	var env chatEnvelope
	env.Choices = make([]struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}, 1)
	env.Choices[0].Message.Content = `{"is_career_page":true,"company_name":null}`

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

	classifier := openrouter.NewCareerPageClassifier(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
	content := &crawler.Content{Title: "x", MainContent: "some page text"}

	if _, err := classifier.Confirm(t.Context(), "https://careers.acme.com/jobs", content); err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}

	if !strings.Contains(captured, "company_name") {
		t.Errorf("classifier request should ask for company_name, got:\n%s", captured)
	}
	if !strings.Contains(captured, "null") {
		t.Errorf("classifier request should instruct a null-biased name, got:\n%s", captured)
	}
}
