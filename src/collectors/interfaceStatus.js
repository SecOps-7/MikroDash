/**
 * Interface Status collector — initial /print on connect, then /listen.
 *
 * /interface/listen fires on any interface state change (up/down, IP change,
 * counter increment). Counter updates happen frequently on active interfaces
 * which is what drives the live rate display on the Interfaces page.
 *
 * /ip/address/print is still polled (5s) — the address listen stream on some
 * RouterOS builds fires unreliably for IP assignment changes, and the address
 * table is small so polling it is cheap.
 */

function parseCounter(val) {
  const parsed = parseInt(val || '0', 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function parseBps(val) {
  if (!val || val === '0') return 0;
  const parsed = parseInt(String(val), 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function bpsToMbps(bps) {
  return +((bps || 0) / 1_000_000).toFixed(4);
}

class InterfaceStatusCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros     = ros;
    this.io      = io;
    this.pollMs  = pollMs || 5000;
    this.state   = state;

    this._ifaces = new Map(); // name -> raw row from RouterOS
    this._addrs  = new Map(); // interface name -> [cidr, ...]
    this._prev   = new Map(); // name -> { rxBytes, txBytes, ts }

    this._stream       = null;
    this._restarting   = false;
    this._restartTimer = null;
    this._addrTimer    = null;
    this._heartbeat    = null;
  }

  // ── build + emit ──────────────────────────────────────────────────────────

  _buildAndEmit() {
    const now = Date.now();
    const interfaces = [];

    for (const i of this._ifaces.values()) {
      const rxBytes = parseCounter(i['rx-byte']);
      const txBytes = parseCounter(i['tx-byte']);
      const rxBps   = parseBps(i['rx-bits-per-second']);
      const txBps   = parseBps(i['tx-bits-per-second']);

      let rxMbps = bpsToMbps(rxBps);
      let txMbps = bpsToMbps(txBps);
      const prev = this._prev.get(i.name);
      if (rxMbps === 0 && txMbps === 0 && prev && now > prev.ts) {
        const elapsedSec = (now - prev.ts) / 1000;
        const rxDelta = rxBytes - prev.rxBytes;
        const txDelta = txBytes - prev.txBytes;
        if (rxDelta >= 0 && txDelta >= 0) {
          rxMbps = bpsToMbps((rxDelta * 8) / elapsedSec);
          txMbps = bpsToMbps((txDelta * 8) / elapsedSec);
        }
      }
      this._prev.set(i.name, { rxBytes, txBytes, ts: now });

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

    this.lastPayload = { ts: now, interfaces, pollMs: 0 }; // 0 = streamed, not polled
    this.io.emit('ifstatus:update', this.lastPayload);
    this.state.lastIfStatusTs = now;
  }

  // ── initial load ──────────────────────────────────────────────────────────

  async _loadInitial() {
    try {
      const [ifRes, addrRes] = await Promise.allSettled([
        this.ros.write('/interface/print', [
          '=stats=',
          '=.proplist=name,type,running,disabled,comment,mac-address,rx-byte,tx-byte,rx-bits-per-second,tx-bits-per-second',
        ]),
        this.ros.write('/ip/address/print', ['=.proplist=interface,address']),
      ]);

      const ifaces = ifRes.status   === 'fulfilled' ? (ifRes.value   || []) : [];
      const addrs  = addrRes.status === 'fulfilled' ? (addrRes.value || []) : [];

      this._ifaces.clear();
      for (const i of ifaces) { if (i.name) this._ifaces.set(i.name, i); }

      this._addrs.clear();
      for (const a of addrs) {
        const n = a.interface || '';
        if (!this._addrs.has(n)) this._addrs.set(n, []);
        this._addrs.get(n).push(a.address || '');
      }

      this._buildAndEmit();
    } catch (e) {
      console.error('[ifstatus] initial load failed:', e && e.message ? e.message : e);
    }
  }

  // ── stats + address poll ─────────────────────────────────────────────────
  // /interface/listen fires on structural changes (up/down) but does NOT
  // push byte-counter updates — those only come from /interface/print with
  // =stats=. We keep a lightweight poll at pollMs to refresh counters and
  // IPs, which drives the live rate bars on the Interfaces page.
  // The listen stream still gives us instant up/down state changes.

  _startAddrPoll() {
    if (this._addrTimer) return;
    this._addrTimer = setInterval(async () => {
      if (!this.ros.connected) return;
      try {
        const [ifRes, addrRes] = await Promise.allSettled([
          this.ros.write('/interface/print', [
            '=stats=',
            '=.proplist=name,rx-byte,tx-byte,rx-bits-per-second,tx-bits-per-second',
          ]),
          this.ros.write('/ip/address/print', ['=.proplist=interface,address']),
        ]);

        // Merge fresh counter values into stored interface rows
        const ifaces = ifRes.status === 'fulfilled' ? (ifRes.value || []) : [];
        for (const i of ifaces) {
          if (!i.name) continue;
          const existing = this._ifaces.get(i.name);
          if (existing) {
            existing['rx-byte']              = i['rx-byte']              ?? existing['rx-byte'];
            existing['tx-byte']              = i['tx-byte']              ?? existing['tx-byte'];
            existing['rx-bits-per-second']   = i['rx-bits-per-second']   ?? existing['rx-bits-per-second'];
            existing['tx-bits-per-second']   = i['tx-bits-per-second']   ?? existing['tx-bits-per-second'];
          }
        }

        // Refresh address map
        const addrs = addrRes.status === 'fulfilled' ? (addrRes.value || []) : [];
        this._addrs.clear();
        for (const a of addrs) {
          const n = a.interface || '';
          if (!this._addrs.has(n)) this._addrs.set(n, []);
          this._addrs.get(n).push(a.address || '');
        }

        this._buildAndEmit();
      } catch (_) {}
    }, this.pollMs);
  }

  _stopAddrPoll() {
    if (this._addrTimer) { clearInterval(this._addrTimer); this._addrTimer = null; }
  }

  // ── interface listen stream ───────────────────────────────────────────────

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    try {
      this._stream = this.ros.stream([
        '/interface/listen',
        // Request stats fields so counter updates are included in stream events
        '=.proplist=name,type,running,disabled,comment,mac-address,rx-byte,tx-byte,rx-bits-per-second,tx-bits-per-second',
      ], (err, data) => {
        if (err) {
          console.error('[ifstatus] stream error:', err && err.message ? err.message : err);
          this._stopStream();
          if (this.ros.connected && !this._restarting) {
            this._restarting = true;
            this._restartTimer = setTimeout(() => {
              this._restarting   = false;
              this._restartTimer = null;
              if (this.ros.connected) this._loadInitial().then(() => this._startStream());
            }, 3000);
          }
          return;
        }
        if (!data) return;
        if (data['.dead'] === 'true' || data['.dead'] === true) {
          if (data.name) { this._ifaces.delete(data.name); this._prev.delete(data.name); }
        } else if (data.name) {
          // Merge delta into stored row — listen sends only changed fields
          const existing = this._ifaces.get(data.name) || {};
          this._ifaces.set(data.name, { ...existing, ...data });
        }
        this._buildAndEmit();
      });
      console.log('[ifstatus] streaming /interface/listen');
    } catch (e) {
      console.error('[ifstatus] stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  // ── heartbeat ────────────────────────────────────────────────────────────
  // Interface counters change constantly when there's traffic, but may be
  // quiet on an idle router. Heartbeat ensures the stale timer stays reset.

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
    await this._loadInitial();
    this._startStream();
    this._startAddrPoll();
    this._startHeartbeat();

    this.ros.on('close', () => {
      this._stopStream();
      this._stopAddrPoll();
      this._stopHeartbeat();
    });
    this.ros.on('connected', async () => {
      this._stopStream();
      this._stopAddrPoll();
      this._stopHeartbeat();
      this._prev.clear();
      await this._loadInitial();
      this._startStream();
      this._startAddrPoll();
      this._startHeartbeat();
    });
  }
}

module.exports = InterfaceStatusCollector;
