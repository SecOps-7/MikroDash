// Domain lists (Settings -> Features): a list of domains kept in step with FWD
// entries on chosen routers. The server owns the sync and its schedule
// (internal/server/dnslists_api.go); this is the card that defines a list,
// previews what a sync would do and starts one.
//
// A sync the server would refuse unattended - the list came back empty, or under
// half its last size - is offered here only after the preview has said so.

import { esc, el, pill, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { confirmAction } from '../confirm';
import { featureOn, onFeatures } from '../caps';
import { fieldHtml, toggleHtml } from '../form';
import { mountPicker, type Picker } from '../device-picker';
import { fmtTime } from '../timefmt';

interface DnsList {
  id: number;
  name: string;
  sourceUrl: string;
  domains: string;
  addressList: string;
  forwardTo: string;
  matchSubdomain: boolean;
  routerIds: string[];
  intervalHours: number;
  lastSyncAt: number;
  lastResult: string;
  lastCount: number;
  running: boolean;
}

interface ListPlan {
  routerId: string; label: string; add: number; change: number; remove: number; skipped: number; error: string;
}

interface Preview { domains: number; lastCount: number; refused: string; routers: ListPlan[] }

interface RouterRef { id: string; label: string; host: string; disabled?: boolean }

const COLS_L: SortCol[] = [
  { key: 'name', label: 'Name' },
  { key: 'sourceUrl', label: 'Source' },
  { key: 'addressList', label: 'Address list' },
  { key: 'forwardTo', label: 'Forward to' },
  { key: 'routers', label: 'Routers' },
  { key: 'intervalHours', label: 'Every' },
  { key: 'lastSyncAt', label: 'Last sync' },
  { label: '' },
];

function call<T>(url: string, init?: RequestInit): Promise<T> {
  return fetch(url, { credentials: 'same-origin', ...init }).then((r) =>
    r.json().then((d) => (r.ok ? d as T : Promise.reject(new Error((d && d.error) || r.statusText)))));
}

/** How the last sync went, as a pill: its state and, in its title, the detail. */
function lastPill(l: DnsList): string {
  if (l.running) return pill('syncing', 'rx');
  if (!l.lastSyncAt) return pill('never', 'muted');
  const tone = /^refused/.test(l.lastResult) ? 'warn' : /^failed/.test(l.lastResult) ? 'err' : 'ok';
  const word = tone === 'warn' ? 'refused' : tone === 'err' ? 'failed' : 'synced';
  return '<span title="' + esc(l.lastResult) + '">' + pill(word, tone) + '</span> ' + esc(fmtTime(l.lastSyncAt));
}

export function initDnsLists(): void {
  let lists: DnsList[] = [];
  let routers: RouterRef[] = [];
  let editing: DnsList | null = null;
  let allowed = true;
  let poll: number | undefined;
  let picker: Picker | null = null;
  const sortL: SortState = { col: 'name', dir: 'asc' };

  const card = el('dnsListsCard');
  const note = (t: string): void => { const n = el('dnsListsNote'); if (n) n.textContent = t; };
  const name = (id: string): string => {
    const r = routers.find((x) => String(x.id) === id);
    return r ? (r.label || r.host || id) : id;
  };

  function show(): void {
    if (card) card.style.display = featureOn('dnsListsEnabled') && allowed ? '' : 'none';
  }

  function render(): void {
    renderSortHeader('dnsListsThead', COLS_L, sortL, render);
    const body = el('dnsListsTable');
    const badge = el('dnsListsBadge');
    if (badge) badge.textContent = String(lists.length);
    if (!body) return;
    const rows = lists.map((l) => ({ ...l, routers: l.routerIds.map(name).join(', ') }));
    body.innerHTML = rows.length ? sortRows(rows, sortL.col, sortL.dir).map((l) =>
      '<tr><td>' + esc(l.name) + '</td>' +
      '<td class="mono" style="word-break:break-all">' + (l.sourceUrl ? esc(l.sourceUrl) : pill('pasted', 'alt')) + '</td>' +
      '<td class="mono">' + esc(l.addressList || '-') + '</td>' +
      '<td class="mono">' + esc(l.forwardTo || 'default servers') + '</td>' +
      '<td>' + esc(l.routers || '-') + '</td>' +
      '<td>' + (l.intervalHours ? l.intervalHours + ' h' : 'by hand') + '</td>' +
      '<td>' + lastPill(l) + '</td>' +
      '<td class="text-end" style="white-space:nowrap">' +
        '<button class="sbtn sbtn-outline is-sm" type="button" data-dnslist="preview" data-id="' + l.id + '">Preview</button> ' +
        '<button class="sbtn sbtn-outline is-sm" type="button" data-dnslist="sync" data-id="' + l.id + '"' +
          (l.running ? ' disabled' : '') + '>Sync</button> ' +
        '<button class="sbtn sbtn-outline is-sm" type="button" data-dnslist="edit" data-id="' + l.id + '">Edit</button> ' +
        '<button class="sbtn sbtn-danger is-sm" type="button" data-dnslist="delete" data-id="' + l.id + '">Delete</button>' +
      '</td></tr>').join('')
      : '<tr><td colspan="8" class="empty-state">No domain lists.</td></tr>';
    // While a sync runs, the card re-reads the lists until it is done.
    if (lists.some((l) => l.running) && poll === undefined) {
      poll = window.setTimeout(() => { poll = undefined; load(); }, 3000);
    }
  }

  function load(): void {
    if (!featureOn('dnsListsEnabled')) { show(); return; }
    Promise.all([
      call<{ lists: DnsList[] }>('/api/dns/lists'),
      call<{ routers: RouterRef[] }>('/api/routers').catch(() => ({ routers: [] as RouterRef[] })),
    ])
      .then(([d, r]) => { allowed = true; lists = d.lists; routers = (r.routers || []).filter((x) => !x.disabled); })
      .catch(() => { allowed = false; })
      .then(() => { show(); render(); });
  }

  function openEditor(l: DnsList | null): void {
    editing = l;
    const f = el('dnsListForm');
    if (!f) return;
    const text = (id: string, label: string, value: string, placeholder: string, help = ''): string =>
      fieldHtml({ id, label, help }, '<input type="text" class="sform-input" id="' + id + '" value="' + esc(value) +
        '" placeholder="' + esc(placeholder) + '" autocomplete="off">');
    f.innerHTML = '<div class="ff-grid">' +
      text('dnsListName', 'Name', l ? l.name : '', 'vpn-sites') +
      text('dnsListUrl', 'Source URL', l ? l.sourceUrl : '', 'https://example.com/domains.txt',
        'Fetched at every sync from a public address. Hosts, adblock and one-a-line lists are read.') +
      fieldHtml({ id: 'dnsListDomains', label: 'Domains, one a line', full: true,
        help: 'Added to what the source holds.' },
      '<textarea class="sform-input ff-code" id="dnsListDomains" rows="4" spellcheck="false">' +
        esc(l ? l.domains : '') + '</textarea>') +
      text('dnsListAddr', 'Address list', l ? l.addressList : '', 'to_vpn_FWD') +
      text('dnsListFwd', 'Forward to', l ? l.forwardTo : '', 'empty: the resolver\'s own servers') +
      fieldHtml({ id: 'dnsListEvery', label: 'Sync every (hours)', help: '0 syncs only when you press Sync.' },
        '<input type="number" class="sform-input" id="dnsListEvery" min="0" max="168" value="' +
        (l ? l.intervalHours : 0) + '">') +
      fieldHtml({ id: 'dnsListSub', label: 'Match subdomains' }, toggleHtml('dnsListSub', l ? l.matchSubdomain : true)) +
      fieldHtml({ label: 'Routers', full: true }, '<div class="dp" data-dnslist-routers="1"></div>') +
      '</div>' +
      '<div class="hdr-actions" style="margin-top:.6rem">' +
        '<button class="sbtn sbtn-primary is-sm" id="dnsListSave" type="button">Save</button>' +
        '<button class="sbtn sbtn-outline is-sm" id="dnsListCancel" type="button">Cancel</button>' +
      '</div>';
    f.style.display = '';
    const host = f.querySelector<HTMLElement>('[data-dnslist-routers]');
    picker = host ? mountPicker(host, {
      items: () => routers.map((r) => ({ id: String(r.id), label: r.label || r.host || r.id, host: r.host })),
      placeholder: 'Search routers', emptyText: 'No router picked: the list syncs nowhere.',
    }) : null;
    picker?.set({ items: l ? l.routerIds : [] });
    el('dnsListSave')?.addEventListener('click', save);
    el('dnsListCancel')?.addEventListener('click', () => { f.style.display = 'none'; f.innerHTML = ''; });
  }

  function save(): void {
    const val = (id: string): string => (el<HTMLInputElement>(id)?.value || '').trim();
    const body = {
      id: editing ? editing.id : 0,
      name: val('dnsListName'), sourceUrl: val('dnsListUrl'),
      domains: el<HTMLTextAreaElement>('dnsListDomains')?.value || '',
      addressList: val('dnsListAddr'), forwardTo: val('dnsListFwd'),
      matchSubdomain: !!el('dnsListForm')?.querySelector<HTMLInputElement>('.ff-toggle input')?.checked,
      intervalHours: Number(el<HTMLInputElement>('dnsListEvery')?.value) || 0,
      routerIds: picker ? picker.get().items : [],
    };
    call<{ dropped: number; purging: boolean }>('/api/dns/lists', { method: 'POST',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      .then((d) => {
        const f = el('dnsListForm');
        if (f) { f.style.display = 'none'; f.innerHTML = ''; }
        note(!d.dropped ? 'saved' : d.purging ? 'saved; removing its entries from the ' + d.dropped + ' router(s) taken off'
          : 'saved; its entries on the ' + d.dropped + ' router(s) taken off are removed by the next sync');
        load();
      })
      .catch((e: Error) => note('not saved: ' + e.message));
  }

  function sync(l: DnsList, force: boolean): void {
    call('/api/dns/lists/sync?id=' + l.id + (force ? '&force=1' : ''), { method: 'POST' })
      .then(() => { note('sync started'); load(); })
      .catch((e: Error) => note('not started: ' + e.message));
  }

  el('dnsListAdd')?.addEventListener('click', () => openEditor(null));

  card?.addEventListener('click', async (ev) => {
    const b = (ev.target as HTMLElement).closest?.('[data-dnslist]') as HTMLElement | null;
    if (!b) return;
    const id = Number(b.getAttribute('data-id'));
    const l = lists.find((x) => x.id === id);
    if (!l) return;
    switch (b.getAttribute('data-dnslist')) {
      case 'edit': openEditor(l); break;
      case 'delete': {
        if (!(await confirmAction({ title: 'Forget the list?', message: 'This DNS list is forgotten.',
          subject: l.name, confirmLabel: 'Forget', danger: true }))) return;
        // Two questions, because the answers are not the same size: forgetting a
        // list is one row, taking its entries off the routers is a job per router.
        const entries = await confirmAction({ title: 'Also remove its entries?',
          message: 'Also remove the entries this list made from its routers? ' +
            'Remove entries removes them first, and forgets the list once every router has. ' +
            'Cancel leaves them where they are.',
          subject: l.name, confirmLabel: 'Remove entries', danger: true });
        call('/api/dns/lists?id=' + id + (entries ? '&entries=1' : ''), { method: 'DELETE' })
          .then(() => { if (entries) note('removing the entries of ' + l.name + ' first'); load(); })
          .catch((e: Error) => note(e.message));
        break;
      }
      case 'preview':
      case 'sync': {
        const andSync = b.getAttribute('data-dnslist') === 'sync';
        note('reading the list and the routers…');
        call<Preview>('/api/dns/lists/preview?id=' + id, { method: 'POST' })
          .then(async (d) => {
            const plan = d.routers.map((p) => p.label + (p.error ? ' ' + p.error
              : ' +' + p.add + ' ~' + p.change + ' -' + p.remove +
                (p.skipped ? ' (' + p.skipped + ' held by other entries)' : ''))).join(' · ');
            note(l.name + ': ' + d.domains + ' domains · ' + plan);
            if (!andSync) return;
            if (d.domains === 0) { note(l.name + ': ' + d.refused); return; }
            if (d.refused && !(await confirmAction({ title: 'Sync it anyway?',
              message: d.refused[0]!.toUpperCase() + d.refused.slice(1) + '. ' + plan,
              subject: l.name, confirmLabel: 'Sync anyway', danger: true }))) return;
            sync(l, d.refused !== '');
          })
          .catch((e: Error) => note('preview failed: ' + e.message));
        break;
      }
    }
  });

  onFeatures(load);
  document.addEventListener('mikrodash:pagechange', (e) => { if ((e as CustomEvent).detail === 'dns') load(); });
  show();
}
