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

let _mainIo = null;
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
      if (evaluator) try { evaluator.evaluate(event, data); } catch (_) {}
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
  ] : [];

  const routerId = router.id;
  let _prevConnected   = null;
  let _downTimer       = null;
  let _declaredOffline = false;
  const session = { ros, collectors, evaluator, alertsEnabled, destroyed: false };

  session._cancelDownTimer = () => { if (_downTimer) { clearTimeout(_downTimer); _downTimer = null; } };

  ros.on('connected', () => {
    if (session.destroyed) return;
    console.log(`[alertSession] ✓ ${router.label} (${router.host})${alertsEnabled ? '' : ' [status-only]'}`);
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
    for (const c of collectors) if (typeof c.start === 'function') c.start();
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
      if (alertsEnabled && _prevConnected !== false)
        alerter.fireConnectivityAlert(routerId, router.label || router.host, false);
      _prevConnected = false;
      return;
    }
    _downTimer = setTimeout(() => {
      _downTimer       = null;
      _declaredOffline = true;
      _prevConnected   = false;
      _statusMap.set(routerId, false);
      if (_mainIo) _mainIo.emit('router:status', { routerId, connected: false });
      dbWriter.recordConnectivity(routerId, false);
      if (alertsEnabled)
        alerter.fireConnectivityAlert(routerId, router.label || router.host, false);
    }, threshMs);
  }

  ros.on('close',           _onDisconnect);
  ros.on('connectionError', _onDisconnect);

  ros.connectLoop();
  return session;
}

function _stopSession(id, session) {
  console.log(`[alertSession] stopping session for router ${id}`);
  session.destroyed = true;
  if (session._cancelDownTimer) session._cancelDownTimer();
  for (const c of session.collectors) {
    if (typeof c.stop === 'function') c.stop();
  }
  session.ros.stop();
  _statusMap.delete(id);
}

module.exports = { init, syncSessions, getStatusMap };
