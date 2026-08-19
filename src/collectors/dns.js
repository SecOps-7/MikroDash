'use strict';
/**
 * DNS collector.
 *
 *   /ip/dns          resolver settings — servers, DoH, cache size and usage
 *   /ip/dns/static   static entries
 *
 * THE CACHE CONTENTS ARE NOT READ. `/ip/dns/cache` holds every name the router
 * has resolved — hundreds of rows on an idle home router, thousands on a busy
 * resolver — and enumerating it is a standing cost for a table nobody asked to
 * browse. The cache-used/cache-size FIGURES still reach the page, but they come
 * from the one settings row, not from counting entries.
 *
 * This is also a privacy line worth keeping: the settings row says how full the
 * cache is, while the cache itself is a log of everywhere the network has been.
 */

const { clampPoll, createPollLoop } = require('./util');

const SETTINGS_CMD = ['/ip/dns/print', ''];
const STATIC_CMD   = ['/ip/dns/static/print',
                      '=.proplist=.id,name,address,type,ttl,disabled,comment,regexp,cname,forward-to'];

// Static entries are configuration: they change when somebody edits the router,
// not every tick. The settings row is read every tick because cache-used is live.
const CONFIG_EVERY = 12;

const _bool = (v) => v === true || v === 'true' || v === 'yes';
const _num  = (v) => { const n = Number(v); return Number.isFinite(n) ? n : null; };

function _split(v) {
  if (!v) return [];
  return String(v).split(',').map(s => s.trim()).filter(Boolean);
}

/** Normalise the settings row into the shape the page renders. */
function parseDnsSettings(row) {
  const r = row || {};
  const dohUrl = r['use-doh-server'] || '';
  return {
    servers:        _split(r.servers),
    dynamicServers: _split(r['dynamic-servers']),
    // A router with no DoH must render the panel as "off", not as blank fields,
    // so the flag is explicit rather than inferred from an empty string at the
    // other end of a socket.
    dohEnabled:     !!dohUrl,
    dohUrl,
    dohVerifyCert:  _bool(r['verify-doh-cert']),
    dohMaxServerConnections: _num(r['doh-max-server-connections']),
    dohMaxConcurrentQueries: _num(r['doh-max-concurrent-queries']),
    dohTimeout:     r['doh-timeout'] || '',
    allowRemoteRequests: _bool(r['allow-remote-requests']),
    cacheSize:      _num(r['cache-size']),
    cacheUsed:      _num(r['cache-used']),
    cacheMaxTtl:    r['cache-max-ttl'] || '',
    maxUdpPacketSize:     _num(r['max-udp-packet-size']),
    maxConcurrentQueries: _num(r['max-concurrent-queries']),
    queryServerTimeout:   r['query-server-timeout'] || '',
    queryTotalTimeout:    r['query-total-timeout'] || '',
    mdnsRepeatIfaces:     _split(r['mdns-repeat-ifaces']),
    vrf:            r.vrf || '',
  };
}

/** Static entries. A regexp entry has no name, so it is keyed on its pattern. */
function parseStaticEntries(rows) {
  const out = [];
  for (const r of rows || []) {
    if (!r || (!r.name && !r.regexp)) continue;    // also drops {undefined:''}
    out.push({
      // The row id, so the page can open an entry in the edit form. It
      // addresses a row, it does not authorise one — every write re-reads and
      // re-checks before touching it.
      id:       r['.id'] || '',
      name:     r.name || '',
      regexp:   r.regexp || '',
      address:  r.address || r.cname || r['forward-to'] || '',
      type:     r.type || (r.cname ? 'CNAME' : 'A'),
      ttl:      r.ttl || '',
      disabled: _bool(r.disabled),
      comment:  r.comment || '',
    });
  }
  out.sort((a, b) => (a.name || a.regexp).localeCompare(b.name || b.regexp));
  return out;
}

class DnsCollector {
  constructor({ ros, io, state, pollMs }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 10000, 60000, 2000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][dns]` : '[dns]';

    this._poll     = createPollLoop(() => this._tick(), () => this.pollMs);
    this._settings = parseDnsSettings(null);
    this._static   = [];
    this._ticks    = 0;
    this._lastFp   = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._settingsAvailable = undefined;
    this._staticAvailable   = undefined;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const args = cmd[1] ? [cmd[1]] : [];
      const rows = await this.ros.write(cmd[0], args);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      else if (msg.includes('not enough permissions') || msg.includes('permission denied')) this[flag] = false;
      else this.state.lastDnsErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /**
   * Re-read now, after a write, so the page shows what the router did.
   *
   * The tick counter is reset rather than the read being called directly:
   * static entries are only re-read every CONFIG_EVERY ticks, and a save that
   * left the table showing the old row until the next config sweep — up to ten
   * minutes on the default interval — would read as a failed save.
   */
  async refreshNow() {
    if (!this.ros.connected) return;
    this._ticks = 0;
    await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;

    if (this._ticks % CONFIG_EVERY === 0) {
      this._static = parseStaticEntries(await this._read(STATIC_CMD, '_staticAvailable'));
    }
    this._ticks++;

    // The settings row carries cache-used, which is live, so it is read every
    // tick rather than on the config cadence — it is what drives the gauge.
    const settingsRows = await this._read(SETTINGS_CMD, '_settingsAvailable');
    this._settings = parseDnsSettings(settingsRows[0]);

    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      settings:      this._settings,
      staticEntries: this._static,
      available:     this._settingsAvailable !== false,
    };
    this.lastPayload = payload;
    this.state.lastDnsTs = payload.ts;

    const fp = JSON.stringify({
      s: this._settings,
      t: this._static.map(e => [e.name, e.regexp, e.address, e.type, e.disabled]),
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-dns').emit('dns:update', payload);
  }

  async start() {
    if (this.ros.connected) await this._tick();
    this._poll.start();
    this.ros.on('close', () => this._poll.stop());
    this.ros.on('connected', async () => {
      this._poll.stop();
      this._lastFp = '';
      this._ticks  = 0;
      this._settingsAvailable = this._staticAvailable = undefined;
      await this._tick();
      this._poll.start();
    });
  }

  suspend() { this._poll.stop(); }
  resume()  { if (this.ros.connected) this._poll.start(); }

  stop() { this._poll.stop(); this._lastFp = ''; }
}

DnsCollector.parseDnsSettings   = parseDnsSettings;
DnsCollector.parseStaticEntries = parseStaticEntries;
module.exports = DnsCollector;
