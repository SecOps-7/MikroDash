class SystemCollector {
  constructor({ ros, io, pollMs, state }) {
    this.ros = ros;
    this.io = io;
    this.pollMs = pollMs || 5000;
    this.state = state;
    this.timer = null;
    this._loggedUpdateFields = false; // one-time field dump for diagnosis
  }

  async tick() {
    if (!this.ros.connected) return;

    let r = {}, h = [], u = {};
    try {
      const [resResult, healthResult, updateResult] = await Promise.allSettled([
        this.ros.write('/system/resource/print'),
        this.ros.write('/system/health/print'),
        this.ros.write('/system/package/update/print'),
      ]);
      r = resResult.status    === 'fulfilled' && resResult.value    && resResult.value[0]    ? resResult.value[0]    : {};
      h = healthResult.status === 'fulfilled' && Array.isArray(healthResult.value)           ? healthResult.value    : [];
      u = updateResult.status === 'fulfilled' && updateResult.value && updateResult.value[0] ? updateResult.value[0] : {};

      // One-time log so we can see exactly what fields RouterOS returns
      if (!this._loggedUpdateFields && Object.keys(u).length) {
        console.log('[system] package/update fields:', JSON.stringify(u));
        this._loggedUpdateFields = true;
      }
    } catch (e) {
      this.state.lastSystemErr = String(e && e.message ? e.message : e);
      console.error('[system]', this.state.lastSystemErr);
      return;
    }

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

    this.io.emit('system:update', {
      ts: Date.now(), uptimeRaw: r.uptime || '', cpuLoad, memPct, usedMem, totalMem,
      hddPct, totalHdd, freeHdd, version: installed,
      latestVersion, updateAvailable: !!updateAvailable, updateStatus,
      boardName: r['board-name'] || r['platform'] || '',
      cpuCount: parseInt(r['cpu-count'] || '1', 10),
      cpuFreq:  parseInt(r['cpu-frequency'] || '0', 10),
      tempC,
    });
    this.state.lastSystemTs = Date.now();
    this.state.lastSystemErr = null;
  }

  start() {
    const run = async () => {
      try { await this.tick(); } catch (e) {
        this.state.lastSystemErr = String(e && e.message ? e.message : e);
        console.error('[system]', this.state.lastSystemErr);
      }
    };
    run();
    this.timer = setInterval(run, this.pollMs);
    this.ros.on('close',     () => { if (this.timer) { clearInterval(this.timer); this.timer = null; } });
    this.ros.on('connected', () => { this.timer = this.timer || setInterval(run, this.pollMs); run(); });
  }
}

module.exports = SystemCollector;
