'use strict';
// Stand-in for a collector the user has switched off for a router (#105).
//
// Why a stub rather than simply not calling start(): 11 of the 16 collectors
// register a `ros.on('connected')` handler in their CONSTRUCTOR that opens their
// streams independently of start() (arp:123, dhcpLeases:187, dhcpNetworks:237,
// logs:122, netwatch:133, ping:52, traffic:48, vpn:432, talkers:55, system:50,
// interfaceStatus:127). Only connections and bandwidth gate on _started. So
// "disabled" has to mean "never constructed", and buildSession() needs something
// to put on the session object in its place.
//
// index.js makes ~107 distinct `session.<prop>.<member>` accesses. A missing
// member here is a TypeError that takes the whole dashboard down, so the stub
// carries the union of every member index.js touches, and a source guard in
// test/code-review-remediation.test.js keeps that coverage complete as index.js
// grows.

// Returns a resolved promise, not undefined. index.js chains on some of these
// (`c.tick(true).catch(...)` at index.js:2888, and several `await x.start()`),
// so a bare undefined throws a TypeError that silently aborts the caller. That
// exact bug swallowed sendInitialState for a router with a disabled collector.
const noop = () => Promise.resolve();

/** Per-key shapes where an empty value of the wrong type would still break a caller. */
const HISTORY_SHAPE = {
  // ping.getHistory() is read as { history }; logs.getHistory() is used as an array.
  ping: () => ({ history: [], target: '', lossPct: 0 }),
  logs: () => [],
};

/**
 * Build an inert collector for `key`. Every method is a no-op and every payload
 * is empty, so callers behave as they would for a collector that has simply
 * never produced data.
 */
function makeNullCollector(key) {
  return {
    // Marks it for diagnostics and for anything that wants to skip it.
    disabled: true,
    collectorKey: key,

    // ── Lifecycle ────────────────────────────────────────────────────────────
    start: noop, stop: noop, suspend: noop, resume: noop, tick: noop,

    // ── Restart hooks called from the settings live-apply path ───────────────
    _restartStream: noop, _restartTimer: noop, _restartEmitTimer: noop,
    _restartMetaStreams: noop, _restartMonitorStream: noop,
    _startCounterStream: noop, _stopCounterStream: noop,
    _startTableStream: noop, _stopTableStream: noop,
    _startStream: noop, _stopStream: noop, _startWatchdog: noop, _stopWatchdog: noop,

    // ── Payload surface ──────────────────────────────────────────────────────
    lastPayload: null,
    lastHealth: null,
    lastWanStatus: null,

    // ── Stream handles: index.js reads these to count live streams ───────────
    _stream: null, _allStream: null, _counterStream: null, _tableStream: null,
    _ifStream: null, _addrStream: null, _monitorStream: null,
    _routeStream: null, _ipv6Stream: null, _bgpStream: null,
    // The /listen refresh handle on the collectors that ship both delivery
    // paths; diagnostics reads .open off it to count live channels. Queues holds
    // one per menu, so it has a plural.
    _listen: null, _listens: [],
    _streams: {},

    // ── Tunables index.js reads or writes ────────────────────────────────────
    pollMs: 0, metaPollMs: 0, topN: 0, maxConns: 0, target: '',
    _lastFp: '', _activeTable: '', _permissionDenied: false, _lossWindow: [],

    // ── Data accessors other code calls without a null guard ─────────────────
    getHistory: HISTORY_SHAPE[key] || (() => []),
    getLanCidrs: () => [],
    byIP: () => null,
    getNameByIP: () => null,
    getNameByMAC: () => null,
    getByIP: () => null,
    setActiveTable: noop,
    // packages.refreshNow is called from the socket actions, which have no idea
    // whether the collector is switched off. queues.forgetRates is the same:
    // called after a write that may have zeroed a counter.
    refreshNow: noop,
    forgetRates: noop,
    setAvailableInterfaces: noop,
    bindSocket: noop,
    unbindSocket: noop,
    preloadHistory: noop,
    hist: new Map(),
    networks: [],
    subscriptions: new Map(),
  };
}

module.exports = { makeNullCollector };
