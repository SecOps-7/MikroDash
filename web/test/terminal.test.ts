/**
 * The Terminal page, and the property the whole page rests on: text that came
 * off a router never becomes markup.
 *
 * ── A HYBRID DOCUMENT, AND WHY ──────────────────────────────────────────────
 *
 * `makeDoc` is an ID REGISTRY: it wires the page's elements, but its
 * `createElement` is `mk('')` and discards the tag, and `appendChild`
 * concatenates `child.outerHTML`, which a node-built subtree does not have. So
 * asserting "a <pre> was produced" against it would be comparing the shim's
 * approximation of a thing this page never does.
 *
 * So the ids come from `makeDoc` and `createElement` is a small tag-preserving
 * factory, the way markdown.test.ts and nine other files already do it for
 * node-building code. The scrollback collects real nodes.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.terminal-entry.ts');
fs.writeFileSync(ENTRY, "export { initTerminalPage } from '../web/src/pages/terminal.js';\n");
const OUT = path.join(ROOT, 'testdata', '.terminal.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

// Elements keep their TAG, which is the point of the local factory.
function node(tag: string): any {
  return {
    nodeType: 1,
    tagName: String(tag).toUpperCase(),
    className: '',
    children: [] as any[],
    appendChild(c: any) { c.parentNode = this; this.children.push(c); return c; },
    parentNode: null as any,
    removeChild(c: any) { this.children = this.children.filter((x: any) => x !== c); return c; },
    _text: '',
    _htmlWrites: 0,
    get firstChild() { return this.children[0] || null; },
    get textContent(): string {
      return this.children.length
        ? this.children.map((c: any) => c.textContent).join('')
        : this._text;
    },
    set textContent(v: string) { this.children = []; this._text = String(v); },
    // A tripwire: nothing in this page may assign markup.
    get innerHTML(): string { return ''; },
    set innerHTML(_v: string) { this._htmlWrites++; htmlWrites++; },
  };
}
let htmlWrites = 0;

const doc = makeDoc(['terminalCard', 'terminalScroll', 'terminalInput', 'terminalRun',
  'terminalClear', 'terminalPrompt', 'terminalStatus', 'terminalNote']);
doc.createElement = (tag: string) => node(tag);

// The scrollback is one of OUR nodes, substituted into the registry that
// `getElementById` reads, rather than the shim's - whose `children` is a getter
// derived from an innerHTML string, which is the one thing this page never sets.
const n: any = doc.nodes;
const scroll = node('div');
scroll.replaceChild = function (fresh: any, old: any) {
  const i = this.children.indexOf(old);
  if (i >= 0) this.children[i] = fresh; else this.children.push(fresh);
  return old;
};
scroll.scrollHeight = 0; scroll.scrollTop = 0; scroll.clientHeight = 0;
scroll.classList = { add: () => {}, remove: () => {}, toggle: () => {}, contains: () => false };
scroll.focus = () => {};
scroll.addEventListener = () => {};
n.terminalScroll = scroll;
// The shim's nodes have no `focus`; the page focuses its input when the pane
// arms, which is right in a browser. Added here rather than dropped from the
// page, and rather than widened in the shared shim for one caller.
n.terminalInput.focus = () => { focused++; };
let focused = 0;

(global as any).document = doc;
(global as any).window = { addEventListener: () => {}, setTimeout, clearTimeout, getSelection: () => selection };
let selection = '';

const handlers: Record<string, any> = {};
const sent: any[] = [];
mod.initTerminalPage(
  { on: (ev: string, fn: any) => { handlers[ev] = fn; }, emit: (ev: string, d: any) => sent.push([ev, d]) },
  () => true,
);

/** Every descendant with this tag, walked rather than queried. */
function byTag(root: any, tag: string): any[] {
  const want = tag.toUpperCase();
  const out: any[] = [];
  const walk = (x: any): void => {
    if (x && x.nodeType === 1 && x.tagName === want) out.push(x);
    for (const c of (x && x.children) || []) walk(c);
  };
  for (const c of root.children || []) walk(c);
  return out;
}
const blocks = () => scroll.children.length;

let failed = 0;
function check(what: string, fn: () => void): void {
  try { fn(); say('  ok   ' + what); } catch (e) {
    failed++;
    say('  FAIL ' + what + '\n       ' + (e as Error).message);
  }
}

say('terminal: a console whose output is never markup');

