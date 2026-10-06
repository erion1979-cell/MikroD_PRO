/**
 * CARDS THAT FOLLOW A DEVICE OF THEIR OWN, BY THEIR PURE PARTS (2026-10-06).
 *
 * - The watch set names each copy's router, the selection standing in for a
 *   copy that follows it, and only a Traffic copy names an interface.
 * - Before any router is selected, a copy following the selection asks for
 *   nothing rather than for a router called "".
 * - New copy uids are unique and carry their type.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.device-cards-entry.ts');
fs.writeFileSync(ENTRY,
  "export { watchSet, newCopyUid, DEVICE_CARDS } from '../web/src/pages/dashboard-device-cards.js';\n");
const OUT = path.join(ROOT, 'testdata', '.device-cards.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);

const copies = [
  { uid: 'a', type: 'card-traffic', router: 'r-2', iface: 'ether5' },
  { uid: 'b', type: 'card-system', router: '', iface: 'stray' },
  { uid: 'c', type: 'dc-card-bw', router: 'r-3', iface: 'ether9' },
];
assert.deepStrictEqual(m.watchSet(copies, 'r-1'), [
  { router: 'r-2', card: 'card-traffic', iface: 'ether5' },
  { router: 'r-1', card: 'card-system', iface: '' },
  { router: 'r-3', card: 'dc-card-bw', iface: '' },
], 'the watch set: ' + JSON.stringify(m.watchSet(copies, 'r-1')));
assert.deepStrictEqual(m.watchSet(copies, '').map((w: { router: string }) => w.router), ['r-2', 'r-3'],
  'a copy following the selection was watched before anything was selected');

// The six agreed in docs/dashboards/PLAN.md, and the server's list is the same
// (dashWatchCards in internal/server/dashwatch.go).
const goSrc = fs.readFileSync(path.join(ROOT, 'internal', 'server', 'dashwatch.go'), 'utf8');
const goList = goSrc.slice(goSrc.indexOf('var dashWatchCards'), goSrc.indexOf('}', goSrc.indexOf('var dashWatchCards')));
const goCards = [...goList.matchAll(/"([a-z-]+)": true/g)].map((x) => x[1]).sort();
assert.deepStrictEqual([...m.DEVICE_CARDS].sort(), goCards,
  'the browser and the server disagree about which cards follow a device');

let n = 0;
const seq = [0.5, 0.5, 0.75];
const first = m.newCopyUid('card-system', [], () => 0.5);
const uid = m.newCopyUid('card-system', [first], () => seq[n++]!);
assert.ok(uid !== first && uid.startsWith('card-system-') && /^[a-z0-9-]{1,40}$/.test(uid),
  'a copy uid repeated, lost its type or is not one the server accepts: ' + uid);

fs.rmSync(OUT, { force: true });
say('# dashboard-device-cards: watch set, the shared card list and copy uids pinned');
