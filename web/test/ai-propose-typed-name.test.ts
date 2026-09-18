/**
 * A REBOOT NEEDS THE ROUTER'S NAME TYPED, IN THE ASSISTANT'S DIALOG TOO.
 *
 * `run_action` lets the assistant ask for the pages' own verbs, and two of them
 * reboot the router. The Packages page has always asked for the router's name to
 * be typed back before it applies changes; the assistant's proposal dialog must
 * ask for the same thing, and must not send an approval that omits it.
 *
 * The server checks the word again against the router's own label, so this box
 * is the prompt rather than the check — which is exactly why it is worth a test:
 * a prompt that never appears leaves a one-press reboot behind a button labelled
 * "Run it", and nothing on the server would fail.
 *
 * Driven through the real module against the DOM shim, because the thing being
 * checked is the wiring: the payload's `typedName` reaching the box, the box's
 * value reaching the frame.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.ai-propose-entry.ts');
fs.writeFileSync(ENTRY, "export { initAiAgentPage } from '../web/src/pages/ai-agent.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ai-propose.cjs');
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
const sent: { event: string; data: Record<string, unknown> }[] = [];
const socket = {
  on: (ev: string, fn: (d: unknown) => void) => { handlers[ev] = fn; },
  emit: (ev: string, data: Record<string, unknown>) => { sent.push({ event: ev, data }); },
};

const { initAiAgentPage } = require(OUT);
initAiAgentPage(socket, () => true);

const el = (id: string) => doc.getElementById(id);
const propose = (d: Record<string, unknown>) => handlers['ai:propose']!(d);

// ── 1. a reboot-class action asks for the name, and Approve waits for it ─────
propose({
  token: 't1', kind: 'action', action: 'packages_apply_and_reboot',
  label: 'Apply package changes and reboot', name: '', command: '/system/package/apply-changes',
  typedName: true, routerName: 'CHR Test', warnCode: '', warning: {}, values: {},
});
ok(el('aiProposeBox').hidden === false, 'the proposal panel stayed hidden');
ok(el('aiProposeTyped').hidden === false, 'a reboot action did not ask for the router name');
ok(el('aiProposeTypedLabel').textContent.includes('CHR Test'),
  `the prompt does not name the router: ${JSON.stringify(el('aiProposeTypedLabel').textContent)}`);
ok(el('aiProposeApprove').disabled === true, 'Approve was live before the name was typed');
ok(/reboot/i.test(el('aiProposeApprove').textContent),
  `the button undersells a reboot: ${JSON.stringify(el('aiProposeApprove').textContent)}`);
ok(el('aiProposeWhat').textContent.startsWith('Apply package changes and reboot'),
  `an action reads as a row change: ${JSON.stringify(el('aiProposeWhat').textContent)}`);

// A WRONG name is not enough — the control for the assert below.
el('aiProposeConfirm').value = 'some other router';
el('aiProposeConfirm').fire('input');
ok(el('aiProposeApprove').disabled === true, 'Approve went live for the wrong router name');

// Case and surrounding spaces do not matter; the point is knowing which router.
el('aiProposeConfirm').value = '  chr test  ';
el('aiProposeConfirm').fire('input');
ok(el('aiProposeApprove').disabled === false, 'Approve stayed dead for the right name');

el('aiProposeApprove').fire('click');
const approve = sent.find((s) => s.event === 'ai:write:approve');
ok(approve && approve.data.token === 't1', 'the approval carried no token');
ok(approve && approve.data.confirm === 'chr test',
  `the typed name did not reach the frame: ${JSON.stringify(approve?.data)}`);
ok(el('aiProposeBox').hidden === true, 'the panel stayed open after approving');
ok(el('aiProposeConfirm').value === '', 'the typed name was left in the box for the next proposal');

// ── 2. an ordinary row change asks for nothing ───────────────────────────────
sent.length = 0;
propose({
  token: 't2', kind: 'row', resource: 'dnsStatic', action: 'create', label: 'DNS Entry',
  name: 'db.example', command: '/ip/dns/static/add =name=db.example', warnCode: '', warning: {}, values: {},
});
ok(el('aiProposeTyped').hidden === true, 'a row change asked for a typed router name');
ok(el('aiProposeApprove').disabled === false, 'a row change left Approve disabled');
el('aiProposeApprove').fire('click');
const rowApprove = sent.find((s) => s.event === 'ai:write:approve');
ok(rowApprove && rowApprove.data.token === 't2', 'the row approval carried no token');
ok(rowApprove && rowApprove.data.confirm === undefined,
  `a row approval carried a confirm: ${JSON.stringify(rowApprove?.data)}`);

// ── 3. a second reboot proposal starts from a blank box ──────────────────────
propose({
  token: 't3', kind: 'action', action: 'firmware_upgrade_and_reboot',
  label: 'Upgrade RouterBOOT firmware and reboot', name: '', command: '/system/routerboard/upgrade',
  typedName: true, routerName: 'CHR Test', warnCode: '', warning: {}, values: {},
});
ok(el('aiProposeApprove').disabled === true,
  'the previous proposal’s typed name carried over, so a reboot was one press away');

// ── 4. an action that logs in elsewhere asks the OPERATOR for the login ──────
//
// The bandwidth test: the model named only the server. The dialog shows a user
// and a password field, the approval frame carries what was typed, and closing
// the dialog empties both. The control is proposal 2, a row change, whose frame
// carried neither.
ok(rowApprove && rowApprove.data.password === undefined && rowApprove.data.user === undefined,
  `a row approval carried a login: ${JSON.stringify(rowApprove?.data)}`);
el('aiProposeReject').fire('click');
sent.length = 0;
propose({
  token: 't4', kind: 'action', action: 'bandwidth_test', label: 'Bandwidth test', name: '198.51.100.53',
  command: '/tool/bandwidth-test address=198.51.100.53 duration=5s protocol=tcp direction=both',
  credentials: true, routerName: 'CHR Test', warnCode: '', warning: {}, values: {},
});
ok(el('aiProposeCreds').hidden === false, 'a bandwidth test did not ask for the far login');
ok(!/password|hunter/i.test(el('aiProposeCmd').textContent || ''), 'the command shown carries a password');
el('aiProposeUser').value = ' md-btest ';
el('aiProposePass').value = 'hunter2';
el('aiProposeApprove').fire('click');
const credApprove = sent.find((s) => s.event === 'ai:write:approve');
ok(credApprove && credApprove.data.user === 'md-btest' && credApprove.data.password === 'hunter2',
  `the typed login did not reach the frame: ${JSON.stringify(credApprove?.data)}`);
ok(el('aiProposePass').value === '' && el('aiProposeUser').value === '' && el('aiProposeCreds').hidden === true,
  'the login was left in the dialog after approving');

// ── 5. a router change closes the proposal (review 2026-09-19) ───────────────
//
// A proposal is about the router it was raised on; the server refuses it on any
// other, and the page must not offer Approve against the next router either.
// The control is section 2: the same row proposal, approved while no router
// change came between, did send its frame.
el('aiProposeReject').fire('click');
sent.length = 0;
propose({
  token: 't5', kind: 'row', resource: 'fwFilter', action: 'create', label: 'Firewall Rule',
  name: 'accept ssh', command: '/ip/firewall/filter/add', warnCode: '', warning: {}, values: {},
});
ok(el('aiProposeBox').hidden === false, 'the proposal did not open');
handlers['router:active']!({ activeId: 'r-B' });
ok(el('aiProposeBox').hidden === true, 'the proposal stayed open after the router changed');
el('aiProposeApprove').fire('click');
ok(!sent.some((s) => s.event === 'ai:write:approve'),
  'Approve still sent a proposal raised on the previous router');

fs.rmSync(OUT, { force: true });
say(`ai-propose-typed-name: ${checks} checks passed`);
