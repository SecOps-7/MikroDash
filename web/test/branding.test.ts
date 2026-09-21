/**
 * Branding (issue #131): the install's own name and icon, as the browser applies
 * them.
 *
 * Five properties:
 *
 *   1. the default wordmark is still "Mikro" and an accented "Dash" span;
 *   2. a custom name is TEXT: a name that looks like markup becomes one text
 *      node, never elements;
 *   3. the sidebar and login icons, the tab title, the favicon and the login
 *      page's name follow the branding;
 *   4. only the two icon URLs the server hands out are ever used;
 *   5. the upload limits the card checks are the ones internal/branding enforces,
 *      read from the Go source so the two cannot drift apart.
 */

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

const B = bundle('web/src/branding.ts', 'port-branding.cjs');
const S = bundle('web/src/pages/branding-settings.ts', 'port-branding-settings.cjs');

// A small DOM: elements with children, text, a style and the attributes used.
function node(tag) {
  return {
    nodeType: 1, tagName: String(tag).toUpperCase(), children: [], src: '', alt: '', href: '', _text: '',
    style: { props: {}, setProperty(k, v) { this.props[k] = v; }, removeProperty(k) { delete this.props[k]; } },
    appendChild(c) { this.children.push(c); return c; },
    get textContent() { return this.children.length ? this.children.map((c) => c.textContent).join('') : this._text; },
    set textContent(v) { this.children = []; this._text = String(v); },
  };
}
function textNode(s) { return { nodeType: 3, textContent: s }; }

function page() {
  const top = node('h1');
  top.appendChild(textNode('Mikro'));
  const dash = node('span');
  dash.textContent = 'Dash';
  top.appendChild(dash);
  const navImg = node('img');
  const loginImg = node('img');
  const loginName = node('span');
  const favicon = node('link');
  global.document = {
    title: 'MikroDash',
    getElementById: (id) => (id === 'topbarLogo' ? top : null),
    querySelector: (sel) => (sel === '.login-brand-name' ? loginName : sel === 'link[rel="icon"]' ? favicon : null),
    querySelectorAll: (sel) => (sel === '.nav-logo img, .login-brand img' ? [navImg, loginImg] : []),
    createElement: node,
    createTextNode: textNode,
  };
  return { top, navImg, loginImg, loginName, favicon };
}

// ── 1. the default wordmark ────────────────────────────────────────────────
{
  const p = page();
  B.applyBranding(B.normaliseBranding({ name: '', font: '', icon: '/logo.png' }));
  assert.strictEqual(p.top.children.length, 2, 'the default wordmark is not two parts');
  assert.strictEqual(p.top.children[0].nodeType, 3);
  assert.strictEqual(p.top.children[0].textContent, 'Mikro');
  assert.strictEqual(p.top.children[1].tagName, 'SPAN', 'the accented half is not a span');
  assert.strictEqual(p.top.children[1].textContent, 'Dash');
  assert.strictEqual(global.document.title, 'MikroDash');
  assert.strictEqual(p.navImg.src, '/logo.png');
  assert.ok(!('font-family' in p.top.style.props), 'the default wordmark was given a font of its own');
  say('ok  the default wordmark is Mikro + an accented Dash');
}

// ── 2 and 3. a custom name, font and icon ──────────────────────────────────
{
  const p = page();
  const name = '<img src=x onerror=alert(1)>';
  B.applyBranding(B.normaliseBranding({ name, font: 'inter', icon: '/brand/icon.png?v=42' }), ' — Sign In');
  assert.strictEqual(p.top.children.length, 1, 'a custom name is not a single node');
  assert.strictEqual(p.top.children[0].nodeType, 3, 'a name that looks like markup became an element');
  assert.strictEqual(p.top.textContent, name);
  assert.ok(B.fontFamily('inter'), 'the inter font has no family');
  assert.strictEqual(p.top.style.props['font-family'], B.fontFamily('inter'));
  for (const img of [p.navImg, p.loginImg]) {
    assert.strictEqual(img.src, '/brand/icon.png?v=42', 'an icon does not follow the branding');
    assert.strictEqual(img.alt, name);
  }
  assert.strictEqual(p.favicon.href, '/brand/icon.png?v=42', 'the tab icon does not follow the branding');
  assert.strictEqual(p.loginName.textContent, name);
  assert.strictEqual(global.document.title, name + ' — Sign In');
  say('ok  a custom name is text, and the icon, favicon, title and login name follow it');
}

