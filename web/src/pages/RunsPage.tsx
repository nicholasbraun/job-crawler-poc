import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getRunStatus,
  isActive,
  isTerminal,
  listCrawls,
  listDefinitions,
  pauseCrawl,
  resumeCrawl,
  stopCrawl,
  type Definition,
  type Run,
} from "../api";
import {
  Button,
  Card,
  formatTime,
  PageHeader,
  QueryState,
  StatusBadge,
} from "../components/ui";

const CRAWLS_KEY = ["crawls"];

export function RunsPage() {
  const queryClient = useQueryClient();

  const crawls = useQuery({
    queryKey: CRAWLS_KEY,
    queryFn: listCrawls,
    refetchInterval: 1500,
  });

  // Definitions rarely change; fetched once so each run can show its name/kind.
  const definitions = useQuery({
    queryKey: ["definitions"],
    queryFn: listDefinitions,
  });

  const stop = useMutation({
    mutationFn: stopCrawl,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CRAWLS_KEY }),
  });

  const pause = useMutation({
    mutationFn: pauseCrawl,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CRAWLS_KEY }),
  });

  const resume = useMutation({
    mutationFn: resumeCrawl,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: CRAWLS_KEY }),
  });

  const defsById = new Map<string, Definition>(
    (definitions.data ?? []).map((d) => [d.id, d]),
  );

  const runs = crawls.data ?? [];

  return (
    <div>
      <PageHeader
        title="Runs"
        subtitle="Live crawl executions. Frontier size updates while a run is active."
      />
      <Card title="Runs">
        <QueryState
          isLoading={crawls.isLoading}
          error={crawls.error}
          isEmpty={runs.length === 0}
          emptyMessage="No runs yet. Start one from the Definitions tab."
        >
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs tracking-wide text-slate-500 uppercase">
                  <th className="px-3 py-2">Definition</th>
                  <th className="px-3 py-2">Status</th>
                  <th className="px-3 py-2 text-right">Pages</th>
                  <th className="px-3 py-2 text-right">Listings</th>
                  <th className="px-3 py-2 text-right">Frontier</th>
                  <th className="px-3 py-2">Started</th>
                  <th className="px-3 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => (
                  <RunRow
                    key={run.id}
                    run={run}
                    definition={defsById.get(run.definitionId)}
                    onStop={() => stop.mutate(run.id)}
                    stopping={stop.isPending}
                    onPause={() => pause.mutate(run.id)}
                    pausing={pause.isPending}
                    onResume={() => resume.mutate(run.id)}
                    resuming={resume.isPending}
                  />
                ))}
              </tbody>
            </table>
          </div>
        </QueryState>
      </Card>
    </div>
  );
}

function RunRow({
  run,
  definition,
  onStop,
  stopping,
  onPause,
  pausing,
  onResume,
  resuming,
}: {
  run: Run;
  definition?: Definition;
  onStop: () => void;
  stopping: boolean;
  onPause: () => void;
  pausing: boolean;
  onResume: () => void;
  resuming: boolean;
}) {
  const active = isActive(run.status);
  // A paused run keeps a preserved Frontier, so poll it too; only terminal runs
  // have no live Frontier worth showing.
  const polling = !isTerminal(run.status);

  // Only failed runs carry a diagnostic message worth surfacing.
  const failure = run.status === "failed" && run.error ? run.error : null;

  // Non-terminal runs (including paused) have a live/preserved frontier; polling
  // stops once terminal.
  const status = useQuery({
    queryKey: ["run-status", run.id],
    queryFn: () => getRunStatus(run.id),
    refetchInterval: 1500,
    enabled: polling,
  });

  return (
    <tr className="border-b border-slate-100 last:border-0">
      <td className="px-3 py-2">
        <div className="font-medium text-slate-800">
          {definition?.name ?? "—"}
        </div>
        <div className="text-xs text-slate-400">{definition?.kind ?? ""}</div>
      </td>
      <td className="px-3 py-2">
        <StatusBadge status={run.status} />
        {failure && (
          <div
            className="mt-1 max-w-xs truncate text-xs text-rose-600"
            title={failure}
            aria-label={`Failure reason: ${failure}`}
          >
            {failure}
          </div>
        )}
      </td>
      <td className="px-3 py-2 text-right tabular-nums">{run.pagesCrawled}</td>
      <td className="px-3 py-2 text-right tabular-nums">{run.listingsFound}</td>
      <td className="px-3 py-2 text-right tabular-nums text-slate-600">
        {polling ? (status.data?.frontierSize ?? "…") : "—"}
      </td>
      <td className="px-3 py-2 text-slate-500">{formatTime(run.startedAt)}</td>
      <td className="px-3 py-2">
        <div className="flex justify-end gap-2">
          {run.status === "running" && (
            <Button variant="secondary" onClick={onPause} disabled={pausing}>
              Pause
            </Button>
          )}
          {run.status === "pausing" && (
            <Button variant="secondary" disabled>
              Pausing…
            </Button>
          )}
          {run.status === "paused" && (
            <Button variant="primary" onClick={onResume} disabled={resuming}>
              Resume
            </Button>
          )}
          {/* A paused run is also stoppable (paused → stopped, terminal, cleans
              the Frontier), so it shows Stop alongside Resume. */}
          {(active || run.status === "paused") && (
            <Button
              variant="danger"
              onClick={onStop}
              disabled={stopping || run.status === "stopping"}
            >
              Stop
            </Button>
          )}
        </div>
      </td>
    </tr>
  );
}
