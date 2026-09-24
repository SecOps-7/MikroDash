// The Markdown subset, and the property the whole module exists for: model
// output never becomes markup.
//
// ── WHY THIS IS WORTH A TEST FILE OF ITS OWN ────────────────────────────────
//
// Issue #98 proposed rendering replies as plain text precisely to avoid a parser
// and a sanitiser. Rendering as nodes gets the tables and code blocks an
// assistant actually emits without either dependency - but only while every
// branch ends in `textContent`. One branch that built a string and assigned it
// would undo the argument, silently, and look like a tidy refactor.
//
// ── A LOCAL DOCUMENT, NOT dom-shim ──────────────────────────────────────────
//
// The shared shim is an ID REGISTRY: `createElement` is `mk('')` and discards
// the tag, it has no `createDocumentFragment` at all, and its `querySelectorAll`
// answers selectors parsed out of an `innerHTML` string - which is exactly what
// this renderer never sets. Asserting "a <pre> was produced" against a node with
// no tag would be comparing the shim's approximation, which its own rule 8 warns
// against.
//
// So this builds a small document, as `branding.test.ts` and nine other files
// already do for node-building code. The subject here is node construction
// rather than page wiring, which is the case that pattern exists for.

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function bundle(entry: string, name: string) {
  const out = path.join(ROOT, 'web', 'dist', '_compare', name);
  fs.mkdirSync(path.dirname(out), { recursive: true });
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [path.join(ROOT, entry), '--bundle', '--format=cjs', '--platform=node',
     '--outfile=' + out, '--log-level=warning'],
    { stdio: 'inherit' });
  return require(out);
}

// ── THE DOCUMENT ────────────────────────────────────────────────────────────
//
// Elements keep their TAG, which is the whole point: the shim's do not, and
// every assertion below is about which element was created.
function node(tag: string): any {
  return {
    nodeType: 1,
    tagName: String(tag).toUpperCase(),
    className: '',
    children: [] as any[],
    attrs: {} as Record<string, string>,
    style: {} as Record<string, string>,
    setAttribute(k: string, v: string) { this.attrs[k] = String(v); },
    getAttribute(k: string) { return this.attrs[k] ?? null; },
    appendChild(c: any) { this.children.push(c); return c; },
    _text: '',
    get textContent(): string {
      return this.children.length
        ? this.children.map((c: any) => c.textContent).join('')
        : this._text;
    },
    set textContent(v: string) { this.children = []; this._text = String(v); },
  };
}
function textNode(s: string): any { return { nodeType: 3, textContent: String(s) }; }
// A fragment is a node with no tag: appending it appends IT, and its textContent
// still concatenates, which is all the renderer relies on.
function fragment(): any { const f = node(''); f.nodeType = 11; return f; }

(global as any).document = {
  createElement: (tag: string) => node(tag),
  createTextNode: (s: string) => textNode(s),
  createDocumentFragment: () => fragment(),
};

const { renderMarkdown, parseBlocks, tokenizeRouterOS, looksLikeRouterOS } =
  bundle('web/src/markdown.ts', 'port-markdown.cjs');

/** Every descendant with this tag, walked rather than queried. */
function byTag(root: any, tag: string): any[] {
  const want = tag.toUpperCase();
  const out: any[] = [];
  const walk = (n: any): void => {
    if (n.nodeType === 1 && n.tagName === want) out.push(n);
    for (const c of n.children || []) walk(c);
  };
  walk(root);
  return out;
}

let failed = 0;
function check(what: string, fn: () => void): void {
  try { fn(); say('  ok   ' + what); } catch (e) {
    failed++;
    say('  FAIL ' + what + '\n       ' + (e as Error).message);
  }
}

say('markdown: a subset rendered as nodes, never as markup');

check('a fenced RouterOS command survives verbatim', () => {
  const out = renderMarkdown('```\n/ip firewall filter add chain=input action=drop\n```');
  assert.strictEqual(byTag(out, 'pre').length, 1, 'no <pre> was produced');
  assert.strictEqual(byTag(out, 'code')[0].textContent,
    '/ip firewall filter add chain=input action=drop',
    'the command was altered; inline parsing must not touch fenced code');
});

check('an unclosed fence is still a code block', () => {
  // A truncated reply ends mid-command. Reflowing the remainder as prose would
  // turn it into something that reads like a sentence and is not.
  const out = renderMarkdown('```\n/ip address add address=198.51.100.1/24');
  assert.strictEqual(byTag(out, 'pre').length, 1);
});

