// The Scheduled tab: what is set to be mailed out, and whether it worked.
//
// ── WHICH BUTTONS ARE WIRED, AND WHY THE REST ARE NOT ───────────────────────
//
// The server answers `permitted` — whether this principal may create a schedule
// — and the New/Edit/Send-now/Remove buttons are drawn only when it is true,
// exactly as the live page draws them.
//
// The rule this file has always followed is worth keeping: a button wired to a
// missing endpoint fails at the worst possible moment, so nothing is bound until
// the endpoint behind it exists. What changed is which endpoints exist. This
// header used to say "this port implements the READ endpoints; the write ones
// are a later slice", and that stopped being true — `internal/server/reports.go`
// registers POST, PUT and DELETE on `schedules`.
//
//   History      wired. A read.
//   Remove       wired. DELETE exists, and an enabled button that deletes
//                nothing is the worst of the four to leave dead.
//   Edit         WIRED. This said "NOT wired ... this port has no
//                `openSchedModal`" and was stale: `openSchedModal` is defined
//                below and fills every field, the delegated handler calls it
//                with the row, and the sched-form check covers the form
//                across five cases including edit and the reopen paths. Corrected
//                2026-08-25 — a stale blocker invites the next session to "fix"
//                working code.
//   Send now     WIRED, 2026-08-27. This said "RENDERED BUT DEAD ... there is no
//                Go handler for `schedules/{id}/run`, because it builds a report
//                and hands it to an SMTP server and neither the builder nor the
//                mailer is ported". Both were ported before this line was read
//                again: `internal/server/reports_run.go` serves the route with
//                `internal/reportpdf` and `internal/mailer` behind it. The
//                button was the only half still missing, and the two "options"
//                this used to weigh — hide it or leave it dead — were both moot.
//                Pinned by the sched-run check, 11 mutations.
//
//                THE THIRD STALE CLAIM IN THIS HEADER, after `rptSchedNew` and
//                the edit-modal note above it. All three were notes about
//                something MISSING, and a note about an absence has no gate:
//                nothing fails when the thing arrives. `attr-audit` recorded
//                `rs-run` as a gap and could not notice its REASON had expired,
//                which is the documented limit of that ledger.
//
// ── THE LIST IS NOT PART OF loadReports ─────────────────────────────────────
//
// It is configuration, not report data, and it must not be re-fetched every time
// somebody presses Load on a date range. The tab bar asks for it when the tab is
// opened, which is the live app's behaviour.

import { esc, el, fmtBytes } from '../dom';

export interface ScheduleRun {
  ran_at: number;
  outcome: string;
  recipients_n?: number;
  bytes?: number;
  error?: string | null;
}

export interface ScheduleRow {
  id: string;
  name: string;
  sections: string[];
  iface?: string;
  aggregate?: string;
  recipients: string[];
  frequency: string;
  sendHour: number;
  enabled: boolean;
  disabledReason?: string | null;
  lastRun?: ScheduleRun | null;
}

interface SchedulePayload {
  ok?: boolean;
  schedules?: ScheduleRow[];
  smtpReady?: boolean;
  permitted?: boolean;
  /** The section vocabulary, sent by the server rather than hardcoded here so
   *  there is one definition of it — see the note in internal/server/reports.go. */
  sections?: string[];
  /** Which of those sections make the interface picker relevant. */
  needsInterface?: string[];
}

const API = '/api/reports/schedules';

const state = {
  rows: [] as ScheduleRow[],
  permitted: false,
  smtpReady: true,
  // Both arrive with the list and were being DISCARDED: the server has always
  // sent them and nothing here kept them, so the form had no vocabulary to draw.
  sections: [] as string[],
  needsInterface: [] as string[],
};

/** The row being edited, or null for a new schedule. */
let editing: ScheduleRow | null = null;

/** A last-run summary: when, and how it went. */
function fmtRun(r: ScheduleRun | null | undefined): string {
  if (!r) return '—';
  // `toLocaleString()` with no arguments, as the original — the operator's own
  // locale and zone, NOT `displayTimezone`. The two disagree for an operator who
  // set that field, and reproducing the disagreement is the port's job rather
  // than quietly improving one column.
  return new Date(r.ran_at).toLocaleString() + ' · ' + r.outcome;
}

