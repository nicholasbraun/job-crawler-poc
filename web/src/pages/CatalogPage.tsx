import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listCareerPages, listCompanies, type Company } from "../api";
import {
  Card,
  formatTime,
  PageHeader,
  QueryState,
} from "../components/ui";

export function CatalogPage() {
  const [selected, setSelected] = useState<Company | null>(null);

  const companies = useQuery({
    queryKey: ["companies"],
    queryFn: listCompanies,
    refetchInterval: 3000,
  });

  const rows = companies.data ?? [];

  return (
    <div>
      <PageHeader
        title="Catalog"
        subtitle="Companies and Career Pages discovered by Discovery Crawls."
      />
      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Companies">
          <QueryState
            isLoading={companies.isLoading}
            error={companies.error}
            isEmpty={rows.length === 0}
            emptyMessage="No companies catalogued yet. Run a discovery crawl."
          >
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-slate-200 text-left text-xs tracking-wide text-slate-500 uppercase">
                    <th className="px-3 py-2">Company</th>
                    <th className="px-3 py-2">ATS</th>
                    <th className="px-3 py-2">Last seen</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((c) => (
                    <tr
                      key={c.id}
                      onClick={() => setSelected(c)}
                      className={`cursor-pointer border-b border-slate-100 last:border-0 hover:bg-slate-50 ${
                        selected?.id === c.id ? "bg-indigo-50" : ""
                      }`}
                    >
                      <td className="px-3 py-2">
                        <div className="font-medium text-slate-800">
                          {c.name || c.companyKey}
                        </div>
                        <div className="text-xs text-slate-400">
                          {c.displayDomain}
                        </div>
                      </td>
                      <td className="px-3 py-2 text-slate-600">
                        {c.atsProvider || "self-hosted"}
                      </td>
                      <td className="px-3 py-2 text-slate-500">
                        {formatTime(c.lastSeen)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </QueryState>
        </Card>

        <Card title={selected ? `Career pages — ${selected.name || selected.companyKey}` : "Career pages"}>
          {selected ? (
            <CareerPages companyId={selected.id} />
          ) : (
            <p className="text-sm text-slate-500">
              Select a company to see its Career Pages.
            </p>
          )}
        </Card>
      </div>
    </div>
  );
}

function CareerPages({ companyId }: { companyId: string }) {
  const pages = useQuery({
    queryKey: ["career-pages", companyId],
    queryFn: () => listCareerPages(companyId),
  });

  const rows = pages.data ?? [];

  return (
    <QueryState
      isLoading={pages.isLoading}
      error={pages.error}
      isEmpty={rows.length === 0}
      emptyMessage="No career pages for this company."
    >
      <ul className="divide-y divide-slate-100">
        {rows.map((p) => (
          <li key={p.id} className="py-2">
            <a
              href={p.url}
              target="_blank"
              rel="noreferrer"
              className="text-sm text-indigo-600 hover:underline"
            >
              {p.url}
            </a>
            <div className="text-xs text-slate-400">
              {p.politenessDomain} · last seen {formatTime(p.lastSeen)}
            </div>
          </li>
        ))}
      </ul>
    </QueryState>
  );
}
