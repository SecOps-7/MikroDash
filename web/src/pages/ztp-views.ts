// Zero-touch provisioning's markup, as pure functions: the Devices page's
// Provisioning section and its cards, the wizards' steps, the script panel
// and Settings → Provisioning's status. `ztp.ts` wires them.

import { esc } from '../dom';
import type { ZTPBatchView, ZTPDeviceView, ZTPPayload, ZTPStatus } from '../gen/payloads';
import { valuesGrid, type RouterOpt } from './config-management-deploy';
import type { VarDef } from './config-management-editor';

/** Each state as a person reads it, and its pill. */
export const STATE: Record<string, { word: string; pill: string; hint: string }> = {
  awaiting: { word: 'Waiting for device', pill: 'ztp-pill-wait',
    hint: 'Run its script on the router. It calls home within a minute of running it.' },
  pending: { word: 'Not yet provisioned', pill: 'ztp-pill-pending',
    hint: 'It called home without being expected. Onboard it to add it to the fleet, or reject it.' },
  enrolled: { word: 'Onboarding', pill: 'ztp-pill-busy', hint: 'Connecting to it through its tunnel.' },
  provisioning: { word: 'Applying its template', pill: 'ztp-pill-busy', hint: 'Deploying through Config Management.' },
  provisioned: { word: 'Provisioned', pill: 'ztp-pill-ok', hint: '' },
  failed: { word: 'Provisioning failed', pill: 'ztp-pill-bad', hint: '' },
  rejected: { word: 'Rejected', pill: 'ztp-pill-off', hint: 'It can no longer reach this MikroDash.' },
};

/** The devices the section shows: every one not yet a working router. A
 *  provisioned device is on the Devices grid like any other router. */
export function sectionDevices(p: ZTPPayload): ZTPDeviceView[] {
  const order = ['pending', 'failed', 'enrolled', 'provisioning', 'awaiting', 'rejected'];
  return p.devices.filter((d) => d.state !== 'provisioned')
    .sort((a, b) => order.indexOf(a.state) - order.indexOf(b.state) || b.createdAt - a.createdAt);
}

/** "in 6 days", "3 minutes ago": near times in words, from `now`. */
export function relTime(ms: number, now: number): string {
  if (!ms) return '';
  const d = ms - now, a = Math.abs(d);
  const [n, unit] = a < 90e3 ? [0, ''] : a < 90 * 60e3 ? [Math.round(a / 60e3), 'minute']
    : a < 36 * 3600e3 ? [Math.round(a / 3600e3), 'hour'] : [Math.round(a / 86400e3), 'day'];
  if (!unit) return d > 0 ? 'in a moment' : 'just now';
  const s = n + ' ' + unit + (n === 1 ? '' : 's');
  return d > 0 ? 'in ' + s : s + ' ago';
}

const btn = (act: string, id: string, label: string, cls = 'sbtn-ghost'): string =>
  '<button class="sbtn ' + cls + ' ztp-btn" type="button" data-ztp-act="' + act + '" data-ztp-id="' + esc(id) + '">' +
  label + '</button>';

