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

## Amendment: the evidence set, widened to the measured false-drops (#257)

The measurement this ADR demanded was taken. The Boundary Stratum (ADR-0043) recorded **37**
real Job Listings the tiered rule above dropped; a blind re-read had already settled the
labels. Those 37 pages, read one by one, are what the evidence set below was widened to. The
tier *shape* is unchanged and stays unchanged — strong marks admit alone, weak marks only in
agreement. What changed is which marks exist.

| | #258's rule | today |
|---|---:|---:|
| boundary-stratum false-drops | 37 | **11** |
| stream-weighted extract-call-rate | 0.1274 | **0.1390** |
| stream-weighted precision | 0.4431 | 0.4063 |
| stream-weighted recall | 0.9677 | 0.9677 |
| gold-set detail recall | 0.7143 (100/140) | **0.9071** (127/140) |
| gold-set leaks (hub-index + residue) | 12 + 38 | 14 + 39 |

Twenty-seven of the 38 in-scope false-drops recovered for +9% of today's extract calls — a
7.85× saving becomes 7.19× — and three extra leaks. Four of the five widenings cost **zero**
measured stream calls.

The stream-weighted **recall is saturated at 0.9677 under every rule, including the blanket
accept**: one random-stratum posting is dropped by a *reject* rung, and no widening of this
rung can move it. The boundary false-drop count is the only responsive guard here; the stream
recall is evidence of nothing about this rung.

The widenings, each with what it recovered:

- **`role`/`roles` and the German apprenticeship family** (`ausbildung`, `ausbildungen`,
  `ausbildungsberuf(e)`, `ausbildungsplatz/-plaetze`) as path job words: **+12 postings, 0
  leaks, 0 stream cost.** "role" is the same kind of job word as "position"; an Ausbildung
  listing is a Job Listing. Both only fire on a non-terminal segment, so the indexes still do
  not admit.
- **A role slug in the FINAL segment**, restricted to SINGULAR posting nouns
  (`stellenangebot`, `stellenanzeige`, `stellenausschreibung`, `jobangebot`, `vacancy`,
  `ausbildung`) in a segment of at least four tokens: **+2, 0 leaks, 0 stream cost.** The
  token floor is a judgement, not a fit — 3, 4 and 5 score identically on the gold set — taken
  on the shape of the German section slugs it must exclude.
- **A Title announcing one vacancy** — "hiring a "/"hiring an " (trailing space load-bearing)
  or `gesucht` as a whole word: **+9, +1 leak, 0 stream cost.** STRONG, not paired: one of the
  pages it recovers carries no posting section in its body at all.
- **A role designation in the Title** (`(m/w/d)` and friends) as a second corroborator for
  dense posting vocabulary: **+2, 0 leaks, 0 stream cost.**
- **Four of the five vocabulary groups as a strong mark**: **+2, +2 leaks, +0.0116 stream call
  rate.** This is the ONLY item that costs stream calls. Backing it out is a one-line change
  and lands at 13 boundary false-drops with the call rate unmoved at 0.1274; it is kept
  deliberately, because the guard is hard-zero false-drops and ADR-0020 keeps cost descriptive
  with no threshold. Recorded here so the choice is visible rather than silent.

**The open question is answered: a high vocabulary-group count does stand alone, at four of
five — and not at three.** Three re-admits `massinc.org/about-us/career-opportunities` and
`ifes.uni-hannover.de/eev/hiwi`, a standing call and a rail of postings that the blind re-read
had *just* taken out of `detail`. A threshold that re-admits the rows a human has just
rejected is moving the wrong way. Five recovers nothing.

### The stopping rule, and what was rejected

The Boundary Stratum holds 37 `detail` against 151 non-detail, ≈**1:4**. A mark that admits
postings at 1:3 or worse is barely better than not having the rung, and carries no evidence.

