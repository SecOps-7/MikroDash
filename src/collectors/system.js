const { stopStreamSafe } = require('./util');

// Update-check schedule, shared per router across every SystemCollector
// instance. See _updateSlot() for why this cannot live on the instance.
// Keyed 'host:port'; entries are tiny and bounded by the number of configured
// routers, so they are never evicted.
const _updateSchedule = new Map();

class SystemCollector {
  constructor({ ros, io, pollMs, state, streamMode, alertsActive }) {
    this.ros = ros;
    this.io = io;
    // The alerter is fed from the emit path, so idle-gating the emit also
    // silences alerts. A router with alerts enabled must keep emitting with no
    // viewer attached; non-active routers already behave this way because
    // alertSessions stubs clientsCount to 1. Defaults to off, so every existing
    // call site and test keeps today's idle behaviour.
    this._alertsActive = typeof alertsActive === 'function' ? alertsActive : () => false;
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
    // Default only. The live value is read from settings on each check (see
    // the UPDATE_INTERVAL accessor) so a change takes effect without a
    // restart, matching how the other intervals behave. Assigning to it still
    // works, which the tests rely on.
    this._updateIntervalOverride = null;
    // Bounded retry for a router still resolving the check. Unbounded, this
    // would become a 60 s upstream poll whenever the update server never
    // settles, which is exactly the pattern that earns a rate limit.
    this.UPDATE_RETRY_MS      = 60 * 1000;
    this.UPDATE_MAX_RETRIES   = 3;
    this._lastUpdateRow    = {};
    // One interval-bypass per collector instance, for a router whose update check
    // has never resolved (see _fetchUpdateStatus).
    this._forcedUpdateCheck = false;
    this._lastFp           = '';
    this.lastPayload       = null;
    this._boardNameReported = false;
    this._staticSerial  = null;
    this._staticLicense = null;
    this._staticFetched = false;

    this.ros.on('close', () => this.stop());
    this.ros.on('connected', () => {
      if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
      if (this._pollTimer)  { clearTimeout(this._pollTimer);  this._pollTimer  = null; }
      if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
      this._pollInflight   = false;
      this._healthInflight = false;
      this._restarting     = false;
      this._lastFp = '';
      // The update schedule and the cached row deliberately survive a
      // reconnect. Resetting them meant every reconnect fired another
      // check-for-updates, so a flapping link turned the 12 h interval into
      // one upstream call per flap, and wiping the row blanked the version
      // info on the dashboard until the next check.
      this._staticFetched = false;
      this._staticSerial  = null;
      this._staticLicense = null;
      this._startResources();
      this._scheduleHealthNext();
      this._pollHealth();
      this._fetchUpdateStatus().catch(() => {});
    });
  }

  // The schedule is keyed on the router, not on this instance. SystemCollector
  // is constructed up to three times per router — the active session
  // (index.js), the routers-overview session (overviewSessions.js) and the
  // background alert session (alertSessions.js) — and each start() would
  // otherwise fire its own check-for-updates, with overviewSessions starting
  // again on every reconnect. check-for-updates is the only call here that
  // leaves the router and reaches upgrade.mikrotik.com, so that turned one
  // interval into several upstream calls per router.
  // Hours in settings, milliseconds here. settings.load() is cached, so reading
  // it per check is cheap. An explicit assignment wins, which keeps the field
  // writable for tests that pin the interval.
  get UPDATE_INTERVAL() {
    if (this._updateIntervalOverride != null) return this._updateIntervalOverride;
    let hours = 12;
    try {
      const h = require('../settings').load().updateCheckHours;
      if (Number.isFinite(h) && h > 0) hours = h;
    } catch (_) { /* settings unreadable — fall back to the default */ }
    return hours * 60 * 60 * 1000;
  }
  set UPDATE_INTERVAL(v) { this._updateIntervalOverride = v; }

  _updateSlot() {
    const cfg = (this.ros && this.ros.cfg) || {};
    // Stub ROS objects in tests carry no host, so they get a private slot and
    // cannot leak scheduling state into each other.
    if (!cfg.host) return (this._ownUpdateSlot || (this._ownUpdateSlot = { lastFetch: 0, inflight: false, retries: 0 }));
    const key = cfg.host + ':' + (cfg.port || '');
    let slot = _updateSchedule.get(key);
    if (!slot) { slot = { lastFetch: 0, inflight: false, retries: 0 }; _updateSchedule.set(key, slot); }
    return slot;
  }

