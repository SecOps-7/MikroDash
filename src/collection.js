'use strict';
// Per-router collection configuration (#105).
//
// Global settings supply the default poll intervals. Everything about HOW a
// collector delivers (stream vs poll) and WHETHER it runs at all is per-router,
// because a fleet is not uniform: a hAP ac2 acting as an access point has no
// routed traffic, no Kid Control devices and no wireless registrations, yet it
// was asked for the same ~17 concurrent streams as a 1 GB hAP ax3. The evidence
// in #104 points at concurrent open channels rather than data volume, so being
// able to switch a router to polling, or to turn a collector off entirely, is
// the lever that matters.
//
// Deliberately pure: no I/O. This is the single source of truth that index.js,
// routers.js and alertSessions.js all resolve through, so there is exactly one
// place holding the precedence rules and exactly one list of collectors.

const { POLL_BOUNDS } = require('./settings');

/**
 * The collector registry. Everything else in this feature derives from it: the
 * modal checkboxes, the null-collector stub, the client card map, the
 * diagnostics list and the tests. Adding a collector here should be the only
 * edit needed to bring it into the feature.
 *
 *   key           identifier used in `off` and in the resolved maps
 *   sessionProp   property name on the session object built by buildSession()
 *   pollKey       settings.json interval key, or null when it has no global one
 *   defaultPollMs fallback used when pollKey is null
 *   streamKey     per-router override key; null when the collector never streams
 *   pollable      whether a poll path exists (or is being built) for it
 *   disableable   whether the user may turn it off
 *   requires      collectors whose data it cannot work without
 *   cards         dashboard card ids, so the client can mark them
 */
