// The Power/UPS unit page's two history charts: voltage (input, output) and
// percent (battery, load).
//
// ── TWO CHARTS, NOT TWO AXES ────────────────────────────────────────────────
//
// Volts and percent are different scales; one chart with an axis each would
// make the eye compare two lines that share nothing. The mockup already draws
// them apart.
//
// ── WHAT IS DRAWN ───────────────────────────────────────────────────────────
//
// Each series is its bucket AVERAGE as a line, over a faint band from the
// bucket's lowest to highest reading, so an outage of two minutes still shows in
// a two-hour bucket. The first series of each chart is the theme's blue, the
// second its pink, dashed - colour is never the only thing that tells them
// apart. A bucket with no rows (the unit unreachable, MikroDash stopped, or
// history off) breaks the line rather than bridging it.

import { fmtTime, fmtMonthDay } from '../timefmt';

interface ChartLike { destroy(): void }
// Loaded by the shell from /vendor, as for the Reports charts.
declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

export interface HistPoint { t: number; avg: number; min: number; max: number }
export interface XY { x: number; y: number | null; min: number | null; max: number | null }

/**
 * The points, with a null inserted wherever more than one and a half buckets
 * pass between two of them, so the chart breaks the line there.
 */
export function withGaps(points: readonly HistPoint[], bucketMs: number): XY[] {
  const out: XY[] = [];
  let prev: HistPoint | null = null;
  for (const p of points) {
    if (prev && p.t - prev.t > bucketMs * 1.5) {
      out.push({ x: prev.t + bucketMs, y: null, min: null, max: null });
    }
    out.push({ x: p.t, y: p.avg, min: p.min, max: p.max });
    prev = p;
  }
  return out;
}

const charts: ChartLike[] = [];

function token(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

function fade(colour: string, alpha: number): string {
  return `color-mix(in srgb, ${colour} ${Math.round(alpha * 100)}%, transparent)`;
}

interface Series { label: string; unit: string; points: XY[] }

function lineChart(canvas: HTMLCanvasElement, a: Series, b: Series, spanMs: number, yMax?: number): ChartLike | null {
  if (typeof Chart === 'undefined') return null;
  const blue = token('--accent-rx', '#38bdf8');
  const pink = token('--accent-pink', '#f472b6');
  const muted = token('--text-muted', 'rgba(148,163,190,.55)');
  const grid = token('--border', 'rgba(99,130,190,.13)');
  const ds = (s: Series, colour: string, dashed: boolean) => [
    // The band: max first, then min filled up to it. Hidden from the legend
    // and the tooltip; the average's tooltip reports both.
    { label: s.label + ' max', data: s.points.map((p) => ({ x: p.x, y: p.max })), borderWidth: 0,
      pointRadius: 0, fill: false, spanGaps: false, band: true },
    { label: s.label + ' min', data: s.points.map((p) => ({ x: p.x, y: p.min })), borderWidth: 0,
      pointRadius: 0, fill: '-1', backgroundColor: fade(colour, 0.13), spanGaps: false, band: true },
    { label: s.label, data: s.points.map((p) => ({ x: p.x, y: p.y })), borderColor: colour, borderWidth: 2,
      borderDash: dashed ? [5, 4] : [], pointRadius: 0, pointHoverRadius: 4, fill: false, spanGaps: false,
      tension: 0.2, minMax: s.points },
  ];
  const day = spanMs <= 86_400_000;
  return new Chart(canvas, {
    type: 'line',
    data: { datasets: [...ds(a, blue, false), ...ds(b, pink, true)] },
    options: {
      responsive: true, maintainAspectRatio: false, animation: false,
      interaction: { mode: 'nearest', axis: 'x', intersect: false },
      scales: {
        x: {
          type: 'linear', min: Date.now() - spanMs, max: Date.now(),
          ticks: { color: muted, maxTicksLimit: 7, callback: (v: number) => (day ? fmtTime(v, false) : fmtMonthDay(v)) },
          grid: { color: fade(grid, 0.6) },
        },
        y: { beginAtZero: true, ...(yMax ? { suggestedMax: yMax } : {}), ticks: { color: muted, maxTicksLimit: 5 }, grid: { color: fade(grid, 0.6) } },
      },
      plugins: {
        legend: {
          labels: { color: muted, boxHeight: 2, filter: (item: { text: string }) => !/ (min|max)$/.test(item.text) },
        },
        tooltip: {
          filter: (item: { dataset: { band?: boolean }; raw: { y: number | null } }) => !item.dataset.band && item.raw.y != null,
          callbacks: {
            title: (items: { raw: { x: number } }[]) => (items[0] ? fmtMonthDay(items[0].raw.x) + ' ' + fmtTime(items[0].raw.x, false) : ''),
            label: (item: { dataset: { label: string; minMax?: XY[] }; dataIndex: number; raw: { y: number } }) => {
              const s = item.dataset.label === a.label ? a : b;
              const p = item.dataset.minMax?.[item.dataIndex];
              const range = p && p.min != null && p.max != null ? ` (${p.min.toFixed(1)}–${p.max.toFixed(1)})` : '';
              return `${s.label}: ${item.raw.y.toFixed(1)} ${s.unit}${range}`;
            },
          },
        },
      },
    },
  });
}

/** Draws both charts, replacing any drawn before. */
export function drawPowerCharts(series: Record<string, HistPoint[]>, bucketMs: number, spanMs: number): void {
  stopPowerCharts();
  const pts = (k: string): XY[] => withGaps(series[k] || [], bucketMs);
  const v = document.getElementById('pwChartV') as HTMLCanvasElement | null;
  const p = document.getElementById('pwChartP') as HTMLCanvasElement | null;
  if (v) {
    const c = lineChart(v, { label: 'Input voltage', unit: 'V', points: pts('input_v') },
      { label: 'Output voltage', unit: 'V', points: pts('output_v') }, spanMs);
    if (c) charts.push(c);
  }
  if (p) {
    const c = lineChart(p, { label: 'Battery', unit: '%', points: pts('battery_pct') },
      // 100 suggested, not imposed: an overload reads 112 % and must show.
      { label: 'Load', unit: '%', points: pts('load_pct') }, spanMs, 100);
    if (c) charts.push(c);
  }
}

export function stopPowerCharts(): void {
  while (charts.length) charts.pop()!.destroy();
}
