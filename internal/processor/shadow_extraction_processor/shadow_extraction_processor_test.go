package shadowextractionprocessor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
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
	rungs    []string
}

func (s *spyRecorder) Call(context.Context, llmobs.Kind, llmobs.Outcome, time.Duration) {
	s.t.Error("a Shadow Extraction must never be recorded on the main LLM call counter")
}

func (s *spyRecorder) Gated(context.Context, llmobs.Kind, llmobs.Reason) {
	s.t.Error("a Shadow Extraction must never be recorded as a gate resolution")
}

func (s *spyRecorder) Shadow(_ context.Context, v llmobs.ShadowVerdict, rung string) {
	s.verdicts = append(s.verdicts, v)
	s.rungs = append(s.rungs, rung)
}

func (s *spyRecorder) ShadowDropped(context.Context, string) {
	s.t.Error("the shadow processor never sheds a sample; only the walk that enqueues one does")
}

func (s *spyRecorder) Content(context.Context, llmobs.Kind, string)          {}
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                    {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)               {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {}

func shadowSample(t *testing.T, rawURL string, rung pagegate.ExtractRung) *crawler.ShadowSample {
	t.Helper()
	u, err := crawler.NewURL(rawURL)
	if err != nil {
		t.Fatalf("NewURL(%q): %v", rawURL, err)
	}
	return &crawler.ShadowSample{
		RawJobListing: crawler.RawJobListing{
			URL:     u,
			Content: crawler.Content{Title: "Senior Engineer", MainContent: "we are hiring"},
		},
		Rung: string(rung),
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

			sample := shadowSample(t, "https://acme.com/careers", pagegate.RungPositiveEvidence)
			if err := p.Process(t.Context(), sample); err != nil {
				t.Fatalf("Process returned %v, want nil (a shadow sample is acked, never retried)", err)
			}

			if len(rec.verdicts) != 1 {
				t.Fatalf("recorded verdicts = %v, want exactly one", rec.verdicts)
			}
			if rec.verdicts[0] != tt.want {
				t.Errorf("verdict = %q, want %q", rec.verdicts[0], tt.want)
			}
			if rec.rungs[0] != string(pagegate.RungPositiveEvidence) {
				t.Errorf("rung = %q, want %q", rec.rungs[0], pagegate.RungPositiveEvidence)
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

	workload := shadowSample(t, "https://acme.com/careers", pagegate.RungIndexTerminal)
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

	if err := p.Process(t.Context(), shadowSample(t, "https://acme.com/careers", pagegate.RungPositiveEvidence)); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
}

// TestProcessLabelsARunglessSampleUnknown covers the upgrade path: an entry enqueued
// by a binary from before the rung attribution existed, redelivered afterwards,
// carries no rung. It must be labelled explicitly rather than filed under the empty
// string, which would land it in whichever series a dashboard happens to render for
// a blank label and quietly inflate a real rung's false-drop rate.
func TestProcessLabelsARunglessSampleUnknown(t *testing.T) {
	rec := &spyRecorder{t: t}
	p := shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
		Extractor: &stubExtractor{extraction: crawler.Extraction{IsJobPosting: true}},
		Recorder:  rec,
	})

	sample := shadowSample(t, "https://acme.com/careers", pagegate.RungNone)
	if err := p.Process(t.Context(), sample); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(rec.rungs) != 1 || rec.rungs[0] != string(pagegate.RungUnknown) {
		t.Errorf("recorded rungs = %v, want one %q", rec.rungs, pagegate.RungUnknown)
	}
}
