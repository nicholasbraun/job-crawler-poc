import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { Listing } from "../api";
import { useDeleteSavedSearch, useRenameSavedSearch, useSavedSearches, useSavedSearchResults } from "../hooks";
import { relativeTime } from "../lib/format";
import { PageShell } from "../components/PageShell";
import { EmptyState, ErrorNote, Icon, Loading } from "../components/primitives";
import { ARRANGEMENT_LABELS, facetChips } from "./SearchesPage";

// DESC_PREVIEW is how many description characters a job card shows before the
// "See more" toggle. Long postings stay scannable; short ones show in full.
const DESC_PREVIEW = 280;

export function SearchDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();

  const searchesQ = useSavedSearches();
  const resultsQ = useSavedSearchResults(id);
  const rename = useRenameSavedSearch();
  const remove = useDeleteSavedSearch();

  const [editing, setEditing] = useState(false);
  const [draftName, setDraftName] = useState("");

  const search = searchesQ.data?.find((s) => s.id === id);
  const listings = resultsQ.data ?? [];

  // The search list is still loading and we have no cached hit yet.
  if (searchesQ.isLoading && !search) {
    return (
      <PageShell title="Search" back={{ to: "/searches", label: "Searches" }}>
        <Loading />
      </PageShell>
    );
  }

  // Loaded, but no such search (bad id, or it was deleted in another tab).
  if (!search) {
    return (
      <PageShell title="Search" back={{ to: "/searches", label: "Searches" }}>
        <EmptyState
          icon="ph-magnifying-glass"
          title="Search not found"
          hint="This saved search no longer exists. It may have been deleted."
          action={
            <button className="btn btn-primary" onClick={() => navigate("/searches")}>
              Back to searches
            </button>
          }
        />
      </PageShell>
    );
  }

  const startEdit = () => {
    setDraftName(search.name);
    setEditing(true);
  };
  const saveEdit = () => {
    const name = draftName.trim();
    if (!name || name === search.name) {
      setEditing(false);
      return;
    }
    rename.mutate({ id, name }, { onSuccess: () => setEditing(false) });
  };
  const onDelete = () => {
    if (window.confirm(`Delete the search "${search.name}"?`)) {
      remove.mutate(id, { onSuccess: () => navigate("/searches") });
    }
  };

  const actions = editing ? (
    <div style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}>
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
    <div style={{ display: "flex", gap: "var(--space-2)" }}>
      <button className="btn btn-secondary" onClick={startEdit}>
        <Icon name="ph-pencil-simple" size={14} /> Rename
      </button>
      <button className="btn btn-icon btn-secondary" onClick={onDelete} disabled={remove.isPending} aria-label="Delete search">
        <Icon name="ph-trash" size={15} />
      </button>
    </div>
  );

  const count = listings.length;

  return (
    <PageShell
      title={search.name}
      subtitle={resultsQ.isLoading ? "Querying the corpus…" : `${count} matching ${count === 1 ? "job" : "jobs"}`}
      back={{ to: "/searches", label: "Searches" }}
      actions={actions}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>{facetChips(search)}</div>

        {resultsQ.error ? (
          <ErrorNote error={resultsQ.error} />
        ) : resultsQ.isLoading ? (
          <Loading />
        ) : listings.length === 0 ? (
          <EmptyState
            icon="ph-briefcase"
            title="No matching jobs yet"
            hint="Nothing in the corpus matches these facets right now. The collector fills the corpus continuously, so matches can appear as it runs."
          />
        ) : (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fill, minmax(min(100%, 380px), 1fr))",
              gap: "var(--space-4)",
              alignItems: "start",
            }}
          >
            {listings.map((l) => (
              <JobCard key={l.id} listing={l} />
            ))}
          </div>
        )}
      </div>
    </PageShell>
  );
}

// JobCard renders one Corpus listing in full detail: the title links to the source
// posting, with company/department, raw location + resolved country, work
// arrangement, the seen-window, and the description behind a "See more" toggle.
function JobCard({ listing }: { listing: Listing }) {
  const [expanded, setExpanded] = useState(false);
  const companyLine = [listing.company, listing.department].filter(Boolean).join(" · ");

  const description = listing.description ?? "";
  const long = description.length > DESC_PREVIEW;
  const shown = expanded || !long ? description : `${description.slice(0, DESC_PREVIEW).trimEnd()}…`;

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-3)", padding: "var(--space-5)" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-3)" }}>
        <a
          href={listing.url}
          target="_blank"
          rel="noreferrer"
          style={{
            fontSize: 15,
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

      {description && (
        <div style={{ fontSize: 13, color: "var(--color-neutral-300)", lineHeight: 1.55, whiteSpace: "pre-wrap" }}>
          {shown}
          {long && (
            <button
              onClick={() => setExpanded((v) => !v)}
              style={{
                display: "block",
                marginTop: 6,
                padding: 0,
                background: "none",
                border: "none",
                cursor: "pointer",
                fontSize: 12,
                fontWeight: 600,
                color: "var(--color-accent-300)",
              }}
            >
              {expanded ? "See less" : "See more"}
            </button>
          )}
        </div>
      )}

      <div style={{ fontSize: 11, color: "var(--color-neutral-600)", marginTop: "auto", paddingTop: "var(--space-1)" }}>
        first seen {relativeTime(listing.firstSeen)} · last seen {relativeTime(listing.lastSeen)}
      </div>
    </div>
  );
}
