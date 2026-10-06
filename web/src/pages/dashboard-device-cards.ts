// Cards that follow a device of their own, on a named dashboard
// (docs/dashboards/PLAN.md, phase 2).
//
// ── A COPY IS THE ORIGINAL CARD, CLONED AND SCOPED ──────────────────────────
//
// Each copy is the Dashboard's own markup for that card, cloned with its ids
// moved to `data-cid` (dashboard-card-scope.ts), and drawn by the same card
// code the original uses, made per copy (`createSystemCard`, `createTrafficCard`
// and the rest). So there is one renderer per card, not a second, simpler one.
//
// ── ITS DATA COMES TAGGED ───────────────────────────────────────────────────
//
// The server streams a watched router's frames with that router's id
// (internal/server/dashwatch.go), and only `socket.onRouter` listeners hear a
// router other than the selected one (web/src/socket.ts). Every copy's frames
// arrive through the listeners below and go to the copies on that router.
//
// ── "THE SELECTED DEVICE" IS A DEVICE TOO ───────────────────────────────────
//
// A copy with no router of its own follows the selection, and is watched like
// any other: the server shares the selection's rooms rather than doubling them.

import { el, esc } from '../dom';
import type { Socket } from '../socket';
import type { GridCard } from '../gen/grid-tables';
import { cloneCard, cloneScope, type CardScope } from './dashboard-card-scope';
import { createSystemCard, type SystemCard } from './dashboard-system';
import { createTrafficCard, type TrafficCard } from './dashboard-traffic';
import { createPingCard, type PingCard } from './dashboard-ping';
import { renderPhysPortsInto } from './dashboard-card-physports';
import { renderBandwidthInto } from './dashboard-card-bandwidth';
import { createWanFlow, type WanFlow } from './wan-flow';

/** The card types that can follow a device of their own (dashwatch.go's list). */
export const DEVICE_CARDS: readonly string[] = [
  'card-system', 'card-traffic', 'dc-card-bw', 'dc-card-ping', 'dc-card-wanflow', 'dc-card-physports',
];

/** A copy's element id: its uid, prefixed so it can never be an original's. */
export const COPY_PREFIX = 'dash-i-';

/** A device a copy can follow: what the picker and the Bandwidth card need of one. */
export interface DeviceRouter {
  id: string; label?: string; name?: string; host?: string; bwDownMbps?: number; bwUpMbps?: number;
}

/** What a copy is, as stored (DashCard in dashboard-tabs.ts, less its place). */
export interface CopySpec { uid: string; type: string; router: string; iface: string }

interface Copy extends CopySpec {
  node: HTMLElement;
  scope: CardScope;
  /** The interface a Traffic or Bandwidth copy is streaming, once known. */
  live: string;
  system?: SystemCard;
  traffic?: TrafficCard;
  ping?: PingCard;
  wan?: WanFlow;
}

/** A new uid for a copy of `type`, unlike any in `taken`. */
export function newCopyUid(type: string, taken: readonly string[], rnd: () => number = Math.random): string {
  for (;;) {
    const uid = type + '-' + Math.floor(rnd() * 36 ** 5).toString(36);
    if (!taken.includes(uid)) return uid;
  }
}

/** The `dash:watch` set for a dashboard's copies, the selection standing in for "no device". */
export function watchSet(
  copies: readonly CopySpec[], selected: string,
): { router: string; card: string; iface: string }[] {
  const out: { router: string; card: string; iface: string }[] = [];
  for (const c of copies) {
    const router = c.router || selected;
    if (router) out.push({ router, card: c.type, iface: c.type === 'card-traffic' ? c.iface : '' });
  }
  return out;
}

export interface DeviceCards {
  /** Draw these copies (a named dashboard's), replacing any drawn. */
  mount(specs: readonly CopySpec[]): void;
  /** Remove every copy: the first dashboard is on screen. */
  unmount(): void;
  /** A copy by its element id, as stored. */
  spec(id: string): CopySpec | undefined;
  /** A new copy of `type`, drawn and returned as a hidden grid card to place. */
  add(type: string, size: { w: number; h: number }): GridCard;
  setRouters(list: readonly DeviceRouter[]): void;
}

/**
 * The copies' manager. `changed` is called when a copy's device or interface
 * is changed from its own controls, so the dashboard can be saved.
 */
