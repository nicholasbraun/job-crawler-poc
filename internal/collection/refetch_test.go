package collection_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// TestRefetchPerListingLiveness drives the crawl-lane refetch outcomes (ADR-0035):
// 404 → Dead, unchanged 200 → Alive with no re-extraction, changed 200 → enqueue
// with no probe, transient error → Inconclusive. The dead and changed listings carry
// the LEGACY description marker to pin that neither branch heals (ADR-0041): a dead
// posting has nothing to heal, and a changed page is re-extracted, whose Save stamps a
// real body and marker.
//
// The changed listing sits on a posting-shaped URL under the live GateConfig because
// this test asserts the LANE's branch logic: its page must be one the Extract Gate
// admits on its own merits, not one that only survives because nothing rejected it
// (ADR-0044).
func TestRefetchPerListingLiveness(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	live := newFakeLiveness()
	extract := &captureExtract{}
	heal := newFakeDescriptions()

	// The page itself is reachable, so it never goes dormant this probe.
	dl.ok("https://acme.com/careers", "hub")

	// Four open listings, one per outcome.
	dead := &crawler.JobListing{CanonicalURL: "c-dead", URL: "https://acme.com/j/dead", SourceHash: "old", CompanyKey: "acme.com",
		DescriptionSource: crawler.DescriptionSourceLLMSummary}
	unchanged := &crawler.JobListing{CanonicalURL: "c-unchanged", URL: "https://acme.com/j/unchanged", SourceHash: "same-body", CompanyKey: "acme.com"}
	changed := &crawler.JobListing{CanonicalURL: "c-changed", URL: "https://acme.com/jobs/senior-go-engineer", SourceHash: "old-body", CompanyKey: "acme.com",
		DescriptionSource: crawler.DescriptionSourceLLMSummary}
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
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      heal,
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
	// Neither the dead nor the changed listing is healed, despite both being legacy.
	if writes := heal.recorded(); len(writes) != 0 {
		t.Errorf("the dead and changed branches must not heal, got %d writes: %+v", len(writes), writes)
	}
}

// TestRefetchHealsLegacyDescription is the ADR-0041 heal at the Process seam: on the
// unchanged-content branch, a listing still marked with the legacy model-authored
// source has its description and marker rewritten from the page the refetch already
// downloaded and parsed — while an already-healed listing and an ATS-lane listing are
// left alone. The ATS case is defence in depth: the loop filters on the marker alone, so
// an ats_board row placed in it directly (as here) must survive untouched, even though
// no live path delivers one. The whole heal runs with NO model call: the
// extract enqueue fails the test if invoked, and the page URL is structurally certain so
// the dormancy probe never consults the classifier either.
func TestRefetchHealsLegacyDescription(t *testing.T) {
	page := uuid.New()
	const pageURL = "https://acme.com/careers" // careerHubRoot → structurally certain
	dl := newFakeDownloader()
	dl.ok(pageURL, "hub")
	live := newFakeLiveness()
	heal := newFakeDescriptions()
	classifier := newFakeClassifier()

	legacy := &crawler.JobListing{CanonicalURL: "c-legacy", URL: "https://acme.com/j/legacy", SourceHash: "legacy body text",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourceLLMSummary}
	healed := &crawler.JobListing{CanonicalURL: "c-healed", URL: "https://acme.com/j/healed", SourceHash: "healed body text",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourcePageContent}
	board := &crawler.JobListing{CanonicalURL: "c-board", URL: "https://acme.com/j/board", SourceHash: "board body text",
		CompanyKey: "acme.com", Source: crawler.SourceLaneATS, DescriptionSource: crawler.DescriptionSourceATSBoard}
	live.open[page] = []*crawler.JobListing{legacy, healed, board}

	// Every page body equals the stored SourceHash under identityHash → unchanged.
	for _, l := range live.open[page] {
		dl.ok(l.URL, l.SourceHash)
	}

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader: dl,
		Parser:     fakeParser{},
		Liveness:   live,
		Dormancy:   &fakeDormancy{},
		Classifier: classifier,
		GateConfig: crawler.DefaultLLMGateConfig(),
		SourceHash: identityHash,
		EnqueueExtract: func(_ context.Context, raw *crawler.RawJobListing) error {
			t.Errorf("the heal must make no LLM call: %q was enqueued for re-extraction", raw.URL.RawURL)
			return nil
		},
		Descriptions:      heal,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: pageURL, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	writes := heal.recorded()
	if len(writes) != 1 {
		t.Fatalf("healed %d listings, want exactly 1 (only the legacy one): %+v", len(writes), writes)
	}
	if writes[0].canonicalURL != legacy.CanonicalURL {
		t.Errorf("healed %q, want the legacy listing %q", writes[0].canonicalURL, legacy.CanonicalURL)
	}
	if writes[0].description != "legacy body text" {
		t.Errorf("healed body = %q, want the page's own text", writes[0].description)
	}
	if writes[0].source != crawler.DescriptionSourcePageContent {
		t.Errorf("healed marker = %q, want %q", writes[0].source, crawler.DescriptionSourcePageContent)
	}

	// All three stay alive: the heal rides alongside liveness, it never replaces it.
	probes := live.recordedProbes()
	if len(probes) != 3 {
		t.Fatalf("recorded %d probes, want 3 (one Alive per unchanged listing): %+v", len(probes), probes)
	}
	for _, p := range probes {
		if p.outcome != crawler.ProbeAlive {
			t.Errorf("probe %q outcome = %v, want Alive", p.canonicalURL, p.outcome)
		}
	}
	if calls := classifier.confirmed(); len(calls) != 0 {
		t.Errorf("the heal Cycle must make no classify call, got %v", calls)
	}
}

