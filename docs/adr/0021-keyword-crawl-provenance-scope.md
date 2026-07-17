# Keyword crawls are fenced to their seed Company by a two-key provenance scope

## Context

A Keyword Crawl is seeded from the entire Catalog into one shared, un-fenced Frontier. Nothing recorded which seed a discovered URL descended from, so a crawl walked absolute links onto unrelated hosts (a personal portfolio at `talish.dev` was saved as a Job Listing under a hallucinated company), and a saved listing's company came from the LLM's guess at the page rather than from the Catalog.

## Decision

Every URL carries two Company keys, set at its seed and inherited unchanged by every link discovered from it (both empty for the Discovery Crawl, which is meant to roam):

- **Scope** — the seed's *URL-derived* CompanyKey (`catalog.Identify(seed)`). A discovered link is kept only if `catalog.Identify(link)` yields the same key. Because `Identify` returns the eTLD+1 for a self-hosted host and `provider:tenant` for an ATS host, this one rule confines a self-hosted crawl to a single registrable domain and an ATS crawl to a single tenant (sibling tenants on the same ATS host are rejected). The guardrail runs on the keyword path only.
- **Owner** — the seed's *catalog-stored* CompanyKey. The saved Job Listing's Company is taken from the Catalog via this key, never from the extractor.

## Considered options / why two keys

The two keys are equal for a plain self-hosted Company, which invites collapsing them into one. They are kept separate because they legitimately diverge: an imported Company may carry an explicit key that differs from its URL-derived one (Identity Ladder, ADR-0013), and an ATS board embedded on a Company page re-roots Scope to the ATS tenant while Owner stays the embedding Company. A single key would silently break either the fence or the attribution the moment they diverge — the design is safe precisely because nothing ever assumes the two are equal.

A per-URL provenance tag was chosen over per-Company Frontier partitions (separate visited sets and queues per Company): the tag rides the existing queue-member encoding with a far smaller blast radius and leaves the Frontier's per-Politeness-Domain scheduling untouched.
