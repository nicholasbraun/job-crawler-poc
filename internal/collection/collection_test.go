package collection_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// liveRow is a stubCorpus listing with its derived lifecycle state.
type liveRow struct {
	jl     crawler.JobListing
	open   bool
	streak int
}

// stubCorpus is a stateful, in-memory crawler.CorpusLivenessRepository: it drives
// the real NextLiveness reducer through ApplyCrawlProbe so a multi-cycle smoke test
// exercises open→refresh→close→reopen without a database. reSaveByURL models the
// extract stage reopening/advancing a changed (or re-discovered) posting.
type stubCorpus struct {
	mu   sync.Mutex
	rows map[string]*liveRow // keyed by canonical_url
}

func newStubCorpus() *stubCorpus { return &stubCorpus{rows: map[string]*liveRow{}} }

func (c *stubCorpus) add(jl crawler.JobListing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[jl.CanonicalURL] = &liveRow{jl: jl, open: true}
}

func (c *stubCorpus) ListOpen(_ context.Context, page uuid.UUID) ([]*crawler.JobListing, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []*crawler.JobListing{}
	for _, r := range c.rows {
		if r.open && r.jl.CareerPageID == page {
			cp := r.jl
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (c *stubCorpus) CloseAbsent(context.Context, uuid.UUID, time.Time, bool) (int, error) {
	return 0, nil
}

func (c *stubCorpus) ApplyCrawlProbe(_ context.Context, canonicalURL string, outcome crawler.ProbeOutcome, threshold int) (crawler.LifecycleState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rows[canonicalURL]
	next := crawler.NextLiveness(crawler.LifecycleState{Open: r.open, InconclusiveStreak: r.streak}, outcome, true, threshold)
	r.open = next.Open
	r.streak = next.InconclusiveStreak
	return next, nil
}

// reSaveByURL reopens and advances the listing at url, stamping newHash — the
// extract-stage effect a changed/re-discovered page has after re-extraction.
func (c *stubCorpus) reSaveByURL(url, newHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.rows {
		if r.jl.URL == url {
			r.open = true
			r.streak = 0
			r.jl.SourceHash = newHash
			return
		}
	}
}

// closeOpenUnderPage closes every open listing under page, returning the count —
// the dormancy cascade.
func (c *stubCorpus) closeOpenUnderPage(page uuid.UUID) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.rows {
		if r.jl.CareerPageID == page && r.open {
			r.open = false
			n++
		}
	}
	return n
}

func (c *stubCorpus) isOpen(canonicalURL string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows[canonicalURL].open
}

// stubDormancy is a stateful collection.DormancyRecorder driving the real
// NextDormancy/Dormant reducers and cascading a close through the stubCorpus when a
// page tips dormant.
type stubDormancy struct {
	mu       sync.Mutex
	failures map[uuid.UUID]int
	corpus   *stubCorpus
}

func newStubDormancy(c *stubCorpus) *stubDormancy {
	return &stubDormancy{failures: map[uuid.UUID]int{}, corpus: c}
}

func (d *stubDormancy) RecordProbe(_ context.Context, page uuid.UUID, outcome crawler.ProbeOutcome, threshold int) (crawler.DormancyResult, error) {
	d.mu.Lock()
	prev := d.failures[page]
	next, _ := crawler.NextDormancy(prev, time.Time{}, outcome, time.Now())
	became := !crawler.Dormant(prev, threshold) && crawler.Dormant(next, threshold)
	d.failures[page] = next
	d.mu.Unlock()

	closed := 0
	if became {
		closed = d.corpus.closeOpenUnderPage(page)
	}
	return crawler.DormancyResult{ConsecutiveFailures: next, BecameDormant: became, ClosedListings: closed}, nil
}

// TestCollectionRefetchLifecycleAcrossCycles is the run-level smoke test (AC-6): it
// composes the RefetchProcessor over a stateful stub Corpus across two consecutive
// Cycles and asserts the open→refresh→close→reopen lifecycle end to end.
func TestCollectionRefetchLifecycleAcrossCycles(t *testing.T) {
	page := uuid.New()
	corpus := newStubCorpus()

	// Fixture: three open crawl-lane listings under one Career Page.
	corpus.add(crawler.JobListing{CanonicalURL: "c-A", URL: "https://acme.com/j/a", SourceHash: "hA", CareerPageID: page, CompanyKey: "acme.com"})
	corpus.add(crawler.JobListing{CanonicalURL: "c-B", URL: "https://acme.com/j/b", SourceHash: "hB", CareerPageID: page, CompanyKey: "acme.com"})
	corpus.add(crawler.JobListing{CanonicalURL: "c-C", URL: "https://acme.com/j/c", SourceHash: "hC", CareerPageID: page, CompanyKey: "acme.com"})

	// The extract stage effect: a re-extracted page reopens/advances with a new hash.
	dl := newFakeDownloader()
	enqueue := func(_ context.Context, raw *crawler.RawJobListing) error {
		corpus.reSaveByURL(raw.URL.RawURL, raw.Content.MainContent)
		return nil
	}
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          corpus,
		Dormancy:          newStubDormancy(corpus),
		Classifier:        newFakeClassifier(), // reachable page still classifies → Alive
		SourceHash:        identityHash,
		EnqueueExtract:    enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})
	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}

	// --- Cycle 1: A unchanged (alive), B gone (404 → close), C changed (refresh) ---
	dl.ok(seed.URL, "hub") // page reachable
	dl.ok("https://acme.com/j/a", "hA")
	dl.status("https://acme.com/j/b", 404)
	dl.ok("https://acme.com/j/c", "hC-v2")
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if !corpus.isOpen("c-A") {
		t.Error("cycle 1: A should stay open (unchanged, alive)")
	}
	if corpus.isOpen("c-B") {
		t.Error("cycle 1: B should be closed (404)")
	}
	if !corpus.isOpen("c-C") || corpus.rows["c-C"].jl.SourceHash != "hC-v2" {
		t.Error("cycle 1: C should be refreshed (reopened/advanced with the new hash)")
	}

	// The walk lane re-discovers B (still a live posting) and re-extracts it, reopening
	// it in place — the reopen half of the lifecycle.
	corpus.reSaveByURL("https://acme.com/j/b", "hB-v2")
	if !corpus.isOpen("c-B") {
		t.Fatal("reopen: B should be open again after re-discovery")
	}

	// --- Cycle 2: everything unchanged → all alive, no re-extraction ---
	dl.ok("https://acme.com/j/a", "hA")
	dl.ok("https://acme.com/j/b", "hB-v2")
	dl.ok("https://acme.com/j/c", "hC-v2")
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	for _, url := range []string{"c-A", "c-B", "c-C"} {
		if !corpus.isOpen(url) {
			t.Errorf("cycle 2: %s should be open (unchanged, alive)", url)
		}
	}
}