export function createDeviceCards(socket: Socket, changed: () => void): DeviceCards {
  const copies = new Map<string, Copy>();
  let routers: readonly DeviceRouter[] = [];
  let lastWatch = '';
  const root = (): HTMLElement | null => el('dash-grid-root');
  const routerOf = (c: Copy): string => c.router || socket.selectedRouter();
  const on = (c: Copy, router: string): boolean => routerOf(c) === router;
  const onScreen = (n: HTMLElement): boolean =>
    !!el('page-dashboard')?.classList.contains('active') && !!n.offsetParent;

  function sendWatch(): void {
    const set = watchSet([...copies.values()], socket.selectedRouter());
    const key = JSON.stringify(set);
    // The same set twice is not news; a reconnect clears this so it is resent.
    if (key === lastWatch) return;
    lastWatch = key;
    socket.emit('dash:watch', set);
  }

  const routerLabel = (r: DeviceRouter): string => r.label || r.name || r.host || r.id;

  /** The device picker in a copy's header. */
  function picker(c: Copy): HTMLSelectElement {
    const sel = document.createElement('select');
    sel.className = 'dash-dev-pick';
    sel.title = 'Which device this card shows';
    const opts = ['<option value="">Selected device</option>'];
    for (const r of routers) {
      if (r.id) opts.push('<option value="' + esc(r.id) + '">' + esc(routerLabel(r)) + '</option>');
    }
    // A device since removed from the list still shows what is stored.
    if (c.router && !routers.some((r) => r.id === c.router)) {
      opts.push('<option value="' + esc(c.router) + '">' + esc(c.router) + ' (not found)</option>');
    }
    sel.innerHTML = opts.join('');
    sel.value = c.router;
    sel.addEventListener('change', () => {
      const spec = { uid: c.uid, type: c.type, router: sel.value, iface: '' };
      replace(c, spec);
      changed();
      sendWatch();
    });
    return sel;
  }

  function build(spec: CopySpec): Copy | null {
    const tpl = document.getElementById(spec.type);
    if (!tpl) return null;
    const node = cloneCard(tpl, COPY_PREFIX + spec.uid);
    // The remove button names the card it removes.
    node.querySelectorAll<HTMLElement>('[data-card]').forEach((b) => { b.dataset.card = node.id; });
    const scope = cloneScope(node);
    const c: Copy = { ...spec, node, scope, live: '' };
    const hdr = node.querySelector('.card-header');
    if (hdr) hdr.appendChild(picker(c));
    switch (spec.type) {
      case 'card-system':
        c.system = createSystemCard(scope);
        break;
      case 'dc-card-ping':
        c.ping = createPingCard(scope);
        break;
      case 'card-traffic': {
        const t = createTrafficCard(scope, () => true);
        t.setRequester((ifName) => {
          c.iface = ifName;
          changed();
          sendWatch();
        });
        c.traffic = t;
        const ifSel = scope.q<HTMLSelectElement>('ifaceSelect');
        ifSel?.addEventListener('change', () => {
          t.pick(ifSel.value);
          c.iface = ifSel.value;
          changed();
          sendWatch();
        });
        const win = scope.q<HTMLSelectElement>('windowSelect');
        const secs: Record<string, number> = { '1m': 60, '5m': 300, '15m': 900, '30m': 1800 };
        win?.addEventListener('change', () => t.applyWindow(secs[win.value] || 60));
        break;
      }
      case 'dc-card-wanflow':
        c.wan = createWanFlow(socket, {
          // Gradient and filter ids must be unique in the document.
          id: 'dcWanFlow-' + spec.uid, fit: 'box', visible: () => onScreen(node),
          cardId: 'dcWanFlowCard', wrapId: 'dcWanFlowWrap', svgId: 'dcWanFlowSvg', emptyId: 'dcWanFlowEmpty',
          noUplinks: 'No uplinks to draw. The WAN page says why.',
          find: (id) => scope.q(id),
          connected: () => true,
        });
        break;
    }
    return c;
  }

  function destroy(c: Copy): void {
    c.traffic?.destroy();
    c.ping?.destroy();
    c.node.remove();
  }

  /** Swap a copy for a new one at the same place: a different device. */
  function replace(old: Copy, spec: CopySpec): void {
    const c = build(spec);
    if (!c) return;
    c.node.style.gridColumn = old.node.style.gridColumn;
    c.node.style.gridRow = old.node.style.gridRow;
    old.node.replaceWith(c.node);
    old.traffic?.destroy();
    old.ping?.destroy();
    copies.set(c.node.id, c);
  }

  // ── EVERY COPY'S FRAMES, BY ROUTER ────────────────────────────────────────
  const each = (fn: (c: Copy) => void): void => { for (const c of copies.values()) fn(c); };
  socket.onRouter('system:update', (d, r) => each((c) => { if (c.system && on(c, r)) c.system.note(d); }));
  socket.onRouter('ping:update', (d, r) => each((c) => { if (c.ping && on(c, r)) c.ping.onUpdate(d); }));
  socket.onRouter('ping:history', (d, r) => each((c) => { if (c.ping && on(c, r)) c.ping.onHistory(d); }));
  socket.onRouter('ifstatus:update', (d, r) => each((c) => {
    if (c.type === 'dc-card-physports' && on(c, r)) renderPhysPortsInto(c.scope, d);
  }));
  socket.onRouter('wan:update', (d, r) => each((c) => { if (c.wan && d && on(c, r)) c.wan.note(d); }));
  socket.onRouter('dash:traffic-history', (d, r) => each((c) => {
    if (!on(c, r) || (c.type !== 'card-traffic' && c.type !== 'dc-card-bw')) return;
    // A copy naming an interface takes only that one's backlog; one naming
    // none (and every Bandwidth copy) takes the router's default, whatever it is.
    if (c.type === 'card-traffic' && c.iface && d.ifName !== c.iface) return;
    if (c.type === 'dc-card-bw' && c.live && d.ifName !== c.live) return;
    c.live = d.ifName;
    c.traffic?.onHistory(d);
  }));
  socket.onRouter('traffic:update', (d, r) => each((c) => {
    if (!on(c, r)) return;
    if (c.traffic) c.traffic.note(d);
    if (c.type === 'dc-card-bw' && c.live && d.ifName === c.live) {
      // The link capacity of the router this copy follows, the defaults when
      // it names none - as the Dashboard's own Bandwidth card does.
      const r = routers.find((x) => x.id === routerOf(c));
      renderBandwidthInto(c.scope, d, { down: r?.bwDownMbps || 1000, up: r?.bwUpMbps || 1000 });
    }
  }));
  socket.onRouter('ifstatus:names', (d, r) => each((c) => {
    if (!c.traffic || !on(c, r)) return;
    const sel = c.scope.q<HTMLSelectElement>('ifaceSelect');
    if (!sel) return;
    const names = (d.interfaces || []).map((i) => i.name);
    const want = c.iface || c.live || sel.value;
    sel.innerHTML = names.map((n) => '<option value="' + esc(n) + '">' + esc(n) + '</option>').join('');
    if (names.includes(want)) sel.value = want;
  }));
  // A reconnect is a new socket with no watches; a switch moves every copy
  // that follows the selection.
  socket.on('connect', () => { lastWatch = ''; sendWatch(); });
  socket.on('router:switched', () => {
    each((c) => {
      if (c.router) return;
      c.system?.reset();
      c.ping?.reset();
      c.traffic?.reset();
      c.live = '';
    });
    sendWatch();
  });

  return {
    mount(specs) {
      this.unmount();
      const host = root();
      for (const s of specs) {
        const c = build(s);
        if (!c || !host) continue;
        host.appendChild(c.node);
        copies.set(c.node.id, c);
      }
      sendWatch();
    },
    unmount() {
      each(destroy);
      copies.clear();
      sendWatch();
    },
    spec(id) {
      const c = copies.get(id);
      return c ? { uid: c.uid, type: c.type, router: c.router, iface: c.iface } : undefined;
    },
    add(type, size) {
      const uid = newCopyUid(type, [...copies.values()].map((c) => c.uid));
      const c = build({ uid, type, router: '', iface: '' });
      const host = root();
      if (c && host) {
        host.appendChild(c.node);
        copies.set(c.node.id, c);
        sendWatch();
      }
      return { id: COPY_PREFIX + uid, x: 1, y: 1, w: size.w, h: size.h, visible: false };
    },
    setRouters(list) {
      routers = list;
      // Redraw every picker with the new names.
      each((c) => {
        const old = c.node.querySelector('.dash-dev-pick');
        if (old) old.replaceWith(picker(c));
      });
    },
  };
}
