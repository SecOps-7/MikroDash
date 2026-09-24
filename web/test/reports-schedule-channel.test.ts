/**
 * A scheduled report picks a CHANNEL, and its recipients come from that channel.
 *
 * ── WHAT THIS REPLACED ─────────────────────────────────────────────────────
 *
 * A schedule used to carry its own list of addresses in a textarea, while a
 * notification channel carried another. Who receives a notification is
 * configured on a channel now and nowhere else, so the textarea became a picker
 * and the schedule stores only an id.
 *
 * ── THE FAILURE THIS FILE IS FOR ───────────────────────────────────────────
 *
 * Every way of getting this wrong is silent. A picker that quietly selects the
 * first option when the stored channel was deleted re-routes a report to
 * different people, and the form looks correct while doing it. A table column
 * that renders an empty cell for a missing channel hides a schedule that will
 * skip every period. Neither throws, neither logs, and the first symptom is a
 * report arriving at the wrong desk, or not at all.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-reports-schedules.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'reports-schedules.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeEl(id) {
  const classes = new Set();
  const node = {
    id, innerHTML: '', textContent: '', value: '', checked: false, disabled: false,
    hidden: false, placeholder: '', style: {}, listeners: {}, dataset: {},
    addEventListener: (ev, fn) => { (node.listeners[ev] = node.listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (node.listeners[ev] || []).forEach((fn) => fn(arg || {})),
    // `openSchedModal` rebuilds the hour list this way. The shim only has to not
    // throw; nothing here asserts on the hours.
    insertAdjacentHTML: (_where, html) => { node.innerHTML += html; },
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    closest: () => null,
    querySelectorAll: () => [],
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
  };
  return node;
}

const CHANNELS = [
  { id: 'm1', name: 'Ops mail', kind: 'smtp', owner: '_install', enabled: true },
  { id: 'm2', name: 'Exec mail', kind: 'smtp', owner: '_install', enabled: true },
  { id: 'off', name: 'Old mail', kind: 'smtp', owner: '_install', enabled: false },
  { id: 'w1', name: 'Ops Telegram', kind: 'webhook', owner: '_install', enabled: true },
  { id: 'mine', name: 'My mail', kind: 'smtp', owner: 'user:7', enabled: true },
];

function schedule(over) {
  return {
    id: 's1', name: 'Weekly WAN', sections: ['ping'], aggregate: 'day',
    channelId: 'm1', frequency: 'weekly', sendHour: 7, enabled: true,
    lastRun: null, ...over,
  };
}

/** Mount the module with a DOM shim and let it load. */
function mount(schedules, channels = CHANNELS) {
  const els = {};
  const get = (id) => {
    if (!els[id]) els[id] = makeEl(id);
    return els[id];
  };
  const sent = [];
  // `wireScheduleForm` delegates on the DOCUMENT and matches `#rs_save` with
  // `closest`, because the dialog's markup is rebuilt each time it opens. A shim
  // that swallowed document listeners could never reach the save path.
  const docListeners = {};
  global.document = {
    getElementById: get,
    querySelectorAll: () => [],
    addEventListener: (ev, fn) => { (docListeners[ev] = docListeners[ev] || []).push(fn); },
    createElement: () => makeEl(''),
  };
  const clickOn = (sel) => (docListeners.click || []).forEach((fn) => fn({
    target: { closest: (q) => (q === sel ? { getAttribute: () => null } : null) },
  }));
  global.window = { confirm: () => true };
  global.fetch = (url, opts) => {
    const u = String(url);
    if (opts && opts.method && opts.method !== 'GET') {
      sent.push({ url: u, method: opts.method, body: JSON.parse(opts.body || '{}') });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    }
    if (u.startsWith('/api/notify-channels')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true, channels }) });
    }
    if (u.startsWith('/api/reports/schedules')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({
          ok: true, permitted: true, schedules, sections: ['ping', 'traffic'],
          needsInterface: ['traffic'],
        }),
      });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  // The loader reads the router picker's value before it asks for anything.
  get('rptRouter').value = 'r1';
  mod.loadSchedules();
  return { els, sent, mod, get, clickOn };
}

const settle = () => new Promise((r) => setImmediate(r));

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

say('reports: a schedule sends through a channel, not a list of addresses');

check('the table names the channel, where it used to count addresses', async () => {
  const d = mount([schedule({ channelId: 'm2' })]);
  await settle();
  const html = d.els.rptSchedTbody.innerHTML;
  assert.ok(html.includes('Exec mail'), 'the row does not name its channel: ' + html);
});

// THE SILENT ONE. A deleted channel leaves an id naming nothing. The schedule
// skips every period until it is pointed somewhere, and an empty cell is how
// that goes unnoticed for a month.
check('a deleted channel is said out loud, not left blank', async () => {
  const d = mount([schedule({ channelId: 'gone' })]);
  await settle();
  const html = d.els.rptSchedTbody.innerHTML;
  assert.ok(/channel deleted/.test(html),
    'a schedule pointing at nothing rendered without saying so: ' + html);
});

