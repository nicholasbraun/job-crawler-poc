// This file is the impure half of Capture Fidelity (ADR-0047, #287): the fetch that
// the pure rule in goldsetfidelity.go judges. It goes through the crawler's own
// downloader, under the crawler's user agent, wrapped in the refetch lane's
// politeness -- a robots.txt re-check, then one-second spacing per Politeness Domain,
// in that order (ADR-0040) -- so measuring a gold-set row costs a host no more than
// the collection lane already does.
//
// The re-fetched bytes are a WORKING ARTEFACT: they are cached OUTSIDE the
// repository (goldRefetchCacheDir refuses anything else), never committed, and never
// written back onto a gold-set row. A row's stored content is what the tap captured,
// full stop -- goldset-apply remains the only writer of the record (ADR-0048).
//
// A row is fetched once per session: Measure single-flights per row id and reads a
// recent cache entry in preference to the network.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
)

const (
	// goldRefetchSpacing is the minimum gap between fetches sharing a Politeness
	// Domain -- the Frontier's blessed rate for arbitrary web servers, and the same one
	// the refetch lane uses (ADR-0040).
	goldRefetchSpacing = time.Second
	// goldRefetchTimeout is one row's whole budget: dial, robots, spacing, GET, parse.
	goldRefetchTimeout = 20 * time.Second
	// goldRefetchMaxAge re-fetches a cached copy older than this. A cached copy is
	// still a re-fetch taken at some instant, and an aid whose age the labeller is told
	// must not be a year old.
	goldRefetchMaxAge = 24 * time.Hour
	// goldRefetchCacheName is the path, under the OS user cache directory, the
	// re-fetched bytes live at by default.
	goldRefetchCacheName = "llmbench/goldset-refetch"
)

// goldRefetchTarget is one row's side of the comparison. CapturedText is ALREADY
// Flattened Text (crawler.FlattenedText of the row's MainContent, ADR-0046) -- the
// caller derives it, so the refetcher never has to know how a row was rendered.
type goldRefetchTarget struct {
	// ID is the row id (rowID: 12 hex digits of sha256 over the URL). It keys the
	// single-flight AND names the cache files, so it must stay a bare hex handle --
	// every construction site derives it with rowID for exactly that reason.
	ID string
	// URL is the row's source URL, re-fetched verbatim.
	URL string
	// CapturedTitle and CapturedText are the row's stored side of the comparison;
	// CapturedText is ALREADY Flattened Text.
	CapturedTitle string
	CapturedText  string
}

// goldUIRefetcher measures a row's Capture Fidelity. Satisfied in production by
// *goldRefetcher; a test injects one pointed at a local server, and a nil one is the
// -refetch=false session that offers no live view at all.
type goldUIRefetcher interface {
	// Measure returns ok=false ONLY when the measurement did not complete because ctx
	// ended. Everything else -- a dead host, a 404, a non-HTML body, a robots.txt that
	// refuses -- IS a measurement, not a failure.
	Measure(ctx context.Context, t goldRefetchTarget) (goldFidelityReport, bool)

	// Rendering returns the live page's Structural Rendering, re-parsed from the bytes
	// the measurement already cached (#288). It NEVER fetches: a row whose bytes are not
	// in the cache has no rendering, and the caller says so rather than taking a second,
	// unmeasured fetch behind a report that describes the first one.
	Rendering(t goldRefetchTarget) (string, bool)
}

// goldRefetchConfig groups the refetcher's dependencies and settings.
type goldRefetchConfig struct {
	// Downloader fetches the live page. Required.
	Downloader downloader.Downloader
	// Parser re-parses it. Defaults to the structural parser (see newGoldRefetcher).
	Parser *parser.HTMLParser
	// CacheDir holds the re-fetched bytes and their sidecars. Required, and validated
	// as outside the repository by goldRefetchCacheDir before it gets here.
	CacheDir string
	// MaxAge is how old a cached copy may be before it is fetched again; defaults to
	// goldRefetchMaxAge.
	MaxAge time.Duration
	// Timeout bounds one row's whole measurement; defaults to goldRefetchTimeout.
	Timeout time.Duration
}

