// The Power/UPS page (docs/inverter/PLAN.md): inverters and UPSs read over
// Modbus TCP by internal/power.
//
// ── TWO VIEWS ON ONE CARD ───────────────────────────────────────────────────
//
// The fleet list, filtered by the status tabs and sorted by any column, and one
// unit's page when a row is opened (the layout agreed in docs/inverter/mockups).
// Everything is drawn from `GET /api/power`, which returns only the units this
// viewer may read; the server decides, per site.
//
// ── WHAT IS DRAWN FROM WHAT ─────────────────────────────────────────────────
//
// A unit's `state` is what the poller last saw, and is null when nothing polls
// it (disabled, a server started with -no-pool, or a model this build lacks).
// Values are shown only when the unit has answered at least once; a unit that
// stops answering keeps its last reading on screen, marked as such, because the
// last known numbers are exactly what somebody wants during an outage.

import { el, esc, renderSortHeader, sortRows, type SortState } from '../dom';
import { fmtTs } from '../timefmt';
import type { Socket } from '../socket';
import type { PowerCond, PowerState } from '../gen/payloads';
import { drawPowerCharts, stopPowerCharts, type HistPoint } from './power-ups-chart';
import { createPowerFlowAnim, flowLanes } from './power-flow-anim';

type Cond = PowerCond;
type UnitState = PowerState;

export interface Unit {
  id: string;
  name: string;
  siteId: string | null;
  model: string;
  host: string;
  port: number;
  slaveId: number;
  routerId: string | null;
  batteryAh: number | null;
  enabled: boolean;
  producerName: string;
  modelName: string;
  serial: string;
  canWrite: boolean;
  state: UnitState | null;
}

export interface ModelInfo {
  id: string; producer: string; producerName: string; modelName: string; kind: string; details: string[];
}

/** The brands the models come in, each once, by name. */
export function brandsOf(models: readonly ModelInfo[]): { id: string; name: string }[] {
  const out: { id: string; name: string }[] = [];
  for (const m of models) if (!out.some((b) => b.id === m.producer)) out.push({ id: m.producer, name: m.producerName });
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** One brand's models, by name. */
export function modelsOf(models: readonly ModelInfo[], brand: string): ModelInfo[] {
  return models.filter((m) => m.producer === brand).sort((a, b) => a.modelName.localeCompare(b.modelName));
}
interface PowerList {
  units: Unit[];
  models: ModelInfo[];
  writableSites: string[];
  polling: boolean;
}

/** The status every view agrees on: one key, one label, one pill colour. */
export type Status = 'mains' | 'battery' | 'fault' | 'off' | 'down' | 'waiting' | 'idle';
export const STATUS: Record<Status, { label: string; pill: string; rank: number }> = {
  fault: { label: 'Fault', pill: 'hs-stale', rank: 0 },
  off: { label: 'Output off', pill: 'hs-stale', rank: 1 },
  // Red and pulsing wherever it shows: a unit that has stopped answering is
  // the one state nobody may read past.
  down: { label: 'Not responding', pill: 'hs-stale pw-pulse', rank: 2 },
  battery: { label: 'On battery', pill: 'hs-warn', rank: 3 },
  waiting: { label: 'Connecting…', pill: 'hs-info', rank: 4 },
  mains: { label: 'On mains', pill: 'hs-ok', rank: 5 },
  idle: { label: 'Not polled', pill: 'hs-never', rank: 6 },
};

export function statusOf(u: Unit): Status {
  const st = u.state;
  if (!st) return 'idle';
  if (!st.online) return 'down';
  if (!st.hasReading) return 'waiting';
  if (st.mode === 'fault' || st.mode === 'battery' || st.mode === 'mains' || st.mode === 'off') return st.mode;
  return 'waiting';
}

export function pill(s: Status, label: string = STATUS[s].label): string {
  return `<span class="vpn-hs-badge ${STATUS[s].pill}">${esc(label)}</span>`;
}

/**
 * A unit's status in words. "Not responding" says which device is silent when
 * the failure tells (internal/power/cause.go): no connection to the converter,
 * or the converter answering for an inverter that does not.
 */
export function statusLabel(u: Unit): string {
  const s = statusOf(u);
  if (s === 'down' && u.state?.cause === 'converter') return 'Converter unreachable';
  if (s === 'down' && u.state?.cause === 'unit') return 'Inverter not responding';
  return STATUS[s].label;
}

export function unitPill(u: Unit): string {
  return pill(statusOf(u), statusLabel(u));
}

const fmt = (v: number | undefined, digits = 1): string =>
  v === undefined || v === null || Number.isNaN(v) ? '—' : v.toFixed(digits);

/** "4 s ago", "12 min ago", "3 h ago". */
export function ago(ms: number, now = Date.now()): string {
  if (!ms) return 'never';
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 60) return s + ' s ago';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  if (s < 86400) return Math.round(s / 3600) + ' h ago';
  return Math.round(s / 86400) + ' d ago';
}

