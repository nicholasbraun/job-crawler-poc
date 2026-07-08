import { NavLink, Outlet } from "react-router-dom";

const NAV = [
  { to: "/", label: "Runs", end: true },
  { to: "/definitions", label: "Definitions", end: false },
  { to: "/catalog", label: "Catalog", end: false },
  { to: "/listings", label: "Listings", end: false },
];

export function Layout() {
  return (
    <div className="min-h-screen bg-slate-50 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex max-w-6xl items-center gap-8 px-6 py-3">
          <span className="text-lg font-bold tracking-tight text-indigo-600">
            Job Crawler
          </span>
          <nav className="flex gap-1">
            {NAV.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) =>
                  `rounded-md px-3 py-1.5 text-sm font-medium transition-colors ${
                    isActive
                      ? "bg-indigo-50 text-indigo-700"
                      : "text-slate-600 hover:bg-slate-100"
                  }`
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-6 py-8">
        <Outlet />
      </main>
    </div>
  );
}
