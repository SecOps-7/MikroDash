class SystemCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs || 5000;
    this.state = state;
    this.timer = null;
    this._inflight = false;
    this._loggedUpdateFields = false;
    this.UPDATE_INTERVAL   = 5 * 60 * 1000; // fetch update status every 5 min
    this._lastUpdateFetch  = 0;             // force fetch on first tick
    this._lastUpdateRow    = {};
    this._lastFp           = '';
  }

  // Fetch update status independently so a slow RouterOS update-server
  // response never delays the resource/health tick (and thus the gauges).
  async _fetchUpdateStatus() {
    if (!this.ros.connected) return;
    const now = Date.now();
    if ((now - this._lastUpdateFetch) < this.UPDATE_INTERVAL) return;
    this._lastUpdateFetch = now; // mark immediately to prevent concurrent fetches
    try {
      const result = await this.ros.write('/system/package/update/print');
      const u = result && result[0] ? result[0] : {};
      this._lastUpdateRow = u;
      if (!this._loggedUpdateFields && Object.keys(u).length) {
        console.log('[system] package/update fields:', JSON.stringify(u));
        this._loggedUpdateFields = true;
      }
      // Re-emit with updated version info if we have a current payload
      if (this.lastPayload) {
        const latestVersion   = u['latest-version'] || '';
        const updateStatus    = u['status'] || '';
        const installedBase   = (this.lastPayload.version || '').replace(/\s*\(.*\)/, '').trim();
        const updateAvailable = latestVersion
          ? latestVersion !== installedBase
          : updateStatus.toLowerCase().includes('new version');
        const updated = { ...this.lastPayload, ts: Date.now(), latestVersion, updateAvailable: !!updateAvailable, updateStatus };
        this.lastPayload = updated;
        this._lastFp = ''; // force emit so update row refreshes
        this.io.emit('system:update', updated);
      }
    } catch (e) {
      console.error('[system] update check failed:', e && e.message ? e.message : e);
    }
  }

  async tick() {
    if (!this.ros.connected) return;

    // Kick off update check in background — intentionally not awaited so it
    // never blocks the resource/health response reaching the browser.
    this._fetchUpdateStatus().catch(() => {});

    let r = {}, h = [];
    try {
      const [resResult, healthResult] = await Promise.allSettled([
        this.ros.write('/system/resource/print', [
          '=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime',
        ]),
        this.ros.write('/system/health/print'),
      ]);
      r = resResult.status    === 'fulfilled' && resResult.value    && resResult.value[0] ? resResult.value[0] : {};
      h = healthResult.status === 'fulfilled' && Array.isArray(healthResult.value)        ? healthResult.value : [];
    } catch (e) {
      this.state.lastSystemErr = String(e && e.message ? e.message : e);
      console.error('[system]', this.state.lastSystemErr);
      return;
    }
    const u = this._lastUpdateRow;

    const cpuLoad  = parseInt(r['cpu-load']       || '0', 10);
    const totalMem = parseInt(r['total-memory']    || '0', 10);
    const freeMem  = parseInt(r['free-memory']     || '0', 10);
    const usedMem  = totalMem - freeMem;
    const memPct   = totalMem > 0 ? Math.round((usedMem / totalMem) * 100) : 0;
    const totalHdd = parseInt(r['total-hdd-space'] || '0', 10);
    const freeHdd  = parseInt(r['free-hdd-space']  || '0', 10);
    const hddPct   = totalHdd > 0 ? Math.round(((totalHdd - freeHdd) / totalHdd) * 100) : 0;

    let tempC = null;
    for (const item of h) {
      if ((item.name || '').toLowerCase().includes('temperature')) {
        const v = parseFloat(item.value || '');
        if (!isNaN(v)) { tempC = v; break; }
      }
    }

    // /system/resource/print returns version as "7.21.3 (stable)" — strip the channel suffix
    // /system/package/update/print returns clean "7.21.3" — compare the base version only
    const installed       = r.version || '';
    const installedBase   = installed.replace(/\s*\(.*\)/, '').trim();
    const latestVersion   = u['latest-version'] || '';
    const updateStatus    = u['status'] || '';
    // Prefer the router's own status string: "System is already up to date" vs "New version is available"
    const updateAvailable = latestVersion
      ? (latestVersion !== installedBase)
      : updateStatus.toLowerCase().includes('new version');

    const payload = {
      ts: Date.now(), uptimeRaw: r.uptime || '', cpuLoad, memPct, usedMem, totalMem,
      hddPct, totalHdd, freeHdd, version: installed,
      latestVersion, updateAvailable: !!updateAvailable, updateStatus,
      boardName: r['board-name'] || r['platform'] || '',
      cpuCount: parseInt(r['cpu-count'] || '1', 10),
      cpuFreq:  parseInt(r['cpu-frequency'] || '0', 10),
      tempC, pollMs: this.pollMs,
    };
    this.lastPayload = payload;
    // Fingerprint dynamic fields only — suppress emit when gauges are unchanged
    const fp = `${cpuLoad},${memPct},${hddPct},${tempC},${r.uptime||''},${updateAvailable},${latestVersion}`;
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('system:update', payload);
    }
    this.state.lastSystemTs = Date.now();
    this.state.lastSystemErr = null;
  }

  start() {
    const run = async () => {
      if (this._inflight) return;
      this._inflight = true;
      try { await this.tick(); } catch (e) {
        this.state.lastSystemErr = String(e && e.message ? e.message : e);
        console.error('[system]', this.state.lastSystemErr);
      } finally { this._inflight = false; }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close',     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this._lastFp = ''; this._lastUpdateFetch = 0; this._lastUpdateRow = {}; this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = SystemCollector;
