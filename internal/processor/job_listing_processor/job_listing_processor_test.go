package joblistingprocessor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/freeextraction"
	"github.com/nicholasbraun/job-crawler-poc/internal/listingid"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
)

type spyJobListingRepo struct {
	saved []*crawler.JobListing
}

func (r *spyJobListingRepo) Save(ctx context.Context, jl *crawler.JobListing) error {
	saved := *jl
	r.saved = append(r.saved, &saved)
	return nil
}

type stubExtractor struct {
	result    crawler.JobListing
	isPosting bool
	err       error
	// calls counts invocations, so "the model was never called" is assertable at the
	// save seam once a Free Extraction (ADR-0042) can resolve a page ahead of it.
	calls int
}

func (e *stubExtractor) Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.Extraction, error) {
	e.calls++
	if e.err != nil {
		return crawler.Extraction{}, e.err
	}
	return crawler.Extraction{Listing: e.result, IsJobPosting: e.isPosting}, nil
}

type recordedCall struct {
	kind    llmobs.Kind
	outcome llmobs.Outcome
}

type recordedGate struct {
	kind   llmobs.Kind
	reason llmobs.Reason
}

type spyRecorder struct {
	calls   []recordedCall
	gates   []recordedGate
	content int
	// contentTexts holds what the duplication probe was actually handed, so a test can
	// assert the FORM as well as the count: the probe counts duplicate pages, so it
	// must digest Flattened Text on both sides of the rendering kill switch (ADR-0046).
	contentTexts []string
}

func (s *spyRecorder) Call(_ context.Context, k llmobs.Kind, o llmobs.Outcome, _ time.Duration) {
	s.calls = append(s.calls, recordedCall{k, o})
}
func (s *spyRecorder) Gated(_ context.Context, k llmobs.Kind, r llmobs.Reason) {
	s.gates = append(s.gates, recordedGate{k, r})
}

// PostingScore is inert here: only the Extract Gate's Learned Veto rung produces one
// (ADR-0049), and this lane never runs that gate.
func (s *spyRecorder) PostingScore(context.Context, float64)                {}
func (s *spyRecorder) Shadow(context.Context, llmobs.ShadowVerdict, string) {}
func (s *spyRecorder) ShadowDropped(context.Context, string)                {}
func (s *spyRecorder) Content(_ context.Context, _ llmobs.Kind, text string) {
	s.content++
	s.contentTexts = append(s.contentTexts, text)
}
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)      {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind) {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {
}

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	return u
}

// testSourceHash stands in for the extraction-cache key: prefixing the page's main
// content makes both the stamped value and the input it was computed from assertable
// without pulling sha256 into the test.
func testSourceHash(mainContent string) string { return "key:" + mainContent }

func TestJobListingProcessorRecordsExtractCall(t *testing.T) {
	repo := &spyJobListingRepo{}
	rec := &spyRecorder{}
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		Corpus:              repo,
		JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer"}, isPosting: true},
		Recorder:            rec,
		SourceHash:          testSourceHash,
	})

	raw := &crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "we are hiring"},
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(repo.saved) != 1 {
		t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
	}
	// The crawl lane stamps the Corpus identity (ADR-0034): Source is crawl and
	// CanonicalURL is the canonicalized source URL.
	got := repo.saved[0]
	if got.Source != crawler.SourceLaneCrawl {
		t.Errorf("Source = %q, want %q", got.Source, crawler.SourceLaneCrawl)
	}
	if wantURL := listingid.FromURL("https://careers.acme.com/jobs/1"); got.CanonicalURL != wantURL {
		t.Errorf("CanonicalURL = %q, want %q", got.CanonicalURL, wantURL)
	}
	if rec.content != 1 {
		t.Errorf("content probes = %d, want 1 (content is fed to the extractor)", rec.content)
	}
	want := recordedCall{llmobs.KindExtract, llmobs.OutcomeOK}
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Errorf("recorded calls = %v, want [%v]", rec.calls, want)
	}
}