// goldRefetcher measures Capture Fidelity for a session's rows, at most once per row.
type goldRefetcher struct {
	cfg goldRefetchConfig

	mu      sync.Mutex
	entries map[string]*goldRefetchEntry
}

// goldRefetchEntry is one row's in-flight or finished measurement. ready is closed
// when report is written, so every caller for a row joins the one fetch.
type goldRefetchEntry struct {
	ready  chan struct{}
	report goldFidelityReport
}

var _ goldUIRefetcher = (*goldRefetcher)(nil)

// newGoldRefetcher builds a refetcher over cfg, creating the cache directory.
func newGoldRefetcher(cfg goldRefetchConfig) (*goldRefetcher, error) {
	if cfg.Downloader == nil {
		return nil, errors.New("a refetcher needs a downloader")
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		return nil, errors.New("a refetcher needs a cache directory outside the repository (ADR-0047)")
	}
	if cfg.Parser == nil {
		// Structural unconditionally, and the reason is the round-trip invariant
		// (ADR-0046): crawler.FlattenedText of a Structural Rendering is byte-identical
		// to the flat parse, so the measurement is unchanged, and #288 needs the
		// rendering the same parse produces. One parse serves both, and the two can
		// never disagree about what the live page says.
		cfg.Parser = parser.NewHTMLParser(parser.WithStructuralRendering(true))
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = goldRefetchMaxAge
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = goldRefetchTimeout
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create the re-fetch cache directory: %w", err)
	}
	return &goldRefetcher{cfg: cfg, entries: map[string]*goldRefetchEntry{}}, nil
}

// Measure returns the row's Capture Fidelity, fetching it at most once per session.
func (r *goldRefetcher) Measure(ctx context.Context, t goldRefetchTarget) (goldFidelityReport, bool) {
	r.mu.Lock()
	entry, ok := r.entries[t.ID]
	if !ok {
		entry = &goldRefetchEntry{ready: make(chan struct{})}
		r.entries[t.ID] = entry
		// The fetch owns its own context: a labeller reloading the tab must not cancel a
		// fetch a second caller is already waiting on (the same reasoning as the
		// robots.txt singleflight in internal/robotstxt).
		go func() {
			entry.report = r.measure(t)
			close(entry.ready)
		}()
	}
	r.mu.Unlock()

	select {
	case <-entry.ready:
		return entry.report, true
	case <-ctx.Done():
		return goldFidelityReport{}, false
	}
}

// measure takes the row's one re-fetch and judges it. It never returns an error: a
// dead host and a 404 are measurements, and the report says which.
func (r *goldRefetcher) measure(t goldRefetchTarget) goldFidelityReport {
	if cached, ok := r.readCache(t); ok {
		return cached
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.Timeout)
	defer cancel()

	resp, err := r.cfg.Downloader.Get(ctx, t.URL)
	if err != nil {
		return r.record(t, nil, r.reportForError(err))
	}

	content, perr := r.cfg.Parser.Parse(resp.Content)
	if perr != nil {
		return r.record(t, resp.Content, goldFidelityReport{
			State: fidelityGone, Status: resp.StatusCode,
			Reason: "the live page could not be parsed: " + perr.Error(),
		}.sealed())
	}

	rep := goldFidelityOf(goldFidelityInput{
		Status:        resp.StatusCode,
		CapturedTitle: t.CapturedTitle,
		LiveTitle:     content.Title,
		CapturedText:  t.CapturedText,
		LiveText:      crawler.FlattenedText(content.MainContent),
	})
	return r.record(t, resp.Content, rep)
}

