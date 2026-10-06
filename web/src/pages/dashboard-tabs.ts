// Named dashboards: the tab strip above the grid (docs/dashboards/PLAN.md).
//
// ── THE FIRST DASHBOARD IS THE ONE EVERYONE ALREADY HAS ─────────────────────
//
// It is still read and saved by `dashboard-grid-store.ts`, through
// `/api/dashboard-layout` and its own localStorage key. `/api/dashboards` holds
// only its name and the dashboards after it, so somebody who never adds one
// sees exactly what they saw before, plus a tab with its name.
//
// ── ONE GRID, SEVERAL LAYOUTS ───────────────────────────────────────────────
//
// A dashboard after the first is a list of the cards it shows, each with its
// place; switching swaps the layout the editor holds, repositions the cards and
// moves the room subscriptions from the old set to the new one.
//
// ── AND ON IT, CARDS THAT FOLLOW A DEVICE OF THEIR OWN ──────────────────────
//
// The card types in DEVICE_CARDS appear there as copies, any number of each,
// each following the selected device or one of its own
// (dashboard-device-cards.ts). Every other type is still the one element on
// the page, following the selection. The first dashboard keeps the originals.
//
// ── HOW MANY DEVICES IT READS ───────────────────────────────────────────────
//
// Each device a dashboard shows keeps its collectors running while it is on
// screen, so the strip says how many that is, and a dashboard may name at most
// DASH_DEVICES_MAX of them (the selection, which is read anyway, aside).

import { el, esc } from '../dom';
import { askConfirm, askText, tell } from '../dialog';
import { DEFAULT_LAYOUT, type GridCard } from '../gen/grid-tables';
import { mergeLayout, repairOverlaps } from './dashboard-grid-layout';
import { applyLayout, loadLayout, saveLayout, syncDashRooms } from './dashboard-grid-store';
import type { GridEditor } from './dashboard-grid-edit';
import {
  COPY_PREFIX, DASH_DEVICES_MAX, DEVICE_CARDS, fixedDevices, type CopySpec, type DeviceCards,
} from './dashboard-device-cards';
import type { Socket } from '../socket';

/** One card on a dashboard after the first: DashboardCard in internal/server/dashboards_api.go. */
export interface DashCard {
  uid: string; type: string; router: string; iface: string; x: number; y: number; w: number; h: number;
}
export interface Dashboard { id: string; name: string; cards: DashCard[] }
export interface Dashboards { mainName: string; list: Dashboard[] }

/** The first dashboard's id in the tab strip. Never a stored id: those are `[a-z0-9]`. */
export const MAIN = '_main';
const ACTIVE_KEY = 'mkd_dash_active';

const isDevice = (type: string): boolean => DEVICE_CARDS.includes(type);

/** The copies a stored dashboard draws. */
export function copiesOf(cards: readonly DashCard[]): CopySpec[] {
  return cards.filter((c) => isDevice(c.type))
    .map((c) => ({ uid: c.uid, type: c.type, router: c.router, iface: c.iface }));
}

/**
 * A stored dashboard as the editor's layout: every other card type's original
 * shown where the dashboard puts it or hidden, then one entry per copy.
 */
export function toGrid(cards: readonly DashCard[]): GridCard[] {
  const byType: Record<string, DashCard> = {};
  for (const c of cards) if (!isDevice(c.type)) byType[c.type] = c;
  const base = mergeLayout(DEFAULT_LAYOUT.map((def) => {
    const c = byType[def.id];
    return c
      ? { id: def.id, x: c.x, y: c.y, w: c.w, h: c.h, visible: true }
      : Object.assign({}, def, { visible: false });
  }));
  const copies = cards.filter((c) => isDevice(c.type))
    .map((c) => ({ id: COPY_PREFIX + c.uid, x: c.x, y: c.y, w: c.w, h: c.h, visible: true }));
  return repairOverlaps([...base, ...copies]);
}

/**
 * The editor's layout as stored cards. A copy is described by `specOf`; an
 * original of a device card type (from duplicating the first dashboard, or a
 * reset) becomes a copy following the selected device.
 */
export function fromGrid(
  layout: readonly GridCard[], specOf: (id: string) => CopySpec | undefined = () => undefined,
): DashCard[] {
  const out: DashCard[] = [];
  for (const c of layout) {
    if (!c.visible) continue;
    const at = { x: c.x, y: c.y, w: c.w, h: c.h };
    if (c.id.startsWith(COPY_PREFIX)) {
      const sp = specOf(c.id);
      if (sp) out.push({ uid: sp.uid, type: sp.type, router: sp.router, iface: sp.iface, ...at });
    } else {
      out.push({ uid: c.id, type: c.id, router: '', iface: '', ...at });
    }
  }
  return out;
}

