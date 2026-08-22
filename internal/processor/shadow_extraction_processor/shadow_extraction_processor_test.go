package shadowextractionprocessor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
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

func (s *spyRecorder) PostingScore(context.Context, float64) {
	s.t.Error("the shadow lane must never enter the Posting Score distribution: it sees ~1% of rejects and none of the accepts, so a distribution built here would describe a population nobody is judging")
}

func (s *spyRecorder) Content(context.Context, llmobs.Kind, string)          {}
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                    {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)               {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {}

// captureLogs installs a JSON slog handler writing into buf for the duration of fn,
// then restores the previous default logger.
func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

// falseDropLine returns the one false-drop log entry in buf, decoded. It fails the
// test when there is not exactly one: the score assertions below are about that line
// and nothing else.
func falseDropLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("could not parse log line %q: %v", line, err)
		}
		if msg, _ := entry["msg"].(string); strings.Contains(msg, "false-drop sample") {
			found = append(found, entry)
		}
	}
	if len(found) != 1 {
		t.Fatalf("false-drop log lines = %d, want exactly 1: %s", len(found), buf.String())
	}
	return found[0]
}

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

// TestProcessFilesTheLearnedVetosVerdictsAndLogsTheScore covers the one rung whose
// drops cannot be explained in words (ADR-0049). Its verdicts are filed under its own
// rung like every other, but its false-drop log line additionally carries the Posting
// Score that dropped the page — the only account of that drop there is, and what makes
// correcting the threshold evidence-backed rather than guesswork.
//
// The middle case is the sharp one: the RUNG gates the read, not the value. A sample
// on another rung carries a Score field that means nothing (here deliberately
// non-zero), and an entry enqueued by an older binary carries no rung at all. Logging
// either as a Posting Score would file a number against a page the veto never judged.
func TestProcessFilesTheLearnedVetosVerdictsAndLogsTheScore(t *testing.T) {
	tests := []struct {
		name      string
		rung      pagegate.ExtractRung
		score     float64
		wantRung  string
		wantScore bool
	}{
		{
			name:      "a Learned Veto false-drop carries the score that dropped it",
			rung:      pagegate.RungLearnedVeto,
			score:     0.4212,
			wantRung:  string(pagegate.RungLearnedVeto),
			wantScore: true,
		},
		{
			name:     "another rung's sample logs no score",
			rung:     pagegate.RungPositiveEvidence,
			score:    0.9,
			wantRung: string(pagegate.RungPositiveEvidence),
		},
		{
			name:     "a rungless sample from an older binary logs no score",
			rung:     pagegate.RungNone,
			wantRung: string(pagegate.RungUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &spyRecorder{t: t}
			p := shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
				// An accepting extractor, so every case produces the false-drop line.
				Extractor: &stubExtractor{extraction: crawler.Extraction{IsJobPosting: true}},
				Recorder:  rec,
			})

			sample := shadowSample(t, "https://acme.com/jobs/senior-engineer", tt.rung)
			sample.Score = tt.score

			var buf bytes.Buffer
			captureLogs(t, &buf, func() {
				if err := p.Process(t.Context(), sample); err != nil {
					t.Fatalf("Process returned error: %v", err)
				}
			})

			if len(rec.rungs) != 1 || rec.rungs[0] != tt.wantRung {
				t.Errorf("recorded rungs = %v, want one %q", rec.rungs, tt.wantRung)
			}

			entry := falseDropLine(t, &buf)
			logged, present := entry["posting_score"]
			if !tt.wantScore {
				if present {
					t.Errorf("false-drop line carries posting_score %v on rung %q: only the Learned Veto's samples hold a score",
						logged, tt.wantRung)
				}
				return
			}
			if !present {
				t.Fatalf("false-drop line carries no posting_score on the Learned Veto's rung: %s", buf.String())
			}
			if got, ok := logged.(float64); !ok || got != tt.score {
				t.Errorf("logged posting_score = %v, want %v: the score that dropped the page is the whole account of the drop", logged, tt.score)
			}
		})
	}
}
