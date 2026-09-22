// The Deploy tab's markup, as pure functions: the router picker, the settings
// grid, each router's preview, and the rollout's tiles.

import { esc } from '../dom';
import { t } from '../i18n';
import type { CfgDeployPayload, CfgDeployTarget } from '../gen/payloads';
import { findingRow, highlight } from './config-management-cards';
import type { VarDef } from './config-management-editor';

export interface RouterOpt { id: string; label: string }

export interface Finding { level: string; code: string; line?: number; message: string }

/** What one router's preview answered (POST …/preview). */
export interface RouterPreview {
  routerId: string;
  error?: string;
  fields?: Record<string, string>;
  hash?: string;
  rendered?: string;
  findings?: Finding[];
  lockClass?: boolean;
  target?: { board: string; serial: string; osVersion: string };
}

/** A finding's key, as the server's cfgdeploy.FindingKey writes it. */
export const findingKey = (f: Finding): string => f.code + '@' + (f.line ?? 0);

/** The router checklist; the order of picking decides the canary. */
export function routerPicker(routers: RouterOpt[], picked: string[]): string {
  if (!routers.length) return '<div class="cfg-meta">' + t('No routers are available.') + '</div>';
  return ('<div class="cfg-pick-bar"><button class="cfg-btn" type="button" data-dep-all="1">' + t('All routers') + '</button>') +
    ('<button class="cfg-btn" type="button" data-dep-all="0">' + t('None') + '</button>') +
    ('<span class="cfg-meta">' + t('The first you pick goes first, as the canary; the rest wait for your OK.') + '</span></div>') +
    '<div class="cfg-pick">' + routers.map((r) => {
      const at = picked.indexOf(r.id);
      return '<label class="cfg-pick-item' + (at >= 0 ? ' is-on' : '') + '"><input type="checkbox" data-dep-router="' +
        esc(r.id) + '"' + (at >= 0 ? ' checked' : '') + '><span class="cfg-pick-name">' + esc(r.label) + '</span>' +
        (at === 0 ? '<span class="vpn-hs-badge cfg-pill-canary">' + t('Canary') + '</span>'
          : at > 0 ? '<span class="cfg-meta">#' + (at + 1) + '</span>' : '') +
        '</label>';
    }).join('') + '</div>';
}

/** A fresh password for a secret setting: 20 letters and digits from the
 *  browser's CSPRNG. A secret has no default on purpose (one would be the
 *  same on every install), so the Deploy tab fills it with one of these. */
export function newSecret(): string {
  const abc = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
  const out: string[] = [];
  // Rejection sampling: only bytes below the largest multiple of the alphabet
  // are used, so no letter is likelier than another.
  const limit = 256 - (256 % abc.length);
  while (out.length < 20) {
    for (const b of crypto.getRandomValues(new Uint8Array(32))) {
      if (b < limit && out.length < 20) out.push(abc[b % abc.length] as string);
    }
  }
  return out.join('');
}

/** The template's settings with these values as their defaults: what "Save
 *  these settings as the template's defaults" stores. A secret keeps no
 *  default, since it would be stored with the template. */
export function defaultsFromValues(defs: VarDef[], values: Record<string, string>): VarDef[] {
  return defs.map((d) => {
    if (d.type === 'secret') return { ...d, default: undefined };
    const v = (values[d.name] ?? '').trim();
    return { ...d, default: v || undefined };
  });
}

/** The settings grid: a row per router, a column per setting, and a first row
 *  that fills them all. Passwords are hidden unless `reveal`. */
