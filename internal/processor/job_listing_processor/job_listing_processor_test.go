package joblistingprocessor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
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
}

func (e *stubExtractor) Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.Extraction, error) {
	if e.err != nil {
		return crawler.Extraction{}, e.err
	}
	return crawler.Extraction{Listing: e.result, IsJobPosting: e.isPosting}, nil
}

type recordedCall struct {
	kind    llmobs.Kind
	outcome llmobs.Outcome
}

type spyRecorder struct {
	calls   []recordedCall
	content int
}

func (s *spyRecorder) Call(_ context.Context, k llmobs.Kind, o llmobs.Outcome, _ time.Duration) {
	s.calls = append(s.calls, recordedCall{k, o})
}
func (s *spyRecorder) Gated(context.Context, llmobs.Kind, llmobs.Reason)  {}
func (s *spyRecorder) Content(_ context.Context, _ llmobs.Kind, _ string) { s.content++ }
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                 {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)            {}
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
