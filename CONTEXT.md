# Job Crawler

A long-running service that discovers company career pages across the web and extracts job
listings from them by keyword. The domain has two phases: a broad **Discovery Crawl** that
builds a Catalog of hiring companies, and targeted **Keyword Crawls** that extract matching
job listings from that Catalog.

## Language

### Crawls

**Crawl Definition**:
A durable, re-runnable configuration for a crawl, of a *kind* — Discovery or Keyword. Immutable once created, except that Seeds may be appended to the Discovery Definition.
_Avoid_: crawl (bare), job, config

**Crawl Run**:
One execution of a Crawl Definition, with a status and live counters.
_Avoid_: crawl (bare), session, task

**Discovery Crawl**:
The single, perpetual Crawl Definition of kind Discovery: bounded-broad, it finds Career Pages and attributes them to Companies, filling the Catalog. Exactly one exists, and it runs continuously until a human Stops it.
_Avoid_: spider, broad crawl

**Keyword Crawl**:
A Crawl Definition kind: seeded from the Catalog, it extracts Job Listings matching a set of OR-matched keywords.
_Avoid_: search, filter crawl

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

**Politeness Domain**:
The host the Frontier rate-limits against (e.g. `boards.greenhouse.io`). Deliberately distinct from Company — many Companies can share one Politeness Domain.
_Avoid_: domain, host (unqualified)

**Career Page**:
The index page that lists a Company's open jobs; may be paginated.
_Avoid_: jobs page, listings page

**Job Listing**:
A single job posting, extracted from under a Career Page.
_Avoid_: job, posting, vacancy, ad

**Catalog**:
The durable collection of Companies and Career Pages, filled by Discovery and by Catalog Imports, consumed by Keyword Crawls.
_Avoid_: index, database

**Sighting**:
A crawl's live observation of a Company or Career Page, refreshing its fields and advancing its last-seen time. Only crawls sight — a Catalog Import is not a Sighting.
_Avoid_: visit, touch, refresh

**Aggregator**:
A multi-company job board, VC-portfolio board, or professional network — never a single Company's Career Page. Rejected at the Gate by host: it is structurally indistinguishable from a legitimate multi-tenant ATS (both serve many companies under `/{slug}` paths), so only a curated host list can tell them apart.
_Avoid_: job board, directory, portal

**Catalog Doctor**:
An idempotent maintenance pass that replays the current URL-structural rules over the already-stored Catalog, hard-deleting or re-attributing rows the rules now reject. It corrects only URL-decidable errors — the Catalog stores no page content, so it cannot re-judge a page the way the Gate and LLM first did.
_Avoid_: cleanup, migration, backfill

**Catalog History**:
The Catalog's growth over time, reconstructed from when each surviving entry was first catalogued rather than recorded as it happened. Because it derives from today's Catalog, it is *revisionist*: entries the Catalog Doctor later removes vanish from the entire history, so it depicts how the current Catalog grew — not a ledger of the Catalog's past sizes.
_Avoid_: catalog snapshot, growth log, time series

**Website**:
A Company's declared homepage. Known only when imported — Discovery does not learn it — and the Keyword Crawl's seed of last resort for a Pageless Company.
_Avoid_: homepage, url, company domain

**Pageless Company**:
A catalogued Company with no Career Page yet: employer known, page undiscovered.
_Avoid_: prospect, stub, empty company

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
The set of URLs a Crawl Run still has to fetch, scheduled per Politeness Domain with a cooldown.
_Avoid_: queue, backlog

**Seed**:
A crawl's starting URLs. For a Discovery Crawl they are configured, and may also be added while it runs — appended to the Definition and injected into the live Frontier; for a Keyword Crawl they are resolved from the Catalog at run start — every Career Page, plus each Pageless Company's Website.
_Avoid_: entry point, root URL

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
A Company's own page that renders a third-party ATS board inline, via an iframe or a provider script. Structurally a Career Page even though its host is the Company's domain rather than the ATS, so identity still attributes it to the Company by that domain.
_Avoid_: iframe, widget, integration

**Terminal-Hub Word**:
The last path segment of a deep career URL that keeps it a Career Page rather than a Job Listing — an openings-index token (`open-positions`, `opportunities`, `vacancies`) as opposed to a role slug. It is what separates `/careers/open-positions` (a hub) from `/careers/senior-engineer` (a single posting) when the Gate would otherwise reject both as postings.
_Avoid_: listing keyword, hub keyword

**Extract Gate**:
The Keyword Crawl's counterpart to the Gate: a deterministic, pre-LLM pass that decides whether a keyword-relevant page reaches the LLM extractor. It *rejects* the page shapes the Gate accepts as hubs — an ATS Embed, a structured-data openings index, a link-saturated page — reading the same Structural Signals with opposite polarity. Verdict is binary (extract or skip), not the Gate's three-way band, and it is tuned separately so its calibration never shifts the Gate.
_Avoid_: extract filter, relevance gate, ShouldExtract (in prose)

**Extractor Abstain**:
The LLM extractor's self-report that a page it was handed is not a single Job Listing — a hub, index, or career-landing page — so the extraction is discarded rather than saved. The extract path's last-resort net for a non-posting the Extract Gate let through.
_Avoid_: skip, empty extraction, reject

**Empty-Extraction Rate**:
The share of extractor calls that end in an Extractor Abstain (`abstain / sent`) — the live measure of wasted extract calls the Extract Gate exists to drive down.
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
The Extract Gate's counterpart to the Gold Set: keyword-relevant real pages, each labelled single-posting **detail**, **hub/index**, or structurally-silent **residue**, scored on the binary extract-or-skip decision (a false-drop is the hard failure). Distinct from the Gold Set, which labels Career-Page-vs-not over a discovery sample.
_Avoid_: extract test set, second gold set
