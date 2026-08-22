# Job Crawler

A long-running service that discovers company career pages across the web and continuously
collects their job listings into a queryable corpus. The domain has three parts: a broad
**Discovery Crawl** that builds a Catalog of hiring companies, a perpetual **Collection Crawl**
that harvests every open listing from that Catalog into the **Corpus**, and **SavedSearches**
that query the Corpus by keyword, country, and work arrangement.

## Language

### Crawls

**Crawl Definition**:
A durable, re-runnable configuration for a crawl, of a *kind* — Discovery or Collection. Immutable once created, except that Seeds may be appended to the Discovery Definition.
_Avoid_: crawl (bare), job, config

**Crawl Run**:
One execution of a Crawl Definition, with a status and live counters.
_Avoid_: crawl (bare), session, task

**Discovery Crawl**:
The single, perpetual Crawl Definition of kind Discovery: bounded-broad, it finds Career Pages and attributes them to Companies, filling the Catalog. Exactly one exists, and it runs continuously until a human Stops it.
_Avoid_: spider, broad crawl

**Collection Crawl**:
The single, perpetual Crawl Definition of kind Collection: seeded from the whole Catalog, each Cycle it harvests every open Job Listing from every Career Page into the Corpus — keyword and country no longer prune at collect time, they filter at query time. Each seed stays confined to its Company by Scope. It acquires listings two ways: an ATS Fetch for a Company on a recognized ATS, otherwise by crawling and extracting posting pages.
_Avoid_: keyword crawl, harvest crawl, scrape

**Collection Cycle**:
One Crawl Run of the Collection Crawl — a single sweep over the whole Catalog, started on a cadence. Cycles never overlap: a new one is skipped while the last still runs.
_Avoid_: pass, sweep (bare), batch

### Run lifecycle

**Pause / Paused**:
A human deliberately parks a running Crawl Run: it stops fetching, keeps its Frontier intact, and stays parked — across restarts — until a human Resumes it. Distinct from the automatic halt of a server restart (which resumes on its own) and from a Stopped run (which is terminal and not resumable).
_Avoid_: stop, suspend, halt, freeze

**Pausing**:
The transient state of a Crawl Run that has been asked to Pause and is draining its in-flight work before it parks as Paused.
_Avoid_: stopping, halting

**Resume**:
Relaunch a Paused Crawl Run from its preserved Frontier and counters, continuing where it left off.
_Avoid_: restart, unpause, continue

### Catalog

**Company**:
An organization that hires; owns any number of Career Pages — possibly none — and may declare a Website. Identity is ATS-aware — the tenant slug on a known ATS host, otherwise the registrable domain (eTLD+1).
_Avoid_: org, employer, site, domain

**Name Ladder**:
The precedence deciding a Company's display name from a discovered page, higher-trust first: a name the page declares about itself (in structured data, then site metadata) outranks a name the LLM reads from the page, which outranks a name parsed from the page title, which outranks the Company's bare identity — its tenant slug or registrable domain. A title parse counts only when it carries a real structural cue; otherwise it abstains to the next rung, so a bare title never becomes a name. The Discovery-side counterpart to the import-time Identity Ladder; every name it yields carries a Name Source.
_Avoid_: title heuristic, name fallback chain

**Name Source**:
The recorded origin of a Company's stored name, and so how far to trust it: a page-declared name is *verified* — the site named itself; a name the LLM reads or the title yields is derived but unverified; the identity fallback means no real name was found. Discovery stamps every name with its Source so the Catalog can be audited for name quality — the dashboard reports the verified share and filters unverified or nameless entries.
_Avoid_: name confidence (bare), label origin

**Politeness Domain**:
The unit of serving infrastructure a lane rate-limits against — deliberately distinct from Company, since many Companies can share one Politeness Domain (every tenant of a multi-tenant ATS or platform). The two lanes approximate it differently: the Frontier walk keys on the full host (e.g. `boards.greenhouse.io`), while the refetch lane keys on the registrable domain (eTLD+1), because politeness is owed to the platform, not the tenant subdomain. That the walk still keys on the full host is a known divergence (ADR-0040).
_Avoid_: domain, host (unqualified)

**Career Page**:
The index page that lists a Company's open jobs; may be paginated. A Collection seed until it goes dormant (see Career-Page Dormancy).
_Avoid_: jobs page, listings page

