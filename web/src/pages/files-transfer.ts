// The Files page's Transfer tab (2026-09-21): moving files onto the router. A
// hand-built panel on the generated Files area, registered with area.ts.
//
// The router downloads a URL (`files:fetch`): the server checks the address,
// names the file after it and refuses a name that would run or install
// (internal/server/files.go), so this form only collects the address and shows
// the answer. The Files tab's own poll shows the file once it has landed.

import { el, esc } from '../dom';
import type { Socket } from '../socket';
import { registerAreaPanel } from './area';

export function initFilesTransfer(socket: Socket): void {
  let host: HTMLElement | null = null;
  let busy = false;

  function note(text: string, ok: boolean): void {
    const n = el('filesFetchNote');
    if (!n) return;
    n.innerHTML = text ? '<span class="vpn-hs-badge ' + (ok ? 'hs-ok' : 'hs-warn') + '">' +
      (ok ? 'done' : 'not downloaded') + '</span> ' + esc(text) : '';
  }

  function layout(h: HTMLElement): void {
    h.innerHTML =
      '<div class="files-transfer">' +
        '<h4 class="card-title" style="font-size:.9rem;margin-bottom:.3rem">Router downloads a URL</h4>' +
        '<p class="muted-note" style="margin:0 0 .6rem">The router fetches the file itself, over its own ' +
          'connection, and saves it under the address\'s file name. Scripts that run on arrival ' +
          '(<code>*.auto.*</code>) and packages are refused.</p>' +
        '<form id="filesFetchForm" class="d-flex gap-2 flex-wrap" autocomplete="off">' +
          '<input id="filesFetchUrl" class="form-control form-control-sm" type="url" required ' +
            'placeholder="https://example.com/file.txt" style="flex:1 1 22rem;min-width:0">' +
          '<button class="btn btn-sm btn-primary" type="submit" id="filesFetchBtn">Download to router</button>' +
        '</form>' +
        '<div id="filesFetchNote" class="muted-note" style="margin-top:.5rem"></div>' +
      '</div>';
    const form = el<HTMLFormElement>('filesFetchForm');
    form?.addEventListener('submit', (e) => {
      e.preventDefault();
      const url = (el<HTMLInputElement>('filesFetchUrl')?.value || '').trim();
      if (!url || busy) return;
      busy = true;
      const btn = el<HTMLButtonElement>('filesFetchBtn');
      if (btn) btn.disabled = true;
      note('', true);
      const n = el('filesFetchNote');
      if (n) n.textContent = 'The router is downloading…';
      socket.emit('files:fetch', { url });
    });
  }

  socket.on('files:fetched', (d) => {
    busy = false;
    const btn = el<HTMLButtonElement>('filesFetchBtn');
    if (btn) btn.disabled = false;
    note(d.ok ? 'Saved as ' + d.name + '.' : d.error, d.ok);
  });

  registerAreaPanel('files', 'transfer', {
    show(h: HTMLElement) {
      if (host !== h) {
        host = h;
        layout(h);
      }
    },
  });
}
