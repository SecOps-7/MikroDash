// The Connections page — the module that wires the four pieces together.
//
//   connections-map.ts        the tables, the projection, the arc geometry
//   connections-worldmap.ts   the SVG map: paths, arcs, labels, zoom
//   connections-lists.ts      the port and country lists, and their derivations
//   connections-sankey.ts     the flow diagram
//
// This file holds the STATE and the wiring, and nothing that draws. The split is
// what keeps a 1,200-line page readable: three of those four are testable
// without a page, and the one that is not is the one that owns live SVG nodes.
//
// ── THE TWO FILTERS ARE MUTUALLY EXCLUSIVE, AND THAT IS DELIBERATE ──────────
//
// A country filter answers "who talks to Germany"; a client filter answers
// "where does this laptop go". Both at once would answer "does this laptop talk
// to Germany" — a question the payload cannot answer without the cross-matrix
// nobody sends. So selecting one clears the other, in both directions.

import { el, iso2Flag, lsGet, lsSet, renderSortHeader, sortRows, type SortState } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import { CC_NAMES } from './connections-map';
import {
  createWorldMap, attachMapZoom, bindZoomButtons, bindMapTooltip, bindMapFullscreen, type WorldMap,
} from './connections-worldmap';
import {
  SPARK_LEN, syncCountryList, portListHTML, portsFromDests,
  countriesFromSourceDests, clientOptions,
} from './connections-lists';
import { createSankeyThrottle, renderSankey } from './connections-sankey';
import {
  CONN_COLS, connRowHTML, filterConns, pageOf, pagerHTML, sortable,
} from './connections-table';
import type {
  ConnCountry, ConnCountryProto, ConnDestEntry, ConnListPayload, ConnPort, ConnsPayload, ConnsUpdate, Lease,
} from '../gen/payloads';

// The last `conn:update`, with the per-country indexes that arrive on their own
// event (`conn:country-data`) carried forward onto it. A view over two payloads,
// so it is composed from their generated types rather than restated.
type ConnView = ConnsUpdate & Partial<Pick<ConnsPayload, 'countryDests' | 'countryPorts'>>;

