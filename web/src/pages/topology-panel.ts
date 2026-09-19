// The Network Topology page's detail panel: what a selected device shows, and
// the cabling picker. Split out of topology.ts; it reads and writes the page's
// shared state (TopoState) and reaches the rest of the page through `deps`.

import { esc, el as byId, fmtMbps } from '../dom';
import type { TopoClient } from '../gen/payloads';
import { glyph, TYPE_LABEL } from './topology';
import type { TopoState, TopoNode, Rate } from './topology';

export function topoPanel(st: TopoState, deps: {
  rateFor(iface: string): Rate | null;
  selectNode(key: string | null): void;
  savePins(): void;
}): { renderPanel(): void } {
  // ── detail panel ──────────────────────────────────────────────────────────

  function row(k: string, v: unknown): string {
    if (v === undefined || v === null || v === '') return '';
    return '<dt>' + esc(k) + '</dt><dd>' + esc(v) + '</dd>';
  }

  /** The name of the device this one sits behind. */
  function parentName(n: TopoNode): string {
    if (!st.data) return '';
    if (n.kind !== 'core' && n.parent === 'core') return st.data.nodes[0]?.name || '';
    if (n.kind === 'core' || !n.parent) return '';
    for (const m of st.data.nodes) if (m.key === n.parent) return m.name || n.parent;
    return n.parent;
  }

  function statusVar(n: TopoNode): string {
    if (n.kind === 'core') return '--accent-rx';
    if (n.status === 'up') return '--accent-ok';
    if (n.status === 'warn') return '--accent-warn';
    if (n.status === 'down') return '--accent-err';
    return '--text-muted';
  }



  function renderClientPanel(panel: HTMLElement, n: TopoClient): void {
    panel.innerHTML =
      '<div class="topo-panel-hdr" style="color:var(' +
        (n.type === 'wifi-client' ? '--accent-rx' : '--accent-tx') + ')">' +
        '<svg viewBox="0 0 24 24">' + glyph(n.type === 'wifi-client' ? 'ap' : 'station') + '</svg>' +
        '<span class="topo-panel-name">' + esc(n.name || n.mac) + '</span>' +
        '<button class="topo-panel-close" id="topoPanelClose" aria-label="Close">&times;</button>' +
      '</div>' +
      '<div class="topo-badges"><span class="topo-badge">' +
        (n.type === 'wifi-client' ? 'Wi-Fi client' : 'Wired client') + '</span>' +
        (n.vlanNames || []).map((v) => '<span class="topo-badge is-vlan">' + esc(v) + '</span>').join('') +
        // Say plainly when the attachment was DEDUCED from a shared port rather
        // than observed, so a wrong guess is visible rather than silent.
        (n.attrib === 'port'
          ? '<span class="topo-badge is-guess" title="Deduced: this device shares a ' +
            'port with that switch. The router cannot see which switch port.">inferred</span>'
          : '') +
      '</div>' +
      '<dl class="topo-kv">' +
        row('IPv4', n.ip) + row('MAC', n.mac) +
        row('VLAN', (n.vlanNames || []).join(', ')) +
        row('Connected to', parentName(n) || 'this router') +
        row('Via', n.port) + row('SSID', n.ssid) +
        row('Signal', n.signal ? n.signal + ' dBm' : '') +
        row('Uptime', n.uptime) +
      '</dl>';
  }

  /**
   * The picker: every infrastructure node this one could hang off.
   *
   * CLIENTS ARE NOT OFFERED. A client is a leaf the graph attributes to whatever
   * it is associated with; hanging a switch off a laptop is not a shape this
   * models, and offering it would be offering a pin that reads as nonsense.
   */
  function pinPicker(key: string): string {
    const rows = (st.data?.nodes || [])
      .filter((m) => m.kind === 'neighbor' && m.key !== key)
      .map((m) => ({ key: m.key, name: m.name || m.key }))
      .sort((a, b) => a.name.localeCompare(b.name));
    const cur = st.pins[key] || '';
    const opt = (v: string, label: string): string =>
      '<option value="' + esc(v) + '"' + (cur === v ? ' selected' : '') + '>' +
      esc(label) + '</option>';
    return '<div class="topo-panel-sec">Cabling</div>' +
      '<div class="topo-pin">' +
        '<select class="rt-sel" id="topoPinSel" aria-label="What this device hangs off">' +
          opt('', 'Work it out (' + esc(parentName(
            (st.data?.nodes || []).find((m) => m.key === key)!) || 'directly attached') + ')') +
          opt('core', 'Directly attached to this router') +
          rows.map((r) => opt(r.key, 'Behind ' + r.name)).join('') +
        '</select>' +
        (st.pins[key]
          ? '<div class="topo-pin-note">Pinned by hand.' +
            (st.pinsEnabled ? '' : ' Pins are switched off, so it is not being applied.') +
            '</div>'
          : '<div class="topo-pin-note">Discovery cannot see through a switch that ' +
            'forwards no LLDP. Set this when the graph puts a device in the wrong place.</div>') +
        pinPortBtn(key) +
      '</div>';
  }

  /**
   * Everything sharing this device's port, which is the shape a dumb switch
   * leaves behind.
   *
   * A SwOS box forwards the discovery protocols and answers no API, so the map
   * gets one flat row of devices on one port with the switch among them, and
   * nothing readable says which of them is behind it. Pinning them one at a
   * time is the same knowledge typed six times.
   */
  function portSiblings(key: string): TopoNode[] {
    const me = (st.data?.nodes || []).find((m) => m.key === key);
    const port = (me && 'port' in me ? me.port : '') || '';
    if (!port) return [];
    return (st.data?.nodes || []).filter((m) =>
      m.kind === 'neighbor' && m.key !== key && 'port' in m && m.port === port);
  }

  function pinPortBtn(key: string): string {
    const sib = portSiblings(key);
    if (!sib.length) return '';
    const me = (st.data?.nodes || []).find((m) => m.key === key);
    const port = (me && 'port' in me ? me.port : '') || '';
    const allMine = sib.every((m) => st.pins[m.key] === key);
    return '<button class="topo-btn topo-pin-port" id="topoPinPort" type="button">' +
      (allMine
        ? 'Put the ' + sib.length + ' back on the router'
        : 'Everything else on ' + esc(port) + ' (' + sib.length + ') is behind this') +
      '</button>';
  }

  function wirePinPortBtn(key: string): void {
    byId('topoPinPort')?.addEventListener('click', () => {
      const sib = portSiblings(key);
      if (!sib.length) return;
      const allMine = sib.every((m) => st.pins[m.key] === key);
      sib.forEach((m) => {
        if (allMine) delete st.pins[m.key];
        else st.pins[m.key] = key;
      });
      deps.savePins();
    });
  }

  function wirePinPicker(key: string): void {
    const selEl = byId<HTMLSelectElement>('topoPinSel');
    if (!selEl) return;
    selEl.addEventListener('change', () => {
      const v = selEl.value;
      if (v) st.pins[key] = v; else delete st.pins[key];
      deps.savePins();
    });
  }

  function renderPanel(): void {
    const panel = byId('topoPanel');
    if (!panel) return;
    // ── NEVER REBUILD A PANEL SOMEBODY IS USING ────────────────────────────
    //
    // The graph republishes between structure polls — the ping loop rebuilds and
    // emits every few seconds — and a full render replaces this panel's markup.
    // With the cabling picker open that destroys the `<select>` mid-choice, so
    // the dropdown snapped shut every couple of seconds and the control was
    // unusable. Reported, and it is the same hazard any future input here would
    // have.
    //
    // Focus is the honest test for "in use": nothing else in the panel takes it,
    // and the next render after the operator tabs or clicks away catches up.
    if (panel.contains(document.activeElement)) return;
    if (!st.sel || !st.data) { panel.className = 'topo-panel'; return; }
    const n = st.data.nodes.find((m) => m.key === st.sel);
    if (!n) { panel.className = 'topo-panel'; return; }

    // The rate on the link INTO this device, so the panel shows the throughput
    // of the cable it hangs off rather than the router's total.
    let rate: Rate | null = null;
    (st.data.edges || []).forEach((e) => {
      if (e.to === n.key) {
        const r = deps.rateFor(e.iface);
        if (r) rate = r;
      }
    });

    const closeBtn = (): void => {
      const c = byId('topoPanelClose');
      if (c) c.addEventListener('click', () => deps.selectNode(null));
    };

    if (n.kind === 'client' && 'attrib' in n) {
      renderClientPanel(panel, n);
      panel.className = 'topo-panel open';
      closeBtn();
      return;
    }

    let badges = '<span class="topo-badge">' + esc(TYPE_LABEL[n.type] || n.type) + '</span>';
    // A GUESS IS LABELLED A GUESS. `caps` means the device advertised what it
    // is; anything else means the type came from its board name or platform.
    if (n.kind !== 'core' && n.typeSource !== 'caps') {
      badges += '<span class="topo-badge is-guess" title="Inferred from the board or platform — this ' +
        'device did not advertise LLDP capabilities">guessed</span>';
    }
    if (n.gone) {
      badges += '<span class="topo-badge" style="border-color:var(--accent-err);color:var(--accent-err)">offline</span>';
    }
    (n.running || []).forEach((r) => { badges += '<span class="topo-badge">' + esc(r) + '</span>'; });

    let live = '';
    if (n.kind !== 'core') {
      live += row('Latency', st.data.pingDenied ? 'unavailable (test policy)'
        : (n.rtt !== null && isFinite(n.rtt) ? n.rtt.toFixed(1) + ' ms' : '—'));
      live += row('Loss', n.loss !== null && isFinite(n.loss) ? n.loss + '%' : '—');
    } else if ('cpuLoad' in n) {
      live += row('CPU', n.cpuLoad !== null && isFinite(n.cpuLoad) ? n.cpuLoad + '%' : '');
      live += row('Memory', n.memPct !== null && isFinite(n.memPct) ? n.memPct + '%' : '');
    }
    if (rate) {
      live += row('Link down', fmtMbps((rate as Rate).rx));
      live += row('Link up', fmtMbps((rate as Rate).tx));
    }

    // What the device ENABLES, falling back to what it merely supports — the
    // same preference the collector applies when classifying it.
    const caps = (n.capsEnabled && n.capsEnabled.length ? n.capsEnabled : (n.caps || [])).join(', ');

    panel.innerHTML =
      '<div class="topo-panel-hdr" style="color:var(' + statusVar(n) + ')">' +
        '<svg viewBox="0 0 24 24">' + glyph(n.type) + '</svg>' +
        '<span class="topo-panel-name">' + esc(n.name || n.key) + '</span>' +
        '<button class="topo-panel-close" id="topoPanelClose" aria-label="Close">&times;</button>' +
      '</div>' +
      '<div class="topo-badges">' + badges + '</div>' +
      '<dl class="topo-kv">' +
        row('IPv4', n.ip) + row('IPv6', n.ip6) + row('MAC', n.mac) +
        row('Board', n.board) + row('Platform', n.platform) + row('Version', n.version) +
        row('Software ID', n.softwareId) + row('Uptime', n.uptime) +
      '</dl>' +
      (live ? '<div class="topo-panel-sec">Live</div><dl class="topo-kv">' + live + '</dl>' : '') +
      '<div class="topo-panel-sec">Discovery</div>' +
      '<dl class="topo-kv">' +
        row('Behind', parentName(n) +
          ('pinned' in n && n.pinned ? ' (pinned)' : '')) +
        row('Router port', n.port || (n.ifaces || []).join(', ')) +
        row('Remote port', n.remoteIface) +
        row('Seen via', (n.via || []).join(', ')) +
        row('Reported by', st.fleetOwner[n.key]) +
        row('Age', n.ageSec !== null && isFinite(n.ageSec) ? n.ageSec + ' s'
          : (n.gone ? 'no longer advertising' : '')) +
        row('Capabilities', caps || (n.kind === 'core' ? '' : 'none advertised')) +
        row('Description', n.description) +
      '</dl>' +
      // The core has nothing to hang off, so it gets no picker.
      (n.kind === 'neighbor' ? pinPicker(n.key) : '');

    panel.className = 'topo-panel open';
    closeBtn();
    if (n.kind === 'neighbor') { wirePinPicker(n.key); wirePinPortBtn(n.key); }
  }

  return { renderPanel };
}
