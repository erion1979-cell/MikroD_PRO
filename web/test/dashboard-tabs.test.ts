/**
 * NAMED DASHBOARDS, BY THEIR PURE PARTS (2026-10-06).
 *
 * - A stored dashboard becomes a layout that shows its cards where they were
 *   put and hides every other type, and turns back into the same cards.
 * - New ids never collide, and names are refused the way the server refuses
 *   them.
 * - A dashboard counts each device it reads once, the selection standing in
 *   for cards that follow it, and its device limit is the server's.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.dashboard-tabs-entry.ts');
fs.writeFileSync(ENTRY,
  "export { toGrid, fromGrid, copiesOf, newDashboardId, cleanName, devicesRead } from '../web/src/pages/dashboard-tabs.js';\n" +
  "export { fixedDevices, DASH_DEVICES_MAX } from '../web/src/pages/dashboard-device-cards.js';\n" +
  "export { DEFAULT_LAYOUT } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dashboard-tabs.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);

type Card = { uid: string; type: string; router: string; iface: string; x: number; y: number; w: number; h: number };
type Grid = { id: string; x: number; y: number; w: number; h: number; visible: boolean };

// ── A STORED DASHBOARD AS A LAYOUT, AND BACK ────────────────────────────────
// Traffic is a device card: it becomes a copy, by uid. Power/UPS is not: it is
// the one original, shown or hidden.
const stored: Card[] = [
  { uid: 'card-traffic-ab12', type: 'card-traffic', router: 'r-2', iface: 'ether1', x: 1, y: 1, w: 12, h: 5 },
  { uid: 'dc-card-power', type: 'dc-card-power', router: '', iface: '', x: 13, y: 1, w: 12, h: 5 },
];
const grid: Grid[] = m.toGrid(stored);
assert.strictEqual(grid.length, m.DEFAULT_LAYOUT.length + 1,
  'every original card type is in the layout, shown or not, plus one entry per copy');
const shown = grid.filter((c) => c.visible).map((c) => c.id).sort();
assert.deepStrictEqual(shown, ['dash-i-card-traffic-ab12', 'dc-card-power'],
  'a dashboard shows only its own cards, though some types are visible by default: ' + shown);
assert.ok(!grid.find((c) => c.id === 'card-traffic')!.visible,
  'the original Traffic card shows on a named dashboard beside its copy');
const power = grid.find((c) => c.id === 'dc-card-power')!;
assert.deepStrictEqual([power.x, power.y, power.w, power.h], [13, 1, 12, 5], 'a card moved from where it was put');
const specOf = (id: string) => (id === 'dash-i-card-traffic-ab12'
  ? { uid: 'card-traffic-ab12', type: 'card-traffic', router: 'r-2', iface: 'ether1' } : undefined);
const byUid = (l: Card[]) => [...l].sort((x, y) => x.uid.localeCompare(y.uid));
assert.deepStrictEqual(byUid(m.fromGrid(grid, specOf)), byUid(stored),
  'a layout does not turn back into the cards it came from');
assert.deepStrictEqual(m.copiesOf(stored), [{ uid: 'card-traffic-ab12', type: 'card-traffic', router: 'r-2', iface: 'ether1' }],
  'the copies a dashboard draws are not its device cards');

// The original of a device card shown on a named dashboard (a duplicated first
// dashboard, or a reset) is stored as a copy following the selected device.
const dup = m.fromGrid([{ id: 'card-system', x: 1, y: 1, w: 8, h: 4, visible: true }]);
assert.deepStrictEqual(dup, [{ uid: 'card-system', type: 'card-system', router: '', iface: '', x: 1, y: 1, w: 8, h: 4 }]);
assert.ok(m.toGrid(dup).some((c: Grid) => c.id === 'dash-i-card-system' && c.visible),
  'a stored device card did not come back as a copy');

assert.strictEqual(m.toGrid([]).filter((c: Grid) => c.visible).length, 0, 'an empty dashboard shows cards');

// Overlapping cards, as a hand-edited store could hold, are repaired rather
// than drawn on top of each other - a copy against an original included.
const clash: Grid[] = m.toGrid([stored[0], { ...stored[1], x: 1 }]);
const [a, b] = ['dash-i-card-traffic-ab12', 'dc-card-power'].map((id) => clash.find((c) => c.id === id)!);
assert.ok(a.x + a.w <= b.x || b.x + b.w <= a.x || a.y + a.h <= b.y || b.y + b.h <= a.y,
  'two stored cards on the same cells were drawn overlapping');

// ── IDS AND NAMES ───────────────────────────────────────────────────────────
let calls = 0;
const fixed = [0.5, 0.5, 0.25];
const id = m.newDashboardId([m.newDashboardId([], () => 0.5)], () => fixed[calls++]!);
assert.ok(/^[a-z0-9]{1,24}$/.test(id) && calls === 3, 'a taken id was reused, or the id is malformed: ' + id);
assert.strictEqual(m.cleanName('  Head Office  '), 'Head Office');
for (const bad of ['', '   ', null, 'x'.repeat(41), 'a\u0007b']) {
  assert.strictEqual(m.cleanName(bad), null, 'accepted the name ' + JSON.stringify(bad));
}


// ── THE DEVICES A DASHBOARD READS ───────────────────────────────────────────
const at = { iface: '', x: 1, y: 1, w: 4, h: 4 };
const reads: Card[] = [
  { uid: 'a', type: 'card-system', router: 'r-2', ...at },
  { uid: 'b', type: 'dc-card-ping', router: 'r-2', ...at },
  { uid: 'c', type: 'card-system', router: '', ...at },
  { uid: 'dc-card-power', type: 'dc-card-power', router: '', ...at },
  { uid: 'd', type: 'card-system', router: 'r-3', ...at },
];
assert.deepStrictEqual(m.devicesRead(reads, 'r-1'), ['r-2', 'r-1', 'r-3'],
  'each device once, the selection for the cards that follow it');
assert.deepStrictEqual(m.devicesRead(reads, 'r-2'), ['r-2', 'r-3'], 'the selection counted twice');
assert.deepStrictEqual(m.devicesRead([], 'r-1'), [], 'an empty dashboard reads a device');
assert.deepStrictEqual(m.fixedDevices(reads), ['r-2', 'r-3'], 'the selection counted against the limit');
const goMax = /dashDevicesMax\s*=\s*(\d+)/.exec(
  fs.readFileSync(path.join(ROOT, 'internal', 'server', 'dashboards_api.go'), 'utf8'));
assert.ok(goMax, 'dashDevicesMax not found in dashboards_api.go');
assert.strictEqual(m.DASH_DEVICES_MAX, Number(goMax![1]), 'the browser and the server disagree on the device limit');
say('ok - a dashboard counts the devices it reads, under the server\'s limit');

fs.rmSync(OUT, { force: true });
say('# dashboard-tabs: layouts round-trip, ids are unique, names are checked, devices are counted');
