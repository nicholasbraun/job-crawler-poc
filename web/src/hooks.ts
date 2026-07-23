import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  addSeed,
  createCrawl,
  createSavedSearch,
  deleteSavedSearch,
  getCatalogHistory,
  getDefinitionDefaults,
  getImportJob,
  getListingStats,
  getSavedSearchResults,
  isImportTerminal,
  listCareerPages,
  listCompanies,
  listCrawls,
  listDefinitions,
  listImportJobs,
  listRecentListings,
  listSavedSearches,
  pauseCrawl,
  renameSavedSearch,
  resumeCrawl,
  startRun,
  stopCrawl,
  submitImport,
} from "./api";
import type { CrawlKind, ImportJob } from "./api";

// Live-ish polling cadence for the run list; catalog/definition data changes
// slowly, so it polls lazily. The run list now carries frontierSize inline
// (backend enrichment), so cards read the live frontier without an extra poll.
const RUNS_POLL_MS = 2000;
const CATALOG_POLL_MS = 8000;
// Recent-imports list refresh while the modal is open. The active job is polled
// faster (see useImportJob); this only needs to catch externally-changed jobs
// (a concurrent submission, a boot-time restart sweep). Completion also
// invalidates it directly, so it can afford to be lazy.
const IMPORT_JOBS_POLL_MS = 4000;
// SavedSearch panels are a live pull over the Corpus (ADR-0037): the Corpus only
// moves on a Collection Cycle, so a lazy cadence keeps each panel current without
// hammering the query endpoint.
const SEARCHES_POLL_MS = 15000;
// The Overview's "recently found" feed tracks a live Collection Cycle, so it polls
// on the run cadence rather than the lazy searches cadence.
const RECENT_LISTINGS_POLL_MS = 5000;

// MOBILE_QUERY is the shared phone-portrait breakpoint: below 640px CSS width
// the dashboard switches to its mobile layout (drawer nav, stacked grids). It
// catches every iPhone portrait width plus small tablets.
export const MOBILE_QUERY = "(max-width: 640px)";

// useMediaQuery tracks a CSS media query and re-renders when it flips. It reads
// the initial value synchronously from matchMedia so the very first paint
// already matches the viewport (no desktop-then-mobile flash).
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mql = window.matchMedia(query);
    const onChange = () => setMatches(mql.matches);
    onChange();
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

// useIsMobile is the dashboard's one phone-portrait check, so every component
// branches on the same breakpoint.
export function useIsMobile(): boolean {
  return useMediaQuery(MOBILE_QUERY);
}

export const keys = {
  crawls: ["crawls"] as const,
  definitions: ["definitions"] as const,
  companies: ["companies"] as const,
  careerPages: ["career-pages"] as const,
  catalogHistory: ["catalog-history"] as const,
  importJobs: ["import-jobs"] as const,
  importJob: (id: string) => ["import-job", id] as const,
  definitionDefaults: (kind: CrawlKind) => ["definition-defaults", kind] as const,
  savedSearches: ["saved-searches"] as const,
  savedSearchResults: (id: string) => ["saved-search-results", id] as const,
  recentListings: (limit: number) => ["recent-listings", limit] as const,
  listingStats: ["listing-stats"] as const,
};

export function useRuns() {
  return useQuery({ queryKey: keys.crawls, queryFn: listCrawls, refetchInterval: RUNS_POLL_MS });
}

export function useDefinitions() {
  return useQuery({ queryKey: keys.definitions, queryFn: listDefinitions, refetchInterval: CATALOG_POLL_MS });
}

// useDefinitionDefaults fetches a crawl modal's per-kind prefill template
// (seeds/keywords + depth). Defaults are static config, so it never refetches;
// `enabled` lets a modal fetch only while open.
export function useDefinitionDefaults(kind: CrawlKind, enabled = true) {
  return useQuery({
    queryKey: keys.definitionDefaults(kind),
    queryFn: () => getDefinitionDefaults(kind),
    enabled,
    staleTime: Infinity,
  });
}

export function useCompanies() {
  return useQuery({ queryKey: keys.companies, queryFn: listCompanies, refetchInterval: CATALOG_POLL_MS });
}

export function useCareerPages() {
  return useQuery({ queryKey: keys.careerPages, queryFn: () => listCareerPages(), refetchInterval: CATALOG_POLL_MS });
}

// useCatalogHistory polls the catalog-growth sparkline series. It shares the
// catalog cadence since the curve only moves as new pages are catalogued.
export function useCatalogHistory() {
  return useQuery({ queryKey: keys.catalogHistory, queryFn: getCatalogHistory, refetchInterval: CATALOG_POLL_MS });
}

