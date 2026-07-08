import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listDefinitions, listListings } from "../api";
import { Card, PageHeader, QueryState } from "../components/ui";

export function ListingsPage() {
  const [definitionId, setDefinitionId] = useState("");
  const [keyword, setKeyword] = useState("");

  const definitions = useQuery({
    queryKey: ["definitions"],
    queryFn: listDefinitions,
  });

  const listings = useQuery({
    queryKey: ["listings", definitionId, keyword],
    queryFn: () => listListings({ definitionId, keyword }),
    enabled: definitionId !== "",
  });

  const defs = definitions.data ?? [];
  const rows = listings.data ?? [];

  return (
    <div>
      <PageHeader
        title="Listings"
        subtitle="Job listings extracted by Keyword Crawls, per definition."
      />

      <Card title="Filter">
        <div className="flex flex-wrap items-end gap-4">
          <label className="grid gap-1 text-sm">
            <span className="font-medium text-slate-600">Definition</span>
            <select
              className="min-w-56 rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
              value={definitionId}
              onChange={(e) => setDefinitionId(e.target.value)}
            >
              <option value="">Select a definition…</option>
              {defs.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name} ({d.kind})
                </option>
              ))}
            </select>
          </label>

          <label className="grid gap-1 text-sm">
            <span className="font-medium text-slate-600">Keyword</span>
            <input
              className="rounded-md border border-slate-300 px-3 py-1.5 text-sm focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
              placeholder="filter title/description…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
            />
          </label>
        </div>
      </Card>

      <div className="mt-6">
        <Card title="Listings">
          {definitionId === "" ? (
            <p className="text-sm text-slate-500">
              Select a definition to see its listings.
            </p>
          ) : (
            <QueryState
              isLoading={listings.isLoading}
              error={listings.error}
              isEmpty={rows.length === 0}
              emptyMessage="No listings match this filter."
            >
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-slate-200 text-left text-xs tracking-wide text-slate-500 uppercase">
                      <th className="px-3 py-2">Title</th>
                      <th className="px-3 py-2">Company</th>
                      <th className="px-3 py-2">Location</th>
                      <th className="px-3 py-2">Remote</th>
                      <th className="px-3 py-2">Tech</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((jl) => (
                      <tr
                        key={jl.url}
                        className="border-b border-slate-100 last:border-0 align-top"
                      >
                        <td className="px-3 py-2">
                          <a
                            href={jl.url}
                            target="_blank"
                            rel="noreferrer"
                            className="font-medium text-indigo-600 hover:underline"
                          >
                            {jl.title || jl.url}
                          </a>
                        </td>
                        <td className="px-3 py-2 text-slate-700">
                          {jl.company || "—"}
                        </td>
                        <td className="px-3 py-2 text-slate-600">
                          {jl.location || "—"}
                        </td>
                        <td className="px-3 py-2 text-slate-600">
                          {jl.remote ? "yes" : "no"}
                        </td>
                        <td className="px-3 py-2">
                          <div className="flex flex-wrap gap-1">
                            {jl.techStack.map((t) => (
                              <span
                                key={t}
                                className="rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600"
                              >
                                {t}
                              </span>
                            ))}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </QueryState>
          )}
        </Card>
      </div>
    </div>
  );
}
