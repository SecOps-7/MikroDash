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
let selection = '';
// ONE selection stub for both readers. The page stringifies it for the Ctrl-C
// check and calls removeAllRanges after a right-click copy, so it has to be an
// object that does both rather than a bare string.
(global as any).window = {
  addEventListener: () => {}, setTimeout, clearTimeout,
  getSelection: () => ({
    toString: () => selection,
    removeAllRanges: () => { selection = ''; },
  }),
};

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

// A clipboard the tests can inspect, and a contextmenu hook on the card.
const clip = { text: '', reads: 0, writes: 0, allowRead: true };
// ── A CLIPBOARD THAT SETTLES SYNCHRONOUSLY ──────────────────────────────────
//
// `check()` does not await, so an async check prints "ok" before its
// assertions have run and a failure surfaces later as an unhandled rejection -
// which is exactly what happened: the checks passed run alone and the full
// suite failed. Rather than make every check in this file async, the stub
// returns a THENABLE that calls back immediately, so the page's `.then(...)`
// runs before the check returns and the assertions are ordinary and synchronous.
//
// Node's own `navigator` global is getter-only, so it is redefined rather than
// assigned.
const settled = (v?: unknown): any => ({ then: (ok: any) => { ok(v); return settled(v); } });
const rejected = (): any => ({ then: (_ok: any, no: any) => { if (no) no(new Error('denied')); return rejected(); } });
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {
  clipboard: {
    writeText: (t: string) => { clip.writes++; clip.text = t; return settled(); },
    readText: () => { clip.reads++; return clip.allowRead ? settled(clip.text) : rejected(); },
  },
} });
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

// TAB COMPLETION IS THE DEVICE'S. Nothing here knows the RouterOS command
// tree; the browser asks, splices at the offset the device gave, and shows the
// device's own help. These pin the asking and the splicing.
check('Tab asks the device for candidates for exactly what is typed', () => {
  const inp = n.terminalInput;
  inp.value = '/ip add';
  const before = sent.length;
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(sent.length, before + 1, 'Tab asked for nothing');
  assert.deepStrictEqual(sent[sent.length - 1], ['term:complete', { line: '/ip add' }]);
});

// THE DEVICE ANSWERS ABOUT MORE THAN ONE POSITION AT ONCE, and this payload is
// copied from what a real RouterOS 7.24.4 CHR actually returned for `/ip add`.
// `address` is the word being typed; `/` is a thing that could come next, at a
// different offset. Treating them as one set makes the common prefix empty and
// the line never completes - which is exactly what shipped, because every test
// here had been written with candidates that shared one offset.
check('candidates for what comes NEXT do not block completing the current word', () => {
  const inp = n.terminalInput;
  inp.value = '/ip add';
  const before = blocks();
  handlers['term:complete']({ line: '/ip add', code: '', candidates: [
    { text: 'address', offset: 4, help: 'Address management', style: 'dir' },
    { text: '/', offset: 7, help: 'top of command hierarchy', style: 'dir' },
  ] });
  assert.strictEqual(inp.value, '/ip address',
    'a next-position candidate stopped the current word completing');
  assert.strictEqual(blocks(), before, 'it listed instead of completing');
});

check('one candidate splices at the offset the device gave', () => {
  const inp = n.terminalInput;
  inp.value = '/ip add';
  // offset 4 is where `add` starts, which is the device's answer - NOT
  // something the browser worked out from the prefix. Quoting and `[`
  // substitution make that inference wrong, which is why the offset is carried.
  handlers['term:complete']({ line: '/ip add', code: '',
    candidates: [{ text: 'address', offset: 4, help: 'Address management', style: 'dir' }] });
  assert.strictEqual(inp.value, '/ip address', 'the candidate was not spliced at its offset');
});

check('candidates that agree no further are listed at once, and only once', () => {
  const inp = n.terminalInput;
  inp.value = '/ip a';
  const cands = [
    { text: 'address', offset: 4, help: 'Address management', style: 'dir' },
    { text: 'address-list', offset: 4, help: '', style: 'dir' },
    { text: 'arp', offset: 4, help: '', style: 'dir' },
  ];
  const before = blocks();
  handlers['term:complete']({ line: '/ip a', code: '', candidates: cands });
  assert.strictEqual(inp.value, '/ip a', 'they agree no further than `a`, so nothing should move');
  // LISTED ON THE FIRST PRESS. bash makes you press twice; there is nothing to
  // be gained by hiding what the device already told us.
  assert.strictEqual(blocks(), before + 1, 'the candidates were not listed');
  const listed = String(lastBlock().textContent);
  assert.ok(/address-list/.test(listed) && /Address management/.test(listed),
    'the list lost a candidate or the device help: ' + listed);

  handlers['term:complete']({ line: '/ip a', code: '', candidates: cands });
  assert.strictEqual(blocks(), before + 1, 'pressing Tab again reprinted the same list');
});

