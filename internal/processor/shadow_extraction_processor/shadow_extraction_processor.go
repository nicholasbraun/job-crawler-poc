// Package shadowextractionprocessor runs a Shadow Extraction: it extracts a page
// the Extract Gate REJECTED, purely to score the gate, and records the extractor's
// verdict (ADR-0044). An accept verdict on a rejected page is a false-drop, and
// this is the only instrument in production that can observe one — a dropped page
// produces no listing, no log line and no metric movement, and is never
// reconsidered, because with no listing saved the Collection Cycle's visited-seeding
// never seeds it.
//
// It is built WITHOUT a Corpus repository, deliberately and permanently. This lane
// is fed the pages the gate decided are not single job postings, so saving one would
// inject exactly the junk the gate exists to remove. ADR-0044 requires that to be
// structurally impossible rather than branch-guarded: a flag on the shared extract
// task would be one forgotten early-return away from writing to the Corpus. There is
// no repository here and no code path that could reach one. Do not add one, and do
// not import the job-listing processor to reuse its port — the port is declared
// locally so this package depends on nothing that can write.
package shadowextractionprocessor

import (
	"context"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// Extractor converts a crawled page into an Extraction carrying the extractor's
// verdict on whether the page is a single job posting. It is the extract lane's
// port re-declared here rather than imported: the SAME extractor instance is wired
// into both lanes, so a shadow verdict is the verdict the page would have received
// had the gate kept it.
type Extractor interface {
	Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.Extraction, error)
}

type Config struct {
	// Extractor is the same extractor the collection extract stage uses. REQUIRED.
	Extractor Extractor
	// Recorder records the verdict on the Shadow Extraction counter. Optional: a nil
	// Recorder records nothing.
	Recorder llmobs.Recorder
	// There is deliberately no Corpus repository here. See the package comment.
}

// ShadowExtractionProcessor scores the Extract Gate by extracting the pages it
// rejected. It implements processor.Processor[crawler.RawJobListing] and persists
// nothing.
type ShadowExtractionProcessor struct {
	extractor Extractor
	recorder  llmobs.Recorder
}

var _ processor.Processor[crawler.RawJobListing] = (*ShadowExtractionProcessor)(nil)

func NewProcessor(cfg *Config) *ShadowExtractionProcessor {
	recorder := cfg.Recorder
	if recorder == nil {
		recorder = llmobs.Nop()
	}
	return &ShadowExtractionProcessor{
		extractor: cfg.Extractor,
		recorder:  recorder,
	}
}

// Process extracts one gate-rejected page and records the extractor's verdict on
// the Shadow Extraction counter. It saves nothing — there is nothing to save into.
//
// It ALWAYS returns nil, so the durable stream acks and deletes the entry on the
// first delivery. A lost sample is acceptable; a stuck stream is not. Retrying a
// failed measurement would spend real model calls re-measuring, and a poison entry
// would cycle through the reclaimer until it dead-lettered — cost and noise for a
// sample nobody is waiting on.
//
// Extraction.Free (ADR-0042) is deliberately ignored: the save processor reads it to
// keep the call counters honest, but nothing here enters those counters at all. A
// verdict reached from the page's own structured data is still a verdict, and a free
// one.
func (p *ShadowExtractionProcessor) Process(ctx context.Context, workload *crawler.RawJobListing) error {
	extraction, err := p.extractor.Extract(ctx, *workload)
	if err != nil {
		// Warn, not Error: a failed measurement is not a failed crawl, and this lane
		// must never make a healthy Collection Cycle look broken.
		slog.Warn("shadow_extraction_processor: shadow extraction failed, sample lost",
			"err", err, "url", workload.URL.RawURL)
	}
	verdict := llmobs.ShadowVerdictOf(err, extraction.IsJobPosting)
	p.recorder.Shadow(ctx, verdict)
	if verdict == llmobs.ShadowAccept {
		slog.Info("shadow extraction: the extract gate rejected a page the extractor reads as a single job posting (false-drop sample)",
			"url", workload.URL.RawURL)
	}
	return nil
}
