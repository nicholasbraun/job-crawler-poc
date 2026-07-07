# Step 6 — Keyword Crawl kind (bounded)

**Prerequisites:** Step 5 (needs a populated Catalog). Read `/CONTEXT.md`, ADR-0004, and
`cmd/cli/main.go:110-126` (the current hard-coded relevance keywords).

## Goal

Implement **Keyword Crawls** that seed from the Catalog and extract keyword-matching **Job
Listings**.

## Scope (build this)

- **Seed resolution:** at run start, resolve the Run's Seeds from the Catalog (its Career
  Pages), optionally filtered by chosen Companies.
- Narrow depth (to follow pagination + reach individual listing pages). A **multi-keyword OR
  filter** built from `crawl_definition.keywords`, applied as the relevance filter — it
  prunes pages **before** the expensive LLM extraction. Reuse `job_listing_processor` (the
  OpenRouter extractor).
- **Move the hard-coded relevance keywords out of `cmd/cli/main.go`** into
  `crawl_definition`; build the keyword `filter.CheckFn` from the Definition.
- Frontier **bounded mode** (`ErrDone` = natural completion → run status `completed`).

## Interface changes

None.

## Out of scope

Dashboard polish (Step 7).

## Verify

Create a Keyword Crawl `{golang, backend}` over the Catalog → `job_listing` rows with
correct `tech_stack`; keyword pruning is visible as reduced LLM calls vs. extracting every
page. The run reaches `ErrDone` and status goes `completed`.
