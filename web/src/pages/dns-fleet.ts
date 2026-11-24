// The DNS page's fleet comparison - the same static entries, across routers.
//
// ── WHY IT IS A SEPARATE MODULE ─────────────────────────────────────────────
//
// `dns.ts` renders what the DNS collector sends for the ACTIVE router, on the
// socket, continuously. This renders what `/api/dns/fleet` answers for SEVERAL
// routers, over HTTP, when asked. Different source, different cadence, different
// table shape - one column per router - and the only thing they share is the
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
// two columns that would need it - a record that exists on both and DIFFERS -
// are marked and left alone. Making "make them the same" a one-click action
// means choosing which router is right, and nothing here knows that.

import { mountPicker, type Picker } from '../device-picker';
import { fmtTime } from '../timefmt';
import { esc, el, lsGet, lsSet } from '../dom';
import { confirmAction } from '../confirm';
import { openResource, registerExtra } from '../resource';
import type { Socket } from '../socket';
import type { RouterRecord } from '../events-hand';
import { featureOn, onFeatures } from '../caps';
import { followJob, jobProgress, jobSummary } from './dns-bulk';

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
  addressList: string;
  matchSubdomain: boolean;
  dynamic: boolean;
  /** The resource descriptor's view of the row - every field the form has. */
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
  /** What a copy writes: a static holder's record, or null when every router
   *  holding it has it from a DHCP server, which is that server's to add. */
  source: FleetEntry | null;
  /** Lined up by value as well as name and type: round-robin records. */
  byValue: boolean;
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
 *  which is a note rather than an answer. The value, the type, the TTL, which
 *  names it covers and which address list it feeds are what a resolver acts on,
 *  and so are an MX record's preference and an SRV record's numbers, which the
 *  value column does not show. */
const NUMBERS = ['mxPreference', 'srvPort', 'srvPriority', 'srvWeight'];

function same(a: FleetEntry, b: FleetEntry): boolean {
  return a.address === b.address && a.type === b.type && a.ttl === b.ttl &&
    a.disabled === b.disabled && a.addressList === b.addressList &&
    a.matchSubdomain === b.matchSubdomain &&
    NUMBERS.every((k) => String(a.values[k] ?? '') === String(b.values[k] ?? ''));
}

/**
 * The key a record lines up on: its name and type, and its value too when any
 * router holds two records with that name and type. Round-robin A records and
 * two TXT on one name were one line before, the second invisible and never
 * copied.
 *
 * A NAME LINES UP WITHOUT CASE. RouterOS keeps the case a name was written in,
 * so `MDPROBE.test` and `mdprobe.test` can both exist, and the resolver answers
 * them as one name. A regexp is a pattern and keeps its case.
 */
function rowKeys(routers: FleetRouter[]): { keyOf: (e: FleetEntry) => string; valued: (e: FleetEntry) => boolean } {
  const base = (e: FleetEntry): string => (e.name ? e.name.toLowerCase() : e.regexp) + SEP + e.type;
  const multi = new Set<string>();
  routers.forEach((r) => {
    const seen = new Set<string>();
    r.entries.forEach((e) => {
      const k = base(e);
      if (seen.has(k)) multi.add(k);
      seen.add(k);
    });
  });
  return {
    keyOf: (e) => (multi.has(base(e)) ? base(e) + SEP + e.address : base(e)),
    valued: (e) => multi.has(base(e)),
  };
}

/** The field each record type keeps its value in, as the form names it. */
const VALUE_FIELD: Record<string, string> = {
  A: 'address', AAAA: 'address', CNAME: 'cname', FWD: 'forwardTo',
  MX: 'mxExchange', NS: 'ns', SRV: 'srvTarget', TXT: 'text',
};

/** A record as the Add or Edit dialog describes it. */
interface FormRecord { name: string; regexp: string; type: string; value: string }

/**
 * Where else a record already is, by the last comparison read: the routers
 * holding it with the same value, and the ones holding its name and type with
 * another. The names line up as the comparison table does, without case; a
 * value is compared exactly, so a TXT record in another case is another value.
 */
