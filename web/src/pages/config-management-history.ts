// The History and Drift tabs' markup, as pure functions: rows in, HTML out.

import { esc, type SortCol } from '../dom';
import { hunksHTML } from '../diffview';
import type { Hunk } from '../gen/payloads';
import { fmtTs } from '../timefmt';
import { highlight } from './config-management-cards';

/** One run as GET /api/config/runs lists it. */
export interface RunRow {
  id: string;
  templateId: string | null;
  templateName: string;
  revision: number;
  method: string;
  state: string;
  startedBy: string;
  createdAt: number;
  finishedAt: number | null;
  error: string | null;
  /** Routers by how each ended: state → count. */
  routers: Record<string, number>;
}

/** One router of a run, as GET /api/config/runs/{id} gives it. */
export interface RunTarget {
  routerId: string;
  label: string;
  state: string;
  step: string | null;
  backupId: number | null;
  dryRunOutput: string | null;
  importOutput: string | null;
  failedLine: number | null;
  reconnectMs: number | null;
  warning: string | null;
  error: string | null;
}

export interface RunDetail {
  run: { bodyMasked: string };
  startedBy: string;
  targets: RunTarget[];
}

/** One baseline as GET /api/config/drift lists it. */
export interface DriftRow {
  templateId: string;
  templateName: string;
  routerId: string;
  routerLabel: string;
  takenAt: number;
}

/** What a check of one baseline found, or is doing. */
export type DriftCheck =
  | { state: 'checking' }
  /** `accept` when it was the accept that failed, not the check. */
  | { state: 'error'; message: string; accept?: boolean }
  | { state: 'done'; drifted: boolean; fingerprint: string; checkedAt: number; hunks: Hunk[]; truncated: boolean };

/** The key a baseline is held under: a template on a router. */
export const driftKey = (r: { templateId: string; routerId: string }): string => r.templateId + '|' + r.routerId;

const RUN_STATE: Record<string, { word: string; cls: string }> = {
  done: { word: 'Deployed', cls: 'cfg-run-ok' },
  halted: { word: 'Stopped', cls: 'cfg-run-bad' },
  cancelled: { word: 'Cancelled', cls: 'cfg-run-muted' },
  interrupted: { word: 'Interrupted', cls: 'cfg-run-warn' },
  expired: { word: 'Expired', cls: 'cfg-run-muted' },
  canary: { word: 'On the canary', cls: 'cfg-run-live' },
  'awaiting-canary': { word: 'Awaiting your OK', cls: 'cfg-run-live' },
  rolling: { word: 'Rolling out', cls: 'cfg-run-live' },
};

const TARGET_STATE: Record<string, { word: string; cls: string }> = {
  applied: { word: 'Applied', cls: 'cfg-run-ok' },
  failed: { word: 'Failed', cls: 'cfg-run-bad' },
  'failed-partial': { word: 'Partly applied', cls: 'cfg-run-warn' },
  unknown: { word: 'Unknown', cls: 'cfg-run-warn' },
  'preflight-failed': { word: 'Not started', cls: 'cfg-run-bad' },
  'not-attempted': { word: 'Skipped', cls: 'cfg-run-muted' },
  pending: { word: 'Waiting', cls: 'cfg-run-muted' },
  applying: { word: 'Working', cls: 'cfg-run-live' },
};

function pill(map: Record<string, { word: string; cls: string }>, state: string): string {
  const k = map[state] ?? { word: state, cls: 'cfg-run-muted' };
  return '<span class="vpn-hs-badge ' + k.cls + '">' + esc(k.word) + '</span>';
}

const METHOD: Record<string, string> = {
  additions: 'Addition', 'full-export': 'Full replacement', 'full-binary': 'Binary clone',
};

export const HISTORY_COLS: SortCol[] = [
  { key: 'createdAt', label: 'Started' },
  { key: 'templateName', label: 'Template' },
  { key: 'method', label: 'Method' },
  { key: 'state', label: 'Outcome' },
  { key: 'total', label: 'Routers' },
  { key: 'startedBy', label: 'By' },
];

