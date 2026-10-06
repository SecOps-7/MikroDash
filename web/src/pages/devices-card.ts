// One Devices-page card: an OVERVIEW of a device, not a live readout.
//
// ── WHAT A CARD ANSWERS ─────────────────────────────────────────────────────
//
// "Is it up, has it been up, is it current, is it backed up, is anything wrong."
// Every one of those changes on the scale of minutes or days, so nothing here
// needs a stream: the status and uptime ride the 2s `routers:stats` the page
// already receives (one `system` read per router), and the strip and backup come
// from `GET /api/devices/overview` once a minute. The live readings (CPU, RAM,
// WAN rates, ports) belong to the device modal, which streams only while open.
//
// ── THREE STATES, AS EVERYWHERE ON THIS PAGE ───────────────────────────────
//
// `!known` is "nothing has looked at this router yet" and is drawn grey and
// "Checking…", never red: see `RouterStatsRow.known` in routers.ts.
//
// Pure: rows in, markup out. `routers.ts` places it and wires the click.
//
// The device modal's "Details" rail is here too (`detailsHtml`): what a device
// IS rather than what it is doing, all of it on the stats row or the overview,
// so the rail renders before the modal's live stream says anything.

import { esc } from '../dom';
import { fmtTs } from '../timefmt';
import type { RouterStatsRow } from '../gen/payloads';
import { stripHtml, uptimeLabel, type DeviceOverview } from './devices-strip';

export type CardState = 'unknown' | 'online' | 'offline';

export function cardState(r: RouterStatsRow): CardState {
  if (!r.known) return 'unknown';
  return r.online ? 'online' : 'offline';
}

const STATE_TEXT: Record<CardState, string> = {
  unknown: 'Checking…', online: 'Online', offline: 'Offline',
};

/**
 * A RouterOS uptime ("1w2d3h4m5s") as its leading components, seconds dropped:
 * "1w 2d 3h 4m". Seconds tick every refresh and add nothing at a glance.
 */
export function uptimeText(raw: string | null): string {
  if (!raw) return '';
  const parts = raw.match(/\d+[wdhm]/g);
  return parts && parts.length ? parts.slice(0, 3).join(' ') : raw;
}

/** How long ago, in one unit: "just now", "5m ago", "3h ago", "2d ago". */
export function ago(ms: number, now: number): string {
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm ago';
  const h = Math.floor(m / 60);
  if (h < 48) return h + 'h ago';
  return Math.floor(h / 24) + 'd ago';
}

/**
 * The backup line, or '' when there is nothing to say.
 *
 * NULL IS SILENCE, NOT "No backup". The server sends null both when a router has
 * never been backed up and when this viewer may not read its backups, and a
 * card claiming "No backup" to someone who simply cannot see them would be a
 * statement the server never made.
 */
export function backupHtml(o: DeviceOverview | undefined, now: number): string {
  const b = o && o.backup;
  if (!b) return '';
  const ok = b.lastOutcome === 'changed' || b.lastOutcome === 'unchanged';
  if (ok) {
    return '<span class="dv-backup dv-backup-ok" title="Last backup '
      + esc(fmtTs(b.lastAt, false)) + '">'
      + '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>'
      + 'Backup ' + ago(b.lastAt, now) + '</span>';
  }
  const good = b.lastSuccessAt != null
    ? 'Last good backup ' + fmtTs(b.lastSuccessAt, false)
    : 'No successful backup yet';
  return '<span class="dv-backup dv-backup-bad" title="' + esc(good) + '">'
    + '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 8v5M12 16.5v.5"/><circle cx="12" cy="12" r="9"/></svg>'
    + 'Backup failed ' + ago(b.lastAt, now) + '</span>';
}

/** "Update" beside the version, only when the check said so. Null is "not checked". */
export function updatePill(r: RouterStatsRow): string {
  if (r.updateAvailable !== true) return '';
  const to = r.latestVersion ? ' to ' + r.latestVersion : '';
  return '<span class="dv-update" title="' + esc('RouterOS update available' + to) + '">'
    + '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5M5 12l7-7 7 7"/></svg>Update</span>';
}

