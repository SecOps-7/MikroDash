/**
 * THE AI AGENT'S SUGGESTED QUESTIONS (2026-09-22).
 *
 * The welcome offers questions as cards; clicking one asks it. Pinned:
 *   - a click sends the card's own question as `ai:ask`, through the ordinary
 *     send path (so it lands in the transcript and the input is cleared);
 *   - a click that is not on a card sends nothing (the control);
 *   - while an answer is pending a second card is refused, as typing is;
 *   - the page offers a Router security check, which the operator asked for.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.ai-suggest-entry.ts');
fs.writeFileSync(ENTRY, "export { initAiAgentPage } from '../web/src/pages/ai-agent.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ai-suggest.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const doc = makeDoc([
  'aiAgentLog', 'aiAgentInput', 'aiAgentSend', 'aiAgentThinking', 'aiAgentThinkingLabel',
  'aiAgentClear', 'aiAgentEmpty', 'aiAgentModel',
  'aiProposeBox', 'aiProposeWhat', 'aiProposeCmd', 'aiProposeValues', 'aiProposeWarn',
  'aiProposeApprove', 'aiProposeReject', 'aiProposeTyped', 'aiProposeTypedLabel',
  'aiProposeConfirm', 'aiProposeCreds', 'aiProposeUser', 'aiProposePass',
]);
(global as never as { document: unknown }).document = doc;

const sent: { ev: string; d: unknown }[] = [];
const handlers: Record<string, (d: unknown) => void> = {};
const socket = { on: (ev: string, fn: (d: unknown) => void) => { handlers[ev] = fn; }, emit: (ev: string, d: unknown) => sent.push({ ev, d }) };
require(OUT).initAiAgentPage(socket, () => true);
const asks = () => sent.filter((s) => s.ev === 'ai:ask').map((s) => (s.d as { text: string }).text);

const card = (prompt: string) => ({ closest: (sel: string) => (sel === '[data-ai-prompt]' ? { getAttribute: () => prompt } : null) });
const empty = doc.getElementById('aiAgentEmpty');

// ── A CLICK BESIDE THE CARDS SENDS NOTHING (the control) ──────────────────
empty.fire('click', { target: { closest: () => null } });
assert.deepStrictEqual(asks(), [], 'a click on the welcome text sent a question');

// ── A CARD ASKS ITS OWN QUESTION ──────────────────────────────────────────
empty.fire('click', { target: card('Run a security check on this router.') });
assert.deepStrictEqual(asks(), ['Run a security check on this router.'], 'the card did not ask its question');
assert.strictEqual(doc.getElementById('aiAgentInput').value, '', 'the question stayed in the input');

// ── NOT WHILE AN ANSWER IS PENDING ────────────────────────────────────────
empty.fire('click', { target: card('How is this router doing?') });
assert.strictEqual(asks().length, 1, 'a second card was sent while the first was unanswered');
handlers['ai:error']!({ error: 'the model is unavailable' }); // ends the wait as a reply does, without Markdown the shim cannot render
empty.fire('click', { target: card('How is this router doing?') });
assert.strictEqual(asks().length, 2, 'after the answer, a card no longer asks');
// End that wait too: a pending answer keeps the page's rotating label on an
// interval, which would hold the test process open.
handlers['ai:error']!({ error: 'the model is unavailable' });

// ── THE PAGE OFFERS A SECURITY CHECK ──────────────────────────────────────
const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-ai-agent.html'), 'utf8');
const prompts = [...html.matchAll(/data-ai-prompt="([^"]+)"/g)].map((m) => m[1]);
assert.ok(prompts.length >= 4, `only ${prompts.length} suggestions`);
assert.ok(prompts.some((p) => /security check/i.test(p!)), 'no Router security check suggestion');

fs.rmSync(OUT, { force: true });
console.log('ai-suggestions: all checks passed');
