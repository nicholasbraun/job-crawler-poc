import { useEffect, useState, type CSSProperties } from "react";
import { Link, NavLink, Outlet, useOutletContext } from "react-router-dom";

import { useDefinitions, useIsMobile, useRuns } from "../hooks";
import { buildDiscovery } from "../lib/model";
import { statusMeta } from "../lib/status";
import { Dot, Icon } from "./primitives";
import { DiscoveryStartModal } from "./DiscoveryStartModal";

// LayoutContext is threaded to every page via the router Outlet, exposing the
// globally-owned "start discovery" modal action (the modal lives in the Layout
// so it overlays the whole app).
export type LayoutContext = { openStartDiscovery: () => void };

export function useLayout(): LayoutContext {
  return useOutletContext<LayoutContext>();
}

type NavDef = { to: string; label: string; icon?: string; end?: boolean; count?: number; nested?: boolean };

function NavItem({ item }: { item: NavDef }) {
  return (
    <NavLink
      to={item.to}
      end={item.end}
      style={({ isActive }) => ({
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: item.nested ? "7px 11px 7px 40px" : "9px 11px",
        borderRadius: "var(--radius-md)",
        fontSize: item.nested ? 13 : 14,
        textDecoration: "none",
        cursor: "pointer",
        color: isActive ? "var(--color-accent-200)" : "var(--color-neutral-300)",
        background: isActive ? "color-mix(in srgb, var(--color-accent) 14%, transparent)" : "transparent",
        boxShadow: isActive ? "inset 0 0 0 1px color-mix(in srgb, var(--color-accent) 40%, transparent)" : "none",
      })}
    >
      {({ isActive }) => (
        <>
          {item.icon && <Icon name={item.icon} size={18} style={{ width: 18 }} />}
          <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {item.label}
          </span>
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

// MobileTopBar is the slim app bar shown only on phone-portrait: the brand plus
// a hamburger that opens the nav drawer. On desktop the sidebar is always in
// view, so this never renders.
function MobileTopBar({ onMenu }: { onMenu: () => void }) {
  return (
    <div
      style={{
        flex: "none",
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: "var(--space-3)",
        padding: "var(--space-3) var(--space-4)",
        background: "var(--color-surface)",
        boxShadow: "0 1px 0 var(--color-divider)",
        zIndex: 30,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 9 }}>
        <span
          style={{
            display: "grid",
            placeItems: "center",
            width: 28,
            height: 28,
            borderRadius: 8,
            background: "var(--color-accent-800)",
            color: "var(--color-accent-200)",
          }}
        >
          <Icon name="ph-broadcast" size={16} />
        </span>
        <span style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 16 }}>Prospect</span>
      </div>
      <button type="button" className="btn btn-secondary btn-icon" onClick={onMenu} aria-label="Open navigation">
        <Icon name="ph-list" size={18} />
      </button>
    </div>
  );
}

export function Layout() {
  const [discoveryOpen, setDiscoveryOpen] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const isMobile = useIsMobile();

  const runs = useRuns();
  const definitions = useDefinitions();
  const defs = definitions.data ?? [];
  const runList = runs.data ?? [];

  const discovery = buildDiscovery(defs, runList);
  const discoveryMeta = discovery ? statusMeta(discovery.status) : null;

  // Growing the viewport back to desktop retires the drawer, so one left open on
  // rotate/resize never lingers as a fixed overlay across the in-flow sidebar.
  useEffect(() => {
    if (!isMobile) setNavOpen(false);
  }, [isMobile]);

  // Escape closes the open drawer, mirroring a scrim tap.
  useEffect(() => {
    if (!isMobile || !navOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setNavOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [isMobile, navOpen]);

  // On mobile the sidebar becomes a fixed drawer that slides in over a scrim; on
  // desktop it is the in-flow 240px column. The inner markup is identical — only
  // its positioning and the slide transform differ.
  const asideStyle: CSSProperties = isMobile
    ? {
        position: "fixed",
        top: 0,
        left: 0,
        height: "100dvh",
        width: "min(280px, 82vw)",
        zIndex: 45,
        transform: navOpen ? "translateX(0)" : "translateX(-100%)",
        transition: "transform 0.22s ease",
        boxShadow: navOpen ? "var(--shadow-lg)" : "none",
        background: "var(--color-surface)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-8)",
        padding: "var(--space-6) var(--space-4)",
      }
    : {
        width: 240,
        flex: "none",
        background: "var(--color-surface)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-8)",
        padding: "var(--space-6) var(--space-4)",
        boxShadow: "1px 0 0 var(--color-divider)",
      };

  return (
    <div
      style={{
        display: "flex",
        flexDirection: isMobile ? "column" : "row",
        height: "100dvh",
        overflow: "hidden",
        background: "var(--color-bg)",
        color: "var(--color-text)",
      }}
    >
      {isMobile && <MobileTopBar onMenu={() => setNavOpen(true)} />}

      {/* Scrim: a tap outside the open drawer dismisses it. Mounted only while
          open, so it never intercepts clicks on desktop. */}
      {isMobile && navOpen && (
        <div
          aria-hidden
          onClick={() => setNavOpen(false)}
          style={{
            position: "fixed",
            inset: 0,
            zIndex: 40,
            background: "color-mix(in srgb, var(--color-neutral-900) 55%, transparent)",
          }}
        />
      )}

      <aside
        // A tap on any nav link inside the drawer navigates and closes it; taps
        // on non-link chrome (the brand) leave it open.
        onClick={isMobile ? (e) => { if ((e.target as HTMLElement).closest("a")) setNavOpen(false); } : undefined}
        style={asideStyle}
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

        <nav style={{ display: "flex", flexDirection: "column", gap: 2, flex: "1 1 auto", minHeight: 0, overflowY: "auto" }}>
          <NavItem item={{ to: "/", label: "Overview", icon: "ph-squares-four", end: true }} />
          <NavItem item={{ to: "/discovery", label: "Discovery", icon: "ph-broadcast" }} />
          <NavItem item={{ to: "/catalog", label: "Catalog", icon: "ph-stack" }} />
        </nav>

        <div style={{ marginTop: "auto", display: "flex", flexDirection: "column", gap: "var(--space-3)" }}>
          <Link
            to="/discovery"
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              padding: "var(--space-2) var(--space-3)",
              borderRadius: "var(--radius-md)",
              background: "color-mix(in srgb, var(--color-accent) 10%, transparent)",
              textDecoration: "none",
              color: "inherit",
              cursor: "pointer",
            }}
          >
            <Dot color={discoveryMeta ? discoveryMeta.dot : "var(--color-neutral-600)"} glow={!!discovery} size={8} />
            <span style={{ fontSize: 12, color: "var(--color-neutral-300)" }}>
              Discovery {discoveryMeta ? discoveryMeta.label : "not started"}
            </span>
          </Link>
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

      {/* overflowX hidden is the backstop: the page scrolls only vertically, so a
          stray wide element can never produce sideways page scroll. Tables keep
          their own overflow-x:auto wrappers and still scroll internally. */}
      <main className="app-scroll" style={{ flex: 1, minWidth: 0, minHeight: 0, overflowX: "hidden", overflowY: "auto", display: "flex", flexDirection: "column" }}>
        <Outlet
          context={
            {
              openStartDiscovery: () => setDiscoveryOpen(true),
            } satisfies LayoutContext
          }
        />
      </main>

      <DiscoveryStartModal open={discoveryOpen} onClose={() => setDiscoveryOpen(false)} />
    </div>
  );
}