export function renderSchedules(): void {
  const body = el('rptSchedTbody');
  const actions = el('rptSchedActions');
  if (!body) return;

  // Said at creation time rather than in a run row a month later.
  const notice = el('rptSchedNotice');
  if (notice) {
    notice.style.display = state.smtpReady ? 'none' : '';
    const nt = el('rptSchedNoticeText');
    if (nt) {
      nt.textContent = 'SMTP is not configured, so these reports cannot be sent. ' +
        'Set a mail server under Settings → Notifications.';
    }
  }

  if (actions) {
    actions.innerHTML = state.permitted
      ? '<button class="sbtn sbtn-primary" id="rptSchedNew">+ New scheduled report</button>' : '';
  }

  if (!state.rows.length) {
    // THE EMPTY TEXT DIFFERS BY PERMISSION, which is not a flourish: "yet"
    // implies the reader could add one, and saying that to somebody who cannot is
    // an invitation to hunt for a button that is not there.
    body.innerHTML = '<tr><td colspan="6" class="rpt-empty">' +
      (state.permitted ? 'No scheduled reports for this router yet.'
        : 'No scheduled reports for this router.') + '</td></tr>';
    return;
  }

  body.innerHTML = state.rows.map((r) => {
    const acts: string[] = [];
    if (state.permitted) {
      acts.push('<button class="sbtn sbtn-ghost" data-rs-edit="' + esc(r.id) + '">Edit</button>');
      acts.push('<button class="sbtn sbtn-ghost" data-rs-run="' + esc(r.id) + '">Send now</button>');
      acts.push('<button class="sbtn sbtn-ghost" data-rs-del="' + esc(r.id) + '">Remove</button>');
    }
    acts.push('<button class="sbtn sbtn-ghost" data-rs-runs="' + esc(r.id) + '">History</button>');
    const why = r.disabledReason
      ? '<div class="bw-mac">' + esc(r.disabledReason) + '</div>' : '';
    return '<tr>' +
      '<td>' + esc(r.name) +
      (r.enabled ? '' : ' <span class="bw-proto bw-proto-other">off</span>') + why + '</td>' +
      '<td>' + esc(r.frequency) + ' at ' + String(r.sendHour).padStart(2, '0') + ':00</td>' +
      '<td>' + esc(r.sections.join(', ')) +
      (r.iface ? '<div class="bw-mac">' + esc(r.iface) + '</div>' : '') + '</td>' +
      // THE COUNT, NOT THE ADDRESSES. They are not secrets — the server sends
      // them — but a column of six addresses makes the table unreadable, and the
      // edit dialog is where they belong.
      '<td>' + esc(String(r.recipients.length)) + '</td>' +
      '<td class="bw-mac">' + esc(fmtRun(r.lastRun)) + '</td>' +
      '<td class="text-end">' + acts.join(' ') + '</td>' +
      '</tr>';
  }).join('');
}

/** Fetch the list for the current router and draw it. */
export function loadSchedules(): void {
  const router = el<HTMLSelectElement>('rptRouter');
  if (!router?.value) return;
  fetch(API + '?routerId=' + encodeURIComponent(router.value), { credentials: 'same-origin' })
    .then((r) => r.json() as Promise<SchedulePayload>)
    .then((d) => {
      if (!d || !d.ok) return;
      state.rows = d.schedules || [];
      state.permitted = !!d.permitted;
      // `!== false` rather than truthiness: a server that omits the field should
      // not have its schedules declared undeliverable.
      state.smtpReady = d.smtpReady !== false;
      state.sections = d.sections || [];
      state.needsInterface = d.needsInterface || [];
      renderSchedules();
    })
    .catch(() => { /* the rest of the page is unaffected */ });
}

/**
 * Wire the History and Remove buttons.
 *
 * DELEGATED, because renderSchedules replaces the whole tbody. Edit and Send now
 * are deliberately left unbound — see the file header for which endpoint each is
 * waiting on.
 */