// reportForError classifies a fetch that never produced a page. The order matters:
// robots comes first, because a re-fetch we declined to take is NOT a statement about
// the page and must not be read as one.
func (r *goldRefetcher) reportForError(err error) goldFidelityReport {
	if errors.Is(err, collection.ErrRobotsDisallowed) {
		// No state at all: `gone` is a claim about the page, and we did not look. The
		// zero State leaves LiveView false by construction.
		return goldFidelityReport{
			Reason: "robots.txt disallows a re-fetch of this page; the captured content is all there is",
		}.sealed()
	}
	var statusErr *downloader.StatusError
	if errors.As(err, &statusErr) {
		return goldFidelityOf(goldFidelityInput{Status: statusErr.StatusCode})
	}
	if errors.Is(err, downloader.ErrNoHTML) {
		return goldFidelityReport{
			State:  fidelityGone,
			Reason: "the live response is not HTML, so it is not the page that was captured",
		}.sealed()
	}
	// A host that no longer resolves, refuses the connection or never answers is the
	// strongest form of gone.
	return goldFidelityReport{
		State:  fidelityGone,
		Reason: "the host did not answer: " + err.Error(),
	}.sealed()
}

// record stamps the report with the instant it was taken and writes the cache. A cache
// write that fails leaves the row UNMEASURED (goldRefetchUnrecorded): the cache is what
// a report stands on, so a measurement it did not land in is not one the tool can honour.
func (r *goldRefetcher) record(t goldRefetchTarget, body []byte, rep goldFidelityReport) goldFidelityReport {
	rep.At = goldUINow()

	entry := goldRefetchCacheEntry{
		ID: t.ID, URL: t.URL, FetchedAt: rep.At,
		Renderer: r.cfg.Parser.RendererID(), Report: rep,
	}
	if len(body) > 0 {
		entry.Bytes = t.ID + ".html"
		if err := os.WriteFile(filepath.Join(r.cfg.CacheDir, entry.Bytes), body, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "goldset-ui: cache the re-fetched bytes for %s: %v\n", t.ID, err)
			return goldRefetchUnrecorded(rep.At, err)
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "goldset-ui: encode the re-fetch cache entry for %s: %v\n", t.ID, err)
		return goldRefetchUnrecorded(rep.At, err)
	}
	if err := os.WriteFile(filepath.Join(r.cfg.CacheDir, t.ID+".json"), raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "goldset-ui: cache the re-fetch entry for %s: %v\n", t.ID, err)
		return goldRefetchUnrecorded(rep.At, err)
	}
	return rep
}

// goldRefetchUnrecorded is the report for a re-fetch the cache refused to hold, and it
// is deliberately UNMEASURED -- no state, and therefore no live view.
//
// The cached bytes are what a rendering is re-parsed from (#288) and the sidecar is what
// finds them, so a `same` or `drifted` report no cache entry stands behind advertises a
// page the tool cannot serve: the screen un-hides the frame, GET /render/{id} finds
// nothing, and its advice to reload never re-measures -- Measure single-flights the row
// for the whole session and the warm loop skips a row that already carries a fidelity.
// "Not measured" is the honest answer, it offers no live view, and because nothing was
// cached the next session takes the measurement again. A full disk or a read-only cache
// directory is what gets here.
func goldRefetchUnrecorded(at string, err error) goldFidelityReport {
	return goldFidelityReport{
		At:     at,
		Reason: "the re-fetch could not be cached, so this row is left unmeasured and no page is shown beside its captured text: " + err.Error(),
	}.sealed()
}