export function valuesGrid(defs: VarDef[], routers: RouterOpt[], values: Record<string, Record<string, string>>,
  reveal = false): string {
  if (!defs.length) return '<div class="cfg-meta">' + t('This template asks for no settings.') + '</div>';
  if (!routers.length) return '<div class="cfg-meta">' + t('Pick routers first.') + '</div>';
  const kind = (d: VarDef): string => (d.type === 'secret' && !reveal ? 'password' : 'text');
  const secrets = defs.some((d) => d.type === 'secret');
  const cell = (rid: string, d: VarDef): string => {
    const v = values[rid]?.[d.name] ?? (d.type === 'secret' ? '' : d.default ?? '');
    return '<td><input class="form-control form-control-sm" data-dep-val="' + esc(rid) + '" data-dep-var="' +
      esc(d.name) + '" type="' + kind(d) + '" value="' + esc(v) + '" placeholder="' + esc(d.type) + '" autocomplete="off"></td>';
  };
  return (secrets ? '<label class="cfg-reveal"><input type="checkbox" data-dep-reveal' + (reveal ? ' checked' : '') +
    '> ' + t('Show passwords') + ' <span class="cfg-meta">' + t('(each was generated for this deploy; change it, or note it for whatever will use it)') + '</span></label>' : '') +
    ('<div class="cfg-scroll"><table class="table table-sm cfg-grid-vals"><thead><tr><th>' + t('Router') + '</th>') +
    defs.map((d) => '<th><span class="cfg-var">{{' + esc(d.name) + '}}</span>' +
      (d.label ? '<div class="cfg-meta">' + esc(d.label) + '</div>' : '') + '</th>').join('') +
    ('</tr></thead><tbody><tr class="cfg-grid-all"><td>' + t('All routers') + '</td>') +
    defs.map((d) => '<td><input class="form-control form-control-sm" data-dep-all-var="' + esc(d.name) + '" type="' +
      kind(d) + '" placeholder="fill every row" autocomplete="off"></td>').join('') +
    '</tr>' + routers.map((r) => '<tr><td>' + esc(r.label) + '</td>' + defs.map((d) => cell(r.id, d)).join('') + '</tr>').join('') +
    '</tbody></table></div>';
}

/** One router's preview: its identity, the checks (an OK box for each that
 *  needs one), and the text it would receive. */
export function previewCard(label: string, p: RouterPreview, acked: Set<string>): string {
  if (p.error) {
    const fields = p.fields ? Object.entries(p.fields).map(([k, v]) => '<li><span class="cfg-var">{{' + esc(k) + '}}</span> ' +
      esc(v) + '</li>').join('') : '';
    return '<div class="cfg-prev is-bad"><div class="cfg-prev-head"><strong>' + esc(label) + '</strong>' +
      ('<span class="vpn-hs-badge cfg-pill-refuse">' + t('Cannot deploy') + '</span></div><div class="cfg-prev-msg">') + esc(p.error) +
      '</div>' + (fields ? '<ul class="cfg-prev-fields">' + fields + '</ul>' : '') + '</div>';
  }
  const findings = p.findings ?? [];
  const refused = findings.some((f) => f.level === 'refuse');
  const checks = findings.map((f) => {
    if (f.level !== 'ack') return findingRow(f);
    const key = p.routerId + '|' + findingKey(f);
    return '<li class="cfg-finding cfg-lvl-ack"><label class="cfg-ack"><input type="checkbox" data-dep-ack="' +
      esc(p.routerId) + '" data-key="' + esc(findingKey(f)) + '"' + (acked.has(key) ? ' checked' : '') + '> OK</label>' +
      '<span class="cfg-meta">line ' + (f.line ?? 0) + '</span><span class="cfg-finding-msg">' + esc(f.message) + '</span></li>';
  }).join('');
  const tgt = p.target;
  return '<div class="cfg-prev' + (refused ? ' is-bad' : '') + '"><div class="cfg-prev-head"><strong>' + esc(label) + '</strong>' +
    (tgt ? '<span class="cfg-meta">' + esc(tgt.board) + ' · RouterOS ' + esc(tgt.osVersion) + '</span>' : '') +
    (p.lockClass ? '<span class="vpn-hs-badge cfg-pill-lock">' + t('Auto-revert armed') + '</span>' : '') +
    (refused ? '<span class="vpn-hs-badge cfg-pill-refuse">' + t('Refused') + '</span>' : '') + '</div>' +
    (findings.length ? '<ul class="cfg-findings">' + checks + '</ul>' : '<div class="cfg-meta">' + t('Nothing to flag.') + '</div>') +
    ('<details class="cfg-prev-text"><summary>' + t('What this router will receive') + '</summary><pre class="cfg-code md-ros">') +
    highlight(p.rendered ?? '') + '</pre></details></div>';
}

/** Why the deploy may not start yet, or '' when it may: every router previewed,
 *  nothing refused, every acknowledgement ticked. */
export function readyToStart(ids: string[], previews: Record<string, RouterPreview>, acked: Set<string>): string {
  if (!ids.length) return t('Pick at least one router');
  for (const id of ids) {
    const p = previews[id];
    if (!p || !p.hash) return p?.error ? t('A router cannot take this template') : t('Preview every router first');
    for (const f of p.findings ?? []) {
      if (f.level === 'refuse') return t('A check refuses this template on a router');
      if (f.level === 'ack' && !acked.has(id + '|' + findingKey(f))) return t('Tick OK on every check that needs it');
    }
  }
  return '';
}

