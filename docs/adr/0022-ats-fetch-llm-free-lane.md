# ATS Job Listings are ingested via provider board APIs in an LLM-free lane

## Context

A Company on a recognized ATS exposes its whole board through a provider API (e.g. `boards-api.greenhouse.io/v1/boards/{tenant}/jobs`). Crawling those postings page-by-page is wasteful, and — critically — a board embedded on a Company's own page via an iframe or script is invisible to the crawler, because embeds are deliberately kept out of the crawled link set. The board API is the only way to reach an embedded board's postings at all.

## Decision

For a Company on — or embedding — a recognized ATS the crawler has an API client for, a Keyword Crawl performs an ATS Fetch: one API call returns the tenant's postings, which are keyword-filtered, mapped to Job Listings, and upserted. The board API supplies every field except a free-text description, which is stored as-is (ADR-0023), so the ATS Fetch makes no LLM call at all. Per-provider JSON differences live in per-provider mappers — a family sharing only the Job Listing output shape, not one Go interface. An ATS with no API client falls back to the ordinary crawl-and-extract path.

## Consequences

The primary win is recall (embedded boards become reachable); the cost saving is secondary. In v1 an ATS Embed triggers an ATS Fetch only; reverse-engineering a crawlable board URL from an embed for a clientless provider is deferred.
