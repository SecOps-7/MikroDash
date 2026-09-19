/**
 * THE PPP SECRETS TABLE (issue #125).
 *
 * ── WHAT THIS PINS, AND WHY EACH ONE ────────────────────────────────────────
 *
 * 1. NO PASSWORD REACHES THE MARKUP. The whole design of this feature is that
 *    the collector never asks the router for `password`, so nothing on this side
 *    can render one. `internal/verify` checks that at the proplist; this checks
 *    it at the far end, on the actual HTML the page writes. Two checks at the
 *    two ends of the same pipe, because this is the property the feature is
 *    allowed to exist on.
 *
 * 2. THREE STATES, NOT TWO. "disabled" and "offline" are different facts and an
 *    operator acts on them differently. The pill was the easiest thing in this
 *    page to write as a boolean, and a boolean would have been wrong.
 *
 * 3. THE CONNECTED JOIN IS SERVER-SIDE. The page renders `connected` and must
 *    not re-derive it: the collector joins against /ppp/active at emit time
 *    precisely so the pill is as live as the sessions table. A page that
 *    recomputed it from `sessions` would silently disagree.
 *
 * 4. ROWS ARE ADDRESSABLE. A row with no `data-id` renders fine and does
 *    nothing when clicked — the failure mode is invisible until somebody tries
 *    to edit a subscriber.
 *
 * 5. THE BADGE COUNTS ACCOUNTS, NOT SEARCH HITS.
 *
 * The real page module is bundled and driven; nothing here reimplements it.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc, RES_MODAL_IDS } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.ppp-entry.ts');
fs.writeFileSync(ENTRY, "export { initPppPage } from '../web/src/pages/ppp.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ppp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'pppTable', 'pppThead', 'pppBadge', 'pppSearch',
  'pppSumCount', 'pppSumServices', 'pppSumRx', 'pppSumTx',
  'pppSecretTable', 'pppSecretThead', 'pppSecretBadge', 'pppSecretSearch',
  'pppProfileTable', 'pppProfileBadge', 'pppServerTable',
  'pppCfgTabBar', 'pppAddSlot',
  'ppptab-secrets', 'ppptab-profiles', 'ppptab-servers',
];

// A MARKER THE PAGE COULD ONLY RENDER IF SOMETHING WENT WRONG.
const NEVER = 'hunter2-should-never-render';

function payload(secrets, sessions) {
  return {
    ts: 1, pollMs: 5000, available: true,
    sessions: sessions || [],
    secrets: secrets || [],
    profiles: [{ id: '*1', name: 'for-pppoe', localAddress: '10.0.0.1',
                 remoteAddress: 'pppoe-pool', rateLimit: '10M/10M', onlyOne: 'default',
                 encryption: 'default' }],
    servers: [],
    byService: {}, totalRxRate: null, totalTxRate: null,
  };
}

function secret(over) {
  return Object.assign({
    id: '*A', name: 'alice', service: 'pppoe', profile: 'for-pppoe',
    localAddress: '10.0.0.1', remoteAddress: '10.0.0.50', callerId: '',
    routes: '', limitIn: null, limitOut: null, comment: '', disabled: false,
    connected: false,
  }, over || {});
}

/** Boot the page against a shim and hand back the document plus an emitter. */
function boot() {
  // THE ENGINE'S SELECTORS ARE DECLARED EMPTY, NOT LEFT UNKNOWN. `mountAdds`
  // and `mountRows` query for their slots; this test renders the tables and
  // does not exercise the mount path, which `resmount-seam.test.ts` owns.
  // Declaring them keeps the unknown-id check below meaningful: it should catch
  // an id the PAGE reads and this test forgot, not a selector belonging to
  // machinery this test is not driving.
  const doc = makeDoc(IDS, { allowUnknown: [...RES_MODAL_IDS, '[data-ppptab]'], query: { '[data-res-add]': [], '[data-res-rows]': [] } });
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
  const { initPppPage } = require(OUT);
  // isVisible true: this page is on screen, which is when it renders.
  initPppPage(socket, () => true);
  return {
    doc,
    send: (p) => handlers['ppp:update'](p),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

// ── 1. the property the feature rests on ────────────────────────────────────
{
  const { doc, send, restore } = boot();
  // Every string field carries the marker, so a page that rendered ANY of them
  // as a password-shaped control fails — this does not depend on guessing which
  // field a regression would leak through.
  send(payload([secret({
    name: NEVER, comment: NEVER, profile: NEVER, callerId: NEVER, routes: NEVER,
  })]));
  const html = String(doc.nodes.pppSecretTable.innerHTML);
  assert.ok(!/password/i.test(html),
    'the secrets table rendered the word "password":\n' + html);
  assert.ok(!/type="password"/i.test(html), 'a password input reached the table');
  restore();
  say('ok  no password field reaches the rendered secrets table');
}

// ── 2. three states, not two ────────────────────────────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload([
    secret({ id: '*1', name: 'online-one', connected: true, disabled: false }),
    secret({ id: '*2', name: 'offline-one', connected: false, disabled: false }),
    secret({ id: '*3', name: 'disabled-one', connected: false, disabled: true }),
  ]));
  const html = String(doc.nodes.pppSecretTable.innerHTML);
  assert.ok(/lease-pill bound">online</.test(html), 'no online pill:\n' + html);
  assert.ok(/lease-pill">offline</.test(html), 'no offline pill:\n' + html);
  assert.ok(/lease-pill expired">disabled</.test(html), 'no disabled pill:\n' + html);
  // A disabled row is dimmed, which is how the table says "this one is off"
  // without spending a column on it.
  assert.ok(/opacity:\.55/.test(html), 'the disabled row is not dimmed');
  restore();
  say('ok  online, offline and disabled are three distinct pills');
}

// ── 3. connected comes from the payload, not from the sessions list ─────────
{
  const { doc, send, restore } = boot();
  // The session list says alice is up; the SECRET says she is not. The page must
  // believe the secret, because the server already did that join — a page that
  // re-derived it here would disagree with the server the moment the two reads
  // land in different ticks.
  send(payload(
    [secret({ name: 'alice', connected: false })],
    [{ id: '*S', name: 'alice', service: 'pppoe', address: '10.0.0.50',
       callerId: '', uptime: '1m', encoding: '', sessionId: '',
       limitIn: null, limitOut: null, rx: 0, tx: 0, rxRate: null, txRate: null }],
  ));
  const html = String(doc.nodes.pppSecretTable.innerHTML);
  assert.ok(/lease-pill">offline</.test(html),
    'the page re-derived connected from the sessions list instead of reading it:\n' + html);
  restore();
  say('ok  the connected pill reads the server-side join, it does not recompute it');
}

// ── 4. rows are addressable, and the tbody opts into the write engine ───────
{
  const { doc, send, restore } = boot();
  send(payload([secret({ id: '*7', name: 'bob' })]));
  const html = String(doc.nodes.pppSecretTable.innerHTML);
  assert.ok(html.includes('data-id="*7"'), 'no data-id on the row:\n' + html);
  assert.ok(html.includes('data-identity="bob"'), 'no data-identity on the row:\n' + html);
  const prof = String(doc.nodes.pppProfileTable.innerHTML);
  assert.ok(prof.includes('data-id="*1"'), 'no data-id on the profile row:\n' + prof);
  restore();
  say('ok  secret and profile rows carry the id and identity the write path needs');
}

// ── 5. the badge counts accounts, not search hits ───────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload([
    secret({ id: '*1', name: 'alice' }),
    secret({ id: '*2', name: 'bob' }),
    secret({ id: '*3', name: 'carol' }),
  ]));
  assert.strictEqual(String(doc.nodes.pppSecretBadge.textContent), '3',
    'the badge does not count the accounts');
  restore();
  say('ok  the secrets badge counts every account');
}

