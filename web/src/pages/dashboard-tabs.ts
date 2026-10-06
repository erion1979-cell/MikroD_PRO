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
// Every card type still has one element on the page and follows the selected
// device. A dashboard after the first is a list of the cards it shows, each
// with its place; switching swaps the layout the editor holds, repositions the
// cards and moves the room subscriptions from the old set to the new one.
// A card bound to a device of its own is phase 2.

import { el, esc } from '../dom';
import { DEFAULT_LAYOUT, type GridCard } from '../gen/grid-tables';
import { mergeLayout } from './dashboard-grid-layout';
import { applyLayout, loadLayout, saveLayout, syncDashRooms } from './dashboard-grid-store';
import type { GridEditor } from './dashboard-grid-edit';

/** One card on a dashboard after the first: DashboardCard in internal/server/dashboards_api.go. */
export interface DashCard { uid: string; type: string; router: string; x: number; y: number; w: number; h: number }
export interface Dashboard { id: string; name: string; cards: DashCard[] }
export interface Dashboards { mainName: string; list: Dashboard[] }

/** The first dashboard's id in the tab strip. Never a stored id: those are `[a-z0-9]`. */
export const MAIN = '_main';
const ACTIVE_KEY = 'mkd_dash_active';

/** A stored dashboard as the editor's layout: its cards shown, every other type hidden. */
export function toGrid(cards: readonly DashCard[]): GridCard[] {
  const byType: Record<string, DashCard> = {};
  for (const c of cards) byType[c.type] = c;
  return mergeLayout(DEFAULT_LAYOUT.map((def) => {
    const c = byType[def.id];
    return c
      ? { id: def.id, x: c.x, y: c.y, w: c.w, h: c.h, visible: true }
      : Object.assign({}, def, { visible: false });
  }));
}

/** The editor's layout as stored cards: the visible ones, each its own type for now. */
export function fromGrid(layout: readonly GridCard[]): DashCard[] {
  return layout.filter((c) => c.visible)
    .map((c) => ({ uid: c.id, type: c.id, router: '', x: c.x, y: c.y, w: c.w, h: c.h }));
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
  editor: GridEditor, setSaver: (fn: (l: GridCard[]) => void) => void,
): { mainShowing: () => boolean } {
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

  function show(id: string): void {
    if (editor.isEditing()) return;
    const target = id === MAIN ? null : data.list.find((d) => d.id === id);
    if (id !== MAIN && !target) id = MAIN;
    const focused = pageActive();
    if (focused) syncDashRooms(editor.getLayout(), false);
    active = id;
    const layout = target ? toGrid(target.cards) : loadLayout();
    editor.setLayout(layout);
    applyLayout(layout);
    if (focused) syncDashRooms(layout, true);
    setSaver(target
      ? (l) => { target.cards = fromGrid(l); persist(); }
      : saveLayout);
    remember(id);
    draw();
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
  el('dashTabNew')?.addEventListener('click', () => {
    if (editor.isEditing()) return;
    if (data.list.length + 1 >= 20) { window.alert('There can be at most 20 dashboards.'); return; }
    const name = cleanName(window.prompt('Name of the new dashboard:', ''));
    // A new dashboard starts empty: Edit, then Add card.
    if (name) add(name, []);
  });
  el('dashTabRename')?.addEventListener('click', () => {
    if (editor.isEditing()) return;
    const cur = active === MAIN ? data.mainName : data.list.find((d) => d.id === active)?.name || '';
    const name = cleanName(window.prompt('Rename this dashboard:', cur));
    if (!name) return;
    if (active === MAIN) data.mainName = name;
    else { const d = data.list.find((x) => x.id === active); if (d) d.name = name; }
    persist();
    draw();
  });
  el('dashTabCopy')?.addEventListener('click', () => {
    if (editor.isEditing()) return;
    if (data.list.length + 1 >= 20) { window.alert('There can be at most 20 dashboards.'); return; }
    const cur = active === MAIN ? data.mainName : data.list.find((d) => d.id === active)?.name || '';
    const name = cleanName(window.prompt('Name of the copy:', (cur + ' (copy)').slice(0, 40)));
    if (name) add(name, fromGrid(editor.getLayout()));
  });
  el('dashTabDelete')?.addEventListener('click', () => {
    if (editor.isEditing()) return;
    const d = data.list.find((x) => x.id === active);
    if (!d || !window.confirm('Delete the dashboard "' + d.name + '"? Its layout cannot be recovered.')) return;
    data.list = data.list.filter((x) => x !== d);
    persist();
    show(MAIN);
  });

  draw();
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

  return { mainShowing: () => active === MAIN };
}
