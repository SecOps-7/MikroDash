/**
 * Settings -> Devices -> Import: add many devices from a CSV (issue #150).
 *
 * ── THE BROWSER ONLY READS THE FILE ─────────────────────────────────────────
 *
 * It splits the CSV into cells and maps headers to fields, and that is all.
 * Whether a row is ready, a duplicate or wrong is the server's verdict
 * (`internal/fleetimport`), asked twice: once for the preview and again, from
 * scratch, when Import is pressed. A browser-side check would be a second
 * validator to drift from the one that decides.
 *
 * ── EXCEL'S OTHER SEPARATOR ─────────────────────────────────────────────────
 *
 * Excel writes `;` instead of `,` wherever the decimal mark is a comma, which is
 * most of Europe. The header line decides: whichever of the two appears more
 * often outside quotes is the separator.
 */

import { el, esc } from '../dom';

/** One row as sent: every field the cell's text. */
export interface ImportRow {
  line: number;
  host: string;
  name: string;
  port: string;
  tls: string;
  tlsInsecure: string;
  username: string;
  password: string;
  credentialProfile: string;
  sites: string;
  uplinkInterface: string;
  pingTarget: string;
}

type Field = Exclude<keyof ImportRow, 'line'>;

/** Header spellings, lower-cased with spaces and dashes as underscores. */
const HEADER_FIELDS: Record<string, Field> = {
  host: 'host', address: 'host', ip: 'host',
  name: 'name', label: 'name',
  port: 'port',
  tls: 'tls',
  tls_insecure: 'tlsInsecure', allow_self_signed: 'tlsInsecure',
  username: 'username', user: 'username',
  password: 'password',
  credential_profile: 'credentialProfile', profile: 'credentialProfile',
  sites: 'sites', site: 'sites',
  uplink_interface: 'uplinkInterface', uplink: 'uplinkInterface', uplink_port: 'uplinkInterface',
  default_interface: 'uplinkInterface',
  ping_target: 'pingTarget',
};

export const TEMPLATE_HEADERS = ['host', 'name', 'port', 'tls', 'tls_insecure', 'username', 'password',
  'credential_profile', 'sites', 'uplink_interface', 'ping_target'];

/** The downloadable template: the header and two example rows to replace. */
export const TEMPLATE = TEMPLATE_HEADERS.join(',') + '\r\n'
  + '192.0.2.10,Branch office,8729,yes,yes,admin,change-me,,Branch|North,ether1,1.1.1.1\r\n'
  + '192.0.2.11,Warehouse,,,,,,MikroDash login,Warehouse,,\r\n';

/** The separator the header line uses: `;` if it has more of those than commas. */
export function detectDelimiter(text: string): string {
  let commas = 0;
  let semis = 0;
  let quoted = false;
  for (const ch of text) {
    if (ch === '"') quoted = !quoted;
    else if (!quoted && (ch === '\n' || ch === '\r')) break;
    else if (!quoted && ch === ',') commas++;
    else if (!quoted && ch === ';') semis++;
  }
  return semis > commas ? ';' : ',';
}

/**
 * RFC 4180: quoted cells may hold the separator, line breaks and doubled quotes.
 * A byte-order mark is dropped; CRLF and LF both end a record.
 */
export function parseCsv(input: string, delimiter = detectDelimiter(input.replace(/^﻿/, ''))): string[][] {
  const text = input.replace(/^﻿/, '');
  const out: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') { cell += '"'; i++; } else quoted = false;
      } else cell += ch;
    } else if (ch === '"') quoted = true;
    else if (ch === delimiter) { row.push(cell); cell = ''; }
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && text[i + 1] === '\n') i++;
      row.push(cell);
      out.push(row);
      row = [];
      cell = '';
    } else cell += ch;
  }
  if (cell !== '' || row.length) {
    row.push(cell);
    out.push(row);
  }
  return out;
}

export interface ReadResult {
  rows: ImportRow[];
  /** Header cells that name no field, so the operator knows they were ignored. */
  ignored: string[];
  error: string;
}

const headerKey = (h: string): string => h.trim().toLowerCase().replace(/[\s-]+/g, '_');

