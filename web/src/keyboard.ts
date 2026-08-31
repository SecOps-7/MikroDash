// The shell's keyboard shortcuts: a digit opens a page, `/` jumps to the log
// search, and a hint appears briefly to say that something happened.
//
// ── WHY IT TAKES `navigate` RATHER THAN CALLING showPage ────────────────────
//
// During coexistence not every page is ported, and going to one that is not
// would show an empty panel. `wireNav` already decides that — ported pages
// render here, everything else hands the browser back to the Node app. The
// shortcut has to make the SAME decision, so it is given the same function
// rather than a second copy of the rule: two copies of a routing decision drift,
// and the way you find out is a shortcut that blanks the page.
//
// ── ONLY NINE OF THE TWENTY-ONE SLOTS CAN BE REACHED ────────────────────────
//
// `parseInt` runs on a single keypress and no keypress produces "10". The list
// is kept whole anyway; see gen/page-keys.ts.

import { el } from './dom.js';
import { PAGE_KEYS } from './gen/page-keys.js';

let hintTimer: ReturnType<typeof setTimeout> | null = null;

/** Flash the hint, and restart its timer if it is already showing. */
function showKbdHint(): void {
  const hint = el('kbdHint');
  if (!hint) return;
  hint.classList.add('show');
  if (hintTimer) clearTimeout(hintTimer);
  hintTimer = setTimeout(() => { hint.classList.remove('show'); }, 1800);
}

export function initKeyboard(navigate: (page: string) => void): void {
  document.addEventListener('keydown', (e) => {
    // Typing in a field is typing, not navigating. Without this, searching the
    // logs for "3" would jump to the third page mid-word.
    const tag = (e.target as HTMLElement | null)?.tagName;
    if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;

    if (e.key === '/') {
      // preventDefault BEFORE navigating: `/` opens quick-find in some browsers,
      // and the character would otherwise land in the search box it is about to
      // focus.
      e.preventDefault();
      navigate('logs');
      // The focus is deferred because the page it targets has just been shown;
      // focusing a field inside a panel that is still display:none does nothing.
      setTimeout(() => { el<HTMLInputElement>('logSearch')?.focus(); }, 100);
      showKbdHint();
      return;
    }

    // NO RADIX, deliberately, because the original has none. With `10` a key of
    // "0x3" parses as 0 and does nothing; without it, it parses as hex 3 and
    // opens the third page. No keyboard can produce that key, so this is
    // unreachable either way — but the differential gate compares the mapping
    // for keys `parseInt` will read a number out of, and it caught the
    // difference. Reproducing beats tidying on a line where the two disagree.
    const n = Number.parseInt(e.key);
    if (n >= 1 && n <= PAGE_KEYS.length) {
      navigate(PAGE_KEYS[n - 1]!);
      showKbdHint();
    }
  });
}