// `page` is the page whose view this collector feeds — the single source for the
// collector↔page edge (issue #108). null means the collector belongs to no one
// page: traffic and system drive the header gauges on every page, and arp emits
// nothing at all, it only feeds other collectors' name resolution. A collector
// that also surfaces as a dashboard card says so through `cards`, not by
// claiming a second page.
const COLLECTORS = Object.freeze([
  // ── Protected: read directly by other collectors, or feed stored history ───
  { key: 'traffic', label: 'Traffic',      sessionProp: 'traffic',      pollKey: null,           defaultPollMs: 1000,
    streamKey: null,             pollable: false, disableable: false, requires: [], page: null, cards: ['trafficCard'] },
  { key: 'system', label: 'System / Gauges',       sessionProp: 'system',       pollKey: 'pollSystem',   defaultPollMs: 2000,
    streamKey: 'streamSystem',   pollable: true,  disableable: false, requires: [], page: null, cards: ['systemCard'] },
  { key: 'arp', label: 'ARP',          sessionProp: 'arp',          pollKey: 'pollArp',      defaultPollMs: 30000,
    streamKey: 'streamArp',      pollable: true,  disableable: false, requires: [], page: null, cards: [] },
  { key: 'dhcpLeases', label: 'DHCP Leases',   sessionProp: 'dhcpLeases',   pollKey: 'pollDhcp',     defaultPollMs: 600000,
    streamKey: 'streamLeases',   pollable: true,  disableable: false, requires: [], page: 'dhcp', cards: [] },
  { key: 'dhcpNetworks', label: 'DHCP Networks', sessionProp: 'dhcpNetworks', pollKey: 'pollDhcp',     defaultPollMs: 600000,
    streamKey: 'streamDhcp',     pollable: true,  disableable: false, requires: [], page: 'dhcp', cards: ['networksCard'] },

  // ── Disableable ────────────────────────────────────────────────────────────
  { key: 'conns', label: 'Connections',        sessionProp: 'conns',        pollKey: 'pollConns',    defaultPollMs: 5000,
    streamKey: 'streamConns',    pollable: true,  disableable: true,  requires: [], page: 'connections', cards: ['connCard'] },
  { key: 'bandwidth', label: 'Bandwidth',    sessionProp: 'bandwidth',    pollKey: 'pollBandwidth', defaultPollMs: 5000,
    streamKey: null,             pollable: true,  disableable: true,  requires: ['conns'], page: 'bandwidth', cards: ['bandwidthCard'] },
  { key: 'talkers', label: 'Top Talkers',      sessionProp: 'talkers',      pollKey: 'pollTalkers',  defaultPollMs: 3000,
    streamKey: 'streamTalkers',  pollable: true,  disableable: true,  requires: [], page: 'dashboard', cards: ['talkersCard'] },
  { key: 'ifStatus', label: 'Interface Rates',     sessionProp: 'ifStatus',     pollKey: 'pollIfstatus', defaultPollMs: 5000,
    streamKey: 'streamIfrates',  pollable: true,  disableable: true,  requires: [], page: 'interfaces', cards: ['ifStatusCard'] },
  { key: 'ping', label: 'Ping',         sessionProp: 'ping',         pollKey: 'pollPing',     defaultPollMs: 5000,
    streamKey: 'streamPing',     pollable: true,  disableable: true,  requires: [], page: 'dashboard', cards: [] },
  { key: 'wireless', label: 'Wireless',     sessionProp: 'wireless',     pollKey: 'pollWireless', defaultPollMs: 30000,
    streamKey: 'streamWireless', pollable: true,  disableable: true,  requires: [], page: 'wireless', cards: ['wirelessCard'] },
  { key: 'vpn', label: 'VPN',          sessionProp: 'vpn',          pollKey: 'pollVpn',      defaultPollMs: 10000,
    streamKey: 'streamVpn',      pollable: true,  disableable: true,  requires: [], page: 'vpn', cards: ['vpnCard'] },
  { key: 'firewall', label: 'Firewall',     sessionProp: 'firewall',     pollKey: 'pollFirewall', defaultPollMs: 5000,
    streamKey: 'streamFirewall', pollable: true,  disableable: true,  requires: [], page: 'firewall', cards: ['firewallCard'] },
  { key: 'routing', label: 'Routing',      sessionProp: 'routing',      pollKey: 'pollRouting',  defaultPollMs: 10000,
    streamKey: 'streamRouting',  pollable: true,  disableable: true,  requires: [], page: 'routing',
    cards: ['routingProtoCard', 'routingBgpCard', 'routingPeersCard', 'routingRoutesCard'] },
  { key: 'netwatch', label: 'NetWatch',     sessionProp: 'netwatch',     pollKey: null,           defaultPollMs: 30000,
    streamKey: 'streamNetwatch', pollable: true,  disableable: true,  requires: [], page: 'dashboard', cards: ['netwatchCard'] },
  { key: 'topology', label: 'Network Topology', sessionProp: 'topology', pollKey: 'pollTopology', defaultPollMs: 30000,
    streamKey: 'streamTopology', pollable: true,  disableable: true,  requires: [], page: 'topology', cards: ['topologyCard'] },
  // requires: [] on purpose for vlans. It reads live rates out of ifStatus and
  // client counts out of dhcpLeases, but declaring those here would cascade into
  // a hard disable — turning off Interface Rates would blank the whole VLANs
  // page, when membership, trunk ports and client counts are all still there.
  // Degrade the rates, not the page.
  { key: 'vlans', label: 'VLANs',        sessionProp: 'vlans',        pollKey: 'pollVlans',    defaultPollMs: 5000,
    streamKey: 'streamVlans',    pollable: true,  disableable: true,  requires: [], page: 'vlans', cards: [] },
  { key: 'ppp',   label: 'PPP',          sessionProp: 'ppp',          pollKey: 'pollPpp',      defaultPollMs: 5000,
    streamKey: 'streamPpp',      pollable: true,  disableable: true,  requires: [], page: 'ppp',   cards: [] },
  // bridges borrows rates from ifStatus the way vlans does, and for the same
  // reason declares no requires: without Interface Rates a bridge still has
  // ports, STP roles and a host table worth showing.
  { key: 'bridges', label: 'Bridges',    sessionProp: 'bridges',      pollKey: 'pollBridges',  defaultPollMs: 5000,
    streamKey: 'streamBridges',  pollable: true,  disableable: true,  requires: [], page: 'bridges', cards: [] },
  { key: 'capsman', label: 'CAPsMAN',    sessionProp: 'capsman',      pollKey: 'pollCapsman',  defaultPollMs: 10000,
    streamKey: 'streamCapsman',  pollable: true,  disableable: true,  requires: [], page: 'capsman', cards: [] },
  // dns, packages and rosusers are streamKey: null ON PURPOSE, and they are the
  // only entries in this registry that are. RouterOS would accept /listen on both menus, so this
  // is a choice rather than a limitation:
  //
  //   dns       the settings row is one record and the static table is single
  //             digits, so there is nothing a channel would save. The expensive
  //             part is the cache, which is already opt-in and fetched only
  //             while somebody has the browser open — an open channel would
  //             hold a resource for a table nobody is looking at.
  //   packages  an inventory changes on a reboot, not on a tick. It polls every
  //             60 s and the page forces a refresh after an action, which is
  //             strictly better than a channel held open for weeks.
  //
  // Both still honour the router's poll interval, so the Poll/Stream switch has
  // nothing to change for them.
  { key: 'dns',   label: 'DNS',          sessionProp: 'dns',          pollKey: 'pollDns',      defaultPollMs: 10000,
    streamKey: null,             pollable: true,  disableable: true,  requires: [], page: 'dns',   cards: [] },
  { key: 'packages', label: 'Packages',  sessionProp: 'packages',     pollKey: 'pollPackages', defaultPollMs: 60000,
    streamKey: null,             pollable: true,  disableable: true,  requires: [], page: 'packages', cards: [] },
  // wan borrows rates from ifStatus the way vlans and bridges do, and declares
  // no requires for the same reason: switching Interface Rates off should cost
  // the rate column, not the page.
  { key: 'wan',   label: 'WAN',        sessionProp: 'wan',          pollKey: 'pollWan',      defaultPollMs: 10000,
    streamKey: 'streamWan',      pollable: true,  disableable: true,  requires: [], page: 'wan', cards: [] },
  // queues borrows the FastTrack summary from the firewall collector the way
  // vlans borrows rates from ifStatus, and declares no `requires` for the same
  // reason: a hard dependency would blank the whole Queues page when somebody
  // switched Firewall collection off. Degrade the banner, not the page.
  { key: 'queues', label: 'Queues',   sessionProp: 'queues',       pollKey: 'pollQueues',   defaultPollMs: 5000,
    streamKey: 'streamQueues',   pollable: true,  disableable: true,  requires: [], page: 'queues', cards: [] },
  // rosusers is the third streamKey: null entry, for the same reason as
  // packages rather than dns: a router's user list changes when an operator
  // edits it, which is a human-timescale event. It polls slowly and the page
  // forces a re-read after every action, so a channel held open for weeks would
  // buy nothing.
  { key: 'rosusers', label: 'Router Users', sessionProp: 'rosusers',  pollKey: 'pollRosusers', defaultPollMs: 60000,
    streamKey: null,             pollable: true,  disableable: true,  requires: [], page: 'rosusers', cards: [] },
  // logs stays streamed even in poll mode: /log/listen pushes new entries, and
  // polling /log/print would drop lines between polls. Correctness, not fidelity.
  { key: 'logs', label: 'Logs',         sessionProp: 'logs',         pollKey: null,           defaultPollMs: 0,
    streamKey: null,             pollable: false, disableable: true,  requires: [], page: 'logs', cards: [] },
]);

