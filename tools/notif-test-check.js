'use strict';
/**
 * The four notification Test buttons, live against ported.
 *
 * ── NOTHING IS SENT. `fetch` IS THE FAKE ────────────────────────────────────
 *
 * Both implementations are driven against a scripted fetch, so this gate never
 * reaches a transport. What it compares is the REQUEST each one would have made
 * — which is the part worth pinning, because the server distinguishes an absent
 * field (fall back to what is stored) from a present-but-false one (override).
 *
 * ── THE FIELD RULES ARE NOT UNIFORM, AND THAT IS THE POINT ──────────────────
 *
 *   text fields    sent only when non-empty
 *   host/from/to   and the ntfy url are TRIMMED; tokens and passwords are not,
 *                  because whitespace can be part of a secret
 *   smtpSecure     sent whenever the checkbox EXISTS — ticked or not
 *   smtpPort       parsed, and only when non-empty
 *
 * A port that guarded `smtpSecure` on truthiness would make it impossible to
 * test with TLS off once it had been saved on, and no rendered output would
 * differ.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/notif-test-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { freezeCase } = require('./lib/lift.js');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/notif-test-check.js --freeze
const G = L.golden('notif-test-check');
const src = L.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

const liveSrc = slice('  function _testNotifBtn(btnId, resultId, channel) {', '\n  }', '_testNotifBtn') +
  "\n_testNotifBtn('btn-test-telegram',  'test-telegram-result',  'telegram');" +
  "\n_testNotifBtn('btn-test-pushbullet', 'test-pushbullet-result', 'pushbullet');" +
  "\n_testNotifBtn('btn-test-smtp',       'test-smtp-result',       'smtp');" +
  "\n_testNotifBtn('btn-test-ntfy',       'test-ntfy-result',       'ntfy');";

const OUT = path.join(ROOT, 'testdata', '.notiftest-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.notiftest-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/settings-notif-test.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const BUTTONS = [
  ['btn-test-telegram', 'test-telegram-result'],
  ['btn-test-pushbullet', 'test-pushbullet-result'],
  ['btn-test-smtp', 'test-smtp-result'],
  ['btn-test-ntfy', 'test-ntfy-result'],
];
const FIELDS = ['s_telegramBotToken', 's_telegramChatId', 's_pushbulletApiKey',
  's_smtpHost', 's_smtpPort', 's_smtpUser', 's_smtpPass', 's_smtpFrom', 's_smtpTo',
  's_ntfyUrl', 's_ntfyToken'];

function makeWorld(form, missing) {
  const ops = [];
  const nodes = {};
  const listeners = {};
  const timers = [];

  function node(id, extra) {
    let text = '', disabled = false;
    const style = new Proxy({}, {
      set(t, k, v) { t[k] = v; ops.push([id, 'style.' + String(k), String(v)]); return true; },
      get(t, k) { return t[k]; },
    });
    const n = {
      _id: id, style,
      get textContent() { return text; },
      set textContent(v) { text = String(v); ops.push([id, 'text', text]); },
      get disabled() { return disabled; },
      set disabled(v) { disabled = !!v; ops.push([id, 'disabled', disabled]); },
      addEventListener(ev, fn) { listeners[id + ':' + ev] = fn; },
    };
    return Object.defineProperties(n, Object.getOwnPropertyDescriptors(extra || {}));
  }

  for (const [b, r] of BUTTONS) { nodes[b] = node(b); nodes[r] = node(r); }
  for (const f of FIELDS) {
    if ((missing || []).includes(f)) continue;
    let v = form[f] === undefined ? '' : String(form[f]);
    nodes[f] = node(f, { get value() { return v; }, set value(x) { v = String(x); } });
  }
  if (!(missing || []).includes('s_smtpSecure')) {
    let checked = !!form.s_smtpSecure;
    nodes.s_smtpSecure = node('s_smtpSecure',
      { get checked() { return checked; }, set checked(x) { checked = !!x; } });
  }
  ops.length = 0;

  const doc = { getElementById: (id) => nodes[id] || null };
  return {
    doc, ops, nodes, listeners, timers,
    setTimeout: (fn, ms) => { timers.push([ms, fn]); return timers.length; },
    runTimers() {
      const due = timers.slice().sort((a, b) => a[0] - b[0]);
      timers.length = 0;
      due.forEach(([, fn]) => fn());
    },
    click(id) { listeners[id + ':click'](); },
    state() {
      // A node the scenario DELETED reports as absent rather than throwing —
      // "a missing result line does not stop the request" removes one on
      // purpose, and a state() that crashed on it would make that case
      // untestable rather than proving anything.
      return BUTTONS.flatMap(([b, r]) => [
        [b, nodes[b] ? nodes[b].disabled : null],
        [r, nodes[r] ? nodes[r].textContent : null, nodes[r] ? nodes[r].style.color : null],
      ]);
    },
  };
}

function fetchFor(reply, log) {
  return (url, init) => {
    log.push(['fetch', url, init && init.body ? JSON.parse(init.body) : null]);
    if (reply === 'reject') return Promise.reject(new Error('boom'));
    return Promise.resolve({ json: () => Promise.resolve(reply) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

async function runLive(sc) {
  const w = makeWorld(sc.form || {}, sc.missing);
  const log = [];
  const ctx = {
    document: w.doc, JSON, String, parseInt, Promise, setImmediate,
    setTimeout: w.setTimeout,
    $: (id) => w.doc.getElementById(id),
    fetch: fetchFor(sc.reply, log),
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  await sc.drive(w, flush);
  return { log, ops: w.ops, state: w.state() };
}

async function runPort(sc) {
  const w = makeWorld(sc.form || {}, sc.missing);
  const log = [];
  const prev = {
    document: globalThis.document, fetch: globalThis.fetch, setTimeout: globalThis.setTimeout,
  };
  globalThis.document = w.doc;
  globalThis.fetch = fetchFor(sc.reply, log);
  globalThis.setTimeout = w.setTimeout;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initNotifTestButtons();
    await sc.drive(w, flush);
  } finally {
    Object.assign(globalThis, prev);
  }
  return { log, ops: w.ops, state: w.state() };
}

const FULL = {
  s_telegramBotToken: '  tg-token-with-space  ', s_telegramChatId: '12345',
  s_pushbulletApiKey: 'pb-key',
  s_smtpHost: '  mail.invalid  ', s_smtpPort: '465', s_smtpSecure: true,
  s_smtpUser: 'user', s_smtpPass: '  pass with spaces  ',
  s_smtpFrom: '  from@invalid  ', s_smtpTo: '  to@invalid  ',
  s_ntfyUrl: '  https://ntfy.invalid/t  ', s_ntfyToken: '  ntfy-token  ',
};

const SCENARIOS = [];
for (const [btn] of BUTTONS) {
  SCENARIOS.push({
    name: btn + ': an empty form sends only the channel',
    form: {}, reply: { ok: true },
    async drive(w, f) { w.click(btn); await f(); await f(); },
  });
  SCENARIOS.push({
    name: btn + ': a full form, with the trim rules',
    form: FULL, reply: { ok: true },
    async drive(w, f) { w.click(btn); await f(); await f(); },
  });
  SCENARIOS.push({
    name: btn + ': a refusal',
    form: FULL, reply: { ok: false, error: 'not permitted' },
    async drive(w, f) { w.click(btn); await f(); await f(); },
  });
  SCENARIOS.push({
    name: btn + ': the request fails outright',
    form: FULL, reply: 'reject',
    async drive(w, f) { w.click(btn); await f(); await f(); w.runTimers(); },
  });
}
SCENARIOS.push({
  name: 'smtp: an UNTICKED secure box is still sent, as false',
  form: { ...FULL, s_smtpSecure: false }, reply: { ok: true },
  async drive(w, f) { w.click('btn-test-smtp'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'smtp: NO secure checkbox at all, so the field is absent',
  form: FULL, missing: ['s_smtpSecure'], reply: { ok: true },
  async drive(w, f) { w.click('btn-test-smtp'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'smtp: a port that is not a number',
  form: { ...FULL, s_smtpPort: 'abc' }, reply: { ok: true },
  async drive(w, f) { w.click('btn-test-smtp'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'smtp: an empty port is not sent at all',
  form: { ...FULL, s_smtpPort: '' }, reply: { ok: true },
  async drive(w, f) { w.click('btn-test-smtp'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'smtp: fields that are ONLY whitespace',
  form: { ...FULL, s_smtpHost: '   ', s_smtpTo: '   ' }, reply: { ok: true },
  async drive(w, f) { w.click('btn-test-smtp'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'the result clears after five seconds on success',
  form: FULL, reply: { ok: true },
  async drive(w, f) { w.click('btn-test-telegram'); await f(); await f(); w.runTimers(); },
});
SCENARIOS.push({
  name: 'the result clears after a refusal too',
  form: FULL, reply: { ok: false, error: 'nope' },
  async drive(w, f) { w.click('btn-test-telegram'); await f(); await f(); w.runTimers(); },
});
SCENARIOS.push({
  name: 'a MISSING result line does not stop the request',
  form: FULL, missing: [], reply: { ok: true },
  async drive(w, f) {
    delete w.nodes['test-telegram-result'];
    w.click('btn-test-telegram'); await f(); await f();
  },
});
SCENARIOS.push({
  name: 'a refusal with NO message falls back to "failed"',
  form: FULL, reply: { ok: false },
  async drive(w, f) { w.click('btn-test-telegram'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'a reply that is not an object at all',
  form: FULL, reply: null,
  async drive(w, f) { w.click('btn-test-telegram'); await f(); await f(); },
});
SCENARIOS.push({
  name: 'two presses in a row',
  form: FULL, reply: { ok: true },
  async drive(w, f) {
    w.click('btn-test-telegram'); await f(); await f();
    w.click('btn-test-telegram'); await f(); await f();
  },
});

(async () => {
  let bad = 0, total = 0;
  for (const sc of SCENARIOS) {
    // One case object reaches BOTH runs; a mutating drive would leak the live
    // run's state into the port's and make the gate accuse correct code.
    // See lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(sc);
    const live = await G.live(G.seq(), () => runLive(sc));
    const port = await runPort(sc);
    total += live.ops.length;
    if (live.log.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name + ' — the LIVE side made no request');
      bad++;
      continue;
    }
    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state }, null, 1);
    if (a === b) { console.log('  ok  ' + sc.name); continue; }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 10; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim()));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim()));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + total + ' operations compared');
  if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
  console.log('all agree');
})();
