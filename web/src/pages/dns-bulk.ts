// Bulk edit and export of static DNS entries (Settings -> Features).
//
// ── ONE JOB, PACED BY THE WRITE LIMIT ───────────────────────────────────────
//
// Ticked rows are changed by one `/api/dns/bulk` job on the server, which sends
// `set` and `remove` with up to a hundred ids each and spends one of the
// router's thirty writes a minute on every command. When they run out the job
// waits for the next minute and the bar says so; see dnsbulk_api.go.
//
// `followJob` is shared: the fleet sync follows its jobs with it.

import { esc, el } from '../dom';
import { confirmAction } from '../confirm';
import { featureOn, onFeatures } from '../caps';
import type { DNSStaticEntry } from '../gen/payloads';

/** One job's state, as `GET /api/dns/bulk` answers it (`DNSBulkJob` in Go). */
export interface BulkJob {
  id: string;
  routerId: string;
  label: string;
  state: string;
  total: number;
  done: number;
  added: number;
  changed: number;
  removed: number;
  failed: number;
  unconfirmed: number;
  waitingUntil: number;
  errors: string[];
}

interface BulkRequest {
  routerId: string;
  label: string;
  set?: Array<{ ids: string[]; patch: Record<string, unknown> }>;
  remove?: string[];
}

/** What a finished job did, in one line. */
export function jobSummary(j: BulkJob): string {
  const bits: string[] = [];
  if (j.added) bits.push(j.added + ' added');
  if (j.changed) bits.push(j.changed + ' changed');
  if (j.removed) bits.push(j.removed + ' removed');
  if (j.failed) bits.push(j.failed + ' failed');
  if (j.unconfirmed) bits.push(j.unconfirmed + ' not confirmed by the read-back');
  if (j.state === 'stopped') bits.push('stopped');
  const errs = j.errors.length ? ': ' + j.errors.slice(0, 2).join(' · ') : '';
  return (bits.join(', ') || 'nothing to do') + errs;
}

/** A running job's progress, in one line. */
export function jobProgress(j: BulkJob, now = Date.now()): string {
  const wait = j.waitingUntil > now
    ? ', waiting ' + Math.ceil((j.waitingUntil - now) / 1000) + ' s for the write limit' : '';
  return j.done + ' of ' + j.total + ' commands' + wait;
}

/** Follow a job to the end: `onProgress` sees every poll. */
export function followJob(id: string, onProgress: (j: BulkJob) => void): Promise<BulkJob> {
  return new Promise<BulkJob>((resolve, reject) => {
    const poll = (): void => {
      fetch('/api/dns/bulk?id=' + encodeURIComponent(id), { credentials: 'same-origin' })
        .then((r) => (r.ok ? r.json() : Promise.reject(new Error('the job was lost'))))
        .then((j: BulkJob) => {
          onProgress(j);
          if (j.state === 'running') setTimeout(poll, 1000);
          else resolve(j);
        })
        .catch(reject);
    };
    setTimeout(poll, 500);
  });
}

