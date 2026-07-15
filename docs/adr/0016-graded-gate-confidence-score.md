# Graded Gate: a Confidence Score in place of the final-rung boolean

The career-page Gate's final rung decided an unresolved candidate with a single
boolean (a career keyword OR at least one linked Job Listing), forwarding every
accept to the LLM as *uncertain*. That spends an LLM Confirmer call on obvious
hubs (an ATS Embed, a structured-data openings index, twenty same-host postings)
and on barely-career-ish pages alike. We replace that rung with an additive
**Confidence Score** over cheap Structural and lexical signals, thresholded into
the Gate's existing three verdicts: a score above `certainθ` certain-accepts
(skips the LLM), below `rejectθ` rejects, and the band between stays uncertain and
reaches the LLM as before. Rungs 0–4 — aggregator reject, ATS `Classify`,
reject-path reject, the posting-path veto (ADR-0010), and career-hub-root
certain-accept — are unchanged and still short-circuit first.

The score is **purely additive**: weak signals may sum to certain-accept, with
`certainθ` alone holding False-Certains at zero. This deliberately widens the
meaning of *certain* — previously "structurally definitive" — to also cover an
accumulation of Structural Signals; the benchmark's False-Certain guard
(ADR-0008), not the old structural-definiteness invariant, is now what makes the
certain path safe. Revisit trigger: if the benchmark cannot reach zero
False-Certains under accumulation, make at least one Structural Signal a necessary
condition for certain (floor the certain path) — chosen but not built.

Signal **weights are hand-set and coarse** (an ATS Embed outweighs a title
keyword), deliberately not fitted. Only the two thresholds are tuned against the
Gold Set, and placed in the widest failure-free score-gap (for margin) rather than
at the Gold-Set LLM-call-rate minimum — because the Gold Set is ~100 stratified
fixtures whose global call rate is composition-dependent (ADR-0008), so fitting
several continuous knobs, or minimising to the failure boundary, would overfit.
Live drift is watched via the real-distribution gate-hit rate.

This shifts `LLMGateConfig` from curated string lists to curated lists **plus
tunable floats** (the weights and thresholds); it stays in-memory and
process-wide — no persistence, no json tags — as it is today.

**Fail-safe rule:** every unknown — an unrecognised ATS provider, a missing
embed-container marker, unparseable structured data, an unweighted signal — leaves
the page in the uncertain band (the LLM confirms), never in a new Leak or
False-Certain. An incomplete curated list costs missed LLM savings, not
correctness.

## Why

Cost is calls × price (ADR-0007) and the Gate's whole job is to hold call volume
down; a binary final rung cannot spend an LLM call only where the decision is
genuinely ambiguous. A deep-research pass confirmed the cheap-gate-then-escalate
shape as a standard LLM cascade with calibrated confidence deferral — but the
calibrated-deferral results assume a trainable model with a real hold-out, and we
have a deterministic Gate and ~100 curated fixtures, so we instantiate the idea as
hand-set weights with two bench-placed thresholds rather than a fitted calibrator.

## Considered options

- **Keep the boolean, add more boolean rungs.** Explainable and in keeping with
  the existing Gate, but every new signal multiplies the hand-written
  combinatorics, and it cannot express "two medium signals together are enough."
  Rejected in favour of an additive score.
- **Fit weights and thresholds together against the Gold Set.** Maximally
  expressive, but several continuous knobs fit to ~100 stratified pages overfits,
  and the weights become fixture-specific magic numbers undefendable in a benchmark
  diff. Rejected for hand-set weights plus two tuned thresholds.
- **Floor the certain path on a required Structural Signal.** Keeps *certain*
  structurally legible and blocks weak-signal accumulation from reaching the fatal
  certain band, but adds a gate the score must clear on top of the threshold.
  Deferred to the revisit trigger; pure accumulation is simpler and the
  False-Certain guard already catches its failure mode.
- **Render pages with a headless browser to read JS-injected boards.** Would
  classify client-side-only career pages the no-JS crawler sees as empty, but at
  discovery scale it inverts the ADR-0007 cost model — an expensive render on every
  page to save a cheap Confirmer call on a few — and it addresses recall, not call
  volume. Out of scope; a separate spec if the SPA recall gap proves real.