// TestCollectionDormancyTransitionAcrossCycles asserts the Career-Page dormancy
// transition end to end (AC-4/AC-6): a page found hard-dead across enough Cycles
// tips dormant and closes its remaining Open listings, and its listings are no
// longer refetched afterward.
func TestCollectionDormancyTransitionAcrossCycles(t *testing.T) {
	page := uuid.New()
	corpus := newStubCorpus()
	corpus.add(crawler.JobListing{CanonicalURL: "d-1", URL: "https://dead.com/j/1", SourceHash: "h1", CareerPageID: page, CompanyKey: "dead.com"})
	corpus.add(crawler.JobListing{CanonicalURL: "d-2", URL: "https://dead.com/j/2", SourceHash: "h2", CareerPageID: page, CompanyKey: "dead.com"})

	const threshold = 2
	dl := newFakeDownloader()
	dl.status("https://dead.com/careers", 404) // page hard-dead every cycle
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          corpus,
		Dormancy:          newStubDormancy(corpus),
		Classifier:        newFakeClassifier(), // page 404s → classifier not consulted
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: threshold,
	})
	seed := &crawler.CollectionSeed{URL: "https://dead.com/careers", CompanyKey: "dead.com", CareerPageID: page}

	// Cycle 1: first hard-dead probe — not yet dormant, listings stay open (the page
	// 404 does not itself close them; only the dormant transition does).
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if !corpus.isOpen("d-1") || !corpus.isOpen("d-2") {
		t.Fatal("cycle 1: listings should stay open before the page is dormant")
	}

	// Cycle 2: second hard-dead probe tips the page dormant → cascade closes both.
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if corpus.isOpen("d-1") || corpus.isOpen("d-2") {
		t.Error("cycle 2: a dormant page must close its remaining open listings")
	}
}

// TestCollectionNonClassifyingPageGoesDormantAcrossCycles is the #190 regression
// end-to-end: a Career Page that keeps returning 200 but NO LONGER classifies as a
// careers page (redesigned into a marketing/landing page listing no jobs) is
// whole-page death. Across enough Cycles it must accrue dormancy through the real
// NextDormancy reducer and eventually cascade its remaining Open listings closed —
// exactly as a 404 board does. Before the fix a reachable 200 always probed Alive, so
// the counter reset every Cycle and the stale listings never closed.
func TestCollectionNonClassifyingPageGoesDormantAcrossCycles(t *testing.T) {
	page := uuid.New()
	corpus := newStubCorpus()
	corpus.add(crawler.JobListing{CanonicalURL: "m-1", URL: "https://redesign.com/j/1", SourceHash: "h1", CareerPageID: page, CompanyKey: "redesign.com"})
	corpus.add(crawler.JobListing{CanonicalURL: "m-2", URL: "https://redesign.com/j/2", SourceHash: "h2", CareerPageID: page, CompanyKey: "redesign.com"})

	const threshold = 2
	dl := newFakeDownloader()
	dl.ok("https://redesign.com/careers", "we are a great place to work") // 200 every cycle
	classifier := newFakeClassifier()
	classifier.verdicts["https://redesign.com/careers"] = false // but no longer a careers page
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          corpus,
		Dormancy:          newStubDormancy(corpus),
		Classifier:        classifier,
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: threshold,
	})
	seed := &crawler.CollectionSeed{URL: "https://redesign.com/careers", CompanyKey: "redesign.com", CareerPageID: page}

	// Cycle 1: first hard-dead (no-longer-classifies) probe — not yet dormant, listings
	// stay open. A reachable page that still classified would reset here and never dorm.
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if !corpus.isOpen("m-1") || !corpus.isOpen("m-2") {
		t.Fatal("cycle 1: listings should stay open before the page is dormant")
	}

	// Cycle 2: second no-longer-classifies probe tips the page dormant → cascade closes both.
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if corpus.isOpen("m-1") || corpus.isOpen("m-2") {
		t.Error("cycle 2: a page that no longer classifies must go dormant and close its listings")
	}
}
