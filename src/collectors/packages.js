'use strict';
/**
 * Packages collector.
 *
 *   /system/package          what is installed, disabled, available, scheduled
 *   /system/routerboard      firmware: current, upgrade, minimum
 *   /system/package/update   the channel and the last known update status
 *
 * THIS COLLECTOR ONLY READS. Every write — enable, disable, uninstall,
 * unschedule, check-for-updates, apply-changes — lives in the socket actions in
 * index.js, gated on router:write. A collector runs unattended on a timer for
 * every connected router, so a write reachable from here would be a write
 * nobody asked for. A test asserts those strings never appear in this file.
 *
 * `/system/package/update/print` is a LOCAL read of the last check's result; it
 * contacts nothing. The check itself (which does reach MikroTik's servers) stays
 * where it already is, on its own 12-hour schedule in system.js, and as the
 * explicit packages:check action.
 *
 * THE FIVE STATES. A package row is not simply installed or not:
 *
 *   installed   version set, not disabled          routeros 7.24
 *   disabled    version set, disabled              an installed package turned off
 *   available   version EMPTY, available=true      on MikroTik's server, not here
 *   scheduled   `scheduled` non-empty              a change waiting for a reboot
 *   unknown     anything else                      reported rather than guessed
 *
 * `scheduled` outranks the others because it is the one the page must lead with:
 * enable/disable/uninstall do not act, they schedule, and nothing happens until
 * apply-changes reboots the router. A row can be "installed" and "scheduled for
 * uninstall" at once, so the scheduled verb travels separately as well.
 */

const { clampPoll, createPollLoop } = require('./util');

const PKG_CMD    = ['/system/package/print',
                    '=.proplist=.id,name,version,build-time,scheduled,size,available,disabled'];
const BOARD_CMD  = ['/system/routerboard/print',
                    '=.proplist=routerboard,board-name,model,serial-number,firmware-type,' +
                    'current-firmware,upgrade-firmware,minimum-firmware'];
const UPDATE_CMD = ['/system/package/update/print', ''];

// Firmware and the update row change on a reboot or a check, not on a tick.
const CONFIG_EVERY = 12;

const _bool = (v) => v === true || v === 'true' || v === 'yes';
const _num  = (v) => { const n = Number(v); return Number.isFinite(n) ? n : null; };

/**
 * RouterOS reports `scheduled` as a SENTENCE, not a verb — the live router
 * answers `Use "apply-changes" to proceed with install`. The page needs the verb
 * to label the pending row and to offer the right Undo, so it is derived here
 * rather than in the browser, and the original text travels alongside it.
 *
 * Order matters: "uninstall" contains "install", so it has to be tested first.
 */
function scheduledAction(text) {
  const t = String(text || '').toLowerCase();
  if (!t) return '';
  if (t.includes('uninstall')) return 'uninstall';
  if (t.includes('disable'))   return 'disable';
  if (t.includes('install'))   return 'install';
  if (t.includes('enable'))    return 'enable';
  if (t.includes('downgrade')) return 'downgrade';
  return 'change';
}

/**
 * Normalise package rows. Pure, so the five-state logic is testable without a
 * router — which matters, because `available` reads as a boolean and means
 * something quite different from "installed".
 */
function parsePackages(rows) {
  const out = [];
  for (const r of rows || []) {
    if (!r || !r.name) continue;                   // also drops {undefined:''}
    const version   = r.version || '';
    const scheduled = r.scheduled || '';
    const disabled  = _bool(r.disabled);
    // available=true means "obtainable from MikroTik", NOT "installed here".
    // An installed package reports available=false.
    const onServer  = _bool(r.available);

    let state = 'unknown';
    if (version && !disabled)      state = 'installed';
    else if (version && disabled)  state = 'disabled';
    else if (!version && onServer) state = 'available';

    out.push({
      // Carried so the action can target the row exactly. Both =numbers=<name>
      // and =.id= were verified against the live router; .id is used because a
      // name is not guaranteed unique and an id is.
      id:        r['.id'] || '',
      name:      String(r.name),
      version,
      buildTime: r['build-time'] || '',
      size:      _num(r.size),
      scheduled,
      scheduledAction: scheduledAction(scheduled),
      disabled,
      onServer,
      state,
    });
  }
  // Scheduled first — they are what the page leads with — then installed, then
  // everything the router merely offers.
  const rank = { scheduled: 0, installed: 1, disabled: 2, available: 3, unknown: 4 };
  out.sort((a, b) => {
    const ra = a.scheduled ? 0 : rank[a.state];
    const rb = b.scheduled ? 0 : rank[b.state];
    return ra - rb || a.name.localeCompare(b.name);
  });
  return out;
}

/** Firmware from /system/routerboard. A CHR or x86 install has no routerboard. */
function parseFirmware(row) {
  const r = row || {};
  const current = r['current-firmware'] || '';
  const upgrade = r['upgrade-firmware'] || '';
  return {
    isRouterboard:   _bool(r.routerboard),
    boardName:       r['board-name'] || '',
    model:           r.model || '',
    serial:          r['serial-number'] || '',
    firmwareType:    r['firmware-type'] || '',
    currentFirmware: current,
    upgradeFirmware: upgrade,
    minimumFirmware: r['minimum-firmware'] || '',
    // Only claim an upgrade when both are known and differ. A missing field must
    // not read as "up to date" any more than it reads as "out of date".
    upgradeAvailable: !!(current && upgrade && current !== upgrade),
  };
}

