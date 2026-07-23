package collection_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

// errNotConfigured is returned by fakeDownloader for a URL the test did not stub,
// so an unconfigured Get surfaces as a (non-status) Inconclusive probe rather than
// an accidental Alive/Dead — a plain misconfiguration signal.
var errNotConfigured = errors.New("fake downloader: url not configured")

// getResult is one stubbed Downloader outcome: a body (a 200), or an error.
type getResult struct {
	body []byte
	err  error
}

// fakeDownloader is an inline downloader.Downloader keyed by URL. Missing URLs
// return errNotConfigured. It records the URLs it was asked for.
type fakeDownloader struct {
	mu      sync.Mutex
	results map[string]getResult
	gotURLs []string
}

func newFakeDownloader() *fakeDownloader {
	return &fakeDownloader{results: map[string]getResult{}}
}

func (d *fakeDownloader) status(url string, code int) {
	d.results[url] = getResult{err: &downloader.StatusError{StatusCode: code}}
}

func (d *fakeDownloader) ok(url, body string) {
	d.results[url] = getResult{body: []byte(body)}
}

func (d *fakeDownloader) Get(_ context.Context, url string) (*downloader.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gotURLs = append(d.gotURLs, url)
	r, ok := d.results[url]
	if !ok {
		return nil, errNotConfigured
	}
	if r.err != nil {
		return nil, r.err
	}
	return &downloader.Response{StatusCode: 200, Content: r.body}, nil
}

// fakeParser is an inline parser.Parser that lifts the raw bytes verbatim into
// MainContent, so a test's SourceHash (identityHash) compares the exact stubbed body.
type fakeParser struct{}

func (fakeParser) Parse(b []byte) (*crawler.Content, error) {
	return &crawler.Content{MainContent: string(b)}, nil
}

// identityHash is the test SourceHash: it returns the content unchanged, so
// "unchanged" means the stubbed page body equals the listing's stored SourceHash.
func identityHash(mainContent string) string { return mainContent }

// crawlProbe records one ApplyCrawlProbe invocation.
type crawlProbe struct {
	canonicalURL string
	outcome      crawler.ProbeOutcome
}

// fakeLiveness is an inline crawler.CorpusLivenessRepository: ListOpen serves a
// canned per-page slice, ApplyCrawlProbe records each call, CloseAbsent records the
// board-sweep count it was told to close.
type fakeLiveness struct {
	mu         sync.Mutex
	open       map[uuid.UUID][]*crawler.JobListing
	probes     []crawlProbe
	closeCalls int
}

func newFakeLiveness() *fakeLiveness {
	return &fakeLiveness{open: map[uuid.UUID][]*crawler.JobListing{}}
}

func (f *fakeLiveness) ListOpen(_ context.Context, careerPageID uuid.UUID) ([]*crawler.JobListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[careerPageID], nil
}

func (f *fakeLiveness) CloseAbsent(_ context.Context, _ uuid.UUID, _ time.Time, _ bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return 0, nil
}

func (f *fakeLiveness) ApplyCrawlProbe(_ context.Context, canonicalURL string, outcome crawler.ProbeOutcome, _ int) (crawler.LifecycleState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, crawlProbe{canonicalURL, outcome})
	return crawler.LifecycleState{Open: outcome != crawler.ProbeDead}, nil
}

func (f *fakeLiveness) recordedProbes() []crawlProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]crawlProbe{}, f.probes...)
}

// dormProbe records one dormancy RecordProbe invocation.
type dormProbe struct {
	careerPageID uuid.UUID
	outcome      crawler.ProbeOutcome
}

// fakeDormancy is an inline collection.DormancyRecorder recording each probe and
// returning a canned DormancyResult (used to force the dormant transition).
type fakeDormancy struct {
	mu     sync.Mutex
	probes []dormProbe
	result crawler.DormancyResult
}

func (f *fakeDormancy) RecordProbe(_ context.Context, careerPageID uuid.UUID, outcome crawler.ProbeOutcome, _ int) (crawler.DormancyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, dormProbe{careerPageID, outcome})
	return f.result, nil
}

func (f *fakeDormancy) recorded() []dormProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dormProbe{}, f.probes...)
}

// captureExtract records the RawJobListings enqueued for re-extraction.
type captureExtract struct {
	mu   sync.Mutex
	raws []*crawler.RawJobListing
}

func (c *captureExtract) enqueue(_ context.Context, raw *crawler.RawJobListing) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raws = append(c.raws, raw)
	return nil
}

func (c *captureExtract) captured() []*crawler.RawJobListing {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*crawler.RawJobListing{}, c.raws...)
}