/** The CSV's rows as import rows. Blank lines are skipped; `line` is the row's place in the file. */
export function readRows(text: string): ReadResult {
  const table = parseCsv(text);
  if (!table.length) return { rows: [], ignored: [], error: 'The file is empty.' };
  const header = table[0]!.map(headerKey);
  const fields = header.map((h) => HEADER_FIELDS[h]);
  if (!fields.includes('host')) {
    return { rows: [], ignored: [], error: 'The first line must be the header, with a "host" column. '
      + 'Download the template to start from one.' };
  }
  const ignored = table[0]!.filter((h, i) => h.trim() !== '' && !fields[i]).map((h) => h.trim());
  const rows: ImportRow[] = [];
  table.slice(1).forEach((cells, i) => {
    if (cells.every((c) => c.trim() === '')) return;
    const r: ImportRow = { line: i + 2, host: '', name: '', port: '', tls: '', tlsInsecure: '', username: '',
      password: '', credentialProfile: '', sites: '', uplinkInterface: '', pingTarget: '' };
    cells.forEach((c, j) => {
      const f = fields[j];
      if (f && r[f] === '') r[f] = c;
    });
    rows.push(r);
  });
  if (!rows.length) return { rows, ignored, error: 'The file has a header but no device rows.' };
  return { rows, ignored, error: '' };
}

// ── what the server says ─────────────────────────────────────────────────────

export interface ImportVerdict {
  line: number; label: string; host: string; port: number; status: string; reason: string;
  mode: string; profile: string; sites: string[]; newSites: string[];
}
export interface ImportPlan {
  rows: ImportVerdict[]; newSites: string[]; ready: number; duplicates: number; errors: number; accounts: number;
}
export interface ImportResultRow { line: number; label: string; state: string; message: string }
export interface ImportJob { running: boolean; started: number; rows: ImportResultRow[]; summary: string }

const STATUS_PILL: Record<string, [string, string]> = {
  ready: ['hs-ok', 'Ready'],
  duplicate: ['hs-never', 'Duplicate'],
  error: ['hs-stale', 'Error'],
  pending: ['hs-never', 'Waiting'],
  working: ['hs-info', 'Working'],
  added: ['hs-ok', 'Added'],
  warning: ['hs-warn', 'Warning'],
  failed: ['hs-stale', 'Failed'],
};

const pill = (state: string): string => {
  const [cls, word] = STATUS_PILL[state] ?? ['hs-never', state];
  return `<span class="vpn-hs-badge ${cls}">${esc(word)}</span>`;
};

function loginText(v: ImportVerdict): string {
  if (v.mode === 'verify') return 'Profile ' + v.profile;
  if (v.mode === 'link') return 'Own login, then ' + v.profile;
  if (v.mode === 'plain') return 'Own login';
  return '';
}

/** The plan's one-line summary. */
export function planSummary(p: ImportPlan): string {
  const parts = [`${p.ready} ready`];
  if (p.duplicates) parts.push(`${p.duplicates} duplicate${p.duplicates === 1 ? '' : 's'} skipped`);
  if (p.errors) parts.push(`${p.errors} with errors`);
  let s = parts.join(', ') + '.';
  if (p.newSites.length) s += ` Creates ${p.newSites.length} site${p.newSites.length === 1 ? '' : 's'}: `
    + p.newSites.join(', ') + '.';
  if (p.accounts) s += ` Creates the mikrodash account on ${p.accounts} router${p.accounts === 1 ? '' : 's'}.`;
  return s;
}

export function previewHtml(p: ImportPlan): string {
  const rows = p.rows.map((v) => {
    const sites = v.sites.map((n) => esc(n) + (v.newSites.includes(n) ? ' <em class="dimp-new">new</em>' : ''))
      .join(', ');
    return `<tr><td class="dimp-line">${v.line}</td><td>${pill(v.status)}</td><td>${esc(v.label)}</td>`
      + `<td class="dimp-mono">${esc(v.host)}${v.port ? ':' + v.port : ''}</td><td>${sites || '-'}</td>`
      + `<td>${esc(loginText(v)) || '-'}</td><td class="dimp-why">${esc(v.reason)}</td></tr>`;
  }).join('');
  return '<table class="rtr-list dimp-table"><thead><tr><th>Line</th><th>Status</th><th>Name</th><th>Host</th>'
    + '<th>Sites</th><th>Login</th><th></th></tr></thead><tbody>' + rows + '</tbody></table>';
}