/** One device's card. */
export function deviceCard(d: ZTPDeviceView, now: number): string {
  const s = STATE[d.state] ?? { word: d.state, pill: 'ztp-pill-off', hint: '' };
  const mode = d.mode === 'local' ? 'Local' : d.mode === 'remote' ? 'Remote' : 'Called home';
  const facts: [string, string][] = [];
  if (d.serial) facts.push(['Serial', d.serial]);
  if (d.model) facts.push(['Model', d.model]);
  if (d.version) facts.push(['RouterOS', d.version]);
  if (d.identity && d.identity !== d.label) facts.push(['Identity', d.identity]);
  if (d.tunnelIp) facts.push(['Tunnel', d.tunnelIp]);
  else if (d.source) facts.push(['From', d.source]);
  if (d.batchName) facts.push(['Script', d.batchName]);
  if (d.state === 'awaiting' && d.expiresAt) {
    facts.push([d.expiresAt < now ? 'Expired' : 'Expires', relTime(d.expiresAt, now)]);
  }
  if (d.firstSeen) facts.push(['First seen', relTime(d.firstSeen, now)]);

  let actions = '';
  switch (d.state) {
    case 'pending':
      actions = btn('onboard', d.id, 'Onboard', 'sbtn-primary') + btn('reject', d.id, 'Reject', 'sbtn-danger');
      break;
    case 'awaiting':
      actions = btn('regenerate', d.id, 'New script') + btn('delete', d.id, 'Remove');
      break;
    case 'failed':
      actions = btn('retry', d.id, 'Try again', 'sbtn-primary') + btn('delete', d.id, 'Remove');
      break;
    case 'rejected':
      actions = btn('delete', d.id, 'Forget');
      break;
  }
  const busy = d.state === 'enrolled' || d.state === 'provisioning';
  const title = d.label || d.identity || d.serial || 'Unnamed device';
  return '<div class="ztp-card state-' + esc(d.state) + '">' +
    '<div class="ztp-card-head"><div class="ztp-card-name">' + esc(title) + '</div>' +
    '<span class="vpn-hs-badge ' + s.pill + '">' + (busy ? '<span class="ztp-spin" aria-hidden="true"></span>' : '') +
    esc(s.word) + '</span></div>' +
    '<div class="ztp-card-mode"><span class="vpn-hs-badge ztp-pill-mode">' + mode + '</span></div>' +
    (d.error ? '<div class="ztp-card-err">' + esc(d.error) + '</div>' : s.hint ? '<div class="ztp-card-hint">' + esc(s.hint) + '</div>' : '') +
    (facts.length ? '<dl class="ztp-facts">' + facts.map(([k, v]) => '<dt>' + esc(k) + '</dt><dd>' + esc(v) + '</dd>').join('') + '</dl>' : '') +
    (d.runId ? '<div class="ztp-card-hint">Its deploy is in Config Management → History.</div>' : '') +
    (actions ? '<div class="ztp-card-actions">' + actions + '</div>' : '') + '</div>';
}

/** The Devices page's Provisioning section: its count and its cards, or '' when
 *  there is nothing to show and provisioning is off. */
export function sectionHtml(p: ZTPPayload, now: number): string {
  const list = sectionDevices(p);
  const pending = list.filter((d) => d.state === 'pending').length;
  if (!list.length) {
    if (!p.status.enabled) return '';
    return '<div class="ztp-empty">No devices are waiting. <strong>Add device</strong> makes a script for a new router; ' +
      'a router running a generic script from Settings → Provisioning appears here to be onboarded.</div>';
  }
  return '<div class="ztp-sec-head"><span class="ztp-sec-title">Provisioning</span>' +
    '<span class="card-badge active-blue">' + list.length + '</span>' +
    (pending ? '<span class="ztp-sec-note">' + pending + (pending === 1 ? ' device is' : ' devices are') +
      ' waiting to be onboarded</span>' : '') + '</div>' +
    '<div class="ztp-grid">' + list.map((d) => deviceCard(d, now)).join('') + '</div>';
}

/** The LAN address to suggest for a local device: the browser's own origin,
 *  unless that is this machine's loopback, which a router can never reach.
 *  Found in the live test, where a browser on the MikroDash host was offered
 *  http://localhost:3081. */
export function lanSuggestion(origin: string, hostname: string): string {
  return /^(localhost|127\.|\[?::1\]?$)/.test(hostname) ? '' : origin;
}

/** Why a remote device cannot be added yet, or ''. */
export function remoteBlocked(s: ZTPStatus): string {
  if (!s.enabled) return 'Switch provisioning on in Settings → Provisioning first.';
  if (!s.up) return 'Provisioning is switched on but not running' + (s.error ? ': ' + s.error : '.');
  if (!s.endpoint) return 'Set the address routers reach this MikroDash on, in Settings → Provisioning.';
  return '';
}

