# llmbench Gold Set

The committed, human-owned fixture set the `llmbench` classifier benchmark scores
against (ADR-0008, spec #44, ticket #50). Each entry in `manifest.json` pairs a
frozen HTML page under `pages/` with its real URL and ground-truth
`label`/`category`. The pipeline classifies at `url`, so the stored bytes and the
URL are the same page.

## How it was built

Fixtures were frozen with `llmbench capture <url>`, which fetches through the
crawler's **own** `downloader` — matching User-Agent, no JS execution — so the
bytes are exactly what the pipeline sees in production. Candidate URLs came from:

- **Positives and single-posting negatives** — sampled from the discovery
  **Catalog** (`career_page` rows), host-diversified. Catalog membership is only a
  *sampling hint*: the Catalog is ~45% accurate (#45), so it never sets a label.
- **Curated negatives** the Catalog can't supply — `aggregator` (denylisted
  job-board / professional-network hosts), `culture_about` (career-adjacent prose
  that isn't a hub), and `unrelated` (homepages, pricing, blogs).

On a redirect, `url` is the final resolved URL and the original request is recorded
in `note`.

## Strata (78 fixtures)

Binary `label` (`career_page` / `not_career_page`) drives scoring; `category`
slices the report.

| Category | Polarity | Count | Role |
|---|---|---|---|
| `hub_ats_root` | + | 15 | ATS board root — Gate certain-accepts |
| `hub_self_hosted` | + | 30 | Self-hosted careers hub — LLM confirms |
| `job_posting_single` | − | 17 | A single posting (dangerous false-positive) |
| `culture_about` | − | 6 | Career-adjacent prose (LLM trap) |
| `aggregator` | − | 3 | Multi-company board — Gate certain-rejects |
| `unrelated` | − | 7 | Homepage / pricing / blog |

45 positives (floor met) · 33 negatives (dangerous negatives over-weighted) · all
six strata populated.

## Labels are provisional — `verified: false`

Every label here is a **provisional** value derived from its sourcing bucket, not a
confirmed ground truth. Nothing is committed `verified: true` without a human
sign-off. To harden the set:

1. `llmbench label` — a **stronger** model (its own `LABELER_*` env) proposes a
   `label` + `category` per fixture, written as `proposed_*` with `verified: false`.
2. `llmbench bench` folds in a **review queue**: the unverified fixtures whose
   provisional label disagrees with the labeler, the Gate, or the pipeline verdict.
3. A human confirms those (~20) by editing `manifest.json` and flipping
   `verified: true`; they drop off the queue on the next run.

## Known findings on this provisional baseline

The Gate scorecard is a hard regression guard (non-zero exit on any Leak or
False-Certain, plus per-category gate expectations). On this initial set it
surfaces **one genuine gap**, which is the harness doing its job — not a fixture
error:

- `remoteok.com/remote-dev-jobs` — a real aggregator the Gate leaves *uncertain*
  because `remoteok.com` is not yet on the discovery aggregator denylist
  (`internal/catalog`). The denylist comment already anticipates being "extended as
  the gold-set harness (#44) surfaces more"; adding it is a one-line discovery
  follow-up, tracked separately from this benchmark.

`bench` therefore exits non-zero on the committed set until that denylist gap (or
the label review) is addressed — the guard firing as designed.

## Growing the set

`llmbench capture <url>` appends a fixture + stub; run `llmbench label` and confirm
disagreements to fold new pages in. Keep every stratum populated
(`TestLoadManifest_CommittedSet` guards this).
