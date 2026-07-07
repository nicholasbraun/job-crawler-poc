import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createCrawl, listCrawls, stopCrawl, type Run } from "./api";

const CRAWLS_KEY = ["crawls"];

export function App() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: CRAWLS_KEY });

  const crawls = useQuery({
    queryKey: CRAWLS_KEY,
    queryFn: listCrawls,
    refetchInterval: 1500,
  });

  const create = useMutation({
    mutationFn: createCrawl,
    onSuccess: invalidate,
  });

  const stop = useMutation({
    mutationFn: stopCrawl,
    onSuccess: invalidate,
  });

  const [name, setName] = useState("");
  const [seeds, setSeeds] = useState("");

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const seedUrls = seeds
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    if (!name.trim() || seedUrls.length === 0) return;
    create.mutate(
      { name: name.trim(), seedUrls },
      {
        onSuccess: () => {
          setName("");
          setSeeds("");
        },
      },
    );
  };

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 800, margin: "2rem auto", padding: "0 1rem" }}>
      <h1>Job Crawler</h1>

      <section>
        <h2>Start a crawl</h2>
        <form onSubmit={onSubmit} style={{ display: "grid", gap: "0.5rem" }}>
          <input
            placeholder="Crawl name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <textarea
            placeholder="Seed URLs (one per line)"
            rows={4}
            value={seeds}
            onChange={(e) => setSeeds(e.target.value)}
          />
          <button type="submit" disabled={create.isPending}>
            {create.isPending ? "Starting…" : "Start crawl"}
          </button>
          {create.isError && <p style={{ color: "crimson" }}>{(create.error as Error).message}</p>}
        </form>
      </section>

      <section>
        <h2>Runs</h2>
        {crawls.isLoading && <p>Loading…</p>}
        {crawls.isError && <p style={{ color: "crimson" }}>{(crawls.error as Error).message}</p>}
        <table style={{ width: "100%", borderCollapse: "collapse" }}>
          <thead>
            <tr>
              <th style={cell}>Status</th>
              <th style={cell}>Pages</th>
              <th style={cell}>Listings</th>
              <th style={cell}>Started</th>
              <th style={cell}></th>
            </tr>
          </thead>
          <tbody>
            {(crawls.data ?? []).map((run) => (
              <RunRow key={run.id} run={run} onStop={() => stop.mutate(run.id)} stopping={stop.isPending} />
            ))}
          </tbody>
        </table>
      </section>
    </main>
  );
}

const cell: React.CSSProperties = { borderBottom: "1px solid #ddd", textAlign: "left", padding: "0.4rem" };

function RunRow({ run, onStop, stopping }: { run: Run; onStop: () => void; stopping: boolean }) {
  const active = run.status === "running" || run.status === "stopping";
  return (
    <tr>
      <td style={cell}>{run.status}</td>
      <td style={cell}>{run.pagesCrawled}</td>
      <td style={cell}>{run.listingsFound}</td>
      <td style={cell}>{new Date(run.startedAt).toLocaleTimeString()}</td>
      <td style={cell}>
        {active && (
          <button onClick={onStop} disabled={stopping || run.status === "stopping"}>
            Stop
          </button>
        )}
      </td>
    </tr>
  );
}
