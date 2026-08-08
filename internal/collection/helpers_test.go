package collection_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
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

// fail stubs url to return err (e.g. ErrRobotsDisallowed) from Get.
func (d *fakeDownloader) fail(url string, err error) {
	d.results[url] = getResult{err: err}
}

// requested returns the URLs Get was called with, in order, under the mutex.
func (d *fakeDownloader) requested() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.gotURLs...)
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
// urls, when set, is published as the page's links, so a test can drive the Extract
// Gate's same-host Job Listing link saturation rung.
type fakeParser struct{ urls []string }

func (p fakeParser) Parse(b []byte) (*crawler.Content, error) {
	return &crawler.Content{MainContent: string(b), URLs: p.urls}, nil
}

// openingsIndexParser is an inline parser.Parser whose pages lift the raw bytes into
// MainContent (so identityHash still compares the stubbed body) AND publish a
// structured-data openings index — two JobPosting nodes — so the Extract Gate's
// openings-index reject rung fires at the Process seam. That is a reject rung, not the
// Positive Evidence rung, so a page built with this parser is rejected under every gate
// calibration and the test does not drift when Positive Evidence lands.
type openingsIndexParser struct{}

func (openingsIndexParser) Parse(b []byte) (*crawler.Content, error) {
	return &crawler.Content{
		MainContent: string(b),
		JSONLD: []string{
			`{"@type":"JobPosting","title":"Backend Engineer"}`,
			`{"@type":"JobPosting","title":"Frontend Engineer"}`,
		},
	}, nil
}

// jsonLDParser is an inline parser.Parser that lifts the raw bytes into MainContent
// (so identityHash still compares the stubbed body) AND publishes them as a page with
// a lone structured-data JobPosting, so the heal's structured-data branch is reachable
// at the processor seam.
type jsonLDParser struct{ description string }

func (p jsonLDParser) Parse(b []byte) (*crawler.Content, error) {
	return &crawler.Content{
		MainContent: string(b),
		JSONLD:      []string{`{"@type":"JobPosting","description":"` + p.description + `"}`},
	}, nil
}

// identityHash is the test SourceHash: it returns the content unchanged, so
// "unchanged" means the stubbed page body equals the listing's stored SourceHash.
func identityHash(mainContent string) string { return mainContent }

// fakeClassifier is an inline careerpageprocessor.Confirmer for the page dormancy
// probe's reclassification step. It maps a URL to a canned verdict (still a careers
// page, or not) or a canned error; an unstubbed URL defaults to still-a-careers-page,
// so tests that don't exercise reclassification keep a reachable page Alive.
type fakeClassifier struct {
	mu       sync.Mutex
	verdicts map[string]bool
	errs     map[string]error
	def      bool
	calls    []string
}

func newFakeClassifier() *fakeClassifier {
	return &fakeClassifier{verdicts: map[string]bool{}, errs: map[string]error{}, def: true}
}

func (c *fakeClassifier) Confirm(_ context.Context, url string, _ *crawler.Content) (careerpageprocessor.Verdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, url)
	if err, ok := c.errs[url]; ok {
		return careerpageprocessor.Verdict{}, err
	}
	still, ok := c.verdicts[url]
	if !ok {
		still = c.def
	}
	return careerpageprocessor.Verdict{IsCareerPage: still}, nil
}

// confirmed returns the URLs the classifier was asked about, so a test can assert the
// structural pre-gate took the verdict without ever consulting the LLM.
func (c *fakeClassifier) confirmed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.calls...)
}

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

// descriptionWrite records one UpdateDescription call.
type descriptionWrite struct {
	canonicalURL string
	description  string
	source       crawler.DescriptionSource
}

// fakeDescriptions is an inline crawler.CorpusDescriptionRepository recording every
// heal write, with an optional per-canonical-URL error so a heal failure can be driven.
type fakeDescriptions struct {
	mu     sync.Mutex
	writes []descriptionWrite
	errs   map[string]error
}

func newFakeDescriptions() *fakeDescriptions {
	return &fakeDescriptions{errs: map[string]error{}}
}

func (f *fakeDescriptions) UpdateDescription(_ context.Context, canonicalURL, description string, source crawler.DescriptionSource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[canonicalURL]; ok {
		return err // a failed write records nothing, like a rejected UPDATE
	}
	f.writes = append(f.writes, descriptionWrite{canonicalURL, description, source})
	return nil
}

func (f *fakeDescriptions) recorded() []descriptionWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]descriptionWrite{}, f.writes...)
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
