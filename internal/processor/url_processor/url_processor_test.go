package urlprocessor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
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

// spyRecorder captures the gate reasons the worker records, the rungs of the Shadow
// Extraction samples it sheds, and every Posting Score it observes.
type spyRecorder struct {
	gates     []llmobs.Reason
	shedRungs []string
	scores    []float64
}

func (s *spyRecorder) Call(context.Context, llmobs.Kind, llmobs.Outcome, time.Duration) {}
func (s *spyRecorder) Gated(_ context.Context, _ llmobs.Kind, r llmobs.Reason) {
	s.gates = append(s.gates, r)
}
func (s *spyRecorder) PostingScore(_ context.Context, score float64) {
	s.scores = append(s.scores, score)
}
func (s *spyRecorder) Shadow(context.Context, llmobs.ShadowVerdict, string) {}
func (s *spyRecorder) ShadowDropped(_ context.Context, rung string) {
	s.shedRungs = append(s.shedRungs, rung)
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
			// A real posting path (not a bare root or index), so ShouldExtract's URL
			// rungs pass and this exercises the relevance gate, not the structure gate.
			seed, err := crawler.NewURL("http://example.com/o/senior-engineer")
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

// shadowSpy captures every sample handed to the Shadow Extraction hook (ADR-0044)
// and can be told to return an error, to drive the best-effort error path.
type shadowSpy struct {
	sampled []crawler.ShadowSample
	retErr  error
}

func (s *shadowSpy) onShadow(_ context.Context, sample *crawler.ShadowSample) error {
	s.sampled = append(s.sampled, *sample)
	return s.retErr
}

// shadowConfig builds a worker config whose seed URL the Extract Gate rejects, so
// every case below exercises the shadow sampling branch rather than the relevance
// branch. The relevance filter deliberately WOULD pass, proving the sample hangs off
// the gate's reject and nothing else.
func shadowConfig(fr *stubFrontier, content *crawler.Content, rec *spyRecorder) *urlprocessor.Config {
	return &urlprocessor.Config{
		Frontier:         fr,
		Downloader:       &stubDownloader{content: []byte("<html></html>")},
		Parser:           &stubParser{content: content},
		ContentFilter:    func(*crawler.Content) error { return nil },
		URLFilter:        func(string) error { return nil },
		RobotsTxtChecker: stubRobots{},
		RelevanceFilter:  func(*crawler.Content) error { return nil },
		GateConfig:       crawler.DefaultLLMGateConfig(),
		Recorder:         rec,
	}
}

// TestProcessShadowSamplesGateRejectedPages proves the Shadow Extraction sampling
// hook (ADR-0044) fires on a page the Extract Gate rejected, carries the page as
// crawled, and is switched off entirely by a zero rate or an unset hook — while
// leaving the gate's existing behaviour (the recorded reason, no OnJobListing)
// untouched.
func TestProcessShadowSamplesGateRejectedPages(t *testing.T) {
	tests := []struct {
		name       string
		shadowRate float64
		hookNil    bool
		wantShadow int
	}{
		{name: "a full rate samples every rejected page", shadowRate: 1.0, wantShadow: 1},
		{name: "a zero rate disables shadow extraction", shadowRate: 0, wantShadow: 0},
		{name: "a nil hook disables shadow extraction", shadowRate: 1.0, hookNil: true, wantShadow: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := &crawler.Content{Title: "Careers", MainContent: "body"}
			rec := &spyRecorder{}
			spy := &shadowSpy{}
			listed := 0

			cfg := shadowConfig(&stubFrontier{}, content, rec)
			cfg.OnJobListing = func(context.Context, *crawler.RawJobListing) error { listed++; return nil }
			cfg.ShadowRate = tt.shadowRate
			if !tt.hookNil {
				cfg.OnShadowExtract = spy.onShadow
			}

			worker := urlprocessor.NewProcessor(cfg)
			// A career-hub index: the Extract Gate rejects it on URL structure.
			seed, err := crawler.NewURL("https://acme.com/careers")
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			if err := worker.Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if got := len(spy.sampled); got != tt.wantShadow {
				t.Fatalf("shadow samples = %d, want %d", got, tt.wantShadow)
			}
			if tt.wantShadow > 0 {
				if got := spy.sampled[0].URL.RawURL; got != "https://acme.com/careers" {
					t.Errorf("sampled URL = %q, want %q", got, "https://acme.com/careers")
				}
				if got := spy.sampled[0].Content; !reflect.DeepEqual(got, *content) {
					t.Errorf("sampled Content = %+v, want %+v", got, *content)
				}
				// The sample carries the rung that shed the page, so the measured
				// false-drop rate can be split by cause. /careers is a terminal jobs-index
				// segment, which is rung 2c.
				if got := spy.sampled[0].Rung; got != string(pagegate.RungIndexTerminal) {
					t.Errorf("sampled rung = %q, want %q", got, pagegate.RungIndexTerminal)
				}
				// The Posting Score is inert on every rung but the Learned Veto's, which is
				// exactly what makes the wire-level zero safe: the rung gates the read, so no
				// presence flag has to cross the durable stream (ADR-0049).
				if got := spy.sampled[0].Score; got != 0 {
					t.Errorf("sampled Score = %v, want 0: only the Learned Veto's rung scores a page", got)
				}
			}

			// Sampling is purely additive: the gate still records its reason and still
			// sheds the page.
			want := []llmobs.Reason{llmobs.ReasonURLStructure}
			if !reflect.DeepEqual(rec.gates, want) {
				t.Errorf("recorded gates = %v, want %v", rec.gates, want)
			}
			if listed != 0 {
				t.Errorf("OnJobListing called %d times, want 0 (the gate rejected this page)", listed)
			}
		})
	}
}