/**
 * The devices a dashboard on screen reads: each card's own, or the selected
 * one for a card that follows the selection.
 */
export function devicesRead(cards: readonly DashCard[], selected: string): string[] {
  const out: string[] = [];
  for (const c of cards) {
    const r = c.router || selected;
    if (r && !out.includes(r)) out.push(r);
  }
  return out;
}

/** A new dashboard id, unlike any in `taken`. */
export function newDashboardId(taken: readonly string[], rnd: () => number = Math.random): string {
  for (;;) {
    const id = 'd' + Math.floor(rnd() * 36 ** 6).toString(36);
    if (!taken.includes(id)) return id;
  }
}

/** A name as the server will accept it, or null. */
export function cleanName(s: string | null): string | null {
  const t = (s || '').trim();
  return t && t.length <= 40 && !/[\u0000-\u001f\u007f]/.test(t) ? t : null;
}

function remembered(): string {
  try { return localStorage.getItem(ACTIVE_KEY) || MAIN; } catch { return MAIN; }
}
function remember(id: string): void {
  try { localStorage.setItem(ACTIVE_KEY, id); } catch { /* a convenience only */ }
}

/**
 * Wire the tab strip. `setSaver` points the editor's Save at the dashboard on
 * screen. Returns whether the first dashboard is the one showing, which the
 * grid asks before applying the first dashboard's server copy.
 */
