import type { ReactNode } from "react";
import type { RunStatus } from "../api";

// Card is the standard panel wrapper used across pages.
export function Card({
  title,
  children,
  actions,
}: {
  title?: string;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <section className="rounded-lg border border-slate-200 bg-white shadow-sm">
      {(title || actions) && (
        <header className="flex items-center justify-between border-b border-slate-100 px-5 py-3">
          {title && (
            <h2 className="text-sm font-semibold tracking-wide text-slate-700 uppercase">
              {title}
            </h2>
          )}
          {actions}
        </header>
      )}
      <div className="p-5">{children}</div>
    </section>
  );
}

export function PageHeader({
  title,
  subtitle,
}: {
  title: string;
  subtitle?: string;
}) {
  return (
    <div className="mb-6">
      <h1 className="text-2xl font-bold text-slate-900">{title}</h1>
      {subtitle && <p className="mt-1 text-sm text-slate-500">{subtitle}</p>}
    </div>
  );
}

// QueryState renders the common loading/error/empty scaffolding around a list
// query so pages don't each re-implement it.
export function QueryState({
  isLoading,
  error,
  isEmpty,
  emptyMessage = "Nothing here yet.",
  children,
}: {
  isLoading: boolean;
  error: unknown;
  isEmpty: boolean;
  emptyMessage?: string;
  children: ReactNode;
}) {
  if (isLoading) return <p className="text-sm text-slate-500">Loading…</p>;
  if (error)
    return (
      <p className="text-sm text-red-600">{(error as Error).message}</p>
    );
  if (isEmpty)
    return <p className="text-sm text-slate-500">{emptyMessage}</p>;
  return <>{children}</>;
}

const STATUS_STYLES: Record<RunStatus, string> = {
  running: "bg-emerald-100 text-emerald-700",
  stopping: "bg-amber-100 text-amber-700",
  pausing: "bg-amber-50 text-amber-600",
  paused: "bg-indigo-100 text-indigo-700",
  stopped: "bg-slate-200 text-slate-600",
  completed: "bg-blue-100 text-blue-700",
  failed: "bg-red-100 text-red-700",
};

export function StatusBadge({ status }: { status: RunStatus }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_STYLES[status]}`}
    >
      {status}
    </span>
  );
}

export function KindBadge({ kind }: { kind: string }) {
  const style =
    kind === "keyword"
      ? "bg-violet-100 text-violet-700"
      : "bg-cyan-100 text-cyan-700";
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${style}`}
    >
      {kind}
    </span>
  );
}

export function Button({
  children,
  onClick,
  disabled,
  type = "button",
  variant = "primary",
}: {
  children: ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  type?: "button" | "submit";
  variant?: "primary" | "secondary" | "danger";
}) {
  const styles = {
    primary: "bg-indigo-600 text-white hover:bg-indigo-500",
    secondary: "bg-slate-100 text-slate-700 hover:bg-slate-200",
    danger: "bg-red-600 text-white hover:bg-red-500",
  }[variant];
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-md px-3 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${styles}`}
    >
      {children}
    </button>
  );
}

// formatTime renders an ISO timestamp as a locale time, or an em dash when the
// timestamp is absent.
export function formatTime(iso: string | null): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleTimeString();
}
