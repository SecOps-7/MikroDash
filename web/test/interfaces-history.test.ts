/**
 * The Interfaces modal's traffic history panel (#59).
 *
 * ── THE FAILURE THIS FILE IS ABOUT ──────────────────────────────────────────
 *
 * An interface nobody chose to record has no rows, and an interface that is
 * recorded but idle has no rows either. Drawn the same way, the first reads as
 * a fault: the operator goes looking for a broken collector instead of a switch
 * that was never turned on. So the panel has to SAY which one it is.
 *
 * ── AND IT MUST RENDER FOR A READ-ONLY VIEWER ───────────────────────────────
 *
 * The dialog used to skip its extras slot entirely when the form was read-only.
 * Reading history is a read, so each extra now decides for itself — but the
 * switch inside this one is a WRITE, and that stays behind `mayRecord`, which
 * the server answers rather than the browser guessing.
 *
 * ── IT GOES THROUGH THE REAL DIALOG ─────────────────────────────────────────
 *
 * The panel is reached by opening the resource dialog, not by calling the
 * panel. A panel registered under the wrong resource key, or a dialog that went
 * back to skipping read-only extras, fails here; a direct call to `render`
 * would pass in both cases.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-iface-history.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'test', 'iface-history-entry.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function makeEl(id) {
  const classes = new Set();
  const node = {
    id,
    innerHTML: '',
    textContent: '',
    value: '',
    checked: false,
    disabled: false,
    hidden: false,
    style: {},
    dataset: {},
    children: [],
    listeners: {},
    focus() {},
    appendChild() {},
    insertAdjacentHTML() {},
    addEventListener: (ev, fn) => { (node.listeners[ev] = node.listeners[ev] || []).push(fn); },
    fire: (ev, arg) => (node.listeners[ev] || []).forEach((fn) => fn(arg || {})),
    setAttribute: (k, v) => { node['__' + k] = v; },
    getAttribute: (k) => (('__' + k) in node ? node['__' + k] : null),
    removeAttribute: () => {},
    closest: () => null,
    querySelectorAll: () => [],
    querySelector: () => null,
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
  };
  return node;
}

/**
 * Open the dialog for one interface and hand back what landed in the slot.
 *
 * `reply` is what /api/interfaces/history answers; `readOnly` is the dialog's
 * own flag, which the server sets per viewer.
 */
