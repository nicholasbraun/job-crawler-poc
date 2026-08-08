# The Extract Gold Set is captured parsed Content, stratified and weighted

ADR-0020's Extract Gold Set is committed HTML fixtures. In practice the committed set is
**synthetic** — invented domains, every `detail` page carrying exactly one structured-data
`JobPosting`, every `hub-index` and `residue` page carrying none at all — so a gate that
reads structured data scores perfectly on it by construction. It proves nothing. The
substrate changes: a row is a page the live extract-decision tap captured, stored as the
**parsed Content the pipeline actually produced**, carrying its label, its stratum, its
sampling weight, and its label provenance.

## Reversing ADR-0008's rejection, deliberately and only here

ADR-0008 considered and rejected pre-parsed fixtures: they "blind the benchmark to
`getMainContent`/parser changes … and drift from live parser output." That reasoning holds
for the classifier Gold Set, whose whole point is re-running the live parser over frozen
bytes, and **that set keeps raw HTML**. It does not hold here. For the Extract Gate a sampled
page is evidence about a *stream*, and re-fetching its HTML months later yields a different
page — drift that corrupts the label itself, which is strictly worse than parser-blindness.
The tap already stores exactly the bytes the gate saw, at the moment it saw them.

## Two strata, because one sample cannot do both jobs

A **random stratum**, weighted back to the live stream, gives honest composition and
call-rate numbers. A **Boundary Stratum** — every page that candidate gate variants disagree
on — gives the false-drop guard its power; a uniform sample of the live stream (94.5%
non-postings) cannot populate the decision boundary densely enough to detect a small drop
rate. The sampling weight lives on each row, so one file yields both a guard verdict and
stream-honest metrics.

Labels stay LLM-proposed and human-confirmed (ADR-0008), with the review set redefined:
confirmation is **required on the Boundary Stratum**, which decides whether the guard goes
red and is exactly where a labeler is most likely to be wrong; the random stratum is
spot-checked. Each row records who proposed and who confirmed it.

## Consequences

- Parser changes no longer flow into extract-gate verdicts — the accepted cost of freezing
  Content. The compensating measurement is live rather than benched: Shadow Extraction
  (ADR-0044) scores the gate continuously against the current parser on the real stream.
- The captured stream is capped per verdict, so **the caps are the sampling design**. Weights
  derive from them, never from the file's own accept/abstain mix, which the caps distort by
  roughly an order of magnitude.
- The existing synthetic fixtures survive as cheap regression cases for the reject rungs, but
  are renamed out of "Gold Set" so they are never again mistaken for evidence.
- The Boundary Stratum is a **census taken under one candidate rule**, so acting on what it
  measures necessarily stops it marking a boundary. It then becomes a historical disagreement
  set plus a recovery ledger — which is the more useful artefact, and the one #257 kept.
  Re-draw only when a *new* boundary is actually needed, and budget the human confirmations
  the fresh rows will owe; re-drawing to keep a provenance test green throws away the recovered
  postings, which are the evidence the rule change produced.
