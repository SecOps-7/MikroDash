'use strict';
const ROS      = require('./routeros/client');
const alerter  = require('./alerter');
const Settings = require('./settings');
const dbWriter = require('./db-writer');

const SystemCollector          = require('./collectors/system');
const PingCollector            = require('./collectors/ping');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const VpnCollector             = require('./collectors/vpn');
const NetwatchCollector        = require('./collectors/netwatch');
const RoutingCollector         = require('./collectors/routing');

let _mainIo = null;
let _identityHook = null;     // (routerId, {model, serial, osVersion}) → void
const _sessions  = new Map(); // routerId → { ros, collectors, evaluator }
const _statusMap = new Map(); // routerId → connected boolean

function init(mainIo) {
  _mainIo = mainIo;
}

// `excludeIds` is the set of routers the main session pool (index.js) is already
// serving with a live connection. We must NOT also run a session for them, or a
// single up/down transition would be recorded twice (duplicate connectivity_events,
// router:status emits, and connectivity alerts). The global active router is always
// excluded for the same reason.
function syncSessions(allRouters, activeRouterId, excludeIds) {
  const excluded = (id) => id === activeRouterId || (excludeIds && excludeIds.has(id));
  // Tear down sessions that are no longer needed, are now pool-owned, or whose
  // alertsEnabled flag changed (flag change requires rebuilding with/without collectors).
  for (const [id, session] of _sessions) {
    const router = allRouters.find(r => r.id === id);
    if (!router || excluded(id) ||
        session.alertsEnabled !== !!router.alertsEnabled) {
      _stopSession(id, session);
      _sessions.delete(id);
    }
  }
  // Maintain a session for every router not handled by the pool so we always know
  // its Online/Offline status regardless of whether alerts are enabled.
  for (const router of allRouters) {
    if (excluded(router.id)) continue;
    if (_sessions.has(router.id)) continue;
    _sessions.set(router.id, _buildSession(router));
  }
}

function getStatusMap() {
  return new Map(_statusMap);
}