// TestJobListingProcessorOnSaved asserts the run's saved-listings counter tap
// (#119): OnSaved fires exactly once when a listing is persisted, and never when
// the extractor abstains -- so the header counts saved rows, not enqueued
// candidates a later abstain would discard.
func TestJobListingProcessorOnSaved(t *testing.T) {
	t.Run("fires once on a saved listing", func(t *testing.T) {
		saved := 0
		proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              &spyJobListingRepo{},
			JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer"}, isPosting: true},
			OnSaved:             func(context.Context) { saved++ },
			SourceHash:          testSourceHash,
		})

		raw := &crawler.RawJobListing{
			URL:     newURL(t, "https://careers.acme.com/jobs/1"),
			Content: crawler.Content{MainContent: "we are hiring"},
		}
		if err := proc.Process(t.Context(), raw); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if saved != 1 {
			t.Errorf("OnSaved fired %d times, want 1", saved)
		}
	})

	t.Run("does not fire on an abstain", func(t *testing.T) {
		saved := 0
		proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              &spyJobListingRepo{},
			JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Careers"}, isPosting: false},
			OnSaved:             func(context.Context) { saved++ },
			SourceHash:          testSourceHash,
		})

		raw := &crawler.RawJobListing{
			URL:     newURL(t, "https://careers.acme.com/jobs"),
			Content: crawler.Content{MainContent: "browse our open roles"},
		}
		if err := proc.Process(t.Context(), raw); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if saved != 0 {
			t.Errorf("OnSaved fired %d times on abstain, want 0", saved)
		}
	})
}

// TestJobListingProcessorAbstainSuppressesSave asserts the Extractor Abstain path:
// a false is-job-posting verdict discards the extraction (no Save), records the
// call as OutcomeAbstain, and still returns nil so the durable stream acks it.
func TestJobListingProcessorAbstainSuppressesSave(t *testing.T) {
	repo := &spyJobListingRepo{}
	rec := &spyRecorder{}
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		Corpus:              repo,
		JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Careers"}, isPosting: false},
		Recorder:            rec,
		SourceHash:          testSourceHash,
	})

	raw := &crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs"),
		Content: crawler.Content{MainContent: "browse our open roles"},
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(repo.saved) != 0 {
		t.Fatalf("want 0 listings saved on abstain, got %d", len(repo.saved))
	}
	if rec.content != 1 {
		t.Errorf("content probes = %d, want 1 (content is still fed to the extractor)", rec.content)
	}
	want := recordedCall{llmobs.KindExtract, llmobs.OutcomeAbstain}
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Errorf("recorded calls = %v, want [%v]", rec.calls, want)
	}
}

// TestJobListingProcessorAttributesOwnerFromSnapshot asserts the ADR-0021
// attribution override: at save time the processor discards the extractor's own
// company guess and instead sets the saved listing's Company from the per-run
// CompanyKey → name snapshot, keyed by the source URL's Owner, and persists that
// Owner as the durable CompanyKey.
func TestJobListingProcessorAttributesOwnerFromSnapshot(t *testing.T) {
	t.Run("snapshot hit: catalog name wins over the extractor guess", func(t *testing.T) {
		repo := &spyJobListingRepo{}
		proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              repo,
			JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer", Company: "WrongGuess"}, isPosting: true},
			CompanyNames:        map[string]string{"acme.com": "Acme Inc"},
			SourceHash:          testSourceHash,
		})

		u := newURL(t, "https://acme.com/jobs/1")
		u.Owner = "acme.com"
		raw := &crawler.RawJobListing{URL: u, Content: crawler.Content{MainContent: "we are hiring"}}
		if err := proc.Process(t.Context(), raw); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if len(repo.saved) != 1 {
			t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
		}
		got := repo.saved[0]
		if got.Company != "Acme Inc" {
			t.Errorf("Company: want %q (catalog name), got %q", "Acme Inc", got.Company)
		}
		if got.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey: want %q (the Owner), got %q", "acme.com", got.CompanyKey)
		}
	})

	t.Run("snapshot miss: extractor guess still discarded, Owner persisted", func(t *testing.T) {
		repo := &spyJobListingRepo{}
		proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              repo,
			JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer", Company: "WrongGuess"}, isPosting: true},
			CompanyNames:        map[string]string{"other.com": "Other Inc"},
			SourceHash:          testSourceHash,
		})

		u := newURL(t, "https://acme.com/jobs/1")
		u.Owner = "acme.com"
		raw := &crawler.RawJobListing{URL: u, Content: crawler.Content{MainContent: "we are hiring"}}
		if err := proc.Process(t.Context(), raw); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if len(repo.saved) != 1 {
			t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
		}
		got := repo.saved[0]
		if got.Company != "" {
			t.Errorf("Company: want empty on snapshot miss (extractor guess discarded), got %q", got.Company)
		}
		if got.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey: want %q (the Owner), got %q", "acme.com", got.CompanyKey)
		}
	})
}

