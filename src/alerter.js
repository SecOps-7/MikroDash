'use strict';
const notifier   = require('./notifier');
const Routers    = require('./routers');
const db         = require('./db');
const userNotify = require('./userNotify');

let _settings = null;
let _io       = null;

// Cooldown maps are keyed per (recipient, subject) since #109, so they hold one
// entry per destination that has been alerted about a subject rather than one
// per subject. The caps are a memory bound against churn from ephemeral
// interface and peer names, not a correctness mechanism — overflow clears the
// map, which at worst lets one alert through early.
const COOLDOWN_MAX      = 1000;
const CONN_COOLDOWN_MAX = 250;

/**
 * Every destination an alert about this router should reach: the install-wide
 * one, plus each user whose grants cover the router (issue #109).
 *
 * The install destination is just another recipient with a reserved id, so the
 * delivery loop has no special case and its cooldown cannot collide with a
 * user's — a user id is prefixed 'user:'.
 *
 * Per-user fan-out is gated on the install-wide switch, which ships off: a
 * personal ntfy topic or SMTP host is a destination the *user* chooses, so
 * enabling it lets any account that can log in make the server issue outbound
 * requests to an address it picks.
 */
function _recipients(routerId) {
  const out = [{ id: '_install', settings: _settings }];
  if (!routerId || !_settings || !_settings.userNotifyEnabled) return out;
  try {
    for (const r of userNotify.recipientsFor(routerId)) out.push(r);
  } catch (e) {
    // A failure resolving personal channels must never cost the install its
    // own notification — that is the destination an operator actually relies on.
    console.warn('[alerter] per-user recipients failed:', e.message);
  }
  return out;
}

/**
 * Deliver one already-rendered message to one recipient.
 *
 * Guard order matches what the single-destination path always did: no usable
 * channel, then the alert-type toggles, then the cooldown — and the cooldown is
 * consumed only where a send actually happens, so a recipient who enables a
 * channel later does not find a warm cooldown stamped while they had none.
 */
function _deliver(cooldownMap, recipient, subjectKey, title, body, maxEntries) {
  const s = recipient && recipient.settings;
  if (!s) return;
  if (!_hasChannel(s)) return;

  const cdKey = recipient.id + '|' + subjectKey;
  const last  = cooldownMap.get(cdKey) || 0;
  if ((Date.now() - last) < ((_settings.notifCooldownSec || 60) * 1000)) return;
  if (cooldownMap.size > (maxEntries || COOLDOWN_MAX)) cooldownMap.clear();
  cooldownMap.set(cdKey, Date.now());

  notifier.send(s, title, body)
    .catch(e => console.warn(`[alerter] notify failed (${recipient.id}):`, e.message));
}

/**
 * Push an alert to the browsers watching this router.
 *
 * Module-level rather than per-evaluator so BOTH alert paths reach it: the main
 * pool sessions and the background sessions in alertSessions.js, which build
 * their own evaluators around a stub io that discards everything it is given.
 * Routing through the real server here is what lets an alert on a router nobody
 * is looking at still reach the bell.
 *
 * Room-scoped: a socket only ever joins its own router's room, and the router
 * list it holds is already filtered by allowedRouterIds, so this cannot leak a
 * restricted router's alerts.
 */
function _emit(routerId, event, payload) {
  if (!_io || !routerId) return;
  try {
    _io.to('router-' + routerId).emit(event, payload);
  } catch (e) {
    console.warn('[alerter] emit failed:', e.message);
  }
}

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

// Delegates to the notifier so "configured" means the same thing on both
// sides. Checking only the *Enabled flags here, while send() also required
// credentials, meant a channel ticked without a token consumed the cooldown,
// sent nothing, and logged nothing — a silent no-op that also suppressed the
// next alert for the whole cooldown window.
/**
 * Does this destination have a channel that could actually deliver?
 *
 * Takes the settings object rather than reading _settings, because since #109
 * the same question is asked of the install-wide destination and of each user's
 * own config. The typeof guard is not decoration: notifier is replaced through
 * require.cache in three test files with a stub carrying only send(), so this
 * has to degrade to the field check rather than throwing.
 */
