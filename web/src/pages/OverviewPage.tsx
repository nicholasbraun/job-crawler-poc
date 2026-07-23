import { Link } from "react-router-dom";

import {
  useCareerPages,
  useCatalogHistory,
  useCompanies,
  useDefinitions,
  useIsMobile,
  useRecentListings,
  useRuns,
} from "../hooks";
import { fmt, prettyUrl, relativeTime } from "../lib/format";
import {
  atsSplit,
  buildCollection,
  buildDiscovery,
  recentlyCatalogued,
  type Collection,
  type Discovery,
} from "../lib/model";
import type { Company, CareerPage, Listing } from "../api";
import { useLayout } from "../components/Layout";
import { PageShell } from "../components/PageShell";
import { EmptyState, Icon, RunControls, Sparkline, StatCard, StatusTag } from "../components/primitives";

export function OverviewPage() {
  const runs = useRuns();
  const definitions = useDefinitions();
  const companiesQ = useCompanies();
  const pagesQ = useCareerPages();

  const defs = definitions.data ?? [];
  const runList = runs.data ?? [];
  const companies = companiesQ.data ?? [];
  const pages = pagesQ.data ?? [];

  const discovery = buildDiscovery(defs, runList);
  const collection = buildCollection(defs, runList);
  const split = atsSplit(companies);
  const atsProviders = new Set(companies.filter((c) => c.atsProvider).map((c) => c.atsProvider)).size;
  const isMobile = useIsMobile();

  return (
    <PageShell
      title="Overview"
      subtitle={discovery ? "One perpetual discovery run" : "No discovery run"}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-6)" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: "var(--space-4)" }}>
          <StatCard
            label="Companies catalogued"
            value={fmt(companies.length)}
            icon="ph-buildings"
            sub={`${atsProviders} ATS providers + self-hosted`}
          />
          <StatCard label="Career pages" value={fmt(pages.length)} icon="ph-stack" sub="catalogued career pages" />
          <StatCard
            label="Discovery frontier"
            value={fmt(discovery?.frontierSize ?? 0)}
            icon="ph-broadcast"
            sub="URLs queued · perpetual"
            subColor="var(--color-accent-300)"
          />
        </div>

        {/* minmax(0, …) keeps a flexible track from growing past the viewport
            when an item's content (the sparkline, the counters) is intrinsically
            wide — the track clamps and the content shrinks instead. */}
        <div style={{ display: "grid", gridTemplateColumns: isMobile ? "minmax(0, 1fr)" : "minmax(0, 1.35fr) minmax(0, 1fr)", gap: "var(--space-4)", alignItems: "start" }}>
          <DiscoveryPanel discovery={discovery} careerPages={pages.length} companies={companies.length} split={split} />
          <RecentlyCatalogued pages={pages} companies={companies} />
        </div>

        <div style={{ display: "grid", gridTemplateColumns: isMobile ? "minmax(0, 1fr)" : "minmax(0, 1.35fr) minmax(0, 1fr)", gap: "var(--space-4)", alignItems: "start" }}>
          <CollectionPanel collection={collection} />
          <RecentlyFound />
        </div>
      </div>
    </PageShell>
  );
}

// CollectionPanel is the Overview's window on the perpetual Collection Cycle: its
// run status, the listings it has collected this cycle, and lifecycle controls. It
// mirrors DiscoveryPanel — the two singleton runs sit side by side.
function CollectionPanel({ collection }: { collection: Collection | null }) {
  // Null only on a pre-inversion database that has no collection definition; the
  // migration seeds one and the scheduler starts it, so this is the cold-boot case.
  if (!collection) {
    return (
      <EmptyState
        icon="ph-package"
        title="Collection hasn't started"
        hint="The perpetual Collection Cycle enumerates every catalogued company's open listings into the corpus. It starts automatically once the collection definition is seeded."
      />
    );
  }

  const idle = collection.status === "idle";
  const counters = [
    { label: "pages crawled", value: fmt(collection.pagesCrawled) },
    { label: "frontier size", value: fmt(collection.frontierSize) },
    { label: "listings this cycle", value: fmt(collection.listingsFound) },
  ];

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
        <div style={{ display: "flex", gap: 12, minWidth: 0 }}>
          <span
            style={{
              display: "grid",
              placeItems: "center",
              width: 38,
              height: 38,
              borderRadius: 10,
              background: "var(--color-accent-800)",
              color: "var(--color-accent-200)",
              flex: "none",
            }}
          >
            <Icon name="ph-package" size={21} />
          </span>
          <div>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <h4 style={{ margin: 0, fontSize: 18 }}>Collection cycle</h4>
              <StatusTag status={collection.status} />
            </div>
            <div style={{ fontSize: 12, color: "var(--color-neutral-400)", marginTop: 2 }}>
              {idle
                ? "Perpetual · scheduled · fills the corpus"
                : `Perpetual · fills the corpus · started ${relativeTime(collection.startedAt)}`}
            </div>
          </div>
        </div>
        <div style={{ display: "flex", gap: "var(--space-2)", flex: "none" }}>
          <RunControls status={collection.status} runId={collection.runId} definitionId={collection.definition.id} />
        </div>
      </div>

      <div style={{ display: "flex", alignItems: "flex-end", gap: "var(--space-8)" }}>
        <div>
          <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 44, lineHeight: 1, letterSpacing: "-0.02em" }}>
            {fmt(collection.listingsFound)}
          </div>
          <div style={{ fontSize: 12, color: "var(--color-neutral-400)", marginTop: 4 }}>listings collected this cycle</div>
        </div>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "var(--space-4)", paddingTop: "var(--space-2)" }}>
        {counters.map((c) => (
          <div key={c.label}>
            <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 19 }}>{c.value}</div>
            <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>{c.label}</div>
          </div>
        ))}
      </div>

      <Link to="/searches" style={{ alignSelf: "flex-start", fontSize: 13, marginTop: "var(--space-2)", textDecoration: "none" }}>
        Search the corpus <Icon name="ph-arrow-right" size={12} />
      </Link>
    </div>
  );
}

