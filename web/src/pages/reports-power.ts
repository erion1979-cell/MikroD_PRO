// The Power/UPS tab: one unit over the page's date range - its outages, the
// share of polls it answered, the extremes of the four charted measures, and
// every event in the window. `GET /api/power/units/{id}/report`
// (internal/server/power_report.go).
//
// ── NOT TIED TO THE ROUTER PICKER ───────────────────────────────────────────
//
// A unit belongs to a site, not a router, so this tab has its own unit picker
// and loads whether or not a router is chosen. The tab is shown only when the
// viewer can read at least one unit: an install without inverters, or a user
// without the Power/UPS page, never sees it.

import { esc, el, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { fmtTs, fmtDuration, statCard } from './reports';
import { eventLine } from './power-ups';
import type { PowerCond } from '../gen/payloads';

interface Point { avg: number; min: number; max: number }

/** What the server sends: PowerReport in internal/server/power_report.go. */
export interface PowerReport {
  from: number;
  to: number;
  outages: number;
  outageMs: number;
  longestMs: number;
  faults: number;
  notResponding: number;
  availability: number;
  stats: Record<string, Point>;
  events: PowerCond[];
}

/** The stat cards, in order. Pure, so the wording is tested. */
export function powerStats(r: PowerReport): [string, string][] {
  const range = (key: string, unit: string): string => {
    const p = r.stats[key];
    return p ? Math.round(p.min) + '–' + Math.round(p.max) + ' ' + unit : '-';
  };
  return [
    [String(r.outages), 'Mains Outages'],
    [fmtDuration(r.outageMs), 'Time on Battery'],
    [fmtDuration(r.longestMs), 'Longest Outage'],
    [String(r.faults), 'Faults'],
    [r.availability < 0 ? '-' : r.availability + '%', 'Answered Polls'],
    [range('input_v', 'V'), 'Input Voltage'],
    [r.stats.battery_pct ? Math.round(r.stats.battery_pct.min) + '%' : '-', 'Lowest Battery'],
    [r.stats.load_pct ? Math.round(r.stats.load_pct.max) + '%' : '-', 'Peak Load'],
  ];
}

interface Row {
  began: number;
  ended: number | null;
  what: string;
  initial: boolean;
  tone: string;
  lasted: number | null;
}

const COLS: SortCol[] = [
  { key: 'began', label: 'Began', style: '' },
  { key: 'what', label: 'Event', style: '' },
  { key: 'ended', label: 'Ended', style: '' },
  { key: 'lasted', label: 'Lasted', style: 'text-align:right' },
];
const TONE: Record<string, string> = { ok: 'hs-ok', warn: 'hs-warn', bad: 'hs-stale', idle: 'hs-never', info: 'hs-info' };

let rows: Row[] = [];
const sort: SortState = { col: 'began', dir: 'desc' };

function applySort(): void {
  const tbody = el('rptPowerTbody');
  if (tbody) {
    const sorted = sortRows(rows, sort.col, sort.dir);
    tbody.innerHTML = sorted.length
      ? sorted.map((r) =>
        '<tr><td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' +
        esc(fmtTs(r.began)) + (r.initial ? ' · already so when monitoring began' : '') + '</td>' +
        '<td><span class="vpn-hs-badge ' + (TONE[r.tone] || 'hs-info') + '">' + esc(r.what) + '</span></td>' +
        '<td style="font-family:var(--font-mono);font-size:.71rem">' +
        (r.ended ? esc(fmtTs(r.ended)) : '<span style="color:var(--accent-warn)">Open</span>') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.71rem;text-align:right">' +
        esc(fmtDuration(r.lasted)) + '</td></tr>').join('')
      : '<tr><td colspan="4" class="rpt-empty">No events for this range.</td></tr>';
  }
  renderSortHeader('rptPowerThead', COLS, sort, applySort);
}

export function renderPowerReport(r: PowerReport): void {
  const stats = el('rptPowerStats');
  if (stats) stats.innerHTML = powerStats(r).map(([v, l]) => statCard(v, l)).join('');
  rows = r.events.map((c) => {
    const line = eventLine(c, r.to);
    return {
      began: c.beganAt,
      ended: c.endedAt ?? null,
      what: line.title,
      initial: c.initial,
      tone: line.tone,
      lasted: c.endedAt != null ? c.endedAt - c.beganAt : null,
    };
  });
  applySort();
}

/** Fill the unit picker and show the tab when there is a unit to show. */
export function loadPowerUnits(): Promise<void> {
  return fetch('/api/power', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : { units: [] }))
    .then((j: { units?: { id: string; name: string }[] }) => {
      const units = j.units || [];
      const tab = document.querySelector<HTMLElement>('#rptTabBar [data-rtab="power"]');
      if (tab) tab.hidden = units.length === 0;
      const sel = el<HTMLSelectElement>('rptPowerUnit');
      if (!sel) return;
      const current = sel.value;
      sel.innerHTML = units.map((u) => '<option value="' + esc(u.id) + '">' + esc(u.name) + '</option>').join('');
      if (current && units.some((u) => u.id === current)) sel.value = current;
    })
    .catch(() => { /* no tab is the right answer to a failed list */ });
}

/** Load the chosen unit's report for the window, and point the exports at it. */
export function loadPowerReport(from: number, to: number): void {
  const id = el<HTMLSelectElement>('rptPowerUnit')?.value;
  if (!id) return;
  const base = '/api/power/units/' + encodeURIComponent(id);
  const q = 'from=' + from + '&to=' + to;
  for (const [link, what] of [['rptPowerCsvLink', 'history'], ['rptPowerEventsCsvLink', 'events']]) {
    const a = el<HTMLAnchorElement>(link as string);
    if (a) {
      a.href = base + '/export.csv?' + q + '&what=' + what;
      a.style.display = '';
    }
  }
  fetch(base + '/report?' + q, { credentials: 'same-origin' })
    .then((r) => r.json())
    .then((j: { ok?: boolean; report?: PowerReport }) => {
      if (j.ok && j.report && el<HTMLSelectElement>('rptPowerUnit')?.value === id) renderPowerReport(j.report);
    })
    .catch(() => { /* the previous view stays */ });
}
