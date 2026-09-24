/**
 * A MOVE READS AS A MOVE IN THE ASSISTANT'S DIALOG.
 *
 * `change_row`'s `before` reorders a firewall rule, and the proposal that asks
 * the operator to confirm it is the last thing between a model and a reordered
 * chain. Its wording is derived from `action`, and every value it does not know
 * falls through to "Change the …" with an "Apply this change" button - which
 * describes a FIELD edit. A rule whose fields are untouched and whose position
 * moved is not that, and in an ordered table position IS the configuration: the
 * operator confirming it has to be able to tell the two apart.
 *
 * Nothing on the server would fail if this fell through, which is exactly why
 * it is worth a test. Driven through the real module against the DOM shim, so
 * "the payload's action reaches the wording" is one assertion rather than an
 * inspection of a string constant.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.ai-move-entry.ts');
fs.writeFileSync(ENTRY, "export { initAiAgentPage } from '../web/src/pages/ai-agent.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ai-move.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

let checks = 0;
function ok(cond: unknown, msg: string) { assert.ok(cond, msg); checks++; }

const doc = makeDoc([
  'aiAgentLog', 'aiAgentInput', 'aiAgentSend', 'aiAgentThinking', 'aiAgentThinkingLabel',
  'aiAgentClear', 'aiAgentEmpty', 'aiAgentModel',
  'aiProposeBox', 'aiProposeWhat', 'aiProposeCmd', 'aiProposeValues', 'aiProposeWarn',
  'aiProposeApprove', 'aiProposeReject', 'aiProposeTyped', 'aiProposeTypedLabel',
  'aiProposeConfirm', 'aiProposeCreds', 'aiProposeUser', 'aiProposePass',
]);
(global as never as { document: unknown }).document = doc;

const handlers: Record<string, (d: unknown) => void> = {};
const socket = {
  on: (ev: string, fn: (d: unknown) => void) => { handlers[ev] = fn; },
  emit: () => { /* nothing is sent in this test */ },
};

const { initAiAgentPage } = require(OUT);
initAiAgentPage(socket, () => true);

const el = (id: string) => doc.getElementById(id);
const propose = (d: Record<string, unknown>) => handlers['ai:propose']!(d);

const moveFrame = {
  token: 'm1', resource: 'fwFilter', label: 'Filter Rule',
  action: 'move', name: 'input\u0001drop\u0001\u0001\u0001everything else',
  command: '/ip/firewall/filter/move =numbers=*3 =destination=*1',
  warnCode: '', warning: {}, values: { moves: 'in front of "input\u0001accept"' },
};

// ── 1. a move says "Move", on the headline and on the button ────────────────
propose(moveFrame);
ok(el('aiProposeBox').hidden === false, 'the proposal panel stayed hidden');
ok(el('aiProposeWhat').textContent.startsWith('Move the '),
  `a reorder reads as something else: ${JSON.stringify(el('aiProposeWhat').textContent)}`);
ok(el('aiProposeApprove').textContent === 'Move it',
  `the button does not say what it does: ${JSON.stringify(el('aiProposeApprove').textContent)}`);
ok(el('aiProposeCmd').textContent.includes('/ip/firewall/filter/move'),
  'the command the move will send is not shown');
ok(el('aiProposeValues').textContent.includes('moves ='),
  `the destination is not shown: ${JSON.stringify(el('aiProposeValues').textContent)}`);
// A move asks for no typed name and no login: those belong to the reboot-class
// actions, and offering them here would train people to type past them.
ok(el('aiProposeTyped').hidden === true, 'a move asked for the router name to be typed');
ok(el('aiProposeApprove').disabled === false, 'Approve was dead for a move');

// ── 2. THE CONTROLS. An edit and a delete are unchanged by the new branch ───
propose({ ...moveFrame, token: 'e1', action: 'update', values: {} });
ok(el('aiProposeWhat').textContent.startsWith('Change the '),
  `an edit now reads as: ${JSON.stringify(el('aiProposeWhat').textContent)}`);
ok(el('aiProposeApprove').textContent === 'Apply this change',
  `an edit's button now says: ${JSON.stringify(el('aiProposeApprove').textContent)}`);

propose({ ...moveFrame, token: 'd1', action: 'delete', values: {} });
ok(el('aiProposeWhat').textContent.startsWith('Delete the '),
  `a delete now reads as: ${JSON.stringify(el('aiProposeWhat').textContent)}`);
ok(el('aiProposeApprove').textContent === 'Delete it',
  `a delete's button now says: ${JSON.stringify(el('aiProposeApprove').textContent)}`);

fs.rmSync(OUT, { force: true });
say(`ai-propose-move: ${checks} checks passed`);
