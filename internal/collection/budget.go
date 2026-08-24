package collection

import (
	"math"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// scopeBudgetHeadroom is the share of the Frontier's seen-memory ceiling a Cycle's
// admissions may claim (ADR-0053). The remaining fifth is headroom for the ADR-0035
// visited pre-pass, which seeds every known-open posting URL into the seen-memory
// before the walk starts, so those entries must not come out of the Scopes' share.
const scopeBudgetHeadroom float64 = 0.8

// minScopeBudget is the floor on a derived Scope Budget: below it a Company's walk
// fails outright rather than merely truncating, so the floor wins over the division
// even though that forfeits the ceiling (ADR-0053, which reports the forfeit through
// ScopeBudget.HoldsCeiling rather than absorbing it silently). PROVISIONAL: it is the
// one judgement call left in an otherwise derived number, and it decides when the
// termination guarantee expires. It should sit above the p99 of a real Company's
// contribution, readable off any running Cycle's Frontier — encodeMember already
// writes Scope into every queued member — and be re-pinned against that read.
const minScopeBudget int = 500

// maxScopeBudget is the ceiling on a derived Scope Budget: no single Company needs
// more, and it sits far below the 100k-800k contributions of the observed traps
// (ADR-0053). PROVISIONAL for the same reason as minScopeBudget.
const maxScopeBudget int = 10_000

// ScopeBudget is one Cycle's derived Scope Budget together with the facts the
// derivation rests on (ADR-0053). It is returned, never logged from inside, so the
// floor-override branch is exercised by the derivation's own table test instead of
// becoming an untested conditional at the call site.
type ScopeBudget struct {
	// URLsPerScope is how many URLs one Scope may contribute to the Cycle's
	// Frontier. Zero disables the budget entirely, mirroring the Frontier's
	// seen-memory-cap convention that a non-positive value means "no bound".
	URLsPerScope int
	// Scopes is the number of DISTINCT Scopes among the Cycle's crawl Seeds — the
	// denominator the division used. Seeds sharing one Company count once.
	Scopes int
	// HoldsCeiling reports whether the derivation still fits under the seen-memory
	// ceiling: true when Scopes × URLsPerScope stays within the headroom share, so
	// the Cycle's seen-memory never has to forget and ADR-0036's termination
	// guarantee survives. It goes false once the floor, not the division, sets
	// URLsPerScope — the floor still wins, but the lapse must be announced.
	HoldsCeiling bool
	// RequiredVisitedCap is the seen-memory ceiling that would restore the
	// guarantee (Scopes × minScopeBudget ÷ scopeBudgetHeadroom). Zero when
	// HoldsCeiling is true, so a caller never reports a fix for a healthy Cycle.
	RequiredVisitedCap int
}

// DeriveScopeBudget derives a Collection Cycle's per-Scope budget from the Frontier's
// seen-memory ceiling and the Cycle's own crawl Seeds (ADR-0053):
//
//	budget      = visitedCap × 0.8      // headroom for the ADR-0035 visited pre-pass
//	scopeBudget = clamp(budget / distinct Scopes, 500, 10_000)
//
// Dividing the ceiling among the Scopes the Catalog supplies is what makes the sum of
// all budgets provably fit underneath it, so a Cycle's seen-memory never has to forget
// and the termination guarantee of ADR-0036 survives. The number is derived, never
// chosen: there is no knob for it.
//
// It is pure — no Redis, no Frontier, no I/O, no clock, no logging — so the floor, the
// ceiling and the crossover between them are covered without standing up a crawl. Both
// inputs are already in hand where a Cycle builds its Frontier, so the Scope count is
// exact and costs no extra query: a Cycle's Seeds are a one-shot read taken at cycle
// start, so the denominator cannot grow underneath a running Cycle.
//
// Seeds are counted by DISTINCT Scope, not by Seed: two Career Pages resolving to one
// Company are one Scope and share one budget, exactly as the Frontier will charge them.
// A Seed carrying no Scope is not counted — an empty Scope is the roam signal
// (ADR-0021) and is never gated, so counting one would shrink every real Scope's share
// for a Scope that can never spend it. With no countable Scope, or with no ceiling to
// divide, the returned budget is zero, which disables the gate rather than dividing by
// zero or handing out a floor that was never derived from anything.
func DeriveScopeBudget(visitedCap int, seeds []crawler.Seed) ScopeBudget {
	scopes := countDistinctScopes(seeds)

	// Truncating the share downward is the conservative direction: the admissions
	// can never claim more than the headroom the pre-pass left them.
	budget := 0
	if visitedCap > 0 {
		budget = int(float64(visitedCap) * scopeBudgetHeadroom)
	}

	// Built in one place, never returned as a bare zero value: the zero value reads
	// HoldsCeiling false, which is the alarming reading, so every path sets it
	// deliberately. With no Scope to bound there is nothing to warn about.
	out := ScopeBudget{
		Scopes:       scopes,
		HoldsCeiling: minScopeBudget*scopes <= budget, // ADR-0053: the floor overrides once this fails
	}
	if scopes > 0 && budget > 0 {
		out.URLsPerScope = min(max(budget/scopes, minScopeBudget), maxScopeBudget)
	}
	if !out.HoldsCeiling {
		// Rounded up: a ceiling that rounded down would not actually restore the
		// guarantee, and an operator raising CRAWL_VISITED_CAP to it would still
		// come up short with no signal.
		out.RequiredVisitedCap = int(math.Ceil(float64(minScopeBudget*scopes) / scopeBudgetHeadroom))
	}
	return out
}

// countDistinctScopes counts the distinct non-empty Scopes among seeds — the
// denominator of the derivation. Seeds are pre-resolved by RouteSeeds, which already
// stamped each crawl Seed's Scope via catalog.Identify, so this never re-derives a
// Scope from a URL: a second derivation could diverge from the one the Frontier will
// actually fence and charge against.
func countDistinctScopes(seeds []crawler.Seed) int {
	seen := map[string]struct{}{}
	for _, s := range seeds {
		if s.Scope == "" {
			continue
		}
		seen[s.Scope] = struct{}{}
	}
	return len(seen)
}
