// SVG geometry for the discovery panel's sparkline.
//
// The series itself comes from the /catalog-history endpoint (a cumulative,
// gap-filled daily growth curve reconstructed from first_seen, per ADR-0012).
// This module is a pure renderer: it maps that number[] onto polyline/polygon
// point strings and holds no state of its own.

// sparklinePoints maps a series onto SVG polyline/polygon point strings for a
// w×h viewBox. The line hugs a small top/bottom padding; the area closes the
// line down to the baseline for the gradient fill. A single sample renders as a
// flat mid-line.
export function sparklinePoints(
  series: number[],
  w: number,
  h: number,
): { line: string; area: string } {
  if (series.length === 0) {
    const mid = h / 2;
    return { line: `0,${mid} ${w},${mid}`, area: `0,${h} 0,${mid} ${w},${mid} ${w},${h}` };
  }
  const max = Math.max(...series);
  const min = Math.min(...series);
  const span = max - min;
  const pad = 6;
  const usable = h - pad * 2;
  const x = (i: number) => (series.length === 1 ? w : (i / (series.length - 1)) * w);
  const y = (v: number) => (span === 0 ? h / 2 : h - pad - ((v - min) / span) * usable);

  const pts =
    series.length === 1
      ? [`0,${y(series[0]).toFixed(1)}`, `${w},${y(series[0]).toFixed(1)}`]
      : series.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`);

  const line = pts.join(" ");
  const area = `0,${h} ${line} ${w},${h}`;
  return { line, area };
}