// TestJobListingProcessorResolvesCountryAtSave asserts the crawl-lane country
// resolution (ADR-0029): at save the processor sets Country from the extractor's
// free-text Location via the real Country Resolver, leaving the raw Location
// unchanged. An unresolvable or empty location yields the empty Country, and the
// listing is still saved (kept, never dropped; ADR-0028).
func TestJobListingProcessorResolvesCountryAtSave(t *testing.T) {
	tests := []struct {
		name        string
		location    string
		wantCountry string
	}{
		{"city and country", "Berlin, Germany", "DE"},
		// Umlaut endonym resolves through the generated gazetteer: the generator
		// derives an alias key from each city's UTF-8 name through the same fold
		// the runtime uses (ü->u), and München is additionally curated in the
		// supplement since GeoNames anglicizes its name to "Munich" (ADR-0031).
		{"city safety-net diacritic", "München", "DE"},
		{"region only is unresolved but kept", "Remote - EU", ""},
		{"empty location is unresolved but kept", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &spyJobListingRepo{}
			proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
				Corpus:              repo,
				JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer", Location: tt.location}, isPosting: true},
				SourceHash:          testSourceHash,
			})

			raw := &crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: crawler.Content{MainContent: "we are hiring"},
			}
			if err := proc.Process(t.Context(), raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if len(repo.saved) != 1 {
				t.Fatalf("want 1 listing saved (kept even when unresolved), got %d", len(repo.saved))
			}
			got := repo.saved[0]
			if got.Country != tt.wantCountry {
				t.Errorf("Country = %q, want %q", got.Country, tt.wantCountry)
			}
			if got.Location != tt.location {
				t.Errorf("Location = %q, want the raw location %q unchanged", got.Location, tt.location)
			}
		})
	}
}

