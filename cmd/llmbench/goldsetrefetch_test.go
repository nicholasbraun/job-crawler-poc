package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
)

// goldRefetchTestCapture is the captured side of every re-fetch test: 102 distinct
// words is 100 distinct 3-grams, so a served page's retention -- and therefore its
// state -- is exact rather than approximate.
const goldRefetchTestTitle = "Senior Go Engineer -- Acme"

func goldRefetchTestCaptured() string { return goldFidelityWordRun("w", 102) }

// goldRefetchTestPage wraps text in the shape the parser takes a main region from.
func goldRefetchTestPage(title, text string) string {
	return "<!doctype html><html><head><title>" + title + "</title></head><body><main><p>" + text + "</p></main></body></html>"
}

// goldRefetchTestServer serves the four outcomes the fidelity rule has to separate,
// records what was asked for, and answers robots.txt per the caller's rules.
type goldRefetchTestServer struct {
	*httptest.Server

	mu         sync.Mutex
	requests   []string
	uris       []string
	userAgents map[string]string
	robots     string // empty serves a 404, which robots.txt reads as allow-all
}

func newGoldRefetchTestServer(t *testing.T, robots string) *goldRefetchTestServer {
	t.Helper()
	s := &goldRefetchTestServer{userAgents: map[string]string{}, robots: robots}

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if s.robots == "" {
			http.Error(w, "no robots.txt", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(s.robots))
	})
	page := func(status int, title, text string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.record(r)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(goldRefetchTestPage(title, text)))
		}
	}
	mux.HandleFunc("/same", page(http.StatusOK, goldRefetchTestTitle, goldRefetchTestCaptured()))
	mux.HandleFunc("/drifted", page(http.StatusOK, goldRefetchTestTitle, goldFidelityDrifted(102, 50)))
	// A 200 whose captured text has vanished: the withdrawal page ADR-0047 calls the
	// dangerous case, because it argues confidently for `residue`.
	mux.HandleFunc("/withdrawn", page(http.StatusOK, "Position filled -- Acme",
		"This position has been filled. Applications are closed. Browse our current openings."))
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		http.Error(w, "not found", http.StatusNotFound)
	})
	// The employer-directory shape, served raw rather than through page(): its whole
	// point is the markup, which page() would flatten into one paragraph.
	mux.HandleFunc("/directory", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(goldRenderTestDirectoryPage))
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// goldRenderTestDirectoryPage is the employer-directory shape ADR-0046 names: a
// heading over a list of links whose TARGETS are the whole answer. Flattened it is a
// run of company names -- which is exactly the page that reads like an index of Job
// Listings and is not one.
const goldRenderTestDirectoryTitle = "Unternehmen -- neckarfilsjobs"

const goldRenderTestDirectoryPage = `<!doctype html><html><head><title>` + goldRenderTestDirectoryTitle +
	`</title></head><body><main>` +
	`<h1>Unternehmen</h1><ul>` +
	`<li><a href="/organization/1">Firma Alpha</a> Musterstrasse 1</li>` +
	`<li><a href="/organization/2">Firma Beta</a> Musterstrasse 2</li>` +
	`</ul></main></body></html>`

// goldRenderTestCaptured returns a page's captured side exactly as the pipeline stored
// it before ADR-0046: the FLATTENING parser's own output over the same bytes. Deriving
// it rather than writing it by hand is what makes the row measure `same`.
func goldRenderTestCaptured(t *testing.T, page string) crawler.Content {
	t.Helper()
	content, err := parser.NewHTMLParser().Parse([]byte(page))
	if err != nil {
		t.Fatalf("parse the captured page: %v", err)
	}
	return *content
}

func (s *goldRefetchTestServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.URL.Path)
	s.uris = append(s.uris, r.URL.RequestURI())
	s.userAgents[r.URL.Path] = r.Header.Get("User-Agent")
}

// distinctPages counts the distinct page URIs asked for, robots.txt excluded, so a
// test can bound how many pages a warm loop paid for.
func (s *goldRefetchTestServer) distinctPages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	for _, uri := range s.uris {
		if strings.HasPrefix(uri, "/robots.txt") {
			continue
		}
		seen[uri] = struct{}{}
	}
	return len(seen)
}

func (s *goldRefetchTestServer) asked(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, got := range s.requests {
		if got == path {
			n++
		}
	}
	return n
}

func (s *goldRefetchTestServer) userAgentFor(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userAgents[path]
}

// goldRefetchTestTarget builds the captured side of a row pointing at path.
func (s *goldRefetchTestServer) target(path string) goldRefetchTarget {
	url := s.URL + path
	return goldRefetchTarget{
		ID: rowID(url), URL: url,
		CapturedTitle: goldRefetchTestTitle, CapturedText: goldRefetchTestCaptured(),
	}
}

