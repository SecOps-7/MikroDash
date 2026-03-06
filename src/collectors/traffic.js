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
    this.ros = ros;
    this.io = io;
    this.defaultIf = defaultIf;
    this.state = state;
    this.maxPoints = Math.max(60, historyMinutes * 60);
    this.hist = new Map();   // ifName -> RingBuffer
    this.subscriptions = new Map();   // socketId -> ifName
    this.timers = new Map();   // ifName -> intervalId
  }

  _ensureHistory(ifName) {
    if (!this.hist.has(ifName)) this.hist.set(ifName, new RingBuffer(this.maxPoints));
  }

  bindSocket(socket) {
    this.attachSocket(socket.id, this.defaultIf);

    // Client changed interface selection
    socket.on('traffic:select', ({ ifName: newIf }) => {
      const history = this.selectInterface(socket.id, newIf);
      if (!history) return;
      socket.emit('traffic:history', {
        ifName: newIf,
        points: history,
      });
    });

    socket.on('disconnect', () => this.detachSocket(socket.id));
  }

  attachSocket(socketId, ifName) {
    const targetIf = ifName || this.defaultIf;
    this.subscriptions.set(socketId, targetIf);
    this._ensureHistory(targetIf);
    this._startPoll(targetIf);
  }

  detachSocket(socketId) {
    this.subscriptions.delete(socketId);
  }

  selectInterface(socketId, ifName) {
    if (!ifName) return null;
    this.subscriptions.set(socketId, ifName);
    this._ensureHistory(ifName);
    this._startPoll(ifName);
    return this.hist.get(ifName).toArray();
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
        const running = data.running !== 'false' && data.running !== false;
        const disabled = data.disabled === 'true' || data.disabled === true;

        const now = Date.now();
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

        this.state.lastTrafficTs = now;
        this.state.lastTrafficErr = null;

      } catch (e) {
        this.state.lastTrafficErr = e && e.message ? e.message : String(e);
        // Don't log every error — only first occurrence
        if (!this._hadTrafficErr) {
          console.error('[traffic] poll error on', ifName, ':', this.state.lastTrafficErr);
          this._hadTrafficErr = true;
        }
      }
    }, POLL_MS);

    this.timers.set(ifName, timer);
  }

  _stopAll() {
    for (const [ifName, timer] of this.timers.entries()) {
      clearInterval(timer);
      console.log('[traffic] stopped polling', ifName);
    }
    this.timers.clear();
    this._hadTrafficErr = false;
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