function open(reply, readOnly, identity, rangeKey) {
  const els = {};
  // ANY id resolves: `show` touches a dozen elements of the dialog chrome, and
  // this test is about one of them. An unknown id returning null would make the
  // dialog skip the slot for the wrong reason.
  const get = (id) => {
    if (!els[id]) els[id] = makeEl(id);
    return els[id];
  };
  const fetched = [];
  const puts = [];
  const modal = makeEl('resModal');
  modal.classList.add('open');
  els.resModal = modal;
  global.document = {
    documentElement: {},
    body: makeEl('body'),
    getElementById: get,
    querySelectorAll: (sel) => (sel === '[data-ifh-range]' ? [] : []),
    querySelector: (sel) => (sel === '.ifh-record' ? els.__recordBtn || null : null),
    addEventListener: () => {},
    createElement: () => makeEl(''),
  };
  global.window = { confirm: () => true };
  // The range is remembered in localStorage, so seeding it is how a test picks
  // one — the same path a returning operator takes. Absent means the default,
  // which is Live.
  global.localStorage = { getItem: () => rangeKey || null, setItem: () => {} };
  global.getComputedStyle = () => ({ getPropertyValue: () => '#38bdf8' });
  // A Chart stub that keeps the config, so the AXIS is assertable. Without it
  // the panel silently skips drawing and a mislabelled axis is invisible here —
  // which is how "12:59 … 13:53" came to mean fifty-four seconds.
  const charts = [];
  // A frame loop that is COUNTED and never actually runs: a real one would
  // spin for the length of the test, and what matters is whether the panel
  // asked for frames at all.
  const frames = { requested: 0, cancelled: 0 };
  global.requestAnimationFrame = () => { frames.requested += 1; return frames.requested; };
  global.cancelAnimationFrame = () => { frames.cancelled += 1; };
  global.Chart = function (canvas, cfg) {
    const inst = {
      // `options` is the live object a real Chart exposes, and the panel writes
      // its animation onto it every tick — so the stub shares it with cfg
      // rather than copying, or the assertions would read a stale snapshot.
      cfg, data: cfg.data, options: cfg.options, destroyed: false,
      destroy() { this.destroyed = true; },
      updates: [],
      update(mode) { this.updates.push(mode); },
    };
    charts.push(inst);
    return inst;
  };
  charts.last = () => charts.filter((c) => !c.destroyed).pop();
  global.fetch = (url, opts) => {
    if (opts && opts.method === 'PUT') {
      puts.push({ url, body: JSON.parse(opts.body) });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) });
    }
    fetched.push(String(url));
    if (String(url).startsWith('/api/routers')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ activeId: 'r1' }) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(reply) });
  };

  delete require.cache[require.resolve(OUT)];
  const mod = require(OUT);

  const handlers = {};
  const emitted = [];
  const socket = {
    on: (ev, fn) => { handlers[ev] = fn; },
    emit: (ev, payload) => { emitted.push({ ev, payload }); },
  };

  mod.initInterfaceHistory(socket);
  if (handlers['router:active']) handlers['router:active']({ activeId: 'r1' });

  // The dialog asks for the schema, then for the row. Both are answered here
  // the way the server answers them.
  mod.openResource(socket, 'iface', identity ? { id: '*1', name: identity } : null);
  if (handlers['res:schema']) {
    handlers['res:schema']({
      key: 'iface', title: 'Interface', permitted: true, fields: [],
    });
  }
  return {
    els, fetched, puts, emitted, mod, charts, frames,
    /**
     * Let everything still in flight finish, then zero the frame counters.
     *
     * A PREVIOUS test's fetch can resolve after this one has installed its own
     * globals, and the stale module then repaints through THIS test's Chart and
     * requestAnimationFrame — one stray chart and one stray frame, attributed
     * to the wrong panel. Measuring from a drained, zeroed point makes the
     * count mean "what this panel did", which is what is being asserted.
     */
    settle: async () => {
      for (let i = 0; i < 3; i += 1) await new Promise((r) => setImmediate(r));
      frames.requested = 0;
      frames.cancelled = 0;
      charts.length = 0;
    },
    /** Flip the recording toggle, as a browser does: the input's `change`
     *  bubbles to the delegated handler carrying the state it landed in. */
     flipRecord: () => {
      const html = get('ifhBody').innerHTML;
      const m = html.match(/<input type="checkbox" class="ifh-record"([^>]*)>/);
      if (!m) throw new Error('no record toggle in: ' + html);
      if (/disabled/.test(m[1])) throw new Error('the toggle is disabled: ' + m[1]);
      const wasOn = /checked/.test(m[1]);
      get('ifhBody').fire('change', {
        target: {
          classList: { contains: (c) => c === 'ifh-record' },
          checked: !wasOn,
        },
      });
    },
    deliverRow: () => {
      // openResource resolves the schema through a promise, so the row is
      // delivered on the next turn of the microtask queue.
      handlers['res:row']({
        resource: 'iface', id: '*1', identity, values: {}, readOnly,
        options: {}, actions: [], removable: false,
      });
    },
    slot: () => get('res_extra').innerHTML,
    body: () => get('ifhBody').innerHTML,
  };
}

/**
 * The recording toggle as rendered: absent, or on/off and movable or not.
 *
 * Read out of the markup rather than by asking the panel, because the question
 * is what an operator is shown — a toggle drawn in the wrong state is exactly
 * the bug worth catching, and it would agree with the panel's own view of
 * itself.
 */
function toggle(html) {
  const m = html.match(/<input type="checkbox" class="ifh-record"([^>]*)>/);
  if (!m) return null;
  return { on: /checked/.test(m[1]), disabled: /disabled/.test(m[1]) };
}

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

