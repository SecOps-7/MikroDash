// The DHCP page — a port of the DHCP renderers in public/app.js.
//
// NOT AN IIFE OVER THERE, and that shapes this file. The live renderer is
// top-level code in two places: the subnet table and the utilisation gauge live
// inside the `lan:overview` handler — which also draws the dashboard's LAN card,
// so one handler serves two pages — and the leases table is a separate block a
// thousand lines further down. `tools/live-renderer.js` grew a range mode to
// lift them; this file puts them back together as one page module.
//
// The markup strings, class names, em dashes and ellipses are the live app's.
// The acceptance criterion is that it renders identically, not that it renders
// correctly.

import { esc, el, resRow } from '../dom';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';

export interface Lease {
  ip: string; name: string; mac: string; hostName: string; comment: string;
  status: string; server: string; iface: string; vlanId: string;
  id: string; dynamic: boolean;
}

export interface LeaseServer {
  name: string; iface: string; vlanId: string; count: number;
}

export interface LeasesPayload { ts: number; leases: Lease[]; servers: LeaseServer[] }

export interface LanNetwork {
  cidr: string; gateway: string; dns: string; leaseCount: number; poolSize: number;
}

export interface LanPayload {
  ts: number; lanCidrs: string[]; networks: LanNetwork[]; wanIp: string;
  totalPoolSize: number; totalLeases: number; pollMs: number;
  internetIfaces: Array<{ name: string; ip: string }>;
}

const SORT_COLS = [
  { id: 'dhcpThName', key: 'name' },
  { id: 'dhcpThIp', key: 'ip' },
  { id: 'dhcpThMac', key: 'mac' },
  { id: 'dhcpThStatus', key: 'status' },
];