export function initConnectionsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const mapSvg = el('worldMap') as unknown as SVGElement | null;
  const listEl = el('connMapList');
  if (!mapSvg && !listEl) return;

  let last: ConnView | null = null;
  let counts: Record<string, number> = {};
  const protoOf: Record<string, ConnCountryProto> = {};
  const cityOf: Record<string, string> = {};
  const sparks: Record<string, number[]> = {};
  let sourceDests: Record<string, ConnDestEntry[]> = {};
  let sourcePorts: Record<string, ConnPort[]> = {};
  let selectedCC: string | null = null;
  let filteredBySrc = '';
  let localCC = 'ZZ';
  let leases: Lease[] = [];

  const sankeySvg = el('sankeySvg') as unknown as SVGElement | null;
  const sankeyEmpty = el('sankeyEmpty');
  const sankey = createSankeyThrottle((srcs, dsts) => {
    if (sankeySvg && sankeyEmpty) renderSankey(sankeySvg, sankeyEmpty, srcs, dsts);
  });

  let map: WorldMap | null = null;
  if (mapSvg) {
    map = createWorldMap(mapSvg, () => {
      // The atlas arrives after the first payload as a rule, so whatever is
      // already known is drawn the moment the paths exist.
      if (last) redrawMap();
    });
    const wrap = el('worldMapWrap');
    if (wrap) {
      const zoom = attachMapZoom(wrap, mapSvg);
      bindZoomButtons(wrap, el('mapZoomIn'), el('mapZoomOut'));
      el('mapZoomReset')?.addEventListener('click', zoom.reset);
      bindMapFullscreen(wrap, mapSvg, zoom, {
        btn: el('mapFullscreenBtn'), overlay: el('mapFsOverlay'), close: el('mapFsClose'),
      });
    }
    // The tooltip lives with the map but reads the PAGE's payload: the map knows
    // where countries are, this knows what is happening in them. `mapTooltip` was
    // in the extracted markup and nothing had ever written to it.
    const tipEl = el('mapTooltip');
    if (tipEl) {
      bindMapTooltip(mapSvg as unknown as HTMLElement, tipEl,
        (cc) => ({ count: counts[cc] || 0, city: cityOf[cc] || '', proto: protoOf[cc] || {} }),
        (cc) => !!map?.hasCountry(cc));
    }
  }

  function pushSpark(cc: string, val: number): void {
    if (!sparks[cc]) sparks[cc] = [];
    sparks[cc]!.push(val);
    if (sparks[cc]!.length > SPARK_LEN) sparks[cc]!.shift();
  }

  function setBadge(n: number): void {
    const badge = el('connMapBadge');
    if (!badge) return;
    badge.textContent = String(n);
    badge.className = 'card-badge' + (n > 0 ? ' active-blue' : '');
  }

  function setSub(text: string): void {
    const sub = el('connMapSub');
    if (sub) sub.textContent = text;
  }

  function redrawMap(): void {
    if (!map) return;
    if (selectedCC) map.select(selectedCC, counts, localCC);
    else {
      map.highlight(counts);
      map.arcs(counts, localCC);
    }
    map.labels(counts);
  }

  /** `cc -> row`, kept across ticks so a row survives a redraw. See ToDo #18. */
  const ccRows: Record<string, HTMLElement> = {};
  let ccClickBound = false;

  function renderCountries(list: ConnCountry[]): void {
    const target = el('connMapList');
    if (!target) return;

    // ── BOUND ONCE ON THE CONTAINER, not per row per tick ──────────────────
    //
    // The old wiring re-bound a listener to every row after every redraw, which
    // is half of what ToDo #18 reported: a click that lands between the redraw
    // and the rebind reaches a detached node and does nothing. Delegating means
    // a row can be moved, or left alone, without its handler going with it.
    if (!ccClickBound) {
      ccClickBound = true;
      target.addEventListener('click', (ev) => {
        const t = ev.target as HTMLElement | null;
        const row = t?.closest?.('.conn-map-row') as HTMLElement | null;
        if (!row || !target.contains(row)) return;
        const cc = row.dataset.cc || '';
        selectCountry(cc === selectedCC ? null : cc);
      });
    }

    syncCountryList(target, list, sparks, selectedCC, ccRows);
    if (list.length) setSub(list.length + ' countries active');
  }

  function renderPorts(ports: ConnPort[]): void {
    const target = el('connPortList');
    if (target) target.innerHTML = portListHTML(ports);
  }

  /** Select a country, or clear with null. */
  function selectCountry(cc: string | null): void {
    selectedCC = cc;
    listPage = 0;
    drawList();
    // The two filters are mutually exclusive — see the header.
    if (cc && filteredBySrc) {
      filteredBySrc = '';
      const sel = el<HTMLSelectElement>('connSrcFilter');
      if (sel) { sel.value = ''; sel.classList.remove('active'); }
    }
    const label = el('connFilterLabel');
    if (label) label.style.display = cc ? '' : 'none';
    if (!last) return;

    renderCountries(last.topCountries || []);
    redrawMap();

    const srcs = (last.topSources || []).slice(0, 8);
    if (!cc) {
      setBadge(last.total || 0);
      renderPorts(last.topPorts || []);
      sankey.setFiltered(false);
      sankey.update(srcs, (last.topDestinations || []).slice(0, 10));
      setSub((last.topCountries || []).length + ' countries active');
      return;
    }

    // The SERVER-BUILT per-country index covers every destination for this
    // country, not merely the ones that made the global top ten. The fallbacks
    // exist for a payload that predates those indexes.
    const dests = (last.countryDests && last.countryDests[cc])
      || (last.topDestinations || []).filter((d) => d.country === cc);
    const ports = (last.countryPorts && last.countryPorts[cc]) || portsFromDests(dests);

    setBadge(counts[cc] || 0);
    renderPorts(ports);
    sankey.setFiltered(true);
    sankey.redrawWith(srcs, dests.slice(0, 10));
    setSub(iso2Flag(cc) + ' ' + (CC_NAMES[cc] || cc) + ' — ' + dests.length +
      ' destination' + (dests.length !== 1 ? 's' : ''));
  }

  /** Select one client, or clear with an empty string. */
  function selectSource(ip: string): void {
    filteredBySrc = ip;
    listPage = 0;
    drawList();
    if (ip && selectedCC) {
      selectedCC = null;
      const label = el('connFilterLabel');
      if (label) label.style.display = 'none';
    }
    const sel = el<HTMLSelectElement>('connSrcFilter');
    if (sel) sel.classList.toggle('active', !!ip);
    if (!last) return;

    if (!ip) {
      counts = countsFrom(last.topCountries || []);
      renderCountries(last.topCountries || []);
      renderPorts(last.topPorts || []);
      setBadge(last.total || 0);
      redrawMap();
      sankey.setFiltered(false);
      sankey.update((last.topSources || []).slice(0, 8),
        (last.topDestinations || []).slice(0, 10));
      setSub((last.topCountries || []).length + ' countries active');
      return;
    }

    const dests = sourceDests[ip] || [];
    const list = countriesFromSourceDests(dests, protoOf, cityOf);
    counts = {};
    list.forEach((c) => { counts[c.cc] = c.count; });

    renderCountries(list);
    redrawMap();
    // The per-source PORT index is uncapped, unlike the destination list, so it
    // counts every connection rather than the top thirty.
    renderPorts(sourcePorts[ip] || []);

    const srcObj = (last.topSources || []).find((s) => s.ip === ip);
    const srcCount = dests.reduce((n, d) => n + d.count, 0);
    setBadge(srcObj ? srcObj.count : srcCount);
    sankey.setFiltered(true);
    sankey.redrawWith(
      [{ ip, name: srcObj ? (srcObj.name || ip) : ip, count: srcCount || 1 }],
      dests.slice(0, 10));
  }

  function countsFrom(list: ConnCountry[]): Record<string, number> {
    const out: Record<string, number> = {};
    list.forEach((e) => { out[e.cc] = e.count; });
    return out;
  }

  function populateClients(): void {
    const sel = el<HTMLSelectElement>('connSrcFilter');
    if (!sel || !last) return;
    const current = sel.value;
    const devices = clientOptions(last.topSources || [], leases);
    sel.innerHTML = '<option value="">' + t('All Clients') + '</option>';
    devices.forEach((d) => {
      const opt = document.createElement('option');
      opt.value = d.ip;
      opt.textContent = (d.name && d.name !== d.ip) ? (d.name + ' — ' + d.ip) : d.ip;
      sel.appendChild(opt);
    });
    if (current && devices.some((d) => d.ip === current)) sel.value = current;
  }

  el<HTMLSelectElement>('connSrcFilter')?.addEventListener('change', function () {
    selectSource(this.value);
  });

  socket.on('conn:update', (data) => {
    if (!data) return;

    // Asked for HERE rather than at init — see fetchLocalCCOnce. Connection
    // data arriving is the proof that the session is up and has reported.
    fetchLocalCCOnce();

    (data.topCountries || []).forEach((e) => {
      protoOf[e.cc] = e.proto || {};
      cityOf[e.cc] = e.city || '';
      pushSpark(e.cc, e.count);
    });

    // THE HEAVY INDEXES ARRIVE SEPARATELY and are not in every payload, so the
    // previous ones are carried forward. Without this a country filter falls
    // back to the capped top-ten list every time the poll lands.
    const prevCountryDests = last && last.countryDests;
    const prevCountryPorts = last && last.countryPorts;
    last = data;
    if (prevCountryDests && !last.countryDests) last.countryDests = prevCountryDests;
    if (prevCountryPorts && !last.countryPorts) last.countryPorts = prevCountryPorts;

    populateClients();

    if (filteredBySrc) {
      // A client filter survives the poll: its view is re-derived rather than
      // replaced, so the selection does not blink out every few seconds.
      selectSource(filteredBySrc);
      return;
    }

    // ── WHICH COUNTRIES JUST GAINED CONNECTIONS ──────────────────────────
    //
    // Computed BEFORE `counts` is replaced, because the rule is a comparison
    // against the PREVIOUS payload. The live app keeps `prevCounts` for exactly
    // this and pulses every country whose count went up.
    //
    // Gated on `newSinceLast`, as live is: without it a country whose count rose
    // only because an older connection aged out of the window would flash, which
    // is a pulse for something that did not arrive.
    const prevCounts = counts;
    counts = countsFrom(data.topCountries || []);
    if ((data.newSinceLast || 0) > 0 && map) {
      const gained = Object.keys(counts)
        .filter((cc) => (counts[cc] || 0) > (prevCounts[cc] || 0));
      if (gained.length) map.pulse(gained);
    }
    if (selectedCC) {
      selectCountry(selectedCC);
    } else {
      setBadge(data.total || 0);
      redrawMap();
      renderCountries(data.topCountries || []);
      renderPorts(data.topPorts || []);
      sankey.update((data.topSources || []).slice(0, 8),
        (data.topDestinations || []).slice(0, 10));
    }
  });

  socket.on('conn:country-data', (d) => {
    if (!d || !last) return;
    if (d.countryDests) last.countryDests = d.countryDests;
    if (d.countryPorts) last.countryPorts = d.countryPorts;
    if (selectedCC) selectCountry(selectedCC);
  });

  socket.on('conn:source-data', (d) => {
    if (!d) return;
    if (d.sourceDests) sourceDests = d.sourceDests;
    if (d.sourcePorts) sourcePorts = d.sourcePorts;
    if (filteredBySrc) selectSource(filteredBySrc);
  });

  // The DHCP leases fill the client picker with devices that have no traffic
  // right now — which is how you find out they have none.
  socket.on('leases:list', (d) => {
    leases = (d && d.leases) || [];
    populateClients();
  });

  /**
   * WHERE THIS ROUTER IS, which is where every arc starts.
   *
   * ── LAZY, NOT AT BOOT, AND THAT IS THE WHOLE FIX ────────────────────────
   *
   * This used to run once at module init and never again. Every page module is
   * initialised at BOOT, and at boot the router session has not settled and
   * `dhcpNetworks` has not produced a payload — so `/api/localcc` answers
   * `{"cc":""}`, the guard below fails, and `localCC` stays `ZZ` FOREVER. `ZZ`
   * has no centroid, so `arcs()` returns immediately: the map colours countries
   * and counts them and draws no arcs or comets at all, which reads as a
   * rendering bug rather than a timing one. Reported by the operator on
   * 2026-08-28, testing the port beside the live app.
   *
   * The live app calls this from inside its `conn:update` handler
   * (`app.js:4782`), so the first attempt happens when connection data has
   * actually arrived and the session is therefore up. Same shape here.
   *
   * The flag is reset on `connect`, as the live one is: a reconnect is a new
   * socket and a new session, and the answer is worth asking for again. And a
   * FAILED fetch resets it too, so a transient error retries on the next
   * payload rather than costing the map its arcs for the life of the page.
   */
  let localCCFetched = false;
  socket.on('connect', () => { localCCFetched = false; });

  function fetchLocalCCOnce(): void {
    if (localCCFetched) return;
    localCCFetched = true;
    fetch('/api/localcc')
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { cc?: string } | null) => {
        if (d && d.cc) {
          localCC = d.cc;
          // PUBLISHED FOR THE DASHBOARD'S MAP CARD, as the live app does
          // (`../MikroDash/public/app.js:4600`). That card draws every arc FROM
          // this country; without it `_worldMapLocalCC` is undefined, the card
          // falls back to 'ZZ', and it draws no arcs at all while still
          // colouring countries.
          (window as unknown as { _worldMapLocalCC?: string })._worldMapLocalCC = d.cc;
          redrawMap();
        }
      })
      .catch(() => { localCCFetched = false; });
  }

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'connections') return;
    if (last) {
      renderCountries(last.topCountries || []);
      renderPorts(last.topPorts || []);
      redrawMap();
      sankey.redraw();
    }
  });

  window.addEventListener('resize', () => {
    if (isVisible('connections')) sankey.redraw();
  });

  // ── The List tab ───────────────────────────────────────────────────────────
  //
  // Every connection, a row each (`conn:list`, see collect/connlist.go). The
  // server sends it only while this viewer's List tab is open, so the tab says
  // so whenever it opens or closes, and again after a reconnect, when the
  // server has forgotten. The client and country filters above apply here too.

  type ConnTab = 'map' | 'list';
  const TAB_KEY = 'mikrodash_conn_tab';
  let tab: ConnTab = lsGet<string>(TAB_KEY, 'map') === 'list' ? 'list' : 'map';
  let listed: ConnListPayload | null = null;
  let listPage = 0;
  let query = '';
  const listSort: SortState = { col: 'rxr', dir: 'desc' };

  function showTab(next: ConnTab): void {
    tab = next;
    lsSet(TAB_KEY, next);
    document.querySelectorAll<HTMLElement>('#connTabs [data-conntab]').forEach((b) => {
      const on = b.getAttribute('data-conntab') === next;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
    });
    const mapPanel = el('connPanelMap');
    const listPanel = el('connPanelList');
    const search = el('connListSearch');
    if (mapPanel) mapPanel.hidden = next !== 'map';
    if (listPanel) listPanel.hidden = next !== 'list';
    if (search) search.hidden = next !== 'list';
    if (isVisible('connections')) socket.emit('conn:list', { on: next === 'list' });
    if (next === 'list') drawList();
    else if (last) redrawMap();
  }

  function drawList(): void {
    if (tab !== 'list' || !isVisible('connections')) return;
    renderSortHeader('connListHead', CONN_COLS, listSort, drawList);
    const body = el('connListBody');
    const status = el('connListStatus');
    if (!listed) {
      if (status) status.textContent = t('Waiting for the connection table…');
      return;
    }
    let rows = filterConns(listed.rows.map(sortable), query, filteredBySrc);
    if (selectedCC) rows = rows.filter((r) => r.country === selectedCC);
    const matched = sortRows(rows, listSort.col, listSort.dir);
    const pg = pageOf(matched, listPage);
    listPage = pg.page;
    if (body) {
      body.innerHTML = pg.rows.map(connRowHTML).join('') ||
        '<tr><td colspan="' + CONN_COLS.length + ('" class="conn-empty">' + t('No connections match.') + '</td></tr>');
    }
    const pager = pagerHTML(pg.rows.length, matched.length, pg.page, pg.pages);
    for (const id of ['connListPager', 'connListPager2']) {
      const p = el(id);
      if (p) p.innerHTML = pager;
    }
    if (status) {
      const filtered = matched.length !== listed.rows.length;
      const v = { n: listed.total.toLocaleString(), shown: matched.length.toLocaleString(), first: listed.rows.length.toLocaleString() };
      status.textContent = (filtered ? t('{n} connections, {shown} shown', v) : t('{n} connections', v)) +
        (listed.capped ? '. ' + t('The router has more than MikroDash processes; the first {first} are listed.', v) : '');
    }
  }

  socket.on('conn:list', (p) => {
    listed = p;
    drawList();
  });
  el('connTabs')?.addEventListener('click', (e) => {
    const t = (e.target as HTMLElement).closest('[data-conntab]')?.getAttribute('data-conntab');
    if ((t === 'map' || t === 'list') && t !== tab) showTab(t);
  });
  el('connListSearch')?.addEventListener('input', (e) => {
    query = (e.target as HTMLInputElement).value;
    listPage = 0;
    drawList();
  });
  for (const id of ['connListPager', 'connListPager2']) {
    el(id)?.addEventListener('click', (e) => {
      const b = (e.target as HTMLElement).closest('[data-conn-page]');
      if (!b || (b as HTMLButtonElement).disabled) return;
      listPage = Number(b.getAttribute('data-conn-page')) || 0;
      drawList();
      // A new page starts at its first row: the list scrolls inside a card of
      // fixed height, and the next page kept the old one's scroll position.
      const scroll = el('connListScroll');
      if (scroll) scroll.scrollTop = 0;
    });
  }
  // A reconnect is a new connection on the server, which has not heard which
  // tab is open.
  document.addEventListener('socket:reconnect', () => {
    if (tab === 'list' && isVisible('connections')) socket.emit('conn:list', { on: true });
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'connections') showTab(tab);
  });
  showTab(tab);
}
