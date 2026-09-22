// The Security Scan page's cards, as pure functions of a report: markup out,
// nothing read from the page. security-scan.ts puts them in place and animates
// them; web/test/security-scan.test.ts checks them from a report.
//
// ── EVERY VALUE IS ESCAPED ──────────────────────────────────────────────────
//
// A finding's detail names router objects (a user, a service, an SSID, a
// certificate), which are the router's text, so every one goes through esc().
// The catalogue's own words (title, why, fix) are ours, and escaped anyway.

import { esc } from '../dom';
import { t } from '../i18n';
import { scoreGrade } from './tools-ping-cards';
import type { Finding, Report, CategoryScore } from '../gen/payloads';

export const GRADE_WORD = { good: t('Good'), fair: t('Fair'), poor: t('At risk') } as const;

/** The score ring's circumference (r=52 in a 120 box): the page's ring and the
 *  Dashboard's Security Score card share it. */
export const RING_C = 2 * Math.PI * 52;

/** A report's age, as the page and the Dashboard card say it. */
export function ago(ms: number): string {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return t('scanned just now');
  if (s < 3600) return 'scanned ' + Math.round(s / 60) + ' min ago';
  return 'scanned ' + Math.round(s / 3600) + ' h ago';
}

/** The label under the score: its grade (the ping Score's thresholds) and how
 *  many issues. */
export function scoreLabel(r: Report): string {
  const issues = r.findings.filter((f) => f.status === 'fail' && f.severity !== 'info').length;
  return GRADE_WORD[scoreGrade(r.score)] + ' · ' + (issues === 1 ? '1 issue' : issues + ' issues');
}

/** A severity as a pill, by kind: the vpn-hs-badge colours, with high its own
 *  orange between critical's red and medium's amber. */
export function sevPill(sev: string): string {
  const kind = sev === 'critical' ? 'hs-stale' : sev === 'high' ? 'sec-sev-high'
    : sev === 'medium' ? 'hs-warn' : sev === 'low' ? 'hs-info' : 'hs-never';
  return '<span class="vpn-hs-badge ' + kind + '">' + esc(sev) + '</span>';
}

/** The page a finding is fixed on, as an Open link; nothing when it has none. */
export function openLink(f: Finding): string {
  return f.link ? '<a href="#" class="sec-open" data-goto="' + esc(f.link) + ('">' + t('Open ›') + '</a>') : '';
}

/** One category's bar: its score, pass/fail counts, and a grade colour. */
export function categoryRow(c: CategoryScore): string {
  const g = scoreGrade(c.score);
  return '<div class="sec-cat">' +
    '<div class="sec-cat-head"><span class="sec-cat-name">' + esc(c.name) + '</span>' +
    '<span class="sec-cat-score grade-' + g + '">' + c.score + '</span></div>' +
    '<div class="sec-bar"><div class="sec-bar-fill grade-' + g + '" style="width:' + c.score + '%"></div></div>' +
    '<div class="sec-cat-counts">' + c.fail + ' issue' + (c.fail === 1 ? '' : 's') + ' · ' + c.pass + ' ok' +
    (c.unknown ? ' · ' + c.unknown + ' unknown' : '') + '</div></div>';
}

/** The most urgent failed findings, most severe first (the report's order). */
export function topFindings(r: Report, n = 5): string {
  const fails = r.findings.filter((f) => f.status === 'fail' && f.severity !== 'info').slice(0, n);
  if (!fails.length) return '<div class="sec-empty">' + t('Nothing urgent: every weighted check passed.') + '</div>';
  return fails.map((f) => '<div class="sec-top-row sev-' + esc(f.severity) + '">' +
    '<span class="sec-top-dot"></span><div class="sec-top-text"><div class="sec-top-title">' + esc(f.title) + '</div>' +
    '<div class="sec-top-detail">' + esc(f.detail.slice(0, 3).join(', ')) + (f.detail.length > 3 ? ' …' : '') + '</div></div>' +
    sevPill(f.severity) + openLink(f) + '</div>').join('');
}

const SHORT: Record<string, string> = {
  'mgmt.mac-telnet': t('MAC Telnet limited'),
  'mgmt.mac-winbox': t('MAC WinBox limited'),
  'mgmt.discovery': t('Discovery kept off the WAN'),
  'mgmt.romon': t('RoMON off'),
  'fw.input-default-drop': t('Input ends in a drop'),
  'fw.input-established': t('Established accepted'),
  'fw.input-invalid': t('Invalid dropped on input'),
  'fw.forward-wan': t('New WAN traffic dropped'),
  'fw.forward-invalid': t('Invalid dropped on forward'),
  'fw.dns-open-resolver': t('DNS not open to the WAN'),
  'fw.wan-in-lan': t('WAN and LAN lists apart'),
  'fw.ipv6-input': t('IPv6 input covered'),
};

/** A check as a ticked, crossed or unknown line. */
export function checkLine(f: Finding, label: string): string {
  const mark = f.status === 'pass' ? '✓' : f.status === 'fail' ? '✕' : '?';
  return '<div class="sec-check st-' + esc(f.status) + '" title="' + esc(f.title) + '"><span class="sec-check-mark">' +
    mark + '</span>' + esc(label) + '</div>';
}