// goldRefetchTestRobots points the REAL robots.txt fetcher at a test server.
// robotstxt.Checker derives the robots URL from a request's hostname and drops the
// port, so on a loopback listener the fetch would otherwise miss it entirely and every
// row would read as disallowed. Rewriting the authority here keeps the real checker,
// the real parse and the crawler's own user agent in the path.
type goldRefetchTestRobots struct {
	inner *robotstxt.RobotsTxtDownloader
	base  string
}

func (g goldRefetchTestRobots) Get(ctx context.Context, url string) (*robotstxt.Response, error) {
	return g.inner.Get(ctx, g.base+"/robots.txt")
}

// newGoldRefetchTestRefetcher builds the real refetcher over the real politeness
// decorator, pointed at a local server. The spacing interval is 0 -- a zero interval
// never blocks, so the test is fast while robots.txt is still fetched and honoured.
func newGoldRefetchTestRefetcher(t *testing.T, base, cacheDir string, maxAge time.Duration) *goldRefetcher {
	t.Helper()
	polite := collection.NewPoliteDownloader(
		downloader.NewClient(userAgent),
		robotstxt.NewChecker(
			temoto.NewRobotsTxtParser(userAgent),
			goldRefetchTestRobots{inner: robotstxt.NewRobotsTxtDownloader(userAgent, nil), base: base},
		),
		atsingest.NewHostLimiter(0),
		nil,
	)
	r, err := newGoldRefetcher(goldRefetchConfig{Downloader: polite, CacheDir: cacheDir, MaxAge: maxAge})
	if err != nil {
		t.Fatalf("newGoldRefetcher: %v", err)
	}
	return r
}

// TestGoldRefetcherMeasuresAgainstALocalServer drives the whole re-fetch path -- the
// crawler's downloader, its user agent, the refetch lane's robots re-check and
// spacing, the structural parser and the fidelity rule -- against a local server, so
// no test touches the network.
func TestGoldRefetcherMeasuresAgainstALocalServer(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	cacheDir := t.TempDir()
	refetcher := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)

	tests := []struct {
		path         string
		want         goldFidelity
		wantLiveView bool
	}{
		{"/same", fidelitySame, true},
		{"/drifted", fidelityDrifted, true},
		{"/withdrawn", fidelityGone, false},
		{"/gone", fidelityGone, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			target := server.target(tt.path)
			rep, ok := refetcher.Measure(t.Context(), target)
			if !ok {
				t.Fatalf("the measurement did not complete")
			}
			if rep.State != tt.want {
				t.Errorf("%s measured %q (retention %.2f), want %q -- %s", tt.path, rep.State, rep.Retention, tt.want, rep.Reason)
			}
			if rep.LiveView != tt.wantLiveView {
				t.Errorf("%s live_view = %v, want %v", tt.path, rep.LiveView, tt.wantLiveView)
			}
			if rep.At == "" {
				t.Error("the report carries no instant; an aid the labeller is told the age of has to have one")
			} else if _, err := time.Parse(time.RFC3339, rep.At); err != nil {
				t.Errorf("the report's instant %q does not parse as RFC3339: %v", rep.At, err)
			}
		})
	}

	// The AC's "the crawler's own downloader, with its user agent and its politeness".
	if got := server.userAgentFor("/same"); got != userAgent {
		t.Errorf("the page was fetched as %q, want the crawler's own user agent %q", got, userAgent)
	}
	if server.asked("/robots.txt") == 0 {
		t.Error("robots.txt was never fetched; the re-fetch must go through the refetch lane's politeness (ADR-0040)")
	}

	// The cache: bytes for the responses that produced any, a sidecar for every
	// measurement, and nothing at all for a row never measured.
	for _, tt := range tests {
		id := rowID(server.URL + tt.path)
		sidecar := filepath.Join(cacheDir, id+".json")
		raw, err := os.ReadFile(sidecar)
		if err != nil {
			t.Fatalf("%s: read the cache sidecar: %v", tt.path, err)
		}
		var entry goldRefetchCacheEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("%s: decode the cache sidecar: %v", tt.path, err)
		}
		if entry.URL != server.URL+tt.path || entry.Report.State != tt.want {
			t.Errorf("%s: the sidecar describes %q at %q, want %q at %q", tt.path, entry.Report.State, entry.URL, tt.want, server.URL+tt.path)
		}
		bytesPath := filepath.Join(cacheDir, id+".html")
		_, err = os.Stat(bytesPath)
		if tt.path == "/gone" && err == nil {
			t.Errorf("%s: a 404 left cached bytes behind, but it produced no page", tt.path)
		}
		if tt.path != "/gone" && err != nil {
			t.Errorf("%s: the re-fetched bytes were not cached: %v", tt.path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cacheDir, rowID(server.URL+"/never-measured")+".json")); err == nil {
		t.Error("a row nobody measured left a cache entry behind")
	}
}

