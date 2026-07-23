import { useState } from "react";

import type { Listing, SavedSearch, WorkArrangement } from "../api";
import {
  useDeleteSavedSearch,
  useIsMobile,
  useRenameSavedSearch,
  useSavedSearches,
  useSavedSearchResults,
} from "../hooks";
import { relativeTime } from "../lib/format";
import { PageShell } from "../components/PageShell";
import { SavedSearchModal } from "../components/SavedSearchModal";
import { EmptyState, ErrorNote, Icon, Loading } from "../components/primitives";

// ARRANGEMENT_LABELS renders the enum values as readable badge text.
const ARRANGEMENT_LABELS: Record<WorkArrangement, string> = {
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
            gridTemplateColumns: isMobile ? "1fr" : "repeat(auto-fill, minmax(min(100%, 420px), 1fr))",
            gap: "var(--space-4)",
            alignItems: "start",
          }}
        >
          {searches.map((ss) => (
            <SavedSearchPanel key={ss.id} savedSearch={ss} />
          ))}
        </div>
      )}
      <SavedSearchModal open={modalOpen} onClose={() => setModalOpen(false)} />
    </PageShell>
  );
}

// Chip is a small facet pill on a panel header (keyword / country / arrangement).
function Chip({ label, variant = "neutral" }: { label: string; variant?: "neutral" | "accent" | "accent-2" }) {
  const cls = variant === "accent" ? "tag tag-accent" : variant === "accent-2" ? "tag tag-accent-2" : "tag tag-neutral";
  return <span className={cls}>{label}</span>;
}

// SavedSearchPanel is one live panel: it re-runs the SavedSearch against the Corpus
// (a query, never a crawl) and renders the matching listings in full detail. The
// header carries the search's facets, an inline rename, and a guarded delete.
function SavedSearchPanel({ savedSearch }: { savedSearch: SavedSearch }) {
  const resultsQ = useSavedSearchResults(savedSearch.id);
  const rename = useRenameSavedSearch();
  const remove = useDeleteSavedSearch();

  const [editing, setEditing] = useState(false);
  const [draftName, setDraftName] = useState(savedSearch.name);

  const listings = resultsQ.data ?? [];

  const startEdit = () => {
    setDraftName(savedSearch.name);
    setEditing(true);
  };
  const saveEdit = () => {
    const name = draftName.trim();
    if (!name || name === savedSearch.name) {
      setEditing(false);
      return;
    }
    rename.mutate({ id: savedSearch.id, name }, { onSuccess: () => setEditing(false) });
  };
  const onDelete = () => {
    if (window.confirm(`Delete the search "${savedSearch.name}"?`)) {
      remove.mutate(savedSearch.id);
    }
  };

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-3)", padding: "var(--space-5)" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-3)" }}>
        {editing ? (
          <div style={{ display: "flex", alignItems: "center", gap: "var(--space-2)", flex: 1, minWidth: 0 }}>
            <input
              className="input"
              value={draftName}
              autoFocus
              onChange={(e) => setDraftName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") saveEdit();
                if (e.key === "Escape") setEditing(false);
              }}
            />
            <button className="btn btn-icon btn-primary" onClick={saveEdit} disabled={rename.isPending} aria-label="Save name">
              <Icon name="ph-check-circle" size={16} />
            </button>
            <button className="btn btn-icon btn-secondary" onClick={() => setEditing(false)} aria-label="Cancel rename">
              <Icon name="ph-x" size={16} />
            </button>
          </div>
        ) : (
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}>
              <h3 style={{ margin: 0, fontSize: 17, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {savedSearch.name}
              </h3>
            </div>
            <div style={{ fontSize: 12, color: "var(--color-neutral-500)", marginTop: 2 }}>
              {resultsQ.isLoading ? "loading…" : `${listings.length} matching ${listings.length === 1 ? "job" : "jobs"}`}
            </div>
          </div>
        )}
        {!editing && (
          <div style={{ display: "flex", gap: "var(--space-2)", flex: "none" }}>
            <button className="btn btn-icon btn-secondary" onClick={startEdit} aria-label="Rename search">
              <Icon name="ph-pencil-simple" size={15} />
            </button>
            <button className="btn btn-icon btn-secondary" onClick={onDelete} disabled={remove.isPending} aria-label="Delete search">
              <Icon name="ph-trash" size={15} />
            </button>
          </div>
        )}
      </div>

      {/* Facet chips summarise the stored query. Keyword terms lead (accent), then
          countries and arrangements as neutral pills; nothing shows for an empty facet. */}
      {(savedSearch.keywords.length > 0 ||
        savedSearch.countries.length > 0 ||
        savedSearch.workArrangements.length > 0) && (
        <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>
          {savedSearch.keywords.map((k) => (
            <Chip key={`k-${k}`} label={k} variant="accent" />
          ))}
          {savedSearch.countries.map((c) => (
            <Chip key={`c-${c}`} label={c} variant="accent-2" />
          ))}
          {savedSearch.workArrangements.map((w) => (
            <Chip key={`w-${w}`} label={ARRANGEMENT_LABELS[w as WorkArrangement] ?? w} />
          ))}
        </div>
      )}

      <div style={{ height: 1, background: "var(--color-divider)" }} />

      {resultsQ.error ? (
        <ErrorNote error={resultsQ.error} />
      ) : resultsQ.isLoading ? (
        <Loading />
      ) : listings.length === 0 ? (
        <div style={{ fontSize: 13, color: "var(--color-neutral-500)", padding: "var(--space-3) 0" }}>
          No matching jobs yet.
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
          {listings.map((l) => (
            <ListingRow key={l.id} listing={l} />
          ))}
        </div>
      )}
    </div>
  );
}

