'use strict';
const ROS     = require('./routeros/client');
const alerter = require('./alerter');
const Settings = require('./settings');

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

function syncSessions(allRouters, activeRouterId) {
  // Tear down sessions for routers that no longer need one
  for (const [id, session] of _sessions) {
    const router = allRouters.find(r => r.id === id);
    if (!router || !router.alertsEnabled || id === activeRouterId) {
      _stopSession(id, session);
      _sessions.delete(id);
    }
  }
  // Start sessions for alert-enabled non-active routers without an existing session
  for (const router of allRouters) {
    if (router.id === activeRouterId || !router.alertsEnabled) continue;
    if (_sessions.has(router.id)) continue;
    _sessions.set(router.id, _buildSession(router));
  }
}

function getStatusMap() {
  return new Map(_statusMap);
}

function _buildSession(router) {
  const evaluator = alerter.createEvaluator(() => router.label || router.host);

  const stubIo = {
    engine: { clientsCount: 1 },
    emit(event, data) {
      try { evaluator.evaluate(event, data); } catch (_) {}
    },
    on() {},
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

  const state = {};
  const collectors = [
    new SystemCollector         ({ ros, io: stubIo, pollMs: cfg.pollSystem   || 2000,  state }),
    new PingCollector           ({ ros, io: stubIo, pollMs: cfg.pollPing     || 5000,  state, target: router.pingTarget || '1.1.1.1' }),
    new InterfaceStatusCollector({ ros, io: stubIo, pollMs: cfg.pollIfstatus || 5000,  metaPollMs: cfg.pollIfaces || 60000, state }),
    new VpnCollector            ({ ros, io: stubIo, pollMs: cfg.pollVpn      || 10000, state }),
    new NetwatchCollector       ({ ros, io: stubIo, state }),
  ];

  const routerId = router.id;
  let _prevConnected = null;

  ros.on('connected', () => {
    console.log(`[alertSession] ✓ ${router.label} (${router.host})`);
    _statusMap.set(routerId, true);
    if (_mainIo) _mainIo.emit('router:status', { routerId, connected: true });
    if (_prevConnected === false)
      alerter.fireConnectivityAlert(routerId, router.label || router.host, true);
    _prevConnected = true;
    for (const c of collectors) if (typeof c.start === 'function') c.start();
  });

  function _onDisconnect() {
    _statusMap.set(routerId, false);
    if (_mainIo) _mainIo.emit('router:status', { routerId, connected: false });
    if (_prevConnected === true)
      alerter.fireConnectivityAlert(routerId, router.label || router.host, false);
    _prevConnected = false;
  }

  ros.on('close',           _onDisconnect);
  ros.on('connectionError', _onDisconnect);

  ros.connectLoop();
  return { ros, collectors, evaluator };
}

function _stopSession(id, session) {
  console.log(`[alertSession] stopping session for router ${id}`);
  for (const c of session.collectors) {
    if (typeof c.stop === 'function') c.stop();
  }
  session.ros.stop();
  _statusMap.delete(id);
}

module.exports = { init, syncSessions, getStatusMap };
