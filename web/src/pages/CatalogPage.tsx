import { useState } from "react";

import type { CareerPage, Company } from "../api";
import { useCareerPages, useCompanies } from "../hooks";
import { fmt, prettyUrl, relativeTime } from "../lib/format";
import { atsSplit, careerPagesByCompany, companySource } from "../lib/model";
import { PageShell } from "../components/PageShell";
import { EmptyState, ErrorNote, Icon, Loading, StatCard } from "../components/primitives";

type Filter = "all" | "ats" | "self";
const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "ats", label: "ATS" },
  { key: "self", label: "Self-hosted" },
];

export function CatalogPage() {
  const [filter, setFilter] = useState<Filter>("all");
  const companiesQ = useCompanies();
  const pagesQ = useCareerPages();

  const companies = companiesQ.data ?? [];
  const pages = pagesQ.data ?? [];
  const split = atsSplit(companies);
  const pagesByCompany = careerPagesByCompany(pages);
  const atsProviders = new Set(companies.filter((c) => c.atsProvider).map((c) => c.atsProvider)).size;
  const avgPerCompany = companies.length ? (pages.length / companies.length).toFixed(1) : "0.0";

  const filtered = companies.filter((c) =>
    filter === "all" ? true : filter === "ats" ? c.atsProvider !== "" : c.atsProvider === "",
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

function CompanyRow({ company, pages }: { company: Company; pages: CareerPage[] }) {
  const [expanded, setExpanded] = useState(false);
  const isAts = company.atsProvider !== "";
  const expandable = pages.length > 0;
  return (
    <>
      <tr
        onClick={expandable ? () => setExpanded((e) => !e) : undefined}
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
