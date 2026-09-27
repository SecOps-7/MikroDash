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

const doc = makeDoc(['terminalCard', 'terminalScroll', 'terminalInput', 'terminalStop',
  'terminalClear', 'terminalPrompt', 'terminalStatus', 'terminalLive']);
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
// The live prompt line is a real node, because the page MOVES it to the end of
// the scroller after every write and that move is the thing under test.
const live = node('div');
live.classList = { add: () => {}, remove: () => {}, toggle: () => {}, contains: () => false };
n.terminalLive = live;
scroll.appendChild(live);
// The shim's nodes have no `focus`; the page focuses its input when the pane
// arms, which is right in a browser. Added here rather than dropped from the
// page, and rather than widened in the shared shim for one caller.
n.terminalInput.focus = () => { focused++; };
let focused = 0;

(global as any).document = doc;
(global as any).window = { addEventListener: () => {}, setTimeout, clearTimeout, getSelection: () => selection };
let selection = '';

// The page listens on `document` for the type-anywhere path, so the harness
// needs a way to fire there and an `activeElement` for it to compare against.
const docHandlers: Record<string, any[]> = {};
const origAdd = doc.addEventListener?.bind(doc);
doc.activeElement = null;
doc.addEventListener = (ev: string, fn: any) => {
  (docHandlers[ev] = docHandlers[ev] || []).push(fn);
  if (origAdd) origAdd(ev, fn);
};
function fireDoc(ev: string, e: any): void {
  for (const fn of docHandlers[ev] || []) fn({ preventDefault() {}, ...e });
}

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
/** Output blocks only: the live prompt line lives in the scroller too. */
const blocks = () => scroll.children.filter((c: any) => c !== live).length;
const lastBlock = () => {
  const out = scroll.children.filter((c: any) => c !== live);
  return out[out.length - 1];
};

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

check('caps enable the input, and there is no Run button to enable', () => {
  assert.strictEqual(n.terminalInput.disabled, false, 'the input is still disabled after mayRun');
  assert.strictEqual(n.terminalStop.hidden, true, 'Stop is offered when nothing is running');
});

