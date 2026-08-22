# The Extract Gold Set

Real pages the **live extract stage actually decided on**, stored as the parsed
`crawler.Content` the pipeline itself produced (ADR-0043, #254). Each row carries the
page URL, the extractor's original verdict, a label
(`detail` / `hub-index` / `residue`, or `ambiguous` for a page a review could not
settle), its sampling stratum, its sampling weight, and its label provenance.

The file holds **four drawings**, one of which is defined and not yet drawn. They share a row
format and a file; they share nothing else, and their weights are never pooled:

| drawing | strata | rows | drawn from | accept share | what it is for |
|---|---|---:|---|---:|---|
| **structural** (#254) | `lone-posting`, `ambiguous-posting`, `no-posting` | 149 | the July capture, 4271 deduped pages | 0.3432 | the Free Extraction's own population, sampled where the mechanism lives |
| **random** (#262) | `random` | 120 | the August faithful frame, 5162 deduped pages | 0.0753 | a random sample of the stream, so composition, precision and cost describe production |
| **boundary** (#263) | `boundary` | 188 | the same frame, 5042 candidates | n/a (a census) | the pages the two gate rules **disagree** on — where a false drop hides |
| **veto-boundary** (#304) | `veto-boundary` | 0 | — (drawn during the rollout) | n/a (a census) | the pages the **Learned Veto** would withhold the call from; see *Turning the Learned Veto on* in the repository README |

This replaces `../extract-testdata` as the Extract Gate's **evidence base**. Those
fixtures are synthetic — invented domains, every `detail` page carrying exactly one
structured posting node and every `hub-index`/`residue` page carrying none — so any
mechanism that reads structured data scores perfectly on them by construction. They
survive as cheap regression cases for the reject rungs and nothing more.

## Files

| File | What it is |
|---|---|
| `goldset.jsonl` | The substrate. One `goldRow` per line, sorted by URL, ~6.3 MB across all three drawings. **Generated — never hand-edited.** |
| `labels.tsv` | The review surface rendered from the substrate: one line per row, 457 lines. This is the file a reviewer reads in a diff and edits to correct a label. A test asserts the two never drift. |
| `expected.tsv` | The second review surface (#256): one line per row the Free Extraction fires on — its expected title, location and working mode, plus any accepted fire. 51 lines. Also rendered from the substrate, also drift-tested. |

All three files are written **atomically**: each is staged in a temporary file beside it
and renamed over it, and the whole group is staged before any of it is renamed, so a
crash or a full disk mid-write leaves the previous versions intact rather than a
truncated 6.3 MB substrate (#283). A `.goldset.jsonl.tmp-…` left in this directory is the
debris of a process killed mid-write — it is gitignored, it is never the record, and
deleting it is safe.

`goldRow` is the extract-capture record (`internal/extractcapture`: `url`, `verdict`,
`ts`, `renderer`, `content`) **extended in place**, not a new format. All 457 committed
rows predate the renderer stamp (#281) and therefore carry no `renderer` at all: they
were drawn from a capture written before the field existed, so their renderer is
*unknown* rather than empty (ADR-0046, ADR-0047). An unlabeled capture file and
a labelled gold-set row therefore read through the same decoder, `score-capture` scores
this file unchanged, and a later spec can add a random stratum or the `expected`
field-fidelity block with no migration.

### The Proposed Label

`label_provenance.proposed_label` is the label the row's proposer actually proposed
(ADR-0048, #284). It is written once, beside `proposed_by`, and **survives a human
override**, so a row can say "the model proposed `residue`, a human made it `detail` and
signed for it". That is what makes a confirmation pass measurable: `goldset-apply` prints
the agreement rate between human confirmers and proposers at the end of every run, and
that rate is the evidence for what the still-unconfirmed rows are worth.

It is additive and omitted when absent, so a row that never carried one re-serializes
unchanged. Backfill filled it on the **379 rows carrying no confirmer**, where it is
truthful because nobody had overridden the proposer's label; the **78 rows a human had
already confirmed carry none**, because the set does not record which of those were
relabelled during the pass that confirmed them. It is deliberately **not** a column on
`labels.tsv`: it records what a proposer said, and is not a field a reviewer edits.

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
  withheld* — see "The labeling protocol" below. **194 of the 457 rows now carry a human
  confirmer** (69 `lone-posting`, 5 `no-posting`, 120 `random`); the other 263 carry a
  **Proposed Label** and no confirmer, and the four confirmation counts in
  `../goldset_test.go` are the machine-visible measure of that gap.
- **The random stratum is fully confirmed.** Its 116 outstanding rows were read one at a
  time through `llmbench goldset-ui` (#274) under **Blind Confirmation**: the labeller
  saw the page and answered before the **Proposed Label** was revealed. The human and the
  proposer agreed on **109 of 116 rows (94.0%)**; the 7 disagreements each carry a note
  saying why, and each preserves what the proposer actually proposed. That agreement rate
  is the direct measurement of what the *remaining* unconfirmed rows are worth — it is
  evidence about the proposer, not a licence to skip reading them (ADR-0048).

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

## The Boundary Stratum (#263)

### What population it describes

> The population is **the 188 pages in the faithful frame where today's blanket accept and
> the tiered Positive Evidence rule DISAGREE, and the live extractor accepted**. It is a
> **census** of that disagreement's accept half — every one of them, not a sample of them.
> A census of a disagreement set describes **no stream**. Nothing here is weighted, nothing
> here estimates production, and no figure here may be pooled with the random stratum's.

Why this stratum exists at all: the live stream is ~94% non-postings, so the random
drawing's 120 rows hold only a handful of pages anywhere near the decision boundary. A
stream-random draw simply cannot detect a small false-drop rate. This drawing goes to where
the drop can happen and takes **all of it**.

Consequences of it being a census, which are also its whole design:

- Inclusion probability is 1, so **every weight is exactly `1.0`** — no accept-share
  correction, no cap correction, nothing to get wrong. The per-drawing weight balance
  holds by construction.
- **No seed and no sampling error** on the number that decides the guard.
- It is byte-reproducible from (capture, `-since`, the URLs the earlier drawings already
  committed, the two gate configs).

### How it was computed — and what invalidates it

```bash
go run ./cmd/llmbench goldset-sample-boundary \
    -capture <repo>/capture/extract-capture.jsonl \
    -since 2026-08-07T21:13:00Z
```

The verb takes **no `-gate-config`, no `-seed` and no quota flags**. The two configs whose
disagreement *defines* the stratum are functions in `cmd/llmbench/goldsetboundary.go`:

| function | what it is |
|---|---|
| `boundaryBaselineConfig()` | the pre-ADR-0044 blanket accept — `RequirePositiveEvidence` set **false explicitly**, so it kept meaning "the previous behaviour" after #264 flipped the default on |
| `boundaryCandidateConfig()` | the tiered Positive Evidence rule of ADR-0044, implemented in `internal/pagegate/positive_evidence.go`, as the gate ships it **today** |

**A moved *reject* rung invalidates this stratum**, because the pages it holds would no longer
be pages today's blanket accept extracts. `TestCommittedBoundaryStratumIsTheDisagreementSet`
re-derives that half over all 188 committed rows on every build and says so when it goes red.
Re-draw from the capture; do not patch the file.

**A widened Positive Evidence rung does not.** The stratum was drawn against #258's rule, and
#257 widened that rule to the false-drops this very stratum measured — 28 of these rows now
extract. It was deliberately **kept, not re-drawn**, and *not* because a re-draw is expensive:
re-running `goldset-sample-boundary` over the same capture and cutoff against the committed
set reports *no accepted page the two configs disagree on*, so a re-draw returns exactly the
160 rows still here, labels and provenance intact, and owes **zero** fresh human
confirmations — measured, not assumed. What re-drawing discards is the evidence: the 26
recovered postings, the only durable proof the widening bought recall (ADR-0043, Consequences;
ADR-0044, "The Boundary Stratum was NOT re-drawn"). What the stratum measures now is that
recovery, and `TestCommittedBoundaryRecoveryLedger` pins it **per label** — total, detail,
non-posting, ambiguous — so a later widening that recovers non-postings faster than postings
goes red rather than showing up as a bigger total. Re-draw when there is a genuinely new
candidate rule to disagree with, and budget confirmations for the rows that draw *adds*.

### The arithmetic

| step | count |
|---|---:|
| capture lines | 11,772 |
| dropped oversized (> 512 KB raw) | 4 |
| distinct URLs (latest `ts` wins) | 8,826 |
| in the faithful frame (`ts >= 2026-08-07T21:13:00Z`) | 5,204 |
| minus the 162 URLs the two earlier drawings hold | **5,042** (1,722 accept / 3,320 abstain) |

Replaying both configs over those 5,042 pages:

| config | extract calls | of the 1,722 accepts | of the 3,320 abstains |
|---|---:|---:|---:|
| baseline (`RequirePositiveEvidence: false`) | 4,963 | 1,680 | 3,283 |
| candidate (`RequirePositiveEvidence: true`) | 1,904 | 1,492 | 412 |

The disagreement:

| cell | population | drawn |
|---|---:|---|
| boundary ∩ live verdict **accept** | **188** | **all 188 — the census** |
| boundary ∩ live verdict **abstain** | 2,871 | none — recorded, not drawn |
| **total boundary** | **3,059** | |
| reverse direction (candidate extracts what the baseline skips) | **0** | asserted by the verb, never assumed |

That last row matters: the rung is supposed to be purely **additive**, and
`goldset-sample-boundary` refuses to write anything if it is not, because a rule that had
stopped being additive would make this one-sided definition silently wrong.

### Why the abstain half is not drawn — a stated limitation

2,871 pages sit in the abstain half and none of them are here. The human confirmation
budget is the binding constraint, and per row the accept cell is where a false drop
actually hides:

- The random stratum measured **31 of 40 accept rows as `detail` against 0 of 80 abstain
  rows.**
- A page in the abstain cell produces no Job Listing today either way, so dropping it is
  not a regression in output. A page in the accept cell is one the crawler pays for *and*
  saves from.
- The abstain half is not unmeasured: **Shadow Extraction (#259) samples exactly the
  rejected population live**, and the 0/80 result bounds its `detail` rate.

If Shadow Extraction ever shows real postings among the rejects, drawing the abstain half
is the follow-up.

### Stated contamination

Every row in this stratum shares one live verdict (accept) and one gate outcome (extracted
today, skipped by the candidate rule). A labeller who knows the design knows both. That is
precisely why ADR-0043 requires a **human** on this stratum, and why
`goldset-confirm-sheet` withholds the verdict, the structured data and the gate's marks
again — a test asserts it does.

### Label distribution

| | `detail` | `hub-index` | `residue` | `ambiguous` |
|---|---:|---:|---:|---:|
| all 188 rows (live verdict accept) | 37 | 62 | 79 | 10 |
| at the draw, before the blind re-read | 47 | 57 | 74 | 10 |
| human-confirmed | 0 | 0 | 0 | 0 |

**One host, `nl.gigroup.com`, contributes 15 of the 188 rows (8%)**, all the same
location-facet template and all labelled `hub-index`. A single site's template can move
this stratum's label mix; read the mix with that in mind.

The ten `ambiguous` rows are recorded rather than forced into a class, and are excluded
from every confusion count, so an undecidable page becomes neither a false drop nor a page
the gate wrongly extracted. Three recurring shapes account for all ten:

| shape | rows | the tension |
|---|---:|---|
| open application against a role family (three Cirque du Soleil casting disciplines, a trainer application) | 4 | requirements and an apply action, but no single opening — and the set already answers this shape both ways (see "Open question" below) |
| an aggregator or list page whose title names one role but whose parsed body carries a different posting's teaser | 3 | the URL and title say one Job Listing; the captured content shows none of it |
| a non-careers page (an institute news page, a facility page) carrying one full posting inline | 2 | the page is not a Job Listing, but it is the only place that posting exists |
| a posting-shaped volunteering opportunity | 1 | tasks, requirements and an apply action, but it is not employment |

### What it says

At the draw, every one of the 188 rows was skipped by the candidate rule by construction,
so the false-drop count was exactly the `detail` rows: **47 of the 3,059 boundary pages
the rule dropped were real Job Listings** — 25% of the accept half. That is the finding
this stratum exists to produce, and no label was softened to keep the number down.

It has moved twice since, both times because the finding was acted on. A blind re-read of
the 57 consequential rows settled 10 of them out of `detail` (47 → 37), and #257 then
widened the rung to the shapes the remaining drops named, recovering 28 rows:

```
boundary stratum (n=188, unweighted)
  count detail 37 / hub-index 62 / residue 79 / ambiguous 10
  extracted 28, skipped 160
  false-drops       11   (real Job Listings the Positive Evidence rule drops here)
  ambiguous-skipped 10   (dropped, unclassifiable: not a false-drop, not forgiven)
  confirmed         0 of 188
```

The label census (`count detail 37`) and the drop count (11) are now different numbers,
and `TestCommittedBoundaryRecoveryLedger` pins the 28 per label so the split stays
visible. **11 is what #264 has to argue with**; ADR-0044 records the eleven shapes and why
each is argued rather than closed by a threshold nothing measured.

> **The false-drop count above rests on LLM-proposed labels.** Until
> `pendingBoundaryConfirmations` is 0 it is one model's opinion of another model's
> opinion, and no guard may act on it.
>
> **This paragraph also said "#264 must not flip the rung on while that constant is
> non-zero", and #264 flipped it anyway. That is a deviation, recorded here rather
> than deleted.** The reasoning: the number that justifies the flip does not depend on
> these labels. The stream extract-call rate (0.9944 → 0.1390) is a share of pages,
> computed from sampling weights and the gate's own decision, with no label on either
> side of the ratio; stream recall is label-dependent but *unmoved* at 0.9677, so it
> cancels. What these unconfirmed labels bound is the **cost** side — the 11 — and
> holding a measured sevenfold saving hostage to unconfirmed evidence *against* it,
> while the evidence *for* it needs no labels at all, is the wrong way round. Two
> things changed to make that defensible: the guard is a ledger that names each of the
> 11 and re-opens each entry the moment a human confirms its label (see *The guard*
> below), and `EXTRACT_REQUIRE_POSITIVE_EVIDENCE=false` reverts the rung with no
> deploy — so a wrong rule is reversible, and the pages it dropped in the interim are
> re-reached and re-extracted on the next Collection Cycle. The permanence argument
> for a false-drop applies to a rule left wrong, not to one that can be pulled.
>
> **The confirmations are still owed**, and they are now the thing that arms the guard
> fully. Nothing above is retracted except the prohibition itself.

### How it was labelled — batched, exactly as #262 was

188 worksheet rows is ~55k tokens, far more than a labelling agent can hold alongside the
rubric and its own output. Never read the worksheet whole, and never read the substrate or
the capture.

```bash
go run ./cmd/llmbench goldset-worksheet -stratum boundary -out /tmp/263/ws.jsonl  # 188 lines
cd /tmp/263 && split -l 16 ws.jsonl b-                                            # b-aa … b-al
```

Then, **per batch**: read `b-XX`, decide its 16 labels against the rubric below, write
`p-XX.tsv` (`id<TAB>label<TAB>note`), and move on without re-opening it or carrying it
forward. Finally `cat p-*.tsv > proposals.tsv` (exactly 188 lines, 188 distinct ids) and
`goldset-apply -proposals … -proposed-by "llm:<model id> (…)"`. Twelve batches at ~9k
tokens each; the results accumulate **on disk**, not in context.

### Second-pass consistency check — and what it is not

Twenty rows were re-rendered under a different presentation seed, in a shorter text
window, and relabelled from scratch:

```bash
go run ./cmd/llmbench goldset-worksheet -stratum boundary -n 20 -seed second-pass-263 -out /tmp/263/pass2.jsonl
```

**Agreement: 19/20.** Those labels were *not* written into the substrate.

The single disagreement is itself the finding: `jura.uni-halle.de/einrichtungen/computerpool`
read as `ambiguous` on the first pass (a facility page carrying one complete student-assistant
posting further down) and as `residue` on the second, where the shorter window never reached
the posting. **The label depends on how much of the page the reader sees** — which is one
more reason the confirmation sheet shows a wider window than the labelling worksheet.

Name the check for what it is: **a same-model re-read, so its agreement rate is an upper
bound on label reliability, not an independent audit.** The human confirmation is the only
thing that measures correctness.

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

Rows the labeler could not decide carry `uncertain - …` in their note; there are 5 in the
structural drawing and 3 in the random one.

| | `detail` | `hub-index` | `residue` |
|---|---:|---:|---:|
| `lone-posting` | 48 | 0 | 22 |
| `ambiguous-posting` | 0 | 0 | 1 |
| `no-posting` | 24 | 7 | 47 |
| `random` | 31 | 12 | 77 |
| `boundary` | 37 | 62 | 79 |

(`boundary` also carries 10 `ambiguous` rows, the only stratum that does.) Three
`lone-posting` rows moved `detail` → `residue` at the #252 review — the closure-banner pages
under "Open question" below.

## Scorecard (today's gate, `DefaultLLMGateConfig`)

Since #264 the Positive Evidence rung ships **on**, so this is the rung's scorecard.

`go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl`
— no network, no model. **Exits 1** on the false-drops below.

```
total             457
extract-calls     180
extract-call-rate 0.3939  (raw, over all three drawings — the file's mix, not the stream's)
overall           precision 0.7056  recall 0.9071  f1 0.7938  accuracy 0.8523
detail     recall 0.9071  (n=140, extracted 127, skipped 13)
hub-index  accuracy 0.8272 (n=81, skipped 67, leaked 14)
residue    accuracy 0.8274 (n=226, skipped 187, leaked 39)
residue-count 226, residue-extracted 39
ambiguous         10 (excluded from scoring entirely, 0 extracted)

stream-weighted estimates (random stratum, n=120, effective n=92.3)
composition detail 0.0584 / hub-index 0.0710 / residue 0.8707
extract-call-rate 0.1390   precision 0.4063   recall 0.9677
projected spend   0.1390x today's extract bill

boundary stratum (n=188, unweighted)
count detail 37 / hub-index 62 / residue 79 / ambiguous 10
extracted 28, skipped 160
false-drops 11, ambiguous-skipped 10, confirmed 0 of 188
```

The stream-weighted line is the number to quote for **cost**: the rung cuts today's
extract calls to 13.9% of what they were — a 7.2× reduction — raises the precision of a
paid call from 5.7% to 40.6%, and leaves recall over today's real postings unmoved at
96.8%. The raw 0.3939 above it is the *file's* composition, which also carries a census
of the boundary; quoting it would misstate the saving badly. That is precisely the
confusion the random stratum exists to end.

The boundary block is the number to quote for **loss**: **11 real Job Listings still
dropped**, plus 10 pages nobody could classify. It reads as a **recovery ledger** now
rather than as a live disagreement — the `extracted 28` are rows this stratum measured
#258's rule dropping and #257's widening got back (26 `detail`, 2 `hub-index`). The
`detail recall 0.9071` on the raw line is 13 drops seen through the file's own composition
— 11 are the boundary and 2 come from the earlier drawings — so it is not a stream estimate
either.

The cost figures are descriptive estimates of the calls the crawler already makes. The
boundary figure is a census of a disagreement and describes no stream at all. Neither may
be pooled with the other. **The 11 rests on LLM-proposed labels**, which is why the guard
below tolerates them by name rather than asserting an absolute zero, and why #259's
Shadow Extraction is the live cross-check on pages the gate rejects before the tap. The
eleven shapes behind it, and why each is argued rather than closed by a threshold, are
written up in ADR-0044.

### The same scorecard with the rung off (the pre-ADR-0044 blanket accept)

This is what `EXTRACT_REQUIRE_POSITIVE_EVIDENCE=false` restores in production.

```bash
echo '{"RequirePositiveEvidence": false}' > /tmp/blanket.json
go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl -gate-config /tmp/blanket.json
```

```
extract-call-rate 0.9912  (soft, no threshold)
overall           precision 0.3115  recall 0.9857  f1 0.4734  accuracy 0.3132
detail     recall 0.9857  (n=140, extracted 138, skipped 2)
hub-index  accuracy 0.0247  (n=81, skipped 2, leaked 79)
residue    accuracy 0.0000  (n=226, skipped 0, leaked 226)
ambiguous         10 (excluded from scoring entirely, 10 extracted)

stream-weighted estimates (random stratum, n=120, effective n=92.3)
extract-call-rate 0.9944   precision 0.0568   recall 0.9677

boundary stratum (n=188, unweighted)
extracted 188, skipped 0, false-drops 0, confirmed 0 of 188
```

The boundary block reads as all zeroes here **by construction**: every row in that
stratum is a page the blanket accept extracts, which is half of what put it there. Its
information appears only under the rung.

**Read the drop side of this scorecard with suspicion.** The capture tap sits
*downstream* of the Extract Gate (`job_listing_processor` calls `ShouldExtract` before
the extractor runs), so all but a handful of rows are pages the gate already admitted.
This file measures the gate's **leak** side honestly and its **drop** side essentially
not at all — an extract-call rate of 0.99 is the sampling frame showing through, not a
measurement. ADR-0044's Boundary Stratum and the Shadow Extraction are what close that
gap; the random stratum makes the *cost* side honest, not the drop side.

Note that the blanket accept **also drops two `detail` rows** (`hiring.cafe/job/…` and
`jobs.blooloop.com/jobs/…`) and therefore also exits 1. A hard zero has never been green
on this file, with the rung or without it: those two are reject-rung drops that predate
ADR-0044 entirely. See the guard below.

### The same scorecard with the Learned Veto on (ADR-0049)

This is what `EXTRACT_LEARNED_VETO=true` would run in production. The rung ships **off**,
so this section is the only place the shipped weights meet the whole Gold Set with the
veto enforcing.

```bash
echo '{"LearnedVeto": true}' > /tmp/veto.json
go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl -gate-config /tmp/veto.json
```

```
total             457
extract-calls     127
extract-call-rate 0.2779  (soft, no threshold)
overall           precision 1.0000  recall 0.9071  f1 0.9513  accuracy 0.9706
detail     recall 0.9071  (n=140, extracted 127, skipped 13)
hub-index  accuracy 1.0000  (n=78, skipped 78, leaked 0)
residue    accuracy 1.0000  (n=224, skipped 224, leaked 0)
residue-count 224, residue-extracted 0
ambiguous         15 (excluded from scoring entirely, 0 extracted)

stream-weighted estimates (random stratum, n=120, effective n=92.3)
composition detail 0.0584 / hub-index 0.0556 / residue 0.8476
extract-call-rate 0.0565   precision 1.0000   recall 0.9677

boundary stratum (n=188, unweighted)
count detail 37 / hub-index 62 / residue 79 / ambiguous 10
extracted 26, skipped 162
false-drops 11, ambiguous-skipped 10, confirmed 0 of 188
```

Run the rung-on scorecard against the same file on the same day and it reads
**180 calls at precision 0.7175**; the veto takes that to **127 at 1.0000** — the 50
withheld calls and the 28.2% cut ADR-0049's amendment records. (The `hub-index` /
`residue` / `ambiguous` counts differ from the block at the top of this section, which
was pasted before later confirmations moved labels; compare the two scorecards by
re-running both, not against that block.)

**That 1.0000 is in-sample and is not the veto's precision.** The shipped weights were
fitted on these very 442 rows, converging to a mean log-loss of 0.0247, so a perfect
score with zero leaks here is the memorisation ceiling of the fit rather than a forecast
about anything. The generalisation read is ADR-0049's out-of-fold, host-grouped ladder,
which at a comparable depth reads **0.8952 and loses 16 `detail` rows**. Read this block
as "the cut is where the trainer said it is", never as "the veto is perfect".

**Read the drop side of this scorecard with the same suspicion as the one above.** The
capture tap sits downstream of the Extract Gate, so this file cannot measure what the
veto would drop out of pages the gate never admitted — it has none. The number this
override exists to show is the one that did not move: `detail` recall is **0.9071**
either way and the thirteen drops are the **same thirteen pages**, which is exactly what
`TestExtractGoldSetFalseDropGuardUnderTheLearnedVeto` asserts as a set comparison. The
`false-drops 11` in the boundary block are eleven of those same thirteen, unchanged.

Turning the rung on in production is a separate act with its own sequence — a capture
window, an offline score, a Blind Confirmation of the pages it would drop, those labels
committed here, and only then the flip. It is written up under *Turning the Learned Veto
on* in the repository `README.md`.

## The guard (#264)

The scorecards above are read by a human. The **guard** is read by CI:
`TestExtractGoldSetFalseDropGuard` in `cmd/llmbench/goldsetguard_test.go` runs under a
plain `go test ./...` — no network, no model, no Docker — and is the only fatal
condition over this file. Everything else on this page is descriptive.

It replays the gate twice over the whole set, under the Positive Evidence rule and
under the blanket accept it replaces, and folds the pair through
`bench.AuditFalseDrops`. The second replay is what makes **rung attribution** a
measurement: the two configs differ in exactly one field, so a page both drop was
resolved by a *reject* rung and is out of #257's scope, while a page only the rule
drops is this rung's.

**It is a ledger of named, argued exceptions — not a budget.** A budget says "up to N
drops are tolerated" and lets a new drop inside N pass silently, which is the failure
#257 was written against. `extractGoldSetFalseDrops` instead names thirteen specific
pages, each with its argument and its rung, and **any other page the rule drops fails
the build by name**. The hard zero is in force everywhere except those thirteen, all
of which ADR-0044 writes up individually.

Four fatal conditions, all exactness, none of them a threshold:

| condition | what it catches |
|---|---|
| **unrecorded** | the rule drops a `detail` row the ledger does not name — the hard zero |
| **recovered** | a ledger entry the rule no longer drops; strike it, the ratchet may only fall |
| **misattributed** | an entry filed under the other rung — a reject rung has silently moved |
| **restanding** | the entry's label gained or lost a human confirmation since it was written |

(Plus two faults in the ledger itself: an entry with no `Reason`, and a URL named twice.)

**Restanding is how the guard arms itself.** ADR-0043 permits a hard zero to be
*argued* only on human-confirmed labels, and eleven of the thirteen entries rest on
labels no human has read (`pendingBoundaryConfirmations` is 188; `goldset-apply`
refuses an `llm:` confirmer outright, so this cannot be shortcut). Each entry records
the confirmation state it was written at. The moment a human confirms one of those
pages the build goes red on that entry alone, and it must be re-argued at ADR-0043's
standard — no code change and no ADR amendment needed to trigger it. When the boundary
is fully confirmed, the hard zero covers it automatically. See *Human confirmation —
what is still owed* below for how to raise the ratchet.

Two of the thirteen — `hiring.cafe/job/…` and `jobs.blooloop.com/jobs/…` — are dropped
identically by the blanket accept: a reject rung resolves them before Positive Evidence
is consulted. They predate this rung, and `ExtractJobLinkSaturationCount = 5` is
already documented in `crawl_definition.go` as under-evidenced and the first reject
signal to raise. Retuning it is a follow-up, not this ticket.

**What the guard may never assert:** "the rule keeps the pages the extractor accepts
today". Every Boundary Stratum row carries `verdict = accept`, so that assertion is one
line away — and it is precisely the objective ADR-0044 considered and rejected, because
roughly 43% of what the pipeline saves today is not a single posting. The guard file's
doc comment signposts the trap rather than merely avoiding it.

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
total             149      free              51
free-rate         0.3423   stream-free-share 0.1396  (sampling-weighted)
detail            72 (free 48)
coverage          0.6667   weighted-coverage 0.4877
```

These are the numbers **after** ADR-0042's narrowing (`crawler.WithdrawalNotice`, the length
ratio plus the closure banner). Before it the mechanism fired on all 70 `lone-posting` rows;
the 19 it no longer fires on are the 16 stale postings that used to need an explicit
acceptance here, plus the 3 closure-banner rows the review relabelled `residue` (see "Open
question" below).

### Where the expected values come from

An **independent** `jq` + `python3` read of the same JSON-LD bytes
(`scripts/propose-expected.sh`), never the Go traversal under test — generating them by
running `crawler.LonePosting` would make the check a tautology, able to catch a future
regression but never a bug that exists today. At the #256 proposal pass all 210 values (70
titles, 70 locations, 70 working modes) matched the Go read exactly on the first pass,
including the 6 rows whose `jobLocation` is a multi-office array, the 2 whose address is a
bare string, and the entity-encoded titles (`Vendeur.se &quot;Ville&quot;`, `People &amp;
Culture Manager`). ADR-0042's narrowing then dropped 19 of those rows from `expected.tsv`,
which is why it holds 51.

### Accepted fires — 3 residue rows

The mechanism fires on **3 rows labelled `residue`**. They are counted and printed but
not fatal, because each carries an explicit `free_ok` flag and a written reason **on the
row** (never a softened threshold and never a code-side allowlist). A `hub-index` row can
never be excused: a hub is the exact shape ADR-0042's predicate rejects structurally, so
excusing one would mean the predicate itself is broken.

| kind | n | hosts |
|---|---:|---|
| evergreen non-role — talent pool / general application / enquiry page | 3 | `careers.coverflex.com`, `karriere.mri.bund.de`, `www.fluidstack.io` |

There used to be a second kind here, and the mechanism no longer produces it: **16 stale
postings** — withdrawn or filled, `JobPosting` JSON-LD still served — on `career.avenga.com`,
`career.happysocks.com` ×3, `careerpoland.autoliv.com`, `careers.atlantishealth.com`,
`careers.beyond-vision.com` ×2, `careers.patria.com`, `careers.pit.com`,
`careers.spacelift.io`, `careers.sundayapp.com`, `careers.tekever.com`,
`careers.worldpackers.com` ×2 and `jobs.pwc.de`. Those were a **liveness** problem (ADR-0035's
territory) — the fields were read correctly and the job was gone — and `WithdrawalNotice` now
refuses every one of them structurally rather than excusing them row by row.

The 3 evergreen pages that remain are genuine **precision** failures and the standing
argument for narrowing ADR-0042 further. Stated plainly: against the labels as they stand,
the check passes *because those 3 are explicitly accepted*, not because they do not happen.

## Findings

**1. A false-drop the synthetic fixtures cannot produce.**
`hiring.cafe/job/softwareentwickler-m-w-d-devops-level-4-…` is a real single posting
(one JSON-LD `JobPosting`, full body, the live extractor accepted it) that today's gate
**skips**: its "similar jobs" rail carries 8 same-host `/job/<slug>` links, tripping the
posting-saturation rung at `JobLinkSaturationCount` K=5. That constant was calibrated on
the synthetic fixtures — where a `detail` page has a sparse sidebar by construction — so
this is the first real evidence about it. Out of scope for #254 (no gate code changes);
worth a follow-up.

**2. 18 of the 24 exhaustively-sampled `lone-posting` abstains are not postings.**
Sixteen of them are **expired or withdrawn postings** ("this position is no longer active",
"a vaga já não está disponível") whose page body is gone but whose `JobPosting` JSON-LD
block is still served. The remaining 6 are real postings the model wrongly abstained on.
ADR-0042 measured the Free Extraction firing on "2 of 1168 abstains … both real postings
the model wrongly abstained on"; on this larger, later capture the lone-posting abstain
cell is 24 pages and **two thirds of it is stale structured data**. A Free Extraction that
fires on structure alone would resurrect them. Whether that matters depends on liveness
handling downstream — but it is the opposite of the shape ADR-0042's measurement
implied, and it deserved a second look before that decorator shipped on by default.

**#256 measured exactly that risk, and ADR-0042 was narrowed on what it found.** The Free
Extraction now fires on **51 of the 149 rows** — the `lone-posting` stratum less the 19 pages
`crawler.WithdrawalNotice` refuses — and **3 of those 51 are labelled `residue`**, all
evergreen non-roles. See "The Free Extraction fidelity check" above.

**3. 24 of the 72 `detail` pages publish no structured data at all.** A structured-data
path can never reach them; whatever admits and extracts those pages stays the cost
driver.

**4. HISTORICAL (#263) — the rung as #258 shipped it dropped 47 real Job Listings on the
boundary.** This is the finding #257 was written against, and #257 answered it; it is kept
here because the shapes are what a future widening has to keep clearing. At the draw, over
the 188 pages where the blanket accept and the tiered rule disagreed and the live extractor
accepted, 25% read as real postings and the rule skipped every one of them. Four shapes
accounted for most of the 47, and all four failed for the same reason — no job word the rung
recognised in the URL, no lone structured posting, and too little section vocabulary to clear
the weak tier:

| shape | example | why the rung missed it | today |
|---|---|---|---|
| German apprenticeship pages | `team-volkmann.de/ausbildungsberuf/<slug>` | `ausbildung*` was not a path job word | **closed** — the apprenticeship family was added |
| university research positions on institute pages | `biochemie.med.fau.de/<role-slug>` | the role is the whole path, with no section segment before it | **open** — 2 of the 11 remaining drops |
| student jobs on university boards | `jobicco.tu-braunschweig.de/de/1763` | numeric slug under a locale segment | **open** — 1 of the 11; its 4 sibling rows on the same template recovered |
| `/open-roles/<id>` career sites | `wynncareersmacau.com/open-roles/81`, `consensys.io/open-roles/8068918` | `open-roles` tokenised to `open` + `roles`, neither of which was a posting word | **closed** — `role`/`roles` were added |

Both numbers here have since moved, and neither by softening a label. A blind re-read of the
57 consequential rows settled 10 of them out of `detail` (47 → 37), and #257 then widened the
rung to the shapes above, recovering 26 of the 37. **Eleven remain** — each named in
`extractGoldSetFalseDrops` and argued individually in ADR-0044.

**These labels are still LLM-proposed and unconfirmed**, so the 11 is a measurement to argue
with, not a verdict; the confirmation pass is what turns it into evidence. See "The Boundary
Stratum" above.

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

# 1c. draw the BOUNDARY stratum and APPEND it (also append-only). It takes NO
#     -gate-config and NO -seed: the two rules whose disagreement defines the
#     stratum live in goldsetboundary.go, and the drawing is a census, not a sample.
go run ./cmd/llmbench goldset-sample-boundary \
    -capture <repo>/capture/extract-capture.jsonl \
    -since 2026-08-07T21:13:00Z

# 1d. the veto boundary (ADR-0049). WITHOUT -draw it writes nothing and only reports
#     the veto depth over the frame -- the pre-registered go/no-go for turning rung 9
#     on, which needs no labels. WITH -draw it appends the pages below the cut, BOTH
#     verdict halves: ADR-0049 forbids grading that rung against the extractor's own
#     verdict, so the drop set may not be filtered by it. Like 1c the pair lives in
#     goldsetboundary.go rather than behind a flag.
go run ./cmd/llmbench goldset-sample-veto-boundary \
    -capture <repo>/capture/veto-window.jsonl \
    -since <the window's start, RFC3339>
go run ./cmd/llmbench goldset-sample-veto-boundary \
    -capture <repo>/capture/veto-window.jsonl \
    -since <the window's start, RFC3339> -draw

# 2. the labeling view (a working artifact, never committed)
#    -stratum / -n cut a deterministic subset: one stratum whole, or 20 rows of it
go run ./cmd/llmbench goldset-worksheet -out /tmp/worksheet.jsonl
go run ./cmd/llmbench goldset-worksheet -stratum random -n 20 -out /tmp/spotcheck.jsonl

# 2b. the CONFIRMATION view for the boundary stratum: 10 Markdown chunks a human
#     works through one at a time (also a working artifact, never committed)
go run ./cmd/llmbench goldset-confirm-sheet -out-dir /tmp/263/confirm

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

# 6. after a confirmation pass: apply, recompute and rewrite the counts this record
#    determines, refit pagegate's Posting Score weights, and run the guard suite --
#    in one pass, with ADR-0049's pre-registered condition checked at the end. It
#    exits non-zero when the result is not shippable, and it edits no label.
go run ./cmd/llmbench goldset-refit
```

`goldset-refit` owns every hard-coded count below that the record determines, and prints
all seventeen of them, changed or not, so a rewrite lands in the diff rather than behind
it. It does **not** own this file's own prose: the drawings table, the scorecard blocks
and the label distributions are a human's to keep current.

To **correct a label**, edit the `label` column of `labels.tsv` and run
`goldset-apply`; it rewrites both files canonically and refuses a sheet whose stratum or
verdict disagrees with the substrate. A correction keeps `proposed_label`, so the record
— and the agreement figure `goldset-apply` prints — still say what the proposer had
proposed.

## Human confirmation — what is still owed

**What the build asserts about confirmations.** The five confirmation counts —
`pendingHumanConfirmations`, `pendingExpectedConfirmations`,
`pendingBoundaryConfirmations`, `pendingVetoBoundaryConfirmations` and
`randomSpotChecks` — are one-directional ratchets
(ADR-0048): each fails only in the direction that means a human signature *vanished*,
and merely logs the figure in the direction that means work landed, so a productive
confirmation pass never leaves `main` red. Two things stay pinned in **both**
directions: the four drawings' row counts, because a drawing is a fixed act of
sampling, and `ambiguousRows`, because marking a page unresolvable is a rare,
deliberate keystroke that changes what every confusion number is computed over.
Pinned means neither direction may pass *unseen*, which the two maintenance paths
deliver differently: a hand edit of the record leaves `go test` red until the constant
is moved, while `goldset-refit` rewrites it and prints it as `ACKNOWLEDGE: a pinned
census moved`, so it lands in your diff rather than stopping the pass. The
direction the ratchets gave up is asserted more sharply instead — the build requires
that `goldset.jsonl`, `labels.tsv` and `expected.tsv` name the **same confirmer on
every row**, and names the rows where they do not.

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

# then refit: goldset-refit recomputes pendingHumanConfirmations in
# cmd/llmbench/goldset_test.go and every other count this record determines, and
# rewrites them in place. Commit them with the confirmations.
go run ./cmd/llmbench goldset-refit
```

### Confirming row by row with the labelling tool (#286)

```bash
go run ./cmd/llmbench goldset-ui -by "<your name>" -stratum random
# open the printed URL — it carries this session's token; the tool binds loopback,
# refuses a foreign origin, and refuses to run under CI.
```

One row at a time in row-id order, restricted to `-stratum` and skipping every row that
already carries a confirmer, so a pass resumes across sittings. `d`/`h`/`r`/`a` label the
page; the **Proposed Label is revealed only after you answer** (ADR-0048), together with
whether you agreed. A note is required when you disagree and on `ambiguous`, and it
replaces the proposer's; on agreement it is left empty so the proposer's own survives.
`u` undoes the last unwritten decision, `w` writes the buffer now, and every decision is
journalled to a file under the OS temp dir the moment it is taken, so a crash costs a
rewrite and not your reading.

The tool is **not a second writer**: it synthesizes a `-proposals` file and a
`-confirm-ids` file and calls `goldset-apply` in process, once per batch, so that verb's
total validation, its refusal to overwrite a proposer, and its retraction of a
confirmation whose label changed all still apply. The batch files are left in the
session's scratch directory as the account of what was applied. After a session, ratchet
the confirmation floors in `../goldset_test.go` to the figures the apply summary just
printed, in the same commit.

Rows are shown as their **captured text** — the parsed page the live extract stage
decided on, which is what the label is a statement about (ADR-0043). Beside it, each row
states its **Capture Fidelity** (ADR-0047, #287): the tool fetches the row's URL again
through the crawler's own downloader, under the crawler's user agent and the refetch
lane's politeness, re-parses it and reports *same* (the live page still carries the
captured text), *drifted* (it has changed, so weigh it), *gone* (it is not the captured
page), or "not measured" (robots.txt refused, or the fetch had not finished). A *gone*
row is never offered a live view — a closed posting's withdrawal page is `residue` in the
rubric and argues confidently for the wrong label. The re-fetched bytes are cached under
the OS user cache directory, never inside the repository and never written back onto a
row; `-refetch=false` turns the whole path off and labels from the captured content
alone.

Where fidelity admits it, the page is **rendered beside the captured text** (#288): the same
Structural Rendering the parser produces, with link targets kept, so an openings index and one
Job Listing are distinguishable at a glance. The tool serves it from loopback in a frame with
scripts disabled and referrers suppressed — the site is never framed, and what is on screen is
the crawler's own parse, which is what the Extract Gate read. A *drifted* page carries a
warning; a *gone* one is refused outright. A row that carries its own Structural Rendering is
shown it directly, with no re-fetch. `o` opens the live URL in a browser tab for any row,
including one the tool will not render.

### The boundary stratum must be FULLY confirmed (#263)

This is the stratum ADR-0043 actually requires a human on. Its 188 rows are the rows a
**hard-zero false-drop guard is decided on**, and they are exactly where an LLM labeller
is least reliable — that is what "boundary" means. **0 of 188 are confirmed**, so
`pendingBoundaryConfirmations` in `../goldset_test.go` is 188, and the build asserts it in
**one** direction (ADR-0048): a pending count that rises fails, one that falls is only
logged, with the number to lower it to. A confirmation that vanished is caught either way —
the build also asserts that `goldset.jsonl` and `labels.tsv` name the same confirmer on
every row, and names the rows where they do not.

The review is chunked so a partial pass is legitimate, resumable and visible in the diff:

```bash
# 10 Markdown chunks of 20 rows each, ordered by row id (a working artifact, never committed)
go run ./cmd/llmbench goldset-confirm-sheet -out-dir /tmp/263/confirm

# read confirm-01.md. Each row shows the URL, the page title, two windows of the page's
# own text, its outbound-link counts, and the proposed label with its one-clause reason.
# The live verdict, the JSON-LD and the gate's marks are WITHHELD on purpose: the number
# this stratum produces is what the gate concluded about these very pages.

# a label you want CHANGED: edit its `label` column in labels.tsv first (which retracts
# any confirmation on that row), then re-run goldset-apply. Mark a row you genuinely
# cannot settle `ambiguous` and put the tension in its `note` column.
go run ./cmd/llmbench goldset-apply

# then confirm EXACTLY the rows you read — each chunk file ends with this command,
# pre-filled with its own ids:
go run ./cmd/llmbench goldset-apply -confirmed-by "<your name>" -confirm-ids /tmp/263/confirmed-01.txt

# then refit. goldset-refit lowers pendingBoundaryConfirmations by what you confirmed and
# recomputes ambiguousRows, boundaryDetailRows and the recovery-ledger constants your
# reading may have moved -- all of them printed, all of them in the same commit.
go run ./cmd/llmbench goldset-refit
```

The verb **refuses** to raise a pending count or lower a confirmed one. That direction
means a human signature vanished, and ADR-0048 reserves it to the person who withdraws
the confirmation; the refusal happens before anything is written.

`-confirm-ids` names exactly the rows a human read, so `labels.tsv` shows precisely which
labels gained a confirmer and the pass can span sessions. `-confirm-stratum boundary` is
still there for the final sweep once every chunk has been read.

**The number that matters when this reaches 0 is `boundaryFalseDropsRemaining` — currently
11.** That is how many real Job Listings the Positive Evidence rule still drops here.
(`boundaryDetailRows`, 37, is the label census these drops are counted against, not the
drop count itself — the two parted company when #257 widened the rung.)

Each confirmation is now load-bearing on the guard, not just on the record: the eleven sit
in `extractGoldSetFalseDrops` recorded at the standing they were argued at, and
`TestExtractGoldSetFalseDropGuard` goes red on any one of them the moment a human confirms
its label, forcing that exception to be re-argued at ADR-0043's standard. Confirming a row
that turns out to be a hub or residue is just as valuable: the label changes, the row stops
being a false-drop at all, and the entry must be struck.

### The random stratum is SPOT-CHECKED, not confirmed (#262)

ADR-0043 asks less of this stratum, and for a reason: it is 120 rows of ordinary stream
traffic, no later guard is decided on any single one of them, and its numbers are
weighted estimates rather than pass/fail conditions. What it needs is evidence that the
labeling process is *sound*, which a sample answers. **`randomSpotChecks` in
`../goldset_test.go` is 4: four random rows have been read by a human.** It is a ratchet
that RISES and it is asserted as a **floor** (ADR-0048): falling below it fails the build,
because a spot-check that vanished means a lost signature; landing above it only logs the
figure to raise it to, so a productive session never leaves `main` red.

```bash
# the spot-check surface: 20 rows in deterministic order, not the 3.9 MB substrate
go run ./cmd/llmbench goldset-worksheet -stratum random -n 20 -out /tmp/spotcheck.jsonl

# read them against the rubric above, correct any wrong label in the `label` column of
# labels.tsv, put your name in the `confirmed_by` column of EXACTLY the rows you read:
go run ./cmd/llmbench goldset-apply

# then refit: goldset-refit raises randomSpotChecks in cmd/llmbench/goldset_test.go to
# what the record now says, and prints the figure beside every other count.
go run ./cmd/llmbench goldset-refit
```

Read the 40 `verdict=true` rows first if you only have time for some: 31 of them are the
`detail` rows the whole composition estimate rests on, and a wrong one there moves every
weighted number.

**Do not use `-confirm-stratum random`.** It would stamp all 120 rows at once from one
command — a full confirmation of 120 pages nobody read, which is exactly the thing the
provenance record exists to prevent. A spot check is 20 rows a human actually opened.

`goldset-ui -stratum random` is what turned that warning into work, and the stratum is
now **fully confirmed**: the 116 rows still owed a confirmer were read one at a time for
a keystroke each, which is what the warning was always asking for — the objection is to
stamping 120 rows nobody read, not to reading them. The warning still stands for any
future drawing: `-confirm-stratum` remains the wrong instrument, whatever the stratum.

### Second pass — the 51 expected extractions (#256)

Everything in `expected.tsv` is **proposed by an automated script and confirmed by
nobody**. `pendingExpectedConfirmations` in `../goldset_test.go` is the machine-visible
count of that gap.

```bash
# the entire #256 review surface is 51 lines, not the 2.2 MB substrate
column -t -s$'\t' cmd/llmbench/extract-goldset/expected.tsv | less -S
```

**(a) the 3 `free_ok` acceptances first** — they are what decides whether ADR-0042 ships
as written, and all three are the evergreen non-roles
(`careers.coverflex.com/…spontaneous-application…`,
`karriere.mri.bund.de/Anfragen-fuer-Praktikumsplaetze-…`, `www.fluidstack.io/jobs/05c2e69c-…`).
A refused acceptance means lowering `acceptedFreeOnResidue` in `../scorefree_test.go`, and
the check then goes red until the **mechanism** is narrowed — the fix belongs there, not
in the benchmark.

**(b) the 51 title / location / work_arrangement values.** They are the page's own
declared data, so a wrong one is usually obvious against the `url` column. 8 rows should
read `remote`, 43 `unspecified`.

```bash
# edit any wrong value or drop any free_ok you refuse, then:
go run ./cmd/llmbench goldset-apply -expected-confirmed-by "<your name>"
# then refit: goldset-refit lowers pendingExpectedConfirmations in
# cmd/llmbench/goldset_test.go to what the record now says.
go run ./cmd/llmbench goldset-refit
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