// TestJobListingProcessorStoresPostingBody asserts the headline behaviour of
// ADR-0041: a saved crawl-lane listing carries the posting's OWN text, derived at
// save from the page the crawler already downloaded, with the Description Source
// naming the branch that produced it. Everything is asserted on the SAVED listing —
// the derivation itself is crawler.PostingBody's own contract.
func TestJobListingProcessorStoresPostingBody(t *testing.T) {
	const structuredBody = "We need a Go engineer who knows Kubernetes."

	tests := []struct {
		name       string
		content    crawler.Content
		maxChars   int
		extracted  crawler.JobListing
		wantBody   string
		wantSource crawler.DescriptionSource
	}{
		{
			name: "page carrying structured data",
			content: crawler.Content{
				MainContent: "site chrome and nav",
				JSONLD:      []string{`{"@type":"JobPosting","description":"` + structuredBody + `"}`},
			},
			extracted:  crawler.JobListing{Title: "Engineer"},
			wantBody:   structuredBody,
			wantSource: crawler.DescriptionSourceStructuredData,
		},
		{
			name:       "page without structured data falls back to main content",
			content:    crawler.Content{MainContent: "we are hiring a backend engineer"},
			extracted:  crawler.JobListing{Title: "Engineer"},
			wantBody:   "we are hiring a backend engineer",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// The cap comes from the processor's OWN knob (DESCRIPTION_MAX_CHARS);
			// nothing in the Config touches the extractor's prompt window.
			name:       "page over the cap is truncated",
			content:    crawler.Content{MainContent: strings.Repeat("a", 50)},
			maxChars:   20,
			extracted:  crawler.JobListing{Title: "Engineer"},
			wantBody:   strings.Repeat("a", 20),
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// A model that still emits a description — and a marker it has no business
			// asserting — cannot leak either: transcription is not a judgment task, and
			// provenance is a save-time fact about the pipeline.
			name:    "model-emitted description and marker are both overwritten",
			content: crawler.Content{MainContent: "the page's own posting text"},
			extracted: crawler.JobListing{
				Title:             "Engineer",
				Description:       "a short model-written summary",
				DescriptionSource: crawler.DescriptionSourceStructuredData,
			},
			wantBody:   "the page's own posting text",
			wantSource: crawler.DescriptionSourcePageContent,
		},
		{
			// A page whose body is injected by JS parses to nothing. Stamping
			// page_content here would put the row beyond the refetch heal's reach
			// forever, since the heal only revisits the legacy marker.
			name:       "page that parses to nothing stays in the heal's queue",
			content:    crawler.Content{},
			extracted:  crawler.JobListing{Title: "Engineer"},
			wantBody:   "",
			wantSource: crawler.DescriptionSourceLLMSummary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &spyJobListingRepo{}
			proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
				Corpus:              repo,
				JobListingExtractor: &stubExtractor{result: tt.extracted, isPosting: true},
				DescriptionMaxChars: tt.maxChars,
				SourceHash:          testSourceHash,
			})

			raw := &crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: tt.content,
			}
			if err := proc.Process(t.Context(), raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if len(repo.saved) != 1 {
				t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
			}
			got := repo.saved[0]
			if got.Description != tt.wantBody {
				t.Errorf("Description = %q, want the Posting Body %q", got.Description, tt.wantBody)
			}
			if got.DescriptionSource != tt.wantSource {
				t.Errorf("DescriptionSource = %q, want %q", got.DescriptionSource, tt.wantSource)
			}
		})
	}
}

// TestJobListingProcessorStampsSourceHash asserts the extraction-cache key
// (ADR-0035) is stamped at SAVE from the page's main content via the injected key
// function — the same closure the refetch lane recomputes with, so an unchanged page
// is confirmed alive with no model call.
func TestJobListingProcessorStampsSourceHash(t *testing.T) {
	repo := &spyJobListingRepo{}
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		Corpus: repo,
		// The extractor no longer returns a key; a stray one must not survive.
		JobListingExtractor: &stubExtractor{
			result:    crawler.JobListing{Title: "Engineer", SourceHash: "stale-extractor-key"},
			isPosting: true,
		},
		SourceHash: testSourceHash,
	})

	raw := &crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "we are hiring"},
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(repo.saved) != 1 {
		t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
	}
	if want := testSourceHash(raw.Content.MainContent); repo.saved[0].SourceHash != want {
		t.Errorf("SourceHash = %q, want %q (keyed on the page's main content at save)",
			repo.saved[0].SourceHash, want)
	}
}

// TestJobListingProcessorKeysOnFlattenedText asserts the save side stamps the
// extraction-cache key over the page's Flattened Text (ADR-0046), not over the content
// field raw. It is one half of a pair: the refetch lane recomputes the key the same way
// (internal/collection, TestRefetchComparesTheKeyOverFlattenedText), and if only one of
// the two flattens, every stored listing reads as changed and the whole Corpus
// re-extracts in one wave the moment the parser starts rendering structure.
//
// It also pins the duplication probe's input, which must be the same form for the same
// reason: it counts duplicate PAGES, so its recurrence numbers would jump when the
// rendering kill switch flips.
func TestJobListingProcessorKeysOnFlattenedText(t *testing.T) {
	repo := &spyJobListingRepo{}
	rec := &spyRecorder{}
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		Corpus: repo,
		JobListingExtractor: &stubExtractor{
			result:    crawler.JobListing{Title: "Engineer"},
			isPosting: true,
		},
		Recorder:   rec,
		SourceHash: testSourceHash,
	})

	raw := &crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "we are hiring\n\n- Go"},
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(repo.saved) != 1 {
		t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
	}
	if want := testSourceHash("we are hiring - Go"); repo.saved[0].SourceHash != want {
		t.Errorf("SourceHash = %q, want %q (keyed on the page's Flattened Text at save)",
			repo.saved[0].SourceHash, want)
	}

	if len(rec.contentTexts) != 1 {
		t.Fatalf("want 1 duplication-probe record, got %d", len(rec.contentTexts))
	}
	if want := "we are hiring - Go"; rec.contentTexts[0] != want {
		t.Errorf("duplication probe saw %q, want %q (the page's Flattened Text)",
			rec.contentTexts[0], want)
	}
}

