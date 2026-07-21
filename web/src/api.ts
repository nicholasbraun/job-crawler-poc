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
  // Live frontier size (queued + in-flight URLs), served on the list/get run
  // endpoints. 0 for terminal runs and on create/start responses (poll to
  // observe a fresh run's frontier fill). See internal/api runDTO.
  frontierSize: number;
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
// its configured defaults when omitted. An omitted maxDepth lets the server
// apply the per-kind default (discovery 10 / keyword 4).
export type CreateDefinitionRequest = {
  name: string;
  kind: CrawlKind;
  seedUrls?: string[];
  keywords?: string[];
  maxDepth?: number;
};

// DefinitionDefaults is the per-kind crawl-modal prefill template from
// GET /api/definitions/defaults. Discovery carries name + seedUrls; keyword
// carries keywords. maxDepth is always present (discovery 10 / keyword 4).
export type DefinitionDefaults = {
  kind: CrawlKind;
  name?: string;
  seedUrls?: string[];
  keywords?: string[];
  maxDepth: number;
};

// NameSource is the Name Ladder rung that produced a Company's name (ADR-0025),
// i.e. how far to trust it. "" = legacy/unknown (catalogued before provenance,
// or an imported row); the server sends it for a SQL NULL. See internal/company.go.
export type NameSource = "jsonld" | "meta" | "llm" | "title" | "domain" | "";

export type Company = {
  id: string;
  companyKey: string;
  atsProvider: string;
  displayDomain: string;
  // Website is the Company's declared homepage, or "" when unknown (Discovery
  // never learns it; only a Catalog Import sets it). See internal/company.go.
  website: string;
  name: string;
  nameSource: NameSource;
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

// CatalogHistory is the Catalog's growth sparkline: a cumulative, daily,
// gap-filled series of catalogued career pages, downsampled server-side. It is
// an object rather than a bare array so a parallel `companies` series can be
// added later without breaking this client.
export type CatalogHistory = {
  careerPages: number[];
};

export type ImportJobStatus = "pending" | "running" | "completed" | "failed";

export type ImportLineError = { line: number; message: string };

// ImportResult is the terminal report of a completed Import Job. For a dry run
// the counters are "would upsert"; for a real import they are what landed.
// `errors` is capped server-side (first ~100); `errorCount` is the true total,
// so the modal shows "…and N more" when errorCount > errors.length.
export type ImportResult = {
  companiesUpserted: number;
  pagesUpserted: number;
  errors: ImportLineError[];
  errorCount: number;
};

// ImportJob mirrors the server's importJobDTO. `result` is null until the job
// completes; `error` holds infrastructure-failure text only for a failed job.
export type ImportJob = {
  id: string;
  status: ImportJobStatus;
  dryRun: boolean;
  filename: string;
  fileSize: number;
  result: ImportResult | null;
  error: string;
  createdAt: string;
  updatedAt: string;
};

// isImportTerminal reports whether an Import Job has stopped: polling ends and
// the result/error view is final.
export function isImportTerminal(status: ImportJobStatus): boolean {
  return status === "completed" || status === "failed";
}

// mintIdempotencyKey returns a fresh key for one import action (ADR-0014: one key
// per user action). crypto.randomUUID needs a secure context (https or
// localhost); the fallback keeps a plain-http LAN deployment working, since the
// key only has to be unique per action.
export function mintIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return `imp-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export type Listing = {
  url: string;
  title: string;
  company: string;
  location: string;
  country: string;
  workArrangement: "remote" | "onsite" | "hybrid" | "unspecified";
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

// createCrawl is the fused create-and-start endpoint: it persists a definition
// and immediately starts a run, atomically (a failed start rolls the definition
// back server-side). It backs the "Create & start" modal action. A keyword
// crawl seeds from the catalog, so it carries keywords but no seedUrls.
export function createCrawl(req: CreateDefinitionRequest): Promise<Run> {
  return request<Run>("/crawls", {
    method: "POST",
    body: JSON.stringify(req),
  });
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

// getDefinitionDefaults fetches a crawl modal's per-kind prefill template
// (baseline seeds/keywords + depth) from the server's configured defaults.
export function getDefinitionDefaults(kind: CrawlKind): Promise<DefinitionDefaults> {
  return request<DefinitionDefaults>(`/definitions/defaults?kind=${encodeURIComponent(kind)}`);
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

// addSeed appends a Seed to the Discovery Definition and injects it into the
// live Frontier at depth 0 (ADR-0018). Discovery-only: the server refuses a
// keyword definition or an invalid/empty URL with a 4xx. Returns the updated
// definition so the caller can reflect the new Seed list.
export function addSeed(definitionId: string, url: string): Promise<Definition> {
  return request<Definition>(`/definitions/${definitionId}/seeds`, {
    method: "POST",
    body: JSON.stringify({ url }),
  });
}

// --- Catalog + listings ---

export function listCompanies(): Promise<Company[]> {
  return request<Company[]>("/companies");
}

export function listCareerPages(companyId?: string): Promise<CareerPage[]> {
  const q = companyId ? `?companyId=${encodeURIComponent(companyId)}` : "";
  return request<CareerPage[]>(`/career-pages${q}`);
}

// getCatalogHistory returns the catalog-growth sparkline series. Its endpoint
// equals the live "career pages catalogued" count (both derive from the same
// data), so the two never drift.
export function getCatalogHistory(): Promise<CatalogHistory> {
  return request<CatalogHistory>("/catalog-history");
}

export function listListings(params: {
  definitionId: string;
  keyword?: string;
}): Promise<Listing[]> {
  const q = new URLSearchParams({ definitionId: params.definitionId });
  if (params.keyword) q.set("keyword", params.keyword);
  return request<Listing[]>(`/listings?${q.toString()}`);
}

// submitImport posts a catalog file as multipart and starts an asynchronous
// Import Job. The Idempotency-Key makes a network retry return the original job
// instead of duplicating it (ADR-0014). Both a fresh 202 and an idempotent 200
// replay carry the job DTO in the body; a 422 (key reused with a different file
// or dry-run flag) and any other non-2xx surface as a thrown Error. This bypasses
// the shared `request` helper because it needs multipart (no JSON content-type),
// a custom header, and the 202/200 body.
export async function submitImport(params: {
  file: File;
  dryRun: boolean;
  idempotencyKey: string;
}): Promise<ImportJob> {
  const form = new FormData();
  form.append("file", params.file);
  const q = params.dryRun ? "?dryRun=true" : "";
  const res = await fetch(`${BASE}/catalog/import${q}`, {
    method: "POST",
    headers: { "Idempotency-Key": params.idempotencyKey },
    body: form,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error ?? `request failed: ${res.status}`);
  }
  return res.json() as Promise<ImportJob>;
}

export function getImportJob(id: string): Promise<ImportJob> {
  return request<ImportJob>(`/catalog/import-jobs/${id}`);
}

export function listImportJobs(): Promise<ImportJob[]> {
  return request<ImportJob[]>("/catalog/import-jobs");
}