// ── 6. the two empty states differ ──────────────────────────────────────────
{
  const { doc, send, restore } = boot();
  send(payload([]));
  const withService = String(doc.nodes.pppSecretTable.innerHTML);
  const p = payload([]);
  p.available = false;
  send(p);
  const without = String(doc.nodes.pppSecretTable.innerHTML);
  assert.ok(/Add one/.test(withService), 'no add-one prompt on an empty list:\n' + withService);
  assert.ok(/no PPP service/.test(without), 'no service message missing:\n' + without);
  assert.notStrictEqual(withService, without,
    'a router with no PPP service reads the same as one with no accounts');
  restore();
  say('ok  "no accounts" and "no PPP service" are different empty states');
}

// ── 7. the shim declared every id the page actually looks up ───────────────
//
// `makeDoc` RECORDS an id nothing declared rather than returning null quietly.
// Without this the checks above could all pass against a page that had renamed
// a table and was writing into nothing.
{
  const { doc, send, restore } = boot();
  send(payload([secret({})]));
  const missed = Array.from(doc.unknown);
  restore();
  assert.deepStrictEqual(missed, [],
    'the page looked up ids this test never declared, so parts of it rendered ' +
    'into nothing while the assertions passed: ' + missed.join(', '));
  say('ok  every id the page reads was declared by this test');
}