// TestRefetchHealUsesStructuredDataWhenPagePublishesIt pins that the heal derives the
// body with crawler.PostingBody rather than blindly storing main content: a page
// publishing a lone structured-data JobPosting heals from THAT description — stripped of
// HTML, entity-unescaped and whitespace-collapsed — and records the structured_data
// marker, so the heal and the save path share one derivation.
func TestRefetchHealUsesStructuredDataWhenPagePublishesIt(t *testing.T) {
	page := uuid.New()
	const pageURL = "https://acme.com/careers"
	dl := newFakeDownloader()
	dl.ok(pageURL, "hub")
	live := newFakeLiveness()
	heal := newFakeDescriptions()

	legacy := &crawler.JobListing{CanonicalURL: "c-legacy", URL: "https://acme.com/j/legacy", SourceHash: "page chrome",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourceLLMSummary}
	live.open[page] = []*crawler.JobListing{legacy}
	dl.ok(legacy.URL, "page chrome") // unchanged under identityHash

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            jsonLDParser{description: "We run <b>Kubernetes</b>&nbsp;clusters."},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      heal,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: pageURL, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	writes := heal.recorded()
	if len(writes) != 1 {
		t.Fatalf("healed %d listings, want 1: %+v", len(writes), writes)
	}
	if want := "We run Kubernetes clusters."; writes[0].description != want {
		t.Errorf("healed body = %q, want the structured posting's text %q", writes[0].description, want)
	}
	if writes[0].source != crawler.DescriptionSourceStructuredData {
		t.Errorf("healed marker = %q, want %q", writes[0].source, crawler.DescriptionSourceStructuredData)
	}
}

