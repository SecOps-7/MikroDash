/**
 * The notification channels card and its dialog.
 *
 * ── THE ONE THING THAT MUST NOT BREAK ──────────────────────────────────────
 *
 * The server never sends a webhook URL back, because each carries its own token.
 * So editing a channel shows an EMPTY url box, and saving with it empty must
 * leave the stored URLs alone. If the form sent `config: {urls: []}` instead of
 * omitting `config`, every edit would silently delete the channel's
 * destinations — a channel that still looks configured and delivers nothing.
 * That is the failure this file exists for.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-notify-channels.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings-notify-channels.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeEl(id) {
  const classes = new Set();
  const node = {
    id, innerHTML: '', textContent: '', value: '', checked: false, disabled: false,
    hidden: false, placeholder: '', style: {}, listeners: {},
    addEventListener: (ev, fn) => { (node.listeners[ev] = node.listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (node.listeners[ev] || []).forEach((fn) => fn(arg || {})),
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

const CHANNEL = {
  id: 'c1', owner: '_install', name: 'Ops Telegram', kind: 'webhook',
  enabled: true, events: ['ping_loss'], routers: [], urlCount: 2,
  hasSecret: true, mine: false,
};

const EVENTS = {
  ok: true,
  events: [
    {
      key: 'ping_loss', label: 'Ping loss', desc: 'loss crossed the threshold',
      backup: false,
    },
    {
      key: 'host_down', label: 'NetWatch host', desc: 'a host stopped responding',
      backup: false,
    },
  ],
  schemes: ['tgram', 'ntfy'],
  defaults: ['ping_loss'],
};

/** Mount the module with a DOM shim, and hand back what it did. */
function mount(channels, canManageInstall, reportChannelId) {
  const els = {};
  const get = (id) => {
    if (!els[id]) els[id] = makeEl(id);
    return els[id];
  };
  // The toggles the module writes with innerHTML cannot be discovered by this
  // shim, so `querySelectorAll` serves empty lists for them. That is the
  // harness's limit, not the panel's: what is asserted below is the REQUEST,
  // which is the thing an operator's configuration actually depends on.
  const dynamic = { '[data-nchan-event]': [], '[data-nchan-router]': [] };
  const sent = [];

  global.document = {
    getElementById: get,
    querySelectorAll: (sel) => dynamic[sel] || [],
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  global.fetch = (url, opts) => {
    const u = String(url);
    if (opts && opts.method && opts.method !== 'GET') {
      sent.push({ url: u, method: opts.method, body: JSON.parse(opts.body || '{}') });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    }
    if (u.startsWith('/api/notify-channels/events')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(EVENTS) });
    }
    if (u.startsWith('/api/notify-channels')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({
          ok: true, channels, canManageInstall: !!canManageInstall,
          reportChannelId: reportChannelId || '',
        }),
      });
    }
    if (u.startsWith('/api/routers')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ routers: [{ id: 'r1', label: 'Office' }] }),
      });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);
  mod.initNotifyChannels();
  return { els, sent, dynamic, get };
}

const settle = () => new Promise((r) => setImmediate(r));

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

check('the grid lists each channel with its destination and scope', async () => {
  const d = mount([CHANNEL]);
  await settle();
  const html = d.els.nchanGrid.innerHTML;
  assert.ok(html.includes('Ops Telegram'), 'the name is missing: ' + html);
  assert.ok(html.includes('Webhook: 2 URLs'), 'the URL count is missing: ' + html);
  assert.ok(html.includes('all routers'), 'an empty router list must read as all: ' + html);
  assert.ok(html.includes('1 event'), 'the event count is missing: ' + html);
});

check('a disabled channel reads OFF, an enabled one ON', async () => {
  const off = mount([{ ...CHANNEL, enabled: false }]);
  await settle();
  assert.ok(off.els.nchanGrid.innerHTML.includes('>OFF<'),
    'a disabled channel did not read OFF: ' + off.els.nchanGrid.innerHTML);
  const on = mount([CHANNEL]);
  await settle();
  assert.ok(on.els.nchanGrid.innerHTML.includes('>ON<'), 'an enabled channel did not read ON');
});

