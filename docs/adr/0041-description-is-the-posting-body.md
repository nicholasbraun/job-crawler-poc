# A Job Listing's description is the Posting Body, never model-authored

ADR-0023 kept a Job Listing's description as "the board API's own text for an ATS Fetch,
and the extractor's *summary* for a crawled page." That asymmetry became a defect when
ADR-0038 made the Corpus and its SavedSearches the product: description is weight B in the
search index (migration 0025), so a SavedSearch matching a term that appears only in a
posting's body hits every ATS-fetched listing and misses every LLM-extracted one — a
systematic search bias correlated with whether a site happens to publish a board API, which
no user can reason about. The description therefore becomes the **Posting Body** on every
lane: the posting's own text, taken from the most structured source available — a lone
JSON-LD `JobPosting`'s description, else the page's main content, else the board API's text
— and the extractor prompt drops the field entirely.

## Considered options

- **Ask the model for the full body.** An extract call sends ~2000 input tokens and returns
  ~120; a verbatim body makes output ≈ input, a ~16× increase in generated tokens on the
  pricier half of the bill — roughly 3–4× today's extract cost, more than the Extract Gate
  (ADR-0044) saves. It also buys paraphrase and truncation risk, per-call latency for
  thousands of serially-generated tokens, and an unbounded response (no `max_tokens` is
  set). Rejected: transcription is not a judgment task, and the page text is already in
  hand. Dropping the field shrinks LLM output to ~30 tokens, making the crawl path *cheaper*
  than before this decision.
- **Keep the summary and add a separate indexed body column.** Preserves a readable
  dashboard card, but leaves two description-shaped fields whose meanings diverge per lane
  and doubles the row. Rejected: truncation is a display concern, and a genuine summary — if
  ever wanted — is a cheap batch job over a stored body, not a per-page cost on every one of
  ~363k pages a day.

## Consequences

- Every listing records a **Description Source**. A row still marked model-authored is a
  legacy summary, and the refetch lane heals it from the parsed page on the unchanged-hash
  branch — no LLM call, one write per listing. The walk cannot heal them: `SeedVisited`
  (ADR-0035) seeds every Open listing past the walk, so refetch is the only path that ever
  revisits a known posting.
- The body cap is **its own knob** (`DESCRIPTION_MAX_CHARS`, default 16000), deliberately
  not `LLM_EXTRACT_MAX_CHARS` — that dial tunes prompt latency for local models and must
  never decide what the Corpus stores. Real postings run to ~12k at p99; the cap is a
  row-size guard against a pathological `<body>`-fallback parse (59k chars observed live).
- A body taken from main content carries page chrome where a page has no semantic container
  — about 40% more text than the clean structured-data body. Accepted: the fix is a better
  main-content extractor, which improves the model's input too, not a more expensive prompt.
- `description` and `source_hash` are both pure functions of the fetched page, so the save
  processor stamps them alongside the other save-time fields and the extractor port returns
  judgment only. The source-hash derivation moves to a neutral package so the crawl-lane
  refetch cache key (ADR-0035) has exactly one definition rather than an invariant held by
  a comment.
