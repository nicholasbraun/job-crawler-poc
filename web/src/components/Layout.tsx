import { useState } from "react";
import { NavLink, Outlet, useOutletContext } from "react-router-dom";

import { useDefinitions, useRuns } from "../hooks";
import { buildDiscovery, buildKeywordCrawls } from "../lib/model";
import { statusMeta } from "../lib/status";
import { Dot, Icon } from "./primitives";
import { NewCrawlModal } from "./NewCrawlModal";

// LayoutContext is threaded to every page via the router Outlet, exposing the
// one globally-owned action: opening the "new keyword crawl" modal (the modal
// itself lives in the Layout so it overlays the whole app).
export type LayoutContext = { openNewCrawl: () => void };

export function useLayout(): LayoutContext {
  return useOutletContext<LayoutContext>();
}

type NavDef = { to: string; label: string; icon: string; end?: boolean; count?: number };

function NavItem({ item }: { item: NavDef }) {
  return (
    <NavLink
      to={item.to}
      end={item.end}
      style={({ isActive }) => ({
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "9px 11px",
        borderRadius: "var(--radius-md)",
        fontSize: 14,
        textDecoration: "none",
        cursor: "pointer",
        color: isActive ? "var(--color-accent-200)" : "var(--color-neutral-300)",
        background: isActive ? "color-mix(in srgb, var(--color-accent) 14%, transparent)" : "transparent",
        boxShadow: isActive ? "inset 0 0 0 1px color-mix(in srgb, var(--color-accent) 40%, transparent)" : "none",
      })}
    >
      {({ isActive }) => (
        <>
          <Icon name={item.icon} size={18} style={{ width: 18 }} />
          <span style={{ flex: 1 }}>{item.label}</span>
          {item.count != null && (
            <span
              style={{
                fontSize: 11,
                padding: "1px 8px",
                borderRadius: 20,
                background: isActive ? "var(--color-accent-700)" : "var(--color-neutral-800)",
                color: "var(--color-neutral-100)",
              }}
            >
              {item.count}
            </span>
          )}
        </>
      )}
    </NavLink>
  );
}

export function Layout() {
  const [modalOpen, setModalOpen] = useState(false);

  const runs = useRuns();
  const definitions = useDefinitions();
  const defs = definitions.data ?? [];
  const runList = runs.data ?? [];

  const discovery = buildDiscovery(defs, runList);
  const keywordCount = buildKeywordCrawls(defs, runList).length;
  const discoveryMeta = discovery ? statusMeta(discovery.status) : null;

  return (
    <div style={{ display: "flex", height: "100vh", overflow: "hidden", background: "var(--color-bg)", color: "var(--color-text)" }}>
      <aside
        style={{
          width: 240,
          flex: "none",
          background: "var(--color-surface)",
          display: "flex",
          flexDirection: "column",
          gap: "var(--space-8)",
          padding: "var(--space-6) var(--space-4)",
          boxShadow: "1px 0 0 var(--color-divider)",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "0 var(--space-2)" }}>
          <span
            style={{
              display: "grid",
              placeItems: "center",
              width: 32,
              height: 32,
              borderRadius: 9,
              background: "var(--color-accent-800)",
              color: "var(--color-accent-200)",
            }}
          >
            <Icon name="ph-broadcast" size={19} />
          </span>
          <div style={{ lineHeight: 1.1 }}>
            <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 17 }}>Prospect</div>
            <div style={{ fontSize: 10, letterSpacing: "0.14em", textTransform: "uppercase", color: "var(--color-neutral-500)" }}>
              Job Crawler
            </div>
          </div>
        </div>

        <nav style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          <NavItem item={{ to: "/", label: "Overview", icon: "ph-squares-four", end: true }} />
          <NavItem item={{ to: "/discovery", label: "Discovery", icon: "ph-broadcast" }} />
          <NavItem item={{ to: "/crawls", label: "Keyword crawls", icon: "ph-magnifying-glass", count: keywordCount }} />
          <NavItem item={{ to: "/catalog", label: "Catalog", icon: "ph-stack" }} />
        </nav>

        <div style={{ marginTop: "auto", display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              padding: "var(--space-2) var(--space-3)",
              borderRadius: "var(--radius-md)",
              background: "color-mix(in srgb, var(--color-accent) 10%, transparent)",
            }}
          >
            <Dot color={discoveryMeta ? discoveryMeta.dot : "var(--color-neutral-600)"} glow={!!discovery} size={8} />
            <span style={{ fontSize: 12, color: "var(--color-neutral-300)" }}>
              Discovery {discoveryMeta ? discoveryMeta.label : "not started"}
            </span>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 9, padding: "0 var(--space-2)" }}>
            <span
              style={{
                display: "grid",
                placeItems: "center",
                width: 30,
                height: 30,
                borderRadius: "50%",
                background: "var(--color-neutral-800)",
                fontSize: 12,
                fontWeight: 600,
                color: "var(--color-neutral-200)",
              }}
            >
              NB
            </span>
            <div style={{ lineHeight: 1.15, minWidth: 0 }}>
              <div style={{ fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>Nicholas Braun</div>
              <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>Job search</div>
            </div>
          </div>
        </div>
      </aside>

      <main className="app-scroll" style={{ flex: 1, overflowY: "auto", display: "flex", flexDirection: "column" }}>
        <Outlet context={{ openNewCrawl: () => setModalOpen(true) } satisfies LayoutContext} />
      </main>

      <NewCrawlModal open={modalOpen} onClose={() => setModalOpen(false)} />
    </div>
  );
}