// A NAME IS OPERATOR INPUT and reaches innerHTML. This app had exactly this bug
// in an interface name (0.7.35).
check('a channel name cannot inject markup', async () => {
  const d = mount([{ ...CHANNEL, name: '<img src=x onerror=alert(1)>' }]);
  await settle();
  const html = d.els.nchanGrid.innerHTML;
  assert.ok(!html.includes('<img'), 'a channel name reached the page as markup: ' + html);
  assert.ok(html.includes('&lt;img'), 'the name was not escaped: ' + html);
});

check('an empty list says so rather than drawing nothing', async () => {
  const d = mount([]);
  await settle();
  assert.ok(d.els.nchanGrid.innerHTML.includes('No channels yet'),
    'an empty grid rendered silently: ' + d.els.nchanGrid.innerHTML);
});

// ── THE SAVE BODY ──────────────────────────────────────────────────────────

async function addChannel(canManageInstall) {
  const d = mount([], canManageInstall);
  await settle();
  d.els.nchanAdd.fire('click');
  await settle();

  d.els.nchanName.value = 'Ops';
  d.els.nchanKind.value = 'webhook';
  d.els.nchanUrls.value = 'tgram://1:A/2\n\nntfy://ntfy.example.net/ops\n';
  d.els.nchanEnabled.checked = true;
  d.els.nchanSaveBtn.fire('click');
  await settle();
  return d;
}

check('a new webhook channel sends its URLs in order, dropping blanks', async () => {
  const d = await addChannel(true);
  const post = d.sent.find((s) => s.method === 'POST');
  assert.ok(post, 'nothing was posted: ' + JSON.stringify(d.sent));
  assert.deepEqual(post.body.config.urls, ['tgram://1:A/2', 'ntfy://ntfy.example.net/ops'],
    'blank lines must be dropped and the rest kept in order');
});

// OWNERSHIP IS ASKED FOR, NOT ASSUMED.
//
// The form sent `owner: "install"` for everybody, and the server refuses that
// for anyone who is not an administrator — so a non-administrator pressing Add
// Channel got a 403 and could not make a channel of their own at all. The 403
// would have read as a permissions misconfiguration rather than a UI bug.
check('an administrator claims install ownership, a plain user claims their own', async () => {
  const admin = await addChannel(true);
  assert.equal(admin.sent.find((s) => s.method === 'POST').body.owner, 'install');

  const user = await addChannel(false);
  assert.equal(user.sent.find((s) => s.method === 'POST').body.owner, 'mine',
    'a non-administrator asked for install ownership, which the server refuses');
});

// THE CHECK THIS FILE EXISTS FOR.
check('editing without retyping the URLs does NOT clear them', async () => {
  const d = mount([CHANNEL]);
  await settle();
  // Reach the edit path the way a click does.
  d.els.nchanGrid.fire('click', {
    target: {
      closest: () => ({
        getAttribute: (k) => (k === 'data-nchan-act' ? 'edit' : 'c1'),
      }),
    },
  });
  await settle();

  assert.equal(d.els.nchanUrls.value, '',
    'the stored URLs must never be sent back to the browser');
  assert.ok(/keep the 2 stored URLs/.test(d.els.nchanUrls.placeholder),
    'the box does not tell the operator that blank means keep: '
    + JSON.stringify(d.els.nchanUrls.placeholder));

  d.els.nchanSaveBtn.fire('click');
  await settle();

  const put = d.sent.find((s) => s.method === 'PUT');
  assert.ok(put, 'nothing was sent: ' + JSON.stringify(d.sent));
  assert.ok(!('config' in put.body),
    'an untouched URL box sent a config, which REPLACES the stored URLs and '
    + 'silently empties the channel: ' + JSON.stringify(put.body));
});

check('the type picker swaps the body, both ways', async () => {
  const d = mount([]);
  await settle();
  d.els.nchanAdd.fire('click');
  await settle();
  assert.equal(d.els.nchanWebhookBody.hidden, false, 'webhook is the default body');
  assert.equal(d.els.nchanSmtpBody.hidden, true);

  d.els.nchanKind.value = 'smtp';
  d.els.nchanKind.fire('change');
  assert.equal(d.els.nchanWebhookBody.hidden, true, 'the webhook body stayed after switching');
  assert.equal(d.els.nchanSmtpBody.hidden, false, 'the SMTP body did not appear');
});