/** "1 h 29 min", "48 s". */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + ' s';
  const m = Math.floor(s / 60);
  if (m < 60) return m + ' min';
  const h = Math.floor(m / 60);
  if (h < 48) return h + ' h ' + (m % 60) + ' min';
  return Math.floor(h / 24) + ' d ' + (h % 24) + ' h';
}

/** How an event reads in the list, with its colour. */
export function eventLine(c: Cond, now = Date.now()): { title: string; sub: string; tone: string } {
  const at = fmtTs(c.beganAt, false);
  const ended = c.endedAt != null;
  const lasted = ended ? duration((c.endedAt as number) - c.beganAt) : duration(now - c.beganAt);
  const since = c.initial ? 'already so when monitoring began' : at;
  switch (c.kind) {
    case 'mains_lost':
      return ended
        ? { title: 'Mains lost, then restored', sub: since + ' · outage lasted ' + lasted, tone: 'ok' }
        : { title: 'Mains lost - running on battery', sub: since + ' · ' + lasted + ' so far', tone: 'warn' };
    case 'not_responding':
      // The text says which device was silent (internal/power/cause.go);
      // events from before it was recorded read plain "Not responding".
      return { title: c.text || 'Not responding', sub: since + (ended ? ' · back after ' + lasted : ' · ' + lasted + ' so far'), tone: 'idle' };
    case 'event':
      return {
        title: c.text + ' (' + String(c.code).padStart(2, '0') + ')',
        sub: since + (ended ? ' · cleared after ' + lasted : ' · active'),
        tone: c.fault ? 'bad' : 'info',
      };
    case 'battery_low':
      return { title: 'Battery low', sub: since + (ended ? ' · lasted ' + lasted : ' · ' + lasted + ' so far'), tone: 'warn' };
    case 'output_off':
      return { title: 'Output switched off', sub: since + (ended ? ' · back on after ' + lasted : ' · still off'), tone: 'bad' };
    default:
      return { title: c.text, sub: since + (ended ? ' · lasted ' + lasted : ''), tone: 'warn' };
  }
}

/** A list row, with the sort keys the table sorts by. */
interface Row {
  unit: Unit;
  name: string;
  site: string;
  status: number;
  input: number | null;
  output: number | null;
  load: number | null;
  battery: number | null;
  converter: string;
  last: number;
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, { credentials: 'same-origin', ...init });
  const body = (await r.json().catch(() => ({}))) as T & { ok?: boolean; error?: string };
  if (!r.ok || body.ok === false) throw new Error(body.error || 'The request failed (' + r.status + ')');
  return body;
}

