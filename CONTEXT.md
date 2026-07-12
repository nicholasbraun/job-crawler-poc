# Job Crawler

A long-running service that discovers company career pages across the web and extracts job
listings from them by keyword. The domain has two phases: a broad **Discovery Crawl** that
builds a Catalog of hiring companies, and targeted **Keyword Crawls** that extract matching
job listings from that Catalog.

## Language

### Crawls

**Crawl Definition**:
A durable, editable, re-runnable configuration for a crawl. Has a *kind* — Discovery or Keyword.
_Avoid_: crawl (bare), job, config

**Crawl Run**:
One execution of a Crawl Definition, with a status and live counters.
_Avoid_: crawl (bare), session, task

**Discovery Crawl**:
A Crawl Definition kind: perpetual and bounded-broad, it finds Career Pages and attributes them to Companies, filling the Catalog.
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
An organization that hires; owns one Career Page. Identity is ATS-aware — the tenant slug on a known ATS host, otherwise the registrable domain (eTLD+1).
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
The durable collection of Companies and Career Pages that Discovery fills and Keyword Crawls consume.
_Avoid_: index, database

**Aggregator**:
A multi-company job board, VC-portfolio board, or professional network — never a single Company's Career Page. Rejected at the Gate by host: it is structurally indistinguishable from a legitimate multi-tenant ATS (both serve many companies under `/{slug}` paths), so only a curated host list can tell them apart.
_Avoid_: job board, directory, portal

**Catalog Doctor**:
An idempotent maintenance pass that replays the current URL-structural rules over the already-stored Catalog, hard-deleting or re-attributing rows the rules now reject. It corrects only URL-decidable errors — the Catalog stores no page content, so it cannot re-judge a page the way the Gate and LLM first did.
_Avoid_: cleanup, migration, backfill

### Crawl mechanics

**Frontier**:
The set of URLs a Crawl Run still has to fetch, scheduled per Politeness Domain with a cooldown.
_Avoid_: queue, backlog

**Seed**:
A Run's starting URLs. Configured for a Discovery Crawl; resolved from the Catalog (its Career Pages) at run start for a Keyword Crawl.
_Avoid_: entry point, root URL

### Classification

**Gate**:
The deterministic, pre-LLM pass over a candidate page that returns one of three verdicts — reject, certain-accept, or uncertain — so only the ambiguous middle costs an LLM call.
_Avoid_: filter, pre-check, heuristic

**Certain / Uncertain**:
The Gate's confidence in an accept. A *certain* accept is structurally definitive (an ATS board root, a career-path URL) and is catalogued with no LLM call; an *uncertain* accept is forwarded to the LLM to confirm.
_Avoid_: sure/maybe, confident, definite

**Terminal-Hub Word**:
The last path segment of a deep career URL that keeps it a Career Page rather than a Job Listing — an openings-index token (`open-positions`, `opportunities`, `vacancies`) as opposed to a role slug. It is what separates `/careers/open-positions` (a hub) from `/careers/senior-engineer` (a single posting) when the Gate would otherwise reject both as postings.
_Avoid_: listing keyword, hub keyword

**Leak**:
A real Career Page the Gate rejects. Irrecoverable — the LLM never gets to save it — so it is a hard failure the benchmark targets at zero.
_Avoid_: false negative, miss

**False-Certain**:
A non–Career-Page the Gate certain-accepts. Irrecoverable — catalogued with no LLM veto — so it is a hard failure the benchmark targets at zero.
_Avoid_: false positive

### Benchmark

**Gold Set**:
The curated collection of real HTML pages, each stored with its true URL, a human-owned ground-truth label (Career Page or not), and a category, that the classifier benchmark scores against.
_Avoid_: test set, fixtures (bare), corpus, sample