// TestJobListingProcessorExtractErrorNotAbstain guards the err==nil half of the
// abstain classification. On an extraction failure the extractor returns the zero
// Extraction (IsJobPosting=false); without the err==nil guard the false verdict
// would be misrecorded as OutcomeAbstain, silently inflating the Empty-Extraction
// Rate. A real failure must record OutcomeError, save nothing, and propagate the
// error so the durable stream retries rather than acks.
func TestJobListingProcessorExtractErrorNotAbstain(t *testing.T) {
	repo := &spyJobListingRepo{}
	rec := &spyRecorder{}
	extractErr := errors.New("openrouter: status 500: oops")
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		Corpus:              repo,
		JobListingExtractor: &stubExtractor{err: extractErr},
		Recorder:            rec,
		SourceHash:          testSourceHash,
	})

	raw := &crawler.RawJobListing{
		URL:     newURL(t, "https://careers.acme.com/jobs/1"),
		Content: crawler.Content{MainContent: "we are hiring"},
	}
	err := proc.Process(t.Context(), raw)
	if err == nil {
		t.Fatal("Process returned nil, want error propagated on extraction failure")
	}
	if !errors.Is(err, extractErr) {
		t.Errorf("Process error = %v, want wrapping %v", err, extractErr)
	}

	if len(repo.saved) != 0 {
		t.Fatalf("want 0 listings saved on extract error, got %d", len(repo.saved))
	}
	want := recordedCall{llmobs.KindExtract, llmobs.OutcomeError}
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Errorf("recorded calls = %v, want [%v] (error must not be miscounted as abstain)", rec.calls, want)
	}
}

// Structured-data fixtures for the Free Extraction seam (ADR-0042). Deliberately
// minimal: the exhaustive schema-traversal table is crawler.LonePosting's own
// contract, asserted in internal/structured_posting_test.go.
const (
	lonePostingRemoteLD = `{"@type":"JobPosting","title":"Backend Engineer",
		"description":"We are hiring a Go engineer.","jobLocationType":"TELECOMMUTE",
		"jobLocation":{"@type":"Place","address":{"addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"DE"}}}`
	lonePostingNoModeLD = `{"@type":"JobPosting","title":"Backend Engineer",
		"description":"We are hiring a Go engineer.",
		"jobLocation":{"@type":"Place","address":{"addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"DE"}}}`
	twoPostingsLD      = `[{"@type":"JobPosting","title":"Backend Engineer"},{"@type":"JobPosting","title":"Frontend Engineer"}]`
	titlelessPostingLD = `{"@type":"JobPosting","description":"We are hiring."}`
	// A non-US posting whose addressRegion is spelled like a US state abbreviation.
	// Composed, it reads "Perth, WA, AU", which the Country Resolver answers US.
	collidingRegionLD = `{"@type":"JobPosting","title":"Backend Engineer",
		"description":"We are hiring a Go engineer.",
		"jobLocation":{"@type":"Place","address":{"addressLocality":"Perth","addressRegion":"WA","addressCountry":"AU"}}}`
)

