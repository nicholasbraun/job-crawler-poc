package extractcapture_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/extractcapture"
)

// testRenderer stands for parser.RendererID's value at the call site. It is spelled
// out rather than imported so this package's tests stay as free of internal/parser
// as the package itself is (#281).
const testRenderer = "structural-v1"

// TestCapsPerVerdict verifies each verdict is bounded independently, so a flood
// of abstains never crowds out the rare positive stratum (or vice versa).
func TestCapsPerVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	hook, closer, err := extractcapture.New(path, 2, testRenderer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for range 5 {
		hook(t.Context(), "https://example.com/accept?a=1&b=2", true, nil)
	}
	for range 5 {
		hook(t.Context(), "https://example.com/abstain", false, nil)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	accepts, abstains := countByVerdict(t, path)
	if accepts != 2 || abstains != 2 {
		t.Fatalf("want 2 accepts / 2 abstains, got %d / %d", accepts, abstains)
	}
}

// TestUnbounded verifies maxPerVerdict == 0 disables the cap.
func TestUnbounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	hook, closer, err := extractcapture.New(path, 0, testRenderer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for range 10 {
		hook(t.Context(), "https://example.com/x", true, nil)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if accepts, _ := countByVerdict(t, path); accepts != 10 {
		t.Fatalf("want 10 accepts, got %d", accepts)
	}
}

// TestURLNotHTMLEscaped guards the SetEscapeHTML(false) contract: a query string
// '&' must survive verbatim so the captured line re-fetches the real URL.
func TestURLNotHTMLEscaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	const raw = "https://example.com/jobs?dept=eng&loc=berlin"
	hook, closer, err := extractcapture.New(path, 0, testRenderer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hook(t.Context(), raw, true, nil)
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Round-trips through JSON to the exact URL...
	var got struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	if got.URL != raw {
		t.Fatalf("URL round-trip: want %q, got %q", raw, got.URL)
	}
	// ...and the '&' is literal on the wire (not &), so a grep/sed filter works.
	if !containsLiteral(string(data), `"url":"`+raw+`"`) {
		t.Fatalf("expected literal URL in JSONL, got: %s", data)
	}
}

// TestConcurrentSafe exercises the mutex under the race detector: 50 concurrent
// decisions (25 of each verdict) all land under a generous cap.
func TestConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	hook, closer, err := extractcapture.New(path, 100, testRenderer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hook(t.Context(), "https://example.com/x", i%2 == 0, nil)
		}(i)
	}
	wg.Wait()
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	accepts, abstains := countByVerdict(t, path)
	if accepts != 25 || abstains != 25 {
		t.Fatalf("want 25/25, got %d/%d", accepts, abstains)
	}
}

// TestContentRoundTrips verifies a passed content payload is serialized under the
// "content" key (the gate-replay substrate), and stays absent when nil.
func TestContentRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	hook, closer, err := extractcapture.New(path, 0, testRenderer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	type content struct {
		JSONLD []string `json:"JSONLD"`
	}
	hook(t.Context(), "https://example.com/a", true, &content{JSONLD: []string{`{"@type":"JobPosting"}`}})
	hook(t.Context(), "https://example.com/b", false, nil)
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)

	sc.Scan()
	var withContent struct {
		Content *content `json:"content"`
	}
	if err := json.Unmarshal(sc.Bytes(), &withContent); err != nil {
		t.Fatalf("line 1: %v", err)
	}
	if withContent.Content == nil || len(withContent.Content.JSONLD) != 1 {
		t.Fatalf("line 1: content did not round-trip: %s", sc.Text())
	}

	sc.Scan()
	if containsLiteral(sc.Text(), `"content"`) {
		t.Fatalf("line 2: nil content must be omitted, got: %s", sc.Text())
	}
}

// TestRendererStampedOnEveryRecord verifies each record names the renderer that
// produced its content, and that a file captured with the Structural Rendering off
// is distinguishable record by record from one captured with it on (#281). Without
// that, two renderings mix inside one gold-set drawing and neither can be told
// apart afterwards (ADR-0046).
func TestRendererStampedOnEveryRecord(t *testing.T) {
	const flattened, structural = "flattened-v1", "structural-v1"

	write := func(renderer string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cap.jsonl")
		hook, closer, err := extractcapture.New(path, 0, renderer)
		if err != nil {
			t.Fatalf("New(%q): %v", renderer, err)
		}
		hook(t.Context(), "https://example.com/jobs/1", true, nil)
		hook(t.Context(), "https://example.com/about", false, nil)
		if err := closer.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		return path
	}

	for _, want := range []string{flattened, structural} {
		got := renderersIn(t, write(want))
		if len(got) != 2 {
			t.Fatalf("renderer %q: want 2 records, got %d", want, len(got))
		}
		for i, r := range got {
			if r != want {
				t.Errorf("renderer %q: record %d says %q; every record must name the renderer its sink was built with", want, i, r)
			}
		}
	}

	if renderersIn(t, write(flattened))[0] == renderersIn(t, write(structural))[0] {
		t.Errorf("a record written with the kill switch off reads the same as one written with it on (%q); the two populations must be distinguishable", flattened)
	}
}

// TestCaptureRefusesAnUnnamedRenderer verifies an unattributable sink is refused
// outright rather than writing records nobody can attribute later, and that the
// refusal happens before the file exists (#281).
func TestCaptureRefusesAnUnnamedRenderer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.jsonl")
	hook, closer, err := extractcapture.New(path, 0, "")
	if !errors.Is(err, extractcapture.ErrNoRenderer) {
		t.Fatalf("New with no renderer: err = %v, want ErrNoRenderer", err)
	}
	if hook != nil || closer != nil {
		t.Errorf("New returned hook=%v closer=%v alongside its error; both must be nil", hook != nil, closer != nil)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat %s: %v; a refused sink must not create its file", path, err)
	}
}

// TestDisabledHookIsNil documents that an unset path yields a no-op tap. New is
// the constructor FromEnv delegates to; an empty path never reaches it, so the
// contract that callers must honor is simply: a nil Hook means disabled.
func TestDisabledHookIsNil(t *testing.T) {
	var h extractcapture.Hook
	if h != nil {
		t.Fatal("zero-value Hook must be nil")
	}
}

func countByVerdict(t *testing.T, path string) (accepts, abstains int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r struct {
			URL     string `json:"url"`
			Verdict bool   `json:"verdict"`
			TS      string `json:"ts"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad json line %q: %v", sc.Text(), err)
		}
		if r.Verdict {
			accepts++
		} else {
			abstains++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return accepts, abstains
}

func containsLiteral(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// renderersIn returns the "renderer" of every record in a capture file, in order.
func renderersIn(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	renderers := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r struct {
			Renderer string `json:"renderer"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad json line %q: %v", sc.Text(), err)
		}
		renderers = append(renderers, r.Renderer)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return renderers
}