check('a table becomes a table', () => {
  const out = renderMarkdown(
    '| Interface | State |\n| --- | --- |\n| ether1 | up |\n| ether4 | down |');
  assert.strictEqual(byTag(out, 'table').length, 1, 'no <table> was produced');
  assert.strictEqual(byTag(out, 'th').length, 2);
  assert.strictEqual(byTag(out, 'td').length, 4);
  assert.ok(out.textContent.includes('ether4'), 'a cell value was lost');
});

check('a line with pipes is not a table without its separator', () => {
  const out = renderMarkdown('use | to pipe output | like this');
  assert.strictEqual(byTag(out, 'table').length, 0,
    'prose containing pipes was read as a table');
});

check('lists, headings and emphasis render as their elements', () => {
  const out = renderMarkdown('## Findings\n\n- **ether4** is down\n- `ether1` is up');
  assert.strictEqual(byTag(out, 'ul').length, 1);
  assert.strictEqual(byTag(out, 'li').length, 2);
  assert.strictEqual(byTag(out, 'strong').length, 1);
  assert.strictEqual(byTag(out, 'code').length, 1);
  // Floored so a model cannot out-shout the page's own headings.
  assert.strictEqual(byTag(out, 'h1').length, 0, 'an h1 reached the page');
  assert.strictEqual(byTag(out, 'h5').length, 1, '## should render as h5');
});

check('an ordered list is an ol', () => {
  const out = renderMarkdown('1. first\n2. second');
  assert.strictEqual(byTag(out, 'ol').length, 1);
  assert.strictEqual(byTag(out, 'li').length, 2);
});

check('HTML in model output renders as characters, not as elements', () => {
  // THE ONE THAT MATTERS. The text arrives from a model that has been reading
  // device-supplied names, so it is untrusted twice over.
  const out = renderMarkdown('<img src=x onerror="alert(1)"> and <script>alert(2)</script>');
  assert.strictEqual(byTag(out, 'img').length, 0, 'an <img> element was created');
  assert.strictEqual(byTag(out, 'script').length, 0, 'a <script> element was created');
  assert.ok(out.textContent.includes('<img src=x onerror='),
    'the markup should be visible as text, so the reader can see what was said');
});

check('HTML inside a fenced block is also inert', () => {
  const out = renderMarkdown('```\n<script>alert(1)</script>\n```');
  assert.strictEqual(byTag(out, 'script').length, 0);
  assert.ok(out.textContent.includes('<script>'));
});

check('a link is shown as text rather than made clickable', () => {
  // A model can be talked into emitting a link by the router text it is reading.
  // An anchor whose href came from that chain is a phishing target rendered by
  // the operator's own dashboard.
  const out = renderMarkdown('See [the docs](https://example.invalid/x) for more');
  assert.strictEqual(byTag(out, 'a').length, 0, 'an anchor was created');
  // THE WHOLE TEXT, EXACTLY: the URL is readable so the operator can judge it,
  // and it is the markdown as written, not something rebuilt around it. (An
  // `includes` here read to code scanning as a URL check, #160, and was the
  // weaker assertion anyway.)
  assert.strictEqual(out.textContent, 'See [the docs](https://example.invalid/x) for more');
});

check('the code block records its language as data, not as a class', () => {
  // A class built from model output would let it choose a selector the
  // stylesheet defines.
  const out = renderMarkdown('```routeros\n/system identity print\n```');
  const code = byTag(out, 'code')[0];
  assert.strictEqual(code.getAttribute('data-lang'), 'routeros');
  assert.ok(!String(code.className).includes('routeros'),
    'the language reached a class name');
});

check('the parser terminates on input that starts no block', () => {
  // The paragraph branch consumes lines; a line matching no rule must still
  // advance, or the loop never ends and the page hangs on one reply.
  for (const src of ['', '   ', '\n\n\n', '|', '#', '```', '- ', '|||']) {
    assert.ok(Array.isArray(parseBlocks(src)),
      'parseBlocks did not return for ' + JSON.stringify(src));
  }
});

if (failed) {
  say('markdown: ' + failed + ' failing');
  process.exitCode = 1;
}

