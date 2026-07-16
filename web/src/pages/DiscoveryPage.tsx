import { useCareerPages, useCompanies, useDefinitions, useRuns } from "../hooks";
import { fmt, prettyUrl, relativeTime } from "../lib/format";
import { buildDiscovery, type Discovery } from "../lib/model";
import { useLayout } from "../components/Layout";
import { PageShell } from "../components/PageShell";
import { Dot, EmptyState, Icon, Loading, RunControls, StatCard } from "../components/primitives";
import { statusMeta } from "../lib/status";

export function DiscoveryPage() {
  const runs = useRuns();
  const definitions = useDefinitions();
  const companiesQ = useCompanies();
  const pagesQ = useCareerPages();
  const { openStartDiscovery } = useLayout();

  const discovery = buildDiscovery(definitions.data ?? [], runs.data ?? []);

  if (definitions.isLoading || runs.isLoading) {
    return (
      <PageShell title="Discovery" back={{ to: "/", label: "Overview" }}>
        <Loading />
      </PageShell>
    );
  }

  if (!discovery) {
    return (
      <PageShell title="Discovery" back={{ to: "/", label: "Overview" }}>
        <EmptyState
          icon="ph-broadcast"
          title="No discovery run"
          hint="The perpetual discovery crawl walks the seed domains to build the catalog. Start one to begin cataloguing companies and career pages."
          action={
            <button className="btn btn-primary" onClick={openStartDiscovery}>
              <Icon name="ph-play" size={14} /> Start discovery
            </button>
          }
        />
      </PageShell>
    );
  }

  return (
    <PageShell
      title="Discovery"
      subtitle="Perpetual background crawl — finds career pages and builds the catalog"
      back={{ to: "/", label: "Overview" }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: "var(--space-4)" }}>
          <StatCard size="md" label="Pages crawled" value={fmt(discovery.pagesCrawled)} sub={`since ${relativeTime(discovery.startedAt)}`} />
          <StatCard size="md" label="Frontier size" value={fmt(discovery.frontierSize)} sub="URLs queued + in-flight" />
          <StatCard size="md" label="Career pages" value={fmt(pagesQ.data?.length ?? 0)} sub="catalogued (hits)" />
          <StatCard size="md" label="Companies" value={fmt(companiesQ.data?.length ?? 0)} sub="ATS-aware identity" />
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "1fr 300px", gap: "var(--space-4)", alignItems: "start" }}>
          <SeedDomains discovery={discovery} />
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
            <RunControlCard discovery={discovery} />
            <DefinitionCard discovery={discovery} />
          </div>
        </div>
      </div>
    </PageShell>
  );
}

function SeedDomains({ discovery }: { discovery: Discovery }) {
  const seeds = discovery.definition.seedUrls;
  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <h4 style={{ margin: 0, fontSize: 17 }}>Seed domains</h4>
      <p style={{ margin: 0, fontSize: 13, color: "var(--color-neutral-400)" }}>
        The discovery run walks these seeds with a perpetual frontier, following the URL filters toward career pages.
        Confirmed hits are attributed to a company (ATS-aware) and catalogued as career pages — the seed set every keyword
        crawl draws from.
      </p>
      <div style={{ overflowX: "auto" }}>
        <table className="table">
          <thead>
            <tr>
              <th>Seed domain</th>
              <th style={{ textAlign: "right" }}>Career pages</th>
              <th style={{ textAlign: "right" }}>Frontier</th>
              <th style={{ textAlign: "right" }}>Last hit</th>
            </tr>
          </thead>
          <tbody>
            {seeds.map((seed) => (
              <tr key={seed}>
                <td style={{ fontSize: 13 }}>{prettyUrl(seed)}</td>
                <td style={{ textAlign: "right", color: "var(--color-neutral-600)" }}>—</td>
                <td style={{ textAlign: "right", color: "var(--color-neutral-600)" }}>—</td>
                <td style={{ textAlign: "right", color: "var(--color-neutral-600)", fontSize: 12 }}>—</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 7, fontSize: 11, color: "var(--color-neutral-500)" }}>
        <Icon name="ph-info" size={12} color="var(--color-neutral-500)" />
        Per-seed attribution isn't tracked yet — the totals above are run-level. (Deferred backend feature.)
      </div>
    </div>
  );
}

function RunControlCard({ discovery }: { discovery: Discovery }) {
  const meta = statusMeta(discovery.status);
  return (
    <div className="card elev-sm" style={{ gap: "var(--space-3)", padding: "var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <Dot color={meta.dot} glow />
        <h4 style={{ margin: 0, fontSize: 16 }}>Run control</h4>
      </div>
      <p style={{ margin: 0, fontSize: 13, color: "var(--color-neutral-400)" }}>
        Pause parks the run and preserves its frontier in Redis; Resume rebuilds the engine and continues from the saved
        counters. Stop is terminal.
      </p>
      <RunControls
        status={discovery.status}
        runId={discovery.runId}
        definitionId={discovery.definition.id}
        block
        stopLabel="Stop run"
        primarySuffix=" run"
      />
    </div>
  );
}

function DefinitionCard({ discovery }: { discovery: Discovery }) {
  const def = discovery.definition;
  const rows = [
    { label: "Kind", value: def.kind },
    { label: "Seed domains", value: fmt(def.seedUrls.length) },
    { label: "Max depth", value: fmt(def.maxDepth) },
  ];
  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <h4 style={{ margin: 0, fontSize: 16 }}>Definition</h4>
      {rows.map((r) => (
        <div
          key={r.label}
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            fontSize: 13,
            padding: "5px 0",
            boxShadow: "0 1px 0 var(--color-divider)",
          }}
        >
          <span style={{ color: "var(--color-neutral-300)" }}>{r.label}</span>
          <span style={{ color: "var(--color-neutral-100)", fontVariantNumeric: "tabular-nums" }}>{r.value}</span>
        </div>
      ))}
    </div>
  );
}