export function wireScheduleActions(): void {
  el('rptSchedTbody')?.addEventListener('click', (e) => {
    const target = e.target as HTMLElement | null;

    // Remove, checked first because it is the destructive one and must not fall
    // through to anything else.
    const del = target?.closest?.('[data-rs-del]') as HTMLElement | null;
    if (del) {
      const id = del.getAttribute('data-rs-del') || '';
      // The NAME comes from the row this page already holds, not from the
      // button, so the confirmation says what the operator is looking at rather
      // than an opaque id.
      const row = state.rows.find((r) => r.id === id);
      const router = el<HTMLSelectElement>('rptRouter');
      // ONE STATED DIFFERENCE. With no router selected the live app confirms and
      // then sends `?routerId=`, which the endpoint answers 400. This returns
      // first. Unreachable either way — the button is drawn from a list that
      // cannot load without a router — and pinned as a difference in
      // tools/sched-remove-check.js rather than quietly diverging.
      if (!row || !router?.value) return;
      if (!window.confirm('Remove the scheduled report "' + row.name + '"?')) return;
      void fetch(API + '/' + encodeURIComponent(id) + '?routerId=' +
        encodeURIComponent(router.value), { method: 'DELETE', credentials: 'same-origin' })
        // RELOADS ONLY ON SUCCESS, which is the live app's shape. An earlier
        // version of this reloaded on failure too and claimed that matched the
        // original — it does not. The live app does that for SEND NOW; its
        // Remove branch has no `.catch` at all.
        //
        // Not reloading is also the right answer on reflection: a DELETE that
        // failed leaves the schedule in place, and the row already on screen is
        // exactly what the server still holds. The empty catch is there only so
        // a network failure does not surface as an unhandled rejection, which
        // changes nothing an operator can see.
        .then(() => { loadSchedules(); })
        .catch(() => { /* the row on screen already reflects the server */ });
      return;
    }

    // ── SEND NOW ────────────────────────────────────────────────────────
    //
    // Drawn since Part 24 and bound only now: the endpoint it needs
    // (`POST schedules/{id}/run`) built a report and mailed it, and neither
    // half was ported. Both are — `internal/server/reports_run.go` with the
    // fpdf renderer and the mailer behind it — so the button is no longer a
    // dead control.
    const run = target?.closest?.('[data-rs-run]') as HTMLButtonElement | null;
    if (run) {
      // DISABLED AND RELABELLED BEFORE THE REQUEST, as the live handler does.
      // Not decoration: a send builds a document and talks to an SMTP server,
      // so it is the slowest button on the page, and without this an impatient
      // operator sends the same report three times.
      run.disabled = true;
      run.textContent = 'Sending…';
      // THE ID IS NOT GUARDED, and the neighbouring Remove branch's guard is
      // not a precedent for one here. There, an id nothing matches means no row
      // to name in the confirmation, so BOTH sides return; here the live app
      // sends `POST …//run` and lets the endpoint answer. An empty id is
      // unreachable — the attribute is drawn from the row this page rendered —
      // but a silent return where the original makes a request is a divergence
      // with nothing to buy it, and the sched-run check compares the
      // empty-id case for exactly that reason.
      const id = run.getAttribute('data-rs-run') ?? '';
      const router = el<HTMLSelectElement>('rptRouter');
      // The ROUTER guard stays, and it is the same stated difference the Remove
      // branch records: with no router the live app sends `routerId=` and the
      // endpoint answers 400. Pinned as a difference rather than quietly
      // diverging.
      if (!router?.value) return;
      // RELOADS ON BOTH OUTCOMES, and here that IS the live shape — unlike the
      // Remove branch above, which has no catch at all. The reason is in the
      // endpoint: a run that did not send answers 200 with `ok:false` and a
      // reason, and the reason reaches the operator through the run HISTORY
      // rather than through this response. So the reload is how the answer is
      // displayed, and it has to happen whether or not the send worked.
      //
      // The reload also restores the button: it is redrawn from the row, which
      // is why nothing here re-enables it by hand.
      void fetch(API + '/' + encodeURIComponent(id) + '/run?routerId=' +
        encodeURIComponent(router.value), { method: 'POST', credentials: 'same-origin' })
        .then(() => { loadSchedules(); })
        .catch(() => { loadSchedules(); });
      return;
    }

    const btn = target?.closest?.('[data-rs-runs]') as HTMLElement | null;
    if (!btn) return;
    const id = btn.getAttribute('data-rs-runs');
    const router = el<HTMLSelectElement>('rptRouter');
    if (!id || !router?.value) return;
    fetch(API + '/' + encodeURIComponent(id) + '/runs?routerId=' +
      encodeURIComponent(router.value), { credentials: 'same-origin' })
      .then((r) => r.json() as Promise<{ ok?: boolean; runs?: ScheduleRun[] }>)
      .then((d) => {
        const box = el('rptSchedRuns');
        if (!d || !d.ok || !box) return;
        const runs = d.runs || [];
        box.innerHTML = '<div class="bw-table-wrap"><table class="bw-table">' +
          '<thead><tr><th>When</th><th>Result</th><th>Recipients</th><th>Size</th>' +
          '<th>Detail</th></tr></thead><tbody>' +
          (runs.length
            ? runs.map((r) =>
              '<tr><td class="bw-mac">' + esc(new Date(r.ran_at).toLocaleString()) + '</td>' +
              '<td>' + esc(r.outcome) + '</td>' +
              '<td>' + esc(String(r.recipients_n ?? 0)) + '</td>' +
              // A zero size shows a dash rather than "0 B": a run that sent
              // nothing and one with no size recorded read the same to anybody
              // looking, and an absence says that better than a number.
              '<td>' + esc(r.bytes ? fmtBytes(r.bytes) : '—') + '</td>' +
              '<td class="bw-mac">' + esc(r.error || '') + '</td></tr>').join('')
            : '<tr><td colspan="5" class="rpt-empty">No runs yet.</td></tr>') +
          '</tbody></table></div>';
      })
      .catch(() => { /* leave whatever history is already shown */ });
  });
}

