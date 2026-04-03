/**
 * Interface Status collector — poll-only.
 *
 * Polls /interface/print (with =stats=) and /ip/address/print every pollMs.
 *
 * The previous hybrid approach (poll + /interface/listen stream) was removed.
 * RouterOS resets its rx/tx-bits-per-second field mid-cycle (~1 s), causing
 * stream events to carry bps=0 unpredictably. Interleaving stream emits with
 * poll emits produced rate-bar flashes that were not fixable without adding
 * more state than the stream was worth. A poll at 1–5 s is fast enough for
 * up/down state detection and gives a clean, predictable delta window for
 * byte-counter rate calculation.
 *
 * Sticky-rate guard: if the computed byte-delta is zero (RouterOS internal
 * counter not yet updated at sub-2 s poll rates), the previous non-zero rate
 * is preserved for up to 3 consecutive zero reads before accepting idle.
 * This stops single-poll timing races from flashing the display to zero.
 */

function parseCounter(val) {
  const parsed = parseInt(val || '0', 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(4);
}

class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros    = ros;
    this.io     = io;
    this.pollMs = pollMs || 5000;
    this.state  = state;

    this._ifaces     = new Map(); // name -> raw row from RouterOS
    this._addrs      = new Map(); // interface name -> [cidr, ...]
    this._prev       = new Map(); // name -> { rxBytes, txBytes, ts }
    this._lastRates  = new Map(); // name -> { rxMbps, txMbps }
    this._zeroStreak = new Map(); // name -> consecutive zero-delta count

    this._timer    = null;
    this._heartbeat = null;
  }

  // ── build + emit ──────────────────────────────────────────────────────────

  _buildAndEmit() {
    const now = Date.now();
    const interfaces = [];

    for (const i of this._ifaces.values()) {
      const rxBytes = parseCounter(i['rx-byte']);
      const txBytes = parseCounter(i['tx-byte']);

      const prev = this._prev.get(i.name);
      let rxMbps = 0, txMbps = 0;

      if (prev && now > prev.ts) {
        const elapsedSec = (now - prev.ts) / 1000;
        if (elapsedSec >= 0.5) {
          const rxDelta = rxBytes - prev.rxBytes;
          const txDelta = txBytes - prev.txBytes;
          if (rxDelta >= 0 && txDelta >= 0) {
            rxMbps = bpsToMbps((rxDelta * 8) / elapsedSec);
            txMbps = bpsToMbps((txDelta * 8) / elapsedSec);
          }
        }
      }
      this._prev.set(i.name, { rxBytes, txBytes, ts: now });

      // Sticky-rate guard: a zero delta can mean RouterOS hasn't updated its
      // internal byte counter yet (common at ≤1 s poll rates; RouterOS ticks
      // internally at ~1 s). Hold the last known non-zero rate for up to 3
      // consecutive zero reads before accepting that the interface is idle.
      const cached = this._lastRates.get(i.name) || { rxMbps: 0, txMbps: 0 };
      const streak  = this._zeroStreak.get(i.name) || 0;
      if (rxMbps > 0 || txMbps > 0) {
        this._lastRates.set(i.name, { rxMbps, txMbps });
        this._zeroStreak.set(i.name, 0);
      } else if (streak < 3) {
        rxMbps = cached.rxMbps;
        txMbps = cached.txMbps;
        this._zeroStreak.set(i.name, streak + 1);
      } else {
        // 3+ consecutive zero polls — traffic genuinely stopped
        this._lastRates.set(i.name, { rxMbps: 0, txMbps: 0 });
        this._zeroStreak.set(i.name, 0);
      }

      interfaces.push({
        name:     i.name     || '',
        type:     i.type     || 'ether',
        running:  i.running  === 'true' || i.running  === true,
        disabled: i.disabled === 'true' || i.disabled === true,
        comment:  i.comment  || '',
        macAddr:  i['mac-address'] || '',
        rxBytes, txBytes, rxMbps, txMbps,
        ips: this._addrs.get(i.name) || [],
      });
    }

    this.lastPayload = { ts: now, interfaces };
    this.io.emit('ifstatus:update', this.lastPayload);
    this.state.lastIfStatusTs = now;
  }

  // ── poll ──────────────────────────────────────────────────────────────────

  async _poll() {
    if (!this.ros.connected) return;
    try {
      const [ifRes, addrRes] = await Promise.allSettled([
        this.ros.write('/interface/print', [
          '=stats=',
          '=.proplist=name,type,running,disabled,comment,mac-address,rx-byte,tx-byte',
        ]),
        this.ros.write('/ip/address/print', ['=.proplist=interface,address']),
      ]);

      const ifaces = ifRes.status === 'fulfilled' ? (ifRes.value || []) : [];
      const addrs  = addrRes.status === 'fulfilled' ? (addrRes.value || []) : [];

      // Guard against transient empty results under RouterOS load (seen on ARM)
      if (ifaces.length > 0 || this._ifaces.size === 0) {
        this._ifaces.clear();
        for (const i of ifaces) { if (i.name) this._ifaces.set(i.name, i); }
      }

      if (addrs.length > 0 || this._addrs.size === 0) {
        this._addrs.clear();
        for (const a of addrs) {
          const n = a.interface || '';
          if (!this._addrs.has(n)) this._addrs.set(n, []);
          this._addrs.get(n).push(a.address || '');
        }
      }

      this._buildAndEmit();
    } catch (_) {}
  }

  _startPoll() {
    if (this._timer) return;
    this._timer = setInterval(() => this._poll(), this.pollMs);
  }

  _stopPoll() {
    if (this._timer) { clearInterval(this._timer); this._timer = null; }
  }

  // Aliases kept for src/index.js live-interval restart compatibility
  _startAddrPoll() { this._startPoll(); }
  _stopAddrPoll()  { this._stopPoll(); }

  // ── heartbeat ─────────────────────────────────────────────────────────────

  _startHeartbeat() {
    if (this._heartbeat) return;
    this._heartbeat = setInterval(() => {
      if (this.lastPayload) this.io.emit('ifstatus:update', { ...this.lastPayload, ts: Date.now() });
    }, 60000);
  }

  _stopHeartbeat() {
    if (this._heartbeat) { clearInterval(this._heartbeat); this._heartbeat = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    await this._poll(); // initial load + emit
    this._startPoll();
    this._startHeartbeat();

    this.ros.on('close', () => {
      this._stopPoll();
      this._stopHeartbeat();
    });
    this.ros.on('connected', async () => {
      this._stopPoll();
      this._stopHeartbeat();
      this._prev.clear();
      this._lastRates.clear();
      this._zeroStreak.clear();
      await this._poll();
      this._startPoll();
      this._startHeartbeat();
    });
  }

  stop() {
    this._stopPoll();
    this._stopHeartbeat();
  }
}

module.exports = InterfaceStatusCollector;
