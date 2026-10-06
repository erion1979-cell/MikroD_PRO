/**
 * MIKRODASH ASKS IN ITS OWN DIALOGS (2026-10-06).
 *
 * - No page calls the browser's confirm, prompt or alert: those read as the
 *   browser speaking ("192.168.20.26:3081 says") and cannot be styled. Every
 *   question goes through src/dialog.ts.
 * - The tests' seam answers in the dialog's place: a confirm returns what the
 *   test says, a prompt returns the test's text, an alert is recorded.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// ── NO BROWSER DIALOGS ──────────────────────────────────────────────────────
const SRC = path.join(ROOT, 'web', 'src');
const found: string[] = [];
const walk = (dir: string): void => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== 'gen') walk(p); continue; }
    if (!p.endsWith('.ts')) continue;
    fs.readFileSync(p, 'utf8').split('\n').forEach((line, i) => {
      const code = line.replace(/\/\/.*$/, '');
      if (/^\s*\*/.test(line)) return;
      if (/(window\.(confirm|alert|prompt)\(|(?<![\w.])(confirm|alert|prompt)\()/.test(code)) {
        found.push(path.relative(ROOT, p) + ':' + (i + 1) + ': ' + line.trim());
      }
    });
  }
};
walk(SRC);
assert.deepStrictEqual(found, [], 'a page asks in the browser\'s dialog, not dialog.ts:\n' + found.join('\n'));

// ── THE TESTS' SEAM ─────────────────────────────────────────────────────────
const ENTRY = path.join(ROOT, 'testdata', '.dialog-entry.ts');
fs.writeFileSync(ENTRY, "export { askConfirm, askText, tell } from '../web/src/dialog.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dialog.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);
fs.rmSync(OUT, { force: true });

(async () => {
  const asked: string[] = [];
  const told: string[] = [];
  (globalThis as Record<string, unknown>).mikrodashTestDialogs = {
    confirm: (msg: string) => { asked.push(msg); return false; },
    prompt: (msg: string, value: string) => msg + '|' + value,
    alert: (msg: string) => { told.push(msg); },
  };
  assert.strictEqual(await m.askConfirm('Delete it?', { danger: true }), false, 'the test\'s "no" was not the answer');
  assert.deepStrictEqual(asked, ['Delete it?'], 'the message did not reach the test');
  assert.strictEqual(await m.askText('Name', { value: 'x' }), 'Name|x', 'the test\'s text was not the answer');
  await m.tell('Saved.');
  assert.deepStrictEqual(told, ['Saved.'], 'an alert was not recorded');
  // A test that answers only confirms: a prompt is cancelled, not hung.
  (globalThis as Record<string, unknown>).mikrodashTestDialogs = { confirm: () => true };
  assert.strictEqual(await m.askText('Name'), null, 'an unanswered prompt was not cancelled');
  assert.strictEqual(await m.askConfirm('Sure?'), true);
  delete (globalThis as Record<string, unknown>).mikrodashTestDialogs;
  console.log('# dialog: no browser dialogs, and the tests\' seam answers');
})().catch((e) => { console.error(e); process.exit(1); });
