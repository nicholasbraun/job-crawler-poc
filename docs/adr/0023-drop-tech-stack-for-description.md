# Job Listings drop tech_stack for a description, so the ATS lane needs no LLM

## Context

An ATS board API returns a posting's title, location, department, and a free-text description, but not a normalized technology list. `tech_stack` was therefore the one Job Listing field an ATS Fetch could not supply — extracting it would force a per-posting LLM call and negate the ATS Fetch's cost and simplicity.

## Decision

Remove the structured `tech_stack` field from Job Listing and keep only a description: the board API's own text for an ATS Fetch, and the extractor's summary for a crawled page. This makes the ATS lane fully LLM-free.

## Consequences

Structured per-listing technology data is lost. A Keyword Crawl already filters on its keywords against page title and content, so technology-targeted crawls still work; only the stored, queryable tech list goes away.