  // Accessors so the field stays writable: tests set it directly to suppress a
  // fetch, and the retry path below rewinds it.
  get _lastUpdateFetch()  { return this._updateSlot().lastFetch; }
  set _lastUpdateFetch(v) { this._updateSlot().lastFetch = v; }

  // Applies a /system/package/update row to the cached payload and emits.
  // Safe to call before the first resource tick: _lastUpdateRow is what
  // _buildPayload reads, so the values still reach the next emit even when
  // there is no payload to update yet. Previously all of this sat behind
  // `if (this.lastPayload)`, so the startup check — which runs before the
  // resource stream has delivered anything — discarded its result while still
  // consuming the 12 h window.
  // A row counts as an "answer" only once the router has actually resolved the
  // check. While it is still working, status is empty or says so and there is no
  // version — caching that would make every later session believe the question
  // had already been answered.
  static _isUpdateAnswer(u) {
    if (!u || typeof u !== 'object') return false;
    const latest = u['latest-version'] || '';
    const status = u['status'] || '';
    if (latest) return true;
    if (!status) return false;
    return !/finding out|checking|in progress/i.test(status);
  }

  _applyUpdateRow(u) {
    this._lastUpdateRow = u;
    // Cache the answer on the shared per-router slot. That slot is keyed by
    // host:port and shared by every session for the router, so a collector built
    // later — switching the active router builds a brand new one — can show the
    // result immediately instead of sitting blank until the window reopens.
    // Costs no upstream call.
    if (SystemCollector._isUpdateAnswer(u)) this._updateSlot().row = u;
    if (!this.lastPayload) return;
    const latestVersion   = u['latest-version'] || '';
    const updateStatus    = u['status'] || '';
    const installedBase   = (this.lastPayload.version || '').replace(/\s*\(.*\)/, '').trim();
    const updateAvailable = latestVersion
      ? latestVersion !== installedBase
      : updateStatus.toLowerCase().includes('new version');
    const updated = { ...this.lastPayload, ts: Date.now(), latestVersion,
                      updateAvailable: !!updateAvailable, updateStatus };
    this.lastPayload = updated;
    this._lastFp = '';
    this.io.emit('system:update', updated);
  }

