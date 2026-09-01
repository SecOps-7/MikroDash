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

import { esc, el, bandBadge, ssidColours, installWifiGlobals } from '../dom';
import type { Socket } from '../socket';

export interface WifiInherits {
  ssid: string | null;
  security: string | null;
  channel: string | null;
}

export interface WifiNetwork {
  id: string; name: string; ssid: string; radio: string; master: string;
  isVirtual: boolean; band: string; bandRaw: string;
  security: string; authTypes: string;
  hidden: boolean; vlanId: string; bridge: string;
  disabled: boolean; running: boolean; clients: number; comment: string;
  capsManaged: boolean; profile: string; profileUsedBy: number;
  inherits: WifiInherits | null;
  readOnlyReason: string; editable: boolean; removable: boolean; resource: string;
}

export interface WifiRadio {
  name: string; defaultName: string; mac: string;
  band: string; bandRaw: string; frequency: string; channelWidth: string;
  country: string; disabled: boolean; running: boolean;
  capsManaged: boolean; readOnlyReason: string; profile: string;
}

export interface WifiSecProfile {
  id: string; name: string; mode: string; authTypes: string;
  security: string; isDefault: boolean;
}

export interface WifiTotals {
  radios: number; networks: number; clients: number;
  capsManaged: number; readOnly: number;
}

export interface WifiPayload {
  ts: number; pollMs: number; stack: string; available: boolean;
  radios: WifiRadio[]; networks: WifiNetwork[]; secProfiles: WifiSecProfile[];
  totals: WifiTotals;
}

export function initWifiPage(socket: Socket, isVisible: (page: string) => boolean): void {
  // Published under the names the live app uses, so a LIFTED renderer finds
  // them during a DOM comparison — see installWifiGlobals.
  installWifiGlobals();

  let state: WifiPayload | null = null;
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
        (radio.capsManaged ? badge('CAP', 'bg-purple-lt') : '') +
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
    return '<tr data-id="' + esc(n.id) + '" data-identity="' + esc(n.name) + '"' +
             ' data-res="' + esc(n.resource) + '">' +
      '<td style="padding-left:1.5rem">' + ssidPill(n) +
        (n.hidden ? badge('Hidden', 'bg-secondary-lt') : '') +
        (n.isVirtual ? badge('Virtual AP', 'bg-azure-lt') : '') +
        // Says WHY the row will not open, which is the difference between a
        // read-only table and a broken one.
        (n.readOnlyReason === 'caps' ? badge('CAP', 'bg-purple-lt')
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

    const badgeEl = el('wnBadge');
    if (badgeEl) badgeEl.textContent = String(nets.length);

    if (!nets.length) {
      const why = st.stack === 'none'
        ? 'This router has no wireless interfaces.'
        : 'No wireless networks are configured.';
      tbody.innerHTML = '<tr><td colspan="7" class="empty-state">' + esc(why) + '</td></tr>';
      return;
    }

    // Group by radio, in the order the collector already sorted them: each
    // radio's own row first, then its virtual APs.
    const byRadio: Record<string, WifiNetwork[]> = {};
    const order: string[] = [];
    nets.forEach((n) => {
      if (!byRadio[n.radio]) { byRadio[n.radio] = []; order.push(n.radio); }
      byRadio[n.radio]!.push(n);
    });
    const radios: Record<string, WifiRadio> = {};
    (st.radios || []).forEach((r) => { radios[r.name] = r; });

    tbody.innerHTML = order.map((name) => {
      const rows = byRadio[name]!;
      const head = radios[name] || { name };
      return radioHeader(head, rows.length) + rows.map(networkRow).join('');
    }).join('');
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

    set('wnStackNote',
      st.stack === 'wifi' ? 'modern (/interface/wifi)'
        : st.stack === 'wireless' ? 'legacy (/interface/wireless)' : '');

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

  socket.on('wifi:update', (d: WifiPayload) => {
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