check('testing an unsaved channel says to save it rather than failing silently', async () => {
  const d = mount([]);
  await settle();
  d.els.nchanAdd.fire('click');
  await settle();
  d.els.nchanTestBtn.fire('click');
  await settle();
  assert.ok(/Save the channel first/.test(d.els.nchanTestResult.textContent),
    'an unsaved test said: ' + JSON.stringify(d.els.nchanTestResult.textContent));
  assert.ok(!d.sent.some((s) => s.url.includes('/test')),
    'an unsaved channel was tested against the server');
});

// THE EVENTS TAB SAYS NOTHING ABOUT A GATE THAT NO LONGER EXISTS.
//
// The endpoint used to send `raised` per event, and the row appended "not raised
// install-wide" when it was false. The install-wide gates went when alert types
// moved onto the channel, so the field went too — and reading the missing field
// made EVERY event carry that phrase, which is exactly backwards. The web suite
// passed throughout; it was found by opening the tab.
check('no event is labelled as not raised install-wide', async () => {
  const d = mount([CHANNEL], true);
  await settle();
  d.els.nchanGrid.fire('click', {
    target: { closest: () => ({ getAttribute: (k) => (k === 'data-nchan-act' ? 'edit' : 'c1') }) },
  });
  await settle();
  await settle();

  const html = d.els.nchanEvents.innerHTML;
  assert.ok(html.includes('Ping loss'),
    'the event list did not render, so this check is reading an empty string: ' + html);
  assert.ok(!/not raised/.test(html),
    'an event is labelled "not raised install-wide", which describes a gate that no '
    + 'longer exists: ' + html);
  assert.ok(!/is-gated/.test(html),
    'an event row is greyed as gated, and there is nothing left to gate it: ' + html);
});

// ── THE SCHEDULED REPORTS TICK ─────────────────────────────────────────────
//
// It replaced a `<select>` under the grid naming which channel the reports go
// through. The rule that matters is EXACTLY ONE: the state is a single
// `reportChannelId` setting rather than a flag per channel, so two cards can
// never both be ticked — but only if the page derives the tick rather than
// storing one. These pin the derivation, because the failure is silent: two
// ticks read as "reports go to both", and none reads as "reports go nowhere",
// while the server carries on sending through exactly one.

const SMTP_A = {
  id: 'm1', owner: '_install', name: 'Mail A', kind: 'smtp', enabled: true,
  events: [], routers: [], urlCount: 0, hasSecret: true, mine: false,
  smtpHost: 'a.example.net', smtpTo: 'ops@example.net',
};
const SMTP_B = { ...SMTP_A, id: 'm2', name: 'Mail B', smtpHost: 'b.example.net' };

/** Which ids are drawn ticked, read off the rendered grid. */
function ticked(html) {
  const out = [];
  const re = /data-nchan-report="([^"]+)"([^>]*)>/g;
  let m;
  while ((m = re.exec(html))) if (/\bchecked\b/.test(m[2])) out.push(m[1]);
  return out;
}

/** Every id that has a tick at all, ticked or not. */
function offered(html) {
  return [...html.matchAll(/data-nchan-report="([^"]+)"/g)].map((m) => m[1]);
}

check('exactly one SMTP channel is ticked, and it is the first when none is stored', async () => {
  const d = mount([SMTP_A, SMTP_B], true);
  await settle();
  const html = d.els.nchanGrid.innerHTML;
  assert.deepEqual(ticked(html), ['m1'],
    'an empty reportChannelId must tick the FIRST enabled install SMTP channel, '
    + 'because that is what the server falls back to: ' + html);
});

check('a stored choice is ticked instead of the first', async () => {
  const d = mount([SMTP_A, SMTP_B], true, 'm2');
  await settle();
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m2']);
});

// THE SILENT ONE. Delete the chosen channel and the stored id names nothing.
// The server's second pass falls through to the first enabled install SMTP
// channel and keeps sending; a page that showed no tick would disagree with it.
check('a stored id naming a deleted channel falls through to the first', async () => {
  const d = mount([SMTP_A, SMTP_B], true, 'gone');
  await settle();
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m1'],
    'a stale reportChannelId left no tick at all, while reports carry on going '
    + 'out through Mail A');
});

