package llmobs

import (
	"math"
	"sync/atomic"
)

// Stats accumulates a single run's LLM-stage tallies for the end-of-run summary
// log. It is fresh per run and never persisted -- the durable per-run row holds
// only PagesCrawled/ListingsFound (ADR-0007 keeps this probe transient). Many LLM
// workers record concurrently, so every field is atomic.
type Stats struct {
	classify kindStats
	extract  kindStats
	// shadow holds the Shadow Extraction stream's DURABLE-STAGE tallies (retries,
	// dead-letters). Its verdict tallies live in shadowVerdicts instead, because a
	// shadow verdict is deliberately not a call (ADR-0044).
	shadow         kindStats
	shadowVerdicts shadowStats
}

// shadowStats accumulates one run's Shadow Extraction verdicts. Separate from
// kindStats so a shadow verdict can never be read as a call.
type shadowStats struct {
	accepts  atomic.Int64
	abstains atomic.Int64
	errors   atomic.Int64
	// dropped counts sampled pages the lane shed before measuring them (a full
	// stream). Deliberately outside the three verdicts and outside the false-drop
	// rate: a dropped sample produced no verdict, and its only job is to say how much
	// of the sample the rate is missing.
	dropped atomic.Int64
}

type kindStats struct {
	calls    atomic.Int64
	errors   atomic.Int64
	timeouts atomic.Int64
	// abstains is the number of calls the extractor completed but disavowed (the
	// page was not a single job posting); extract-only. It still counts toward
	// calls ("sent"), so the Empty-Extraction Rate is abstains / calls.
	abstains atomic.Int64
	gated    atomic.Int64
	// seen is the number of page contents fed to this LLM kind; dup is how many
	// of those hashes had been seen before (this run or a prior one).
	seen atomic.Int64
	dup  atomic.Int64
	// retries counts durable-stage redeliveries (a pending task reclaimed for
	// another attempt); deadletter counts tasks that exhausted their attempts and
	// were moved to the dead-letter stream.
	retries    atomic.Int64
	deadletter atomic.Int64
}

// forKind routes a kind's tallies. KindShadow is listed explicitly because the
// shadow stream records durable-stage retries and dead-letters under it: without
// its own case the default would silently file them under the classifier.
func (s *Stats) forKind(kind Kind) *kindStats {
	switch kind {
	case KindExtract:
		return &s.extract
	case KindShadow:
		return &s.shadow
	default:
		return &s.classify
	}
}

func (s *Stats) recordShadow(v ShadowVerdict) {
	switch v {
	case ShadowAccept:
		s.shadowVerdicts.accepts.Add(1)
	case ShadowAbstain:
		s.shadowVerdicts.abstains.Add(1)
	default:
		s.shadowVerdicts.errors.Add(1)
	}
}

func (s *Stats) recordShadowDropped() { s.shadowVerdicts.dropped.Add(1) }

func (s *Stats) recordCall(kind Kind, outcome Outcome) {
	ks := s.forKind(kind)
	ks.calls.Add(1)
	switch outcome {
	case OutcomeError:
		ks.errors.Add(1)
	case OutcomeTimeout:
		ks.timeouts.Add(1)
	case OutcomeAbstain:
		ks.abstains.Add(1)
	}
}

func (s *Stats) recordGated(kind Kind) { s.forKind(kind).gated.Add(1) }

func (s *Stats) recordRetry(kind Kind)      { s.forKind(kind).retries.Add(1) }
func (s *Stats) recordDeadLetter(kind Kind) { s.forKind(kind).deadletter.Add(1) }

func (s *Stats) recordContent(kind Kind, duplicate bool) {
	ks := s.forKind(kind)
	ks.seen.Add(1)
	if duplicate {
		ks.dup.Add(1)
	}
}

// Summary key prefixes, one per LLM kind, disambiguating the two kinds' tallies
// on the shared end-of-run log line.
const (
	classifyPrefix = "classify"
	extractPrefix  = "extract"
	shadowPrefix   = "shadow"
)

// Summary returns the run's LLM-stage tallies as slog key/value pairs for a
// single end-of-run log line: the raw counts plus the derived rates the ADR-0007
// measurement cares about (gate hit rate, error/timeout rate, duplicate-content
// ratio), per kind.
func (s *Stats) Summary() []any {
	kv := s.classify.summary(classifyPrefix)
	kv = append(kv, s.extract.summary(extractPrefix)...)
	return append(kv, s.shadowSummary()...)
}

// shadowSummary reports the run's Shadow Extraction tallies (ADR-0044). It is
// bespoke rather than kindStats.summary because most of that struct is meaningless
// here: the shadow lane makes no gated decisions and no calls in the call-counter
// sense. The live false-drop rate is accepts over COMPLETED verdicts -- an errored
// sample produced no verdict and must not deflate it.
//
// shadow_dropped sits alongside that rate rather than inside it: a non-zero count
// says the rate was computed over an incomplete, non-uniformly shed sample, which is
// the one thing a reader has to know before trusting it.
func (s *Stats) shadowSummary() []any {
	accepts := s.shadowVerdicts.accepts.Load()
	abstains := s.shadowVerdicts.abstains.Load()
	return []any{
		shadowPrefix + "_accepts", accepts,
		shadowPrefix + "_abstains", abstains,
		shadowPrefix + "_errors", s.shadowVerdicts.errors.Load(),
		shadowPrefix + "_dropped", s.shadowVerdicts.dropped.Load(),
		shadowPrefix + "_retries", s.shadow.retries.Load(),
		shadowPrefix + "_deadletter", s.shadow.deadletter.Load(),
		shadowPrefix + "_false_drop_rate", ratio(accepts, accepts+abstains),
	}
}

func (ks *kindStats) summary(prefix string) []any {
	calls := ks.calls.Load()
	errs := ks.errors.Load()
	timeouts := ks.timeouts.Load()
	gated := ks.gated.Load()
	seen := ks.seen.Load()
	dup := ks.dup.Load()
	retries := ks.retries.Load()
	deadletter := ks.deadletter.Load()
	kv := []any{
		prefix + "_calls", calls,
		prefix + "_errors", errs,
		prefix + "_timeouts", timeouts,
		prefix + "_gated", gated,
		prefix + "_retries", retries,
		prefix + "_deadletter", deadletter,
		prefix + "_gate_hit_rate", ratio(gated, gated+calls),
		prefix + "_error_rate", ratio(errs, calls),
		prefix + "_timeout_rate", ratio(timeouts, calls),
		prefix + "_dup_ratio", ratio(dup, seen),
	}
	// Abstain is extract-only (the classifier never abstains), so the abstain
	// count and the Empty-Extraction Rate are emitted only for the extract kind --
	// under classify they would always report zero and mislead.
	if prefix == extractPrefix {
		abstains := ks.abstains.Load()
		kv = append(kv,
			prefix+"_abstains", abstains,
			prefix+"_empty_extraction_rate", ratio(abstains, calls),
		)
	}
	return kv
}

// ratio is n/d rounded to four decimals, or 0 when d is 0.
func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return math.Round(float64(n)/float64(d)*10000) / 10000
}