function _hasChannel(s) {
  if (!s) return false;
  if (typeof notifier.hasConfiguredChannel === 'function') return !!notifier.hasConfiguredChannel(s);
  return !!(s.telegramEnabled || s.pushbulletEnabled || s.smtpEnabled || s.ntfyEnabled);
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

// The interface-type filter is a second toggle the interface rule answers to,
// so it is returned as a key for the per-recipient check rather than resolved
// against _settings here — a user filtering to wlan-only is the whole point.
function _ifaceTypeKey(type) {
  const map = { ether:'notifIfaceEther', wlan:'notifIfaceWlan', bridge:'notifIfaceBridge', vlan:'notifIfaceVlan', other:'notifIfaceOther' };
  return map[type] || 'notifIfaceOther';
}

// ── Per-router evaluator factory ──────────────────────────────────────────────
// Returns an isolated { evaluate(event, data) } with its own cooldown and state maps.
// getNameFn() is called at fire-time to get the router label for {{routerName}}.

/**
 * Human name for a stored alert type (issue: update alerts read as raw types).
 *
 * `alert_type` in the database is minted by lowercasing and underscoring the
 * alertType a fire() call passed, which makes it a good key and a poor label —
 * an alert loaded from the database rendered as "routeros_update".
 *
 * Deriving the label here rather than storing it keeps one source of truth: the
 * live socket path and the historical database path both come through this
 * function, so what you see when an alert arrives and what you see after a
 * reload cannot disagree. It also means renaming an alert does not need a data
 * migration — the stored key stays put and only the presentation moves.
 */
const ALERT_LABELS = {
  routeros_update:  'Update Available',
  routeros_updated: 'Up To Date',
  connectivity:     'Router Connectivity',
};
// Words the mechanical title-caser would otherwise mangle into "Bgp" and "Ok".
const _LABEL_ACRONYMS = { bgp: 'BGP', vpn: 'VPN', cpu: 'CPU', ok: 'OK', os: 'OS' };

function labelFor(alertType) {
  if (!alertType) return 'Alert';
  const key = String(alertType).toLowerCase();
  if (ALERT_LABELS[key]) return ALERT_LABELS[key];
  // Anything unmapped still reads as words rather than as a database column, so
  // a future alert type degrades gracefully instead of leaking its key.
  return key.split('_').filter(Boolean)
    .map(w => _LABEL_ACRONYMS[w] || (w.charAt(0).toUpperCase() + w.slice(1)))
    .join(' ');
}

function createEvaluator(getNameFn, getRouterFn) {
  const cooldown          = new Map();
  const prevIfState       = new Map();
  const prevVpnState      = new Map();
  const prevNetwatchState = new Map();
  let   prevCpuAlert      = null;   // null=unknown, true=was alerting, false=was normal
  const prevPingAlert     = {};     // target → boolean (was alerting)
  // The version last alerted on, not a boolean. system:update fires every poll
  // (~2 s), so a boolean would re-fire as soon as the cooldown lapsed. Keying
  // on the version gives one alert per release, and a later release still
  // notifies instead of being swallowed as "already alerting".
  let   prevUpdateVersion = null;
  const prevBgpState      = new Map();  // peer key → last state string
  const prevBgpPfx        = new Map();  // peer key → prefix count at last established reading
  const prevBgpFlap       = new Map();  // peer key → boolean, a flap alert is open
  const prevBgpHold       = new Map();  // peer key → boolean, a hold-timer alert is open
  const prevBgpPfxAlert   = new Map();  // peer key → boolean, a prefix-swing alert is open

  // The cooldown map is capped against churn from ephemeral interface names,
  // but the prev-state maps are populated from that same churning source and
  // had no cap at all — dynamic pppoe/l2tp/WireGuard peers grew them for the
  // lifetime of the evaluator. Same bound, same reasoning.
  const STATE_MAX = 500;
  function _capMap(m) { if (m.size > STATE_MAX) m.clear(); }

  // Fraction the advertised prefix count must move to be worth an alert.
  const BGP_PFX_THRESH = 0.2;

  function fire(key, vars, isUp, notifKeys) {
    // The install-wide toggles suppress for everyone, and they do it here —
    // before the event is recorded, before the bell, before anyone's push.
    //
    // #109 moved this check down into per-recipient delivery so a user could opt
    // IN to a type the install had switched off. That had a consequence nobody
    // wanted: the Interface Alert Filter stopped filtering the notification
    // bell, so switching Wireless off silenced the push and still rang the bell
    // on every wlan flap. A filter that does not filter what you are looking at
    // is not a filter. The install setting is authoritative again; the
    // per-recipient check in _deliver now only ever narrows further.
    if (notifKeys && !notifKeys.every(k => _settings[k] === true)) return;

    // Persist alert to DB unconditionally — the Reports tab must reflect every
    // event regardless of whether a notification channel is configured. The
    // cooldown gates only the push notification, not persistence (see below).
    const router = typeof getRouterFn === 'function' ? getRouterFn() : null;
    // Declared out here, not inside the block below: the notification render also
    // needs it, and an install with no router record still sends notifications.
    // For up (recovery) events, resolveType holds the matching down alert_type so
    // the WHERE clause in resolveAlertEvent finds the correct open row.
    const alertType = (vars.alertType || key).toLowerCase().replace(/\s+/g, '_');
    if (router && router.id) {
      const subject   = vars.ifaceName || vars.vpnPeer || vars.netwatchName || vars.pingTarget ||
                        vars.bgpPeer || null;
      // The browser emit belongs HERE, beside the unconditional DB write, not
      // below with the push. Everything past this block is gated on a delivery
      // channel being configured — put the emit there and the notification bell
      // goes silent for anyone without Telegram/ntfy/SMTP set up, which is the
      // common case. The bell is a view of what was recorded, so it follows the
      // recording, not the sending.
      if (isUp) {
        const ids = db.resolveAlertEvent(router.id, vars.resolveType || alertType, subject);
        if (ids && ids.length) {
          _emit(router.id, 'alert:resolved', {
            ids, routerId: router.id, routerName: getNameFn(),
            alertType: vars.resolveType || alertType, subject,
            label: labelFor(alertType), detail: vars.detail || null,
            resolvedAt: Date.now(),
          });
        }
      } else {
        // Already open and unresolved: nothing new has happened, so nothing new
        // is recorded, shown or sent. Without this a condition that simply
        // persists — an available update, most visibly — rings the bell again
        // every time the evaluator is rebuilt, and acknowledging it achieves
        // nothing because the next rebuild files a fresh one.
        if (db.hasOpenAlert(router.id, alertType, subject)) return;
        const id = db.insertAlertEvent(router.id, alertType, subject, vars.detail || null);
        _emit(router.id, 'alert:fired', {
          id, routerId: router.id, routerName: getNameFn(),
          alertType, subject,
          label: labelFor(alertType), detail: vars.detail || null,
          firedAt: Date.now(), resolvedAt: null,
          acknowledgedAt: null, acknowledgedBy: null,
        });
      }
    }

    // Render once, then fan out (issue #109). The message is identical for
    // every destination — templates stay install-wide — so it is built before
    // the loop and each recipient only decides whether it wants it.
    //
    // No per-recipient type check: which alerts exist is the install's decision,
    // settled at the top of fire(). A user chooses only where theirs go.
    // alertType is overridden AFTER the spread so the rendered notification uses
    // the same human name as the bell — otherwise a push would say "RouterOS
    // Update" while the alert list said "Update Available".
    const allVars = { routerName: getNameFn(), timestamp: _ts(), ...vars,
                      alertType: labelFor(alertType) };
    const title   = _render(_settings.notifTitle   || 'MikroDash Alert', allVars);
    const bodyTpl = isUp
      ? (_settings.notifBodyUp  || _settings.notifBody || '✅ {{alertType}} on {{routerName}}: {{detail}}')
      : (_settings.notifBody    || '⚠️ {{alertType}} on {{routerName}}: {{detail}}');
    const body = _render(bodyTpl, allVars);

    for (const recipient of _recipients(router && router.id)) {
      _deliver(cooldown, recipient, key, title, body);
    }
  }

  function evaluate(event, data) {
    if (!_settings) return;
    // Re-check alertsEnabled in case it was toggled after session creation.
    const router = typeof getRouterFn === 'function' ? getRouterFn() : null;
    if (router && !router.alertsEnabled) return;

    // Type toggles are checked in fire(), from the keys each rule passes, rather
    // than wrapping the rules here. Same outcome as the original gates — a
    // disabled type is not detected, recorded, belled or sent — but stated once
    // instead of seven times, and reusable per recipient.
    if (event === 'system:update') {
      if (typeof data.cpuLoad === 'number') {
        const isHigh = data.cpuLoad >= _settings.alertCpuThreshold;
        if (isHigh && prevCpuAlert !== true) {
          fire('cpu:router:down', {
            alertType: 'High CPU',
            cpuLoad:   data.cpuLoad + '%',
            detail:    'CPU at ' + data.cpuLoad + '% (threshold: ' + _settings.alertCpuThreshold + '%)',
          }, false, ['notifCpu']);
        } else if (!isHigh && prevCpuAlert === true) {
          fire('cpu:router:up', {
            alertType:   'CPU Normal',
            resolveType: 'high_cpu',
            cpuLoad:     data.cpuLoad + '%',
            detail:      'CPU back to ' + data.cpuLoad + '% (below threshold)',
          }, true, ['notifCpu']);
        }
        prevCpuAlert = isHigh;
      }
    }

    if (event === 'system:update') {
      const latest = data.latestVersion || '';
      if (data.updateAvailable && latest) {
        // Only on a version we have not announced. Without this the alert
        // would repeat on every poll once the cooldown expired.
        if (prevUpdateVersion !== latest) {
          prevUpdateVersion = latest;
          fire('update:router:down', {
            alertType: 'RouterOS Update',
            detail:    'RouterOS ' + latest + ' is available (running ' +
                       ((data.version || '').replace(/\s*\(.*\)/, '').trim() || 'unknown') + ')',
          }, false, ['notifRouterUpdate']);
        }
      } else if (!data.updateAvailable && prevUpdateVersion !== null) {
        // Router reached the version, or the channel changed. Clear the open
        // alert so the Reports tab does not show it pending forever.
        prevUpdateVersion = null;
        fire('update:router:up', {
          alertType:   'RouterOS Updated',
          // Must be the stored form: fire() lowercases and underscores
          // vars.alertType before inserting, but passes resolveType through
          // untouched, so anything else here silently fails to match the row.
          resolveType: 'routeros_update',
          detail:      'RouterOS is up to date (' +
                       ((data.version || '').replace(/\s*\(.*\)/, '').trim() || 'unknown') + ')',
        }, true, ['notifRouterUpdate']);
      }
    }

    if (event === 'ping:update') {
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
          }, false, ['notifPing']);
        } else if (!isLoss && prevPingAlert[target] === true) {
          fire(base + ':up', {
            alertType:   'Ping Restored',
            resolveType: 'ping_loss',
            pingTarget:  data.target || '',
            pingLoss:    data.loss + '%',
            pingRtt:     data.rtt != null ? data.rtt + ' ms' : 'N/A',
            detail:      'Ping to ' + data.target + ' restored',
          }, true, ['notifPing']);
        }
        prevPingAlert[target] = isLoss;
      }
    }

    if (event === 'ifstatus:update' && Array.isArray(data.interfaces)) {
      for (const iface of data.interfaces) {
        const prev       = prevIfState.get(iface.name);
        const wasRunning = prev ? prev.running : undefined;
        const isRunning  = !!iface.running;
        const isDisabled = !!iface.disabled;
        // An interface disabled by an admin also stops running, which used to
        // fire "Interface Down". The disabled flag was already being captured
        // for exactly this purpose and then never read. Suppress both the alert
        // and the recovery when the transition is administrative, so an admin
        // re-enabling it does not produce an unpaired "Interface Up" either.
        const adminToggled = isDisabled || (prev && prev.disabled);
        if (prev !== undefined && wasRunning !== isRunning && !adminToggled) {
          // Two toggles gate an interface alert: the feature itself and the
          // filter for this interface's type. Both are install-wide first — the
          // Interface Alert Filter is expected to filter the bell, not merely
          // the push — and both then travel on to each recipient.
          const ifKeys = ['notifIfaceUpDown', _ifaceTypeKey(_ifaceType(iface.name, iface.type))];
          if (!isRunning) {
            fire('iface:' + iface.name + ':down', { alertType:'Interface Down', ifaceName:iface.name, comment:iface.comment || '', status:'down', detail:iface.name + ' went down' }, false, ifKeys);
          } else {
            fire('iface:' + iface.name + ':up',   { alertType:'Interface Up',   resolveType:'interface_down', ifaceName:iface.name, comment:iface.comment || '', status:'up',   detail:iface.name + ' came up'   }, true, ifKeys);
          }
        }
        _capMap(prevIfState);
        prevIfState.set(iface.name, { running: isRunning, disabled: isDisabled });
      }
    }

    if (event === 'vpn:update' && Array.isArray(data.tunnels)) {
      for (const tunnel of data.tunnels) {
        // VpnCollector.peerState emits 'active' | 'stale' | 'never' — there is no
        // 'connected'. It previously emitted 'connected'/'idle' and this compared
        // against that; when the collector's contract changed this consumer was
        // missed, so wasConn and isConn were both permanently false and VPN
        // alerts could not fire at all. Connected means actively handshaking,
        // which is what the UI badge shows too.
        const prev    = prevVpnState.get(tunnel.name);
        const wasConn = prev === 'active';
        const isConn  = tunnel.state === 'active';
        if (prev !== undefined && wasConn !== isConn) {
          if (!isConn) {
            fire('vpn:' + tunnel.name + ':down', { alertType:'VPN Disconnected', vpnPeer:tunnel.name, comment:tunnel.comment || '', status:'down', detail:'VPN peer ' + tunnel.name + ' disconnected' }, false, ['notifVpn']);
          } else {
            fire('vpn:' + tunnel.name + ':up',   { alertType:'VPN Connected',    resolveType:'vpn_disconnected', vpnPeer:tunnel.name, comment:tunnel.comment || '', status:'up',   detail:'VPN peer ' + tunnel.name + ' connected'    }, true, ['notifVpn']);
          }
        }
        _capMap(prevVpnState);
        prevVpnState.set(tunnel.name, tunnel.state);
      }
    }

    if (event === 'netwatch:update' && Array.isArray(data.hosts)) {
      for (const host of data.hosts) {
        if (host.status === 'unknown') continue; // transient re-probe state — skip to avoid premature fire/resolve
        const prev    = prevNetwatchState.get(host.id);
        const wasDown = prev === 'down';
        const isDown  = host.status === 'down';
        if (prev !== undefined && wasDown !== isDown) {
          const netwatchName = host.name || host.host;
          const netwatchDesc = netwatchName !== host.host ? netwatchName + ' (' + host.host + ')' : host.host;
          if (isDown) {
            fire('netwatch:' + host.id + ':down', { alertType:'Host Down',                            host:host.host, netwatchName, comment:host.comment || '', status:'down', detail:'NetWatch host ' + netwatchDesc + ' is unreachable' }, false, ['notifNetwatch']);
          } else {
            fire('netwatch:' + host.id + ':up',   { alertType:'Host Up', resolveType:'host_down',     host:host.host, netwatchName, comment:host.comment || '', status:'up',   detail:'NetWatch host ' + netwatchDesc + ' is reachable'   }, true, ['notifNetwatch']);
          }
        }
        _capMap(prevNetwatchState);
        prevNetwatchState.set(host.id, host.status);
      }
    }

    // BGP. These used to live in public/app.js and fired straight at the browser
    // Notification API, so they never reached the Reports tab, never honoured
    // alertsEnabled, and used their own 2-minute cooldown instead of
    // notifCooldownSec.
    //
    // Every rule below is EDGE-triggered — fire on entering the condition,
    // resolve on leaving it. The browser versions gated the three level
    // conditions (prefix swing, flapping, hold timer) on a cooldown alone, which
    // meant a peer with hold-time=3s/keepalive=0 re-alerted every 2 minutes
    // forever: the condition is static configuration, so it never stopped being
    // true. A cooldown cannot express "tell me once until it changes"; an edge
    // can, and it is also what gives the bell something to resolve.
    if (event === 'routing:update' && Array.isArray(data.peers)) {
      for (const p of data.peers) {
        const key   = p.key;
        if (!key) continue;
        const peer  = p.name || p.remoteAddr || key;
        const where = p.remoteAddr ? peer + ' (' + p.remoteAddr + ')' : peer;
        // `p.description` IS the peer's RouterOS comment — routing.js reads the
        // comment field into it under that name. That is why every {{comment}}
        // below reads `description`; it is not the wrong field.
        const isEst = p.state === 'established';
        const prev  = prevBgpState.get(key);

        if (prev !== undefined && prev !== isEst) {
          if (!isEst) {
            fire('bgp:' + key + ':down', {
              alertType: 'BGP Peer Down', bgpPeer: peer, comment: p.description || '',
              detail: 'BGP peer ' + where + ' left established (' + (p.state || 'unknown') + ')',
            }, false, ['notifBgp']);
          } else {
            fire('bgp:' + key + ':up', {
              alertType: 'BGP Peer Up', resolveType: 'bgp_peer_down', bgpPeer: peer, comment: p.description || '',
              detail: 'BGP peer ' + where + ' is established',
            }, true, ['notifBgp']);
          }
        }
        _capMap(prevBgpState);
        prevBgpState.set(key, isEst);

        // Prefix swing. Compared against the previous ESTABLISHED reading, so a
        // session bounce does not read as a 100% drop — the peer-down alert
        // already covers that, and counting it twice is noise.
        const oldPfx = prevBgpPfx.get(key);
        if (isEst && typeof p.prefixes === 'number') {
          if (oldPfx !== undefined && oldPfx > 0) {
            const swung = Math.abs(p.prefixes - oldPfx) / oldPfx >= BGP_PFX_THRESH;
            if (swung && !prevBgpPfxAlert.get(key)) {
              const dir = p.prefixes > oldPfx ? '+' : '-';
              fire('bgp-pfx:' + key + ':down', {
                alertType: 'BGP Prefix Change', bgpPeer: peer, comment: p.description || '',
                detail: peer + ': ' + dir + Math.abs(p.prefixes - oldPfx) + ' prefixes (' +
                        oldPfx + ' → ' + p.prefixes + ')',
              }, false, ['notifBgp']);
              prevBgpPfxAlert.set(key, true);
            } else if (!swung && prevBgpPfxAlert.get(key)) {
              // The count held steady for a reading, so the table has settled.
              fire('bgp-pfx:' + key + ':up', {
                alertType: 'BGP Prefixes Settled', resolveType: 'bgp_prefix_change',
                bgpPeer: peer, comment: p.description || '', detail: peer + ': prefix count steady at ' + p.prefixes,
              }, true, ['notifBgp']);
              prevBgpPfxAlert.set(key, false);
            }
          }
          _capMap(prevBgpPfx);
          prevBgpPfx.set(key, p.prefixes);
        }

        const flapping = !!p.flapping;
        if (flapping !== !!prevBgpFlap.get(key)) {
          if (flapping) {
            fire('bgp-flap:' + key + ':down', {
              alertType: 'BGP Session Flapping', bgpPeer: peer, comment: p.description || '',
              detail: 'BGP session ' + where + ' is flapping',
            }, false, ['notifBgp']);
          } else if (prevBgpFlap.has(key)) {
            fire('bgp-flap:' + key + ':up', {
              alertType: 'BGP Session Stable', resolveType: 'bgp_session_flapping',
              bgpPeer: peer, comment: p.description || '', detail: 'BGP session ' + where + ' has stopped flapping',
            }, true, ['notifBgp']);
          }
          _capMap(prevBgpFlap);
          prevBgpFlap.set(key, flapping);
        }

        // Hold timer / keepalive misconfiguration.
        const badHold = isEst && p.holdTime > 0 && p.holdTime < 9 && p.keepalive === 0;
        if (badHold !== !!prevBgpHold.get(key)) {
          if (badHold) {
            fire('bgp-hold:' + key + ':down', {
              alertType: 'BGP Hold Timer Warning', bgpPeer: peer, comment: p.description || '',
              detail: peer + ': hold-time=' + p.holdTime + 's, keepalive=0',
            }, false, ['notifBgp']);
          } else if (prevBgpHold.has(key)) {
            fire('bgp-hold:' + key + ':up', {
              alertType: 'BGP Hold Timer OK', resolveType: 'bgp_hold_timer_warning',
              bgpPeer: peer, comment: p.description || '', detail: peer + ': hold timer no longer misconfigured',
            }, true, ['notifBgp']);
          }
          _capMap(prevBgpHold);
          prevBgpHold.set(key, badHold);
        }
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
  // Router up/down is the one alert that is inherently fleet-wide: the router it
  // concerns is by definition not the one you are looking at when it matters.
  // Broadcast rather than room-scoped, mirroring how `router:status` already
  // reaches every browser — so this adds no exposure the client did not have.
  // The browser shows it only for routers in its own (RBAC-filtered) list.
  const _bcast = (event, payload) => {
    if (!_io) return;
    try { _io.emit(event, payload); } catch (e) { console.warn('[alerter] emit failed:', e.message); }
  };

  if (connected) {
    const ids = db.resolveAlertEvent(routerId, 'connectivity', null);
    if (ids && ids.length) {
      _bcast('alert:resolved', {
        ids, routerId, routerName: routerLabel, alertType: 'connectivity',
        subject: null, label: 'Router Online',
        detail: routerLabel + ' is now reachable', resolvedAt: Date.now(),
      });
    }
  } else {
    // Already recorded as unreachable — say nothing rather than ring again.
    if (db.hasOpenAlert(routerId, 'connectivity', null)) return;
    const id = db.insertAlertEvent(routerId, 'connectivity', null,
      routerLabel + ' is unreachable');
    _bcast('alert:fired', {
      id, routerId, routerName: routerLabel, alertType: 'connectivity',
      subject: null, label: 'Router Offline',
      detail: routerLabel + ' is unreachable',
      firedAt: Date.now(), resolvedAt: null,
      acknowledgedAt: null, acknowledgedBy: null,
    });
  }

  // Render once, then fan out — same shape as fire(), including the
  // Push only when the install allows this category. Unlike fire(), the DB write
  // and broadcast above stay unconditional — they always have, since a router
  // being unreachable is worth recording whether or not anyone asked to be
  // paged about it.
  if (!_settings.notifRouterStatus) return;

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

  const key = 'router-conn:' + routerId + ':' + (connected ? 'up' : 'down');
  for (const recipient of _recipients(routerId)) {
    _deliver(_connCooldowns, recipient, key, title, body, CONN_COOLDOWN_MAX);
  }
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
  // Previously discarded. The alerter is now the single detector for the whole
  // app, so it needs a way to tell the browser what it found.
  _io = io || null;
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

module.exports = { init, updateSettings, createEvaluator, evaluateForRouter, dropEvaluator, fireConnectivityAlert, labelFor };
