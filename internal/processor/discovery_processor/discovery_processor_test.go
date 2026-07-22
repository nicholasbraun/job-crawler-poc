package discoveryprocessor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	discoveryprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/discovery_processor"
)

// --- inline test doubles ---

type spyFrontier struct {
	addErr   error
	added    []crawler.URL
	markDone []string
}

func (f *spyFrontier) AddURL(ctx context.Context, u crawler.URL) error {
	f.added = append(f.added, u)
	return f.addErr
}
func (f *spyFrontier) Next(ctx context.Context) (crawler.URL, error) { return crawler.URL{}, nil }
func (f *spyFrontier) MarkDone(ctx context.Context, u string) error {
	f.markDone = append(f.markDone, u)
	return nil
}

type stubDownloader struct{ html []byte }

func (d *stubDownloader) Get(ctx context.Context, url string) (*downloader.Response, error) {
	return &downloader.Response{StatusCode: 200, Content: d.html}, nil
}

type stubParser struct{ content *crawler.Content }

func (p *stubParser) Parse(b []byte) (*crawler.Content, error) { return p.content, nil }

type allowAllRobots struct{}

func (allowAllRobots) Check(ctx context.Context, u string) error { return nil }

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("error building url: %v", err)
	}
	return u
}

func TestDiscoveryProcessorEmitsCareerPageAndDiscoversLinks(t *testing.T) {
	frontier := &spyFrontier{}
	// An ATS board root (structurally a career page) that links to two pages.
	content := &crawler.Content{
		Title:       "Jobs at Acme",
		MainContent: "open roles",
		URLs:        []string{"/acme/jobs/1", "https://acme.com/about"},
	}

	var emitted []*crawler.RawCareerPage
	proc := discoveryprocessor.NewProcessor(&discoveryprocessor.Config{
		Frontier:         frontier,
		Downloader:       &stubDownloader{html: []byte("<html></html>")},
		Parser:           &stubParser{content: content},
		ContentFilter:    filter.Chain[*crawler.Content](),
		URLFilter:        filter.Chain[string](),
		RobotsTxtChecker: allowAllRobots{},
		GateConfig:       crawler.DefaultLLMGateConfig(),
		OnCareerPage: func(ctx context.Context, page *crawler.RawCareerPage) error {
			emitted = append(emitted, page)
			return nil
		},
	})

	seed := newURL(t, "https://job-boards.greenhouse.io/acme")
	if err := proc.Process(t.Context(), &seed); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(emitted) != 1 {
		t.Fatalf("want 1 career page emitted, got %d", len(emitted))
	}
	if !emitted[0].Certain {
		t.Error("an ATS board root should be emitted with Certain=true")
	}
	if emitted[0].URL.RawURL != seed.RawURL {
		t.Errorf("emitted URL = %q, want %q", emitted[0].URL.RawURL, seed.RawURL)
	}

	if len(frontier.added) != 2 {
		t.Fatalf("want 2 links added to frontier, got %d: %v", len(frontier.added), frontier.added)
	}
	if len(frontier.markDone) != 1 {
		t.Errorf("want MarkDone called once, got %d", len(frontier.markDone))
	}
}

func TestDiscoveryProcessorSkipsJobPosting(t *testing.T) {
	frontier := &spyFrontier{}
	// A single ATS job posting must NOT be catalogued as a career page.
	content := &crawler.Content{
		Title:       "Job Application for Engineer at Acme",
		MainContent: "apply now",
		URLs:        []string{"/acme"},
	}

	emitted := 0
	proc := discoveryprocessor.NewProcessor(&discoveryprocessor.Config{
		Frontier:         frontier,
		Downloader:       &stubDownloader{html: []byte("<html></html>")},
		Parser:           &stubParser{content: content},
		ContentFilter:    filter.Chain[*crawler.Content](),
		URLFilter:        filter.Chain[string](),
		RobotsTxtChecker: allowAllRobots{},
		GateConfig:       crawler.DefaultLLMGateConfig(),
		OnCareerPage: func(ctx context.Context, page *crawler.RawCareerPage) error {
			emitted++
			return nil
		},
	})

	seed := newURL(t, "https://job-boards.greenhouse.io/acme/jobs/123")
	if err := proc.Process(t.Context(), &seed); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if emitted != 0 {
		t.Errorf("a job posting must not emit a career-page candidate, emitted %d", emitted)
	}
	// Link discovery still runs on postings (to find more pages).
	if len(frontier.added) != 1 {
		t.Errorf("want the discovered link added, got %d", len(frontier.added))
	}
}

