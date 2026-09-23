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
function open(reply, readOnly, identity) {
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
  global.localStorage = { getItem: () => null, setItem: () => {} };
  global.getComputedStyle = () => ({ getPropertyValue: () => '#38bdf8' });
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
    els, fetched, puts, emitted,
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

let failed = 0;
const checks = [];
function check(name, fn) { checks.push({ name, fn }); }

check('an unrecorded interface explains itself instead of drawing nothing', async (t) => {
  const d = open({ ok: true, recorded: false, mayRecord: true, defaultIf: 'ether1' },
    false, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  assert.ok(d.slot().includes('Traffic history'), 'the panel did not render: ' + d.slot());
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('not being recorded'),
    'an unrecorded interface must say so, or an empty chart reads as a fault: ' + body);
  assert.ok(body.includes('Record this interface'),
    'somebody who may record it was not offered the switch: ' + body);
});

check('a viewer who may not record is told, not offered a control that fails', async () => {
  const d = open({ ok: true, recorded: false, mayRecord: false, defaultIf: 'ether1' },
    true, 'ether5');
  await Promise.resolve();
  d.deliverRow();
  await new Promise((r) => setImmediate(r));
  const body = d.body();
  assert.ok(body.includes('not being recorded'), body);
  assert.ok(!body.includes('Record this interface'),
    'a read-only viewer was offered a write the server would refuse: ' + body);
});

// THE READ-ONLY CASE RENDERS AT ALL. This is the half the dialog used to decide
// on the extra's behalf, and getting it wrong hides a chart from somebody the
// server would have answered.
check('the panel still renders on a read-only form', async () => {
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, true, 'ether1');
  await Promise.resolve();
  d.deliverRow();
  assert.ok(d.slot().includes('Traffic history'),
    'the slot was empty for a read-only viewer: ' + JSON.stringify(d.slot()));
});

check('a recorded interface with no rows says it is idle, not unrecorded', async () => {
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, false, 'ether1');
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
  const d = open({ ok: true, recorded: true, rows: [], defaultIf: 'ether1' }, false, 'ether 5');
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
  }, false, 'ether3');
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
  const d = open({ ok: true, recorded: true, defaultIf: 'ether1' }, false, null);
  await Promise.resolve();
  assert.ok(!d.slot().includes('Traffic history'),
    'a form for an interface that does not exist yet drew a history panel');
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
