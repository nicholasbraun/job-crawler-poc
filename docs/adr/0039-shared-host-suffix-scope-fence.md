# The Scope fence keys shared-hosting platforms on the full host

## Context

The Scope fence (ADR-0021) confines a Collection Crawl to its seed Company: a
discovered link is kept only when `catalog.Identify(link).CompanyKey` equals the
seed's Scope. For a self-hosted seed, `Identify` returns the registrable domain
(eTLD+1), so the fence spans the whole domain and every subdomain — exactly what a
real company wants, because a crawl seeded at `careers.acme.com` should still follow
onto `jobs.acme.com`.

That default breaks on a multi-tenant shared-hosting platform, where one registrable
domain fronts thousands of INDEPENDENT tenants on distinct subdomains. Keying on the
eTLD+1 opens the fence to the entire platform. Live proof: an `acme.substack.com`
seed gets Scope `substack.com`, so all 1,830 newsletters — 164K URLs, 6.6% of the
frontier — pass the fence and are crawled as if they were one company. `substack.com`
is not in the Public Suffix List's PRIVATE section, so `publicsuffix.EffectiveTLDPlusOne("acme.substack.com")`
returns `substack.com`, not the full host.

## Decision

Add a curated set `sharedHostSuffixes` (a `map[string]struct{}`) beside
`aggregatorHosts` in `internal/catalog/identity.go`, and one branch in `Identify`,
placed AFTER the ATS match and BEFORE the eTLD+1 fallback: when the host's eTLD+1 is
in the set, key `CompanyKey` on the FULL (already url.normalize-lowercased) host and
return; otherwise the eTLD+1 fallback stands unchanged. Because `Identify` is the
single identity source, the tighter key flows to every consumer that reads it — the
Scope fence, Company upsert, seed routing, import derivation, and the Catalog Doctor
— confining a shared-host seed to its own subdomain and stopping N tenants from
collapsing into one fake Company, the same principle by which ATS multi-tenant hosts
key on `provider:tenant`.

This REFINES ADR-0021; it does not replace it. eTLD+1 remains the DEFAULT for every
real company. That default is load-bearing: a Postgres audit of 7,862 crawl listings
found 22.4% live on a SIBLING subdomain of the same eTLD+1 (`careers.x.com` →
`jobs.x.com`), so a blanket host-level fence would lose roughly one in five real
listings. On a genuine shared host two subdomains are different entities, so there is
no sibling discovery to lose — the tighter key costs nothing there.

The set is a Public-Suffix-List SUPPLEMENT: it lists only multi-tenant hosts the PSL
does NOT already fence. Platforms already in the PSL PRIVATE section (`github.io`,
`blogspot.com`, `wixsite.com`, `notion.site`, `myshopify.com`, `webflow.io`, …)
already return their full host from `eTLDPlusOne`, so they never leaked and an entry
would be a pure no-op — they are omitted. Each entry is PSL-verified un-fenced before
it is added. It ships with `substack.com` (the empirically proven leaker) plus
`beehiiv.com`, `ghost.io`, and `wordpress.com` (the same one-independent-tenant-per-subdomain
newsletter/blog shape, each PSL-verified un-fenced), and is curated and extended over
time exactly like `aggregatorHosts`.

Mega-institution / government / university umbrella domains (`tum.de`, `europa.eu`,
`bayern.de`, …) are deliberately OUT of the set. `karriere.tum.de` and `jobs.tum.de`
are the SAME employer, so full-host keying would both split one company in two and
drop the exact 22.4% cross-subdomain listings this decision preserves; it is an
incomplete fix anyway (a career page on the main `www` host still confines to the
whole university site). Institutions are legitimate employers, so they are neither
full-host-keyed nor blocklisted here — their volume is a separate problem owned by
the deferred per-scope enqueue cap (#203), depth-7 and query hygiene (already
shipped), and locale collapsing (#204).

## Considered options

- **Blanket fence-to-host everywhere** (make the full host the default key).
  Rejected: it discards the audited 22.4% cross-subdomain discovery that eTLD+1
  keying exists to preserve — a real company's `careers.` and `jobs.` subdomains
  would fall out of each other's scope.
- **Add the shared hosts to a `BlockedHostnames` denylist.** Rejected as too blunt:
  blocking `substack.com` outright risks dropping a legitimate careers page that a
  company happens to host there, and the denylist has no notion of "confine to this
  subdomain" — it only rejects. The set keys instead of blocks, so a single-tenant
  seed on the platform is still crawled, just fenced to itself.
- **Fence-to-host with a `careers`/`jobs` subdomain allow-set** (re-admit named
  sibling subdomains). Rejected: more machinery for no gain on genuine shared hosts,
  where sibling subdomains ARE distinct entities that must stay out of scope — the
  allow-set only helps the institution case, which is explicitly out of scope here.

## Consequences

- **Full-host strictness.** `www.<tenant>.substack.com` and `<tenant>.substack.com`
  now key to different CompanyKeys, so a link between them would be cross-scope. Real
  Substack tenants do not use the `www.<tenant>` form, so the risk is low; the plain
  `<tenant>.substack.com` form is the one covered and tested.
- **`RegistrableDomain` is unchanged.** Only `Identify` is touched. The
  `RegistrableDomain` helper (used for `DisplayDomain` and the import Website-derivation
  rung) still returns the eTLD+1, so an imported Website on a shared host derives
  `substack.com` while a career-page-derived record keys the full host. This
  divergence is pre-existing and out of scope for #202.
- **Import Identity Ladder.** A single imported record listing two different
  shared-host subdomains now resolves to two distinct companies (correctly — they
  are distinct) and errors "career pages derive multiple companies" unless it sets an
  explicit `companyKey`. This is the intended behavior, not a regression.
- **Catalog Doctor re-attribution.** The Doctor replays `Identify`, so after deploy a
  Doctor run RE-KEYS existing stored shared-host companies from the whole-platform key
  (`substack.com`) to full-host keys. This is the desired correction — it stops N
  tenants collapsing into one fake company — but it is a real data-re-keying event,
  expected rather than surprising.