/** The strip block: label and percentage, the bar, and its time axis. */
export function connBlock(o: DeviceOverview | undefined, withAxis: boolean): string {
  if (!o) {
    // Not fetched yet: a shimmer the size of the real thing, so the card does
    // not jump when the overview lands.
    return '<div class="dv-conn"><div class="dv-conn-head"><span class="dv-label">Connectivity · 24h</span></div>'
      + '<div class="dv-strip dv-strip-loading" aria-hidden="true"></div>'
      + (withAxis ? '<div class="dv-axis"><span>24h ago</span><span>now</span></div>' : '') + '</div>';
  }
  const from = o.spans.length ? o.spans[0]!.from : 0;
  const to = o.spans.length ? o.spans[o.spans.length - 1]!.to : 0;
  const pct = o.uptimePct;
  const pctCls = pct == null ? 'dv-pct-none' : pct >= 99.9 ? 'dv-pct-good' : pct >= 99 ? 'dv-pct-warn' : 'dv-pct-bad';
  return '<div class="dv-conn"><div class="dv-conn-head"><span class="dv-label">Connectivity · 24h</span>'
    + '<span class="dv-pct ' + pctCls + '">' + uptimeLabel(pct) + '</span></div>'
    + stripHtml(o.spans, from, to, { uptimePct: pct, windowWord: '24 hours' })
    + (withAxis ? '<div class="dv-axis"><span>24h ago</span><span>now</span></div>' : '') + '</div>';
}

/** The whole card, column wrapper included. */
export function deviceCardHtml(
  r: RouterStatsRow, o: DeviceOverview | undefined, compact: boolean, now: number,
): string {
  const st = cardState(r);
  // Blank names are deleted sites the server padded for (see routers.ts).
  const sites = (r.siteNames || []).filter(Boolean);
  const sub = [r.host && r.host !== r.label ? r.host : '', sites.join(', ')].filter(Boolean);
  const alerts = r.openAlerts > 0
    ? '<span class="dv-alerts" title="' + r.openAlerts + ' unresolved alert' + (r.openAlerts === 1 ? '' : 's') + '">'
      + '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 9v4M12 17h.01"/><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/></svg>'
      + r.openAlerts + '</span>'
    : '';
  // The server sends this already sanitized; esc() it like any other value.
  // A device on a login profile says so when it is down: "invalid user name or
  // password" then points at the profile, not at the device.
  const why = st === 'offline' && r.lastError
    ? '<div class="dv-why">' + esc(r.lastError)
      + (r.loginProfile ? ' <span class="dv-why-via">(login profile ' + esc(r.loginProfile) + ')</span>' : '')
      + '</div>' : '';
  const up = uptimeText(r.uptime);

  let foot = '';
  if (compact) {
    foot = up ? '<div class="dv-foot"><div class="dv-foot-row"><span class="dv-up">Up ' + esc(up) + '</span></div></div>' : '';
  } else {
    const model = [r.boardName || '', r.version ? 'RouterOS ' + r.version : ''].filter(Boolean).join(' · ');
    foot = '<div class="dv-foot">'
      + '<div class="dv-foot-row"><span class="dv-model">' + (model ? esc(model) : '&nbsp;') + '</span>' + updatePill(r) + '</div>'
      + '<div class="dv-foot-row"><span class="dv-up">' + (up ? 'Up ' + esc(up) : '&nbsp;') + '</span>'
      + backupHtml(o, now) + '</div>'
      + '</div>';
  }

  return (compact
    ? '<div class="col-sm-6 col-lg-4 col-xl-3 col-xxl-2">'
    : '<div class="col-md-6 col-xl-4 col-xxl-3">')
    + '<div class="card h-100 dv-card dv-' + st + (compact ? ' dv-compact' : '')
    + '" role="button" tabindex="0" data-device="' + esc(r.id) + '" aria-label="'
    + esc('Open overview of ' + r.label + ', ' + STATE_TEXT[st]) + '">'
    + '<div class="dv-head">'
    + '<span class="dv-dot" aria-hidden="true"></span>'
    + '<div class="dv-id"><div class="dv-name"><strong>' + esc(r.label) + '</strong>'
    + (r.isActive ? '<span class="dv-active">active</span>' : '') + '</div>'
    + (sub.length && !compact ? '<div class="dv-sub">' + esc(sub.join(' · ')) + '</div>' : '')
    + '</div>'
    + '<div class="dv-badges">' + alerts + '<span class="dv-status">' + STATE_TEXT[st] + '</span></div>'
    + '</div>'
    + why
    + connBlock(o, !compact)
    + foot
    + '</div></div>';
}