// TestProcessNeverShadowsAPageTheGateKeeps pins the branch: a page the Extract Gate
// admits goes to the real extract lane, never to the measurement lane. Shadowing a
// kept page would double its model spend and count a normal accept as a false-drop.
func TestProcessNeverShadowsAPageTheGateKeeps(t *testing.T) {
	spy := &shadowSpy{}
	listed := 0

	cfg := shadowConfig(&stubFrontier{}, &crawler.Content{Title: "role", MainContent: "body"}, &spyRecorder{})
	cfg.OnJobListing = func(context.Context, *crawler.RawJobListing) error { listed++; return nil }
	cfg.OnShadowExtract = spy.onShadow
	cfg.ShadowRate = 1.0

	worker := urlprocessor.NewProcessor(cfg)
	// A posting-shaped URL, which the Positive Evidence rung admits on the URL mark
	// alone (TestShouldExtract_PositiveEvidence pins that). The fixture must carry
	// Positive Evidence since #264 turned the rung on by default: "clears every reject
	// rung" no longer means "the gate keeps it", and this test is about the KEPT
	// branch.
	seed, err := crawler.NewURL("http://example.com/careers/senior-engineer")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	if err := worker.Process(t.Context(), &seed); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if listed != 1 {
		t.Errorf("OnJobListing called %d times, want 1", listed)
	}
	if got := len(spy.sampled); got != 0 {
		t.Errorf("shadow samples = %d, want 0 (a kept page is never shadowed)", got)
	}
}

// TestProcessShadowSamplesAtTheConfiguredRate proves the rate is a real sampling
// fraction rather than an all-or-nothing switch: without it, a hook that fired on
// every rejected page would still pass every other test here while spending a
// hundred times the intended measurement budget.
func TestProcessShadowSamplesAtTheConfiguredRate(t *testing.T) {
	spy := &shadowSpy{}
	cfg := shadowConfig(&stubFrontier{}, &crawler.Content{Title: "Careers", MainContent: "body"}, &spyRecorder{})
	cfg.OnJobListing = func(context.Context, *crawler.RawJobListing) error { return nil }
	cfg.OnShadowExtract = spy.onShadow
	cfg.ShadowRate = 0.25

	worker := urlprocessor.NewProcessor(cfg)
	const n = 2000
	for i := 0; i < n; i++ {
		// A query string keeps the gate's verdict identical while making each URL
		// distinct, so nothing dedups the sample.
		seed, err := crawler.NewURL(fmt.Sprintf("https://acme.com/careers?p=%d", i))
		if err != nil {
			t.Fatalf("NewURL: %v", err)
		}
		if err := worker.Process(t.Context(), &seed); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	}

	// n=2000 at p=0.25 has sd ≈ 19.4, so the ±0.10 band is ~10 sigma wide: this
	// fails on a broken rate, effectively never on chance.
	if got := len(spy.sampled); got < 300 || got > 700 {
		t.Errorf("shadow samples = %d of %d, want roughly 25%% (300..700)", got, n)
	}
}

