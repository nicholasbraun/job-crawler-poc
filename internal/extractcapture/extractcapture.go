// Package extractcapture taps the extract stage to harvest a verdict-tagged
// sample of crawled pages for the Extract Gold Set (#116). It is a short-lived
// experiment aid: when EXTRACT_CAPTURE_PATH is set, every completed extractor
// decision appends one JSONL record {url, verdict, ts} to that file, capped per
// verdict so the rare positives are not drowned by abstains. The captured URLs
// are later re-fetched into raw-HTML fixtures by `llmbench capture -kind extract`
// (a fixture needs the raw bytes; the live pipeline keeps only parsed content).
// With EXTRACT_CAPTURE_PATH unset the tap is off and Hook is nil -- a no-op at the
// call site.
package extractcapture

import (
	"context"
	"encoding/json"
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

var (
	once sync.Once
	hook Hook
)

// FromEnv returns the capture Hook configured from the environment, memoized so
// repeated calls (once per Cycle) share one sink and one open file. It returns
// nil -- a no-op tap -- when EXTRACT_CAPTURE_PATH is unset or the file cannot be
// opened (the error is logged, never fatal: a capture-experiment fault must not
// take down the crawl).
func FromEnv() Hook {
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
		h, _, err := New(path, maxPerVerdict)
		if err != nil {
			slog.Error("extractcapture: capture disabled", "err", err)
			return
		}
		// The file is deliberately left open for the process lifetime: each record
		// is a direct O_APPEND write (durable without an explicit Close), and the OS
		// closes it on exit. A short-lived capture experiment does not warrant
		// threading a Closer through the run lifecycle.
		slog.Info("extractcapture: extract-decision capture enabled", "path", path, "max_per_verdict", maxPerVerdict)
		hook = h
	})
	return hook
}

// New opens path for appending and returns a capture Hook that writes one JSONL
// record per extractor decision, capped at maxPerVerdict records per verdict
// (0 = unbounded). Close the returned io.Closer to release the file. New is the
// testable core; production wiring uses FromEnv.
func New(path string, maxPerVerdict int) (Hook, io.Closer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("extractcapture: open %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	// Keep '&', '<', '>' literal so a captured URL's query string re-fetches
	// verbatim; the default HTML-escaping would turn "&" into "&".
	enc.SetEscapeHTML(false)
	s := &sink{file: f, enc: enc, maxPerVerdict: maxPerVerdict}
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
}

// record is one captured decision. Field order is fixed (url, verdict first) so a
// simple line filter can select by verdict and extract the URL without a JSON
// parser; content trails and is omitted when nil (e.g. a fallback caller that
// only has the URL).
type record struct {
	URL     string `json:"url"`
	Verdict bool   `json:"verdict"`
	TS      string `json:"ts"`
	Content any    `json:"content,omitempty"`
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
	if err := s.enc.Encode(record{URL: url, Verdict: isJobPosting, TS: time.Now().UTC().Format(time.RFC3339), Content: content}); err != nil {
		slog.Error("extractcapture: write failed", "err", err)
		return
	}
	*counter++
}
