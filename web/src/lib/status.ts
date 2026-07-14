import { isTerminal, type RunStatus } from "../api";

// DisplayStatus extends the API's RunStatus with "idle" — a keyword-crawl
// definition that has never been run, which the fused card model surfaces as its
// own resting state.
export type DisplayStatus = RunStatus | "idle";

export type StatusMeta = {
  label: string;
  // tagClass is a full nocturne tag class pair, e.g. "tag tag-accent".
  tagClass: string;
  // dot is a CSS color for the status indicator dot.
  dot: string;
};

const STATUS_META: Record<DisplayStatus, StatusMeta> = {
  running: { label: "Running", tagClass: "tag tag-accent", dot: "var(--color-accent)" },
  pausing: { label: "Pausing…", tagClass: "tag tag-neutral", dot: "var(--color-neutral-400)" },
  paused: { label: "Paused", tagClass: "tag tag-neutral", dot: "var(--color-neutral-500)" },
  stopping: { label: "Stopping…", tagClass: "tag tag-neutral", dot: "var(--color-neutral-400)" },
  stopped: { label: "Stopped", tagClass: "tag tag-outline", dot: "var(--color-neutral-600)" },
  completed: { label: "Completed", tagClass: "tag tag-accent-2", dot: "var(--color-accent-2-400)" },
  failed: { label: "Failed", tagClass: "tag tag-outline", dot: "var(--color-neutral-400)" },
  idle: { label: "No runs", tagClass: "tag tag-neutral", dot: "var(--color-neutral-600)" },
};

export function statusMeta(status: DisplayStatus): StatusMeta {
  return STATUS_META[status] ?? STATUS_META.idle;
}

// PrimaryAction is the main lifecycle button a run/crawl offers, given its
// current status. `act` selects the mutation: pause/resume act on the run id;
// rerun starts a fresh run of the definition. A disabled action reflects a
// transient state (pausing/stopping) that only the server can move forward.
export type PrimaryAction = {
  label: string;
  icon: string;
  act: "pause" | "resume" | "rerun";
  cls: string;
  disabled: boolean;
};

export function primaryAction(status: DisplayStatus): PrimaryAction {
  switch (status) {
    case "running":
      return { label: "Pause", icon: "ph-pause", act: "pause", cls: "btn btn-secondary", disabled: false };
    case "pausing":
      return { label: "Pausing…", icon: "ph-circle-notch", act: "pause", cls: "btn btn-secondary", disabled: true };
    case "paused":
      return { label: "Resume", icon: "ph-play", act: "resume", cls: "btn btn-primary", disabled: false };
    case "stopping":
      return { label: "Stopping…", icon: "ph-circle-notch", act: "rerun", cls: "btn btn-secondary", disabled: true };
    case "idle":
      return { label: "Run", icon: "ph-play", act: "rerun", cls: "btn btn-primary", disabled: false };
    default: // stopped | completed | failed
      return { label: "Re-run", icon: "ph-arrow-clockwise", act: "rerun", cls: "btn btn-primary", disabled: false };
  }
}

// stoppable reports whether a Stop control should be offered: any non-terminal
// run that actually has a run behind it. "idle" has no run to stop.
export function stoppable(status: DisplayStatus): boolean {
  return status !== "idle" && !isTerminal(status as RunStatus);
}