check('an unrecorded interface explains itself instead of drawing nothing', async (t) => {
  const d = open({ ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5', '24h');
  await Promise.resolve();
  d.deliverRow();
  assert.ok(d.slot().includes('class="ifh"'), 'the panel did not render: ' + d.slot());
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('not being recorded'),
    'an unrecorded interface must say so, or an empty chart reads as a fault: ' + body);
  assert.deepEqual(toggle(body), { on: false, disabled: false },
    'somebody who may record it was not offered a movable, off toggle: ' + body);
});

check('a viewer who may not record is told, not offered a control that fails', async () => {
  const d = open({ ok: true, recorded: false, mayRecord: false, defaultIf: 'ether1' },
    true, 'ether5', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('not being recorded'), body);
  assert.equal(toggle(body), null,
    'a read-only viewer was offered a write the server would refuse: ' + body);
});

// THE READ-ONLY CASE RENDERS AT ALL. This is the half the dialog used to decide
// on the extra's behalf, and getting it wrong hides a chart from somebody the
// server would have answered.
check('the panel still renders on a read-only form', async () => {
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, true, 'ether1', '24h');
  await Promise.resolve();
  d.deliverRow();
  assert.ok(d.slot().includes('class="ifh"'),
    'the slot was empty for a read-only viewer: ' + JSON.stringify(d.slot()));
});

check('a recorded interface with no rows says it is idle, not unrecorded', async () => {
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, false, 'ether1', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('No traffic recorded'),
    'a recorded but idle interface should say it is idle: ' + body);
  assert.ok(!body.includes('not being recorded'),
    'a RECORDED interface was described as unrecorded: ' + body);
});

// THE REQUEST NAMES THE INTERFACE AND THE RANGE. The server refuses an unknown
// range and owns the aggregation, so the one thing the browser must get right
// is asking about the interface it is showing.
check('the request names the interface and the range', async () => {
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, false, 'ether 5', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const call = d.fetched.find((u) => u.startsWith('/api/interfaces/history'));
  assert.ok(call, 'the panel never asked for any history: ' + JSON.stringify(d.fetched));
  assert.ok(call.includes('interface=ether%205'),
    'the interface name was not encoded into the query: ' + call);
  assert.ok(/range=(1h|24h|7d|30d)/.test(call), 'no range was sent: ' + call);
});

// AN INTERFACE THAT USED TO BE RECORDED STILL HAS A HISTORY, and it must be
// drawn. Found live, not here: ether3 came back recorded:false with an hour row
// inside the 30-day range, because the hourly rollups outlive the switch being
// turned off. The panel asked `recorded` before it asked whether there was
// anything to draw, and hid a real chart behind "not being recorded".
check('history kept from before recording was turned off is still drawn', async () => {
  const d = open({
    ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1',
    rows: [{ ts: 1790000000000, rx_mbps: 5, tx_mbps: 2 }],
    rxTotalMb: 40, txTotalMb: 12, rxMaxMbps: 9, txMaxMbps: 3,
  }, false, 'ether3', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('ifhChart'),
    'an interface with rows drew no chart because recording is off now: ' + body);
  assert.ok(body.includes('Recording is off'),
    'the chart must say nothing new is being added to it: ' + body);
  assert.ok(!body.includes('not being recorded</div>'),
    'it showed the empty-state explanation over a chart it had data for: ' + body);
});

// AN ADD FORM HAS NO INTERFACE, so there is nothing to show a history for.
check('an add form draws no history panel', async () => {
  const d = open({ ok: true, recorded: true, defaultIf: 'ether1' }, false, null, '24h');
  await Promise.resolve();
  assert.ok(!d.slot().includes('class="ifh"'),
    'a form for an interface that does not exist yet drew a history panel');
});

// ── LIVE ───────────────────────────────────────────────────────────────────
//
// Live is the DEFAULT and is drawn from the browser's own buffer, so it works
// for an interface nothing is recording. That is the whole point of it: the
// page can already answer "what is this doing right now" for anything, and
// making that wait on a recording switch would be an arbitrary refusal.