// ── 8. the tab strip, and the Add button that follows it ───────────────────
//
// THE HAZARD IS THE ADD BUTTON, NOT THE PANELS. `mountAddSlots` fills a slot
// from its `data-res-add`, so a tab change that rewrites the attribute without
// announcing `mikrodash:resmount` leaves the PREVIOUS tab's button in place.
// That exact bug shipped once already: three pages announced the event and
// nothing listened, so pressing Add on the NAT table opened the filter-rule
// form. Both halves are asserted here, the attribute and the announcement.
//
// And Servers must offer NO Add at all: the table is read-only, so the slot
// names nothing rather than naming a resource the write path would refuse.
{
  const { doc, send, restore } = boot();
  send(payload([secret({})]));

  let announced = 0;
  doc.addEventListener('mikrodash:resmount', () => { announced += 1; });

  const bar = doc.nodes.pppCfgTabBar;
  const slot = doc.nodes.pppAddSlot;
  // THE SHIM'S DEFAULT getAttribute ANSWERS null. setAttribute records into
  // `node.attributes`, so that is where the written value is read back from;
  // asserting through getAttribute would compare null to null and pass on a
  // page that never wrote anything.
  const addRes = () => slot.attributes['data-res-add'];
  const click = (tab) => bar.fire('click', {
    target: { closest: (sel) => (sel === '[data-ppptab]' ? { dataset: { ppptab: tab } } : null) },
  });

  click('profiles');
  assert.strictEqual(addRes(), 'pppProfile',
    'the Add slot did not follow the tab to Profiles');
  assert.ok(announced >= 1, 'no mikrodash:resmount after a tab change — the Add ' +
    'button keeps the previous tab\'s resource');
  assert.strictEqual(doc.nodes['ppptab-profiles'].hidden, false, 'profiles panel stayed hidden');
  assert.strictEqual(doc.nodes['ppptab-secrets'].hidden, true, 'secrets panel stayed visible');

  const before = announced;
  click('servers');
  assert.strictEqual(addRes(), '',
    'the read-only Servers tab offers an Add button');
  assert.ok(announced > before, 'no resmount when moving to Servers, so the ' +
    'Profiles Add button is still on screen over a read-only table');

  click('secrets');
  assert.strictEqual(addRes(), 'pppSecret',
    'the Add slot did not come back to Secrets');
  restore();
  say('ok  the tab strip moves the panels, the Add slot and the announcement');
}

fs.rmSync(OUT, { force: true });
say('ppp-secrets: all checks passed');
