package crawler_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestPostingBody covers the Posting Body derivation (ADR-0041) directly: it is a
// pure function of a parsed page, which is why a unit test below the behaviour seam
// is warranted here and only here.
func TestPostingBody(t *testing.T) {
	tests := []struct {
		name       string
		content    *crawler.Content
		maxChars   int
		wantBody   string
		wantSource crawler.DescriptionSource
	}{
		{
			name: "lone JobPosting description wins over main content",
			content: &crawler.Content{
				MainContent: "site chrome and nav",
				JSONLD:      []string{`{"@context":"https://schema.org","@type":"JobPosting","title":"Engineer","description":"We are hiring."}`},
			},
			maxChars:   1000,
			wantBody:   "We are hiring.",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			// Tags become SPACES (not ""), so words do not glue across block tags,
			// and entities unescape exactly once.
			name: "HTML tags stripped, entities unescaped, whitespace collapsed",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD:      []string{`{"@type":"JobPosting","description":"<p>Build <b>things</b> &amp; ship them.</p><ul><li>Go</li><li>Postgres</li></ul>"}`},
			},
			maxChars:   1000,
			wantBody:   "Build things & ship them. Go Postgres",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			// Regression guard on the strip-before-unescape ordering: unescaping
			// first would leave a literal "<" the tag regex swallows along with the
			// text up to the next real tag.
			name: "entity-encoded angle bracket survives the reduction",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD:      []string{`{"@type":"JobPosting","description":"team of &lt;10 engineers"}`},
			},
			maxChars:   1000,
			wantBody:   "team of <10 engineers",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name: "two JobPosting nodes are an openings index, not a lone posting",
			content: &crawler.Content{
				MainContent: "two roles on this page",
				JSONLD: []string{
					`{"@type":"JobPosting","title":"Engineer","description":"Engineer body."}`,
					`{"@type":"JobPosting","title":"Designer","description":"Designer body."}`,
				},
			},
			maxChars:   1000,
			wantBody:   "two roles on this page",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// An ItemList wrapping even a SINGLE JobPosting is the Gate's
			// openings-index shape, so the derivation must not treat it as lone.
			name: "ItemList wrapping one JobPosting falls through to main content",
			content: &crawler.Content{
				MainContent: "openings index",
				JSONLD: []string{`{"@context":"https://schema.org","@type":"ItemList","itemListElement":[
					{"@type":"ListItem","position":1,"item":{"@type":"JobPosting","title":"Engineer","description":"Engineer body."}}]}`},
			},
			maxChars:   1000,
			wantBody:   "openings index",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name: "JSON-LD without any JobPosting falls through to main content",
			content: &crawler.Content{
				MainContent: "about us",
				JSONLD:      []string{`{"@type":"Organization","name":"Acme","description":"We make things."}`},
			},
			maxChars:   1000,
			wantBody:   "about us",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// An unparseable block contributes nothing and never fails the read.
			name: "unparseable block alongside a lone posting still yields structured data",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD: []string{
					`{"@type":"JobPosting", NOT JSON`,
					`{"@type":"JobPosting","description":"We are hiring."}`,
				},
			},
			maxChars:   1000,
			wantBody:   "We are hiring.",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name: "lone posting without a description falls through to main content",
			content: &crawler.Content{
				MainContent: "page text worth indexing",
				JSONLD:      []string{`{"@type":"JobPosting","title":"Engineer"}`},
			},
			maxChars:   1000,
			wantBody:   "page text worth indexing",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name: "lone posting with an empty description falls through to main content",
			content: &crawler.Content{
				MainContent: "page text worth indexing",
				JSONLD:      []string{`{"@type":"JobPosting","title":"Engineer","description":"   "}`},
			},
			maxChars:   1000,
			wantBody:   "page text worth indexing",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name: "lone posting with a non-string description falls through to main content",
			content: &crawler.Content{
				MainContent: "page text worth indexing",
				JSONLD:      []string{`{"@type":"JobPosting","title":"Engineer","description":42}`},
			},
			maxChars:   1000,
			wantBody:   "page text worth indexing",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name: "@type as an array is recognized",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD:      []string{`{"@type":["JobPosting"],"description":"We are hiring."}`},
			},
			maxChars:   1000,
			wantBody:   "We are hiring.",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name: "@type as a full schema.org URL is recognized",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD:      []string{`{"@type":"https://schema.org/JobPosting","description":"We are hiring."}`},
			},
			maxChars:   1000,
			wantBody:   "We are hiring.",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name: "@graph-nested lone posting is recognized",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD: []string{`{"@context":"https://schema.org","@graph":[
					{"@type":"Organization","name":"Acme"},
					{"@type":"JobPosting","description":"We are hiring."}]}`},
			},
			maxChars:   1000,
			wantBody:   "We are hiring.",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name: "cap fires on the structured-data branch",
			content: &crawler.Content{
				MainContent: "fallback",
				JSONLD:      []string{`{"@type":"JobPosting","description":"abcdefghijKLMNOPQRST"}`},
			},
			maxChars:   10,
			wantBody:   "abcdefghij",
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name:       "cap fires on the main-content branch",
			content:    &crawler.Content{MainContent: "abcdefghijKLMNOPQRST"},
			maxChars:   10,
			wantBody:   "abcdefghij",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name:       "cap boundary is inclusive",
			content:    &crawler.Content{MainContent: "abcdefghij"},
			maxChars:   10,
			wantBody:   "abcdefghij",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// A byte cut would split a multi-byte character.
			name:       "cap counts runes, not bytes",
			content:    &crawler.Content{MainContent: "äöüßéèêë"},
			maxChars:   4,
			wantBody:   "äöüß",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			name:       "empty content yields an empty body attributed to page content",
			content:    &crawler.Content{},
			maxChars:   1000,
			wantBody:   "",
			wantSource: crawler.DescriptionSourcePageContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, source := crawler.PostingBody(tt.content, tt.maxChars)
			assertStrings(t, tt.wantBody, body)
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
		})
	}
}

// TestPostingBodyNilContent pins the nil-page contract: an empty body attributed to
// page content, never a panic.
func TestPostingBodyNilContent(t *testing.T) {
	body, source := crawler.PostingBody(nil, 100)
	assertStrings(t, "", body)
	if source != crawler.DescriptionSourcePageContent {
		t.Errorf("source = %q, want %q", source, crawler.DescriptionSourcePageContent)
	}
}

// TestPostingBodyDefaultCap pins the documented default and the non-positive-cap
// fallback: a forgotten knob must never blank the Corpus by capping to zero.
func TestPostingBodyDefaultCap(t *testing.T) {
	if crawler.DefaultDescriptionMaxChars != 16000 {
		t.Fatalf("DefaultDescriptionMaxChars = %d, want 16000 (the documented row-size guard)", crawler.DefaultDescriptionMaxChars)
	}

	long := strings.Repeat("a", crawler.DefaultDescriptionMaxChars+500)
	body, source := crawler.PostingBody(&crawler.Content{MainContent: long}, 0)
	if got := utf8.RuneCountInString(body); got != crawler.DefaultDescriptionMaxChars {
		t.Errorf("body length = %d runes, want %d (non-positive cap falls back to the default)", got, crawler.DefaultDescriptionMaxChars)
	}
	if source != crawler.DescriptionSourcePageContent {
		t.Errorf("source = %q, want %q", source, crawler.DescriptionSourcePageContent)
	}
}
