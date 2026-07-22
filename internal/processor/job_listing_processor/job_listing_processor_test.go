package joblistingprocessor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
)

type spyJobListingRepo struct {
	saved []*crawler.JobListing
}

func (r *spyJobListingRepo) Save(ctx context.Context, definitionID uuid.UUID, jl *crawler.JobListing) error {
	saved := *jl
	r.saved = append(r.saved, &saved)
	return nil
}
func (r *spyJobListingRepo) Find(ctx context.Context) ([]*crawler.JobListing, error) {
	return r.saved, nil
}
func (r *spyJobListingRepo) FindByDefinition(ctx context.Context, definitionID uuid.UUID, keyword string) ([]*crawler.JobListing, error) {
	return r.saved, nil
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

func TestJobListingProcessorRecordsExtractCall(t *testing.T) {
	repo := &spyJobListingRepo{}
	rec := &spyRecorder{}
	proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
		JobListingRepository: repo,
		JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer"}, isPosting: true},
		DefinitionID:         uuid.New(),
		Recorder:             rec,
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
			JobListingRepository: &spyJobListingRepo{},
			JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer"}, isPosting: true},
			DefinitionID:         uuid.New(),
			OnSaved:              func(context.Context) { saved++ },
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
			JobListingRepository: &spyJobListingRepo{},
			JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Careers"}, isPosting: false},
			DefinitionID:         uuid.New(),
			OnSaved:              func(context.Context) { saved++ },
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
		JobListingRepository: repo,
		JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Careers"}, isPosting: false},
		DefinitionID:         uuid.New(),
		Recorder:             rec,
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
			JobListingRepository: repo,
			JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer", Company: "WrongGuess"}, isPosting: true},
			DefinitionID:         uuid.New(),
			CompanyNames:         map[string]string{"acme.com": "Acme Inc"},
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
			JobListingRepository: repo,
			JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer", Company: "WrongGuess"}, isPosting: true},
			DefinitionID:         uuid.New(),
			CompanyNames:         map[string]string{"other.com": "Other Inc"},
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
				JobListingRepository: repo,
				JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer", Location: tt.location}, isPosting: true},
				DefinitionID:         uuid.New(),
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

// TestJobListingProcessorCountryConstraintGate asserts the crawl-lane Country
// Constraint (ADR-0028): with a target set of {DE}, a listing is kept only when its
// resolved Country is DE or its Country is unresolved; any other resolved Country is
// discarded regardless of Work Arrangement (Remote is not an override). A discard is
// a completed decision -- Process returns nil (no retry), nothing is saved, and
// OnSaved never fires.
func TestJobListingProcessorCountryConstraintGate(t *testing.T) {
	tests := []struct {
		name        string
		countries   []string
		location    string
		arrangement crawler.WorkArrangement
		wantSaved   bool
	}{
		{"in-set country is kept", []string{"DE"}, "Berlin, Germany", crawler.WorkArrangementOnsite, true},
		{"out-of-set country is dropped", []string{"DE"}, "Paris, France", crawler.WorkArrangementOnsite, false},
		{"unresolved country is kept", []string{"DE"}, "", crawler.WorkArrangementOnsite, true},
		{"remote does NOT override an out-of-set country", []string{"DE"}, "Paris, France", crawler.WorkArrangementRemote, false},
		{"remote with unresolved location is still kept", []string{"DE"}, "Remote - EU", crawler.WorkArrangementRemote, true},
		{"empty constraint keeps every country", nil, "Paris, France", crawler.WorkArrangementOnsite, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &spyJobListingRepo{}
			saved := 0
			proc := joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
				JobListingRepository: repo,
				JobListingExtractor: &stubExtractor{
					result:    crawler.JobListing{Title: "Engineer", Location: tt.location, WorkArrangement: tt.arrangement},
					isPosting: true,
				},
				DefinitionID: uuid.New(),
				Countries:    tt.countries,
				OnSaved:      func(context.Context) { saved++ },
			})

			raw := &crawler.RawJobListing{
				URL:     newURL(t, "https://careers.acme.com/jobs/1"),
				Content: crawler.Content{MainContent: "we are hiring"},
			}
			// A drop is a completed decision: Process must return nil, never an error.
			if err := proc.Process(t.Context(), raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if tt.wantSaved {
				if len(repo.saved) != 1 {
					t.Fatalf("want 1 listing saved, got %d", len(repo.saved))
				}
				if saved != 1 {
					t.Errorf("OnSaved fired %d times, want 1 on a kept listing", saved)
				}
			} else {
				if len(repo.saved) != 0 {
					t.Fatalf("want 0 listings saved (dropped by country), got %d", len(repo.saved))
				}
				if saved != 0 {
					t.Errorf("OnSaved fired %d times, want 0 on a dropped listing", saved)
				}
			}
		})
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
		JobListingRepository: repo,
		JobListingExtractor:  &stubExtractor{err: extractErr},
		DefinitionID:         uuid.New(),
		Recorder:             rec,
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
