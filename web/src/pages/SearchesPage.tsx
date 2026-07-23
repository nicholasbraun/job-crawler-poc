import { useState } from "react";
import { Link } from "react-router-dom";

import type { SavedSearch, WorkArrangement } from "../api";
import { useIsMobile, useSavedSearches, useSavedSearchResults } from "../hooks";
import { PageShell } from "../components/PageShell";
import { SavedSearchModal } from "../components/SavedSearchModal";
import { EmptyState, ErrorNote, Icon, Loading } from "../components/primitives";

// ARRANGEMENT_LABELS renders the enum values as readable badge text.
export const ARRANGEMENT_LABELS: Record<WorkArrangement, string> = {
  remote: "Remote",
  onsite: "Onsite",
  hybrid: "Hybrid",
  unspecified: "Unspecified",
};

export function SearchesPage() {
  const searchesQ = useSavedSearches();
  const isMobile = useIsMobile();
  const [modalOpen, setModalOpen] = useState(false);

  const searches = searchesQ.data ?? [];

  const newButton = (
    <button className="btn btn-primary" onClick={() => setModalOpen(true)}>
      <Icon name="ph-plus" size={14} /> New search
    </button>
  );

  return (
    <PageShell
      title="Searches"
      subtitle="Live panels over the collected corpus"
      back={{ to: "/", label: "Overview" }}
      actions={newButton}
    >
      {searchesQ.error ? (
        <ErrorNote error={searchesQ.error} />
      ) : searchesQ.isLoading ? (
        <Loading />
      ) : searches.length === 0 ? (
        <EmptyState
          icon="ph-magnifying-glass"
          title="No saved searches"
          hint="Define a keyword / country / work-arrangement search to watch matching corpus jobs as a live panel."
          action={newButton}
        />
      ) : (
        <div
          style={{
            display: "grid",
            gridTemplateColumns: isMobile ? "1fr" : "repeat(auto-fill, minmax(min(100%, 340px), 1fr))",
            gap: "var(--space-4)",
            alignItems: "stretch",
          }}
        >
          {searches.map((ss) => (
            <SavedSearchCard key={ss.id} savedSearch={ss} />
          ))}
        </div>
      )}
      <SavedSearchModal open={modalOpen} onClose={() => setModalOpen(false)} />
    </PageShell>
  );
}

// Chip is a small facet pill (keyword / country / arrangement).
export function Chip({ label, variant = "neutral" }: { label: string; variant?: "neutral" | "accent" | "accent-2" }) {
  const cls = variant === "accent" ? "tag tag-accent" : variant === "accent-2" ? "tag tag-accent-2" : "tag tag-neutral";
  return <span className={cls}>{label}</span>;
}

// facetChips renders a search's stored facets: keyword terms lead (accent), then
// countries (accent-2) and arrangements (neutral). Nothing renders for an empty facet.
export function facetChips(ss: SavedSearch) {
  return (
    <>
      {ss.keywords.map((k) => (
        <Chip key={`k-${k}`} label={k} variant="accent" />
      ))}
      {ss.countries.map((c) => (
        <Chip key={`c-${c}`} label={c} variant="accent-2" />
      ))}
      {ss.workArrangements.map((w) => (
        <Chip key={`w-${w}`} label={ARRANGEMENT_LABELS[w as WorkArrangement] ?? w} />
      ))}
    </>
  );
}

// SavedSearchCard is one search's summary tile on the list page: its name, facets,
// and a live match count. The whole tile links to the search's detail page, where
// the matching jobs are rendered as cards.
function SavedSearchCard({ savedSearch }: { savedSearch: SavedSearch }) {
  const resultsQ = useSavedSearchResults(savedSearch.id);
  const count = resultsQ.data?.length ?? 0;
  const hasFacets =
    savedSearch.keywords.length > 0 || savedSearch.countries.length > 0 || savedSearch.workArrangements.length > 0;

  return (
    <Link
      to={`/searches/${savedSearch.id}`}
      className="card elev-sm card-link"
      style={{
        gap: "var(--space-4)",
        padding: "var(--space-6)",
        textDecoration: "none",
        color: "inherit",
        justifyContent: "space-between",
      }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
        <h3 style={{ margin: 0, fontSize: 17, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {savedSearch.name}
        </h3>
        {hasFacets && (
          <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>{facetChips(savedSearch)}</div>
        )}
      </div>

      <div
        style={{
          display: "flex",
          alignItems: "baseline",
          justifyContent: "space-between",
          gap: "var(--space-3)",
          paddingTop: "var(--space-3)",
          borderTop: "1px solid var(--color-divider)",
        }}
      >
        <div style={{ display: "flex", alignItems: "baseline", gap: 7 }}>
          <span style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 26, lineHeight: 1 }}>
            {resultsQ.isLoading ? "…" : count}
          </span>
          <span style={{ fontSize: 12, color: "var(--color-neutral-500)" }}>
            matching {count === 1 ? "job" : "jobs"}
          </span>
        </div>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4, fontSize: 13, color: "var(--color-accent-300)" }}>
          View jobs <Icon name="ph-arrow-right" size={13} />
        </span>
      </div>
    </Link>
  );
}