  // Fetch update status independently so a slow RouterOS update-server
  // response never delays the resource/health tick (and thus the gauges).
  async _fetchUpdateStatus() {
    if (!this.ros.connected) return;
    const slot = this._updateSlot();
    if (slot.inflight) return;
    const now = Date.now();

    // This collector has no answer of its own yet, but one is already cached for
    // the router: adopt it. This is the router-switch case — alertSessions and
    // overviewSessions run their own SystemCollector against a null io, so they
    // consume the shared window and discard the result, leaving the session you
    // actually switch to with a blank Updates card for up to updateCheckHours.
    if (!SystemCollector._isUpdateAnswer(this._lastUpdateRow) &&
        SystemCollector._isUpdateAnswer(slot.row)) {
      this._applyUpdateRow(slot.row);
      return;
    }

    if ((now - slot.lastFetch) < this.UPDATE_INTERVAL) {
      // Nothing cached and the window is still open — normally we wait. But a
      // router that has never resolved its check would then stay blank until the
      // window reopens, which is the very thing being fixed. Allow exactly one
      // bypass per collector instance: enough to fill the card on a switch, and
      // bounded, so a router whose check always fails cannot turn the 2 s
      // resource tick into a poll against upgrade.mikrotik.com.
      const haveAnswer = SystemCollector._isUpdateAnswer(slot.row) ||
                         SystemCollector._isUpdateAnswer(this._lastUpdateRow);
      if (haveAnswer || this._forcedUpdateCheck) return;
      this._forcedUpdateCheck = true;
    }
    slot.inflight = true;
    slot.lastFetch = now;
    // Declared out here, cleared in the finally below. Attaching .finally() to
    // the race is not enough: this.ros.write can throw SYNCHRONOUSLY, in which
    // case the throw escapes before any handler is attached and the timer —
    // already created by the Promise executor — is left running for its full
    // 15s. That is exactly what stalled the test suite.
    let checkT = null, printT = null;
    try {
      // Explicitly trigger a check with the update server (blocks until done or times out).
      // Without this, print returns cached/transient "finding out latest version..." state.
      // The loser of a Promise.race keeps running, so these timers must be
      // cleared explicitly. Left dangling they hold the event loop open for up
      // to 15s after the work is done — harmless in a server that runs forever,
      // but it stalled the test suite by that long and was part of why the
      // documented test command needed --test-force-exit.
      const checkTimeout = new Promise((_, reject) => {
        checkT = setTimeout(() => reject(new Error('check-for-updates timed out')), 15000);
      });
      let checkErr = null;
      await Promise.race([
        this.ros.write('/system/package/update/check-for-updates'),
        checkTimeout,
      ]).catch(e => { checkErr = e; }).finally(() => clearTimeout(checkT));

      const printTimeout = new Promise((_, reject) => {
        printT = setTimeout(() => reject(new Error('update check timed out')), 5000);
      });
      const result = await Promise.race([
        this.ros.write('/system/package/update/print'),
        printTimeout,
      ]).finally(() => clearTimeout(printT));
      const u = result && result[0] ? result[0] : {};
      if (!this._loggedUpdateFields) {
        console.log('%s', this._lbl + ' package/update fields:', JSON.stringify(u));
        this._loggedUpdateFields = true;
      }

      // A denied check is not a transient condition. /print still succeeds on
      // read permission alone and returns whatever the router last cached, so
      // swallowing this error made the dashboard show stale data and look
      // healthy doing it. Report it instead, and do not retry: only a config
      // change fixes it. The word "unavailable" is deliberate, since the
      // frontend styles a status matching it as a warning row.
      const denied = checkErr && /not enough permission|no permission|not allowed/i.test(checkErr.message || '');
      if (denied) {
        if (!this._loggedCheckDenied) {
          this._loggedCheckDenied = true;
          console.warn('%s update check unavailable: the API user lacks "write" permission for ' +
            '/system/package/update/check-for-updates, so only the router\'s cached state is shown', this._lbl);
        }
        this._applyUpdateRow({ ...u, status: 'Update check unavailable — API user needs write permission' });
        slot.retries = 0;
        return;
      }
      if (checkErr && !this._loggedCheckErr) {
        this._loggedCheckErr = true;
        console.warn('%s check-for-updates failed (%s); reporting the router\'s cached state',
          this._lbl, checkErr.message || String(checkErr));
      }

      this._applyUpdateRow(u);

      // Retry only while the router says it is still working, and only a
      // bounded number of times. Each retry is another upstream call, so an
      // update server that never settles must not become a 60 s poll forever.
      const latestVersion = u['latest-version'] || '';
      const status        = u['status'] || '';
      const isTransient = !latestVersion && (status === '' || /finding out|checking|in progress/i.test(status));
      if (isTransient && slot.retries < this.UPDATE_MAX_RETRIES) {
        slot.retries++;
        slot.lastFetch = now - this.UPDATE_INTERVAL + this.UPDATE_RETRY_MS;
      } else {
        slot.retries = 0;
      }
    } catch (e) {
      const msg = e && e.message ? e.message : String(e);
      // Literal format string with the label passed as an argument, not
      // concatenated into position 0. console.* treats its first argument as a
      // format string, and _lbl embeds the user-set router label — a router
      // named with a "%s" would otherwise swallow msg and garble the line.
      console.error('%s update check failed: %s', this._lbl, msg);
      this._applyUpdateRow({ status: 'Update check unavailable' });
    } finally {
      clearTimeout(checkT);
      clearTimeout(printT);
      slot.inflight = false;
    }
  }

  // One-time fetch of static hardware/license info (serial, license level).
  // Called fire-and-forget from _processRow(); silently ignores failures so
  // CHR/virtual routers (no routerboard) are handled gracefully.
  async _fetchStaticInfo() {
    if (this._staticFetched) return;
    this._staticFetched = true;
    try {
      const [rb, lic] = await Promise.allSettled([
        this.ros.write('/system/routerboard/print'),
        this.ros.write('/system/license/print'),
      ]);
      if (rb.status === 'fulfilled' && rb.value && rb.value[0])
        this._staticSerial = rb.value[0]['serial-number'] || null;
      if (lic.status === 'fulfilled' && lic.value && lic.value[0])
        this._staticLicense = lic.value[0]['level'] || lic.value[0]['nlevel'] || null;
    } catch (_) {}
  }

