// The Files page's Transfer tab (2026-09-21): moving files onto the router and
// reading one back. A hand-built panel on the generated Files area, registered
// with area.ts. Uploading a text file is the File tab's own Add (the `file`
// resource), so it is not repeated here.
//
// The router downloads a URL (`files:fetch`): the server checks the address,
// names the file after it and refuses a name that would run or install
// (internal/server/files.go), so this form only collects the address and shows
// the answer. The Files tab's own poll shows the file once it has landed.
//
// Reading a file (`files:read`) returns its text, capped and with every
// credential value masked by the server; it is set as text, never markup.

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
        '<h4 class="card-title" style="font-size:.9rem;margin:1.2rem 0 .3rem">Read a text file</h4>' +
        '<p class="muted-note" style="margin:0 0 .6rem">Text files up to 64 KiB. Passwords, secrets and ' +
          'keys are shown as «hidden»; a file holding a private key is not shown.</p>' +
        '<form id="filesReadForm" class="d-flex gap-2 flex-wrap" autocomplete="off">' +
          '<input id="filesReadName" class="form-control form-control-sm" type="text" required ' +
            'placeholder="flash/notes.txt" style="flex:1 1 22rem;min-width:0">' +
          '<button class="btn btn-sm btn-outline-secondary" type="submit">Show</button>' +
        '</form>' +
        '<div id="filesReadNote" class="muted-note" style="margin-top:.5rem"></div>' +
        '<pre id="filesReadText" hidden style="margin-top:.5rem;max-height:60vh;overflow:auto;font-size:.72rem;' +
          'white-space:pre-wrap;word-break:break-all"></pre>' +
      '</div>';
    el<HTMLFormElement>('filesReadForm')?.addEventListener('submit', (e) => {
      e.preventDefault();
      const name = (el<HTMLInputElement>('filesReadName')?.value || '').trim();
      if (!name) return;
      const n = el('filesReadNote');
      if (n) n.textContent = 'Reading…';
      const pre = el('filesReadText');
      if (pre) { pre.textContent = ''; pre.hidden = true; }
      socket.emit('files:read', { name });
    });
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

  socket.on('files:content', (d) => {
    const n = el('filesReadNote');
    const pre = el('filesReadText');
    if (!n || !pre) return;
    if (d.error) {
      n.textContent = d.error;
      pre.textContent = '';
      pre.hidden = true;
      return;
    }
    n.textContent = d.name + ', ' + d.size + ' bytes' +
      (d.masked ? ', ' + d.masked + ' value' + (d.masked === 1 ? '' : 's') + ' hidden' : '');
    pre.textContent = d.text;
    pre.hidden = false;
  });

  // A FILE'S TEXT GOES WITH THE ROUTER, and with the page: what was read on
  // one router must not sit on screen while another is selected.
  function clearRead(): void {
    const pre = el('filesReadText');
    if (pre) { pre.textContent = ''; pre.hidden = true; }
    const n = el('filesReadNote');
    if (n) n.textContent = '';
  }
  socket.on('router:active', clearRead);
  document.addEventListener('mikrodash:pagechange', clearRead);

  registerAreaPanel('files', 'transfer', {
    show(h: HTMLElement) {
      if (host !== h) {
        host = h;
        layout(h);
      }
    },
  });
}
