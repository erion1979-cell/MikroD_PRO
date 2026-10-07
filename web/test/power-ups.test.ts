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
 * - The form's Brand list holds each brand once, and its Model list only that
 *   brand's models.
 * - The power flow moves the way the status bits say: input to unit to output
 *   (and to the battery while charging) on mains, battery to unit to output on
 *   battery, and nothing at all from a unit that is not answering.
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
  "export { statusOf, statusLabel, ago, duration, eventLine, brandsOf, modelsOf, unitLabel, batteryTone } from '../web/src/pages/power-ups.js';",
  "export { withGaps } from '../web/src/pages/power-ups-chart.js';",
  "export { flowLanes } from '../web/src/pages/power-flow-anim.js';",
  "export { powerStats, defaultSep, EXPORT_STEPS } from '../web/src/pages/reports-power.js';",
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
// A reading kept from before a restart is last known: until the unit answers
// this run it is connecting, and if it stays silent it is not responding.
assert.strictEqual(m.statusOf(unit(state({ answered: 0, polls: 1 }))), 'waiting', 'a kept reading reads as live');
assert.strictEqual(m.statusOf(unit(state({ answered: 0, online: false }))), 'down', 'a silent unit with a kept reading is not down');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: 'converter' }))), 'Converter unreachable');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: 'unit' }))), 'Inverter not responding');
assert.strictEqual(m.statusLabel(unit(state({ online: false, cause: '' }))), 'Not responding');
// One failed poll is not an outage: the cause waits for the unit to be down.
assert.strictEqual(m.statusLabel(unit(state({ cause: 'converter' }))), 'On mains', 'a cause outranked a live reading');
assert.strictEqual(m.statusOf(unit(state({ mode: 'nonsense' }))), 'waiting', 'an unknown mode is not caught');

// ── BRAND, THEN MODEL ───────────────────────────────────────────────────────
const models = [
  { id: 'powerguard/v2', producer: 'powerguard', producerName: 'PowerGuard', modelName: 'Online UPS', kind: 'ups' },
  { id: 'acme/x', producer: 'acme', producerName: 'Acme', modelName: 'X', kind: 'inverter' },
  { id: 'powerguard/modbus-v1.1', producer: 'powerguard', producerName: 'PowerGuard', modelName: 'Line-interactive', kind: 'inverter' },
];
assert.deepStrictEqual(m.brandsOf(models), [{ id: 'acme', name: 'Acme' }, { id: 'powerguard', name: 'PowerGuard' }],
  'each brand once, by name');
assert.deepStrictEqual(m.modelsOf(models, 'powerguard').map((x: { id: string }) => x.id),
  ['powerguard/modbus-v1.1', 'powerguard/v2'], "a brand's models, by name");
assert.deepStrictEqual(m.modelsOf(models, 'nobody'), [], 'an unknown brand has models');
// The unit's box reads "Model - Brand", the brand in its own span for a phone to drop.
assert.strictEqual(m.unitLabel({ modelName: 'HP-10212', producerName: 'PowerGuard' }),
  '<span class="pw-mname">HP-10212</span><span class="pw-bname"> - PowerGuard</span>');
assert.strictEqual(m.unitLabel({ modelName: '', producerName: '' }), 'Unit', 'a unit with no model has no label');

// ── WHICH WAY THE POWER FLOWS ───────────────────────────────────────────────
const lanes = (flags: Record<string, boolean>, down = false, values: Record<string, number> = { load_pct: 42 }) =>
  m.flowLanes({ down, flags, values }).map((l: { from: string; to: string; key: string }) => l.key + ':' + l.from + '>' + l.to).join(' ');
assert.strictEqual(lanes({ mains_ok: true, charger_on: true, inverter_on: false, output_on: true }),
  'mains:input>unit charge:unit>battery output:unit>output', 'on mains, charging');
assert.strictEqual(lanes({ mains_ok: true, charger_on: false, inverter_on: false, output_on: true }),
  'mains:input>unit output:unit>output', 'a charger that is off still fills the battery');
assert.strictEqual(lanes({ mains_ok: false, charger_on: false, inverter_on: true, output_on: true }),
  'discharge:battery>unit output:unit>output', 'on battery');
assert.strictEqual(lanes({ mains_ok: true, charger_on: true, inverter_on: false, output_on: false }),
  'mains:input>unit charge:unit>battery', 'an output switched off still flows');
assert.strictEqual(lanes({ mains_ok: true, charger_on: true, inverter_on: true, output_on: true }, true), '',
  'a unit that is not answering shows movement');
// An online unit: mains charges the battery through the Charger, the Inverter
// runs from the battery side and always feeds the output; mains reaches the
// output only on bypass.
const online = (flags: Record<string, boolean>, down = false) =>
  m.flowLanes({ down, topology: 'online', flags, values: { load_pct: 42 } })
    .map((l: { from: string; to: string; key: string }) => l.key + ':' + l.from + '>' + l.to).join(' ');
assert.strictEqual(online({ mains_ok: true, charger_on: true, inverter_on: true, output_on: true }),
  'mains:input>unit charge:charger>battery dcbus:battery>inverter output:unit>output', 'online, on mains');
assert.strictEqual(online({ mains_ok: false, charger_on: false, inverter_on: true, output_on: true }),
  'discharge:battery>inverter output:unit>output', 'online, on battery');
assert.strictEqual(online({ mains_ok: true, charger_on: true, inverter_on: false, output_on: true, bypass: true }),
  'bypass:input>unit charge:charger>battery bypass:unit>output', 'online, on bypass');
assert.strictEqual(online({ mains_ok: true, charger_on: false, inverter_on: false, output_on: false }),
  'mains:input>unit', 'an online unit with its inverter off still feeds the output');
assert.strictEqual(online({ mains_ok: true, inverter_on: true, output_on: true }, true), '',
  'a silent online unit shows movement');
assert.strictEqual(lanes({ mains_ok: true, charger_on: true, inverter_on: false, output_on: true, bypass: true }),
  'mains:input>unit charge:unit>battery output:unit>output', 'an offline unit drew a bypass');
assert.strictEqual(m.flowLanes({ down: false, flags: { mains_ok: true, output_on: true }, values: { load_pct: 250 } })[0].load, 1,
  'a load past 100 % is not capped');

// ── THE BATTERY BOX BY CHARGE ───────────────────────────────────────────────
for (const [pct, tone] of [[100, 'ok'], [30, 'ok'], [29, 'warn'], [10, 'warn'], [9, 'down'], [0, 'down']] as [number, string][]) {
  assert.strictEqual(m.batteryTone(pct), tone, pct + ' % is not ' + tone);
}
assert.strictEqual(m.batteryTone(undefined), '', 'a unit reporting no charge is coloured');

// ── THE EXPORT FOR EXCEL ────────────────────────────────────────────────────
// The CSV form follows the language's decimal sign, as Excel does.
assert.strictEqual(m.defaultSep('de-DE'), 'semicolon', 'a decimal-comma language got commas');
assert.strictEqual(m.defaultSep('en-US'), 'comma', 'a decimal-point language got semicolons');
// The page offers exactly the intervals the server folds into.
const goSteps = [...fs.readFileSync(path.join(ROOT, 'internal', 'server', 'power_export.go'), 'utf8')
  .matchAll(/"(\d+[mhd])":\s*[\d_]+/g)].map((x) => x[1]).sort();
assert.deepStrictEqual(m.EXPORT_STEPS.map((x: string[]) => x[0]).sort(), goSteps, 'the page and the server disagree on the intervals');

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
