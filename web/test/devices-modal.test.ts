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
    setAttribute: (k, v) => { n['attr_' + k] = v; }, textContent: '',
  };
  return n;
}
const nodes = {};
['deviceModal', 'dvmHdr', 'dvmDetails', 'dvmConn', 'dvmUsage', 'dvmRes', 'dvmPorts', 'dvmAlerts',
  'dvmTabsCard', 'dvmTabClients', 'dvmTabAlerts', 'dvmClientsPane', 'dvmClientsCount', 'dvmClientsBody',
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
  tempC: null, uptime: '1d', wanIf: 'ether1', points: [], ports: [], portsRead: false, leases: null,
  clientsAllowed: true, clientsSent: false, clients: [] }, extra || {});

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

// ── the Clients tab ─────────────────────────────────────────────────────────
//
// The list arrives only when it CHANGED (clientsSent), so a frame without it must
// keep the table, not blank it. Sorted by address NUMERICALLY by default: .10
// after .9. A viewer without DHCP read gets no Clients tab, and the card falls
// back to Recent alerts.
{
  const c = (ip, host) => ({ hostName: host, ip, mac: '02:00:00:00:00:01', vlanId: '' });
  handlers['device:live'](frame('r2', { clientsSent: true,
    clients: [c('198.51.100.10', 'ten'), c('198.51.100.9', 'nine')] }));
  const body = nodes.dvmClientsBody.innerHTML;
  assert.ok(body.indexOf('nine') < body.indexOf('ten') && body.indexOf('nine') !== -1,
    'the client list is not ordered by address numerically (.9 before .10)');
  assert.strictEqual(nodes.dvmClientsCount.textContent, '2', 'the Clients tab does not count the list');
  handlers['device:live'](frame('r2'));
  assert.strictEqual(nodes.dvmClientsBody.innerHTML, body, 'a frame without the list blanked or redrew the table');
  assert.strictEqual(nodes.dvmClientsPane.hidden, false, 'the Clients pane is not showing');
  handlers['device:live'](frame('r2', { clientsAllowed: false }));
  assert.strictEqual(nodes.dvmTabClients.hidden, true, 'a viewer without DHCP read still has a Clients tab');
  assert.strictEqual(nodes.dvmClientsPane.hidden, true, 'the Clients pane stayed open without DHCP read');
  say('ok  clients: ordered by address, kept between lists, gone without DHCP read');
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
  assert.match(V.usageHeadHtml(off, undefined), /Offline: no live throughput/);
  // Ports "read and none" is not "not read".
  assert.match(V.portsSectionHtml(frame('r1', { portsRead: true })), /No ethernet ports/);
  // RE-AIMED 2026-10-01: the chart is a Chart.js canvas scrolled by a frame
  // loop now (devices-modal-chart.ts), not SVG in the repaint; the header
  // carries the current rates.
  const head = V.usageHeadHtml(frame('r1'), { ts: now, rx_mbps: 12, tx_mbps: 3 });
  assert.match(head, /12\.00 Mbps/);
  assert.match(head, /3\.00 Mbps/);
  // An empty client list says so, and differently when offline.
  assert.match(V.clientsBodyHtml([], true), /No active DHCP leases/);
  assert.match(V.clientsBodyHtml([], false), /Offline/);
  assert.ok(!/<img/.test(V.clientsBodyHtml(V.clientRows([{ hostName: '<img src=x>', ip: '1.2.3.4', mac: '', vlanId: '<b>' }]), true)),
    'a client hostname reached the markup unescaped');
  // A viewer who may not read reports gets no alerts section at all.
  assert.strictEqual(V.alertsHtml(null), '');
  assert.match(V.alertsHtml([]), /No alerts in the last 7 days/);
  // Escaping.
  const evil = '<img src=x>';
  const html = V.alertsHtml([{ id: 1, alert_type: evil, subject: evil, fired_at: now }]);
  assert.ok(!/<img/.test(html), 'an alert subject reached the markup unescaped');
  say('ok  the views: offline is said, not zeroed; the rates are drawn; alerts escape');
}
fs.rmSync(VOUT, { force: true });
// ── the chart's buffer ──────────────────────────────────────────────────────
//
// No Chart.js under node, so this is the buffer half: the first frame seeds it,
// later frames append, points older than the window are dropped, and a stop
// empties it - so a modal reopened on another device never inherits a line.
const COUT = path.join(ROOT, 'testdata', '.dvchart.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-modal-chart.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + COUT, '--log-level=warning'],
  { stdio: 'inherit' });
const W = require(COUT);
{
  const now = Date.now();
  W.startWanChart({}, [{ ts: now - 2000, rx_mbps: 1, tx_mbps: 1 }, { ts: now - 1000, rx_mbps: 2, tx_mbps: 1 }]);
  assert.strictEqual(W.wanPoints().length, 2, 'the first frame did not seed the buffer');
  W.pushWanPoints([{ ts: now, rx_mbps: 3, tx_mbps: 1 }]);
  assert.strictEqual(W.wanPoints().length, 3, 'a later frame was not appended');
  W.pushWanPoints([{ ts: now + 1000, rx_mbps: 3, tx_mbps: 1 }]);
  W.startWanChart({}, [{ ts: now - 400_000, rx_mbps: 9, tx_mbps: 9 }, { ts: now, rx_mbps: 1, tx_mbps: 1 }]);
  W.pushWanPoints([{ ts: now + 1000, rx_mbps: 1, tx_mbps: 1 }]);
  assert.ok(W.wanPoints().every((p) => p.ts > now - 320_000), 'a point older than the window was kept');
  W.stopWanChart();
  assert.strictEqual(W.wanPoints().length, 0, 'stopping left the old device\u2019s line in the buffer');
  say('ok  the chart buffer: seeded, appended, windowed, emptied on stop');
}
fs.rmSync(COUT, { force: true });
say('devices-modal: all checks passed');
