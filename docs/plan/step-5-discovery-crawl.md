# Step 5 — Discovery Crawl kind (perpetual)

**Prerequisites:** Step 4. Read `/CONTEXT.md`, ADR-0001 (ATS-aware Company), ADR-0004
(two-phase crawling).

## Goal

Implement the perpetual **Discovery Crawl** that fills the **Catalog** (Companies + Career
Pages).

## Scope (build this)

- `internal/processor/discovery_processor/` + a Career Page classifier: **hybrid gate** —
  URL/content heuristics + schema.org JSON-LD `JobPosting` parse; LLM confirm **only** for
  gate-passers that lack JSON-LD (bounds LLM cost at perpetual scale).
- **ATS-aware Company identity (ADR-0001):** a small ATS-host registry (`greenhouse`,
  `lever`, `ashby`, `recruitee`, `personio`, `workable`, …) extracts the tenant slug; fall
  back to eTLD+1 via `golang.org/x/net/publicsuffix`. Keep **Politeness Domain** (the host)
  separate — the frontier already keys cooldown on it. Persist `company` / `career_page`
  (one main Career Page per Company; `last_seen` for freshness).
- The Discovery Crawl Definition uses the generalized URL-filter config (today's
  `config.json` lists → `crawl_definition.url_filter` jsonb) and the frontier's
  **perpetual mode** (never `ErrDone`; ends only via desired-state stop).
- Seeds: ATS aggregators (`boards.greenhouse.io`, `jobs.lever.co`, `jobs.ashbyhq.com`,
  `jobs.personio.de`, …) + EU directories + the existing YC list from `config.json`.

## Interface changes

None.

## Out of scope

Keyword extraction and Job Listings (Step 6).

## Verify

Run a small Discovery Crawl against a seeded ATS board → `company` / `career_page` rows
appear with the correct **ATS-aware `company_key`** (distinct Greenhouse tenants are *not*
collapsed into one company), while their **Politeness Domain** is shared. The perpetual run
keeps running until stopped via desired-state.
