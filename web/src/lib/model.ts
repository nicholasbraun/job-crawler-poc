// View-model builders that reconcile the API's normalized shape (definitions,
// runs, companies, career pages — each fetched separately) into the fused
// entities the dashboard renders. The design treats a "crawl" as a definition
// married to its latest run; the catalog panels need company/career-page joins.

import type { CareerPage, Company, Definition, NameSource, Run } from "../api";
import type { DisplayStatus } from "./status";

// isoAfter reports whether ISO timestamp a is strictly later than b, comparing
// parsed instants rather than the raw strings. Go marshals time.Time as
// RFC3339Nano, which trims trailing zeros in the fractional seconds, so
// lexicographic order is not reliably chronological within the same second.
function isoAfter(a: string, b: string): boolean {
  return Date.parse(a) > Date.parse(b);
}

// latestRunByDefinition reduces the flat run list to each definition's most
// recent run (by startedAt). A definition with no runs is simply absent.
export function latestRunByDefinition(runs: Run[]): Map<string, Run> {
  const latest = new Map<string, Run>();
  for (const run of runs) {
    const prev = latest.get(run.definitionId);
    if (!prev || isoAfter(run.startedAt, prev.startedAt)) {
      latest.set(run.definitionId, run);
    }
  }
  return latest;
}

// KeywordCrawl fuses a keyword definition with its latest run — the design's
// unit of "a crawl". run is null (status "idle") for a definition never started.
export type KeywordCrawl = {
  definitionId: string;
  runId: string | null;
  name: string;
  keywords: string[];
  status: DisplayStatus;
  listingsFound: number;
  pagesCrawled: number;
  frontierSize: number;
  error: string;
};

function fuse(def: Definition, run: Run | undefined): KeywordCrawl {
  return {
    definitionId: def.id,
    runId: run?.id ?? null,
    name: def.name,
    keywords: def.keywords,
    status: run ? run.status : "idle",
    listingsFound: run?.listingsFound ?? 0,
    pagesCrawled: run?.pagesCrawled ?? 0,
    frontierSize: run?.frontierSize ?? 0,
    error: run?.error ?? "",
  };
}

// buildKeywordCrawls fuses every keyword definition with its latest run, newest
// definition first (createdAt desc) so freshly created crawls surface on top.
export function buildKeywordCrawls(defs: Definition[], runs: Run[]): KeywordCrawl[] {
  const latest = latestRunByDefinition(runs);
  return defs
    .filter((d) => d.kind === "keyword")
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
    .map((d) => fuse(d, latest.get(d.id)));
}

// crawlLabel is the sidebar/detail-friendly name for a keyword crawl, falling
// back to a legible placeholder when the crawl was saved without a name so the
// nav entry stays readable and clickable.
export function crawlLabel(crawl: KeywordCrawl): string {
  return crawl.name.trim() || "Untitled crawl";
}

// Discovery is the single perpetual discovery run the dashboard centers on: the
// discovery definition plus its latest run. Null when no discovery crawl exists.
export type Discovery = {
  definition: Definition;
  runId: string | null;
  status: DisplayStatus;
  pagesCrawled: number;
  frontierSize: number;
  startedAt: string | null;
};

// buildDiscovery fuses the single discovery definition with its latest run.
// ADR-0017 guarantees at most one discovery definition, so no tie-break over
// several is needed. Null when no discovery crawl exists.
export function buildDiscovery(defs: Definition[], runs: Run[]): Discovery | null {
  const definition = defs.find((d) => d.kind === "discovery");
  if (!definition) return null;
  const run = latestRunByDefinition(runs).get(definition.id);
  return {
    definition,
    runId: run?.id ?? null,
    status: run ? run.status : "idle",
    pagesCrawled: run?.pagesCrawled ?? 0,
    frontierSize: run?.frontierSize ?? 0,
    startedAt: run?.startedAt ?? null,
  };
}

// --- Catalog derivations ---

// atsSplit partitions catalogued companies into ATS-hosted vs self-hosted (an
// empty atsProvider means self-hosted) and rounds the percentages to whole
// numbers that sum to 100.
export function atsSplit(companies: Company[]): {
  atsCount: number;
  selfCount: number;
  atsPct: number;
  selfPct: number;
} {
  const total = companies.length;
  const atsCount = companies.filter((c) => c.atsProvider !== "").length;
  const selfCount = total - atsCount;
  if (total === 0) return { atsCount: 0, selfCount: 0, atsPct: 0, selfPct: 0 };
  const atsPct = Math.round((atsCount / total) * 100);
  return { atsCount, selfCount, atsPct, selfPct: 100 - atsPct };
}