| candidate | postings | leaks | stream call rate | verdict |
|---|---:|---:|---:|---|
| **host substring** (`jobicco`, `jobben`, `unistellenmarkt`, `wynncareers` as job hosts) | +8 | **+40** | 0.1274 → **0.1737** | reject. On a job-*board* host every page reads as a posting: company profiles, search facets, salary pages, employer résumé searches, and `Gesuch/…` pages — postings sought *by job seekers*. |
| final-segment job word over the full word list, ≥3 tokens | +2 | +9 | +0.0116 | reject. Reads `careers-at-sedus`, `career-open-positions`, `offene-stellen-schulamt-neuruppin` as postings. The singular restriction is the predicate. |
| vocabulary ≥3 as a strong mark | +3 | +8 | 0.1390 → 0.1621 | reject (above) |
| role designation in the Title as STRONG | +6 | +4 | 0.1274 | reject. Admits a posting header above a rail of other postings, and a title with no role body. |
| enriched vocabulary phrase lists (~30 more section headings) | +1 | +5 | 0.1390 → **0.1737** | reject. Extra phrases inflate the group count on HUBS — which carry many postings' headings — at least as fast as on postings. The mark is saturated. |
| body vacancy phrases ("is looking for a", "we are seeking") | +2 | +4 | 0.1390 | reject, 1:2 |
| body "zu besetzen" / "zu vergeben" | +2 | +6 | 0.1390 | reject, 1:3 |
| body "Stellenbeschreibung" / "job description" heading | +2 | +10 | 0.1390 | reject, 1:5 — worse than no rung |
| gender designation in the URL slug | +1 | +3 | 0.1390 | reject, 1:3 |
| dated editorial path (`/2026/07/28/<slug>`) | +2 | +3 | 0.1390 → 0.1621 | reject. A dated path is evidence of a blog post. |
| `praktikum` / `praktika` as path words | 0 | +2 | 0.1274 | reject, no evidence |
| "wir suchen" / "we are looking for a" in the Title | 0 | 0 | 0.1274 | reject, no evidence |

### The Boundary Stratum was NOT re-drawn

It was drawn as the pages the #258 rule skipped, so widening that rule means it no longer
marks today's boundary. Re-drawing it from the capture — which its own README instructed —
would **delete the 26 recovered postings from the record**, which is exactly the evidence this
work produced, and would owe 188 fresh human confirmations nobody has budgeted. The stratum is
kept as a HISTORICAL disagreement set plus a **recovery ledger** (29 rows recovered: 26
detail, 2 hub-index, 1 ambiguous), pinned per label so a later widening that recovers
non-postings faster than postings goes red. The half of its provenance test that is still a
live guard — every row is extracted by the blanket accept, i.e. no *reject* rung moved — is
untouched.

### Hard zero is not reached, and why

Eleven pages remain, stated rather than closed by a threshold nothing measured:

- **`observermedia.com` ×3** — a bare role slug on a company domain, prose with almost no
  section headings, JSON-LD that is `WebSite`/`Organization` only, a title that is just the
  role name. Nothing in URL, structure or vocabulary separates them from an "about our team"
  page.
- **`abc7.com/12393058`, `theygsgroup.com/2026/07/28/<slug>`, `ecomat-bremen.de/<role-slug>`,
  `biochemie.med.fau.de` ×2** — these need the vocabulary threshold at 2–3 as a strong mark,
  which re-admits rows a human blind read already rejected.
- **`duckwallfruit.com` (Spanish) and `liepin.com` (Chinese)** — every text mark here is
  English/German. A Spanish or Chinese phrase set cannot be validated on a handful of rows, so
  it is a follow-up, not a guess. Separately, `liepin.com` is a large Chinese job board and
  probably belongs on the Aggregator denylist — a different mechanism.
- **`jobicco.tu-braunschweig.de/de/1763`** — an 846-character body whose only posting marker
  is the heading "Beschreibung des Jobs"; reading its host as a job host needs the substring
  test rejected above.

Two further false-drops in the gold set (`hiring.cafe/job/…`, `jobs.blooloop.com/jobs/…`) are
**not this rung's**: a reject rung drops both before Positive Evidence is consulted, and they
are dropped identically under the blanket accept.

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
