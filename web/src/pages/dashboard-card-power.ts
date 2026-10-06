// The Dashboard's Power/UPS card (dc-card-power): the units at the selected
// router's site, with their status and the readings that matter in an outage.
//
// ── WHICH UNITS ─────────────────────────────────────────────────────────────
//
// `GET /api/power?router=` answers the units at that router's sites, or every
// unit the viewer may read when none is there, and says which (`scoped`). Asked
// again on each router switch; between switches the card follows `power:state`,
// which the server sends while the card is on the grid (internal/server/
// power_live.go). Status and pills are the Power/UPS page's own, so the card and
// the page can never disagree about a unit.

import type { Socket } from '../socket';
import { el, esc } from '../dom';
import { statusOf, STATUS, unitPill, type Unit } from './power-ups';

let units: Unit[] = [];
let scoped = false;
let routerId = '';

const fmt = (v: number | undefined, d = 0): string => (v === undefined ? '—' : v.toFixed(d));

/** The card's body: one line per unit, worst first. */
export function powerCardHTML(list: readonly Unit[]): string {
  if (!list.length) return '<div class="muted-note">No Power/UPS units. Add them on the Power/UPS page.</div>';
  return list
    .slice()
    .sort((a, b) => STATUS[statusOf(a)].rank - STATUS[statusOf(b)].rank || a.name.localeCompare(b.name))
    .map((u) => {
      const v = (u.state?.hasReading && u.state.values) || {};
      return `<div class="dc-pw-row${statusOf(u) === 'down' ? ' dc-pw-row-down' : ''}">
        <span class="dc-pw-name">${esc(u.name)}</span>${unitPill(u)}
        <span class="dc-pw-vals"><span title="Input voltage">In ${fmt(v.input_v)} V</span>
        <span title="Battery">Bat ${fmt(v.battery_pct)} %</span>
        <span title="Load">Load ${fmt(v.load_pct)} %</span></span></div>`;
    })
    .join('');
}

function draw(): void {
  const body = el('dc-pwBody');
  if (body) body.innerHTML = powerCardHTML(units);
  const scope = el('dc-pwScope');
  if (scope) scope.textContent = units.length ? (scoped ? '· this site' : '· all sites') : '';
}

async function load(): Promise<void> {
  const asked = routerId;
  try {
    const r = await fetch('/api/power' + (asked ? '?router=' + encodeURIComponent(asked) : ''),
      { credentials: 'same-origin' });
    if (!r.ok) return;
    const body = (await r.json()) as { units: Unit[]; scoped: boolean };
    if (asked !== routerId) return;
    units = body.units;
    scoped = body.scoped;
    draw();
  } catch {
    // The card keeps what it last drew; the next switch asks again.
  }
}

export function initPowerCard(socket: Socket): void {
  socket.on('router:switched', (d) => {
    routerId = d.activeId;
    void load();
  });
  socket.on('power:state', (st) => {
    const u = units.find((x) => x.id === st.unitId);
    if (u) u.state = st;
    // A unit the card has not listed was added since it loaded: ask again.
    else void load();
    draw();
  });
  el('dc-pwOpen')?.addEventListener('click', (e) => {
    e.preventDefault();
    document.querySelector<HTMLElement>('.nav-item[data-page="power-ups"]')?.click();
  });
}