**Job Listing**:
A single job posting collected under a Career Page and attributed to that Career Page's Company rather than to a name read off the page. It lives in the Corpus keyed by its canonical source URL, carries a Listing Lifecycle (Open or Closed), and is obtained on either Source Lane — an ATS Fetch or a crawled-and-extracted posting page.
_Avoid_: job, posting, vacancy, ad

**Catalog**:
The durable collection of Companies and Career Pages, filled by Discovery and by Catalog Imports, consumed by the Collection Crawl.
_Avoid_: index, database

**Sighting**:
A crawl's live observation of a Company or Career Page, refreshing its fields and advancing its last-seen time. Only crawls sight — a Catalog Import is not a Sighting.
_Avoid_: visit, touch, refresh

**Aggregator**:
A multi-company job board, VC-portfolio board, or professional network — never a single Company's Career Page. Rejected at the Gate by host: it is structurally indistinguishable from a legitimate multi-tenant ATS (both serve many companies under `/{slug}` paths), so only a curated host list can tell them apart. Because the verdict is that list and not a reading of the page, it is the Gate's one *certain* reject — so adding a host retroactively closes the Catalog rows and Job Listings it already minted, via the Collection Crawl's dormancy probe, rather than waiting on the Catalog Doctor.
_Avoid_: job board, directory, portal

**Catalog Doctor**:
An idempotent maintenance pass that replays the current URL-structural rules over the already-stored Catalog, hard-deleting or re-attributing rows the rules now reject. It corrects only URL-decidable *identity* errors — page liveness is the Collection Crawl's concern (see Career-Page Dormancy), not the Doctor's.
_Avoid_: cleanup, migration, backfill

**Catalog History**:
The Catalog's growth over time, reconstructed from when each surviving entry was first catalogued rather than recorded as it happened. Because it derives from today's Catalog, it is *revisionist*: entries the Catalog Doctor later removes vanish from the entire history, so it depicts how the current Catalog grew — not a ledger of the Catalog's past sizes.
_Avoid_: catalog snapshot, growth log, time series

**Website**:
A Company's declared homepage. Known only when imported — Discovery does not learn it — and the Collection Crawl's seed of last resort for a Pageless Company.
_Avoid_: homepage, url, company domain

**Pageless Company**:
A catalogued Company with no Career Page yet: employer known, page undiscovered.
_Avoid_: prospect, stub, empty company

### Corpus & search

**Corpus**:
The global, deduplicated store of collected Job Listings — one row per real-world posting, keyed by its canonical source URL and owned by no single Crawl. Collection Cycles fill and refresh it; SavedSearches query it.
_Avoid_: index, results, listings table

**SavedSearch**:
A named, stored query over the Corpus — keywords, Countries, and Work Arrangement — rendered as a live dashboard panel of the matching Job Listings. Replaces the Keyword Crawl as how a user asks "which jobs match?"; it queries, it never crawls.
_Avoid_: alert, saved query (bare), keyword crawl, filter

**Listing Lifecycle**:
A Job Listing's state in the Corpus: Open while its source still offers it, Closed once the source drops it — soft-deleted, never removed, so history survives — and reopened in place, keeping its original first-seen time, if it returns.
_Avoid_: status, active/inactive, deleted

**Liveness**:
Whether a Job Listing is still Open, decided per Source Lane: an ATS posting by its Absence-from-Board, a crawled posting by re-fetching its page.
_Avoid_: freshness, health, alive check

**Absence-from-Board**:
The ATS liveness signal: a posting the freshly-fetched board no longer lists is Closed. Trusted only when the board was fetched completely — a partial or failed fetch closes nothing.
_Avoid_: gone, missing, delisted

**Source Lane**:
Which of the two paths collected a Job Listing — an ATS Fetch (structured board API, the primary lane) or a Crawl (fetch-and-extract the posting page, the fallback). It decides how the listing's Liveness is judged.
_Avoid_: source type, method, channel

**Posting Body**:
The posting's own text as stored on a Job Listing — the page's structured-data description where there is one, otherwise the page's Flattened Text, and the board API's text on an ATS Fetch. Never model-authored: the LLM judges a page, it does not transcribe it.
_Avoid_: description (bare), summary, body text

