package collection_test

import (
	"context"
	"fmt"
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
		GateConfig:        crawler.DefaultLLMGateConfig(),
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
// Alive, so whole-page death reset the counter and never accrued dormancy. The page
// URL is structurally AMBIGUOUS (a /careers sub-page, score 1.0 = uncertain), so the
// verdict falls to the LLM classifier — the only case the LLM decides.
func TestRefetchPageNoLongerClassifiesRecordsDead(t *testing.T) {
	page := uuid.New()
	const url = "https://acme.com/careers/overview"
	dl := newFakeDownloader()
	dl.ok(url, "we are a great place to work") // 200, marketing copy
	classifier := newFakeClassifier()
	classifier.verdicts[url] = false // no longer a careers page

	dorm := &fakeDormancy{}
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          newFakeLiveness(),
		Dormancy:          dorm,
		Classifier:        classifier,
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: url, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	probes := dorm.recorded()
	if len(probes) != 1 || probes[0].outcome != crawler.ProbeDead || probes[0].careerPageID != page {
		t.Fatalf("dormancy probes = %+v, want one Dead for page %v (page no longer classifies)", probes, page)
	}
}

// TestRefetchStructurallyCertainPageStaysAliveDespiteLLM guards the review fix: a
// page discovery certain-accepted on STRUCTURE alone (a /careers hub root) is
// re-classified WITHOUT the LLM, so a repeatable LLM false-negative can never
// deterministically dormant-close it. The classifier is wired to say "no", yet the
// probe must be Alive because the structural pre-gate certain-accepts first.
func TestRefetchStructurallyCertainPageStaysAliveDespiteLLM(t *testing.T) {
	page := uuid.New()
	const url = "https://acme.com/careers" // careerHubRoot → structurally certain
	dl := newFakeDownloader()
	dl.ok(url, "we are a great place to work")
	classifier := newFakeClassifier()
	classifier.verdicts[url] = false // the LLM would (wrongly) say no — must be ignored

	dorm := &fakeDormancy{}
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          newFakeLiveness(),
		Dormancy:          dorm,
		Classifier:        classifier,
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: url, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	probes := dorm.recorded()
	if len(probes) != 1 || probes[0].outcome != crawler.ProbeAlive {
		t.Fatalf("dormancy probes = %+v, want one Alive (structural certain-accept beats the LLM)", probes)
	}
}

// TestRefetchRobotsDisallowedListingDecays is the ADR-0040 decay rule at the
// Process seam: a per-listing refetch that the politeDownloader short-circuits
// with ErrRobotsDisallowed records an Inconclusive crawl probe — NOT Dead — so a
// fresh robots block rides the staleness backstop instead of hard-closing.
func TestRefetchRobotsDisallowedListingDecays(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://acme.com/careers", "hub") // page reachable → not dormant
	live := newFakeLiveness()

	// A non-hub listing URL so it passes the IsHubOrRootURL re-gate and reaches the
	// GET, where the (fake) downloader returns the bare robots sentinel.
	blocked := &crawler.JobListing{CanonicalURL: "c-blocked", URL: "https://acme.com/j/blocked", SourceHash: "x", CompanyKey: "acme.com"}
	dl.fail(blocked.URL, collection.ErrRobotsDisallowed)
	live.open[page] = []*crawler.JobListing{blocked}

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	probes := live.recordedProbes()
	if len(probes) != 1 || probes[0].canonicalURL != "c-blocked" || probes[0].outcome != crawler.ProbeInconclusive {
		t.Fatalf("crawl probes = %+v, want one Inconclusive for c-blocked (robots decay, not Dead)", probes)
	}
}

// TestRefetchRobotsDisallowedPageProbeLingersNonDormant is the ADR-0040
// wholesale-block rule at the Process seam: a page probe the politeDownloader
// short-circuits (wrapped sentinel here, exercising the wrapped classify path)
// folds to an Inconclusive dormancy probe, leaves the page non-dormant, and its
// listings are still refetched — a blocked page path never short-circuits its
// listings' own checks.
func TestRefetchRobotsDisallowedPageProbeLingersNonDormant(t *testing.T) {
	page := uuid.New()
	const pageURL = "https://acme.com/careers"
	dl := newFakeDownloader()
	dl.fail(pageURL, fmt.Errorf("robots: %w", collection.ErrRobotsDisallowed)) // page probe blocked (wrapped)

	// One open listing that refetches cleanly, proving listings still run despite
	// the blocked page probe.
	alive := &crawler.JobListing{CanonicalURL: "c-alive", URL: "https://acme.com/j/alive", SourceHash: "same", CompanyKey: "acme.com"}
	dl.ok(alive.URL, "same") // identityHash unchanged → Alive
	live := newFakeLiveness()
	live.open[page] = []*crawler.JobListing{alive}

	dorm := &fakeDormancy{} // default BecameDormant:false
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          dorm,
		Classifier:        newFakeClassifier(),
		SourceHash:        identityHash,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: pageURL, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	dp := dorm.recorded()
	if len(dp) != 1 || dp[0].outcome != crawler.ProbeInconclusive {
		t.Fatalf("dormancy probes = %+v, want one Inconclusive (blocked page never counts toward dormancy)", dp)
	}
	probes := live.recordedProbes()
	if len(probes) != 1 || probes[0].canonicalURL != "c-alive" || probes[0].outcome != crawler.ProbeAlive {
		t.Fatalf("crawl probes = %+v, want one Alive for c-alive (listings still refetched under a blocked page)", probes)
	}
}
