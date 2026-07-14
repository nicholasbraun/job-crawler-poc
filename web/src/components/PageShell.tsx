import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { useLayout } from "./Layout";
import { Icon } from "./primitives";

// PageShell is the per-view frame: a sticky header (optional back link, title,
// subtitle) with an optional per-page actions slot and the New-keyword-crawl
// action, over the scrolling body. Every page renders one. Slotted actions
// render before the global primary action so page-specific controls never
// displace it.
export function PageShell({
  title,
  subtitle,
  back,
  actions,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  back?: { to: string; label: string };
  actions?: ReactNode;
  children: ReactNode;
}) {
  const { openNewCrawl } = useLayout();

  return (
    <>
      <header
        style={{
          position: "sticky",
          top: 0,
          zIndex: 5,
          display: "flex",
          alignItems: "flex-end",
          justifyContent: "space-between",
          gap: "var(--space-6)",
          padding: "var(--space-6) var(--space-8) var(--space-4)",
          background: "linear-gradient(var(--color-bg) 78%, transparent)",
        }}
      >
        <div>
          {back && (
            <Link
              to={back.to}
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: 5,
                fontSize: 12,
                marginBottom: 6,
                color: "var(--color-neutral-400)",
                textDecoration: "none",
              }}
            >
              <Icon name="ph-arrow-left" size={12} /> {back.label}
            </Link>
          )}
          <h2 style={{ margin: 0, fontSize: 27 }}>{title}</h2>
          {subtitle != null && (
            <div style={{ fontSize: 13, color: "var(--color-neutral-400)", marginTop: 3 }}>{subtitle}</div>
          )}
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--space-3)", flex: "none" }}>
          {actions}
          <button className="btn btn-primary" onClick={openNewCrawl}>
            <Icon name="ph-plus" size={14} /> New keyword crawl
          </button>
        </div>
      </header>
      <div style={{ padding: "0 var(--space-8) var(--space-8)", flex: 1 }}>{children}</div>
    </>
  );
}