  // Called for every interval push from the resource stream.
  // packet is the raw parsed row object (may include a .section field from
  // RouterOS interval responses — we ignore it and read only the data fields).
  _processRow(packet) {
    if (!packet || typeof packet !== 'object' || Array.isArray(packet)) return;
    // Require at least one real data field so empty .section-only objects are skipped.
    if (!packet['cpu-load'] && !packet['total-memory']) return;

    this._fetchStaticInfo().catch(() => {});

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
      arch:         r['architecture-name'] || null,
      serial:       this._staticSerial     || null,
      licenseLevel: this._staticLicense    || null,
    };

    // Always set lastPayload so sendInitialState can replay it regardless of idle state.
    this.lastPayload = payload;

    if (!this._boardNameReported && payload.boardName && typeof this._onFirstBoardName === 'function') {
      this._boardNameReported = true;
      this._onFirstBoardName(payload.boardName);
    }

    // Report hardware/firmware identity so index.js can persist it against the
    // router entry. Fires only when the triple actually changes: this method
    // runs every tick, and while model and serial are fixed for the life of a
    // device, the version does change on upgrade and must not be write-once.
    //
    // installedBase, not payload.version: the Routers table wants a bare
    // "7.23.3", and dropping the channel from the stored value (rather than
    // only hiding it in the UI) also means switching stable→testing at the same
    // release does not churn a write and a broadcast.
    const identityKey = [payload.boardName, payload.serial, installedBase].join(' ');
    if (identityKey !== this._lastIdentityKey && typeof this._onIdentity === 'function') {
      this._lastIdentityKey = identityKey;
      this._onIdentity({ model: payload.boardName, serial: payload.serial, osVersion: installedBase });
    }

    // Run update check independently of browser connections — rate-limited by
    // UPDATE_INTERVAL (12 h) so this is effectively a no-op on most ticks.
    this._fetchUpdateStatus().catch(() => {});

    // Gate emit only — lastPayload already set above. Alerts ride the emit
    // path, so a router with alerts enabled must not be gated here.
    if (this.io.engine.clientsCount === 0 && !this._alertsActive()) return;

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
        '=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime,architecture-name',
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
    }, Math.max(500, Math.min(60000, this.pollMs)));
  }

  // ── stream-mode resource path ─────────────────────────────────────────────

  _restartStream() {
    if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
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
        '=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space,version,board-name,platform,cpu-count,cpu-frequency,uptime,architecture-name',
      ],
      null
    );

    this._stream.on('data', (packet) => {
      try { this._processRow(packet); } catch (e) {
        console.error('%s', this._lbl + ' processRow:', e && e.message ? e.message : e);
      }
    });

    this._stream.on('error', (err) => {
      this.state.lastSystemErr = String(err && err.message ? err.message : err);
      console.error('%s', this._lbl + ' stream error:', this.state.lastSystemErr);
      this._stream = null;
      if (this._restarting) return;
      this._restarting = true;
      this._errRestartTimer = setTimeout(() => {
        this._errRestartTimer = null;
        this._restarting = false;
        if (this.ros.connected && !this._stream) this._startResourceStream();
      }, 3000);
    });
  }

  _startResources() {
    if (this.streamMode) {
      this._startResourceStream();
    } else {
      console.log('%s', this._lbl + ' poll mode — polling /system/resource/print every', this.pollMs + 'ms');
      this._pollResourceOnce();
      this._scheduleResourceNext();
    }
  }

  start() {
    this._pollHealth();
    this._scheduleHealthNext();
    this._startResources();
    this._fetchUpdateStatus().catch(() => {}); // run once at startup
  }

  suspend() {
    if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
  }

  resume() {
    this._scheduleHealthNext();
    this._pollHealth();
  }

  stop() {
    if (this._stream) { stopStreamSafe(this._stream); this._stream = null; }
    if (this._pollTimer)  { clearTimeout(this._pollTimer);  this._pollTimer  = null; }
    if (this._healthTimer) { clearTimeout(this._healthTimer); this._healthTimer = null; }
    if (this._errRestartTimer) { clearTimeout(this._errRestartTimer); this._errRestartTimer = null; }
    this._restarting = false;
  }
}

module.exports = SystemCollector;
