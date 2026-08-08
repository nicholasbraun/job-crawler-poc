# The Extract Gold Set

Real pages the **live extract stage actually decided on**, stored as the parsed
`crawler.Content` the pipeline itself produced (ADR-0043, #254). Each row carries the
page URL, the extractor's original verdict, a three-way label
(`detail` / `hub-index` / `residue`), its structural stratum, its sampling weight, and
its label provenance.

This replaces `../extract-testdata` as the Extract Gate's **evidence base**. Those
fixtures are synthetic — invented domains, every `detail` page carrying exactly one
structured posting node and every `hub-index`/`residue` page carrying none — so any
mechanism that reads structured data scores perfectly on them by construction. They
survive as cheap regression cases for the reject rungs and nothing more.

## Files

| File | What it is |
|---|---|
| `goldset.jsonl` | The substrate. One `goldRow` per line, sorted by URL, ~2.2 MB. **Generated — never hand-edited.** |
| `labels.tsv` | The review surface rendered from the substrate: one line per row. This is the file a reviewer reads in a diff and edits to correct a label. A test asserts the two never drift. |
| `expected.tsv` | The second review surface (#256): one line per row the Free Extraction fires on — its expected title, location and working mode, plus any accepted fire. 70 lines. Also rendered from the substrate, also drift-tested. |

`goldRow` is the extract-capture record (`internal/extractcapture`: `url`, `verdict`,
`ts`, `content`) **extended in place**, not a new format. An unlabeled capture file and
a labelled gold-set row therefore read through the same decoder, `score-capture` scores
this file unchanged, and a later spec can add a random stratum or the `expected`
field-fidelity block with no migration.

## Provenance

- **Source**: the extract-decision tap's local capture,
  `EXTRACT_CAPTURE_PATH=<repo>/capture/extract-capture.jsonl` (gitignored, never
  committed). 5669 records captured `2026-07-24T22:51Z` .. `2026-08-06T21:27Z` across
  three collection sessions. Those records predate the Free Extraction (ADR-0042), so
  every accept verdict in them is the model's.
- **Re-capturing since ADR-0042**: the tap still fires on the free path, so an accept
  row's `verdict` is now the *pipeline's*, not necessarily the model's, and the
  per-verdict accept cap fills faster with lone-posting pages. A harvest intended as
  model-verdict evidence must run with `EXTRACT_FROM_JSONLD=false` for its duration.
- **Deduped by URL** to 4271 pages, latest capture winning (the newest parse is closest
  to today's parser). 1395 duplicate lines collapsed; 3 lines dropped as oversized
  (> 512 KB raw); 0 dropped for an unparseable URL.
- **Sampled** 2026-08-07 with `llmbench goldset-sample -seed extract-goldset-v1` under
  the plan in `../goldset.go`. Same capture + same seed → byte-identical file.
- **Labels** were **proposed by an LLM** (the #254 delivery agent) from each page's own
  title, text and outbound-link counts, with the structured data *deliberately
  withheld* — see "The labeling protocol" below. **No human has confirmed any row yet**;
  `label_provenance.confirmed_by` is empty everywhere and
  `pendingHumanConfirmations` in `../goldset_test.go` is the machine-visible count of
  the gap.

## Why parsed Content and not re-fetchable HTML

ADR-0008 rejected pre-parsed fixtures for the classifier Gold Set, and that set still
keeps raw HTML: its whole point is re-running the live parser over frozen bytes.
ADR-0043 reverses that **only here**. For the Extract Gate a sampled page is evidence
about a *stream*, and re-fetching its HTML months later yields a different page — drift
that corrupts the label itself, which is worse than parser-blindness. The accepted cost
is that parser changes no longer flow into these verdicts; ADR-0044's Shadow Extraction
is the compensating live measurement.

## The sampling design

The stratum is **structural**, decided by the ADR-0042 lone-posting predicate over the
page's JSON-LD — never by the label. You cannot stratify on a label you have not
produced yet, and a stratum defined by the label would make the sample circular. The
sampling cell is `(stratum, verdict)`.

| stratum | verdict | population | sampled | weight |
|---|---|---:|---:|---:|
| `lone-posting` (1 `JobPosting`, no `ItemList`, non-empty title) | accept | 777 | 46 | 0.4762 |
| `lone-posting` | abstain | 24 | **24 (all)** | 0.0398 |
| `ambiguous-posting` (≥2 nodes, an `ItemList` of jobs, or a title-less node) | accept | 0 | 0 | — |
| `ambiguous-posting` | abstain | 1 | **1 (all)** | 0.0398 |
| `no-posting` | accept | 1037 | 33 | 0.8859 |
| `no-posting` | abstain | 2432 | 45 | 2.1525 |
| **total** | | **4271** | **149** | Σ = 149 |

The mechanism's own stratum is sampled heavily and its **abstain half exhaustively**:
that half is the model-override population — pages the extractor refused that
nonetheless publish a lone structured posting, i.e. exactly where a Free Extraction can
silently create a listing the model rejected. The `no-posting` cells are the matched
`hub-index` / `residue` set.

`ambiguous-posting` has a population of **1 in the whole capture**. Structured
openings-index pages barely survive the Extract Gate's own JSON-LD-hub reject rung, so
they never reach the tap. The stratum is real but too thin to score anything; a
decorator that must refuse that shape needs its own unit tests, not this file.

## Weights

The capture is **capped per verdict**, so its raw accept/abstain mix is sampling design,
not stream (ADR-0043: "the caps are the sampling design"). `liveAcceptShare`
reconstructs the pre-cap mix from the capture's own timeline: records are grouped into
sessions (a `ts` gap over 1 h starts a new one, since the tap appends across process
restarts and its caps reset with each), and within a session every record after
`min(last accept, last abstain)` is dropped — from that point one verdict had capped and
the mix stopped describing the stream.

| session | records | accepts | abstains | kept after the cap cut | accept share |
|---|---:|---:|---:|---:|---:|
| 1 (2026-07-24) | 1000 | 500 | 500 | 523 | 0.0440 |
| 2 (2026-07-25) | 1222 | 554 | 668 | 859 | 0.2224 |
| 3 (2026-08-06) | 3447 | 1447 | 2000 | 3440 | 0.4186 |
| **pooled** | 5669 | 2501 | 3168 | 4822 | **0.3430** |

The raw file mix is 0.4412, so the caps do distort it — by about 29% relative, not the
order of magnitude the ticket assumed. The correction that genuinely matters here is
**structural**: the `lone-posting`/abstain cell is sampled at 100% against 1.8% for
`no-posting`/abstain, a ~54× oversample, and the weights invert exactly that. Each row's
weight is its inverse inclusion probability, normalized so the file's weights sum to its
row count (asserted to 1e-6).

Session 1 is worth reading on its own: cap-corrected it shows a **4.4% accept share**,
right at the ~5.5% yield the extract-cost work assumed. Session 3's 42% is a genuinely
different stream, not a cap artifact (its abstain cap bound only on the final 7
records). Pooling them is the honest thing the estimator can do without extra
assumptions, but a later spec that needs a stream-composition number should pick the
session that matches the crawl it is reasoning about, not this pooled figure.

## The labeling protocol

`llmbench goldset-worksheet` renders the labeler's view: the page's own title, two
windows into its main content, and its outbound-link counts (`urls_total`,
`urls_same_host`, `urls_joblike`). It **withholds the stratum and every JSON-LD-derived
field**, and shuffles the row order. If a labeler could see that a page publishes a lone
`JobPosting`, the labels would agree with the structured data by construction — the exact
circularity ADR-0043 exists to end. A test asserts the worksheet leaks neither.

Rubric, judging what the *page* is rather than what the URL promises:

- **`detail`** — the page *is* one job posting: one role's responsibilities/requirements
  and an apply action. A "similar jobs" rail at the bottom does not change this.
- **`hub-index`** — the page lists openings: a board root, search results, a department
  listing. A page listing exactly one opening is still `hub-index`.
- **`residue`** — neither: culture/about/benefits landing pages, blogs, press, contact
  and privacy pages, cookie or login walls, JS shells with no parsed content, 404s, and
  "this position has been filled" pages with no role body.

Rows the labeler could not decide carry `uncertain — …` in their note; there are 6.

| | `detail` | `hub-index` | `residue` |
|---|---:|---:|---:|
| `lone-posting` | 51 | 0 | 19 |
| `ambiguous-posting` | 0 | 0 | 1 |
| `no-posting` | 24 | 7 | 47 |

## Scorecard (today's gate, `DefaultLLMGateConfig`)

`go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl`
— no network, no model. **Exits 1** on the false-drop below.

```
total             149
extract-calls     148
extract-call-rate 0.9933  (soft, no threshold)
overall           precision 0.5000  recall 0.9867  f1 0.6637  accuracy 0.4966
detail     recall 0.9867  (n=75, extracted 74, skipped 1)
hub-index  accuracy 0.0000  (n=7,  skipped 0, leaked 7)
residue    accuracy 0.0000  (n=67, skipped 0, leaked 67)
residue-count 67, residue-extracted 67
```

**Read the drop side of this scorecard with suspicion.** The capture tap sits
*downstream* of the Extract Gate (`url_processor` calls `ShouldExtract` before the
extractor runs), so all but one row is a page the gate already admitted. This file
measures the gate's **leak** side honestly and its **drop** side essentially not at all
— an extract-call rate of 0.99 is the sampling frame showing through, not a
measurement. ADR-0044's random stratum and Shadow Extraction are what close that gap.

## The Free Extraction fidelity check (#256)

A Free Extraction (ADR-0042) saves a Job Listing with **no model veto anywhere in the
loop**, so both of its failure modes are silent: it fires on a page that is not a single
posting, or it fires correctly but mis-reads a field. Neither shows up in any live
metric — a wrong listing looks exactly like a right one.

```bash
go run ./cmd/llmbench score-free          # exit 1 on either failure mode
go test ./cmd/llmbench/ -run FreeExtraction   # the same check, enforced by CI
```

`score-free` replays the **real** `freeextraction.Extractor` over every row with a stub in
place of the model, so "served free" is observed from a stub that was never called rather
than read off the extraction's own `Free` marker. No network, no model, no Docker.

**Fatal** (prints red, exits non-zero): a fire on a `hub-index` or `residue` row that is
not explicitly accepted; a field diverging from its expected value; a fire with no
expected value to score it against; an expected value on a row it did not fire on. The
last two exist so that deleting an `expected` block cannot silently disarm the guard, and
a mechanism that stopped firing entirely cannot produce a clean scorecard.

**Soft, no threshold** (ADR-0020's split between a hard structural guard and
composition-dependent measurement), as measured today:

```
total             149      free              70
free-rate         0.4698   stream-free-share 0.1534  (sampling-weighted)
detail            75 (free 51)
coverage          0.6800   weighted-coverage 0.4996
```

### Where the expected values come from

An **independent** `jq` + `python3` read of the same JSON-LD bytes
(`scripts/propose-expected.sh`), never the Go traversal under test — generating them by
running `crawler.LonePosting` would make the check a tautology, able to catch a future
regression but never a bug that exists today. All 210 values (70 titles, 70 locations, 70
working modes) matched the Go read exactly on the first pass, including the 6 rows whose
`jobLocation` is a multi-office array, the 2 whose address is a bare string, and the
entity-encoded titles (`Vendeur.se &quot;Ville&quot;`, `People &amp; Culture Manager`).

### Accepted fires — 19 residue rows

The mechanism fires on **19 rows labelled `residue`**. They are counted and printed but
not fatal, because each carries an explicit `free_ok` flag and a written reason **on the
row** (never a softened threshold and never a code-side allowlist). A `hub-index` row can
never be excused: a hub is the exact shape ADR-0042's predicate rejects structurally, so
excusing one would mean the predicate itself is broken.

| kind | n | hosts |
|---|---:|---|
| stale ad — withdrawn or filled, `JobPosting` JSON-LD still served | 16 | `career.avenga.com`, `career.happysocks.com` ×3, `careerpoland.autoliv.com`, `careers.atlantishealth.com`, `careers.beyond-vision.com` ×2, `careers.patria.com`, `careers.pit.com`, `careers.spacelift.io`, `careers.sundayapp.com`, `careers.tekever.com`, `careers.worldpackers.com` ×2, `jobs.pwc.de` |
| evergreen non-role — talent pool / general application / enquiry page | 3 | `careers.coverflex.com`, `karriere.mri.bund.de`, `www.fluidstack.io` |

The 16 stale ads are a **liveness** problem (ADR-0035's territory): the fields are read
correctly and the job is gone. The 3 evergreen pages are genuine **precision** failures
and the strongest argument for narrowing ADR-0042. Stated plainly: against the labels as
they stand, the check passes *because those 19 are explicitly accepted*, not because they
do not happen.

## Findings

**1. A false-drop the synthetic fixtures cannot produce.**
`hiring.cafe/job/softwareentwickler-m-w-d-devops-level-4-…` is a real single posting
(one JSON-LD `JobPosting`, full body, the live extractor accepted it) that today's gate
**skips**: its "similar jobs" rail carries 8 same-host `/job/<slug>` links, tripping the
posting-saturation rung at `JobLinkSaturationCount` K=5. That constant was calibrated on
the synthetic fixtures — where a `detail` page has a sparse sidebar by construction — so
this is the first real evidence about it. Out of scope for #254 (no gate code changes);
worth a follow-up.

**2. 17 of the 24 exhaustively-sampled `lone-posting` abstains are not postings.**
Fifteen of them are **expired or withdrawn ads** ("this position is no longer active",
"a vaga já não está disponível") whose page body is gone but whose `JobPosting` JSON-LD
block is still served. The remaining 7 are real postings the model wrongly abstained on.
ADR-0042 measured the Free Extraction firing on "2 of 1168 abstains … both real postings
the model wrongly abstained on"; on this larger, later capture the lone-posting abstain
cell is 24 pages and **71% of it is stale structured data**. A Free Extraction that
fires on structure alone would resurrect them. Whether that matters depends on liveness
handling downstream — but it is the opposite of the shape ADR-0042's measurement
implied, and it deserves a second look before that decorator ships on by default.

**#256 measured exactly that risk.** The Free Extraction fires on **70 of the 149 rows**
— precisely the `lone-posting` stratum — and **19 of those 70 are labelled `residue`**:
16 stale ads and 3 evergreen non-roles. See "The Free Extraction fidelity check" above.

**3. 24 of the 75 `detail` pages publish no structured data at all.** A structured-data
path can never reach them; whatever admits and extracts those pages stays the cost
driver.

## Regenerating and maintaining the set

```bash
# 1. resample (destructive: overwrites both files, dropping all labels)
go run ./cmd/llmbench goldset-sample -capture <repo>/capture/extract-capture.jsonl

# 2. the labeling view (a working artifact, never committed)
go run ./cmd/llmbench goldset-worksheet -out /tmp/worksheet.jsonl

# 3. fold labels in from a proposer's id<TAB>label<TAB>note file
go run ./cmd/llmbench goldset-apply -proposals /tmp/proposals.tsv \
    -proposed-by "llm:<model id> (…)"

# 4. propose the expected extractions from an independent read of the JSON-LD
scripts/propose-expected.sh
go run ./cmd/llmbench goldset-apply \
    -expected-proposed-by "script:propose-expected.sh (…)"

# 5. score it
go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl
go run ./cmd/llmbench score-free
```

To **correct a label**, edit the `label` column of `labels.tsv` and run
`goldset-apply`; it rewrites both files canonically and refuses a sheet whose stratum or
verdict disagrees with the substrate.

## Human confirmation — what is still owed

ADR-0043 requires a human confirmation on every row carrying a lone structured posting:
those are the rows a later false-drop guard is decided on, and where a labeler is most
likely to be wrong. **All 70 `lone-posting` rows are still unconfirmed.**

```bash
# the whole review surface is 70 lines, not the 2.2 MB substrate
awk -F'\t' '$3=="lone-posting"' cmd/llmbench/extract-goldset/labels.tsv | column -t -s$'\t' | less -S
#   read the 24 rows with verdict=false first: that is finding 2 above.

# fix any wrong label in the `label` column, then:
go run ./cmd/llmbench goldset-apply -confirmed-by "<your name>" -confirm-stratum lone-posting

# then set pendingHumanConfirmations to 0 in cmd/llmbench/goldset_test.go and commit.
```

### Second pass — the 70 expected extractions (#256)

Everything in `expected.tsv` is **proposed by an automated script and confirmed by
nobody**. `pendingExpectedConfirmations` in `../goldset_test.go` is the machine-visible
count of that gap.

```bash
# the entire #256 review surface is 70 lines, not the 2.2 MB substrate
column -t -s$'\t' cmd/llmbench/extract-goldset/expected.tsv | less -S
```

**(a) the 19 `free_ok` acceptances first** — they are what decides whether ADR-0042 ships
as written. If you refuse any, refuse the 3 evergreen non-roles
(`careers.coverflex.com/…spontaneous-application…`,
`karriere.mri.bund.de/Anfragen-fuer-Praktikumsplaetze-…`, `www.fluidstack.io/jobs/05c2e69c-…`).
A refused acceptance means lowering `acceptedFreeOnResidue` in `../scorefree_test.go`, and
the check then goes red until the **mechanism** is narrowed — the fix belongs there, not
in the benchmark.

**(b) the 70 title / location / work_arrangement values.** They are the page's own
declared data, so a wrong one is usually obvious against the `url` column. 13 rows should
read `remote`, 57 `unspecified`.

```bash
# edit any wrong value or drop any free_ok you refuse, then:
go run ./cmd/llmbench goldset-apply -expected-confirmed-by "<your name>"
# then set pendingExpectedConfirmations to 0 in cmd/llmbench/goldset_test.go
```

These acceptances rest on the labels underneath them, so confirming (a) without confirming
the `lone-posting` labels above is confirming half a claim.

`goldset-apply` rejects an `llm:` name for `-confirmed-by` and an `llm:`/`script:` name for
`-expected-confirmed-by`, and a test fails if any row claims a machine confirmer — the gap
cannot be closed by the tooling that opened it.

## Open question: four rows held back from confirmation

The confirmation pass confirmed 66 of the 70 `lone-posting` labels and 50 of the 54 expected
extractions. Four rows are deliberately **not** confirmed, because confirming them would
attest to something the pass found reason to doubt. They are why
`pendingHumanConfirmations` and `pendingExpectedConfirmations` are 4 rather than 0.

Three carry a closure banner above an otherwise complete ad, and are labelled `detail`:

| row | body opens with |
| --- | --- |
| `jobs.drivetlv.com/companies/uveye/jobs/85776469-…` | "This job is no longer accepting applications" |
| `talent.seedcamp.com/companies/source-dev/jobs/87201415-…` | "This job is no longer accepting applications" |
| `www.builtincolorado.com/job/product-training-specialist-us/9511859` | "Sorry, this job was removed at 04:14 p.m. …" |

These are the same failure as the sixteen withdrawn ads ADR-0042's narrowing removed, except
that **the length-ratio test cannot reach them by design**: the page really does render its
ad, so the declared/rendered ratio stays under the bound. Only the banner marks them, and a
banner is template text in whatever language the ATS ships. Labelled `detail` they are
counted among the detail fires the mechanism is measured by, which flatters it; relabelled
`residue` they become fires on non-postings and the guard goes red until either they are
accepted like the other three or a second signal is added.

The fourth is a labelling inconsistency rather than a mechanism failure:

| row | labelled | but compare |
| --- | --- | --- |
| `career.paradoxplaza.com/jobs/7673868-open-application-for-game-programmers` | `detail` | `careers.coverflex.com/jobs/7117813-spontaneous-application-…`, labelled `residue` |

Both are open applications against a role family with no specific opening. Whichever answer
is right, the set should give the same one twice.

Resolving these is a decision about what the Corpus should contain, not a stamp. Until it is
made, the four stay unconfirmed and visible.
