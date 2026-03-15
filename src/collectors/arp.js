/**
 * ARP collector — initial /print on connect, then /ip/arp/listen for changes.
 *
 * The ARP table is a stable, low-churn dataset: entries are created when a
 * device first communicates, and deleted when they expire (default 30 min on
 * RouterOS). On a typical home/office network the stream fires only a handful
 * of times per hour, vs the previous 2 API calls/min from 30s polling.
 *
 * Other collectors (connections, bandwidth, wireless) call getByIP/getByMAC
 * synchronously — the Maps are updated in-place so callers always see the
 * latest data without any coordination overhead.
 */
class ArpCollector {
  constructor({ ros, pollMs, state }) {
    this.ros    = ros;
    this.pollMs = pollMs; // retained for Settings UI; no longer drives polling
    this.state  = state;
    this.byIP   = new Map();
    this.byMAC  = new Map();

    this._stream       = null;
    this._restarting   = false;
    this._restartTimer = null;
  }

  // ── public lookup API (unchanged — callers are unaffected) ────────────────

  getByIP(ip)   { return this.byIP.get(ip) || null; }
  getByMAC(mac) { return this.byMAC.get(mac) || null; }

  // ── helpers ───────────────────────────────────────────────────────────────

  _applyEntry(a, dead = false) {
    const ip  = a.address        || a['active-address'] || '';
    const mac = a['mac-address'] || '';
    if (!ip && !mac) return;

    if (dead) {
      // Remove by IP (primary key in RouterOS ARP)
      if (ip) {
        const existing = this.byIP.get(ip);
        if (existing) {
          this.byMAC.delete(existing.mac);
          this.byIP.delete(ip);
        }
      }
      return;
    }

    if (ip && mac) {
      const entry = { mac, iface: a.interface || '' };
      this.byIP.set(ip, entry);
      this.byMAC.set(mac, { ip, ...entry });
    }
  }

  // ── initial load ──────────────────────────────────────────────────────────

  async _loadInitial() {
    try {
      const items = await this.ros.write('/ip/arp/print',
        ['=.proplist=address,mac-address,interface']);
      this.byIP.clear();
      this.byMAC.clear();
      for (const a of (items || [])) this._applyEntry(a);
      this.state.lastArpTs = Date.now();
      console.log(`[arp] loaded ${this.byIP.size} entries`);
    } catch (e) {
      console.error('[arp] initial load failed:', e && e.message ? e.message : e);
    }
  }

  // ── stream management ─────────────────────────────────────────────────────

  _startStream() {
    if (this._stream) return;
    if (!this.ros.connected) return;
    try {
      this._stream = this.ros.stream(
        ['/ip/arp/listen', '=.proplist=address,mac-address,interface'],
        (err, data) => {
          if (err) {
            console.error('[arp] stream error:', err && err.message ? err.message : err);
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
          const dead = data['.dead'] === 'true' || data['.dead'] === true;
          this._applyEntry(data, dead);
          this.state.lastArpTs = Date.now();
        }
      );
      console.log('[arp] streaming /ip/arp/listen');
    } catch (e) {
      console.error('[arp] stream start failed:', e && e.message ? e.message : e);
    }
  }

  _stopStream() {
    if (this._restartTimer) { clearTimeout(this._restartTimer); this._restartTimer = null; }
    this._restarting = false;
    if (this._stream) { try { this._stream.stop(); } catch (_) {} this._stream = null; }
  }

  // ── lifecycle ─────────────────────────────────────────────────────────────

  async start() {
    await this._loadInitial();
    this._startStream();

    this.ros.on('close', () => this._stopStream());
    this.ros.on('connected', async () => {
      this._stopStream();
      await this._loadInitial();
      this._startStream();
    });
  }
}

module.exports = ArpCollector;
