// A live, session-scoped trend for the discovery panel's sparkline.
//
// The API exposes only point-in-time counts — there is no history endpoint (a
// deliberately deferred backend feature). Rather than fabricate a trend, we
// honestly accumulate the real values observed during this browser session:
// each time a polled metric changes, we append it. The line therefore starts
// flat and fills in as the dashboard runs, reflecting only data actually seen.

const store = new Map<string, number[]>();
const CAP = 48;

// record appends `value` to the series for `key` when it differs from the last
// sample (steady state stays flat rather than piling up duplicates), capping the
// retained history. Returns the current series.
export function record(key: string, value: number): number[] {
  const series = store.get(key) ?? [];
  if (series.length === 0 || series[series.length - 1] !== value) {
    series.push(value);
    if (series.length > CAP) series.shift();
    store.set(key, series);
  }
  return series;
}

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