const TILE: Record<string, { cls: string; word: string }> = {
  pending: { cls: 'is-queued', word: t('Waiting') },
  applying: { cls: 'is-active', word: t('Working') },
  applied: { cls: 'is-ok', word: t('Applied') },
  failed: { cls: 'is-bad', word: t('Failed') },
  'failed-partial': { cls: 'is-part', word: t('Partly applied') },
  unknown: { cls: 'is-part', word: t('Unknown') },
  'preflight-failed': { cls: 'is-bad', word: t('Not started') },
  'not-attempted': { cls: 'is-skip', word: t('Skipped') },
};

const STEP: Record<string, string> = {
  sweep: t('Tidying old files'), backup: t('Taking a restore point'), upload: t('Uploading'), 'dry-run': t('Checking syntax on the router'),
  recheck: t('Checking nothing moved'), arm: t('Arming the auto-revert'), import: t('Applying'), reconnect: t('Logging in afresh'),
  disarm: t('Disarming the auto-revert'), baseline: t('Recording the baseline'), reverting: t('Waiting for the revert'),
  reset: t('Resetting'), reboot: t('Waiting for the reboot'),
};

/** One router in the rollout. */
export function deployTile(tgt: CfgDeployTarget): string {
  const k = TILE[tgt.state] ?? { cls: 'is-queued', word: tgt.state };
  const detail = tgt.state === 'applying' ? STEP[tgt.step] ?? tgt.step
    : tgt.message || (tgt.state === 'applied' && tgt.reconnectMs ? t('Back in {s} s', { s: (tgt.reconnectMs / 1000).toFixed(1) }) : '');
  return '<div class="cfg-tile ' + k.cls + '"><div class="cfg-tile-ring"></div><div class="cfg-tile-body">' +
    '<div class="cfg-tile-name">' + esc(tgt.label) + (tgt.canary ? '<span class="vpn-hs-badge cfg-pill-canary">' + t('Canary') + '</span>' : '') +
    '</div><div class="cfg-tile-state">' + esc(k.word) + (tgt.reverted ? ' · reverted' : '') + '</div>' +
    (detail ? '<div class="cfg-tile-detail">' + esc(detail) + '</div>' : '') +
    (tgt.backupId ? '<div class="cfg-meta">' + t('Restore point #{id}', { id: tgt.backupId }) + '</div>' : '') + '</div></div>';
}

const RUN_WORD: Record<string, string> = {
  canary: t('Deploying to the canary'), 'awaiting-canary': t('The canary is done: your call'), rolling: t('Rolling out'),
  done: t('Deployed'), halted: t('Stopped'), cancelled: t('Cancelled'), interrupted: t('Interrupted by a restart'),
};

/** The rollout: a headline, the decision the canary needs, and the tiles. */
export function rolloutView(p: CfgDeployPayload): string {
  if (!p.runId) return '';
  const live = p.state === 'canary' || p.state === 'rolling' || p.state === 'awaiting-canary';
  const ask = p.state === 'awaiting-canary'
    ? '<div class="cfg-decide"><span>' + t('Check the canary, then type <strong>{n}</strong> to deploy to every router.', { n: p.targets.length }) +
      '</span><input class="form-control form-control-sm" id="cfgDepCount" ' +
      'autocomplete="off" placeholder="' + p.targets.length + '"><button class="cfg-btn cfg-btn-go" type="button" ' +
      ('id="cfgDepContinue">' + t('Continue') + '</button></div>')
    : '';
  const reset = p.kind === 'full-export'
    ? '<div class="cfg-banner is-warn">' + t('A full replacement resets each router and reboots it; MikroDash cannot see it until it returns.') + '</div>'
    : '';
  return '<div class="cfg-roll-head state-' + esc(p.state) + '"><div><div class="cfg-roll-title">' +
    esc(RUN_WORD[p.state] ?? p.state) + '</div><div class="cfg-meta">' + esc(p.templateName) + ' · ' + t('started by {user}', { user: esc(p.startedBy) }) + '</div></div>' + (live ? '<button class="cfg-btn" type="button" id="cfgDepCancel">' + t('Cancel') + '</button>' : '') +
    '</div>' + (p.error ? '<div class="cfg-banner is-bad">' + esc(p.error) + '</div>' : '') + reset + ask +
    '<div class="cfg-tiles">' + p.targets.map(deployTile).join('') + '</div>';
}