// readCache returns a cached measurement for t when one is present, describes the
// SAME url, and is younger than MaxAge.
//
// The report is re-SEALED on the way out, so LiveView is derived from State here exactly
// as it is on a fresh measurement and is never trusted from JSON. A sidecar decoding to
// `{"state":"gone","live_view":true}` -- what a build carrying a different
// goldFidelityGoneBelow writes while the rule itself is being iterated on, and what a
// hand-edited file can say -- would otherwise be honoured for the whole MaxAge window and
// put a withdrawal page in front of the labeller, which is ADR-0047's case that corrupts a
// label rather than merely weakening an aid. sealed only ever withholds, so it is safe on
// every cached shape, the robots refusal that carries no state included.
func (r *goldRefetcher) readCache(t goldRefetchTarget) (goldFidelityReport, bool) {
	entry, ok := r.readCacheEntry(t)
	if !ok {
		return goldFidelityReport{}, false
	}
	return entry.Report.sealed(), true
}

// readCacheEntry reads t's sidecar and applies the guards every reader of the cache
// owes: it must describe the SAME url, it must come from the SAME renderer, it may name
// no bytes but its own, and it must be younger than MaxAge. They live here once so a
// rendering can never describe a different page -- or a staler one -- than the report
// shown beside it.
//
// Every one of them is a defence against a sidecar the refetcher did not write. The URL
// check catches a hand-edited or colliding file measuring the wrong page; the renderer
// check refuses an entry whose text came from a parse this build no longer takes
// (ADR-0046); and pinning Bytes to t.ID+".html" means a sidecar can only ever name its
// own file, so no entry can point Rendering at a path outside the cache.
func (r *goldRefetcher) readCacheEntry(t goldRefetchTarget) (goldRefetchCacheEntry, bool) {
	raw, err := os.ReadFile(filepath.Join(r.cfg.CacheDir, t.ID+".json"))
	if err != nil {
		return goldRefetchCacheEntry{}, false
	}
	var entry goldRefetchCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return goldRefetchCacheEntry{}, false
	}
	if entry.URL != t.URL {
		return goldRefetchCacheEntry{}, false
	}
	if entry.Renderer != r.cfg.Parser.RendererID() {
		return goldRefetchCacheEntry{}, false
	}
	if entry.Bytes != "" && entry.Bytes != t.ID+".html" {
		return goldRefetchCacheEntry{}, false
	}
	fetchedAt, err := time.Parse(time.RFC3339, entry.FetchedAt)
	if err != nil || time.Since(fetchedAt) >= r.cfg.MaxAge {
		return goldRefetchCacheEntry{}, false
	}
	return entry, true
}

// Rendering re-parses the cached bytes with the SAME structural parser the measurement
// used, so what the labeller reads and what Capture Fidelity was computed from are one
// parse of one document (ADR-0046). Re-parsing on demand rather than holding every
// measured page in memory is what bounds the cost: a sitting that confirms 380 rows
// keeps one document, not 380.
//
// ok is false whenever there is nothing to render -- no cache entry, a fetch that
// produced no bytes (a 404, a dead host, a robots.txt refusal), or a parse that failed
// -- and the caller says so rather than fetching again behind the labeller's back.
func (r *goldRefetcher) Rendering(t goldRefetchTarget) (string, bool) {
	entry, ok := r.readCacheEntry(t)
	if !ok || entry.Bytes == "" {
		return "", false
	}
	body, err := os.ReadFile(filepath.Join(r.cfg.CacheDir, entry.Bytes))
	if err != nil {
		return "", false
	}
	content, err := r.cfg.Parser.Parse(body)
	if err != nil {
		return "", false
	}
	return content.MainContent, true
}

// goldRefetchCacheEntry is the sidecar beside the re-fetched bytes. A WORKING
// ARTEFACT (ADR-0047): it lives outside the repository, it is never committed, and
// nothing in it is ever written back onto a gold-set row. Rendering re-parses Bytes to
// put the page on screen (#288); they are still a working artefact and still never
// written back onto a row.
type goldRefetchCacheEntry struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	FetchedAt string `json:"fetched_at"` // RFC3339 UTC
	// Bytes is the basename of the cached HTML beside this file; absent when the
	// fetch produced none (a 404, a dead host, a robots.txt refusal).
	Bytes string `json:"bytes,omitempty"`
	// Renderer is parser.RendererID of the parse behind Report, so a cached entry
	// says which renderer produced the text it measured.
	Renderer string             `json:"renderer"`
	Report   goldFidelityReport `json:"fidelity"`
}

