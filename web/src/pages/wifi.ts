// The Wifi Networks page — a port of the `wifiPage` IIFE in public/app.js.
//
// The configuration side of wireless: what this router broadcasts. Who is
// connected to it is the Wifi Clients page, and deliberately a different
// collector on a different cadence.
//
// ── ONE ROW PER INTERFACE, GROUPED UNDER ITS RADIO ──────────────────────────
//
// That is RouterOS's own model — a master radio plus a virtual-AP interface for
// each extra SSID. Merging bands into a single "network" row would read more
// like a consumer router and make the write target ambiguous, which is the one
// thing an editable table cannot afford.
//
// Editing is the shared resource dialog: this file draws rows and nothing else.
// Each row carries `data-res` because the two RouterOS wireless stacks are two
// different resources sharing one table.
//
// ── FOUR VIEWS OF ONE LIST ──────────────────────────────────────────────────
//
// One row per interface is RouterOS's model and stays the default, but it is the
// wrong shape for two ordinary questions. "Is the guest network up everywhere?" is a
// question about an SSID, which this table splits across a dozen rows; "what is
// that hAP doing?" is a question about a piece of hardware, and the radio
// grouping splits one AP into its two bands. So the same rows are drawn four
// ways and the viewer picks:
//
//	radio  one group per master interface   (the original, and the default)
//	ap     one group per access point       (both bands of a CAP together)
//	ssid   one row per network              (aggregated across every radio)
//	flat   no grouping at all               (sort and read)
//
// THE ROW BUILDER IS SHARED and so is the sort, so a column means the same thing
// in every view and a change to a cell lands in all four. Only the grouping and
// the SSID aggregate are per-view.

