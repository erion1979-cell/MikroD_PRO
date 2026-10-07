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

/** The export intervals: `step` in internal/server/power_export.go. */
export const EXPORT_STEPS: [string, string][] = [
  ['1m', '1 minute'], ['5m', '5 minutes'], ['15m', '15 minutes'], ['1h', '1 hour'], ['1d', '1 day'],
];

/**
 * The CSV form Excel expects here: `semicolon` (decimal comma) where numbers
 * are written 1,5, `comma` where they are written 1.5. From the browser's
 * language, the same thing Excel follows.
 */
export function defaultSep(locale?: string): 'comma' | 'semicolon' {
  return (1.5).toLocaleString(locale).includes(',') ? 'semicolon' : 'comma';
}

const STEP_KEY = 'mkd_pw_export_step';
const SEP_KEY = 'mkd_pw_export_sep';
const remembered = (key: string): string => { try { return localStorage.getItem(key) || ''; } catch { return ''; } };
const remember = (key: string, v: string): void => { try { localStorage.setItem(key, v); } catch { /* a convenience */ } };

/** One report's elements, by id: the Reports page's and the Power/UPS page's. */
export interface PowerReportIds {
  unit: string; stats: string; thead: string; tbody: string;
  csv: string; eventsCsv: string; step: string; sep: string;
}

export interface PowerReportView {
  /** Fill the unit picker; resolves with the units the viewer may read. */
  loadUnits(): Promise<{ id: string; name: string }[]>;
  /** Load the chosen unit's report for the window, and point the exports at it. */
  load(from: number, to: number): void;
}

/**
 * One Power/UPS report, drawn into the elements `ids` names: the stat cards, the
 * events table and the two exports, with the interval and CSV-form pickers that
 * shape the history export. The Reports page's Power/UPS tab and the Power/UPS
 * page's Reports tab are both one of these, so they can never disagree.
 */
export function createPowerReport(ids: PowerReportIds): PowerReportView {
  let rows: Row[] = [];
  const sort: SortState = { col: 'began', dir: 'desc' };
  let span: { from: number; to: number } | null = null;

  const stepSel = el<HTMLSelectElement>(ids.step);
  if (stepSel) {
    stepSel.innerHTML = EXPORT_STEPS.map(([v, l]) => '<option value="' + v + '">' + esc(l) + '</option>').join('');
    stepSel.value = EXPORT_STEPS.some(([v]) => v === remembered(STEP_KEY)) ? remembered(STEP_KEY) : '1m';
  }
  const sepSel = el<HTMLSelectElement>(ids.sep);
  if (sepSel) {
    sepSel.innerHTML = '<option value="semicolon">Excel, decimal comma (1,5 ; )</option>' +
      '<option value="comma">Excel, decimal point (1.5 , )</option>';
    const r = remembered(SEP_KEY);
    sepSel.value = r === 'comma' || r === 'semicolon' ? r : defaultSep();
  }

  function applySort(): void {
    const tbody = el(ids.tbody);
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
    renderSortHeader(ids.thead, COLS, sort, applySort);
  }

  function render(r: PowerReport): void {
    const stats = el(ids.stats);
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

  /** Point the two exports at the window, with the interval and form chosen. */
  function links(): void {
    const id = el<HTMLSelectElement>(ids.unit)?.value;
    if (!id || !span) return;
    const base = '/api/power/units/' + encodeURIComponent(id) + '/export.csv?from=' + span.from + '&to=' + span.to +
      '&sep=' + (sepSel?.value || 'comma');
    for (const [link, q] of [[ids.csv, '&what=history&step=' + (stepSel?.value || '1m')], [ids.eventsCsv, '&what=events']]) {
      const a = el<HTMLAnchorElement>(link as string);
      if (a) {
        a.href = base + q;
        a.style.display = '';
      }
    }
  }
  stepSel?.addEventListener('change', () => { remember(STEP_KEY, stepSel.value); links(); });
  sepSel?.addEventListener('change', () => { remember(SEP_KEY, sepSel.value); links(); });

  return {
    loadUnits() {
      return fetch('/api/power', { credentials: 'same-origin' })
        .then((r) => (r.ok ? r.json() : { units: [] }))
        .then((j: { units?: { id: string; name: string }[] }) => {
          const units = j.units || [];
          const sel = el<HTMLSelectElement>(ids.unit);
          if (sel) {
            const current = sel.value;
            sel.innerHTML = units.map((u) => '<option value="' + esc(u.id) + '">' + esc(u.name) + '</option>').join('');
            if (current && units.some((u) => u.id === current)) sel.value = current;
          }
          return units;
        })
        .catch(() => []);
    },
    load(from, to) {
      const id = el<HTMLSelectElement>(ids.unit)?.value;
      if (!id) return;
      span = { from, to };
      links();
      fetch('/api/power/units/' + encodeURIComponent(id) + '/report?from=' + from + '&to=' + to, { credentials: 'same-origin' })
        .then((r) => r.json())
        .then((j: { ok?: boolean; report?: PowerReport }) => {
          if (j.ok && j.report && el<HTMLSelectElement>(ids.unit)?.value === id) render(j.report);
        })
        .catch(() => { /* the previous view stays */ });
    },
  };
}

// ── THE REPORTS PAGE'S POWER/UPS TAB ────────────────────────────────────────

const reportsPage = (): PowerReportView => (pageReport ??= createPowerReport({
  unit: 'rptPowerUnit', stats: 'rptPowerStats', thead: 'rptPowerThead', tbody: 'rptPowerTbody',
  csv: 'rptPowerCsvLink', eventsCsv: 'rptPowerEventsCsvLink', step: 'rptPowerStep', sep: 'rptPowerSep',
}));
let pageReport: PowerReportView | undefined;

/** Fill the Reports page's unit picker, and show its tab only when there is a unit. */
export function loadPowerUnits(): Promise<void> {
  return reportsPage().loadUnits().then((units) => {
    const tab = document.querySelector<HTMLElement>('#rptTabBar [data-rtab="power"]');
    if (tab) tab.hidden = units.length === 0;
  });
}

/** Load the Reports page's Power/UPS tab for the window. */
export function loadPowerReport(from: number, to: number): void {
  reportsPage().load(from, to);
}
