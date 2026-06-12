class SystemCollector {
  constructor({ ros, io, pollMs, state, streamMode }) {
    this.ros = ros;
    this.io = io;
    this._lbl = ros.routerLabel ? `[${ros.routerLabel}][system]` : '[system]';
    this.pollMs = pollMs || 2000;
    this.state = state;
    this.streamMode = streamMode !== false; // default true
    this._stream = null;
    this._restarting   = false;
    this._pollTimer    = null;
    this._pollInflight = false;
    this._healthTimer = null;
    this._healthInflight = false;
    this._lastHealth = [];
    this._loggedUpdateFields = false;
    this.UPDATE_INTERVAL   = 12 * 60 * 60 * 1000;
    this._lastUpdateFetch  = 0;
    this._lastUpdateRow    = {};
    this._lastFp           = '';
    this.lastPayload       = null;
    this._boardNameReported = false;
  }

  // Fetch update status independently so a slow RouterOS update-server
  // response never delays the resource/health tick (and thus the gauges).
  async _fetchUpdateStatus() {
    if (!this.ros.connected) return;
    const now = Date.now();
    if ((now - this._lastUpdateFetch) < this.UPDATE_INTERVAL) return;
    this._lastUpdateFetch = now;
    try {
      // Explicitly trigger a check with the update server (blocks until done or times out).
      // Without this, print returns cached/transient "finding out latest version..." state.
      const checkTimeout = new Promise((_, reject) =>
        setTimeout(() => reject(new Error('check-for-updates timed out')), 15000));
      await Promise.race([
        this.ros.write('/system/package/update/check-for-updates'),
        checkTimeout,
      ]).catch(() => {}); // ignore errors — fall through to print regardless

      const printTimeout = new Promise((_, reject) =>
        setTimeout(() => reject(new Error('update check timed out')), 5000));
      const result = await Promise.race([
        this.ros.write('/system/package/update/print'),
        printTimeout,
      ]);
      const u = result && result[0] ? result[0] : {};
      this._lastUpdateRow = u;
      if (!this._loggedUpdateFields) {
        console.log(this._lbl + ' package/update fields:', JSON.stringify(u)); // codeql[js/tainted-format-string]
        this._loggedUpdateFields = true;
      }
      if (this.lastPayload) {
        const latestVersion   = u['latest-version'] || '';
        const updateStatus    = u['status'] || '';
        const installedBase   = (this.lastPayload.version || '').replace(/\s*\(.*\)/, '').trim();
        const updateAvailable = latestVersion
          ? latestVersion !== installedBase
          : updateStatus.toLowerCase().includes('new version');
        const updated = { ...this.lastPayload, ts: Date.now(), latestVersion, updateAvailable: !!updateAvailable, updateStatus };
        this.lastPayload = updated;
        this._lastFp = '';
        this.io.emit('system:update', updated);

        // If still unresolved, retry in 60 s (update server may be slow)
        const isTransient = !latestVersion && (
          updateStatus === '' ||
          /finding out|checking|in progress/i.test(updateStatus)
        );
        if (isTransient) this._lastUpdateFetch = now - this.UPDATE_INTERVAL + 60000;
      }
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      console.error(this._lbl + ' update check failed:', msg); // codeql[js/tainted-format-string]
      this._lastUpdateRow = { status: 'Update check unavailable' };
      if (this.lastPayload) {
        const updated = { ...this.lastPayload, ts: Date.now(),
          latestVersion: '', updateAvailable: false,
          updateStatus: 'Update check unavailable' };
        this.lastPayload = updated;
        this._lastFp = '';
        this.io.emit('system:update', updated);
      }
    }
  }

  // Called for every interval push from the resource stream.
  // packet is the raw parsed row object (may include a .section field from
  // RouterOS interval responses — we ignore it and read only the data fields).
  _processRow(packet) {
    if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
    // Require at least one real data field so empty .section-only objects are skipped.
    if (!packet['cpu-load'] && !packet['total-memory']) return;

    const r = packet;
    const u = this._lastUpdateRow;
    const h = this._lastHealth;

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

    const installed       = r.version || '';
    const installedBase   = installed.replace(/\s*\(.*\)/, '').trim();
    const latestVersion   = u['latest-version'] || '';
    const updateStatus    = u['status'] || '';
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

    // Always set lastPayload so sendInitialState can replay it regardless of idle state.
    this.lastPayload = payload;

    if (!this._boardNameReported && payload.boardName && typeof this._onFirstBoardName === 'function') {
      this._boardNameReported = true;
      this._onFirstBoardName(payload.boardName);
    }

    // Run update check independently of browser connections — rate-limited by
    // UPDATE_INTERVAL (12 h) so this is effectively a no-op on most ticks.
    this._fetchUpdateStatus().catch(() => {});

    // Gate emit only — lastPayload already set above.
    if (this.io.engine.clientsCount === 0) return;

    const fp = `${cpuLoad},${memPct},${hddPct},${tempC},${r.uptime||''},${updateAvailable},${latestVersion}`;
    if (fp !== this._lastFp) {
      this._lastFp = fp;
      this.io.emit('system:update', payload);
    }
    this.state.lastSystemTs = Date.now();
    this.state.lastSystemErr = null;
  }

  // Polls /system/health/print on a slower interval — health data changes
  // rarely and the command does not support interval streaming.
  _pollHealth() {
    if (this.io.engine.clientsCount === 0) return;
    if (!this.ros.connected) return;
    this.ros.write('/system/health/print').then(h => {
      if (Array.isArray(h)) this._lastHealth = h;
    }).catch(() => {});
  }

  _scheduleHealthNext() {
    if (this._healthTimer) return;
    this._healthTimer = setTimeout(async () => {
      this._healthTimer = null;
      if (!this._healthInflight && this.ros.connected && this.io.engine.clientsCount > 0) {
        this._healthInflight = true;
        try {
          const h = await this.ros.write('/system/health/print');
          if (Array.isArray(h)) this._lastHealth = h;
        } catch (e) {} finally { this._healthInflight = false; }
      }
      this._scheduleHealthNext();
    }, 30000);
  }

  // ── poll-mode resource path ───────────────────────────────────────────────

  async _pollResourceOnce() {
    if (!this.ros.connected || this._pollInflight) return;
    this._pollInflight = true;
    try {
      const rows = await this.ros.write('/system/resource/print', [
        '=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime',
      ]);
      if (rows && rows[0]) this._processRow(rows[0]);
    } catch (e) {
      this.state.lastSystemErr = String(e && e.message ? e.message : e);
    } finally {
      this._pollInflight = false;
    }
  }

  _scheduleResourceNext() {
    clearTimeout(this._pollTimer);
    this._pollTimer = setTimeout(async () => {
      this._pollTimer = null;
      if (!this.streamMode) {
        await this._pollResourceOnce();
        this._scheduleResourceNext();
      }
    }, this.pollMs);
  }

  // ── stream-mode resource path ─────────────────────────────────────────────

  _restartStream() {
    if (this._stream) { try { this._stream.stop().catch(() => {}); } catch (e) {} this._stream = null; }
    if (this.streamMode) this._startResourceStream();
  }

  _startResourceStream() {
    if (this._stream || this._restarting) return;
    if (!this.ros.connected) return;
    const intervalSec = Math.max(1, Math.round(this.pollMs / 1000));

    // Pass null as the callback so RStream skips the section-handling debounce
    // in onStream() — RouterOS interval responses include a .section field that
    // routes packets through a 300 ms accumulator, which swallows data.
    // Instead we subscribe to the RStream 'data' event, which fires
    // unconditionally for every !re packet before the callback path runs.
    this._stream = this.ros.stream(
      '/system/resource/print',
      [
        `=interval=${intervalSec}`,
        '=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime',
      ],
      null
    );

    this._stream.on('data', (packet) => {
      try { this._processRow(packet); } catch (e) {
        console.error(this._lbl + ' processRow:', e && e.message ? e.message : e); // codeql[js/tainted-format-string]
      }
    });

    this._stream.on('error', (err) => {
      this.state.lastSystemErr = String(err && err.message ? err.message : err);
      console.error(this._lbl + ' stream error:', this.state.lastSystemErr); // codeql[js/tainted-format-string]
      this._stream = null;
      if (this._restarting) return;
      this._restarting = true;
      setTimeout(() => {
        this._restarting = false;
        if (this.ros.connected && !this._stream) this._startResourceStream();
      }, 3000);
    });
  }

  _startResources() {
    if (this.streamMode) {
      this._startResourceStream();
    } else {
      console.log(this._lbl + ' poll mode — polling /system/resource/print every', this.pollMs + 'ms'); // codeql[js/tainted-format-string]
      this._pollResourceOnce();
      this._scheduleResourceNext();
    }
  }

  start() {
    this._pollHealth();
    this._scheduleHealthNext();
    this._startResources();
    this._fetchUpdateStatus().catch(() => {}); // run once at startup
    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      // Tear down any live stream/timers before restarting to prevent duplicates.
      if (this._stream) { try { this._stream.stop().catch(() => {}); } catch (_) {} this._stream = null; }
      if (this._pollTimer)  { clearTimeout(this._pollTimer);  this._pollTimer  = null; }
      if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
      this._pollInflight   = false;
      this._healthInflight = false;
      this._restarting     = false;
      this._lastFp = '';
      this._lastUpdateFetch = 0;
      this._lastUpdateRow = {};
      this._startResources();
      this._scheduleHealthNext();
      this._pollHealth();
      this._fetchUpdateStatus().catch(() => {}); // re-check on reconnect
    });
  }

  suspend() {
    if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
  }

  resume() {
    this._scheduleHealthNext();
    this._pollHealth();
  }

  stop() {
    if (this._stream) { try { this._stream.stop().catch(() => {}); } catch (e) {} this._stream = null; }
    if (this._pollTimer)  { clearTimeout(this._pollTimer);  this._pollTimer  = null; }
    if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
    this._restarting = false;
  }
}

module.exports = SystemCollector;
