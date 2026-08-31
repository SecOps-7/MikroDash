// The RouterOS upgrade dialog.
//
// ── IT LIVES IN THE SHELL, NOT ON A PAGE ────────────────────────────────────
//
// The button is drawn into `#sysUpdateAction`, a SLOT the System card leaves in
// its update row, and the dialog markup is in `shell.html`. So this is chrome:
// visible from wherever the System card is, which is every page.
//
// `template-id-audit` recorded `sysUpdateAction` as an unbound slot and
// `wiring-audit` recorded the eight `upd_*` ids as an unported group; both are
// this module. `inbound-audit` recorded `packages:upgrade` as an action the
// live app answered and this port did not — the HANDLER landed first
// (`internal/server/packagesUpgrade`), and this is the control that finally
// sends it.
//
// ── THE VERSIONS COME FROM THE CARD, NEVER FROM A SECOND READ ───────────────
//
// The System card publishes what it DREW on `mikrodash:updateavailable`, so
// this module never re-reads the gauge payload or guesses at version strings
// already on screen. Two readers of one payload can disagree; a publisher and a
// subscriber cannot.

import { el, esc } from '../dom';
import type { Socket } from '../socket';

interface UpdCaps { permitted: boolean; routerName: string }
// The producer's type, not a copy of it — see the note beside the declaration
// in `dashboard-system.ts`. The cast below still cannot be checked (detail is
// `any`), but the two modules can no longer drift to different shapes in silence.
import type { UpdInfo } from './dashboard-system';

/** What the dialog says when the router refuses. */
export function upgradeErrorText(code: string | undefined, routerName: string): string {
  if (code === 'confirm-mismatch') return 'That is not this router’s name. Type "' + routerName + '".';
  if (code === 'nothing-to-update') return 'This router is already on the newest version it knows about.';
  if (code === 'denied') return 'You do not have permission to update this router.';
  if (code === 'router-write-policy') return 'The RouterOS user MikroDash connects with lacks the write policy.';
  return 'The router refused the upgrade.';
}

/**
 * The button's markup for the System card's slot.
 *
 * Drawn only with BOTH write permission and a known newer version: an Update
 * button on a router already up to date would be a control with nothing to do,
 * and one shown to a reader would refuse on click.
 */
export function updateSlotHtml(permitted: boolean, latest: string): string {
  return (permitted && latest)
    ? '<button class="sbtn sbtn-warn" id="sysUpdateBtn" style="padding:.1rem .45rem;font-size:.64rem">Update</button>'
    : '';
}

/**
 * What the dialog looks like at each point in the upgrade.
 *
 *   idle       nothing sent yet, the operator can still type and confirm
 *   issuing    the command is on its way to the router
 *   rebooting  the router accepted it and is going down
 *
 * THE SPINNER RUNS THROUGH THE LAST TWO because the interesting wait is the
 * reboot, not the round trip: the router acknowledges in milliseconds and is
 * then unreachable for a minute or two. Without this the dialog simply vanished
 * and left nothing to explain the silence.
 *
 * Returned as DATA rather than applied, so the three states can be compared
 * against the original without a DOM.
 */
export interface UpdView {
  goDisabled: boolean;
  goText: string;
  goIsHtml: boolean;
  confirmDisabled: boolean;
  // NULL MEANS "LEAVE IT ALONE", and that is the original's behaviour rather
  // than a convenience. `issuing` writes ONLY the go button and
  // `confirm.disabled`; it does not touch Cancel or the pending box, which keep
  // whatever `idle` left there. Expressing that as absolute values — "Cancel",
  // hidden — looks identical in every real sequence, because `idle` always runs
  // first, and diverges the moment it does not.
  //
  // The gate caught exactly that: it drives `updState('issuing')` on untouched
  // elements, where the live app leaves them empty and a forcing port does not.
  cancelText: string | null;
  confirmHidden: boolean | null;
  pendingText: string | null;
  pendingHidden: boolean | null;
}

export function updView(state: 'idle' | 'issuing' | 'rebooting'): UpdView {
  if (state === 'idle') {
    return {
      goDisabled: false, goText: 'Update & Reboot', goIsHtml: false,
      confirmDisabled: false,
      cancelText: 'Cancel', confirmHidden: false,
      pendingText: '', pendingHidden: true,
    };
  }
  if (state === 'issuing') {
    return {
      goDisabled: true,
      goText: '<span class="sbtn-spin"></span>Issuing&hellip;',
      goIsHtml: true,
      confirmDisabled: true,
      // Untouched — see the interface.
      cancelText: null, confirmHidden: null, pendingText: null, pendingHidden: null,
    };
  }
  return {
    goDisabled: true,
    goText: '<span class="sbtn-spin"></span>Rebooting&hellip;',
    goIsHtml: true,
    confirmDisabled: true,
    // Once the command is out there is nothing left to confirm, and "Cancel"
    // would imply the upgrade could still be called off. It cannot.
    cancelText: 'Close',
    confirmHidden: true,
    pendingText: 'The router is downloading the packages and restarting. It will be unreachable '
      + 'for a minute or two, and MikroDash reconnects on its own.',
    pendingHidden: false,
  };
}

/**
 * Should a `packages:notes` reply be painted?
 *
 * ONLY IF IT IS FOR THE VERSION THE DIALOG IS SHOWING. The live comment: without
 * this, "switching routers with the dialog open paints the previous router's
 * changelog under the new router's version numbers, which is worse than showing
 * nothing". The server echoes the version back for exactly this test.
 *
 * `showing` is empty when no dialog is open, and a reply arriving then is a
 * straggler for a dialog that has since closed.
 */
