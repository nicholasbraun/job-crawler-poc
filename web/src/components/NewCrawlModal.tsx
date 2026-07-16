import { useState } from "react";
import { useNavigate } from "react-router-dom";

import { useCareerPages, useCreateCrawl } from "../hooks";
import { fmt } from "../lib/format";
import { Dialog } from "./Dialog";
import { Icon } from "./primitives";

// NewCrawlModal collects a name + keywords and fires the fused create-and-start
// endpoint. A keyword crawl seeds from the catalog (no seed URLs), so the copy
// reflects the current catalogued career-page count. On success it navigates to
// the new crawl's detail view. The backdrop/focus-trap/Escape scaffolding lives
// in the shared Dialog.
export function NewCrawlModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [keywords, setKeywords] = useState("");
  const create = useCreateCrawl();
  const navigate = useNavigate();
  const careerPages = useCareerPages();
  const careerPageCount = careerPages.data?.length ?? 0;

  if (!open) return null;

  const parsedKeywords = keywords
    .split(",")
    .map((k) => k.trim())
    .filter(Boolean);
  const canSubmit = parsedKeywords.length > 0 && !create.isPending;

  const reset = () => {
    setName("");
    setKeywords("");
    create.reset();
  };
  const close = () => {
    reset();
    onClose();
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    create.mutate(
      { name: name.trim() || parsedKeywords[0], kind: "keyword", keywords: parsedKeywords },
      {
        onSuccess: (run) => {
          reset();
          onClose();
          navigate(`/crawls/${run.definitionId}`);
        },
      },
    );
  };

  return (
    <Dialog title="New keyword crawl" onClose={close} onSubmit={submit}>
      <div className="dialog-body" style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
        <div className="field">
          <label>Name</label>
          <input
            className="input"
            placeholder="e.g. Senior Frontend Engineer"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
        </div>
        <div className="field">
          <label>
            Keywords <span style={{ color: "var(--color-neutral-500)" }}>— comma separated, required</span>
          </label>
          <input
            className="input"
            placeholder="React, TypeScript, Senior, Remote"
            value={keywords}
            onChange={(e) => setKeywords(e.target.value)}
          />
        </div>
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
            A keyword crawl seeds from the catalog — it searches all {fmt(careerPageCount)} catalogued career pages and
            gates pages by these keywords. It runs bounded and completes when the frontier drains.
          </span>
        </div>

        {create.isError && (
          <div style={{ fontSize: 12, color: "var(--color-accent-300)" }}>
            {create.error instanceof Error ? create.error.message : "Could not create the crawl."}
          </div>
        )}
      </div>

      <div className="dialog-actions">
        <button type="button" className="btn btn-secondary" onClick={close}>
          Cancel
        </button>
        <button type="submit" className="btn btn-primary" disabled={!canSubmit}>
          <Icon name="ph-play" size={14} /> {create.isPending ? "Starting…" : "Create & start"}
        </button>
      </div>
    </Dialog>
  );
}
