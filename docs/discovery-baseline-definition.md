# Discovery baseline crawl definition (retired)

The baseline **discovery** Seed set and URL filter no longer live in this
directory. They moved into the application so a database reset can never lose
them, and so the dashboard can prefill them.

**Source of truth (code):**

- `internal/crawl_definition.go` → `crawler.DefaultDiscoverySeeds()` — the 25
  baseline Seed URLs (startup directories + VC portfolios, Germany / EU focus).
- `internal/crawl_definition.go` → `crawler.DefaultURLFilterConfig()` — the live
  URL filter. (This is deliberately *not* the old frozen `urlFilter` payload:
  editorial paths — blog/news/press/media — are no longer blocked here, since they
  are worth crawling for their outbound links to companies.)

**How it is used now:** a Discovery Crawl is started from the dashboard, not by
hand-POSTing a JSON payload. The Discovery start modal prefills its editable Seed
list and depth from `GET /api/definitions/defaults?kind=discovery`, which returns
`{ name: "discovery", kind, seedUrls: [...], maxDepth }` sourced from the code
defaults above.

## Historical audit provenance

The previously committed payload (`discovery-baseline-definition.json`, now
removed) was the fixed baseline for the catalog-accuracy audits of the ADR-0007
epic ([#37](https://github.com/nicholasbraun/job-crawler-poc/issues/37)), frozen
so run-to-run precision deltas were attributable to code changes rather than to a
different seed set.

- **Definition id (v3 audit run):** `0b29f7f2-3201-4ecf-aa4d-001cac42480e`
- **First used:** the v3 discovery run (2026-07-10), which measured ~62%
  career-page precision after the #45/#46 fixes shipped.
- **Kind:** `discovery`

This id is referenced from `internal/catalog/identity.go` to explain which run
minted the fake host-companies its curated eTLD+1 list shields against.