export function progressHtml(job: ImportJob): string {
  const rows = job.rows.map((r) => `<tr><td class="dimp-line">${r.line}</td><td>${pill(r.state)}</td>`
    + `<td>${esc(r.label)}</td><td class="dimp-why">${esc(r.message)}</td></tr>`).join('');
  return '<table class="rtr-list dimp-table"><thead><tr><th>Line</th><th>Status</th><th>Name</th><th></th></tr>'
    + '</thead><tbody>' + rows + '</tbody></table>';
}

// ── the dialog ───────────────────────────────────────────────────────────────

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch('/api/routers/bulk' + path, {
    method, credentials: 'same-origin',
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  });
  const out = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new Error(out.error || 'The request failed (' + r.status + ')');
  return out;
}

export function initDeviceImport(): void {
  const modal = el('dimpModal');
  if (!modal) return;
  let rows: ImportRow[] = [];
  let poll: ReturnType<typeof setTimeout> | null = null;

  const show = (id: string, on: boolean): void => { const n = el(id); if (n) n.style.display = on ? '' : 'none'; };
  const setText = (id: string, t: string): void => { const n = el(id); if (n) n.textContent = t; };
  const go = el<HTMLButtonElement>('dimpGo');
  const file = el<HTMLInputElement>('dimpFile');

  function reset(): void {
    rows = [];
    if (file) file.value = '';
    setText('dimpError', '');
    setText('dimpSummary', '');
    const body = el('dimpBody');
    if (body) body.innerHTML = '';
    if (go) { go.disabled = true; go.textContent = 'Import'; go.style.display = ''; }
    show('dimpPick', true);
  }

  function open(on: boolean): void {
    if (on) reset();
    else if (poll) { clearTimeout(poll); poll = null; }
    modal!.classList.toggle('open', on);
  }

  async function preview(text: string): Promise<void> {
    const read = readRows(text);
    rows = read.rows;
    setText('dimpError', read.error);
    if (read.error) return;
    try {
      const plan = await api<ImportPlan>('POST', '/check', { rows });
      const body = el('dimpBody');
      if (body) body.innerHTML = previewHtml(plan);
      setText('dimpSummary', planSummary(plan) + (read.ignored.length
        ? ' Ignored column' + (read.ignored.length === 1 ? '' : 's') + ': ' + read.ignored.join(', ') + '.' : ''));
      if (go) {
        go.disabled = plan.ready === 0;
        go.textContent = plan.ready ? `Import ${plan.ready} device${plan.ready === 1 ? '' : 's'}` : 'Import';
      }
    } catch (e) {
      setText('dimpError', (e as Error).message);
    }
  }

  async function watch(): Promise<void> {
    try {
      const { job } = await api<{ job: ImportJob | null }>('GET', '');
      if (!job) return;
      const body = el('dimpBody');
      if (body) body.innerHTML = progressHtml(job);
      setText('dimpSummary', job.running ? 'Importing…' : job.summary);
      if (job.running) poll = setTimeout(() => void watch(), 1500);
    } catch (e) {
      setText('dimpError', (e as Error).message);
    }
  }

  el('rtrImportBtn')?.addEventListener('click', () => open(true));
  el('dimpClose')?.addEventListener('click', () => open(false));
  el('dimpTemplate')?.addEventListener('click', () => {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([TEMPLATE], { type: 'text/csv' }));
    a.download = 'mikrodash-devices.csv';
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  });
  file?.addEventListener('change', () => {
    const f = file.files?.[0];
    if (!f) return;
    void f.text().then((t) => preview(t));
  });
  go?.addEventListener('click', async () => {
    if (!rows.length || go.disabled) return;
    go.disabled = true;
    setText('dimpError', '');
    try {
      await api('POST', '', { rows });
      show('dimpPick', false);
      go.style.display = 'none';
      await watch();
    } catch (e) {
      setText('dimpError', (e as Error).message);
      go.disabled = false;
    }
  });
}