// careerPagesByCompany groups catalogued career pages by company id so the
// catalog can list a company's pages inline when its row is expanded. Pages
// keep their input order within each group.
export function careerPagesByCompany(pages: CareerPage[]): Map<string, CareerPage[]> {
  const byCompany = new Map<string, CareerPage[]>();
  for (const p of pages) {
    const list = byCompany.get(p.companyId);
    if (list) list.push(p);
    else byCompany.set(p.companyId, [p]);
  }
  return byCompany;
}

// RecentPage is a career page joined to its company for the "Recently
// catalogued" feed.
export type RecentPage = {
  id: string;
  company: string;
  url: string;
  firstSeen: string;
  isAts: boolean;
};

// recentlyCatalogued returns the most-recently-catalogued career pages (joined
// to their company), newest first, capped at `limit`. "Catalogued" is when a
// page was first added, so it orders by firstSeen — matching the ADR-0012
// growth curve — not lastSeen, which a re-crawl bumps. Career pages whose
// company is missing from the catalog are skipped rather than shown headless.
export function recentlyCatalogued(
  pages: CareerPage[],
  companiesById: Map<string, Company>,
  limit: number,
): RecentPage[] {
  return [...pages]
    .sort((a, b) => Date.parse(b.firstSeen) - Date.parse(a.firstSeen))
    .map((p) => {
      const company = companiesById.get(p.companyId);
      if (!company) return null;
      return {
        id: p.id,
        company: company.name || company.displayDomain,
        url: p.url,
        firstSeen: p.firstSeen,
        isAts: company.atsProvider !== "",
      };
    })
    .filter((r): r is RecentPage => r !== null)
    .slice(0, limit);
}

// companySource labels a company by how its career pages are hosted: the ATS
// provider name, or "Self-hosted".
export function companySource(company: Company): string {
  return company.atsProvider || "Self-hosted";
}

// NameTier buckets a Company's Name Source into the dashboard's 3-tier confidence
// treatment (ADR-0025): "structured" = the site declared its own name (verified),
// "derived" = the LLM read it or the title yielded it (unverified), "fallback" =
// no real name was found (the domain, or a legacy/unknown NULL source).
export type NameTier = "structured" | "derived" | "fallback";

export function nameTier(company: Company): NameTier {
  switch (company.nameSource) {
    case "jsonld":
    case "meta":
      return "structured";
    case "llm":
    case "title":
      return "derived";
    default: // "domain", "" (NULL/legacy/unknown)
      return "fallback";
  }
}

// verifiedNameShare is the share of catalogued Companies whose name a site
// declared about itself (the "structured" tier) — the Catalog's name-quality
// headline. Percentage rounds to a whole number; 0 for an empty Catalog.
export function verifiedNameShare(companies: Company[]): {
  verifiedCount: number;
  verifiedPct: number;
} {
  const total = companies.length;
  const verifiedCount = companies.filter((c) => nameTier(c) === "structured").length;
  const verifiedPct = total === 0 ? 0 : Math.round((verifiedCount / total) * 100);
  return { verifiedCount, verifiedPct };
}

// nameSourceLabel is the human tooltip text naming the exact Name Ladder rung a
// Company's name came from (ADR-0025), so an analyst sees precisely how the name
// was obtained, not just its tier.
export function nameSourceLabel(source: NameSource): string {
  switch (source) {
    case "jsonld":
      return "Declared in structured data (JSON-LD)";
    case "meta":
      return "Site metadata (og:site_name)";
    case "llm":
      return "Read by the LLM from the page";
    case "title":
      return "Parsed from the page title";
    case "domain":
      return "Domain fallback — no name found";
    default:
      return "Unknown — catalogued before name provenance";
  }
}

// companyInitial buckets a company by the first character of its display name
// (name || displayDomain) for the /catalog A–Z index: an uppercase A–Z letter,
// or "0-9" for digits, symbols, empty names, and non-Latin initials. Diacritics
// are stripped first so "Étude" buckets under "E". The single-char guard keeps a
// letter that uppercases to two chars (e.g. "ß" → "SS") out of a phantom bucket
// the index has no button for.
export function companyInitial(company: Company): string {
  const name = (company.name || company.displayDomain).trim();
  const first = name
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .charAt(0)
    .toUpperCase();
  return first.length === 1 && first >= "A" && first <= "Z" ? first : "0-9";
}
