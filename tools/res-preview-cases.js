#!/usr/bin/env node
'use strict';
/**
 * Pin `previewCommand` — the RouterOS command a resource form WOULD issue —
 * against the live `src/routeros/resources.js`.
 *
 * ── WHY THIS ONE MATTERS MORE THAN ITS SIZE ─────────────────────────────────
 *
 * The preview is rendered in the browser and can be copied out of it, so a
 * passphrase or pre-shared key that reaches it is a credential in a screenshot.
 * The live function masks every SECRET field's value with «set» and leaves the
 * `=key=` head visible.
 *
 * The mask is keyed on the FIELD, not the value. Masking by value would be the
 * version that leaks: two fields can hold the same text and only one of them is
 * a secret, so a value-keyed mask either misses the secret or hides the
 * ordinary field. The corpus carries exactly that case.
 *
 * ── AND WHY THE PORT HAD NO PREVIEW AT ALL ──────────────────────────────────
 *
 * `resource.ts` hid the button unconditionally while the live app shows it on
 * every writable form. Found by `inbound-audit`, which noticed the live answers
 * `res:preview` and ws.go does not.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/res-preview-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'res-preview-cases.json');
const CHECK = process.argv.includes('--check');

const Resources = require(path.join(SRC, 'src', 'routeros', 'resources.js'));
assert.strictEqual(typeof Resources.previewCommand, 'function', 'previewCommand is no longer exported');
assert.strictEqual(typeof Resources.validate, 'function', 'validate is no longer exported');

// Real resources, chosen for what their FIELDS are rather than for the page:
// one with a secret, one with clearable fields, one with neither.
const PICKS = ['wgPeer', 'dnsStatic', 'route'];
const byKey = {};
for (const r of (Resources.all ? Resources.all() : Object.values(Resources.RESOURCES || {}))) {
  byKey[r.key] = r;
}
for (const k of PICKS) assert.ok(byKey[k], 'resource ' + k + ' is gone from the registry');

const CASES = [];
function add(name, key, values, id) {
  const res = byKey[key];
  const validated = Resources.validate(res, values, { editing: !!id });
  if (!validated.ok) { CASES.push({ name, key, values, id: id || null, invalid: true }); return; }
  CASES.push({
    name, key, values, id: id || null,
    command: Resources.previewCommand(res, validated, id || null),
    // The validated values are recorded too: the Go side is driven from THESE,
    // so a difference in the command cannot be blamed on a different validate.
    validated: validated.values, editing: !!validated.editing,
  });
}

// Field names are the SCHEMA's, read from the registry rather than guessed —
// the first draft used RouterOS's wire names (`allowed-address`) and every case
// failed validation, which the believability assert caught before any of it
// reached a corpus.
const WG = { interface: 'wg0', publicKey: 'A'.repeat(42) + '4=', allowedAddress: '10.0.0.2/32' };
add('a create with no secret', 'wgPeer', WG);
add('an edit with no secret', 'wgPeer', WG, '*7');
add('a create WITH a secret', 'wgPeer', Object.assign({}, WG, { presharedKey: 'hunter2' }));
add('an edit with a secret', 'wgPeer', Object.assign({}, WG, { presharedKey: 'hunter2' }), '*7');
// THE CASE A VALUE-KEYED MASK GETS WRONG: an ordinary field holding exactly the
// secret's text. The comment must stay visible and only the key must be masked.
add("a NON-secret field holding the secret's value", 'wgPeer',
  Object.assign({}, WG, { comment: 'hunter2', presharedKey: 'hunter2' }), '*7');
add('an EMPTY secret is dropped, not masked', 'wgPeer',
  Object.assign({}, WG, { presharedKey: '' }), '*7');

const DNS = { name: 'a.lan', type: 'A', address: '10.0.0.1' };
add('a create with clearables left out', 'dnsStatic', DNS);
add('an EDIT with clearables left out', 'dnsStatic', DNS, '*3');
add('a value containing spaces', 'dnsStatic', Object.assign({}, DNS, { comment: 'two words' }), '*3');
add('a value containing an equals sign', 'dnsStatic', Object.assign({}, DNS, { comment: 'a=b' }), '*3');
add('a bool clearable set true', 'dnsStatic', Object.assign({}, DNS, { disabled: true }), '*3');

const RT = { dstAddress: '0.0.0.0/0', gateway: '10.0.0.1' };
add('a route create', 'route', RT);
add('a route edit', 'route', RT, '*1');
add('an id containing a quote', 'route', RT, '*"1');
add('a route with a distance', 'route', Object.assign({}, RT, { distance: 5 }), '*1');

const usable = CASES.filter((c) => !c.invalid);
assert.ok(usable.length >= 10, 'too many cases failed validation to prove anything');
// BELIEVABILITY: the mask must actually appear, and must NOT appear everywhere.
const masked = usable.filter((c) => c.command.includes('«set»'));
assert.ok(masked.length, 'no case produces a masked secret — the corpus cannot see a leak');
assert.ok(masked.length < usable.length, 'every case is masked — the corpus cannot see over-masking');
assert.ok(usable.some((c) => c.command.startsWith(byKey[c.key].menu + '/add')), 'no create case');
assert.ok(usable.some((c) => c.command.includes('/set')), 'no edit case');
// And no case may leak the secret's text next to its own key.
for (const c of usable) {
  assert.ok(!/=preshared-key=hunter2/.test(c.command), 'a case leaks the secret: ' + c.name);
}

const text = JSON.stringify({ generatedFrom: 'src/routeros/resources.js previewCommand', cases: CASES }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('res-preview-cases.json is STALE — run: node tools/res-preview-cases.js'); process.exit(1); }
  console.log(`res-preview-cases.json up to date (${usable.length} cases, ${masked.length} masked)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${usable.length} cases (${masked.length} masked) -> ${path.relative(process.cwd(), OUT)}`);
}
