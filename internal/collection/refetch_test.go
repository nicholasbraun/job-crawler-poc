package collection_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// TestRefetchPerListingLiveness drives the crawl-lane refetch outcomes (ADR-0035):
// 404 → Dead, unchanged 200 → Alive with no re-extraction, changed 200 → enqueue
// with no probe, transient error → Inconclusive.
func TestRefetchPerListingLiveness(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	live := newFakeLiveness()
	extract := &captureExtract{}

	// The page itself is reachable, so it never goes dormant this probe.
	dl.ok("https://acme.com/careers", "hub")

	// Four open listings, one per outcome.
	dead := &crawler.JobListing{CanonicalURL: "c-dead", URL: "https://acme.com/j/dead", SourceHash: "old", CompanyKey: "acme.com"}
	unchanged := &crawler.JobListing{CanonicalURL: "c-unchanged", URL: "https://acme.com/j/unchanged", SourceHash: "same-body", CompanyKey: "acme.com"}
	changed := &crawler.JobListing{CanonicalURL: "c-changed", URL: "https://acme.com/j/changed", SourceHash: "old-body", CompanyKey: "acme.com"}
	transient := &crawler.JobListing{CanonicalURL: "c-transient", URL: "https://acme.com/j/transient", SourceHash: "old", CompanyKey: "acme.com"}
	live.open[page] = []*crawler.JobListing{dead, unchanged, changed, transient}

	dl.status(dead.URL, 404)
	dl.ok(unchanged.URL, "same-body") // identityHash(body) == stored SourceHash
	dl.ok(changed.URL, "new-body")    // differs from stored
	dl.status(transient.URL, 503)

	var refreshed, closed int
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{}, // Alive result (BecameDormant=false)
		Classifier:        newFakeClassifier(),
		SourceHash:        identityHash,
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
		OnRefreshed:       func(context.Context) { refreshed++ },
		OnClosed:          func(_ context.Context, n int) { closed += n },
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Probe outcomes: dead→Dead, unchanged→Alive, transient→Inconclusive; changed is
	// NOT probed (it re-extracts instead).
	want := map[string]crawler.ProbeOutcome{
		dead.CanonicalURL:      crawler.ProbeDead,
		unchanged.CanonicalURL: crawler.ProbeAlive,
		transient.CanonicalURL: crawler.ProbeInconclusive,
	}
	probes := live.recordedProbes()
	if len(probes) != len(want) {
		t.Fatalf("recorded %d probes, want %d: %+v", len(probes), len(want), probes)
	}
	for _, p := range probes {
		if want[p.canonicalURL] != p.outcome {
			t.Errorf("probe %q outcome = %v, want %v", p.canonicalURL, p.outcome, want[p.canonicalURL])
		}
	}

	// The changed page is enqueued for re-extraction, carrying its Owner.
	caps := extract.captured()
	if len(caps) != 1 {
		t.Fatalf("enqueued %d for re-extraction, want 1 (only the changed page)", len(caps))
	}
	if caps[0].URL.RawURL != changed.URL || caps[0].URL.Owner != "acme.com" {
		t.Errorf("re-extract raw = %+v, want changed URL with Owner acme.com", caps[0].URL)
	}
	if refreshed != 1 {
		t.Errorf("OnRefreshed = %d, want 1", refreshed)
	}
	if closed != 0 {
		t.Errorf("OnClosed = %d, want 0 (no dormancy this Cycle)", closed)
	}
}