export function initPowerUpsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let data: PowerList = { units: [], models: [], writableSites: [], polling: true };
  let sites: Record<string, string> = {};
  let routers: { id: string; label: string }[] = [];
  let filter: Status | 'all' | 'down' = 'all';
  let openId = '';
  const flowAnim = createPowerFlowAnim('pwFlow', 'pwFlowAnim');
  let range = '24h';
  let historyAt = 0;
  const sort: SortState = { col: 'site', dir: 'asc' };

  const siteName = (id: string | null): string => (id && sites[id]) || (id ? 'Unknown site' : 'No site');

  async function load(): Promise<void> {
    try {
      const [list, siteBody] = await Promise.all([
        api<PowerList & { ok: boolean }>('/api/power'),
        api<{ sites: { id: string; name: string }[] }>('/api/sites').catch(() => ({ sites: [] })),
      ]);
      data = list;
      sites = {};
      for (const s of siteBody.sites) sites[s.id] = s.name;
      draw();
    } catch (e) {
      notice((e as Error).message);
    }
  }

  function notice(msg: string): void {
    const n = el('pwNotice');
    if (!n) return;
    n.textContent = msg;
    n.hidden = !msg;
  }

  function draw(): void {
    notice(data.polling ? '' : 'Polling is off on this server (started with -no-pool): units are listed but not read.');
    const add = el('pwAdd');
    if (add) add.hidden = data.writableSites.length === 0;
    if (openId && !data.units.some((u) => u.id === openId)) openId = '';
    el('pwListView')!.hidden = !!openId;
    el('pwUnitView')!.hidden = !openId;
    if (openId) drawUnit();
    else drawList();
  }

  // ── THE LIST ──────────────────────────────────────────────────────────────

  function drawList(): void {
    const counts: Record<string, number> = { all: data.units.length, mains: 0, battery: 0, fault: 0, down: 0 };
    for (const u of data.units) {
      const s = statusOf(u);
      if (s === 'mains' || s === 'battery' || s === 'down') counts[s]!++;
      if (s === 'fault' || s === 'off') counts.fault!++;
    }
    const badge = el('pwBadge');
    if (badge) badge.textContent = String(data.units.length);
    el('pwSummary')!.innerHTML = ([
      ['Units', counts.all, ''], ['On mains', counts.mains, 'ok'], ['On battery', counts.battery, 'warn'],
      ['Fault', counts.fault, 'bad'], ['Not responding', counts.down, 'idle'],
    ] as [string, number, string][]).map(([label, n, tone]) =>
      `<div class="pw-sum"><div class="pw-sum-label">${label}</div><div class="pw-sum-n ${n ? 'pw-tone-' + tone : ''}">${n}</div></div>`,
    ).join('');

    const rows: Row[] = data.units
      .filter((u) => {
        const s = statusOf(u);
        if (filter === 'all') return true;
        if (filter === 'fault') return s === 'fault' || s === 'off';
        return s === filter;
      })
      .map((u) => {
        const v = (u.state?.hasReading && u.state.values) || {};
        return {
          unit: u, name: u.name, site: siteName(u.siteId), status: STATUS[statusOf(u)].rank,
          input: v.input_v ?? null, output: v.output_v ?? null, load: v.load_pct ?? null,
          battery: v.battery_pct ?? null, converter: u.host + ':' + u.port + ' #' + u.slaveId,
          last: u.state?.lastOk ?? 0,
        };
      });

    renderSortHeader('pwHead', [
      { key: 'name', label: 'Name' }, { key: 'site', label: 'Site' }, { key: 'status', label: 'Status' },
      { key: 'input', label: 'Input' }, { key: 'output', label: 'Output' }, { key: 'load', label: 'Load' },
      { key: 'battery', label: 'Battery' }, { key: 'converter', label: 'Converter' }, { key: 'last', label: 'Last reading' },
    ], sort, drawList);
    // GROUPED BY SITE, as the brief asks: sites in name order (reversed when
    // the Site column is sorted descending), and within a site the chosen
    // column decides.
    const bySite = new Map<string, Row[]>();
    for (const r of sortRows(rows, sort.col === 'site' ? 'name' : sort.col, sort.col === 'site' ? 'asc' : sort.dir)) {
      if (!bySite.has(r.site)) bySite.set(r.site, []);
      bySite.get(r.site)!.push(r);
    }
    const siteOrder = [...bySite.keys()].sort((a, b) => a.localeCompare(b));
    if (sort.col === 'site' && sort.dir === 'desc') siteOrder.reverse();
    const sorted = siteOrder.flatMap((k) => bySite.get(k)!);
    el('pwBody')!.innerHTML = sorted.map((r) => `
      <tr class="pw-row${statusOf(r.unit) === 'down' ? ' pw-row-stale' : ''}" data-pwunit="${esc(r.unit.id)}" tabindex="0">
        <td><b>${esc(r.name)}</b>${r.unit.enabled ? '' : ' <span class="muted-note">(disabled)</span>'}</td>
        <td>${esc(r.site)}</td>
        <td>${unitPill(r.unit)}</td>
        <td class="pw-num">${r.input == null ? '—' : fmt(r.input) + ' V'}</td>
        <td class="pw-num">${r.output == null ? '—' : fmt(r.output) + ' V'}</td>
        <td class="pw-num">${r.load == null ? '—' : fmt(r.load, 0) + ' %'}</td>
        <td class="pw-num">${r.battery == null ? '—' : fmt(r.battery, 0) + ' %'}</td>
        <td class="pw-mono">${esc(r.converter)}</td>
        <td class="${statusOf(r.unit) === 'down' ? 'pw-age-alert' : 'muted-note'}">${r.last ? ago(r.last) : '—'}</td>
      </tr>`).join('');
    el('pwEmpty')!.hidden = data.units.length > 0;
  }

  el('pwBody')?.addEventListener('click', (e) => {
    const tr = (e.target as HTMLElement).closest('[data-pwunit]');
    if (tr) openUnit(tr.getAttribute('data-pwunit') || '');
  });
  el('pwBody')?.addEventListener('keydown', (e) => {
    const tr = (e.target as HTMLElement).closest('[data-pwunit]');
    if (tr && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault();
      openUnit(tr.getAttribute('data-pwunit') || '');
    }
  });
  document.querySelectorAll('#pwTabs [data-pwtab]').forEach((b) => b.addEventListener('click', () => {
    filter = (b.getAttribute('data-pwtab') || 'all') as typeof filter;
    document.querySelectorAll('#pwTabs [data-pwtab]').forEach((x) => {
      const on = x === b;
      x.classList.toggle('active', on);
      x.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    openId = '';
    draw();
  }));

  // ── ONE UNIT ──────────────────────────────────────────────────────────────

  function openUnit(id: string): void {
    openId = id;
    draw();
    void loadEvents();
    void loadHistory();
  }

  el('pwBack')?.addEventListener('click', () => {
    openId = '';
    stopPowerCharts();
    draw();
  });

  /** Points the two export links at the open unit and the chosen window. */
  function setExportLinks(): void {
    const base = '/api/power/units/' + encodeURIComponent(openId) + '/export.csv?range=' + range;
    el<HTMLAnchorElement>('pwExport')?.setAttribute('href', base);
    el<HTMLAnchorElement>('pwExportEvents')?.setAttribute('href', base + '&what=events');
  }

  async function loadHistory(): Promise<void> {
    const id = openId;
    if (!id) return;
    setExportLinks();
    historyAt = Date.now();
    const note = el('pwHistoryNote');
    try {
      const body = await api<{ series: Record<string, HistPoint[]>; bucketMs: number; from: number; to: number }>(
        '/api/power/units/' + encodeURIComponent(id) + '/history?range=' + range);
      if (id !== openId) return;
      const empty = Object.values(body.series).every((pts) => !pts.length);
      if (note) {
        note.hidden = !empty;
        note.textContent = 'No history for this window yet. History is recorded once a minute while ' +
          'MikroDash runs with -history (the Docker image does).';
      }
      drawPowerCharts(body.series, body.bucketMs, body.to - body.from);
    } catch (e) {
      if (note) {
        note.hidden = false;
        note.textContent = (e as Error).message;
      }
    }
  }

  document.querySelectorAll('#pwRange [data-pwrange]').forEach((b) => b.addEventListener('click', () => {
    range = b.getAttribute('data-pwrange') || '24h';
    document.querySelectorAll('#pwRange [data-pwrange]').forEach((x) => {
      const on = x === b;
      x.classList.toggle('active', on);
      x.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    void loadHistory();
  }));

  async function loadEvents(): Promise<void> {
    const id = openId;
    const box = el('pwEvents');
    if (!id || !box) return;
    try {
      const body = await api<{ events: Cond[] }>('/api/power/units/' + encodeURIComponent(id) + '/events?limit=20');
      if (id !== openId) return;
      box.innerHTML = body.events.length ? body.events.map((c) => {
        const l = eventLine(c);
        return `<div class="pw-ev"><span class="pw-dot pw-tone-${l.tone}"></span><div><div>${esc(l.title)}</div><div class="pw-ev-sub">${esc(l.sub)}</div></div></div>`;
      }).join('') : '<div class="muted-note">No events recorded yet.</div>';
    } catch (e) {
      box.innerHTML = '<div class="muted-note">' + esc((e as Error).message) + '</div>';
    }
  }

  function drawUnit(): void {
    const u = data.units.find((x) => x.id === openId);
    if (!u) return;
    const st = u.state;
    const s = statusOf(u);
    const v = (st?.hasReading && st.values) || {};
    const f = (st?.hasReading && st.flags) || {};
    // ── A UNIT NOT ANSWERING SHOWS ITS LAST READING, AND SAYS SO ────────────
    //
    // The numbers are kept on screen (they are what somebody wants during an
    // outage), but greyed and labelled "last known"; the status bits become
    // unknown, the flow arrows stop, and everything saying "Not responding"
    // is red and pulses. Nothing may look live while the unit is silent.
    const down = s === 'down';
    el('pwUnitView')?.classList.toggle('pw-is-stale', down);

    el('pwUnitSite')!.textContent = siteName(u.siteId);
    el('pwUnitName')!.textContent = u.name;
    el('pwUnitOnline')!.innerHTML = st ? (st.online ? '<span class="vpn-hs-badge hs-ok">Online</span>' : unitPill(u)) : pill('idle');
    const age = el('pwUnitAge')!;
    age.textContent = st?.lastOk ? 'Last reading ' + ago(st.lastOk) : (down ? 'No reading yet' : '');
    age.classList.toggle('pw-age-alert', down);
    el('pwEdit')!.hidden = !u.canWrite;

    // The banner: what the unit is doing, in words, and the status bits.
    const banner = el('pwBanner')!;
    banner.className = 'pw-banner pw-banner-' + s;
    const openEvent = st?.open.find((c) => c.kind === 'event');
    const openMains = st?.open.find((c) => c.kind === 'mains_lost');
    let title = statusLabel(u);
    let sub = '';
    if (s === 'fault' && openEvent) {
      title = 'Event ' + String(openEvent.code).padStart(2, '0') + ' - ' + openEvent.text;
      sub = 'Active for ' + duration(Date.now() - openEvent.beganAt);
    } else if (s === 'battery') {
      title = 'On battery - mains lost';
      sub = (openMains ? 'Mains failed ' + ago(openMains.beganAt) + ' · ' : '') + 'output is supplied from the battery';
    } else if (s === 'mains') {
      title = 'Normal - running on mains';
      sub = st?.eventCode ? st.eventText : 'No active events';
    } else if (s === 'down' && st?.cause === 'converter') {
      sub = 'No connection to the converter at ' + u.host + ':' + u.port +
        ' - it is off, unplugged or off the network. The inverter behind it cannot be seen.';
    } else if (s === 'down' && st?.cause === 'unit') {
      sub = 'The converter at ' + u.host + ':' + u.port + ' answers, but the inverter (slave ' + u.slaveId +
        ') does not: it is switched off, or its RS485 wiring is broken.';
    } else if (s === 'down') {
      sub = st?.lastError ? st.lastError : 'No Modbus reply';
    } else if (s === 'idle') {
      sub = u.enabled ? 'Not read by this server' : 'Polling is switched off for this unit';
    }
    el('pwBannerTitle')!.textContent = title;
    el('pwBannerSub')!.textContent = sub;
    el('pwChips')!.innerHTML = ([
      ['mains_ok', 'Mains normal'], ['charger_on', 'Charger running'], ['inverter_on', 'Inverter running'], ['output_on', 'Output on'],
    ] as [string, string][]).filter(([k]) => k in f).map(([k, label]) => down
      // Unknown, not off: the last reading said one thing, and nobody knows now.
      ? `<span class="pw-chip pw-chip-unknown" title="Unknown: the unit is not answering"><span class="pw-dot pw-tone-idle"></span>${label}?</span>`
      : `<span class="pw-chip"><span class="pw-dot ${f[k] ? 'pw-tone-ok' : 'pw-tone-idle'}"></span>${label}</span>`).join('');

    // The power flow.
    const mainsOn = !down && !!f.mains_ok;
    const fromBattery = !down && !!f.inverter_on && !mainsOn;
    const apparent = el('pwApparent')!;
    apparent.classList.toggle('pw-age-alert', down);
    apparent.textContent = down
      ? (st?.hasReading && st.lastOk ? 'Last known values, read ' + ago(st.lastOk) : 'No reading yet')
      : st?.apparentVa != null ? 'Apparent power (calculated V × A): ' + Math.round(st.apparentVa).toLocaleString() + ' VA' : '';
    el('pwFlow')!.innerHTML = `
      <div class="pw-node" data-pwn="input"><div class="pw-node-label">Mains input</div><div class="pw-node-v">${fmt(v.input_v)} V</div><div class="pw-node-sub">${fmt(v.input_hz)} Hz</div></div>
      <div class="pw-arrow ${mainsOn ? 'is-on' : ''}"></div>
      <div class="pw-node pw-node-mid${down ? ' pw-node-down' : ''}" data-pwn="unit"><div class="pw-node-label">${u.producerName ? esc(u.producerName) : 'Unit'}</div><div class="pw-node-v pw-tone-${s === 'mains' ? 'ok' : s === 'battery' ? 'warn' : 'bad'}">${esc(statusLabel(u))}</div><div class="pw-node-sub">DC bus ${fmt(v.dc_bus_a)} A</div></div>
      <div class="pw-arrow ${f.output_on && !down ? 'is-on' : ''} ${s === 'fault' ? 'is-bad' : ''}"></div>
      <div class="pw-node" data-pwn="output"><div class="pw-node-label">Output / load</div><div class="pw-node-v">${fmt(v.output_v)} V</div><div class="pw-node-sub">${fmt(v.output_a)} A · ${fmt(v.load_pct, 0)} %</div></div>
      <div class="pw-battery-link ${fromBattery ? 'is-on' : mainsOn && f.charger_on ? 'is-charge' : ''}"><div class="pw-node" data-pwn="battery"><div class="pw-node-label">Battery</div><div class="pw-node-v">${fmt(v.battery_v)} V · ${fmt(v.battery_pct, 0)} %</div></div></div>`;

    // The particles along those lines, from the same status bits.
    flowAnim.set(flowLanes({ down, flags: f, values: v }));

    // Battery: % is voltage-based and reads high while charging, so the
    // voltage stands beside it.
    const pct = v.battery_pct;
    el('pwBatPct')!.innerHTML = pct === undefined ? '—' : fmt(pct, 0) + '<small> %</small>';
    const bar = el('pwBatBar');
    if (bar) {
      bar.style.width = pct === undefined ? '0' : Math.max(0, Math.min(100, pct)) + '%';
      bar.className = pct !== undefined && pct <= 20 ? 'pw-tone-bg-bad' : 'pw-tone-bg-ok';
    }
    el('pwBatKv')!.innerHTML = kvs([
      ['Voltage', fmt(v.battery_v) + ' V'], ['DC bus current', fmt(v.dc_bus_a) + ' A'],
      ['Charger', down ? 'Unknown' : f.charger_on ? 'Running' : 'Off'],
      ['Capacity', u.batteryAh ? u.batteryAh + ' Ah' : 'not set'],
    ]);
    el('pwInput')!.innerHTML = big(v.input_v, 'V') + kvs([['Frequency', fmt(v.input_hz) + ' Hz'],
      ['State', down ? 'Unknown' : mainsOn ? 'OK' : 'lost']]);
    el('pwOutput')!.innerHTML = big(v.output_v, 'V') + kvs([['Frequency', fmt(v.output_hz) + ' Hz'], ['Current', fmt(v.output_a) + ' A']]);
    el('pwLoad')!.innerHTML = big(v.load_pct, '% of rated', 0) + kvs([
      ['Apparent', st?.apparentVa != null ? Math.round(st.apparentVa).toLocaleString() + ' VA' : '—'],
      ['Event', st?.hasReading ? String(st.eventCode).padStart(2, '0') : '—'],
    ]);
    el('pwTemp')!.innerHTML = big(v.temp_internal, '°C internal') + kvs([['Ambient', fmt(v.temp_ambient) + ' °C']]);

    const success = st && st.polls ? Math.round((st.answered / st.polls) * 1000) / 10 : null;
    const router = u.routerId ? routers.find((r) => r.id === u.routerId) : undefined;
    el('pwConn')!.innerHTML = '<b>Connection</b>' + [
      ['Converter', u.host + ':' + u.port + (!down ? '' : st?.cause === 'converter' ? ' (unreachable)'
        : st?.cause === 'unit' ? ' (answering)' : '')], ['Slave ID', String(u.slaveId)], ['RS485', u.serial || '—'],
      ['Reply time', st?.answered ? Math.round(st.replyMs) + ' ms' : '—'],
      ['Success', success == null ? '—' : success + ' %'],
      ['Model', (u.producerName + ' ' + u.modelName).trim() || u.model],
      ...(router ? [['Router', router.label]] : []),
    ].map(([k, val]) => `<span><span class="muted-note">${k}</span> <span class="pw-mono">${esc(val)}</span></span>`).join('');
  }

  const big = (n: number | undefined, unit: string, digits = 1): string =>
    `<div class="pw-big">${fmt(n, digits)}<small> ${esc(unit)}</small></div>`;
  const kvs = (pairs: string[][]): string =>
    '<div class="pw-kv">' + pairs.map(([k, val]) => `<div><span class="muted-note">${esc(k)}</span><span>${esc(val)}</span></div>`).join('') + '</div>';

  // ── THE FORM ──────────────────────────────────────────────────────────────

  const input = <T extends HTMLElement = HTMLInputElement>(id: string): T => el<T>(id)!;

  async function openForm(u: Unit | null): Promise<void> {
    if (!routers.length) {
      try {
        const body = await api<{ routers: { id: string; label?: string; host?: string }[] }>('/api/routers');
        routers = body.routers.map((r) => ({ id: r.id, label: r.label || r.host || r.id }));
      } catch {
        routers = [];
      }
    }
    el('pwf_title')!.textContent = u ? 'Edit ' + u.name : 'Add unit';
    input('pwf_id').value = u?.id || '';
    input('pwf_name').value = u?.name || '';
    input('pwf_host').value = u?.host || '';
    input('pwf_port').value = String(u?.port || 502);
    input('pwf_slave').value = String(u?.slaveId || 1);
    input('pwf_battery').value = u?.batteryAh ? String(u.batteryAh) : '';
    input('pwf_enabled').checked = u ? u.enabled : true;

    // Only the sites a save would be allowed to put it in, plus the one it is
    // in now, which the server will refuse to move it out of if that is wrong.
    const siteSel = input<HTMLSelectElement>('pwf_site');
    const choices = new Set(data.writableSites);
    if (u) choices.add(u.siteId || '');
    siteSel.innerHTML = [...choices].sort((a, b) => siteName(a).localeCompare(siteName(b)))
      .map((id) => `<option value="${esc(id)}">${esc(id ? siteName(id) : 'No site')}</option>`).join('');
    siteSel.value = u?.siteId || [...choices][0] || '';

    // Brand first; the Model list below it holds that brand's models.
    const brandSel = input<HTMLSelectElement>('pwf_brand');
    brandSel.innerHTML = brandsOf(data.models).map((b) =>
      `<option value="${esc(b.id)}">${esc(b.name)}</option>`).join('');
    brandSel.value = data.models.find((m) => m.id === u?.model)?.producer || brandsOf(data.models)[0]?.id || '';
    fillModels(u?.model || '');

    const routerSel = input<HTMLSelectElement>('pwf_router');
    routerSel.innerHTML = '<option value="">None</option>' + routers.map((r) =>
      `<option value="${esc(r.id)}">${esc(r.label)}</option>`).join('');
    routerSel.value = u?.routerId || '';

    el('pwf_delete')!.hidden = !u;
    formError('');
    el('pwFormWrap')!.classList.add('open');
    input('pwf_name').focus();
  }

  /** The Model list for the brand chosen, keeping `want` when it is one of them. */
  function fillModels(want: string): void {
    const list = modelsOf(data.models, input<HTMLSelectElement>('pwf_brand').value);
    const modelSel = input<HTMLSelectElement>('pwf_model');
    modelSel.innerHTML = list.map((m) => `<option value="${esc(m.id)}">${esc(m.modelName)}</option>`).join('');
    modelSel.value = list.some((m) => m.id === want) ? want : list[0]?.id || '';
    drawModelInfo();
  }

  /** The "?" beside Model: the chosen model's technical details, closed on each change. */
  function drawModelInfo(): void {
    const details = data.models.find((m) => m.id === input<HTMLSelectElement>('pwf_model').value)?.details || [];
    const btn = el('pwf_modelInfoBtn')!, box = el('pwf_modelInfo')!;
    btn.hidden = !details.length;
    btn.setAttribute('aria-expanded', 'false');
    box.hidden = true;
    box.innerHTML = details.map((d) => '<li>' + esc(d) + '</li>').join('');
  }

  function formError(msg: string): void {
    const n = el('pwf_error');
    if (!n) return;
    n.textContent = msg;
    n.hidden = !msg;
  }

  async function save(): Promise<void> {
    const id = input('pwf_id').value;
    const battery = input('pwf_battery').value.trim();
    const body = {
      name: input('pwf_name').value, siteId: input<HTMLSelectElement>('pwf_site').value,
      model: input<HTMLSelectElement>('pwf_model').value, host: input('pwf_host').value,
      port: Number(input('pwf_port').value), slaveId: Number(input('pwf_slave').value),
      routerId: input<HTMLSelectElement>('pwf_router').value,
      batteryAh: battery ? Number(battery) : null, enabled: input('pwf_enabled').checked,
    };
    try {
      await api(id ? '/api/power/units/' + encodeURIComponent(id) : '/api/power/units', {
        method: id ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      el('pwFormWrap')!.classList.remove('open');
      await load();
    } catch (e) {
      formError((e as Error).message);
    }
  }

  async function remove(): Promise<void> {
    const id = input('pwf_id').value;
    const name = input('pwf_name').value;
    if (!id || !confirm('Delete ' + name + '? Its history and events are deleted with it.')) return;
    try {
      await api('/api/power/units/' + encodeURIComponent(id), { method: 'DELETE' });
      el('pwFormWrap')!.classList.remove('open');
      openId = '';
      await load();
    } catch (e) {
      formError((e as Error).message);
    }
  }

  el('pwAdd')?.addEventListener('click', () => void openForm(null));
  el('pwEdit')?.addEventListener('click', () => void openForm(data.units.find((u) => u.id === openId) || null));
  el('pwf_save')?.addEventListener('click', () => void save());
  el('pwf_brand')?.addEventListener('change', () => fillModels(''));
  el('pwf_model')?.addEventListener('change', drawModelInfo);
  el('pwf_modelInfoBtn')?.addEventListener('click', () => {
    const box = el('pwf_modelInfo')!;
    box.hidden = !box.hidden;
    el('pwf_modelInfoBtn')!.setAttribute('aria-expanded', box.hidden ? 'false' : 'true');
  });
  el('pwf_delete')?.addEventListener('click', () => void remove());
  el('pwf_infoBtn')?.addEventListener('click', () => {
    const info = el('pwf_info');
    if (!info) return;
    info.hidden = !info.hidden;
    el('pwf_infoBtn')!.setAttribute('aria-expanded', info.hidden ? 'false' : 'true');
  });

  // ── LIVE ──────────────────────────────────────────────────────────────────
  //
  // One unit after each of its polls, sent only while this page is open and
  // only for units this viewer may read (internal/server/power_live.go). The
  // events list is re-read when the unit's open conditions change, which is
  // the only time it can have changed.
  socket.on('power:state', (st) => {
    const u = data.units.find((x) => x.id === st.unitId);
    if (!u) return;
    const before = (u.state?.open || []).map((c) => c.kind + c.code).join();
    u.state = st;
    if (!isVisible('power-ups')) return;
    draw();
    if (openId === u.id && before !== st.open.map((c) => c.kind + c.code).join()) void loadEvents();
    // A history row is written once a minute, so the charts are re-read at
    // most that often, and only on an update for the unit on screen.
    if (openId === u.id && Date.now() - historyAt > 60_000) void loadHistory();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'power-ups') {
      void load();
      if (openId) void loadHistory();
    } else {
      stopPowerCharts();
    }
  });
  if (isVisible('power-ups')) void load();
}