// THE TWO SIDES DISAGREED HERE. This page filters to install-owned SMTP BEFORE
// looking for the stored id; `chooseReportChannel` in reports_run.go matched the
// id against any SMTP channel and took the user's. The page never offers a
// user-owned channel, so a stored id naming one arrived some other way and is
// not honoured for being specific. The server was narrowed to match.
check('a stored id naming a USER\'s mail channel is not honoured', async () => {
  const d = mount([{ ...SMTP_A, id: 'mine', owner: 'user:7', mine: true }, SMTP_A], true, 'mine');
  await settle();
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m1']);
});

check('a disabled install channel is skipped by the fallback', async () => {
  const d = mount([{ ...SMTP_A, id: 'off', enabled: false }, SMTP_A], true);
  await settle();
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m1'],
    'a channel that is switched off was drawn as the reports sender');
});

// ENABLED IS CHECKED ONLY ON THE FALLBACK. An operator who ticked a channel and
// later disabled it made two decisions; silently re-routing their reports to
// another mail server would be worse than not sending them, because the mail
// arrives from the wrong address looking right.
check('a disabled but explicitly chosen channel keeps the tick', async () => {
  const d = mount([SMTP_A, { ...SMTP_B, enabled: false }], true, 'm2');
  await settle();
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m2']);
});

check('only install-owned SMTP channels are offered', async () => {
  const d = mount([
    CHANNEL,                                   // a webhook: cannot carry a PDF
    { ...SMTP_A, id: 'mine', owner: 'user:7', mine: true },  // a user's own
    SMTP_A,
  ], true);
  await settle();
  assert.deepEqual(offered(d.els.nchanGrid.innerHTML), ['m1'],
    'a webhook or a user-owned mail channel was offered as the install\'s '
    + 'reports sender: ' + d.els.nchanGrid.innerHTML);
});

// IT DOES NOT UNTICK. An untick would store "", which the server reads as "the
// first enabled install SMTP channel" — this one again. A control whose off
// state is its on state is worse than one that does not turn off.
check('the ticked box is disabled, and the others are not', async () => {
  const d = mount([SMTP_A, SMTP_B], true);
  await settle();
  const html = d.els.nchanGrid.innerHTML;
  const on = /data-nchan-report="m1"([^>]*)>/.exec(html)[1];
  const off = /data-nchan-report="m2"([^>]*)>/.exec(html)[1];
  assert.ok(/disabled/.test(on), 'the ticked box can be unticked: ' + on);
  assert.ok(!/disabled/.test(off), 'the untickd box is disabled too: ' + off);
});

check('a non-admin cannot move the reports channel', async () => {
  const d = mount([SMTP_A, SMTP_B], false);
  await settle();
  const html = d.els.nchanGrid.innerHTML;
  assert.ok(/data-nchan-report="m2"[^>]*disabled/.test(html),
    'a plain user was offered a live box for an install-wide setting: ' + html);
});

/** Fire the delegated change handler as a click on one box would. */
function tick(d, id) {
  d.els.nchanGrid.fire('change', {
    target: { closest: (sel) => (sel === '[data-nchan-report]'
      ? { getAttribute: () => id } : null) },
  });
}

check('ticking another channel posts exactly the one settings key', async () => {
  const d = mount([SMTP_A, SMTP_B], true);
  await settle();
  tick(d, 'm2');
  await settle();
  const posts = d.sent.filter((x) => x.url === '/api/settings');
  assert.equal(posts.length, 1, 'posted ' + posts.length + ' times: '
    + JSON.stringify(d.sent));
  assert.deepEqual(posts[0].body, { reportChannelId: 'm2' },
    'the body carried more than the one key, so an unrelated setting would be '
    + 'rewritten from a stale page: ' + JSON.stringify(posts[0].body));
  assert.deepEqual(ticked(d.els.nchanGrid.innerHTML), ['m2'],
    'the tick did not move after the server accepted it');
});

// TICKING THE TICKED ONE SENDS NOTHING. It is disabled, so this cannot happen
// through the UI — but the guard is in the handler, and a guard nothing tests is
// a guard that gets deleted as redundant.
check('ticking the already-chosen channel posts nothing', async () => {
  const d = mount([SMTP_A, SMTP_B], true);
  await settle();
  tick(d, 'm1');
  await settle();
  assert.equal(d.sent.filter((x) => x.url === '/api/settings').length, 0,
    'a no-op write reached the server: ' + JSON.stringify(d.sent));
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