// TestRefetchClosesReGatedListing asserts the crawl-lane refetch self-heals: a
// known-open listing whose URL the extract gate now rejects on structure alone (a
// bare/locale root or a terminal jobs-index segment) is closed (Dead) with NO
// network call, rather than refetched or re-extracted. Neither URL is configured in
// the downloader, so a GET would be a transient error -> Inconclusive; a recorded
// Dead proves the re-gate short-circuited before the GET.
func TestRefetchClosesReGatedListing(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	live := newFakeLiveness()
	extract := &captureExtract{}

	dl.ok("https://acme.com/careers", "hub") // the page probe reaches (not dormant)

	root := &crawler.JobListing{CanonicalURL: "c-root", URL: "https://acme.com/", CompanyKey: "acme.com"}
	index := &crawler.JobListing{CanonicalURL: "c-index", URL: "https://acme.com/job-offers", CompanyKey: "acme.com"}
	live.open[page] = []*crawler.JobListing{root, index}

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		SourceHash:        identityHash,
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	want := map[string]crawler.ProbeOutcome{
		root.CanonicalURL:  crawler.ProbeDead,
		index.CanonicalURL: crawler.ProbeDead,
	}
	probes := live.recordedProbes()
	if len(probes) != len(want) {
		t.Fatalf("recorded %d probes, want %d: %+v", len(probes), len(want), probes)
	}
	for _, p := range probes {
		if want[p.canonicalURL] != p.outcome {
			t.Errorf("re-gated %q outcome = %v, want Dead", p.canonicalURL, p.outcome)
		}
	}
	if caps := extract.captured(); len(caps) != 0 {
		t.Errorf("re-gated listings must not re-extract, got %d enqueued", len(caps))
	}
}

// TestRefetchDormantPageSkipsRefetch asserts a page that tips dormant on its probe
// closes its listings via the cascade and is NOT refetched (ADR-0035).
func TestRefetchDormantPageSkipsRefetch(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.status("https://dead.com/careers", 404) // page 404 → Dead probe
	live := newFakeLiveness()
	live.open[page] = []*crawler.JobListing{
		{CanonicalURL: "c1", URL: "https://dead.com/j/1", SourceHash: "x"},
	}
	extract := &captureExtract{}
	var closed int
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{result: crawler.DormancyResult{BecameDormant: true, ClosedListings: 4}},
		Classifier:        newFakeClassifier(),
		SourceHash:        identityHash,
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
		OnClosed:          func(_ context.Context, n int) { closed += n },
	})

	seed := &crawler.CollectionSeed{URL: "https://dead.com/careers", CompanyKey: "dead.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := len(live.recordedProbes()); got != 0 {
		t.Errorf("a dormant page must not refetch its listings, got %d per-listing probes", got)
	}
	if len(extract.captured()) != 0 {
		t.Errorf("a dormant page must not re-extract, got %d", len(extract.captured()))
	}
	if closed != 4 {
		t.Errorf("OnClosed = %d, want 4 (the dormancy cascade count)", closed)
	}
}

// TestRefetchRecordsPageDormancyOutcome asserts the page probe classification feeds
// dormancy: a reachable page records Alive.
func TestRefetchRecordsPageDormancyOutcome(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://live.com/careers", "hub")
	live := newFakeLiveness()
	dorm := &fakeDormancy{}
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          dorm,
		Classifier:        newFakeClassifier(), // reachable page still classifies → Alive
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: "https://live.com/careers", CompanyKey: "live.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	probes := dorm.recorded()
	if len(probes) != 1 || probes[0].outcome != crawler.ProbeAlive || probes[0].careerPageID != page {
		t.Fatalf("dormancy probes = %+v, want one Alive for page %v", probes, page)
	}
}

// TestRefetchPageNoLongerClassifiesRecordsDead is the #190 regression at the
// processor seam: a reachable 200-OK Career Page that no longer classifies as a
// careers page (redesigned into a marketing/landing page listing no jobs) must record
// a Dead dormancy probe, not Alive. Before the fix a reachable 200 always mapped to
// Alive, so whole-page death reset the counter and never accrued dormancy.
func TestRefetchPageNoLongerClassifiesRecordsDead(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://acme.com/careers", "we are a great place to work") // 200, marketing copy
	classifier := newFakeClassifier()
	classifier.verdicts["https://acme.com/careers"] = false // no longer a careers page

	dorm := &fakeDormancy{}
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          newFakeLiveness(),
		Dormancy:          dorm,
		Classifier:        classifier,
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	probes := dorm.recorded()
	if len(probes) != 1 || probes[0].outcome != crawler.ProbeDead || probes[0].careerPageID != page {
		t.Fatalf("dormancy probes = %+v, want one Dead for page %v (page no longer classifies)", probes, page)
	}
}