// goldRefetchCacheDir resolves the cache directory and REFUSES one inside any
// repository, or inside the gold-set directory itself. Re-fetched bytes are a working
// artefact: a cache under a tracked tree is a cache somebody commits, and a cache beside
// the record is live text under the directory that holds ground truth (ADR-0047).
//
// The repository is resolved by walking up from the CACHE path, never from goldDir.
// Pointing -dir at a copy of the gold set outside every checkout is the cautious thing a
// labeller does, and it must not be what disarms the guard: judged from goldDir, that
// session finds no repository at all and then accepts a cache path inside this one.
// An empty want takes <user cache dir>/llmbench/goldset-refetch, falling back to the OS
// temp dir when the OS has no user cache directory.
func goldRefetchCacheDir(want, goldDir string) (string, error) {
	cache := want
	if strings.TrimSpace(cache) == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		cache = filepath.Join(base, filepath.FromSlash(goldRefetchCacheName))
	}
	cache, err := filepath.Abs(cache)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", want, err)
	}
	cache = filepath.Clean(cache)

	// repoRootOf walks lexically, so a cache directory that does not exist yet is judged
	// by the ancestors it would be created under.
	if root := repoRootOf(cache); root != "" {
		return "", fmt.Errorf("%s is inside the repository at %s; re-fetched bytes are a working artefact and are never committed (ADR-0047)", cache, root)
	}

	gold, err := filepath.Abs(goldDir)
	if err != nil {
		return "", fmt.Errorf("resolve the gold-set directory %q: %w", goldDir, err)
	}
	gold = filepath.Clean(gold)
	// A gold set outside every repository is still a gold set: nothing the live page said
	// may land under the directory holding the record (ADR-0047).
	if withinDir(gold, cache) {
		return "", fmt.Errorf("%s is inside the gold-set directory at %s; re-fetched bytes are a working artefact and never land beside the record (ADR-0047)", cache, gold)
	}
	return cache, nil
}

// repoRootOf walks up from dir and returns the first directory holding a .git entry,
// or "" when none does.
func repoRootOf(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// withinDir reports whether path is root or sits under it.
func withinDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// newLiveGoldRefetcher builds the re-fetch path the labelling tool uses: the
// crawler's own downloader under the crawler's user agent, wrapped in the refetch
// lane's politeness -- a robots.txt re-check then one-second spacing per Politeness
// Domain, in that order (ADR-0040) -- and the structural parser, so what is measured
// is what #288 renders.
//
// maxTries is 2 with a short backoff: this is an aid on a developer's machine, not a
// crawl, and one blip must not cost the row its rendering, while the retry client's
// default five attempts would blow the per-row budget on a throttling host.
func newLiveGoldRefetcher(cacheDir string) (*goldRefetcher, error) {
	// One transport for pages and robots.txt, as cmd/server wires it: a host resolved
	// for its robots.txt is then a cache hit for its page.
	transport := downloader.NewCachingTransport()
	client := downloader.NewRetryClient(
		downloader.NewClient(userAgent, downloader.WithTransport(transport)),
		downloader.WithMaxTries(2), downloader.WithBackoff(500*time.Millisecond),
	)
	robots := robotstxt.NewChecker(
		temoto.NewRobotsTxtParser(userAgent),
		robotstxt.NewRobotsTxtDownloader(userAgent, transport),
	)
	polite := collection.NewPoliteDownloader(client, robots, atsingest.NewHostLimiter(goldRefetchSpacing), nil)
	return newGoldRefetcher(goldRefetchConfig{Downloader: polite, CacheDir: cacheDir})
}