export function notesAreForThisDialog(showing: string, replyVersion: unknown): boolean {
  if (!showing) return false;
  return replyVersion === showing;
}

export function initUpgrade(socket: Socket): void {
  let caps: UpdCaps = { permitted: false, routerName: '' };
  let upd: UpdInfo = { installed: '', latest: '', channel: '' };

  function draw(): void {
    const slot = el('sysUpdateAction');
    if (!slot) return;
    slot.innerHTML = updateSlotHtml(caps.permitted, upd.latest);
  }

  function apply(state: 'idle' | 'issuing' | 'rebooting'): void {
    const go = el<HTMLButtonElement>('upd_go');
    if (!go) return;
    const v = updView(state);
    go.disabled = v.goDisabled;
    if (v.goIsHtml) go.innerHTML = v.goText; else go.textContent = v.goText;
    const cancel = el('upd_cancel');
    if (cancel && v.cancelText !== null) cancel.textContent = v.cancelText;
    const confirm = el<HTMLInputElement>('upd_confirm');
    if (confirm) {
      confirm.disabled = v.confirmDisabled;
      if (v.confirmHidden !== null) confirm.style.display = v.confirmHidden ? 'none' : '';
    }
    const pending = el('upd_pending');
    if (pending && v.pendingText !== null) pending.textContent = v.pendingText;
    if (pending && v.pendingHidden !== null) pending.style.display = v.pendingHidden ? 'none' : '';
  }

  socket.on('packages:caps', (d: UpdCaps) => {
    caps = d || { permitted: false, routerName: '' };
    draw();
  });

  document.addEventListener('mikrodash:updateavailable', (e) => {
    upd = ((e as CustomEvent).detail as UpdInfo) || upd;
    draw();
    // Permission is a property of this socket, not of the payload the card drew
    // from, so it is asked for rather than assumed.
    socket.emit('packages:caps', {});
  });

  // The version the open dialog is about, and the test every reply must pass.
  let notesFor = '';

  const setNotes = (text: string, muted: boolean): void => {
    const box = el('upd_notes');
    if (!box) return;
    box.className = 'upd-notes' + (muted ? ' muted' : '');
    // ESCAPED BEFORE INSERTION. This is the only THIRD-PARTY content this app
    // renders into the DOM — fetched from mikrotik.com, not produced by this
    // app or by a router — so it goes through `esc` per the hard constraint in
    // CLAUDE.md. `white-space: pre-wrap` in the stylesheet keeps the
    // "*) area - what changed;" layout without parsing or trusting a line of it.
    box.innerHTML = esc(text);
    box.scrollTop = 0;
  };

  socket.on('packages:notes', (d: { version?: unknown; notes?: string } | undefined) => {
    const reply = d || {};
    if (!notesAreForThisDialog(notesFor, reply.version)) return;
    if (reply.notes) setNotes(reply.notes, false);
    else setNotes('Release notes unavailable', true);
  });

  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    if (!t?.closest) return;
    if (t.closest('#sysUpdateBtn')) {
      const set = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };
      set('upd_from', upd.installed || '—');
      set('upd_to', upd.latest || '—');
      set('upd_channel', upd.channel ? 'channel: ' + upd.channel : '');
      // ASKED FOR HERE, ON OPEN, and never on the update-available path: that
      // fires on every poll tick, and the unconditional rebuild is what made the
      // update strip flash before `lastUpdateRowHtml` was added. Nobody who
      // never opens the dialog should cost a fetch of a third-party URL.
      notesFor = upd.latest || '';
      setNotes('Loading release notes…', true);
      if (notesFor) socket.emit('packages:notes', { version: notesFor });
      else setNotes('Release notes unavailable', true);

      const confirm = el<HTMLInputElement>('upd_confirm');
      if (confirm) { confirm.value = ''; confirm.placeholder = caps.routerName || ''; }
      const err = el('upd_error');
      if (err) err.style.display = 'none';
      apply('idle');
      el('updModal')?.classList.add('open');
      return;
    }
    if (t.closest('#upd_go')) {
      // One command per dialog, not one per click. The disabled check is the
      // guard: a second click while `issuing` would send a second install.
      if (el<HTMLButtonElement>('upd_go')?.disabled) return;
      const err = el('upd_error');
      if (err) err.style.display = 'none';
      apply('issuing');
      socket.emit('packages:upgrade', { confirm: el<HTMLInputElement>('upd_confirm')?.value || '' });
    }
  });

  socket.on('packages:error', (d: { code?: string; routerName?: string }) => {
    const modal = el('updModal');
    // Only while THIS dialog is open: `packages:error` is shared with the
    // Packages page, and painting its refusals into a closed dialog would put a
    // message where nobody will see it and leave the page's own unshown.
    if (!d || !modal || !modal.classList.contains('open')) return;
    const box = el('upd_error');
    if (box) {
      box.textContent = upgradeErrorText(d.code, d.routerName || '');
      box.style.display = '';
    }
    // Nothing was issued, so the operator can correct the name and try again.
    apply('idle');
  });

  socket.on('packages:ok', (d: { action?: string }) => {
    if (!d || d.action !== 'upgrade') return;
    // DELIBERATELY NOT CLOSED. The command has been accepted and the router is
    // about to disappear; closing on success would throw away the only moment
    // we can say so. The operator closes it when they are ready.
    apply('rebooting');
  });
}