// TestProcessShadowHookErrorDoesNotFailTheWalk proves a failing measurement stays a
// measurement: the page's own processing — including link discovery — completes, and
// nothing is logged at ERROR, so a full shadow backlog cannot make a healthy
// Collection Cycle look broken.
func TestProcessShadowHookErrorDoesNotFailTheWalk(t *testing.T) {
	fr := &stubFrontier{}
	spy := &shadowSpy{retErr: errors.New("shadow backlog full")}

	rec := &spyRecorder{}
	content := &crawler.Content{Title: "Careers", MainContent: "body", URLs: []string{"/jobs/1"}}
	cfg := shadowConfig(fr, content, rec)
	cfg.OnJobListing = func(context.Context, *crawler.RawJobListing) error { return nil }
	cfg.OnShadowExtract = spy.onShadow
	cfg.ShadowRate = 1.0

	worker := urlprocessor.NewProcessor(cfg)
	seed, err := crawler.NewURL("https://acme.com/careers")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}

	var buf bytes.Buffer
	captureLogs(t, &buf, func() {
		if err := worker.Process(t.Context(), &seed); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	if hasErrorLevel(t, &buf) {
		t.Errorf("a dropped shadow sample must not log at ERROR; logs:\n%s", buf.String())
	}
	if len(fr.added) != 1 {
		t.Errorf("enqueued URLs = %v, want the page's one discovered link", fr.added)
	}
	// A shed sample is COUNTED, keyed by the rung it was shed from. Without this the
	// live false-drop rate would silently rest on a non-uniform subsample: the bounded
	// stream sheds whatever arrives while it is full, which is not how the sampler
	// chooses.
	want := []string{string(pagegate.RungIndexTerminal)}
	if !reflect.DeepEqual(rec.shedRungs, want) {
		t.Errorf("recorded shed rungs = %v, want %v", rec.shedRungs, want)
	}
}

// atsEmbedCall records one OnATSEmbed invocation so a test can assert exactly
// which boards a crawled page's embeds triggered a fetch for, and with which Owner.
type atsEmbedCall struct {
	Provider string
	Tenant   string
	Owner    string
}

// atsEmbedSpy captures every OnATSEmbed call the worker makes and can be told to
// return an error, to drive the best-effort error path.
type atsEmbedSpy struct {
	calls  []atsEmbedCall
	retErr error
}

func (s *atsEmbedSpy) onEmbed(_ context.Context, provider, tenant, owner string) error {
	s.calls = append(s.calls, atsEmbedCall{Provider: provider, Tenant: tenant, Owner: owner})
	return s.retErr
}

// TestProcessATSEmbedTrigger proves the Keyword Crawl ATS Embed trigger (ADR-0022,
// #129) fires an ATS Fetch for a firing board embed whose provider has a registered
// fetcher, attributes it to the page's Owner, ignores clientless providers, and is
// inert (and panic-free) when the hooks are nil.
func TestProcessATSEmbedTrigger(t *testing.T) {
	// A firing Greenhouse script embed: needs its board-container marker present.
	greenhouseEmbed := &crawler.Content{
		Title:       "Careers",
		MainContent: "body",
		Embeds:      []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
		ElementIDs:  []string{"grnhse_app"},
	}
	// A firing Personio iframe embed (clientless provider — no registered fetcher).
	personioEmbed := &crawler.Content{
		Title:       "Team",
		MainContent: "body",
		Embeds:      []crawler.Embed{{Src: "https://globex.jobs.personio.de/search", IsFrame: true}},
	}

	t.Run("registered provider fires tagged with the page Owner", func(t *testing.T) {
		spy := &atsEmbedSpy{}
		cfg := &urlprocessor.Config{
			Frontier:         &stubFrontier{},
			Downloader:       &stubDownloader{content: []byte("<html></html>")},
			Parser:           &stubParser{content: greenhouseEmbed},
			ContentFilter:    func(*crawler.Content) error { return nil },
			URLFilter:        func(string) error { return nil },
			RobotsTxtChecker: stubRobots{},
			RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
			OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
			HasATSFetcher:    func(p string) bool { return p == "greenhouse" },
			OnATSEmbed:       spy.onEmbed,
		}

		worker := urlprocessor.NewProcessor(cfg)
		seed, err := crawler.NewURL("https://acme.com/team")
		if err != nil {
			t.Fatalf("NewURL: %v", err)
		}
		seed.Owner = "acme.com"

		if err := worker.Process(t.Context(), &seed); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		want := []atsEmbedCall{{Provider: "greenhouse", Tenant: "acme", Owner: "acme.com"}}
		if !reflect.DeepEqual(spy.calls, want) {
			t.Errorf("OnATSEmbed calls = %v, want %v", spy.calls, want)
		}
	})

	t.Run("clientless provider does not fire", func(t *testing.T) {
		spy := &atsEmbedSpy{}
		cfg := &urlprocessor.Config{
			Frontier:         &stubFrontier{},
			Downloader:       &stubDownloader{content: []byte("<html></html>")},
			Parser:           &stubParser{content: personioEmbed},
			ContentFilter:    func(*crawler.Content) error { return nil },
			URLFilter:        func(string) error { return nil },
			RobotsTxtChecker: stubRobots{},
			RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
			OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
			// Personio has no registered fetcher, so the firing embed must not fire.
			HasATSFetcher: func(string) bool { return false },
			OnATSEmbed:    spy.onEmbed,
		}

		worker := urlprocessor.NewProcessor(cfg)
		seed, err := crawler.NewURL("https://globex.com/team")
		if err != nil {
			t.Fatalf("NewURL: %v", err)
		}

		if err := worker.Process(t.Context(), &seed); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if len(spy.calls) != 0 {
			t.Errorf("OnATSEmbed calls = %v, want none (clientless provider)", spy.calls)
		}
	})

	t.Run("nil hooks are inert", func(t *testing.T) {
		cfg := &urlprocessor.Config{
			Frontier:         &stubFrontier{},
			Downloader:       &stubDownloader{content: []byte("<html></html>")},
			Parser:           &stubParser{content: greenhouseEmbed},
			ContentFilter:    func(*crawler.Content) error { return nil },
			URLFilter:        func(string) error { return nil },
			RobotsTxtChecker: stubRobots{},
			RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
			OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
			// HasATSFetcher and OnATSEmbed left nil (Discovery / un-wired Keyword Crawl).
		}

		worker := urlprocessor.NewProcessor(cfg)
		seed, err := crawler.NewURL("https://acme.com/team")
		if err != nil {
			t.Fatalf("NewURL: %v", err)
		}

		if err := worker.Process(t.Context(), &seed); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	t.Run("trigger error is logged and does not abort Process", func(t *testing.T) {
		spy := &atsEmbedSpy{retErr: errors.New("boom")}
		cfg := &urlprocessor.Config{
			Frontier:         &stubFrontier{},
			Downloader:       &stubDownloader{content: []byte("<html></html>")},
			Parser:           &stubParser{content: greenhouseEmbed},
			ContentFilter:    func(*crawler.Content) error { return nil },
			URLFilter:        func(string) error { return nil },
			RobotsTxtChecker: stubRobots{},
			RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
			OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
			HasATSFetcher:    func(p string) bool { return p == "greenhouse" },
			OnATSEmbed:       spy.onEmbed,
		}

		worker := urlprocessor.NewProcessor(cfg)
		seed, err := crawler.NewURL("https://acme.com/team")
		if err != nil {
			t.Fatalf("NewURL: %v", err)
		}

		var buf bytes.Buffer
		captureLogs(t, &buf, func() {
			if err := worker.Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}
		})

		if !hasErrorLevel(t, &buf) {
			t.Errorf("expected an ERROR-level log for the failed trigger; logs:\n%s", buf.String())
		}
	})
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

// learnedVetoConfig builds shadowConfig's worker with the Learned Veto rung switched
// on. The rung ships OFF (ADR-0049), so every case that is about it has to name the
// switch, and a test that read the shipped default would be asserting about a rung
// that never ran.
func learnedVetoConfig(content *crawler.Content, rec *spyRecorder, on bool) *urlprocessor.Config {
	cfg := shadowConfig(&stubFrontier{}, content, rec)
	cfg.OnJobListing = func(context.Context, *crawler.RawJobListing) error { return nil }
	cfg.GateConfig.LearnedVeto = on
	return cfg
}

// richPostingContent is a page carrying the sections of a real posting: the shape the
// Posting Score ranks highest, so it survives the Learned Veto. Copied from
// internal/pagegate/learned_veto_test.go, which owns the fixture -- that one lives in
// an external test package and cannot be imported.
func richPostingContent() *crawler.Content {
	return &crawler.Content{
		Title: "Senior Engineer (m/w/d) gesucht",
		MainContent: "Ihre Aufgaben. Ihr Profil. Wir bieten. Vollzeit. Ansprechpartner. " +
			"Jetzt bewerben. Wir freuen uns auf Ihre Bewerbung. Vergütung nach Tarif. Arbeiten bei uns.",
	}
}

// requireScoreSide fails the test unless the fixture falls on the side of
// VetoThreshold the case needs. A retrain that moves the weights is expected to break
// this precondition before it breaks any assertion below, and the message names the
// repair. It mirrors the pair in internal/pagegate/learned_veto_test.go, which is not
// importable from here.
func requireScoreSide(t *testing.T, u crawler.URL, content *crawler.Content, wantVetoed bool) {
	t.Helper()
	got := pagegate.Score(u, content)
	if wantVetoed && got >= pagegate.VetoThreshold {
		t.Fatalf("fixture scores %.6f, at or above VetoThreshold %.6f: this case needs a page the veto drops. "+
			"A retrain moved the weights -- pick a weaker fixture; never move the threshold to fit a test.", got, pagegate.VetoThreshold)
	}
	if !wantVetoed && got < pagegate.VetoThreshold {
		t.Fatalf("fixture scores %.6f, below VetoThreshold %.6f: this case needs a page the veto keeps. "+
			"A retrain moved the weights -- pick a stronger fixture; never move the threshold to fit a test.", got, pagegate.VetoThreshold)
	}
}

// TestProcessRecordsThePostingScoreForEveryPageTheVetoJudged pins the population the
// live score distribution covers (ADR-0049): the Learned Veto's ACCEPTS as well as its
// vetoes, and nothing else. Recording only the vetoes would show where the cut is
// without showing what it is cutting into, which is the whole reason this histogram
// exists — it is the drift detector against the Extract Gold Set the weights were
// fitted on. Recording a page the rung never judged would be worse: an unjudged page
// carries a zero that is not a score, and folding those in would report a stream that
// scores lowest of all.
func TestProcessRecordsThePostingScoreForEveryPageTheVetoJudged(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		content    *crawler.Content
		veto       bool
		wantScored bool
		wantVetoed bool
		wantReason llmobs.Reason
	}{
		{
			name:       "a vetoed page is recorded",
			url:        "https://acme.com/jobs/senior-engineer",
			content:    &crawler.Content{},
			veto:       true,
			wantScored: true,
			wantVetoed: true,
			wantReason: llmobs.ReasonLearnedVeto,
		},
		{
			name:       "a page the veto keeps is recorded too",
			url:        "https://acme.com/o/senior-engineer",
			content:    richPostingContent(),
			veto:       true,
			wantScored: true,
		},
		{
			name:       "an earlier rung's reject is not recorded",
			url:        "https://acme.com/careers",
			content:    &crawler.Content{},
			veto:       true,
			wantReason: llmobs.ReasonURLStructure,
		},
		{
			name:    "the veto off records nothing",
			url:     "https://acme.com/jobs/senior-engineer",
			content: &crawler.Content{},
			veto:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed, err := crawler.NewURL(tt.url)
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			if tt.wantScored {
				requireScoreSide(t, seed, tt.content, tt.wantVetoed)
			}

			rec := &spyRecorder{}
			cfg := learnedVetoConfig(tt.content, rec, tt.veto)
			// This case is about the histogram, not the sampling lane.
			cfg.ShadowRate = 0

			if err := urlprocessor.NewProcessor(cfg).Process(t.Context(), &seed); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if !tt.wantScored {
				if len(rec.scores) != 0 {
					t.Errorf("recorded Posting Scores = %v, want none: this page was never judged by the Learned Veto", rec.scores)
				}
			} else {
				if len(rec.scores) != 1 {
					t.Fatalf("recorded Posting Scores = %v, want exactly one", rec.scores)
				}
				if want := pagegate.Score(seed, tt.content); rec.scores[0] != want {
					t.Errorf("recorded Posting Score = %.6f, want %.6f: the histogram records the score the rung judged by",
						rec.scores[0], want)
				}
			}

			if tt.wantReason == "" {
				if len(rec.gates) != 0 {
					t.Errorf("recorded gates = %v, want none: the gate kept this page", rec.gates)
				}
				return
			}
			if !reflect.DeepEqual(rec.gates, []llmobs.Reason{tt.wantReason}) {
				t.Errorf("recorded gates = %v, want [%q]", rec.gates, tt.wantReason)
			}
		})
	}
}

// TestProcessCarriesThePostingScoreOnAVetoedSample closes the loop from the rung to
// the durable stream: a page the Learned Veto dropped reaches the Shadow Extraction
// lane carrying the score that dropped it (ADR-0049). The score travels WITH the
// sample rather than being re-derived downstream, because a re-derivation could run a
// different binary's weights and file a verdict against a score the gate never
// computed — and this rung is the one whose drops cannot be explained in words, so
// that number is the only account of them there is.
func TestProcessCarriesThePostingScoreOnAVetoedSample(t *testing.T) {
	content := &crawler.Content{}
	seed, err := crawler.NewURL("https://acme.com/jobs/senior-engineer")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	requireScoreSide(t, seed, content, true)

	rec := &spyRecorder{}
	spy := &shadowSpy{}
	cfg := learnedVetoConfig(content, rec, true)
	cfg.OnShadowExtract = spy.onShadow
	cfg.ShadowRate = 1.0

	if err := urlprocessor.NewProcessor(cfg).Process(t.Context(), &seed); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(spy.sampled) != 1 {
		t.Fatalf("shadow samples = %d, want 1", len(spy.sampled))
	}
	if got := spy.sampled[0].Rung; got != string(pagegate.RungLearnedVeto) {
		t.Errorf("sampled rung = %q, want %q", got, pagegate.RungLearnedVeto)
	}
	want := pagegate.Score(seed, content)
	if got := spy.sampled[0].Score; got != want {
		t.Errorf("sampled Score = %.6f, want %.6f exactly", got, want)
	}
	if want <= 0 {
		t.Errorf("the fixture's Posting Score is %.6f: a zero score would make this assertion vacuous", want)
	}
	// The veto's saving is counted under its OWN gated reason, so the calls it saves
	// are readable without the structural rejects pooled in.
	if !reflect.DeepEqual(rec.gates, []llmobs.Reason{llmobs.ReasonLearnedVeto}) {
		t.Errorf("recorded gates = %v, want [%q]", rec.gates, llmobs.ReasonLearnedVeto)
	}
	if len(rec.scores) != 1 || rec.scores[0] != want {
		t.Errorf("recorded Posting Scores = %v, want [%.6f]: the histogram and the sample must report one number", rec.scores, want)
	}
}

// TestTheLearnedVetoGatedReasonMatchesItsRungName holds two constants equal that are
// declared independently: llmobs knows nothing about the Extract Gate, so the reason
// and the rung are written in two packages. Holding them equal is what lets an
// operator join the calls the veto saved (the gated counter) to the false-drops it
// caused (the shadow counter) — one word for one mechanism, the same discipline
// ReasonStructuredData follows.
func TestTheLearnedVetoGatedReasonMatchesItsRungName(t *testing.T) {
	if string(llmobs.ReasonLearnedVeto) != string(pagegate.RungLearnedVeto) {
		t.Errorf("gated reason %q and rung %q differ: an operator cannot join the veto's saving to its false-drop rate",
			llmobs.ReasonLearnedVeto, pagegate.RungLearnedVeto)
	}
}