// ── ROUTEROS HIGHLIGHTING ───────────────────────────────────────────────────
//
// The colouring must not change the text. An operator copies a command out of
// the transcript and runs it, so a highlighter that inserts, drops or reorders
// a single character is worse than no highlighter at all.

/** The text of every span carrying one token class, in order. */
function tokens(root: any, cls: string): string[] {
  return byTag(root, 'span').filter((s: any) => s.className === cls)
    .map((s: any) => s.textContent);
}

check('highlighting is a strict partition: nothing is added or lost', () => {
  for (const src of [
    '/ip/dns/static/add name=server.lan address=10.0.0.1',
    '/ip firewall filter add chain=input action=drop',
    '# a note\n/interface set comment="on the roof"',
    '   leading spaces and  odd   gaps   ',
    '=====',
    '',
  ]) {
    const joined = tokenizeRouterOS(src).map((t: any) => t.text).join('');
    assert.strictEqual(joined, src,
      'the tokens do not reproduce the source, so a copied command would differ');
  }
});

check('a RouterOS command is coloured by part and still copies verbatim', () => {
  const src = '/ip/dns/static/add name=server.lan address=10.0.0.1';
  const out = renderMarkdown('```\n' + src + '\n```');
  const code = byTag(out, 'code')[0];
  // THE ASSERTION THAT MATTERS MOST, repeated here against the tokenised path.
  assert.strictEqual(code.textContent, src, 'highlighting altered the command');
  assert.strictEqual(String(code.className), 'md-ros');
  assert.deepStrictEqual(tokens(out, 'ros-path'), ['/ip/dns/static/add']);
  assert.deepStrictEqual(tokens(out, 'ros-key'), ['name', 'address']);
  assert.deepStrictEqual(tokens(out, 'ros-value'), ['server.lan']);
  assert.deepStrictEqual(tokens(out, 'ros-num'), ['10.0.0.1'],
    'an address should not be coloured as an ordinary word');
});

check('the space-separated form colours its verb and its pairs', () => {
  const out = renderMarkdown('```\n/ip firewall filter add chain=input action=drop\n```');
  assert.deepStrictEqual(tokens(out, 'ros-path'), ['/ip']);
  assert.deepStrictEqual(tokens(out, 'ros-verb'), ['add']);
  assert.deepStrictEqual(tokens(out, 'ros-key'), ['chain', 'action']);
  assert.deepStrictEqual(tokens(out, 'ros-value'), ['input', 'drop']);
});

check('comments and quoted values are their own colours', () => {
  const out = renderMarkdown('```\n# a note\n/interface set comment="on the roof"\n```');
  assert.deepStrictEqual(tokens(out, 'ros-comment'), ['# a note']);
  assert.deepStrictEqual(tokens(out, 'ros-string'), ['"on the roof"'],
    'a quoted value must read as a string, not as an ordinary value');
});

check('a command introduced by a comment is still RouterOS', () => {
  // A model explains before it commands. Judging only the first line left every
  // commented example rendering as plain text, which is most of them.
  assert.strictEqual(
    looksLikeRouterOS('# add a static entry\n/ip/dns/static/add name=x', ''), true);
  // BOTH DIRECTIONS: a block that is only prose after its comment is not a
  // command, and must not be coloured as one.
  assert.strictEqual(looksLikeRouterOS('# just a note\nnot a command', ''), false);
  assert.strictEqual(looksLikeRouterOS('# only a comment', ''), false);
});

check('a named language is believed, even when it looks like RouterOS', () => {
  // A block the model labelled `json` is not run through a RouterOS
  // highlighter because it happens to start with a slash.
  assert.strictEqual(looksLikeRouterOS('/ip/dns/print', 'json'), false);
  assert.strictEqual(looksLikeRouterOS('/ip/dns/print', 'routeros'), true);
  assert.strictEqual(looksLikeRouterOS('/ip/dns/print', ''), true);
  assert.strictEqual(looksLikeRouterOS('{"a":1}', ''), false);
});

check('a non-RouterOS block is left as plain text', () => {
  const out = renderMarkdown('```json\n{"name":"server.lan"}\n```');
  const code = byTag(out, 'code')[0];
  assert.strictEqual(code.textContent, '{"name":"server.lan"}');
  assert.ok(!String(code.className).includes('md-ros'),
    'a JSON block was run through the RouterOS highlighter');
  assert.strictEqual(byTag(out, 'span').length, 0,
    'plain code should produce no token spans at all');
});
