import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createDefinition,
  listDefinitions,
  startRun,
  type CrawlKind,
  type Definition,
} from "../api";
import {
  Button,
  Card,
  formatTime,
  KindBadge,
  PageHeader,
  QueryState,
} from "../components/ui";

const DEFINITIONS_KEY = ["definitions"];

export function DefinitionsPage() {
  const queryClient = useQueryClient();

  const definitions = useQuery({
    queryKey: DEFINITIONS_KEY,
    queryFn: listDefinitions,
  });

  const start = useMutation({
    mutationFn: startRun,
    // A new run should show up on the Runs tab immediately.
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["crawls"] }),
  });

  const defs = definitions.data ?? [];

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_360px]">
      <div>
        <PageHeader
          title="Definitions"
          subtitle="A re-runnable library of crawl specifications."
        />
        <Card title="Definitions">
          <QueryState
            isLoading={definitions.isLoading}
            error={definitions.error}
            isEmpty={defs.length === 0}
            emptyMessage="No definitions yet. Create one on the right."
          >
            <ul className="divide-y divide-slate-100">
              {defs.map((def) => (
                <DefinitionRow
                  key={def.id}
                  definition={def}
                  onStart={() => start.mutate(def.id)}
                  starting={start.isPending}
                />
              ))}
            </ul>
            {start.isError && (
              <p className="mt-3 text-sm text-red-600">
                {(start.error as Error).message}
              </p>
            )}
          </QueryState>
        </Card>
      </div>

      <div>
        <PageHeader title="New definition" />
        <CreateDefinitionForm />
      </div>
    </div>
  );
}

function DefinitionRow({
  definition,
  onStart,
  starting,
}: {
  definition: Definition;
  onStart: () => void;
  starting: boolean;
}) {
  return (
    <li className="flex items-center justify-between gap-4 py-3">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="font-medium text-slate-800">{definition.name}</span>
          <KindBadge kind={definition.kind} />
        </div>
        {definition.kind === "keyword" ? (
          <div
            className="truncate text-xs text-slate-400"
            title={definition.keywords.join(", ")}
          >
            {definition.keywords.join(", ") || "—"}
          </div>
        ) : (
          <ul className="mt-1 grid gap-0.5">
            {definition.seedUrls.map((url) => (
              <li
                key={url}
                className="truncate font-mono text-xs text-slate-400"
                title={url}
              >
                {url}
              </li>
            ))}
            {definition.seedUrls.length === 0 && (
              <li className="text-xs text-slate-400">—</li>
            )}
          </ul>
        )}
        <div className="mt-1 text-xs text-slate-400">
          created {formatTime(definition.createdAt)}
        </div>
      </div>
      <Button onClick={onStart} disabled={starting}>
        {starting ? "Starting…" : "Start run"}
      </Button>
    </li>
  );
}

function CreateDefinitionForm() {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<CrawlKind>("discovery");
  const [seeds, setSeeds] = useState("");
  const [keywords, setKeywords] = useState("");

  const create = useMutation({
    mutationFn: createDefinition,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: DEFINITIONS_KEY });
      setName("");
      setSeeds("");
      setKeywords("");
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const seedUrls = splitLines(seeds);
    const keywordList = splitList(keywords);
    if (!name.trim()) return;
    if (kind === "discovery" && seedUrls.length === 0) return;
    if (kind === "keyword" && keywordList.length === 0) return;
    create.mutate({
      name: name.trim(),
      kind,
      seedUrls: kind === "discovery" ? seedUrls : undefined,
      keywords: kind === "keyword" ? keywordList : undefined,
    });
  };

  return (
    <Card>
      <form onSubmit={onSubmit} className="grid gap-3">
        <label className="grid gap-1 text-sm">
          <span className="font-medium text-slate-600">Name</span>
          <input
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
            placeholder="e.g. Berlin Go jobs"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>

        <label className="grid gap-1 text-sm">
          <span className="font-medium text-slate-600">Kind</span>
          <select
            className="rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
            value={kind}
            onChange={(e) => setKind(e.target.value as CrawlKind)}
          >
            <option value="discovery">discovery — fill the Catalog</option>
            <option value="keyword">keyword — extract job listings</option>
          </select>
        </label>

        {kind === "discovery" ? (
          <label className="grid gap-1 text-sm">
            <span className="font-medium text-slate-600">
              Seed URLs (one per line)
            </span>
            <textarea
              className="rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
              rows={4}
              placeholder="https://example.com"
              value={seeds}
              onChange={(e) => setSeeds(e.target.value)}
            />
          </label>
        ) : (
          <label className="grid gap-1 text-sm">
            <span className="font-medium text-slate-600">
              Keywords (comma or newline separated)
            </span>
            <textarea
              className="rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
              rows={4}
              placeholder="golang, backend, kubernetes"
              value={keywords}
              onChange={(e) => setKeywords(e.target.value)}
            />
            <span className="text-xs text-slate-400">
              A keyword crawl seeds itself from the Catalog; no URLs needed.
            </span>
          </label>
        )}

        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "Creating…" : "Create definition"}
        </Button>
        {create.isError && (
          <p className="text-sm text-red-600">
            {(create.error as Error).message}
          </p>
        )}
      </form>
    </Card>
  );
}

function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
}

function splitList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}
