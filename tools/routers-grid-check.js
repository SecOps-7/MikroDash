'use strict';
/**
 * Drive the LIVE Routers renderer and the PORT's from ONE payload, compare the
 * DOM they produce.
 *
 * ── WHY THIS EXISTS BESIDE tools/live-renderer.js ──────────────────────────
 *
 * live-renderer.js lifts the live renderer and the comparison then happens in a
 * browser against a running stack. That is the real gate and this does not
 * replace it — but it needs the Go server, the Node app and a browser, so it is
 * not something a unit run can do. This gets the same evidence for the parts
 * that only touch innerHTML and textContent, with a ~40-line document shim and
 * no browser at all.
 *
 * WHAT IT CANNOT SEE, said plainly: anything that is not innerHTML or
 * textContent. Event listeners, focus, computed layout, the SVG map's
 * measurements. A green run here is evidence about generated MARKUP.
 *
 *   node tools/routers-grid-check.js          compare, exit 1 on a difference
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

// ── the shim ────────────────────────────────────────────────────────────────
//
// Only what the two renderers actually touch. Deliberately NOT a DOM library: a
// real one would silently normalise the markup — reordering attributes, closing
// tags, lower-casing — and a comparison of two normalised strings can pass while
// the raw markup differs. These nodes store innerHTML exactly as assigned.
function makeDoc(ids) {
  const nodes = {};
  const clicks = [];
  const thead = { addEventListener: (ev, fn) => { if (ev === 'click') clicks.push(fn); } };
  for (const id of ids) {
    // textContent AND innerHTML COERCE TO STRING, because a real DOM does. The
    // live renderer assigns `el.total.textContent = total` — a NUMBER — while a
    // typed port assigns `String(total)`. A shim that stored each verbatim
    // reported four differences between values that render identically, which is
    // a false failure and exactly as damaging as a false pass: it trains the
    // reader to skim the output.
    const store = { innerHTML: '', textContent: '' };
    // `classList` RECORDS, rather than answering false and dropping writes.
    // #117's site filter calls `toggle('active', …)` to show the control is
    // narrowing, and a `contains`-only stub threw on the first row that had a
    // site. The set is part of what the two sides are compared on: a filter that
    // silently stopped marking itself active looks identical in markup.
    const cls = new Set();
    const n = { id, style: {}, value: '',
                addEventListener() {},
                classList: {
                  contains: (c) => cls.has(c),
                  add: (c) => cls.add(c),
                  remove: (c) => cls.delete(c),
                  toggle: (c, on) => {
                    if (on === undefined) { cls.has(c) ? cls.delete(c) : cls.add(c); }
                    else if (on) cls.add(c); else cls.delete(c);
                  },
                },
                _classes: cls };
    for (const k of ['innerHTML', 'textContent']) {
      Object.defineProperty(n, k, {
        get: () => store[k],
        set: (v) => { store[k] = String(v); },
        enumerable: true,
      });
    }
    nodes[id] = n;
  }
  // ── UNKNOWN LOOKUPS ARE RECORDED, NOT SILENTLY NULL ─────────────────────
  //
  // An id the renderer asks for and the shim does not provide comes back null,
  // and the renderer skips that element without a sound. That is how three
  // slider labels hid from the settings gate. Here the same shape is a KNOWN
  // gap — the map's SVG half is not ported — but a gate should say which gaps it
  // has rather than leaving them to be rediscovered, so any NEW unknown id is a
  // failure.
  const unknown = new Set();
  return {
    nodes, unknown,
    getElementById: (id) => {
      if (!nodes[id]) { unknown.add(id); return null; }
      return nodes[id];
    },
    // A CLICKABLE HEADER, because that is how the live page changes its sort.
    // `_rtlSetSort` is a top-level function inside the lifted module's scope and
    // cannot be called from outside it — the only reachable path is the
    // delegated click listener the page installs on the thead. Driving the live
    // side through its real mechanism is the same choice made for the view and
    // localStorage.
    querySelector: (sel) => (sel.includes('thead') ? thead : null),
    querySelectorAll: () => [],
    addEventListener() {},
    createElement: () => ({ style: {}, setAttribute() {}, appendChild() {} }),
    // Deliver a header click the way the browser would: an event whose target
    // resolves `closest('th[data-sort]')` to a header naming the column.
    __clickColumn(key) {
      const th = { dataset: { sort: key } };
      clicks.forEach((fn) => fn({ target: { closest: () => th } }));
    },
  };
}

const IDS = ['rsTotal', 'rsOnline', 'rsOffline', 'rsAlerting', 'rsSites', 'routersSiteFilter', 'routers-grid',
             'routersShown', 'routersSearch', 'routersListBody', 'routersListWrap',
             'routersMapWrap', 'routersMap', 'routersView', 'page-devices',
             'rtrMapTray', 'rtrMapPop'];

function snapshot(doc) {
  const out = {};
  for (const id of IDS) {
    const n = doc.nodes[id];
    out[id] = { innerHTML: n.innerHTML, textContent: n.textContent,
                color: n.style.color === undefined ? '' : n.style.color,
                // THE SWITCHER'S ENTIRE OBSERVABLE EFFECT is these two: which
                // wrappers are hidden, and what the select reads. Without them
                // the view switch was ported but never compared.
                hidden: n.hidden === undefined ? null : !!n.hidden,
                value: n.value === undefined ? '' : n.value };
  }
  return out;
}

// ── the two renderers ───────────────────────────────────────────────────────

// THE VIEW IS DRIVEN DIFFERENTLY ON EACH SIDE, and that is deliberate rather
// than sloppy. The live module carries its own view switcher, which reads
// `localStorage.getItem('mikrodash_routers_view')` at init and applies it — so
// the live side is driven through the mechanism the real page uses. The port's
// switcher is not ported yet (it needs the change listener and the socket
// wiring), so its view is set through the exported `setView`. When the switcher
// lands, both sides can use localStorage and this note goes away.
function runLive(payload, query, view, sorts, then) {
  const file = path.join(ROOT, 'web', 'dist', '_compare', 'live-routers.js');
  if (!fs.existsSync(file)) {
    console.error('no lifted live renderer — run: node tools/live-renderer.js routers');
    process.exit(1);
  }
  const doc = makeDoc(IDS);
  doc.nodes.routersSearch.value = query || '';
  const store = { mikrodash_routers_view: view || 'comfortable' };
  const localStorage = {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
  };
  const win = {};
  const src = fs.readFileSync(file, 'utf8');
  // The lifted module assigns onto `window`; give it one, plus the globals a
  // renderer reaches for.
  new Function('window', 'document', 'requestAnimationFrame', 'MutationObserver',
    'localStorage',
    src + '\nwindow.__runLive_routers(arguments[5]);')(
    win, doc, () => {}, function () { return { observe() {}, disconnect() {} }; },
    localStorage, payload);
  (sorts || []).forEach((k) => doc.__clickColumn(k));
  // A SECOND stats payload to the SAME page — the order matters: the sort is
  // chosen first, so what is on trial is whether a refresh keeps it.
  if (then) win.__runLive_routers(then);
  const snap = snapshot(doc);
  snap.__mapFns = win.__mapFns;
  snap.__doc = doc;
  Object.defineProperty(snap, '__unknown', { value: [...doc.unknown], enumerable: false });
  return snap;
}

let portMod = null, portDoc = null;

// The port's functions reach for a global `document`, and `runPort` restores the
// previous one in its finally — so anything called AFTER it needs the shim put
// back for the duration. Installed and removed around each call rather than left
// in place: a leaked global document is how a later pass ends up writing into
// the wrong shim.
function withPortDoc(fn) {
  const prev = global.document;
  global.document = portDoc;
  try { return fn(); }
  finally { if (prev === undefined) delete global.document; else global.document = prev; }
}
function runPort(payload, query, view, sorts, then) {
  const handlers = {};
  const out = path.join(ROOT, 'web', 'dist', '_compare', 'port-routers.cjs');
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
     '--bundle', '--format=cjs', '--platform=node', '--outfile=' + out, '--log-level=warning'],
    { stdio: 'inherit' });

  const doc = makeDoc(IDS);
  doc.nodes.routersSearch.value = query || '';
  const store = { mikrodash_routers_view: view || 'comfortable' };
  const prev = global.document, prevLS = global.localStorage;
  global.document = doc;
  global.localStorage = {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
  };
  try {
    delete require.cache[require.resolve(out)];
    const mod = require(out);
    // THE SAME MECHANISMS AS THE LIVE SIDE, not a shortcut past them. The live
    // module's own switcher IIFE reads localStorage and installs the listeners;
    // `mountRouters` is the port of that, so calling it here means both sides
    // reach their state the way the real page does. The earlier version called
    // `setView` and `renderRoutersStats` directly, which compared the renderers
    // while leaving the wiring untested.
    mod.mountRouters({ on: (ev, cb) => { handlers[ev] = cb; } });
    handlers['routers:stats'](payload);
    (sorts || []).forEach((k) => doc.__clickColumn(k));
    if (then) handlers['routers:stats'](then);
    portMod = mod;
    portDoc = doc;
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
    if (prevLS === undefined) delete global.localStorage; else global.localStorage = prevLS;
  }
  return snapshot(doc);
}

// ── the payload ─────────────────────────────────────────────────────────────
//
// Every field exercised in both states. The rows are chosen so each branch the
// grid can take is present in ONE run: a healthy router, one at every colour
// threshold, an offline one with a reason, one with no readings at all, and one
// whose host equals its label (which suppresses the host line).
const ROWS = [
  { id: 'r1', label: 'Branch Office', host: '198.51.100.2', isActive: true, connected: true,
    lastError: null, openAlerts: 0, cpu: 7, uptime: '3w4d5h', memPct: 42, hddPct: 13,
    version: '7.24', boardName: 'hAP ax^3', arch: 'arm64', serial: 'HGL09XY1ZQ2',
    licenseLevel: '4', rxMbps: 12.5, txMbps: 3.25, clients: 18,
    siteId: 's1', siteName: 'HQ', geo: null },
  { id: 'r2', label: 'Hot Router', host: '198.51.100.3', isActive: false, connected: true,
    lastError: null, openAlerts: 3, cpu: 95, uptime: '2w0d5h', memPct: 80, hddPct: 91,
    version: '7.19.4', boardName: 'CCR2004', arch: null, serial: null,
    licenseLevel: null, rxMbps: 0, txMbps: 0, clients: 0,
    siteId: null, siteName: null, geo: null },
  { id: 'r3', label: '198.51.100.4', host: '198.51.100.4', isActive: false, connected: false,
    lastError: 'dial tcp 198.51.100.4:8728: connect: connection refused', openAlerts: 1,
    cpu: null, uptime: null, memPct: null, hddPct: null, version: null, boardName: null,
    arch: null, serial: null, licenseLevel: null, rxMbps: null, txMbps: null, clients: null,
    siteId: null, siteName: null, geo: null },
  // OVER 100%. RouterOS can report a usage above the bar's range, and the live
  // renderer CLAMPS THE WIDTH while printing the raw number. Without a row like
  // this, clamping the number too passed a green run.
  { id: 'r5', label: 'Router 10', host: '198.51.100.10', isActive: false, connected: true,
    lastError: null, openAlerts: 0, cpu: 150, uptime: '1d', memPct: 101, hddPct: 100,
    version: '7.9', boardName: 'RB5009', arch: 'arm', serial: 'X1', licenseLevel: '5',
    rxMbps: 2, txMbps: 2, clients: 2, siteId: null, siteName: null, geo: null },
  // NUMERIC COLLATION. "Router 2" must sort before "Router 10"; a plain
  // localeCompare puts "Router 10" first, and that mutation survived until both
  // labels existed.
  { id: 'r6', label: 'Router 2', host: '198.51.100.11', isActive: false, connected: false,
    lastError: null, openAlerts: 0, cpu: null, uptime: null, memPct: null, hddPct: null,
    version: null, boardName: null, arch: null, serial: null, licenseLevel: null,
    rxMbps: null, txMbps: null, clients: null, siteId: null, siteName: null, geo: null },
  // ESCAPING. A label a hostile record could carry; both sides must escape it
  // identically, and a difference here is a cross-site scripting difference.
  { id: 'r4', label: '<img src=x onerror=alert(1)>', host: '198.51.100.5', isActive: false,
    connected: true, lastError: null, openAlerts: 0, cpu: 76, uptime: '45s', memPct: 76,
    hddPct: 76, version: '7.24 "beta"', boardName: 'a & b', arch: '<i>', serial: "o'brien",
    licenseLevel: '<6>', rxMbps: 1.005, txMbps: 999.999, clients: 1,
    siteId: null, siteName: null, geo: null },
];

// ── THE MAP'S STRING AND ARITHMETIC HALF ───────────────────────────────────
//
// Building and moving the SVG needs a browser and is NOT compared here — that is
// the live-renderer browser gate's job, and saying so is the point of this
// comment. What IS compared: the projection, the co-location grouping, both
// popovers and the unlocated tray, all of which are arithmetic and strings.
//
// The live half comes out of the map's own IIFE through `window.__mapFns`,
// published by tools/live-renderer.js — see publishInside there.
function compareMap(live, port, doc) {
  const bad = [];
  let checks = 0;
  const fns = live.__mapFns;
  if (!fns) {
    console.error('the lifted module published no __mapFns — regenerate with: ' +
                  'node tools/live-renderer.js routers');
    process.exit(1);
  }

  // project(): a systematic sweep plus the poles, the antimeridian and 0,0.
  const PTS = [[0, 0], [13.4, 52.52], [-74, 40.7], [151.2, -33.87], [180, 90],
               [-180, -90], [179.999, 89.999], [-0.0001, 0.0001]];
  for (const [lon, lat] of PTS) {
    checks++;
    const a = fns.project(lon, lat), b = port.project(lon, lat);
    if (a[0] !== b[0] || a[1] !== b[1]) {
      bad.push({ what: 'project(' + lon + ',' + lat + ')',
                 live: a.join(','), port: b.join(',') });
    }
  }

  // layout(): grouping, averaging and member order.
  const located = ROWS.map((r, i) => Object.assign({}, r, {
    // Two pairs land in ONE bucket and one sits alone, so grouping, averaging
    // and the count are all exercised. The tiny offsets are inside GRID.
    // THE PAIRINGS PUT EACH CLUSTER'S MEMBERS OUT OF LABEL ORDER on input, and
    // that is the whole reason for this arrangement. With the obvious pairing
    // (rows 0+1, rows 2+3) both clusters happened to arrive already sorted, so
    // deleting the member sort entirely passed a green run — the corpus, not the
    // code. Row 1 "Hot Router" now shares Berlin with row 2 "198.51.100.4", and
    // row 0 "Branch Office" shares New York with row 3 "<img …>", so both
    // clusters must be reordered before they read correctly.
    geo: { lat: [40.7, 52.52, 52.521, 40.7009, -33.87, 10][i],
           lon: [-74, 13.4, 13.401, -74.0009, 151.2, 10][i],
           source: ['auto', 'manual', 'site', 'auto', 'auto', 'auto'][i],
           label: ['New York, NY, US', 'Berlin, BE, DE', 'Berlin, BE, DE',
                   'New York, NY, US', 'Sydney, NSW, AU', ''][i],
           wanIp: i === 0 ? '198.51.100.7' : '' },
  }));
  checks++;
  const la = fns.layout(located), lb = port.layout(located);
  const norm = (gs) => JSON.stringify(gs.map((g) => ({
    key: g.key, x: g.x, y: g.y, ids: g.routers.map((r) => r.id) })));
  if (norm(la) !== norm(lb)) bad.push({ what: 'layout()', live: norm(la), port: norm(lb) });

  // The popovers, for every row and for a real cluster.
  for (const r of located) {
    checks++;
    const a = fns.popHtml(r), b = port.popHtml(r);
    if (a !== b) bad.push({ what: 'popHtml(' + r.id + ')', live: a, port: b });
  }
  for (const g of la) {
    checks++;
    const a = fns.groupPopHtml(g), b = port.groupPopHtml(g);
    if (a !== b) bad.push({ what: 'groupPopHtml(' + g.key + ')', live: a, port: b });
  }

  // ── THE GEOMETRY ────────────────────────────────────────────────────────
  //
  // `clampTranslate` is pure once the shim's svg reports no clientWidth — both
  // sides then take the same 1000x500 fallback. `fitToMarkers` is not pure, but
  // what it produces IS observable: it writes a transform string onto the svg's
  // style, and that string is the comparison. Its other two calls are inert here
  // — `resize()` needs `window._lastRtrRows`, which is unset, and `positionPop()`
  // returns early with no popover open.
  // NOTE ON MAX_SCALE, measured rather than assumed: `fitToMarkers` pads its box
  // by 45 map units on every side, so the box is never shorter than 90 and the
  // scale it computes never exceeds 500/90 ≈ 5.55. MAX_SCALE (8) is therefore
  // UNREACHABLE through the fit path — raising it to 16 passes this gate — and
  // binds only through the zoom controls, which are not ported. Recorded so the
  // gap is visible instead of reading as coverage.
  const CLAMPS = [
    [1, 0, 0], [1, -50, -50], [1, 50, 50],       // scale 1 pins to the origin
    [2, -100, -100], [2, -5000, -5000], [2, 500, 500],
    [8, -3500, -1750], [8, -100000, -100000], [4.5, -123.45, -678.9],
  ];
  for (const [sc, x, y] of CLAMPS) {
    checks++;
    const a = fns.clampTranslate(sc, x, y);
    const b = withPortDoc(() => port.clampTranslate(sc, x, y));
    if (a[0] !== b[0] || a[1] !== b[1]) {
      bad.push({ what: 'clampTranslate(' + sc + ',' + x + ',' + y + ')',
                 live: a.join(','), port: b.join(',') });
    }
  }

  const FITS = [
    [{ x: 500, y: 250 }],                                    // one marker
    [{ x: 100, y: 100 }, { x: 900, y: 400 }],                // the whole world
    [{ x: 495, y: 245 }, { x: 505, y: 255 }],                // a tight cluster
    [{ x: 0, y: 0 }, { x: 1000, y: 500 }],                   // the corners
    [{ x: 480, y: 240 }, { x: 480, y: 240 }],                // identical points
    // THE CLAMP HAS TO BIND SOMEWHERE. Every case above centres to a
    // translation already inside the legal range, so removing the final
    // `clampTranslate` call changed nothing and passed a green run. A cluster
    // hard against an edge is what makes it bind.
    [{ x: 985, y: 495 }],                                    // bottom-right corner
    [{ x: 15, y: 5 }],                                       // top-left corner
    [{ x: 990, y: 250 }, { x: 995, y: 255 }],                // right edge, tight
  ];
  for (const pts of FITS) {
    checks++;
    doc.nodes.routersMap.style.transform = '';
    fns.fitToMarkers(pts);
    const a = doc.nodes.routersMap.style.transform;
    portDoc.nodes.routersMap.style.transform = '';
    withPortDoc(() => port.fitToMarkers(pts));
    const b = portDoc.nodes.routersMap.style.transform;
    if (!a) {
      console.error('fitToMarkers wrote no transform on the LIVE side — the ' +
                    'comparison would be two empty strings');
      process.exit(1);
    }
    if (a !== b) {
      bad.push({ what: 'fitToMarkers(' + pts.length + ' pts)', live: a, port: b });
    }
  }

  // The tray, in both of its states.
  for (const set of [located.slice(0, 3), []]) {
    checks++;
    doc.nodes.rtrMapTray.innerHTML = '';
    fns.renderTray(set);
    const a = { html: doc.nodes.rtrMapTray.innerHTML, hidden: doc.nodes.rtrMapTray.hidden };
    portDoc.nodes.rtrMapTray.innerHTML = '';
    withPortDoc(() => port.renderTray(set));
    const b = { html: portDoc.nodes.rtrMapTray.innerHTML, hidden: portDoc.nodes.rtrMapTray.hidden };
    if (a.html !== b.html || a.hidden !== b.hidden) {
      bad.push({ what: 'renderTray(' + set.length + ')',
                 live: a.hidden + '|' + a.html, port: b.hidden + '|' + b.html });
    }
  }
  // A COUNT THAT IS REPORTED, so a comparison that quietly stopped running
  // cannot look like agreement. `layout()` produced the groups these popovers
  // are built from, so a zero there would silently skip the group checks too.
  if (la.length < 2) {
    console.error('layout() produced ' + la.length + ' group(s) — the corpus no ' +
                  'longer exercises clustering, so the group popover is unchecked');
    process.exit(1);
  }
  bad.checks = checks;
  return bad;
}

function diff(a, b) {
  const bad = [];
  for (const id of IDS) {
    // EVERY KEY THE SNAPSHOT RECORDS, and the list is derived rather than
    // retyped. The first version of this compared only innerHTML, textContent
    // and color while the snapshot had grown `hidden` and `value` — so the view
    // switcher was collected and never looked at, and four mutations to it
    // passed a green run. A diff that names its keys by hand drifts from the
    // snapshot silently.
    for (const k of Object.keys(a[id])) {
      if (a[id][k] !== b[id][k]) bad.push({ id, k, live: a[id][k], port: b[id][k] });
    }
  }
  return bad;
}

// TWO PASSES, AND THE SECOND ONE IS NOT DECORATION.
//
// The first version ran unfiltered only. With no query every row survives, so
// `visible.length === all.length`, and the branch that writes "N of M shown"
// never executes — a real bug of mine in exactly that branch (a missing
// ' shown') was NOT caught. Measured, not theorised: the mutation that removed
// the suffix passed a green run.
//
// The keyword terms are the second reason: `online`, `offline` and `alerting`
// are filters rather than text, and a query of plain text exercises none of
// them.
// ── THE SECOND PAYLOADS ─────────────────────────────────────────────────────
//
// Derived from ROWS rather than written out, so a field added to ROWS is
// present here too. Each one changes something a re-render has to react to:
// REFRESHED moves every numeric reading (so a sort that was silently recomputed
// from stale rows would show), SHRUNK loses a router, GROWN gains one whose cpu
// lands in the MIDDLE of the existing order — appended at the end it would pass
// a sort that had stopped sorting.
const REFRESHED = ROWS.map((r, i) => ({
  ...r,
  cpu: r.cpu === null || r.cpu === undefined ? r.cpu : (r.cpu * 7 + 11) % 101,
  clients: r.clients === null || r.clients === undefined ? r.clients : r.clients + i + 1,
  memPct: r.memPct === null || r.memPct === undefined ? r.memPct : (r.memPct + 17) % 101,
  rxMbps: r.rxMbps === null || r.rxMbps === undefined ? r.rxMbps : r.rxMbps * 2,
}));
const SHRUNK = REFRESHED.slice(1);
const GROWN = REFRESHED.concat([{
  ...ROWS[0], id: 'rNew', label: 'Newly Adopted', host: '198.51.100.99',
  cpu: 50, clients: 4, serial: 'NEW00000001', siteId: 's1', siteName: 'HQ', geo: null,
}]);

const PASSES = [
  { name: 'unfiltered', query: '' },
  { name: 'a text search', query: 'branch' },
  { name: 'the offline keyword', query: 'offline' },
  { name: 'the alerting keyword', query: 'alerting' },
  { name: 'two terms', query: 'online 198.51' },
  { name: 'a search matching nothing', query: 'zzzz' },
  // COMPACT IS A DIFFERENT GRID, not a different view: it changes only the
  // column classes, and swapping the two was a mutation that passed until this
  // pass existed.
  { name: 'the compact grid', query: '', view: 'compact' },
  // The LIST view. Its markup is a table body rather than cards, and its rules
  // differ from the grid's in ways easy to harmonise by accident: a third
  // uptime rule, a dash that is a span rather than a bare em dash, and a usage
  // bar whose width clamps while its number does not.
  { name: 'the list view', query: '', view: 'list' },
  { name: 'the list view, filtered', query: 'online', view: 'list' },
  { name: 'the list view, empty', query: 'zzzz', view: 'list' },

  // ── THE SORTS, AND WHY EACH ONE IS HERE ────────────────────────────────
  //
  // The default sort is `label`, a TEXT column, so the numeric branch — and
  // with it the null-last rule — never ran. Three mutations passed a green run
  // until these existed: nulls sorted first, the bar's percentage clamped, and
  // the text collation losing `numeric: true`.
  { name: 'sorted by cpu (numeric, nulls last)', query: '', view: 'list', sorts: ['cpu'] },
  { name: 'sorted by cpu, reversed', query: '', view: 'list', sorts: ['cpu', 'cpu'] },
  { name: 'sorted by clients', query: '', view: 'list', sorts: ['clients'] },
  { name: 'sorted by label, reversed', query: '', view: 'list', sorts: ['label'] },
  { name: 'sorted by version then host', query: '', view: 'list', sorts: ['version', 'host'] },
  // An unknown key must be IGNORED, leaving the previous sort intact.
  { name: 'an unknown sort key', query: '', view: 'list', sorts: ['cpu', 'nosuchcolumn'] },

  // ── A SECOND STATS PAYLOAD, WHICH IS WHAT THE PAGE ACTUALLY GETS ───────
  //
  // Two mutations survived the sixteen cases above and die on these: a refresh
  // that resets the sort to the default, and one that re-renders the STALE rows
  // it already had. Both are invisible to a gate that renders once. (A third —
  // a refresh forgetting the view — turned out to be caught already, and is
  // recorded here rather than claimed as new.)
  //
  // The Routers page is not rendered once. `routers:stats` arrives on a timer,
  // and everything the operator chose in between — the sort, the view, the
  // search box — has to survive it. None of that could be tested here until
  // `live-renderer.js` stopped declaring the bundle's state inside the exported
  // function (2026-08-25); a second call used to re-declare the page rather than
  // refresh it, so both sides looked like fresh pages and agreed vacuously.
  //
  // The order is deliberate in every case below: state is chosen FIRST, the
  // payload arrives SECOND. Reversed, the case proves nothing — a renderer that
  // discarded the sort on refresh would still show it, because the click came
  // after the refresh.
  { name: 'a refresh KEEPS the chosen sort', query: '', view: 'list',
    sorts: ['cpu'], then: REFRESHED },
  { name: 'a refresh keeps a REVERSED sort', query: '', view: 'list',
    sorts: ['cpu', 'cpu'], then: REFRESHED },
  { name: 'a refresh keeps the view', query: '', view: 'compact', then: REFRESHED },
  { name: 'a refresh keeps the search filter', query: 'online', view: 'list',
    then: REFRESHED },
  // A router REMOVED from the fleet between payloads. The grid must lose the
  // card, and the sort must survive an input one row shorter.
  { name: 'a router VANISHES on refresh', query: '', view: 'list',
    sorts: ['cpu'], then: SHRUNK },
  // ...and one ADDED, which has to land in the right place under the standing
  // sort rather than at the end.
  { name: 'a router APPEARS on refresh', query: '', view: 'list',
    sorts: ['cpu'], then: GROWN },
  // The fleet emptying entirely: the empty state must appear on a REFRESH, not
  // only on a first render.
  { name: 'the fleet empties on refresh', query: '', view: 'list', then: [] },
];

function main() {
  let bad = [];
  let chars = 0;
  let mapChecks = 0;
  for (const pass of PASSES) {
    const live = runLive(pass.rows || ROWS, pass.query, pass.view, pass.sorts, pass.then);
    const port = runPort(pass.rows || ROWS, pass.query, pass.view, pass.sorts, pass.then);

    // ── A PASS THAT RENDERED NOTHING IS NOT A PASS ────────────────────────
    //
    // Two empty strings compare equal. Without this, a view that silently drew
    // nothing on BOTH sides — a mis-set view, a renderer that returned early on
    // a missing node — would be reported as agreement. The target node is
    // whichever one this pass is supposed to fill.
    // ── THE SWITCHER MUST HAVE ACTUALLY RUN ───────────────────────────────
    //
    // `hidden` starts undefined and is recorded as null. If neither side ever
    // set it, both read null and compare equal — the switcher would be "ported
    // but never compared" and this would still print green. So the wrappers'
    // flags must be BOOLEANS by now on the live side, which is the one on trial.
    for (const wrapper of ['routers-grid', 'routersListWrap', 'routersMapWrap']) {
      if (typeof live[wrapper].hidden !== 'boolean') {
        console.error('[' + pass.name + '] the LIVE switcher never set #' + wrapper +
                      '.hidden — the view switch is not being exercised');
        process.exit(1);
      }
    }
      // Every id the live renderer asked for and did not get. Two are expected:
    // the map's viewport and its auto-frame button belong to the SVG half, which
    // is not ported and is gated in a browser instead.
    const EXPECTED_ABSENT = new Set(['rtrMapViewport', 'rtrMapAutoFrame']);
    for (const id of live.__unknown || []) {
      if (!EXPECTED_ABSENT.has(id)) {
        console.error('[' + pass.name + '] the live renderer looked up #' + id +
                      ', which this shim does not provide — it was skipped silently. ' +
                      'Add it to IDS, or to EXPECTED_ABSENT with a reason.');
        process.exit(1);
      }
    }

    // ── A SECOND PAYLOAD THAT CHANGED NOTHING IS NOT A SECOND PAYLOAD ─────
    //
    // The whole `then:` mechanism is worthless if the second call never reaches
    // the renderer — both sides would simply show the first payload and agree,
    // which is a fair comparison and a vacuous one at the same time. So the
    // LIVE side alone is driven twice and once, and the two must DIFFER.
    //
    // `then: []` is exempt because an empty fleet is the one refresh whose
    // point is what it REMOVES; it is covered by the emptiness check below.
    if (pass.then && pass.then.length) {
      const once = runLive(pass.rows || ROWS, pass.query, pass.view, pass.sorts);
      const tgt = pass.view === 'list' ? 'routersListBody' : 'routers-grid';
      if (once[tgt].innerHTML === live[tgt].innerHTML) {
        console.error('[' + pass.name + '] the second payload changed nothing on the ' +
                      'LIVE side — the refresh is not being delivered, and this pass ' +
                      'would compare two first renders');
        process.exit(1);
      }
    }

    const target = pass.view === 'list' ? 'routersListBody' : 'routers-grid';
    if (!live[target].innerHTML) {
      console.error('[' + pass.name + '] the LIVE renderer wrote nothing into #' + target +
                    ' — this pass is comparing two empty strings');
      process.exit(1);
    }
    chars += live[target].innerHTML.length;
    for (const d of diff(live, port)) bad.push(Object.assign({ pass: pass.name }, d));
  }
  // The map's comparable half, once — it does not vary with the view or search.
  {
    const live = runLive(ROWS, '', 'comfortable');
    runPort(ROWS, '', 'comfortable');
    const mapBad = compareMap(live, portMod, live.__doc);
    mapChecks = mapBad.checks;
    for (const d of mapBad) {
      bad.push({ pass: 'the map: ' + d.what, id: d.what, k: 'value',
                 live: d.live, port: d.port });
    }
  }

  if (!bad.length) {
    console.log('routers matches the live renderer (' + PASSES.length + ' passes, ' +
                chars + ' chars of markup, ' + ROWS.length + ' rows, ' +
                mapChecks + ' map checks)');
    return;
  }
  for (const d of bad) {
    console.error('\n[' + d.pass + ']');
    console.error('\n#' + d.id + '.' + d.k + ' differs');
    const at = firstDiff(String(d.live), String(d.port));
    console.error('  live: ' + String(d.live).slice(Math.max(0, at - 60), at + 90));
    console.error('  port: ' + String(d.port).slice(Math.max(0, at - 60), at + 90));
    console.error('  first difference at character ' + at);
  }
  process.exit(1);
}

function firstDiff(a, b) {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) if (a[i] !== b[i]) return i;
  return n;
}

main();
