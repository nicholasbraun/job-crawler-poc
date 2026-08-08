# The Extract Gold Set

Real pages the **live extract stage actually decided on**, stored as the parsed
`crawler.Content` the pipeline itself produced (ADR-0043, #254). Each row carries the
page URL, the extractor's original verdict, a three-way label
(`detail` / `hub-index` / `residue`), its sampling stratum, its sampling weight, and
its label provenance.

The file holds **two drawings**. They share a row format and a file; they share nothing
else, and their weights are never pooled:

| drawing | strata | rows | drawn from | accept share | what it is for |
|---|---|---:|---|---:|---|
| **structural** (#254) | `lone-posting`, `ambiguous-posting`, `no-posting` | 149 | the July capture, 4271 deduped pages | 0.3432 | the Free Extraction's own population, sampled where the mechanism lives |
| **random** (#262) | `random` | 120 | the August faithful frame, 5162 deduped pages | 0.0753 | a random sample of the stream, so composition, precision and cost describe production |

This replaces `../extract-testdata` as the Extract Gate's **evidence base**. Those
fixtures are synthetic — invented domains, every `detail` page carrying exactly one
structured posting node and every `hub-index`/`residue` page carrying none — so any
mechanism that reads structured data scores perfectly on them by construction. They
survive as cheap regression cases for the reject rungs and nothing more.

## Files

| File | What it is |
|---|---|
| `goldset.jsonl` | The substrate. One `goldRow` per line, sorted by URL, ~3.9 MB across both drawings. **Generated — never hand-edited.** |
| `labels.tsv` | The review surface rendered from the substrate: one line per row, 269 lines. This is the file a reviewer reads in a diff and edits to correct a label. A test asserts the two never drift. |
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

**Weights are never pooled across drawings.** A weight is an inverse inclusion
probability normalized within the draw that produced it, so each drawing's weights sum
to *its own* row count (asserted per drawing and file-wide). The structural drawing's
rest on the July frame and an accept share of 0.3432; the random drawing's on the
August frame and 0.0753. Adding them would be meaningless arithmetic. The code makes
this explicit: `goldStratum.Drawing()` maps a stratum to its draw, and it is derived,
never stored on a row. A known limitation, and a candidate follow-up: the structural
drawing's accept-share basis is now stale — its 0.3432 was reconstructed by
`liveAcceptShare` from a capture whose sessions the estimator could still split, while
#261's census puts the true stream rate at 0.0753. Its *within-drawing* comparisons are
unaffected; any stream-level number read off it is not.

## The random stratum (#262)

### What population it describes

> The population is the stream of extractor decisions the Collection Crawl produced
> between `2026-08-07T21:13Z` and `2026-08-08T11:43Z` — **pages that had already cleared
> the Extract Gate and been paid for**. Every weighted number computed over this stratum
> estimates that population, i.e. today's extract bill. It says nothing about pages the
> gate already rejects: they never reach the tap.

That is the whole reason to read `extract-call-rate` as *the share of today's calls a
gate config would still make* rather than as "how often the gate fires", and `recall` as
*the share of today's real postings it would keep*. False-drops on pages the gate
already rejects are invisible here and always will be — that is the Boundary Stratum's
(#263) and the Shadow Extraction's (#259) job.

### The frame

| quantity | value |
|---|---|
| capture lines | 11,772 |
| frame cutoff (`-since`) | `2026-08-07T21:13:00Z` |
| in-frame window | `2026-08-07T21:13:23Z` … `2026-08-08T11:43:46Z` |
| in-frame distinct URLs (latest `ts` wins) | 5,204 = 1,801 accept / 3,403 abstain |
| duplicate records collapsed (whole file) | 2,942 |
| oversized lines dropped (> 512 KB raw) | 4 |
| already committed by the #254 drawing | 42 (39 accept / 3 abstain) — excluded |
| **candidate frame** | **5,162 = 1,762 accept / 3,400 abstain** |

**Why the cutoff.** Two parser changes landed *mid-capture*: `ec1e7bd fix(parser): strip
site chrome from MainContent` (08-06 20:31Z) and `fceaf87 fix(parser): only strip site
chrome on the body fallback` (08-07 18:59Z). ADR-0043's premise is that a captured page
is the exact bytes the gate will later see, so content produced by a parser that no
longer exists is a *different page*. The cutoff is the first record after `fceaf87`.
The 5,669 earlier records are not sampled; their windows also carry census accept rates
of 32% and 60% against 2.5–11% in the faithful frame.

**The exclusion.** The 42 URLs the #254 drawing already holds are dropped rather than
redrawn: a duplicate URL fails the substrate's well-formedness guard, and it would give
one page two incompatible weights from two different drawings. This carries a mild,
stated bias — the pages a walk re-visits most often are the likeliest to have been drawn
twice — which is recorded rather than corrected for.

**A note on #261's counts.** #261 records distinct in-frame counts of 1,811 accept /
3,437 abstain. Those sum to 5,248 rather than 5,204 because 43 URLs appear there under
*both* verdicts. The table above resolves each URL to its latest record — the rule
`scanCapture` already implements — and is internally consistent.

### The sampling design and the weights

The capture's per-verdict caps (`EXTRACT_CAPTURE_MAX=500` in
`docker-compose.override.yml`, counters living in process memory and resetting on every
restart, so the frame is six process lifetimes) are the only structure the harvest
imposed. So **the random draw's only sampling cell is the verdict.** Stratifying it
structurally, as the #254 drawing does, would destroy the honest composition this
stratum exists to produce.

`p = 0.0753` is the true stream accept rate — #261's **census measurement**, not an
estimate: up to the moment a frame's abstain cap binds, the tap writes *every* extractor
decision, and pooling the six frames' pre-cap prefixes gives 285 accepts in 3,785
decisions. It is never the file's own 0.4265 accept mix, which overstates accepts 5.7×.

| cell | population | sampled | fraction | weight |
|---|---:|---:|---:|---:|
| `random` / accept | 1,762 | 40 | 2.27% | 0.22590 |
| `random` / abstain | 3,400 | 80 | 2.35% | 1.38705 |
| **total** | **5,162** | **120** | | Σ = 120.0000 |

The two sampling fractions agree to within a percentage point, so the draw is
effectively a simple random sample of the frame and the weights carry the cap
correction and nothing else. With a 1:2 draw they collapse to `3p` and `1.5(1−p)`;
the weighted accept share reconstructs `p` to 5e-4, which a test asserts. **Kish
effective n = 120² / Σw² ≈ 92.3.**

`-accept-share` is **required** and never reconstructed. `liveAcceptShare` splits
capture sessions on a one-hour `ts` gap, and three of this capture's six process frames
are contiguous within that (`08:38→09:18`, `09:18→10:04`, `10:07→10:51`), so the
estimator merges them and returns **0.3240** — wrong, and wrong silently. The sampler
prints that number beside the supplied one, labelled a cross-check that does not govern.

### Two stated assumptions

1. The frame is **distinct URLs**; `p` is per **decision**. Weighting URLs to decision
   shares assumes that, within a verdict cell, how often a page is re-decided is
   independent of its label. Cap truncation makes the file's own duplication counts
   unusable as a correction, so this is an assumption, not a fix.
2. The final frame (`08-08 10:52→11:43Z`) ran with the Free Extraction (#252) live, and
   the tap fires on that path too, so some accepts there carry **no model call**. Label
   semantics are unaffected; any figure derived from accept counts is a *decision* count,
   not a model-call count.

### What it says

```
composition detail     0.0584  (n=31)
composition hub-index  0.0710  (n=12)
composition residue    0.8707  (n=77)
```

**94% of what the crawler pays an extract call on is not a single posting.** That is the
honest, weighted version of the spec's "43% of saved listings are not single postings":
the raw file mix is 26% `detail`, and weighting it back to the stream cuts that to 5.8%.

Two one-liners over the committed review surface say the same thing without any code —
within a verdict cell every weight is identical, so the raw share *is* the stream
estimate:

```bash
# precision of today's extractor ACCEPTS: 31/40 = 0.775
awk -F'\t' '$3=="random" && $4=="true"  {n++; if($5=="detail") d++} END{printf "%d/%d = %.3f\n", d,n,d/n}' \
    cmd/llmbench/extract-goldset/labels.tsv
# real postings among today's ABSTAINS: 0/80 = 0.000
awk -F'\t' '$3=="random" && $4=="false" {n++; if($5=="detail") d++} END{printf "%d/%d = %.3f\n", d,n,d/n}' \
    cmd/llmbench/extract-goldset/labels.tsv
```

So the live extractor's own accept verdict is right 77.5% of the time on the calls it is
paid for, and in this sample its abstains cost nothing: **not one of the 80 abstain rows
is a real posting.** The gate's cost, not the model's judgment, is what the 94% is
about.

### Label distribution

| | `detail` | `hub-index` | `residue` |
|---|---:|---:|---:|
| accept (40 rows) | 31 | 7 | 2 |
| abstain (80 rows) | 0 | 5 | 75 |

Three rows carry an `uncertain - …` note: a ministry internship invitation with no named
role, and two careers-site navigation pages (a board `locations` facet and a
"working at <site>" page) that route to openings but list none.

### How it was labelled — batched, and the next stratum should copy this

A worksheet row is ~2 KB, so 120 of them is ~240 KB ≈ 55k tokens: more than a labeling
agent can hold while also holding the rubric and its own output. Never read the
worksheet whole, and never read the substrate or the capture.

```bash
go run ./cmd/llmbench goldset-worksheet -stratum random -out /tmp/262/ws.jsonl   # 120 lines
cd /tmp/262 && split -l 15 ws.jsonl b-                                          # b-aa … b-ah
```

Then, **per batch**: read `b-XX`, decide its 15 labels, write `p-XX.tsv`
(`id<TAB>label<TAB>note`), and move on without re-opening it or carrying it forward.
Finally `cat p-*.tsv > proposals.tsv` (exactly 120 lines) and `goldset-apply`. Each batch
costs ~8k tokens and the results accumulate **on disk**, not in context.

### Second-pass consistency check — and what it is not

Twenty rows were re-rendered under a different presentation seed and relabelled from
scratch in a fresh batch:

```bash
go run ./cmd/llmbench goldset-worksheet -stratum random -n 20 -seed extract-goldset-audit-v1 -out /tmp/262/audit.jsonl
```

**Agreement: 20/20.** Those labels were *not* written into the substrate.

Name it for what it is: **a same-model re-read, so its agreement rate is an upper bound
on label reliability, not an independent audit.** It catches slips — a batch drifting
from the rubric, a row read carelessly — and it establishes nothing about correctness. A
model that is confidently wrong the first time is confidently wrong the second. The
human spot-check below is the only thing that measures correctness.

## The labeling protocol

`llmbench goldset-worksheet` renders the labeler's view: the page's own title, two
windows into its main content, and its outbound-link counts (`urls_total`,
`urls_same_host`, `urls_joblike`). It **withholds the stratum, every JSON-LD-derived
field, and the live extractor's verdict**, and shuffles the row order. If a labeler could
see that a page publishes a lone `JobPosting`, the labels would agree with the structured
data by construction — the exact circularity ADR-0043 exists to end. A test asserts the
worksheet leaks none of them.

The verdict was withheld from #262 onward, for the same reason (`worksheetRow` no longer
carries it). The random stratum's headline number is the *precision of that verdict*, so
a labeler who could see it would agree with it by construction. `labels.tsv` still shows
the verdict: that is the **review** surface, read after the label already exists.

**Known contamination, stated rather than hidden:** the #254 (structural) labels were
proposed with the verdict visible. It affects that drawing only, and the two drawings are
never pooled, so no number in the random stratum rests on it. Re-proposing 149 labels to
remove it would throw away a human confirmation pass; the honest move is to record it.

Rubric, judging what the *page* is rather than what the URL promises:

- **`detail`** — the page *is* one job posting: one role's responsibilities/requirements
  and an apply action. A "similar jobs" rail at the bottom does not change this.
- **`hub-index`** — the page lists openings: a board root, search results, a department
  listing. A page listing exactly one opening is still `hub-index`.
- **`residue`** — neither: culture/about/benefits landing pages, blogs, press, contact
  and privacy pages, cookie or login walls, JS shells with no parsed content, 404s, and
  "this position has been filled" pages with no role body.

Rows the labeler could not decide carry `uncertain - …` in their note; there are 6 in the
structural drawing and 3 in the random one.

| | `detail` | `hub-index` | `residue` |
|---|---:|---:|---:|
| `lone-posting` | 51 | 0 | 19 |
| `ambiguous-posting` | 0 | 0 | 1 |
| `no-posting` | 24 | 7 | 47 |
| `random` | 31 | 12 | 77 |

## Scorecard (today's gate, `DefaultLLMGateConfig`)

`go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl`
— no network, no model. **Exits 1** on the false-drops below.

```
total             269
extract-calls     265
extract-call-rate 0.9851  (soft, no threshold)
overall           precision 0.3811  recall 0.9806  f1 0.5489  accuracy 0.3829
detail     recall 0.9806  (n=103, extracted 101, skipped 2)
hub-index  accuracy 0.1053  (n=19,  skipped 2, leaked 17)
residue    accuracy 0.0000  (n=147, skipped 0, leaked 147)
residue-count 147, residue-extracted 147

stream-weighted estimates (random stratum, n=120, effective n=92.3)
composition detail 0.0584 / hub-index 0.0710 / residue 0.8707
extract-call-rate 0.9944   precision 0.0568   recall 0.9677
```

**Read the drop side of this scorecard with suspicion.** The capture tap sits
*downstream* of the Extract Gate (`job_listing_processor` calls `ShouldExtract` before
the extractor runs), so all but a handful of rows are pages the gate already admitted.
This file measures the gate's **leak** side honestly and its **drop** side essentially
not at all — an extract-call rate of 0.99 is the sampling frame showing through, not a
measurement. ADR-0044's Boundary Stratum and the Shadow Extraction are what close that
gap; the random stratum makes the *cost* side honest, not the drop side.

### The same scorecard with the Positive Evidence rung on

```bash
echo '{"RequirePositiveEvidence": true}' > /tmp/positive.json
go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl -gate-config /tmp/positive.json
```

```
extract-call-rate 0.5576  (raw, over both drawings — the file's mix, not the stream's)
detail     recall 0.9709  (n=103, extracted 100, skipped 3)
residue    accuracy 0.7415 (n=147, skipped 109, leaked 38)

stream-weighted estimates (random stratum, n=120, effective n=92.3)
extract-call-rate 0.1274   precision 0.4431   recall 0.9677
```

The stream-weighted line is the number to quote: **the rung would cut today's extract
calls to 12.7% of what they are — a 7.8× reduction — raise the precision of a paid call
from 5.7% to 44.3%, and leave recall over today's real postings unmoved at 96.8%.** The
raw 0.5576 above it is the *file's* composition, which oversamples the mechanism's own
stratum; quoting it would understate the saving by more than 4×. That is precisely the
confusion this stratum exists to end.

Both figures are descriptive. They measure the calls the crawler already makes and say
nothing about what the rung would drop *before* the tap — #263 and #259 measure that.

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
| stale posting — withdrawn or filled, `JobPosting` JSON-LD still served | 16 | `career.avenga.com`, `career.happysocks.com` ×3, `careerpoland.autoliv.com`, `careers.atlantishealth.com`, `careers.beyond-vision.com` ×2, `careers.patria.com`, `careers.pit.com`, `careers.spacelift.io`, `careers.sundayapp.com`, `careers.tekever.com`, `careers.worldpackers.com` ×2, `jobs.pwc.de` |
| evergreen non-role — talent pool / general application / enquiry page | 3 | `careers.coverflex.com`, `karriere.mri.bund.de`, `www.fluidstack.io` |

The 16 stale postings are a **liveness** problem (ADR-0035's territory): the fields are read
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
Fifteen of them are **expired or withdrawn postings** ("this position is no longer active",
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
16 stale postings and 3 evergreen non-roles. See "The Free Extraction fidelity check" above.

**3. 24 of the 75 `detail` pages publish no structured data at all.** A structured-data
path can never reach them; whatever admits and extracts those pages stays the cost
driver.

## Regenerating and maintaining the set

```bash
# 1. resample the STRUCTURAL drawing
#    (destructive: overwrites both files, dropping all labels — never run it casually)
go run ./cmd/llmbench goldset-sample -capture <repo>/capture/extract-capture.jsonl

# 1b. draw the RANDOM stratum and APPEND it (existing rows and labels pass through
#     untouched; -since and -accept-share are required and never reconstructed)
go run ./cmd/llmbench goldset-sample-random \
    -capture <repo>/capture/extract-capture.jsonl \
    -since 2026-08-07T21:13:00Z -accept-share 0.0753

# 2. the labeling view (a working artifact, never committed)
#    -stratum / -n cut a deterministic subset: the whole random stratum, or 20 rows of it
go run ./cmd/llmbench goldset-worksheet -out /tmp/worksheet.jsonl
go run ./cmd/llmbench goldset-worksheet -stratum random -n 20 -out /tmp/spotcheck.jsonl

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
likely to be wrong. **69 of the 70 `lone-posting` rows are confirmed**; the one that is
not is the open-application row described under "Open question" below.

```bash
# the whole review surface is 70 lines, not the 3.9 MB substrate
awk -F'\t' '$3=="lone-posting"' cmd/llmbench/extract-goldset/labels.tsv | column -t -s$'\t' | less -S
#   read the 24 rows with verdict=false first: that is finding 2 above.

# fix any wrong label in the `label` column, then:
go run ./cmd/llmbench goldset-apply -confirmed-by "<your name>" -confirm-stratum lone-posting

# then set pendingHumanConfirmations to 0 in cmd/llmbench/goldset_test.go and commit.
```

### The random stratum is SPOT-CHECKED, not confirmed (#262)

ADR-0043 asks less of this stratum, and for a reason: it is 120 rows of ordinary stream
traffic, no later guard is decided on any single one of them, and its numbers are
weighted estimates rather than pass/fail conditions. What it needs is evidence that the
labeling process is *sound*, which a sample answers. **`randomSpotChecks` in
`../goldset_test.go` is 0: no random row has been read by a human yet.** It is a ratchet
that RISES, and it is asserted in both directions, so a confirmation that lands without
the constant moving fails the build just as one that vanishes does.

```bash
# the spot-check surface: 20 rows in deterministic order, not the 3.9 MB substrate
go run ./cmd/llmbench goldset-worksheet -stratum random -n 20 -out /tmp/spotcheck.jsonl

# read them against the rubric above, correct any wrong label in the `label` column of
# labels.tsv, put your name in the `confirmed_by` column of EXACTLY the rows you read:
go run ./cmd/llmbench goldset-apply

# then raise randomSpotChecks in cmd/llmbench/goldset_test.go to the number you confirmed.
```

Read the 40 `verdict=true` rows first if you only have time for some: 31 of them are the
`detail` rows the whole composition estimate rests on, and a wrong one there moves every
weighted number.

**Do not use `-confirm-stratum random`.** It would stamp all 120 rows at once from one
command — a full confirmation of 120 pages nobody read, which is exactly the thing the
provenance record exists to prevent. A spot check is 20 rows a human actually opened.

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

## Open question: one row held back from confirmation

The confirmation pass held four rows back, because confirming them would attest to
something the pass found reason to doubt. **Three have since been resolved**: they carried
a closure banner above an otherwise complete posting, they were relabelled `residue`, and
`crawler.WithdrawalNotice` was narrowed to refuse them by that banner, so they no longer
fire. One remains, and it is why `pendingHumanConfirmations` and
`pendingExpectedConfirmations` are 1 rather than 0.

The three that were resolved, kept here because they are the shape a future signal must
keep catching:

| row | body opens with |
| --- | --- |
| `jobs.drivetlv.com/companies/uveye/jobs/85776469-…` | "This job is no longer accepting applications" |
| `talent.seedcamp.com/companies/source-dev/jobs/87201415-…` | "This job is no longer accepting applications" |
| `www.builtincolorado.com/job/product-training-specialist-us/9511859` | "Sorry, this job was removed at 04:14 p.m. …" |

These are the same failure as the sixteen withdrawn postings ADR-0042's narrowing removed, except
that **the length-ratio test cannot reach them by design**: the page really does render its
posting, so the declared/rendered ratio stays under the bound. Only the banner marks them, and a
banner is template text in whatever language the ATS ships. The banner test is what now
refuses them, and it is the second signal the length ratio could never be.

The fourth row is still open, and it is a labelling inconsistency rather than a mechanism
failure:

| row | labelled | but compare |
| --- | --- | --- |
| `career.paradoxplaza.com/jobs/7673868-open-application-for-game-programmers` | `detail` | `careers.coverflex.com/jobs/7117813-spontaneous-application-…`, labelled `residue` |

Both are open applications against a role family with no specific opening. Whichever answer
is right, the set should give the same one twice.

Resolving it is a decision about what the Corpus should contain, not a stamp. Until it is
made, the row stays unconfirmed and visible.
