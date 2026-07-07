// BASE is overridable for deployments that serve the API on a different origin;
// by default the SPA and API share an origin (the embedded server).
const BASE = import.meta.env.VITE_API_BASE_URL ?? "/api";

export type Run = {
  id: string;
  definitionId: string;
  status: "running" | "stopping" | "stopped" | "completed" | "failed";
  pagesCrawled: number;
  listingsFound: number;
  startedAt: string;
  finishedAt: string | null;
  error: string;
};

export type CreateCrawlRequest = {
  name: string;
  seedUrls: string[];
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

export function listCrawls(): Promise<Run[]> {
  return request<Run[]>("/crawls");
}

export function createCrawl(req: CreateCrawlRequest): Promise<Run> {
  return request<Run>("/crawls", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export function stopCrawl(id: string): Promise<void> {
  return request<void>(`/crawls/${id}/stop`, { method: "POST" });
}
