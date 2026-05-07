/**
 * Traffic collector — streams /interface/monitor-traffic with interval=1.
 *
 * Uses ros.stream() with a null callback and subscribes to the RStream 'data'
 * event directly, bypassing the section-handling debounce in RStream.onStream()
 * (RouterOS interval responses include a .section field that would otherwise
 * delay or lose packets). One persistent channel per interface replaces the
 * previous write()+once= approach.
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
    this.streams       = new Map();  // ifName -> RStreamHandle
    this.availableIfs  = new Set();
    this._loggedErrs   = new Set();
  }

  _ensureHistory(ifName) {
    if (!this.hist.has(ifName)) this.hist.set(ifName, new RingBuffer(this.maxPoints));
  }

  setAvailableInterfaces(interfaces) {
    const names = (interfaces || []).map(i => typeof i === 'string' ? i : i && i.name).filter(Boolean);
    this.availableIfs = new Set(names);
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

  _stopStream(ifName) {
    const stream = this.streams.get(ifName);
    if (!stream) return;
    try { stream.stop().catch(() => {}); } catch (e) {}
    this.streams.delete(ifName);
    console.log('[traffic] stopped stream', ifName);
  }

  _pruneUnusedStreams() {
    const active = new Set(this.subscriptions.values());
    active.add(this.defaultIf);
    for (const ifName of this.streams.keys()) {
      if (!active.has(ifName)) this._stopStream(ifName);
    }
  }

  bindSocket(socket) {
    this.subscriptions.set(socket.id, this.defaultIf);

    socket.on('traffic:select', (payload) => {
      const nextIf = this._normalizeIfName(payload && payload.ifName);
      if (!nextIf) return;
      this.subscriptions.set(socket.id, nextIf);
      this._ensureHistory(nextIf);
      this._startStream(nextIf);
      this._pruneUnusedStreams();
      socket.emit('traffic:history', {
        ifName: nextIf,
        points: this.hist.get(nextIf).toArray(),
      });
    });

    socket.on('disconnect', () => {
      this.subscriptions.delete(socket.id);
      this._pruneUnusedStreams();
    });
  }

  _startStream(ifName) {
    if (this.streams.has(ifName)) return;
    if (!this.ros.connected) return;

    console.log('[traffic] streaming', ifName, 'interval=1s');

    const stream = this.ros.stream(
      '/interface/monitor-traffic',
      [
        `=interface=${ifName}`,
        '=interval=1',
        '=.proplist=rx-bits-per-second,tx-bits-per-second,running,disabled',
      ],
      null  // null callback — use 'data' event to bypass section-handling debounce
    );

    stream.on('data', (packet) => {
      if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
      if (!packet['rx-bits-per-second'] && !packet['tx-bits-per-second']) return;
      this._processPacket(ifName, packet);
    });

    stream.on('error', (err) => {
      const msg = err && err.message ? err.message : String(err);
      if (!this._loggedErrs.has(ifName)) {
        console.error('[traffic] stream error on', ifName, ':', msg);
        this._loggedErrs.add(ifName);
      }
      this.state.lastTrafficErr = msg;
      this.streams.delete(ifName);
    });

    this.streams.set(ifName, stream);
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
    this._loggedErrs.delete(ifName);
  }

  _stopAll() {
    for (const ifName of [...this.streams.keys()]) this._stopStream(ifName);
    this._loggedErrs.clear();
  }

  start() {
    this._ensureHistory(this.defaultIf);
    this._startStream(this.defaultIf);

    this.ros.on('connected', () => {
      console.log('[traffic] reconnected — restarting streams');
      this._stopAll();
      this._ensureHistory(this.defaultIf);
      this._startStream(this.defaultIf);
      const subscribed = new Set(this.subscriptions.values());
      for (const ifName of subscribed) {
        if (ifName !== this.defaultIf) this._startStream(ifName);
      }
    });

    this.ros.on('close', () => this._stopAll());
  }

  stop() { this._stopAll(); }
}

module.exports = TrafficCollector;
