'use strict';
/**
 * The poll sliders, the preset profiles, the banner and the reset — live
 * against ported.
 *
 * ── THE TABLE IS INJECTED, AND THAT IS THE POINT ────────────────────────────
 *
 * `cfg.streamed` is a real branch in `buildSliders` and in the custom save, and
 * NO ROW IN TODAY'S TABLE CARRIES IT (measured; `tools/poll-tables.js` records
 * it). Driven against the real table, both implementations would skip that
 * branch and agree perfectly about code neither one ran.
 *
 * So both sides are driven against tables this file supplies: the real one, and
 * synthetic ones carrying a streamed row and a profile missing a key. The live
 * IIFE closes over `POLL_SLIDERS`, so injecting means running the lifted
 * functions in a context where those names are already bound.
 *
 * ── WHAT IS COMPARED ────────────────────────────────────────────────────────
 *
 * The rendered `pollSlidersWrap` element by element, the banner's class and text
 * over time, the profile buttons' active states, what localStorage was asked to
 * remember, and every fetch body.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/poll-sliders-check.js
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
// in lib/lift.js. Re-freeze with: node tools/poll-sliders-check.js --freeze
const G = L.golden('poll-sliders-check');
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

// The FUNCTIONS only. The tables come from this file so the streamed branch can
// be reached; lifting them here too would defeat the injection.
const liveFns = [
  slice('  function fmtMs(ms) {', '\n  }', 'fmtMs'),
  slice('  function showBanner(type, msg) {', '\n  }', 'showBanner'),
  slice('  function buildSliders(data) {', '\n  }\n', 'buildSliders'),
  slice('  function _detectProfile(data) {', '\n  }', '_detectProfile'),
  slice('  function _setPollProfileUI(name) {', '\n  }', '_setPollProfileUI'),
  slice('  function _applyPollProfile(name) {', '\n  }', '_applyPollProfile'),
  slice('  function _showCustomStatus(ok, msg) {', '\n  }', '_showCustomStatus'),
  // THE CUSTOM SAVE, lifted as its registered handler rather than re-typed. Its
  // body is the only place the live app builds the save payload, and that
  // payload is the thing worth pinning: it carries every interval as its own key
  // AND the whole set again as a JSON string. A mutant dropping the second half
  // survived this gate until this scenario existed — it saves the intervals and
  // silently loses the preset, which nothing else can see.
  slice("  if (pollCustomSaveBtn) pollCustomSaveBtn.addEventListener('click', function() {",
        '\n  });', 'the custom-save handler'),
  // The POLL HALF of the live `populate(data)`, lifted by its three statements
  // rather than by a function boundary — it has none, being inline in a 200-line
  // populate. The anchor is the `customPollProfile` restore, which is the first
  // of the three and the one whose ORDER matters.
  'function applyPollSettings(data) {\n' +
    slice('    if (data.customPollProfile) {', '_setPollProfileUI(_detectProfile(data));') +
    '\n}',
].join('\n');

const OUT = path.join(ROOT, 'testdata', '.poll-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.poll-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/settings-poll.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const REAL = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'poll-tables.json'), 'utf8'));

// ── The fake DOM ────────────────────────────────────────────────────────────

function makeWorld() {
  const ops = [];
  const nodes = {};
  const stored = [];
  const timers = [];

  function node(id, tag) {
    const kids = [];
    let text = '', html = '', cls = '', value = '';
    const style = new Proxy({}, {
      set(t, k, v) { t[k] = v; ops.push([id, 'style.' + String(k), String(v)]); return true; },
      get(t, k) { return t[k]; },
    });
    const classes = new Set();
    const n = {
      _id: id, tagName: tag || 'DIV', dataset: {}, children: kids, style,
      get textContent() { return text; },
      set textContent(v) { text = String(v); ops.push([id, 'text', text]); },
      get innerHTML() { return html; },
      set innerHTML(v) {
        html = String(v);
        if (v === '') kids.length = 0;
        ops.push([id, 'html', html]);
      },
      get className() { return cls; },
      set className(v) { cls = String(v); ops.push([id, 'class', cls]); },
      get value() { return value; },
      set value(v) { value = String(v); ops.push([id, 'value', value]); },
      classList: {
        add: (c) => classes.add(c), remove: (c) => classes.delete(c),
        contains: (c) => classes.has(c),
        toggle: (c, on) => {
          if (on) classes.add(c); else classes.delete(c);
          ops.push([id, 'classList.toggle', c, !!on]);
        },
        _set: classes,
      },
      appendChild(c) {
        kids.push(c);
        // The APPENDED node's markup, in order. The wrap's own innerHTML never
        // contains its children in this fake, so without this the entire
        // rendered slider list would be invisible to the comparison.
        ops.push([id, 'append', c.tagName, c.className, c.innerHTML, c.textContent]);
        return c;
      },
      addEventListener(ev, fn) { (n._on ||= {})[ev] = fn; },
      _on: {},
    };
    return n;
  }

  // Created lazily: `buildSliders` writes ids that do not exist until it has
  // appended them, and both implementations then look them up.
  function ensure(id, tag) {
    if (!nodes[id]) nodes[id] = node(id, tag);
    return nodes[id];
  }

  for (const id of ['pollSlidersWrap', 'settingsBanner', 'pollCustomSaveBtn',
                    'pollCustomSaveStatus', 'settingsResetBtn']) {
    ensure(id);
  }
  nodes.pollCustomSaveBtn.textContent = 'Save Custom Profile';
  nodes.settingsResetBtn.textContent = 'Reset to Defaults';

  const profileBtns = REAL.offeredProfiles.map((p) => {
    const b = node('btn:' + p, 'BUTTON');
    b.dataset.profile = p;
    return b;
  });

  ops.length = 0;

  const doc = {
    getElementById: (id) => nodes[id] || null,
    createElement: (tag) => {
      // A created element has no id until its innerHTML gives its CHILDREN one.
      // Registering them here is what lets a later `$('s_pollSystem')` find an
      // input that exists only as a string inside a parent's markup.
      const n = node('new:' + tag + ':' + (doc._n = (doc._n || 0) + 1), tag);
      const desc = Object.getOwnPropertyDescriptor(n, 'innerHTML');
      Object.defineProperty(n, 'innerHTML', {
        get: desc.get,
        set(v) {
          desc.set.call(n, v);
          for (const m of String(v).matchAll(/id="(s_|sv_)([A-Za-z0-9_]+)"/g)) {
            const id = m[1] + m[2];
            const child = ensure(id, m[1] === 's_' ? 'INPUT' : 'SPAN');
            const val = String(v).match(new RegExp('id="' + id + '"[^>]*value="([^"]*)"'));
            if (val) child.value = val[1];
            const span = String(v).match(new RegExp('id="' + id + '"[^>]*>([^<]*)<'));
            if (span) child.textContent = span[1];
          }
        },
      });
      return n;
    },
    addEventListener(ev, fn) { (doc._on ||= {})[ev] = fn; },
    querySelectorAll(sel) {
      if (sel === '.poll-profile-btn') return profileBtns;
      throw new Error('unexpected selector: ' + sel);
    },
    _on: {},
  };

  return {
    doc, ops, nodes, stored, timers, profileBtns, ensure,
    localStorage: { setItem: (k, v) => { stored.push([k, v]); }, getItem: () => null },
    setTimeout: (fn, ms) => { timers.push([ms, fn]); return timers.length; },
    runTimers() {
      // Ordered by delay, so a 3000 and a 4000 fire in the order a real clock
      // would rather than in registration order.
      const due = timers.slice().sort((a, b) => a[0] - b[0]);
      timers.length = 0;
      due.forEach(([, fn]) => fn());
    },
    state() {
      return {
        stored,
        nodes: Object.keys(nodes).sort().map((id) => [
          id, nodes[id].textContent, nodes[id].innerHTML, nodes[id].className, nodes[id].value,
        ]),
        active: profileBtns.map((b) => [b.dataset.profile, [...b.classList._set].sort()]),
      };
    },
  };
}

function fetchFor(reply, log) {
  return (url, init) => {
    const body = init && init.body ? JSON.parse(init.body) : null;
    log.push(['fetch', url, body]);
    if (reply === 'reject') return Promise.reject(new Error('network'));
    return Promise.resolve({ json: () => Promise.resolve(reply) });
  };
}

const flush = () => new Promise((r) => setImmediate(r));

// ── The runners ─────────────────────────────────────────────────────────────

async function runLive(sc) {
  const w = makeWorld();
  const log = [];
  const ctx = {
    document: w.doc, Object, Array, JSON, Math, String, Number, Promise, parseInt,
    localStorage: w.localStorage, setTimeout: w.setTimeout, setImmediate,
    $: (id) => w.doc.getElementById(id),
    // `banner` IS A CLOSURE VARIABLE in the live IIFE — `var banner =
    // $('settingsBanner')` runs ONCE, at mount. The port looks the element up
    // per call instead, which is the same answer whenever the element is stable
    // (it is: the markup is static) and a working banner rather than a silent
    // one if the card is ever mounted after the module. Bound here so the lifted
    // function has the same variable its closure gave it.
    banner: w.doc.getElementById('settingsBanner'),
    // The other two closure variables the lifted handler needs.
    pollCustomSaveBtn: w.doc.getElementById('pollCustomSaveBtn'),
    pollCustomSaveStatus: w.doc.getElementById('pollCustomSaveStatus'),
    window: {},
    fetch: fetchFor(sc.reply, log),
    confirm: (m) => { log.push(['confirm', m]); return sc.confirm !== false; },
    POLL_SLIDERS: sc.sliders,
    POLL_PROFILES: JSON.parse(JSON.stringify(sc.profiles)),
    POLL_PROFILE_KEY: REAL.profileKey,
  };
  vm.createContext(ctx);
  vm.runInContext(liveFns, ctx);
  await sc.drive({
    build: (data) => ctx.buildSliders(data),
    apply: (name) => ctx._applyPollProfile(name),
    detect: (data) => ctx._detectProfile(data),
    banner: (t, m) => ctx.showBanner(t, m),
    fmt: (ms) => ctx.fmtMs(ms),
    saveClick: () => w.nodes.pollCustomSaveBtn._on.click(),
    applySettings: (data) => ctx.applyPollSettings(data),
    // Against the table `applyPollSettings` just MUTATED, not an injected copy.
    // That is the whole point of the restore: the Custom button has to move the
    // sliders to the set the operator saved.
    applyDefault: (name) => ctx._applyPollProfile(name),
  }, w, log, flush);
  return { log, ops: w.ops, state: w.state() };
}

async function runPort(sc) {
  const w = makeWorld();
  const log = [];
  const prev = {
    document: globalThis.document, fetch: globalThis.fetch, confirm: globalThis.confirm,
    localStorage: globalThis.localStorage, setTimeout: globalThis.setTimeout,
  };
  globalThis.document = w.doc;
  globalThis.fetch = fetchFor(sc.reply, log);
  globalThis.confirm = (m) => { log.push(['confirm', m]); return sc.confirm !== false; };
  globalThis.localStorage = w.localStorage;
  globalThis.setTimeout = w.setTimeout;
  try {
    delete require.cache[require.resolve(OUT)];
    const m = require(OUT);
    const profiles = JSON.parse(JSON.stringify(sc.profiles));
    await sc.drive({
      build: (data) => m.buildSliders(data, sc.sliders),
      apply: (name) => m.applyPollProfile(name, sc.sliders, profiles),
      detect: (data) => m.detectProfile(data, profiles),
      banner: (t, msg) => m.showBanner(t, msg),
      fmt: (ms) => m.fmtMs(ms),
      // The port registers its handler through the real `initPollAndBanner`,
      // so this drives the same listener a browser would — not a function the
      // page never calls.
      saveClick: () => {
        if (!w.nodes.pollCustomSaveBtn._on.click) m.initPollAndBanner(() => log.push(['reload']));
        return w.nodes.pollCustomSaveBtn._on.click();
      },
      applySettings: (data) => m.applyPollSettings(data),
      applyDefault: (name) => m.applyPollProfile(name),
    }, w, log, flush);
  } finally {
    Object.assign(globalThis, prev);
  }
  return { log, ops: w.ops, state: w.state() };
}

// ── The tables ──────────────────────────────────────────────────────────────

const STREAMED = [
  { key: 'pollSystem', label: 'System / Gauges', min: 1000, max: 30000, step: 1000, unit: 'ms' },
  // THE BRANCH NO REAL ROW REACHES.
  { key: 'pollConns', label: 'Connections', min: 1000, max: 30000, step: 1000, streamed: true },
  { key: 'pollDhcp', label: 'DHCP', min: 10000, max: 600000, step: 10000, unit: 'ms' },
];
const GAPPY_PROFILES = {
  // `slow` deliberately omits pollDhcp: applying it must LEAVE THAT SLIDER ALONE
  // rather than writing undefined into it.
  slow: { pollSystem: 10000 },
  full: { pollSystem: 5000, pollDhcp: 60000 },
};

const SCENARIOS = [
  {
    name: 'the real table renders',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) { api.build({}); },
  },
  {
    name: 'the real table with stored values',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      const data = {};
      REAL.sliders.forEach((s, i) => { data[s.key] = s.min + (i % 4) * s.step; });
      api.build(data);
    },
  },
  {
    name: 'a value BELOW the slider minimum is clamped',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) { api.build({ pollSystem: 1, pollDhcp: 1 }); },
  },
  {
    name: 'a value ABOVE the slider maximum is clamped',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) { api.build({ pollSystem: 999999, pollDhcp: 999999 }); },
  },
  {
    name: 'a STREAMED row renders as Event-driven and gets no input',
    sliders: STREAMED, profiles: GAPPY_PROFILES,
    async drive(api) { api.build({ pollSystem: 2000, pollConns: 3000, pollDhcp: 30000 }); },
  },
  {
    name: 'a profile MISSING a key leaves that slider alone',
    sliders: STREAMED, profiles: GAPPY_PROFILES,
    async drive(api) {
      api.build({ pollSystem: 2000, pollConns: 3000, pollDhcp: 30000 });
      api.apply('slow');
    },
  },
  {
    name: 'a profile covering everything moves every slider',
    sliders: STREAMED, profiles: GAPPY_PROFILES,
    async drive(api) {
      api.build({ pollSystem: 2000, pollConns: 3000, pollDhcp: 30000 });
      api.apply('full');
    },
  },
  {
    name: 'an UNKNOWN profile still becomes the active button',
    sliders: STREAMED, profiles: GAPPY_PROFILES,
    async drive(api) {
      api.build({ pollSystem: 2000, pollDhcp: 30000 });
      api.apply('custom');
    },
  },
  {
    name: 'each real profile applied in turn',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      api.build({});
      for (const name of Object.keys(REAL.profiles)) api.apply(name);
    },
  },
  {
    name: 'detectProfile over every profile, plus a near miss',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api, w, log) {
      for (const [name, p] of Object.entries(REAL.profiles)) {
        log.push(['detect', name, api.detect({ ...p })]);
        log.push(['detect-off-by-one', name, api.detect({ ...p, pollSystem: 12345 })]);
      }
      log.push(['detect-empty', api.detect({})]);
      // Believability: this scenario writes no DOM of its own, so it needs an
      // anchor or the comparison would be over an empty op list.
      api.build({});
    },
  },
  {
    name: 'fmtMs across every boundary',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api, w, log) {
      for (const ms of [0, 1, 999, 1000, 1001, 1500, 1999, 2000, 30000, 59999,
                        60000, 60001, 90000, 120000, 599999, 600000]) {
        log.push(['fmtMs', ms, api.fmt(ms)]);
      }
      api.build({});
    },
  },
  {
    name: 'the custom save sends every interval AND the JSON blob',
    sliders: REAL.sliders, profiles: REAL.profiles,
    reply: { ok: true },
    async drive(api, w, log, f) {
      api.build({});
      api.saveClick();
      await f(); await f(); await f();
      w.runTimers();
    },
  },
  {
    name: 'the custom save with a STREAMED row in the table',
    sliders: STREAMED, profiles: GAPPY_PROFILES,
    reply: { ok: true },
    async drive(api, w, log, f) {
      api.build({ pollSystem: 2000, pollConns: 3000, pollDhcp: 30000 });
      api.saveClick();
      await f(); await f(); await f();
      w.runTimers();
    },
  },
  {
    name: 'the custom save is refused',
    sliders: REAL.sliders, profiles: REAL.profiles,
    reply: { ok: false, error: 'not permitted' },
    async drive(api, w, log, f) {
      api.build({});
      api.saveClick();
      await f(); await f(); await f();
      w.runTimers();
    },
  },
  {
    name: 'the custom save request fails outright',
    sliders: REAL.sliders, profiles: REAL.profiles,
    reply: 'reject',
    async drive(api, w, log, f) {
      api.build({});
      api.saveClick();
      await f(); await f(); await f();
      w.runTimers();
    },
  },
  {
    name: 'applyPollSettings restores a saved custom profile and detects it',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      const custom = {};
      REAL.sliders.forEach((s, i) => { custom[s.key] = s.min + (i % 3) * s.step; });
      api.applySettings({ ...custom, customPollProfile: JSON.stringify(custom) });
    },
  },
  {
    name: 'applyPollSettings over a CORRUPT custom profile still draws the sliders',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      api.applySettings({ customPollProfile: '{not json' });
    },
  },
  {
    name: 'applyPollSettings on a stored set that IS a preset',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) { api.applySettings({ ...REAL.profiles.fast }); },
  },
  {
    name: 'the Custom BUTTON moves the sliders to the restored profile',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      const custom = {};
      REAL.sliders.forEach((s, i) => { custom[s.key] = s.min + (i % 3) * s.step; });
      // Load a DIFFERENT set, so the sliders start somewhere else and the click
      // has to actually move them.
      api.applySettings({ ...REAL.profiles.fast, customPollProfile: JSON.stringify(custom) });
      api.applyDefault('custom');
    },
  },
  {
    name: 'the Custom button after a CORRUPT blob moves nothing',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api) {
      api.applySettings({ ...REAL.profiles.fast, customPollProfile: '{not json' });
      api.applyDefault('custom');
    },
  },
  {
    name: 'the banner: ok clears itself, err stays up',
    sliders: REAL.sliders, profiles: REAL.profiles,
    async drive(api, w, log) {
      api.banner('ok', '✓ Saved');
      log.push(['after ok, before timers', w.nodes.settingsBanner.className]);
      w.runTimers();
      log.push(['after ok, after timers', w.nodes.settingsBanner.className]);
      api.banner('err', 'Reset failed: not permitted');
      w.runTimers();
      log.push(['after err, after timers', w.nodes.settingsBanner.className]);
    },
  },
];

// ── Run and compare ─────────────────────────────────────────────────────────

(async () => {
  let bad = 0, totalOps = 0;
  for (const sc of SCENARIOS) {
    // One case object reaches BOTH runs; a mutating drive would leak the live
    // run's state into the port's and make the gate accuse correct code.
    // See lift.js:freezeCase — this happened once and was hard to see.
    freezeCase(sc);
    // THE LIVE HALF IS FROZEN — see golden() in lib/lift.js.
    const live = await G.live(sc.name, () => runLive(sc));
    const port = await runPort(sc);
    totalOps += live.ops.length;

    if (live.ops.length === 0) {
      console.log('  UNDRIVEN     ' + sc.name + ' — the LIVE side performed no DOM operation');
      bad++;
      continue;
    }
    const a = JSON.stringify({ log: live.log, ops: live.ops, state: live.state }, null, 1);
    const b = JSON.stringify({ log: port.log, ops: port.ops, state: port.state }, null, 1);
    if (a === b) {
      console.log('  ok  ' + sc.name + '  (' + live.ops.length + ' ops)');
      continue;
    }
    bad++;
    console.log('  DIFF  ' + sc.name);
    const al = a.split('\n'), bl = b.split('\n');
    for (let k = 0, shown = 0; k < Math.max(al.length, bl.length) && shown < 12; k++) {
      if (al[k] !== bl[k]) {
        console.log('        live: ' + (al[k] === undefined ? '(end)' : al[k].trim()));
        console.log('        port: ' + (bl[k] === undefined ? '(end)' : bl[k].trim()));
        shown++;
      }
    }
  }
  fs.rmSync(OUT, { force: true });
  console.log('\n' + SCENARIOS.length + ' scenarios, ' + totalOps + ' live DOM operations compared');
  if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
  console.log('all agree');
})();
