// Presentation helpers shared across the dashboard.

// fmt renders an integer with thousands separators (1247 → "1,247").
export function fmt(n: number): string {
  return n.toLocaleString("en-US");
}

const UNITS: [limit: number, secs: number, label: string][] = [
  [60, 1, "sec"],
  [3600, 60, "min"],
  [86400, 3600, "hr"],
  [2592000, 86400, "day"],
  [31536000, 2592000, "mo"],
  [Infinity, 31536000, "yr"],
];

// relativeTime renders an ISO timestamp as a coarse "N unit ago", or "—" when
// absent. Kept dependency-free (no date library) since the crawler timestamps
// are always recent.
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const secs = Math.max(0, (Date.now() - then) / 1000);
  if (secs < 5) return "just now";
  for (const [limit, div, label] of UNITS) {
    if (secs < limit) {
      const v = Math.floor(secs / div);
      return `${v} ${label}${v === 1 ? "" : "s"} ago`;
    }
  }
  return "—";
}

// prettyUrl strips the scheme and any trailing slash so a career-page URL reads
// as a compact host+path (https://careers.acme.io/ → "careers.acme.io").
export function prettyUrl(raw: string): string {
  return raw.replace(/^https?:\/\//, "").replace(/\/$/, "");
}

// hostOf returns just the hostname of a URL, falling back to prettyUrl when the
// string will not parse as a URL.
export function hostOf(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return prettyUrl(raw).split("/")[0];
  }
}