// TestRefetchHealFailureDoesNotAbortRemainingListings asserts a per-listing heal
// failure is joined with the page's other errors rather than dropping the remaining
// postings, and that liveness is unaffected: both listings still record Alive.
func TestRefetchHealFailureDoesNotAbortRemainingListings(t *testing.T) {
	page := uuid.New()
	const pageURL = "https://acme.com/careers"
	dl := newFakeDownloader()
	dl.ok(pageURL, "hub")
	live := newFakeLiveness()
	heal := newFakeDescriptions()

	first := &crawler.JobListing{CanonicalURL: "c-first", URL: "https://acme.com/j/first", SourceHash: "first body",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourceLLMSummary}
	second := &crawler.JobListing{CanonicalURL: "c-second", URL: "https://acme.com/j/second", SourceHash: "second body",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourceLLMSummary}
	live.open[page] = []*crawler.JobListing{first, second}
	dl.ok(first.URL, first.SourceHash)
	dl.ok(second.URL, second.SourceHash)
	heal.errs[first.CanonicalURL] = errors.New("boom")

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      heal,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: pageURL, CompanyKey: "acme.com", CareerPageID: page}
	err := proc.Process(t.Context(), seed)
	if err == nil {
		t.Fatal("Process: want the heal failure reported, got nil")
	}
	if !strings.Contains(err.Error(), first.CanonicalURL) {
		t.Errorf("Process error = %v, want it to name the failing listing %q", err, first.CanonicalURL)
	}

	writes := heal.recorded()
	if len(writes) != 1 || writes[0].canonicalURL != second.CanonicalURL {
		t.Fatalf("healed %+v, want the second listing healed despite the first failing", writes)
	}
	probes := live.recordedProbes()
	if len(probes) != 2 {
		t.Fatalf("recorded %d probes, want 2 (liveness is unaffected by a heal failure): %+v", len(probes), probes)
	}
	for _, p := range probes {
		if p.outcome != crawler.ProbeAlive {
			t.Errorf("probe %q outcome = %v, want Alive", p.canonicalURL, p.outcome)
		}
	}
}