// TestJobListingProcessorFreeExtraction asserts the whole Free Extraction behaviour
// (ADR-0042) at the seam an operator observes it from: a listing saved, the model
// extractor never called, and a gate resolution recorded in place of an LLM call —
// while an ambiguous or plain page still reaches the model exactly as before.
func TestJobListingProcessorFreeExtraction(t *testing.T) {
	const pageText = "site chrome and nav"

	// newProcessor wires the decorator over stub as the extractor port, exactly as
	// cmd/server does for the collection extract stage.
	newProcessor := func(stub *stubExtractor, repo *spyJobListingRepo, rec *spyRecorder) *joblistingprocessor.JobListingProcessor {
		return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              repo,
			JobListingExtractor: freeextraction.NewExtractor(stub),
			Recorder:            rec,
			SourceHash:          testSourceHash,
		})
	}

	t.Run("a lone structured posting is saved with no model call", func(t *testing.T) {
		tests := []struct {
			name            string
			jsonld          string
			wantArrangement crawler.WorkArrangement
		}{
			{"a posting declaring remote work is tagged Remote", lonePostingRemoteLD, crawler.WorkArrangementRemote},
			// ADR-0030: a posting that states no mode is never guessed Onsite.
			{"a posting declaring nothing stays Unspecified", lonePostingNoModeLD, crawler.WorkArrangementUnspecified},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				stub := &stubExtractor{result: crawler.JobListing{Title: "model-extracted"}, isPosting: true}
				repo := &spyJobListingRepo{}
				rec := &spyRecorder{}

				raw := &crawler.RawJobListing{
					URL:     newURL(t, "https://careers.acme.com/jobs/1"),
					Content: crawler.Content{MainContent: pageText, JSONLD: []string{tt.jsonld}},
				}
				if err := newProcessor(stub, repo, rec).Process(t.Context(), raw); err != nil {
					t.Fatalf("Process returned error: %v", err)
				}

				if stub.calls != 0 {
					t.Errorf("model extractor called %d times, want 0 (the whole point of a Free Extraction)", stub.calls)
				}
				if len(repo.saved) != 1 {
					t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
				}
				got := repo.saved[0]
				if got.Title != "Backend Engineer" {
					t.Errorf("Title = %q, want the page's declared %q", got.Title, "Backend Engineer")
				}
				if got.Location != "Berlin, Berlin, DE" {
					t.Errorf("Location = %q, want the page's composed address", got.Location)
				}
				// The real Country Resolver runs at save on the free path too (ADR-0029).
				if got.Country != "DE" {
					t.Errorf("Country = %q, want %q resolved from the declared location", got.Country, "DE")
				}
				if got.WorkArrangement != tt.wantArrangement {
					t.Errorf("WorkArrangement = %q, want %q", got.WorkArrangement, tt.wantArrangement)
				}
				if got.WorkArrangement == crawler.WorkArrangementOnsite {
					t.Error("WorkArrangement = onsite, which is never guessed (ADR-0030)")
				}
				if got.Description != "We are hiring a Go engineer." {
					t.Errorf("Description = %q, want the posting's own body", got.Description)
				}
				if got.DescriptionSource != crawler.DescriptionSourceStructuredData {
					t.Errorf("DescriptionSource = %q, want %q", got.DescriptionSource, crawler.DescriptionSourceStructuredData)
				}
				if want := testSourceHash(pageText); got.SourceHash != want {
					t.Errorf("SourceHash = %q, want %q (stamped at save for both paths)", got.SourceHash, want)
				}
				if len(rec.calls) != 0 {
					t.Errorf("recorded calls = %v, want none (no model call was made)", rec.calls)
				}
				wantGate := recordedGate{llmobs.KindExtract, llmobs.ReasonStructuredData}
				if len(rec.gates) != 1 || rec.gates[0] != wantGate {
					t.Errorf("recorded gates = %v, want [%v]", rec.gates, wantGate)
				}
				if rec.content != 1 {
					t.Errorf("content probes = %d, want 1 (the probe measures the extract stream)", rec.content)
				}
			})
		}
	})

	// The Free Extraction is the first path to hand the Country Resolver a location
	// carrying an ISO country code — the model path's prompt forbids them — and the
	// Resolver keys US state abbreviations but not bare alpha-2 countries. Without the
	// page's own addressCountry as a hint, every "<city>, <US-state-lookalike>, <ISO>"
	// posting is filed under the wrong country.
	t.Run("a country declared as an ISO code beats a region that looks like a US state", func(t *testing.T) {
		stub := &stubExtractor{result: crawler.JobListing{Title: "model-extracted"}, isPosting: true}
		repo := &spyJobListingRepo{}
		rec := &spyRecorder{}

		raw := &crawler.RawJobListing{
			URL:     newURL(t, "https://careers.acme.com/jobs/perth"),
			Content: crawler.Content{MainContent: pageText, JSONLD: []string{collidingRegionLD}},
		}
		if err := newProcessor(stub, repo, rec).Process(t.Context(), raw); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}

		if len(repo.saved) != 1 {
			t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
		}
		got := repo.saved[0]
		if got.Country != "AU" {
			t.Errorf("Country = %q, want %q — the page declares AU; %q resolves to US on the region alone",
				got.Country, "AU", got.Location)
		}
		// Location stays verbatim (ADR-0029): the hint decides the Country, not the text.
		if got.Location != "Perth, WA, AU" {
			t.Errorf("Location = %q, want the page's composed address verbatim", got.Location)
		}
	})

	t.Run("an ambiguous or plain page still reaches the model", func(t *testing.T) {
		tests := []struct {
			name    string
			content crawler.Content
		}{
			{"a page publishing an openings index", crawler.Content{MainContent: pageText, JSONLD: []string{twoPostingsLD}}},
			{"a page with no structured data", crawler.Content{MainContent: pageText}},
			{"a title-less posting node", crawler.Content{MainContent: pageText, JSONLD: []string{titlelessPostingLD}}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				stub := &stubExtractor{result: crawler.JobListing{Title: "model-extracted"}, isPosting: true}
				repo := &spyJobListingRepo{}
				rec := &spyRecorder{}

				raw := &crawler.RawJobListing{
					URL:     newURL(t, "https://careers.acme.com/jobs"),
					Content: tt.content,
				}
				if err := newProcessor(stub, repo, rec).Process(t.Context(), raw); err != nil {
					t.Fatalf("Process returned error: %v", err)
				}

				if stub.calls != 1 {
					t.Fatalf("model extractor called %d times, want 1", stub.calls)
				}
				wantCall := recordedCall{llmobs.KindExtract, llmobs.OutcomeOK}
				if len(rec.calls) != 1 || rec.calls[0] != wantCall {
					t.Errorf("recorded calls = %v, want [%v]", rec.calls, wantCall)
				}
				if len(rec.gates) != 0 {
					t.Errorf("recorded gates = %v, want none (the page reached the model)", rec.gates)
				}
				if len(repo.saved) != 1 {
					t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
				}
				if repo.saved[0].Title != "model-extracted" {
					t.Errorf("Title = %q, want the model's %q", repo.saved[0].Title, "model-extracted")
				}
			})
		}
	})

	// The refetch lane's unchanged-page check compares the stored extraction-cache key
	// against a recomputed one (ADR-0035); if the free path stamped a different key the
	// whole Corpus would re-extract every Cycle. Both keys are the save processor's, so
	// the two paths agree by construction — asserted here where an operator would see it.
	t.Run("both paths stamp the same extraction-cache key and body", func(t *testing.T) {
		content := crawler.Content{MainContent: pageText, JSONLD: []string{lonePostingNoModeLD}}
		stub := &stubExtractor{result: crawler.JobListing{Title: "model-extracted"}, isPosting: true}

		freeRepo := &spyJobListingRepo{}
		freeProc := newProcessor(stub, freeRepo, &spyRecorder{})
		modelRepo := &spyJobListingRepo{}
		modelProc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              modelRepo,
			JobListingExtractor: stub, // unwrapped: exactly what EXTRACT_FROM_JSONLD=false restores
			SourceHash:          testSourceHash,
		})

		for _, proc := range []*joblistingprocessor.JobListingProcessor{freeProc, modelProc} {
			raw := &crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: content,
			}
			if err := proc.Process(t.Context(), raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}
		}

		if len(freeRepo.saved) != 1 || len(modelRepo.saved) != 1 {
			t.Fatalf("want 1 listing saved per path, got free=%d model=%d", len(freeRepo.saved), len(modelRepo.saved))
		}
		free, model := freeRepo.saved[0], modelRepo.saved[0]
		if free.SourceHash != model.SourceHash {
			t.Errorf("SourceHash: free = %q, model = %q — the two paths must produce identical cache keys",
				free.SourceHash, model.SourceHash)
		}
		if want := testSourceHash(content.MainContent); free.SourceHash != want {
			t.Errorf("SourceHash = %q, want %q (keyed on the page's main content)", free.SourceHash, want)
		}
		if free.Description != model.Description {
			t.Errorf("Description: free = %q, model = %q — both are the save-time Posting Body",
				free.Description, model.Description)
		}
		if free.DescriptionSource != model.DescriptionSource {
			t.Errorf("DescriptionSource: free = %q, model = %q", free.DescriptionSource, model.DescriptionSource)
		}
	})
}

