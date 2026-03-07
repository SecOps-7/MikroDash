/**
 * Traffic collector — polls /interface/monitor-traffic =once= every 1 second.
 *
 * WHY polling instead of streaming:
 *   /interface/monitor-traffic is an interactive RouterOS command. When called
 *   via the binary API without =once=, it may stream, but the behavior varies
 *   by ROS version and is unreliable. Every known working implementation uses
 *   write() + =once= on a 1-second interval. This is the correct approach.
 */
const RingBuffer = require('../util/ringbuffer');

const POLL_MS = 1000; // 1 second
// RouterOS interface names are short in practice; cap this to reject malformed input early.
const MAX_INTERFACE_NAME_LENGTH = 128;

function parseBps(val) {
  // RouterOS API returns raw integer strings via binary API (e.g. "27800")
  // but format strings in terminal output ("27.8kbps") — just in case, handle both.
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
    this.ros       = ros;
    this.io        = io;
    this.defaultIf = defaultIf;
    this.state     = state;
    this.maxPoints = Math.max(60, historyMinutes * 60);
    this.hist          = new Map();   // ifName -> RingBuffer
    this.subscriptions = new Map();   // socketId -> ifName
    this.timers        = new Map();   // ifName -> intervalId
    this.availableIfs  = new Set();
    this._loggedErrs   = new Set();   // ifNames that have logged an error
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
    if (this.availableIfs.size && !this.availableIfs.has(trimmed)) return null;
    return trimmed;
  }

  _stopPoll(ifName) {
    const timer = this.timers.get(ifName);
    if (!timer) return;
    clearInterval(timer);
    this.timers.delete(ifName);
    console.log('[traffic] stopped polling', ifName);
  }

  _pruneUnusedPolls() {
    const active = new Set(this.subscriptions.values());
    active.add(this.defaultIf);
    for (const ifName of this.timers.keys()) {
      if (!active.has(ifName)) this._stopPoll(ifName);
    }
  }

  bindSocket(socket) {
    // Subscribe this socket to the default interface immediately
    this.subscriptions.set(socket.id, this.defaultIf);

    // Client changed interface selection
    socket.on('traffic:select', (payload) => {
      const nextIf = this._normalizeIfName(payload && payload.ifName);
      if (!nextIf) return;
      this.subscriptions.set(socket.id, nextIf);
      this._ensureHistory(nextIf);
      this._startPoll(nextIf);
      this._pruneUnusedPolls();
      socket.emit('traffic:history', {
        ifName: nextIf,
        points: this.hist.get(nextIf).toArray(),
      });
    });

    socket.on('disconnect', () => {
      this.subscriptions.delete(socket.id);
      this._pruneUnusedPolls();
    });
  }

  _startPoll(ifName) {
    if (this.timers.has(ifName)) return; // already polling
    if (!this.ros.connected) return;

    console.log('[traffic] polling', ifName, 'every', POLL_MS, 'ms');

    const timer = setInterval(async () => {
      if (!this.ros.connected) return;
      try {
        const rows = await this.ros.write(
          '/interface/monitor-traffic',
          [`=interface=${ifName}`, '=once=']
        );
        if (!rows || !rows.length) return;
        const data = rows[0];

        const rxBps = parseBps(data['rx-bits-per-second']);
        const txBps = parseBps(data['tx-bits-per-second']);
        const running  = data.running  !== 'false' && data.running  !== false;
        const disabled = data.disabled === 'true'  || data.disabled === true;

        const now    = Date.now();
        const sample = {
          ifName, ts: now,
          rx_mbps: bpsToMbps(rxBps),
          tx_mbps: bpsToMbps(txBps),
          running, disabled,
        };

        this._ensureHistory(ifName);
        this.hist.get(ifName).push({ ts: now, rx_mbps: sample.rx_mbps, tx_mbps: sample.tx_mbps });

        // Push to subscribed sockets
        for (const [sid, subIf] of this.subscriptions.entries()) {
          if (subIf === ifName) this.io.to(sid).emit('traffic:update', sample);
        }

        // WAN status for default interface
        if (ifName === this.defaultIf) {
          this.io.emit('wan:status', { ifName, ts: now, running, disabled });
        }

        this.state.lastTrafficTs  = now;
        this.state.lastTrafficErr = null;

      } catch (e) {
        this.state.lastTrafficErr = e && e.message ? e.message : String(e);
        if (!this._loggedErrs.has(ifName)) {
          console.error('[traffic] poll error on', ifName, ':', this.state.lastTrafficErr);
          this._loggedErrs.add(ifName);
        }
      }
    }, POLL_MS);

    this.timers.set(ifName, timer);
  }

  _stopAll() {
    for (const ifName of this.timers.keys()) this._stopPoll(ifName);
    this.timers.clear();
    this._loggedErrs.clear();
  }

  start() {
    this._ensureHistory(this.defaultIf);
    this._startPoll(this.defaultIf);

    this.ros.on('connected', () => {
      console.log('[traffic] reconnected — restarting polls');
      this._stopAll();
      this._ensureHistory(this.defaultIf);
      this._startPoll(this.defaultIf);
      // Re-poll any currently subscribed interfaces
      const subscribed = new Set(this.subscriptions.values());
      for (const ifName of subscribed) {
        if (ifName !== this.defaultIf) this._startPoll(ifName);
      }
    });

    this.ros.on('close', () => this._stopAll());
  }
}

module.exports = TrafficCollector;