// Arm the page the way the server does, with a hostile identity for good measure.
handlers['term:scrollback']({
  routerId: 'r1', entries: [], mayRun: true, why: '',
  identity: '<img src=x onerror=alert(1)>', user: 'admin', trimmed: false, running: false,
});

check('the prompt carries the identity as text, never as markup', () => {
  assert.strictEqual(String(n.terminalPrompt.textContent),
    '[admin@<img src=x onerror=alert(1)>] > ', 'the prompt was not built from textContent');
  assert.strictEqual(htmlWrites, 0, 'something assigned innerHTML');
});

check('caps enable the input and the button', () => {
  assert.strictEqual(n.terminalInput.disabled, false, 'the input is still disabled after mayRun');
  assert.strictEqual(n.terminalRun.disabled, false, 'the Run button is still disabled after mayRun');
});

check('Enter sends the line and latches the input', () => {
  n.terminalInput.value = '/ip address print';
  n.terminalInput.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  assert.deepStrictEqual(sent[sent.length - 1], ['term:run', { line: '/ip address print' }],
    'the line was not sent');
  assert.strictEqual(n.terminalInput.disabled, true,
    'a second line can be typed while the first is running');
  assert.strictEqual(String(n.terminalRun.textContent), 'Stop', 'Run did not become Stop');
  assert.ok(n.terminalRun.classList.contains('sbtn-danger'), 'Stop is not the danger colour');
});

check('ROUTER OUTPUT IS TEXT, NEVER MARKUP', () => {
  // The 0.7.35 case, in the one place it would do most damage: this is a whole
  // console blob, not a single field, and it is the device that chose it.
  const hostile = '<img src=x onerror=alert(1)>\r\n0 R name="<b>ether1</b>"';
  handlers['term:output']({
    entry: { seq: 1, at: 0, command: '/ip address print', lines: hostile.split('\r\n'),
             truncated: false, ms: 3, code: '', message: '' },
    running: false, done: true,
  });
  const pre = byTag(scroll, 'pre');
  assert.strictEqual(pre.length, 1, 'the output is not in a <pre>; RouterOS aligns with spaces');
  assert.strictEqual(String(pre[0].textContent), hostile.replace('\r\n', '\n'),
    'the device text was altered on its way to the page');
  assert.strictEqual(htmlWrites, 0, 'innerHTML was assigned somewhere in this page');
  assert.strictEqual(n.terminalInput.disabled, false, 'the input stayed latched after the answer');
  assert.strictEqual(String(n.terminalRun.textContent), 'Run', 'Stop did not become Run again');
});

// THE ECHO AND THE ANSWER ARE ONE BLOCK, NOT TWO.
//
// A run arrives twice: the echo when the line is sent, so a slow command shows
// at once, and the answer when it returns. Appending both printed every command
// twice - once bare, once above its output. Found in a browser with this file
// already green, because the replay check below counts blocks after a REPLACE
// and never saw an echo followed by its own answer.
check('the answer replaces the echo instead of printing the command twice', () => {
  const before = blocks();
  const entry = { seq: 42, at: 0, command: '/system resource print', lines: [] as string[],
                  truncated: false, ms: 0, code: '', message: '' };
  handlers['term:output']({ entry, running: true, done: false });
  assert.strictEqual(blocks(), before + 1, 'the echo did not draw');
  handlers['term:output']({ entry: { ...entry, lines: ['uptime: 1h'] }, running: false, done: true });
  assert.strictEqual(blocks(), before + 1,
    'the answer appended a second block, so the command is printed twice');
  const b = scroll.children[scroll.children.length - 1];
  const cmds = [...(b.children || [])].flatMap((c: any) =>
    (c.children || []).filter((x: any) => x.className === 'term-cmd'));
  assert.strictEqual(cmds.length, 1, 'the command is echoed more than once in its own block');
  assert.ok(/uptime: 1h/.test(String(b.textContent)), 'the answer was lost in the replace');
});

check('a replay REPLACES the pane and cannot draw anything twice', () => {
  const entry = { seq: 1, at: 0, command: '/ip address print', lines: ['one'],
                  truncated: false, ms: 3, code: '', message: '' };
  const before = blocks();
  handlers['term:scrollback']({ routerId: 'r1', entries: [entry], mayRun: true, why: '',
    identity: 'MikroTik', user: 'admin', trimmed: false, running: false });
  assert.strictEqual(blocks(), 1, 'the replay appended to the pane instead of replacing it');
  handlers['term:scrollback']({ routerId: 'r1', entries: [entry], mayRun: true, why: '',
    identity: 'MikroTik', user: 'admin', trimmed: false, running: false });
  assert.strictEqual(blocks(), 1, 'a second replay drew the same command twice');
  assert.ok(before >= 0);
});