/** Step 1 of Add device: two choices. */
export function whereStep(mode: string, s: ZTPStatus): string {
  const blocked = remoteBlocked(s);
  const card = (m: string, title: string, lines: string, off = ''): string =>
    '<label class="ztp-choice' + (mode === m ? ' is-on' : '') + (off ? ' is-off' : '') + '">' +
    '<input type="radio" name="ztpMode" value="' + m + '"' + (mode === m ? ' checked' : '') + (off ? ' disabled' : '') + '>' +
    '<span class="ztp-choice-title">' + title + '</span><span class="ztp-choice-text">' + lines + '</span>' +
    (off ? '<span class="ztp-choice-off">' + esc(off) + '</span>' : '') + '</label>';
  return '<p class="ztp-lead">Where will this router be?</p><div class="ztp-choices">' +
    card('remote', 'Remote', 'Anywhere with internet access, behind NAT included. It dials this MikroDash over an ' +
      'encrypted WireGuard tunnel, and is managed through it.', blocked) +
    card('local', 'Local', 'On a network this MikroDash can reach directly. No tunnel: it calls home over your LAN ' +
      'and is managed at its own address.') + '</div>';
}

export interface DeviceForm { label: string; serial: string; siteIds: string[]; days: number; lanUrl: string }
export interface SiteOpt { id: string; name: string }

export const SERIAL_RE = /^[A-Za-z0-9+/=._-]{0,64}$/;

