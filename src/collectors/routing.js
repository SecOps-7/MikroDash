/**
 * Routing collector — BGP session state + route table summary.
 *
 * RouterOS v7 exposes BGP sessions at /routing/bgp/session/print and
 * configuration at /routing/bgp/peer/print (pre-v7: /routing/bgp/peer/print).
 * Route counts are derived from /ip/route/print with count-only flags.
 *
 * BGP sessions are polled — /routing/bgp/session/listen exists but fires on
 * every keepalive exchange (every ~30s per peer), making it noisier than a
 * clean 10s poll. Route counts change infrequently so a 15s poll is fine.
 *
 * Prefix history (last 60 samples per peer) is maintained server-side to
 * drive per-peer sparklines on the client.
 */

const HISTORY_LEN = 60; // samples kept per peer for sparkline

class RoutingCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 10000;
    this.state  = state;
    this.timer  = null;
    this._inflight = false;

    // Per-peer prefix history: key -> circular array of {ts, prefixCount}
    this._prefixHistory = new Map();

    // Flap detection: key -> { lastState, lastChange, flapCount, flapWindow: [] }
    this._peerState = new Map();

    this.lastPayload = null;
  }

  // ── helpers ───────────────────────────────────────────────────────────────

  // Classify peer type for group-by filter.
  // Uses RFC6996 private ASN ranges and description keywords as heuristics.
  _classifyPeer(remoteAs, description, name) {
    const desc = (description + ' ' + name).toLowerCase();
    // Private / internal ASNs
    if ((remoteAs >= 64512 && remoteAs <= 65534) ||
        (remoteAs >= 4200000000 && remoteAs <= 4294967294)) return 'private';
    // IX / route-server heuristics
    if (/(ix|ixp|peering|rs\d|route.server|routeserver)/.test(desc)) return 'ix';
    // Upstream / transit fallback
    return 'upstream';
  }

  async _safeWrite(cmd, args) {
    try {
      const r = await this.ros.write(cmd, args || []);
      return Array.isArray(r) ? r : [];
    } catch (_) { return []; }
  }

  _parseUptime(s) {
    // RouterOS uptime: "3d4h12m5s" or "12:34:56" — return seconds
    if (!s) return 0;
    // hh:mm:ss format
    const hms = s.match(/^(\d+):(\d+):(\d+)$/);
    if (hms) return parseInt(hms[1])*3600 + parseInt(hms[2])*60 + parseInt(hms[3]);
    // d/h/m/s format
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

  // ── main tick ─────────────────────────────────────────────────────────────

  async tick() {
    if (!this.ros.connected) return;
    const now = Date.now();

    // ── 1. BGP sessions (ROS v7 path, falls back to legacy) ─────────────────
    let sessions = await this._safeWrite('/routing/bgp/session/print', [
      '=.proplist=name,remote.address,remote.as,local.role,established,uptime,' +
      'prefix-count,updates-sent,updates-received,state,last-notification,' +
      'inactive-reason,remote.id,hold-time,keepalive-time,output.filter,input.filter',
    ]);

    // Legacy ROS v6 path
    if (!sessions.length) {
      sessions = await this._safeWrite('/routing/bgp/peer/print', [
        '=.proplist=name,remote-address,remote-as,state,uptime,' +
        'prefix-count,updates-sent,updates-received,last-error',
      ]);
    }

    // ── 2. BGP peer config for names / descriptions ─────────────────────────
    const peerCfg = await this._safeWrite('/routing/bgp/peer/print', [
      '=.proplist=name,remote.address,remote-address,remote.as,remote-as,comment',
    ]);
    const cfgByAddr = new Map();
    for (const p of peerCfg) {
      const addr = p['remote.address'] || p['remote-address'] || '';
      if (addr) cfgByAddr.set(addr, p);
    }

    // Route counts are derived after the route table fetch below (step 5),
    // using the same rows — no extra API call needed.

    // ── 4. Process BGP sessions ──────────────────────────────────────────────
    // Filter out any row that has no usable remote address and no meaningful name.
    // peerCfg rows (config objects with no active session) and blank summary rows
    // can appear in some ROS builds and would render as ghost "?" peers.
    const validSessions = sessions.filter(s => {
      const addr = s['remote.address'] || s['remote-address'] || '';
      const name = (s.name || '').trim();
      return addr !== '' || (name !== '' && name !== '?');
    });

    const peers = validSessions.map(s => {
      const remoteAddr = s['remote.address'] || s['remote-address'] || '';
      const cfg        = cfgByAddr.get(remoteAddr) || {};
      const key        = this._peerKey(s);

      const remoteAs   = parseInt(s['remote.as'] || s['remote-as'] || cfg['remote.as'] || cfg['remote-as'] || '0', 10);
      const prefixes   = parseInt(s['prefix-count'] || '0', 10);
      const uptimeSec  = this._parseUptime(s.uptime);

      // Normalise state string
      const rawState = (s.state || (s.established === 'true' || s.established === true ? 'established' : 'idle')).toLowerCase();
      const state =
        rawState.includes('establish') ? 'established' :
        rawState.includes('active')    ? 'active' :
        rawState.includes('connect')   ? 'connect' :
        rawState.includes('opensent')  ? 'opensent' :
        rawState.includes('openconfirm')? 'openconfirm' :
        rawState.includes('idle')      ? 'idle' : rawState;

      // Prefix history
      if (!this._prefixHistory.has(key)) this._prefixHistory.set(key, []);
      const hist = this._prefixHistory.get(key);
      hist.push({ ts: now, v: prefixes });
      if (hist.length > HISTORY_LEN) hist.shift();

      // Flap detection — track state transitions within a 5-minute window
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

      const peerType = this._classifyPeer(remoteAs, cfg.comment || '', s.name || cfg.name || '');
      return {
        key,
        peerType,
        name:        s.name || cfg.name || remoteAddr || '?',
        description: cfg.comment || '',
        remoteAddr,
        remoteAs,
        state,
        uptimeSec,
        prefixes,
        prefixHistory: hist.map(h => h.v),
        updatesSent: parseInt(s['updates-sent']     || '0', 10),
        updatesRecv: parseInt(s['updates-received'] || '0', 10),
        lastError:   s['last-notification'] || s['inactive-reason'] || s['last-error'] || '',
        holdTime:    parseInt(s['hold-time'] || '0', 10),
        keepalive:   parseInt(s['keepalive-time'] || '0', 10),
        flapping,
      };
    });

    // ── 5. Fetch all routes in one call, classify via flags string ──────────
    // RouterOS v7 only sends boolean flag fields (static, dynamic, active etc.)
    // when they are TRUE — absent = false. The most reliable cross-version
    // approach is to request the .flags field which is a compact string of
    // flag characters (e.g. "DAb" = Dynamic, Active, bgp) and parse it.
    //
    // RouterOS /ip/route flag characters:
    //   A = active    C = connect    S = static    r/D = dynamic/rip
    //   b/B = bgp     o/O = ospf     e = ecmp      d = dhcp
    //
    // We also request dst-address, gateway, distance, comment as data fields.
    const allRouteRows = await this._safeWrite('/ip/route/print', [
      '=.proplist=.id,dst-address,gateway,distance,comment,.flags,active,static,dynamic,connect,bgp,ospf',
    ]);

    // Parse flags — works whether RouterOS sends the compact .flags string
    // or the individual boolean fields (handles both v6 and v7 builds).
    const parseFlags = (r) => {
      const f = (r['.flags'] || r.flags || '').toString();
      // Individual boolean fields (present = true, absent = false/undefined)
      const hasField = (k) => r[k] === 'true' || r[k] === true;
      return {
        active:  f.includes('A') || f.includes('a') || hasField('active'),
        static:  f.includes('S') || f.includes('s') || hasField('static'),
        dynamic: f.includes('D') || f.includes('d') || hasField('dynamic'),
        connect: f.includes('C') || f.includes('c') || hasField('connect'),
        bgp:     f.includes('b') || f.includes('B') || hasField('bgp'),
        ospf:    f.includes('o') || f.includes('O') || hasField('ospf'),
      };
    };

    const mapRoute = (r) => {
      const flags    = parseFlags(r);
      const type     = flags.static  ? 'static'  :
                       flags.dynamic ? 'dynamic' : 'connect';
      const protocol = flags.bgp     ? 'bgp'     :
                       flags.ospf    ? 'ospf'    : type;
      return {
        dst:      r['dst-address'] || '',
        gateway:  r.gateway        || '',
        distance: parseInt(r.distance || '0', 10),
        active:   flags.active,
        comment:  r.comment || '',
        type,
        protocol,
        _flags:   flags, // keep for counting below
      };
    };

    const mappedRoutes = allRouteRows.map(mapRoute);

    // Only include static and dynamic in the routes table (skip pure connected)
    const routes = mappedRoutes
      .filter(r => r.type === 'static' || r.type === 'dynamic')
      .slice(0, 400)
      .map(({ _flags, ...r }) => r); // strip internal _flags field

    // ── Route counts derived from the same fetch ──────────────────────────
    const routeCounts = {
      total:   mappedRoutes.length,
      connect: mappedRoutes.filter(r => r._flags.connect).length,
      static:  mappedRoutes.filter(r => r._flags.static).length,
      dynamic: mappedRoutes.filter(r => r._flags.dynamic).length,
      bgp:     mappedRoutes.filter(r => r._flags.bgp).length,
      ospf:    mappedRoutes.filter(r => r._flags.ospf).length,
    };

    // Prune history for peers no longer present
    const liveKeys = new Set(peers.map(p => p.key));
    for (const k of this._prefixHistory.keys()) { if (!liveKeys.has(k)) this._prefixHistory.delete(k); }
    for (const k of this._peerState.keys())     { if (!liveKeys.has(k)) this._peerState.delete(k); }

    const established = peers.filter(p => p.state === 'established').length;
    const down        = peers.filter(p => p.state !== 'established').length;

    const payload = {
      ts: now,
      pollMs: this.pollMs,
      routeCounts,
      peers,
      routes,
      summary: { total: peers.length, established, down },
    };
    this.lastPayload = payload;
    // Always emit so stale timers on the client reset every poll cycle.
    // Routing data includes live prefix histories so suppressing identical
    // payloads would prevent the stale timer from resetting on stable networks.
    this.io.emit('routing:update', payload);

    this.state.lastRoutingTs  = now;
    this.state.lastRoutingErr = null;
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastRoutingErr = String(e && e.message ? e.message : e);
        console.error('[routing]', this.state.lastRoutingErr);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close',     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => {
      this._peerState.clear();
      this.timer = this.timer || setInterval(run, this.pollMs);
      run();
    });
  }
}

module.exports = RoutingCollector;