check('a schedule with no channel at all says so', async () => {
  const d = mount([schedule({ channelId: '' })]);
  await settle();
  assert.ok(/no channel/.test(d.els.rptSchedTbody.innerHTML),
    'an unset channel rendered as an empty cell');
});

check('a disabled channel is named and marked off', async () => {
  const d = mount([schedule({ channelId: 'off' })]);
  await settle();
  const html = d.els.rptSchedTbody.innerHTML;
  assert.ok(html.includes('Old mail') && html.includes('(off)'),
    'a switched-off channel did not read as off: ' + html);
});

// ── THE PICKER ────────────────────────────────────────────────────────────

/** The option values the dialog offers, in order. */
function options(d) {
  return [...d.els.rs_channel.innerHTML.matchAll(/value="([^"]*)"/g)].map((m) => m[1]);
}

check('only install-owned SMTP channels are offered', async () => {
  const d = mount([schedule()]);
  await settle();
  d.mod.openSchedModal(null);
  assert.deepEqual(options(d), ['m1', 'm2', 'off'],
    'a webhook cannot carry a PDF, and a user-owned channel would hand that user '
    + 'routers they were never granted');
});

// FOUND IN A BROWSER, NOT HERE — which is why it is here now. `sel.value = ''`
// matches no option, so a new schedule opened with an empty box and saving was
// refused with "a schedule needs a channel to send through", on a form offering
// exactly one answer. The shim's `value` is a plain property and accepted the
// empty string happily, so nothing failed.
check('a new schedule preselects the first channel rather than nothing', async () => {
  const d = mount([schedule()]);
  await settle();
  d.mod.openSchedModal(null);
  assert.equal(d.els.rs_channel.value, 'm1',
    'a new schedule opened with no channel selected, so saving it would be '
    + 'refused for a choice the operator was never asked to make');
});

check('editing selects the channel the schedule already uses', async () => {
  const d = mount([schedule({ channelId: 'm2' })]);
  await settle();
  d.mod.openSchedModal(schedule({ channelId: 'm2' }));
  assert.equal(d.els.rs_channel.value, 'm2');
});

// A DELETED CHANNEL IS KEPT AS AN OPTION, labelled, rather than silently
// replaced by whatever is first. Dropping it would make an unrelated edit —
// changing the send hour, say — quietly re-route the report, which is the one
// thing this whole change exists to stop.
check('a deleted channel is kept as a labelled option, not replaced', async () => {
  const d = mount([schedule({ channelId: 'gone' })]);
  await settle();
  d.mod.openSchedModal(schedule({ channelId: 'gone' }));
  assert.equal(options(d)[0], 'gone',
    'the stored channel was dropped from the picker, so saving anything else '
    + 'about this schedule would move its report to another set of people');
  assert.equal(d.els.rs_channel.value, 'gone');
  assert.ok(/now deleted/.test(d.els.rs_channel.innerHTML),
    'the dead option is offered without saying it is dead: ' + d.els.rs_channel.innerHTML);
});

check('with no mail channel the picker says what is missing', async () => {
  const d = mount([schedule()],
    [{ id: 'w1', name: 'Tg', kind: 'webhook', owner: '_install', enabled: true }]);
  await settle();
  d.mod.openSchedModal(null);
  const html = d.els.rs_channel.innerHTML;
  assert.ok(/Settings/.test(html),
    'an empty picker reads as "still loading" rather than "add a channel": ' + html);
});

// ── THE SAVE BODY ─────────────────────────────────────────────────────────

check('saving sends channelId and no recipients', async () => {
  const d = mount([schedule()]);
  await settle();
  d.mod.wireScheduleForm();
  d.mod.openSchedModal(schedule({ channelId: 'm2' }));
  d.clickOn('#rs_save');
  await settle();
  const posts = d.sent.filter((x) => x.url.startsWith('/api/reports/schedules'));
  assert.equal(posts.length, 1, 'posted ' + posts.length + ' times: ' + JSON.stringify(d.sent));
  assert.equal(posts[0].body.channelId, 'm2',
    'the body did not carry the channel: ' + JSON.stringify(posts[0].body));
  assert.ok(!('recipients' in posts[0].body),
    'the body still carries a recipient list, which the server no longer reads — '
    + 'so the addresses an operator typed would vanish without a word: '
    + JSON.stringify(posts[0].body));
});

(async () => {
  for (const c of checks) {
    try {
      await c.fn();
      say('  ok   ' + c.name);
    } catch (e) {
      failed += 1;
      say('  FAIL ' + c.name + '\n       ' + (e && e.message));
    }
  }
  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
})();