function checks(r: Report, ids: string[]): string {
  return ids.map((id) => r.findings.find((f) => f.id === id)).filter((f): f is Finding => !!f)
    .map((f) => checkLine(f, SHORT[f.id] || f.title)).join('');
}

/** The management services, green when restricted to addresses, red when
 *  plaintext, amber when open to any address, muted when disabled; then the
 *  layer-2 ways in. */
export function surfaceCard(r: Report): string {
  const svc = r.facts.services;
  const pills = svc.length ? svc.map((s) => {
    const kind = !s.enabled ? 'hs-never' : s.plaintext ? 'hs-stale' : s.restricted ? 'hs-ok' : 'hs-warn';
    const tip = !s.enabled ? 'disabled' : s.plaintext ? 'plaintext' : s.restricted ? 'restricted' : t('open to any address');
    return '<span class="vpn-hs-badge ' + kind + ' sec-svc" title="' + esc(s.name + ': ' + tip) + '">' + esc(s.name) +
      (s.port ? ' <small>' + esc(s.port) + '</small>' : '') + '</span>';
  }).join('') : '<div class="sec-empty">' + t('The service list could not be read.') + '</div>';
  return '<div class="sec-svc-row">' + pills + '</div>' +
    '<div class="sec-mini-list">' + checks(r, ['mgmt.mac-telnet', 'mgmt.mac-winbox', 'mgmt.discovery', 'mgmt.romon']) + '</div>';
}

/** The firewall baseline as a checklist. */
export function firewallCard(r: Report): string {
  return checks(r, ['fw.input-default-drop', 'fw.input-established', 'fw.input-invalid', 'fw.forward-wan',
    'fw.forward-invalid', 'fw.dns-open-resolver', 'fw.wan-in-lan', 'fw.ipv6-input']);
}

function fact(label: string, value: string, bad = false): string {
  return '<div class="sec-fact"><span>' + esc(label) + '</span><b' + (bad ? ' class="sec-bad"' : '') + '>' +
    esc(value) + '</b></div>';
}

export function accountsCard(r: Report): string {
  const f = r.facts;
  const n = Number(f.minPasswordLen);
  const len = f.minPasswordLen === '' ? 'unknown' : n === 0 ? 'none' : n + ' characters';
  return fact('Accounts enabled', String(f.users)) +
    fact('With full rights', String(f.fullUsers), f.fullUsers > 2) +
    fact('Minimum password length', len, f.minPasswordLen !== '' && n < 8) +
    fact('Default admin', f.adminEnabled ? 'enabled' : 'disabled', f.adminEnabled);
}

export function updatesCard(r: Report): string {
  const f = r.facts;
  const behind = r.findings.some((x) => x.id === 'sys.update' && x.status === 'fail');
  const fwBehind = r.findings.some((x) => x.id === 'sys.firmware' && x.status === 'fail');
  return fact('RouterOS installed', f.installed || 'unknown') +
    fact('Latest', f.latest || 'not checked', behind) +
    fact('Firmware', f.firmwareCurrent ? f.firmwareCurrent + (fwBehind ? ' → ' + f.firmwareUpgrade : '') : t('no RouterBOARD'), fwBehind);
}

export function coverageCard(r: Report): string {
  const failed = Object.values(r.failed ?? {}).reduce((a, b) => a + b, 0);
  const unknown = r.findings.filter((f) => f.status === 'unknown');
  return fact('Checks run', String(r.findings.length)) + fact('Passed', String(r.passed)) +
    fact('Issues', String(failed), failed > 0) + fact('Could not be answered', String(unknown.length)) +
    (unknown.length ? '<div class="sec-unknown">' + unknown.map((f) => esc(f.title)).join('<br>') + '</div>' : '');
}

/** One findings-table row. */
export function findingRow(f: Finding): string {
  return '<tr class="sec-row">' +
    '<td>' + sevPill(f.severity) + '</td>' +
    '<td><div class="sec-finding-title">' + esc(f.title) + '</div>' +
    (f.detail.length ? '<div class="sec-finding-detail">' + esc(f.detail.join(', ')) + '</div>' : '') +
    '<div class="sec-finding-why">' + esc(f.why) + '</div></td>' +
    '<td><span class="vpn-hs-badge hs-info">' + esc(f.category) + '</span></td>' +
    '<td class="sec-fix">' + esc(f.fix) + '</td>' +
    '<td>' + openLink(f) + '</td></tr>';
}

/** One passed-check row. */
export function passedRow(f: Finding): string {
  return '<tr><td>' + esc(f.title) + '</td><td><span class="vpn-hs-badge hs-info">' + esc(f.category) +
    '</span></td><td>' + sevPill(f.severity) + '</td></tr>';
}

const SEV_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

/** A finding's sort value: severity sorts by rank, not alphabetically. */
export function sortValue(f: Finding, col: string): string | number {
  if (col === 'severity') return SEV_RANK[f.severity] ?? 9;
  if (col === 'category') return f.category;
  return f.title;
}
