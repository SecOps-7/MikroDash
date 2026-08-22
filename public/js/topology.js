/* Network Topology map.
 *
 * Loaded after app.js, so `socket`, `esc`, `$`, `fmtMbps` and `pageVisible` are
 * available as globals.
 *
 * Three deliberate design choices, each of which has a cheaper wrong answer:
 *
 *  1. Layout is a DETERMINISTIC RADIAL grouped by local interface, not a force
 *     simulation. The graph is always a one-hop star, so a physics sim would add
 *     a frame loop and non-determinism to solve a problem that does not exist —
 *     and the map would settle differently on every visit.
 *
 *  2. Link bandwidth is joined CLIENT-SIDE against the ifstatus:update payload the
 *     browser already receives. The topology collector therefore stays slow (30 s)
 *     while links animate at the interface cadence (~5 s), with no extra router
 *     load and no duplicated data.
 *
 *  3. Rendering is a KEYED DIFF, never innerHTML. Replacing the SVG would cancel
 *     an in-progress drag and restart every SMIL animation; flow dots are only
 *     rebuilt when their bucketed signature changes, so a routine rate update does
 *     not visibly stutter the whole canvas.
 */
(function () {
  'use strict';

  var NS = 'http://www.w3.org/2000/svg';
  var svg, gViewport, gEdges, gFlow, gNodes, stage, panel, emptyEl, footEl;
  var elSearch, elType, elVlan, elFlowBtn, elRateBtn, elClientsBtn;

  var _data = null;          // last topology:update payload
  var _rates = {};           // iface name -> {rx, tx, running} from ifstatus:update
  var _pos = {};             // node key -> {x, y}   (what is on screen now)
  var _saved = {};           // node key -> {x, y}   (user-dragged only, persisted)
  var _placed = {};          // node key -> {x, y}   (last laid-out spot, in-memory)
  var _rid = null;
  var _sel = null;
  var _filter = '';
  var _typeFilter = '';
  var _vlanFilter = '';
  var _vlanSig = '';         // so the dropdown is only rebuilt when the set changes
  var _showFlow = true;
  var _showRates = true;
  // Clients are collapsed by default: a home LAN has an order of magnitude more
  // clients than infrastructure, and showing them all would bury the very thing
  // the map exists to make legible.
  var _showClients = false;
  var _expanded = {};        // parent key -> true, for per-device expansion

  var _view = { k: 1, x: 0, y: 0 };
  var _draggingKey = null;
  var _pendingRender = false;
  var _rafId = null;
  var _saveTimer = null;
  var _nodeEls = {};         // key -> <g>
  var _edgeEls = {};         // id  -> {g, path, load, label, iface, hit}

  // ── small helpers ────────────────────────────────────────────────────────

  function el(tag, attrs) {
    var e = document.createElementNS(NS, tag);
    if (attrs) for (var k in attrs) e.setAttribute(k, attrs[k]);
    return e;
  }
  function attr(e, k, v) { if (e && e.getAttribute(k) !== String(v)) e.setAttribute(k, v); }
  function text(e, v) { if (e && e.textContent !== String(v)) e.textContent = v; }

  function lsGet(key, fallback) {
    try { var v = localStorage.getItem(key); return v ? JSON.parse(v) : fallback; }
    catch (e) { return fallback; }
  }
  function lsSet(key, val) { try { localStorage.setItem(key, JSON.stringify(val)); } catch (e) {} }

  // Device glyphs, drawn in a 24x24 box centred on the node. Deliberately the
  // same visual vocabulary as the left nav so a switch reads as a switch in both.
  var GLYPHS = {
    router: '<circle cx="18" cy="5" r="2.4"/><circle cx="6" cy="12" r="2.4"/><circle cx="18" cy="19" r="2.4"/>' +
            '<line x1="8.2" y1="13.3" x2="15.6" y2="17.7"/><line x1="15.6" y1="6.3" x2="8.2" y2="10.7"/>',
    switch: '<rect x="2.5" y="5" width="19" height="5" rx="1"/><rect x="2.5" y="14" width="19" height="5" rx="1"/>' +
            '<circle cx="18.5" cy="7.5" r=".9" fill="currentColor" stroke="none"/>' +
            '<circle cx="18.5" cy="16.5" r=".9" fill="currentColor" stroke="none"/>',
    ap:     '<path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/>' +
            '<path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><circle cx="12" cy="20" r="1" fill="currentColor" stroke="none"/>',
    station:'<rect x="3" y="4" width="18" height="12" rx="1.5"/><line x1="8" y1="20" x2="16" y2="20"/>' +
            '<line x1="12" y1="16" x2="12" y2="20"/>',
    phone:  '<path d="M6 3h12v18H6z"/><line x1="10" y1="18" x2="14" y2="18"/>',
    modem:  '<rect x="2.5" y="9" width="19" height="8" rx="1.5"/><line x1="6" y1="13" x2="6" y2="13.01"/>' +
            '<line x1="9.5" y1="13" x2="9.5" y2="13.01"/><path d="M15 12.5h4"/>',
    repeater:'<path d="M4 10a8 8 0 0 1 16 0"/><circle cx="12" cy="15" r="2"/>',
    other:  '<path d="M12 2.5l8.2 4.7v9.6L12 21.5 3.8 16.8V7.2z"/>',
    unknown:'<circle cx="12" cy="12" r="9"/><path d="M9.4 9.4a2.7 2.7 0 0 1 5.2.9c0 1.8-2.6 2.7-2.6 2.7"/>' +
            '<line x1="12" y1="17" x2="12" y2="17.01"/>',
  };
  function glyph(type) { return GLYPHS[type] || GLYPHS.unknown; }

  var TYPE_LABEL = {
    router: 'Router', switch: 'Switch', ap: 'Access point', station: 'Station',
    phone: 'VoIP phone', modem: 'Modem', repeater: 'Repeater',
    other: 'Other device', unknown: 'Unidentified',
  };

  // ── layout ───────────────────────────────────────────────────────────────

  /**
   * Group neighbours by their local interface, give each group an angular sector
   * proportional to its size, and place nodes on a ring inside it. Groups are
   * sorted by interface name, so the same network always lays out the same way.
   */
  /** Is this client currently drawn? Its parent must be expanded (or the global
   *  toggle on), and a search match forces it visible so filtering still works
   *  while everything is collapsed. */
  function clientShown(n) {
    if (n.kind !== 'client') return true;
    // A search or VLAN match forces the client into view, so filtering works
    // from the collapsed state rather than appearing to find nothing.
    if ((_filter || _vlanFilter) && !isFiltered(n)) return true;
    return _showClients || !!_expanded[n.parent];
  }

  function visibleNodes(nodes) {
    return nodes.filter(clientShown);
  }

  /** Edges whose BOTH ends are on screen. Rendering the raw edge list draws
   *  links to collapsed clients, which have no position and so anchor at the
   *  origin — the stray grey lines that used to fan out during a drag and when
   *  toggling Flow or Rates. */
  function visibleEdges() {
    if (!_data) return [];
    var shown = {};
    visibleNodes(_data.nodes).forEach(function (n) { shown[n.key] = 1; });
    return _data.edges.filter(function (e) { return shown[e.from] && shown[e.to]; });
  }

  function computeLayout(nodes) {
    // The core is not pinned to the origin — it can be dragged like anything
    // else, and everything below is positioned RELATIVE to wherever it is. Using
    // a hardcoded origin here left the whole map fanned around the core's
    // original spot after it was moved.
    var c0 = _saved.core || { x: 0, y: 0 };
    var out = { core: { x: c0.x, y: c0.y } };

    var neighbours = nodes.filter(function (n) { return n.kind !== 'core'; });
    if (!neighbours.length) return out;

    // Children by parent, so a device behind a switch is drawn behind it rather
    // than beside it. The collector decides parentage; this only draws it.
    var kids = {};
    neighbours.forEach(function (n) {
      var p = n.parent || 'core';
      (kids[p] = kids[p] || []).push(n);
    });
    Object.keys(kids).forEach(function (p) {
      kids[p].sort(function (a, b) {
        var ai = (a.ifaces && a.ifaces[0]) || '', bi = (b.ifaces && b.ifaces[0]) || '';
        return ai === bi ? String(a.name).localeCompare(String(b.name)) : ai.localeCompare(bi);
      });
    });

    // The core's own clients are not a topology tier and must not share the ring
    // with switches and APs, so they are split out and given the arc's gap.
    var coreKids = kids.core || [];
    var tier1 = coreKids.filter(function (n) { return n.kind !== 'client'; });
    var coreClients = coreKids.filter(function (n) { return n.kind === 'client'; });

    // Tier 1 fans around the core on a capped angular STEP. Dividing a full
    // circle would place two devices exactly opposite and make the core read as
    // a pass-through in a chain — the one thing a star must not look like.
    var MAX_ARC = Math.PI * 1.7;
    var STEP = 0.62;
    var step = STEP;
    var span = step * Math.max(0, tier1.length - 1);
    if (span > MAX_ARC) { step = MAX_ARC / Math.max(1, tier1.length - 1); span = MAX_ARC; }

    var base = 2.399963 - span / 2;   // golden-angle base so it is never dead flat
    var R1 = 200;

    /** Where a node ACTUALLY is: a dragged position wins over the computed one.
     *  Anchoring children to the computed slot is what previously left them
     *  behind at the parent's original spot after a drag. */
    function anchor(key) { return _saved[key] || out[key] || { x: 0, y: 0 }; }

    function placeChildren(parent, depth) {
      var list = kids[parent.key];
      if (!list || !list.length || depth > 4) return;
      var p = anchor(parent.key);

      // Fan away from the CORE along the core→parent vector, so wherever the
      // parent is dragged its children swing round to stay outside it.
      var dx = p.x - c0.x, dy = p.y - c0.y;
      var ang = (dx || dy) ? Math.atan2(dy, dx) : 0;

      var isClient = list[0] && list[0].kind === 'client';
      var spread = isClient
        ? Math.min(Math.PI * 1.5, 0.42 * list.length)
        : Math.min(Math.PI * 0.75, 0.5 * list.length);
      var start = ang - spread / 2;
      var stepC = list.length === 1 ? 0 : spread / (list.length - 1);
      var R = (isClient ? 118 : 165) * Math.pow(0.88, depth - 1);

      list.forEach(function (c, i) {
        var a = list.length === 1 ? ang : start + stepC * i;
        out[c.key] = { x: p.x + Math.cos(a) * R * 1.3, y: p.y + Math.sin(a) * R * 0.9 };
        placeChildren(c, depth + 1);
      });
    }

    tier1.forEach(function (n, i) {
      var a = base + step * i;
      out[n.key] = { x: c0.x + Math.cos(a) * R1 * 1.45, y: c0.y + Math.sin(a) * R1 * 0.82 };
      placeChildren(n, 1);
    });

    // The core's clients go in the unused part of the arc, on their own ring, so
    // they never sit on top of the infrastructure fan.
    if (coreClients.length) {
      var gapCentre = base + span + (Math.PI * 2 - span) / 2;
      var cSpread = Math.min(Math.PI * 1.15, 0.3 * coreClients.length);
      var cStart = gapCentre - cSpread / 2;
      var cStep = coreClients.length === 1 ? 0 : cSpread / (coreClients.length - 1);
      coreClients.forEach(function (c, i) {
        var a = coreClients.length === 1 ? gapCentre : cStart + cStep * i;
        var ring = 300 + (i % 2) * 58;         // stagger, so labels do not collide
        out[c.key] = { x: c0.x + Math.cos(a) * ring * 1.35, y: c0.y + Math.sin(a) * ring * 0.86 };
        placeChildren(c, 2);
      });
    }

    // Anything whose parent never resolved (should not happen, but the layout
    // must not drop a node on the floor) gets a slot on the outer ring.
    var orphan = 0;
    neighbours.forEach(function (n) {
      if (out[n.key]) return;
      var a = base + step * (tier1.length + orphan++);
      out[n.key] = { x: c0.x + Math.cos(a) * 320 * 1.45, y: c0.y + Math.sin(a) * 320 * 0.82 };
    });
    return out;
  }

  function applyPositions(nodes) {
    var auto = computeLayout(nodes);
    var next = {};
    nodes.forEach(function (n) {
      // Precedence: an explicit drag, then wherever the node was last PLACED,
      // then a fresh computed slot.
      //
      // The _placed pin is what makes a node independent once it is on screen:
      // dragging a device moves only that device, and its already-expanded
      // children stay where they are. The pin is dropped when a node leaves the
      // canvas (collapse / re-layout / router switch), so re-expanding lays the
      // children out afresh around wherever the parent is by then.
      next[n.key] = _saved[n.key] || _placed[n.key] || auto[n.key] || { x: 0, y: 0 };
      _placed[n.key] = next[n.key];
    });
    _pos = next;
  }

  /** Forget the placement of nodes that are leaving the canvas, so they are laid
   *  out again relative to their parent's current position next time. */
  function unpinClientsOf(parentKey) {
    if (!_data) return;
    _data.nodes.forEach(function (n) {
      if (n.kind !== 'client') return;
      if (parentKey && n.parent !== parentKey) return;
      delete _placed[n.key];
    });
  }

  // ── edge geometry ────────────────────────────────────────────────────────

  function edgePath(x0, y0, x1, y1) {
    var mx = (x0 + x1) / 2, my = (y0 + y1) / 2;
    var dx = x1 - x0, dy = y1 - y0, k = 0.08;
    return 'M' + x0.toFixed(1) + ',' + y0.toFixed(1) +
           ' Q' + (mx - dy * k).toFixed(1) + ',' + (my + dx * k).toFixed(1) +
           ' ' + x1.toFixed(1) + ',' + y1.toFixed(1);
  }

  /** Absolute Mbps, log-scaled. A utilisation percentage would need a link speed
   *  that /interface/print does not reliably report, so it would be a guess. */
  function loadWidth(mbps) {
    return 1.2 + 4 * Math.min(1, Math.log10(1 + Math.max(0, mbps)) / 3);
  }

  function rateFor(iface) {
    if (!iface) return null;
    return _rates[iface] || null;
  }

  // ── render: nodes ────────────────────────────────────────────────────────

  function buildNode(n) {
    var g = el('g', { class: 'topo-node', 'data-key': n.key });
    if (n.kind === 'client') return buildClientNode(n, g);
    g.appendChild(el('circle', { class: 'topo-halo', r: n.kind === 'core' ? 34 : 26 }));
    if (n.kind === 'core') {
      var ring = el('circle', { class: 'topo-ring', r: 42 });
      ring.appendChild(el('animateTransform', {
        attributeName: 'transform', type: 'rotate', from: '0 0 0', to: '360 0 0',
        dur: '24s', repeatCount: 'indefinite',
      }));
      g.appendChild(ring);
    }
    g.appendChild(el('circle', { class: 'topo-disc', r: n.kind === 'core' ? 28 : 20 }));

    var gl = el('g', { class: 'topo-glyph' });
    gl.innerHTML = glyph(n.type);
    var s = n.kind === 'core' ? 1.15 : 0.85;
    gl.setAttribute('transform', 'translate(' + (-12 * s) + ',' + (-12 * s) + ') scale(' + s + ')');
    g.appendChild(gl);

    // The count chip rides the bottom edge of the ring, mirroring the latency
    // pill at the top-right, so the labels start below it rather than at the
    // ring — otherwise the chip would land on top of the device name.
    var ringR = n.kind === 'core' ? 34 : 26;
    var y = ringR + 24;
    g.appendChild(el('text', { class: 'topo-label', y: y }));
    g.appendChild(el('text', { class: 'topo-sub', y: y + 13 }));

    var rttG = el('g', { class: 'topo-rtt', transform: 'translate(19,-19)' });
    rttG.appendChild(el('rect', { class: 'topo-rtt-bg', x: -16, y: -7, width: 32, height: 14, rx: 7 }));
    rttG.appendChild(el('text', { class: 'topo-rtt-tx', y: 3 }));
    g.appendChild(rttG);

    // A chip carrying the client count, which is also the expand affordance.
    // Only infrastructure gets one; clients have no children.
    //
    // On the ring's bottom edge, opposite the latency pill.
    var chip = el('g', { class: 'topo-chip-g', transform: 'translate(0,' + ringR + ')' });
    chip.appendChild(el('rect', { class: 'topo-chip-bg', x: -15, y: -9, width: 30, height: 16, rx: 8 }));
    chip.appendChild(el('text', { class: 'topo-chip-tx', y: 3 }));
    g.appendChild(chip);

    // Native tooltip via textContent — no escaping concern, and free.
    g.appendChild(el('title'));
    gNodes.appendChild(g);
    _nodeEls[n.key] = g;
    return g;
  }

  /** Clients are deliberately much plainer than infrastructure: a small dot and
   *  a label. They are the numerous tier, so any extra chrome multiplies. */
  function buildClientNode(n, g) {
    g.appendChild(el('circle', { class: 'topo-cdot', r: 6 }));
    g.appendChild(el('text', { class: 'topo-clabel', y: 19 }));
    g.appendChild(el('title'));
    gNodes.appendChild(g);
    _nodeEls[n.key] = g;
    return g;
  }

  function renderNodes(nodes) {
    var seen = {};
    nodes.forEach(function (n) {
      seen[n.key] = 1;
      var g = _nodeEls[n.key] || buildNode(n);
      if (n.key === _draggingKey) return;   // never fight an in-progress drag

      var p = _pos[n.key] || { x: 0, y: 0 };
      attr(g, 'transform', 'translate(' + p.x.toFixed(1) + ',' + p.y.toFixed(1) + ')');

      if (n.kind === 'client') {
        var ccls = 'topo-node is-client ' + (n.type === 'wifi-client' ? 'is-wifi' : 'is-wired');
        if (_sel === n.key) ccls += ' is-sel';
        if (isFiltered(n)) ccls += ' is-dim';
        attr(g, 'class', ccls);
        text(g.querySelector('.topo-clabel'), n.name || n.mac);
        text(g.querySelector('title'),
          [n.name || n.mac, n.ip,
           (n.vlanNames || []).length ? 'VLAN ' + n.vlanNames.join('/') : '',
           n.type === 'wifi-client' ? ('Wi-Fi' + (n.ssid ? ' · ' + n.ssid : '') +
            (n.signal ? ' · ' + n.signal + ' dBm' : '')) : 'wired',
           n.mac].filter(Boolean).join(' · '));
        return;
      }

      var cls = 'topo-node st-' + (n.status || 'unknown');
      if (n.kind === 'core') cls += ' is-core';
      if (n.gone) cls += ' is-gone';
      if (_sel === n.key) cls += ' is-sel';
      if (isFiltered(n)) cls += ' is-dim';
      attr(g, 'class', cls);

      text(g.querySelector('.topo-label'), n.name || n.key);
      text(g.querySelector('.topo-sub'), n.ip || n.mac || '');

      var rttG = g.querySelector('.topo-rtt');
      var showRtt = n.kind !== 'core' && isFinite(n.rtt) && n.rtt !== null;
      rttG.style.display = showRtt ? '' : 'none';
      if (showRtt) text(rttG.querySelector('.topo-rtt-tx'), n.rtt.toFixed(n.rtt < 10 ? 1 : 0) + 'ms');

      var chipG = g.querySelector('.topo-chip-g');
      if (chipG) {
        var count = n.clientCount || 0;
        var open = _showClients || !!_expanded[n.key];
        chipG.style.display = count ? '' : 'none';
        if (count) {
          text(chipG.querySelector('.topo-chip-tx'), (open ? '−' : '+') + count);
          chipG.setAttribute('class', 'topo-chip-g' + (open ? ' is-open' : ''));
        }
      }

      text(g.querySelector('title'), tooltipFor(n));
    });

    Object.keys(_nodeEls).forEach(function (k) {
      if (seen[k]) return;
      if (_nodeEls[k].parentNode) _nodeEls[k].parentNode.removeChild(_nodeEls[k]);
      delete _nodeEls[k];
      if (_sel === k) { _sel = null; renderPanel(); }
    });
  }

  function tooltipFor(n) {
    var bits = [n.name || n.key, TYPE_LABEL[n.type] || n.type];
    if (n.ip) bits.push(n.ip);
    if (n.board) bits.push(n.board);
    if (n.gone) bits.push('last seen ' + new Date(n.lastSeen).toLocaleTimeString());
    return bits.join(' · ');
  }

  function isFiltered(n) {
    if (n.kind === 'core') return false;
    if (_typeFilter && n.type !== _typeFilter) return true;
    // A VLAN is a property of a client, so infrastructure is never dimmed by it —
    // dimming the switch a filtered client hangs off would hide the answer.
    if (_vlanFilter && n.kind === 'client' &&
        (n.vlans || []).indexOf(Number(_vlanFilter)) === -1) return true;
    if (!_filter) return false;
    var hay = [n.name, n.identity, n.ip, n.mac, n.board, n.platform,
               (n.vlanNames || []).join(' ')].join(' ').toLowerCase();
    return hay.indexOf(_filter) === -1;
  }

  /** Rebuild the VLAN dropdown from whatever the router actually reported, so it
   *  never offers an option that would match nothing. */
  function syncVlanOptions() {
    if (!elVlan || !_data) return;
    var list = _data.vlans || [];
    var sig = list.map(function (v) { return v.vid + ':' + v.name; }).join(',');
    if (sig === _vlanSig) return;
    _vlanSig = sig;
    var keep = _vlanFilter;
    elVlan.innerHTML = '<option value="">All VLANs</option>' +
      list.map(function (v) {
        return '<option value="' + v.vid + '">' + esc(v.name) +
               (String(v.name) === String(v.vid) ? '' : ' (' + v.vid + ')') + '</option>';
      }).join('');
    // Keep the current selection if it still exists; otherwise fall back to all.
    if (keep && list.some(function (v) { return String(v.vid) === String(keep); })) elVlan.value = keep;
    else { elVlan.value = ''; _vlanFilter = ''; }
  }

  // ── render: edges ────────────────────────────────────────────────────────

  function buildEdge(e) {
    var g = el('g', { 'data-id': e.id });
    var rec = {
      g: g,
      path: el('path', { class: 'topo-edge' }),
      load: el('path', { class: 'topo-edge-load' }),
      hit: el('path', { class: 'topo-edge-hit' }),
      label: el('text', { class: 'topo-elabel' }),
      iface: el('text', { class: 'topo-iflabel' }),
    };
    g.appendChild(rec.path); g.appendChild(rec.load); g.appendChild(rec.hit);
    g.appendChild(rec.iface); g.appendChild(rec.label);
    g.appendChild(el('title'));
    gEdges.appendChild(g);
    _edgeEls[e.id] = rec;
    return rec;
  }

  function renderEdges(edges) {
    var seen = {};
    edges.forEach(function (e) {
      seen[e.id] = 1;
      var rec = _edgeEls[e.id] || buildEdge(e);
      var a = _pos[e.from] || { x: 0, y: 0 };
      var b = _pos[e.to] || { x: 0, y: 0 };
      var d = edgePath(a.x, a.y, b.x, b.y);
      attr(rec.path, 'd', d);
      attr(rec.load, 'd', d);
      attr(rec.hit, 'd', d);

      var cls = 'topo-edge';
      if (e.shared) cls += ' is-shared';
      if (e.inferred) cls += ' is-inferred';
      if (e.gone) cls += ' is-gone';
      attr(rec.path, 'class', cls);

      var r = rateFor(e.iface);
      var total = r ? (r.rx + r.tx) : 0;
      if (r && total > 0.01 && !e.gone) {
        attr(rec.load, 'stroke', r.rx >= r.tx ? 'var(--accent-rx)' : 'var(--accent-tx)');
        attr(rec.load, 'stroke-width', loadWidth(total).toFixed(2));
        rec.load.style.display = '';
      } else {
        rec.load.style.display = 'none';
      }

      var mx = (a.x + b.x) / 2 - (b.y - a.y) * 0.08;
      var my = (a.y + b.y) / 2 + (b.x - a.x) * 0.08;
      attr(rec.iface, 'x', mx.toFixed(1)); attr(rec.iface, 'y', (my - 5).toFixed(1));
      attr(rec.label, 'x', mx.toFixed(1)); attr(rec.label, 'y', (my + 7).toFixed(1));
      text(rec.iface, e.iface || '');

      if (_showRates && r && total > 0.01) {
        text(rec.label, '↓' + fmtShort(r.rx) + '  ↑' + fmtShort(r.tx));
        rec.label.style.display = '';
      } else {
        rec.label.style.display = 'none';
      }

      var tip;
      if (e.inferred) {
        // Say plainly that this link is deduced, and from what. The router can
        // see that the device is behind this switch, but not which switch port.
        tip = 'behind this device on ' + (e.viaPort || 'the same port') +
              ' — inferred: seen via MNDP/CDP only, so it is not directly attached';
        if (e.remoteIface) tip += '\nits port: ' + e.remoteIface;
      } else {
        tip = e.iface || 'link';
        if (e.remoteIface) tip += '  →  ' + e.remoteIface;
        if (e.shared) tip += '  (shared segment — more than one device on this port)';
        if (r) tip += '  ↓' + fmtMbps(r.rx) + '  ↑' + fmtMbps(r.tx);
      }
      text(rec.g.querySelector('title'), tip);

      updateFlow(e, d, total);
    });

    Object.keys(_edgeEls).forEach(function (id) {
      if (seen[id]) return;
      var rec = _edgeEls[id];
      if (rec.g.parentNode) rec.g.parentNode.removeChild(rec.g);
      delete _edgeEls[id];
      removeFlow(id);
    });
  }

  function fmtShort(mbps) {
    var n = +mbps || 0;
    if (n >= 1000) return (n / 1000).toFixed(1) + 'G';
    if (n >= 1) return n.toFixed(1) + 'M';
    return (n * 1000).toFixed(0) + 'K';
  }

  // ── flow animation ───────────────────────────────────────────────────────
  //
  // The throttle here is the whole point. ifstatus:update lands every ~5 s;
  // rebuilding <animateMotion> nodes each time restarts every animation at once
  // and the entire canvas visibly jumps. So dot count and duration are bucketed
  // into a signature and the DOM is only rebuilt when that signature changes.

  var _flowEls = {};

  function flowBudget() {
    var n = Object.keys(_edgeEls).length;
    if (n > 60) return 0;
    if (n > 30) return 1;
    return 4;
  }

  function updateFlow(e, d, totalMbps) {
    var maxDots = flowBudget();
    if (!_showFlow || !maxDots || e.gone || totalMbps <= 0.05) { removeFlow(e.id); return; }

    var dots = Math.max(1, Math.min(maxDots, Math.ceil(Math.log10(1 + totalMbps))));
    var dur = Math.max(0.9, Math.min(6, 6 / (0.5 + Math.log10(1 + totalMbps)))).toFixed(1);
    var sig = dots + ':' + dur + ':' + (e.shared ? 1 : 0);

    var rec = _flowEls[e.id];
    if (rec && rec.sig === sig) { rec.paths.forEach(function (p) { attr(p, 'path', d); }); return; }

    removeFlow(e.id);
    var g = el('g', { class: 'topo-flow-dot' });
    var paths = [];
    var r = rateFor(e.iface) || { rx: 0, tx: 0 };
    var inbound = r.rx >= r.tx;
    for (var i = 0; i < dots; i++) {
      var c = el('circle', { r: 2.2, fill: inbound ? 'var(--accent-rx)' : 'var(--accent-tx)' });
      var m = el('animateMotion', {
        dur: dur + 's', repeatCount: 'indefinite', path: d,
        // Negative begin de-syncs the dots deterministically.
        begin: (-(i * dur) / dots).toFixed(2) + 's',
        keyPoints: inbound ? '1;0' : '0;1', keyTimes: '0;1', calcMode: 'linear',
      });
      c.appendChild(m);
      g.appendChild(c);
      paths.push(m);
    }
    gFlow.appendChild(g);
    _flowEls[e.id] = { g: g, sig: sig, paths: paths };
  }

  function removeFlow(id) {
    var rec = _flowEls[id];
    if (!rec) return;
    if (rec.g.parentNode) rec.g.parentNode.removeChild(rec.g);
    delete _flowEls[id];
  }

  function clearFlow() { Object.keys(_flowEls).forEach(removeFlow); }

  // ── stats, empty state, footer ───────────────────────────────────────────

  function renderStats() {
    var nodes = _data ? _data.nodes.filter(function (n) {
      return n.kind !== 'core' && n.kind !== 'client';
    }) : [];
    var clients = _data ? (_data.clientCount || 0) : 0;
    // Infrastructure count stays the headline; clients ride along so the number
    // does not silently jump when the tier is expanded.
    text($('topoStatDevices'), nodes.length ? String(nodes.length) + (clients ? ' + ' + clients : '') : '—');
    var links = _data ? _data.edges.filter(function (e) { return !e.client; }).length : 0;
    text($('topoStatLinks'), links ? String(links) : '—');

    var thru = 0;
    (_data ? _data.edges : []).forEach(function (e) {
      var r = rateFor(e.iface); if (r) thru += r.rx + r.tx;
    });
    text($('topoStatThru'), thru > 0 ? fmtMbps(thru) : '—');

    var worst = null;
    nodes.forEach(function (n) {
      if (n.rtt !== null && isFinite(n.rtt) && (worst === null || n.rtt > worst)) worst = n.rtt;
    });
    if (_data && _data.pingDenied) text($('topoStatRtt'), 'n/a');
    else text($('topoStatRtt'), worst === null ? '—' : worst.toFixed(worst < 10 ? 1 : 0) + ' ms');
  }

  function renderEmpty() {
    if (!emptyEl) return;
    var count = _data ? _data.nodes.filter(function (n) { return n.kind !== 'core'; }).length : 0;
    if (!_data) {
      emptyEl.className = 'topo-empty show';
      emptyEl.innerHTML = '<b>Waiting for the router…</b>';
      return;
    }
    if (count) { emptyEl.className = 'topo-empty'; emptyEl.innerHTML = ''; return; }

    // An empty map that explains itself. Each branch is a real reason a
    // correctly-working router reports no neighbours.
    var html = '<b>No neighbouring devices discovered</b>';
    var d = _data.discovery;
    if (_data.permissionDenied) {
      html += '<div class="topo-empty-hint">This API user cannot read <code>/ip/neighbor</code>. ' +
              'Grant the <code>read</code> policy to see discovered devices.</div>';
    } else if (d && d.mode === 'tx-only') {
      html += '<div class="topo-empty-hint">Discovery is set to <code>tx-only</code>, so this router ' +
              'advertises itself but never records neighbours. Set it to <code>tx-and-rx</code> under ' +
              '<code>/ip/neighbor/discovery-settings</code>.</div>';
    } else if (d && d.interfaceList && d.interfaceList !== 'all') {
      html += '<div class="topo-empty-hint">Discovery only runs on the <code>' + esc(d.interfaceList) +
              '</code> interface list. Devices reached through other interfaces will not appear here.</div>';
    } else {
      html += '<div class="topo-empty-hint">Nothing is advertising LLDP, CDP or MNDP on this router’s ' +
              'discovery interfaces. Unmanaged switches and most end devices stay invisible by design.</div>';
    }
    emptyEl.className = 'topo-empty show';
    emptyEl.innerHTML = html;
  }

  function renderFoot() {
    if (!footEl) return;
    var parts = [];
    var d = _data && _data.discovery;
    if (d && d.protocol && d.protocol.length) parts.push('Discovery: ' + esc(d.protocol.join(', ')));
    if (d && d.interfaceList) parts.push('on <code>' + esc(d.interfaceList) + '</code>');
    if (_data && _data.pingDenied) {
      parts.push('<span style="color:var(--accent-warn)">latency needs the <code>test</code> policy</span>');
    }
    var legend = [['up', '--accent-ok'], ['warn', '--accent-warn'], ['down', '--accent-err']]
      .map(function (p) {
        return '<span class="topo-legend" style="color:var(' + p[1] + ')">' +
               '<span class="topo-swatch"></span>' + p[0] + '</span>';
      }).join('');
    // Say which links are observed and which are deduced, rather than presenting
    // the whole map as equally certain.
    var inferred = (_data && _data.edges || []).filter(function (e) { return e.inferred; }).length;
    var note = inferred
      ? '<span style="color:var(--accent-alt)">' + inferred + ' link' +
        (inferred === 1 ? '' : 's') + ' inferred</span> &middot; LLDP/CDP/MNDP'
      : 'LLDP/CDP/MNDP';
    footEl.innerHTML = legend +
      '<span style="margin-left:auto;text-align:right">' + parts.join(' &middot; ') +
      (parts.length ? ' &middot; ' : '') + note + '</span>';
  }

  // ── detail panel ─────────────────────────────────────────────────────────

  function row(k, v) {
    if (v === undefined || v === null || v === '') return '';
    return '<dt>' + esc(k) + '</dt><dd>' + esc(v) + '</dd>';
  }

  function renderPanel() {
    if (!panel) return;
    if (!_sel || !_data) { panel.className = 'topo-panel'; return; }
    var n = null;
    for (var i = 0; i < _data.nodes.length; i++) if (_data.nodes[i].key === _sel) { n = _data.nodes[i]; break; }
    if (!n) { panel.className = 'topo-panel'; return; }

    var rate = null;
    (_data.edges || []).forEach(function (e) {
      if (e.to === n.key) { var r = rateFor(e.iface); if (r) rate = r; }
    });

    var badges = '<span class="topo-badge">' + esc(TYPE_LABEL[n.type] || n.type) + '</span>';
    if (n.kind !== 'core' && n.typeSource !== 'caps') {
      badges += '<span class="topo-badge is-guess" title="Inferred from the board or platform — this ' +
                'device did not advertise LLDP capabilities">guessed</span>';
    }
    if (n.gone) {
      badges += '<span class="topo-badge" style="border-color:var(--accent-err);color:var(--accent-err)">offline</span>';
    }
    (n.running || []).forEach(function (r) { badges += '<span class="topo-badge">' + esc(r) + '</span>'; });

    if (n.kind === 'client') {
      panel.innerHTML =
        '<div class="topo-panel-hdr" style="color:var(' +
          (n.type === 'wifi-client' ? '--accent-rx' : '--accent-tx') + ')">' +
          '<svg viewBox="0 0 24 24">' + glyph(n.type === 'wifi-client' ? 'ap' : 'station') + '</svg>' +
          '<span class="topo-panel-name">' + esc(n.name || n.mac) + '</span>' +
          '<button class="topo-panel-close" id="topoPanelClose" aria-label="Close">&times;</button>' +
        '</div>' +
        '<div class="topo-badges"><span class="topo-badge">' +
          (n.type === 'wifi-client' ? 'Wi-Fi client' : 'Wired client') + '</span>' +
          (n.vlanNames || []).map(function (v) {
            return '<span class="topo-badge is-vlan">' + esc(v) + '</span>';
          }).join('') +
          // Say plainly when the attachment was deduced from a shared port
          // rather than observed, so a wrong guess is visible rather than silent.
          (n.attrib === 'port'
            ? '<span class="topo-badge is-guess" title="Deduced: this device shares a port with that switch. The router cannot see which switch port.">inferred</span>'
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
      panel.className = 'topo-panel open';
      var cc = $('topoPanelClose');
      if (cc) cc.addEventListener('click', function () { selectNode(null); });
      return;
    }

    var live = '';
    if (n.kind !== 'core') {
      live += row('Latency', _data.pingDenied ? 'unavailable (test policy)'
        : (n.rtt !== null && isFinite(n.rtt) ? n.rtt.toFixed(1) + ' ms' : '—'));
      live += row('Loss', n.loss !== null && isFinite(n.loss) ? n.loss + '%' : '—');
    } else {
      live += row('CPU', n.cpuLoad !== null && isFinite(n.cpuLoad) ? n.cpuLoad + '%' : '');
      live += row('Memory', n.memPct !== null && isFinite(n.memPct) ? n.memPct + '%' : '');
    }
    if (rate) {
      live += row('Link down', fmtMbps(rate.rx));
      live += row('Link up', fmtMbps(rate.tx));
    }

    var caps = (n.capsEnabled && n.capsEnabled.length ? n.capsEnabled : (n.caps || [])).join(', ');

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
        row('Behind', parentName(n)) +
        row('Router port', n.port || (n.ifaces || []).join(', ')) +
        row('Remote port', n.remoteIface) +
        row('Seen via', (n.via || []).join(', ')) +
        row('Age', n.ageSec !== null && isFinite(n.ageSec) ? n.ageSec + ' s'
                 : (n.gone ? 'no longer advertising' : '')) +
        row('Capabilities', caps || (n.kind === 'core' ? '' : 'none advertised')) +
        row('Description', n.description) +
      '</dl>';

    panel.className = 'topo-panel open';
    var close = $('topoPanelClose');
    if (close) close.addEventListener('click', function () { selectNode(null); });
  }

  /** Name of the device this one sits behind, for the detail panel. */
  function parentName(n) {
    if (n.parent === 'core' && _data) return _data.nodes[0].name;
    if (!n.parent || !_data) return '';
    for (var i = 0; i < _data.nodes.length; i++) {
      if (_data.nodes[i].key === n.parent) return _data.nodes[i].name || n.parent;
    }
    return n.parent;
  }

  function statusVar(n) {
    if (n.kind === 'core') return '--accent-rx';
    if (n.status === 'up') return '--accent-ok';
    if (n.status === 'warn') return '--accent-warn';
    if (n.status === 'down') return '--accent-err';
    return '--text-muted';
  }

  function selectNode(key) {
    _sel = key;
    renderPanel();
    if (_data) renderNodes(_data.nodes);
  }

  // ── viewport ─────────────────────────────────────────────────────────────

  function applyView() {
    attr(gViewport, 'transform',
      'translate(' + _view.x.toFixed(1) + ',' + _view.y.toFixed(1) + ') scale(' + _view.k.toFixed(3) + ')');
  }

  function saveView() { if (_rid) lsSet('mikrodash.topo.view.' + _rid, _view); }

  function fitView() {
    var keys = Object.keys(_pos);
    if (!keys.length || !stage) return;
    var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    keys.forEach(function (k) {
      var p = _pos[k];
      minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x);
      minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y);
    });
    var pad = 80;
    var w = (maxX - minX) + pad * 2, h = (maxY - minY) + pad * 2;
    var rect = stage.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    var k = Math.min(rect.width / Math.max(w, 1), rect.height / Math.max(h, 1));
    _view.k = Math.max(0.35, Math.min(1.6, k));
    _view.x = rect.width / 2 - ((minX + maxX) / 2) * _view.k;
    _view.y = rect.height / 2 - ((minY + maxY) / 2) * _view.k;
    applyView();
    saveView();
  }

  // ── main render ──────────────────────────────────────────────────────────

  function render() {
    if (!_data || !svg) return;
    if (_draggingKey) { _pendingRender = true; return; }
    syncVlanOptions();
    var vis = visibleNodes(_data.nodes);
    applyPositions(vis);
    renderEdges(visibleEdges());
    renderNodes(vis);
    renderStats();
    renderEmpty();
    renderFoot();
    renderPanel();
  }

  /** The ~5 s path: geometry is unchanged, only rates moved. */
  function renderLive() {
    if (!_data || !svg || _draggingKey) return;
    renderEdges(visibleEdges());
    renderStats();
  }

  function scheduleFrame(fn) {
    if (_rafId) return;
    _rafId = requestAnimationFrame(function () { _rafId = null; fn(); });
  }

  // ── persistence ──────────────────────────────────────────────────────────

  function loadSaved() {
    if (!_rid) return;
    _saved = lsGet('mikrodash.topo.pos.' + _rid, {}) || {};
    var v = lsGet('mikrodash.topo.view.' + _rid, null);
    if (v && isFinite(v.k)) { _view = v; applyView(); }

    fetch('/api/topology-layout?routerId=' + encodeURIComponent(_rid), { credentials: 'same-origin' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) {
        if (!j || !j.positions) return;
        _saved = j.positions;
        lsSet('mikrodash.topo.pos.' + _rid, _saved);
        render();
      })
      .catch(function () { /* the localStorage copy is already applied */ });
  }

  function savePositions() {
    if (!_rid) return;
    lsSet('mikrodash.topo.pos.' + _rid, _saved);
    clearTimeout(_saveTimer);
    _saveTimer = setTimeout(function () {
      fetch('/api/topology-layout', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ routerId: _rid, positions: _saved }),
      }).catch(function () {});
    }, 800);
  }

  // ── interaction ──────────────────────────────────────────────────────────

  function svgPoint(evt) {
    var rect = svg.getBoundingClientRect();
    return {
      x: (evt.clientX - rect.left - _view.x) / _view.k,
      y: (evt.clientY - rect.top - _view.y) / _view.k,
    };
  }

  function wireInteraction() {
    var dragStart = null, panStart = null, moved = 0;

    svg.addEventListener('pointerdown', function (e) {
      var nodeEl = e.target.closest ? e.target.closest('.topo-node') : null;
      moved = 0;
      // The count chip toggles that device's clients instead of starting a drag,
      // so expanding is one click on the thing showing the number.
      var chipEl = e.target.closest ? e.target.closest('.topo-chip-g') : null;
      if (chipEl && nodeEl) {
        var k = nodeEl.getAttribute('data-key');
        if (_expanded[k]) {
          delete _expanded[k];
          unpinClientsOf(k);        // so a later expand starts from where the parent is now
        } else {
          _expanded[k] = true;
        }
        render();
        return;
      }
      if (nodeEl) {
        _draggingKey = nodeEl.getAttribute('data-key');
        var p = svgPoint(e);
        var cur = _pos[_draggingKey] || { x: 0, y: 0 };
        dragStart = { px: p.x, py: p.y, nx: cur.x, ny: cur.y };
        nodeEl.classList.add('is-dragging');
        nodeEl.classList.remove('is-animated');
      } else {
        panStart = { x: e.clientX, y: e.clientY, vx: _view.x, vy: _view.y };
        svg.classList.add('is-panning');
      }
      try { svg.setPointerCapture(e.pointerId); } catch (_) {}
    });

    svg.addEventListener('pointermove', function (e) {
      if (_draggingKey && dragStart) {
        var p = svgPoint(e);
        moved = Math.max(moved, Math.abs(p.x - dragStart.px) + Math.abs(p.y - dragStart.py));
        _pos[_draggingKey] = {
          x: dragStart.nx + (p.x - dragStart.px),
          y: dragStart.ny + (p.y - dragStart.py),
        };
        scheduleFrame(function () {
          var g = _nodeEls[_draggingKey];
          if (!g) return;
          var q = _pos[_draggingKey];
          attr(g, 'transform', 'translate(' + q.x.toFixed(1) + ',' + q.y.toFixed(1) + ')');
          if (_data) renderEdges(visibleEdges());
        });
      } else if (panStart) {
        moved = Math.max(moved, Math.abs(e.clientX - panStart.x) + Math.abs(e.clientY - panStart.y));
        _view.x = panStart.vx + (e.clientX - panStart.x);
        _view.y = panStart.vy + (e.clientY - panStart.y);
        scheduleFrame(applyView);
      }
    });

    function endPointer(e) {
      if (_draggingKey) {
        var key = _draggingKey;
        var g = _nodeEls[key];
        if (g) g.classList.remove('is-dragging');
        _draggingKey = null;
        dragStart = null;
        if (moved < 4) {
          selectNode(key === _sel ? null : key);
        } else {
          _saved[key] = _pos[key];
          savePositions();
          // Re-lay out immediately: anything hanging off this node is positioned
          // relative to it, and without this the children only catch up when the
          // next update happens to arrive.
          _pendingRender = true;
        }
        if (_pendingRender) { _pendingRender = false; render(); }
      } else if (panStart) {
        svg.classList.remove('is-panning');
        panStart = null;
        if (moved < 4) selectNode(null);
        saveView();
      }
      try { svg.releasePointerCapture(e.pointerId); } catch (_) {}
    }
    svg.addEventListener('pointerup', endPointer);
    svg.addEventListener('pointercancel', endPointer);

    svg.addEventListener('wheel', function (e) {
      e.preventDefault();
      var rect = svg.getBoundingClientRect();
      var px = e.clientX - rect.left, py = e.clientY - rect.top;
      var k0 = _view.k;
      var k1 = Math.max(0.35, Math.min(3, k0 * Math.exp(-e.deltaY * 0.0015)));
      if (k1 === k0) return;
      _view.x = px - (px - _view.x) * (k1 / k0);
      _view.y = py - (py - _view.y) * (k1 / k0);
      _view.k = k1;
      scheduleFrame(applyView);
      saveView();
    }, { passive: false });

    function zoomBy(f) {
      var rect = svg.getBoundingClientRect();
      var px = rect.width / 2, py = rect.height / 2;
      var k0 = _view.k, k1 = Math.max(0.35, Math.min(3, k0 * f));
      _view.x = px - (px - _view.x) * (k1 / k0);
      _view.y = py - (py - _view.y) * (k1 / k0);
      _view.k = k1;
      applyView(); saveView();
    }
    $('topoZoomIn').addEventListener('click', function () { zoomBy(1.25); });
    $('topoZoomOut').addEventListener('click', function () { zoomBy(0.8); });
    $('topoFit').addEventListener('click', fitView);

    $('topoRelayout').addEventListener('click', function () {
      _saved = {};
      _pos = {};
      _placed = {};
      Object.keys(_nodeEls).forEach(function (k) { _nodeEls[k].classList.add('is-animated'); });
      savePositions();
      render();
      setTimeout(fitView, 60);
    });

    // Filtering re-runs the full render, not just the node pass: a search has to
    // be able to surface a client whose parent is collapsed, which changes which
    // nodes exist on the canvas at all.
    elSearch.addEventListener('input', function () {
      _filter = elSearch.value.toLowerCase().trim();
      render();
    });
    elType.addEventListener('input', function () {
      _typeFilter = elType.value;
      render();
    });
    if (elVlan) {
      elVlan.addEventListener('input', function () {
        _vlanFilter = elVlan.value;
        // Selecting a VLAN reveals matching clients even where the parent is
        // collapsed — otherwise the filter would appear to find nothing.
        render();
      });
    }
    elFlowBtn.addEventListener('click', function () {
      _showFlow = !_showFlow;
      elFlowBtn.classList.toggle('is-on', _showFlow);
      if (!_showFlow) clearFlow();
      if (_data) renderEdges(visibleEdges());
    });
    elRateBtn.addEventListener('click', function () {
      _showRates = !_showRates;
      elRateBtn.classList.toggle('is-on', _showRates);
      if (_data) renderEdges(visibleEdges());
    });
    if (elClientsBtn) {
      elClientsBtn.addEventListener('click', function () {
        _showClients = !_showClients;
        elClientsBtn.classList.toggle('is-on', _showClients);
        // Turning the global toggle off also drops per-device expansions, so the
        // button always means what it says.
        if (!_showClients) { _expanded = {}; unpinClientsOf(null); }
        render();
        setTimeout(fitView, 60);
      });
    }

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && _sel && pageVisible('topology')) selectNode(null);
    });
  }

  // ── animation pause discipline ───────────────────────────────────────────
  // Matches how #netDiagram is handled in app.js: SMIL keeps running in a hidden
  // tab otherwise, burning CPU for something nobody can see.

  function setAnimations(on) {
    if (!svg) return;
    try { if (on) svg.unpauseAnimations(); else svg.pauseAnimations(); } catch (e) {}
  }

  function syncAnimations() {
    setAnimations(!document.hidden && pageVisible('topology'));
  }

  // ── boot ─────────────────────────────────────────────────────────────────

  function init() {
    svg = $('topoSvg');
    if (!svg) return;
    gViewport = $('topoViewport');
    gEdges = $('topoEdges');
    gFlow = $('topoFlow');
    gNodes = $('topoNodes');
    stage = $('topoStage');
    panel = $('topoPanel');
    emptyEl = $('topoEmpty');
    footEl = $('topoFoot');
    elSearch = $('topoSearch');
    elType = $('topoType');
    elVlan = $('topoVlan');
    elFlowBtn = $('topoFlowBtn');
    elRateBtn = $('topoRateBtn');
    elClientsBtn = $('topoClientsBtn');

    applyView();
    wireInteraction();
    renderEmpty();

    socket.on('topology:update', function (data) {
      if (!data) return;
      var firstForRouter = _rid !== data.routerId;
      _data = data;
      if (firstForRouter) {
        _rid = data.routerId;
        _pos = {};
        _saved = {};
        _placed = {};
        _expanded = {};
        clearFlow();
        loadSaved();
      }
      if (pageVisible('topology')) {
        render();
        if (firstForRouter) setTimeout(fitView, 40);
      }
    });

    // Link rates ride in on the interface collector, which the browser already
    // receives router-wide — no extra subscription, no extra router load.
    socket.on('ifstatus:update', function (data) {
      if (!data || !Array.isArray(data.interfaces)) return;
      var next = {};
      data.interfaces.forEach(function (i) {
        next[i.name] = { rx: +i.rxMbps || 0, tx: +i.txMbps || 0, running: !!i.running };
      });
      _rates = next;
      if (pageVisible('topology')) scheduleFrame(renderLive);
    });

    document.addEventListener('mikrodash:pagechange', function (e) {
      if (e.detail === 'topology') {
        render();
        if (_data) setTimeout(fitView, 40);
      }
      syncAnimations();
    });

    document.addEventListener('visibilitychange', syncAnimations);
    socket.on('disconnect', function () { setAnimations(false); });
    socket.on('connect', syncAnimations);

    window.addEventListener('resize', function () { if (pageVisible('topology')) scheduleFrame(applyView); });

    syncAnimations();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
