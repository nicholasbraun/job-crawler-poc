# ATS Provider Reference — Recognition, Embed, and Board-API Schemas

> Research deliverable for [issue #131](https://github.com/nicholasbraun/job-crawler-poc/issues/131).
> Follows #112 / ADR-0022 (the LLM-free ATS Fetch lane) and ADR-0016 (`ATSProviderForHost`).
> This is the input for expanding the provider registry (`internal/ats`) beyond the shipped Greenhouse + Lever.

**Method.** Two deep-research passes (fan-out search → live-fetch → adversarial verification at 3 votes/claim).
- **Round 1** (2026-07-17): broad market sweep — 6 angles, 25 sources, 25 claims verified (18 confirmed / 7 refuted). Verified Ashby, SmartRecruiters, Recruitee.
- **Round 2** (2026-07-18): focused deep-dive on Personio, join.com, Workable — 6 angles, 21 sources, 25 claims verified (20 confirmed / 5 refuted).

Every "VERIFIED-live" entry below was fetched **unauthenticated against a real tenant** with a captured sample. Claims appearing only in commercial-scraper marketing or vendor prose, without a live capture, are labelled **unverified** and not treated as fact.

**Provenance caveat.** All API captures are 2026-07-17/18 snapshots; endpoints, field sets, encodings, and pagination can change without notice. **Re-verify each provider's live sample before implementing its fetcher ticket.** Where a field map comes from live capture only, that is called out per-provider.

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

---

## Not a fetcher (crawl-and-fence fallback)

### join.com — ⛔ CRAWL-FALLBACK-ONLY / NEEDS-AUTH (R2)

- **Recognition (path prefix):** `join.com/companies/<slug>`. In `catalog.pathRules` (prefix `companies`). Stands and is the only verified schema for join.com.
- **Board API:** **no public no-auth board endpoint.** The only documented API is the **auth-required JOIN API v2** — base `https://api.join.com/v2`, jobs listing `GET https://api.join.com/v2/jobs`, requiring a per-company token in the `Authorization` header (tokens minted manually in-app at `join.com/user/api`; v1 deprecated). Live probing found no public feed: `join.com/api/companies/<slug>` → 401; guessed `/api/v2/companies/<slug>/jobs` and `api.join.com/v2/...` → 404.
- **Embed:** the careers embed is a dashboard-generated client-side JS widget, but the specifics are **UNVERIFIED** — round-1's widget-doc claims (unpkg `join-web-components` script, `team-id`/`alias` data attributes) were all **refuted 0-3**. No embed markers or tenant-key location can be asserted.
- **Verdict:** stays on the ordinary crawl-and-fence path (Scope-fenced by `join.com/companies/<slug>`). The public-API *absence* is a negative established by search + probing (401/404), which is inherently weaker than a positive live 200 — an undocumented endpoint can't be fully ruled out, but none was found.

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

**The dual-API conflation remains the #1 implementation trap.** Ashby, SmartRecruiters, and Workable each ship a public read API *and* a separate auth-gated API at neighboring URLs. Pin the fetcher to the public one and add a test asserting no auth header is sent.

---

## Unresearched candidates (blank ≠ "no API")

The following were **not** fetched and are **not** confirmed to lack a public API — do not read the empty table rows as "no API exists": **Teamtailor**, **BambooHR** (widget hits an internal JSON endpoint whose host/shape reportedly drift — likely no stable public API, unconfirmed), **iCIMS**, **Workday** (`<tenant>.<shard>.myworkdayjobs.com`), **SAP SuccessFactors**, **Oracle Taleo**, **Breezy**, **JazzHR**, **Jobvite**, **Recruiterflow**, **Rippling**, **Comeet** (developer docs exist at `developers.comeet.com` — a good next target), **indigo.jobs**, **careers.hibob.com**, **careers.haileyhr.app**.

Recommended next batch by recognized-host priority + likely-public-API odds: **Teamtailor, Comeet**, then the enterprise/widget-only long tail (Workday, iCIMS, BambooHR) which most likely stay **CRAWL-FALLBACK-ONLY**.

---

## Implementation notes (mapping to the codebase)

For each READY provider, a fetcher ticket mirrors the Greenhouse/Lever pattern in `internal/ats`:

1. **`internal/ats/<provider>.go`** — a `BoardFetcher` with `WithBaseURL` / `WithHTTPClient` options, `Fetch(ctx, tenant)`, its own structs + `map<Provider>` mapper. Leave `Company`/`CompanyKey` empty for the lane to stamp from Owner (ADR-0022). Skip postings with no canonical URL (the upsert key). Non-200 → `ErrBoardStatus`. Empty board → empty non-nil slice.
2. **`ProviderXxx` const** MUST equal the string `catalog.Identify` emits (`"ashby"`, `"smartrecruiters"`, `"recruitee"`, `"personio"`, `"workable"`), so seed-time routing resolves it against the `Registry` (#127).
3. **Register** via `ats.WithFetcher(ProviderXxx, NewXxxFetcher(...))` at the wiring point.
4. **Catalog recognition:** Ashby, Recruitee, Personio, and Workable hosts are already recognized. **SmartRecruiters is not** — add a `pathRule` for its API/careers host.

Provider-specific gotchas to encode:

- **Ashby:** take `descriptionPlain` directly — skip the HTML-strip helper.
- **SmartRecruiters:** canonical URL needs the per-posting detail call (list lacks `postingUrl`); verify description encoding live.
- **Recruitee:** custom time layout `"2006-01-02 15:04:05 MST"`; `careers_url` often a custom domain.
- **Personio (XML — the outlier):** use `encoding/xml`, root `<workzag-jobs>` > `<position>`. **Dual-parse `createdAt`** (`time.RFC3339` → `"2006-01-02T15:04:05-0700"`). **Concatenate** the multiple `<jobDescriptions>/<jobDescription>` CDATA-HTML sections into Description. Synthesize the canonical URL from `<id>` + host. Support **both `.de` and `.com`** hosts. Treat `404`/empty `<workzag-jobs/>` as "no open roles". **Never send `X-Company-ID`.** Since the feed omits per-posting URLs, decide the upsert-key scheme (id-derived URL) up front.
- **Workable:** hit `www.workable.com/api/accounts/<slug>?details=true` and let the client follow the 302; iterate the **nested `jobs[]`** (not root). Time layout `"2006-01-02"` (date-only → midnight UTC). **Never** wire the `spi/v3` endpoint (auth-gated). Verify description encoding live.

---

## Open questions / follow-ups

1. **Personio `createdAt`** — is the live format now uniformly RFC3339 (`+00:00`) across all tenants, or do some still emit the documented basic `+0200` (no colon)? Dual-layout parser is the safe default until confirmed on a wider sample.
2. **Personio `jobDescriptions`** — exact section ordering and entity-encoding depth of the CDATA HTML, and how the mapper should concatenate sections for the plain-text strip.
3. **Workable pagination** — does `apply.workable.com/api/v1/widget/accounts/<slug>` paginate/cap for very large boards? Observed responses returned all jobs in one payload; large-board behavior not stress-tested.
4. **Description encoding** for Personio, Workable, SmartRecruiters, Recruitee — pin none/single/double entity-encoding from a live sample so the plain-text mapper runs the right number of decode passes.
5. **join.com** — is there truly no undocumented public JSON endpoint behind `join.com/companies/<slug>`? (crawl-only for now); and if a widget embed is ever needed for recognition, capture the *actual* current embed markers given the refuted round-1 claims.
6. **Unresearched recognized hosts** — live-verify Teamtailor, Comeet, and the enterprise long tail; each needs its own reference section.

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

**Secondary / practitioner (corroborating, lower weight, marketing/blog claims used only to locate endpoints, never as verification):** OSS scrapers ([adgramigna/job-board-scraper](https://github.com/adgramigna/job-board-scraper), [speedyapply/JobSpy](https://github.com/speedyapply/JobSpy), [PaulMcInnis/JobFunnel](https://github.com/PaulMcInnis/JobFunnel)); ATS-API roundups ([fantastic.jobs/ats](https://fantastic.jobs/article/ats-with-api), [cavuno.com](https://cavuno.com/blog/ats-platforms-public-job-posting-apis), [getknit.dev Workable](https://www.getknit.dev/blog/get-all-open-jobs-from-workable--api-a7Ih5O), [zsevic dev.to Workable](https://dev.to/zsevic/integration-with-workable-public-jobs-api-3nk4)).

---

_Research: round 1 (2026-07-17, broad sweep) + round 2 (2026-07-18, Personio/join/Workable deep-dive) · deep-research harness, claims verified at 3 votes each · re-verify live samples before each fetcher ticket._
