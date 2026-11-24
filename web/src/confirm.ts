// The app's own dialogs: confirm, ask for a value, and tell. NEVER the
// browser's confirm(), prompt() or alert() (the operator, 2026-11-24: "the
// whole app should never use the browser popup prompt"). A browser dialog
// blocks the page, cannot be styled or themed, and on some browsers offers to
// suppress further dialogs, which silently turns a confirmation into a yes or a
// no. `TestNoBrowserDialogs` (internal/verify) refuses any call to them.
//
// Built from the shared modal classes (`.rtr-modal-bg` > `.rtr-modal.pmodal`),
// so they look like every other dialog. One at a time; Escape, Cancel, the close
// glyph and a click outside all answer no (or null), and the main button is
// focused, so Enter answers yes.
//
// Each returns a Promise, so a caller that used to read a browser dialog's
// answer synchronously awaits it instead, or continues in `.then`.

import { copyText, esc } from './dom';

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

export interface AskOpts {
  title: string;
  message: string;
  /** The field's label. */
  label: string;
  /** What the field starts with. */
  value?: string;
  placeholder?: string;
  confirmLabel?: string;
}

let open: HTMLElement | null = null;

/** The shared frame: header, body, footer buttons. The buttons marked
 *  data-answer, Escape and a click outside answer it. */
function show(title: string, body: string, buttons: string, focus: string,
  onAnswer: (yes: boolean, bg: HTMLElement) => void): HTMLElement {
  if (open) open.remove();
  const bg = document.createElement('div');
  bg.className = 'rtr-modal-bg open';
  bg.innerHTML =
    '<div class="rtr-modal pmodal" role="alertdialog" aria-modal="true" aria-label="' + esc(title) + '">' +
      '<div class="rtr-modal-hdr"><span class="rtr-modal-title">' + esc(title) + '</span>' +
        '<button class="rtr-modal-close" type="button" data-answer="no" aria-label="Close">&#10005;</button></div>' +
      '<div class="rtr-modal-body">' + body + '</div>' +
      '<div class="rtr-modal-footer">' + buttons + '</div>' +
    '</div>';
  const done = (yes: boolean): void => {
    document.removeEventListener('keydown', onKey, true);
    onAnswer(yes, bg);
    bg.remove();
    if (open === bg) open = null;
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') { e.preventDefault(); done(false); }
    // ENTER IN THE FIELD answers yes, as a browser prompt does.
    if (e.key === 'Enter' && (e.target as HTMLElement | null)?.tagName === 'INPUT') { e.preventDefault(); done(true); }
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
  (bg.querySelector(focus) as HTMLElement | null)?.focus();
  return bg;
}

const cancelBtn = '<button type="button" class="sbtn sbtn-outline" data-answer="no">Cancel</button>';
const mainBtn = (label: string, danger?: boolean): string =>
  '<button type="button" class="sbtn ' + (danger ? 'sbtn-danger' : 'sbtn-primary') + '" data-answer="yes">' +
  esc(label) + '</button>';

/** Asks, and answers true only on the confirm button. */
export function confirmAction(o: ConfirmOpts): Promise<boolean> {
  return new Promise((resolve) => {
    show(o.title,
      '<p class="confirm-msg">' + esc(o.message) + '</p>' +
        (o.subject ? '<p class="confirm-subject">' + esc(o.subject) + '</p>' : ''),
      cancelBtn + mainBtn(o.confirmLabel, o.danger), '[data-answer="yes"]',
      (yes) => resolve(yes));
  });
}

/** Asks for a value: the text entered (possibly empty) on OK, null on Cancel. */
export function askText(o: AskOpts): Promise<string | null> {
  return new Promise((resolve) => {
    show(o.title,
      '<p class="confirm-msg">' + esc(o.message) + '</p>' +
        '<div class="ff-grid ff-one"><div class="ff">' +
          '<label class="sform-label" for="confirmAskValue">' + esc(o.label) + '</label>' +
          '<input class="sform-input" id="confirmAskValue" type="text" autocomplete="off" value="' +
            esc(o.value || '') + '" placeholder="' + esc(o.placeholder || '') + '">' +
          '<div class="ff-help"></div>' +
        '</div></div>',
      cancelBtn + mainBtn(o.confirmLabel || 'OK'), '#confirmAskValue',
      (yes, bg) => resolve(yes ? ((bg.querySelector('#confirmAskValue') as HTMLInputElement | null)?.value ?? '') : null));
  });
}

/** Shows a value to copy, such as a shared link's address, with its own Copy
 *  button (copyText, so it works over plain http too) and Done. */
export function showCopyable(title: string, message: string, value: string): Promise<void> {
  return new Promise((resolve) => {
    const bg = show(title,
      '<p class="confirm-msg">' + esc(message) + '</p>' +
        '<div class="share-url"><code class="confirm-copy-value">' + esc(value) + '</code>' +
          '<button class="sbtn sbtn-outline is-sm" type="button" data-copy>Copy</button></div>',
      mainBtn('Done'), '[data-copy]', () => resolve());
    const btn = bg.querySelector('[data-copy]') as HTMLButtonElement | null;
    btn?.addEventListener('click', () => {
      copyText(value, (ok) => {
        btn.textContent = ok ? 'Copied' : 'Select and copy';
        window.setTimeout(() => { btn.textContent = 'Copy'; }, 2000);
      });
    });
  });
}

/** Tells, with one OK button. Resolves when it is closed, however. */
export function notice(title: string, message: string): Promise<void> {
  return new Promise((resolve) => {
    show(title, '<p class="confirm-msg">' + esc(message) + '</p>', mainBtn('OK'), '[data-answer="yes"]',
      () => resolve());
  });
}
