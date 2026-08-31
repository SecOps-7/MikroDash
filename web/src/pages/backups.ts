/**
 * The Backups page.
 *
 * A thin view over ONE server payload (`backups:state`): settings, summary and
 * history arrive together, so there is no order in which the parts can disagree
 * with each other.
 *
 * ── WRITE PERMISSION IS CARRIED, NOT INFERRED ───────────────────────────────
 *
 * `permitted` comes on the payload rather than being worked out from the role
 * here. The server decides; this only draws. That matters because "may
 * download" is a WRITE-level question — an export describes the whole network
 * and the binary carries every key on the device — and a page inferring it from
 * "can I see this page" would offer both to a viewer.
 *
 * ── A VIEWER GETS NO BUTTONS, NOT DISABLED ONES ─────────────────────────────
 *
 * There is nothing for them to try, so offering a control and then refusing is
 * just noise. Within the permitted case, buttons DIM rather than disappear as
 * the selection changes, because a control that vanishes as you tick boxes is
 * harder to find again than one that greys.
 */

import { esc, el, fmtBytes } from '../dom';
import type { Socket } from '../socket';

interface BkSettings {
  enabled: boolean;
  schedule: string;
  time: string;
  timezone: string;
  keepCount: number;
  keepDays: number;
}

interface BkSummary {
  runs: number; stored: number; bytes: number;
  lastAt: number; lastOutcome: string | null;
}

interface BkRow {
  id: number;
  takenAt: number;
  outcome: string;
  source: string;
  actor: string | null;
  stem: string | null;
  pruned: boolean;
  bytes: number;
  osVersion: string | null;
  model: string | null;
  serial: string | null;
  ms: number;
  error: string | null;
}

interface BkState {
  routerId: string;
  label: string;
  settings: BkSettings;
  summary: BkSummary;
  running: boolean;
  permitted: boolean;
  rows: BkRow[];
}

interface DiffLine { op: string; text: string; aLine?: number; bLine?: number }
interface DiffHunk {
  aStart: number; bStart: number; aCount: number; bCount: number; lines: DiffLine[];
}
interface DiffPayload {
  baseline?: boolean; truncated?: boolean;
  added?: number; removed?: number; hunks: DiffHunk[];
}

/** Every outcome the runner can record. An unknown one still renders. */
const OUTCOME: Record<string, { label: string; cls: string }> = {
  changed: { label: 'Stored', cls: 'bg-green-lt' },
  unchanged: { label: 'No change', cls: 'bg-azure-lt' },
  skipped: { label: 'Skipped', cls: 'bg-yellow-lt' },
  failed: { label: 'Failed', cls: 'bg-red-lt' },
};

const BTN = 'padding:.3125rem .5rem;font-size:.75rem;line-height:1.3333333333';

const ERRORS: Record<string, string> = {
  denied: 'You do not have permission to do that.',
  'not-configured': 'Enable backups for this router first, so a password can be generated.',
  'not-found': 'That backup is no longer available.',
  'confirm-mismatch': 'The name you typed did not match the router name.',
  'no-route-back': 'The router has no address it can reach MikroDash on. Set a backup base URL in Settings.',
  unavailable: 'The router is not connected.',
};

