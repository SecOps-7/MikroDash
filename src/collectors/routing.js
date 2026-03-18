/**
 * Routing collector — fully streaming: /ip/route/listen + /routing/bgp/session/listen.
 *
 * Both the route table and BGP session state are event-driven:
 *
 *  /ip/route/listen        — fires on every route add/remove/change.
 *                            In-memory Map keyed by .id, updated via delta rows.
 *
 *  /routing/bgp/session/listen — fires on every session state change AND every
 *                            keepalive exchange (~30s/peer). Keepalive-only
 *                            updates are suppressed by fingerprinting session
 *                            state — the socket emit is skipped when nothing
 *                            meaningful has changed.
 *
 *  /routing/bgp/peer/print — loaded once on connect for peer names/descriptions.
 *                            Refreshed when a session state change is detected.
 *
 * A 60-second heartbeat re-emits the last payload so the client stale timer
 * never fires on a stable network.
 */

const HISTORY_LEN = 60;

// parseInt that returns 0 instead of NaN for non-numeric strings
const safeInt = (v) => parseInt(v || '0', 10) || 0;

class RoutingCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000; // retained for Settings UI; no longer drives polling
    this.state  = state;
    this.timer  = null; // unused — kept so shutdown loop / settings code are safe

    // Route table — keyed by RouterOS .id for O(1) stream delta updates
    this._routes = new Map();

    // BGP session state — keyed by peer key
    this._sessions   = new Map(); // key -> raw session row (merged)
    this._peerCfg    = new Map(); // remote-address -> config row (names/descriptions)
    this._sessionsFp = '';        // fingerprint for keepalive suppression

    // Per-peer prefix history and flap detection
    this._prefixHistory = new Map();
    this._peerState     = new Map();

    this.lastPayload = null;

    // Stream handles
    this._routeStream   = null;
    this._bgpStream     = null;

    // Restart state (one set per stream)
    this._routeRestarting  = false;
    this._routeRestartTimer = null;
    this._bgpRestarting    = false;
    this._bgpRestartTimer  = null;

    this._heartbeat = null;
  }

  // ── helpers ───────────────────────────────────────────────────────────────

  _classifyPeer(remoteAs, description, name) {
    const desc = (description + ' ' + name).toLowerCase();
    if ((remoteAs >= 64512 && remoteAs <= 65534) ||
        (remoteAs >= 4200000000 && remoteAs <= 4294967294)) return 'private';
    if (/\b(ix|ixp|peering|rs\d|route.server|routeserver)\b/.test(desc)) return 'ix';
    return 'upstream';
  }

  async _safeWrite(cmd, args) {
    try {
      const r = await this.ros.write(cmd, args || []);
      return Array.isArray(r) ? r : [];
    } catch (_) { return []; }
  }

  _parseUptime(s) {
    if (!s) return 0;
    const hms = s.match(/^(\d+):(\d+):(\d+)$/);
    if (hms) return parseInt(hms[1])*3600 + parseInt(hms[2])*60 + parseInt(hms[3]);
    let sec = 0;
    const d = s.match(/(\d+)d/); if (d) sec += parseInt(d[1]) * 86400;
    const h = s.match(/(\d+)h/); if (h) sec += parseInt(h[1]) * 3600;
    const m = s.match(/(\d+)m/); if (m) sec += parseInt(m[1]) * 60;
    const t = s.match(/(\d+)s/); if (t) sec += parseInt(t[1]);
    return sec;
  }

  _peerKey(p) {
    return (p['remote.address'] || p['remote-address'] || p.name || '?');
  }

  // ── route parsing ─────────────────────────────────────────────────────────

  _parseFlags(r) {
    const f = (r['.flags'] || r.flags || '').toString();
    const has = (k) => r[k] === 'true' || r[k] === true;
    return {
      active:  f.includes('A') || f.includes('a') || has('active'),
      static:  f.includes('S') || f.includes('s') || has('static'),
      dynamic: f.includes('D') || has('dynamic'),
      connect: f.includes('C') || f.includes('c') || has('connect'),
      bgp:     f.includes('b') || f.includes('B') || has('bgp'),
      ospf:    f.includes('o') || f.includes('O') || has('ospf'),
      disabled:f.includes('X') || f.includes('x') || has('disabled'),
    };
  }

  _mapRoute(r) {
    const flags   = this._parseFlags(r);
    const gateway = r.gateway || '';

    const hasTypeInfo    = flags.static || flags.dynamic || flags.connect ||
                           flags.bgp    || flags.ospf;
    const hasRealNexthop = gateway !== '' && gateway !== '0.0.0.0' &&
                           gateway !== '::' &&
                           (/^(\d{1,3}\.){3}\d{1,3}$/.test(gateway) || gateway.includes(':'));
    if (!hasTypeInfo && hasRealNexthop) flags.static = true;

    const type     = flags.static  ? 'static'  :
                     flags.dynamic ? 'dynamic' : 'connect';
    const protocol = flags.bgp     ? 'bgp'     :
                     flags.ospf    ? 'ospf'    : type;

    return {
      _id:  r['.id'] || '',
      _raw: r,
      dst:      r['dst-address'] || '',
      gateway,
      distance: safeInt(r.distance),
      active:   flags.active,
      comment:  r.comment || '',
      type,
      protocol,
      flags,
    };
  }

  _applyRouteDelta(data) {
    const id = data['.id'];
    if (!id) return;
    if (data['.dead'] === 'true' || data['.dead'] === true) {
      this._routes.delete(id);
      return;
    }
    const existing = this._routes.get(id);
    const merged   = existing ? Object.assign({}, existing._raw, data) : data;
    this._routes.set(id, this._mapRoute(merged));
  }

  // ── BGP session parsing ───────────────────────────────────────────────────

  // Apply a stream delta from /routing/bgp/session/listen.
  // Returns true if a meaningful state change occurred (not just keepalive).
  _applySessionDelta(data) {
    const key = this._peerKey(data);
    if (!key || key === '?') return false;

    if (data['.dead'] === 'true' || data['.dead'] === true) {
      const changed = this._sessions.has(key);
      this._sessions.delete(key);
      return changed;
    }

    const existing = this._sessions.get(key);
    const merged   = existing ? Object.assign({}, existing, data) : data;
    this._sessions.set(key, merged);

    // Fingerprint only the fields that indicate a meaningful change.
    // Keepalive exchanges update uptime and counters — suppress those.
    const fp = JSON.stringify(
      Array.from(this._sessions.entries()).map(([k, s]) => ({
        k,
        state:    s.state || s.established,
        prefixes: s['prefix-count'],
        error:    s['last-notification'] || s['inactive-reason'] || '',
      }))
    );
    if (fp === this._sessionsFp) return false;
    this._sessionsFp = fp;
    return true;
  }

  // Build the peers array from current _sessions and _peerCfg state.
  _buildPeers() {
    const now = Date.now();
    const peers = [];

    for (const [, s] of this._sessions) {
      const remoteAddr = s['remote.address'] || s['remote-address'] || '';
      const cfg        = this._peerCfg.get(remoteAddr) || {};
      const key        = this._peerKey(s);

      // Skip ghost rows (no address, no meaningful name)
      const name = (s.name || '').trim();
      if (!remoteAddr && (!name || name === '?')) continue;

      const remoteAs  = safeInt(s['remote.as'] || s['remote-as'] || cfg['remote.as'] || cfg['remote-as']);
      const prefixes  = safeInt(s['prefix-count']);
      const uptimeSec = this._parseUptime(s.uptime);

      const rawState = (s.state || (s.established === 'true' || s.established === true ? 'established' : 'idle')).toLowerCase();
      const state =
        rawState.includes('establish') ? 'established' :
        rawState.includes('active')    ? 'active'      :
        rawState.includes('connect')   ? 'connect'     :
        rawState.includes('opensent')  ? 'opensent'    :
        rawState.includes('openconfirm') ? 'openconfirm' :
        rawState.includes('idle')      ? 'idle'        : rawState;

      if (!this._prefixHistory.has(key)) this._prefixHistory.set(key, []);
      const hist = this._prefixHistory.get(key);
      hist.push({ ts: now, v: prefixes });
      if (hist.length > HISTORY_LEN) hist.shift();

      const FLAP_WINDOW = 5 * 60 * 1000;
      const FLAP_THRESH = 3;
      if (!this._peerState.has(key)) this._peerState.set(key, { lastState: state, lastChange: now, flapWindow: [] });
      const ps = this._peerState.get(key);
      let flapping = false;
      if (ps.lastState !== state) {
        ps.flapWindow.push(now);
        ps.flapWindow = ps.flapWindow.filter(t => now - t < FLAP_WINDOW);
        flapping = ps.flapWindow.length >= FLAP_THRESH;
        ps.lastState  = state;
        ps.lastChange = now;
      }

      peers.push({
        key, peerType: this._classifyPeer(remoteAs, cfg.comment || '', s.name || cfg.name || ''),
        name:        s.name || cfg.name || remoteAddr || '?',
        description: cfg.comment || '',
        remoteAddr, remoteAs, state, uptimeSec, prefixes,
        prefixHistory: hist.map(h => h.v),
        updatesSent: safeInt(s['updates-sent']),
        updatesRecv: safeInt(s['updates-received']),
        lastError:   s['last-notification'] || s['inactive-reason'] || s['last-error'] || '',
        holdTime:    safeInt(s['hold-time']),
        keepalive:   safeInt(s['keepalive-time']),
        flapping,
      });
    }

    // Prune history for sessions no longer present
    const liveKeys = new Set(peers.map(p => p.key));
    for (const k of this._prefixHistory.keys()) { if (!liveKeys.has(k)) this._prefixHistory.delete(k); }
    for (const k of this._peerState.keys())     { if (!liveKeys.has(k)) this._peerState.delete(k); }

    return peers;
  }

  // ── emit ──────────────────────────────────────────────────────────────────

  _emit(peers) {
    const now       = Date.now();
    const allRoutes = Array.from(this._routes.values());

    const routes = allRoutes
      .filter(r => r.type === 'static' || r.type === 'dynamic')
      .slice(0, 400)
      .map(({ _id, _raw, flags, ...r }) => r);

    const routeCounts = {
      total:   allRoutes.length,
      connect: allRoutes.filter(r => r.flags.connect).length,
      static:  allRoutes.filter(r => r.flags.static).length,
      dynamic: allRoutes.filter(r => r.flags.dynamic).length,
      bgp:     allRoutes.filter(r => r.flags.bgp).length,
      ospf:    allRoutes.filter(r => r.flags.ospf).length,
    };

    const usePeers     = peers !== null ? peers : (this.lastPayload ? this.lastPayload.peers : []);
    const established  = usePeers.filter(p => p.state === 'established').length;
    const down         = usePeers.filter(p => p.state !== 'established').length;

    const payload = {
      ts: now,
      pollMs: 0, // streamed
      routeCounts,
      peers:   usePeers,
      routes,
      summary: { total: usePeers.length, established, down },
    };
    this.lastPayload          = payload;
    this.state.lastRoutingTs  = now;
    this.state.lastRoutingErr = null;
    this.io.emit('routing:update', payload);
  }

  // ── initial data load ─────────────────────────────────────────────────────

  async _loadRoutes() {
    const rows = await this._safeWrite('/ip/route/print', [
      '=.proplist=.id,dst-address,gateway,distance,comment,.flags,active,static,dynamic,connect,bgp,ospf,disabled',
    ]);
    this._routes.clear();
    for (const r of rows) {
      if (r['.id']) this._routes.set(r['.id'], this._mapRoute(r));
    }
  }

  async _loadBgpSessions() {
    // Try v7 session endpoint first, fall back to legacy peer endpoint
    let rows = await this._safeWrite('/routing/bgp/session/print', [
      '=.proplist=name,remote.address,remote.as,local.role,established,uptime,' +
      'prefix-count,updates-sent,updates-received,state,last-notification,' +
      'inactive-reason,hold-time,keepalive-time',
    ]);
    if (!rows.length) {
      rows = await this._safeWrite('/routing/bgp/peer/print', [
        '=.proplist=name,remote-address,remote-as,state,uptime,' +
        'prefix-count,updates-sent,updates-received,last-error',
      ]);
    }
    this._sessions.clear();
    this._sessionsFp = '';
    for (const r of rows) {
      const key = this._peerKey(r);
      if (key && key !== '?') this._sessions.set(key, r);
    }
  }

  async _loadPeerCfg() {
    const rows = await this._safeWrite('/routing/bgp/peer/print', [
      '=.proplist=name,remote.address,remote-address,remote.as,remote-as,comment',
    ]);
    this._peerCfg.clear();
    for (const p of rows) {
      const addr = p['remote.address'] || p['remote-address'] || '';
      if (addr) this._peerCfg.set(addr, p);
    }
  }

  // ── stream management ─────────────────────────────────────────────────────

  _startRouteStream() {
    if (this._routeStream || !this.ros.connected) return;
    try {
      this._routeStream = this.ros.stream(['/ip/route/listen'], (err, data) => {
        if (err) {
          const msg = err && err.message ? err.message : String(err);
          console.error('[routing] route stream error:', msg);
          this.state.lastRoutingErr = msg;
          this._stopRouteStream();
          if (this.ros.connected && !this._routeRestarting) {
            this._routeRestarting = true;
            this._routeRestartTimer = setTimeout(async () => {
              this._routeRestarting    = false;
              this._routeRestartTimer  = null;
              if (!this.ros.connected) return;
              await this._loadRoutes();
              this._emit(null);
              this._startRouteStream();
            }, 3000);
          }
          return;
        }
        if (data) {
          this._applyRouteDelta(data);
          this._emit(null);
        }
      });
      console.log('[routing] streaming /ip/route/listen');
    } catch (e) {
      console.error('[routing] route stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopRouteStream() {
    if (this._routeRestartTimer) { clearTimeout(this._routeRestartTimer); this._routeRestartTimer = null; }
    this._routeRestarting = false;
    if (this._routeStream) { try { this._routeStream.stop(); } catch (_) {} this._routeStream = null; }
  }

  _startBgpStream() {
    if (this._bgpStream || !this.ros.connected) return;
    try {
      this._bgpStream = this.ros.stream(['/routing/bgp/session/listen'], async (err, data) => {
        if (err) {
          const msg = err && err.message ? err.message : String(err);
          console.error('[routing] BGP session stream error:', msg);
          this.state.lastRoutingErr = msg;
          this._stopBgpStream();
          if (this.ros.connected && !this._bgpRestarting) {
            this._bgpRestarting = true;
            this._bgpRestartTimer = setTimeout(async () => {
              this._bgpRestarting    = false;
              this._bgpRestartTimer  = null;
              if (!this.ros.connected) return;
              await this._loadBgpSessions();
              await this._loadPeerCfg();
              this._emit(this._buildPeers());
              this._startBgpStream();
            }, 3000);
          }
          return;
        }
        if (data) {
          const changed = this._applySessionDelta(data);
          if (changed) {
            // Reload peer config on state changes so new peers get their descriptions
            await this._loadPeerCfg();
            this._emit(this._buildPeers());
          }
        }
      });
      console.log('[routing] streaming /routing/bgp/session/listen');
    } catch (e) {
      // BGP session stream may not be available on RouterOS v6 or non-BGP builds.
      // Log at debug level and fall back gracefully — route data is still streamed.
      if (process.env.ROS_DEBUG === 'true') {
        console.warn('[routing] BGP session stream unavailable:', e && e.message ? e.message : e);
      }
      this._bgpStream = null;
    }
  }

  _stopBgpStream() {
    if (this._bgpRestartTimer) { clearTimeout(this._bgpRestartTimer); this._bgpRestartTimer = null; }
    this._bgpRestarting = false;
    if (this._bgpStream) { try { this._bgpStream.stop(); } catch (_) {} this._bgpStream = null; }
  }

  _stopAllStreams() {
    this._stopRouteStream();
    this._stopBgpStream();
  }

  // ── heartbeat ─────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.emit('routing:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  stop() {
    // Retained for the settings live-update loop compatibility (col.timer check).
    // Routing has no poll timer — this is a no-op but must not throw.
    if (this.timer) { clearInterval(this.timer); this.timer = null; }
  }

  async start() {
    await this._loadRoutes();
    await this._loadBgpSessions();
    await this._loadPeerCfg();
    this._emit(this._buildPeers());

    this._startRouteStream();
    this._startBgpStream();
    this._startHeartbeat();

    this.ros.on('close', () => {
      this._stopAllStreams();
      this._stopHeartbeat();
    });
    this.ros.on('connected', async () => {
      this._stopAllStreams();
      this._stopHeartbeat();
      this._peerState.clear();
      this._sessions.clear();
      this._sessionsFp = '';
      this._routes.clear();
      await this._loadRoutes();
      await this._loadBgpSessions();
      await this._loadPeerCfg();
      this._emit(this._buildPeers());
      this._startRouteStream();
      this._startBgpStream();
      this._startHeartbeat();
    });
  }
}

module.exports = RoutingCollector;
