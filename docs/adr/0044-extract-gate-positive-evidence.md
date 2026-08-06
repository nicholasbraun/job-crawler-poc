# The Extract Gate requires tiered Positive Evidence, superseding reject-only

ADR-0019 built the Extract Gate as reject-only and dropped #111's positive-ACCEPT rung for a
specific reason: the keyword relevance filter already stood in front of the extractor, so
"every page reaching the extractor has already been accepted — a structural ACCEPT is a
no-op." ADR-0038 retired that filter. The Collection Crawl runs a pass-all chain, the gate's
final rung is a blanket accept, and the extractor became a paid yes/no "is there a job here?"
detector on nearly every walked page — ~363k calls a day at ~5.5% yield. The premise expired,
so the decision it justified is superseded: a page that clears every reject rung is now
extracted only on **Positive Evidence** that it is one posting.

The evidence is **tiered, not OR'd**. A posting-shaped URL or a lone structured-data
`JobPosting` admits alone; the text marks — an apply affordance, dense posting vocabulary —
admit only in agreement with each other. Replaying variants over 2199 live pages that clear
the reject rungs: an OR of all four keeps 88.5% of the pages the extractor accepts at ~50% of
today's call volume, while the tiered form with URL matching extended to compound segments
and job-word hosts (`karriere.x.de/<slug>`, `/jobs-karriere/<slug>`,
`/stellenangebote_unternehmen/<firm>/<slug>`) keeps **90.6% at ~18%** — better recall at a
third of the cost. An apply affordance alone fires on 33% of non-postings and is the entire
remaining bill in the OR form.

The posting-URL predicate for this rung is **new code, never a widening of the shared
posting-path predicate**. That predicate also feeds the Discovery Gate's career-page veto and
the Catalog Doctor's removal planner, so widening it would retroactively delete Catalog rows
and shift job-link saturation counting — the config-coupling hazard ADR-0019 named. Two
notions of "posting URL" in one package is the price of that isolation.

## The guard, and the objective that was rejected

A false-drop stays a **hard zero** on the labeled set (ADR-0020's contract, unchanged),
because it is permanent: no listing means `SeedVisited` never seeds the page, so the walk
re-reaches and re-drops it every Cycle, forever, and nothing in production moves. A false
*accept*, by contrast, self-heals through the refetch lane.

"Keeps the listings we save today" was considered as the objective and **rejected**: roughly
43% of what the pipeline saves today is not a single posting, so optimizing to preserve
current output optimizes to preserve the false positives. Recall on true postings is the
guard; precision and cost are reported alongside it and neither is a threshold.

## Consequences

- **1% of gate skips are Shadow Extractions** — extracted anyway purely to score the gate,
  for roughly eight cents a day. The extractor's verdict on a rejected page is the live
  false-drop measurement, which is otherwise unobservable. A Shadow Extraction must be
  structurally incapable of reaching the Corpus, not merely tested against it.
- The refetch lane's changed-content re-extract path is gated too. It fed the extractor
  ungated, which was already a hole and becomes the only way junk re-enters the Corpus once
  this rung tightens.
- A counter reports how many Open listings a full content re-gate *would* close, without
  closing them. Actually healing the Corpus is a separate decision, deliberately not taken
  here: it is a bulk destructive operation driven by fresh thresholds, on a lane already
  bitten once by a mass-close (#208).
- The **L2 cheap-confirm deferred by ADR-0019 is closed, not pending.** Its rationale was
  that a binary "is this one posting?" call is materially cheaper than a full extraction;
  once the extractor emits only a verdict and three short fields (ADR-0041), an extract call
  *is* a confirm call. The three-part data gate ADR-0019 set can no longer be met.