function fmtWhen(ts: number): string {
  if (!ts) return '—';
  const d = new Date(ts);
  return d.toLocaleDateString() + ' ' +
    d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function initBackupsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  if (!el('bkTable')) return;

  let state: BkState | null = null;
  let busy = false;
  let pendingRestore: number | null = null;

  // IDS RATHER THAN ROW INDEXES, so a list that re-renders under a scheduled
  // run does not silently move the selection onto different backups.
  const picked = new Set<number>();
  // Which selected rows Restore could actually act on. Delete works on any row;
  // Restore needs one whose files are still on disk, so the two cannot share a
  // single "is it selected" answer.
  const restorable = new Set<number>();

  const pickBoxes = (): HTMLInputElement[] => {
    const t = el('bkTable');
    return t ? Array.from(t.querySelectorAll<HTMLInputElement>('[data-bk-pick]:not([disabled])')) : [];
  };

  /** Drop ids that have left the table entirely — deleted, or a router switch. */
  function prunePicked(st: BkState): void {
    const live = new Set((st.rows || []).map((r) => r.id));
    Array.from(picked).forEach((id) => { if (!live.has(id)) picked.delete(id); });
    restorable.clear(); // rebuilt as renderRows walks the rows
  }

  function renderSummary(st: BkState): void {
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    set('bkSumLast', fmtWhen(st.summary?.lastAt || 0));
    set('bkSumStored', String(st.summary?.stored || 0));
    set('bkSumBytes', fmtBytes(st.summary?.bytes || 0));
    set('bkSumSchedule', st.settings.enabled
      ? st.settings.schedule.charAt(0).toUpperCase() + st.settings.schedule.slice(1)
      : 'Off');
    set('bkRouterName', st.label || '');
  }

  /**
   * An hourly backup that waits for 02:00 is a daily backup, so the time is not
   * offered for that frequency — greyed WITH THE REASON, rather than silently
   * accepted and then ignored by the scheduler.
   *
   * The hint names the clock, because "02:00" is meaningless until you know
   * whose 02:00. An empty timezone is the server's own.
   */
  function syncTime(st: BkState | null): void {
    const input = el<HTMLInputElement>('bkTime');
    const hint = el('bkTimeHint');
    if (!input || !hint) return;
    const hourly = el<HTMLSelectElement>('bkSchedule')?.value === 'hourly';
    input.disabled = hourly || !st?.permitted;
    if (hourly) { hint.textContent = 'not used for hourly'; return; }
    if (!input.value) { hint.textContent = 'any time'; return; }
    const tz = st?.settings?.timezone;
    hint.textContent = tz ? tz + ' time' : 'server time';
  }

  function renderSettings(st: BkState): void {
    const enabled = el<HTMLInputElement>('bkEnabled');
    if (enabled) enabled.checked = !!st.settings.enabled;
    const sched = el<HTMLSelectElement>('bkSchedule');
    if (sched) sched.value = st.settings.schedule;
    const time = el<HTMLInputElement>('bkTime');
    if (time) time.value = st.settings.time || '';
    const kc = el<HTMLInputElement>('bkKeepCount');
    if (kc) kc.value = String(st.settings.keepCount);
    const kd = el<HTMLInputElement>('bkKeepDays');
    if (kd) kd.value = String(st.settings.keepDays);

    ['bkEnabled', 'bkSchedule', 'bkTime', 'bkKeepCount', 'bkKeepDays'].forEach((id) => {
      const e = el<HTMLInputElement>(id);
      if (e) e.disabled = !st.permitted;
    });
    syncTime(st);

    const actions = el('bkSettingsActions');
    if (actions) {
      actions.innerHTML = st.permitted
        ? '<button class="sbtn sbtn-primary" style="' + BTN + '" id="bkSave">Save</button>' : '';
      if (st.permitted) el('bkSave')?.addEventListener('click', saveSettings);
    }

    const hist = el('bkHistoryActions');
    if (hist) {
      // Restore and Delete lead; Back Up Now anchors the right-hand end, so the
      // button that needs no selection never changes state as rows are ticked.
      hist.innerHTML = st.permitted
        ? '<button class="sbtn sbtn-purple" style="' + BTN + '" id="bkRestore" disabled>Restore</button>' +
          '<button class="sbtn sbtn-danger" style="' + BTN + '" id="bkDelete" disabled>Delete</button>' +
          '<button class="sbtn sbtn-primary" style="' + BTN + '" id="bkRun"' + (busy ? ' disabled' : '') + '>' +
          (busy ? 'Backing up&hellip;' : '+ Back Up Now') + '</button>' : '';
      if (st.permitted) {
        el('bkRun')?.addEventListener('click', runNow);
        el('bkDelete')?.addEventListener('click', deleteSelected);
        el('bkRestore')?.addEventListener('click', restoreSelected);
      }
    }
  }

  function renderRows(st: BkState): void {
    const rows = st.rows || [];
    const badge = el('bkBadge');
    if (badge) badge.textContent = String(rows.length);
    const note = el('bkNote');
    if (note) {
      note.textContent = rows.length ? '' : 'No backups have been taken yet.';
      note.style.color = '';
    }

    const tbody = el('bkTable');
    if (!tbody) return;
    tbody.innerHTML = rows.map((r) => {
      const o = OUTCOME[r.outcome] || { label: r.outcome, cls: '' };
      const actions: string[] = [];
      if (r.stem && !r.pruned) {
        actions.push('<button class="sbtn sbtn-ghost" style="' + BTN +
          '" data-bk-diff="' + r.id + '">Changes</button>');
        if (st.permitted) {
          // PLAIN LINKS, so the browser saves the file rather than the page
          // having to hold several MB in memory to hand it over.
          const q = '?routerId=' + encodeURIComponent(st.routerId);
          actions.push('<a class="sbtn sbtn-ghost" style="' + BTN +
            '" href="/api/backups/' + r.id + '/rsc' + q + '">.rsc</a>');
          actions.push('<a class="sbtn sbtn-ghost" style="' + BTN +
            '" href="/api/backups/' + r.id + '/backup' + q + '">.backup</a>');
        }
      } else if (r.pruned) {
        actions.push('<span class="muted-note">pruned</span>');
      }

      const detail = r.error ? esc(r.error) : (r.stem ? esc(r.stem) : '');
      // EVERY row is selectable, because Delete clears the row itself and a row
      // with no files — an unchanged run, or one retention took — is still the
      // operator's to remove. Restore is the narrower action, so it is gated on
      // `restorable` rather than on the checkbox.
      const pickable = !!st.permitted;
      const canRestore = !!(r.stem && !r.pruned);
      if (canRestore) restorable.add(r.id);
      const isPicked = pickable && picked.has(r.id);

      return '<tr' + (isPicked ? ' class="bk-row-picked"' : '') + '>' +
        '<td class="text-center" style="padding-right:0">' +
          '<input type="checkbox" class="bk-pick" data-bk-pick="' + r.id + '"' +
          (isPicked ? ' checked' : '') + (pickable ? '' : ' disabled') +
          ' aria-label="Select this restore point">' +
        '</td>' +
        // The row id is what an audit entry names and what a support question
        // quotes — so it needs to be readable, not dug out of the DOM.
        '<td><span class="bk-id-pill">' + esc(String(r.id)) + '</span></td>' +
        '<td>' + esc(fmtWhen(r.takenAt)) +
          (detail ? '<div class="muted-note" style="font-size:.7rem">' + detail + '</div>' : '') + '</td>' +
        '<td><span class="badge ' + o.cls + '">' + esc(o.label) + '</span></td>' +
        '<td>' + esc(r.source === 'manual' ? ('Manual' + (r.actor ? ' · ' + r.actor : '')) : 'Schedule') + '</td>' +
        '<td>' + esc(r.osVersion || '—') + '</td>' +
        '<td>' + (r.bytes ? esc(fmtBytes(r.bytes)) : '—') + '</td>' +
        '<td class="text-end">' + actions.join(' ') + '</td>' +
      '</tr>';
    }).join('');
  }

  /**
   * Delete needs one or more; Restore needs EXACTLY ONE, because it replaces a
   * whole configuration and "which of these three?" has no sensible answer.
   */
  function syncBulk(): void {
    const n = picked.size;
    const del = el<HTMLButtonElement>('bkDelete');
    if (del) {
      del.disabled = n === 0 || busy;
      del.textContent = n > 1 ? 'Delete (' + n + ')' : 'Delete';
      del.title = n === 0 ? 'Select one or more restore points to delete' : '';
    }
    const rst = el<HTMLButtonElement>('bkRestore');
    if (rst) {
      // Exactly one, AND that one has files. Selecting a row whose backup is
      // gone leaves nothing to restore from, so the button stays dim and says
      // which case it is.
      const only = n === 1 ? Array.from(picked)[0]! : null;
      const ok = only !== null && restorable.has(only);
      rst.disabled = !ok || busy;
      rst.title = n === 0 ? 'Select a restore point to restore from'
        : n > 1 ? 'Restore takes a single restore point — select just one'
        : !ok ? 'That row has no stored backup to restore from'
        : '';
    }
    const all = el<HTMLInputElement>('bkPickAll');
    if (all) {
      const boxes = pickBoxes();
      const on = boxes.filter((b) => b.checked).length;
      all.disabled = boxes.length === 0;
      all.checked = boxes.length > 0 && on === boxes.length;
      all.indeterminate = on > 0 && on < boxes.length;
    }
  }

  function render(): void {
    if (!state) return;
    // Before the rows, so a selection retention pruned under us is dropped
    // rather than left pointing at a row that no longer offers a checkbox.
    prunePicked(state);
    renderSummary(state);
    renderSettings(state);
    renderRows(state);
    // After renderSettings (which rebuilds the header buttons) and renderRows
    // (which rebuilds the boxes the select-all state is derived from).
    syncBulk();
  }

  function saveSettings(): void {
    socket.emit('backups:settings', {
      enabled: el<HTMLInputElement>('bkEnabled')?.checked,
      schedule: el<HTMLSelectElement>('bkSchedule')?.value,
      // SENT EVEN WHEN HOURLY DISABLED THE FIELD: a chosen time should survive
      // a trip through Hourly and back rather than being silently discarded.
      time: el<HTMLInputElement>('bkTime')?.value,
      keepCount: el<HTMLInputElement>('bkKeepCount')?.value,
      keepDays: el<HTMLInputElement>('bkKeepDays')?.value,
    });
  }

  function runNow(): void {
    busy = true;
    render();
    socket.emit('backups:run');
  }

  function deleteSelected(): void {
    if (!state || !picked.size) return;
    const ids = Array.from(picked);
    const msg = ids.length === 1
      ? 'Delete this restore point?'
      : 'Delete these ' + ids.length + ' restore points?';
    // BOTH HALVES GO — the files and the row listing them — so say so, and say
    // where the record does survive rather than implying nothing is kept.
    if (!window.confirm(msg + '\n\nThe stored files and their history rows are removed,\n' +
      'and cannot be recovered. The Audit page keeps the record.')) return;
    socket.emit('backups:delete', { ids });
    picked.clear();
    syncBulk();
  }

  function restoreSelected(): void {
    if (picked.size !== 1) return;
    askRestore(Array.from(picked)[0]!, false);
  }

  /**
   * Restoring replaces the whole configuration and reboots, so the prompt says
   * so in those words and asks the operator to type the router's name — the same
   * confirmation a package upgrade requires.
   *
   * `acceptVersion` carries a second pass: the server refuses once on a RouterOS
   * version mismatch, and the answer to that question is what turns the refusal
   * into a go-ahead.
   */
  function askRestore(id: number, acceptVersion: boolean, versionNote?: string): void {
    if (!state) return;
    const lines = [
      'Restore ' + state.label + ' from this backup?',
      '',
      'This REPLACES the entire configuration and reboots the router.',
      'Everything configured since this backup is lost.',
      '',
      'The API user MikroDash connects as is part of what gets replaced — if',
      'that user did not exist when this backup was taken, MikroDash will lose',
      'access to this router.',
    ];
    if (versionNote) lines.push('', versionNote);
    lines.push('', 'Type the router name to confirm:');
    const answer = window.prompt(lines.join('\n'), '');
    if (answer === null) return;
    socket.emit('backups:restore', { id, confirm: answer, acceptVersion: !!acceptVersion });
    pendingRestore = id;
  }

  function renderDiff(d: DiffPayload): void {
    const title = el('bkDiffTitle');
    const summary = el('bkDiffSummary');
    const body = el('bkDiffBody');
    if (!title || !summary || !body) return;

    if (d.baseline) {
      title.textContent = 'First backup';
      summary.textContent = 'This is the earliest stored configuration, so there is nothing to compare it against.';
      body.innerHTML = '<div class="bk-diff-empty">No earlier backup.</div>';
    } else if (d.truncated) {
      title.textContent = 'Changes';
      summary.textContent = '';
      body.innerHTML = '<div class="bk-diff-empty">These two configurations differ too widely to show as a line-by-line diff.</div>';
    } else if (!d.hunks.length) {
      title.textContent = 'Changes';
      summary.textContent = '';
      body.innerHTML = '<div class="bk-diff-empty">No differences.</div>';
    } else {
      title.textContent = 'Changes';
      summary.textContent = d.added + ' added, ' + d.removed + ' removed';
      body.innerHTML = d.hunks.map((h) => {
        const head = '<div class="bk-hunk-hdr">@@ -' + h.aStart + ',' + h.aCount +
          ' +' + h.bStart + ',' + h.bCount + ' @@</div>';
        return head + h.lines.map((l) => {
          const cls = l.op === '+' ? 'bk-add' : l.op === '-' ? 'bk-del' : '';
          const num = l.op === '+' ? l.bLine : l.aLine;
          return '<div class="bk-line ' + cls + '"><span class="bk-ln">' + (num || '') + '</span>' +
            esc(l.op + ' ' + l.text) + '</div>';
        }).join('');
      }).join('');
    }
    el('bkDiffModal')?.classList.add('open');
  }

  // ── Wiring ────────────────────────────────────────────────────────────────

  socket.on('backups:state', (st: BkState) => {
    state = st;
    busy = st.running || false;
    render();
  });

  socket.on('backups:running', () => { busy = true; render(); });

  socket.on('backups:ran', () => {
    // A scheduled run finished while the page was open.
    if (isVisible('backups')) socket.emit('backups:list');
  });

  socket.on('backups:diff', renderDiff);

  socket.on('backups:restoring', () => {
    const n = el('bkNote');
    if (n) { n.textContent = 'Sending the backup to the router…'; n.style.color = ''; }
  });

  socket.on('backups:restored', () => {
    pendingRestore = null;
    const n = el('bkNote');
    if (n) {
      n.textContent = 'Restore started. The router is rebooting and will be unreachable for a minute or two.';
      n.style.color = '';
    }
  });

  socket.on('backups:error', (e: { code?: string; message?: string; was?: string; now?: string }) => {
    busy = false;
    render();
    if (e?.code === 'version-mismatch' && pendingRestore !== null) {
      // Asked once, then answered by re-submitting. The server refuses the first
      // attempt precisely so this sentence can name both versions.
      askRestore(pendingRestore, true,
        'WARNING: this backup was taken on RouterOS ' + e.was +
        ' and the router now runs ' + e.now + '. MikroTik recommend matching versions.');
      return;
    }
    const note = el('bkNote');
    if (e?.code === 'serial-mismatch') {
      if (note) {
        note.textContent = 'Refused: this backup was taken from serial ' + e.was +
          ', but this router reports ' + e.now + '. A backup belongs to one device.';
        note.style.color = 'var(--accent-warn)';
      }
      pendingRestore = null;
      return;
    }
    pendingRestore = null;
    // The page's own note line is the sink: there is no global toast, and each
    // page surfaces its errors where the thing that failed is on screen.
    if (note) {
      note.textContent = (e?.code ? ERRORS[e.code] : '') || e?.message || 'Backup request failed';
      note.style.color = 'var(--accent-warn)';
    }
  });

  // Frequency drives whether the time applies at all, so re-evaluate as it
  // changes rather than only on the next payload from the server.
  document.addEventListener('change', (ev) => {
    const t = ev.target as HTMLElement | null;
    if (!t) return;
    if (t.id === 'bkSchedule' || t.id === 'bkTime') { syncTime(state); return; }

    // CHANGE, not click: a checkbox toggled by keyboard has to count too.
    if (t.id === 'bkPickAll') {
      const on = (t as HTMLInputElement).checked;
      pickBoxes().forEach((b) => {
        b.checked = on;
        const id = Number(b.getAttribute('data-bk-pick'));
        if (on) picked.add(id); else picked.delete(id);
        b.closest('tr')?.classList.toggle('bk-row-picked', on);
      });
      syncBulk();
      return;
    }
    if (t.hasAttribute?.('data-bk-pick')) {
      const id = Number(t.getAttribute('data-bk-pick'));
      const on = (t as HTMLInputElement).checked;
      if (on) picked.add(id); else picked.delete(id);
      t.closest('tr')?.classList.toggle('bk-row-picked', on);
      syncBulk();
    }
  });

  document.addEventListener('click', (ev) => {
    const btn = (ev.target as HTMLElement | null)?.closest?.('[data-bk-diff]');
    if (!btn) return;
    ev.preventDefault();
    socket.emit('backups:diff', { id: Number(btn.getAttribute('data-bk-diff')) });
  });

  document.addEventListener('mikrodash:pagechange', (ev) => {
    if ((ev as CustomEvent).detail === 'backups') socket.emit('backups:list');
  });

  // A router switch makes every row on screen belong to the wrong device.
  socket.on('router:switched', () => {
    state = null;
    busy = false;
    // And every selected id belongs to the device we just left. The server
    // rejects a foreign id anyway (the row lookup is router-scoped), but leaving
    // them ticked would show Delete armed for rows no longer on screen.
    picked.clear();
    if (isVisible('backups')) socket.emit('backups:list');
  });
}
