'use strict';
/**
 * PPP collector (issue #32 — "Live PPPoE Metrics").
 *
 *   /ppp/active                      the sessions
 *   /ppp/profile                     the profiles they were assigned
 *   /interface/pppoe-server/server   the PPPoE servers that accept them
 *
 * /ppp/secret IS NEVER READ. It stores account passwords in clear text, and a
 * page that lists who is connected has no need of them. vpn.js:279 recorded the
 * same decision; a test now enforces it across both files.
 *
 * WHY THIS DOES NOT SHARE vpn.js's /ppp/active READ. vpn.js frames that table as
 * the VPN tunnel view — L2TP, PPTP, SSTP, OpenVPN — and renders it on the VPN
 * page. This is the PPPoE session-metrics view. Having either collector consume
 * the other would couple two independently disableable collectors: turning off
 * one page would silently empty a card on the other. The duplicate print is
 * cheap and self-limiting (vpn.js backs off to 60s when empty and stops
 * entirely when the subsystem is absent), and this collector only runs while
 * somebody is on its page. The overlap is deliberate, not an oversight.
 *
 * NOT VERIFIED AGAINST HARDWARE. The fleet this was written on runs no PPP at
 * all — /ppp/active returns zero rows and /ppp/secret returns the empty-menu
 * junk row. Every session-shaped field below comes from the RouterOS field
 * reference and fixtures. The empty state is the only part real hardware has
 * exercised.
 */

const { clampPoll, createPollLoop, createListenRefresh } = require('./util');

const ACTIVE_CMD  = ['/ppp/active/print',
                     '=.proplist=.id,name,service,caller-id,address,uptime,encoding,session-id,limit-bytes-in,limit-bytes-out,bytes-in,bytes-out'];
const PROFILE_CMD = ['/ppp/profile/print',
                     '=.proplist=.id,name,local-address,remote-address,rate-limit,only-one,use-encryption'];
const SERVER_CMD  = ['/interface/pppoe-server/server/print',
                     '=.proplist=.id,service-name,interface,disabled,max-sessions,authentication'];

const LISTEN_CMD = '/ppp/active/listen';

// Config re-read every N ticks; sessions are read every tick.
const CONFIG_EVERY = 12;
// Bytes unchanged for longer than this means idle, not "still at the last rate".
const IDLE_AFTER_SEC = 10;

const _int = (v) => {
  const n = parseInt(String(v == null ? '' : v), 10);
  return Number.isFinite(n) ? n : 0;
};

/**
 * Parse /ppp/active rows into sessions with DERIVED RATES.
 *
 * RouterOS reports cumulative bytes only, so "real-time bandwidth consumption
 * per user" — the actual ask in #32 — has to come from deltas. `prev` is the
 * caller's state map and is mutated; `now` is injected so the arithmetic is
 * testable without touching the clock.
 */
function parsePppSessions(rows, prev, now) {
  const out = [];
  const live = new Set();
  for (const r of rows || []) {
    // Drops the {undefined:''} row RouterOS returns for an empty menu.
    if (!r || !r.name) continue;
    const rx = _int(r['bytes-in']);
    const tx = _int(r['bytes-out']);
    const key = r['.id'] || (r.name + '/' + (r.service || ''));
    live.add(key);

    const p = prev.get(key);
    // null on the first sample, not 0: there is no measurement window yet, and
    // reporting 0 would claim an idle session that may be saturating the line.
    let rxRate = null, txRate = null;
    if (p && now > p.ts) {
      const dtSec = (now - p.ts) / 1000;
      // Clamped at 0: a session that reconnects restarts its counters, and a
      // negative rate is worse than a missed sample.
      rxRate = Math.max(0, (rx - p.rx) / dtSec);
      txRate = Math.max(0, (tx - p.tx) / dtSec);
      if (rx === p.rx && tx === p.tx && dtSec > IDLE_AFTER_SEC) { rxRate = 0; txRate = 0; }
    }
    // Only advance the timestamp when the bytes actually moved, so dtSec always
    // spans a real window even when polls land between counter updates.
    if (!p || rx !== p.rx || tx !== p.tx) prev.set(key, { rx, tx, ts: now });

    out.push({
      id:       r['.id'] || '',
      name:     String(r.name),
      service:  String(r.service || '').toUpperCase(),
      address:  r.address || '',
      callerId: r['caller-id'] || '',
      uptime:   r.uptime || '',
      encoding: r.encoding || '',
      sessionId: r['session-id'] || '',
      limitIn:  r['limit-bytes-in']  ? _int(r['limit-bytes-in'])  : null,
      limitOut: r['limit-bytes-out'] ? _int(r['limit-bytes-out']) : null,
      rx, tx, rxRate, txRate,
    });
  }
  for (const k of [...prev.keys()]) if (!live.has(k)) prev.delete(k);
  out.sort((a, b) => a.name.localeCompare(b.name));
  return out;
}

