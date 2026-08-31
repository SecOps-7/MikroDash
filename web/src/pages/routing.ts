// The Routing page — a port of the Routing IIFE in public/app.js.
//
// The biggest page ported so far, and the first with a CHART. Three things carry
// the work: a routes table and a BGP peers table, each with its own filters and
// sort, and a doughnut of routes-by-protocol.
//
// THE CHART IS NOT COVERED BY THE DOM COMPARISON, and that is worth saying
// rather than letting a green diff imply otherwise. `tools/live-renderer.js`
// compares innerHTML, and a Chart.js doughnut is pixels on a canvas. What the
// port guarantees instead is the same library driven by the same configuration:
// the dataset, the colours, the cutout, the centre-label plugin and the
// exclusions below are reproduced exactly, so a difference would have to come
// from Chart.js itself. The library is the one the live app already serves at
// /vendor/chart.umd.min.js — the Go server proxies everything outside /next, so
// it is the identical file rather than a second copy.

import { esc, el, resRow } from '../dom';
import { mountAdds, mountRows } from '../resource';
import type { Socket } from '../socket';
import type { RoutingPayload, Peer, Route, RouteCounts, PeerSummary } from './routing-types';

// Chart.js is loaded by the shell from /vendor, so it is a global here rather
// than an import. Typed loosely on purpose: the port does not own the library's
// types, and pinning them would be a second thing to keep in step.
interface ChartLike {
  data: { labels: string[]; datasets: Array<{ data: number[]; backgroundColor: string[] }> };
  update(mode?: string): void;
}
declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

const DONUT_COLORS: Record<string, string> = {
  static: 'rgba(56,189,248,.85)',
  dynamic: 'rgba(251,191,36,.85)',
  bgp: 'rgba(167,139,250,.85)',
  ospf: 'rgba(251,113,133,.85)',
  other: 'rgba(99,130,190,.4)',
};
const DONUT_LABELS: Record<string, string> = {
  static: 'Static', dynamic: 'Dynamic', bgp: 'BGP', ospf: 'OSPF', other: 'Other',
};

// established first, then the rest of the BGP state machine in order.
const STATE_ORDER: Record<string, number> = {
  established: 0, active: 1, connect: 2, opensent: 3, openconfirm: 4, idle: 5,
};

function fmtUptime(sec: number): string {
  if (!sec) return '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return d + 'd ' + h + 'h';
  if (h > 0) return h + 'h ' + m + 'm';
  return m + 'm';
}

function stateBadge(state: string, flapping: boolean): string {
  if (flapping) return '<span class="bgp-state flap">Flapping</span>';
  // The class is the state with everything but letters and hyphens removed, so
  // an unrecognised state from a future RouterOS cannot inject a class name.
  return '<span class="bgp-state ' + state.replace(/[^a-z-]/gi, '') + '">' + esc(state) + '</span>';
}

// Inline SVG sparkline from the prefix-count history.
function sparkSvg(history: number[] | undefined): string {
  if (!history || history.length < 2) return '<svg width="80" height="20"></svg>';
  const min = Math.min.apply(null, history);
  const max = Math.max.apply(null, history);
  const range = max - min || 1;
  const w = 80, h = 20, pad = 2;
  const pts = history.map((v, i) => {
    const x = pad + (i / (history.length - 1)) * (w - pad * 2);
    const y = h - pad - ((v - min) / range) * (h - pad * 2);
    return x.toFixed(1) + ',' + y.toFixed(1);
  });
  return '<svg class="rt-spark" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '">' +
    '<polyline points="' + pts.join(' ') + '" fill="none" stroke="rgba(167,139,250,.7)" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>' +
    '</svg>';
}

