import { useState } from "react";

import type { CareerPage, Company } from "../api";
import { useCareerPages, useCompanies } from "../hooks";
import { fmt, hostOf, prettyUrl, relativeTime } from "../lib/format";
import type { NameTier } from "../lib/model";
import {
  atsSplit,
  careerPagesByCompany,
  companyInitial,
  companySource,
  nameSourceLabel,
  nameTier,
  verifiedNameShare,
} from "../lib/model";
import { ImportModal } from "../components/ImportModal";
import { PageShell } from "../components/PageShell";
import { Dot, EmptyState, ErrorNote, Icon, Loading, StatCard } from "../components/primitives";

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
  const [unverifiedOnly, setUnverifiedOnly] = useState(false);
  const [letter, setLetter] = useState<string>("all");
  const [importOpen, setImportOpen] = useState(false);
  const companiesQ = useCompanies();
  const pagesQ = useCareerPages();

  const companies = companiesQ.data ?? [];
  const pages = pagesQ.data ?? [];
  const split = atsSplit(companies);
  const verified = verifiedNameShare(companies);
  const pagesByCompany = careerPagesByCompany(pages);
  const atsProviders = new Set(companies.filter((c) => c.atsProvider).map((c) => c.atsProvider)).size;
  const avgPerCompany = companies.length ? (pages.length / companies.length).toFixed(1) : "0.0";

  // The source filter (ATS/self), the unverified toggle, and the A–Z letter
  // filter AND together. Source + unverified narrow first (base); `available` is
  // the set of initials present after them, so the index dims empty buckets;
  // then the letter filter applies.
  const base = companies.filter((c) => {
    const sourceOk = filter === "all" ? true : filter === "ats" ? c.atsProvider !== "" : c.atsProvider === "";
    const verifiedOk = !unverifiedOnly || nameTier(c) !== "structured";
    return sourceOk && verifiedOk;
  });
  const available = new Set(base.map(companyInitial));
  // Fall back to "all" when the selected bucket has no companies under the
  // current source filter (e.g. after switching All → ATS), so the table never
  // shows an empty view with a stale, disabled bucket still highlighted.
  const activeLetter = letter === "all" || available.has(letter) ? letter : "all";
  const byLetter = activeLetter === "all" ? base : base.filter((c) => companyInitial(c) === activeLetter);
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
      actions={
        <>
          {/* Icon convention: import brings data INTO the app (arrow down into
              the tray), export sends it OUT (arrow up) — not the raw
              upload/download transport direction. */}
          <button className="btn btn-secondary" onClick={() => setImportOpen(true)}>
            <Icon name="ph-download-simple" size={14} /> Import
          </button>
          {/* A plain anchor, not a fetch: the browser follows the server's
              Content-Disposition and saves the Catalog Export directly. */}
          <a className="btn btn-secondary" href="/api/catalog/export" download>
            <Icon name="ph-upload-simple" size={14} /> Export
          </a>
        </>
      }
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: "var(--space-4)" }}>
          <StatCard size="md" label="Companies" value={fmt(companies.length)} sub="globally-unique keys" />
          <StatCard size="md" label="Career pages" value={fmt(pages.length)} sub={`${avgPerCompany} avg per company`} />
          <StatCard size="md" label="Self-hosted" value={`${split.selfPct}%`} sub={`${fmt(split.selfCount)} companies`} />
          <StatCard size="md" label="ATS hubs" value={`${split.atsPct}%`} sub={`${fmt(split.atsCount)} · ${atsProviders} providers`} />
          <StatCard size="md" label="Verified names" value={`${verified.verifiedPct}%`} sub={`${fmt(verified.verifiedCount)} site-declared`} />
        </div>

        <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
            <h4 style={{ margin: 0, fontSize: 17 }}>Companies</h4>
            {/* Source is a mutually-exclusive segmented control; "Unverified
                only" is an orthogonal narrowing that ANDs on top, so it sits
                beside the control as a checkbox rather than as a fourth radio. */}
            <div style={{ display: "flex", gap: "var(--space-4)", alignItems: "center", flexWrap: "wrap" }}>
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
              <label
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: "var(--space-2)",
                  fontSize: 13,
                  color: "var(--color-neutral-400)",
                  cursor: "pointer",
                }}
              >
                <input
                  type="checkbox"
                  checked={unverifiedOnly}
                  onChange={(e) => setUnverifiedOnly(e.target.checked)}
                />
                Unverified only
              </label>
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
              <table className="table" style={{ minWidth: 560 }}>
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
      {/* Sibling of the content, not a child of the table: the modal is
          position:fixed, so its place in the tree does not affect layout. */}
      <ImportModal open={importOpen} onClose={() => setImportOpen(false)} />
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

// dotColor maps a name-confidence tier to the accent+neutral palette: verified
// names get the trusted accent, derived names a mid neutral, fallbacks a dim one.
function dotColor(tier: NameTier): string {
  switch (tier) {
    case "structured":
      return "var(--color-accent)";
    case "derived":
      return "var(--color-neutral-400)";
    case "fallback":
      return "var(--color-neutral-700)";
  }
}

function CompanyRow({ company, pages }: { company: Company; pages: CareerPage[] }) {
  const [expanded, setExpanded] = useState(false);
  const isAts = company.atsProvider !== "";
  const tier = nameTier(company);
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
              <div
                style={{ display: "flex", alignItems: "center", gap: "var(--space-2)", fontSize: 14 }}
                title={nameSourceLabel(company.nameSource)}
              >
                <Dot color={dotColor(tier)} size={8} />
                <span style={tier === "fallback" ? { color: "var(--color-neutral-500)", fontStyle: "italic" } : undefined}>
                  {company.name || company.displayDomain}
                </span>
              </div>
              {company.website ? (
                // Imported companies carry a homepage; link the domain to it. The
                // row toggles its career-page list on click, so following the link
                // must not also toggle the row (stopPropagation).
                <a
                  href={company.website}
                  target="_blank"
                  rel="noreferrer"
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    fontSize: 11,
                    color: "var(--color-neutral-500)",
                    textDecoration: "none",
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 4,
                    width: "fit-content",
                  }}
                >
                  {company.displayDomain || hostOf(company.website)}
                  <Icon name="ph-arrow-square-out" size={10} />
                </a>
              ) : (
                <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>{company.displayDomain}</div>
              )}
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