function runBulkJob(body: BulkRequest, onProgress: (j: BulkJob) => void): Promise<BulkJob> {
  return fetch('/api/dns/bulk', {
    method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then((r) => r.json())
    .then((d) => {
      if (!d || d.ok !== true) {
        const errs = ((d && d.errors) || []) as string[];
        throw new Error(((d && (d.code || d.error)) || 'refused') +
          (errs.length ? ': ' + errs.slice(0, 3).join(' · ') : ''));
      }
      onProgress(d.job as BulkJob);
      return followJob(String(d.job.id), onProgress);
    });
}

// ── export ──────────────────────────────────────────────────────────────────

/** The RouterOS properties an export writes, in the form's order. */
const EXPORT_FIELDS: Array<[string, string]> = [
  ['name', 'name'], ['regexp', 'regexp'], ['type', 'type'], ['address', 'address'],
  ['cname', 'cname'], ['forwardTo', 'forward-to'], ['text', 'text'], ['mxExchange', 'mx-exchange'],
  ['mxPreference', 'mx-preference'], ['ns', 'ns'], ['srvTarget', 'srv-target'],
  ['srvPort', 'srv-port'], ['srvPriority', 'srv-priority'], ['srvWeight', 'srv-weight'],
  ['addressList', 'address-list'], ['ttl', 'ttl'], ['matchSubdomain', 'match-subdomain'],
  ['comment', 'comment'], ['disabled', 'disabled'],
];

const RSC_ESCAPE: Record<string, string> = { '\n': '\\n', '\r': '\\r', '\t': '\\t' };

/**
 * A value as RouterOS script writes it: bare when it can be, quoted otherwise.
 * A control character is written as its escape, `\n`, `\r`, `\t` or `\XX`:
 * left in, a line break ends the line the `add` is on, and what follows it runs
 * as a command of its own on `/import`.
 */
export function rscValue(v: string): string {
  if (/^[A-Za-z0-9._:/@+*-]+$/.test(v)) return v;
  return '"' + v.replace(/[\\"$?\x00-\x1f\x7f]/g, (c) => RSC_ESCAPE[c] ?? (c < ' ' || c === '\x7f'
    ? '\\' + c.charCodeAt(0).toString(16).toUpperCase().padStart(2, '0') : '\\' + c)) + '"';
}

/** One entry's `add` line, from the resource's own view of the row. */
export function rscLine(values: Record<string, unknown>): string {
  const words: string[] = [];
  const type = String(values.type || 'A');
  for (const [field, ros] of EXPORT_FIELDS) {
    let v = values[field];
    if (v === undefined || v === null || v === '') continue;
    if (field === 'ttl' && type === 'FWD') continue;
    if (typeof v === 'boolean') {
      // Only what differs from a new entry: enabled, and not matching subdomains.
      if (!v) continue;
      v = 'yes';
    }
    words.push(ros + '=' + rscValue(String(v)));
  }
  return 'add ' + words.join(' ');
}

/**
 * One CSV cell. A value a spreadsheet would read as a formula - `=`, `+`, `-`,
 * `@`, a tab or a carriage return first - gets a leading apostrophe, so a
 * comment or a TXT record cannot run in whoever opens the export.
 */
export function csvCell(v: unknown): string {
  let s = v === undefined || v === null ? '' : String(v);
  if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
  return /[",\n\r]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

function saveFile(name: string, text: string, type: string): void {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([text], { type }));
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

// ── the page's half ─────────────────────────────────────────────────────────

export interface DnsBulk {
  /** Whether rows draw a tick box. */
  on(): boolean;
  /** The table's rows as drawn, so ticks, select-all and export follow the search. */
  setRows(rows: DNSStaticEntry[]): void;
  headCell(): string;
  cell(e: DNSStaticEntry): string;
}

export function initDnsBulk(activeRouter: () => string, rerender: () => void): DnsBulk {
  const picked = new Set<string>();
  let shown: DNSStaticEntry[] = [];
  let busy = false;

  const on = (): boolean => featureOn('dnsBulkEnabled');
  const note = (text: string): void => { const n = el('dnsBulkNote'); if (n) n.textContent = text; };

  function syncBar(): void {
    const bar = el('dnsBulkBar');
    if (bar) bar.style.display = on() && picked.size ? '' : 'none';
    const exp = el('dnsExportTools');
    if (exp) exp.style.display = on() ? '' : 'none';
    const n = el('dnsBulkCount');
    if (n) n.textContent = picked.size + ' selected';
    el('dnsBulkBar')?.querySelectorAll<HTMLButtonElement>('button').forEach((b) => { b.disabled = busy; });
  }

  function chosen(): DNSStaticEntry[] {
    return shown.filter((e) => picked.has(e.id));
  }

  function submit(body: Omit<BulkRequest, 'routerId'>): void {
    const routerId = activeRouter();
    if (!routerId || busy) return;
    busy = true;
    syncBar();
    note('starting…');
    runBulkJob({ ...body, routerId }, (j) => note(jobProgress(j)))
      .then((j) => { note(jobSummary(j)); picked.clear(); })
      .catch((e: Error) => note('refused: ' + e.message))
      .then(() => { busy = false; syncBar(); rerender(); });
  }

  function patch(values: Record<string, unknown>, rows: DNSStaticEntry[], what: string): void {
    if (!rows.length) { note('none of the selected entries take that'); return; }
    submit({ label: what + ' on ' + rows.length + ' entries', set: [{ ids: rows.map((e) => e.id), patch: values }] });
  }

  el('dnsBulkApply')?.addEventListener('click', () => {
    const field = (el<HTMLSelectElement>('dnsBulkField')?.value || 'addressList');
    const value = (el<HTMLInputElement>('dnsBulkValue')?.value || '').trim();
    // forward-to belongs to FWD entries only and a TTL to every other type;
    // the rest of the selection is left out rather than refused by the router.
    const rows = chosen().filter((e) =>
      field === 'forwardTo' ? e.type === 'FWD' : field === 'ttl' ? e.type !== 'FWD' : true);
    patch({ [field]: value }, rows, field + (value ? ' = ' + value : ' cleared'));
  });
  el('dnsBulkEnable')?.addEventListener('click', () => patch({ disabled: false }, chosen(), 'enable'));
  el('dnsBulkDisable')?.addEventListener('click', () => patch({ disabled: true }, chosen(), 'disable'));
  el('dnsBulkRemove')?.addEventListener('click', async () => {
    const rows = chosen();
    if (!rows.length || !(await confirmAction({ title: 'Remove the DNS entries?',
      message: 'Remove ' + rows.length + ' static DNS entries from this router?',
      confirmLabel: 'Remove', danger: true }))) return;
    submit({ label: 'remove ' + rows.length + ' entries', remove: rows.map((e) => e.id) });
  });
  el('dnsBulkClear')?.addEventListener('click', () => { picked.clear(); syncBar(); rerender(); });

  // A tick box sits inside a row the resource engine opens on click; the click
  // stops here, at the table, before the document-level handler sees it.
  el('dnsStaticTable')?.addEventListener('click', (ev) => {
    const box = (ev.target as HTMLElement).closest?.('.dns-pick') as HTMLInputElement | null;
    if (!box) return;
    ev.stopPropagation();
    const id = box.getAttribute('data-pick') || '';
    if (box.checked) picked.add(id); else picked.delete(id);
    syncBar();
  });
  el('dnsStaticThead')?.addEventListener('click', (ev) => {
    const all = (ev.target as HTMLElement).closest?.('.dns-pick-all') as HTMLInputElement | null;
    if (!all) return;
    ev.stopPropagation();
    shown.forEach((e) => { if (!e.dynamic) { if (all.checked) picked.add(e.id); else picked.delete(e.id); } });
    syncBar();
    rerender();
  });

  function exportRows(kind: 'rsc' | 'csv'): void {
    const routerId = activeRouter();
    if (!routerId) return;
    const want = new Set((picked.size ? chosen() : shown).map((e) => e.id));
    note('reading the entries…');
    // The fleet read carries every property of every row; the page's own
    // payload folds nine of them into one value column.
    fetch('/api/dns/fleet?routers=' + encodeURIComponent(routerId), { credentials: 'same-origin' })
      .then((r) => r.json())
      .then((d) => {
        const router = ((d && d.routers) || [])[0];
        if (!router || !router.ok) throw new Error((router && router.error) || 'no answer');
        const rows = (router.entries as Array<{ id: string; values: Record<string, unknown> }>)
          .filter((e) => want.has(e.id));
        const stamp = new Date().toISOString().slice(0, 10);
        if (kind === 'rsc') {
          saveFile('dns-static-' + stamp + '.rsc',
            '/ip dns static\n' + rows.map((e) => rscLine(e.values)).join('\n') + '\n', 'text/plain');
        } else {
          const head = EXPORT_FIELDS.map(([, ros]) => ros);
          saveFile('dns-static-' + stamp + '.csv', head.join(',') + '\n' +
            rows.map((e) => EXPORT_FIELDS.map(([f]) => csvCell(e.values[f])).join(',')).join('\n') + '\n',
          'text/csv');
        }
        note(rows.length + ' entries exported');
      })
      .catch((e: Error) => note('export failed: ' + e.message));
  }
  el('dnsExportRsc')?.addEventListener('click', () => exportRows('rsc'));
  el('dnsExportCsv')?.addEventListener('click', () => exportRows('csv'));

  onFeatures(() => { if (!on()) picked.clear(); syncBar(); });
  syncBar();

  return {
    on,
    setRows(rows) {
      shown = rows;
      const ids = new Set(rows.map((e) => e.id));
      // A tick on a row that has left the table (removed, or searched away)
      // would act on something nobody can see.
      [...picked].forEach((id) => { if (!ids.has(id)) picked.delete(id); });
      syncBar();
    },
    headCell() {
      const n = shown.filter((e) => !e.dynamic).length;
      const all = n > 0 && shown.every((e) => e.dynamic || picked.has(e.id));
      return '<input type="checkbox" class="dns-pick-all" aria-label="Select every entry shown"' +
        (all ? ' checked' : '') + '>';
    },
    cell(e) {
      return '<td><input type="checkbox" class="dns-pick" data-pick="' + esc(e.id) + '"' +
        ' aria-label="Select ' + esc(e.name || e.regexp) + '"' +
        (picked.has(e.id) ? ' checked' : '') + (e.dynamic ? ' disabled' : '') + '></td>';
    },
  };
}
