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
// the discovery crawl's career-page classifier, or the keyword crawl's
// job-listing extractor.
type Kind string

const (
	KindClassify Kind = "classify"
	KindExtract  Kind = "extract"
)

// Outcome is the coarse result of an LLM call, the outcome label on the call
// counter and latency histogram.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeError   Outcome = "error"
	OutcomeTimeout Outcome = "timeout"
)

// Reason is why a page skipped the LLM: a structurally-certain ATS board root or
// a keyword-relevance miss. It labels the gated counter.
type Reason string

const (
	ReasonCertain    Reason = "certain"
	ReasonIrrelevant Reason = "irrelevant"
	// ReasonURLStructure marks a page a URL-structure signal resolved (a Career
	// Page index or a reject path) rather than keyword relevance.
	ReasonURLStructure Reason = "url_structure"
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