// RecentlyFound is the live feed of newly-collected job listings, newest first —
// the corpus filling in real time as a Collection Cycle runs.
function RecentlyFound() {
  const { data } = useRecentListings(8);
  const listings = data ?? [];

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-3)", padding: "var(--space-4) var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <h4 style={{ margin: 0, fontSize: 16 }}>Recently found</h4>
        <span style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>live</span>
      </div>
      {listings.length === 0 ? (
        <div style={{ padding: "var(--space-6) 0", textAlign: "center", fontSize: 13, color: "var(--color-neutral-500)" }}>
          No listings collected yet.
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column" }}>
          {listings.map((l) => (
            <RecentListingRow key={l.id} listing={l} />
          ))}
        </div>
      )}
    </div>
  );
}

function RecentListingRow({ listing }: { listing: Listing }) {
  const companyLine = [listing.company, listing.department].filter(Boolean).join(" · ");
  return (
    <a
      href={listing.url}
      target="_blank"
      rel="noreferrer"
      style={{
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "9px 0",
        boxShadow: "0 1px 0 var(--color-divider)",
        textDecoration: "none",
        color: "inherit",
      }}
    >
      <span
        style={{
          display: "grid",
          placeItems: "center",
          width: 30,
          height: 30,
          borderRadius: 8,
          flex: "none",
          background: listing.source === "ats" ? "var(--color-accent-900)" : "var(--color-neutral-800)",
          color: listing.source === "ats" ? "var(--color-accent-300)" : "var(--color-neutral-300)",
        }}
      >
        <Icon name={listing.source === "ats" ? "ph-stack" : "ph-globe-hemisphere-west"} size={15} />
      </span>
      <div style={{ minWidth: 0, flex: 1 }}>
        <div style={{ fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {listing.title || "Untitled role"}
        </div>
        <div style={{ fontSize: 11, color: "var(--color-neutral-500)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {companyLine || "—"}
        </div>
      </div>
      {listing.country && <span className="tag tag-accent-2" style={{ flex: "none" }}>{listing.country}</span>}
      <span style={{ fontSize: 11, color: "var(--color-neutral-500)", flex: "none" }}>{relativeTime(listing.firstSeen)}</span>
    </a>
  );
}

function DiscoveryPanel({
  discovery,
  careerPages,
  companies,
  split,
}: {
  discovery: Discovery | null;
  careerPages: number;
  companies: number;
  split: ReturnType<typeof atsSplit>;
}) {
  // Cumulative catalog-growth curve reconstructed server-side from each page's
  // first_seen (ADR-0012), so it survives reloads and restarts. Its endpoint
  // equals the headline careerPages count. Hooks run unconditionally, so fetch
  // even when there is no discovery run yet.
  const { data } = useCatalogHistory();
  const series = data?.careerPages ?? [];
  const { openStartDiscovery } = useLayout();

  if (!discovery) {
    return (
      <EmptyState
        icon="ph-broadcast"
        title="Discovery hasn't started"
        hint="The perpetual discovery run walks the seed domains to build the catalog. Start one to populate companies and career pages."
        action={
          <button className="btn btn-primary" onClick={openStartDiscovery}>
            <Icon name="ph-play" size={14} /> Start discovery
          </button>
        }
      />
    );
  }

  const counters = [
    { label: "pages crawled", value: fmt(discovery.pagesCrawled) },
    { label: "frontier size", value: fmt(discovery.frontierSize) },
    { label: "companies", value: fmt(companies) },
  ];

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-4)", padding: "var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "var(--space-4)", flexWrap: "wrap" }}>
        <div style={{ display: "flex", gap: 12, minWidth: 0 }}>
          <span
            style={{
              display: "grid",
              placeItems: "center",
              width: 38,
              height: 38,
              borderRadius: 10,
              background: "var(--color-accent-800)",
              color: "var(--color-accent-200)",
              flex: "none",
            }}
          >
            <Icon name="ph-broadcast" size={21} />
          </span>
          <div>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <h4 style={{ margin: 0, fontSize: 18 }}>Discovery run</h4>
              <StatusTag status={discovery.status} />
            </div>
            <div style={{ fontSize: 12, color: "var(--color-neutral-400)", marginTop: 2 }}>
              Perpetual · seeds {fmt(discovery.definition.seedUrls.length)} domains · started {relativeTime(discovery.startedAt)}
            </div>
          </div>
        </div>
        <div style={{ display: "flex", gap: "var(--space-2)", flex: "none" }}>
          <RunControls status={discovery.status} runId={discovery.runId} definitionId={discovery.definition.id} />
        </div>
      </div>

      <div style={{ display: "flex", alignItems: "flex-end", gap: "var(--space-8)" }}>
        <div>
          <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 44, lineHeight: 1, letterSpacing: "-0.02em" }}>
            {fmt(careerPages)}
          </div>
          <div style={{ fontSize: 12, color: "var(--color-neutral-400)", marginTop: 4 }}>career pages catalogued</div>
        </div>
        <Sparkline series={series} />
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "var(--space-4)", paddingTop: "var(--space-2)" }}>
        {counters.map((c) => (
          <div key={c.label}>
            <div style={{ fontFamily: "var(--font-heading)", fontWeight: 600, fontSize: 19 }}>{c.value}</div>
            <div style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>{c.label}</div>
          </div>
        ))}
      </div>

      <div style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: "var(--space-2)" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", fontSize: 12 }}>
          <span style={{ display: "flex", alignItems: "center", gap: 7, color: "var(--color-neutral-300)" }}>
            <Icon name="ph-globe-hemisphere-west" size={13} color="var(--color-neutral-400)" /> Self-hosted{" "}
            <b style={{ color: "var(--color-text)" }}>{split.selfPct}%</b>
          </span>
          <span style={{ display: "flex", alignItems: "center", gap: 7, color: "var(--color-neutral-300)" }}>
            <b style={{ color: "var(--color-text)" }}>{split.atsPct}%</b> ATS{" "}
            <Icon name="ph-stack" size={13} color="var(--color-accent-300)" />
          </span>
        </div>
        <div style={{ display: "flex", height: 10, borderRadius: 6, overflow: "hidden", background: "var(--color-neutral-800)" }}>
          <span style={{ width: `${split.selfPct}%`, background: "var(--color-neutral-500)" }} />
          <span style={{ width: `${split.atsPct}%`, background: "var(--color-accent-500)" }} />
        </div>
        <Link to="/catalog" style={{ alignSelf: "flex-start", fontSize: 13, marginTop: 6, textDecoration: "none" }}>
          Browse the catalog <Icon name="ph-arrow-right" size={12} />
        </Link>
      </div>
    </div>
  );
}