/**
 * The update row. Mirrors system.js's interpretation deliberately — the same
 * router state must not produce two different answers on two pages.
 */
function parseUpdate(row) {
  const r = row || {};
  const installed = String(r['installed-version'] || '').replace(/\s*\(.*\)/, '').trim();
  const latest    = r['latest-version'] || '';
  const status    = r.status || '';
  return {
    channel:          r.channel || '',
    installedVersion: installed,
    latestVersion:    latest,
    status,
    updateAvailable:  latest ? latest !== installed
                             : status.toLowerCase().includes('new version'),
  };
}

class PackagesCollector {
  constructor({ ros, io, state, pollMs }) {
    this.ros    = ros;
    this.io     = io;
    this.state  = state;
    this.pollMs = clampPoll(pollMs, 30000, 300000, 5000);
    this._lbl   = ros.routerLabel ? `[${ros.routerLabel}][packages]` : '[packages]';

    this._poll     = createPollLoop(() => this._tick(), () => this.pollMs);
    this._packages = [];
    this._firmware = parseFirmware(null);
    this._update   = parseUpdate(null);
    this._ticks    = 0;
    this._lastFp   = '';
    // undefined = unprobed, false = this router has no such menu, stop asking.
    this._pkgAvailable    = undefined;
    this._boardAvailable  = undefined;
    this._updateAvailable = undefined;
    this.lastPayload = null;
  }

  async _read(cmd, flag) {
    if (this[flag] === false) return [];
    try {
      const args = cmd[1] ? [cmd[1]] : [];
      const rows = await this.ros.write(cmd[0], args);
      this[flag] = true;
      return (rows || []).filter(r => r && Object.keys(r).length);
    } catch (e) {
      const msg = String((e && e.message) || e).toLowerCase();
      if (msg.includes('no such') || msg.includes('unknown command')) this[flag] = false;
      else if (msg.includes('not enough permissions') || msg.includes('permission denied')) this[flag] = false;
      else this.state.lastPackagesErr = e && e.message ? e.message : String(e);
      return [];
    }
  }

  /**
   * Re-read now. Called after an action so the pending-changes banner reflects
   * what the router actually did, rather than what the browser hoped it did.
   */
  async refreshNow() {
    this._ticks = 0;
    if (this.ros.connected) await this._tick();
  }

  async _tick() {
    if (!this.ros.connected) return;

    if (this._ticks % CONFIG_EVERY === 0) {
      const [board, upd] = await Promise.all([
        this._read(BOARD_CMD,  '_boardAvailable'),
        this._read(UPDATE_CMD, '_updateAvailable'),
      ]);
      this._firmware = parseFirmware(board[0]);
      this._update   = parseUpdate(upd[0]);
    }
    this._ticks++;

    this._packages = parsePackages(await this._read(PKG_CMD, '_pkgAvailable'));

    const scheduled = this._packages.filter(p => p.scheduled);
    const payload = {
      ts: Date.now(), pollMs: this.pollMs,
      packages: this._packages,
      firmware: this._firmware,
      update:   this._update,
      counts: {
        total:     this._packages.length,
        installed: this._packages.filter(p => p.state === 'installed').length,
        disabled:  this._packages.filter(p => p.state === 'disabled').length,
        available: this._packages.filter(p => p.state === 'available').length,
        scheduled: scheduled.length,
      },
      // The page leads with this: any scheduled change is inert until a reboot,
      // and saying so is the difference between "nothing happened" and "nothing
      // has happened YET".
      pendingReboot: scheduled.length > 0,
      available: this._pkgAvailable !== false,
    };
    this.lastPayload = payload;
    this.state.lastPackagesTs = payload.ts;

    const fp = JSON.stringify({
      p: this._packages.map(p => [p.name, p.version, p.state, p.scheduled]),
      f: [this._firmware.currentFirmware, this._firmware.upgradeFirmware],
      u: [this._update.latestVersion, this._update.status, this._update.updateAvailable],
    });
    if (fp === this._lastFp) return;
    this._lastFp = fp;
    this.io.to('page-packages').emit('packages:update', payload);
  }

  async start() {
    if (this.ros.connected) await this._tick();
    this._poll.start();
    this.ros.on('close', () => this._poll.stop());
    this.ros.on('connected', async () => {
      this._poll.stop();
      this._lastFp = '';
      this._ticks  = 0;
      this._pkgAvailable = this._boardAvailable = this._updateAvailable = undefined;
      await this._tick();
      this._poll.start();
    });
  }

  suspend() { this._poll.stop(); }
  resume()  { if (this.ros.connected) this._poll.start(); }

  stop() { this._poll.stop(); this._lastFp = ''; }
}

PackagesCollector.parsePackages    = parsePackages;
PackagesCollector.scheduledAction  = scheduledAction;
PackagesCollector.parseFirmware    = parseFirmware;
PackagesCollector.parseUpdate      = parseUpdate;
module.exports = PackagesCollector;
