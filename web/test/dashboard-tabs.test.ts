/**
 * NAMED DASHBOARDS, BY THEIR PURE PARTS (2026-10-06).
 *
 * - A stored dashboard becomes a layout that shows its cards where they were
 *   put and hides every other type, and turns back into the same cards.
 * - New ids never collide, and names are refused the way the server refuses
 *   them.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.dashboard-tabs-entry.ts');
fs.writeFileSync(ENTRY,
  "export { toGrid, fromGrid, newDashboardId, cleanName } from '../web/src/pages/dashboard-tabs.js';\n" +
  "export { DEFAULT_LAYOUT } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dashboard-tabs.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);

type Card = { uid: string; type: string; router: string; x: number; y: number; w: number; h: number };
type Grid = { id: string; x: number; y: number; w: number; h: number; visible: boolean };

// ── A STORED DASHBOARD AS A LAYOUT, AND BACK ────────────────────────────────
const stored: Card[] = [
  { uid: 'card-traffic', type: 'card-traffic', router: '', x: 1, y: 1, w: 12, h: 5 },
  { uid: 'dc-card-power', type: 'dc-card-power', router: '', x: 13, y: 1, w: 12, h: 5 },
];
const grid: Grid[] = m.toGrid(stored);
assert.strictEqual(grid.length, m.DEFAULT_LAYOUT.length, 'every card type is in the layout, shown or not');
const shown = grid.filter((c) => c.visible).map((c) => c.id).sort();
assert.deepStrictEqual(shown, ['card-traffic', 'dc-card-power'],
  'a dashboard shows only its own cards, though some types are visible by default: ' + shown);
const power = grid.find((c) => c.id === 'dc-card-power')!;
assert.deepStrictEqual([power.x, power.y, power.w, power.h], [13, 1, 12, 5], 'a card moved from where it was put');
assert.deepStrictEqual(m.fromGrid(grid), stored, 'a layout does not turn back into the cards it came from');

assert.strictEqual(m.toGrid([]).filter((c: Grid) => c.visible).length, 0, 'an empty dashboard shows cards');

// Overlapping cards, as a hand-edited store could hold, are repaired rather
// than drawn on top of each other.
const clash: Grid[] = m.toGrid([stored[0], { ...stored[1], x: 1 }]);
const [a, b] = ['card-traffic', 'dc-card-power'].map((id) => clash.find((c) => c.id === id)!);
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

fs.rmSync(OUT, { force: true });
say('# dashboard-tabs: layouts round-trip, ids are unique, names are checked');
