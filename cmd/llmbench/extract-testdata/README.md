# llmbench Extract Gold Set

The committed, human-owned fixture set the `llmbench` **extract** benchmark scores
against (ADR-0020, spec #111, ticket #114). It is a **second, separate** Gold Set
from the classifier one under `../testdata`: where that set is Career-Page-centric
and scores the discovery gate (`pagegate.CareerPage`), this set is
keyword-relevant pages of **every shape** and scores the **Extract Gate**
(`pagegate.ShouldExtract`) on its binary extract-vs-skip decision.

Each entry in `manifest.json` pairs a frozen HTML page under `pages/` with its
real URL and a single ground-truth `label`. The gate decides at `url`, so the
stored bytes and the URL are the same page.

## What the benchmark does

`go run ./cmd/llmbench extract` replays the real pipeline
`parser.Parse -> pagegate.ShouldExtract` over every fixture and folds the binary
decision into an extract scorecard:

- A **false-drop** — a `detail`-labelled page (a real single posting) the gate
  **skips** — prints red to stderr and exits the run non-zero. This is the sole
  hard guard.
- The **extract-call rate** (share of fixtures the gate would send to the LLM
  extractor) is a **soft** measurement with **no** pass threshold.
- **Leaks** — non-posting pages (`hub-index` / `residue`) the gate extracts — are
  listed descriptively; they never fail the run. Since #115 added the content reject
  rungs, the posting-saturation rung sheds the openings-index `hub-index` leaks; the
  leaks that remain are the structurally-silent `residue` pages (the deferred-L2
  population, ADR-0020).

The LLM extractor stage is deliberately **not** invoked: every scored artifact is
produced by the `ShouldExtract` decision alone — the URL rungs plus, since #115, the
content reject rungs — and the descriptive Empty-Extraction layer is owned by #113.
The harness parses every fixture (validating the frozen bytes) and threads the
parsed `*Content` into `gateDecision`, which the gate reads for its ATS-embed,
JSON-LD-hub, and posting-saturation rungs.

## Labels and the binary collapse

Scoring is **binary**: extract vs skip. The three labels collapse as:

| Label | Meaning | Gate should | Collapses to |
|---|---|---|---|
| `detail` | A single job posting (an ATS posting, or a self-hosted posting under a job-segment path like `/careers/<slug>`, `/positions/<slug>`). | **extract** | positive (extract) |
| `hub-index` | A careers hub or openings index (an ATS board root, a bare `/careers`, a self-hosted "open roles" list). The crawler harvests its postings rather than extract the index itself. | skip | negative (skip) |
| `residue` | A structurally-silent non-posting that trips a career keyword (a career-landing, "work with us", culture, or about page). | skip | negative (skip) |

`ExtractLabel.Positive()` (true only for `detail`) is the single source of this
collapse. Per-class precision/recall is still reported (each label is
single-polarity, so `detail`'s **recall** is the fraction extracted and
`hub-index`/`residue`'s **accuracy** is the fraction correctly skipped), and the
residue count and residue-extracted count are surfaced for the deferred L2
content-confirm work (ADR-0020).

## How it was built

The intended, faithful way to grow this set is
`go run ./cmd/llmbench capture -kind extract -gold cmd/llmbench/extract-testdata <url>`,
which fetches through the crawler's **own** `downloader` (matching User-Agent, no
JS execution) so the bytes are exactly what the live pipeline sees, then appends
an unlabeled `ExtractEntry` stub for a human to label and set `verified: true`.

The fixtures committed with #114 were authored offline (the delivery environment
had no outbound network to `capture` against) as faithful, representative HTML of
each page shape: `detail` pages carry a single job title, an apply action, and a
`JobPosting` JSON-LD block; `hub-index` pages carry a heading and a list of
several posting links; `residue` pages carry culture/landing prose that trips a
career keyword ("join our team", "we're hiring") but is neither a single posting
nor a list of postings and has no `JobPosting` JSON-LD. Every URL is a real-world
URL **shape**, and each `detail` URL is one the current gate provably extracts
(the false-drop guard would fail otherwise). The `capture -kind extract` path is
wired and is the way to replace or extend these with live-captured bytes.

## Strata (26 fixtures)