export function initDashboardTabs(
  editor: GridEditor, setSaver: (fn: (l: GridCard[]) => void) => void, devices: DeviceCards, socket: Socket,
): { mainShowing: () => boolean; copyChanged: () => void } {
  let data: Dashboards = { mainName: 'Overview', list: [] };
  let active = MAIN;
  const pageActive = (): boolean => !!el('page-dashboard')?.classList.contains('active');

  function persist(): void {
    void fetch('/api/dashboards', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    }).then((r) => { if (!r.ok) console.warn('[MikroDash] saving dashboards failed - HTTP', r.status); })
      .catch((e) => console.warn('[MikroDash] saving dashboards:', e));
  }

  function draw(): void {
    const bar = el('dashTabBar');
    if (!bar) return;
    const tabs = [{ id: MAIN, name: data.mainName }, ...data.list.map((d) => ({ id: d.id, name: d.name }))];
    bar.innerHTML = tabs.map((t) =>
      '<button class="stab' + (t.id === active ? ' active' : '') + '" type="button" data-dash="' +
      esc(t.id) + '">' + esc(t.name) + '</button>').join('');
    const del = el<HTMLButtonElement>('dashTabDelete');
    if (del) del.disabled = active === MAIN;
  }

  /** The count beside the tabs, and the devices it counts on hover. */
  function drawCount(): void {
    const pill = el('dashDevCount');
    if (!pill) return;
    const cards = fromGrid(editor.getLayout(), devices.spec);
    const read = devicesRead(cards, socket.selectedRouter());
    const fixed = fixedDevices(cards).length;
    pill.textContent = read.length + (read.length === 1 ? ' device' : ' devices');
    pill.title = (read.length ? 'Reads ' + read.map((r) => devices.label(r)).join(', ') : 'Reads no device') +
      (active === MAIN ? '' : ' · ' + fixed + ' of at most ' + DASH_DEVICES_MAX + ' named by its cards');
  }

  function show(id: string): void {
    if (editor.isEditing()) return;
    const target = id === MAIN ? null : data.list.find((d) => d.id === id);
    if (id !== MAIN && !target) id = MAIN;
    const focused = pageActive();
    if (focused) syncDashRooms(editor.getLayout(), false);
    active = id;
    // The copies first: the layout positions their elements.
    if (target) devices.mount(copiesOf(target.cards));
    else devices.unmount();
    const layout = target ? toGrid(target.cards) : loadLayout();
    editor.setLayout(layout);
    applyLayout(layout);
    if (focused) syncDashRooms(layout, true);
    setSaver(target
      ? (l) => {
        target.cards = fromGrid(l, devices.spec);
        persist();
        // Redrawn from what was stored, which drops the copies removed while
        // editing.
        show(target.id);
      }
      : (l) => { saveLayout(l); drawCount(); });
    remember(id);
    draw();
    drawCount();
  }

  function add(name: string, cards: DashCard[]): void {
    const id = newDashboardId(data.list.map((d) => d.id));
    data.list.push({ id, name, cards });
    persist();
    show(id);
  }

  el('dashTabBar')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-dash]') as HTMLElement | null;
    if (b?.dataset.dash && b.dataset.dash !== active) show(b.dataset.dash);
  });
  // The app's own dialogs (dialog.ts), not the browser's. Each asks, then acts
  // on what it captured before asking: the dashboard can change while the
  // dialog is open (a tab click behind it is not possible, a socket update is).
  const full = (): boolean => data.list.length + 1 >= 20;
  const atMost = (): Promise<void> =>
    tell('There can be at most 20 dashboards. Delete one to make room.', { title: 'Dashboards' });
  const nameOf = (): string => (active === MAIN ? data.mainName : data.list.find((d) => d.id === active)?.name || '');
  el('dashTabNew')?.addEventListener('click', async () => {
    if (editor.isEditing()) return;
    if (full()) { await atMost(); return; }
    const name = cleanName(await askText('Name', { title: 'New dashboard', okLabel: 'Create', maxLength: 40 }));
    // A new dashboard starts empty: Edit, then Add card.
    if (name) add(name, []);
  });
  el('dashTabRename')?.addEventListener('click', async () => {
    if (editor.isEditing()) return;
    const was = active;
    const name = cleanName(await askText('Name', { title: 'Rename dashboard', value: nameOf(), okLabel: 'Rename', maxLength: 40 }));
    if (!name) return;
    if (was === MAIN) data.mainName = name;
    else { const d = data.list.find((x) => x.id === was); if (d) d.name = name; }
    persist();
    draw();
  });
  el('dashTabCopy')?.addEventListener('click', async () => {
    if (editor.isEditing()) return;
    if (full()) { await atMost(); return; }
    const cards = fromGrid(editor.getLayout(), devices.spec);
    const name = cleanName(await askText('Name of the copy', {
      title: 'Duplicate dashboard', value: (nameOf() + ' (copy)').slice(0, 40), okLabel: 'Duplicate', maxLength: 40,
    }));
    if (name) add(name, cards);
  });
  el('dashTabDelete')?.addEventListener('click', async () => {
    if (editor.isEditing()) return;
    const d = data.list.find((x) => x.id === active);
    if (!d || !(await askConfirm('Delete the dashboard "' + d.name + '"? Its layout cannot be recovered.', {
      title: 'Delete dashboard', okLabel: 'Delete', danger: true,
    }))) return;
    data.list = data.list.filter((x) => x !== d);
    persist();
    if (active === d.id) show(MAIN);
    else draw();
  });

  // Adding a device card type on a named dashboard makes a copy, however many
  // there already are; on the first dashboard it shows the original, as ever.
  editor.setAddHook((id) => {
    if (active === MAIN || !isDevice(id)) return false;
    const def = DEFAULT_LAYOUT.find((d) => d.id === id);
    const g = devices.add(id, { w: def?.w || 8, h: def?.h || 4 });
    editor.insertCard(g);
    editor.addCard(g.id);
    return true;
  });
  // Discard puts the old layout back; a copy added since has no place in it,
  // so the dashboard is redrawn from what is stored. After the editor's own
  // handler, which was registered first.
  el('dashDiscardBtn')?.addEventListener('click', () => { if (active !== MAIN) show(active); });

  /** A copy's device or interface changed from its own controls: store it. */
  function copyChanged(): void {
    const d = data.list.find((x) => x.id === active);
    if (!d) return;
    for (const c of d.cards) {
      const sp = devices.spec(COPY_PREFIX + c.uid);
      if (sp) { c.router = sp.router; c.iface = sp.iface; }
    }
    persist();
    drawCount();
  }
  socket.on('router:switched', drawCount);

  draw();
  drawCount();
  void fetch('/api/dashboards', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j: { ok?: boolean; dashboards?: Dashboards } | null) => {
      if (!j?.ok || !j.dashboards) return;
      data = j.dashboards;
      const want = remembered();
      if (want !== MAIN && data.list.some((d) => d.id === want)) show(want);
      else draw();
    })
    .catch(() => { /* the first dashboard still shows; no tabs beyond it */ });

  return { mainShowing: () => active === MAIN, copyChanged };
}