function RecentlyCatalogued({ pages, companies }: { pages: CareerPage[]; companies: Company[] }) {
  const companiesById = new Map(companies.map((c) => [c.id, c]));
  const recent = recentlyCatalogued(pages, companiesById, 6);

  return (
    <div className="card elev-sm" style={{ gap: "var(--space-3)", padding: "var(--space-4) var(--space-6)" }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <h4 style={{ margin: 0, fontSize: 16 }}>Recently catalogued</h4>
        <span style={{ fontSize: 11, color: "var(--color-neutral-500)" }}>live</span>
      </div>
      {recent.length === 0 ? (
        <div style={{ padding: "var(--space-6) 0", textAlign: "center", fontSize: 13, color: "var(--color-neutral-500)" }}>
          Nothing catalogued yet.
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column" }}>
          {recent.map((p) => (
            <div
              key={p.id}
              style={{ display: "flex", alignItems: "center", gap: 11, padding: "9px 0", boxShadow: "0 1px 0 var(--color-divider)" }}
            >
              <span
                style={{
                  display: "grid",
                  placeItems: "center",
                  width: 30,
                  height: 30,
                  borderRadius: 8,
                  flex: "none",
                  background: p.isAts ? "var(--color-accent-900)" : "var(--color-neutral-800)",
                  color: p.isAts ? "var(--color-accent-300)" : "var(--color-neutral-300)",
                }}
              >
                <Icon name={p.isAts ? "ph-stack" : "ph-globe-hemisphere-west"} size={15} />
              </span>
              <div style={{ minWidth: 0, flex: 1 }}>
                <div style={{ fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{p.company}</div>
                <div
                  style={{
                    fontSize: 11,
                    color: "var(--color-neutral-500)",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  {prettyUrl(p.url)}
                </div>
              </div>
              <span style={{ fontSize: 11, color: "var(--color-neutral-500)", flex: "none" }}>{relativeTime(p.firstSeen)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