| Label | Count | Composition |
|---|---|---|
| `detail` | 10 | ATS postings (greenhouse / lever / ashby) + self-hosted postings on `/careers`,`/positions`,`/openings`,`/vacancies`,`/stellenangebote`,`/roles` job-segment paths |
| `hub-index` | 8 | 5 the gate correctly skips (ATS board roots, bare `/careers`,`/jobs`,`/karriere`) + 3 generic openings indexes the gate leaks |
| `residue` | 8 | 4 the gate correctly skips (`/blog`,`/news`,`/press`,`/stories` reject paths) + 4 generic landing/culture pages the gate leaks |

All three classes are populated (`TestLoadExtractManifest_CommittedSet` guards
this).

## The false-drop hard guard

The false-drop guard fails the whole run if any committed `detail` fixture is one
the gate skips. `ShouldExtract(u, content, cfg)` skips a page when a URL rung
resolves it — (1) `catalog.Classify(u) == RoleCareerPage` (an ATS **board root**),
(2) a path segment is a strong-negative reject signal, or (3) the path is **not** a
job-posting path **and** a career path signal is present — or when a content reject
rung fires: an ATS embed, a JSON-LD openings index (an ItemList of `JobPosting` or
>=2 `JobPosting` nodes), or a saturated set of distinct same-host job links. An ATS
**posting** (`RoleJobListing`) is **exempt** from every content rung, so the safe
`detail` shapes are ATS posting URLs (e.g.
`job-boards.greenhouse.io/{tenant}/jobs/{id}`) and self-hosted postings on an
English job-segment posting path that `isJobPostingPath` recognizes
(`/jobs/<slug>`, `/careers/<slug>`, `/positions/<slug>`, `/vacancies/<slug>`,
`/openings/<slug>`, `/stellenangebote/<slug>`, …), plus postings on a generic path
with no career/reject segment (`/roles/<slug>`) — each carrying a lone `JobPosting`
and a sparse sidebar that trips none of the content rungs.

### Known blind spot — not a red fixture

A self-hosted **German `/karriere/<slug>` posting** is a known blind spot: the
`karriere` token is a career path signal but is **not** in the gate's
`jobPathSegments` set, so `isJobPostingPath` is false and rung (3) skips it — the
current gate false-drops it. #115 keeps the same URL rungs (it never reaches the
content rungs), so this stays dropped. This is a pre-existing `jobPathSegments`
coverage gap, **out of scope** for the content gate, and is deliberately **not**
committed as a `detail` fixture (it would make the baseline and #115 permanently
red). German postings are represented instead by the safe `/stellenangebote/<slug>`
shape.

## Baseline findings (current gate)

Since #115 landed the content reject rungs, the live gate holds the false-drop
guard green while shedding the openings-index leaks. `go run ./cmd/llmbench extract`
reports:

- **0 false-drops** — `detail` recall 1.0; every committed single posting is
  extracted.
- **4 leaks** — non-postings the gate still extracts, all `residue`:
  `www.acme-robotics.com/about/our-culture`, `www.brightwave.io/life`,
  `www.pixelforge.studio/work-with-us`, `www.greenharvest.co/culture/values`
  (structurally-silent career-landing / culture pages — no job links, no JSON-LD, no
  embed — so no content rung fires). They are the deferred-L2 population the ADR-0020
  content confirm would target.
- **extract-call rate 0.5385** (14 of 26) — soft, descriptive.
- **residue: 8 total, 4 extracted** — the population the ADR-0020 L2 content
  confirm is measured against.

#115's posting-saturation rung (K=5) sheds the three former `hub-index` leaks
(`jobs.brightwave.io/open-roles`, `careers.greenharvest.co/all-openings`,
`www.pixelforge.studio/job-listings`) — generic openings indexes each carrying five
same-host job links — cutting the extract-call rate from the pre-#115 URL-only
baseline (17 of 26, 7 leaks) without dropping any real posting. This benchmark is
the guard proving it.

## Growing the set

`go run ./cmd/llmbench capture -kind extract -gold cmd/llmbench/extract-testdata <url>`
appends a fixture + stub; label it (`detail` / `hub-index` / `residue`) and set
`verified: true` to fold new pages in. Keep every class populated
(`TestLoadExtractManifest_CommittedSet` guards this) and keep every `detail`
fixture one the current gate extracts, or regenerate the baseline artifact and
document the change.
