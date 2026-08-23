# A host-breadth drawing widens the Posting Score without reading it

ADR-0049's Learned Veto is fitted, not argued, which gives it the only maintenance loop in
`internal/pagegate`. `docs/improving-the-posting-score.md` records what that loop costs and
where it goes wrong. This decides how the **next** turn of it draws its rows.

The fit today stands at 692 scorable rows over **516 hosts**, against an artifact carrying
**517 weighted entries**. Two of the four things that improve it are about hosts rather
than rows: cross-validation is host-grouped, and the leakage guard bans the host words of
the fit population, so both read **hosts**. A thousand rows from twenty hosts buy far less
than two hundred from two hundred.

## The drawing

A fifth drawing, `host-breadth`, over one closed capture window:

- The sampling unit is a **host**, not a page. The frame is reduced to at most one page per
  `(hostname, verdict)` — the page with the lowest `sha256(seed + "\n" + url)`, the same
  deterministic order every drawing here selects in — and the quotas are then applied to
  those representatives. 900 rows are ~900 distinct hosts.
- `hostname` is the key, not eTLD+1, because `hostname` is exactly what the fit's fold
  assignment groups on. Sampling on a coarser key would leave the folds seeing repeats the
  drawing thought it had spent a row to avoid.
- The cluster is `(hostname, verdict)` rather than `hostname`, so a host publishing both an
  accepted and an abstained page represents itself once in each cell and the two cells'
  quotas stay independent.
- Quotas: **500 accept / 400 abstain**. Unequal because `detail` is what the fit is short
  of and real Job Listings concentrate in the accept half, where the live extractor's
  precision is 0.454 against human labels; the abstain cell still gets 400, because a fit
  shown only accepts learns nothing about what a hub or a benefits page looks like.

## It reads nothing the previous fit produced

This is the decision, not the arithmetic.

The obvious way to draw more rows is to re-band the drop set at today's `VetoThreshold` and
oversample where the model is wrong. That targets the highest-value rows — and it is
exactly the failure `docs/improving-the-posting-score.md` names:

> Never let two consecutive refits draw their training data from a threshold the previous
> refit chose, with no human confirmation in between.

The 280-row `veto-boundary` drawing was banded at `VetoThreshold` 0.605395. Refitting moved
the threshold to 0.165048 and **not one** of those 280 rows has been confirmed by a human.
A second banded draw would therefore select its training set from beliefs the first draw
produced, one level up from the extractor-verdict trap ADR-0049 already refused: there the
*labels* came from a model, here the *sampling frame* would.

So this drawing conditions on the **live extractor verdict** and on nothing else. That is
an outside fact about the frame, and ADR-0049 already permits stratifying on it — every
page keeps a non-zero, known inclusion probability, where filtering would give one half
probability zero. The drawing reads no Posting Score, no threshold, and no band derived
from either, which also means it stays byte-reproducible across every future refit where a
banded draw does not.

The cost is stated rather than hidden: this buys host breadth and volume, and it does
**not** buy "rows the model gets wrong". Those are still worth drawing — after a human
confirmation pass has put an outside fact back in the loop.

## The weights, and what they estimate

Inverse selection probabilities normalized within the drawing:

    w_c = (N_c / n_c) x (n / N)

the same arithmetic ADR-0049's bands use, and for the same reason: this population is
*enumerated* by the replay, so its probabilities are known exactly and there is no capped
stream to reconstruct a verdict share from. `weightsFor` is deliberately not used — it
exists to undo the tap's per-verdict caps on a drawing whose frame is a capped stream
sample, and this frame is not one.

A weighted count over these rows estimates **the hosts in that window, one page each**. It
never estimates the stream, in which a host publishing 900 pages counts 900 times and here
counts once. Its rows therefore stay out of the weighted stream scorecard, exactly as a
Boundary Stratum's do — and its weights are never pooled with another drawing's, which
would be adding two different denominators together.

## One renderer, fenced on the stamp

ADR-0046 requires a drawing to hold one renderer: a captured page is evidence about the
bytes the gate will later see, and a window a crawl flipped `PARSE_STRUCTURAL_RENDERING`
part-way through holds rows from two parsers.

Every earlier drawing enforced that with `-since`. That cannot work here — `-since` is a
floor, and the renderer this window flipped *to* is the one at its end. The drawing
therefore fences on the **stamp itself** (`-renderer`, defaulting to the renderer today's
parser writes, read off a parser instance rather than written down). A row carrying **no**
stamp is dropped too: an unstamped row predates the stamp, which makes its renderer unknown
rather than equal to anything, and a drawing may not silently assume the answer.

## Consequences

- **It is drawn once.** A second window's rows would carry a second set of inclusion
  probabilities into one stratum. A fresh window is a new drawing, declared in code.
- **900 unconfirmed rows enter the fit population.** `pendingHostBreadthConfirmations`
  counts them and only falls as a human signs for them. This drawing is not a Boundary
  Stratum, so ADR-0043's every-row rule does not reach it — the debt is counted anyway,
  because what makes an unconfirmed row expensive is not which drawing it came from. The
  shipped Posting Score is now derived partly from labels nobody has read, and every figure
  quoted off it inherits that.
- **The set's host count doubles.** Measured: the fit population went from 692 scorable
  rows on 516 hosts to **1,589 on 1,074**. The leakage guard grew with it, as expected —
  1,516 candidate entries dropped across **1,147 distinct host words**, a real cost the
  guard accepts by design (ADR-0049).
- **Nothing here changes a gate verdict on its own.** The drawing appends rows; the
  threshold moves only when `goldset-refit` re-chooses it under the zero-`detail`-loss
  constraint. It re-chose **0.136551**, down from 0.165048.

## What it actually bought

The refit's own numbers, in the two readings that must never be quoted as one (ADR-0049):

| | before (737 rows) | after (1,637 rows) |
|---|---:|---:|
| scorable rows / hosts | 692 / 516 | **1,589 / 1,074** |
| mean log-loss | 0.0421 | **0.0812** |
| in-sample precision after the veto | 0.9626 | **0.8500** |
| out-of-fold `detail` lost at ~50% depth | 27 of 180 (15%) | **53 of 527 (10.1%)** |

In-sample precision **falling** while out-of-fold `detail` loss also falls is the whole
point: the previous fit's 0.9626 was a 517-weight model largely memorising 692 rows. More
hosts made it generalise, and the honest read improved by a third.

## What it exposed

Sampling one page per host reached hosts no earlier drawing had, and five of them are ATS
posting URLs the gate returns on at **rung 2** — before the Positive Evidence rung this
veto prunes. ADR-0049 asserted that number was zero. It is now a pinned census
(`atsExemptRows`), and two of the five are not postings at all: an apply-form URL and an
ATS vendor's own marketing page, both classified as Job Listings by path or host. See
ADR-0049, *Correction: rung 8 spends nearly, not quite, the whole extract bill*.

That is a drawing doing its job. A sample that only ever revisits known hosts cannot
falsify a claim about hosts nobody sampled.

## Related

- **ADR-0049** — the Learned Veto, why the score is fitted, and how the threshold is chosen.
- **ADR-0043** — the Extract Gold Set, its drawings and their weights.
- **ADR-0048** — Blind Confirmation and the Proposed Label.
- **ADR-0046** — the renderer stamp and why a drawing may not mix two.
- `docs/improving-the-posting-score.md` — the loop this drawing is one turn of.
