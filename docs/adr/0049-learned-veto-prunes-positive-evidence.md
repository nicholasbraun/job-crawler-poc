# A Learned Veto prunes the Extract Gate's Positive Evidence accepts

ADR-0044 inverted the Extract Gate's burden of proof: a page clearing every reject rung is
extracted only on **Positive Evidence** that it is one posting. That took the stream's
extract-call rate from 0.9944 to 0.1390. What remains is spent at precision 0.7175 on the
labelled set — roughly two in seven calls buy nothing — and every further tightening of the
tiered rule now costs an ADR amendment and a bench run, and can only ever express what a human
thought to write down.

The Extract Gold Set holds 457 real captured pages with human-owned labels. That is enough to
**learn** a ranking over them. So a final rung is added: a page carrying Positive Evidence is
extracted only if its **Posting Score** — a learned, graded estimate that the page is one Job
Listing — clears a shipped threshold. This is the **Learned Veto**, and it is the first rule in
`pagegate` that is fitted rather than argued.

## Where it sits, and why that is the only place it can sit

Replaying `ExtractDecision` over all 457 gold-set rows, bucketed by the rung that produced each
verdict:

| rung | verdict | n | detail |
|---|---|---:|---:|
| 1–6 | reject | **0** | 0 |
| 7 job_link_saturation | reject | 4 | 2 (false-drops) |
| 8 positive_evidence | reject | 273 | 11 (false-drops) |
| 8 positive_evidence | **accept** | **180** | 127 |
| 2 ATS posting exempt | accept | **0** | 0 |

**One hundred percent of the extract bill on this population is spent by rung 8.** Rungs 1–6
shed nothing, because the capture tap sits downstream of the gate — pages they reject were never
captured, so the Extract Gold Set *is* the rung-8 population. The ATS exemption spends nothing
either: `catalog.Classify` returns `RoleUnknown` for all 457 rows, since recognized ATS boards go
down the ATS Fetch lane (ADR-0022) and never reach crawl-and-extract.

It follows that a rule which cuts calls must prune rung 8's **accepts**. A rung placed anywhere
else is judging pages nobody is paying for. This is not a preference; it is the only position
with a bill attached.

The Learned Veto therefore runs **after** Positive Evidence and is **subtractive only**. Rung 8
is untouched: every mark ADR-0044 argues page by page still admits exactly what it admitted, the
tier shape is unchanged, and none of the thirteen entries in the false-drop ledger move. What
changes is that rung 8's accepts became prunable.

## Considered options

| option | why not |
|---|---|
| **The score replaces `hasPositiveEvidence`** (the two-sided form the probe measured) | Buys more — 180 calls to ~152, leaks 50 to 25 at equal recall — but discards ADR-0044's independent 2199-page replay on the strength of 442 rows with signals hand-picked on the same set, and puts all thirteen ledger entries back in play at once. Available later, on this rung's infrastructure, if the veto proves itself. |
| **The score re-admits pages rung 8 rejects** (a rescue rung) | Cannot serve the objective. That population is the 273 rows the gate already refuses to pay for, so the rung can only add calls. The one number bearing on it is discouraging: zero false-drops costs 317 calls against the gate's 177 — recovering 13 postings for +140 calls, a 1:10 ratio, where ADR-0044's own stopping rule rejects any mark worse than 1:3. Deferred to #298, where a low score *defers* a page under a budget instead of dropping it. |
| **A three-band score with two thresholds**, mirroring the Gate's Confidence Score (ADR-0016) | The Gate has three *actions* — certain-accept, ask the LLM, reject — so its middle band means "spend the classifier". The Extract Gate has two: extracting *is* asking the model. ADR-0044 closed the L2 cheap-confirm that would have been the third action, so the middle band has nothing to defer to. |

## The threshold is pinned to the ledger, not to recall

