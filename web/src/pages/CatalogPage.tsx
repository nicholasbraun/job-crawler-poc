import { useState } from "react";

import type { CareerPage, Company } from "../api";
import { useCareerPages, useCompanies } from "../hooks";
import { fmt, prettyUrl, relativeTime } from "../lib/format";
import { atsSplit, careerPagesByCompany, companyInitial, companySource } from "../lib/model";
import { PageShell } from "../components/PageShell";
import { EmptyState, ErrorNote, Icon, Loading, StatCard } from "../components/primitives";

type Filter = "all" | "ats" | "self";
const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "ats", label: "ATS" },
  { key: "self", label: "Self-hosted" },
];

// LETTERS drives the A–Z index; the "0-9" bucket (rendered "0–9") and the "all"
// reset sentinel are handled separately from this list.
const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ".split("");

export function CatalogPage() {
  const [filter, setFilter] = useState<Filter>("all");
  const [letter, setLetter] = useState<string>("all");
  const companiesQ = useCompanies();
  const pagesQ = useCareerPages();

  const companies = companiesQ.data ?? [];
  const pages = pagesQ.data ?? [];
  const split = atsSplit(companies);
  const pagesByCompany = careerPagesByCompany(pages);
  const atsProviders = new Set(companies.filter((c) => c.atsProvider).map((c) => c.atsProvider)).size;
  const avgPerCompany = companies.length ? (pages.length / companies.length).toFixed(1) : "0.0";

  // The ATS/self source filter and the A–Z letter filter AND together: the
  // source filter narrows first (bySource), then the letter filter. `available`
  // is the set of initials present after the source filter, so the index dims
  // buckets that have no rows under the current source selection.
  const bySource = companies.filter((c) =>
    filter === "all" ? true : filter === "ats" ? c.atsProvider !== "" : c.atsProvider === "",
  );
  const available = new Set(bySource.map(companyInitial));
  // Fall back to "all" when the selected bucket has no companies under the
  // current source filter (e.g. after switching All → ATS), so the table never
  // shows an empty view with a stale, disabled bucket still highlighted.
  const activeLetter = letter === "all" || available.has(letter) ? letter : "all";
  const byLetter = activeLetter === "all" ? bySource : bySource.filter((c) => companyInitial(c) === activeLetter);
  const filtered = [...byLetter].sort((a, b) =>
    (a.name || a.displayDomain).localeCompare(b.name || b.displayDomain, undefined, { sensitivity: "base" }),
  );

  const error = companiesQ.error ?? pagesQ.error;
  const loading = companiesQ.isLoading || pagesQ.isLoading;

  return (
    <PageShell
      title="Catalog"
      subtitle="Companies and career pages the discovery run has catalogued"
      back={{ to: "/", label: "Overview" }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: "var(--space-4)" }}>
          <StatCard size="md" label="Companies" value={fmt(companies.length)} sub="globally-unique keys" />
          <StatCard size="md" label="Career pages" value={fmt(pages.length)} sub={`${avgPerCompany} avg per company`} />
          <StatCard size="md" label="Self-hosted" value={`${split.selfPct}%`} sub={`${fmt(split.selfCount)} companies`} />
          <StatCard size="md" label="ATS hubs" value={`${split.atsPct}%`} sub={`${fmt(split.atsCount)} · ${atsProviders} providers`} />
        </div>

        <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
            <h4 style={{ margin: 0, fontSize: 17 }}>Companies</h4>
            <div className="seg">
              {FILTERS.map((f) => (
                <label key={f.key} className="seg-opt">
                  <input
                    type="radio"
                    name="catfilter"
                    checked={filter === f.key}
                    onChange={() => setFilter(f.key)}
                  />
                  {f.label}
                </label>
              ))}
            </div>
          </div>

          <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>
            <AlphaButton label="All" active={activeLetter === "all"} enabled onClick={() => setLetter("all")} />
            {LETTERS.map((l) => (
              <AlphaButton
                key={l}
                label={l}
                active={activeLetter === l}
                enabled={available.has(l)}
                onClick={() => setLetter(l)}
              />
            ))}
            <AlphaButton
              label="0–9"
              active={activeLetter === "0-9"}
              enabled={available.has("0-9")}
              onClick={() => setLetter("0-9")}
            />
          </div>

          {error ? (
            <ErrorNote error={error} />
          ) : loading ? (
            <Loading />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon="ph-buildings"
              title={companies.length === 0 ? "Nothing catalogued yet" : "No companies match this filter"}
              hint={companies.length === 0 ? "The discovery run catalogues companies as it confirms career pages." : undefined}
            />
          ) : (
            <div style={{ overflowX: "auto" }}>
              <table className="table">
                <thead>
                  <tr>
                    <th>Company</th>
                    <th>Identity key</th>
                    <th>Source</th>
                    <th style={{ textAlign: "right" }}>Career pages</th>
                    <th style={{ textAlign: "right" }}>Last seen</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((c) => (
                    <CompanyRow key={c.id} company={c} pages={pagesByCompany.get(c.id) ?? []} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </PageShell>
  );
}

// AlphaButton is one toggle in the A–Z index. Disabled (no companies under the
// current source filter) buttons dim and stop responding; the active bucket
// picks up accent styling.
function AlphaButton({
  label,
  active,
  enabled,
  onClick,
}: {
  label: string;
  active: boolean;
  enabled: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      disabled={!enabled}
      onClick={enabled ? onClick : undefined}
      style={{
        padding: "4px 8px",
        fontSize: 12,
        minWidth: 26,
        textAlign: "center",
        borderRadius: "var(--radius-sm)",
        border: `1px solid ${active ? "var(--color-accent)" : "var(--color-divider)"}`,
        background: "transparent",
        color: active ? "var(--color-accent)" : "var(--color-neutral-400)",
        boxShadow: active ? "inset 0 0 0 1px var(--color-accent)" : undefined,
        cursor: enabled ? "pointer" : "default",
        opacity: enabled ? 1 : 0.35,
        fontVariantNumeric: "tabular-nums",
      }}
    >
      {label}
    </button>
  );
}

function CompanyRow({ company, pages }: { company: Company; pages: CareerPage[] }) {
  const [expanded, setExpanded] = useState(false);
  const isAts = company.atsProvider !== "";
  const expandable = pages.length > 0;
  return (
    <>
      <tr
        onClick={expandable ? () => setExpanded((e) => !e) : undefined}
        onKeyDown={
          expandable
            ? (e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setExpanded((v) => !v);
                }
              }
            : undefined
        }
        role={expandable ? "button" : undefined}
        tabIndex={expandable ? 0 : undefined}
        style={expandable ? { cursor: "pointer" } : undefined}
        aria-expanded={expandable ? expanded : undefined}
      >
        <td>
          <div style={{ display: "flex", alignItems: "center", gap: "var(--space-3)" }}>
            <Icon
              name={expanded ? "ph-caret-down" : "ph-caret-right"}
              size={12}
              color="var(--color-neutral-500)"
              style={{ visibility: expandable ? "visible" : "hidden" }}
            />
            <div>
              <div style={{ fontSize: 14 }}>{company.name || company.displayDomain}</div>
              <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>{company.displayDomain}</div>
            </div>
          </div>
        </td>
        <td>
          <code style={{ fontSize: 12, color: "var(--color-neutral-300)", fontFamily: "ui-monospace, monospace" }}>
            {company.companyKey}
          </code>
        </td>
        <td>
          <span className={isAts ? "tag tag-accent-2" : "tag tag-neutral"}>{companySource(company)}</span>
        </td>
        <td style={{ textAlign: "right", fontVariantNumeric: "tabular-nums" }}>{pages.length}</td>
        <td style={{ textAlign: "right", color: "var(--color-neutral-500)", fontSize: 12 }}>{relativeTime(company.lastSeen)}</td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={5} style={{ paddingLeft: "calc(var(--space-2) + 12px + var(--space-3))" }}>
            <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-2)", padding: "var(--space-2) 0" }}>
              {pages.map((p) => (
                <a
                  key={p.id}
                  href={p.url}
                  target="_blank"
                  rel="noreferrer"
                  style={{
                    fontSize: 13,
                    textDecoration: "none",
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 6,
                    width: "fit-content",
                  }}
                >
                  <Icon name="ph-arrow-square-out" size={13} />
                  {prettyUrl(p.url)}
                </a>
              ))}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}
