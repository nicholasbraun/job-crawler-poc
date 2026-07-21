# Country is resolved deterministically; the LLM names it, the Resolver owns the code

## Context

Constraining a Keyword Crawl by Country (ADR-0028) needs a structured ISO code out
of the messy free-text location a Job Listing carries — `"Berlin, Germany"`,
`"München"`, `"Remote - EU"`. That resolution has to happen on both acquisition
lanes: the crawl lane (which calls an LLM extractor) and the ATS Fetch lane (which,
by design, makes no LLM call — a structural guarantee, ADR-0022).

## Decision

A single deterministic **Country Resolver** (`internal/geo`, hand-rolled, no geo
dependency) is the sole authority on the ISO code. It is fed the most
country-specific string each source offers:

- **Crawl lane:** the LLM's free-text location. The extract prompt is nudged only
  to *name* the country in that text (`"Berlin, Germany"`) — the LLM never emits a
  code.
- **ATS lane:** the provider's structured country field when present (Recruitee
  `country_code`, SmartRecruiters/Workable `country`, Ashby `addressCountry`),
  else the composed location string.

A location the Resolver cannot place yields the empty Country, which the Country
Constraint keeps (ADR-0028).

## Considered options / why deterministic, not LLM-emitted

The obvious alternative is to let the crawl-lane LLM emit the country code directly.
Rejected: the ATS lane holds no extractor, so an LLM-emitted code would require a
*second*, separate mechanism there and split "Germany means Germany" across two
code paths. A deterministic resolver gives one path across both lanes, plus
determinism and table-driven tests — the repo's deterministic-first grain (the Gate,
the Extract Gate, ADR-0004). The LLM still contributes what it is good at (reading a
page and naming the country in context); the Resolver contributes what *it* is good
at (canonical name/code → ISO). No geo library is pulled: the ISO set is a static
table, and the demonyms, synonyms, and city safety-net are curated for the countries
the Catalog actually contains.

## Consequences

Coverage is deliberately partial. The gazetteer grows over time; countries and
cities outside it resolve to unknown and are kept — under-filtering, the safe
direction. Ambiguous tokens (`"Georgia"` the country vs. the US state) are the sharp
edge, mitigated by word-boundary matching and preferring an explicit country token
over a city. And the extract prompt gains a country nudge — a small, low-risk change
to an otherwise tuned prompt (the raw location string is still stored verbatim).
