import type { CSSProperties, ReactNode } from "react";

import { useCrawlControls } from "../hooks";
import { primaryAction, statusMeta, stoppable, type DisplayStatus } from "../lib/status";
import { sparklinePoints } from "../lib/sparkline";

// Icon renders a Phosphor (regular) glyph by class name, e.g. "ph-broadcast".
export function Icon({
  name,
  size = 16,
  color,
  style,
}: {
  name: string;
  size?: number;
  color?: string;
  style?: CSSProperties;
}) {
  return <i className={`ph ${name}`} style={{ fontSize: size, color, ...style }} />;
}

// StatusTag renders a run's status pill using the nocturne tag classes.
export function StatusTag({ status }: { status: DisplayStatus }) {
  const meta = statusMeta(status);
  return <span className={meta.tagClass}>{meta.label}</span>;
}

// Dot is the small status indicator with an optional glow.
export function Dot({ color, glow = false, size = 9 }: { color: string; glow?: boolean; size?: number }) {
  return (
    <span
      style={{
        width: size,
        height: size,
        borderRadius: "50%",
        flex: "none",
        background: color,
        boxShadow: glow ? `0 0 8px ${color}` : undefined,
      }}
    />
  );
}

// StatCard is a labelled metric tile. `lg` is the overview's larger variant
// (with an optional trailing icon); `md` is the denser detail/catalog variant.
export function StatCard({
  label,
  value,
  sub,
  subColor = "var(--color-neutral-500)",
  icon,
  size = "lg",
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  subColor?: string;
  icon?: string;
  size?: "lg" | "md";
}) {
  return (
    <div className="card elev-sm" style={{ gap: 6 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <span className="card-kicker">{label}</span>
        {icon && <Icon name={icon} size={16} color="var(--color-neutral-500)" />}
      </div>
      <div
        style={{
          fontFamily: "var(--font-heading)",
          fontWeight: 600,
          fontSize: size === "lg" ? 32 : 28,
          lineHeight: 1,
          letterSpacing: "-0.02em",
        }}
      >
        {value}
      </div>
      {sub != null && <div style={{ fontSize: 12, color: subColor }}>{sub}</div>}
    </div>
  );
}

// Sparkline draws a filled trend line for a session-sampled series in a 280×70
// viewBox that stretches to fill its flex slot.
export function Sparkline({ series }: { series: number[] }) {
  const { line, area } = sparklinePoints(series, 280, 70);
  return (
    <svg viewBox="0 0 280 70" preserveAspectRatio="none" style={{ flex: 1, height: 70, overflow: "visible" }}>
      <defs>
        <linearGradient id="sparkfill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--color-accent)" stopOpacity="0.32" />
          <stop offset="100%" stopColor="var(--color-accent)" stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon points={area} fill="url(#sparkfill)" />
      <polyline
        points={line}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={2}
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}

// RunControls renders a fused crawl's primary lifecycle button (pause / resume /
// re-run, per status) plus a Stop button when the run is stoppable, wired to the
// shared mutations. Variants tune it for the card grid (ghost), the detail
// header, and the stacked run-control panel (block).
export function RunControls({
  status,
  runId,
  definitionId,
  ghost = false,
  block = false,
  stopLabel,
  primarySuffix = "",
}: {
  status: DisplayStatus;
  runId: string | null;
  definitionId: string;
  ghost?: boolean;
  block?: boolean;
  stopLabel?: string;
  primarySuffix?: string;
}) {
  const { dispatch, pending } = useCrawlControls();
  const primary = primaryAction(status);
  const blockCls = block ? " btn-block" : "";
  const primaryCls = (ghost ? "btn btn-ghost" : primary.cls) + blockCls;
  const stopCls = (ghost ? "btn btn-ghost" : "btn btn-secondary") + blockCls;

  const fire = (action: "pause" | "resume" | "rerun" | "stop") => (e: React.MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    dispatch(action, { runId, definitionId });
  };

  return (
    <>
      <button className={primaryCls} disabled={primary.disabled || pending} onClick={fire(primary.act)}>
        <Icon name={primary.icon} size={14} />
        {primary.label}
        {primarySuffix}
      </button>
      {stoppable(status) && (
        <button
          className={stopCls}
          disabled={pending}
          onClick={fire("stop")}
          style={ghost ? { color: "var(--color-neutral-400)" } : undefined}
        >
          <Icon name="ph-stop" size={14} />
          {stopLabel ? ` ${stopLabel}` : ""}
        </button>
      )}
    </>
  );
}

// Loading / ErrorNote / EmptyState are the shared query-state scaffolds.
export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div style={{ padding: "var(--space-8)", textAlign: "center", fontSize: 13, color: "var(--color-neutral-500)" }}>
      {label}
    </div>
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  const message = error instanceof Error ? error.message : "Something went wrong.";
  return (
    <div
      className="card elev-sm"
      style={{ flexDirection: "row", alignItems: "center", gap: 9, color: "var(--color-accent-300)", fontSize: 13 }}
    >
      <Icon name="ph-warning-circle" size={16} color="var(--color-accent-300)" />
      {message}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  hint,
  action,
}: {
  icon: string;
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <div
      className="card elev-sm"
      style={{ alignItems: "center", textAlign: "center", gap: "var(--space-3)", padding: "var(--space-8)" }}
    >
      <Icon name={icon} size={30} color="var(--color-neutral-500)" />
      <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 16 }}>{title}</div>
      {hint && <div style={{ fontSize: 13, color: "var(--color-neutral-500)", maxWidth: 380 }}>{hint}</div>}
      {action}
    </div>
  );
}
