// A line diff as markup: the one renderer for every diff the app shows.
//
// The diff itself is computed on the server (internal/backups/diff.go) and
// arrives as hunks. Backups shows a restore point against the one before it;
// Config Management shows a template against what a router holds. Both draw
// here, so a change to how a diff reads is made once.

import { esc } from './dom';
import type { Hunk } from './gen/payloads';

/** Every hunk, with its header and each line numbered on its own side. */
export function hunksHTML(hunks: Hunk[]): string {
  return hunks.map((h) => {
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