/** Why the device step is incomplete, or ''. */
export function deviceProblem(f: DeviceForm, mode: string): string {
  if (!f.label.trim()) return 'Give it a name';
  if (f.label.trim().length > 64) return 'The name is longer than 64 characters';
  if (!SERIAL_RE.test(f.serial.trim())) return 'A serial number is letters, digits and . _ - / + =';
  if (mode === 'local' && !/^https?:\/\/[^\s/?#]+\/?$/.test(f.lanUrl.trim())) {
    return 'Give the address the router reaches this MikroDash on, like http://192.168.88.10:3081';
  }
  return '';
}

/** Step 2: what the device is called, and where it belongs. */
export function deviceStep(f: DeviceForm, mode: string, sites: SiteOpt[], facts?: ZTPDeviceView): string {
  const known = facts ? '<dl class="ztp-facts ztp-facts-wide">' + ([
    ['Serial', facts.serial], ['Model', facts.model], ['RouterOS', facts.version], ['Identity', facts.identity],
    ['Called from', facts.source], ['Script', facts.batchName],
  ] as [string, string][]).filter(([, v]) => v).map(([k, v]) => '<dt>' + k + '</dt><dd>' + esc(v) + '</dd>').join('') +
    '</dl>' : '';
  return known +
    '<div class="sform-group"><label class="sform-label" for="ztpLabel">Name</label>' +
    '<input id="ztpLabel" class="sform-input" maxlength="64" autocomplete="off" value="' + esc(f.label) +
    '" placeholder="Branch office router"></div>' +
    (facts ? '' : '<div class="sform-group"><label class="sform-label" for="ztpSerial">Serial number ' +
      '<span class="ztp-opt">optional</span></label>' +
      '<input id="ztpSerial" class="sform-input" maxlength="64" autocomplete="off" value="' + esc(f.serial) +
      '" placeholder="HF1234567AB"><div class="ztp-help">When given, only the router with this serial can use ' +
      'the script. On the router: <code>/system routerboard print</code>, or on a CHR ' +
      '<code>/system license print</code>.</div></div>') +
    (mode === 'local' ? '<div class="sform-group"><label class="sform-label" for="ztpLanUrl">This MikroDash, as the ' +
      'router reaches it</label><input id="ztpLanUrl" class="sform-input" autocomplete="off" value="' + esc(f.lanUrl) +
      '" placeholder="http://192.168.88.10:3081"><div class="ztp-help">The router calls home here, and its API user ' +
      'accepts logins from this address only.</div></div>' : '') +
    (sites.length ? '<div class="sform-group"><span class="sform-label">Sites</span><div class="ztp-sites">' +
      sites.map((s) => '<label class="ztp-site"><input type="checkbox" data-ztp-site="' + esc(s.id) + '"' +
        (f.siteIds.includes(s.id) ? ' checked' : '') + '> ' + esc(s.name) + '</label>').join('') + '</div></div>' : '') +
    (facts ? '' : '<div class="sform-group"><label class="sform-label" for="ztpDays">The script works for</label>' +
      '<select id="ztpDays" class="sform-input">' + [1, 7, 30, 90].map((n) => '<option value="' + n + '"' +
        (f.days === n ? ' selected' : '') + '>' + n + (n === 1 ? ' day' : ' days') + '</option>').join('') +
      '</select></div>');
}

export interface TemplateOpt { id: string; name: string; description: string; canned: boolean }
export interface Finding { level: string; code: string; line?: number; message: string }
/** A template, opened for the wizard. */
export interface OpenTemplate { id: string; defs: VarDef[]; findings: Finding[] }

/** The checks that need an OK, one per code: the server accepts by code, since
 *  line numbers are not known until the device's own preview. */
export function ackCodes(findings: Finding[]): Finding[] {
  const seen = new Set<string>();
  return findings.filter((f) => f.level === 'ack' && !seen.has(f.code) && seen.add(f.code));
}

/** Why the configuration step is incomplete, or ''. */
export function configProblem(tpl: OpenTemplate | null, chosen: string, values: Record<string, string>,
  acked: Set<string>): string {
  if (!chosen) return '';
  if (!tpl || tpl.id !== chosen) return 'Opening the template…';
  if (tpl.findings.some((f) => f.level === 'refuse')) return 'This template cannot be applied on arrival';
  for (const d of tpl.defs) {
    if (d.required && !(values[d.name] ?? d.default ?? '').trim()) return 'Fill in ' + (d.label || d.name);
  }
  if (ackCodes(tpl.findings).some((f) => !acked.has(f.code))) return 'Tick OK on every check that needs it';
  return '';
}

/** Step 3: a template to apply on arrival, its settings and its checks. */
export function configStep(templates: TemplateOpt[], chosen: string, tpl: OpenTemplate | null, label: string,
  values: Record<string, string>, acked: Set<string>, reveal: boolean): string {
  const pick = '<div class="sform-group"><label class="sform-label" for="ztpTpl">Apply on arrival</label>' +
    '<select id="ztpTpl" class="sform-input"><option value="">Nothing: just add it to the fleet</option>' +
    templates.map((t) => '<option value="' + esc(t.id) + '"' + (t.id === chosen ? ' selected' : '') + '>' +
      esc(t.name) + (t.canned ? ' (built in)' : '') + '</option>').join('') + '</select>' +
    '<div class="ztp-help">A Config Management template that adds configuration. It is previewed on the router ' +
    'itself when it arrives, and deployed with a restore point and the auto-revert, as any deploy is.</div></div>';
  if (!chosen) return pick;
  if (!tpl || tpl.id !== chosen) return pick + '<div class="ztp-help">Opening the template…</div>';
  const one: RouterOpt[] = [{ id: 'device', label: label || 'This device' }];
  const grid = valuesGrid(tpl.defs, one, { device: values }, reveal);
  const refused = tpl.findings.filter((f) => f.level === 'refuse');
  const acks = ackCodes(tpl.findings);
  const notes = tpl.findings.filter((f) => f.level !== 'ack' && f.level !== 'refuse');
  return pick + '<div class="ztp-sub">Settings</div>' + grid +
    (refused.length ? '<div class="cfg-banner is-bad">' + refused.map((f) => esc(f.message)).join('<br>') + '</div>' : '') +
    (acks.length ? '<div class="ztp-sub">Checks that need your OK</div><ul class="cfg-findings">' + acks.map((f) =>
      '<li class="cfg-finding cfg-lvl-ack"><label class="cfg-ack"><input type="checkbox" data-ztp-ack="' + esc(f.code) +
      '"' + (acked.has(f.code) ? ' checked' : '') + '> OK</label><span class="cfg-finding-msg">' + esc(f.message) +
      '</span></li>').join('') + '</ul>' : '') +
    (notes.length ? '<details class="ztp-notes"><summary>' + notes.length + ' more ' +
      (notes.length === 1 ? 'note' : 'notes') + '</summary><ul class="cfg-findings">' + notes.map((f) =>
      '<li class="cfg-finding cfg-lvl-' + esc(f.level) + '"><span class="cfg-finding-msg">' + esc(f.message) +
      '</span></li>').join('') + '</ul></details>' : '');
}

/** What the server answers when it makes a script. */
export interface ScriptResult { script: string; filename: string; expiresAt: number }

/** The command that runs a script once the file is on the router. */
export const importCommand = (filename: string): string => '/import file-name=' + filename;

/** The script, how to run it, and when it stops working. */
export function scriptPanel(r: ScriptResult, now: number, what: string): string {
  return '<div class="ztp-script-head"><div><strong>' + esc(r.filename) + '</strong><div class="ztp-help">' + esc(what) +
    ' It stops working ' + esc(relTime(r.expiresAt, now)) + '.</div></div><div class="ztp-script-btns">' +
    '<button class="sbtn sbtn-ghost" type="button" data-ztp-copy>Copy</button>' +
    '<button class="sbtn sbtn-primary" type="button" data-ztp-download>Download</button></div></div>' +
    '<pre class="ztp-script" tabindex="0">' + esc(r.script) + '</pre>' +
    '<ol class="ztp-steps"><li>Upload <strong>' + esc(r.filename) + '</strong> to the router: drag it into WinBox\'s ' +
    'Files window, or use the Files page here.</li><li>In a terminal on the router, run <code>' +
    esc(importCommand(r.filename)) + '</code>.</li><li>Or paste the whole script into the terminal instead.</li></ol>' +
    '<div class="cfg-banner is-warn">This script is shown once and holds a secret. Keep it like a password; if it ' +
    'is lost, make a new one, which also stops this one working.</div>';
}

/** Settings → Provisioning's status line. */
export function statusLine(s: ZTPStatus): string {
  if (!s.enabled) return '<span class="vpn-hs-badge ztp-pill-off">Off</span> Routers cannot call home.';
  if (!s.up) return '<span class="vpn-hs-badge ztp-pill-bad">Not running</span> ' + esc(s.error || 'Save to start it.');
  return '<span class="vpn-hs-badge ztp-pill-ok">Running</span> Listening on UDP ' + s.port + ' · ' +
    s.peers + (s.peers === 1 ? ' peer' : ' peers') + ', ' + s.handshakes + ' seen in the last 3 minutes';
}

/** Settings → Provisioning's generic scripts. */
export function batchRows(batches: ZTPBatchView[], now: number): string {
  if (!batches.length) return '<tr><td colspan="4" class="ztp-help">No generic scripts yet.</td></tr>';
  return batches.map((b) => '<tr><td>' + esc(b.name) + '</td><td>' +
    (b.revokedAt ? '<span class="vpn-hs-badge ztp-pill-off">Revoked</span>'
      : b.live ? '<span class="vpn-hs-badge ztp-pill-ok">Live</span> <span class="ztp-help">expires ' +
        esc(relTime(b.expiresAt, now)) + '</span>'
        : '<span class="vpn-hs-badge ztp-pill-off">Expired</span>') + '</td><td>' + b.devices + '</td><td>' +
    (b.live ? '<button class="sbtn sbtn-danger ztp-btn" type="button" data-ztp-revoke="' + esc(b.id) + '">Revoke</button>' : '') +
    '</td></tr>').join('');
}