check('Enter sends the line and latches the input', () => {
  n.terminalInput.value = '/ip address print';
  n.terminalInput.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  assert.deepStrictEqual(sent[sent.length - 1], ['term:run', { line: '/ip address print' }],
    'the line was not sent');
  assert.strictEqual(n.terminalInput.disabled, true,
    'a second line can be typed while the first is running');
  assert.strictEqual(n.terminalStop.hidden, false, 'Stop is not offered while a line is running');
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
  assert.strictEqual(n.terminalStop.hidden, true, 'Stop is still offered after the answer arrived');
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
  const b = lastBlock();
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

// THE PROMPT STAYS AT THE END. This is the whole of what makes the page read as
// a terminal rather than a log with a search box: output is added ABOVE the
// line being typed on. `appendChild` moves a node already in the document, and
// a refactor that built the block list some other way would silently leave the
// caret stranded halfway up the pane.
check('the live prompt is always the last thing in the pane', () => {
  assert.strictEqual(scroll.children[scroll.children.length - 1], live,
    'output was appended below the prompt, so the caret is no longer at the end');
  handlers['term:output']({
    entry: { seq: 7, at: 0, command: '/x', lines: ['out'], truncated: false, ms: 1, code: '', message: '' },
    running: false, done: true,
  });
  assert.strictEqual(scroll.children[scroll.children.length - 1], live,
    'a new answer was drawn below the prompt');
});

// THE OPENING BANNER is just an entry with no command, so it draws as output
// with no echo line above it - which is what a login banner looks like.
check('the banner draws as output with no command echoed above it', () => {
  const banner = { seq: 99, at: 0, command: '', lines: ['', '  MMM      MMM       KKK', '',
    '  MikroTik RouterOS 7.24.4  (c) 1999-2026'], truncated: false, ms: 0, code: '', message: '' };
  handlers['term:scrollback']({ routerId: 'r1', entries: [banner], mayRun: true, why: '',
    identity: 'CHR Test', user: 'admin', trimmed: false, running: false });
  const b = lastBlock();
  assert.strictEqual(b.children.filter((c: any) => c.className === 'term-echo').length, 0,
    'the banner drew a prompt line above itself');
  assert.ok(/MikroTik RouterOS/.test(String(b.textContent)), 'the banner text was lost');
  assert.strictEqual(scroll.children[scroll.children.length - 1], live,
    'the banner was drawn below the prompt');
});

check('Clear tells the server as well as the screen', () => {
  const before = sent.length;
  n.terminalClear.fire('click', {});
  assert.strictEqual(blocks(), 0, 'the pane was not emptied');
  assert.strictEqual(scroll.children[scroll.children.length - 1], live,
    'clearing removed the prompt as well as the output');
  assert.deepStrictEqual(sent[sent.length - 1], ['term:clear', {}],
    'the server still holds the pane, so walking away and back would undo the Clear');
  assert.ok(before >= 0);
});

// TYPING ANYWHERE ON THE PAGE TYPES AT THE PROMPT.
//
// Reported as "hitting enter does nothing", and that is exactly how it looks:
// focus lands on the card header or the page background, nothing brings it
// back, and every keystroke goes nowhere while the page works perfectly.
check('a keystroke with focus elsewhere reaches the prompt, and is kept', () => {
  const inp = n.terminalInput;
  inp.value = '';
  doc.activeElement = n.terminalCard;          // focus is NOT in the input
  fireDoc('keydown', { key: '/', target: n.terminalCard });
  fireDoc('keydown', { key: 'i', target: n.terminalCard });
  assert.strictEqual(inp.value, '/i',
    'the keystrokes were swallowed rather than kept; focusing alone loses the first character');
});

check('Enter from outside the input runs the line', () => {
  const inp = n.terminalInput;
  inp.value = '/ip address print';
  doc.activeElement = n.terminalCard;
  const before = sent.length;
  fireDoc('keydown', { key: 'Enter', target: n.terminalCard });
  assert.strictEqual(sent.length, before + 1, 'Enter outside the input did nothing');
  assert.deepStrictEqual(sent[sent.length - 1], ['term:run', { line: '/ip address print' }]);
  handlers['term:output']({ entry: { seq: 0, at: 0, command: '', lines: [], truncated: false,
    ms: 0, code: '', message: '' }, running: false, done: true });
});

// WHAT IT MUST NOT TAKE. Each of these would break something else on the page.
check('it does not steal from other controls, or the browser', () => {
  const inp = n.terminalInput;
  inp.value = '';
  doc.activeElement = n.terminalCard;

  // Another text field on the page owns its own typing.
  fireDoc('keydown', { key: 'x', target: { tagName: 'INPUT' } });
  // Browser and OS keys pass through untouched.
  fireDoc('keydown', { key: 'r', ctrlKey: true, target: n.terminalCard });
  fireDoc('keydown', { key: 'c', metaKey: true, target: n.terminalCard });
  // TAB ABOVE ALL: stealing it strands a keyboard user on this page.
  fireDoc('keydown', { key: 'Tab', target: n.terminalCard });
  fireDoc('keydown', { key: 'Escape', target: n.terminalCard });
  fireDoc('keydown', { key: 'F5', target: n.terminalCard });

  assert.strictEqual(inp.value, '',
    'the page took a key that belongs to another control or to the browser: ' + inp.value);

  // ENTER ON A BUTTON OR A LINK MUST STILL ACTIVATE IT, and that cannot be
  // seen in `inp.value` - Enter runs the line rather than typing a character.
  // Asserting on the value alone let a dropped BUTTON guard survive a mutation
  // sweep, so this watches what Enter actually does.
  inp.value = '/should-not-run';
  const before = sent.length;
  fireDoc('keydown', { key: 'Enter', target: { tagName: 'BUTTON' } });
  fireDoc('keydown', { key: 'Enter', target: { tagName: 'A' } });
  fireDoc('keydown', { key: ' ', target: { tagName: 'BUTTON' } });
  assert.strictEqual(sent.length, before,
    'Enter or Space on a button ran a terminal line instead of activating the button');
  assert.strictEqual(inp.value, '/should-not-run', 'Space on a button typed into the prompt');
  inp.value = '';
});

check('a refusal code becomes a sentence, and no device text', () => {
  handlers['term:output']({
    entry: { seq: 0, at: 0, command: '', lines: [], truncated: false, ms: 0,
             code: 'denied', message: '' },
    running: false, done: true,
  });
  // Scoped to the LAST block: earlier checks left their own output in the pane,
  // and counting across all of it would assert about them instead.
  const last = lastBlock();
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
  assert.strictEqual(n.terminalStop.hidden, true, 'Stop is offered to somebody who may not run');
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
