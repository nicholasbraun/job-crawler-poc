import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import { useCareerPages, useCreateCrawl, useDefinitionDefaults } from "../hooks";
import { fmt } from "../lib/format";
import { Dialog } from "./Dialog";
import { Icon } from "./primitives";

// NewCrawlModal collects a name + keywords + editable crawl depth and fires the
// fused create-and-start endpoint. Depth is prefilled from GET
// /api/definitions/defaults?kind=keyword (default 4). A keyword crawl seeds from
// the catalog (no seed URLs), so the copy reflects the current catalogued
// career-page count. On success it navigates to the new crawl's detail view. The
// backdrop/focus-trap/Escape scaffolding lives in the shared Dialog.
export function NewCrawlModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [keywords, setKeywords] = useState("");
  const [depth, setDepth] = useState("4");
  const [prefilled, setPrefilled] = useState(false);
  const create = useCreateCrawl();
  const navigate = useNavigate();
  const careerPages = useCareerPages();
  const careerPageCount = careerPages.data?.length ?? 0;
  const defaults = useDefinitionDefaults("keyword", open);

  // Prefill the depth once from the keyword defaults endpoint (default 4) after
  // it loads; keep any edit the user has since made. Unlike DiscoveryStartModal
  // this never gates submit — `depth` inits to a valid "4", so the keyword modal
  // stays usable even if the defaults fetch is slow or fails. reset() clears
  // `prefilled` so a reopen refills.
  useEffect(() => {
    if (open && defaults.data && !prefilled) {
      setDepth(String(defaults.data.maxDepth));
      setPrefilled(true);
    }
  }, [open, defaults.data, prefilled]);

  if (!open) return null;

  const parsedKeywords = keywords
    .split(",")
    .map((k) => k.trim())
    .filter(Boolean);
  const depthNum = Number(depth);
  const depthValid = Number.isInteger(depthNum) && depthNum >= 1 && depthNum <= 20;
  const canSubmit = parsedKeywords.length > 0 && depthValid && !create.isPending;

  const reset = () => {
    setName("");
    setKeywords("");
    setDepth("4");
    setPrefilled(false);
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
      { name: name.trim() || parsedKeywords[0], kind: "keyword", keywords: parsedKeywords, maxDepth: depthNum },
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
        <div className="field">
          <label>
            Crawl depth <span style={{ color: "var(--color-neutral-500)" }}>— 1–20</span>
          </label>
          <input
            className="input"
            type="number"
            min={1}
            max={20}
            value={depth}
            onChange={(e) => setDepth(e.target.value)}
          />
          {!depthValid && (
            <div style={{ fontSize: 12, color: "var(--color-accent-300)", marginTop: 5 }}>
              Depth must be a whole number between 1 and 20.
            </div>
          )}
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
