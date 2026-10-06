/**
 * THE POWER/UPS PAGE, BY ITS PURE PARTS (2026-10-04).
 *
 * - One status per unit, decided in one place: no poller is "Not polled", a
 *   unit that stopped answering is "Not responding" whatever its last reading
 *   said, and otherwise the reading's mode decides. Its label names the silent
 *   device when the failure tells: the converter, or the inverter behind it.
 * - Durations and ages read the way the mockups show them.
 * - Each kind of event reads as a sentence with the right colour, and an open
 *   one says how long it has lasted so far.
 * - The Reports tab's summary: a window with no polls says "-", not 0%.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.power-ups-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { statusOf, statusLabel, ago, duration, eventLine } from '../web/src/pages/power-ups.js';",
  "export { withGaps } from '../web/src/pages/power-ups-chart.js';",
  "export { powerStats } from '../web/src/pages/reports-power.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.power-ups.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);

// ── ONE STATUS PER UNIT ─────────────────────────────────────────────────────
const state = (over: Record<string, unknown>) => ({
  unitId: 'u1', online: true, hasReading: true, mode: 'mains', values: {}, flags: {}, raw: {},
  apparentVa: null, eventCode: 0, eventText: 'No event', lastOk: 1, replyMs: 100, polls: 1, answered: 1,
  lastError: '', open: [], ...over,
});
const unit = (st: unknown) => ({ id: 'u1', name: 'INV-01', state: st });
assert.strictEqual(m.statusOf(unit(null)), 'idle', 'a unit nothing polls is not "Not polled"');
assert.strictEqual(m.statusOf(unit(state({}))), 'mains');
assert.strictEqual(m.statusOf(unit(state({ mode: 'battery' }))), 'battery');
assert.strictEqual(m.statusOf(unit(state({ mode: 'fault' }))), 'fault');
assert.strictEqual(m.statusOf(unit(state({ mode: 'off' }))), 'off');
// Silence outranks the last reading: a unit on mains that stopped answering is
// not "On mains".
assert.strictEqual(m.statusOf(unit(state({ online: false }))), 'down', 'a silent unit kept its last mode');
assert.strictEqual(m.statusOf(unit(state({ hasReading: false }))), 'waiting', 'a unit that never answered has a mode');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: 'converter' }))), 'Converter unreachable');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: 'unit' }))), 'Inverter not responding');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: '' }))), 'Not responding');
// One failed poll is not an outage: the cause waits for the unit to be down.
assert.strictEqual(m.statusLabel(unit(state({ cause: 'converter' }))), 'On mains', 'a cause outranked a live reading');
assert.strictEqual(m.statusOf(unit(state({ mode: 'nonsense' }))), 'waiting', 'an unknown mode is not caught');

// ── DURATIONS AND AGES ──────────────────────────────────────────────────────
assert.strictEqual(m.duration(48_000), '48 s');
assert.strictEqual(m.duration(5_340_000), '1 h 29 min', 'the mockup\'s outage does not read "1 h 29 min"');
assert.strictEqual(m.duration(3 * 86_400_000 + 7_200_000), '3 d 2 h');
assert.strictEqual(m.ago(0), 'never');
assert.strictEqual(m.ago(96_000, 100_000), '4 s ago');
assert.strictEqual(m.ago(100_000 - 37 * 60_000, 100_000), '37 min ago');

// ── EVENTS AS SENTENCES ─────────────────────────────────────────────────────
const now = 10_000_000;
const ev = (over: Record<string, unknown>) => ({
  kind: 'mains_lost', code: 0, text: 'Mains lost', fault: false, initial: false,
  beganAt: now - 2_220_000, endedAt: null, ...over,
});
let l = m.eventLine(ev({}), now);
assert.ok(l.title.startsWith('Mains lost') && l.sub.includes('37 min so far') && l.tone === 'warn',
  'an ongoing outage: ' + JSON.stringify(l));
l = m.eventLine(ev({ endedAt: now }), now);
assert.ok(l.title.includes('restored') && l.sub.includes('outage lasted 37 min') && l.tone === 'ok',
  'an outage that ended: ' + JSON.stringify(l));
l = m.eventLine(ev({ kind: 'event', code: 3, text: 'Output overload protection', fault: true, endedAt: now - 2_172_000 }), now);
assert.ok(l.title === 'Output overload protection (03)' && l.sub.includes('cleared after 48 s') && l.tone === 'bad',
  'a cleared overload: ' + JSON.stringify(l));
l = m.eventLine(ev({ kind: 'event', code: 9, text: 'ECO starts', fault: false }), now);
assert.strictEqual(l.tone, 'info', 'ECO is coloured as a fault');
l = m.eventLine(ev({ kind: 'not_responding', text: 'Not responding - converter not reachable', initial: true }), now);
assert.strictEqual(l.title, 'Not responding - converter not reachable', 'the silent device is not named');
assert.ok(l.sub.includes('already so when monitoring began') && l.tone === 'idle',
  'a condition already true at start: ' + JSON.stringify(l));

// ── A MISSING INTERVAL BREAKS THE LINE ──────────────────────────────────────
const pt = (t: number, v: number) => ({ t, avg: v, min: v - 1, max: v + 1 });
const g = m.withGaps([pt(0, 230), pt(300_000, 229), pt(1_500_000, 231), pt(1_800_000, 230)], 300_000);
assert.deepStrictEqual(g.map((p: { x: number; y: number | null }) => [p.x, p.y]),
  [[0, 230], [300_000, 229], [600_000, null], [1_500_000, 231], [1_800_000, 230]],
  'a gap of four intervals was bridged: ' + JSON.stringify(g));
assert.strictEqual(m.withGaps([pt(0, 1), pt(400_000, 1)], 300_000).length, 2,
  'a slightly late interval (under one and a half) broke the line');
assert.deepStrictEqual([g[0].min, g[0].max], [229, 231], 'the band is not carried');

// ── THE REPORTS TAB'S SUMMARY ───────────────────────────────────────────────
const rep = (over: Record<string, unknown>) => ({ from: 0, to: 1, outages: 2, outageMs: 5_400_000,
  longestMs: 3_600_000, faults: 1, notResponding: 0, availability: 99.5, events: [],
  stats: { input_v: { avg: 228, min: 180.4, max: 241.6 }, battery_pct: { avg: 80, min: 34, max: 100 } }, ...over });
const cards = new Map(m.powerStats(rep({})).map(([v, l]: [string, string]) => [l, v]));
assert.deepStrictEqual([cards.get('Mains Outages'), cards.get('Time on Battery'), cards.get('Longest Outage'),
  cards.get('Answered Polls'), cards.get('Input Voltage'), cards.get('Lowest Battery'), cards.get('Peak Load')],
['2', '1h 30m', '1h 0m', '99.5%', '180–242 V', '34%', '-'], 'summary cards: ' + JSON.stringify([...cards]));
assert.strictEqual(new Map(m.powerStats(rep({ availability: -1 })).map(([v, l]: [string, string]) => [l, v]))
  .get('Answered Polls'), '-', 'a window with no polls reads as a percentage');

fs.rmSync(OUT, { force: true });
say('# power-ups: status, durations, event lines, chart gaps and the report summary pinned');