**Description Source**:
The recorded origin of a Job Listing's Posting Body — structured data, page content, or the ATS board — and so how clean that text is. A listing still marked model-authored carries a legacy summary, which the refetch lane heals from the page it already fetched.
_Avoid_: provenance (bare), origin, description type

**Career-Page Dormancy**:
A Career Page repeatedly found dead — a 404 board, or a page that no longer classifies — goes dormant and drops out of the Collection seed set, Closing its remaining Open Job Listings. Reversible: re-discovery revives it.
_Avoid_: dead page, disabled, removed

### Location

**Country**:
The ISO 3166-1 alpha-2 code a Job Listing resolves to (e.g. `DE`, `US`) — tagged on each listing at collection and the unit a SavedSearch filters by. Derived from the listing's raw, free-text location string, which is kept unchanged for display; the Country is the structured, filterable value, and is empty when nothing resolves.
_Avoid_: location (for the code), region, geo, locale

**Country Resolver**:
The deterministic mapping from a Job Listing's free-text location to a Country, and the sole authority on the ISO code. A location it cannot place resolves to the empty Country — kept, never dropped.
_Avoid_: geocoder, normalizer, geo lookup

**Work Arrangement**:
A Job Listing's working mode — Remote, Onsite, Hybrid, or Unspecified — extracted per listing and shown as a tag. Unspecified is the honest default: a source that does not positively state the mode never becomes Onsite. Replaces the former remote boolean.
_Avoid_: remote (as a standalone flag), workplace type, remote status

### Import / Export

**Catalog Export**:
A complete snapshot of the Catalog as a single ordered file. Deterministic — the same Catalog always exports byte-identically, so consecutive Exports diff meaningfully.
_Avoid_: dump, backup, download

**Catalog Import**:
The idempotent merge of a catalog file — exported or hand-written — into the Catalog. It can only extend recorded history, never rewrite it, and it is not a Sighting.
_Avoid_: upload, restore, sync

**Import Job**:
One asynchronous execution of a Catalog Import, durable with a status lifecycle; a dry-run Job validates and reports without writing.
_Avoid_: task, batch, upload

**Identity Ladder**:
The precedence deciding an imported record's Company identity: the record's explicit key, else derivation from its Website, else derivation from its Career Page URLs. The file outranks derivation, so content-based attributions survive a round trip.
_Avoid_: fallback chain, resolution order

### Crawl mechanics

**Frontier**:
The set of URLs a Crawl Run still has to fetch, scheduled per Politeness Domain with a cooldown. It also remembers the URLs it has already seen, deduping every addition against that memory.
_Avoid_: queue, backlog

**Re-admission**:
A URL the Frontier had already seen being crawled again because its seen-memory is finite: a perpetual Discovery Crawl caps that memory and forgets the oldest URLs first, so one re-linked afterwards is treated as new. Accepted as the price of a bounded Frontier — politeness and correctness are unaffected; only a repeat LLM classify can result, and only when the re-crawled page also re-passes the Gate.
_Avoid_: re-crawl, re-visit, duplicate

**Seed**:
A crawl's starting URLs. For a Discovery Crawl they are configured, and may also be added while it runs — appended to the Definition and injected into the live Frontier; for a Collection Cycle they are resolved from the Catalog at cycle start — every non-dormant Career Page, plus each Pageless Company's Website.
_Avoid_: entry point, root URL

**Scope**:
The Company-identity boundary a Collection Crawl stays within: a crawl seeded from one Career Page follows links only into that same Company — its own site and subdomains, or its single ATS tenant — never onto unrelated hosts. Derived from the seed's URL so any discovered link can be tested against it. The Discovery Crawl has no Scope; roaming is its job.
_Avoid_: domain limit, allowlist, fence

**Shared-Host Suffix**:
A registrable domain that fronts many independent tenants on distinct subdomains (one newsletter or blog per `{tenant}.substack.com`), where the Scope fence keys on the full hostname rather than the eTLD+1 — so a seed is confined to its own subdomain instead of the whole platform, the way it otherwise would be for a self-hosted company (see Scope, ADR-0021/0039). A curated Public-Suffix-List supplement covering only the multi-tenant hosts the PSL does not already fence; a real company keeps the eTLD+1 default so its sibling subdomains stay in one Scope.
_Avoid_: shared domain, multi-tenant host, platform domain

