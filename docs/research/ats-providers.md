# ATS Provider Reference — Recognition, Embed, and Board-API Schemas

> Research deliverable for [issue #131](https://github.com/nicholasbraun/job-crawler-poc/issues/131).
> Follows #112 / ADR-0022 (the LLM-free ATS Fetch lane) and ADR-0016 (`ATSProviderForHost`).
> This is the input for expanding the provider registry (`internal/ats`) beyond the shipped Greenhouse + Lever.

**Method.** Three deep-research passes (fan-out search → live-fetch → adversarial verification at 3 votes/claim).
- **Round 1** (2026-07-17): broad market sweep — 6 angles, 25 sources, 25 claims verified (18 confirmed / 7 refuted). Verified Ashby, SmartRecruiters, Recruitee.
- **Round 2** (2026-07-18): focused deep-dive on Personio, join.com, Workable — 6 angles, 21 sources, 25 claims verified (20 confirmed / 5 refuted).
- **Round 3** (2026-07-23): **empirically-driven** — candidates chosen from ATS hosts observed leaking into the crawl lane of the live Corpus (the pipeline-inversion collection crawl, #181), not from market research. 7 providers deep-verified + 5 light-touch + 3 aggregators, each fetched live against a self-discovered real tenant; every READY endpoint additionally re-confirmed by the orchestrator with a cold-start curl. Verified softgarden, Teamtailor, Manatal, helixjobs.

Every "VERIFIED-live" entry below was fetched **unauthenticated against a real tenant** with a captured sample. Claims appearing only in commercial-scraper marketing or vendor prose, without a live capture, are labelled **unverified** and not treated as fact.

**Provenance caveat.** API captures are 2026-07-17/18 (R1/R2) and 2026-07-23 (R3) snapshots; endpoints, field sets, encodings, and pagination can change without notice. **Re-verify each provider's live sample before implementing its fetcher ticket.** Where a field map comes from live capture only, that is called out per-provider.

---

## Summary table

| Provider | Recognition host(s) & tenancy | Public board API? | Payload | List endpoint template | Verdict |
|---|---|---|---|---|---|
| **Greenhouse** | `boards.greenhouse.io/<slug>`, `job-boards.greenhouse.io/<slug>`, EU: `boards.eu.greenhouse.io` — **path** | Yes, no-auth | JSON | `GET https://boards-api.greenhouse.io/v1/boards/<slug>/jobs?content=true` | ✅ SHIPPED |
| **Lever** | `jobs.lever.co/<slug>` — **path**; EU `api.eu.lever.co` (unverified) | Yes, no-auth | JSON | `GET https://api.lever.co/v0/postings/<slug>?mode=json` | ✅ SHIPPED |
| **Ashby** | `jobs.ashbyhq.com/<slug>` — **path** | Yes, no-auth | JSON | `GET https://api.ashbyhq.com/posting-api/job-board/<slug>?includeCompensation=true` | ✅ READY (R1) |
| **SmartRecruiters** | `careers.smartrecruiters.com/<companyIdentifier>` / API path slug — **path** | Yes, no-auth (**Posting API only**) | JSON | `GET https://api.smartrecruiters.com/v1/companies/<companyIdentifier>/postings` | ✅ READY (R1) |
| **Recruitee** | `<tenant>.recruitee.com` — **subdomain** | Yes, no-auth | JSON | `GET https://<tenant>.recruitee.com/api/offers/` | ✅ READY (R1) |
| **Personio** | `<account>.jobs.personio.de` (+ `.com` twin) — **subdomain** | Yes, no-auth | **XML** ⚠️ | `GET https://<account>.jobs.personio.de/xml` | ✅ READY (R2) — **XML, needs `encoding/xml`** |
| **Workable** | `apply.workable.com/<slug>` (path) + `<slug>.workable.com` (subdomain) | Yes, no-auth (**widget API only**) | JSON | `GET https://www.workable.com/api/accounts/<slug>?details=true` (→302) | ✅ READY (R2) |
| **join.com** | `join.com/companies/<slug>` — path prefix | **No public API** (v2 REST is token-gated) | — | — | ⛔ CRAWL-FALLBACK-ONLY (R2) |
| Teamtailor | `<tenant>.teamtailor.com` — subdomain | Unknown | — | — | ❓ UNRESEARCHED |
| BambooHR | `<tenant>.bamboohr.com` — subdomain | Widget-only (internal JSON, unstable) | — | — | ❓ UNRESEARCHED / likely no stable API |
| iCIMS | `<tenant>.icims.com` — subdomain | Unknown (enterprise) | — | — | ❓ UNRESEARCHED |
| Workday | `<tenant>.<shard>.myworkdayjobs.com` — subdomain+shard | Unknown (enterprise) | — | — | ❓ UNRESEARCHED |
| Comeet | — | Docs exist (`developers.comeet.com`) | — | — | ❓ UNRESEARCHED (good next target) |
| Breezy, JazzHR, Jobvite, SuccessFactors, Taleo, Recruiterflow, Rippling, indigo.jobs, hibob, haileyhr | (various) | Unknown | — | — | ❓ UNRESEARCHED |

**Bottom line: 5 providers are now READY to implement as `BoardFetcher`s beyond the shipped two — Ashby, SmartRecruiters, Recruitee, Personio, Workable.** Personio is the one that breaks the mould: its feed is **XML, not JSON**, so it needs `encoding/xml` and a distinct mapper. join.com has no public board API and stays on the crawl-and-fence fallback. The remaining rows are genuinely unresearched — blank ≠ "no API exists".

### Round 3 summary (empirical — corpus crawl-lane leakage, #181)

Candidates ranked by **crawl hits** in a ~933-row corpus sample — treat the *ranking* as signal, not the absolutes (at full corpus scale these are much larger). Every host below is one `catalog.Identify` does **not** route to the ATS Fetch lane today, so its tenants fell through to the expensive crawl-and-extract lane.

| Provider | Crawl hits | Recognition & tenancy | Public board API? | Payload | Endpoint / source | Verdict |
|---|---|---|---|---|---|---|
| **softgarden** | 8 | `<tenant>.career.softgarden.de` — **subdomain** (+ custom-domain CNAME) | Yes, no-auth | JSON (schema.org `DataFeed`) | `GET https://<host>/jobs.feed.json` | ✅ READY (R3) |
| **Manatal** (`careers-page.com`) | 6 | `<tenant>.careers-page.com` + `www.careers-page.com/<tenant>` — **subdomain + path** | Yes, no-auth (**documented + public OpenAPI**) | JSON | `GET https://api.careers-page.com/open/v1/career-pages/<slug>/job-posts` | ✅ READY (R3) |
| **Teamtailor** | 3 | `<tenant>.teamtailor.com` — **subdomain** (already recognized) | Yes, no-auth | RSS / JSON Feed | `GET https://<tenant>.teamtailor.com/jobs.rss` | ✅ READY (R3) |
| **helixjobs** | 4 | `<tenant>.helixjobs.com` — **subdomain** (🇩🇪 Sparkassen/Volksbanken) | Yes, no-auth ⚠️ | JSON + JSON-LD | `GET /_/jobmap/geojson?h=<hash>` (2-step) | ✅ READY-w/-caveats (R3) |
| **CareerPlug** | 5 | `<tenant>.careerplug.com` — **subdomain** | No (native API contract-gated) | JSON-LD-in-HTML | crawl + JSON-LD extract | ⛔ CRAWL-FALLBACK (R3) |
| **ApplicantStack** | 5 | `<tenant>.applicantstack.com` — **subdomain** | No | JSON-LD-on-detail | crawl + JSON-LD extract | ⛔ CRAWL-FALLBACK (R3) |
| **onlyfy** | 2 | `<tenant>.onlyfy.jobs` — **subdomain** (🇩🇪 New Work/XING) | No | HTML only (no JSON-LD) | crawl + LLM | ⛔ CRAWL-FALLBACK (R3) |
| Jobvite | 1 | `jobs.jobvite.com/<tenant>` — path | No (OAuth-gated) | — | crawl | ⛔ CRAWL-FALLBACK (R3 light) |
| iCIMS | 1 | `careers-<tenant>.icims.com` — subdomain (already recognized) | No (OAuth XML feed, gated) | — | crawl | ⛔ CRAWL-FALLBACK (R3 light) |
| Freshteam | 2 | `<tenant>.freshteam.com` — subdomain | No (auth) · **SUNSET ~2027** | — | skip | ⛔ CRAWL-FALLBACK / SUNSET (R3 light) |
| Quickin | 1 | `jobs.quickin.io/<tenant>` — path (🇩🇪) | No (Nuxt SPA, none found) | — | crawl | ⛔ CRAWL-FALLBACK (R3 light) |
| GoHire | 1 | `<tenant>.gohire.io` — subdomain | No public route found | — | crawl | ⛔ CRAWL-FALLBACK (R3 light) |

**Round-3 bottom line: 4 new READY providers — softgarden, Teamtailor, Manatal, helixjobs** (softgarden/Teamtailor/Manatal are clean; **helixjobs is READY-with-caveats** — a 2-step, map-widget-gated index, see its section). The other 8 are CRAWL-FALLBACK: CareerPlug/ApplicantStack expose no board API but embed **schema.org JSON-LD** (so they can be crawled *LLM-free* — a future crawl-lane optimization, not an `ats.BoardFetcher`); onlyfy and the 5 light-touch providers are gated / HTML-only / dying. Recognition-only rules and one aggregator-denylist **correction** (jobsocial.de) are folded into a single catalog-hygiene ticket — see [Catalog-hygiene](#catalog-hygiene-round-3).

---

## Verified providers (READY / shipped)

### Greenhouse — ✅ shipped (baseline)

- **Recognition (path):** `boards.greenhouse.io/<slug>`, `job-boards.greenhouse.io/<slug>`, EU variants `boards.eu.greenhouse.io` / `job-boards.eu.greenhouse.io`. Slug = first path segment. (In `catalog.pathRules`.)
- **Embed:** `<div id="grnhse_app"></div>` + `<script src="https://boards.greenhouse.io/embed/job_board/js?for=<token>"></script>` before `</body>`. Tenant = job board token in `for=`. Matches `catalog.ATSEmbedTenant`'s `?for=` special-case. Live-verified: `boards.greenhouse.io/embed/job_board/js?for=cobaltio` calls `document.getElementById("grnhse_app")`.
- **Board API:** `GET https://boards-api.greenhouse.io/v1/boards/<slug>/jobs?content=true` → `{"jobs":[{title, absolute_url, content, first_published, location:{name}, departments:[{name}]}]}`. No auth. Single posting: `.../jobs/<id>`.
- **Field map:** `title`→Title, `absolute_url`→URL, `location.name`→Location, `departments[0].name`→Department, `first_published` (RFC3339)→FirstPublished, `content`→Description.
- **Description encoding:** **double-encoded HTML** (`htmlDoubleEncodedToText`).

### Lever — ✅ shipped (baseline)

- **Recognition (path):** `jobs.lever.co/<slug>`. Slug = first path segment.
- **Board API:** `GET https://api.lever.co/v0/postings/<slug>?mode=json` → array of `{text, hostedUrl, categories:{department,location}, description, lists:[{text,content}], additional, createdAt, workplaceType}`. No auth.
- **Field map:** `text`→Title, `hostedUrl`→URL (upsert key; skip a posting missing it), `categories.location`→Location, `categories.department`→Department, `createdAt` (ms epoch)→FirstPublished, `workplaceType == "remote"`→Remote, `description`+`lists[]`+`additional`→Description.
- **Description encoding:** **single-encoded** real HTML (`htmlSingleEncodedToText`, per-section).
- **Supplementary (unverified):** an EU host `api.eu.lever.co/v0/postings/<slug>` was reported by an OSS adapter but not live-captured — verify before relying on it.

### Ashby — ✅ VERIFIED-live (R1) → **READY**

- **Recognition (path):** host `jobs.ashbyhq.com`, tenant = final path segment / job-board name (`/ramp`, `/linear`, `/stripe`). In `catalog.pathRules`.
- **Embed:** `<script src="https://jobs.ashbyhq.com/<slug>/embed?version=2"></script>` — slug in the **path**. (Refuted: container id is `ashby_embed`, not `ashby-embed`; not load-bearing.)
- **Board API:** `GET https://api.ashbyhq.com/posting-api/job-board/<slug>?includeCompensation=true` — **public, zero auth**. HTTP 200 vs Ramp (125 jobs), Linear (24), OpenAI (721), Vanta (105).
- **Field map:** `title`→Title, `department`→Department, `location`/`address`→Location, `jobUrl`→URL, `publishedAt` (RFC3339)→FirstPublished, `isRemote`/`workplaceType`→Remote, plus `employmentType`.
- **Description encoding:** **ships both `descriptionHtml` and `descriptionPlain`** — map `descriptionPlain` directly, **no HTML-strip pass needed**.
- **⚠️ Do NOT use** `POST https://api.ashbyhq.com/jobPosting.list` — that is HTTP-Basic-auth (API key, `jobsRead`). Use the public GET `posting-api` endpoint.

### SmartRecruiters — ✅ VERIFIED-live (R1) → **READY**

- **Recognition (path):** tenant is a **path slug** (`companyIdentifier`), not a subdomain. **Not yet in `catalog` rules — needs a new `pathRule`.**
- **Board API (Posting API — the one to use):** list `GET https://api.smartrecruiters.com/v1/companies/<companyIdentifier>/postings`; single `GET .../postings/<postingId>`. **No auth** — live HTTP 200 for SmartRecruiters, Bosch, Visa, Ubisoft. List returns `{totalFound, content:[…]}`.
- **Field map:** `name`→Title, `location.city` + `location.country`→Location, `department` + `function.label`→Department, `releasedDate` (ISO 8601)→FirstPublished, `jobAd.sections[].{title,text}` (companyDescription / jobDescription / qualifications / additionalInformation)→Description.
- **⚠️ Canonical-URL trap:** `postingUrl`/`applyUrl` live on the **single-posting `PostingDetails` object, NOT the list object** (list carries only `ref`). A stable posting URL needs the per-posting detail call or a synthesized template.
- **Description encoding:** `jobAd.text` is "plain text with basic html tags"; **exact entity-encoding undocumented / not verified live** — determine at implementation time.
- **⚠️ Do NOT confuse with the SmartRecruiters _Job Board API_** (`developers.smartrecruiters.com/docs/partners-job-board-api`) — that is **partner-gated (Partner API Key)**. The `feed/publications` + `X-SmartToken` endpoint is also auth-gated/unverified.

### Recruitee — ✅ VERIFIED-live (R1) → **READY**

- **Recognition (subdomain):** tenant = careers-site subdomain, `<tenant>.recruitee.com`. In `catalog.subdomainRules`.
- **Board API:** `GET https://<tenant>.recruitee.com/api/offers/` — **public, no auth**. Live HTTP 200 JSON for hostaway, anywhereworks, constellr, framestore.
- **Field map:** `title`→Title, `location` (string) + rich `locations[]`→Location, `department`→Department, `published_at`→FirstPublished, `careers_url`→URL (**often a custom domain**), `description` + `requirements`→Description.
- **⚠️ CRITICAL — timestamp:** `published_at`/`created_at` are `"YYYY-MM-DD HH:MM:SS UTC"` (e.g. `"2026-07-17 10:29:15 UTC"`) — **NOT RFC3339** → custom Go layout `"2006-01-02 15:04:05 MST"`. From **live capture only** (docs stub doesn't enumerate fields) — re-confirm live.
- **⚠️ Custom-domain URLs:** `careers_url` often points at the tenant's own domain, not `*.recruitee.com`. Fine as the upsert key; attribution comes from Owner/seed (ADR-0022).
- **Embed:** the Recruitee **widget** is a separate JS mechanism (`RTWidget`/`RecruiteeWidget`) keyed on a **numeric company ID** in `{ "companies": [ID] }` — **not via URL**. An embedded Recruitee widget carries no URL-derivable slug (unlike Greenhouse `?for=`); embed→fetch for Recruitee would need numeric-ID→subdomain resolution — out of scope for v1 embed handling (#129).
- **Description encoding:** not separately confirmed; `description`/`requirements` are HTML — assume single-encoded pending a live check.

### Personio — ✅ VERIFIED-live (R2) → **READY (XML-only)**

> ⚠️ **This is the one that does not fit the JSON path.** Personio's public feed is **XML**, so the fetcher must use `encoding/xml` with its own mapper shape — it cannot reuse the `encoding/json` Greenhouse/Lever plumbing.

- **Recognition (subdomain):** tenant = subdomain label of `<account>.jobs.personio.de` (and identical `.com` twin). In `catalog.subdomainRules` (`jobs.personio.de`, `jobs.personio.com`). Both regional hosts serve **identical content** per tenant. Live-confirmed: `knime`→KNIME GmbH, `deskbird`→deskbird GmbH.
- **Board API:** `GET https://<account>.jobs.personio.de/xml` (optional `?language=en`) — **public, no auth**, served from the career-site host (**not** the token-gated `api.personio.de` Recruiting API). Live-fetched with zero auth headers (HTTP 200, `text/xml`) across many real tenants (personio, data4life, knime, centogene, jobleads, holidaycheck, deskbird, gnosis, seek-development, konux, open-mind-technologies on `.de`; stark, tozero, flatpay, index-soft on `.com`).
- **Payload: XML.** Prolog `<?xml version="1.0" encoding="UTF-8"?>`, root `<workzag-jobs>` wrapping repeated **`<position>`** elements. ⚠️ The wire element is `<position>`, **not `<posting>`** (the OpenAPI `posting` is only the array *property* name — verifier grep of a live body found 11 `<position>`, 0 `<posting>`). No JSON/`Accept` content-negotiation variant exists.
- **Field map (XML → JobListing):** `<name>`→Title, `<office>`→Location (plus `<additionalOffices>`), `<department>`→Department, `<createdAt>`→FirstPublished, `<id>`→posting id (for the canonical URL — the feed does not carry a per-posting URL, so synthesize `https://<account>.jobs.personio.de/job/<id>` or equivalent). `<subcompany>` = employer; `<recruitingCategory>`, `<employmentType>`/`<seniority>`/`<schedule>`/`<yearsOfExperience>` enums and `<keywords>` also present.
- **Description:** `<jobDescriptions>` is a **container** of named `<jobDescription>(name, value)` sections whose `value` is **CDATA-wrapped HTML**. The mapper must **concatenate the sections** into one Description before the plain-text strip. (Exact entity-encoding depth not pinned — verify against a live sample.)
- **⚠️ CRITICAL — timestamp is ambiguous, dual-parse:** the documented example is basic ISO-8601 with a **no-colon** offset `2016-05-31T12:14:07+0200` (Go layout `"2006-01-02T15:04:05-0700"`), but **every live 2026 tenant returned RFC3339 with a colon** (`2026-07-02T15:34:32+00:00`). Try `time.RFC3339` first, fall back to `"2006-01-02T15:04:05-0700"`; keep the zero-time fail-safe.
- **⚠️ Opt-in feed:** the XML feed is toggled per tenant (Settings → Recruiting → Career page). A recognized tenant may legitimately return `404` or an **empty `<workzag-jobs/>`** — treat as "no open roles", not an error.
- **⚠️ Header noise:** the ReadMe `get_xml` reference lists `X-Company-ID` as "required" — this is a doc auto-gen artifact, disproven by zero-header 200s. **Do not send it.**

### Workable — ✅ VERIFIED-live (R2) → **READY**

- **Recognition (path + subdomain):** `apply.workable.com/<slug>` (path) and `<slug>.workable.com` (subdomain) both map to the `<subdomain>` tenant key. In `catalog.pathRules` (`apply.workable.com`) and `subdomainRules` (`workable.com`).
- **Board API:** `GET https://www.workable.com/api/accounts/<slug>?details=true` — **public, no auth**. It **302-redirects to** the canonical serving host `https://apply.workable.com/api/v1/widget/accounts/<slug>` (Go's default `http.Client` follows automatically — no special handling). Returns JSON `{name, description, jobs:[…]}` — **postings are nested under the `jobs` key, not at the root.** Live-verified no-auth: huggingface, cloudfactory, subscript, blueground.
- **Field map (per `jobs[]` entry):** `title`→Title; `country`/`state`/`city` (+ `locations[]`) and `telecommuting`/`workplace_type`→Location; `department`→Department; `published_on` (or `created_at`) → FirstPublished; `url`/`application_url`/`shortlink` (`apply.workable.com/j/<shortcode>`)→canonical URL; `description` (HTML)→Description. Extra fields: `code`, `shortcode`, `employment_type`, `industry`, `function`, `experience`, `education`.
- **⚠️ Timestamp is date-only:** `published_on`/`created_at` are `YYYY-MM-DD` (e.g. `2026-02-12`) — Go layout `"2006-01-02"`. **FirstPublished loses time-of-day precision** (store as midnight UTC).
- **Description encoding:** job `description` is HTML; **single- vs double-encoding not pinned** — verify against a live sample before finalizing the strip.
- **⚠️ TRAP — do NOT use `<subdomain>.workable.com/spi/v3/jobs`.** That is the **authenticated** partner/HR API (Bearer access token, scope `r_jobs` — "accessible with all token types" still means a token is mandatory). Workable's official help page (article 115013356548) documents *only* this auth-gated SPI v3 API, not the public widget one. Also note the sibling `apply.workable.com/api/v3/accounts/<sub>/jobs` returns 404 — the widget API above is the correct public endpoint. Same round-1 "public read API vs auth-gated API at neighboring URLs" pattern.

### softgarden — ✅ VERIFIED-live (R3) → **READY**  🇩🇪

- **Recognition (subdomain):** `<tenant>.career.softgarden.de`; slug = leftmost host label. **Not yet in `catalog` — add a `subdomainRule` (suffix `career.softgarden.de`).** The `<tenant>.softgarden.io` form named in the corpus was **not** confirmed to serve the feed (demo → 404); recognize `.career.softgarden.de` only until `.io` is verified. **Custom domains:** tenants CNAME their own domain (e.g. `karriere.betasystems.com`, `career.nuvisan.com`) onto the same backend and the **identical `/jobs.feed.json` path works there** — but a custom domain is not host-recognizable, so those tenants fall to eTLD+1 identity (same accepted limitation as Recruitee `careers_url`).
- **Board API:** `GET https://<tenant-host>/jobs.feed.json` — **public, zero auth** (live HTTP 200 on demo, sigsales1 [525 jobs], betasystems, nuvisan). A schema.org **`DataFeed` of `JobPosting` items** (`application/feed+json`). **No separate detail call** — each item carries the full HTML description. **No pagination** (whole feed in one response; `numberOfItems` == array length, verified 525/525).
- **Field map:** `item.title`→Title, `item.url`→URL (canonical), `item.jobLocation.address.{addressLocality,addressRegion,addressCountry,postalCode,streetAddress}`→Location (`"-"` placeholders = absent), `item.datePosted` (ISO-8601 **with offset**, e.g. `2024-09-05T11:55:12.145+02:00`)→FirstPublished, `item.description` (HTML)→Description. **No `department`/category field exists** on any tenant (540 items inspected) — do not invent one.
- **Description encoding:** single-encoded real HTML (`<h2>/<h3>/<p>/<ul>`) — strip like Lever/Recruitee.
- **⚠️ Do NOT use** the token/OAuth **Frontend API v3 / Jobs API / JobBoard API** at `dev.softgarden.de` — that is the credentialed job-board-syndication surface. Use the public `/jobs.feed.json`.
- **Bonus signal:** every tenant serves a companion `/llms.txt` that explicitly documents `/jobs.feed.json` as the machine-readable feed for AI crawlers — about as strong a "yes, read this" as an ATS gives.

### Teamtailor — ✅ VERIFIED-live (R3) → **READY**

- **Recognition (subdomain):** `<tenant>.teamtailor.com` — **already in `catalog.subdomainRules`**, so the ticket is fetcher + registry only. **⚠️ Regional / disambiguated hosts:** `<tenant>.<region>.teamtailor.com` (e.g. `thestudio.na.teamtailor.com`) and numeric-suffix hosts (`asklocala-1671530238.teamtailor.com`) exist — the current `subdomainLabel` (leftmost label) returns `"na"` for the regional form, so the **fetcher must template the full board host**, not a single slug. Plain `<slug>.teamtailor.com` (the common case) is fine.
- **Board API (prefer RSS):** `GET https://<tenant>.teamtailor.com/jobs.rss` → **public, no auth**, `application/rss+xml` with a `tt:` namespace (carries **department**, role, per-location name, `remoteStatus`). Also `GET /jobs.json` → JSON Feed v1.1 (`application/feed+json`) — **leaner: no department**. The RSS `<link rel="alternate" type="application/rss+xml" href="/jobs.rss">` is advertised in the `/jobs` `<head>`. **No detail call** (description inline). **No pagination observed** (65/65 single response; not stress-tested past ~65 — completeness on 100+ boards unconfirmed).
- **Field map (RSS):** `<title>`→Title, `<link>`→URL (`/jobs/<id>-<slug>`), `tt:locations/tt:location{name,address,zip,city,country}`→Location, `tt:department`→Department (RSS-only), `<pubDate>` (RFC-822)→FirstPublished, `<description>` (HTML)→Description. JSON-feed equivalents: `date_published` (RFC3339 offset), `content_html`, `_jobposting.jobLocation[].address`.
- **⚠️ Do NOT use** `api.teamtailor.com` — that is the key-gated REST API (`Authorization: Token` + `X-Api-Version`; 406 without a version, 401 without a token). `/jobs.xml` → 406, `/jobs.atom` & `/feed` → 404 (RSS/JSON only).

### Manatal (`careers-page.com`) — ✅ VERIFIED-live (R3) → **READY**  (strongest evidence: documented + public OpenAPI)

- **Recognition (subdomain + path):** tenancy is **both** `<tenant>.careers-page.com` (subdomain) and legacy `www.careers-page.com/<tenant>` (path); both resolve the same `client_slug`. Custom-domain CNAMEs → `customer.careers-page.com`. **Not yet in `catalog` — add a `subdomainRule` (suffix `careers-page.com`) that EXCLUDES the `www`/`api`/`customer` labels, plus a `pathRule` for `www.careers-page.com`.** (A naive subdomain rule would slug `www`/`api` as tenants.) For custom-domain tenants, recover the slug from a posting's apply link (`https://<slug>.careers-page.com/jobs/<uuid>`).
- **Board API (`/open/v1` — the public one):** list `GET https://api.careers-page.com/open/v1/career-pages/<slug>/job-posts?page=&size=` (size ≤ 250, envelope `{items,total,page,size,pages}` — **real pagination, loop pages**); detail `GET https://api.careers-page.com/open/v1/job-posts/<id>`. **No auth** (live 200; total=38 for `manatal`). Officially documented (developers.manatal.com "Career Page (Current)") with a **public `api.careers-page.com/openapi.json`** — the highest-confidence, most-stable entry in the whole doc.
- **Field map:** `translations[].name`→Title, constructed `https://<slug>.careers-page.com/jobs/<id>`→URL, `location.{city,state,country}`→Location, `client.name`→Department, **`published_at`→FirstPublished (ISO-8601 UTC µs) — DETAIL ENDPOINT ONLY** (absent from index items), `translation(s).description` (HTML)→Description.
- **⚠️ N+1 for FirstPublished:** the index inlines title/location/description but **not `published_at`** — complete publish dates need a per-posting detail GET (the #140 pattern), or accept FirstPublished missing from index-only.
- **⚠️ Do NOT use** `api.careers-page.com/api/v1/...` — that is the JWT-Bearer recruiter API (403 without a token), keyed by an internal `career_page_id` UUID (not the public `client_slug`). Legacy path job IDs are short alphanumerics ≠ the UUID the API/subdomain form uses — don't cross-feed. HTML host 429s under rapid load; pace/backoff.

### helixjobs — ⚠️✅ VERIFIED-live (R3) → **READY (with caveats)**  🇩🇪

> Stack note: the `*.helixjobs.com` candidate board is a **Perbility** skin over a **Concludis** ATS engine with a **Peras** HR backend — recognized as one provider (`helixjobs`) here. Widely used by German Sparkassen / Volksbanken, commonly **iframed** into the bank's own site.

- **Recognition (subdomain):** `<tenant>.helixjobs.com`; slug = leftmost host label. **Not yet in `catalog` — add a `subdomainRule` (suffix `helixjobs.com`).** No custom-domain instance found (banks iframe instead). Do **not** confuse the public board with the auth-gated recruiter backend at `<tenant>.<group>.hr.peras.de` (303 → /login).
- **Board API (public no-auth, but 2-step and widget-gated):** the JSON index is `GET https://<tenant>.helixjobs.com/_/jobmap/geojson?h=<board-hash>` → 200 `application/json`. The `<board-hash>` is **not slug-derivable** — it must be scraped once from the tenant's `/_/jobmap` HTML (`data-data-url="/_/jobmap/geojson?h=…"`); omitting it → `400 {"errors":["Cache file not found"]}`. Detail is `GET /_/jobad?prj=<id>` → HTML embedding a schema.org `JobPosting` **JSON-LD** block. **No pagination** (whole board per response).
- **⚠️ Caveat 1 — map-widget-gated:** the geojson index only exists where the tenant enabled the map widget (present: vblh, spktw, spkhb; **absent**: spk-bbg, sska). It is **not** platform-guaranteed.
- **⚠️ Caveat 2 — completeness (ADR-0035):** the geojson is a *map* dataset, so an address-less posting could be dropped. One tenant fully cross-checked (vblh: 17 geojson == 17 `/_/joblist`) matched, but this is not guaranteed → the fetcher should emit `ErrBoardIncomplete` when the map is disabled or counts mismatch.
- **Robust alternative (fetcher design choice):** `GET /_/joblist` (HTML, lists every posting) → per-posting `GET /_/jobad?prj=<id>` **JSON-LD** — widget-independent and complete, but **N+1** (the #140 concern). The fetcher ticket must choose geojson-primary-with-joblist-fallback vs joblist+JSON-LD. **This is why helixjobs is filed needs-design, not ready-for-agent.**
- **Field map:** `properties.title` / JSON-LD `title`→Title; `properties.url` (relative — prefix host)→URL; `properties.locationName` (free text) or JSON-LD `jobLocation[].address` (structured, may be multi-location)→Location; **JSON-LD `datePosted` (`YYYY-MM-DD`, date-only)**→FirstPublished — **do NOT use the index `changed` field (`DD.MM.YYYY`, last-modified, different meaning)**; JSON-LD `description` (HTML)→Description. No `department` field.

---

## Not a fetcher (crawl-and-fence fallback)

### join.com — ⛔ CRAWL-FALLBACK-ONLY / NEEDS-AUTH (R2)

- **Recognition (path prefix):** `join.com/companies/<slug>`. In `catalog.pathRules` (prefix `companies`). Stands and is the only verified schema for join.com.
- **Board API:** **no public no-auth board endpoint.** The only documented API is the **auth-required JOIN API v2** — base `https://api.join.com/v2`, jobs listing `GET https://api.join.com/v2/jobs`, requiring a per-company token in the `Authorization` header (tokens minted manually in-app at `join.com/user/api`; v1 deprecated). Live probing found no public feed: `join.com/api/companies/<slug>` → 401; guessed `/api/v2/companies/<slug>/jobs` and `api.join.com/v2/...` → 404.
- **Embed:** the careers embed is a dashboard-generated client-side JS widget, but the specifics are **UNVERIFIED** — round-1's widget-doc claims (unpkg `join-web-components` script, `team-id`/`alias` data attributes) were all **refuted 0-3**. No embed markers or tenant-key location can be asserted.
- **Verdict:** stays on the ordinary crawl-and-fence path (Scope-fenced by `join.com/companies/<slug>`). The public-API *absence* is a negative established by search + probing (401/404), which is inherently weaker than a positive live 200 — an undocumented endpoint can't be fully ruled out, but none was found.

### ApplicantStack — ⛔ CRAWL-FALLBACK (R3)  (JSON-LD-on-detail → LLM-free-crawlable)

- **Recognition (subdomain):** `<tenant>.applicantstack.com`; index `/x/openings`, detail `/x/detail/<opaque-alnum-id>`. Recognition-only candidate.
- **No board API:** the index is a bare HTML `<table>` (no JSON-LD); `.json`/`.xml` suffixes → 404, `Accept: application/json` ignored (always `text/html`), `/x/openings/rss` is a **soft catch-all** that re-serves the openings HTML (a status-code-only trap). Verified across 5 tenants.
- **But** each **detail** page (`/x/detail/<id>`) embeds a schema.org `JobPosting` **JSON-LD** block (verified on `best`, `rstover`): `title`, `datePosted` (ISO-8601 offset), `jobLocation.address` (structured), `description` (HTML), `identifier.value`. No `department`. So the ordinary crawl lane can extract it **without an LLM** (index HTML → detail URLs → JSON-LD) — a future crawl-lane optimization, **not** an `ats.BoardFetcher`.

### CareerPlug — ⛔ CRAWL-FALLBACK (R3)  (JSON-LD-in-HTML → LLM-free-crawlable)

- **Recognition (subdomain):** `<tenant>.careerplug.com`, free-form customer-chosen slug; forms `<slug>-careers` (a **franchise-network board** spanning many franchisees) and plain `<slug>` (one franchisee). Invalid slug → 302 to `app.careerplug.com/user/sign_in`. Recognition-only candidate — **caveat:** a `-careers` franchise board collapses many franchisees under one slug (a mild, within-brand #46).
- **No board API:** `/jobs.json` → 406 `not_acceptable`; `/api` → 404. The native API/webhooks are contract-gated (Grow plan + TAM).
- **But** the `/jobs` listing embeds a schema.org `ItemList` and each `/jobs/<id>/apps/new` a single `JobPosting` **JSON-LD** — LLM-free-crawlable. Quirks: **description is plain-text (no HTML tags)**, no `department` (only a coarse `industry`), `datePosted` ISO-8601 offset, and the `<link rel="canonical">` is **broken (empty subdomain) — reconstruct the URL from the request host**.

### onlyfy — ⛔ CRAWL-FALLBACK (R3)  (true LLM-needed — no JSON-LD)  🇩🇪

- **Recognition (subdomain):** `<tenant>.onlyfy.jobs` (ex-Prescreen; New Work SE / XING). Recognition-only candidate.
- **No structured data at all:** Next.js SSR HTML, **no** JSON/XML and **no** JSON-LD (`/api/jobs`, `/jobs.json`, `/feed`, `/sitemap.xml` → 404; `ld+json` absent on 3 tenants). The index (`/<locale>?page=N`) SSR-renders job cards; the human detail page `/en/job/<id>` has an **empty `<main>`** — the content loads in an iframe at `/job/show/<id>/full` (discoverable only by decompiling the JS bundle). The facts-table there is **per-tenant hand-authored HTML** with no schema and **no reliable publish date**. Gated REST API (API-key, recruiter admin). → unlike ApplicantStack/CareerPlug, onlyfy needs the LLM extractor on the crawl path.

### Light-touch (R3) — confirmed gated / no public API

Single-probe confirmations; each fell through to the crawl lane and stays there.

- **Jobvite** — `jobs.jobvite.com/<tenant>` (path). SPA shell; every guessed API path re-serves the board HTML. Official REST/GraphQL API is OAuth-2.0 / partner-key gated. **Recognition-only candidate** (path; not yet recognized).
- **iCIMS** — `careers-<tenant>.icims.com` (subdomain, **already recognized**). The "Standard XML Feed for Job Boards" is OAuth-gated (client_id/secret after manual approval); `mode=json` ignored (HTML fallback). No action.
- **Freshteam** — `<tenant>.freshteam.com`. Real `/api/*` but 401 `invalid_credentials` (auth-walled). **SUNSET:** Freshworks is discontinuing Freshteam (renewals halt 2026-03-07, full shutdown ~2027-04) → **not worth even a recognition rule.**
- **Quickin** — `jobs.quickin.io/<tenant>` (path, 🇩🇪). Nuxt SPA; no API endpoint in source, no docs. Low-priority recognition-only candidate.
- **GoHire** — `<tenant>.gohire.io` / `careers.gohire.io` (subdomain). `api.gohire.io` is a real JSON API host (structured 404s) but no public jobs route found without reverse-engineering (out of light-touch scope). Recognition-only candidate.

<a name="catalog-hygiene-round-3"></a>
## Catalog-hygiene (Round 3) — recognition-only + aggregator denylist

R3 surfaces two kinds of pure-`internal/catalog/identity.go` change, bundled into **one combined catalog-hygiene ticket** (they are semantic opposites on look-alike hosts, so the ticket body must keep the two lists clearly separated):

**(a) Recognition-only rules** — hosts confirmed to be clean multi-tenant ATS with **no** usable board API. Adding a rule fixes the #46 fake-company collapse (correct per-tenant `CompanyKey`) without moving the tenant off the crawl lane. Add only where tenancy is confirmed clean:

| Host | Rule | Notes |
|---|---|---|
| `applicantstack.com` | `subdomainRule` | JSON-LD-on-detail (future LLM-free crawl) |
| `careerplug.com` | `subdomainRule` | franchise `-careers` boards collapse within-brand (accepted) |
| `onlyfy.jobs` | `subdomainRule` | 🇩🇪; true LLM-needed crawl, but recognition still fixes identity |
| `gohire.io` | `subdomainRule` | note `careers.`/`jobs.` shared subdomains — exclude those labels |
| `jobs.jobvite.com` | `pathRule` | tenant = first path segment |
| `jobs.quickin.io` | `pathRule` | 🇩🇪 |
| `jobsocial.de` | `subdomainRule` | **see (c) — reclassified from aggregator** |

Excluded: **Freshteam** (sunset — dying platform, no rule); **iCIMS** (already recognized).

**(b) Aggregator denylist** — add to `aggregatorHosts` (eTLD+1): **`peopleopsjobs.io`** (confirmed 50 distinct employers on `/companies`, paid featured tiers) and **`moovijob.com`** (Luxembourg's #1 board, 2000+ companies — *caveat:* direct fetch 403'd, classified via cross-source corroboration, not a first-party page).

**(c) Correction — `jobsocial.de` is NOT an aggregator.** The empirical comment listed it for exclusion, but it is a **per-tenant career-page vendor**: `<company>.jobsocial.de` subdomains, one employer each (verified `reha-gmbh.jobsocial.de`); `www.jobsocial.de` is only the vendor's marketing site. Denylisting it at eTLD+1 would wrongly exclude legitimate single-company pages. It moves to the **recognition-only** side (b→a) as a `subdomainRule`. Its board API was **not probed** this pass (it was in the aggregator batch) — if a fetcher is later wanted, verify its API first.

---

## Traps & refuted claims (do not implement these as public fetchers)

| Refuted / gated claim | Verdict | Correct reading |
|---|---|---|
| Workable `spi/v3/jobs` is the public jobs API | — (it's auth-gated) | Bearer token + scope `r_jobs`. **Use the `www.workable.com/api/accounts/<slug>` widget API** instead. |
| join.com `api.join.com/v2` is a public documented board API | 0-3 refuted | It's **token-required** (Authorization header). No public mode → CRAWL-FALLBACK. |
| join.com widget uses an unpkg `join-web-components` script + `team-id` attribute | 0-3 refuted | Embed specifics **unverified**; do not assert any markers. |
| Personio `createdAt` is basic ISO-8601 `+0200` (no colon) only | 0-3 refuted | Live tenants emit RFC3339 `+00:00` (colon). **Dual-parse.** |
| Personio `X-Company-ID` header is required for `/xml` | refuted by live 200s | Doc auto-gen artifact — **do not send it.** |
| SmartRecruiters **Job Board API** is public no-auth | 0-3 refuted (R1) | Partner API Key required — use the **Posting API**. |
| SmartRecruiters `feed/publications` is a public read endpoint | 1-2 refuted (R1) | Needs `X-SmartToken`; auth-gated/unverified — avoid. |
| Ashby `jobPosting.list` is public/no-auth | 0-3 refuted (R1) | POST + HTTP Basic auth (API key). Use the GET `posting-api` endpoint. |
| Teamtailor `api.teamtailor.com` is the jobs API | — (it's key-gated, R3) | Needs `Authorization: Token` + `X-Api-Version` (406/401). **Use the public tenant `/jobs.rss`.** |
| Manatal `api.careers-page.com/api/v1/...` is the board API | — (it's JWT-gated, R3) | 403 without a token; keyed by internal UUID. **Use `/open/v1/...`** (public, documented). |
| softgarden `dev.softgarden.de` Jobs/JobBoard API | — (token/OAuth, R3) | Job-board-syndication surface. **Use the tenant `/jobs.feed.json`.** |
| ApplicantStack `/x/openings/rss` is a jobs feed | — (soft catch-all, R3) | Returns the openings HTML, not a feed. Check content-type, not just status. |
| CareerPlug / ApplicantStack `.json` is a JSON API | — (R3) | 406/404 or ignored. Structured data is **JSON-LD embedded in HTML**, not an API. |
| helixjobs `/_/jobmap/geojson` works from the slug alone | — (R3) | The `?h=<hash>` is required and **must be scraped** from `/_/jobmap` first (else 400). |

**The dual-API conflation remains the #1 implementation trap.** Ashby, SmartRecruiters, Workable — and now **Teamtailor, Manatal, and softgarden** (R3) — each ship a public read API *and* a separate auth-gated API at neighboring URLs. Pin the fetcher to the public one and add a test asserting no auth header is sent. **R3 adds a second trap class: JSON-LD-in-HTML is not a board API** — CareerPlug/ApplicantStack look ingestible but expose no endpoint; they belong on the (LLM-free-capable) crawl path, not in `ats`.

---

## Unresearched candidates (blank ≠ "no API")

The following were **not** fetched and are **not** confirmed to lack a public API — do not read the empty table rows as "no API exists": **BambooHR** (widget hits an internal JSON endpoint whose host/shape reportedly drift — likely no stable public API, unconfirmed), **Workday** (`<tenant>.<shard>.myworkdayjobs.com`), **SAP SuccessFactors**, **Oracle Taleo**, **Breezy**, **JazzHR**, **Recruiterflow**, **Rippling**, **Comeet** (developer docs exist at `developers.comeet.com` — a good next target), **indigo.jobs**, **careers.hibob.com**, **careers.haileyhr.app**.

_Resolved since this list was first written:_ **Teamtailor** → READY (R3), **Jobvite** & **iCIMS** → CRAWL-FALLBACK (R3, gated).

Recommended next batch by recognized-host priority + likely-public-API odds: **Comeet**, then the enterprise/widget-only long tail (Workday, BambooHR, SuccessFactors, Taleo) which most likely stay **CRAWL-FALLBACK-ONLY**. A separate high-value follow-up (not a fetcher): teach the crawl lane to harvest **schema.org JSON-LD** where present (CareerPlug, ApplicantStack, and many others embed it) so those pages ingest LLM-free without a per-provider client.

---

## Implementation notes (mapping to the codebase)

For each READY provider, a fetcher ticket mirrors the Greenhouse/Lever pattern in `internal/ats`:

1. **`internal/ats/<provider>.go`** — a `BoardFetcher` with `WithBaseURL` / `WithHTTPClient` options, `Fetch(ctx, tenant)`, its own structs + `map<Provider>` mapper. Leave `Company`/`CompanyKey` empty for the lane to stamp from Owner (ADR-0022). Skip postings with no canonical URL (the upsert key). Non-200 → `ErrBoardStatus`. Empty board → empty non-nil slice.
2. **`ProviderXxx` const** MUST equal the string `catalog.Identify` emits (`"ashby"`, `"smartrecruiters"`, `"recruitee"`, `"personio"`, `"workable"`), so seed-time routing resolves it against the `Registry` (#127).
3. **Register** via `ats.WithFetcher(ProviderXxx, NewXxxFetcher(...))` at the wiring point.
4. **Catalog recognition:** Ashby, Recruitee, Personio, Workable, and **Teamtailor** hosts are already recognized. **SmartRecruiters, softgarden, Manatal, and helixjobs are not** — add rules (SmartRecruiters `pathRule`; softgarden/helixjobs `subdomainRule`; Manatal `subdomainRule` with `www`/`api`/`customer` exclusion **+** a `www.careers-page.com` `pathRule`).

Provider-specific gotchas to encode:

- **Ashby:** take `descriptionPlain` directly — skip the HTML-strip helper.
- **SmartRecruiters:** canonical URL needs the per-posting detail call (list lacks `postingUrl`); verify description encoding live.
- **Recruitee:** custom time layout `"2006-01-02 15:04:05 MST"`; `careers_url` often a custom domain.
- **Personio (XML — the outlier):** use `encoding/xml`, root `<workzag-jobs>` > `<position>`. **Dual-parse `createdAt`** (`time.RFC3339` → `"2006-01-02T15:04:05-0700"`). **Concatenate** the multiple `<jobDescriptions>/<jobDescription>` CDATA-HTML sections into Description. Synthesize the canonical URL from `<id>` + host. Support **both `.de` and `.com`** hosts. Treat `404`/empty `<workzag-jobs/>` as "no open roles". **Never send `X-Company-ID`.** Since the feed omits per-posting URLs, decide the upsert-key scheme (id-derived URL) up front.
- **Workable:** hit `www.workable.com/api/accounts/<slug>?details=true` and let the client follow the 302; iterate the **nested `jobs[]`** (not root). Time layout `"2006-01-02"` (date-only → midnight UTC). **Never** wire the `spi/v3` endpoint (auth-gated). Verify description encoding live.
- **softgarden (R3):** `GET https://<host>/jobs.feed.json`; schema.org `DataFeed` items; no pagination; no `department`; `datePosted` is RFC3339-with-offset; description single-encoded HTML. The fetcher's "tenant" is the full board host (subdomain or custom domain), so template the host, not a slug.
- **Teamtailor (R3):** prefer `GET /jobs.rss` (RSS+`tt:` namespace has `department`; JSON feed doesn't); no pagination; `pubDate` RFC-822. Template the **full board host** — regional (`<t>.na.teamtailor.com`) and numeric-suffix hosts break leftmost-label slugging.
- **Manatal (R3):** `GET https://api.careers-page.com/open/v1/career-pages/<slug>/job-posts` — **paginate** (`page`/`size≤250`). `published_at` is **detail-endpoint-only** → N+1 or accept-missing. `department` = `client.name`. Never touch `/api/v1` (JWT).
- **helixjobs (R3 — needs-design):** 2-step (scrape `?h=` hash from `/_/jobmap`, then geojson) **or** `/_/joblist` HTML → per-`/_/jobad` JSON-LD. Map-widget-gated + completeness risk (`ErrBoardIncomplete` when disabled/mismatch). `datePosted` date-only; ignore the index `changed` field.

---

## Round 3 tickets (spun off this pass)

Per the READY verdicts, one vertical-slice fetcher ticket per provider (each: `internal/ats/<provider>.go` `BoardFetcher` + mapper, register in `NewDefaultRegistry`, catalog recognition, table-test against a captured live sample), plus **one** combined catalog-hygiene ticket. Fetcher tickets are independent of each other and feed a later `/deliver` session.

- **softgarden** — fetcher (READY; `subdomainRule` `career.softgarden.de`; JSON `DataFeed`). _ready-for-agent._
- **Teamtailor** — fetcher (READY; recognition already exists; RSS-preferred; full-host slugging). _ready-for-agent._
- **Manatal / careers-page.com** — fetcher (READY; `subdomainRule`+`pathRule` with `www`/`api` exclusion; paginate; N+1 for `published_at`). _ready-for-agent._
- **helixjobs** — fetcher (READY-with-caveats; **needs-design**: geojson-2-step vs joblist+JSON-LD, map-gating, completeness).
- **Catalog-hygiene** — recognition-only rules (applicantstack, careerplug, onlyfy, gohire, jobvite, quickin, jobsocial.de) **+** aggregator denylist (peopleopsjobs.io, moovijob.com). One `internal/catalog/identity.go` change; body keeps the "these ARE ATS" and "these are NOT" lists clearly separated.

Not ticketed: Freshteam (sunset); iCIMS (already recognized). CareerPlug/ApplicantStack recognition rides in the catalog-hygiene ticket; a separate follow-up could add JSON-LD harvesting to the crawl lane to make them (and others) LLM-free.

## Open questions / follow-ups

1. **Personio `createdAt`** — is the live format now uniformly RFC3339 (`+00:00`) across all tenants, or do some still emit the documented basic `+0200` (no colon)? Dual-layout parser is the safe default until confirmed on a wider sample.
2. **Personio `jobDescriptions`** — exact section ordering and entity-encoding depth of the CDATA HTML, and how the mapper should concatenate sections for the plain-text strip.
3. **Workable pagination** — does `apply.workable.com/api/v1/widget/accounts/<slug>` paginate/cap for very large boards? Observed responses returned all jobs in one payload; large-board behavior not stress-tested.
4. **Description encoding** for Personio, Workable, SmartRecruiters, Recruitee — pin none/single/double entity-encoding from a live sample so the plain-text mapper runs the right number of decode passes.
5. **join.com** — is there truly no undocumented public JSON endpoint behind `join.com/companies/<slug>`? (crawl-only for now); and if a widget embed is ever needed for recognition, capture the *actual* current embed markers given the refuted round-1 claims.
6. **Unresearched recognized hosts** — live-verify Comeet and the enterprise long tail; each needs its own reference section.
7. **helixjobs (R3)** — the fetcher design choice: geojson-2-step (one call, map-widget-gated, completeness risk) vs `/_/joblist` HTML → per-`/_/jobad` JSON-LD (widget-independent, complete, N+1). Resolve before implementing.
8. **softgarden (R3)** — is the `<tenant>.softgarden.io` host form also serving `/jobs.feed.json`, or is `.career.softgarden.de` the only canonical board host? (`.io` not confirmed live.)
9. **Teamtailor / Manatal large-board behavior (R3)** — Teamtailor feeds were unpaginated up to ~65 jobs (100+ unconfirmed); Manatal paginates (`size≤250`) — confirm both enumerate a very large board completely (ADR-0035).
10. **JSON-LD crawl-harvest (R3)** — CareerPlug, ApplicantStack (and many crawl-lane pages) embed schema.org `JobPosting` JSON-LD. A generic JSON-LD extractor in the crawl lane would ingest them LLM-free without a per-provider client — scope this as its own follow-up.

---

## Sources

**Primary (vendor docs / live captures):**
- Greenhouse: [embed article](https://support.greenhouse.io/hc/en-us/articles/46365908766875-Embed-a-Greenhouse-job-board-on-your-career-site); live JS `boards.greenhouse.io/embed/job_board/js?for=cobaltio`
- Ashby: [public job posting API](https://developers.ashbyhq.com/docs/public-job-posting-api), [embedding](https://docs.ashbyhq.com/embedding-ashby-job-boards-in-an-external-careers-page), [jobPosting.list](https://developers.ashbyhq.com/reference/jobpostinglist); live `api.ashbyhq.com/posting-api/job-board/{Ashby,ramp}`
- SmartRecruiters: [Posting API](https://developers.smartrecruiters.com/docs/posting-api), [JobAd object](https://developers.smartrecruiters.com/customer-api/posting-api/objects/jobad/), [Job Board API (partner)](https://developers.smartrecruiters.com/docs/partners-job-board-api); live `api.smartrecruiters.com/v1/companies/BoschGroup/postings`
- Recruitee: [Careers Site API](https://docs.recruitee.com/reference/intro-to-careers-site-api), [widget script](https://github.com/Recruitee/careers-site-scripts/blob/master/widget_on_careers.html), [widget docs](https://recruitee.github.io/widget/docs/); live `hostaway.recruitee.com/api/offers/`
- **Personio (R2):** [Retrieving open job positions](https://developer.personio.de/docs/retrieving-open-job-positions), [get_xml reference](https://developer.personio.de/v1.0/reference/get_xml), [recruiting OpenAPI spec](https://raw.githubusercontent.com/personio/api-docs/master/personio-recruiting-api.yaml), [XML integration support article](https://support.personio.de/hc/en-us/articles/207576365-Integrate-jobs-from-Personio-into-your-website-via-XML); live `personio.jobs.personio.de/xml`, `knime.jobs.personio.de/xml`, `data4life.jobs.personio.de/xml` (+ many, 2026-07-18)
- **Workable (R2):** [public accounts API reference](https://workable.readme.io/reference/jobs-1), [SPI v3 jobs (auth-gated)](https://workable.readme.io/reference/jobs), [API documentation help](https://help.workable.com/hc/en-us/articles/115013356548-Workable-API-Documentation), [job widget embed](https://help.workable.com/hc/en-us/articles/115012801727); live `www.workable.com/api/accounts/{cloudfactory,blueground,huggingface,subscript}?details=true` (2026-07-18)
- **join.com (R2):** [API documentation](https://help.join.com/join-api/api-documentation), [getting started (auth)](https://docs.join.com/reference/getting-started), [job widget FAQ](https://join.com/faq-pages/how-implement-job-widget-to-website); probing `join.com/api/companies/<slug>` (401), guessed v2 paths (404)
- Comeet (unresearched, next target): [Careers API — list positions](https://developers.comeet.com/reference/careers-api-list-all-positions)
- **Round 3 (2026-07-23), live captures:** softgarden `demo.career.softgarden.de/jobs.feed.json` (+ sigsales1 [525 jobs], betasystems, nuvisan), tenant `/llms.txt`; Teamtailor `footasylum.teamtailor.com/jobs.rss` + `/jobs.json` (+ tixtrack, statsperform, thestudio.na), gated `api.teamtailor.com/v1/jobs` (406/401); Manatal `api.careers-page.com/open/v1/career-pages/manatal/job-posts` + public `openapi.json`, gated `/api/v1/...` (403); helixjobs `vblh.helixjobs.com/_/jobmap/geojson?h=…` + `/_/jobad?prj=…` JSON-LD (+ spktw, spkhb); [Manatal Advanced Career Page](https://support.manatal.com/docs/advanced-career-page-integration), [Freshteam sunset](https://www.peoplematters.in/news/business/freshworks-to-end-freshteam-hr-product-stop-renewals-from-march-2026-47939). CRAWL-FALLBACK probes: ApplicantStack, CareerPlug, onlyfy, Jobvite, iCIMS, Quickin, GoHire (status codes captured per provider). Aggregators: peopleopsjobs.io `/companies`, `reha-gmbh.jobsocial.de`, moovijob.com (via corroboration, 403 direct).

**Secondary / practitioner (corroborating, lower weight, marketing/blog claims used only to locate endpoints, never as verification):** OSS scrapers ([adgramigna/job-board-scraper](https://github.com/adgramigna/job-board-scraper), [speedyapply/JobSpy](https://github.com/speedyapply/JobSpy), [PaulMcInnis/JobFunnel](https://github.com/PaulMcInnis/JobFunnel)); ATS-API roundups ([fantastic.jobs/ats](https://fantastic.jobs/article/ats-with-api), [cavuno.com](https://cavuno.com/blog/ats-platforms-public-job-posting-apis), [getknit.dev Workable](https://www.getknit.dev/blog/get-all-open-jobs-from-workable--api-a7Ih5O), [zsevic dev.to Workable](https://dev.to/zsevic/integration-with-workable-public-jobs-api-3nk4)).

---

_Research: round 1 (2026-07-17, broad sweep) + round 2 (2026-07-18, Personio/join/Workable deep-dive) + round 3 (2026-07-23, empirical corpus-leakage: softgarden/Teamtailor/Manatal/helixjobs READY, 8 crawl-fallback) · deep-research harness, live captures re-confirmed by cold-start curl · re-verify live samples before each fetcher ticket._
