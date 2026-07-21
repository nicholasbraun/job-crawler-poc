import { useMemo, useState } from "react";

import type { CountryOption } from "../api";
import { Icon } from "./primitives";

// CountryMultiSelect is the New Keyword Crawl form's Country Constraint control
// (ADR-0028): a searchable, chip-based multi-select over a known-country set.
// It is purely presentational — options come from the caller (the keyword
// defaults endpoint's `countries`, never hardcoded) and selection is the
// controlled `selected` array of uppercase ISO alpha-2 codes. Selecting none
// means "anywhere", so the control never forces a choice. The options list is
// rendered inline and scrollable (not a floating popover) so it stays contained
// inside the focus-trapped modal and the ≤640px mobile layout.
export function CountryMultiSelect({
  options,
  selected,
  onChange,
  disabled = false,
}: {
  options: CountryOption[];
  selected: string[];
  onChange: (next: string[]) => void;
  disabled?: boolean;
}) {
  const [query, setQuery] = useState("");

  // Index the options by code so chips can resolve a display name from a bare
  // selected code, and so toggling stays O(1) per row.
  const byCode = useMemo(() => {
    const m = new Map<string, CountryOption>();
    for (const o of options) m.set(o.code, o);
    return m;
  }, [options]);

  // Filter by name OR code substring, case-insensitively. Options arrive already
  // name-sorted from the server, so no re-sort is needed.
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) => `${o.name} ${o.code}`.toLowerCase().includes(q));
  }, [options, query]);

  const toggle = (code: string) => {
    if (selected.includes(code)) {
      onChange(selected.filter((c) => c !== code));
    } else {
      onChange([...selected, code]);
    }
  };

  if (disabled) {
    return (
      <div style={{ fontSize: 12, color: "var(--color-neutral-500)" }}>
        Country options are unavailable — the crawl will target anywhere.
      </div>
    );
  }

  return (
    <div>
      {selected.length > 0 && (
        <div className="country-chips">
          {selected.map((code) => (
            <span key={code} className="tag tag-accent" style={{ gap: 5 }}>
              {byCode.get(code)?.name ?? code}
              <button
                type="button"
                aria-label={`Remove ${byCode.get(code)?.name ?? code}`}
                onClick={() => toggle(code)}
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  background: "transparent",
                  border: "none",
                  padding: 0,
                  marginLeft: 2,
                  cursor: "pointer",
                  color: "inherit",
                }}
              >
                <Icon name="ph-x" size={11} />
              </button>
            </span>
          ))}
        </div>
      )}

      <div style={{ position: "relative", display: "flex", alignItems: "center" }}>
        <Icon
          name="ph-magnifying-glass"
          size={14}
          color="var(--color-neutral-500)"
          style={{ position: "absolute", left: 10, pointerEvents: "none" }}
        />
        <input
          className="input"
          style={{ paddingLeft: 30 }}
          placeholder="Search countries…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      <div className="country-options app-scroll">
        {filtered.length === 0 ? (
          <div style={{ padding: "var(--space-3)", fontSize: 12, color: "var(--color-neutral-500)" }}>
            No countries match “{query.trim()}”.
          </div>
        ) : (
          filtered.map((o) => {
            const isSelected = selected.includes(o.code);
            return (
              <button
                key={o.code}
                type="button"
                className={`country-opt${isSelected ? " country-opt-selected" : ""}`}
                aria-pressed={isSelected}
                onClick={() => toggle(o.code)}
              >
                <span>
                  {o.name} <span style={{ color: "var(--color-neutral-500)" }}>{o.code}</span>
                </span>
                {isSelected && <Icon name="ph-check-circle" size={15} color="var(--color-accent)" />}
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}
