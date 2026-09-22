// The DNS page's fleet comparison — the same static entries, across routers.
//
// ── WHY IT IS A SEPARATE MODULE ─────────────────────────────────────────────
//
// `dns.ts` renders what the DNS collector sends for the ACTIVE router, on the
// socket, continuously. This renders what `/api/dns/fleet` answers for SEVERAL
// routers, over HTTP, when asked. Different source, different cadence, different
// table shape — one column per router — and the only thing they share is the
// card they sit under. Folding them together would mean one render deciding
// which of two data flows it was in on every line.
//
// ── ON DEMAND, NOT LIVE ─────────────────────────────────────────────────────
//
// Reading every router's DNS table continuously would be a standing cost for a
// comparison somebody looks at for a minute. The button says Reload for that
// reason: the table is a snapshot and says when it was taken.
//
// ── WHAT "SYNCHRONISE" WRITES, AND WHAT IT DOES NOT ─────────────────────────
//
// It ADDS a record that is missing. It never edits and never deletes, and the
// two columns that would need it — a record that exists on both and DIFFERS —
// are marked and left alone. Making "make them the same" a one-click action
// means choosing which router is right, and nothing here knows that.

import { fmtTime } from '../timefmt';
import { t } from '../i18n';
import { esc, el, lsGet, lsSet } from '../dom';
import { openResource, registerExtra } from '../resource';
import type { Socket } from '../socket';
import type { RouterRecord } from '../events-hand';

/** One record as the fleet endpoint reports it. */
interface FleetEntry {
  id: string;
  name: string;
  regexp: string;
  address: string;
  type: string;
  ttl: string;
  disabled: boolean;
  comment: string;
  /** The resource descriptor's view of the row — every field the form has. */
  values: Record<string, unknown>;
}

interface FleetRouter {
  id: string;
  label: string;
  ok: boolean;
  error: string;
  entries: FleetEntry[];
}

/** One line of the comparison: a record, and what each router says about it. */
interface FleetRow {
  /** The handle a button carries. AN INDEX, not the key: the key joins the name
   *  and the type with a control character, and putting one in an HTML attribute
   *  is asking to lose it to whatever normalises the markup next. */
  id: string;
  key: string;
  name: string;
  type: string;
  /** The routers that have it, by id. */
  on: Record<string, FleetEntry>;
  missing: string[];
  differs: boolean;
}

const PICK_KEY = 'mkd_dns_fleet_routers';

/** Joins a record's name and type into the key the comparison lines up on.
 *
 *  U+0001, written as an ESCAPE rather than a literal for the reason
 *  `capsIdentitySep` in internal/collect/capsman.go gives: a literal control
 *  character is invisible in a diff and lost by anything that normalises the
 *  file. It only has to be something a DNS name cannot contain. */
const SEP = '\u0001';

/** What two routers have to agree on for a record to count as the same.
 *
 *  NOT the row id, which is per-router and always differs; not the comment,
 *  which is a note rather than an answer. The value, the type and the TTL are
 *  what a resolver acts on. */
function same(a: FleetEntry, b: FleetEntry): boolean {
  return a.address === b.address && a.type === b.type && a.ttl === b.ttl &&
    a.disabled === b.disabled;
}

function buildRows(routers: FleetRouter[]): FleetRow[] {
  const by = new Map<string, FleetRow>();
  routers.forEach((r) => {
    if (!r.ok) return;
    r.entries.forEach((e) => {
      const key = (e.name || e.regexp) + SEP + e.type;
      let row = by.get(key);
      if (!row) {
        row = { id: 'r' + String(by.size), key, name: e.name || e.regexp,
          type: e.type, on: {}, missing: [], differs: false };
        by.set(key, row);
      }
      row.on[r.id] = e;
    });
  });
  const rows = [...by.values()];
  const live = routers.filter((r) => r.ok);
  rows.forEach((row) => {
    row.missing = live.filter((r) => !row.on[r.id]).map((r) => r.id);
    const held = Object.values(row.on);
    row.differs = held.some((e) => !same(e, held[0]!));
  });
  return rows;
}

type FleetSort = 'name' | 'presence';