// TestRefetchHealSkipsEmptyParse asserts a page that parses to nothing never trades a
// stale summary for an empty description (and an empty weight-B index entry): no write,
// and the listing still records Alive so a later Cycle with a real parse can heal it.
func TestRefetchHealSkipsEmptyParse(t *testing.T) {
	page := uuid.New()
	const pageURL = "https://acme.com/careers"
	dl := newFakeDownloader()
	dl.ok(pageURL, "hub")
	live := newFakeLiveness()
	heal := newFakeDescriptions()

	// Stored hash "" and an empty body: the unchanged branch is reached with no content.
	empty := &crawler.JobListing{CanonicalURL: "c-empty", URL: "https://acme.com/j/empty", SourceHash: "",
		CompanyKey: "acme.com", Source: crawler.SourceLaneCrawl, DescriptionSource: crawler.DescriptionSourceLLMSummary}
	live.open[page] = []*crawler.JobListing{empty}
	dl.ok(empty.URL, "")

	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      heal,
		EnqueueExtract:    (&captureExtract{}).enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
	})

	seed := &crawler.CollectionSeed{URL: pageURL, CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if writes := heal.recorded(); len(writes) != 0 {
		t.Errorf("an empty parse must not heal, got %+v", writes)
	}
	probes := live.recordedProbes()
	if len(probes) != 1 || probes[0].outcome != crawler.ProbeAlive {
		t.Fatalf("crawl probes = %+v, want one Alive (liveness is unaffected by the skipped heal)", probes)
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

// TestRefetchChangedPageFailingTheExtractGateIsNotReExtracted closes the hole
// ADR-0044 names: the changed-content path used to enqueue straight to the extractor,
// the one caller that never consulted the Extract Gate, so a hub already saved as a Job
// Listing was re-extracted and re-saved every time its page changed. The page here
// publishes a structured-data openings index, which the gate rejects — so nothing is
// enqueued, and (the load-bearing half) nothing about the listing moves either: no
// probe, no Close, no Refreshed.
func TestRefetchChangedPageFailingTheExtractGateIsNotReExtracted(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://acme.com/careers", "hub") // the page probe reaches (not dormant)
	live := newFakeLiveness()
	extract := &captureExtract{}

	hub := &crawler.JobListing{CanonicalURL: "c-hub", URL: "https://acme.com/jobs/berlin",
		SourceHash: "old-body", CompanyKey: "acme.com"}
	live.open[page] = []*crawler.JobListing{hub}
	dl.ok(hub.URL, "new-body") // differs from the stored hash → the changed branch

	var refreshed, rejected int
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            openingsIndexParser{}, // two JobPosting nodes → gate rung 6 rejects
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      newFakeDescriptions(),
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
		OnRefreshed:       func(context.Context) { refreshed++ },
		OnRegateRejected:  func(context.Context) { rejected++ },
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if caps := extract.captured(); len(caps) != 0 {
		t.Errorf("a changed page the Extract Gate rejects must not re-extract, got %d enqueued: %+v", len(caps), caps)
	}
	if refreshed != 0 {
		t.Errorf("OnRefreshed = %d, want 0 (nothing was re-enqueued)", refreshed)
	}
	if probes := live.recordedProbes(); len(probes) != 0 {
		t.Errorf("the re-gate must not touch Liveness, got %d probes: %+v", len(probes), probes)
	}
	if rejected != 1 {
		t.Errorf("OnRegateRejected = %d, want 1", rejected)
	}
}

// TestRefetchChangedPagePassingTheExtractGateIsReExtracted is the other half of
// ADR-0044's gate: a changed page that still reads as a single posting is re-enqueued
// exactly as before — byte-identical raw URL and Owner, since the save path derives the
// Corpus identity from that stored RawURL and a re-normalized one risks minting a
// duplicate row instead of updating one.
func TestRefetchChangedPagePassingTheExtractGateIsReExtracted(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://acme.com/careers", "hub")
	live := newFakeLiveness()
	extract := &captureExtract{}

	posting := &crawler.JobListing{CanonicalURL: "c-posting", URL: "https://acme.com/jobs/senior-go-engineer",
		SourceHash: "old-body", CompanyKey: "acme.com"}
	live.open[page] = []*crawler.JobListing{posting}
	dl.ok(posting.URL, "new-body")

	var refreshed, rejected int
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            fakeParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      newFakeDescriptions(),
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
		OnRefreshed:       func(context.Context) { refreshed++ },
		OnRegateRejected:  func(context.Context) { rejected++ },
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	caps := extract.captured()
	if len(caps) != 1 {
		t.Fatalf("enqueued %d for re-extraction, want 1", len(caps))
	}
	if caps[0].URL.RawURL != posting.URL || caps[0].URL.Owner != "acme.com" {
		t.Errorf("re-extract raw = %+v, want the stored URL %q with Owner acme.com", caps[0].URL, posting.URL)
	}
	if refreshed != 1 {
		t.Errorf("OnRefreshed = %d, want 1", refreshed)
	}
	if probes := live.recordedProbes(); len(probes) != 0 {
		t.Errorf("the changed branch applies no probe, got %+v", probes)
	}
	if rejected != 0 {
		t.Errorf("OnRegateRejected = %d, want 0 (the gate admitted the page)", rejected)
	}
}

// TestRefetchCountsAListingTheGateWouldRejectWithoutClosingIt is the counter's headline
// contract (ADR-0044): an Open Job Listing whose page the Extract Gate would now reject
// on full content is counted, and NOTHING else happens to it. The page is unchanged, so
// it still records exactly one ProbeAlive — not Dead (which would be the bulk
// destructive pass this ticket defers) and not Inconclusive (which would tick the
// staleness backstop and Close it by the back door).
func TestRefetchCountsAListingTheGateWouldRejectWithoutClosingIt(t *testing.T) {
	page := uuid.New()
	dl := newFakeDownloader()
	dl.ok("https://acme.com/careers", "hub")
	live := newFakeLiveness()
	heal := newFakeDescriptions()
	extract := &captureExtract{}

	// DescriptionSource is left at its zero value, so the heal is inert and the only
	// effect under test is the probe.
	hub := &crawler.JobListing{CanonicalURL: "c-hub", URL: "https://acme.com/jobs/berlin",
		SourceHash: "hub body", CompanyKey: "acme.com"}
	live.open[page] = []*crawler.JobListing{hub}
	dl.ok(hub.URL, "hub body") // identityHash(body) == stored SourceHash → unchanged

	var rejected int
	proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
		Downloader:        dl,
		Parser:            openingsIndexParser{},
		Liveness:          live,
		Dormancy:          &fakeDormancy{},
		Classifier:        newFakeClassifier(),
		GateConfig:        crawler.DefaultLLMGateConfig(),
		SourceHash:        identityHash,
		Descriptions:      heal,
		EnqueueExtract:    extract.enqueue,
		StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
		DormancyThreshold: crawler.DefaultPageDormancyThreshold,
		OnRegateRejected:  func(context.Context) { rejected++ },
	})

	seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), seed); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if rejected != 1 {
		t.Errorf("OnRegateRejected = %d, want 1", rejected)
	}
	probes := live.recordedProbes()
	if len(probes) != 1 || probes[0].canonicalURL != hub.CanonicalURL || probes[0].outcome != crawler.ProbeAlive {
		t.Fatalf("crawl probes = %+v, want exactly one Alive for %q (the counter Closes nothing)", probes, hub.CanonicalURL)
	}
	if caps := extract.captured(); len(caps) != 0 {
		t.Errorf("an unchanged page never re-extracts, got %d enqueued", len(caps))
	}
	if writes := heal.recorded(); len(writes) != 0 {
		t.Errorf("a non-legacy listing must not be healed, got %+v", writes)
	}
}

