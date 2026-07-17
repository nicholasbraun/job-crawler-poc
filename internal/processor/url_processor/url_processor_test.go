package urlprocessor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	urlprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/url_processor"
)

type stubDownloader struct {
	content []byte
}

func (d *stubDownloader) Get(ctx context.Context, url string) (*downloader.Response, error) {
	return &downloader.Response{StatusCode: 200, Content: d.content}, nil
}

type stubParser struct {
	content *crawler.Content
}

func (p *stubParser) Parse(b []byte) (*crawler.Content, error) {
	return p.content, nil
}

type stubRobots struct{}

func (stubRobots) Check(ctx context.Context, u string) error { return nil }

// stubFrontier returns addErr from every AddURL call, letting a test drive the
// worker's handling of a specific frontier rejection. It also records every URL
// passed to AddURL so a test can assert exactly which links reached the
// frontier (e.g. the scope fence dropping off-scope links before enqueue).
type stubFrontier struct {
	addErr error
	added  []crawler.URL
}

func (f *stubFrontier) AddURL(ctx context.Context, url crawler.URL) error {
	f.added = append(f.added, url)
	return f.addErr
}
func (f *stubFrontier) Next(ctx context.Context) (crawler.URL, error) {
	return crawler.URL{}, frontier.ErrDone
}
func (f *stubFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// spyRecorder captures the gate reasons the worker records.
type spyRecorder struct {
	gates []llmobs.Reason
}

func (s *spyRecorder) Call(context.Context, llmobs.Kind, llmobs.Outcome, time.Duration) {}
func (s *spyRecorder) Gated(_ context.Context, _ llmobs.Kind, r llmobs.Reason) {
	s.gates = append(s.gates, r)
}
func (s *spyRecorder) Content(context.Context, llmobs.Kind, string)          {}
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                    {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)               {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {}

// captureLogs installs a JSON slog handler writing into buf for the duration of
// fn, then restores the previous default logger.
func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

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

func TestProcessAddURLRejections(t *testing.T) {
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
				Title:       "some page",
				MainContent: "body",
				URLs:        []string{"/next"},
			}

			cfg := &urlprocessor.Config{
				Frontier:         &stubFrontier{addErr: tt.addErr},
				Downloader:       &stubDownloader{content: []byte("<html></html>")},
				Parser:           &stubParser{content: content},
				ContentFilter:    func(*crawler.Content) error { return nil },
				URLFilter:        func(string) error { return nil },
				RobotsTxtChecker: stubRobots{},
				RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
				OnJobListing: func(context.Context, *crawler.RawJobListing) error {
					return nil
				},
			}

			worker := urlprocessor.NewProcessor(cfg)

			seed, err := crawler.NewURL("http://example.com")
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}

			var buf bytes.Buffer
			captureLogs(t, &buf, func() {
				if err := worker.Process(t.Context(), &seed); err != nil {
					t.Fatalf("Process returned error: %v", err)
				}
			})

			if got := hasErrorLevel(t, &buf); got != tt.wantErrorLog {
				t.Errorf("error-level log present = %v, want %v; logs:\n%s", got, tt.wantErrorLog, buf.String())
			}
		})
	}
}

func TestProcessRecordsURLStructureGate(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "career-hub index is gated by URL structure", url: "https://acme.com/careers"},
		{name: "reject path is gated by URL structure", url: "https://acme.com/blog/hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &spyRecorder{}
			listed := 0
			cfg := &urlprocessor.Config{
				Frontier:         &stubFrontier{},
				Downloader:       &stubDownloader{content: []byte("<html></html>")},
				Parser:           &stubParser{content: &crawler.Content{Title: "role", MainContent: "body"}},
				ContentFilter:    func(*crawler.Content) error { return nil },
				URLFilter:        func(string) error { return nil },
				RobotsTxtChecker: stubRobots{},
				// A relevance filter that WOULD pass, to prove the URL-structure gate
				// short-circuits before keyword relevance.
				RelevanceFilter: func(*crawler.Content) error { return nil },
				GateConfig:      crawler.DefaultLLMGateConfig(),
				OnJobListing:    func(context.Context, *crawler.RawJobListing) error { listed++; return nil },
				Recorder:        rec,
			}

			worker := urlprocessor.NewProcessor(cfg)
			seed, err := crawler.NewURL(tt.url)
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			if err := worker.Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			want := []llmobs.Reason{llmobs.ReasonURLStructure}
			if len(rec.gates) != len(want) {
				t.Fatalf("recorded gates = %v, want %v", rec.gates, want)
			}
			if rec.gates[0] != want[0] {
				t.Errorf("gate[0] = %v, want %v", rec.gates[0], want[0])
			}
			if listed != 0 {
				t.Errorf("OnJobListing called %d times, want 0 (URL structure gate must short-circuit)", listed)
			}
		})
	}
}