class PppCollector {
  constructor({ ros, io, state, pollMs, streamMode }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 5000, 60000, 2000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][ppp]` : '[ppp]';

    // Delivery switch (#105), and it is worth being straight about what it buys
    // here: /ppp/active/listen fires when a session comes or goes, so stream
    // mode shows a connect or disconnect immediately instead of up to one
    // interval later. It does NOT save the periodic read — per-session rates are
    // derived from byte counters, and those change with no event behind them.
    // The saving is the profile and server tables, which stream mode re-reads
    // only when something changed. Poll mode holds no channel open.
    this.streamMode = streamMode !== false;
    this._poll     = createPollLoop(() => this._tick(), () => this.pollMs);
    this._listen   = createListenRefresh({
      ros, cmd: LISTEN_CMD, label: this._lbl,
      onEvent: () => { this._dirty = true; this._tick().catch(() => {}); },
    });
    this._dirty    = true;
    this._prev     = new Map();   // session key -> { rx, tx, ts }
    this._sessions = [];
    this._profiles = [];
    this._servers  = [];
    this._ticks    = 0;
    this._lastFp   = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._activeAvailable  = undefined;
    this._profileAvailable = undefined;
    this._serverAvailable  = undefined;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const rows = await this.ros.write(cmd[0], [cmd[1]]);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      // Latched off, the same way vpn.js does: a router without PPP should be
      // asked once, not every five seconds forever.
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      else this.state.lastPppErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  async _loadConfig() {
    this._profiles = (await this._read(PROFILE_CMD, '_profileAvailable')).map(r => ({
      name:          r.name || '',
      localAddress:  r['local-address'] || '',
      remoteAddress: r['remote-address'] || '',
      rateLimit:     r['rate-limit'] || '',
      onlyOne:       r['only-one'] || '',
      encryption:    r['use-encryption'] || '',
    })).filter(p => p.name);

    this._servers = (await this._read(SERVER_CMD, '_serverAvailable')).map(r => ({
      serviceName: r['service-name'] || '',
      interface:   r.interface || '',
      maxSessions: r['max-sessions'] || '',
      auth:        r.authentication || '',
      disabled:    r.disabled === 'true' || r.disabled === true,
    })).filter(s => s.interface || s.serviceName);
  }

  _emit() {
    const byService = {};
    for (const s of this._sessions) {
      const k = s.service || 'OTHER';
      byService[k] = (byService[k] || 0) + 1;
    }
    const known = this._sessions.filter(s => s.rxRate !== null);
    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      sessions: this._sessions,
      profiles: this._profiles,
      servers:  this._servers,
      byService,
      totalRxRate: known.length ? known.reduce((n, s) => n + s.rxRate, 0) : null,
      totalTxRate: known.length ? known.reduce((n, s) => n + s.txRate, 0) : null,
      // So the page can say "this router has no PPP service" rather than just
      // showing an empty table, which reads as a failure.
      available: this._activeAvailable !== false,
    };
    this.lastPayload = payload;
    this.state.lastPppTs = payload.ts;

    const fp = JSON.stringify(this._sessions.map(s => [s.id, s.name, s.service, s.address, s.rx, s.tx]))
             + '|' + this._profiles.length + '|' + this._servers.length + '|' + payload.available;
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-ppp').emit('ppp:update', payload);
  }

  async _tick() {
    if (!this.ros.connected) return;
    if (!this.streamMode || this._dirty || this._ticks % CONFIG_EVERY === 0) {
      await this._loadConfig();
      this._dirty = false;
    }
    this._ticks++;
    const rows = await this._read(ACTIVE_CMD, '_activeAvailable');
    this._sessions = parsePppSessions(rows, this._prev, Date.now());
    this.state.lastPppErr = this.state.lastPppErr || null;
    this._emit();
  }

  _startDelivery() {
    this._poll.start();
    if (this.streamMode) this._listen.start();
  }

  _stopDelivery() {
    this._poll.stop();
    this._listen.stop();
  }

  async start() {
    if (this.ros.connected) { await this._tick(); }
    this._startDelivery();
    this.ros.on('close', () => this._stopDelivery());
    this.ros.on('connected', async () => {
      this._stopDelivery();
      // A reconnect may be a different router, and session byte counters restart
      // anyway, so the rate baseline must go with it.
      this._prev.clear();
      this._lastFp = '';
      this._ticks = 0;
      this._activeAvailable = this._profileAvailable = this._serverAvailable = undefined;
      this._dirty = true;
      await this._tick();
      this._startDelivery();
    });
  }

  suspend() { this._stopDelivery(); }
  resume()  { if (this.ros.connected) this._startDelivery(); }

  stop() { this._stopDelivery(); this._lastFp = ''; }
}

PppCollector.parsePppSessions = parsePppSessions;
module.exports = PppCollector;
