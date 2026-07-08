package urlprocessor_test

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
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
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
// worker's handling of a specific frontier rejection.
type stubFrontier struct {
	addErr error
}

func (f *stubFrontier) AddURL(ctx context.Context, url crawler.URL) error { return f.addErr }
func (f *stubFrontier) Next(ctx context.Context) (crawler.URL, error) {
	return crawler.URL{}, frontier.ErrDone
}
func (f *stubFrontier) MarkDone(ctx context.Context, url string) error { return nil }

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
		{name: "max domain limit is not an error", addErr: frontier.ErrMaxDomainLimit, wantErrorLog: false},
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
