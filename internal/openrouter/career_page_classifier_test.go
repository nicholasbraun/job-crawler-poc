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
		{
			// A reasoning model that ignores the non-reasoning prompt may prepend its
			// chain-of-thought; a leading <think> block is stripped before decode.
			name:        "leading think block is stripped before decode",
			content:     "<think>the page lists open roles</think>\n{\"is_career_page\":true,\"company_name\":\"Acme\"}",
			wantCareer:  true,
			wantCompany: "Acme",
		},
		{
			// A server that ignores response_format may wrap the object in prose; the
			// outermost-brace fallback salvages the object.
			name:        "prose-wrapped json salvaged by brace fallback",
			content:     "Sure! Here is the JSON: {\"is_career_page\":true,\"company_name\":\"Acme\"} — hope that helps.",
			wantCareer:  true,
			wantCompany: "Acme",
		},
		{
			// think block + code fence together: strip the think block, then the brace
			// fallback recovers the object from inside the surviving fence.
			name:        "think-wrapped fenced json still decodes",
			content:     "<think>reasoning about the page</think>\n```json\n{\"is_career_page\":true,\"company_name\":\"Acme\"}\n```",
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

// TestConfirmPromptReadsTheStructuralRendering asserts the classifier's user message
// carries the page's Structural Rendering with link targets omitted (ADR-0046, #280):
// an openings list reaches the model as headings and list items rather than one run of
// words, while the hrefs stay out of the prompt window.
//
// It decodes the captured body rather than matching it raw: a rendering's tabs and
// newlines reach the wire as JSON escapes, so a raw match would be asserting about the
// encoding instead of about the prompt.
func TestConfirmPromptReadsTheStructuralRendering(t *testing.T) {
	var captured string
	srv := newCapturingServer(t, `{"is_career_page":true,"company_name":null}`, &captured)

	classifier := openrouter.NewCareerPageClassifier(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
	content := &crawler.Content{Title: "x", MainContent: "#\tOpen positions\n" +
		"-\t[Backend Engineer](/jobs/backend)\n" +
		"[input checkbox: role]\tSales Manager (m/w/d)\n" +
		"[button: Jetzt bewerben]"}

	if _, err := classifier.Confirm(t.Context(), "https://careers.acme.com/jobs", content); err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}

	userMessage := promptMessage(t, captured, "user")
	for _, want := range []string{
		"#\tOpen positions",
		"-\t[Backend Engineer]",
		"[input checkbox: role]",
		"[button: Jetzt bewerben]",
	} {
		if !strings.Contains(userMessage, want) {
			t.Errorf("user message should carry the page's structure %q, got:\n%s", want, userMessage)
		}
	}
	for _, unwanted := range []string{"/jobs/backend", "]("} {
		if strings.Contains(userMessage, unwanted) {
			t.Errorf("user message should carry no link targets, but contains %q:\n%s", unwanted, userMessage)
		}
	}
}

// TestConfirmPromptIsUnchangedByFlattenedText asserts #280's kill-switch criterion at
// this seam: with PARSE_STRUCTURAL_RENDERING off the parser hands over Flattened Text,
// which carries no markers, so the narrowing is the identity and the prompt is
// byte-identical to the one that shipped before the rendering existed. The scale version
// runs over all 90 committed fixtures in internal/parser/prompt_variant_test.go.
func TestConfirmPromptIsUnchangedByFlattenedText(t *testing.T) {
	var captured string
	srv := newCapturingServer(t, `{"is_career_page":true,"company_name":null}`, &captured)

	classifier := openrouter.NewCareerPageClassifier(openrouter.Config{BaseURL: srv.URL, APIKey: "test"})
	const flattened = "Open roles Backend Engineer Apply now"
	content := &crawler.Content{Title: "x", MainContent: flattened}

	if _, err := classifier.Confirm(t.Context(), "https://careers.acme.com/jobs", content); err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}

	if userMessage := promptMessage(t, captured, "user"); !strings.Contains(userMessage, flattened) {
		t.Errorf("with the rendering off the user message should carry the page text unchanged (%q), got:\n%s", flattened, userMessage)
	}
}
