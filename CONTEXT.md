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
