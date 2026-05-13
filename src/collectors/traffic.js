/**
 * Traffic collector — streams /interface/monitor-traffic =interface=<comma-list> =interval=1.
 *
 * A single persistent stream covers all known interfaces simultaneously.
 * The interface list is sourced from setAvailableInterfaces(), which is called
 * after fetchInterfaces() completes in sendInitialState().  Until that list
 * arrives, the stream runs for the default interface only.
 *
 * Each data packet carries a 'name' field (RouterOS includes it when more than
 * one interface is listed); the handler fans it out to the matching socket(s).
 * History is still maintained per-interface in memory.
 *
 * This replaces the previous pattern of opening one stream per subscribed
 * interface, which could create N simultaneous RouterOS streams when multiple
 * browser clients were watching different interfaces.
 */
const RingBuffer = require('../util/ringbuffer');

const MAX_INTERFACE_NAME_LENGTH = 128;

function parseBps(val) {
  if (!val || val === '0') return 0;
  var s = String(val);
  if (s.endsWith('kbps') || s.endsWith('Kbps')) return parseFloat(s) * 1000;
  if (s.endsWith('Mbps') || s.endsWith('mbps')) return parseFloat(s) * 1_000_000;
  if (s.endsWith('Gbps') || s.endsWith('gbps')) return parseFloat(s) * 1_000_000_000;
  if (s.endsWith('bps')) return parseFloat(s);
  return parseInt(s, 10) || 0;
}

function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(3);
}

class TrafficCollector {
  constructor({ ros, io, defaultIf, historyMinutes, state }) {
    this.ros        = ros;
    this.io         = io;
    this.defaultIf  = defaultIf;
    this.state      = state;
    this.maxPoints  = Math.max(60, historyMinutes * 60);
    this.hist          = new Map();  // ifName -> RingBuffer
    this.subscriptions = new Map();  // socketId -> ifName
    this._allStream    = null;       // single shared stream for all interfaces
    this._ifNames      = [];         // ordered list of interface names for the stream
    this._ifNamesKey   = '';         // sorted key — detect list changes without restart
    this.availableIfs  = new Set();
    this._loggedErr    = false;
  }

  _ensureHistory(ifName) {
    if (!this.hist.has(ifName)) this.hist.set(ifName, new RingBuffer(this.maxPoints));
  }

  setAvailableInterfaces(interfaces) {
    const names = (interfaces || []).map(i => typeof i === 'string' ? i : i && i.name).filter(Boolean);
    this.availableIfs = new Set(names);

    const key = names.slice().sort().join(',');
    if (key === this._ifNamesKey) return; // same list — no restart needed
    this._ifNames    = names;
    this._ifNamesKey = key;
    // Restart stream with the expanded interface list
    this._stopAllStream();
    this._startAllStream();
  }

  _normalizeIfName(ifName) {
    if (typeof ifName !== 'string') return null;
    const trimmed = ifName.trim();
    if (!trimmed || trimmed.length > MAX_INTERFACE_NAME_LENGTH) return null;
    if (/[\r\n\0]/.test(trimmed)) return null;
    if (!this.availableIfs.size) {
      console.warn('[traffic] traffic:select rejected — interface list not yet ready');
      return null;
    }
    if (!this.availableIfs.has(trimmed)) return null;
    return trimmed;
  }

  _stopAllStream() {
    if (!this._allStream) return;
    try { this._allStream.stop().catch(() => {}); } catch (e) {}
    this._allStream = null;
    console.log('[traffic] stopped stream');
  }

  _startAllStream() {
    if (this._allStream) return;
    if (!this.ros.connected) return;

    // Use the full interface list if available, otherwise fall back to defaultIf only.
    const names = this._ifNames.length ? this._ifNames : [this.defaultIf];
    console.log('[traffic] streaming', names.length, 'interface(s) interval=1s');

    const stream = this.ros.stream(
      '/interface/monitor-traffic',
      [
        `=interface=${names.join(',')}`,
        '=interval=1',
        '=.proplist=name,rx-bits-per-second,tx-bits-per-second,running,disabled',
      ],
      null  // null callback — use 'data' event to bypass section-handling debounce
    );

    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
      // When a single interface is monitored, RouterOS may omit the 'name' field.
      const ifName = packet.name || (names.length === 1 ? names[0] : null);
      if (!ifName) return;
      if (!packet['rx-bits-per-second'] && !packet['tx-bits-per-second']) return;
      this._processPacket(ifName, packet);
    });

    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      if (!this._loggedErr) {
        console.error('[traffic] stream error:', msg);
        this._loggedErr = true;
      }
      this.state.lastTrafficErr = msg;
      this._allStream = null;
    });

    this._allStream = stream;
  }

  bindSocket(socket) {
    this.subscriptions.set(socket.id, this.defaultIf);

    socket.on('traffic:select', (payload) => {
      const nextIf = this._normalizeIfName(payload && payload.ifName);
      if (!nextIf) return;
      this.subscriptions.set(socket.id, nextIf);
      this._ensureHistory(nextIf);
      // No new stream needed — the single stream already covers all interfaces.
      socket.emit('traffic:history', {
        ifName: nextIf,
        points: this.hist.get(nextIf).toArray(),
      });
    });

    socket.on('disconnect', () => {
      this.subscriptions.delete(socket.id);
    });
  }

  _processPacket(ifName, data) {
    if (this.io.engine.clientsCount === 0) return;

    const rxBps    = parseBps(data['rx-bits-per-second']);
    const txBps    = parseBps(data['tx-bits-per-second']);
    const running  = data.running  !== 'false' && data.running  !== false;
    const disabled = data.disabled === 'true'  || data.disabled === true;

    const now    = Date.now();
    const sample = { ifName, ts: now, rx_mbps: bpsToMbps(rxBps), tx_mbps: bpsToMbps(txBps), running, disabled };

    this._ensureHistory(ifName);
    this.hist.get(ifName).push({ ts: now, rx_mbps: sample.rx_mbps, tx_mbps: sample.tx_mbps });

    for (const [sid, subIf] of this.subscriptions.entries()) {
      if (subIf === ifName) this.io.to(sid).emit('traffic:update', sample);
    }

    if (ifName === this.defaultIf) {
      this.io.emit('wan:status', { ifName, ts: now, running, disabled });
    }

    this.state.lastTrafficTs  = now;
    this.state.lastTrafficErr = null;
    this._loggedErr = false;
  }

  start() {
    this._ensureHistory(this.defaultIf);
    this._startAllStream();

    this.ros.on('connected', () => {
      console.log('[traffic] reconnected — restarting stream');
      this._stopAllStream();
      this._ifNamesKey = ''; // force restart with current _ifNames on reconnect
      this._ensureHistory(this.defaultIf);
      this._startAllStream();
    });

    this.ros.on('close', () => this._stopAllStream());
  }

  stop() { this._stopAllStream(); }
}

module.exports = TrafficCollector;
