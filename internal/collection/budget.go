package collection

import (
	"log/slog"
	"math"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// scopeBudgetHeadroom is the share of the Frontier's seen-memory ceiling a Cycle's
// admissions may claim (ADR-0053). The remaining fifth is headroom for the ADR-0035
// visited pre-pass, which seeds every known-open posting URL into the seen-memory
// before the walk starts, so those entries must not come out of the Scopes' share.
const scopeBudgetHeadroom float64 = 0.8

// prePassShare is the share of the seen-memory ceiling left for the ADR-0035 visited
// pre-pass — the complement of scopeBudgetHeadroom, and the assumption the whole
// derivation rests on. The two must sum to 1; that is what makes the ceiling argument
// add up. It is spelled as its own decimal rather than as 1 - scopeBudgetHeadroom
// because that subtraction on a typed float64 constant yields 0.19999999999999996,
// which would push every ceiling computed from it one URL past the number the
// derivation actually needs.
const prePassShare float64 = 0.2

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

// DefaultScopeBudgetEnabled is what COLLECTION_SCOPE_BUDGET_ENABLED defaults to:
// every Collection Cycle runs under a derived Scope Budget (ADR-0053). It ships ON,
// the ordinary kill-switch convention — the budget IS the live behaviour once this
// ships. Pulling the switch restores the unbounded walk: no Scope is bounded, the
// Cycle's seen-memory can saturate and start forgetting, and the Cycle may never
// drain. That is the pre-ADR-0053 behaviour, kept reachable with no deploy so the
// budget can be ruled out as a cause if crawl-lane Job Listings drop unexpectedly.
const DefaultScopeBudgetEnabled = true

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

// Announce writes this Cycle's Scope Budget to the log exactly once, at cycle start
// (ADR-0053). It lives beside the derivation because the two readings a derived number
// can carry are not legible from the number itself: a budget that still holds the
// seen-memory ceiling is routine, while one the floor had to override means ADR-0036's
// termination guarantee has lapsed for this Cycle — a WARN that must name the
// seen-memory ceiling which would restore it, so the fix is a stated number rather than
// a diagnosis the operator has to perform. Exactly one line either way, so a Cycle's
// start reads as one grep.
//
// seenMemory is how many entries the run's Frontier ALREADY holds at cycle start (ZCARD
// visited, the quantity behind the crawler.frontier.visited.size gauge). It belongs on
// this line because the budget is derived per PROCESS while the seen-memory it is granted
// against is per RUN: one Cycle is one CrawlRun (ADR-0036) and only a terminal status
// sweeps its Frontier keys, so a Cycle adopted or resumed across a restart re-derives a
// full allowance against entries earlier processes already admitted. It is REPORTED, never
// judged: for an ordinary resume that carry is ADR-0053's intended reading (the spend
// carries with it, so the ceiling still holds), and warning on a non-zero reading would
// fire on every restart of a healthy Cycle. What it is FOR is telling an adopted Cycle
// from a fresh one at cycle start — the one reading whose entries may have been admitted
// under no budget at all is a Cycle adopted across the deploy that first enabled it, and
// the deploy note in README says what to do about that. A negative seenMemory means the
// reading was unavailable (the caller logs why); the attribute is then omitted rather than
// reported as 0, which would read as a fresh Frontier.
func (b ScopeBudget) Announce(runID uuid.UUID, seenMemory int) {
	attrs := []any{"run_id", runID, "scopes", b.Scopes}
	if seenMemory >= 0 {
		attrs = append(attrs, "seen_memory", seenMemory)
	}

	switch {
	case !b.HoldsCeiling:
		// Checked FIRST: a Cycle with Scopes but no derivable budget (a non-positive
		// seen-memory ceiling) reads URLsPerScope 0 AND HoldsCeiling false, and that is
		// the alarming reading, not the "nothing to bound" one below.
		slog.Warn("collection: the scope budget floor overrides the derivation, so this cycle's seen-memory can saturate and its walk may never drain; raise CRAWL_VISITED_CAP to required_visited_cap to restore the guarantee (ADR-0053)",
			append(attrs, "budget", b.URLsPerScope, "required_visited_cap", b.RequiredVisitedCap)...)
	case b.URLsPerScope <= 0:
		slog.Info("collection: no scoped crawl seeds, so no scope budget applies to this cycle (ADR-0053)",
			attrs...)
	default:
		slog.Info("collection: scope budget derived for this cycle (ADR-0053)",
			append(attrs, "budget", b.URLsPerScope)...)
	}
}

// requiredVisitedCapForPrePass returns the smallest seen-memory ceiling under which a
// pre-pass of prePass URLs and the Scope Budgets this Cycle would RE-DERIVE at that
// ceiling both fit. It is a fixed point, not prePass + today's budgets: raising
// CRAWL_VISITED_CAP also raises the share the budgets are divided out of
// (budget = ceiling × scopeBudgetHeadroom), so a ceiling that merely adds the overshoot
// hands four fifths of the raise straight back to the admissions and comes up short
// again — the same trap ADR-0053 already avoids by rounding the floor's required
// ceiling up.
//
// Two branches, because the budgets stop growing with the ceiling once the division
// clamps at maxScopeBudget:
//
//	clamped:  ceiling = prePass + maxScopeBudget × scopes  (the budgets are fixed)
//	dividing: ceiling = ceil(prePass ÷ prePassShare)       (the pre-pass gets the
//	                                                        complement of the headroom)
//
// The clamped branch is taken only when its own answer really would still clamp, which
// also covers scopes == 0 — nothing to divide, so the ceiling need only hold the
// pre-pass — with no special case.
func requiredVisitedCapForPrePass(scopes, prePass int) int {
	claimed := maxScopeBudget * scopes
	if clamped := prePass + claimed; float64(clamped)*scopeBudgetHeadroom >= float64(claimed) {
		return clamped
	}
	return int(math.Ceil(float64(prePass) / prePassShare))
}

// AnnouncePrePass writes the OTHER half of ADR-0053's ceiling argument to the log,
// exactly once, right after the ADR-0035 visited pre-pass has run. The derivation can
// only claim that the Cycle's ADMISSIONS fit under the seen-memory ceiling; the
// pre-pass — every Open Job Listing under every crawl-lane Career Page, seeded before
// the walk so the Cycle surfaces only new ones — claims the rest of that ceiling, and
// it is measured, not derived: it grows with the Corpus, and nothing in the
// Catalog-derived denominator bounds it. Unchecked it is the one assumption standing
// between the derivation and a silent failure — the budgets fit, the Cycle announces a
// budget that holds the ceiling, and the seen-memory saturates anyway, bringing
// ADR-0027's eviction and Re-admission back and costing the Cycle its end (#310).
//
// prePass is how many URLs SeedVisited seeded; visitedCap is the ceiling the derivation
// divided. The headroom prePass is measured against is what the budgets leave
// UNCLAIMED — visitedCap − Scopes × URLsPerScope — not the flat scopeBudgetHeadroom
// share: a small Catalog is clamped at maxScopeBudget and leaves far more than a fifth,
// and warning on the flat share would cry wolf for every Cycle with room to spare. It
// can go negative once the floor has overridden the derivation, which Announce has
// already warned about.
//
// This can only be said AFTER the pre-pass: the count does not exist before it, and the
// pre-pass needs the Frontier the derived number configures. It is still preventive for
// the walk, which has not admitted a URL yet, and it is the only preventive signal
// there is. The one case it cannot get ahead of is a pre-pass that alone exceeds the
// whole ceiling and has already evicted by the time this runs; the same WARN names it.
func (b ScopeBudget) AnnouncePrePass(runID uuid.UUID, visitedCap, prePass int) {
	headroom := visitedCap - b.Scopes*b.URLsPerScope

	switch {
	case visitedCap <= 0:
		// Checked FIRST: a non-positive ceiling DISABLES capping in the Frontier (the
		// ADR-0027 fail-safe), so the seen-memory never forgets and no pre-pass, however
		// large, can cost the Cycle its end. The misconfiguration itself is Announce's
		// WARN, not this one.
		slog.Info("collection: the seen-memory ceiling is disabled, so the visited pre-pass cannot cost this cycle its end (ADR-0053)",
			"run_id", runID, "pre_pass", prePass)
	case prePass > headroom:
		slog.Warn("collection: the visited pre-pass no longer fits the seen-memory this cycle's scope budgets leave it, so the cycle's seen-memory can saturate and its walk may never drain; raise CRAWL_VISITED_CAP to required_visited_cap to restore the guarantee (ADR-0053)",
			"run_id", runID, "pre_pass", prePass, "headroom", headroom,
			// Both halves of the argument have to hold, so the ceiling that restores the
			// guarantee is the larger of the two: a Cycle whose floor already overrode the
			// derivation needs Announce's number as well as this one.
			"required_visited_cap", max(requiredVisitedCapForPrePass(b.Scopes, prePass), b.RequiredVisitedCap))
	default:
		slog.Info("collection: the visited pre-pass fits the seen-memory this cycle's scope budgets leave it (ADR-0053)",
			"run_id", runID, "pre_pass", prePass, "headroom", headroom)
	}
}
