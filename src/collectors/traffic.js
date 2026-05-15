/**
 * Traffic collector — streams /interface/monitor-traffic =interface=<list> =interval=1.
 *
 * The stream covers only the union of interfaces currently being watched by
 * connected clients, plus defaultIf (always included for the WAN status badge).
 * When subscriptions change (traffic:select, disconnect) the stream is restarted
 * with the updated list.  This keeps RouterOS API load proportional to what is
 * actually being watched rather than the total interface count.
 *
 * setAvailableInterfaces() populates availableIfs for input validation only —
 * it no longer drives the stream.
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
    this._allStream    = null;
    this._ifNamesKey   = '';         // sorted key of current stream — detects changes
    this.availableIfs  = new Set();  // validated set from fetchInterfaces()
    this._loggedErr    = false;
    this._restartTimer = null;
  }

  _ensureHistory(ifName) {
    if (!this.hist.has(ifName)) this.hist.set(ifName, new RingBuffer(this.maxPoints));
  }

  setAvailableInterfaces(interfaces) {
    const names = (interfaces || []).map(i => typeof i === 'string' ? i : i && i.name).filter(Boolean);
    this.availableIfs = new Set(names);
    // Stream is driven by active subscriptions, not the available-interface list.
  }

  // Returns the sorted union of subscribed interfaces + defaultIf.
  _getStreamNames() {
    const s = new Set([this.defaultIf]);
    for (const ifName of this.subscriptions.values()) s.add(ifName);
    return [...s].sort();
  }

  // Restart the stream only when the subscription set has changed.
  _updateStream() {
    const key = this._getStreamNames().join(',');
    if (key === this._ifNamesKey) return;
    this._ifNamesKey = key;
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
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    if (!this._allStream) return;
    try { this._allStream.stop().catch(() => {}); } catch (e) {}
    this._allStream = null;
    console.log('[traffic] stopped stream');
  }

  _startAllStream() {
    if (this._allStream) return;
    if (!this.ros.connected) return;

    const names = this._getStreamNames();
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
      this._allStream = null;
      const isMissing = msg.includes('no such item');
      if (!isMissing) {
        if (!this._loggedErr) {
          console.error('[traffic] stream error:', msg);
          this._loggedErr = true;
        }
        this.state.lastTrafficErr = msg;
      }
      // Always schedule a restart — 'no such item' is a transient interface blip,
      // other errors may be CHR/VM killing the stream under resource pressure.
      this._restartTimer = setTimeout(() => {
        this._restartTimer = null;
        this._loggedErr = false;
        if (this.ros.connected && !this._allStream) this._startAllStream();
      }, isMissing ? 5000 : 3000);
    });

    this._allStream = stream;
  }

  bindSocket(socket) {
    this.subscriptions.set(socket.id, this.defaultIf);
    // defaultIf is always in the stream, so this is a no-op on first connect.
    this._updateStream();

    socket.on('traffic:select', (payload) => {
      const nextIf = this._normalizeIfName(payload && payload.ifName);
      if (!nextIf) return;
      this.subscriptions.set(socket.id, nextIf);
      this._ensureHistory(nextIf);
      this._updateStream(); // expands stream to include nextIf if not already there
      socket.emit('traffic:history', {
        ifName: nextIf,
        points: this.hist.get(nextIf).toArray(),
      });
    });

    socket.on('disconnect', () => {
      this.subscriptions.delete(socket.id);
      this._updateStream(); // shrinks stream if nextIf is no longer subscribed
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
      this._ifNamesKey = ''; // force restart on reconnect
      this._stopAllStream();
      this._ensureHistory(this.defaultIf);
      this._updateStream(); // restart with current subscription set
    });

    this.ros.on('close', () => this._stopAllStream());
  }

  stop() { this._stopAllStream(); }
}

module.exports = TrafficCollector;
