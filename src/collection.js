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
const COLLECTORS = Object.freeze([
  // ── Protected: read directly by other collectors, or feed stored history ───
  { key: 'traffic', label: 'Traffic',      sessionProp: 'traffic',      pollKey: null,           defaultPollMs: 1000,
    streamKey: null,             pollable: false, disableable: false, requires: [], cards: ['trafficCard'] },
  { key: 'system', label: 'System / Gauges',       sessionProp: 'system',       pollKey: 'pollSystem',   defaultPollMs: 2000,
    streamKey: 'streamSystem',   pollable: true,  disableable: false, requires: [], cards: ['systemCard'] },
  { key: 'arp', label: 'ARP',          sessionProp: 'arp',          pollKey: 'pollArp',      defaultPollMs: 30000,
    streamKey: 'streamArp',      pollable: true,  disableable: false, requires: [], cards: [] },
  { key: 'dhcpLeases', label: 'DHCP Leases',   sessionProp: 'dhcpLeases',   pollKey: 'pollDhcp',     defaultPollMs: 600000,
    streamKey: 'streamLeases',   pollable: true,  disableable: false, requires: [], cards: [] },
  { key: 'dhcpNetworks', label: 'DHCP Networks', sessionProp: 'dhcpNetworks', pollKey: 'pollDhcp',     defaultPollMs: 600000,
    streamKey: 'streamDhcp',     pollable: true,  disableable: false, requires: [], cards: ['networksCard'] },

  // ── Disableable ────────────────────────────────────────────────────────────
  { key: 'conns', label: 'Connections',        sessionProp: 'conns',        pollKey: 'pollConns',    defaultPollMs: 5000,
    streamKey: 'streamConns',    pollable: true,  disableable: true,  requires: [], cards: ['connCard'] },
  { key: 'bandwidth', label: 'Bandwidth',    sessionProp: 'bandwidth',    pollKey: 'pollBandwidth', defaultPollMs: 5000,
    streamKey: null,             pollable: true,  disableable: true,  requires: ['conns'], cards: ['bandwidthCard'] },
  { key: 'talkers', label: 'Top Talkers',      sessionProp: 'talkers',      pollKey: 'pollTalkers',  defaultPollMs: 3000,
    streamKey: 'streamTalkers',  pollable: true,  disableable: true,  requires: [], cards: ['talkersCard'] },
  { key: 'ifStatus', label: 'Interface Rates',     sessionProp: 'ifStatus',     pollKey: 'pollIfstatus', defaultPollMs: 5000,
    streamKey: 'streamIfrates',  pollable: true,  disableable: true,  requires: [], cards: ['ifStatusCard'] },
  { key: 'ping', label: 'Ping',         sessionProp: 'ping',         pollKey: 'pollPing',     defaultPollMs: 5000,
    streamKey: 'streamPing',     pollable: true,  disableable: true,  requires: [], cards: [] },
  { key: 'wireless', label: 'Wireless',     sessionProp: 'wireless',     pollKey: 'pollWireless', defaultPollMs: 30000,
    streamKey: 'streamWireless', pollable: true,  disableable: true,  requires: [], cards: ['wirelessCard'] },
  { key: 'vpn', label: 'VPN',          sessionProp: 'vpn',          pollKey: 'pollVpn',      defaultPollMs: 10000,
    streamKey: 'streamVpn',      pollable: true,  disableable: true,  requires: [], cards: ['vpnCard'] },
  { key: 'firewall', label: 'Firewall',     sessionProp: 'firewall',     pollKey: 'pollFirewall', defaultPollMs: 5000,
    streamKey: 'streamFirewall', pollable: true,  disableable: true,  requires: [], cards: ['firewallCard'] },
  { key: 'routing', label: 'Routing',      sessionProp: 'routing',      pollKey: 'pollRouting',  defaultPollMs: 10000,
    streamKey: 'streamRouting',  pollable: true,  disableable: true,  requires: [],
    cards: ['routingProtoCard', 'routingBgpCard', 'routingPeersCard', 'routingRoutesCard'] },
  { key: 'netwatch', label: 'NetWatch',     sessionProp: 'netwatch',     pollKey: null,           defaultPollMs: 30000,
    streamKey: 'streamNetwatch', pollable: true,  disableable: true,  requires: [], cards: ['netwatchCard'] },
  // logs stays streamed even in poll mode: /log/listen pushes new entries, and
  // polling /log/print would drop lines between polls. Correctness, not fidelity.
  { key: 'logs', label: 'Logs',         sessionProp: 'logs',         pollKey: null,           defaultPollMs: 0,
    streamKey: null,             pollable: false, disableable: true,  requires: [], cards: [] },
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

  return { mode, poll, stream, enabled };
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
  clampPollValue, resolveCollection, collectionFingerprint,
};