// TestRefetchReGateReadsTheLaneGateConfig proves the re-gate verdict comes from the
// lane's OWN GateConfig — the same value cmd/server hands the walk's url processor — and
// not from a hardcoded default, so the two lanes cannot calibrate apart (ADR-0044). The
// same page and URL flip from re-extracted to counted purely by moving the Extract
// Gate's job-link saturation count across the one distinct same-host posting link the
// page publishes.
func TestRefetchReGateReadsTheLaneGateConfig(t *testing.T) {
	tests := []struct {
		name         string
		cfg          crawler.LLMGateConfig
		wantEnqueued int
		wantRejected int
	}{
		{
			name:         "saturation count of 1 rejects the page",
			cfg:          crawler.LLMGateConfig{ExtractJobLinkSaturationCount: 1},
			wantEnqueued: 0,
			wantRejected: 1,
		},
		{
			name:         "saturation count of 0 leaves the rung silent",
			cfg:          crawler.LLMGateConfig{ExtractJobLinkSaturationCount: 0},
			wantEnqueued: 1,
			wantRejected: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := uuid.New()
			dl := newFakeDownloader()
			dl.ok("https://acme.com/careers", "hub")
			live := newFakeLiveness()
			extract := &captureExtract{}

			posting := &crawler.JobListing{CanonicalURL: "c-posting", URL: "https://acme.com/jobs/senior-go-engineer",
				SourceHash: "old-body", CompanyKey: "acme.com"}
			live.open[page] = []*crawler.JobListing{posting}
			dl.ok(posting.URL, "new-body") // changed → the re-extract branch

			var rejected int
			proc := collection.NewRefetchProcessor(&collection.RefetchConfig{
				Downloader: dl,
				// One distinct same-host job link, so the saturation rung is the only
				// thing separating the two cases.
				Parser:            fakeParser{urls: []string{"https://acme.com/jobs/other-role"}},
				Liveness:          live,
				Dormancy:          &fakeDormancy{},
				Classifier:        newFakeClassifier(),
				GateConfig:        tt.cfg,
				SourceHash:        identityHash,
				Descriptions:      newFakeDescriptions(),
				EnqueueExtract:    extract.enqueue,
				StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
				DormancyThreshold: crawler.DefaultPageDormancyThreshold,
				OnRegateRejected:  func(context.Context) { rejected++ },
			})

			seed := &crawler.CollectionSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com", CareerPageID: page}
			if err := proc.Process(t.Context(), seed); err != nil {
				t.Fatalf("Process: %v", err)
			}
			if got := len(extract.captured()); got != tt.wantEnqueued {
				t.Errorf("enqueued %d for re-extraction, want %d", got, tt.wantEnqueued)
			}
			if rejected != tt.wantRejected {
				t.Errorf("OnRegateRejected = %d, want %d", rejected, tt.wantRejected)
			}
		})
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
