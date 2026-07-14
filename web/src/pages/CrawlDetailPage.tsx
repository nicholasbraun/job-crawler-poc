import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";

import type { Listing } from "../api";
import { useDefinitions, useListings, useRuns } from "../hooks";
import { fmt, hostOf } from "../lib/format";
import { buildKeywordCrawls, type KeywordCrawl } from "../lib/model";
import { PageShell } from "../components/PageShell";
import { EmptyState, Icon, Loading, RunControls, StatusTag } from "../components/primitives";

export function CrawlDetailPage() {
  const { definitionId = "" } = useParams();
  const runs = useRuns();
  const definitions = useDefinitions();

  const crawl = buildKeywordCrawls(definitions.data ?? [], runs.data ?? []).find(
    (c) => c.definitionId === definitionId,
  );

  if (definitions.isLoading || runs.isLoading) {
    return (
      <PageShell title="Keyword crawl" back={{ to: "/crawls", label: "Keyword crawls" }}>
        <Loading />
      </PageShell>
    );
  }

  if (!crawl) {
    return (
      <PageShell title="Keyword crawl" back={{ to: "/crawls", label: "Keyword crawls" }}>
        <EmptyState icon="ph-magnifying-glass" title="Crawl not found" hint="This keyword crawl no longer exists." />
      </PageShell>
    );
  }

  return (
    <PageShell
      title={crawl.name}
      subtitle={crawl.keywords.join(" · ")}
      back={{ to: "/crawls", label: "Keyword crawls" }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <CrawlHeaderCard crawl={crawl} />
        <ListingsCard definitionId={definitionId} />
      </div>
    </PageShell>
  );
}

function CrawlHeaderCard({ crawl }: { crawl: KeywordCrawl }) {
  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
        <div style={{ display: "flex", flexDirection: "column", gap: 11 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 9 }}>
            <StatusTag status={crawl.status} />
            <span style={{ fontSize: 12, color: "var(--color-neutral-500)" }}>bounded run · seeds from catalog</span>
          </div>
          <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
            {crawl.keywords.map((kw) => (
              <span key={kw} className="tag tag-accent">
                {kw}
              </span>
            ))}
          </div>
          <div style={{ display: "flex", gap: "var(--space-8)", fontSize: 13, color: "var(--color-neutral-400)", flexWrap: "wrap" }}>
            <span>
              <b style={{ color: "var(--color-text)", fontSize: 17 }}>{crawl.listingsFound}</b> listings extracted
            </span>
            <span>
              <b style={{ color: "var(--color-text)", fontSize: 17 }}>{fmt(crawl.pagesCrawled)}</b> pages crawled
            </span>
            <span>
              frontier <b style={{ color: "var(--color-text)", fontSize: 17 }}>{fmt(crawl.frontierSize)}</b>
            </span>
          </div>
          {crawl.error && (
            <div style={{ display: "flex", alignItems: "center", gap: 7, fontSize: 12, color: "var(--color-neutral-300)" }}>
              <Icon name="ph-warning-circle" size={14} color="var(--color-accent-300)" /> {crawl.error}
            </div>
          )}
        </div>
        <div style={{ display: "flex", gap: "var(--space-3)", flex: "none" }}>
          <RunControls status={crawl.status} runId={crawl.runId} definitionId={crawl.definitionId} stopLabel="Stop" />
        </div>
      </div>
    </div>
  );
}

function ListingsCard({ definitionId }: { definitionId: string }) {
  const [filter, setFilter] = useState("");
  // Fetch the crawl's full listing set once, then filter client-side across all
  // visible columns (title, company, location, tech) — broader and snappier than
  // round-tripping the server's title/description keyword match per keystroke.
  const listingsQ = useListings(definitionId, "");
  const all = listingsQ.data ?? [];

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return all;
    return all.filter((l) =>
      `${l.title} ${l.company} ${l.location} ${l.techStack.join(" ")}`.toLowerCase().includes(q),
    );
  }, [all, filter]);

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
        <h4 style={{ margin: 0, fontSize: 17 }}>Extracted listings</h4>
        <div className="field" style={{ width: 260, maxWidth: "100%" }}>
          <input
            className="input"
            placeholder="Filter by keyword…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
      </div>

      <div style={{ overflowX: "auto" }}>
        <table className="table">
          <thead>
            <tr>
              <th>Role</th>
              <th>Company</th>
              <th>Location</th>
              <th>Tech stack</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((l, i) => (
              <ListingRow key={`${l.url}-${i}`} listing={l} />
            ))}
          </tbody>
        </table>
      </div>

      {listingsQ.isLoading ? (
        <Loading />
      ) : filtered.length === 0 ? (
        <div style={{ padding: "var(--space-8) 0", textAlign: "center", fontSize: 13, color: "var(--color-neutral-500)" }}>
          {all.length === 0 ? "No listings extracted yet." : "No listings match this filter yet."}
        </div>
      ) : null}
    </div>
  );
}

function ListingRow({ listing }: { listing: Listing }) {
  return (
    <tr>
      <td>
        <a
          href={listing.url}
          target="_blank"
          rel="noreferrer"
          style={{ textDecoration: "none" }}
        >
          {listing.title}
        </a>
        <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>{hostOf(listing.url)}</div>
      </td>
      <td style={{ fontSize: 13 }}>{listing.company}</td>
      <td style={{ fontSize: 13 }}>
        <span style={{ color: "var(--color-neutral-400)" }}>{listing.location}</span>
        {listing.remote && (
          <span className="tag tag-accent-2" style={{ fontSize: 10, padding: "2px 7px", marginLeft: 4 }}>
            Remote
          </span>
        )}
      </td>
      <td>
        <div style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
          {listing.techStack.map((t) => (
            <span key={t} className="tag tag-neutral" style={{ fontSize: 10, padding: "2px 7px" }}>
              {t}
            </span>
          ))}
        </div>
      </td>
    </tr>
  );
}
