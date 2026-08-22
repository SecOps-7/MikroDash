'use strict';
/**
 * Consumers with no producer, and producers with no consumer.
 *
 * Three of these turned up in one porting pass: an upgrade dialog rendering a
 * field the system payload never sent, a topology node preferring an identity
 * nothing set, and a device count written into an element that was not in the
 * markup. None of them broke anything, which is the problem — every one is
 * guarded, so it reads as working code and stays that way.
 *
 * The two sweeps here are the general form. They are cheap and they run every
 * time, which beats finding the next one by porting it.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const P = (...p) => fs.readFileSync(path.join(__dirname, '..', ...p), 'utf8');
const APP = P('public', 'app.js');
const MARKUP = P('public', 'index.html') + P('public', 'login.html');

test('every element app.js looks up exists somewhere', () => {
  // `$('bwStats')` returned null on every render of the Bandwidth page, so the
  // device count under the filter row was computed and thrown away.
  const have = new Set([...MARKUP.matchAll(/id="([^"]+)"/g)].map(m => m[1]));
  // Ids app.js builds itself, in template strings, count as present.
  for (const m of APP.matchAll(/id\s*=\s*\\?['"]([A-Za-z][\w-]*)\\?['"]/g)) have.add(m[1]);

  const missing = new Map();
  for (const m of APP.matchAll(/\$\(\s*'([A-Za-z][\w-]*)'\s*\)/g)) {
    if (!have.has(m[1])) missing.set(m[1], (missing.get(m[1]) || 0) + 1);
  }
  // Known orphans, left in place deliberately: each is a remnant of UI that was
  // removed, and CLAUDE.md says to mention unrelated dead code rather than
  // delete it. Shrink this list, never grow it.
  const KNOWN = new Set(['connMapSub', 'ndGateway', 'ndLanCidr', 'routerRestartNotice',
                         's_routerPass', 's_routerPort', 'wanIpDisplay',
                         'wlBand24', 'wlBand5', 'wlBand6']);
  const fresh = [...missing.keys()].filter(id => !KNOWN.has(id)).sort();
  assert.deepEqual(fresh, [],
    'app.js looks up elements that exist in no markup:\n  '
    + fresh.map(id => `${id} (${missing.get(id)} site(s))`).join('\n  '));

  // And the allow-list must not rot: an id that has since been given an element
  // should come off it.
  const stale = [...KNOWN].filter(id => have.has(id)).sort();
  assert.deepEqual(stale, [],
    'these are no longer orphans and should leave the allow-list: ' + stale.join(', '));
});

test('the fields the frontend reads off a collector payload are sent', () => {
  // `d.updateChannel` and `sys.identity` were both read by code that could never
  // see a value, because no collector emitted either name.
  const SYSTEM = P('src', 'collectors', 'system.js');
  const TOPOLOGY = P('src', 'collectors', 'topology.js');

  assert.match(SYSTEM, /updateChannel:/,
    'the upgrade dialog renders updateChannel; the system collector must send it');
  assert.match(APP, /_upd\.channel/,
    'and the dialog must still be reading it');

  // The topology core node no longer prefers a field nothing sets.
  assert.ok(!/sys && sys\.identity/.test(TOPOLOGY),
    'topology must not prefer sys.identity: the system payload carries no identity');
});