check('Live is the default range', async () => {
  const d = open({ ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  const active = d.slot().match(/class="ifh-range active" data-ifh-range="([^"]+)"/);
  assert.ok(active, 'no range is marked active: ' + d.slot());
  assert.equal(active[1], 'live');
});

check('Live draws for an interface that is not recorded', async () => {
  const d = open({ ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  // Before any sample there is nothing to draw, and the panel says which.
  assert.ok(d.body().includes('Waiting for the first live sample'),
    'an empty buffer should say it is waiting: ' + d.body());
  d.mod.recordLiveSamples([
    { name: 'ether5', rxMbps: 12, txMbps: 3 },
    { name: 'ether1', rxMbps: 1, txMbps: 1 },
  ]);
  // The chart is drawn from the buffer alone and does not wait for the server.
  assert.ok(d.body().includes('ifhChart'),
    'Live waited for the history reply before drawing: ' + d.body());
  // The recording NOTE does wait for it, since only the server knows. Let the
  // reply land before asking about it.
  await new Promise((r) => setImmediate(r));
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 12, txMbps: 3 }]);
  const body = d.body();
  assert.ok(body.includes('ifhChart'),
    'Live drew no chart for an unrecorded interface, which is the one thing it '
    + 'must always do: ' + body);
  assert.ok(body.includes('Now down'), 'the live stats are missing: ' + body);
  // AND IT SAYS WHY THE OTHER RANGES ARE EMPTY, rather than leaving the
  // operator to wonder whether the whole panel is broken.
  assert.ok(body.includes('Not recorded'), body);
  assert.deepEqual(toggle(body), { on: false, disabled: false }, body);
});

check('a live sample for another interface does not draw here', async () => {
  const d = open({ ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  d.mod.recordLiveSamples([{ name: 'ether9', rxMbps: 99, txMbps: 99 }]);
  assert.ok(d.body().includes('Waiting for the first live sample'),
    'another interface\'s traffic was drawn under this one: ' + d.body());
});

// THE LIVE AXIS SAYS HOW LONG AGO, NOT WHAT TIME.
//
// It was mm:ss, and on the 60-second view that reads as a clock: the axis ran
// "12:59 … 13:53" for fifty-four SECONDS of traffic. The numbers were right and
// the axis was a lie, which a test that only checked the numbers cannot see.
check('a live window is labelled by age', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 5, txMbps: 2 }]);
  const x = d.charts.last().cfg.options.scales.x;
  const labels = [x.min, (x.min + x.max) / 2, x.max].map((v) => x.ticks.callback(v));
  labels.forEach((l) => {
    assert.ok(/^(now|-\d+[sm])$/.test(l),
      'a live label reads as a time of day rather than an age: ' + JSON.stringify(labels));
  });
});

// AND A RECORDED RANGE KEEPS THE CLOCK, which is what an operator lines an
// incident up against.
check('an hourly window is labelled by the clock', async () => {
  const d = open({
    ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1', recordedIfaces: ['ether5'],
    rows: [{ ts: 1790000000000, rx_mbps: 5, tx_mbps: 2 }],
  }, false, 'ether5', '1h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const x = d.charts.last().cfg.options.scales.x;
  const label = x.ticks.callback(1790000000000);
  assert.ok(/^\d\d:\d\d$/.test(label),
    'an hourly label is not a clock time: ' + JSON.stringify(label));
});

// ── SMOOTHNESS, AND WHAT IT IS NOT ─────────────────────────────────────────
//
// The first attempt animated Chart.js between data updates. On an index-based
// axis that interpolates every point's VALUE towards its neighbour's, which is
// not a scroll: the whole line slides vertically at once and reads as the chart
// morphing. The operator's words were "looks like its morphing constantly".
//
// So nothing is tweened. The points carry their real timestamps and the WINDOW
// moves, which is rigid by construction.

check('nothing is tweened, because a tween on this axis morphs the line', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 1, txMbps: 1 }]);
  assert.equal(d.charts.last().cfg.options.animation, false,
    'an animation on a value axis morphs the line instead of scrolling it');
});

// THE WINDOW IS ITS FULL WIDTH FROM THE FIRST SAMPLE. It was one category per
// point, so five seconds of traffic stretched across the whole chart and the
// trace sat against the left edge from the start: "the graph is already pinned
// to the left edge, instead of drawing towards it".
check('a live window spans its whole range even with one sample', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 7, txMbps: 2 }]);
  const x = d.charts.last().cfg.options.scales.x;
  assert.equal(x.type, 'linear', 'an index axis cannot hold a point at its real time');
  assert.equal(x.max - x.min, 60000, 'the Live window is not 60 seconds wide');
  // AND THE ONE POINT SITS AT THE RIGHT-HAND END, not spread across the chart.
  const pt = d.charts.last().cfg.data.datasets[0].data[0];
  assert.ok(pt.x > x.min + 50000,
    'the only sample is sitting near the left edge; it will draw from the left '
    + 'instead of towards it');
});

