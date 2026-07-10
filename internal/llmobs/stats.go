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
}

type kindStats struct {
	calls    atomic.Int64
	errors   atomic.Int64
	timeouts atomic.Int64
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

func (s *Stats) forKind(kind Kind) *kindStats {
	if kind == KindExtract {
		return &s.extract
	}
	return &s.classify
}

func (s *Stats) recordCall(kind Kind, outcome Outcome) {
	ks := s.forKind(kind)
	ks.calls.Add(1)
	switch outcome {
	case OutcomeError:
		ks.errors.Add(1)
	case OutcomeTimeout:
		ks.timeouts.Add(1)
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

// Summary returns the run's LLM-stage tallies as slog key/value pairs for a
// single end-of-run log line: the raw counts plus the derived rates the ADR-0007
// measurement cares about (gate hit rate, error/timeout rate, duplicate-content
// ratio), per kind.
func (s *Stats) Summary() []any {
	kv := s.classify.summary("classify")
	return append(kv, s.extract.summary("extract")...)
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
	return []any{
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
}

// ratio is n/d rounded to four decimals, or 0 when d is 0.
func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return math.Round(float64(n)/float64(d)*10000) / 10000
}