// ── The schedule form ───────────────────────────────────────────────────────
//
// New and Edit share it: the only difference is whether `editing` holds a row,
// which decides the title, the initial values and whether the save is a POST or
// a PUT.
//
// ── THE BODY DOES NOT CARRY routerId, AND THAT IS NOT AN OVERSIGHT ──────────
//
// The live payload puts `routerId` in the body. This port's endpoints take it
// from the QUERY and decode the body with `DisallowUnknownFields`, so sending it
// there would be rejected outright as a malformed request — the server's comment
// says why it refuses unknown fields: "a field the port does not know is a field
// the operator thinks they set".
//
// So the shape differs from the original's by exactly one key, in the direction
// the server requires. Everything else is the original's, field for field.

/** Which section toggles are ticked, in the order the form drew them. */
function chosenSections(): string[] {
  // `Array.from`, not spread: this project's tsconfig does not enable
  // downlevelIteration, so a NodeList is not spreadable here. Same result.
  return Array.from(document.querySelectorAll<HTMLInputElement>('[data-rs-sec]'))
    .filter((i) => i.checked)
    .map((i) => i.getAttribute('data-rs-sec') || '');
}

/**
 * The interface picker is only relevant to some sections.
 *
 * Which ones comes from the server's `needsInterface`, not from a list here —
 * the same reasoning as the section vocabulary itself.
 */
function syncIfaceVisibility(): void {
  const need = chosenSections().some((sec) => state.needsInterface.indexOf(sec) !== -1);
  const wrap = el('rs_ifaceWrap');
  if (wrap) wrap.style.display = need ? '' : 'none';
}