// TestGoldRefetcherRefusesWhereRobotsDoes pins the one outcome that is NOT a
// measurement: a host that disallows the re-fetch leaves the row unmeasured, because
// `gone` is a claim about the page and we did not look.
func TestGoldRefetcherRefusesWhereRobotsDoes(t *testing.T) {
	server := newGoldRefetchTestServer(t, "User-agent: *\nDisallow: /\n")
	refetcher := newGoldRefetchTestRefetcher(t, server.URL, t.TempDir(), goldRefetchMaxAge)

	rep, ok := refetcher.Measure(t.Context(), server.target("/same"))
	if !ok {
		t.Fatalf("the measurement did not complete")
	}
	if rep.State != "" {
		t.Errorf("a robots.txt refusal measured %q, want no state at all: we did not look at the page", rep.State)
	}
	if rep.LiveView {
		t.Error("a row we never fetched was offered a live view")
	}
	if !strings.Contains(rep.Reason, "robots") {
		t.Errorf("the reason is %q, want it to name robots.txt so the labeller knows why there is no aid", rep.Reason)
	}
	if n := server.asked("/same"); n != 0 {
		t.Errorf("the page was fetched %d times despite robots.txt disallowing it", n)
	}
}

// TestGoldRefetcherFetchesEachRowOnce pins the cost of the aid: one fetch per row per
// session, one fetch per row per day across sessions, and a re-fetch once the cached
// copy is older than MaxAge.
func TestGoldRefetcherFetchesEachRowOnce(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	cacheDir := t.TempDir()
	target := server.target("/same")

	refetcher := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)
	for range 2 {
		if _, ok := refetcher.Measure(t.Context(), target); !ok {
			t.Fatal("the measurement did not complete")
		}
	}
	if n := server.asked("/same"); n != 1 {
		t.Errorf("two measurements of one row cost %d fetches, want 1", n)
	}

	// A fresh session over the same cache reads the copy on disk rather than the host.
	second := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)
	if _, ok := second.Measure(t.Context(), target); !ok {
		t.Fatal("the cached measurement did not complete")
	}
	if n := server.asked("/same"); n != 1 {
		t.Errorf("a second session over the same cache cost %d fetches in total, want the 1 the first session paid", n)
	}

	// A cached copy older than MaxAge is fetched again: a cached copy is still a
	// re-fetch taken at some instant, and the labeller is told its age.
	sidecarPath := filepath.Join(cacheDir, target.ID+".json")
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read the cache sidecar: %v", err)
	}
	var entry goldRefetchCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode the cache sidecar: %v", err)
	}
	entry.FetchedAt = time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	aged, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode the aged sidecar: %v", err)
	}
	if err := os.WriteFile(sidecarPath, aged, 0o644); err != nil {
		t.Fatalf("write the aged sidecar: %v", err)
	}
	third := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)
	if _, ok := third.Measure(t.Context(), target); !ok {
		t.Fatal("the re-measurement did not complete")
	}
	if n := server.asked("/same"); n != 2 {
		t.Errorf("a cached copy two days old cost %d fetches in total, want a second one", n)
	}

	// A cache entry describing a different URL is not this row's measurement.
	other := goldRefetchTarget{ID: target.ID, URL: server.URL + "/drifted", CapturedTitle: target.CapturedTitle, CapturedText: target.CapturedText}
	fourth := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)
	if _, ok := fourth.Measure(t.Context(), other); !ok {
		t.Fatal("the colliding measurement did not complete")
	}
	if n := server.asked("/drifted"); n != 1 {
		t.Errorf("a cache entry describing another URL was reused for this row (%d fetches of /drifted, want 1)", n)
	}
}

