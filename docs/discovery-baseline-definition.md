# Discovery baseline crawl definition

This is the canonical **discovery** crawl definition used as the fixed baseline for the
catalog-accuracy audits (see the ADR-0007 epic, [#37](https://github.com/nicholasbraun/job-crawler-poc/issues/37)).

**Why it's committed here:** crawl definitions are runtime data (created via the API /
dashboard, stored in Postgres `crawl_definition`). They get deleted when the DB is reset,
and the seed set is then unrecoverable — this already happened once, losing the "Background
Crawl" definition behind the v1/v2 audits. To keep audit runs comparable, **freeze this
definition and vary only code**; then run-to-run precision deltas are attributable to code
changes rather than to a different seed set / filter.

- **Definition id (v3 audit run):** `0b29f7f2-3201-4ecf-aa4d-001cac42480e`
- **First used:** the v3 discovery run (2026-07-10), which measured ~62% career-page
  precision after the #45/#46 fixes shipped.
- **Kind:** `discovery` · **maxDepth:** 4

## Seed URLs (25)

Startup directories + VC portfolios (Germany / EU focus):

```
https://www.eu-startups.com/directory/
https://www.startupbrett.de/startups/
https://www.deutsche-startups.de/startup-datenbank/
https://startup-map.berlin/
https://www.gruenderszene.de/datenbank
https://dealroom.co/companies
https://www.crunchbase.com/hub/germany-startups
https://www.f6s.com/companies/germany
https://www.rocketinternet.com/companies
https://www.earlybird.com/portfolio
https://www.hv.capital/portfolio
https://www.pointnine.com/portfolio
https://cherry.vc/portfolio
https://www.projecta.com/portfolio
https://www.holtzbrinck-ventures.com/portfolio/
https://www.speedinvest.com/portfolio
https://www.lakestar.com/portfolio
https://www.techstars.com/portfolio
https://www.ycombinator.com/companies?regions=Europe
https://www.startupberlin.io/
https://www.germanaccelerator.com/portfolio/
https://www.bitkom.org/Mitglieder
https://www.startupverband.de/mitglieder/
https://www.wko.at/startups
https://www.swissstartupradar.ch/
```

## Full definition (replayable payload)

POST this to recreate the definition, then start a run:

```bash
curl -X POST localhost:8080/api/definitions \
  -H 'Content-Type: application/json' \
  -d @docs/discovery-baseline-definition.json
# → returns the created definition; then:
curl -X POST localhost:8080/api/definitions/{id}/runs
```

The exact request body is kept alongside this doc as
[`discovery-baseline-definition.json`](./discovery-baseline-definition.json) (seeds +
`urlFilter` + params). The `urlFilter` blocks editorial/generic paths & subdomains
(blog/news/press/sitemap/login/shop/docs…) and passes career subdomains/paths
(jobs/career/karriere…); it is the second half of what made the v3 catalog cleaner, so it
must be preserved together with the seeds for a faithful baseline.