/** A run with the fields its sortable columns read. */
export type SortableRun = RunRow & { total: number };

export const sortable = (r: RunRow): SortableRun =>
  ({ ...r, total: Object.values(r.routers).reduce((a, b) => a + b, 0) });

/** The routers cell: how many applied, of how many. */
function routersCell(r: SortableRun): string {
  const ok = r.routers.applied ?? 0;
  const bad = r.total - ok - (r.routers['not-attempted'] ?? 0);
  return '<span class="cfg-hist-n">' + ok + '/' + r.total + '</span>' +
    (bad > 0 ? ' <span class="cfg-meta">' + bad + ' not applied</span>' : '');
}

/** The History table's body; the open run is followed by its detail row. */
export function historyRows(rows: SortableRun[], open: string, detail: RunDetail | null): string {
  return rows.map((r) => {
    const tr = '<tr class="cfg-hist-row' + (r.id === open ? ' is-open' : '') + '" data-run="' + esc(r.id) + '">' +
      '<td>' + esc(fmtTs(r.createdAt, false)) + '</td>' +
      '<td><strong>' + esc(r.templateName) + '</strong>' +
      (r.revision ? ' <span class="cfg-meta">rev ' + r.revision + '</span>' : '') + '</td>' +
      '<td>' + esc(METHOD[r.method] ?? r.method) + '</td>' +
      '<td>' + pill(RUN_STATE, r.state) + '</td>' +
      '<td>' + routersCell(r) + '</td>' +
      '<td>' + esc(r.startedBy) + '</td></tr>';
    if (r.id !== open) return tr;
    const inner = detail ? runDetail(r, detail) : '<div class="cfg-meta">Reading the run…</div>';
    return tr + '<tr class="cfg-hist-open"><td colspan="' + HISTORY_COLS.length + '">' + inner + '</td></tr>';
  }).join('');
}

/** A router's report from RouterOS, when it gave one. */
function output(title: string, text: string | null, failedLine: number | null): string {
  if (!text) return '';
  const lines = text.split('\n').map((l, i) => '<span class="cfg-out-line' +
    (failedLine && i + 1 === failedLine ? ' is-bad' : '') + '">' + esc(l) + '</span>').join('\n');
  return '<details class="cfg-prev-text"><summary>' + esc(title) + '</summary><pre class="cfg-code cfg-out">' +
    lines + '</pre></details>';
}

/** One run opened: each router and what it said, then what was sent. */
export function runDetail(r: RunRow, d: RunDetail): string {
  const targets = d.targets.map((t) => {
    const facts = [
      t.backupId ? 'Restore point #' + t.backupId + ' (Backups page)' : '',
      t.reconnectMs ? 'back in ' + (t.reconnectMs / 1000).toFixed(1) + ' s' : '',
      t.failedLine ? 'stopped at line ' + t.failedLine : '',
      t.state === 'applying' && t.step ? 'step: ' + t.step : '',
    ].filter(Boolean);
    return '<div class="cfg-hist-target"><div class="cfg-prev-head"><strong>' + esc(t.label) + '</strong>' +
      pill(TARGET_STATE, t.state) + (facts.length ? '<span class="cfg-meta">' + esc(facts.join(' · ')) + '</span>' : '') +
      '</div>' + (t.error ? '<div class="cfg-banner is-bad">' + esc(t.error) + '</div>' : '') +
      (t.warning ? '<div class="cfg-banner is-warn">' + esc(t.warning) + '</div>' : '') +
      output('The syntax check, in RouterOS\'s words', t.dryRunOutput, null) +
      output('The import, in RouterOS\'s words', t.importOutput, t.failedLine) + '</div>';
  }).join('');
  // The run's error is usually one router's, named ("CHR Test: …"): shown
  // with that router already, it is not said twice.
  const own = !!r.error && !d.targets.some((t) => t.error && r.error?.endsWith(t.error));
  return '<div class="cfg-hist-detail">' +
    (own ? '<div class="cfg-banner is-bad">' + esc(r.error) + '</div>' : '') +
    (targets || '<div class="cfg-meta">No routers were recorded for this run.</div>') +
    '<details class="cfg-prev-text"><summary>What was sent (settings as placeholders)</summary>' +
    '<pre class="cfg-code md-ros">' + highlight(d.run.bodyMasked) + '</pre></details></div>';
}

