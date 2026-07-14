// View-model builders that reconcile the API's normalized shape (definitions,
// runs, companies, career pages — each fetched separately) into the fused
// entities the dashboard renders. The design treats a "crawl" as a definition
// married to its latest run; the catalog panels need company/career-page joins.

import type { CareerPage, Company, Definition, Run } from "../api";
import type { DisplayStatus } from "./status";

// latestRunByDefinition reduces the flat run list to each definition's most
// recent run (by startedAt). A definition with no runs is simply absent.
export function latestRunByDefinition(runs: Run[]): Map<string, Run> {
  const latest = new Map<string, Run>();
  for (const run of runs) {
    const prev = latest.get(run.definitionId);
    if (!prev || run.startedAt > prev.startedAt) {
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
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
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

// buildDiscovery picks the most-recently-started discovery run. The design
// assumes one perpetual discovery crawl; if several definitions exist we take
// the one with the newest run (falling back to the newest definition).
export function buildDiscovery(defs: Definition[], runs: Run[]): Discovery | null {
  const discoveryDefs = defs.filter((d) => d.kind === "discovery");
  if (discoveryDefs.length === 0) return null;
  const latest = latestRunByDefinition(runs);

  let best: { def: Definition; run: Run | undefined } | null = null;
  for (const def of discoveryDefs) {
    const run = latest.get(def.id);
    if (!best) {
      best = { def, run };
      continue;
    }
    const bestStart = best.run?.startedAt ?? best.def.createdAt;
    const thisStart = run?.startedAt ?? def.createdAt;
    if (thisStart > bestStart) best = { def, run };
  }
  if (!best) return null;

  return {
    definition: best.def,
    runId: best.run?.id ?? null,
    status: best.run ? best.run.status : "idle",
    pagesCrawled: best.run?.pagesCrawled ?? 0,
    frontierSize: best.run?.frontierSize ?? 0,
    startedAt: best.run?.startedAt ?? null,
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
    .sort((a, b) => b.firstSeen.localeCompare(a.firstSeen))
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