The merge gate here is not precision and not cost. It is `TestExtractGoldSetFalseDropGuard`
(ADR-0020, #264): an exact-set ratchet over thirteen individually argued pages, where any other
page the rule drops fails the build **by name**, and the ledger may only shrink.

An operating point chosen at "recall parity" is therefore useless: holding recall at 0.9071 while
dropping a *different* thirteen `detail` rows turns the guard red thirteen times over. So the
threshold is defined as **the deepest cut that loses none of the 127 `detail` rows rung 8
currently accepts** — the same rows, by name. The guard stays green by construction of the
threshold rather than by luck, and the thirteen ledger entries — eleven of which rest on labels
awaiting a human, and re-open the moment that confirmation lands — are untouched.

The threshold ships **compiled in beside the weights**, not as a config knob. It is the operating
point the training run *chose* under that constraint; letting it drift independently of the
weights would mean running an operating point nobody measured.

## What the score reads

**Score Signals**: the Structural Signals the gate already computes, taken as **gradations**
rather than as the thresholded marks Positive Evidence collapses them into — the vocabulary-group
count as 0–5 rather than `>= 4`, the distinct same-host job-link count bucketed across 0–4 rather
than the rung-7 threshold at 5 — plus the Score Vocabulary. The gradation is the point: every page
this rung judges has already cleared Positive Evidence's thresholds, so within that band the
thresholded marks are near-constant and only the degrees still separate anything. ADR-0016 made
the same move once already, folding the Gate's link count in continuously as `min(count/K, 1)`.

**Score Vocabulary**: a learned word list, and it is the entire justification for learning
anything here. Ablating the signals at the probe's parity point: URL alone 0.614; adding Title,
link count, saturation and JSON-LD 0.756; adding the learned body vocabulary **0.836**. Everything
hand-craftable tops out barely above today's hand-tuned gate (0.7175). The body vocabulary is the
result; the rest is scaffolding.

It is shipped as an **explicit list of words with weights**, not as the probe's 2^14 hashed bag,
and the reason is a specific hazard rather than taste. There are 457 rows but far fewer hosts —
`observermedia` ×3, `careers.happysocks.com` ×3, `biochemie.med.fau.de` ×2, `berufstest.zeit.de`
×2. A blind hash will learn host boilerplate, cross-validation will *reward* it, and production
will be worse; nobody can read a hashed weight to notice. An explicit list is read by a human, who
checks that it weighs `aufgaben` and `vollzeit` and not `happysocks`. Host and URL words are
excluded by a test, not by a promise. Presence is binary, never a count — ADR-0044 measured that
what distinguishes a posting is the *range* of sections, not the frequency of any one word.

The truncation from a full hashed bag to N explicit words has a cost, and it is measured and
recorded rather than assumed. If it exceeds ~0.02 precision, the hashed form returns with a
generated human-readable companion listing.

## How the weights ship

`llmbench train-scorer` fits the weights over the committed Extract Gold Set and emits **generated
Go source** into `pagegate` itself, not into a sub-package. The gate stays a pure function — URL and
content in, verdict out, no model, no network, no database — and the weights are compiled in.

A sub-package was the first design and does not compile: the Score Signals reuse the gate's
unexported detectors, so `extractscore` would import `pagegate` while `pagegate` imports
`extractscore` for the score — an import cycle. Both ways out are worse. Moving ADR-0044's argued
word lists into a third leaf package relocates the disjointness invariant `positive_evidence.go`'s
comment holds, and duplicating the detectors is the exact drift a shared `Signals()` exists to
prevent. Same-package is also *less* API widening: three exported symbols (`Signals`, `Score`,
`VetoThreshold`) rather than four exported detectors plus a new package's own surface.

Two deviations from the repo's one precedent for generated data, `internal/geo` (ADR-0031), each
deliberate:

- **Generated Go source, not an embedded artifact.** `parseGazetteer` *silently skips* malformed
  rows; a gate that decides spend on every walked page must not be able to boot with a quietly
  truncated vocabulary. At a few hundred entries there is no reason to accept a runtime parse, and
  generated source fails at compile time instead.
- **The trainer is a `llmbench` verb, not a `gen/` binary.** It is in Go for a reason stronger
  than single-language tidiness: it calls the **same** `Signals()` the gate calls. Train/serve
  skew — a trainer that tokenizes German slightly differently from the gate — is the failure mode
  that makes a learned rule silently not the rule that was measured, and sharing one
  implementation makes it unrepresentable.

Two guards hold the artifact: retraining from the committed Gold Set must reproduce the committed
weights bit for bit (so a weight can only move by a Gold Set change or a trainer change, both
visible in the same diff), and no vocabulary entry may be a word of any host in the Gold Set.

## Attribution, and why the score is not a label

The Learned Veto gets one rung name, `learned_veto`, primed at zero like every other
(`RejectRungs`). The score is **not** bucketed into the label value. Shadow Extraction samples 1%
of rejects filed *by rung*; splitting the veto across `low`/`mid`/`high` series would divide those
already-sparse samples three ways and triple the time to observe the first false-drop — which is
the exact failure `PrimeShadow` exists to prevent, after PR #272 shipped a rung whose first shadow
accept was invisible to every panel.

Instead the score travels **with the sample**, for ADR-0044's own reason for carrying the rung:
re-deriving it downstream could use a different binary's weights, filing a verdict against a score
the gate never computed. A `Score` field on `ShadowSample` is safe against the durable stream
without a presence flag — an entry from before the deploy decodes it as zero, but can never carry
`Rung == "learned_veto"`, and the rung gates the read.

Alongside it: a histogram of the Posting Score over **both** accepts and vetoes, which is the drift
detector — it shows whether the cut sits in a dense or sparse region of the live distribution and
whether that distribution still resembles the Gold Set the weights were fitted on; and a distinct
gated reason, so the calls the veto saves are countable on their own rather than pooled with every
other structural reject.

## Rollout, and the condition this is allowed to fail

The rung **ships off**. `EXTRACT_LEARNED_VETO=false` is bit-identical to today, and off, the score
is never computed at all. The two extract switches compose independently: pulling this one
restores the unconditional rung-8 accept, pulling `EXTRACT_REQUIRE_POSITIVE_EVIDENCE` restores the
blanket accept, pulling both restores pre-#257.

There is **no in-production observe mode**, and that is a decision rather than an omission. Grading
a would-be veto against the extractor's own verdict is the trap #296 documents: that verdict runs
at precision 0.454 against human labels (0.816 on the Random Stratum), and its errors concentrate
on exactly the pages that decide an operating point. The capture tap already provides observation
offline and better — it stores the full parsed content of every extracted page, so the share a
candidate threshold would veto is computable over a real stream frame with **no labels involved**,
which is the stream-weighted saving nobody has yet measured.

The flip is therefore its own act, on the #264 pattern: run a capture window, score it offline,
draw the pages the veto would drop — which is by construction the Boundary Stratum for this rule —
confirm them blind (ADR-0048), commit them into the Gold Set, then flip the default. The
confirmed rows outlive the decision, which matters because label quality, not volume, is the
binding constraint on every approach in this line.

**The condition, registered before the measurement:** the Learned Veto is turned on only if it
vetoes **at least 10% of rung-8 accepts while losing none of the 127 `detail` rows** rung 8
accepts today. Below that, a generated weights table, a trainer verb, a package, a rung and a
metric label cost more than they return, and the honest outcome is that 442 rows do not support a
learned veto — recorded as an amendment here, with the wiring left in place behind its switch for
when the label count grows. Writing the condition down first is the same discipline this package
already applies to thresholds: a cut that flatters the run is moving the wrong way.

## Consequences

- **`pagegate` now contains a rule that cannot explain itself.** Every other list in the package
  carries an argument per entry — `praktikum` excluded for "0 additional postings and 2 extra
  leaks", `roleSlugMinTokens` documented as "a judgement, not a fit". A weight vector cannot do
  that, which is why a veto carries its score, why the vocabulary is read as data, and why this
  rung is the only one behind its own switch after ADR-0044's.
- **The Extract Gold Set becomes load-bearing in a second way.** It already scores the gate; it now
  also *produces* part of it. A change to the Gold Set changes the shipped weights, and the
  regenerability guard makes that visible rather than silent. Labels stay human-owned: nothing here
  licenses editing one to move a number.
- **The measurements above are provisional.** They come from an uncommitted probe: 442 rows, 5-fold
  CV, no held-out test set, signals hand-picked while looking at the same set, and unweighted
  numbers over a deliberately Boundary-heavy population (188 of 457). Treat the ordering of the
  results as the finding, not the third decimal. The committed trainer has now replaced them, under
  **host-grouped** folds so repeated hosts cannot flatter a split; what it reported is the
  amendment below.
- **Rung 7's two false-drops are still rung 7's.** `hiring.cafe` and `jobs.blooloop.com` are dropped
  before Positive Evidence is consulted and are untouched here, as they were in #257.


## Amendment: what the training run measured (#300)

`llmbench train-scorer` was written, run over the committed Extract Gold Set, and its output is
now `internal/pagegate/posting_score_weights_gen.go`. **The pre-registered condition is MET.**
Everything below is restricted to the pages the Positive Evidence rung accepts, because that is
the only population the Learned Veto will ever judge; no figure over all scorable rows appears in
the trainer's report at all, so one cannot be mistaken for the other.

| | rung 8 today | with the Learned Veto |
|---|---:|---:|
| scorable accepts | 177 | 127 |
| `detail` among them | 127 | **127** |
| leaks (hub-index + residue) | 13 + 37 | 0 + 0 |
| precision | 0.7175 | **1.0000** |
| `detail` lost | — | **0** |

`VetoThreshold` is **0.605395**, and at that cut the rung withholds **50 of 177** extract calls —
**28.2%** of rung 8's accepts, well past the 10% floor — while dropping none of the 127 `detail`
rows, by name.

**The population.** 457 rows: 0 unlabelled, 15 `ambiguous` set aside, **442 scorable** (140
`detail`) across **357 hosts**. The rung admits **180** pages, of which 3 carry no scorable label,
leaving the **177 / 127 / 0.7175** the table above starts from. **Zero** rows take the ATS
exemption, confirming the census at the top of this ADR: the whole extract bill on this population
is rung 8's.

**The fit.** Full-batch gradient descent, 500 iterations, learning rate 0.5, L2 1e-4 on the
weights and never the intercept, converging to a mean log-loss of 0.0247 with an intercept of
-2.5028. Full batch removes the example-shuffling seed entirely; the run's only randomness is the
committed fold seed. Cross-validation is 5 folds **grouped by host** (fold sizes 92 / 90 / 90 / 76
/ 94 over the 357 host groups), with the Score Vocabulary reselected inside each fold's training
half — selecting once over the whole set and then cross-validating measures a model that has
already seen the held-out rows. Neither the L2 term nor the iteration count is a lever here: the
out-of-fold read is flat across L2 from 1e-4 to 1e-2, and every longer run only overfits (at 1000
iterations the out-of-fold precision falls).

**The Score Vocabulary.** 37,320 candidate words; **583 removed as a word of a Gold Set host**
and 32,321 as too rare (document frequency below 5), leaving 4,416 admissible. The artifact ships
**500** of them plus the 17 structural Score Signals. The host exclusion is a real cost paid
deliberately — it removes `jobs`, `career`, `karriere`, `berlin` and 579 others — and it is the
price of the hazard this ADR names: 457 rows on 357 hosts.

Out-of-fold precision over the surviving accepts at a 10% veto depth, against vocabulary size:

| size | 50 | 100 | 200 | 300 | **500** | 1000 | all (4,416) | hashed 2^14 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| precision | 0.7736 | 0.7862 | 0.7862 | 0.7925 | **0.7987** | 0.7987 | 0.7987 | 0.7987 |
| `detail` lost | 4 | 2 | 2 | 1 | **0** | 0 | 0 | 0 |

Survivors are 159 at every size, so those precision steps are four rows, then two, then one. 500
is the knee: the smallest size at the plateau, and the first at which the cut loses no `detail`
row out of fold.

**The gap to a full hashed representation is 0.0000** — the untruncated 2^14 hashed bag scores
exactly what the 500-word explicit list scores, on the same rows, out of the same folds. The
~0.02 line this ADR names as the point where the readable list is revisited is not approached, so
the explicit list stands and costs nothing. It also earns its keep on the day it was written: the
heaviest entries read `bewerben`, `apply`, `arbeiten`, `bieten`, `interesse`, `vergütung`,
`ausbildung` on the positive side and `filter`, `view`, `see`, `page`, `featured`, `members`,
`newsletter` on the negative — posting words against hub words, and not one sampled employer's
name.

**The within-accepts curve**, the thing nobody had computed. In-sample, with the shipped weights:

| depth | 5% | 10% | 15% | 20% | 25% | 30% |
|---|---:|---:|---:|---:|---:|---:|
| `detail` lost | 0 | 0 | 0 | 0 | 0 | 3 |
| precision | 0.7560 | 0.7987 | 0.8467 | 0.8944 | 0.9478 | 1.0000 |

Out of fold, host-grouped — the honest generalization read, printed beside it and not to be
confused with it:

| depth | 5% | 10% | 15% | 20% | 25% | 30% |
|---|---:|---:|---:|---:|---:|---:|
| `detail` lost | 0 | 0 | 1 | 5 | 8 | 16 |
| precision | 0.7560 | 0.7987 | 0.8400 | 0.8592 | 0.8947 | 0.8952 |

The two agree to a 10% cut and diverge past it, which is the honest shape of a rule fitted on 442
rows. **The chosen operating point is in-sample by necessity, not by oversight**:
`TestExtractGoldSetFalseDropGuard`, the merge gate this ADR pins the threshold to, reads the same
rows the same way, and a threshold chosen against anything else would turn that guard red for
every posting it swapped. The out-of-fold ladder is what says how far past 10% the in-sample
number can be trusted: not far. Whoever flips `EXTRACT_LEARNED_VETO` should read the 28.2% cut as
an in-sample figure whose out-of-fold counterpart at 30% depth already costs 16 `detail` rows,
and take the capture-window pass this ADR's rollout section describes before believing it.

**Determinism.** The artifact is a pure function of (Gold Set, trainer code, flag defaults): rows
sorted before the first sum, signal names indexed through a sorted slice rather than ranged out of
a map, single-threaded accumulation, a fixed-seed fold assignment, every product in the descent
written to block a fused multiply-add, and every weight rounded to six decimals before anything
discrete reads it. Regenerating the committed file on `darwin/arm64` and on `amd64` produces
byte-identical output; the largest cross-architecture divergence in the raw weights is 1e-15,
against a nearest rounding boundary 1.2e-9 away.