**ATS Fetch**:
The Collection Crawl's primary acquisition of a Company's Job Listings straight from its ATS provider's board API in one call, rather than by crawling and extracting its posting pages. Available for a Company on — or embedding — a recognized ATS the crawler has an API client for; other ATS boards are crawled as a fallback. A complete Fetch is the sole trusted basis for Absence-from-Board.
_Avoid_: board fetch, API scrape, direct ingest

**Transient Frontier Error**:
A momentary Redis disruption (a blip, failover, or dropped connection) the Frontier rides out by retrying while the Crawl Run's context is live, so the run stays Running rather than Failing. Distinct from a fatal Frontier error — a corrupt or unrecognized Redis reply — which still Fails the run.
_Avoid_: outage, crash

**Structural Rendering**:
A fetched page's main content kept in the form that shows its structure — headings, list items, table rows, link text with its target, and form controls — rather than collapsed into one run of words. What the extractor and a human labeller read, because the structure is frequently what decides whether a page is one Job Listing at all: three role titles in a row are an index, the same three inside an apply form's picker are one posting. A human labeller reads it whole, link text with its target; the model reads a narrowing of it with the targets omitted, because carrying them would cost about a quarter of the prompt window at an unchanged cap.
_Avoid_: markdown (bare), markup, rich text, HTML

**Flattened Text**:
A Structural Rendering with its structure stripped back out — the plain run of words every non-model consumer reads: the Posting Body, the search index, and the Extract Gate's phrase marks. Derived on demand rather than stored beside it, so the two can never disagree about what a page says.
_Avoid_: main content (bare), plain text (bare), normalized text

### Classification

**Gate**:
The deterministic, pre-LLM pass over a candidate page that returns one of three verdicts — reject, certain-accept, or uncertain — so only the ambiguous middle costs an LLM call.
_Avoid_: filter, pre-check, heuristic

**Certain / Uncertain**:
The Gate's Confidence Score band for an accept. A *certain* accept — structurally definitive (an ATS board root, a career-path URL) or carrying Structural Signals that clear the upper threshold — is catalogued with no LLM call; an *uncertain* accept is forwarded to the LLM to confirm.
_Avoid_: sure/maybe, confident, definite

**Confidence Score**:
The Gate's graded measure of how strongly a candidate page reads as a Career Page hub, which its thresholds collapse into the three verdicts — reject, uncertain, or certain-accept.
_Avoid_: gate score, hub score, confidence (bare)

**Structural Signal**:
A content-derived mark that a page is a Career Page hub — an ATS Embed, a structured-data openings index, or a dense set of same-host Job Listing links — weighing more in the Confidence Score than a bare career keyword in the URL or title.
_Avoid_: feature, heuristic, hint

**ATS Embed**:
A Company's own page that renders a third-party ATS board inline, via an iframe or a provider script. Structurally a Career Page even though its host is the Company's domain rather than the ATS, so identity still attributes it to the Company by that domain. In a Collection Crawl it triggers an ATS Fetch of the embedded board, reaching postings the crawler cannot otherwise follow.
_Avoid_: iframe, widget, integration

**Terminal-Hub Word**:
The last path segment of a deep career URL that keeps it a Career Page rather than a Job Listing — an openings-index token (`open-positions`, `opportunities`, `vacancies`) as opposed to a role slug. It is what separates `/careers/open-positions` (a hub) from `/careers/senior-engineer` (a single posting) when the Gate would otherwise reject both as postings.
_Avoid_: listing keyword, hub keyword

**Extract Gate**:
The collection lane's counterpart to the Gate: a deterministic, pre-LLM pass that decides whether a candidate posting page reaches the LLM extractor. It first *rejects* the page shapes the Gate accepts as hubs — an ATS Embed, a structured-data openings index, a link-saturated page — reading the same Structural Signals with opposite polarity, then requires Positive Evidence of a single posting from whatever survives, and finally withholds the call from a page whose Posting Score is too low to be worth it (the Learned Veto). Verdict is binary (extract or skip), not the Gate's three-way band, and it is tuned separately so its calibration never shifts the Gate.
_Avoid_: extract filter, relevance gate, ShouldExtract (in prose)