func TestProcessRecordsRelevanceGate(t *testing.T) {
	tests := []struct {
		name      string
		relevant  bool
		wantGates []llmobs.Reason
	}{
		{name: "irrelevant page is gated without the LLM", relevant: false, wantGates: []llmobs.Reason{llmobs.ReasonIrrelevant}},
		{name: "relevant page is forwarded, not gated", relevant: true, wantGates: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relevance := func(*crawler.Content) error { return errors.New("not a listing") }
			if tt.relevant {
				relevance = func(*crawler.Content) error { return nil }
			}

			rec := &spyRecorder{}
			cfg := &urlprocessor.Config{
				Frontier:         &stubFrontier{},
				Downloader:       &stubDownloader{content: []byte("<html></html>")},
				Parser:           &stubParser{content: &crawler.Content{Title: "role", MainContent: "body"}},
				ContentFilter:    func(*crawler.Content) error { return nil },
				URLFilter:        func(string) error { return nil },
				RobotsTxtChecker: stubRobots{},
				RelevanceFilter:  relevance,
				OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
				Recorder:         rec,
			}

			worker := urlprocessor.NewProcessor(cfg)
			seed, err := crawler.NewURL("http://example.com")
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			if err := worker.Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if len(rec.gates) != len(tt.wantGates) {
				t.Fatalf("recorded gates = %v, want %v", rec.gates, tt.wantGates)
			}
			for i, want := range tt.wantGates {
				if rec.gates[i] != want {
					t.Errorf("gate[%d] = %v, want %v", i, rec.gates[i], want)
				}
			}
		})
	}
}

// TestProcessScopeFence proves the Keyword Crawl scope fence (ADR-0021) drops
// off-scope discovered links before they reach the frontier, and is inert when
// the seed carries no Scope (the Discovery Crawl roam property).
func TestProcessScopeFence(t *testing.T) {
	urls := []string{
		"https://acme.com/jobs/1",
		"https://careers.acme.com/jobs/2",
		"https://talish.dev/portfolio",
		"https://boards.greenhouse.io/globex",
	}

	tests := []struct {
		name  string
		scope string
		want  []string
	}{
		{
			// A self-hosted seed keyed on acme.com follows only links resolving to the
			// same Company: its registrable domain and every subdomain. The off-catalog
			// host (talish.dev) and the sibling ATS tenant (greenhouse:globex) are
			// fenced out before enqueue — which also proves a self-hosted seed does not
			// follow a link onto a known ATS host.
			name:  "keyword crawl fences off-scope links",
			scope: "acme.com",
			want: []string{
				"https://acme.com/jobs/1",
				"https://careers.acme.com/jobs/2",
			},
		},
		{
			// Without a Scope the fence is inert — the Discovery Crawl roams — so every
			// discovered link is enqueued regardless of Company.
			name:  "empty scope roams",
			scope: "",
			want:  urls,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &stubFrontier{}
			cfg := &urlprocessor.Config{
				Frontier:         fr,
				Downloader:       &stubDownloader{content: []byte("<html></html>")},
				Parser:           &stubParser{content: &crawler.Content{Title: "role", MainContent: "body", URLs: urls}},
				ContentFilter:    func(*crawler.Content) error { return nil },
				URLFilter:        func(string) error { return nil },
				RobotsTxtChecker: stubRobots{},
				RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
				OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
			}

			worker := urlprocessor.NewProcessor(cfg)
			seed, err := crawler.NewURL("https://acme.com")
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			seed.Scope = tt.scope

			if err := worker.Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			got := make([]string, len(fr.added))
			for i, u := range fr.added {
				got[i] = u.RawURL
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("enqueued URLs = %v, want %v", got, tt.want)
			}
		})
	}
}