export const DRIFT_COLS: SortCol[] = [
  { key: 'templateName', label: 'Template' },
  { key: 'routerLabel', label: 'Router' },
  { key: 'takenAt', label: 'Baseline' },
  { label: 'Status' },
  { label: '' },
];

function driftStatus(c: DriftCheck | undefined): string {
  if (!c) return '<span class="vpn-hs-badge cfg-run-muted">Not checked</span>';
  if (c.state === 'checking') return '<span class="vpn-hs-badge cfg-run-live">Reading the router…</span>';
  if (c.state === 'error') {
    return '<span class="vpn-hs-badge cfg-run-bad">' + (c.accept ? 'Not accepted' : 'Could not check') + '</span>';
  }
  return c.drifted ? '<span class="vpn-hs-badge cfg-run-warn">Drifted</span>'
    : '<span class="vpn-hs-badge cfg-run-ok">As deployed</span>';
}

/** The Drift table's body; a checked baseline that drifted can show its diff. */
export function driftRows(rows: DriftRow[], checks: Record<string, DriftCheck>, open: string): string {
  return rows.map((r) => {
    const key = driftKey(r);
    const c = checks[key];
    const busy = c?.state === 'checking';
    const drifted = c?.state === 'done' && c.drifted;
    const acts = '<button class="cfg-btn" type="button" data-drift-act="check"' + (busy ? ' disabled' : '') + '>' +
      (c ? 'Check again' : 'Check') + '</button>' +
      (drifted ? '<button class="cfg-btn" type="button" data-drift-act="diff">' +
        (open === key ? 'Hide changes' : 'Show changes') + '</button>' : '');
    const tr = '<tr data-drift="' + esc(key) + '"><td><strong>' + esc(r.templateName) + '</strong></td>' +
      '<td>' + esc(r.routerLabel) + '</td><td>' + esc(fmtTs(r.takenAt, false)) + '</td>' +
      '<td>' + driftStatus(c) + (c?.state === 'done' ? ' <span class="cfg-meta">' +
        esc(fmtTs(c.checkedAt, false)) + '</span>' : '') + '</td>' +
      '<td class="cfg-drift-acts">' + acts + '</td></tr>';
    if (c?.state === 'error') {
      return tr + '<tr class="cfg-hist-open"><td colspan="' + DRIFT_COLS.length + '"><div class="cfg-banner is-bad">' +
        esc(c.message) + '</div></td></tr>';
    }
    if (c?.state !== 'done' || !c.drifted || open !== key) return tr;
    return tr + '<tr class="cfg-hist-open" data-drift="' + esc(key) + '"><td colspan="' + DRIFT_COLS.length + '">' +
      driftDiff(c.hunks, c.truncated) + '</td></tr>';
  }).join('');
}

/** A drifted baseline: what changed since the deploy, and the two ways on. */
export function driftDiff(hunks: Hunk[], truncated: boolean): string {
  return '<div class="cfg-hist-detail"><p class="cfg-meta">Lines marked − were there after the deploy and are ' +
    'gone; lines marked + are on the router now and were not.</p>' +
    (truncated ? '<div class="cfg-banner is-warn">The difference is too large to show in full.</div>' : '') +
    '<div class="bk-diff cfg-drift-diff">' + hunksHTML(hunks) + '</div>' +
    '<div class="cfg-drift-go"><button class="cfg-btn cfg-btn-go" type="button" data-drift-act="reapply">Re-apply ' +
    'the template</button><button class="cfg-btn" type="button" data-drift-act="accept">Accept as the new baseline' +
    '</button></div></div>';
}