const BY_KEY      = Object.freeze(Object.fromEntries(COLLECTORS.map(c => [c.key, c])));
const DISABLEABLE = Object.freeze(COLLECTORS.filter(c => c.disableable).map(c => c.key));
const POLL_KEYS   = Object.freeze([...new Set(COLLECTORS.map(c => c.pollKey).filter(Boolean))]);
const STREAM_KEYS = Object.freeze(COLLECTORS.map(c => c.streamKey).filter(Boolean));

const DEFAULT_MODE = 'stream';
const MODES = Object.freeze(['stream', 'poll']);

/** Clamp an interval using the bounds settings.js already enforces. */
function clampPollValue(key, raw) {
  const n = Number.isFinite(Number(raw)) ? Math.trunc(Number(raw)) : NaN;
  if (!Number.isFinite(n)) return null;
  const bounds = POLL_BOUNDS && POLL_BOUNDS[key];
  if (!bounds) return n;
  return Math.max(bounds[0], Math.min(bounds[1], n));
}

/**
 * Resolve the effective collection config for one router.
 *
 * Precedence, lowest to highest:
 *   interval : global setting   <  router override
 *   delivery : router mode      <  router per-collector override
 *
 * Delivery takes NO global input by design: stream-vs-poll is a property of the
 * router, not of the installation. Mode switches delivery only and never touches
 * intervals, so choosing Poll cannot silently also mean "slower" — which would
 * be unrecoverable from the UI.
 */
// Legacy app-global stream-vs-poll keys, retired in #105. Read only by
// planMigration below; nothing else in the codebase may consult them.
const LEGACY_STREAM_KEYS = Object.freeze({
  streamSystem:  'system',
  streamPing:    'ping',
  streamConns:   'conns',
  streamTalkers: 'talkers',
  streamIfrates: 'ifStatus',
});

/**
 * Work out how the retired global Collection Method maps onto per-router blocks.
 *
 * Pure: returns `[{ id, collection }]` for the routers that need writing, so the
 * caller owns persistence and the mapping itself is testable. Routers that
 * already carry a `mode` are left alone — an explicit per-router choice must
 * never be overwritten by a global that no longer has a UI.
 *
 * All-true is the new default, so it needs no block at all; that is why the
 * common case returns an empty array and writes nothing.
 */