// TestGoldRefetchCacheMustBeOutsideTheRepository is the rule that makes "never
// committed" a property of the tool rather than a hope: a cache directory anywhere
// inside the repository holding the gold set is refused. No network.
func TestGoldRefetchCacheMustBeOutsideTheRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("create the fake repository: %v", err)
	}
	goldDir := filepath.Join(repo, "cmd", "llmbench", "extract-goldset")
	if err := os.MkdirAll(goldDir, 0o755); err != nil {
		t.Fatalf("create the gold-set directory: %v", err)
	}
	outside := t.TempDir()

	tests := []struct {
		name   string
		want   string
		refuse bool
	}{
		{name: "beside the gold set", want: filepath.Join(goldDir, "refetch"), refuse: true},
		{name: "the repository root itself", want: repo, refuse: true},
		{name: "a hidden directory in the repository", want: filepath.Join(repo, ".refetch-cache"), refuse: true},
		{name: "a path that climbs back into the repository", want: filepath.Join(outside, "..", filepath.Base(repo), "cache"), refuse: true},
		{name: "a sibling directory outside it", want: filepath.Join(outside, "cache"), refuse: false},
		{name: "the default under the OS user cache directory", want: "", refuse: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := goldRefetchCacheDir(tt.want, goldDir)
			if tt.refuse {
				if err == nil {
					t.Fatalf("goldRefetchCacheDir(%q) = %q, want it refused: re-fetched bytes are a working artefact and are never committed (ADR-0047)", tt.want, got)
				}
				if !strings.Contains(err.Error(), "ADR-0047") {
					t.Errorf("the refusal is %q, want it to name the rule it enforces", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("goldRefetchCacheDir(%q) = %v, want it accepted", tt.want, err)
			}
			if got == "" || !filepath.IsAbs(got) {
				t.Errorf("goldRefetchCacheDir(%q) = %q, want an absolute path", tt.want, got)
			}
			if withinDir(repo, got) {
				t.Errorf("goldRefetchCacheDir(%q) = %q, which sits inside the repository at %q", tt.want, got, repo)
			}
		})
	}
}

// TestGoldRefetchCacheDirDefaultNamesTheTool pins where the default cache lands, so
// the startup line a labeller is told to delete is the directory that actually holds
// the bytes.
func TestGoldRefetchCacheDirDefaultNamesTheTool(t *testing.T) {
	got, err := goldRefetchCacheDir("", t.TempDir())
	if err != nil {
		t.Fatalf("goldRefetchCacheDir: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(got), goldRefetchCacheName) {
		t.Errorf("the default cache directory is %q, want it to end in %q", got, goldRefetchCacheName)
	}
}

// TestGoldRefetcherStopsWaitingWhenTheCallerDoes pins the one case Measure reports as
// incomplete: the caller's context ended. The fetch itself is NOT cancelled -- a
// labeller reloading the tab must not cancel a fetch a second caller is waiting on.
func TestGoldRefetcherStopsWaitingWhenTheCallerDoes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Error(w, "no robots.txt", http.StatusNotFound)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(goldRefetchTestPage(goldRefetchTestTitle, goldRefetchTestCaptured())))
	}))
	t.Cleanup(server.Close)

	refetcher := newGoldRefetchTestRefetcher(t, server.URL, t.TempDir(), goldRefetchMaxAge)
	target := goldRefetchTarget{
		ID: rowID(server.URL + "/slow"), URL: server.URL + "/slow",
		CapturedTitle: goldRefetchTestTitle, CapturedText: goldRefetchTestCaptured(),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := refetcher.Measure(ctx, target); ok {
		t.Error("a caller whose context ended was handed a measurement; the row is unmeasured and the screen must say so")
	}

	// The fetch it left running is joined before the test returns -- which is also the
	// proof that a caller walking away did NOT cancel it, so a labeller reloading the
	// tab never costs a second caller their measurement.
	close(release)
	rep, ok := refetcher.Measure(t.Context(), target)
	if !ok || rep.State != fidelitySame {
		t.Errorf("the fetch the cancelled caller started measured %q (ok=%v), want it to have run to completion as %q", rep.State, ok, fidelitySame)
	}
}

// TestGoldRefetchCacheEntryIsAWorkingArtefact pins the sidecar's shape: it names the
// renderer behind the text it measured, so a cached entry cannot be read as describing
// a parse it did not come from.
func TestGoldRefetchCacheEntryIsAWorkingArtefact(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	cacheDir := t.TempDir()
	refetcher := newGoldRefetchTestRefetcher(t, server.URL, cacheDir, goldRefetchMaxAge)
	target := server.target("/same")
	if _, ok := refetcher.Measure(t.Context(), target); !ok {
		t.Fatal("the measurement did not complete")
	}

	raw, err := os.ReadFile(filepath.Join(cacheDir, target.ID+".json"))
	if err != nil {
		t.Fatalf("read the cache sidecar: %v", err)
	}
	var entry goldRefetchCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode the cache sidecar: %v", err)
	}
	if entry.Renderer != parser.RendererStructural {
		t.Errorf("the sidecar names renderer %q, want %q: the measurement and the rendering #288 serves come from ONE parse (ADR-0046)", entry.Renderer, parser.RendererStructural)
	}
	if entry.Bytes != target.ID+".html" {
		t.Errorf("the sidecar names bytes %q, want %q", entry.Bytes, target.ID+".html")
	}
	body, err := os.ReadFile(filepath.Join(cacheDir, entry.Bytes))
	if err != nil {
		t.Fatalf("read the cached bytes: %v", err)
	}
	if !strings.Contains(string(body), goldRefetchTestTitle) {
		t.Errorf("the cached bytes do not carry the page they were fetched from: %s", firstBytes(body))
	}
}

// firstBytes trims a body for a failure message.
func firstBytes(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