export function initDnsFleet(socket: Socket, isVisible: (page: string) => boolean): void {
  const cardEl = el('dnsFleetCard');
  if (!cardEl || !el('dnsStaticCard')) return;
  const card = cardEl;

  let fleet: RouterRecord[] = [];
  let picked: string[] = lsGet<string[]>(PICK_KEY, []);
  let data: FleetRouter[] = [];
  let rows: FleetRow[] = [];
  let sortBy: FleetSort = 'name';
  let loading = false;
  let takenAt = 0;
  let scope: 'one' | 'fleet' = 'one';
  let busy = '';
  /** The router the dialog and the live table are bound to. */
  let activeID = '';
  /**
   * What the last write said, held across the re-read that follows it.
   *
   * ── THE MESSAGE USED TO LIVE FOR ONE FRAME ────────────────────────────
   *
   * Every writer here reloads the table when it is done, and `render` paints the
   * same element with the read's own status. So a refusal was written into the
   * note and taken straight back out a frame later, which looks exactly like the
   * button doing nothing — the symptom the write path was just fixed for.
   */
  let notice = '';
  /** Which other routers the open dialog should also reach. CLEARED EVERY TIME
   *  THE DIALOG OPENS: a tick left over from the record before it would write to
   *  a router nobody looked at on this form. */
  let alsoIDs: string[] = [];

  /**
   * The fleet, from the endpoint that applies this principal's grants.
   *
   * ── `routers:update` IS NOT AN ARRIVAL EVENT ────────────────────────────
   *
   * It is sent when the fleet CHANGES — an edit, a removal, an identity read —
   * and not on connect. A page that only ever listened for it holds an empty
   * list until something happens to a router, and every name it draws is an id.
   * That is what the Add dialog's picker was showing.
   *
   * So the list is FETCHED when this page needs it, and the event is kept for
   * what it is: a change notification. `dbcleanup.ts` makes the same fetch for
   * the same reason, and its comment says the same thing about ids for names.
   */
  function loadFleet(): Promise<void> {
    return fetch('/api/routers', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => {
        const list = ((j && j.routers) || []) as RouterRecord[];
        if (list.length) fleet = list.filter((r) => !r.disabled);
        if (j && j.activeId) activeID = String(j.activeId);
        if (!picked.length && fleet.length) {
          picked = fleet.map((r) => String(r.id));
          lsSet(PICK_KEY, picked);
        } else if (list.length) {
          // A PICK OUTLIVES THE ROUTER IT NAMES. Pruned only when the fetch
          // actually answered, or a failed one would throw the selection away.
          const known = new Set(fleet.map((r) => String(r.id)));
          const kept = picked.filter((id) => known.has(id));
          if (kept.length !== picked.length) {
            picked = kept;
            lsSet(PICK_KEY, picked);
          }
        }
      })
      .catch(() => { /* the comparison still works; the names are ids */ });
  }

  /**
   * How a router is named on screen.
   *
   * ITS ID IS NOT A NAME. A router the operator never labelled fell through to
   * its UUID, which says nothing about which box is about to be written to —
   * reported from the Add dialog's picker. The address is what a label is short
   * for, so it is the fallback and it is shown beside the label either way.
   */
  function named(id: string): { id: string; label: string; host: string } {
    const r = fleet.find((x) => String(x.id) === id);
    const host = String((r && r.host) || '');
    return { id, host, label: String((r && r.label) || '') || host || id };
  }

  function syncScope(): void {
    el('dnsScopeOne')?.classList.toggle('is-on', scope === 'one');
    el('dnsScopeFleet')?.classList.toggle('is-on', scope === 'fleet');
    card.style.display = scope === 'fleet' ? '' : 'none';
    // The single-router table stays where it is. Both are useful at once: one
    // says what THIS router holds and can be edited from, the other says who
    // else holds it.
  }

  function renderPicker(): void {
    const host = el('dnsFleetPick');
    if (!host) return;
    if (!fleet.length) {
      host.innerHTML = '<span class="muted-note">' + t('No routers.') + '</span>';
      return;
    }
    host.innerHTML = fleet.map((r) => {
      const id = String(r.id);
      const on = picked.indexOf(id) !== -1;
      const who = named(id);
      return '<label class="dns-fleet-router' + (on ? ' is-on' : '') + '"' +
        (who.host ? ' title="' + esc(who.host) + '"' : '') + '>' +
        '<input type="checkbox" data-dnsfleet="' + esc(id) + '"' + (on ? ' checked' : '') + '>' +
        '<span>' + esc(who.label) + '</span>' +
      '</label>';
    }).join('');
  }

  function renderHead(): void {
    const tr = el('dnsFleetThead');
    if (!tr) return;
    const live = data.filter((r) => r.ok);
    tr.innerHTML =
      ('<th style="cursor:pointer;user-select:none" data-dnssort="name">' + t('Record') + '</th>') +
      ('<th>' + t('Type') + '</th><th>' + t('Value') + '</th>') +
      live.map((r) => '<th class="dns-fleet-col">' + esc(r.label) + '</th>').join('') +
      ('<th style="cursor:pointer;user-select:none" data-dnssort="presence">' + t('On') + '</th>');
  }

  function sorted(): FleetRow[] {
    const out = rows.slice();
    if (sortBy === 'presence') {
      // MISSING FIRST, which is the reason to open this table at all: the rows
      // that need attention are the ones a router does not have, and a
      // difference is the next most interesting thing after that.
      out.sort((a, b) => {
        const am = a.missing.length, bm = b.missing.length;
        if (am !== bm) return bm - am;
        if (a.differs !== b.differs) return a.differs ? -1 : 1;
        return a.name.localeCompare(b.name);
      });
    } else {
      out.sort((a, b) => a.name.localeCompare(b.name) || a.type.localeCompare(b.type));
    }
    return out;
  }

  function cell(row: FleetRow, r: FleetRouter): string {
    const e = row.on[r.id];
    if (!e) {
      return '<td class="dns-fleet-col">' +
        '<button class="dns-fleet-copy" data-dnscopy="' + esc(row.id) + '"' +
          ' data-dnsto="' + esc(r.id) + '"' + (busy ? ' disabled' : '') +
          ' title="Copy this record to ' + esc(r.label) + '">copy &rarr;</button></td>';
    }
    const first = Object.values(row.on)[0]!;
    const odd = !same(e, first);
    return '<td class="dns-fleet-col">' +
      '<span class="dns-fleet-yes' + (odd ? ' is-odd' : '') + '" title="' +
        esc(e.address + (e.ttl ? ' · ttl ' + e.ttl : '') + (e.disabled ? ' · disabled' : '')) +
        '">' + (odd ? '≠' : '✓') + '</span></td>';
  }

  function render(): void {
    const tb = el('dnsFleetTable');
    const badge = el('dnsFleetBadge');
    const note = el('dnsFleetNote');
    if (badge) badge.textContent = String(rows.length);
    if (note) {
      const failed = data.filter((r) => !r.ok);
      note.textContent = notice || (loading ? t('reading…')
        : takenAt ? fmtTime(takenAt) +
          (failed.length ? ' · ' + failed.length + ' unreachable' : '')
        : '');
    }
    renderHead();
    if (!tb) return;
    const live = data.filter((r) => r.ok);
    const cols = live.length + 4;
    if (!picked.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + '" class="empty-state">' + t('Pick the routers to compare.') + '</td></tr>';
      return;
    }
    if (loading && !rows.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + ('" class="empty-state">' + t('Reading…') + '</td></tr>');
      return;
    }
    if (!rows.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + '" class="empty-state">' + t('No static entries on the selected routers.') + '</td></tr>';
      return;
    }
    tb.innerHTML = sorted().map((row) => {
      const any = Object.values(row.on)[0]!;
      return '<tr' + (row.missing.length ? ' class="is-partial"' : '') + '>' +
        '<td>' + esc(row.name) + (row.differs
          ? '<span class="badge bg-yellow-lt" style="margin-left:.35rem;font-size:.6rem">differs</span>'
          : '') + '</td>' +
        '<td>' + esc(row.type) + '</td>' +
        '<td class="mono">' + esc(any.address) + '</td>' +
        live.map((r) => cell(row, r)).join('') +
        '<td>' + (live.length - row.missing.length) + '/' + live.length + '</td>' +
      '</tr>';
    }).join('');
  }

  function load(): void {
    if (!picked.length) { data = []; rows = []; render(); return; }
    loading = true;
    render();
    fetch('/api/dns/fleet?routers=' + encodeURIComponent(picked.join(',')),
      { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        data = (d && d.routers) || [];
        rows = buildRows(data);
        takenAt = Date.now();
      })
      .catch(() => { data = []; rows = []; })
      .then(() => { loading = false; render(); });
  }

  /**
   * Write one record to some routers and SAY WHAT HAPPENED.
   *
   * ── A SILENT WRITE IS WHY THIS WENT UNNOTICED ─────────────────────────
   *
   * The endpoint refused every copy with a 400 — it decoded a checkbox as a
   * string — and this threw the answer away and reloaded the table, which looks
   * exactly like a button that does nothing. The per-router result now goes in
   * the card's note, the same place the Add dialog puts it.
   */
  function push(routerIds: string[], values: Record<string, unknown>): Promise<void> {
    notice = '';
    return fetch('/api/dns/fleet-add', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ routerIds, values }),
    })
      .then((r) => (r.ok ? r.json() : { ok: false, code: 'refused' }))
      .then((d) => {
        if (d && d.ok === false) {
          // THE FIELD ERRORS TOO: `invalid` alone says a record was refused
          // without saying which property the descriptor would not take.
          const errs = (d.errors || []) as Array<{ field?: string; message?: string }>;
          notice = (d.code ? t('the write was refused: {code}', { code: d.code }) : t('the write was refused')) +
            (errs.length ? ' — ' + errs.map((e) => e.message || e.field).join('; ') : '');
          return;
        }
        const bad = (((d && d.results) || []) as Array<{ id: string; ok: boolean; code: string }>)
          .filter((x) => !x.ok);
        if (bad.length) {
          notice = bad.map((x) => named(x.id).label + ': ' + x.code).join(' · ');
        }
      })
      .catch(() => { notice = t('the write did not reach the server'); });
  }

  /** Copy one record to one or more routers, then re-read so the table is the
   *  router's answer rather than this page's assumption. */
  function copy(rowID: string, toIDs: string[]): void {
    const row = rows.find((x) => x.id === rowID);
    if (!row || !toIDs.length) return;
    const from = Object.values(row.on)[0];
    if (!from) return;
    busy = rowID;
    render();
    void push(toIDs, from.values).then(() => { busy = ''; load(); });
  }

  // ── wiring ────────────────────────────────────────────────────────────────

  el('dnsScopeOne')?.addEventListener('click', () => { scope = 'one'; syncScope(); });
  el('dnsScopeFleet')?.addEventListener('click', () => {
    scope = 'fleet';
    syncScope();
    void loadFleet().then(() => {
      renderPicker();
      if (!rows.length) load();
    });
  });
  // ── creating one record on several routers ────────────────────────────────
  //
  // THE DIALOG IS THE ORIGINAL ONE. It is generated from the `dnsStatic`
  // descriptor and therefore knows which of nine properties each record type
  // puts its value in, which `showIf` rules apply, and what the router will
  // refuse. This adds a router picker under its fields and, once the ACTIVE
  // router has accepted the write, sends the same values to the rest through
  // the endpoint `copy` already uses.
  //
  // ONE WINDOW, TWO WRITES, AND THE SECOND IS NOT SILENT: a router that refuses
  // is named in the card's note rather than rolled up into a tick.

  /** The routers an Add could also reach: picked, less the one being written.
   *
   *  ONLY ROUTERS THAT STILL EXIST. `picked` is remembered in localStorage, so a
   *  router that has since been removed left a checkbox behind naming an id the
   *  server would drop anyway. */
  function others(): Array<{ id: string; label: string; host: string }> {
    return picked
      .filter((id) => id !== activeID && fleet.some((r) => String(r.id) === id))
      .map(named);
  }

  registerExtra('dnsStatic', {
    render() {
      const rest = others();
      alsoIDs = [];
      if (scope !== 'fleet' || !rest.length) return '';
      return '<div class="dns-extra">' +
        ('<div class="dns-extra-title">' + t('Also add it to') + '</div>') +
        '<div class="dns-extra-pick">' +
          rest.map((r) => '<label class="dns-fleet-router">' +
            '<input type="checkbox" data-dnsalso="' + esc(r.id) + '">' +
            '<span>' + esc(r.label) + '</span>' +
            (r.host && r.host !== r.label
              ? '<span class="dns-extra-host">' + esc(r.host) + '</span>' : '') +
            '</label>').join('') +
        '</div>' +
        ('<div class="muted-note">' + t('Written after this router accepts it, one at a time. A router that already has the record is left alone.') + '</div>') +
      '</div>';
    },
    wire() {
      el('res_extra')?.querySelectorAll('[data-dnsalso]').forEach((b) => {
        b.addEventListener('change', () => {
          const box = b as HTMLInputElement;
          const id = box.getAttribute('data-dnsalso') || '';
          alsoIDs = box.checked ? alsoIDs.concat([id]) : alsoIDs.filter((x) => x !== id);
          box.closest('.dns-fleet-router')?.classList.toggle('is-on', box.checked);
        });
      });
    },
    saved(values) {
      const to = alsoIDs.slice();
      if (scope !== 'fleet' || !to.length) { if (scope === 'fleet') load(); return; }
      void push(to, values).then(() => load());
    },
  });

  el('dnsFleetAddBtn')?.addEventListener('click', () => {
    openResource(socket, 'dnsStatic', null);
  });

  el('dnsFleetReload')?.addEventListener('click', load);
  el('dnsFleetSyncAll')?.addEventListener('click', () => {
    // EVERY MISSING PAIR, IN ONE REQUEST PER RECORD. The server is idempotent —
    // a record already present answers `already-present` rather than adding a
    // second — so a second press is safe.
    const work = rows.filter((r) => r.missing.length);
    if (!work.length) return;
    if (!window.confirm(work.length === 1 ? t('Copy 1 record to every router that is missing it?')
      : t('Copy {n} records to every router that is missing it?', { n: work.length }))) return;
    let left = work.length;
    work.forEach((row) => {
      const from = Object.values(row.on)[0];
      if (!from) { left--; return; }
      void push(row.missing, from.values).then(() => { if (--left <= 0) load(); });
    });
  });

  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const cp = t?.closest?.('[data-dnscopy]') as HTMLElement | null;
    if (cp) {
      copy(cp.getAttribute('data-dnscopy') || '', [cp.getAttribute('data-dnsto') || '']);
      return;
    }
    const th = t?.closest?.('[data-dnssort]') as HTMLElement | null;
    if (th && el('dnsFleetThead')?.contains(th)) {
      sortBy = (th.getAttribute('data-dnssort') as FleetSort) || 'name';
      render();
    }
  });

  document.addEventListener('change', (e) => {
    const t = e.target as HTMLInputElement | null;
    const id = t?.getAttribute?.('data-dnsfleet');
    if (!id) return;
    picked = t!.checked ? picked.concat([id]) : picked.filter((x) => x !== id);
    lsSet(PICK_KEY, picked);
    renderPicker();
    load();
  });

  socket.on('router:active', (d) => { activeID = (d && d.activeId) || activeID; });
  socket.on('router:switched', (d) => {
    activeID = (d && d.activeId) || '';
    if (scope === 'fleet') load();
  });

  socket.on('routers:update', (d) => {
    fleet = (d || []).filter((r) => !r.disabled);
    // FIRST VISIT PICKS EVERYTHING. A comparison of one router is not a
    // comparison, and making the operator tick boxes before the table says
    // anything is a worse first impression than reading them all once.
    if (!picked.length && fleet.length) {
      picked = fleet.map((r) => String(r.id));
      lsSet(PICK_KEY, picked);
    }
    if (scope === 'fleet') { renderPicker(); }
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'dns') return;
    syncScope();
    void loadFleet().then(() => {
      if (scope === 'fleet') { renderPicker(); load(); }
    });
  });

  syncScope();
  void isVisible;
}