function planMigration(settings, routers) {
  const cfg     = settings || {};
  const present = Object.keys(LEGACY_STREAM_KEYS).filter(k => typeof cfg[k] === 'boolean');
  if (!present.length) return [];

  const allOff = present.every(k => cfg[k] === false);
  const allOn  = present.every(k => cfg[k] === true);
  if (allOn) return [];                       // already the default

  const plan = [];
  for (const r of (routers || [])) {
    if (r && r.collection && r.collection.mode) continue;
    if (allOff) {
      plan.push({ id: r.id, collection: { mode: 'poll' } });
    } else {
      // Mixed: stay on stream and record only the collectors that were polled.
      const overrides = {};
      for (const k of present) if (cfg[k] === false) overrides[k] = false;
      plan.push({ id: r.id, collection: { mode: 'stream', overrides } });
    }
  }
  return plan;
}

function resolveCollection(settings, routerRecord) {
  const cfg  = settings || {};
  const coll = (routerRecord && routerRecord.collection) || {};
  const mode = MODES.includes(coll.mode) ? coll.mode : DEFAULT_MODE;
  const off  = Array.isArray(coll.off) ? coll.off : [];
  const ovr  = (coll.overrides && typeof coll.overrides === 'object') ? coll.overrides : {};

  const poll = {}, stream = {}, enabled = {};

  for (const c of COLLECTORS) {
    const globalVal = c.pollKey ? cfg[c.pollKey] : undefined;
    const raw = (c.pollKey && ovr[c.pollKey] !== undefined) ? ovr[c.pollKey]
              : (globalVal !== undefined ? globalVal : c.defaultPollMs);
    const clamped = c.pollKey ? clampPollValue(c.pollKey, raw) : Math.trunc(Number(raw) || 0);
    poll[c.key] = clamped === null ? c.defaultPollMs : clamped;

    if (!c.pollable) {
      stream[c.key] = true;                        // logs, traffic: stream is the only path
    } else if (c.streamKey && ovr[c.streamKey] !== undefined) {
      stream[c.key] = ovr[c.streamKey] === true || ovr[c.streamKey] === 'true';
    } else if (c.streamKey) {
      stream[c.key] = mode !== 'poll';
    } else {
      stream[c.key] = false;                       // bandwidth: timer-driven, never a stream
    }

    enabled[c.key] = c.disableable ? !off.includes(c.key) : true;
  }

  // pollIfaces is interfaceStatus's *metadata* interval, not a collector of its
  // own, so it has no registry row — but it is override-able like any other.
  const _ifaces = clampPollValue('pollIfaces',
    ovr.pollIfaces !== undefined ? ovr.pollIfaces : cfg.pollIfaces);
  poll.ifaces = _ifaces === null ? 60000 : _ifaces;

  // pingEnabled is a separate global kill switch and still wins.
  if (cfg.pingEnabled === false) enabled.ping = false;

  // Cascade dependencies here rather than in the UI, so a hand-edited
  // routers.json cannot produce a combination that silently breaks a card.
  // Bandwidth has no fetch of its own: it reads connTableCache, which only the
  // connections collector fills. Loop until stable so a chain would also settle.
  let changed = true;
  while (changed) {
    changed = false;
    for (const c of COLLECTORS) {
      if (!enabled[c.key]) continue;
      if (c.requires.some(dep => !enabled[dep])) { enabled[c.key] = false; changed = true; }
    }
  }

  // `overrides` is passed through so callers can tell an inherited value from a
  // pinned one — the settings live-patch must not drag a pinned router back to
  // the fleet default.
  return { mode, poll, stream, enabled, overrides: ovr };
}

/**
 * Stable string answering "would this router's session be built differently?".
 * Lets a router edit skip the session rebuild when nothing that matters changed,
 * so a label-only edit costs no reconnect. Key and array order are normalised so
 * a cosmetic re-save produces an identical fingerprint.
 */
function collectionFingerprint(settings, routerRecord) {
  const r = resolveCollection(settings, routerRecord);
  const pick = (obj) => Object.keys(obj).sort().map(k => k + '=' + obj[k]).join(',');
  const rec = routerRecord || {};
  const cfg = settings || {};
  return [
    'mode=' + r.mode,
    'poll:' + pick(r.poll),
    'stream:' + pick(r.stream),
    'enabled:' + pick(r.enabled),
    // Not part of `collection`, but they change how the session is built too.
    ['defaultIf', 'pingTarget'].map(k => k + '=' + (rec[k] || '')).join(','),
    ['topN', 'topTalkersN', 'maxConns', 'historyMinutes']
      .map(k => k + '=' + (cfg[k] === undefined ? '' : cfg[k])).join(','),
  ].join('|');
}

module.exports = {
  COLLECTORS, BY_KEY, DISABLEABLE, POLL_KEYS, STREAM_KEYS, MODES, DEFAULT_MODE,
  clampPollValue, resolveCollection, collectionFingerprint, planMigration, LEGACY_STREAM_KEYS,
};