// ListingRow renders one Corpus listing in full detail: the title links to the
// source posting, with company/department, raw location + resolved country, work
// arrangement, a truncated description, and the seen-window.
function ListingRow({ listing }: { listing: Listing }) {
  const companyLine = [listing.company, listing.department].filter(Boolean).join(" · ");
  return (
    <div
      className="card"
      style={{ background: "var(--color-bg)", gap: "var(--space-2)", padding: "var(--space-3)" }}
    >
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-3)" }}>
        <a
          href={listing.url}
          target="_blank"
          rel="noreferrer"
          style={{
            fontSize: 14,
            fontWeight: 600,
            textDecoration: "none",
            display: "inline-flex",
            alignItems: "center",
            gap: 6,
            minWidth: 0,
          }}
        >
          <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{listing.title || "Untitled role"}</span>
          <Icon name="ph-arrow-square-out" size={13} style={{ flex: "none" }} />
        </a>
        <div style={{ display: "flex", gap: "var(--space-2)", flex: "none" }}>
          {listing.country && <span className="tag tag-accent-2">{listing.country}</span>}
          {listing.workArrangement !== "unspecified" && (
            <span className="tag tag-neutral">{ARRANGEMENT_LABELS[listing.workArrangement] ?? listing.workArrangement}</span>
          )}
        </div>
      </div>

      {companyLine && <div style={{ fontSize: 13, color: "var(--color-neutral-300)" }}>{companyLine}</div>}

      {(listing.location || listing.country) && (
        <div style={{ fontSize: 12, color: "var(--color-neutral-500)", display: "flex", alignItems: "center", gap: 5 }}>
          <Icon name="ph-globe-hemisphere-west" size={13} color="var(--color-neutral-500)" />
          {listing.location || listing.country}
        </div>
      )}

      {listing.description && (
        <div
          style={{
            fontSize: 12,
            color: "var(--color-neutral-400)",
            display: "-webkit-box",
            WebkitLineClamp: 2,
            WebkitBoxOrient: "vertical",
            overflow: "hidden",
          }}
        >
          {listing.description}
        </div>
      )}

      <div style={{ fontSize: 11, color: "var(--color-neutral-600)" }}>
        first seen {relativeTime(listing.firstSeen)} · last seen {relativeTime(listing.lastSeen)}
      </div>
    </div>
  );
}
