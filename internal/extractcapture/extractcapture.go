// Package extractcapture taps the extract stage to harvest a verdict-tagged
// sample of crawled pages for the Extract Gold Set (#116). When
// EXTRACT_CAPTURE_PATH is set, every completed extractor decision appends one
// JSONL record {url, verdict, ts, renderer, content} to that file, capped per
// verdict so the rare positives are not drowned by abstains. With
// EXTRACT_CAPTURE_PATH unset the tap is off and Hook is nil -- a no-op at the call
// site.
//
// renderer names the renderer that produced the captured content
// (parser.RendererID), bound once when the sink is built. A Structural Rendering
// is a derived artefact, so a renderer change has to show up as rows produced by
// two renderers rather than mixing silently inside one drawing (ADR-0046, #281).
//
// What this file writes IS the gold-set substrate (ADR-0043): the captured
// content is the parsed page the live gate and extractor saw, and
// `llmbench goldset-sample` stratifies, weights and commits a sample of it
// verbatim. It is deliberately never re-fetched into raw-HTML fixtures -- a page
// re-fetched months later is a different page, and that drift corrupts the label.
// Because the caps decide which records survive, they ARE the sampling design and
// the weights are derived from them.
package extractcapture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

// Hook is the per-decision callback the job listing processor invokes once per
// completed extraction with the source URL, the extractor's verdict
// (isJobPosting: true = a single job posting was extracted, false = an abstain),
// and the parsed page content the extractor (and the Extract Gate) saw. content
// is serialized verbatim so the gate can be replayed against the exact bytes the
// live pipeline produced -- no re-fetch, no drift. It is typed as any to keep
// this package free of the domain type; pass *crawler.Content (or nil). A nil
// Hook is a no-op and must be treated as "capture disabled" by callers.
type Hook func(ctx context.Context, url string, isJobPosting bool, content any)

const (
	// pathEnv names the JSONL capture file; unset disables the tap.
	pathEnv = "EXTRACT_CAPTURE_PATH"
	// maxEnv caps records written PER VERDICT (0 = unbounded). Balancing per
	// verdict keeps the ~5% positive stream from being buried under abstains and
	// bounds the file on a 363k-call/day crawl.
	maxEnv = "EXTRACT_CAPTURE_MAX"
	// defaultMaxPerVerdict bounds the file when EXTRACT_CAPTURE_MAX is unset:
	// enough of each stratum to sample a few hundred labels from.
	defaultMaxPerVerdict = 2000
)

// ErrNoRenderer rejects a capture sink that cannot say which renderer produced the
// content it stores. An unattributable record is worse than a missing one: it
// mixes two renderings inside one drawing, which is the exact failure the stamp
// exists to prevent (ADR-0046, #281).
var ErrNoRenderer = errors.New("extractcapture: renderer must be named (parser.RendererID)")

var (
	once sync.Once
	hook Hook
)

// FromEnv returns the capture Hook configured from the environment, memoized so
// repeated calls (once per Cycle) share one sink and one open file. It returns
// nil -- a no-op tap -- when EXTRACT_CAPTURE_PATH is unset or the file cannot be
// opened (the error is logged, never fatal: a capture-experiment fault must not
// take down the crawl).
//
// renderer is parser.RendererID from the parser that produces the captured
// content. Like the sink and the file it is bound by the FIRST call and memoized
// with it, which is correct because a process runs one parser configuration.
func FromEnv(renderer string) Hook {
	once.Do(func() {
		path := os.Getenv(pathEnv)
		if path == "" {
			return
		}
		maxPerVerdict := defaultMaxPerVerdict
		if v := os.Getenv(maxEnv); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				maxPerVerdict = n
			} else {
				slog.Error("extractcapture: invalid "+maxEnv+", using default", "value", v, "default", defaultMaxPerVerdict)
			}
		}
		h, _, err := New(path, maxPerVerdict, renderer)
		if err != nil {
			slog.Error("extractcapture: capture disabled", "err", err)
			return
		}
		// The file is deliberately left open for the process lifetime: each record
		// is a direct O_APPEND write (durable without an explicit Close), and the OS
		// closes it on exit. A short-lived capture experiment does not warrant
		// threading a Closer through the run lifecycle.
		slog.Info("extractcapture: extract-decision capture enabled", "path", path, "max_per_verdict", maxPerVerdict, "renderer", renderer)
		hook = h
	})
	return hook
}

// New opens path for appending and returns a capture Hook that writes one JSONL
// record per extractor decision, capped at maxPerVerdict records per verdict
// (0 = unbounded). renderer names the renderer that produced the content the Hook
// will be handed (parser.RendererID) and is stamped on every record; an empty one
// is refused with ErrNoRenderer before the file is even created. Close the
// returned io.Closer to release the file. New is the testable core; production
// wiring uses FromEnv.
func New(path string, maxPerVerdict int, renderer string) (Hook, io.Closer, error) {
	if renderer == "" {
		return nil, nil, ErrNoRenderer
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("extractcapture: open %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	// Keep '&', '<', '>' literal so a captured URL's query string re-fetches
	// verbatim; the default HTML-escaping would turn "&" into "&".
	enc.SetEscapeHTML(false)
	s := &sink{file: f, enc: enc, maxPerVerdict: maxPerVerdict, renderer: renderer}
	return s.record, f, nil
}

// sink appends one JSON record per extractor decision, guarding the shared file
// and the per-verdict counters with a mutex (the extract stage runs many workers
// concurrently). Once a verdict's cap is reached its later records are dropped.
type sink struct {
	mu            sync.Mutex
	file          *os.File
	enc           *json.Encoder
	maxPerVerdict int
	accepts       int
	abstains      int
	// renderer is stamped on every record this sink writes; it is fixed for the
	// file's lifetime because a process runs one parser configuration (#281).
	renderer string
}

// record is one captured decision. Field order is fixed (url, verdict first) so a
// simple line filter can select by verdict and extract the URL without a JSON
// parser; content trails and is omitted when nil (e.g. a fallback caller that
// only has the URL).
type record struct {
	URL     string `json:"url"`
	Verdict bool   `json:"verdict"`
	TS      string `json:"ts"`
	// Renderer names the renderer that produced Content (parser.RendererFlattened
	// or parser.RendererStructural). It carries no omitempty: it is never empty,
	// because a sink that cannot name its renderer is refused at construction, and
	// an absent renderer must keep meaning "written before the stamp existed".
	Renderer string `json:"renderer"`
	Content  any    `json:"content,omitempty"`
}

func (s *sink) record(_ context.Context, url string, isJobPosting bool, content any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	counter := &s.abstains
	if isJobPosting {
		counter = &s.accepts
	}
	if s.maxPerVerdict > 0 && *counter >= s.maxPerVerdict {
		return
	}
	if err := s.enc.Encode(record{URL: url, Verdict: isJobPosting, TS: time.Now().UTC().Format(time.RFC3339), Renderer: s.renderer, Content: content}); err != nil {
		slog.Error("extractcapture: write failed", "err", err)
		return
	}
	*counter++
}