// PRESSING TAB AGAIN COMPLETES. It used to do nothing at all: swallowed by the
// "already listed" guard, and by the server's one-at-a-time latch if it was
// quick. In a console Tab always does something.
check('a second Tab puts a candidate on the line, and further presses cycle', () => {
  const inp = n.terminalInput;
  inp.value = '/ip a';
  const cands = [
    { text: 'address', offset: 4, help: 'Address management', style: 'dir' },
    { text: 'address-list', offset: 4, help: '', style: 'dir' },
    { text: 'arp', offset: 4, help: '', style: 'dir' },
  ];
  // First press lists and leaves the line alone.
  handlers['term:complete']({ line: '/ip a', code: '', candidates: cands });
  assert.strictEqual(inp.value, '/ip a', 'the first press moved the line');

  const before = sent.length;
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address', 'the second press did not complete');
  assert.strictEqual(sent.length, before,
    'cycling asked the device again; it should use the candidates it already has');

  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address-list', 'the third press did not advance');
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip arp', 'the fourth press did not advance');
  // AND IT WRAPS, rather than sticking on the last one.
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address', 'the cycle did not wrap round');
});

// TYPING ENDS THE CYCLE, or a later Tab would replace a word that has since
// been edited.
check('typing abandons the cycle', () => {
  const inp = n.terminalInput;
  inp.value = '/ip a';
  handlers['term:complete']({ line: '/ip a', code: '', candidates: [
    { text: 'address', offset: 4, help: '', style: 'dir' },
    { text: 'arp', offset: 4, help: '', style: 'dir' },
  ] });
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address', 'the cycle did not start');

  inp.fire('keydown', { key: 'x', preventDefault: () => {} });
  inp.value = '/ip addressx';                 // what the keystroke would do
  const before = sent.length;
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip addressx',
    'Tab after typing replaced the edited word from a stale cycle');
  assert.strictEqual(sent.length, before + 1, 'it should ask the device afresh instead');

  // THE CASE THE VALUE CHECK ALONE CANNOT SEE, and the only reason the
  // keystroke reset is not redundant: an edit that lands BACK on a value the
  // cycle would recognise. Backspacing `/ip addressx` to `/ip address` leaves
  // the line looking exactly like the cycle's first candidate, so without the
  // reset the next Tab would quietly resume cycling a sequence the operator
  // had abandoned.
  inp.value = '/ip a';
  handlers['term:complete']({ line: '/ip a', code: '', candidates: [
    { text: 'address', offset: 4, help: '', style: 'dir' },
    { text: 'arp', offset: 4, help: '', style: 'dir' },
  ] });
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address', 'the cycle did not start');
  inp.fire('keydown', { key: 'Backspace', preventDefault: () => {} });
  const asked = sent.length;
  inp.fire('keydown', { key: 'Tab', preventDefault: () => {} });
  assert.strictEqual(inp.value, '/ip address',
    'Tab resumed an abandoned cycle after an edit landed back on its candidate');
  assert.strictEqual(sent.length, asked + 1, 'it should ask the device afresh');
});

check('two candidates sharing a longer prefix extend to it', () => {
  const inp = n.terminalInput;
  inp.value = '/ip ad';
  handlers['term:complete']({ line: '/ip ad', code: '', candidates: [
    { text: 'address', offset: 4, help: '', style: 'dir' },
    { text: 'address-list', offset: 4, help: '', style: 'dir' },
  ] });
  assert.strictEqual(inp.value, '/ip address', 'the common prefix was not taken');
});

// A REPLY TO AN OLDER LINE MUST BE DROPPED. Completion is a round trip; by the
// time it lands the operator may have typed on, and splicing then corrupts the
// line instead of completing it.
check('a stale reply is ignored', () => {
  const inp = n.terminalInput;
  inp.value = '/system reboot';
  handlers['term:complete']({ line: '/ip add', code: '',
    candidates: [{ text: 'address', offset: 4, help: '', style: 'dir' }] });
  assert.strictEqual(inp.value, '/system reboot',
    'a candidate computed for an older line was spliced into the current one');
});

