// A small "are you sure?" dialog, for an action that is not undone by a click.
//
// Built from the shared modal classes (`.rtr-modal-bg` > `.rtr-modal.pmodal`),
// so it looks like every other dialog. One at a time; Escape, Cancel, the close
// glyph and a click outside all answer no, and the confirm button is focused
// so Enter answers yes.
//
// Replaced the typed confirmations on the Packages page (the operator,
// 2026-11-23): typing a long router name to reboot it took too long. The page
// still sends the router's name with the action, which the server checks, so an
// action can only land on the router the page was showing.

import { esc } from './dom';

export interface ConfirmOpts {
  title: string;
  /** The question, as text; the router or thing it acts on goes in `subject`. */
  message: string;
  /** Shown in bold under the message, e.g. the router's name. */
  subject?: string;
  confirmLabel: string;
  /** A destructive action gets the danger button. */
  danger?: boolean;
}

let open: HTMLElement | null = null;

/** Asks, and answers true only on the confirm button. */
export function confirmAction(o: ConfirmOpts): Promise<boolean> {
  if (open) open.remove();
  return new Promise((resolve) => {
    const bg = document.createElement('div');
    bg.className = 'rtr-modal-bg open';
    bg.innerHTML =
      '<div class="rtr-modal pmodal" role="alertdialog" aria-modal="true" aria-labelledby="confirmTitle">' +
        '<div class="rtr-modal-hdr"><span class="rtr-modal-title" id="confirmTitle">' + esc(o.title) + '</span>' +
          '<button class="rtr-modal-close" type="button" data-answer="no" aria-label="Close">&#10005;</button></div>' +
        '<div class="rtr-modal-body"><p class="confirm-msg">' + esc(o.message) + '</p>' +
          (o.subject ? '<p class="confirm-subject">' + esc(o.subject) + '</p>' : '') + '</div>' +
        '<div class="rtr-modal-footer">' +
          '<button type="button" class="sbtn sbtn-outline" data-answer="no">Cancel</button>' +
          '<button type="button" class="sbtn ' + (o.danger ? 'sbtn-danger' : 'sbtn-primary') + '" data-answer="yes">' +
            esc(o.confirmLabel) + '</button>' +
        '</div>' +
      '</div>';
    const done = (yes: boolean): void => {
      document.removeEventListener('keydown', onKey, true);
      bg.remove();
      if (open === bg) open = null;
      resolve(yes);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') { e.preventDefault(); done(false); }
    };
    bg.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      if (t === bg) { done(false); return; }
      const b = t?.closest?.('[data-answer]') as HTMLElement | null;
      if (b) done(b.dataset.answer === 'yes');
    });
    document.addEventListener('keydown', onKey, true);
    document.body.appendChild(bg);
    open = bg;
    (bg.querySelector('[data-answer="yes"]') as HTMLButtonElement | null)?.focus();
  });
}
