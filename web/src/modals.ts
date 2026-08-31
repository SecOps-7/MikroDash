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

  // Escape closes every one of them, open or not — removing a class an element
  // does not have is a no-op, and checking first would be more code for the same
  // result.
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    for (const id of CLOSABLE_MODALS) el(id)?.classList.remove('open');
  });
}