/**
 * How a licence level is written.
 *
 * `4` becomes `L4`, MikroTik's own notation for a RouterBOARD licence. `free`,
 * `p1`, `p10` and `p-unlimited` are CHR licence levels and are already words, so
 * they are left exactly as the router said them: `Lfree` is not a thing.
 */
export function licenseLabel(level: string): string {
  return /^\d+$/.test(level) ? 'L' + level : level;
}

/** A RouterOS uptime ("1w2d3h4m5s") in milliseconds, or null when unreadable. */
export function uptimeMs(raw: string | null): number | null {
  if (!raw) return null;
  const unit: Record<string, number> = { w: 604800, d: 86400, h: 3600, m: 60, s: 1 };
  const parts = raw.match(/\d+[wdhms]/g);
  if (!parts) return null;
  return parts.reduce((t, p) => t + parseInt(p, 10) * (unit[p.slice(-1)] || 0), 0) * 1000;
}

function row(k: string, v: string): string {
  return '<div class="dvm-kv"><dt>' + esc(k) + '</dt><dd>' + v + '</dd></div>';
}

export function detailsHtml(r: RouterStatsRow, o: DeviceOverview | undefined, now: number): string {
  const dash = '<span class="text-muted">-</span>';
  const ros = r.version
    ? esc(r.version) + (r.updateAvailable === true
      ? ' <span class="dv-update">' + esc('Update' + (r.latestVersion ? ' to ' + r.latestVersion : '')) + '</span>'
      : r.updateAvailable === false ? ' <span class="dvm-current">Current</span>' : '')
    : dash;
  const up = uptimeMs(r.uptime);
  const sites = (r.siteNames || []).filter(Boolean);
  let backup = dash;
  if (o && o.backup) {
    const b = o.backup;
    const ok = b.lastOutcome === 'changed' || b.lastOutcome === 'unchanged';
    backup = ok
      ? esc(fmtTs(b.lastAt, false)) + ' <span class="text-muted">(' + ago(b.lastAt, now) + ')</span>'
      : '<span class="dv-backup-bad">Failed ' + ago(b.lastAt, now) + '</span>'
        + (b.lastSuccessAt != null
          ? '<div class="text-muted">Last good ' + esc(fmtTs(b.lastSuccessAt, false)) + '</div>'
          : '<div class="text-muted">None has succeeded</div>');
  }
  return '<dl class="dvm-details">'
    + row('Model', r.boardName ? esc(r.boardName) : dash)
    + row('Serial', r.serial ? '<span class="dvm-mono">' + esc(r.serial) + '</span>' : dash)
    + row('Architecture', r.arch ? esc(r.arch) : dash)
    + row('RouterOS', ros)
    + row('Licence', r.licenseLevel ? '<span class="dvm-pill">' + esc(licenseLabel(r.licenseLevel)) + '</span>' : dash)
    + row('Uptime', r.uptime ? esc(uptimeText(r.uptime)) : dash)
    // Derived, so it moves by a second or two between refreshes; minutes hide it.
    + row('Last boot', up != null ? esc(fmtTs(now - up, false)) : dash)
    + row('Host', '<span class="dvm-mono">' + esc(r.host) + '</span>')
    // From the router's IP Cloud when it has one (right behind NAT), otherwise
    // only a public interface address: a private WAN is never shown as one.
    + row('Public IP', r.publicIp
      ? '<span class="dvm-mono">' + esc(r.publicIp) + '</span>'
      // RouterOS learns it from the cloud time update, which does not run while
      // the NTP client is on (measured on the hAP AC2), or from DDNS.
      : '<span class="text-muted" title="Not known. Turn on IP &gt; Cloud &gt; DDNS on the router: '
        + 'while its NTP client is on, RouterOS skips the cloud time update that would find it.">-</span>')
    + (r.loginProfile ? row('Login', '<span class="dvm-mono">mikrodash</span> <span class="dvm-pill">'
      + esc(r.loginProfile) + '</span>') : '')
    + row('Sites', sites.length ? sites.map((s) => '<span class="dvm-pill">' + esc(s) + '</span>').join(' ') : dash)
    + row('Backup', backup)
    + row('Open alerts', r.openAlerts > 0
      ? '<span class="dv-alerts">' + r.openAlerts + '</span>' : '<span class="text-muted">None</span>')
    + '</dl>';
}
