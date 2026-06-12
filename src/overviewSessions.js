'use strict';
const ROS                      = require('./routeros/client');
const SystemCollector          = require('./collectors/system');
const InterfaceStatusCollector = require('./collectors/interfaceStatus');
const DhcpLeasesCollector      = require('./collectors/dhcpLeases');

// routerId → { ros, system, ifStatus, dhcpLeases, connected }
const _sessions  = new Map();
let   _suspended = false;

// Null-io: collectors need an io object but we don't want them to broadcast.
// clientsCount=1 prevents idle-gating inside the collectors.
const _nullIo = {
  engine: { clientsCount: 1 },
  emit() {},
  on() {},
  sockets: { adapter: { rooms: { get() { return undefined; } } } },
};

function syncSessions(allRouters, excludeIds) {
  // Tear down sessions for removed routers or routers now in the main pool.
  for (const [id, session] of _sessions) {
    if (excludeIds.has(id) || !allRouters.find(r => r.id === id)) {
      _stopSession(id, session);
      _sessions.delete(id);
    }
  }
  // Start sessions for routers not in the main pool and not already tracked.
  for (const router of allRouters) {
    if (excludeIds.has(router.id)) continue;
    if (_sessions.has(router.id)) continue;
    _sessions.set(router.id, _buildSession(router));
  }
}

function getSummaries() {
  const result = [];
  for (const [routerId, s] of _sessions) {
    result.push({
      routerId,
      connected:         s.connected,
      systemPayload:     s.system     ? s.system.lastPayload     : null,
      ifStatusPayload:   s.ifStatus   ? s.ifStatus.lastPayload   : null,
      dhcpLeasesPayload: s.dhcpLeases ? s.dhcpLeases.lastPayload : null,
    });
  }
  return result;
}

function suspend() {
  _suspended = true;
  for (const [, s] of _sessions) {
    if (s.system     && typeof s.system.stop     === 'function') s.system.stop();
    if (s.ifStatus   && typeof s.ifStatus.stop   === 'function') s.ifStatus.stop();
    if (s.dhcpLeases && typeof s.dhcpLeases.stop === 'function') s.dhcpLeases.stop();
  }
}

function resume() {
  _suspended = false;
  for (const [, s] of _sessions) {
    if (!s.connected) continue;
    if (s.system     && typeof s.system.start     === 'function') s.system.start();
    if (s.ifStatus   && typeof s.ifStatus.start   === 'function') s.ifStatus.start();
    if (s.dhcpLeases && typeof s.dhcpLeases.start === 'function') s.dhcpLeases.start();
  }
}

function stopAll() {
  for (const [id, session] of _sessions) _stopSession(id, session);
  _sessions.clear();
}

function _buildSession(router) {
  const pollMs  = 1000;
  const tlsOpts = router.tls ? { rejectUnauthorized: !router.tlsInsecure } : false;

  const ros = new ROS({
    host:     router.host,
    port:     router.port,
    tls:      tlsOpts,
    username: router.username,
    password: router.password,
  });
  ros.routerLabel = router.label || router.host;

  const state      = {};
  const system     = new SystemCollector         ({ ros, io: _nullIo, pollMs, state });
  const ifStatus   = new InterfaceStatusCollector({ ros, io: _nullIo, pollMs, metaPollMs: pollMs * 12, state });
  const dhcpLeases = new DhcpLeasesCollector     ({ ros, io: _nullIo, state });

  const session = { ros, system, ifStatus, dhcpLeases, connected: false };

  ros.on('connected', () => {
    session.connected = true;
    if (!_suspended) {
      system.start();
      ifStatus.start();
      dhcpLeases.start();
    }
  });

  ros.on('close',           () => { session.connected = false; });
  ros.on('connectionError', () => { session.connected = false; });

  ros.connectLoop();
  return session;
}

function _stopSession(id, session) {
  if (typeof session.system.stop     === 'function') session.system.stop();
  if (typeof session.ifStatus.stop   === 'function') session.ifStatus.stop();
  if (typeof session.dhcpLeases.stop === 'function') session.dhcpLeases.stop();
  session.ros.stop();
}

module.exports = { syncSessions, getSummaries, stopAll, suspend, resume };