// PASTING A BLOCK RUNS IT. /execute takes a whole script, so the block goes to
// the device intact rather than being fed in a line at a time.
check('a multi-line paste runs as one block; a single-line paste does not', () => {
  const inp = n.terminalInput;
  inp.value = '';
  let prevented = 0;
  const before = sent.length;
  fireDoc('paste', {
    target: inp,
    clipboardData: { getData: () => '/ip address print\r\n/interface print\n' },
    preventDefault: () => { prevented++; },
  });
  assert.strictEqual(sent.length, before + 1, 'the pasted block did not run');
  assert.deepStrictEqual(sent[sent.length - 1],
    ['term:run', { line: '/ip address print\n/interface print' }],
    'the block was not normalised to newlines, or the trailing newline was kept');
  assert.strictEqual(prevented, 1, 'the browser also pasted it into the input');
  handlers['term:output']({ entry: { seq: 0, at: 0, command: '', lines: [], truncated: false,
    ms: 0, code: '', message: '' }, running: false, done: true });

  const single = sent.length;
  fireDoc('paste', {
    target: inp,
    clipboardData: { getData: () => '/ip address print' },
    preventDefault: () => { prevented++; },
  });
  assert.strictEqual(sent.length, single,
    'a single-line paste ran on its own, which is a surprise rather than a terminal');
});

// A BARE ENTER MOVES THE LINE ON, which is the one thing a console always does
// and this page used to not do at all: `if (!text) return` meant pressing Enter
// on an empty line did nothing whatsoever.
check('Enter on an empty line prints a fresh prompt and asks the device nothing', () => {
  const inp = n.terminalInput;
  inp.value = '';
  const before = blocks();
  const sentBefore = sent.length;
  inp.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  assert.strictEqual(blocks(), before + 1, 'a bare Enter did not move the line on');
  assert.strictEqual(sent.length, sentBefore,
    'an empty line was sent to the device, which has nothing to run and earns an audit row');
  // Just the prompt, no output and no error.
  const b = lastBlock();
  assert.strictEqual(byTag({ children: [b] }, 'pre').length, 0, 'a blank line drew an output block');
  assert.ok(/>/.test(String(b.textContent)), 'the blank line has no prompt on it');
  assert.strictEqual(scroll.children[scroll.children.length - 1], live,
    'the blank line was drawn below the live prompt');

  // WHITESPACE ONLY IS STILL BLANK, the way a console treats it.
  inp.value = '   ';
  inp.fire('keydown', { key: 'Enter', preventDefault: () => {} });
  assert.strictEqual(blocks(), before + 2, 'a whitespace-only line did not move on');
  assert.strictEqual(sent.length, sentBefore, 'whitespace was sent to the device');
  assert.strictEqual(inp.value, '', 'the input kept its whitespace');
});

// THE PROMPT SHOWS THE MENU, which is what the operator asked for. The value
// comes off the payload rather than being tracked here: the server resolves the
// path and decides what is a menu, so the prompt cannot drift from what a
// command would actually run against.
check('the prompt shows the menu the session is in', () => {
  handlers['term:scrollback']({ routerId: 'r1', entries: [], mayRun: true, why: '',
    identity: 'CHR Test', user: 'claude', trimmed: false, running: false, cwd: '' });
  assert.strictEqual(String(n.terminalPrompt.textContent), '[claude@CHR Test] > ',
    'the root prompt changed shape');

  const before = blocks();
  handlers['term:output']({ cwd: '/ip/address', running: false, done: true,
    entry: { seq: 5, at: 0, command: '/ip address', lines: [], truncated: false,
             ms: 0, code: '', message: '', cwd: '' } });
  assert.strictEqual(String(n.terminalPrompt.textContent), '[claude@CHR Test] /ip/address> ',
    'the prompt does not say which menu you are in');

  // AND THE LINE REMEMBERS WHERE IT WAS TYPED. `/ip address` is typed at the
  // ROOT and lands somewhere else; echoing it under the new prompt rewrites
  // history, and does it again to the whole pane every time you move.
  assert.strictEqual(blocks(), before + 1, 'the line did not draw');
  const echoed = String(lastBlock().textContent);
  assert.ok(/\[claude@CHR Test\] > \/ip address/.test(echoed),
    'the echo used the menu it landed in rather than the one it was typed at: ' + echoed);

  handlers['term:output']({ cwd: '', running: false, done: true,
    entry: { seq: 6, at: 0, command: '..', lines: [], truncated: false,
             ms: 0, code: '', message: '' } });
  assert.strictEqual(String(n.terminalPrompt.textContent), '[claude@CHR Test] > ',
    'climbing out left the menu in the prompt');
});