// THE NEWEST SECOND IS HELD OFF-SCREEN.
//
// The right-hand end of the line is the part still being drawn — a sample
// lands, its segment appears, another follows. Flush against the frame that is
// a twitching stub at the edge, so the window ends one sample interval short of
// now and the leading edge is complete by the time it is visible. The same gap
// the Dashboard's traffic charts hold open, from the same constant.
check('the visible window stops short of now, hiding the leading edge', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  const before = Date.now();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 9, txMbps: 3 }]);
  const x = d.charts.last().cfg.options.scales.x;
  assert.ok(before - x.max >= 900,
    'the window runs right up to now, so the segment being drawn is on screen: '
    + (before - x.max) + 'ms of gap');
  assert.ok(before - x.max <= 1500, 'the gap is wider than one sample interval');
  // AND THE WINDOW IS STILL ITS FULL WIDTH: the gap shifts it, never shrinks it.
  assert.equal(x.max - x.min, 60000);
});

check('the window scrolls on animation frames, not on samples', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  await d.settle();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 1, txMbps: 1 }]);
  assert.ok(d.frames.requested > 0,
    'no frame was requested, so the window only moves when a sample lands — '
    + 'which is the once-a-second jump this replaced');
});

// NOT FOR THE 30-MINUTE WINDOW: it advances about a third of a pixel a second,
// so a frame-rate redraw of 1800 points would be paid for motion no one sees.
check('the 30-minute window does not run a frame loop', async () => {
  const d = open({ ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5', '30m');
  await Promise.resolve();
  d.deliverRow();
  await d.settle();
  d.mod.recordLiveSamples([{ name: 'ether5', rxMbps: 1, txMbps: 1 }]);
  assert.equal(d.frames.requested, 0,
    'the 30-minute window is redrawing every frame for 0.3 px/s of movement');
});

// ── THE SWITCH GOES BOTH WAYS ──────────────────────────────────────────────

check('a recorded interface has the toggle ON, and movable', async () => {
  const d = open({
    ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1',
    recordedIfaces: ['ether5'], rows: [],
  }, false, 'ether5', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(toggle(d.body()), { on: true, disabled: false },
    'recording could be turned on and never off: ' + d.body());
});

check('switching the toggle off sends the list without this interface', async () => {
  const d = open({
    ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1',
    recordedIfaces: ['ether2', 'ether5'], rows: [],
  }, false, 'ether5', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  d.flipRecord();
  await new Promise((r) => setImmediate(r));
  assert.equal(d.puts.length, 1, 'no write was sent: ' + JSON.stringify(d.puts));
  assert.deepEqual(d.puts[0].body.recordedIfaces, ['ether2'],
    'switching ether5 off must leave the others alone: ' + JSON.stringify(d.puts[0].body));
});

check('switching the toggle on appends to the stored list', async () => {
  const d = open({
    ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1',
    recordedIfaces: ['ether2'], rows: [],
  }, false, 'ether5', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  d.flipRecord();
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(d.puts[0].body.recordedIfaces, ['ether2', 'ether5']);
});

// THE WAN HAS NO SWITCH. It is recorded because it is the default interface and
// the resolver puts it back whatever the stored list says, so a control that
// appeared to turn it off would lie.
check('the WAN toggle is on and cannot be moved', async () => {
  const d = open({
    ok: true, recorded: true, mayRecord: true, defaultIf: 'ether1',
    recordedIfaces: [], rows: [],
  }, false, 'ether1', '24h');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.deepEqual(toggle(body), { on: true, disabled: true },
    'the WAN toggle must show ON and refuse to move, since the resolver puts it '
    + 'back whatever the stored list says: ' + body);
  assert.ok(/WAN/.test(body), 'it should say why it cannot be moved: ' + body);
});

(async () => {
  for (const c of checks) {
    try {
      await c.fn();
      say('  ok  ' + c.name);
    } catch (e) {
      failed += 1;
      say('  FAIL ' + c.name + '\n       ' + (e && e.message));
    }
  }
  if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
  say('\nall passed');
})();