**Positive Evidence**:
The mark the Extract Gate requires before spending an extractor call: that a page *is* one posting, not merely that it is not a hub. Tiered — a *strong* mark stands on its own (a posting-shaped URL, a lone structured-data posting, a title announcing one vacancy, or four of the five sections a posting has), while the *weak* marks count only in agreement: two posting sections corroborated by an apply affordance or by a role designation in the title. The tiering is the measurement, not a taste — an apply affordance alone fires on a third of non-postings — and which marks sit in which tier is settled page by page against the Extract Gold Set.
_Avoid_: accept signal, positive signal (bare), allowlist

**Posting Score**:
The Extract Gate's learned, graded estimate that a page is one Job Listing. Where Positive Evidence is argued mark by mark, this is fitted to the Extract Gold Set — so it cannot explain a verdict in words, it can only rank one page against another. Distinct from the Gate's Confidence Score, which grades a different question on the discovery path and is hand-weighted.
_Avoid_: confidence score, model score, probability, ML score

**Learned Veto**:
The Extract Gate's refusal to spend an extractor call on a page that carries Positive Evidence but whose Posting Score is too low. It only ever removes a call, never adds one, so all of its risk sits on the false-drop side — which is why a page it drops carries the score that dropped it.
_Avoid_: scorer rung, model gate, ML gate, classifier

**Veto Depth**:
Of the pages the Extract Gate would extract with the Learned Veto off, the share it withholds with the veto on. It needs no labels, which is what lets it be measured over a real capture frame rather than only over the Extract Gold Set; it is the pre-registered condition on turning the rung on. The offline reading and the live dashboard panel share this denominator — every page the rung judged, its vetoes and its survivors alike — and a depth quoted over anything else is a different number. One reading is deliberately narrower and always says so: the trainer's in-sample figure over the Extract Gold Set (50 of **177**, 28.2%) counts only the rung's accepts that carry a **scorable** label, because the zero-`detail`-loss constraint the cut is chosen under can only be read on a labelled row. Over all 180 of that set's accepts the same cut reads 53 of 180, 29.4%. Quote either, never without its denominator.
_Avoid_: veto rate, cut rate, drop rate, savings

**Score Signals**:
What the Posting Score reads: the Structural Signals the Extract Gate already computes, taken as gradations rather than as the thresholded marks Positive Evidence collapses them into, together with the Score Vocabulary. The gradation is the whole point — every page the Learned Veto judges has already cleared Positive Evidence's thresholds, so only the degrees still separate them.
_Avoid_: features, inputs, variables

**Score Vocabulary**:
The words the Posting Score weighs, learned from the Extract Gold Set rather than curated like the Extract Gate's other word lists. It holds no host or URL words, so the score cannot mistake the handful of sites the Gold Set happens to sample for evidence about the web. Being learned rather than argued, it is reviewed as data: a reader checks that it weighs the words a posting uses, not the words one sampled employer uses.
_Avoid_: word bag, feature set, dictionary, stopwords

**Free Extraction**:
A Job Listing read straight from a page's lone structured-data posting, with no LLM call. It fires only where the structured data is unambiguous — exactly one posting node, no openings index, a title — **and the page still renders what it declares**. That last condition exists because a withdrawn or filled posting is routinely served with its structured data intact above a body reading only "this position is no longer active": unambiguous structured data alone proved to be a claim a page can keep making after it stops being true.
_Avoid_: bypass, JSON-LD extract, shortcut

**Withdrawal Notice**:
A posting page whose structured data still declares a Job Listing the page has stopped offering. Refused by the Free Extraction — which would otherwise save a job that no longer exists, with no model in the loop and no way for the refetch lane to Close it, since the page keeps serving that same body unchanged. Sites produce it two ways: the body replaced by a closure message, leaving the page declaring far more than it renders; or the whole posting left in place under a closure banner, where only the wording says it is gone. `validThrough` marks neither — these pages leave it absent.
_Avoid_: expired posting, dead listing, stale ad

**Extractor Abstain**:
The LLM extractor's self-report that a page it was handed is not a single Job Listing — a hub, index, or career-landing page — so the extraction is discarded rather than saved. The extract path's last-resort net for a non-posting the Extract Gate let through.
_Avoid_: skip, empty extraction, reject

