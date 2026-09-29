/**
 * THE CREDENTIALS TAB'S RENDERING (#143).
 *
 * Executed rather than type-checked, for the reason web/test/README.md gives:
 * tsc proves the shapes agree, and these functions build HTML strings, where
 * the only thing that proves the output is reading it.
 *
 * Three claims, each of which has a way of being quietly wrong:
 *
 *   - a profile row shows the WORST state among its routers, not the commonest.
 *     Nineteen applied and one refused must read `refused`, or the one device
 *     that needs attention is invisible until somebody opens the profile;
 *   - `refused` and `conflict` are AMBER. Neither is a fault - both are
 *     MikroDash declining to do something - and a red row for "the guard
 *     protected your login" reads as a bug report;
 *   - a custom permission set NAMES its policies. "custom" tells an operator
 *     nothing about what the account can do, which is the one question the
 *     column exists to answer.
 *
 * And the control every one of them needs: a profile with no routers, and
 * values that must survive escaping.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.cred-profiles-entry.ts');
fs.writeFileSync(ENTRY,
  "export { drawProfiles, permissionText, profileRow, statePill, worstState }\n"
  + "  from '../web/src/pages/config-management-credentials.js';\n");
const OUT = path.join(ROOT, 'testdata', '.cred-profiles.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);

const profile = (over = {}) => ({
  id: 'cp1', name: 'NOC read-only', description: '', username: 'noc',
  groupName: 'grp-noc', policies: ['read'],
  hasSecret: true, links: 0, revision: 1, pendingDelete: false, ...over,
});
const link = (routerId, state) => ({
  profileId: 'cp1', routerId, state, code: '', error: '', appliedRevision: 1, attempts: 0,
  via: 'direct',
});

// ── THE WORST STATE WINS ────────────────────────────────────────────────────
{
  const many = [];
  for (let i = 0; i < 19; i++) many.push(link('r' + i, 'applied'));
  many.push(link('r19', 'refused'));
  assert.strictEqual(mod.worstState(many), 'refused',
    'nineteen applied and one refused read as applied, so the device that needs '
    + 'attention is invisible from the list');

  // AND IT IS A RANKING, not "the last one seen".
  assert.strictEqual(mod.worstState([link('a', 'refused'), link('b', 'applied')]), 'refused');
  assert.strictEqual(mod.worstState([link('a', 'unreachable'), link('b', 'conflict')]), 'conflict');
  // THE CONTROL: all applied reads applied, so the rule is a ranking rather
  // than one that always finds something to worry about.
  assert.strictEqual(mod.worstState([link('a', 'applied'), link('b', 'applied')]), 'applied');
  assert.strictEqual(mod.worstState([]), '', 'no routers should read as no state');
}
say('  worst state wins over the commonest');

// ── REFUSAL IS AMBER, FAILURE IS RED ────────────────────────────────────────
{
  for (const s of ['refused', 'conflict', 'orphaned']) {
    assert.ok(mod.statePill(s).includes('cp-warn'),
      s + ' is not amber; MikroDash declining to do something is not a fault, and '
      + 'red reads as a bug report');
  }
  for (const s of ['failed', 'unreachable', 'unknown']) {
    assert.ok(mod.statePill(s).includes('cp-bad'), s + ' is not red');
  }
  assert.ok(mod.statePill('applied').includes('cp-ok'), 'applied is not green');
  for (const s of ['pending', 'applying', 'removing']) {
    assert.ok(mod.statePill(s).includes('cp-wait'), s + ' is not the in-progress colour');
  }
  // A REFUSAL EXPLAINS ITSELF. The state is a word; the title is the sentence,
  // and without it `refused` is a dead end on the page.
  assert.ok(mod.statePill('refused').includes('title="'), 'refused carries no explanation');
  assert.ok(mod.statePill('refused').includes('Retry'),
    'the refusal does not say that it will not be retried on its own, which is the '
    + 'one thing an operator has to know about that state');
  // AND AN UNKNOWN STATE IS STILL DRAWN, escaped, rather than dropped.
  const odd = mod.statePill('<b>new</b>');
  assert.ok(!odd.includes('<b>new'), 'an unknown state was not escaped');
}
say('  refusals are amber and explain themselves');

// ── A CUSTOM SET NAMES ITS POLICIES ─────────────────────────────────────────
{
  // EVERY profile names its own group and its policies - there is no longer a
  // form that borrows one of RouterOS's.
  assert.strictEqual(mod.permissionText(profile()), 'grp-noc: read');
  assert.strictEqual(
    mod.permissionText(profile({ groupName: 'noc-grp', policies: ['read', 'api'] })),
    'noc-grp: read, api',
    'the column does not name the policies, so it cannot answer what the account can do');
  // A group with nothing granted is a real thing to make, and it must not read
  // as though it simply has not loaded.
  assert.strictEqual(
    mod.permissionText(profile({ groupName: 'locked', policies: [] })),
    'locked (no permissions)');
}
say('  a custom permission set names its policies');

// ── THE ROW ─────────────────────────────────────────────────────────────────
{
  const row = mod.profileRow(profile({ links: 2 }), [link('r1', 'applied'), link('r2', 'refused')]);
  assert.ok(row.includes('NOC read-only'), 'the row does not name the profile');
  assert.ok(row.includes('<code>noc</code>'), 'the RouterOS username is not shown as a name');
  assert.ok(row.includes('cp-warn'), 'the row does not show the refused router');
  assert.ok(row.includes('data-cp-edit="cp1"') && row.includes('data-cp-links="cp1"'),
    'the row has no way into the profile or its routers');
  // DELETE SAYS WHICH KIND IT IS. On a linked profile the accounts come off
  // routers first, and a button reading "Delete" that then leaves the row in
  // place for a minute reads as a failure rather than as the design.
  assert.ok(row.includes('data-cp-del="cp1"'), 'the row offers no way to delete the profile');
  assert.ok(row.includes('Remove &amp; delete'),
    'a profile that is on routers does not say the accounts come off first');
  assert.ok(mod.profileRow(profile(), []).includes('>Delete<'),
    'an unlinked profile should just say Delete');

  // A PROFILE ON NO ROUTERS says so rather than showing an empty pill, which
  // would read as a state that failed to load.
  const lonely = mod.profileRow(profile(), []);
  assert.ok(lonely.includes('not linked'), 'an unlinked profile shows no state at all');
  assert.ok(!lonely.includes('cp-pill'), 'an unlinked profile was given a state pill');

  // ESCAPING, on every value that reaches the row. A profile name and a
  // username are operator input.
  const nasty = mod.profileRow(
    profile({ name: '<img src=x onerror=1>', username: '<b>u</b>', description: '<i>d</i>' }),
    []);
  assert.ok(!nasty.includes('<img'), 'a profile name was rendered as markup');
  assert.ok(!nasty.includes('<b>u</b>'), 'a username was rendered as markup');
  assert.ok(!nasty.includes('<i>d</i>'), 'a description was rendered as markup');
}
say('  the row names the profile, its worst state, and escapes what it shows');

// ── THE TABLE, AND ITS EMPTY STATE ──────────────────────────────────────────
{
  const doc = makeDoc(['cpBody', 'cpEmpty']);
  global.document = doc;

  mod.drawProfiles([], []);
  assert.strictEqual(doc.nodes.cpEmpty.hidden, false, 'no profiles did not show the empty state');
  assert.strictEqual(doc.nodes.cpBody.innerHTML, '', 'an empty list drew rows');

  mod.drawProfiles([profile({ links: 1 })], [link('r1', 'applied')]);
  assert.strictEqual(doc.nodes.cpEmpty.hidden, true, 'the empty state stayed up with a profile listed');
  assert.ok(doc.nodes.cpBody.innerHTML.includes('NOC read-only'), 'the profile was not drawn');
}
say('  the table draws its rows and its empty state');

fs.rmSync(OUT, { force: true });
say('cred-profiles: ok');
