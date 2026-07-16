import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import { useCreateCrawl, useDefinitionDefaults } from "../hooks";
import { Dialog } from "./Dialog";
import { Icon } from "./primitives";

// DiscoveryStartModal creates and starts the single perpetual Discovery Crawl.
// It prefills from GET /api/definitions/defaults?kind=discovery (editable Seed
// list + depth), auto-names the definition "discovery", and posts the fused
// /api/crawls endpoint. On success it navigates to the Discovery page, which
// then shows the running crawl. A duplicate discovery (409) surfaces inline.
export function DiscoveryStartModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const defaults = useDefinitionDefaults("discovery", open);
  const create = useCreateCrawl();
  const navigate = useNavigate();

  const [seeds, setSeeds] = useState("");
  const [depth, setDepth] = useState("10");
  const [prefilled, setPrefilled] = useState(false);

  // Prefill once from the defaults endpoint after it loads; keep any edits the
  // user has since made. reset() clears `prefilled` so a reopen refills.
  useEffect(() => {
    if (open && defaults.data && !prefilled) {
      setSeeds((defaults.data.seedUrls ?? []).join("\n"));
      setDepth(String(defaults.data.maxDepth));
      setPrefilled(true);
    }
  }, [open, defaults.data, prefilled]);

  const reset = () => {
    setSeeds("");
    setDepth("10");
    setPrefilled(false);
    create.reset();
  };
  const close = () => {
    reset();
    onClose();
  };

  if (!open) return null;

  const parsedSeeds = seeds
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  const depthNum = Number(depth);
  const depthValid = Number.isInteger(depthNum) && depthNum >= 1 && depthNum <= 20;
  // `prefilled` blocks submitting before defaults load (the textarea would be
  // empty); combined with the empty-seed guard it never posts an empty seed set.
  const canSubmit = parsedSeeds.length > 0 && depthValid && !create.isPending && prefilled;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    create.mutate(
      { name: "discovery", kind: "discovery", seedUrls: parsedSeeds, maxDepth: depthNum },
      {
        onSuccess: () => {
          reset();
          onClose();
          navigate("/discovery");
        },
      },
    );
  };

  return (
    <Dialog title="Start discovery" onClose={close} onSubmit={submit}>
      <div className="dialog-body" style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
        <div className="field">
          <label>
            Seed URLs <span style={{ color: "var(--color-neutral-500)" }}>— one per line, required</span>
          </label>
          <textarea
            className="input"
            style={{ minHeight: 160, resize: "vertical", fontFamily: "ui-monospace, monospace", fontSize: 13 }}
            value={seeds}
            onChange={(e) => setSeeds(e.target.value)}
            autoFocus
          />
          {parsedSeeds.length === 0 && (
            <div style={{ fontSize: 12, color: "var(--color-accent-300)", marginTop: 5 }}>
              Enter at least one seed URL (one per line).
            </div>
          )}
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
            There is one perpetual discovery crawl. It walks these seed domains, following the URL filters toward
            career pages, and catalogues confirmed hits — the seed set every keyword crawl draws from.
          </span>
        </div>

        {defaults.isError && (
          <div style={{ fontSize: 12, color: "var(--color-accent-300)" }}>Could not load the baseline seeds.</div>
        )}
        {create.isError && (
          <div style={{ fontSize: 12, color: "var(--color-accent-300)" }}>
            {create.error instanceof Error ? create.error.message : "Could not start the discovery crawl."}
          </div>
        )}
      </div>

      <div className="dialog-actions">
        <button type="button" className="btn btn-secondary" onClick={close}>
          Cancel
        </button>
        <button type="submit" className="btn btn-primary" disabled={!canSubmit}>
          <Icon name="ph-play" size={14} /> {create.isPending ? "Starting…" : "Start discovery"}
        </button>
      </div>
    </Dialog>
  );
}
