/**
 * A FRAME FROM ANOTHER ROUTER REACHES ONLY onRouter (2026-10-06).
 *
 * A dashboard watching other routers receives their frames on the same socket
 * (docs/dashboards/PLAN.md). Every `on` listener was written for the SELECTED
 * router, so a tagged frame must reach it only when it names that router;
 * `onRouter` listeners hear every router, told which one. Untagged frames are
 * the selected router's, or no router's, and reach both.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.socket-router-entry.ts');
fs.writeFileSync(ENTRY, "export { Socket } from '../web/src/socket.js';\n");
const OUT = path.join(ROOT, 'testdata', '.socket-router.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// Just enough browser for the socket: one fake WebSocket that the test drives.
class FakeWS {
  static OPEN = 1; static CONNECTING = 0;
  static last: FakeWS | null = null;
  readyState = 1;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() { FakeWS.last = this; }
  send(f: string): void { this.sent.push(f); }
  close(): void { /* not needed */ }
}
const g = globalThis as unknown as Record<string, unknown>;
g.WebSocket = FakeWS;
g.window = g;
g.document = { addEventListener: () => {}, hidden: false };
const { Socket } = require(OUT);

const sock = new Socket('ws://test');
const ws = FakeWS.last!;
const deliver = (frame: object): void => ws.onmessage!({ data: JSON.stringify(frame) });

const plain: unknown[] = [];
const any: [unknown, string][] = [];
sock.on('system:update', (d: unknown) => plain.push(d));
sock.onRouter('system:update', (d: unknown, r: string) => any.push([d, r]));

sock.emit('router:select', 'r-A');
assert.strictEqual(sock.selectedRouter(), 'r-A');
deliver({ event: 'system:update', data: 1 });                  // untagged: the selected router's
deliver({ event: 'system:update', data: 2, router: 'r-A' });   // tagged with the selected router
deliver({ event: 'system:update', data: 3, router: 'r-B' });   // another router
assert.deepStrictEqual(plain, [1, 2], 'an ordinary listener heard another router: ' + JSON.stringify(plain));
assert.deepStrictEqual(any, [[1, 'r-A'], [2, 'r-A'], [3, 'r-B']],
  'a router-aware listener missed a router or was told the wrong one: ' + JSON.stringify(any));

// After a switch, frames still in flight from the old router stop reaching
// ordinary listeners.
sock.emit('router:select', 'r-B');
deliver({ event: 'system:update', data: 4, router: 'r-A' });
deliver({ event: 'system:update', data: 5, router: 'r-B' });
assert.deepStrictEqual(plain.slice(2), [5], 'the previous router\'s late frame reached the new one\'s cards');
assert.ok(ws.sent.some((f) => f === '{"event":"router:select","data":"r-B"}'), 'the selection was not sent');

fs.rmSync(OUT, { force: true });
say('# socket-router: tagged frames reach ordinary listeners only for the selected router');
