import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import type { ImportJob, ImportJobStatus } from "../api";
import { isImportTerminal, mintIdempotencyKey } from "../api";
import { keys, useImportJob, useImportJobs, useSubmitImport } from "../hooks";
import { relativeTime } from "../lib/format";
import { Icon } from "./primitives";

// ImportModal is the full Catalog Import experience on /catalog: pick a
// .ndjson/.jsonl file, optionally dry-run it, watch the Import Job live, read the
// per-line error report, promote a dry run to a real import (reusing the held
// file with a fresh idempotency key), and see recent imports. It is a sibling of
// NewCrawlModal and follows its focus-trap conventions; Escape is handled at the
// document level so it closes the modal even when focus has fallen back to the
// body. Closing mid-job leaves the job running server-side; it reappears under
// recent imports.
export function ImportModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [dryRun, setDryRun] = useState(true);
  const [jobId, setJobId] = useState<string | null>(null);
  const submit = useSubmitImport();
  const jobQ = useImportJob(jobId);
  const jobsQ = useImportJobs(open);
  const dialogRef = useRef<HTMLDivElement>(null);
  // Ids whose terminal effect (query invalidation) has already fired, so
  // reopening the modal or a late re-render never re-invalidates a finished job.
  const invalidatedRef = useRef<Set<string>>(new Set());
  // Ids of jobs this session started from the currently-held file. "Import for
  // real" and "Try again" resubmit that file, so they are offered only for these
  // jobs — selecting a historical job from recent imports must not show a button
  // that would silently post a different file than the one whose results are on
  // screen. Cleared whenever the held file changes.
  const heldFileJobIdsRef = useRef<Set<string>>(new Set());
  // Ids of every job this session started, never cleared while mounted. The
  // terminal-invalidation effect fires only for these: merely *viewing* an
  // already-finished job from recent imports must not refetch the whole catalog
  // (the refetch visibly flickered the page behind the dialog).
  const startedJobIdsRef = useRef<Set<string>>(new Set());

  const job = jobQ.data;

  // When a real (non-dry-run) import this session started reaches a terminal
  // state, refresh the catalog views so the effect is visible immediately; its
  // terminal transition also refreshes the recent-imports list. Fires once per
  // job id. A failed real import also refreshes, because best-effort per-line
  // merges may have written some rows before an infrastructure error aborted
  // the job.
  useEffect(() => {
    if (!job || !isImportTerminal(job.status)) return;
    if (!startedJobIdsRef.current.has(job.id)) return;
    if (invalidatedRef.current.has(job.id)) return;
    invalidatedRef.current.add(job.id);
    qc.invalidateQueries({ queryKey: keys.importJobs });
    if (!job.dryRun) {
      qc.invalidateQueries({ queryKey: keys.companies });
      qc.invalidateQueries({ queryKey: keys.careerPages });
      qc.invalidateQueries({ queryKey: keys.catalogHistory });
    }
  }, [job, qc]);

  const reset = () => {
    setFile(null);
    setDryRun(true);
    setJobId(null);
    heldFileJobIdsRef.current.clear();
    submit.reset();
  };
  const close = () => {
    reset();
    onClose();
  };

  // Close on Escape at the document level: focus can fall back to <body> (e.g.
  // the submit button disables while a request is in flight, blurring it), where
  // the dialog's own onKeyDown never fires. Keydowns from inside the dialog
  // bubble here too, so this is the single Escape path. closeRef keeps the
  // listener on the latest close without re-subscribing every render.
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    if (!open) return;
    const onEsc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      closeRef.current();
    };
    document.addEventListener("keydown", onEsc);
    return () => document.removeEventListener("keydown", onEsc);
  }, [open]);

  // Return focus to whatever opened the modal (the Import button) when it
  // closes, so a keyboard user is not dropped back at the top of the page.
  useEffect(() => {
    if (!open) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    return () => opener?.focus();
  }, [open]);

  if (!open) return null;

  // Picking a file clears any shown job so the result pane resets to the new file.
  const onPickFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    setFile(e.target.files?.[0] ?? null);
    setJobId(null);
    heldFileJobIdsRef.current.clear();
    submit.reset();
  };

  // runImport mints a fresh idempotency key (a dry run and its later real import
  // are different actions → different keys → different fingerprints server-side),
  // posts the held file, and adopts the returned job for polling. The returned
  // pending job is seeded into the cache so progress shows without a poll delay.
  const runImport = (dry: boolean) => {
    if (!file || submit.isPending) return;
    submit.mutate(
      { file, dryRun: dry, idempotencyKey: mintIdempotencyKey() },
      {
        onSuccess: (started) => {
          heldFileJobIdsRef.current.add(started.id);
          startedJobIdsRef.current.add(started.id);
          qc.setQueryData(keys.importJob(started.id), started);
          setJobId(started.id);
        },
      },
    );
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== "Tab") return;
    const focusables = dialogRef.current?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input, [href], [tabindex]:not([tabindex="-1"])',
    );
    if (!focusables || focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  const jobs = jobsQ.data ?? [];

  return (
    <div className="dialog-backdrop" onClick={close}>
      <div
        ref={dialogRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Import catalog"
        style={{ width: "min(560px, 100%)", maxHeight: "85vh", overflowY: "auto" }}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <div className="dialog-title">Import catalog</div>
          <button type="button" className="btn btn-icon btn-secondary" onClick={close} aria-label="Close">
            <Icon name="ph-x" size={16} />
          </button>
        </div>

        <div className="dialog-body" style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
          {/* — submit form — */}
          <div className="field">
            <label>File</label>
            <label
              className="input"
              style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer", position: "relative" }}
            >
              <Icon name="ph-file-text" size={15} color="var(--color-neutral-400)" />
              <span style={{ color: file ? "var(--color-text)" : "var(--color-neutral-500)" }}>
                {file ? file.name : "Choose an .ndjson / .jsonl file"}
              </span>
              <input
                type="file"
                accept=".ndjson,.jsonl,application/x-ndjson"
                onChange={onPickFile}
                style={{ position: "absolute", opacity: 0, width: 0, height: 0 }}
              />
            </label>
          </div>

          <label style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13, cursor: "pointer" }}>
            {/* autoFocus moves focus into the dialog on open so the onKeyDown
                Escape/Tab-trap engages immediately, matching NewCrawlModal. */}
            <input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} autoFocus />
            Dry run — validate the file and report what would happen, without writing
          </label>

          <div
            style={{
              display: "flex",
              gap: 9,
              alignItems: "flex-start",
              fontSize: 12,
              color: "var(--color-neutral-400)",
              background: "color-mix(in srgb, var(--color-accent) 8%, transparent)",
              padding: "var(--space-3)",
              borderRadius: "var(--radius-md)",
            }}
          >
            <Icon name="ph-info" size={15} color="var(--color-accent-300)" style={{ flex: "none", marginTop: 1 }} />
            <span>
              NDJSON — one JSON object per line, each a company with nested <code>careerPages</code>. Export the catalog
              to see the exact shape. Imports merge: they extend history and never overwrite richer data.
            </span>
          </div>

          {submit.isError && (
            <div style={{ fontSize: 12, color: "var(--color-accent-300)" }}>
              {submit.error instanceof Error ? submit.error.message : "Could not start the import."}
            </div>
          )}

          <div className="dialog-actions" style={{ marginTop: 0 }}>
            <button
              type="button"
              className="btn btn-primary"
              disabled={!file || submit.isPending}
              onClick={() => runImport(dryRun)}
            >
              <Icon name="ph-download-simple" size={14} />{" "}
              {submit.isPending ? "Starting…" : dryRun ? "Dry run" : "Import"}
            </button>
          </div>

          {/* — active job — */}
          {jobId && (
            <ActiveJob
              job={job}
              canResubmit={!!file && !!job && heldFileJobIdsRef.current.has(job.id)}
              onRunReal={() => runImport(false)}
              onRetry={(d) => runImport(d)}
            />
          )}

          {/* — recent imports — */}
          {jobs.length > 0 && (
            <div>
              <div style={{ fontSize: 12, color: "var(--color-neutral-500)", marginBottom: "var(--space-2)" }}>
                Recent imports
              </div>
              <div style={{ display: "flex", flexDirection: "column", gap: 4, maxHeight: 180, overflowY: "auto" }}>
                {jobs.map((j) => (
                  <RecentRow
                    key={j.id}
                    job={j}
                    active={j.id === jobId}
                    onClick={() => {
                      // Seed the detail cache from the row so ActiveJob renders
                      // instantly instead of unmounting for the fetch round trip
                      // (the height jump read as a flicker). Never overwrite
                      // fresher polled data with the list's snapshot.
                      if (!qc.getQueryData(keys.importJob(j.id))) qc.setQueryData(keys.importJob(j.id), j);
                      setJobId(j.id);
                    }}
                  />
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// STATUS_TAG maps an Import Job status to a label + nocturne tag class. Kept
// local: the run-status meta in lib/status.ts has no "pending" and is
// crawl-specific.
const STATUS_TAG: Record<ImportJobStatus, { label: string; cls: string }> = {
  pending: { label: "Pending", cls: "tag tag-neutral" },
  running: { label: "Running", cls: "tag tag-accent" },
  completed: { label: "Completed", cls: "tag tag-accent-2" },
  failed: { label: "Failed", cls: "tag tag-outline" },
};

function StatusTag({ status }: { status: ImportJobStatus }) {
  const meta = STATUS_TAG[status];
  return <span className={meta.cls}>{meta.label}</span>;
}

// ActiveJob shows the selected job: a live spinner while pending/running, then a
// result (counters + capped, line-tagged error list) or the failure text. A
// completed dry run offers "Import for real"; a failed job offers a retry — both
// only when canResubmit says this job was started from the file still held this
// session, so the buttons can never post a different file than the results shown.
function ActiveJob({
  job,
  canResubmit,
  onRunReal,
  onRetry,
}: {
  job: ImportJob | undefined;
  canResubmit: boolean;
  onRunReal: () => void;
  onRetry: (dryRun: boolean) => void;
}) {
  if (!job) return null;
  const terminal = isImportTerminal(job.status);
  const verb = job.dryRun ? "would upsert" : "upserted";

  return (
    <div
      className="card"
      style={{ gap: "var(--space-3)", background: "color-mix(in srgb, var(--color-text) 4%, transparent)" }}
    >
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
          <span style={{ fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {job.filename}
          </span>
          {job.dryRun && <span className="tag tag-neutral">Dry run</span>}
        </div>
        <StatusTag status={job.status} />
      </div>

      {!terminal && (
        <div style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13, color: "var(--color-neutral-400)" }}>
          <Icon name="ph-circle-notch" size={15} style={{ animation: "spin 0.8s linear infinite" }} />
          {job.dryRun ? "Validating…" : "Importing…"}
        </div>
      )}

      {terminal && job.status === "failed" && (
        <div style={{ fontSize: 13, color: "var(--color-accent-300)" }}>{job.error || "Import failed."}</div>
      )}

      {terminal && job.status === "completed" && job.result && (
        <>
          <div style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 14 }}>
            <Icon name="ph-check-circle" size={16} color="var(--color-accent-2-400)" />
            <span>
              {job.result.companiesUpserted} companies · {job.result.pagesUpserted} pages {verb}
            </span>
          </div>
          {job.result.errorCount > 0 && (
            <div>
              <div style={{ fontSize: 12, color: "var(--color-neutral-500)", marginBottom: 4 }}>
                {job.result.errorCount} line {job.result.errorCount === 1 ? "error" : "errors"}
              </div>
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: 3,
                  maxHeight: 160,
                  overflowY: "auto",
                  fontSize: 12,
                  fontFamily: "ui-monospace, monospace",
                }}
              >
                {job.result.errors.map((e, i) => (
                  <div key={i} style={{ color: "var(--color-accent-300)" }}>
                    <span style={{ color: "var(--color-neutral-500)" }}>Line {e.line}:</span> {e.message}
                  </div>
                ))}
                {job.result.errorCount > job.result.errors.length && (
                  <div style={{ color: "var(--color-neutral-500)" }}>
                    …and {job.result.errorCount - job.result.errors.length} more
                  </div>
                )}
              </div>
            </div>
          )}
        </>
      )}

      {terminal && canResubmit && job.status === "completed" && job.dryRun && (
        <button type="button" className="btn btn-primary" onClick={onRunReal} style={{ alignSelf: "flex-start" }}>
          <Icon name="ph-download-simple" size={14} /> Import for real
        </button>
      )}
      {terminal && canResubmit && job.status === "failed" && (
        <button
          type="button"
          className="btn btn-secondary"
          onClick={() => onRetry(job.dryRun)}
          style={{ alignSelf: "flex-start" }}
        >
          <Icon name="ph-arrow-clockwise" size={14} /> Try again
        </button>
      )}
    </div>
  );
}

// RecentRow is one line in the recent-imports list. Clicking loads the job into
// the active view (resuming its poll if still live).
function RecentRow({ job, active, onClick }: { job: ImportJob; active: boolean; onClick: () => void }) {
  const counts = job.result ? `${job.result.companiesUpserted}c · ${job.result.pagesUpserted}p` : "—";
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 8,
        padding: "6px 8px",
        borderRadius: "var(--radius-sm)",
        border: `1px solid ${active ? "var(--color-accent)" : "transparent"}`,
        background: "transparent",
        // An unstyled <button> falls back to the UA's buttontext color (black),
        // unreadable on the dark dialog; inherit the dialog's text color.
        color: "inherit",
        cursor: "pointer",
        textAlign: "left",
        width: "100%",
      }}
    >
      <span
        style={{
          flex: 1,
          minWidth: 0,
          fontSize: 13,
          overflow: "hidden",
          textOverflow: "ellipsis",
          whiteSpace: "nowrap",
        }}
      >
        {job.filename}
        {job.dryRun && <span style={{ color: "var(--color-neutral-500)" }}> · dry run</span>}
      </span>
      <span style={{ fontSize: 12, color: "var(--color-neutral-500)", fontVariantNumeric: "tabular-nums" }}>
        {counts}
      </span>
      <span style={{ fontSize: 11, color: "var(--color-neutral-500)", width: 84, textAlign: "right" }}>
        {relativeTime(job.createdAt)}
      </span>
      <StatusTag status={job.status} />
    </button>
  );
}