export function initDhcpPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let sortKey = 'ip';
  let sortDir = 1;
  let leaseFilter = '';
  let leaseServerFilter = '';
  let allLeases: Lease[] = [];
  // Held from lan:overview so the gauge can be redrawn from a leases:list update
  // without waiting for the next networks read.
  let totalPoolSize = 0;
  // null until the first lan:overview: see renderDhcpGauge for why the
  // fallback matters and why it is only a fallback.
  let totalLeases: number | null = null;

  function refreshSortHeaders(): void {
    for (const c of SORT_COLS) {
      const th = el(c.id);
      if (!th) continue;
      th.className = c.key === sortKey ? (sortDir === 1 ? 'sort-asc' : 'sort-desc') : '';
    }
  }

  // The IP column sorts NUMERICALLY, octet by octet, and the JavaScript it is
  // ported from does that with `(a.ip||'').split('.').map(Number)`.
  //
  // THAT GOES NaN ON AN IPv6 ADDRESS: there are no dots to split on, so the
  // single element is `Number('fe80::1')`, which is NaN. `NaN !== NaN` is true,
  // so the comparator reaches `dir * (NaN - NaN)` and returns NaN, which
  // Array.prototype.sort treats as zero — every IPv6 lease compares equal to
  // every other and they keep their input order. Reproduced rather than fixed:
  // this is a DHCP lease table, the addresses are IPv4 in practice, and a
  // "corrected" order would be a visible difference from the live page for a
  // defect nobody has hit.
  function octets(ip: string): number[] {
    return (ip || '').split('.').map((p) => Number(p));
  }

  function sortLeases(leases: Lease[]): Lease[] {
    return leases.slice().sort((a, b) => {
      if (sortKey === 'ip') {
        const ao = octets(a.ip);
        const bo = octets(b.ip);
        for (let i = 0; i < 4; i++) {
          const av = ao[i];
          const bv = bo[i];
          if (av === undefined && bv === undefined) continue;
          if (av !== bv) {
            const d = sortDir * ((av as number) - (bv as number));
            return Number.isNaN(d) ? 0 : d;
          }
        }
        return 0;
      }
      let av = '';
      let bv = '';
      if (sortKey === 'name') {
        av = (a.name || a.hostName || '').toLowerCase();
        bv = (b.name || b.hostName || '').toLowerCase();
      } else if (sortKey === 'mac') {
        av = (a.mac || '').toLowerCase();
        bv = (b.mac || '').toLowerCase();
      } else if (sortKey === 'status') {
        av = (a.status || '').toLowerCase();
        bv = (b.status || '').toLowerCase();
      }
      return sortDir * av.localeCompare(bv);
    });
  }

  function renderDhcp(leases: Lease[]): void {
    const table = el('dhcpTable');
    if (!table) return;

    // Server filter first, then free text — the two compose, so you can search
    // within one VLAN rather than having to choose between the controls.
    let filtered = leaseServerFilter
      ? leases.filter((l) => (l.server || '') === leaseServerFilter)
      : leases;
    if (leaseFilter) {
      filtered = filtered.filter((l) => {
        const hay = (l.name + ' ' + l.ip + ' ' + l.mac + ' ' + (l.comment || '')).toLowerCase();
        return hay.indexOf(leaseFilter) !== -1;
      });
    }

    // The badge counts ALL leases, not the filtered ones: it is the size of the
    // table, and a filter is a view of it.
    const count = leases.length;
    const badge = el('dhcpTotalBadge');
    if (badge) {
      badge.textContent = String(count);
      badge.className = 'card-badge' + (count > 0 ? ' active-blue' : '');
    }

    if (!filtered.length) {
      table.innerHTML = '<tr><td colspan="4" class="empty-state">No leases' +
        ((leaseFilter || leaseServerFilter) ? ' matching filter' : '') + '…</td></tr>';
      return;
    }
    table.innerHTML = sortLeases(filtered).map((l) => {
      const st = (l.status || '').toLowerCase();
      const pillCls = st === 'bound' ? 'bound'
        : (st === 'waiting' || st === 'offered') ? 'waiting' : 'expired';
      return '<tr' + resRow(l.id, l.mac) + '>' +
        '<td style="font-weight:600">' + esc(l.name || l.hostName || '—') + '</td>' +
        '<td style="color:var(--accent-rx)">' + esc(l.ip) + '</td>' +
        '<td style="font-size:.7rem;color:var(--text-muted)">' + esc(l.mac || '—') + '</td>' +
        '<td><span class="lease-pill ' + pillCls + '">' + esc(l.status || '?') + '</span></td>' +
        '</tr>';
    }).join('');
  }

  // The gauge is a 120° arc from 210° to 330°, centred at (100,105) with r=72.
  //
  // The coordinates go through `+(...).toFixed(2)` — rounded to two places and
  // then read back as a NUMBER, so 37.60 renders as "37.6" and not "37.60". The
  // `d` attribute is compared character by character, so the difference between
  // those two is a failed comparison.
  function renderDhcpGauge(): void {
    const fill = el('dhcpGaugeFill');
    const track = el('dhcpGaugeTrack');
    const pctEl = el('dhcpGaugePct');
    if (!fill || !track) return;

    // THE SERVER'S COUNT, not the length of the lease TABLE.
    //
    // They answer different questions. The table lists every lease including
    // `waiting` reservations — addresses nobody currently holds — while the
    // gauge asks how much of the pool a new client could NOT be given. Taking
    // the row count made this gauge read 99% directly above per-subnet bars
    // reading 22% (live issue #115), and both numerators must come from the same
    // place or they disagree on screen.
    //
    // Falls back to the row count only before the first `lan:overview` arrives,
    // so a cold load shows something rather than zero.
    const totalUsed = totalLeases != null ? totalLeases : allLeases.length;
    const usedPct = totalPoolSize > 0 ? Math.round((totalUsed / totalPoolSize) * 100) : 0;

    const cx = 100, cy = 105, r = 72, startDeg = 210, totalDeg = 120;
    const xy = (deg: number): { x: number; y: number } => {
      const rad = deg * Math.PI / 180;
      return { x: +(cx + r * Math.cos(rad)).toFixed(2), y: +(cy + r * Math.sin(rad)).toFixed(2) };
    };
    const sa = xy(startDeg);
    const ea = xy(startDeg + totalDeg);
    track.setAttribute('d', 'M' + sa.x + ',' + sa.y + ' A' + r + ',' + r + ' 0 0,1 ' + ea.x + ',' + ea.y);

    const fillDeg = totalDeg * (Math.min(100, usedPct) / 100);
    if (fillDeg > 0.5) {
      const fa = xy(startDeg + fillDeg);
      fill.setAttribute('d', 'M' + sa.x + ',' + sa.y + ' A' + r + ',' + r + ' 0 ' +
        (fillDeg > 180 ? 1 : 0) + ',1 ' + fa.x + ',' + fa.y);
    } else {
      fill.setAttribute('d', '');
    }
    const colour = usedPct >= 90 ? '#f87171' : usedPct >= 70 ? '#fbbf24' : '#38bdf8';
    fill.setAttribute('stroke', colour);
    if (pctEl) {
      pctEl.textContent = totalPoolSize > 0 ? (usedPct + '%') : '—';
      pctEl.setAttribute('fill', colour);
    }
  }

  // One control covers the interface, DHCP-server and VLAN filters: on a real
  // config a DHCP server binds to exactly one interface and that interface is
  // the VLAN, so the three are the same axis. Each option names the server and
  // shows its interface and VLAN as context; a server on a plain ether
  // interface just has no VLAN segment.
  function renderServerOptions(servers: LeaseServer[]): void {
    const sel = el<HTMLSelectElement>('dhcpServerFilter');
    if (!sel) return;
    if (!servers || !servers.length) { sel.style.display = 'none'; return; }
    sel.style.display = '';

    let html = '<option value="">All leases (' + allLeases.length + ')</option>';
    html += servers.map((s) => {
      const bits = [s.name];
      if (s.iface && s.iface !== s.name) bits.push(s.iface);
      if (s.vlanId) bits.push('VLAN ' + s.vlanId);
      return '<option value="' + esc(s.name) + '">' + esc(bits.join(' · ')) +
        ' (' + s.count + ')</option>';
    }).join('');
    sel.innerHTML = html;

    // A server can disappear between updates (config change); fall back to All.
    if (leaseServerFilter && !servers.some((s) => s.name === leaseServerFilter)) {
      leaseServerFilter = '';
    }
    sel.value = leaseServerFilter;
  }

  function renderSubnets(nets: LanNetwork[]): void {
    const host = el('dhcpSubnetTable');
    if (!host) return;
    const rows = nets.map((n) => {
      const used = n.leaseCount || 0;
      const pool = n.poolSize || 0;
      const pct = pool > 0 ? Math.round((used / pool) * 100) : 0;
      const fillColour = pct >= 90 ? '#f87171' : pct >= 70 ? '#fbbf24' : '#34d399';
      const poolLabel = pool > 0 ? (used + ' / ' + pool) : ('' + used + ' leases');
      const pctLabel = pool > 0 ? (' (' + pct + '%)') : '';
      return '<tr>' +
        '<td style="font-size:.76rem;font-family:var(--font-mono);color:var(--accent-rx)">' + esc(n.cidr) + '</td>' +
        '<td class="td-label">' + esc(n.gateway || '—') + '</td>' +
        '<td class="td-label">' + esc(n.dns || '—') + '</td>' +
        '<td>' +
          '<span style="font-size:.72rem;color:var(--text-main)">' + poolLabel +
          '<span style="color:var(--text-muted)">' + pctLabel + '</span></span>' +
          (pool > 0 ? '<div class="dhcp-util-bar"><div class="dhcp-util-fill" style="width:' +
            Math.min(100, pct) + '%;background:' + fillColour + '"></div></div>' : '') + '</td>' +
      '</tr>';
    }).join('');
    host.innerHTML = '<table class="dhcp-subnet-table">' +
      '<thead><tr><th>Subnet</th><th>Gateway</th><th>DNS</th><th>Leases</th></tr></thead>' +
      '<tbody>' + rows + '</tbody></table>';
  }

  // ── the network diagram's WAN readout ─────────────────────────────────────
  //
  // `lan:wan` is emitted ROUTER-WIDE rather than page-scoped (#108), so it
  // arrives whatever page is open — which is the point: `#ndWanIp` is chrome on
  // the dashboard's diagram, not part of this page.
  //
  // THE LIVE HANDLER DOES THREE THINGS AND ONLY ONE OF THEM EXISTS. It also
  // calls `window._wanGeoDetect`, which is assigned nowhere in the live repo,
  // and writes `wanIpDisplay`, which is in that repo's own KNOWN orphan set.
  // Reproducing either would mean adding a lookup for an element that does not
  // exist and a call to a function nobody defines — no behaviour, and this
  // port's own `lookup-audit` would then have to carry them as orphans it
  // invented. Reported as ToDo.md #23; only the working statement is ported.
  //
  // `.split('/')[0]` because the collector reports the WAN address WITH its
  // prefix and the diagram shows a bare address; the em dash is the original's
  // empty case, not a guard added here.
  socket.on('lan:wan', (d: { ts?: number; wanIp?: string }) => {
    const node = el('ndWanIp');
    if (!node) return;
    node.textContent = ((d && d.wanIp) || '').split('/')[0] || '\u2014';
  });

  socket.on('lan:overview', (d: LanPayload) => {
    const nets = (d && d.networks) ? d.networks : [];
    // AN EMPTY PAYLOAD RETURNS EARLY, and this is the live behaviour rather than
    // an oversight in the port. Over there the handler bails before it reaches
    // the DHCP page at all — it clears the dashboard's LAN card and returns — so
    // a router with no DHCP networks leaves the subnet table showing "Waiting
    // for network data…" and the gauge untouched, indefinitely. The branch
    // inside the subnet block that renders "No DHCP networks" is unreachable for
    // exactly that reason.
    //
    // It is reproduced because it is visible: rendering the empty state here
    // would be a page that differs from the live one, which is the single thing
    // this port may not do. Reported in ../MikroDash/ToDo.md.
    if (!nets.length) return;
    renderSubnets(nets);
    totalPoolSize = d.totalPoolSize || 0;
    // `typeof === number` and not `|| 0`: a legitimately ZERO count must not
    // fall back to the lease-table length, which is exactly the case a router
    // whose every lease is a `waiting` reservation produces.
    totalLeases = typeof d.totalLeases === 'number' ? d.totalLeases : null;
    renderDhcpGauge();
  });

  socket.on('leases:list', (d: LeasesPayload) => {
    allLeases = d.leases || [];
    renderServerOptions(d.servers);
    renderDhcp(allLeases);
    renderDhcpGauge(); // update the gauge with the fresh lease count
  });

  for (const col of SORT_COLS) {
    el(col.id)?.addEventListener('click', () => {
      if (sortKey === col.key) sortDir *= -1;
      else { sortKey = col.key; sortDir = 1; }
      refreshSortHeaders();
      renderDhcp(allLeases);
    });
  }
  refreshSortHeaders();

  const search = el<HTMLInputElement>('dhcpSearch');
  search?.addEventListener('input', () => {
    leaseFilter = (search.value || '').trim().toLowerCase();
    renderDhcp(allLeases);
  });

  const serverSel = el<HTMLSelectElement>('dhcpServerFilter');
  serverSel?.addEventListener('change', () => {
    leaseServerFilter = serverSel.value || '';
    renderDhcp(allLeases);
  });

  mountAdds(socket);
  mountRows(socket);

  void isVisible;
}
