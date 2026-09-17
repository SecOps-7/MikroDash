/**
 * THE AGENT OVERVIEW CARD TYPES ITS LINE.
 *
 * The operator asked for the card to look like a terminal being typed into: the
 * prompt and a blinking cursor alone for at least a second, then the text typed
 * in front of the cursor. None of that is visible to a check that renders once
 * and reads the result, because the RESULT of an instant render and a typed one
 * is the same string. So this samples the DOM over time:
 *
 *   - during the hold, nothing is typed yet;
 *   - part way through, a proper prefix is on screen (typing, not instant);
 *   - at the end, the whole line, the attribution, and a cursor blinking again;
 *   - the same line re-sent is left alone rather than retyped;
 *   - a refusal replaces a line mid-type at once, with no cursor.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.agent-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderAgentCard, TYPE_HOLD_MS } from '../web/src/pages/dashboard-card-agent.js';\n");
const OUT = path.join(ROOT, 'testdata', '.agent.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
let checks = 0;
function ok(cond: unknown, msg: string) { assert.ok(cond, msg); checks++; }

(async () => {
  const doc = makeDoc(['dc-agentText', 'dc-agentCursor', 'dc-agentMeta', 'dc-agentRefresh', 'dc-agentTerm']);
  (global as any).document = doc;
  const { renderAgentCard, TYPE_HOLD_MS } = require(OUT);
  const text = () => doc.getElementById('dc-agentText').textContent;
  const cursor = doc.getElementById('dc-agentCursor');
  const meta = () => doc.getElementById('dc-agentMeta').textContent;

  ok(TYPE_HOLD_MS >= 1000, `the hold is ${TYPE_HOLD_MS}ms; the operator asked for at least a second`);

  const line = 'Router looks healthy: 6% CPU, 28 clients online';
  renderAgentCard({ text: line, error: '', model: 'test-model', at: 1000, color: '#38bdf8' });

  // ── 1. the hold: prompt and cursor only ───────────────────────────────────
  ok(text() === '', `the line appeared before the hold: ${JSON.stringify(text())}`);
  ok(cursor.style.display !== 'none', 'the cursor is hidden during the hold');
  ok(meta() === '', 'the attribution appeared before the sentence it attributes');
  await sleep(TYPE_HOLD_MS - 200);
  ok(text() === '', `typing started before the hold elapsed: ${JSON.stringify(text())}`);

  // ── 2. part way: a proper prefix, and the cursor held solid ───────────────
  await sleep(200 + 300);
  const mid = text();
  ok(mid.length > 0 && mid.length < line.length && line.startsWith(mid),
    `mid-way the card shows ${JSON.stringify(mid)}, which is not a partial line (instant or broken)`);
  ok(cursor.classList.contains('is-typing'), 'the cursor is not held solid while typing');

  // ── 3. the end: whole line, attribution, blinking again ───────────────────
  await sleep(4500);
  ok(text() === line, `typing ended on ${JSON.stringify(text())}`);
  ok(!cursor.classList.contains('is-typing'), 'the cursor did not go back to blinking');
  ok(meta().startsWith('test-model'), `the attribution is ${JSON.stringify(meta())}`);
  ok(doc.getElementById('dc-agentTerm').style.color === '#38bdf8', 'the text colour was not applied');

  // ── 4. the same line re-sent is not retyped ───────────────────────────────
  renderAgentCard({ text: line, error: '', model: 'test-model', at: 1000, color: '#38bdf8' });
  ok(text() === line, 'a re-sent line was cleared to be typed again');

  // ── 5. a refusal mid-type is shown at once, with no cursor ────────────────
  renderAgentCard({ text: 'A different line that will be interrupted', error: '', model: 'm', at: 2000, color: '#38bdf8' });
  await sleep(TYPE_HOLD_MS + 150);
  renderAgentCard({ text: '', error: 'The endpoint refused', model: '', at: 3000, color: 'not-a-colour' });
  ok(text() === 'The endpoint refused', `the refusal reads ${JSON.stringify(text())}`);
  ok(cursor.style.display === 'none', 'a cursor blinks beside an error');
  await sleep(400);
  ok(text() === 'The endpoint refused', 'the interrupted typing kept writing over the refusal');
  ok(doc.getElementById('dc-agentTerm').style.color === '', 'an invalid colour reached the style');

  fs.rmSync(OUT, { force: true });
  say(`agent-card-typing: ${checks} checks passed`);
})().catch((e) => { fs.rmSync(OUT, { force: true }); console.error(e); process.exit(1); });
