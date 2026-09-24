// Closing a dialog: the × button, the backdrop, and Escape.
//
// SHELL wiring, not page wiring. The extracted markup uses `data-modal-close`
// on every dialog's × and Cancel, and the live app handles all three routes once
// with delegated listeners rather than per page — which is why porting the first
// route per page left wanWarnWrap's two buttons dead.
//
// ── THE LIST IS GENERATED BECAUSE ITS NAME LIES ─────────────────────────────
//
// Upstream it is `_PRINCIPAL_MODALS`, which is a leftover: it began as the
// Settings principals dialogs and has not been that for a long time. The live
// source says so in as many words — "Escape and backdrop-click are handled here
// for every dialog in the app". An earlier version of this port read the name,
// believed it, and skipped both routes on the stated grounds that none of the
// list was ported. Four of the ten are, so five dialogs here had no Escape and
// no backdrop click behind a justification that read as considered.

import { el } from './dom.js';
import { CLOSABLE_MODALS } from './gen/modals.js';

export function wireModals(): void {
  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const closer = t?.closest?.('[data-modal-close]');
    if (closer) {
      e.preventDefault();
      el(closer.getAttribute('data-modal-close') || '')?.classList.remove('open');
      return;
    }
    // Clicking INSIDE must not close, which is what testing the target itself
    // rather than an ancestor achieves: only the backdrop element carries the
    // class, so a click on the dialog's own content never matches.
    if (t && t.classList && t.classList.contains('rtr-modal-bg') &&
        CLOSABLE_MODALS.includes(t.id)) {
      t.classList.remove('open');
    }
  });

  // ── ESCAPE CLOSES THE TOPMOST ONE, NOT ALL OF THEM ────────────────────
  //
  // It used to close every dialog in the list, open or not, on the stated
  // grounds that "removing a class an element does not have is a no-op, and
  // checking first would be more code for the same result". That was true while
  // no dialog could open over another.
  //
  // One can now: the notification channel's Events tab has a gear that opens an
  // interface-type picker over it. Escape in the picker closed the channel
  // dialog with it, throwing away every edit the operator had made and not yet
  // saved — and it looked like the picker doing something violent rather than
  // Escape doing too much.
  //
  // BY z-index, NOT DOM ORDER. A dialog stacks above another because its
  // z-index says so, and that is the same thing the operator sees; DOM order
  // agrees today and is not what decides what is on top.
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    let top: HTMLElement | null = null;
    let topZ = -Infinity;
    for (const id of CLOSABLE_MODALS) {
      const m = el(id);
      if (!m || !m.classList.contains('open')) continue;
      // A dialog with no z-index of its own reads as 'auto' and parses NaN; it
      // sits at the shared default, so treat it as 0 rather than dropping it.
      const z = parseInt(getComputedStyle(m).zIndex, 10);
      const rank = Number.isNaN(z) ? 0 : z;
      // `>=` so that, among equals, the LAST one wins: later in the list is
      // later in the document, which is what sits on top when z-indexes tie.
      if (rank >= topZ) {
        topZ = rank;
        top = m;
      }
    }
    top?.classList.remove('open');
  });
}