func TestDiscoveryProcessorSkipsNonCareerPage(t *testing.T) {
	frontier := &spyFrontier{}
	content := &crawler.Content{
		Title:       "Our Blog",
		MainContent: "latest product news",
		URLs:        []string{},
	}

	emitted := 0
	proc := discoveryprocessor.NewProcessor(&discoveryprocessor.Config{
		Frontier:         frontier,
		Downloader:       &stubDownloader{html: []byte("<html></html>")},
		Parser:           &stubParser{content: content},
		ContentFilter:    filter.Chain[*crawler.Content](),
		URLFilter:        filter.Chain[string](),
		RobotsTxtChecker: allowAllRobots{},
		GateConfig:       crawler.DefaultLLMGateConfig(),
		OnCareerPage: func(ctx context.Context, page *crawler.RawCareerPage) error {
			emitted++
			return nil
		},
	})

	seed := newURL(t, "https://acme.com/blog/hello")
	if err := proc.Process(t.Context(), &seed); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if emitted != 0 {
		t.Errorf("non-career page should not emit a candidate, emitted %d", emitted)
	}
}

// captureLogs installs a JSON slog handler writing into buf for the duration of
// fn, then restores the previous default logger.
func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

// hasErrorLevel reports whether any captured log line was emitted at ERROR.
func hasErrorLevel(t *testing.T, buf *bytes.Buffer) bool {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("could not parse log line %q: %v", line, err)
		}
		if entry["level"] == "ERROR" {
			return true
		}
	}
	return false
}

// A max-depth rejection from the frontier is an expected client-side outcome, so
// discovery must not log it at ERROR — a deep hub page would otherwise flood the
// logs with thousands of identical lines. Any other AddURL error still logs at
// ERROR. Mirrors url_processor's TestProcessAddURLRejections.
func TestDiscoveryProcessorAddURLRejections(t *testing.T) {
	tests := []struct {
		name         string
		addErr       error
		wantErrorLog bool
	}{
		{name: "max depth is not an error", addErr: frontier.ErrMaxDepth, wantErrorLog: false},
		{name: "unexpected error is logged at error", addErr: errors.New("boom"), wantErrorLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := &crawler.Content{
				Title:       "Our Blog",
				MainContent: "latest product news",
				URLs:        []string{"/next"},
			}

			proc := discoveryprocessor.NewProcessor(&discoveryprocessor.Config{
				Frontier:         &spyFrontier{addErr: tt.addErr},
				Downloader:       &stubDownloader{html: []byte("<html></html>")},
				Parser:           &stubParser{content: content},
				ContentFilter:    filter.Chain[*crawler.Content](),
				URLFilter:        filter.Chain[string](),
				RobotsTxtChecker: allowAllRobots{},
				GateConfig:       crawler.DefaultLLMGateConfig(),
				OnCareerPage:     func(context.Context, *crawler.RawCareerPage) error { return nil },
			})

			seed := newURL(t, "https://acme.com/blog/hello")

			var buf bytes.Buffer
			captureLogs(t, &buf, func() {
				if err := proc.Process(t.Context(), &seed); err != nil {
					t.Fatalf("Process returned error: %v", err)
				}
			})

			if got := hasErrorLevel(t, &buf); got != tt.wantErrorLog {
				t.Errorf("error-level log present = %v, want %v; logs:\n%s", got, tt.wantErrorLog, buf.String())
			}
		})
	}
}
