# Classifier benchmark: two-layer scoring over a human-owned Gold Set

The career-page classifier is measured by `cmd/llmbench`: a single pass of the
**real** pipeline (`parser.Parse` → `pagegate.CareerPage` → `openrouter`
classifier) over one Gold Set of labeled HTML fixtures, emitting layered reports.
Because the Gate decides *which* pages ever reach the LLM, the two things worth
optimizing — the **Gate's LLM-call rate** (cost) and the **LLM's accuracy** — are
produced by the same traversal and reported as one Gate scorecard plus one LLM
scorecard plus an end-to-end scorecard, not as two separate benchmarks.

The **Gate is a hard regression guard, the LLM is a soft measurement.** A Leak or
False-Certain is a deterministic, structural error, so the benchmark prints it in
red and exits non-zero. LLM precision/recall/flip-rate carry no pass threshold —
they are for tuning, and coupling an exit code to a flaky model is worthless.

**Gold labels are human-owned but LLM-bootstrapped.** A stronger labeler model
(distinct from the model under test, its own `LABELER_*` env) proposes a label +
category for each fixture offline via `llmbench label`; the committed label is
trusted by default, and the bench run folds a review queue that surfaces only the
fixtures whose proposed label disagrees with the Gate or the pipeline — the ~20
that actually move the numbers. This keeps the manual load at ~20 not ~100 while
keeping the labels that decide precision/recall ones a human signed off on.

> **Amendment (label verb removed).** The committed `llmbench label` verb, its
> `LABELER_*` proposer, and the `proposed_label`/`proposed_category` manifest fields
> were dropped: the automated proposer did not work as intended, and the initial
> classification was instead done by a stronger model driven interactively via
> Claude Code. The bootstrap-then-human-confirm approach still holds — only the
> committed tooling for it does not. The review queue accordingly loses its labeler
> axis and now surfaces only fixtures whose committed label disagrees with the Gate
> or the pipeline.

A fixture is stored as **raw HTML + its real URL** (in `manifest.json`) and run
through the live parser every time — so a `getMainContent`/parser change flows
straight into the verdicts, which is exactly the measurement #44 exists to unblock.
Fixtures are captured through the crawler's own `downloader` (matching UA, no JS),
so the frozen bytes are exactly what the pipeline classifies.

The Gold Set is **stratified with a positive floor**, not sampled from the natural
crawl distribution: each of the six categories is filled to a minimum (~30–40 real
Career Pages spanning ATS-root and self-hosted hubs, with the dangerous negatives —
single postings, culture/about pages, aggregators — over-weighted), sourced from a
Catalog/log harvest of pages the crawler actually hits plus deliberate hard-case
curation. Catalog membership is only a sampling *hint* (the Catalog is ~45%
accurate), never the label. Because each Gate verdict is a pure function of one
page, every per-category number is mix-invariant; only the *global* LLM-call rate
depends on composition, so the report frames it as **Gold-Set-relative** (for
A/B-diffing gate configs against a fixed set) and leaves live production-volume
measurement to `llmobs`' gate-hit rate on the real distribution.

## Why

Issue #44 (under the ADR-0007 epic) needs the LLM path *measurable and
regression-testable* before extractor-cap tuning, boilerplate stripping, and
positive-set expansion can be justified by data rather than guesswork. The Gate's
whole job is to hold LLM volume down (ADR-0007: cost is calls × price), so the
benchmark's primary output is the LLM-call rate and the two irrecoverable Gate
failure modes (a dropped real Career Page, a rubber-stamped non-page), with LLM
accuracy layered on the subset the Gate actually forwards.

Determinism note: the classifier runs `temp 0` (greedy), which makes the request
seed a no-op; run-to-run drift comes from float non-associativity and batch
non-invariance on shared endpoints, not sampling. So repeats default to **N=1**
(a solo local model is reproducible enough) and are an opt-in hedge (`-n`) for
batched/hosted endpoints, scored by majority vote with a separate flip-rate.

## Considered options

- **Pure LLM labels as ground truth (no human pass).** Fully automated and cheap,
  but then the benchmark measures *agreement with a reference model*, not accuracy;
  labeler and classifier share failure modes, so correlated errors inflate the
  score on exactly the ambiguous pages the benchmark exists to expose. Kept only
  as the bootstrap draft, never as the scored truth.
- **Freeze pre-parsed `Content` as the fixtures.** Smaller diffs and
  parser-independent, but it blinds the benchmark to `getMainContent`/parser
  changes — the very thing #44 lists this tool as unblocking — and drifts from
  live parser output. Rejected in favor of storing raw HTML + URL.
- **A built-in A/B matrix runner.** Issue #44 calls this an "A/B harness," but a
  parameterized single run (`-gate-config`, `LLM_*` env) plus external JSON diff
  covers the 2–3 hand variants #42 actually compared, without bloating the flag
  surface. A matrix runner is deferred until manual diffing hurts.
- **Worst-case (unanimity-for-positive) LLM scoring.** More conservative, but it
  folds instability into the accuracy number so you can't tell a *less accurate*
  model from a *less stable* one. Rejected for majority-vote + a separate,
  by-URL flip-rate, keeping accuracy and stability orthogonal.
- **Strict scoring (only `verified: true` labels count).** Maximally trustworthy
  but forces confirming all ~100 fixtures, defeating the reason to auto-label at
  all. Rejected for trusted-by-default, with the review queue bounding the risk.
- **Sample the Gold Set from the natural crawl distribution.** More faithful to the
  production mix and yields the real call rate for free, but starves the set of
  positives and hard negatives (the #44 wide-error-bars problem) and merely
  reproduces a number `llmobs` already reports live. Rejected as the primary frame;
  a captured run still feeds the Catalog/log harvest.
