// BASE is overridable for deployments that serve the API on a different origin;
// by default the SPA and API share an origin (the embedded server).
const BASE = import.meta.env.VITE_API_BASE_URL ?? "/api";

export type RunStatus =
  | "running"
  | "stopping"
  | "pausing"
  | "paused"
  | "stopped"
  | "completed"
  | "failed";

// A run is active while it is still doing work; only active runs are stoppable
// and worth polling for live status. A pausing run is still draining, so it
// keeps its per-run frontier poll alive and its row controls rendered.
export function isActive(status: RunStatus): boolean {
  return (
    status === "running" || status === "stopping" || status === "pausing"
  );
}

// A run is terminal once it has stopped for good: it holds no live Frontier and is
// not worth polling. A paused run is NOT terminal — it keeps a preserved Frontier,
// so it is still polled for its remaining size until a human Resumes or Stops it.
export function isTerminal(status: RunStatus): boolean {
  return status === "stopped" || status === "completed" || status === "failed";
}

export type Run = {
  id: string;
  definitionId: string;
  status: RunStatus;
  pagesCrawled: number;
  listingsFound: number;
  startedAt: string;
  finishedAt: string | null;
  error: string;
};

// Live per-run progress: durable counters plus the transient frontier size.
export type RunStatusSnapshot = {
  pagesCrawled: number;
  listingsFound: number;
  frontierSize: number;
};

export type CrawlKind = "discovery" | "keyword";

export type UrlFilterConfig = {
  allowedTLDs: string[];
  blockedSubdomains: string[];
  blockedPathSegments: string[];
  blockedHostnames: string[];
  passSubdomains: string[];
  passPathSegments: string[];
};

export type Definition = {
  id: string;
  name: string;
  kind: CrawlKind;
  seedUrls: string[];
  keywords: string[];
  maxDepth: number;
  urlFilter: UrlFilterConfig;
  createdAt: string;
};

// Only the fields a user supplies; the server fills depth/urlFilter from
// its configured defaults when omitted.
export type CreateDefinitionRequest = {
  name: string;
  kind: CrawlKind;
  seedUrls?: string[];
  keywords?: string[];
};

export type Company = {
  id: string;
  companyKey: string;
  atsProvider: string;
  displayDomain: string;
  name: string;
  firstSeen: string;
  lastSeen: string;
};

export type CareerPage = {
  id: string;
  companyId: string;
  url: string;
  politenessDomain: string;
  firstSeen: string;
  lastSeen: string;
};

export type Listing = {
  url: string;
  title: string;
  company: string;
  location: string;
  remote: boolean;
  techStack: string[];
  description: string;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error ?? `request failed: ${res.status}`);
  }
  if (res.status === 202 || res.status === 204) {
    return undefined as T;
  }
  return res.json() as Promise<T>;
}

// --- Runs ---

export function listCrawls(): Promise<Run[]> {
  return request<Run[]>("/crawls");
}

export function getRunStatus(id: string): Promise<RunStatusSnapshot> {
  return request<RunStatusSnapshot>(`/crawls/${id}/status`);
}

export function stopCrawl(id: string): Promise<void> {
  return request<void>(`/crawls/${id}/stop`, { method: "POST" });
}

export function pauseCrawl(id: string): Promise<void> {
  return request<void>(`/crawls/${id}/pause`, { method: "POST" });
}

export function resumeCrawl(id: string): Promise<void> {
  return request<void>(`/crawls/${id}/resume`, { method: "POST" });
}

// --- Definitions ---

export function listDefinitions(): Promise<Definition[]> {
  return request<Definition[]>("/definitions");
}

export function getDefinition(id: string): Promise<Definition> {
  return request<Definition>(`/definitions/${id}`);
}

export function createDefinition(
  req: CreateDefinitionRequest,
): Promise<Definition> {
  return request<Definition>("/definitions", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// startRun launches a new run of an existing definition and returns it.
export function startRun(definitionId: string): Promise<Run> {
  return request<Run>(`/definitions/${definitionId}/runs`, { method: "POST" });
}

// --- Catalog + listings ---

export function listCompanies(): Promise<Company[]> {
  return request<Company[]>("/companies");
}

export function listCareerPages(companyId?: string): Promise<CareerPage[]> {
  const q = companyId ? `?companyId=${encodeURIComponent(companyId)}` : "";
  return request<CareerPage[]>(`/career-pages${q}`);
}

export function listListings(params: {
  definitionId: string;
  keyword?: string;
}): Promise<Listing[]> {
  const q = new URLSearchParams({ definitionId: params.definitionId });
  if (params.keyword) q.set("keyword", params.keyword);
  return request<Listing[]>(`/listings?${q.toString()}`);
}
