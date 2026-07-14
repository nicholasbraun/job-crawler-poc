import { useNavigate } from "react-router-dom";

import { fmt } from "../lib/format";
import type { KeywordCrawl } from "../lib/model";
import { statusMeta } from "../lib/status";
import { Dot, Icon, RunControls, StatusTag } from "./primitives";

// CrawlCard is one keyword crawl (definition + latest run) in the grid. The card
// opens the crawl's detail; its lifecycle buttons stop propagation so they act
// without navigating.
export function CrawlCard({ crawl }: { crawl: KeywordCrawl }) {
  const navigate = useNavigate();
  const meta = statusMeta(crawl.status);
  const open = () => navigate(`/crawls/${crawl.definitionId}`);

  return (
    <div
      className="card elev-sm card-link"
      style={{ gap: "var(--space-3)" }}
      role="button"
      tabIndex={0}
      onClick={open}
      // Enter/Space activate the card like a button, but only when the card
      // itself holds focus: a keypress on an inner control (Run/Stop) bubbles
      // here too and must not also navigate.
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
        }
      }}
    >
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-3)" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 9, minWidth: 0 }}>
          <Dot color={meta.dot} />
          <span
            style={{
              fontFamily: "var(--font-heading)",
              fontWeight: 600,
              fontSize: 16,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {crawl.name}
          </span>
        </div>
        <StatusTag status={crawl.status} />
      </div>

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
        {crawl.keywords.slice(0, 4).map((kw) => (
          <span key={kw} className="tag tag-neutral">
            {kw}
          </span>
        ))}
      </div>

      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginTop: 2, gap: "var(--space-3)" }}>
        <div style={{ display: "flex", gap: "var(--space-6)", fontSize: 12, color: "var(--color-neutral-400)", flexWrap: "wrap" }}>
          <span>
            <b style={{ color: "var(--color-text)", fontSize: 15 }}>{crawl.listingsFound}</b> listings
          </span>
          <span>
            frontier <b style={{ color: "var(--color-text)", fontSize: 15 }}>{fmt(crawl.frontierSize)}</b>
          </span>
          <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
            <Icon name="ph-file-text" size={13} /> {fmt(crawl.pagesCrawled)} pages
          </span>
        </div>
        <div style={{ display: "flex", gap: 4, flex: "none" }}>
          <RunControls status={crawl.status} runId={crawl.runId} definitionId={crawl.definitionId} ghost />
        </div>
      </div>
    </div>
  );
}