check('a refusal code becomes a sentence, and no device text', () => {
  handlers['term:output']({
    entry: { seq: 0, at: 0, command: '', lines: [], truncated: false, ms: 0,
             code: 'denied', message: '' },
    running: false, done: true,
  });
  // Scoped to the LAST block: earlier checks left their own output in the pane,
  // and counting across all of it would assert about them instead.
  const last = scroll.children[scroll.children.length - 1];
  const txt = String(last.textContent);
  assert.ok(/not available to you/.test(txt), 'the refusal code was not turned into a sentence: ' + txt);
  assert.strictEqual(byTag({ children: [last] }, 'pre').length, 0,
    'a refusal produced a device-output block');
});

check('Up and Down walk the history, and Down again restores the draft', () => {
  const inp = n.terminalInput;
  for (const line of ['/one', '/two', '/three']) {
    inp.value = line;
    inp.fire('keydown', { key: 'Enter', preventDefault: () => {} });
    handlers['term:output']({ entry: { seq: 0, at: 0, command: line, lines: [], truncated: false,
      ms: 0, code: '', message: '' }, running: false, done: true });
  }
  inp.value = 'half-typed';
  inp.fire('keydown', { key: 'ArrowUp', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/three', 'Up did not reach the last command');
  inp.fire('keydown', { key: 'ArrowUp', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/two', 'a second Up did not go further back');
  inp.fire('keydown', { key: 'ArrowDown', preventDefault: () => {} });
  inp.fire('keydown', { key: 'ArrowDown', preventDefault: () => {} });
  assert.strictEqual(inp.value, 'half-typed', 'Down past the end lost what was being typed');
});

check('Ctrl-C copies when there is a selection and interrupts when there is not', () => {
  n.terminalInput.value = '/ip address print';
  n.terminalInput.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  const before = sent.length;

  selection = 'some output the operator highlighted';
  n.terminalInput.fire('keydown', { key: 'c', ctrlKey: true, preventDefault: () => {} });
  assert.strictEqual(sent.length, before,
    'Ctrl-C with a selection interrupted instead of copying; that breaks copying output');

  selection = '';
  n.terminalInput.fire('keydown', { key: 'c', ctrlKey: true, preventDefault: () => {} });
  assert.deepStrictEqual(sent[sent.length - 1], ['term:stop', {}], 'Ctrl-C did not interrupt');
  handlers['term:output']({ entry: { seq: 0, at: 0, command: '', lines: [], truncated: false,
    ms: 0, code: 'stopped', message: 'Stopped waiting.' }, running: false, done: true });
});

check('switching device clears the pane, the history and the prompt', () => {
  handlers['router:switched']({ activeId: 'r2' });
  assert.strictEqual(blocks(), 0, 'one device\'s output survived into another\'s pane');
  assert.strictEqual(String(n.terminalPrompt.textContent), '> ', 'the prompt kept the old identity');
  assert.strictEqual(n.terminalInput.disabled, true,
    'the input stayed enabled before the new device\'s caps arrived');
  n.terminalInput.fire('keydown', { key: 'ArrowUp', preventDefault: () => {} });
  assert.strictEqual(n.terminalInput.value, '', 'the previous device\'s history is still recallable');
});

check('without mayRun the controls are never offered', () => {
  handlers['term:scrollback']({ routerId: 'r2', entries: [], mayRun: false,
    why: 'The Terminal is not available.', identity: '', user: '', trimmed: false, running: false });
  assert.strictEqual(n.terminalInput.disabled, true, 'the input is offered to somebody who may not run');
  assert.strictEqual(n.terminalRun.disabled, true, 'the button is offered to somebody who may not run');
  assert.ok(/not available/.test(String(n.terminalStatus.textContent)), 'no reason was shown');
  const before = sent.length;
  n.terminalInput.value = '/system reboot';
  n.terminalInput.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  assert.strictEqual(sent.length, before, 'a line was sent by somebody who may not run one');
});

check('nothing in this page ever assigned innerHTML', () => {
  assert.strictEqual(htmlWrites, 0,
    'the scrollback is device-controlled text; one innerHTML undoes the whole page (0.7.35)');
});

fs.rmSync(OUT, { force: true });
if (failed) { say('terminal: ' + failed + ' failed'); process.exit(1); }
say('terminal: all checks passed');
