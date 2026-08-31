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

import { el } from '../dom';
import type { Socket } from '../socket';
import { CC_NAMES, iso2Flag } from './connections-map';
import {
  createWorldMap, attachMapZoom, bindMapTooltip, bindMapFullscreen, type WorldMap,
} from './connections-worldmap';
import {
  SPARK_LEN, syncCountryList, portListHTML, portsFromDests,
  countriesFromSourceDests, clientOptions,
  type ConnCountry, type ConnDestEntry, type ConnPort, type ConnSource,
} from './connections-lists';
import { createSankeyThrottle, renderSankey, type SankeyDest } from './connections-sankey';

interface ConnPayload {
  ts: number; total: number; newSinceLast: number;
  topSources: ConnSource[];
  topDestinations: Array<ConnDestEntry & { proto?: Record<string, number> }>;
  topCountries: ConnCountry[];
  topPorts: ConnPort[];
  countryDests?: Record<string, ConnDestEntry[]>;
  countryPorts?: Record<string, ConnPort[]>;
  sourceDests?: Record<string, ConnDestEntry[]>;
  sourcePorts?: Record<string, ConnPort[]>;
}

export function initConnectionsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const mapSvg = el('worldMap') as unknown as SVGElement | null;
  const listEl = el('connMapList');
  if (!mapSvg && !listEl) return;

  let last: ConnPayload | null = null;
  let counts: Record<string, number> = {};
  const protoOf: Record<string, Record<string, number>> = {};
  const cityOf: Record<string, string> = {};
  const sparks: Record<string, number[]> = {};
  let sourceDests: Record<string, ConnDestEntry[]> = {};
  let sourcePorts: Record<string, ConnPort[]> = {};
  let selectedCC: string | null = null;
  let filteredBySrc = '';
  let localCC = 'ZZ';
  let leases: Array<{ ip: string; name?: string; hostName?: string }> = [];

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
      el('mapZoomIn')?.addEventListener('click', () => wrap.dispatchEvent(
        new WheelEvent('wheel', { deltaY: -100, clientX: wrap.getBoundingClientRect().width / 2,
          clientY: wrap.getBoundingClientRect().height / 2, bubbles: true, cancelable: true })));
      el('mapZoomOut')?.addEventListener('click', () => wrap.dispatchEvent(
        new WheelEvent('wheel', { deltaY: 100, clientX: wrap.getBoundingClientRect().width / 2,
          clientY: wrap.getBoundingClientRect().height / 2, bubbles: true, cancelable: true })));
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
      sankey.update(srcs, (last.topDestinations || []).slice(0, 10) as SankeyDest[]);
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
    sankey.redrawWith(srcs, dests.slice(0, 10) as SankeyDest[]);
    setSub(iso2Flag(cc) + ' ' + (CC_NAMES[cc] || cc) + ' — ' + dests.length +
      ' destination' + (dests.length !== 1 ? 's' : ''));
  }

  /** Select one client, or clear with an empty string. */
  function selectSource(ip: string): void {
    filteredBySrc = ip;
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
        (last.topDestinations || []).slice(0, 10) as SankeyDest[]);
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
      dests.slice(0, 10) as SankeyDest[]);
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
    sel.innerHTML = '<option value="">All Clients</option>';
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

  socket.on('conn:update', (data: ConnPayload) => {
    if (!data) return;

    // Asked for HERE rather than at init — see fetchLocalCCOnce. Connection
    // data arriving is the proof that the session is up and has reported.
    fetchLocalCCOnce();

    (data.topCountries || []).forEach((e) => {
      protoOf[e.cc] = (e.proto || {}) as Record<string, number>;
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
    if (data.sourceDests) sourceDests = data.sourceDests;
    if (data.sourcePorts) sourcePorts = data.sourcePorts;

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
        (data.topDestinations || []).slice(0, 10) as SankeyDest[]);
    }
  });

  socket.on('conn:country-data', (d: { countryDests?: Record<string, ConnDestEntry[]>;
    countryPorts?: Record<string, ConnPort[]> }) => {
    if (!d || !last) return;
    if (d.countryDests) last.countryDests = d.countryDests;
    if (d.countryPorts) last.countryPorts = d.countryPorts;
    if (selectedCC) selectCountry(selectedCC);
  });

  socket.on('conn:source-data', (d: { sourceDests?: Record<string, ConnDestEntry[]>;
    sourcePorts?: Record<string, ConnPort[]> }) => {
    if (!d) return;
    if (d.sourceDests) sourceDests = d.sourceDests;
    if (d.sourcePorts) sourcePorts = d.sourcePorts;
    if (filteredBySrc) selectSource(filteredBySrc);
  });

  // The DHCP leases fill the client picker with devices that have no traffic
  // right now — which is how you find out they have none.
  socket.on('leases:list', (d: { leases?: Array<{ ip: string; name?: string; hostName?: string }> }) => {
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
}