export function heldElsewhere(routers: FleetRouter[], except: string,
                              rec: FormRecord): { same: string[]; differ: string[] } {
  const out = { same: [] as string[], differ: [] as string[] };
  if (!rec.name && !rec.regexp) return out;
  const field = VALUE_FIELD[rec.type];
  routers.forEach((r) => {
    if (!r.ok || r.id === except) return;
    const hits = r.entries.filter((e) => e.type === rec.type && (rec.regexp
      ? e.regexp === rec.regexp : e.name.toLowerCase() === rec.name.toLowerCase()));
    if (!hits.length) return;
    const same = !field || hits.some((e) => String(e.values[field] ?? '') === rec.value);
    (same ? out.same : out.differ).push(r.id);
  });
  return out;
}

export function buildRows(routers: FleetRouter[]): FleetRow[] {
  const by = new Map<string, FleetRow>();
  const { keyOf, valued } = rowKeys(routers.filter((r) => r.ok));
  routers.forEach((r) => {
    if (!r.ok) return;
    r.entries.forEach((e) => {
      const key = keyOf(e);
      let row = by.get(key);
      if (!row) {
        row = { id: 'r' + String(by.size), key, name: e.name || e.regexp,
          type: e.type, on: {}, source: null, byValue: valued(e),
          missing: [], differs: false };
        by.set(key, row);
      }
      row.on[r.id] = e;
      if (!row.source && !e.dynamic) row.source = e;
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
   * button doing nothing - the symptom the write path was just fixed for.
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
   * It is sent when the fleet CHANGES - an edit, a removal, an identity read -
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
   * its UUID, which says nothing about which box is about to be written to -
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

  /**
   * Which routers the comparison reads (`device-picker.ts`). Remembered per
   * browser, and every change re-reads the table.
   */
  let fleetPicker: Picker | null = null;
  function renderPicker(): void {
    const host = el('dnsFleetPick');
    if (!host) return;
    if (!fleetPicker) {
      fleetPicker = mountPicker(host, {
        items: () => fleet.map((r) => ({ id: String(r.id), label: named(String(r.id)).label,
          host: String(r.host || ''), siteIds: (r as { siteIds?: string[] }).siteIds ?? [] })),
        placeholder: 'Search routers to compare',
        emptyText: 'No routers picked. Search to add the routers to compare.',
        onChange: () => {
          picked = fleetPicker!.get().items;
          lsSet(PICK_KEY, picked);
          load();
        },
      });
    }
    fleetPicker.set({ items: picked });
  }

  function renderHead(): void {
    const tr = el('dnsFleetThead');
    if (!tr) return;
    const live = data.filter((r) => r.ok);
    tr.innerHTML =
      '<th class="th-sort" data-dnssort="name">Record</th>' +
      '<th>Type</th><th>Value</th>' +
      live.map((r) => '<th class="dns-fleet-col">' + esc(r.label) + '</th>').join('') +
      '<th class="th-sort" data-dnssort="presence">On</th>';
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
    if (!e && !row.source) {
      return '<td class="dns-fleet-col"><span class="muted-note" title="Added by a DHCP server where it exists, ' +
        'so not copied">-</span></td>';
    }
    if (!e) {
      return '<td class="dns-fleet-col">' +
        '<button class="sbtn sbtn-outline" data-dnscopy="' + esc(row.id) + '"' +
          ' data-dnsto="' + esc(r.id) + '"' + (busy ? ' disabled' : '') +
          ' title="Copy this record to ' + esc(r.label) + '">copy &rarr;</button></td>';
    }
    const first = Object.values(row.on)[0]!;
    const odd = !same(e, first);
    return '<td class="dns-fleet-col">' +
      '<span class="dns-fleet-yes' + (odd ? ' is-odd' : '') + '" title="' +
        esc(e.address + (e.ttl ? ' · ttl ' + e.ttl : '') +
          (e.addressList ? ' · list ' + e.addressList : '') +
          (e.matchSubdomain ? ' · subdomains' : '') + (e.disabled ? ' · disabled' : '')) +
        '">' + (odd ? '≠' : '✓') + '</span></td>';
  }

  function render(): void {
    syncTools();
    const tb = el('dnsFleetTable');
    const badge = el('dnsFleetBadge');
    const note = el('dnsFleetNote');
    if (badge) badge.textContent = String(rows.length);
    if (note) {
      const failed = data.filter((r) => !r.ok);
      note.textContent = notice || (loading ? 'reading…'
        : takenAt ? fmtTime(takenAt) +
          (failed.length ? ' · ' + failed.length + ' unreachable' : '')
        : '');
    }
    renderHead();
    if (!tb) return;
    const live = data.filter((r) => r.ok);
    const cols = live.length + 4;
    if (!picked.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + '" class="empty-state">' +
        'Pick the routers to compare.</td></tr>';
      return;
    }
    if (loading && !rows.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + '" class="empty-state">Reading…</td></tr>';
      return;
    }
    if (!rows.length) {
      tb.innerHTML = '<tr><td colspan="' + cols + '" class="empty-state">' +
        'No static entries on the selected routers.</td></tr>';
      return;
    }
    tb.innerHTML = sorted().map((row) => {
      const any = Object.values(row.on)[0]!;
      return '<tr' + (row.missing.length ? ' class="is-partial"' : '') + '>' +
        '<td>' + esc(row.name) + (row.differs
          ? '<span class="pill pill-warn" style="margin-left:.35rem">differs</span>'
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
   * The endpoint refused every copy with a 400 - it decoded a checkbox as a
   * string - and this threw the answer away and reloaded the table, which looks
   * exactly like a button that does nothing. The per-router result now goes in
   * the card's note, the same place the Add dialog puts it.
   */
  interface PushOutcome {
    /** Why the whole request was refused, or '' when each router answered. */
    refused: string;
    results: Array<{ id: string; ok: boolean; code: string }>;
  }

  /** `byValue`: the record is lined up by its value too, so another value under
   *  its name and type on a router is not it (see rowKeys). */
  function push(routerIds: string[], values: Record<string, unknown>, byValue = false): Promise<PushOutcome> {
    return fetch('/api/dns/fleet-add', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ routerIds, values, byValue }),
    })
      .then((r) => (r.ok ? r.json() : { ok: false, code: 'refused' }))
      .then((d): PushOutcome => {
        if (d && d.ok === false) {
          // THE FIELD ERRORS TOO: `invalid` alone says a record was refused
          // without saying which property the descriptor would not take.
          const errs = (d.errors || []) as Array<{ field?: string; message?: string }>;
          return { results: [], refused: 'the write was refused' + (d.code ? ': ' + d.code : '') +
            (errs.length ? ' - ' + errs.map((e) => e.message || e.field).join('; ') : '') };
        }
        return { refused: '', results: (d && d.results) || [] };
      })
      .catch((): PushOutcome => ({ refused: 'the write did not reach the server', results: [] }));
  }

  /** One write's outcome, for the card's note: only the routers that refused. */
  function describe(o: PushOutcome): string {
    if (o.refused) return o.refused;
    return o.results.filter((x) => !x.ok).map((x) => named(x.id).label + ': ' + x.code).join(' · ');
  }

  /** Copy one record to one or more routers, then re-read so the table is the
   *  router's answer rather than this page's assumption. */
  function copy(rowID: string, toIDs: string[]): void {
    const row = rows.find((x) => x.id === rowID);
    if (!row || !toIDs.length) return;
    const from = row.source;
    if (!from) return;
    busy = rowID;
    notice = '';
    render();
    void push(toIDs, from.values, row.byValue).then((o) => { notice = describe(o); busy = ''; load(); });
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
    render(_mode, ctx) {
      const rest = others();
      alsoIDs = [];
      // NOTHING FOR A READ-ONLY VIEWER: this offers a write to other routers,
      // and the dialog no longer makes that decision on an extra's behalf.
      if (ctx.readOnly || scope !== 'fleet' || !rest.length) return '';
      return '<div class="dns-extra">' +
        '<div class="muted-note" data-dnsalso-on style="display:none"></div>' +
        '<div class="dns-extra-title">Also add it to</div>' +
        '<div class="dp" data-dnsalso-host="1"></div>' +
        '<div class="muted-note">Written after this router accepts it, one at a ' +
          'time. A router that already has the record is left alone.</div>' +
      '</div>';
    },
    wire() {
      // THE SHARED PICKER over the routers the comparison has picked, less this
      // one. Mounted fresh each time the dialog renders, and cleared with it.
      showHeld();
      const host = el('res_extra')?.querySelector<HTMLElement>('[data-dnsalso-host]');
      if (!host) return;
      const also = mountPicker(host, {
        items: () => others(),
        placeholder: 'Search the compared routers',
        emptyText: 'Only this router.',
        onChange: () => { alsoIDs = also.get().items; },
      });
    },
    saved(values) {
      const to = alsoIDs.slice();
      if (scope !== 'fleet' || !to.length) { if (scope === 'fleet') load(); return; }
      notice = '';
      void push(to, values).then((o) => { notice = describe(o); load(); });
    },
  });

  /** The dialog's record, read from its fields. */
  function formRecord(): FormRecord {
    const v = (name: string): string => (el<HTMLInputElement>('resf_' + name)?.value || '').trim();
    const type = v('type') || 'A';
    const field = VALUE_FIELD[type];
    return { name: v('name'), regexp: v('regexp'), type, value: field ? v(field) : '' };
  }

  /** Which compared routers already hold the record in the dialog, as it is typed. */
  function showHeld(): void {
    const box = el('res_extra')?.querySelector<HTMLElement>('[data-dnsalso-on]');
    if (!box) return;
    const held = heldElsewhere(data, activeID, formRecord());
    const names = (ids: string[]): string => ids.map((id) => esc(named(id).label)).join(', ');
    box.innerHTML = [
      held.same.length ? 'Already on ' + names(held.same) + '.' : '',
      held.differ.length ? 'With another value on ' + names(held.differ) + '.' : '',
    ].filter(Boolean).join(' ');
    box.style.display = box.innerHTML ? '' : 'none';
  }
  ['input', 'change'].forEach((ev) => document.addEventListener(ev, (e) => {
    if (el('res_fields')?.contains(e.target as Node)) showHeld();
  }));

  el('dnsFleetAddBtn')?.addEventListener('click', () => {
    openResource(socket, 'dnsStatic', null);
  });

  el('dnsFleetReload')?.addEventListener('click', load);
  // ── fleet sync to a reference router (Settings -> Features) ─────────────
  //
  // The server plans it from tables it reads itself (dnsfleetsync_api.go):
  // this asks for the counts first, shows them, and only then for the writes.

  let removeExtra = false;

  function syncTools(): void {
    const tools = el('dnsFleetSyncTools');
    if (tools) tools.style.display = featureOn('dnsFleetSyncEnabled') ? '' : 'none';
    const sel = el<HTMLSelectElement>('dnsFleetRef');
    if (!sel) return;
    const live = data.filter((r) => r.ok);
    const cur = sel.value || activeID;
    sel.innerHTML = live.map((r) => '<option value="' + esc(r.id) + '"' + (r.id === cur ? ' selected' : '') + '>' +
      esc(r.label) + '</option>').join('');
  }

  el('dnsFleetRemoveExtra')?.addEventListener('click', (ev) => {
    removeExtra = !removeExtra;
    const b = ev.currentTarget as HTMLElement;
    b.classList.toggle('is-on', removeExtra);
    b.setAttribute('aria-pressed', String(removeExtra));
  });

  el('dnsFleetSyncRef')?.addEventListener('click', () => {
    const refId = el<HTMLSelectElement>('dnsFleetRef')?.value || '';
    const routerIds = data.filter((r) => r.ok && r.id !== refId).map((r) => r.id);
    if (busy || !refId || !routerIds.length) return;
    interface Planned { routerId: string; label: string; add: number; change: number; remove: number; code: string; jobId: string }
    const ask = (dryRun: boolean): Promise<Planned[]> => fetch('/api/dns/fleet-sync', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refId, routerIds, removeExtra, dryRun }),
    }).then((r) => r.json().then((d) => (r.ok && d.ok ? d.routers as Planned[]
      : Promise.reject(new Error((d && d.error) || r.statusText)))));
    busy = 'sync';
    notice = 'planning…';
    render();
    ask(true)
      .then(async (plans) => {
        const todo = plans.filter((p) => p.add + p.change + p.remove > 0);
        if (!todo.length) { notice = 'every router already matches ' + named(refId).label; return; }
        const lines = plans.map((p) => p.label + ': ' + (p.code || 'add ' + p.add + ', change ' + p.change +
          (removeExtra ? ', remove ' + p.remove : '')));
        if (!(await confirmAction({ title: 'Make these routers match?',
          message: 'Make these routers match the reference?\n' + lines.join('\n'),
          subject: named(refId).label, confirmLabel: 'Sync' }))) {
          notice = '';
          return;
        }
        return ask(false).then((started) => Promise.all(started.map((p) => (p.jobId
          ? followJob(p.jobId, (j) => { notice = p.label + ': ' + jobProgress(j); render(); })
            .then((j) => p.label + ': ' + jobSummary(j))
          : Promise.resolve(p.label + ': ' + (p.code || 'nothing to do'))))))
          .then((results) => { notice = results.join(' · '); });
      })
      .catch((e: Error) => { notice = 'not synced: ' + e.message; })
      .then(() => { busy = ''; load(); });
  });
  onFeatures(syncTools);

  el('dnsFleetSyncAll')?.addEventListener('click', async () => {
    // EVERY MISSING PAIR, ONE RECORD AT A TIME. The server is idempotent - a
    // record already present answers `already-present` rather than adding a
    // second - so a second press is safe.
    //
    // NOT ALL AT ONCE. A router takes 30 writes a minute from one user (#97),
    // and a hundred parallel requests got thirty through, seventy refused, and a
    // note naming whichever refusal arrived last. A router that says
    // `rate-limited` is left out of the rest of the run, and the note counts.
    const work = rows.filter((r) => r.missing.length && r.source);
    if (!work.length || busy) return;
    if (!(await confirmAction({ title: 'Copy the missing records?',
      message: 'Copy ' + work.length + ' record' + (work.length === 1 ? '' : 's') +
        ' to every router that is missing it?', confirmLabel: 'Copy' }))) return;
    if (busy) return;
    busy = 'all';
    notice = 'copying…';
    render();
    const limited = new Set<string>();
    let added = 0, left = 0;
    const failed: string[] = [];
    const next = (i: number): Promise<void> => {
      if (i >= work.length) return Promise.resolve();
      const row = work[i]!;
      const from = row.source;
      const to = row.missing.filter((id) => !limited.has(id));
      left += row.missing.length - to.length;
      if (!from || !to.length) return next(i + 1);
      return push(to, from.values, row.byValue).then((o) => {
        if (o.refused) { failed.push(row.name + ': ' + o.refused); return; }
        o.results.forEach((x) => {
          if (x.code === 'added') added++;
          else if (x.code === 'rate-limited') { limited.add(x.id); left++; }
          else if (!x.ok) failed.push(row.name + ' on ' + named(x.id).label + ': ' + x.code);
        });
      }).then(() => next(i + 1));
    };
    void next(0).then(() => {
      notice = added + ' copied' +
        (left ? ' · ' + left + ' left on ' + [...limited].map((id) => named(id).label).join(', ') +
          ' (30 writes a minute per router): press Sync all missing again in a minute' : '') +
        (failed.length ? ' · ' + failed.length + ' refused: ' + failed.slice(0, 3).join(' · ') +
          (failed.length > 3 ? ' …' : '') : '');
      busy = '';
      load();
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