export function openSchedModal(row: ScheduleRow | null): void {
  editing = row;
  const title = el('rptSchedTitle');
  if (title) title.textContent = row ? 'Edit scheduled report' : 'New scheduled report';

  const name = el<HTMLInputElement>('rs_name');
  if (name) name.value = row ? row.name : '';
  const freq = el<HTMLSelectElement>('rs_frequency');
  if (freq) freq.value = row ? row.frequency : 'daily';
  const iface = el<HTMLInputElement>('rs_iface');
  if (iface) iface.value = row ? (row.iface || '') : '';
  const recips = el<HTMLTextAreaElement>('rs_recipients');
  if (recips) recips.value = row ? row.recipients.join('\n') : '';
  const enabled = el<HTMLInputElement>('rs_enabled');
  // A NEW schedule defaults to enabled, matching the server's own default for an
  // absent `enabled` — see the pointer field in reports.ScheduleInput.
  if (enabled) enabled.checked = row ? !!row.enabled : true;
  const err = el('rs_error');
  if (err) err.style.display = 'none';

  // The hours are rebuilt on every open rather than once, because the original
  // does: it is twenty-four options and rebuilding costs nothing next to
  // remembering whether they are already there.
  const hour = el<HTMLSelectElement>('rs_hour');
  if (hour) {
    hour.innerHTML = '';
    for (let h = 0; h < 24; h++) {
      hour.insertAdjacentHTML('beforeend',
        '<option value="' + h + '">' + String(h).padStart(2, '0') + ':00</option>');
    }
    hour.value = String(row ? row.sendHour : 7);
  }

  const chosen = row ? row.sections : ['ping'];
  const secs = el('rs_sections');
  if (secs) {
    secs.innerHTML = state.sections.map((sec) =>
      '<label class="stoggle"><span class="stoggle-label">' + esc(sec) + '</span>' +
      '<span class="stoggle-switch"><input type="checkbox" data-rs-sec="' + esc(sec) + '"' +
      (chosen.indexOf(sec) !== -1 ? ' checked' : '') + '>' +
      '<span class="stoggle-track"></span><span class="stoggle-thumb"></span></span></label>').join('');
  }
  syncIfaceVisibility();
  el('rptSchedModal')?.classList.add('open');
}

function schedError(message: string): void {
  const box = el('rs_error');
  if (!box) return;
  box.textContent = message;
  box.style.display = '';
}

function saveSchedule(): void {
  const router = el<HTMLSelectElement>('rptRouter');
  if (!router?.value) return;
  const body = {
    name: el<HTMLInputElement>('rs_name')?.value ?? '',
    frequency: el<HTMLSelectElement>('rs_frequency')?.value ?? '',
    sendHour: Number(el<HTMLSelectElement>('rs_hour')?.value ?? '0'),
    sections: chosenSections(),
    iface: el<HTMLInputElement>('rs_iface')?.value ?? '',
    // `split(/\n+/)` on an empty field yields [''], which the original sends and
    // the validator refuses with a message an operator can read. Reproduced
    // rather than pre-filtered here, so the refusal keeps coming from the one
    // place that owns it.
    recipients: (el<HTMLTextAreaElement>('rs_recipients')?.value ?? '').split(/\n+/),
    enabled: !!el<HTMLInputElement>('rs_enabled')?.checked,
  };
  const q = '?routerId=' + encodeURIComponent(router.value);
  const url = editing ? API + '/' + encodeURIComponent(editing.id) + q : API + q;
  void fetch(url, {
    method: editing ? 'PUT' : 'POST',
    credentials: 'same-origin',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then((r) => r.json())
    .then((d) => {
      if (d && d.ok) {
        el('rptSchedModal')?.classList.remove('open');
        loadSchedules();
        return;
      }
      // The server's message where there is one: the validator writes for an
      // operator to read, and replacing it with a generic line would throw away
      // the only thing that says which field is wrong.
      schedError((d && d.error) || 'Could not save the schedule.');
    })
    .catch(() => { schedError('Could not save the schedule.'); });
}

/** New, Edit, Save, and the section toggles that reveal the interface picker. */
export function wireScheduleForm(): void {
  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    if (!t?.closest) return;
    if (t.closest('#rptSchedNew')) { openSchedModal(null); return; }
    if (t.closest('#rs_save')) { saveSchedule(); return; }
    const edit = t.closest('[data-rs-edit]') as HTMLElement | null;
    if (edit) {
      const id = edit.getAttribute('data-rs-edit') || '';
      const row = state.rows.find((r) => r.id === id);
      if (row) openSchedModal(row);
    }
  });

  // DELEGATED on `change`, because the toggles are rebuilt every time the form
  // opens and a listener bound to them would be lost with the markup.
  document.addEventListener('change', (e) => {
    const t = e.target as HTMLElement | null;
    if (t?.hasAttribute?.('data-rs-sec')) syncIfaceVisibility();
  });
}