// useImportJobs lists recent Import Jobs for the modal history, newest first.
// `enabled` is the modal's open state so it polls only while visible. The list
// endpoint returns every job, so jobs from before a server restart (swept to
// failed) still appear.
export function useImportJobs(enabled: boolean) {
  return useQuery({
    queryKey: keys.importJobs,
    queryFn: listImportJobs,
    enabled,
    refetchInterval: IMPORT_JOBS_POLL_MS,
  });
}

// useImportJob polls one Import Job at ~1 s while it is live and stops once it
// reaches a terminal state (completed/failed), so a finished job is not polled.
export function useImportJob(jobId: string | null) {
  return useQuery({
    queryKey: keys.importJob(jobId ?? ""),
    queryFn: () => getImportJob(jobId as string),
    enabled: !!jobId,
    refetchInterval: (query) => {
      const job = query.state.data as ImportJob | undefined;
      return job && isImportTerminal(job.status) ? false : 1000;
    },
  });
}

// useSubmitImport posts a catalog file and starts an Import Job, invalidating the
// recent-imports list so a just-started job appears immediately. The caller mints
// the idempotency key (one per user action) and, on success, adopts the returned
// job id for polling.
export function useSubmitImport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: submitImport,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.importJobs });
    },
  });
}

// CrawlAction is the lifecycle verb a control fires. pause/resume/stop target a
// run id; rerun starts a new run of a definition.
export type CrawlAction = "pause" | "resume" | "stop" | "rerun";

// useCrawlControls exposes the four lifecycle mutations, each invalidating the
// run list on success so the affected card reflects its new state immediately.
export function useCrawlControls() {
  const qc = useQueryClient();
  const invalidate = () => qc.invalidateQueries({ queryKey: keys.crawls });
  const pause = useMutation({ mutationFn: pauseCrawl, onSuccess: invalidate });
  const resume = useMutation({ mutationFn: resumeCrawl, onSuccess: invalidate });
  const stop = useMutation({ mutationFn: stopCrawl, onSuccess: invalidate });
  const rerun = useMutation({ mutationFn: startRun, onSuccess: invalidate });

  // dispatch routes an action to the right mutation with the right id. rerun
  // needs the definition id; the others need the run id (a no-op if absent).
  function dispatch(action: CrawlAction, ids: { runId: string | null; definitionId: string }) {
    if (action === "rerun") return rerun.mutate(ids.definitionId);
    if (!ids.runId) return;
    if (action === "pause") return pause.mutate(ids.runId);
    if (action === "resume") return resume.mutate(ids.runId);
    if (action === "stop") return stop.mutate(ids.runId);
  }

  const pending = pause.isPending || resume.isPending || stop.isPending || rerun.isPending;
  return { dispatch, pending };
}

export function useCreateCrawl() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: createCrawl,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.crawls });
      qc.invalidateQueries({ queryKey: keys.definitions });
    },
  });
}

// useAddSeed appends a Seed to the running Discovery Crawl and invalidates the
// definitions query so the Seed list re-renders with the new URL.
export function useAddSeed() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ definitionId, url }: { definitionId: string; url: string }) =>
      addSeed(definitionId, url),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.definitions }),
  });
}

// --- Saved searches ---

// useSavedSearches lists the saved searches backing the panels, polling lazily so
// a search created in another tab appears.
export function useSavedSearches() {
  return useQuery({
    queryKey: keys.savedSearches,
    queryFn: listSavedSearches,
    refetchInterval: SEARCHES_POLL_MS,
  });
}

// useSavedSearchResults is the live pull backing one panel: it re-runs the search
// against the Corpus on the shared searches cadence.
export function useSavedSearchResults(id: string) {
  return useQuery({
    queryKey: keys.savedSearchResults(id),
    queryFn: () => getSavedSearchResults(id),
    refetchInterval: SEARCHES_POLL_MS,
  });
}

// useRecentListings polls the newly-discovered corpus listings for the Overview's
// live collection feed, on the run cadence so it tracks a running Cycle.
export function useRecentListings(limit = 12) {
  return useQuery({
    queryKey: keys.recentListings(limit),
    queryFn: () => listRecentListings(limit),
    refetchInterval: RECENT_LISTINGS_POLL_MS,
  });
}

// useListingStats polls the true corpus size (distinct open/total rows) for the
// collection headline, tracking a running Cycle on the same live cadence.
export function useListingStats() {
  return useQuery({
    queryKey: keys.listingStats,
    queryFn: getListingStats,
    refetchInterval: RECENT_LISTINGS_POLL_MS,
  });
}

export function useCreateSavedSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: createSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.savedSearches }),
  });
}

export function useRenameSavedSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => renameSavedSearch(id, name),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.savedSearches }),
  });
}

export function useDeleteSavedSearch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: deleteSavedSearch,
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.savedSearches }),
  });
}
