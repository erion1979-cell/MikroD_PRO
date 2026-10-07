// The Power/UPS unit view's power flow, animated: glowing particles running
// along the lines between Mains input, the unit, Output / load and Battery, in
// the direction the power goes. The Dashboard's Network Flow card draws its
// particles the same way (dashboard-card-netflow.ts).
//
// ── WHAT FLOWS IS DECIDED BY THE READING, IN ONE PURE FUNCTION ──────────────
//
// `flowLanes` turns a reading into the lanes that carry power now:
//
//   on mains     input → unit, unit → output, and unit → battery while the
//                charger runs;
//   on battery   battery → unit, unit → output;
//   silent       nothing: a unit that is not answering shows no movement,
//                because nobody knows what it is doing.
//
// That is an offline unit, whose mains passes to the output. An ONLINE unit
// (double conversion, the model file's "topology") has a Charger and an
// Inverter inside its box, and its output always comes from the inverter:
//
//   on mains     input → unit, charger → battery while the charger runs,
//                battery → inverter (the DC bus), unit → output;
//   on battery   battery → inverter, unit → output;
//   on bypass    input → unit → output in amber, when the unit reports it:
//                mains straight to the load, the inverter out of the path.
//
// Each comes from the unit's own status bits, never from the mode alone, so a
// fault that switched the output off stops the output lane.
//
// ── THE OVERLAY MEASURES THE BOXES ──────────────────────────────────────────
//
// The flow is an HTML grid (power-ups.ts), redrawn on every reading and laid
// out differently on a phone. The particles are an SVG over it whose lanes are
// measured from the boxes themselves, again on every reading and on resize, so
// they always run between the edges the lines join. A particle's place is kept
// as a fraction of its lane, so a re-measure moves it rather than restarting it.
//
// ── ONE LOOP, ONLY WHILE IT IS SEEN ─────────────────────────────────────────
//
// One requestAnimationFrame loop, stopped whenever the flow is off screen and
// started again by the next reading. Under reduced motion there are no
// particles; the lines' colours still say which are live.

import { el } from '../dom';

const NS = 'http://www.w3.org/2000/svg';

export type FlowNode = 'input' | 'unit' | 'output' | 'battery' | 'charger' | 'inverter';
export type LaneKey = 'mains' | 'output' | 'charge' | 'discharge' | 'dcbus' | 'bypass';

/** One lane carrying power now: where from, where to, and how busy. */
export interface FlowLane { key: LaneKey; from: FlowNode; to: FlowNode; load: number }

/** What `flowLanes` reads of a unit. */
export interface FlowReading {
  down: boolean;
  /** The model's topology: "online", or offline when anything else. */
  topology?: string;
  flags: Record<string, boolean>;
  values: Record<string, number>;
}

/** The lanes carrying power, from the unit's status bits. */
export function flowLanes(r: FlowReading): FlowLane[] {
  if (r.down) return [];
  const f = r.flags;
  const pct = r.values.load_pct;
  // The output's share of the rating drives the input and output lanes; a
  // model that does not report it moves at a steady middle pace.
  const load = pct === undefined ? .3 : Math.max(0, Math.min(1, pct / 100));
  const out: FlowLane[] = [];
  if (r.topology === 'online') {
    const bypass = !!f.bypass;
    if (f.mains_ok) {
      out.push({ key: bypass ? 'bypass' : 'mains', from: 'input', to: 'unit', load });
      if (f.charger_on) out.push({ key: 'charge', from: 'charger', to: 'battery', load: .2 });
    }
    if (bypass) {
      if (f.output_on) out.push({ key: 'bypass', from: 'unit', to: 'output', load });
      return out;
    }
    // On mains the battery floats on the DC bus the charger feeds; without
    // mains it is what the inverter runs on.
    if (f.inverter_on) out.push({ key: f.mains_ok ? 'dcbus' : 'discharge', from: 'battery', to: 'inverter', load });
    if (f.output_on) out.push({ key: 'output', from: 'unit', to: 'output', load });
    return out;
  }
  if (f.mains_ok) {
    out.push({ key: 'mains', from: 'input', to: 'unit', load });
    if (f.charger_on) out.push({ key: 'charge', from: 'unit', to: 'battery', load: .2 });
  } else if (f.inverter_on) {
    out.push({ key: 'discharge', from: 'battery', to: 'unit', load });
  }
  if (f.output_on) out.push({ key: 'output', from: 'unit', to: 'output', load });
  return out;
}

const TRAIL = 8, SPACING = 3.4;

interface Particle { g: SVGGElement; dots: SVGCircleElement[]; f: number; v: number }
interface Lane extends FlowLane {
  a: [number, number]; b: [number, number]; len: number;
  acc: number; parts: Particle[]; layer: SVGGElement;
}

export interface PowerFlowAnim {
  /** The lanes live now; measured against the boxes as they are laid out. */
  set(lanes: readonly FlowLane[]): void;
}

/**
 * The overlay for one flow: `flowId` is the grid of boxes (each marked
 * `data-pwn`), `svgId` the SVG laid over it.
 */
