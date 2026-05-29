'use strict';
const notifier = require('./notifier');
const Routers  = require('./routers');
const db       = require('./db');

let _settings = null;

// ── Shared helpers ────────────────────────────────────────────────────────────

function _ts() {
  const tz = _settings && _settings.displayTimezone;
  if (tz) {
    return new Intl.DateTimeFormat('en-GB', {
      timeZone: tz, hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date());
  }
  const d = new Date();
  const p = n => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// WARNING: _settings MUST NEVER be spread into the vars/allVars passed here —
// that would leak credentials (tokens, passwords) into notification messages.
function _render(tpl, vars) {
  return tpl.replace(/\{\{(\w+)\}\}/g, (_, k) => {
    if (vars[k] === undefined) return '';
    // Strip control characters; cap length to prevent oversized payloads.
    return String(vars[k]).replace(/[\x00-\x1f\x7f]/g, '').slice(0, 200);
  });
}

function _noChannelsActive() {
  if (!_settings) return true;
  return !_settings.telegramEnabled && !_settings.pushbulletEnabled && !_settings.smtpEnabled && !_settings.ntfyEnabled;
}

function _ifaceType(name, type) {
  // Prefer the explicit type field from RouterOS (present in ifstatus:update payloads).
  // RouterOS 7 new wifi package reports type='wifi' — normalise to 'wlan'.
  const t = (type || '').toLowerCase();
  if (t === 'ether')                             return 'ether';
  if (t === 'wlan' || t === 'wifi')              return 'wlan';
  if (t === 'bridge')                            return 'bridge';
  if (t === 'vlan')                              return 'vlan';
  if (t && t !== 'unknown')                      return 'other';
  // Fall back to name-based detection when type is missing or unknown.
  if (/^ether/i.test(name))                      return 'ether';
  if (/^wlan|^wireless|^wifi/i.test(name))       return 'wlan';
  if (/^bridge/i.test(name))                     return 'bridge';
  if (/^vlan|\.\d+$/i.test(name))                return 'vlan';
  return 'other';
}

function _ifaceTypeAllowed(type) {
  const map = { ether:'notifIfaceEther', wlan:'notifIfaceWlan', bridge:'notifIfaceBridge', vlan:'notifIfaceVlan', other:'notifIfaceOther' };
  return !!_settings[map[type] || 'notifIfaceOther'];
}

// ── Per-router evaluator factory ──────────────────────────────────────────────
// Returns an isolated { evaluate(event, data) } with its own cooldown and state maps.
// getNameFn() is called at fire-time to get the router label for {{routerName}}.

function createEvaluator(getNameFn, getRouterFn) {
  const cooldown          = new Map();
  const prevIfState       = new Map();
  const prevVpnState      = new Map();
  const prevNetwatchState = new Map();
  let   prevCpuAlert      = null;   // null=unknown, true=was alerting, false=was normal
  const prevPingAlert     = {};     // target → boolean (was alerting)

  function fire(key, vars, isUp) {
    // Persist alert to DB unconditionally — the Reports tab must reflect every
    // event regardless of whether a notification channel is configured. The
    // cooldown gates only the push notification, not persistence (see below).
    const router = typeof getRouterFn === 'function' ? getRouterFn() : null;
    if (router && router.id) {
      // For up (recovery) events, resolveType holds the matching down alert_type so the
      // WHERE clause in resolveAlertEvent finds the correct open row.
      const alertType = (vars.alertType || key).toLowerCase().replace(/\s+/g, '_');
      const subject   = vars.ifaceName || vars.vpnPeer || vars.netwatchName || vars.pingTarget || null;
      if (isUp) {
        db.resolveAlertEvent(router.id, vars.resolveType || alertType, subject);
      } else {
        db.insertAlertEvent(router.id, alertType, subject, vars.detail || null);
      }
    }

    // Send push notification only when a delivery channel is configured. The
    // cooldown is consumed only on the path that actually sends, so enabling a
    // channel later does not find a warm cooldown set while no channel existed.
    if (_noChannelsActive()) return;
    const last = cooldown.get(key) || 0;
    if ((Date.now() - last) < ((_settings.notifCooldownSec || 60) * 1000)) return;
    // Cap cooldown map to prevent unbounded growth from ephemeral interface names
    const COOLDOWN_MAX = 500;
    if (cooldown.size > COOLDOWN_MAX) cooldown.clear();
    cooldown.set(key, Date.now());
    const allVars = { routerName: getNameFn(), timestamp: _ts(), ...vars };
    const title   = _render(_settings.notifTitle   || 'MikroDash Alert', allVars);
    const bodyTpl = isUp
      ? (_settings.notifBodyUp  || _settings.notifBody || '✅ {{alertType}} on {{routerName}}: {{detail}}')
      : (_settings.notifBody    || '⚠️ {{alertType}} on {{routerName}}: {{detail}}');
    const body = _render(bodyTpl, allVars);
    notifier.send(_settings, title, body).catch(e => console.warn('[alerter] notify failed:', e.message));
  }

  function evaluate(event, data) {
    if (!_settings) return;
    // Re-check alertsEnabled in case it was toggled after session creation.
    const router = typeof getRouterFn === 'function' ? getRouterFn() : null;
    if (router && !router.alertsEnabled) return;

    if (event === 'system:update' && _settings.notifCpu) {
      if (typeof data.cpuLoad === 'number') {
        const isHigh = data.cpuLoad >= _settings.alertCpuThreshold;
        if (isHigh && prevCpuAlert !== true) {
          fire('cpu:router:down', {
            alertType: 'High CPU',
            cpuLoad:   data.cpuLoad + '%',
            detail:    'CPU at ' + data.cpuLoad + '% (threshold: ' + _settings.alertCpuThreshold + '%)',
          }, false);
        } else if (!isHigh && prevCpuAlert === true) {
          fire('cpu:router:up', {
            alertType:   'CPU Normal',
            resolveType: 'high_cpu',
            cpuLoad:     data.cpuLoad + '%',
            detail:      'CPU back to ' + data.cpuLoad + '% (below threshold)',
          }, true);
        }
        prevCpuAlert = isHigh;
      }
    }

    if (event === 'ping:update' && _settings.notifPing) {
      const target = data.target || 'host';
      const base   = 'ping:' + target;
      if (typeof data.loss === 'number') {
        const isLoss = data.loss >= _settings.alertPingLoss;
        if (isLoss && prevPingAlert[target] !== true) {
          fire(base + ':down', {
            alertType:  'Ping Loss',
            pingTarget: data.target || '',
            pingLoss:   data.loss + '%',
            pingRtt:    data.rtt != null ? data.rtt + ' ms' : 'N/A',
            detail:     'Ping loss to ' + data.target + ' is ' + data.loss + '%',
          }, false);
        } else if (!isLoss && prevPingAlert[target] === true) {
          fire(base + ':up', {
            alertType:   'Ping Restored',
            resolveType: 'ping_loss',
            pingTarget:  data.target || '',
            pingLoss:    data.loss + '%',
            pingRtt:     data.rtt != null ? data.rtt + ' ms' : 'N/A',
            detail:      'Ping to ' + data.target + ' restored',
          }, true);
        }
        prevPingAlert[target] = isLoss;
      }
    }

    if (event === 'ifstatus:update' && _settings.notifIfaceUpDown && Array.isArray(data.interfaces)) {
      for (const iface of data.interfaces) {
        const prev       = prevIfState.get(iface.name);
        const wasRunning = prev ? prev.running : undefined;
        const isRunning  = !!iface.running;
        if (prev !== undefined && wasRunning !== isRunning) {
          const ifType = _ifaceType(iface.name, iface.type);
          if (_ifaceTypeAllowed(ifType)) {
            if (!isRunning) {
              fire('iface:' + iface.name + ':down', { alertType:'Interface Down', ifaceName:iface.name, status:'down', detail:iface.name + ' went down' }, false);
            } else {
              fire('iface:' + iface.name + ':up',   { alertType:'Interface Up',   resolveType:'interface_down', ifaceName:iface.name, status:'up',   detail:iface.name + ' came up'   }, true);
            }
          }
        }
        prevIfState.set(iface.name, { running: isRunning, disabled: !!iface.disabled });
      }
    }

    if (event === 'vpn:update' && _settings.notifVpn && Array.isArray(data.tunnels)) {
      for (const tunnel of data.tunnels) {
        const prev    = prevVpnState.get(tunnel.name);
        const wasConn = prev === 'connected';
        const isConn  = tunnel.state === 'connected';
        if (prev !== undefined && wasConn !== isConn) {
          if (!isConn) {
            fire('vpn:' + tunnel.name + ':down', { alertType:'VPN Disconnected', vpnPeer:tunnel.name, status:'down', detail:'VPN peer ' + tunnel.name + ' disconnected' }, false);
          } else {
            fire('vpn:' + tunnel.name + ':up',   { alertType:'VPN Connected',    resolveType:'vpn_disconnected', vpnPeer:tunnel.name, status:'up',   detail:'VPN peer ' + tunnel.name + ' connected'    }, true);
          }
        }
        prevVpnState.set(tunnel.name, tunnel.state);
      }
    }

    if (event === 'netwatch:update' && _settings.notifNetwatch && Array.isArray(data.hosts)) {
      for (const host of data.hosts) {
        if (host.status === 'unknown') continue; // transient re-probe state — skip to avoid premature fire/resolve
        const prev    = prevNetwatchState.get(host.id);
        const wasDown = prev === 'down';
        const isDown  = host.status === 'down';
        if (prev !== undefined && wasDown !== isDown) {
          const netwatchName = host.name || host.host;
          const netwatchDesc = netwatchName !== host.host ? netwatchName + ' (' + host.host + ')' : host.host;
          if (isDown) {
            fire('netwatch:' + host.id + ':down', { alertType:'Host Down',                            host:host.host, netwatchName, status:'down', detail:'NetWatch host ' + netwatchDesc + ' is unreachable' }, false);
          } else {
            fire('netwatch:' + host.id + ':up',   { alertType:'Host Up', resolveType:'host_down',     host:host.host, netwatchName, status:'up',   detail:'NetWatch host ' + netwatchDesc + ' is reachable'   }, true);
          }
        }
        prevNetwatchState.set(host.id, host.status);
      }
    }
  }

  return { evaluate };
}

// ── Router connectivity alerts ────────────────────────────────────────────────
const _connCooldowns = new Map();

function fireConnectivityAlert(routerId, routerLabel, connected) {
  if (!_settings) return;
  const _r = Routers.getById(routerId);
  if (_r && !_r.alertsEnabled) return;

  // Persist connectivity transition to DB unconditionally so the Reports tab
  // stays complete even when router-status notifications are disabled.
  if (connected) {
    db.resolveAlertEvent(routerId, 'connectivity', null);
  } else {
    db.insertAlertEvent(routerId, 'connectivity', null,
      routerLabel + ' is unreachable');
  }

  // Send push only when the router-status toggle is on AND a channel exists.
  // Cooldown is consumed only on the sending path (see fire() for rationale).
  if (!_settings.notifRouterStatus || _noChannelsActive()) return;
  const key  = 'router-conn:' + routerId + ':' + (connected ? 'up' : 'down');
  const last = _connCooldowns.get(key) || 0;
  if ((Date.now() - last) < ((_settings.notifCooldownSec || 60) * 1000)) return;
  if (_connCooldowns.size > 100) _connCooldowns.clear();
  _connCooldowns.set(key, Date.now());
  const vars = {
    alertType:  connected ? 'Router Online' : 'Router Offline',
    routerName: routerLabel,
    status:     connected ? 'online' : 'offline',
    detail:     routerLabel + (connected ? ' is now reachable' : ' is unreachable'),
    timestamp:  _ts(),
  };
  const title   = _render(_settings.notifTitle || 'MikroDash Alert', vars);
  const bodyTpl = connected
    ? (_settings.notifBodyUp || _settings.notifBody || '✅ {{alertType}} on {{routerName}}: {{detail}}')
    : (_settings.notifBody   || '⚠️ {{alertType}} on {{routerName}}: {{detail}}');
  const body = _render(bodyTpl, vars);
  notifier.send(_settings, title, body).catch(e => console.warn('[alerter] notify failed:', e.message));
}

// ── Module init ───────────────────────────────────────────────────────────────

// One isolated evaluator per router id. Each owns its own cooldown and
// threshold-crossing state so concurrently-active routers (the global default
// plus any on-demand sessions a modern-auth user opened) never clobber each
// other's prev-state maps or mis-attribute alerts across routers.
const _evaluators = new Map(); // routerId → { evaluate }

function _evaluatorFor(routerId) {
  let ev = _evaluators.get(routerId);
  if (!ev) {
    ev = createEvaluator(
      () => {
        const r = Routers.getById(routerId);
        return (r && r.label) || (r && r.host) || 'router';
      },
      () => Routers.getById(routerId),
    );
    _evaluators.set(routerId, ev);
  }
  return ev;
}

function init(io, settings) {
  _settings = settings;
}

// Called from buildRouterIo.emit for every event emitted by a pool-session
// collector. io.to(room).emit bypasses the io.emit wrapper, so this is the only
// reliable hook for alert evaluation. Routed through the per-router evaluator so
// the event is attributed to the router that actually produced it.
function evaluateForRouter(routerId, event, data) {
  if (!_settings || !routerId) return;
  const r = Routers.getById(routerId);
  if (!r || !r.alertsEnabled) return;
  try { _evaluatorFor(routerId).evaluate(event, data); } catch (e) { console.error('[alerter] evaluate error:', e.message); }
}

// Drop a router's evaluator when its session is torn down so its prev-state
// doesn't leak into a future session (e.g. an interface that was down stays
// "remembered" as down across a teardown/rebuild and suppresses the next alert).
function dropEvaluator(routerId) {
  _evaluators.delete(routerId);
}

function updateSettings(settings) {
  _settings = settings;
}

module.exports = { init, updateSettings, createEvaluator, evaluateForRouter, dropEvaluator, fireConnectivityAlert };
