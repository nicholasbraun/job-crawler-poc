package shadowextractionprocessor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	shadowextractionprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/shadow_extraction_processor"
)

// stubExtractor returns a fixed Extraction/error and records the page it was fed,
// so a test can assert the shadow lane hands the extractor the page as crawled.
type stubExtractor struct {
	extraction crawler.Extraction
	err        error
	got        []crawler.RawJobListing
}

func (e *stubExtractor) Extract(_ context.Context, raw crawler.RawJobListing) (crawler.Extraction, error) {
	e.got = append(e.got, raw)
	return e.extraction, e.err
}

// spyRecorder captures the Shadow verdicts recorded, and fails the test if the
// processor ever records a Call or a gate resolution: a Shadow Extraction is
// measurement spend that must stay off the counters the Extract Gate is judged by
// (ADR-0044).
type spyRecorder struct {
	t        *testing.T
	verdicts []llmobs.ShadowVerdict
}

func (s *spyRecorder) Call(context.Context, llmobs.Kind, llmobs.Outcome, time.Duration) {
	s.t.Error("a Shadow Extraction must never be recorded on the main LLM call counter")
}

func (s *spyRecorder) Gated(context.Context, llmobs.Kind, llmobs.Reason) {
	s.t.Error("a Shadow Extraction must never be recorded as a gate resolution")
}

func (s *spyRecorder) Shadow(_ context.Context, v llmobs.ShadowVerdict) {
	s.verdicts = append(s.verdicts, v)
}

func (s *spyRecorder) Content(context.Context, llmobs.Kind, string)          {}
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                    {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)               {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {}

func rawListing(t *testing.T, rawURL string) *crawler.RawJobListing {
	t.Helper()
	u, err := crawler.NewURL(rawURL)
	if err != nil {
		t.Fatalf("NewURL(%q): %v", rawURL, err)
	}
	return &crawler.RawJobListing{
		URL:     u,
		Content: crawler.Content{Title: "Senior Engineer", MainContent: "we are hiring"},
	}
}

// TestProcessRecordsTheExtractorVerdict pins the measurement itself: the verdict
// the extractor reaches on a page the Extract Gate rejected, and the ack-don't-retry
// contract that keeps a failed sample from re-spending model calls on the durable
// stream.
func TestProcessRecordsTheExtractorVerdict(t *testing.T) {
	tests := []struct {
		name         string
		isJobPosting bool
		err          error
		want         llmobs.ShadowVerdict
	}{
		{name: "a posting the gate rejected is an accept", isJobPosting: true, want: llmobs.ShadowAccept},
		{name: "an abstain is an abstain", isJobPosting: false, want: llmobs.ShadowAbstain},
		{name: "a failed call is an error verdict", err: errors.New("boom"), want: llmobs.ShadowError},
		{name: "an error wins over a stale verdict", isJobPosting: true, err: errors.New("boom"), want: llmobs.ShadowError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &spyRecorder{t: t}
			p := shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
				Extractor: &stubExtractor{
					extraction: crawler.Extraction{IsJobPosting: tt.isJobPosting},
					err:        tt.err,
				},
				Recorder: rec,
			})

			if err := p.Process(t.Context(), rawListing(t, "https://acme.com/careers")); err != nil {
				t.Fatalf("Process returned %v, want nil (a shadow sample is acked, never retried)", err)
			}

			if len(rec.verdicts) != 1 {
				t.Fatalf("recorded verdicts = %v, want exactly one", rec.verdicts)
			}
			if rec.verdicts[0] != tt.want {
				t.Errorf("verdict = %q, want %q", rec.verdicts[0], tt.want)
			}
		})
	}
}

// TestProcessFeedsTheExtractorThePageAsCrawled proves the shadow lane measures the
// page the walk actually saw: a verdict on anything else would not be the verdict
// the gate's decision cost us.
func TestProcessFeedsTheExtractorThePageAsCrawled(t *testing.T) {
	ext := &stubExtractor{}
	p := shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
		Extractor: ext,
		Recorder:  &spyRecorder{t: t},
	})

	workload := rawListing(t, "https://acme.com/careers")
	if err := p.Process(t.Context(), workload); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(ext.got) != 1 {
		t.Fatalf("extractor called %d times, want 1", len(ext.got))
	}
	if got := ext.got[0].URL.RawURL; got != workload.URL.RawURL {
		t.Errorf("extracted URL = %q, want %q", got, workload.URL.RawURL)
	}
	if got := ext.got[0].Content; !reflect.DeepEqual(got, workload.Content) {
		t.Errorf("extracted Content = %+v, want %+v", got, workload.Content)
	}
}

func TestProcessWithoutARecorderDoesNotPanic(t *testing.T) {
	p := shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
		Extractor: &stubExtractor{extraction: crawler.Extraction{IsJobPosting: true}},
	})

	if err := p.Process(t.Context(), rawListing(t, "https://acme.com/careers")); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
}