// PASTE REACHES THE TERMINAL WHEREVER FOCUS IS. It used to be bound to the
// input alone, so Ctrl-V did nothing unless focus happened to be sitting in it
// - which it is not after a right-click, or after clicking anything but the
// scrollback. Reported as "pasting multiple lines does not execute", and that
// was the whole of it: the event never reached the handler.
check('a paste with focus elsewhere on the page still runs the block', () => {
  const inp = n.terminalInput;
  inp.value = '';
  const before = sent.length;
  let prevented = 0;
  fireDoc('paste', {
    target: n.terminalCard,                    // NOT the input
    clipboardData: { getData: () => '/tool mac-server set allowed-interface-list=LAN\n/tool bandwidth-server set enabled=no' },
    preventDefault: () => { prevented++; },
  });
  assert.strictEqual(sent.length, before + 1, 'a paste from outside the input did nothing');
  assert.deepStrictEqual(sent[sent.length - 1], ['term:run', { line:
    '/tool mac-server set allowed-interface-list=LAN\n/tool bandwidth-server set enabled=no' }]);
  assert.strictEqual(prevented, 1, 'the browser was left to paste it as well');
  handlers['term:output']({ entry: { seq: 0, at: 0, command: '', lines: [], truncated: false,
    ms: 0, code: '', message: '' }, running: false, done: true, cwd: '' });
});

check('another text field on the page keeps its own paste', () => {
  const before = sent.length;
  let prevented = 0;
  fireDoc('paste', {
    target: { tagName: 'INPUT', id: 'somethingElse' },
    clipboardData: { getData: () => 'a\nb' },
    preventDefault: () => { prevented++; },
  });
  assert.strictEqual(sent.length, before, 'the terminal stole another field\'s paste');
  assert.strictEqual(prevented, 0, 'and cancelled it');
});

// TERMINAL MOUSE BUTTONS: select then right-click copies, right-click with
// nothing selected pastes. PuTTY's convention, and what was asked for.
check('right-click copies the selection, then pastes when there is none', () => {
  const card = n.terminalCard;
  selection = 'Flags: D - DYNAMIC';
  let prevented = 0;
  card.fire('contextmenu', { preventDefault: () => { prevented++; } });
  assert.strictEqual(clip.writes, 1, 'the selection was not copied');
  assert.strictEqual(clip.text, 'Flags: D - DYNAMIC', 'the wrong text was copied');
  assert.strictEqual(prevented, 1, 'the browser menu was left to open over it');
  // COLLAPSED AFTERWARDS, so the next right-click pastes rather than copying
  // the same thing again - the console's own behaviour.
  assert.strictEqual(selection, '', 'the selection survived the copy');

  const inp = n.terminalInput;
  inp.value = '';
  const readsBefore = clip.reads;
  card.fire('contextmenu', { preventDefault: () => { prevented++; } });
  assert.strictEqual(clip.reads, readsBefore + 1, 'right-click with no selection did not paste');
  assert.strictEqual(inp.value, 'Flags: D - DYNAMIC', 'the clipboard did not reach the line');
  inp.value = '';
});

// BOTH HALVES MUST WORK OR PEOPLE HAVE NO WAY TO MOVE TEXT, since the browser's
// own menu is suppressed inside the card. A refused clipboard says which keys
// still do it rather than failing silently.
check('a refused clipboard read says so instead of doing nothing', () => {
  clip.allowRead = false;
  selection = '';
  n.terminalCard.fire('contextmenu', { preventDefault: () => {} });
  assert.ok(/Ctrl-V/.test(String(n.terminalStatus.textContent)),
    'a refused paste left no hint at all: ' + n.terminalStatus.textContent);
  clip.allowRead = true;
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

check('switching device clears the pane, the history, the menu and the prompt', () => {
  // Walk into a menu first, so the reset has something to undo.
  handlers['term:output']({ cwd: '/ip/address', running: false, done: true,
    entry: { seq: 77, at: 0, command: '/ip address', lines: [], truncated: false,
             ms: 0, code: '', message: '' } });
  assert.ok(/ip\/address/.test(String(n.terminalPrompt.textContent)),
    'the menu never showed, so the reset below would prove nothing');

  handlers['router:switched']({ activeId: 'r2' });
  assert.ok(!/ip\/address/.test(String(n.terminalPrompt.textContent)),
    'one device\'s menu survived into another\'s prompt');
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