**Empty-Extraction Rate**:
The share of extractor calls that end in an Extractor Abstain (`abstain / sent`) — the live measure of wasted extract calls the Extract Gate exists to drive down. It counts model calls only, so a Free Extraction never enters it: it is a measure of wasted spend, not of Corpus precision.
_Avoid_: waste rate, abstain rate (bare), miss rate

**Leak**:
A real Career Page the Gate rejects. Irrecoverable — the LLM never gets to save it — so it is a hard failure the benchmark targets at zero.
_Avoid_: false negative, miss

**False-Certain**:
A non–Career-Page the Gate certain-accepts. Irrecoverable — catalogued with no LLM veto — so it is a hard failure the benchmark targets at zero.
_Avoid_: false positive

**false-drop**:
A real single Job Listing the Extract Gate rejects. The extract-path analog of a Leak — irrecoverable, since the page is never extracted — so the Extract Gold Set targets it at zero.
_Avoid_: false negative, miss, dropped posting

### Benchmark

**Gold Set**:
The curated collection of real HTML pages, each stored with its true URL, a human-owned ground-truth label (Career Page or not), and a category, that the classifier benchmark scores against.
_Avoid_: test set, fixtures (bare), corpus, sample

**Extract Gold Set**:
The Extract Gate's counterpart to the Gold Set: real pages the live extract stream was sampled from, each labelled single-posting **detail**, **hub/index**, or structurally-silent **residue**, scored on the binary extract-or-skip decision (a false-drop is the hard failure). Stored as the parsed page content the pipeline itself produced rather than as re-fetchable HTML, since a page re-fetched later is a different page — a later fetch can only ever be an aid to reading a row, admitted under Capture Fidelity, never the thing a label describes. Distinct from the Gold Set, which labels Career-Page-vs-not over a discovery sample.
_Avoid_: extract test set, second gold set

**Boundary Stratum**:
The part of the Extract Gold Set drawn from pages where candidate gate variants disagree, rather than sampled from the stream — where a false-drop hides, and the only labels that must be human-confirmed. Its counterpart random stratum carries sampling weights instead, so stream-level numbers stay honest. The set may hold more than one, each defined by the pair of gate rules whose disagreement drew it and named after that pair: rows drawn under different rules describe different boundaries, so pooling them would make each unreadable, and a row's stratum is how it says which pair drew it.
_Avoid_: hard cases, edge sample, review queue

**Random Stratum**:
The part of the Extract Gold Set drawn at random from the captured extract stream rather than from where gate variants disagree. Each row carries the sampling weight that maps it back to the live stream, so composition, precision and extract-call rate computed over it describe production rather than the sample's own mix. Because the capture tap sits after the Extract Gate, it describes the stream of pages the crawler already pays an extract call on. Its labels are spot-checked, where the Boundary Stratum's must be human-confirmed.
_Avoid_: uniform sample, control set, holdout

**Shadow Extraction**:
A sampled page the Extract Gate rejected that is extracted anyway, purely to score the gate — the extractor's verdict on it is the live false-drop measurement, which nothing else can observe. It saves nothing by construction: a Shadow Extraction can never reach the Corpus.
_Avoid_: dry run, canary, A/B test

**Capture Fidelity**:
How much of an Extract Gold Set row's captured text a fresh fetch of its URL still carries — *same*, *drifted*, or *gone*. It decides whether a live re-fetch may be shown to a labeller as a view of that row: the captured content is always the authority, and a re-fetch is admissible only while fidelity says the page is still the one that was captured. A *gone* page is the dangerous case, not the useless one — a closed posting argues confidently for the wrong label.
_Avoid_: freshness, staleness, drift (bare)

**Proposed Label**:
The label a proposer put on an Extract Gold Set row, kept on the record even after a human overrides it. It is what makes a confirmation pass measurable: how often an independent human and the proposer reach the same answer is the evidence for what the still-unconfirmed rows are worth.
_Avoid_: original label, suggestion, prior, model label

**Blind Confirmation**:
A confirmation given before the labeller is shown the Proposed Label. The gold set's review surfaces already withhold structured data and the live verdict so a label cannot be read off the signals the set exists to test; the proposed label is the strongest anchor of all, since it is a direct answer to the question being asked. Only a blind confirmation makes agreement with the proposer evidence rather than an echo.
_Avoid_: double-blind, independent review, unbiased labelling
