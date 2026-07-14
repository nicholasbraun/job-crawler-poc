import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  createCrawl,
  getCatalogHistory,
  listCareerPages,
  listCompanies,
  listCrawls,
  listDefinitions,
  listListings,
  pauseCrawl,
  resumeCrawl,
  startRun,
  stopCrawl,
} from "./api";

// Live-ish polling cadence for the run list; catalog/definition data changes
// slowly, so it polls lazily. The run list now carries frontierSize inline
// (backend enrichment), so cards read the live frontier without an extra poll.
const RUNS_POLL_MS = 2000;
const CATALOG_POLL_MS = 8000;

export const keys = {
  crawls: ["crawls"] as const,
  definitions: ["definitions"] as const,
  companies: ["companies"] as const,
  careerPages: ["career-pages"] as const,
  catalogHistory: ["catalog-history"] as const,
  listings: (definitionId: string, keyword: string) =>
    ["listings", definitionId, keyword] as const,
};

export function useRuns() {
  return useQuery({ queryKey: keys.crawls, queryFn: listCrawls, refetchInterval: RUNS_POLL_MS });
}

export function useDefinitions() {
  return useQuery({ queryKey: keys.definitions, queryFn: listDefinitions, refetchInterval: CATALOG_POLL_MS });
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

export function useListings(definitionId: string, keyword: string) {
  return useQuery({
    queryKey: keys.listings(definitionId, keyword),
    queryFn: () => listListings({ definitionId, keyword: keyword || undefined }),
    refetchInterval: RUNS_POLL_MS,
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
