import { useDefinitions, useIsMobile, useRuns } from "../hooks";
import { buildKeywordCrawls } from "../lib/model";
import { CrawlCard } from "../components/CrawlCard";
import { PageShell } from "../components/PageShell";
import { EmptyState, ErrorNote, Loading } from "../components/primitives";

export function CrawlsPage() {
  const runs = useRuns();
  const definitions = useDefinitions();

  const crawls = buildKeywordCrawls(definitions.data ?? [], runs.data ?? []);
  const running = crawls.filter((c) => c.status === "running").length;
  const loading = definitions.isLoading || runs.isLoading;
  const error = definitions.error ?? runs.error;
  const isMobile = useIsMobile();

  return (
    <PageShell
      title="Keyword crawls"
      subtitle={`${crawls.length} definitions · ${running} running`}
    >
      {error ? (
        <ErrorNote error={error} />
      ) : loading ? (
        <Loading />
      ) : crawls.length === 0 ? (
        <EmptyState
          icon="ph-magnifying-glass"
          title="No keyword crawls yet"
          hint="A keyword crawl seeds from the catalogued career pages and gates them by your keywords. Create one from the header."
        />
      ) : (
        <div style={{ display: "grid", gridTemplateColumns: isMobile ? "minmax(0, 1fr)" : "repeat(2, minmax(0, 1fr))", gap: "var(--space-4)" }}>
          {crawls.map((c) => (
            <CrawlCard key={c.definitionId} crawl={c} />
          ))}
        </div>
      )}
    </PageShell>
  );
}