export function createPowerFlowAnim(flowId: string, svgId: string): PowerFlowAnim {
  const reduce = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
  let lanes: Lane[] = [];
  let running = false;
  let last = 0;

  const svg = (): SVGSVGElement | null => el(svgId) as unknown as SVGSVGElement | null;
  const shown = (): boolean => !!el(flowId)?.offsetParent;

  /** Where a lane starts and ends, in the overlay's own pixels. */
  function measure(from: FlowNode, to: FlowNode): { a: [number, number]; b: [number, number] } | null {
    const flow = el(flowId), s = svg();
    const n1 = flow?.querySelector<HTMLElement>(`[data-pwn="${from}"]`);
    const n2 = flow?.querySelector<HTMLElement>(`[data-pwn="${to}"]`);
    if (!s || !n1 || !n2) return null;
    const o = s.getBoundingClientRect();
    const r1 = n1.getBoundingClientRect(), r2 = n2.getBoundingClientRect();
    const mid = (r: DOMRect): [number, number] => [r.left + r.width / 2 - o.left, r.top + r.height / 2 - o.top];
    // The battery sits below the unit: those lanes join bottom edge to top
    // edge, straight down from the unit or from its Charger or Inverter. The
    // rest join the facing side edges.
    if (from === 'battery' || to === 'battery') {
      const top = from === 'battery' ? r2 : r1, bottom = from === 'battery' ? r1 : r2;
      const x = mid(top)[0];
      const up: [number, number] = [x, top.bottom - o.top], down: [number, number] = [x, bottom.top - o.top];
      return from === 'battery' ? { a: down, b: up } : { a: up, b: down };
    }
    const left = r1.left < r2.left ? r1 : r2, right = r1.left < r2.left ? r2 : r1;
    const p: [number, number] = [left.right - o.left, mid(left)[1]];
    const q: [number, number] = [right.left - o.left, mid(right)[1]];
    return r1.left < r2.left ? { a: p, b: q } : { a: q, b: p };
  }

  function remeasure(): void {
    for (const l of lanes) {
      const m = measure(l.from, l.to);
      if (!m) continue;
      l.a = m.a; l.b = m.b;
      l.len = Math.hypot(m.b[0] - m.a[0], m.b[1] - m.a[1]);
    }
  }

  function spawn(l: Lane): void {
    const g = document.createElementNS(NS, 'g') as SVGGElement;
    const dots: SVGCircleElement[] = [];
    for (let i = 0; i < TRAIL; i++) {
      const c = document.createElementNS(NS, 'circle') as SVGCircleElement;
      c.setAttribute('r', (3 * (1 - (i / TRAIL) * .7)).toFixed(2));
      g.appendChild(c);
      dots.push(c);
    }
    l.layer.appendChild(g);
    l.parts.push({ g, dots, f: 0, v: 0 });
  }

  function frame(t: number): void {
    if (!shown() || !lanes.length) { running = false; return; }
    const dt = Math.min(.05, (t - last) / 1000);
    last = t;
    for (const l of lanes) {
      if (l.len < 1) continue;
      // Busier lanes send more particles, faster: 0.8 to 3.5 a second, 60 to
      // 170 px/s, eased so a light load still visibly moves.
      const a = Math.sqrt(l.load);
      const speed = 60 + a * 110;
      l.acc += (.8 + a * 2.7) * dt;
      while (l.acc >= 1) { l.acc -= 1; spawn(l); l.parts[l.parts.length - 1]!.v = speed * (.85 + Math.random() * .3); }
      for (let i = l.parts.length - 1; i >= 0; i--) {
        const p = l.parts[i]!;
        p.f += (p.v * dt) / l.len;
        const head = p.f * l.len;
        if (head - TRAIL * SPACING >= l.len) {
          p.g.remove();
          l.parts.splice(i, 1);
          continue;
        }
        p.dots.forEach((d, k) => {
          const s = head - k * SPACING;
          if (s < 0 || s > l.len) { d.setAttribute('opacity', '0'); return; }
          const u = s / l.len;
          const edge = Math.min(1, s / 10, (l.len - s) / 10);
          d.setAttribute('cx', (l.a[0] + (l.b[0] - l.a[0]) * u).toFixed(1));
          d.setAttribute('cy', (l.a[1] + (l.b[1] - l.a[1]) * u).toFixed(1));
          d.setAttribute('opacity', (edge * Math.pow(1 - k / TRAIL, 1.6)).toFixed(2));
        });
      }
    }
    requestAnimationFrame(frame);
  }

  if (typeof ResizeObserver === 'function') {
    const flow = el(flowId);
    if (flow) new ResizeObserver(() => remeasure()).observe(flow);
  }

  return {
    set(next) {
      const s = svg();
      if (!s || reduce || typeof document.createElementNS !== 'function') return;
      const id = (l: FlowLane): string => l.key + ':' + l.from + '>' + l.to;
      const want = new Set(next.map(id));
      // A lane no longer live goes, particles and all; one still live keeps
      // its particles where they are.
      lanes = lanes.filter((l) => {
        if (want.has(id(l))) return true;
        l.layer.remove();
        return false;
      });
      for (const n of next) {
        const have = lanes.find((l) => id(l) === id(n));
        if (have) { have.load = n.load; continue; }
        const layer = document.createElementNS(NS, 'g') as SVGGElement;
        layer.setAttribute('class', 'pwa-lane pwa-' + n.key);
        s.appendChild(layer);
        lanes.push({ ...n, a: [0, 0], b: [0, 0], len: 0, acc: Math.random(), parts: [], layer });
      }
      remeasure();
      if (!running && lanes.length && shown()) {
        running = true;
        last = performance.now();
        requestAnimationFrame(frame);
      }
    },
  };
}
