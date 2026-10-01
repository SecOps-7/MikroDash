/**
 * THE DEVICE MODAL STREAMS WHILE OPEN, AND ONLY THEN.
 *
 *   - Opening sends ONE `device:peek` for that device.
 *   - Opening another device moves the stream: `device:unpeek`, then a peek.
 *   - EVERY close path converges on one `device:unpeek`. The ×, Escape and a
 *     backdrop click all just remove the `open` class (modals.ts), so the class
 *     is what the modal watches - and closing twice must not unpeek twice.
 *   - A `device:live` frame for any other device is IGNORED: after a switch, a
 *     frame for the old device can still be in flight.
 *
 * The document here is a minimal stand-in with a working MutationObserver on
 * `class`, because that observer IS the mechanism under test.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.dvmodal.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-modal.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// ── a document just large enough ────────────────────────────────────────────
const observers = [];
function node(id) {
  const cls = new Set();
  const n = {
    id, innerHTML: '', hidden: false,
    classList: {
      add: (c) => { cls.add(c); observers.filter((o) => o.target === n).forEach((o) => o.cb()); },
      remove: (c) => { cls.delete(c); observers.filter((o) => o.target === n).forEach((o) => o.cb()); },
      contains: (c) => cls.has(c),
      toggle: () => {},
    },
    addEventListener: () => {}, querySelector: () => null, querySelectorAll: () => [],
  };
  return n;
}
const nodes = {};
['deviceModal', 'dvmHdr', 'dvmDetails', 'dvmConn', 'dvmUsage', 'dvmRes', 'dvmPorts', 'dvmAlerts',
  'routers-grid', 'routersListBody', 'page-devices'].forEach((id) => { nodes[id] = node(id); });
global.document = {
  getElementById: (id) => nodes[id] || null,
  addEventListener: () => {}, querySelector: () => null, querySelectorAll: () => [], hidden: false,
};
global.window = { addEventListener: () => {} };
global.MutationObserver = class {
  constructor(cb) { this.cb = cb; }
  observe(target) { observers.push({ target, cb: this.cb }); }
};
global.fetch = async () => ({ ok: false, json: async () => ({}) });

const M = require(OUT);

const emitted = [];
const handlers = {};
const socket = {
  emit: (ev, data) => emitted.push(data === undefined ? ev : ev + ' ' + data),
  on: (ev, cb) => { handlers[ev] = cb; },
};
M.mountDeviceModal(socket, { openDashboard: () => {}, canEdit: () => false, edit: () => {} });

const frame = (id, extra) => Object.assign({ routerId: id, connected: true, cpu: 12, memPct: 30, hddPct: 40,
  tempC: null, uptime: '1d', wanIf: 'ether1', points: [], ports: [], portsRead: false, leases: null }, extra || {});

// ── open, switch, close ─────────────────────────────────────────────────────
{
  M.openDeviceModal('r1');
  assert.deepStrictEqual(emitted, ['device:peek r1'], 'opening did not peek exactly once');
  assert.ok(nodes.deviceModal.classList.contains('open'));
  M.openDeviceModal('r1');
  assert.deepStrictEqual(emitted, ['device:peek r1'], 're-opening the open device peeked again');

  M.openDeviceModal('r2');
  assert.deepStrictEqual(emitted.slice(1), ['device:unpeek', 'device:peek r2'],
    'opening another device did not move the stream');
  say('ok  open peeks once, and another device moves the stream');
}

// ── frames for another device are ignored ───────────────────────────────────
{
  handlers['device:live'](frame('r1', { cpu: 99 }));
  assert.ok(/dvm-skel/.test(nodes.dvmRes.innerHTML),
    'a frame for the device just left was drawn under the open one');
  handlers['device:live'](frame('r2', { cpu: 12 }));
  assert.ok(/12%/.test(nodes.dvmRes.innerHTML), 'the open device’s frame was not drawn');
  say('ok  a frame for another device is ignored');
}

// ── every close converges on one unpeek ─────────────────────────────────────
{
  const before = emitted.length;
  nodes.deviceModal.classList.remove('open');      // what ×, Escape and the backdrop do
  assert.deepStrictEqual(emitted.slice(before), ['device:unpeek'], 'closing did not unpeek');
  nodes.deviceModal.classList.remove('open');
  assert.strictEqual(emitted.length, before + 1, 'closing twice unpeeked twice');
  assert.strictEqual(M.openDeviceId(), '', 'the modal still thinks a device is open');
  handlers['device:live'](frame('r2', { cpu: 77 }));
  assert.ok(!/77%/.test(nodes.dvmRes.innerHTML), 'a frame after closing was drawn');
  say('ok  every close is one unpeek, and nothing is drawn after it');
}

fs.rmSync(OUT, { force: true });

// ── the views ───────────────────────────────────────────────────────────────

const VOUT = path.join(ROOT, 'testdata', '.dvviews.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-modal-views.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + VOUT, '--log-level=warning'],
  { stdio: 'inherit' });
const V = require(VOUT);
{
  const now = 1_000_000_000_000;
  // Offline with nothing read says so, rather than drawing zeros.
  const off = frame('r1', { connected: false, cpu: null, memPct: null, hddPct: null });
  assert.match(V.resourcesHtml(off), /Offline: no reading/);
  assert.match(V.portsSectionHtml(off), /Offline: no port reading/);
  assert.match(V.usageHtml(off, [], now), /Offline: no live throughput/);
  // Ports "read and none" is not "not read".
  assert.match(V.portsSectionHtml(frame('r1', { portsRead: true })), /No ethernet ports/);
  // The chart draws Rx and Tx, and only the last five minutes.
  const pts = [{ ts: now - 400_000, rx_mbps: 999, tx_mbps: 999 },
    { ts: now - 2000, rx_mbps: 10, tx_mbps: 2 }, { ts: now - 1000, rx_mbps: 12, tx_mbps: 3 }];
  const svg = V.wanChartSvg(pts, now);
  assert.match(svg, /dvm-rx-line/);
  assert.match(svg, /dvm-tx-line/);
  assert.ok(!/999/.test(svg) && !/1\.15 Gbps/.test(svg), 'a sample older than the window set the scale');
  // A viewer who may not read reports gets no alerts section at all.
  assert.strictEqual(V.alertsHtml(null), '');
  assert.match(V.alertsHtml([]), /No alerts in the last 7 days/);
  // Escaping.
  const evil = '<img src=x>';
  const html = V.alertsHtml([{ id: 1, alert_type: evil, subject: evil, fired_at: now }]);
  assert.ok(!/<img/.test(html), 'an alert subject reached the markup unescaped');
  say('ok  the views: offline is said, not zeroed; the chart keeps its window; alerts escape');
}
fs.rmSync(VOUT, { force: true });
say('devices-modal: all checks passed');