export function initRoutingPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbody = el('rtTbody');
  const search = el<HTMLInputElement>('rtSearch');
  const selState = el<HTMLSelectElement>('rtSelState');
  const selType = el<HTMLSelectElement>('rtSelType');
  const selIpver = el<HTMLSelectElement>('rtSelIpver');

  let data: RoutingPayload | null = null;
  let sortKey = 'state';
  let sortDir = 1;

  let donut: ChartLike | null = null;
  let donutTotal = 0;

  // ── peers: filter + sort ───────────────────────────────────────────────────

  function filterPeers(peers: Peer[]): Peer[] {
    const q = (search?.value || '').toLowerCase().trim();
    const state = selState?.value || '';
    const type = selType?.value || '';
    const ipver = selIpver?.value || '';
    return peers.filter((p) => {
      if (state && p.state !== state) return false;
      if (type && p.peerType !== type) return false;
      if (ipver === '6' && !p.remoteAddr.includes(':')) return false;
      if (ipver === '4' && p.remoteAddr.includes(':')) return false;
      if (q) {
        const hay = (p.name + ' ' + p.remoteAddr + ' ' + p.remoteAs + ' ' + p.description).toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }

  function sortPeers(peers: Peer[]): Peer[] {
    return peers.slice().sort((a, b) => {
      let av: string | number, bv: string | number;
      if (sortKey === 'name') { av = a.name; bv = b.name; }
      else if (sortKey === 'addr') { av = a.remoteAddr; bv = b.remoteAddr; }
      else if (sortKey === 'as') { av = a.remoteAs; bv = b.remoteAs; }
      else if (sortKey === 'state') {
        // Assigned first: TypeScript will not narrow an index signature through
        // the ternary the JavaScript original uses. Same answer — an unknown
        // state sorts after every known one.
        const ao = STATE_ORDER[a.state], bo = STATE_ORDER[b.state];
        av = ao === undefined ? 9 : ao;
        bv = bo === undefined ? 9 : bo;
      } else if (sortKey === 'uptime') { av = a.uptimeSec; bv = b.uptimeSec; }
      else if (sortKey === 'prefixes') { av = a.prefixes; bv = b.prefixes; }
      else if (sortKey === 'sent') { av = a.updatesSent; bv = b.updatesSent; }
      else if (sortKey === 'recv') { av = a.updatesRecv; bv = b.updatesRecv; }
      else { av = 0; bv = 0; }
      if (typeof av === 'string') return sortDir * av.localeCompare(bv as string);
      return sortDir * (av - (bv as number));
    });
  }

  // ── doughnut ───────────────────────────────────────────────────────────────

  function updateDonut(rc: Partial<RouteCounts>): void {
    const canvas = el<HTMLCanvasElement>('rtDonutCanvas');
    if (!canvas) return;

    // Connected is EXCLUDED from the doughnut and shown in the count grid only —
    // but it is still counted as "known", so Other means unclassified rather
    // than "everything the four slices left out".
    const keys = ['static', 'dynamic', 'bgp', 'ospf'] as const;
    const known = keys.reduce((a, k) => a + (rc[k] || 0), 0) + (rc.connect || 0);
    const other = Math.max(0, (rc.total || 0) - known);
    const dataKeys: string[] = other > 0 ? [...keys, 'other'] : [...keys];
    const vals = keys.map((k) => rc[k] || 0).concat(other > 0 ? [other] : []);
    // dataKeys only ever holds keys DONUT_COLORS defines, so the fallback is
    // unreachable — it is here because the compiler cannot know that.
    const colors = dataKeys.map((k) => DONUT_COLORS[k] ?? DONUT_COLORS.other ?? '');

    donutTotal = rc.total || 0;

    // Chart.js is served by the proxy. Without it the page still renders every
    // table — a missing chart is a worse page, a thrown ReferenceError is a
    // blank one.
    if (typeof Chart === 'undefined') return;

    if (!donut) {
      donut = new Chart(canvas, {
        type: 'doughnut',
        data: {
          labels: dataKeys.map((k) => DONUT_LABELS[k] || k),
          datasets: [{
            data: vals, backgroundColor: colors, borderWidth: 1,
            borderColor: 'rgba(0,0,0,.15)', hoverOffset: 4,
          }],
        },
        options: {
          cutout: '68%',
          animation: { duration: 400 },
          plugins: {
            legend: { display: false },
            tooltip: {
              callbacks: {
                label: (ctx: { label: string; parsed: number }) => ' ' + ctx.label + ': ' + ctx.parsed,
              },
            },
          },
          responsive: false,
        },
        plugins: [{
          // The total in the middle of the ring, drawn after the arcs. The theme
          // colour is read at draw time rather than captured, so the number
          // follows a theme switch.
          afterDraw: (chart: {
            ctx: CanvasRenderingContext2D;
            chartArea: { left: number; right: number; top: number; bottom: number };
          }) => {
            const ctx = chart.ctx;
            const cx = (chart.chartArea.left + chart.chartArea.right) / 2;
            const cy = (chart.chartArea.top + chart.chartArea.bottom) / 2;
            const color = getComputedStyle(document.documentElement)
              .getPropertyValue('--text-main').trim() || 'rgba(200,215,240,.9)';
            ctx.save();
            ctx.textAlign = 'center';
            ctx.textBaseline = 'middle';
            ctx.font = "bold 26px 'JetBrains Mono',ui-monospace,monospace";
            ctx.fillStyle = color;
            ctx.fillText(String(donutTotal || '—'), cx, cy);
            ctx.restore();
          },
        }],
      });
    } else {
      donut.data.labels = dataKeys.map((k) => DONUT_LABELS[k] || k);
      const ds = donut.data.datasets[0];
      if (ds) {
        ds.data = vals;
        ds.backgroundColor = colors;
      }
      donut.update('none');
    }
  }

  // ── summary ────────────────────────────────────────────────────────────────

  function updateSummary(d: RoutingPayload): void {
    const rc = (d.routeCounts || {}) as Partial<RouteCounts>;
    const sm = (d.summary || {}) as Partial<PeerSummary>;
    const set = (id: string, v: number | undefined): void => {
      const e = el(id);
      if (e) e.textContent = v !== undefined ? String(v) : '—';
    };
    set('rtTotal', rc.total);
    set('rtConnect', rc.connect);
    set('rtStatic', rc.static);
    set('rtDynamic', rc.dynamic);
    set('rtBgp', rc.bgp);
    set('rtOspf', rc.ospf);
    set('rtBgpTotal', sm.total);
    set('rtBgpEstab', sm.established);
    set('rtBgpDown', sm.down);
    updateDonut(rc);
  }

  // ── peers table ────────────────────────────────────────────────────────────

  function render(): void {
    if (!data || !tbody) return;
    const peers = sortPeers(filterPeers(data.peers || []));

    if (!peers.length) {
      tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;padding:1.5rem;color:var(--text-muted);font-size:.75rem">No BGP peers' +
        ((data.peers || []).length ? ' match current filter' : ' — BGP may not be configured') + '</td></tr>';
      return;
    }

    tbody.innerHTML = peers.map((p) => {
      const typeColors: Record<string, string> = {
        upstream: 'rgba(56,189,248,.1)', ix: 'rgba(167,139,250,.1)', private: 'rgba(251,191,36,.1)',
      };
      const typeText: Record<string, string> = {
        upstream: 'rgba(56,189,248,.8)', ix: 'rgba(167,139,250,.8)', private: 'rgba(251,191,36,.8)',
      };
      const typeLabel: Record<string, string> = { upstream: 'Upstream', ix: 'IX', private: 'Private' };
      const ptype = p.peerType || 'upstream';
      const typeBadge = '<span style="font-size:.6rem;font-family:var(--font-ui);padding:.1rem .35rem;border-radius:3px;' +
        'background:' + (typeColors[ptype] || 'rgba(99,130,190,.1)') + ';color:' + (typeText[ptype] || 'var(--text-muted)') + '">' +
        (typeLabel[ptype] || esc(ptype)) + '</span>';
      const nameCell = '<div class="rt-peer-name">' + esc(p.name) + ' ' + typeBadge + '</div>' +
        (p.description ? '<div class="rt-peer-desc">' + esc(p.description) + '</div>' : '');
      const errCell = p.lastError
        ? '<span title="' + esc(p.lastError) + '" style="font-size:.65rem;color:rgba(251,113,133,.85);cursor:help;display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">⚠ ' + esc(p.lastError) + '</span>'
        : '<span style="color:var(--text-muted);font-size:.65rem">—</span>';
      return '<tr>' +
        '<td>' + nameCell + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.7rem">' + esc(p.remoteAddr) + '</td>' +
        '<td style="font-family:var(--font-mono)">' + (p.remoteAs || '—') + '</td>' +
        '<td>' + stateBadge(p.state, p.flapping) + '</td>' +
        '<td style="font-family:var(--font-mono)">' + fmtUptime(p.uptimeSec) + '</td>' +
        '<td style="font-family:var(--font-mono);text-align:right">' + (p.prefixes || 0).toLocaleString() + '</td>' +
        '<td style="font-family:var(--font-mono);text-align:right">' + (p.updatesSent || 0).toLocaleString() + '</td>' +
        '<td style="font-family:var(--font-mono);text-align:right">' + (p.updatesRecv || 0).toLocaleString() + '</td>' +
        '<td>' + errCell + '</td>' +
        '<td>' + sparkSvg(p.prefixHistory) + '</td>' +
        '</tr>';
    }).join('');
  }

  const sortCols = [
    { id: 'rtThName', key: 'name' }, { id: 'rtThAddr', key: 'addr' },
    { id: 'rtThAs', key: 'as' }, { id: 'rtThState', key: 'state' },
    { id: 'rtThUptime', key: 'uptime' }, { id: 'rtThPfx', key: 'prefixes' },
    { id: 'rtThSent', key: 'sent' }, { id: 'rtThRecv', key: 'recv' },
  ];
  function refreshSortHeaders(): void {
    sortCols.forEach((c) => {
      const e = el(c.id);
      if (!e) return;
      e.className = c.key === sortKey ? (sortDir === 1 ? 'sort-asc' : 'sort-desc') : '';
    });
  }
  sortCols.forEach((col) => {
    const th = el(col.id);
    if (!th) return;
    th.addEventListener('click', () => {
      if (sortKey === col.key) sortDir *= -1;
      // A new column starts ascending for the two textual ones and descending
      // for the numeric ones — biggest-first is what you want from a prefix
      // count, and A-first from a name.
      else { sortKey = col.key; sortDir = col.key === 'state' || col.key === 'name' ? 1 : -1; }
      refreshSortHeaders();
      render();
    });
  });
  refreshSortHeaders();

  [search, selState, selType, selIpver].forEach((e) => {
    if (e) e.addEventListener('input', render);
  });

  // ── routes table ───────────────────────────────────────────────────────────

  const routesTbody = el('rtRoutesTbody');
  const routeSearch = el<HTMLInputElement>('rtRouteSearch');
  const routeSelType = el<HTMLSelectElement>('rtRouteSelType');
  const routeSelFamily = el<HTMLSelectElement>('rtRouteSelFamily');
  const routeSelActive = el<HTMLSelectElement>('rtRouteSelActive');

  let routeSort = 'dst';
  let routeSortDir = 1;

  function filterRoutes(routes: Route[]): Route[] {
    const q = (routeSearch?.value || '').toLowerCase().trim();
    const type = routeSelType?.value || '';
    const family = routeSelFamily?.value || '';
    const active = routeSelActive?.value || '';
    return routes.filter((r) => {
      if (type && r.type !== type) return false;
      if (family && r.family !== family) return false;
      if (active && !r.active) return false;
      if (q && !(r.dst + ' ' + r.gateway + ' ' + r.comment).toLowerCase().includes(q)) return false;
      return true;
    });
  }

  function sortRoutes(routes: Route[]): Route[] {
    return routes.slice().sort((a, b) => {
      let av: string | number, bv: string | number;
      if (routeSort === 'dst') { av = a.dst; bv = b.dst; }
      else if (routeSort === 'gateway') { av = a.gateway; bv = b.gateway; }
      else if (routeSort === 'distance') { av = a.distance; bv = b.distance; }
      else if (routeSort === 'active') { av = a.active ? 0 : 1; bv = b.active ? 0 : 1; }
      else if (routeSort === 'type') { av = a.type; bv = b.type; }
      else { av = 0; bv = 0; }
      if (typeof av === 'string') return routeSortDir * av.localeCompare(bv as string);
      return routeSortDir * (av - (bv as number));
    });
  }

  function renderRoutes(): void {
    if (!data || !routesTbody) return;
    const routes = sortRoutes(filterRoutes(data.routes || []));
    if (!routes.length) {
      routesTbody.innerHTML = '<tr><td colspan="6" style="text-align:center;padding:1.5rem;color:var(--text-muted);font-size:.75rem">No routes' +
        ((data.routes || []).length ? ' match current filter' : '') + '</td></tr>';
      return;
    }
    routesTbody.innerHTML = routes.map((r) => {
      const activeCell = r.active
        ? '<span style="color:rgba(52,211,153,.9);font-size:.7rem">&#10003; Active</span>'
        : '<span style="color:var(--text-muted);font-size:.7rem">—</span>';
      const typeCell = r.type === 'static'
        ? '<span style="font-size:.65rem;padding:.1rem .35rem;border-radius:3px;background:rgba(56,189,248,.1);color:rgba(56,189,248,.8)">Static</span>'
        : '<span style="font-size:.65rem;padding:.1rem .35rem;border-radius:3px;background:rgba(251,191,36,.1);color:rgba(251,191,36,.8)">' +
          (r.protocol !== r.type ? esc(r.protocol.toUpperCase()) : 'Dynamic') + '</span>';
      const familyBadge = r.family === 'ipv6'
        ? '<span style="font-size:.6rem;padding:.1rem .3rem;border-radius:3px;background:rgba(167,139,250,.12);color:rgba(167,139,250,.8);margin-right:.3rem">IPv6</span>'
        : '';
      // The family picks the RouterOS menu, so a v6 row overrides the table's
      // default resource on itself.
      return '<tr' + resRow(r.id, r.dst, r.family === 'ipv6' ? 'route6' : undefined) + '>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + familyBadge + esc(r.dst || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(r.gateway || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);text-align:right">' + r.distance + '</td>' +
        '<td>' + activeCell + '</td>' +
        '<td>' + typeCell + '</td>' +
        '<td style="font-size:.7rem;color:var(--text-muted)">' + esc(r.comment || '—') + '</td>' +
        '</tr>';
    }).join('');
  }

  const routeSortCols = [
    { id: 'rtRThDst', key: 'dst' }, { id: 'rtRThGw', key: 'gateway' },
    { id: 'rtRThDist', key: 'distance' }, { id: 'rtRThActive', key: 'active' },
    { id: 'rtRThType', key: 'type' },
  ];
  function refreshRouteSortHeaders(): void {
    routeSortCols.forEach((c) => {
      const e = el(c.id);
      if (!e) return;
      e.className = c.key === routeSort ? (routeSortDir === 1 ? 'sort-asc' : 'sort-desc') : '';
    });
  }
  routeSortCols.forEach((col) => {
    const th = el(col.id);
    if (!th) return;
    th.addEventListener('click', () => {
      if (routeSort === col.key) routeSortDir *= -1;
      // A NEW COLUMN ALWAYS STARTS ASCENDING. The live line reads
      // `col.key === 'active' || col.key === 'distance' ? 1 : 1` — both branches
      // are 1, so the condition decides nothing. Reproduced as the behaviour it
      // actually has rather than as the choice it looks like it is making.
      else { routeSort = col.key; routeSortDir = 1; }
      refreshRouteSortHeaders();
      renderRoutes();
    });
  });
  refreshRouteSortHeaders();

  [routeSearch, routeSelType, routeSelFamily, routeSelActive].forEach((e) => {
    if (e) e.addEventListener('input', renderRoutes);
  });

  // ── tabs ───────────────────────────────────────────────────────────────────

  const RT_TABS: Record<string, () => void> = { routes: renderRoutes, bgp: render };
  // The card is shared, so its title and its filter group belong to whichever
  // tab is showing.
  const RT_TAB_META: Record<string, { title: string; filters: string }> = {
    routes: { title: 'Static & Dynamic Routes', filters: 'rtRoutesFilters' },
    bgp: { title: 'BGP Peers', filters: 'rtPeersFilters' },
  };
  let rtTab = 'routes';

  // Render whichever panel is on screen. Rendering only the visible one matters
  // in both directions: a payload arriving while a panel is hidden must not be
  // dropped, or the panel sits on "Waiting for data…" the first time it is
  // opened; and there is no point building rows nobody can see.
  function renderActiveTab(): void {
    const fn = RT_TABS[rtTab];
    if (fn) fn();
  }

  function setRtTab(key: string): void {
    if (!RT_TABS[key]) key = 'routes';
    rtTab = key;
    const bar = el('rtTabBar');
    if (bar) {
      // Scoped to this bar and this page. The Reports switcher this is modelled
      // on queries document-wide, which is safe only while exactly one such
      // strip exists.
      bar.querySelectorAll('.stab').forEach((b) => {
        const on = b.getAttribute('data-rttab') === key;
        b.classList.toggle('active', on);
        b.setAttribute('aria-selected', on ? 'true' : 'false');
      });
    }
    const page = el('page-routing');
    if (page) {
      page.querySelectorAll('.rttab-panel').forEach((p) => {
        p.classList.toggle('active', p.id === 'rttab-' + key);
      });
    }
    const title = el('rtCardTitle');
    const meta = RT_TAB_META[key];
    if (title && meta) title.textContent = meta.title;
    Object.entries(RT_TAB_META).forEach(([k, m]) => {
      const f = el(m.filters);
      if (f) f.hidden = k !== key;
    });
    renderActiveTab();
  }

  const bar = el('rtTabBar');
  if (bar) {
    bar.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      const btn = t && t.closest ? t.closest('[data-rttab]') : null;
      if (btn) setRtTab(btn.getAttribute('data-rttab') || 'routes');
    });
    // Arrow-key movement along the strip, per the ARIA tablist pattern.
    bar.addEventListener('keydown', (ev) => {
      const e = ev as KeyboardEvent;
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      const btns = Array.from(bar.querySelectorAll<HTMLElement>('[data-rttab]'));
      const i = btns.findIndex((b) => b.getAttribute('data-rttab') === rtTab);
      if (i === -1) return;
      e.preventDefault();
      const next = btns[(i + (e.key === 'ArrowRight' ? 1 : btns.length - 1)) % btns.length];
      if (!next) return;
      setRtTab(next.getAttribute('data-rttab') || 'routes');
      next.focus();
    });
  }

  socket.on('routing:update', (d: RoutingPayload) => {
    data = d;
    updateSummary(d);
    if (isVisible('routing')) renderActiveTab();
  });

  // The write path: the Add slot declares `route,route6` and the routes tbody
  // declares `data-res-rows="route"`, with each v6 row overriding itself. Both
  // are the shared implementations, so the button text comes from the schema
  // and the family comes from the row.
  mountAdds(socket);
  mountRows(socket);

  document.addEventListener('mikrodash:pagechange', (e) => {
    // Always back to Routes, the primary tab, rather than wherever you were
    // last. Matches how the Settings and Reports strips behave.
    if ((e as CustomEvent).detail === 'routing') setRtTab('routes');
  });
}
