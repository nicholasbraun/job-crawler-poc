package joblistingprocessor_test

import (
	"context"
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
	result crawler.JobListing
}

func (e *stubExtractor) Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.JobListing, error) {
	return e.result, nil
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
		JobListingExtractor:  &stubExtractor{result: crawler.JobListing{Title: "Engineer"}},
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