import { esc, el, bandBadge, bandRank, ssidColours, installWifiGlobals,
  renderSortHeader, lsGet, lsSet, type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';
import type { WifiNetwork, WifiRadio, WifiPayload } from '../gen/payloads';

/** The views, in the order their buttons appear. */
type WnView = 'radio' | 'ap' | 'ssid' | 'flat';
const WN_VIEWS: WnView[] = ['radio', 'ap', 'ssid', 'flat'];

/** What the page remembers between visits. `localStorage`, like the Wifi
 *  Clients table's grouping: a chosen view is a per-browser preference, not
 *  configuration, and nothing on the server has any business holding it. */
const WN_VIEW_KEY = 'mkd_wifi_networks_view';

function loadView(): WnView {
  const v = lsGet<WnView | null>(WN_VIEW_KEY, null);
  return v && WN_VIEWS.indexOf(v) !== -1 ? v : 'radio';
}

function saveView(v: WnView): void {
  lsSet(WN_VIEW_KEY, v);
}

/** One SSID, aggregated across every interface broadcasting it. */
interface SsidRow {
  ssid: string;
  bands: string[];
  ifaces: WifiNetwork[];
  aps: string[];
  security: string;
  vlanId: string;
  clients: number;
  running: boolean;
}

/**
 * The interface rows collapsed to one row per network.
 *
 * ── WHAT "MIXED" MEANS AND WHY IT IS WORTH SAYING ───────────────────────────
 *
 * Security and VLAN are aggregated by AGREEMENT: when every interface carrying
 * an SSID says the same thing, that is the answer; when they disagree, the
 * answer is that they disagree. Picking the first would hide exactly the case
 * worth finding — one AP left on WPA2 while the rest moved to WPA3, or one radio
 * on the wrong VLAN.
 *
 * RUNNING IS ANY, NOT ALL. One radio broadcasting the network is enough for the
 * network to be on the air, which is the same rule the Wifi Clients page's SSID
 * card already applies.
 */
function bySsid(nets: WifiNetwork[]): SsidRow[] {
  const out: SsidRow[] = [];
  const byName = new Map<string, SsidRow>();
  nets.forEach((n) => {
    const name = n.ssid || '(no SSID)';
    let row = byName.get(name);
    if (!row) {
      row = { ssid: name, bands: [], ifaces: [], aps: [], security: n.security,
        vlanId: n.vlanId, clients: 0, running: false };
      byName.set(name, row);
      out.push(row);
    }
    row.ifaces.push(n);
    row.clients += n.clients;
    if (n.running) row.running = true;
    if (n.band && row.bands.indexOf(n.band) === -1) row.bands.push(n.band);
    if (n.ap && row.aps.indexOf(n.ap) === -1) row.aps.push(n.ap);
    if (n.security !== row.security) row.security = 'Mixed';
    if (n.vlanId !== row.vlanId) row.vlanId = 'Mixed';
  });
  out.forEach((r) => r.bands.sort((a, b) => bandRank(a) - bandRank(b)));
  return out;
}

export function initWifiPage(socket: Socket, isVisible: (page: string) => boolean): void {
  // Published under the names the live app uses, so a LIFTED renderer finds
  // them during a DOM comparison — see installWifiGlobals.
  installWifiGlobals();

  let state: WifiPayload | null = null;
  let view: WnView = loadView();
  // NO COLUMN SORT UNTIL A HEADER IS CLICKED. The collector already orders the
  // rows — each radio's own row first, then its virtual APs — and starting on a
  // column would silently reorder a page that has always looked one way. An
  // empty key matches no column, so `applySort` hands the list back untouched
  // and the header draws no indicator.
  const sort: SortState = { col: '', dir: 'asc' };
  // SSID -> colour, recomputed per render. Assigned once for the whole table so
  // the two rows of a dual-band network agree; per-row assignment would give
  // the same SSID a different colour on each band.
  let colours: Record<string, string> = {};

  function badge(text: string, cls: string): string {
    return '<span class="badge ' + cls + '" style="margin-left:.35rem">' + esc(text) + '</span>';
  }

  function stateCell(n: WifiNetwork): string {
    if (n.disabled) return '<span class="badge bg-secondary-lt">Disabled</span>';
    if (n.running) return '<span class="badge bg-green-lt">Running</span>';
    // Enabled but not running is its own answer, and the interesting one: a
    // radio with no country set, or no supported channel, sits exactly here.
    return '<span class="badge bg-yellow-lt">Not running</span>';
  }

  function securityCell(n: WifiNetwork): string {
    // An open network is the thing worth noticing on this page, so it is the
    // one value that gets a colour rather than plain text.
    const cls = n.security === 'Open' ? 'bg-red-lt' : 'bg-azure-lt';
    return '<span class="badge ' + cls + '">' + esc(n.security || '—') + '</span>';
  }

  function radioHeader(radio: Partial<WifiRadio> & { name: string }, count: number): string {
    const bits: string[] = [];
    if (radio.band) bits.push(radio.band);
    if (radio.frequency) bits.push(radio.frequency);
    if (radio.channelWidth) bits.push(radio.channelWidth);
    if (radio.country) bits.push(radio.country);
    return '<tr class="wn-radio-row">' +
      '<td colspan="7" style="background:var(--bg-subtle);font-weight:600;font-size:.76rem">' +
        esc(radio.name) +
        (bits.length ? '<span class="muted-note" style="margin-left:.5rem;font-weight:400">' +
                       esc(bits.join(' · ')) + '</span>' : '') +
        (radio.readOnlyReason === 'capsv1' ? badge('CAPsMAN v1', 'bg-orange-lt')
          : radio.capsManaged ? badge('CAP', 'bg-purple-lt') : '') +
        (radio.disabled ? badge('Disabled', 'bg-secondary-lt') : '') +
        '<span class="muted-note" style="float:right;font-weight:400">' +
          esc(String(count)) + (count === 1 ? ' network' : ' networks') + '</span>' +
      '</td></tr>';
  }

  /**
   * The SSID, as a colour-coded pill.
   *
   * The colour comes from the Wifi Clients page's palette so one network wears
   * one colour wherever you look at it. A dual-band SSID appears on two rows and
   * must read as the same network on both, which is the whole reason the palette
   * hashes the name rather than counting down the list.
   */
  function ssidPill(n: WifiNetwork): string {
    const name = n.ssid || '(no SSID)';
    const col = colours[n.ssid] || 'var(--text-main)';
    return '<span class="wn-ssid-pill" style="color:' + col + ';border-color:' + col + '">' +
           esc(name) + '</span>';
  }

  function bandCell(n: WifiNetwork): string {
    // Both pages spell the three bands the same way, which is why this needs no
    // translation.
    if (!n.band) return '<span class="muted-note">&mdash;</span>';
    return bandBadge(n.band);
  }

  function networkRow(n: WifiNetwork): string {
    // NO `data-id` ON A LEGACY CAPsMAN ROW, which is what makes it read-only
    // without a branch anywhere else: the resource engine opens a row only when
    // it has one. A `/caps-man` interface is configured on the CAPsMAN page's
    // profiles, not here, and the write path has no menu for it at all.
    const handle = n.id
      ? ' data-id="' + esc(n.id) + '" data-identity="' + esc(n.name) + '"' +
        ' data-res="' + esc(n.resource) + '"'
      : '';
    return '<tr' + handle + '>' +
      '<td style="padding-left:1.5rem">' + ssidPill(n) +
        (n.hidden ? badge('Hidden', 'bg-secondary-lt') : '') +
        (n.isVirtual ? badge('Virtual AP', 'bg-azure-lt') : '') +
        // Says WHY the row will not open, which is the difference between a
        // read-only table and a broken one.
        (n.readOnlyReason === 'caps' ? badge('CAP', 'bg-purple-lt')
          : n.readOnlyReason === 'capsv1' ? badge('CAPsMAN v1', 'bg-orange-lt')
            : n.readOnlyReason === 'provisioned' ? badge('Provisioned', 'bg-purple-lt') : '') +
        // Saying which profile a value comes from is what makes the override
        // prompt make sense when it appears.
        (n.inherits && n.inherits.ssid
          ? '<div class="muted-note" style="font-size:.7rem;margin-top:.2rem">inherits from ' +
            esc(n.inherits.ssid) + '</div>' : '') + '</td>' +
      '<td>' + esc(n.name) + '</td>' +
      '<td>' + bandCell(n) + '</td>' +
      '<td>' + securityCell(n) + '</td>' +
      '<td>' + esc(n.vlanId || '—') + '</td>' +
      '<td>' + esc(String(n.clients)) + '</td>' +
      '<td>' + stateCell(n) + '</td>' +
    '</tr>';
  }

  /** A group header that is not a radio - the AP view's. */
  function groupHeader(label: string, sub: string, count: number): string {
    return '<tr class="wn-radio-row">' +
      '<td colspan="7" style="background:var(--bg-subtle);font-weight:600;font-size:.76rem">' +
        esc(label) +
        (sub ? '<span class="muted-note" style="margin-left:.5rem;font-weight:400">' +
               esc(sub) + '</span>' : '') +
        '<span class="muted-note" style="float:right;font-weight:400">' +
          esc(String(count)) + (count === 1 ? ' network' : ' networks') + '</span>' +
      '</td></tr>';
  }

  /** One aggregated network, for the SSID view. */
  function ssidRow(r: SsidRow): string {
    const col = colours[r.ssid] || 'var(--text-main)';
    const apNote = r.aps.length
      ? r.aps.length + (r.aps.length === 1 ? ' AP' : ' APs')
      : 'this router';
    const secCls = r.security === 'Open' ? 'bg-red-lt'
      : r.security === 'Mixed' ? 'bg-yellow-lt' : 'bg-azure-lt';
    return '<tr>' +
      '<td><span class="wn-ssid-pill" style="color:' + col + ';border-color:' + col + '">' +
        esc(r.ssid) + '</span>' +
        '<div class="muted-note" style="font-size:.7rem;margin-top:.2rem">' +
          esc(apNote) + '</div></td>' +
      '<td>' + esc(String(r.ifaces.length)) +
        '<div class="muted-note" style="font-size:.68rem">' +
          esc(r.ifaces.map((n) => n.name).join(', ')) + '</div></td>' +
      '<td>' + (r.bands.map(bandBadge).join(' ') || '<span class="muted-note">&mdash;</span>') + '</td>' +
      '<td><span class="badge ' + secCls + '">' + esc(r.security || '\u2014') + '</span></td>' +
      '<td>' + esc(r.vlanId || '\u2014') + '</td>' +
      '<td>' + esc(String(r.clients)) + '</td>' +
      '<td>' + (r.running
        ? '<span class="badge bg-green-lt">Running</span>'
        : '<span class="badge bg-yellow-lt">Not running</span>') + '</td>' +
    '</tr>';
  }

  // -- Sorting ---------------------------------------------------------------
  //
  // ONE STATE FOR EVERY VIEW, because a column means the same thing in all of
  // them. In a grouped view the sort orders rows WITHIN each group and the
  // groups by the order the sorted list first mentions them - the same
  // behaviour the Wifi Clients table has, and the useful one: sorting by
  // Clients floats the busiest radio to the top.
  //
  // Band ranks rather than compares as text; see `bandRank` in dom.ts.
  const CMP: Record<string, (a: WifiNetwork, b: WifiNetwork) => number> = {
    ssid: (a, b) => (a.ssid || '').localeCompare(b.ssid || ''),
    name: (a, b) => a.name.localeCompare(b.name),
    band: (a, b) => bandRank(a.band) - bandRank(b.band),
    security: (a, b) => a.security.localeCompare(b.security),
    vlanId: (a, b) => (a.vlanId || '').localeCompare(b.vlanId || ''),
    clients: (a, b) => a.clients - b.clients,
    state: (a, b) => Number(a.running) - Number(b.running),
  };

  const SSID_CMP: Record<string, (a: SsidRow, b: SsidRow) => number> = {
    ssid: (a, b) => a.ssid.localeCompare(b.ssid),
    name: (a, b) => a.ifaces.length - b.ifaces.length,
    band: (a, b) => bandRank(a.bands[0] || '') - bandRank(b.bands[0] || ''),
    security: (a, b) => a.security.localeCompare(b.security),
    vlanId: (a, b) => a.vlanId.localeCompare(b.vlanId),
    clients: (a, b) => a.clients - b.clients,
    state: (a, b) => Number(a.running) - Number(b.running),
  };

  function applySort<T>(rows: T[], cmp: Record<string, (a: T, b: T) => number>): T[] {
    const fn = cmp[sort.col];
    if (!fn) return rows;
    const out = rows.slice().sort(fn);
    if (sort.dir === 'desc') out.reverse();
    return out;
  }

  const COLS_ROW: SortCol[] = [
    { key: 'ssid', label: 'SSID' },
    { key: 'name', label: 'Interface' },
    { key: 'band', label: 'Band' },
    { key: 'security', label: 'Security' },
    { key: 'vlanId', label: 'VLAN' },
    { key: 'clients', label: 'Clients' },
    { key: 'state', label: 'State' },
  ];
  // The same seven columns, two of them answering the aggregate's question.
  const COLS_SSID: SortCol[] = [
    { key: 'ssid', label: 'SSID' },
    { key: 'name', label: 'Interfaces' },
    { key: 'band', label: 'Bands' },
    { key: 'security', label: 'Security' },
    { key: 'vlanId', label: 'VLAN' },
    { key: 'clients', label: 'Clients' },
    { key: 'state', label: 'State' },
  ];

  /** Rows grouped by a key, in the order the sorted list first mentions each. */
  function groupBy(nets: WifiNetwork[], key: (n: WifiNetwork) => string):
      Array<{ key: string; rows: WifiNetwork[] }> {
    const by = new Map<string, WifiNetwork[]>();
    const order: string[] = [];
    nets.forEach((n) => {
      const k = key(n);
      if (!by.has(k)) { by.set(k, []); order.push(k); }
      by.get(k)!.push(n);
    });
    return order.map((k) => ({ key: k, rows: by.get(k)! }));
  }

  function renderTable(st: WifiPayload): void {
    const tbody = el('wnTable');
    if (!tbody) return;
    const nets = st.networks || [];

    // One colour per UNIQUE SSID, not per row: the same network on 2.4 and 5
    // GHz is one network and has to look like it.
    const unique: string[] = [];
    nets.forEach((n) => {
      if (n.ssid && unique.indexOf(n.ssid) === -1) unique.push(n.ssid);
    });
    colours = ssidColours(unique);

    renderSortHeader('wnThead', view === 'ssid' ? COLS_SSID : COLS_ROW, sort,
      () => renderTable(st));

    const badgeEl = el('wnBadge');
    if (badgeEl) {
      badgeEl.textContent = String(view === 'ssid' ? bySsid(nets).length : nets.length);
    }

    if (!nets.length) {
      const why = st.stack === 'none'
        ? 'This router has no wireless interfaces.'
        : 'No wireless networks are configured.';
      tbody.innerHTML = '<tr><td colspan="7" class="empty-state">' + esc(why) + '</td></tr>';
      return;
    }

    if (view === 'ssid') {
      tbody.innerHTML = applySort(bySsid(nets), SSID_CMP).map(ssidRow).join('');
      return;
    }

    const rows = applySort(nets, CMP);

    if (view === 'flat') {
      tbody.innerHTML = rows.map(networkRow).join('');
      return;
    }

    if (view === 'ap') {
      // "" is this router's own radios, and it is a real group rather than an
      // absence: on a manager that also runs radios of its own, those networks
      // belong to the manager and saying so beats an empty heading.
      tbody.innerHTML = groupBy(rows, (n) => n.ap).map((g) => {
        const label = g.key || 'This router';
        const bands = [...new Set(g.rows.map((n) => n.band).filter(Boolean))]
          .sort((a, b) => bandRank(a) - bandRank(b));
        const clients = g.rows.reduce((t, n) => t + n.clients, 0);
        const sub = [bands.join(' \u00b7 '), clients + (clients === 1 ? ' client' : ' clients')]
          .filter(Boolean).join(' \u00b7 ');
        return groupHeader(label, sub, g.rows.length) + g.rows.map(networkRow).join('');
      }).join('');
      return;
    }

    // radio: each radio's own row first, then its virtual APs - the order the
    // collector already sorted them into, unless a column sort has moved them.
    const radios: Record<string, WifiRadio> = {};
    (st.radios || []).forEach((r) => { radios[r.name] = r; });
    tbody.innerHTML = groupBy(rows, (n) => n.radio).map((g) => {
      const head = radios[g.key] || { name: g.key };
      return radioHeader(head, g.rows.length) + g.rows.map(networkRow).join('');
    }).join('');
  }

  function syncViewBtns(): void {
    const bar = el('wnViewBtns');
    if (!bar) return;
    bar.querySelectorAll('.wl-sort-btn').forEach((b) => {
      b.classList.toggle('active', (b as HTMLElement).dataset.wnview === view);
    });
  }

  function renderSecProfiles(st: WifiPayload): void {
    const card = el('wnSecCard');
    if (!card) return;
    // Only the legacy stack keeps the passphrase in a menu of its own. On
    // modern wifi this card would describe nothing.
    const show = st.stack === 'wireless';
    card.style.display = show ? '' : 'none';
    if (!show) return;

    const rows = st.secProfiles || [];
    const badgeEl = el('wnSecBadge');
    if (badgeEl) badgeEl.textContent = String(rows.length);
    const table = el('wnSecTable');
    if (!table) return;
    table.innerHTML = rows.length
      ? rows.map((p) =>
        '<tr data-id="' + esc(p.id) + '" data-identity="' + esc(p.name) + '"' +
          ' data-res="wlSecProfile">' +
          '<td>' + esc(p.name) + (p.isDefault ? badge('default', 'bg-secondary-lt') : '') + '</td>' +
          '<td>' + esc(p.mode || '—') + '</td>' +
          '<td>' + esc(p.authTypes || 'none') + '</td>' +
        '</tr>').join('')
      : '<tr><td colspan="3" class="empty-state">No security profiles</td></tr>';
  }

  function renderSummary(st: WifiPayload): void {
    const t = st.totals;
    const set = (id: string, v: string) => { const e = el(id); if (e) e.textContent = v; };
    set('wnRadioCount', t == null || t.radios == null ? '—' : String(t.radios));
    set('wnNetCount', t == null || t.networks == null ? '—' : String(t.networks));
    set('wnClientCount', t == null || t.clients == null ? '—' : String(t.clients));

    // The stack note names where the radios came from, and on a router running a
    // legacy manager that is two places. Counted off the rows rather than from a
    // totals field: the payload already says which are v1 and a second count
    // would be a number to keep in step.
    const v1Radios = (st.radios || []).filter((r) => r.readOnlyReason === 'capsv1').length;
    const local = st.stack === 'wifi' ? 'modern (/interface/wifi)'
      : st.stack === 'wireless' ? 'legacy (/interface/wireless)' : '';
    const viaV1 = v1Radios ? v1Radios + ' via CAPsMAN v1' : '';
    set('wnStackNote', [local, viaV1].filter(Boolean).join(' · '));

    const virtual = (st.networks || []).filter((n) => n.isVirtual).length;
    set('wnVirtualNote', virtual ? virtual + (virtual === 1 ? ' virtual AP' : ' virtual APs') : '');

    const caps = (t && t.capsManaged) || 0;
    set('wnCapNote', caps
      ? caps + (caps === 1 ? ' network is CAP-managed' : ' networks are CAP-managed')
      : '');
  }

  /**
   * Why the whole table is read-only, when it is.
   *
   * A router that provisions its own radios through CAPsMAN reports every
   * interface as dynamic, and a table where nothing opens looks broken rather
   * than deliberate. Saying so once above the rows costs a line and answers the
   * question before it is asked.
   */
  function renderNote(st: WifiPayload): void {
    const note = el('wnNote');
    if (!note) return;
    const nets = st.networks || [];
    const ro = (st.totals && st.totals.readOnly) || 0;
    note.style.color = '';
    if (!nets.length || !ro) { note.textContent = ''; return; }
    note.textContent = ro === nets.length
      ? 'Every network here is provisioned by CAPsMAN — edit them on the CAPsMAN page, not here.'
      : ro + ' of these are provisioned by CAPsMAN and cannot be edited here.';
  }

  function render(): void {
    if (!state) return;
    renderSummary(state);
    renderTable(state);
    renderNote(state);
    renderSecProfiles(state);
    // The Add buttons and the row click handlers belong to the resource dialog;
    // it re-reads its mounts when told the table changed.
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  el('wnViewBtns')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('.wl-sort-btn') as HTMLElement | null;
    const next = btn?.dataset.wnview as WnView | undefined;
    if (!next || next === view) return;
    view = next;
    saveView(view);
    syncViewBtns();
    render();
  });

  syncViewBtns();

  socket.on('wifi:update', (d) => {
    state = d || null;
    render();
  });

  document.addEventListener('mikrodash:pagechange', (ev) => {
    if ((ev as CustomEvent).detail === 'wifi-networks') render();
  });

  socket.on('router:switched', () => {
    // The previous router's radios are not this one's, and leaving them on
    // screen would offer an edit against rows that no longer exist.
    state = null;
    const tbody = el('wnTable');
    if (tbody) tbody.innerHTML = '';
    const card = el('wnSecCard');
    if (card) card.style.display = 'none';
  });

  void isVisible;
}
