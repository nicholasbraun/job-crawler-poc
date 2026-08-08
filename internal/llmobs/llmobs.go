// Package llmobs instruments the LLM stage of the crawl (the career-page
// classifier and the job-listing extractor) so ADR-0007 step 1 can measure
// where LLM calls and cost actually go before any gating/caching work is built.
// It is pure observation: call counts and latency by kind/outcome, the cheap-
// gate hit rate, and a content-duplication probe -- all exposed on the existing
// Prometheus endpoint and summarized in a per-run log line. Nothing here changes
// crawl behavior.
//
// Three collaborators fan out from a per-run Recorder: shared OTel Metrics
// (cross-run, scraped by Prometheus), a Redis-backed DupProbe (transient,
// cross-run, TTL-bounded), and per-run Stats (for the end-of-run summary log).
package llmobs

import (
	"context"
	"errors"
	"net"
)

// Kind identifies which LLM a call, gate decision, or content probe concerns:
// the discovery crawl's career-page classifier, the collection crawl's
// job-listing extractor, or the Shadow Extraction lane that scores the Extract
// Gate (ADR-0044).
type Kind string

const (
	KindClassify Kind = "classify"
	KindExtract  Kind = "extract"
	// KindShadow names the Shadow Extraction lane: its own durable stream
	// (llmstream:{runID}:shadow) and the kind label on that stream's retry,
	// dead-letter and queue-depth instruments. It is deliberately NOT a call kind --
	// a Shadow Extraction is recorded via Recorder.Shadow on its own counter and
	// NEVER on crawler.llm.calls, because folding measurement spend into the call
	// counter would corrupt the extract call rate the Extract Gate is judged by.
	KindShadow Kind = "shadow"
)

// Outcome is the coarse result of an LLM call, the outcome label on the call
// counter and latency histogram.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeError   Outcome = "error"
	OutcomeTimeout Outcome = "timeout"
	// OutcomeAbstain is an extract call the extractor completed but disavowed: the
	// page was not a single job posting. It is distinct from OutcomeOK so the
	// Empty-Extraction Rate (abstain / sent) is derivable from the call counter and
	// no abstain is counted as "ok". Extract-only; the classifier never abstains.
	OutcomeAbstain Outcome = "abstain"
)

// ShadowVerdict is the extractor's decision on one Shadow Extraction -- a page the
// Extract Gate rejected that was extracted anyway purely to score the gate
// (ADR-0044). ShadowAccept is the observable false-drop: the gate shed a page the
// extractor reads as a single job posting.
type ShadowVerdict string

const (
	ShadowAccept  ShadowVerdict = "accept"
	ShadowAbstain ShadowVerdict = "abstain"
	// ShadowError is a shadow extraction that produced no verdict at all (the call
	// failed or timed out). It is counted so a broken measurement lane is visible,
	// and excluded from the false-drop rate's denominator, which is completed
	// verdicts only.
	ShadowError ShadowVerdict = "error"
)

// ShadowVerdictOf maps one shadow extraction's result to its verdict. An error
// wins over isJobPosting: on the error path the Extraction is the zero value, so
// its verdict field means nothing.
func ShadowVerdictOf(err error, isJobPosting bool) ShadowVerdict {
	switch {
	case err != nil:
		return ShadowError
	case isJobPosting:
		return ShadowAccept
	default:
		return ShadowAbstain
	}
}

// Reason is why a page skipped the LLM: a structurally-certain ATS board root or
// a keyword-relevance miss. It labels the gated counter.
type Reason string

const (
	ReasonCertain    Reason = "certain"
	ReasonIrrelevant Reason = "irrelevant"
	// ReasonURLStructure marks a page the Extract Gate resolved without the LLM
	// extractor -- from a URL signal (a Career Page index or a reject path) or a
	// page-structure signal (an ATS embed, a JSON-LD openings index, or job-link
	// saturation) -- rather than from keyword relevance.
	ReasonURLStructure Reason = "url_structure"
	// ReasonStructuredData marks an extraction the crawler produced from the page's
	// own structured data with no model call -- a Free Extraction (ADR-0042). It is
	// the ONE gated reason that means "a Job Listing was SAVED, for free": every
	// other reason means "the page was shed and nothing was saved". Recording it on
	// the gated counter is deliberate -- that counter's documented meaning is
	// "resolved without an LLM call", which is exactly what happened -- but the two
	// polarities must never be read as one number, which is why it has its own label
	// and why the dashboard splits the counter by reason. The value matches
	// DescriptionSourceStructuredData's string so an operator reading a listing's
	// description_source and the gate reason sees one word for one mechanism.
	ReasonStructuredData Reason = "structured_data"
)

// Classify maps the error an LLM call returned to a coarse Outcome. A nil error
// is ok; a request that hit the client's timeout or a cancelled/expired context
// (surfacing as a net timeout) is timeout; anything else is error. The two
// OpenRouter clients wrap every failure with %w and draw no timeout distinction
// of their own, so the call site recovers it here.
func Classify(err error) Outcome {
	switch {
	case err == nil:
		return OutcomeOK
	case isTimeout(err):
		return OutcomeTimeout
	default:
		return OutcomeError
	}
}

// isTimeout reports whether err is (or wraps) a deadline exceeded or a net-level
// timeout -- the two shapes an http.Client Timeout / expired context produce.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
