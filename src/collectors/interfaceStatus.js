/**
 * Interface Status collector — all three data sources use persistent streams.
 *
 * Metadata streams (interval = metaPollMs, default 60 s):
 *   /interface/print =.proplist=name,type,running,disabled,comment,mac-address =interval=N
 *   /ip/address/print =.proplist=interface,address =interval=N
 *
 * Rate stream (interval derived from pollMs, default 5 s):
 *   /interface/monitor-traffic =interface=<all> =.proplist=name,rx-bits-per-second,tx-bits-per-second =interval=N
 *
 * All three use ros.stream() with null callback + 'data' event to bypass
 * RStream's section-handling debounce.
 *
 * _emitTimer fires every pollMs — calls _buildAndEmit() so rate bars update
 * smoothly. _commitMeta() fires immediately after each metadata tick (via a
 * 300 ms debounce) so interface up/down changes are reflected without waiting
 * for the next emit tick.
 */

function parseBps(val) {
  if (!val || val === '0') return 0;
  const s = String(val);
  if (s.endsWith('kbps') || s.endsWith('Kbps')) return parseFloat(s) * 1000;
  if (s.endsWith('Mbps') || s.endsWith('mbps')) return parseFloat(s) * 1_000_000;
  if (s.endsWith('Gbps') || s.endsWith('gbps')) return parseFloat(s) * 1_000_000_000;
  if (s.endsWith('bps')) return parseFloat(s);
  return parseInt(s, 10) || 0;
}

function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(4);
}

class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, metaPollMs, state }) {
    this.ros        = ros;
    this.io         = io;
    this.pollMs     = pollMs    || 5000;   // rate stream + emit timer interval
    this.metaPollMs = metaPollMs || 60000; // metadata streams interval
    this.state      = state;

    this._ifaces     = new Map(); // name -> committed interface row
    this._addrs      = new Map(); // interface name -> [cidr, ...]
    this._ifacesNext = new Map(); // accumulator for current metadata tick
    this._addrsNext  = new Map(); // accumulator for current metadata tick

    this._ifStream     = null;
    this._addrStream   = null;
    this._metaDebounce = null;

    this._monitorStream   = null;
    this._streamRates     = new Map(); // name -> { rxMbps, txMbps }
    this._monitorIfaceKey = '';