{
  const p = page();
  B.applyBranding(B.normaliseBranding({ name: 'Acme', font: 'not-a-font', icon: '/logo.png' }));
  assert.ok(!('font-family' in p.top.style.props), 'an unknown font id was applied');
  say('ok  an unknown font falls back to the wordmark font');
}

// ── 4. only the server's icon URLs ─────────────────────────────────────────
for (const icon of ['javascript:alert(1)', 'https://example.com/x.png', '/brand/icon.png', '//evil/logo.png', 42]) {
  const b = B.normaliseBranding({ name: 'Acme', icon });
  assert.strictEqual(b.icon, '/logo.png', 'the icon URL ' + String(icon) + ' was accepted');
  assert.strictEqual(b.customIcon, false);
}
assert.deepStrictEqual(B.normaliseBranding(null), B.DEFAULT_BRANDING);
assert.strictEqual(B.normaliseBranding({ name: 7 }).displayName, 'MikroDash');
say('ok  only /logo.png and the versioned stored icon are used');

// ── 5. the upload limits match internal/branding ───────────────────────────
{
  const go = fs.readFileSync(path.join(ROOT, 'internal', 'branding', 'branding.go'), 'utf8');
  const num = (re) => {
    const m = re.exec(go);
    assert.ok(m, 'could not read ' + re + ' from internal/branding/branding.go');
    return m[1];
  };
  assert.strictEqual(S.NAME_MAX, Number(num(/MaxNameRunes = (\d+)/)), 'the name limit differs from the server');
  assert.strictEqual(S.ICON_MIN_PX, Number(num(/MinIconPx = (\d+)/)), 'the smallest icon differs from the server');
  assert.strictEqual(S.ICON_MAX_PX, Number(num(/MaxIconPx = (\d+)/)), 'the largest icon differs from the server');
  assert.strictEqual(S.ICON_MAX_BYTES, Number(num(/MaxIconBytes = (\d+) << 10/)) * 1024,
    'the icon byte limit differs from the server');

  assert.ok(S.iconFileProblem({ type: 'image/svg+xml', size: 100 }), 'an SVG passed the file check');
  assert.ok(S.iconFileProblem({ type: 'image/png', size: S.ICON_MAX_BYTES + 1 }), 'an over-size file passed');
  assert.strictEqual(S.iconFileProblem({ type: 'image/jpeg', size: 1000 }), '');
  // The Name Font list: the wordmark's own font is the Default option, not a
  // second entry, and the Appearance card's "(Default)" (the interface font's)
  // is not carried over.
  const wm = S.wordmarkFontOptions(new Map([['oxanium', 'Oxanium (Default)'], ['syne', 'Syne']]));
  assert.ok(!wm.some((o) => o.id === S.WORDMARK_FONT), 'the wordmark font is listed beside its own Default option');
  assert.ok(!wm.some((o) => /Default/.test(o.label)), 'a Name Font choice claims to be the default');
  assert.deepStrictEqual(wm.find((o) => o.id === 'oxanium'), { id: 'oxanium', label: 'Oxanium' });
  assert.strictEqual(S.iconSizeProblem(64, 64), '');
  assert.strictEqual(S.iconSizeProblem(512, 512), '');
  assert.ok(S.iconSizeProblem(63, 63), 'a 63px icon passed');
  assert.ok(S.iconSizeProblem(513, 513), 'a 513px icon passed');
  assert.ok(S.iconSizeProblem(64, 65), 'a non-square icon passed');
  say('ok  the card checks the same limits the server enforces');
}