// TestJobListingProcessorCareerPageAttribution covers the write-path half of the
// "a crawl-lane listing always has a Career Page" invariant that migration 0027's
// strict FK enforces on the delete path. The Attributor returns Nil when the Owner
// has no page in the Cycle snapshot — what a page removed mid-run looks like while
// the frontier still holds URLs enqueued under it. Such a posting must be dropped,
// not saved: ListOpen selects BY career_page_id, so a NULL one is unreachable by the
// refetch lane forever — never re-verified, never closed, permanently Open.
func TestJobListingProcessorCareerPageAttribution(t *testing.T) {
	newRaw := func(t *testing.T) *crawler.RawJobListing {
		t.Helper()
		return &crawler.RawJobListing{
			URL:     newURL(t, "https://goodjobs.eu/jobs/backend-engineer"),
			Content: crawler.Content{MainContent: "we are hiring"},
		}
	}
	newProc := func(repo *spyJobListingRepo, attribute func(string, string) uuid.UUID) *joblistingprocessor.JobListingProcessor {
		return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
			Corpus:              repo,
			JobListingExtractor: &stubExtractor{result: crawler.JobListing{Title: "Engineer"}, isPosting: true},
			Recorder:            &spyRecorder{},
			SourceHash:          testSourceHash,
			AttributeCareerPage: attribute,
		})
	}

	t.Run("an unattributable posting is dropped, not saved as an orphan", func(t *testing.T) {
		repo := &spyJobListingRepo{}
		proc := newProc(repo, func(string, string) uuid.UUID { return uuid.Nil })

		if err := proc.Process(t.Context(), newRaw(t)); err != nil {
			t.Fatalf("dropping an unattributable posting is not an error, got: %v", err)
		}
		if len(repo.saved) != 0 {
			t.Errorf("want 0 listings saved, got %d — a NULL career_page_id is unreachable by ListOpen", len(repo.saved))
		}
	})

	t.Run("an attributable posting is saved carrying its career page", func(t *testing.T) {
		repo := &spyJobListingRepo{}
		page := uuid.New()
		proc := newProc(repo, func(string, string) uuid.UUID { return page })

		if err := proc.Process(t.Context(), newRaw(t)); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
		}
		if repo.saved[0].CareerPageID != page {
			t.Errorf("CareerPageID = %v, want %v", repo.saved[0].CareerPageID, page)
		}
	})

	// The hook is nil for Discovery and un-scoped runs; those paths are unchanged and
	// must keep saving, so the guard cannot fire where there is nothing to attribute.
	t.Run("no attributor wired still saves", func(t *testing.T) {
		repo := &spyJobListingRepo{}
		proc := newProc(repo, nil)

		if err := proc.Process(t.Context(), newRaw(t)); err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
		if len(repo.saved) != 1 {
			t.Errorf("want 1 listing saved with no attributor wired, got %d", len(repo.saved))
		}
	})
}
