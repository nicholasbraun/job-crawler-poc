import { useState } from "react";

import { useCreateSavedSearch } from "../hooks";
import { Dialog } from "./Dialog";
import { Icon } from "./primitives";

// ARRANGEMENTS are the three positively-stated working modes a SavedSearch filters
// by (ADR-0030). "unspecified" is a listing's honest default, never a filter facet.
const ARRANGEMENTS: { value: string; label: string }[] = [
  { value: "remote", label: "Remote" },
  { value: "onsite", label: "Onsite" },
  { value: "hybrid", label: "Hybrid" },
];

// splitList parses a comma/newline-separated field into trimmed, non-blank tokens.
function splitList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// SavedSearchModal creates a SavedSearch: a named, stored Corpus query (ADR-0037).
// The stored query is fixed at creation in v1 (only the name is later editable), so
// this is the one place its facets are set. On success it resets and closes; the
// panel then appears from the invalidated saved-searches list.
export function SavedSearchModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreateSavedSearch();

  const [name, setName] = useState("");
  const [keywords, setKeywords] = useState("");
  const [countries, setCountries] = useState("");
  const [arrangements, setArrangements] = useState<string[]>([]);

  const reset = () => {
    setName("");
    setKeywords("");
    setCountries("");
    setArrangements([]);
    create.reset();
  };
  const close = () => {
    reset();
    onClose();
  };

  if (!open) return null;

  const nameValid = name.trim().length > 0;
  const canSubmit = nameValid && !create.isPending;

  const toggleArrangement = (value: string) =>
    setArrangements((prev) => (prev.includes(value) ? prev.filter((v) => v !== value) : [...prev, value]));

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    create.mutate(
      {
        name: name.trim(),
        keywords: splitList(keywords),
        countries: splitList(countries).map((c) => c.toUpperCase()),
        workArrangements: arrangements,
      },
      {
        onSuccess: () => {
          reset();
          onClose();
        },
      },
    );
  };

  return (
    <Dialog title="New search" onClose={close} onSubmit={submit}>
      <div className="dialog-body" style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)" }}>
        <div className="field">
          <label>
            Name <span style={{ color: "var(--color-neutral-500)" }}>— required</span>
          </label>
          <input
            className="input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Remote Go roles in DACH"
            autoFocus
          />
          {!nameValid && (
            <div style={{ fontSize: 12, color: "var(--color-accent-300)", marginTop: 5 }}>
              Give the search a name.
            </div>
          )}
        </div>

        <div className="field">
          <label>
            Keywords <span style={{ color: "var(--color-neutral-500)" }}>— comma or newline separated, optional</span>
          </label>
          <input
            className="input"
            value={keywords}
            onChange={(e) => setKeywords(e.target.value)}
            placeholder="golang, backend"
          />
        </div>

        <div className="field">
          <label>
            Countries <span style={{ color: "var(--color-neutral-500)" }}>— ISO codes (DE, AT), optional</span>
          </label>
          <input
            className="input"
            value={countries}
            onChange={(e) => setCountries(e.target.value)}
            placeholder="DE, AT, CH"
          />
        </div>

        <div className="field">
          <label>
            Work arrangement <span style={{ color: "var(--color-neutral-500)" }}>— any if none picked</span>
          </label>
          <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-4)" }}>
            {ARRANGEMENTS.map((a) => (
              <label
                key={a.value}
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: "var(--space-2)",
                  fontSize: 13,
                  color: "var(--color-neutral-300)",
                  cursor: "pointer",
                }}
              >
                <input
                  type="checkbox"
                  checked={arrangements.includes(a.value)}
                  onChange={() => toggleArrangement(a.value)}
                />
                {a.label}
              </label>
            ))}
          </div>
        </div>

        {create.isError && (
          <div style={{ fontSize: 12, color: "var(--color-accent-300)" }}>
            {create.error instanceof Error ? create.error.message : "Could not create the search."}
          </div>
        )}
      </div>

      <div className="dialog-actions">
        <button type="button" className="btn btn-secondary" onClick={close}>
          Cancel
        </button>
        <button type="submit" className="btn btn-primary" disabled={!canSubmit}>
          <Icon name="ph-plus" size={14} /> {create.isPending ? "Creating…" : "Create search"}
        </button>
      </div>
    </Dialog>
  );
}