    this._emitTimer = null;
  }

  // ── metadata streams ──────────────────────────────────────────────────────

  _startMetaStreams() {
    this._startIfStream();
    this._startAddrStream();
  }

  _stopMetaStreams() {
    if (this._ifStream)   { try { this._ifStream.stop().catch(() => {}); }   catch (e) {} this._ifStream   = null; }
    if (this._addrStream) { try { this._addrStream.stop().catch(() => {}); } catch (e) {} this._addrStream = null; }
    clearTimeout(this._metaDebounce);
    this._metaDebounce = null;
    this._ifacesNext   = new Map();
    this._addrsNext    = new Map();
  }

  _restartMetaStreams() {
    this._stopMetaStreams();
    this._startMetaStreams();
  }

  _startIfStream() {
    if (this._ifStream || !this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.metaPollMs / 1000));
    console.log('[ifstatus] streaming /interface/print, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/interface/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=name,type,running,disabled,comment,mac-address',
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || !packet.name || typeof packet.name !== 'string') return;
      this._ifacesNext.set(packet.name, packet);
      this._scheduleMetaCommit();
    });
    stream.on('error', (err) => {
      console.error('[ifstatus] /interface/print stream error:', err && err.message ? err.message : String(err));
      this._ifStream = null;
    });
    this._ifStream = stream;
  }

  _startAddrStream() {
    if (this._addrStream || !this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.metaPollMs / 1000));
    console.log('[ifstatus] streaming /ip/address/print, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/ip/address/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=interface,address',
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || !packet.interface || typeof packet.interface !== 'string') return;
      if (!this._addrsNext.has(packet.interface)) this._addrsNext.set(packet.interface, []);
      this._addrsNext.get(packet.interface).push(packet.address || '');
      this._scheduleMetaCommit();
    });
    stream.on('error', (err) => {
      console.error('[ifstatus] /ip/address/print stream error:', err && err.message ? err.message : String(err));
      this._addrStream = null;
    });
    this._addrStream = stream;
  }

  _scheduleMetaCommit() {
    clearTimeout(this._metaDebounce);
    this._metaDebounce = setTimeout(() => this._commitMeta(), 300);
  }

  _commitMeta() {
    this._metaDebounce = null;
    if (this._ifacesNext.size > 0) {
      this._ifaces     = this._ifacesNext;
      this._ifacesNext = new Map();
    }
    // Always swap addresses (empty set is valid — no IPs assigned)
    this._addrs     = this._addrsNext;
    this._addrsNext = new Map();

    this._startMonitorStream(); // no-op if already running with same iface set
    this._buildAndEmit();
  }

  // ── monitor-traffic stream ────────────────────────────────────────────────

  _startMonitorStream() {
    const names = [...this._ifaces.keys()];
    if (!names.length) return;
    const key = names.slice().sort().join(',');
    if (this._monitorStream && this._monitorIfaceKey === key) return;
    this._stopMonitorStream();
    if (!this.ros.connected) return;

    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));
    console.log('[ifstatus] starting monitor-traffic stream,', names.length, 'interfaces, interval=' + intervalSec + 's');
    const stream = this.ros.stream(
      '/interface/monitor-traffic',
      [
        `=interface=${names.join(',')}`,
        '=.proplist=name,rx-bits-per-second,tx-bits-per-second',
        `=interval=${intervalSec}`,
      ],
      null
    );
    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
      const name = packet.name;
      if (!name || typeof name !== 'string') return;
      this._streamRates.set(name, {
        rxMbps: bpsToMbps(parseBps(packet['rx-bits-per-second'])),
        txMbps: bpsToMbps(parseBps(packet['tx-bits-per-second'])),
      });
    });
    stream.on('error', (err) => {
      console.error('[ifstatus] monitor-traffic stream error:', err && err.message ? err.message : String(err));
      this._monitorStream   = null;
      this._monitorIfaceKey = '';
    });
    this._monitorStream   = stream;
    this._monitorIfaceKey = key;
  }

  _stopMonitorStream() {
    if (!this._monitorStream) return;
    try { this._monitorStream.stop().catch(() => {}); } catch (e) {}
    this._monitorStream   = null;
    this._monitorIfaceKey = '';
    this._streamRates.clear();
  }

  _restartMonitorStream() {
    this._stopMonitorStream();
    this._startMonitorStream();
  }

  // ── emit timer ────────────────────────────────────────────────────────────

  _startEmitTimer() {
    if (this._emitTimer) return;
    this._emitTimer = setInterval(() => this._buildAndEmit(), this.pollMs); // codeql[js/resource-exhaustion]
  }

  _stopEmitTimer() {
    if (this._emitTimer) { clearInterval(this._emitTimer); this._emitTimer = null; }
  }

  _restartEmitTimer() {
    this._stopEmitTimer();
    this._startEmitTimer();
  }

  // Aliases kept for index.js pollIfstatus live-update handler compatibility
  _startAddrPoll() { this._startEmitTimer(); }
  _stopAddrPoll()  { this._stopEmitTimer(); }

  // ── build + emit ──────────────────────────────────────────────────────────

  _buildAndEmit() {
    if (!this._ifaces.size) return;
    if (this.io.engine.clientsCount === 0) return;

    const now = Date.now();
    const interfaces = [];

    for (const i of this._ifaces.values()) {
      const sr = this._streamRates.get(i.name) || { rxMbps: 0, txMbps: 0 };
      interfaces.push({
        name:     i.name     || '',
        type:     i.type     || 'ether',
        running:  i.running  === 'true' || i.running  === true,
        disabled: i.disabled === 'true' || i.disabled === true,
        comment:  i.comment  || '',
        macAddr:  i['mac-address'] || '',
        rxMbps:   sr.rxMbps,
        txMbps:   sr.txMbps,
        ips: this._addrs.get(i.name) || [],
      });
    }

    this.lastPayload = { ts: now, interfaces };
    this.io.emit('ifstatus:update', this.lastPayload);
    this.state.lastIfStatusTs = now;
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  start() {
    this._startMetaStreams();
    this._startEmitTimer();

    this.ros.on('close', () => {
      this._stopMetaStreams();
      this._stopMonitorStream();
      this._stopEmitTimer();
    });
    this.ros.on('connected', () => {
      this._stopMetaStreams();
      this._stopMonitorStream();
      this._stopEmitTimer();
      this._ifaces.clear();
      this._addrs.clear();
      this._streamRates.clear();
      this._startMetaStreams();
      this._startEmitTimer();
    });
  }

  suspend() {
    this._stopMonitorStream();
    this._stopEmitTimer();
  }

  resume() {
    this._startMonitorStream();
    this._startEmitTimer();
  }

  stop() {
    this._stopMetaStreams();
    this._stopMonitorStream();
    this._stopEmitTimer();
  }
}

module.exports = InterfaceStatusCollector;
