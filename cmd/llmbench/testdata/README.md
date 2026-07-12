# llmbench Gold Set

The committed, human-owned fixture set the `llmbench` classifier benchmark scores
against (ADR-0008, spec #44, ticket #50). Each entry in `manifest.json` pairs a
frozen HTML page under `pages/` with its real URL and ground-truth
`label`/`category`. The pipeline classifies at `url`, so the stored bytes and the
URL are the same page.

## How it was built

Fixtures were frozen with `llmbench capture <url>`, which fetches through the
crawler's **own** `downloader` (matching User-Agent, no JS execution) — so the
bytes are exactly what the pipeline sees. Candidate URLs came from the discovery
**Catalog** (`career_page` rows, positives + single postings; a sampling *hint*
only — the Catalog is ~45% accurate, #45) plus curated `aggregator` / `culture_about`
/ `unrelated` negatives the Catalog can't supply. On a redirect, `url` is the final
resolved URL and the original request is recorded in `note`.

## Labels are verified

Labels were produced in three passes and are now **human-owned ground truth**
(`verified: true`):

1. **Provisional** — each fixture seeded from its sourcing bucket.
2. **Model proposal** — a stronger model, driven interactively via Claude Code,
   proposed a `label`/`category` per fixture.
3. **Full-content review** — every fixture whose proposal disagreed with the
   provisional label was adjudicated against the **full** page (not the pipeline's
   1500-char cap), and 17 categories were corrected. The distinction applied: a
   careers **entry/overview** page that seeds the crawler to jobs one level deeper
   (often an external ATS) is a hub (`hub_self_hosted`); a "working-at-us" /
   "career-development" culture sub-page, with jobs on a sibling page, is
   `culture_about`.

## Strata (80 fixtures)

Binary `label` (`career_page` / `not_career_page`) drives scoring; `category`
slices the report.

| Category | Polarity | Count | Role |
|---|---|---|---|
| `hub_ats_root` | + | 16 | ATS board root — Gate certain-accepts |
| `hub_self_hosted` | + | 33 | Self-hosted careers hub / entry page — LLM confirms |
| `job_posting_single` | − | 6 | A single posting (dangerous false-positive) |
| `culture_about` | − | 11 | Career-adjacent prose / hiring-process page (LLM trap) |
| `aggregator` | − | 3 | Multi-company board — Gate certain-rejects |
| `unrelated` | − | 11 | Homepage / pricing / blog / news / software-docs |

49 positives · 31 negatives · all six strata populated.

Two fixtures guard the #63 posting-path work: `0079-atlassian-com-company-careers-all-jobs`
is a Terminal-Hub-Word deep hub (`/company/careers/all-jobs`) the exemption must keep
out of the posting-path veto (else a Leak), and `0078-docs-rundeck-com-docs-manual-jobs`
is a software-docs page (`/docs/manual/jobs`) the `docs` reject-path must shed (else a
False-Certain).

## Gate findings on the verified set

The Gate scorecard is a hard regression guard (non-zero exit on any Leak,
False-Certain, or per-category gate violation). Against the verified labels it now
surfaces **one genuine gate gap** — the harness doing its job, not a fixture error.
`bench` therefore exits non-zero until it is addressed in the gate (discovery /
ADR-0007 work, tracked separately from this benchmark):

- **0 Leaks** — the gate rejects no real Career Page on this set.
- **0 Violations** — every ATS board root certain-accepts and every aggregator
  certain-rejects (the earlier `job-boards.eu.greenhouse.io` ATS-host and
  `remoteok.com` aggregator gaps are resolved in `internal/catalog`).
- **False-Certain — `businessinsider.com/careers`**: a news section certain-accepted,
  skipping the LLM veto, purely because its path contains a `careers` segment. This is
  a content/aggregator gap, not a posting-path one — it is closed by the
  Aggregator-denylist sub-issue of spec #62, which adds `businessinsider.com` to
  `catalog`'s aggregator hosts. (The earlier `governikus.de/karriere/arbeiten-bei-uns`
  culture-page False-Certain is already resolved.)

## Growing the set

`llmbench capture <url>` appends a fixture + stub; label it and set `verified: true`
to fold new pages in. Keep every stratum populated
(`TestLoadManifest_CommittedSet` guards this).