function _buildSession(router) {
  const alertsEnabled = !!router.alertsEnabled;

  // Alert evaluation is only wired when alertsEnabled — otherwise stubIo discards all events.
  const evaluator = alertsEnabled
    ? alerter.createEvaluator(() => router.label || router.host, () => router)
    : null;

  const stubIo = {
    engine: { clientsCount: 1 },
    emit(event, data) {
      // Logged, not swallowed: this used to be an empty catch, so a throw inside
    // trigger evaluation silenced alerts for this router with no trace at all.
    // The pool path already logs the equivalent (alerter.js evaluateForRouter).
    if (evaluator) {
      try { evaluator.evaluate(event, data); }
      catch (e) { console.error('[alertSessions] evaluate error: %s', e && e.message ? e.message : e); }
    }
    },
    // Collectors that emit via io.to(room).emit (e.g. vpn) must still reach the
    // evaluator here — without to() they would throw and VPN alerts never fire.
    // Recursively chainable: a collector reaching three rooms (ifstatus, #108)
    // threw "to is not a function" here with the old two-level shim, killing the
    // alert pool. Depth is irrelevant to this shim — every emit is forwarded to
    // the evaluator regardless of room — so it must not care how deep it goes.
    to() {
      const _c = { to: () => _c, emit: (e, d) => stubIo.emit(e, d) };
      return _c;
    },
    on() {},
    sockets: { adapter: { rooms: { get() { return undefined; } } } },
  };

  const cfg     = Settings.load();
  const tlsOpts = router.tls ? { rejectUnauthorized: !router.tlsInsecure } : false;
  const ros     = new ROS({
    host:     router.host,
    port:     router.port,
    tls:      tlsOpts,
    username: router.username,
    password: router.password,
  });
  ros.routerLabel = router.label || router.host;

  // Alert collectors only run when alertsEnabled — status-only sessions need no
  // collectors since the ROS connection events alone provide Online/Offline state.
  const state = {};
  const collectors = alertsEnabled ? [
    new SystemCollector         ({ ros, io: stubIo, pollMs: cfg.pollSystem   || 2000,  state }),
    new PingCollector           ({ ros, io: stubIo, pollMs: cfg.pollPing     || 5000,  state, target: router.pingTarget || '1.1.1.1' }),
    new InterfaceStatusCollector({ ros, io: stubIo, pollMs: cfg.pollIfstatus || 5000,  metaPollMs: cfg.pollIfaces || 60000, state }),
    new VpnCollector            ({ ros, io: stubIo, pollMs: cfg.pollVpn      || 10000, state }),
    new NetwatchCollector       ({ ros, io: stubIo, state }),
    // BGP alerts used to fire only for the router whose Routing page was open,
    // because this pool had no routing collector — so an alert type that reads
    // as enabled in Settings did nothing for every other router.
    //
    // bgpOnly + streamMode:false is deliberate on both counts. The evaluator
    // reads data.peers and nothing else, so the route table would be load for a
    // payload nobody renders; and streaming would hold three more open channels
    // per alert-enabled router on the small hardware #105 exists for.
    new RoutingCollector        ({ ros, io: stubIo, pollMs: cfg.pollRouting  || 10000, state,
                                   streamMode: false, bgpOnly: true }),
  ] : [];

  const routerId = router.id;
  // This pool runs whenever alerts are enabled, so it is usually the first to
  // learn a non-active router's identity — well before anyone opens a page.
  if (typeof _identityHook === 'function') {
    const sys = collectors.find(c => c instanceof SystemCollector);
    if (sys) sys._onIdentity = (identity) => _identityHook(routerId, identity);
  }
  let _prevConnected   = null;
  let _downTimer       = null;
  let _declaredOffline = false;
  const session = { ros, collectors, evaluator, alertsEnabled, destroyed: false };

  session._cancelDownTimer = () => { if (_downTimer) { clearTimeout(_downTimer); _downTimer = null; } };

  ros.on('connected', () => {
    if (session.destroyed) return;
    console.log('%s', `[alertSession] ✓ ${router.label} (${router.host})${alertsEnabled ? '' : ' [status-only]'}`);
    session._cancelDownTimer();
    _statusMap.set(routerId, true);
    if (_mainIo) _mainIo.emit('router:status', { routerId, connected: true });
    // Record connected=1 only on a real transition (see wireRosEvents in index.js):
    // unconditional writes on every reconnect inflate uptime for a flapping link.
    if (_prevConnected !== true) dbWriter.recordConnectivity(routerId, true);
    if (_declaredOffline && alertsEnabled) {
      alerter.fireConnectivityAlert(routerId, router.label || router.host, true);
      _declaredOffline = false;
    }
    _prevConnected = true;
    for (const c of collectors) {
      // Routing is page-gated in the app: start() only primes lastPayload once,
      // and resume() is what opens the running loop, called when the Routing
      // page becomes visible. Nothing opens a page in this pool, so resume() is
      // what makes it collect at all — start() alone would evaluate BGP once
      // per reconnect and then go quiet, which looks like working and is not.
      // Calling both would simply repeat the same loads.
      if (c instanceof RoutingCollector) {
        // resume() has no catch of its own, and an unhandled rejection here
        // would take down the process on a router that merely went away
        // mid-load.
        Promise.resolve(c.resume()).catch((e) => {
          console.error('[alertSessions] routing resume failed: %s', e && e.message ? e.message : e);
        });
        continue;
      }
      if (typeof c.start === 'function') c.start();
    }
  });

  function _onDisconnect() {
    if (session.destroyed) return;
    if (_downTimer) return;
    if (_prevConnected === null) {
      _statusMap.set(routerId, false);
      if (_mainIo) _mainIo.emit('router:status', { routerId, connected: false });
      dbWriter.recordConnectivity(routerId, false);
      _prevConnected = false;
      return;
    }
    const threshMs = ((router.connDownThresholdSec !== undefined) ? router.connDownThresholdSec : 30) * 1000;
    if (threshMs <= 0) {
      _statusMap.set(routerId, false);
      if (_mainIo) _mainIo.emit('router:status', { routerId, connected: false });
      dbWriter.recordConnectivity(routerId, false);
      if (alertsEnabled && _prevConnected !== false) {
        // Mirrors the debounce branch below. The recovery path is guarded on
        // _declaredOffline, so leaving it false here meant a router with
        // connDownThresholdSec = 0 opened a connectivity alert that could
        // never be resolved.
        _declaredOffline = true;
        alerter.fireConnectivityAlert(routerId, router.label || router.host, false);
      }
      _prevConnected = false;
      return;
    }
    // The outage started now, not when the debounce expires. Record the
    // observed time so downtime is not under-reported by threshMs (#99).
    const downAt = Date.now();
    _downTimer = setTimeout(() => {
      _downTimer       = null;
      _declaredOffline = true;
      _prevConnected   = false;
      _statusMap.set(routerId, false);
      if (_mainIo) _mainIo.emit('router:status', { routerId, connected: false });
      dbWriter.recordConnectivity(routerId, false, downAt);
      if (alertsEnabled)
        alerter.fireConnectivityAlert(routerId, router.label || router.host, false);
    }, threshMs);
  }

  ros.on('close',           _onDisconnect);
  ros.on('connectionError', _onDisconnect);

  ros.connectLoop().catch((e) => {
    console.error('%s', `[alertSession] connectLoop exited unexpectedly for ${router.host}:`, e && e.message ? e.message : e);
  });
  return session;
}

function _stopSession(id, session) {
  console.log('%s', `[alertSession] stopping session for router ${id}`);
  session.destroyed = true;
  if (session._cancelDownTimer) session._cancelDownTimer();
  for (const c of session.collectors) {
    if (typeof c.stop === 'function') c.stop();
  }
  session.ros.stop();
  _statusMap.delete(id);
}

/** Called with (routerId, {model, serial, osVersion}) when RouterOS reports identity. */
function setIdentityHook(fn) { _identityHook = typeof fn === 'function' ? fn : null; }

module.exports = { init, syncSessions, getStatusMap, setIdentityHook };
