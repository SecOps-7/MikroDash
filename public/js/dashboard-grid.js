/* ══════════════════════════════════════════════════════════════════════════
   dashboard-grid.js  —  MikroDash configurable 24×22 drag-and-drop grid
   Pure vanilla JS · Pointer Events API · No external dependencies
   ══════════════════════════════════════════════════════════════════════════ */
'use strict';

(function () {
  /* ── Constants ──────────────────────────────────────────────────────────── */
  var COLS = 24, ROWS = 22, GAP = 12, PAD = 20; /* .75rem gap / 1.25rem padding @ 16px base */
  var MIN_W = 1, MIN_H = 1;
  var LS_KEY = 'mikrodash_dashboard_layout_v12'; /* bump only on breaking card-object format changes */

  var CARD_LABELS = {
    'card-traffic':       'Traffic',
    'card-connections':   'Connections',
    'card-system':        'System',
    'card-network':       'Network',
    'card-toptalkers':    'Top Talkers',
    'card-wireguard':     'WireGuard',
    'dc-card-netflow':    'Network Flow',
    'dc-card-ping':       'Ping',
    /* extra cards (hidden by default) */
    'dc-card-signal':     'Signal Health',
    'dc-card-band':       'Band Split',
    'dc-card-physports':  'Physical Ports',
    'dc-card-iputil':     'IP Utilisation',
    'dc-card-destcc':     'Connections Map',
    'dc-card-topcc':      'Top Countries',
    'dc-card-flow':       'Conn. Flow',
    'dc-card-topports':   'Top Ports',
    'dc-card-routes':     'Routes',
    'dc-card-bgp':        'BGP Peers',
    'dc-card-bw':         'Bandwidth',
    'dc-card-fwaction':   'FW Actions',
    'dc-card-fwhits':     'Total Hits',
    'dc-card-logs':       'Logs',
    'dc-card-netwatch':   'NetWatch'
  };

  /* Cards that need a backend Socket.IO room (Tier-2: page-gated collectors).
     Key = card id, value = room key emitted via dashcard:focus/blur.
     Two cards can share the same room (fwaction + fwhits both use 'firewall'). */
  var CARD_ROOMS = {
    'dc-card-fwaction': 'firewall',
    'dc-card-fwhits':   'firewall',
    'dc-card-logs':     'logs'
  };
  /* dc-card-bw uses traffic:update which is already delivered to every socket
     via per-socket emit in traffic.js — no room subscription needed. */

  var DEFAULT_LAYOUT = [
    { id: 'card-traffic',      x: 1,  y: 1,  w: 20, h: 5,  visible: true  },
    { id: 'card-connections',  x: 1,  y: 6,  w: 8,  h: 16, visible: true  },
    { id: 'card-system',       x: 9,  y: 6,  w: 8,  h: 4,  visible: true  },
    { id: 'dc-card-netflow',   x: 9,  y: 10, w: 8,  h: 4,  visible: true  },
    { id: 'card-network',      x: 9,  y: 14, w: 8,  h: 6,  visible: true  },
    { id: 'dc-card-ping',      x: 9,  y: 20, w: 8,  h: 2,  visible: true  },
    { id: 'card-toptalkers',   x: 17, y: 6,  w: 8,  h: 8,  visible: true  },
    { id: 'card-wireguard',    x: 17, y: 14, w: 8,  h: 8,  visible: true  },
    { id: 'dc-card-bw',        x: 21, y: 1,  w: 4,  h: 5,  visible: true  },
    { id: 'dc-card-signal',    x: 1,  y: 1,  w: 8,  h: 4,  visible: false },
    { id: 'dc-card-band',      x: 1,  y: 1,  w: 4,  h: 4,  visible: false },
    { id: 'dc-card-physports', x: 1,  y: 1,  w: 8,  h: 4,  visible: false },
    { id: 'dc-card-iputil',    x: 1,  y: 1,  w: 4,  h: 4,  visible: false },
    { id: 'dc-card-destcc',    x: 1,  y: 1,  w: 8,  h: 6,  visible: false },
    { id: 'dc-card-topcc',     x: 1,  y: 1,  w: 8,  h: 6,  visible: false },
    { id: 'dc-card-flow',      x: 1,  y: 1,  w: 8,  h: 8,  visible: false },
    { id: 'dc-card-topports',  x: 1,  y: 1,  w: 4,  h: 6,  visible: false },
    { id: 'dc-card-routes',    x: 1,  y: 1,  w: 6,  h: 6,  visible: false },
    { id: 'dc-card-bgp',       x: 1,  y: 1,  w: 6,  h: 4,  visible: false },
    { id: 'dc-card-fwaction',  x: 1,  y: 1,  w: 8,  h: 6,  visible: false },
    { id: 'dc-card-fwhits',    x: 1,  y: 1,  w: 4,  h: 4,  visible: false },
    { id: 'dc-card-logs',      x: 1,  y: 1,  w: 10, h: 6,  visible: false },
    { id: 'dc-card-netwatch',  x: 1,  y: 1,  w: 8,  h: 6,  visible: false }
  ];

  /* ── Room management helpers ────────────────────────────────────────────── */

  /* Dispatch custom DOM events so app.js can relay them to the socket without
     creating a circular dependency between the two script files.            */
  function _notifyRoom(eventName, room) {
    document.dispatchEvent(new CustomEvent(eventName, { detail: room }));
  }

  /* Join/leave the correct backend rooms for all currently-visible room-gated
     cards.  Called when the dashboard page gains / loses focus.             */
  function syncDashRooms(focused) {
    var sent = {};
    layout.forEach(function (c) {
      if (!c.visible) return;
      var room = CARD_ROOMS[c.id];
      if (!room || sent[room]) return;
      sent[room] = true;
      _notifyRoom(focused ? 'dashcard:room:focus' : 'dashcard:room:blur', room);
    });
  }

  /* ── DOM refs ────────────────────────────────────────────────────────────── */
  var gridRoot, placeholder, editBtn, editControls, saveBtn, discardBtn,
      addCardBtn, addPanel;

  /* ── State ───────────────────────────────────────────────────────────────── */
  var layout       = [];   // live layout
  var editSnapshot = [];   // copy taken on enterEditMode
  var isEditing    = false;
  var dragState    = null;
  var resizeState  = null;

  /* ══════════════════════════════════════════════════════════════════════════
     Layout helpers
     ══════════════════════════════════════════════════════════════════════════ */

  function cloneLayout(l) {
    return l.map(function (c) { return Object.assign({}, c); });
  }

  function mergeLayout(saved) {
    var byId = {};
    saved.forEach(function (c) { byId[c.id] = c; });
    return DEFAULT_LAYOUT.map(function (def) {
      return byId[def.id] ? byId[def.id] : Object.assign({}, def);
    });
  }

  function loadLayout() {
    try {
      var raw = localStorage.getItem(LS_KEY);
      if (raw) {
        var parsed = JSON.parse(raw);
        if (parsed && Array.isArray(parsed.cards) && parsed.cards.length > 0) {
          return mergeLayout(parsed.cards);
        }
      }
    } catch (e) { /* ignore */ }
    return cloneLayout(DEFAULT_LAYOUT);
  }

  function saveLayout() {
    localStorage.setItem(LS_KEY, JSON.stringify({ cards: layout }));
    fetch('/api/dashboard-layout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ cards: layout })
    })
    .then(function (r) {
      if (!r.ok) console.warn('[MikroDash] dashboard layout save failed — HTTP', r.status);
      else console.log('[MikroDash] dashboard layout saved to server');
    })
    .catch(function (e) { console.warn('[MikroDash] dashboard layout save error:', e); });
  }

  function applyLayout(l) {
    l.forEach(function (c) {
      var el = document.getElementById(c.id);
      if (!el) return;
      if (!c.visible) {
        el.style.display = 'none';
        return;
      }
      el.style.display      = '';
      el.style.gridColumn   = c.x + ' / span ' + c.w;
      el.style.gridRow      = c.y + ' / span ' + c.h;
    });
  }

  function getCard(id) {
    for (var i = 0; i < layout.length; i++) {
      if (layout[i].id === id) return layout[i];
    }
    return null;
  }

  /* Two cards overlap when their rectangles intersect */
  function rectOverlaps(a, b) {
    return a.x < b.x + b.w && a.x + a.w > b.x &&
           a.y < b.y + b.h && a.y + a.h > b.y;
  }

  function hasOverlap(candidate, excludeId) {
    for (var i = 0; i < layout.length; i++) {
      var c = layout[i];
      if (c.id === excludeId || !c.visible) continue;
      if (rectOverlaps(candidate, c)) return true;
    }
    return false;
  }

  function inBounds(x, y, w, h) {
    return x >= 1 && y >= 1 && x + w - 1 <= COLS && y + h - 1 <= ROWS;
  }

  /* Scan left-to-right, top-to-bottom for the first free w×h slot */
  function findFreeSlot(w, h) {
    for (var row = 1; row <= ROWS - h + 1; row++) {
      for (var col = 1; col <= COLS - w + 1; col++) {
        var cand = { id: '__test__', x: col, y: row, w: w, h: h, visible: true };
        if (!hasOverlap(cand, '__test__')) return { x: col, y: row };
      }
    }
    return { x: 1, y: 1 };   // last-resort fallback
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Grid geometry  (cell sizes in pixels)
     ══════════════════════════════════════════════════════════════════════════ */

  function getCellSize() {
    var r    = gridRoot.getBoundingClientRect();
    var colW = (r.width  - 2 * PAD - (COLS - 1) * GAP) / COLS;
    var rowH = (r.height - 2 * PAD - (ROWS - 1) * GAP) / ROWS;
    return { colW: colW, rowH: rowH, r: r };
  }

  /* Convert 1-based grid coords to pixel rect relative to the grid root */
  function cellToPixel(x, y, w, h) {
    var sz = getCellSize();
    return {
      left:   PAD + (x - 1) * (sz.colW + GAP),
      top:    PAD + (y - 1) * (sz.rowH + GAP),
      width:  w * sz.colW + (w - 1) * GAP,
      height: h * sz.rowH + (h - 1) * GAP
    };
  }

  /* Convert grid-root-relative pixel coords to 1-based column/row */
  function ptrToCell(pxRel, pyRel) {
    var sz  = getCellSize();
    var col = Math.floor((pxRel - PAD) / (sz.colW + GAP)) + 1;
    var row = Math.floor((pyRel - PAD) / (sz.rowH + GAP)) + 1;
    return {
      col: Math.max(1, Math.min(COLS, col)),
      row: Math.max(1, Math.min(ROWS, row))
    };
  }

  /* Update CSS variables used by the grid-line overlay */
  function updateGridOverlay() {
    var sz = getCellSize();
    gridRoot.style.setProperty('--grid-cell-w', (sz.colW + GAP) + 'px');
    gridRoot.style.setProperty('--grid-cell-h', (sz.rowH + GAP) + 'px');
    gridRoot.style.setProperty('--grid-pad-x',  PAD + 'px');
    gridRoot.style.setProperty('--grid-pad-y',  PAD + 'px');
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Edit mode
     ══════════════════════════════════════════════════════════════════════════ */

  function enterEditMode() {
    isEditing    = true;
    editSnapshot = cloneLayout(layout);
    gridRoot.classList.add('dashboard--editing');
    editBtn.style.display      = 'none';
    editControls.style.display = 'flex';
    updateGridOverlay();
  }

  function exitEditMode(doSave) {
    isEditing = false;
    if (doSave) {
      saveLayout();
    } else {
      layout = editSnapshot;
      applyLayout(layout);
    }
    gridRoot.classList.remove('dashboard--editing');
    editBtn.style.display      = 'flex';
    editControls.style.display = 'none';
    closeAddPanel();
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Add / Remove cards
     ══════════════════════════════════════════════════════════════════════════ */

  function removeCard(id) {
    var c = getCard(id);
    if (c) c.visible = false;
    applyLayout(layout);
    renderAddPanel();
    /* Leave room if dashboard is active and no other visible card uses it */
    var room = CARD_ROOMS[id];
    if (room && _dashActive()) {
      var stillNeeded = layout.some(function (x) {
        return x.id !== id && x.visible && CARD_ROOMS[x.id] === room;
      });
      if (!stillNeeded) _notifyRoom('dashcard:room:blur', room);
    }
  }

  function addCard(id) {
    var c = getCard(id);
    if (!c) return;
    var defW = c.w || 3, defH = c.h || 2;
    var slot = findFreeSlot(defW, defH);
    c.x = slot.x;
    c.y = slot.y;
    c.w = defW;
    c.h = defH;
    c.visible = true;
    applyLayout(layout);
    renderAddPanel();
    /* Join room if dashboard is active and this is the first card using it */
    var room = CARD_ROOMS[id];
    if (room && _dashActive()) {
      var alreadyJoined = layout.some(function (x) {
        return x.id !== id && x.visible && CARD_ROOMS[x.id] === room;
      });
      if (!alreadyJoined) _notifyRoom('dashcard:room:focus', room);
    }
  }

  function _dashActive() {
    var p = document.getElementById('page-dashboard');
    return p && p.classList.contains('active');
  }

  function renderAddPanel() {
    addPanel.innerHTML = '';

    var hdr = document.createElement('div');
    hdr.className   = 'dash-add-header';
    hdr.textContent = 'Hidden Cards';
    addPanel.appendChild(hdr);

    var hidden = layout.filter(function (c) { return !c.visible; });

    if (hidden.length === 0) {
      var empty = document.createElement('div');
      empty.className   = 'dash-add-empty';
      empty.textContent = 'All cards are visible';
      addPanel.appendChild(empty);
    } else {
      var chips = document.createElement('div');
      chips.className = 'dash-add-chips';
      hidden.forEach(function (c) {
        var chip = document.createElement('button');
        chip.type      = 'button';
        chip.className = 'dash-add-chip';
        chip.innerHTML = '<span>+</span>' + (CARD_LABELS[c.id] || c.id);
        chip.addEventListener('click', function () { addCard(c.id); });
        chips.appendChild(chip);
      });
      addPanel.appendChild(chips);
    }

    var resetLink = document.createElement('a');
    resetLink.className   = 'dash-reset-link';
    resetLink.href        = '#';
    resetLink.textContent = 'Reset to default layout';
    resetLink.addEventListener('click', function (e) {
      e.preventDefault();
      layout = cloneLayout(DEFAULT_LAYOUT);
      applyLayout(layout);
      closeAddPanel();
    });
    addPanel.appendChild(resetLink);
  }

  function openAddPanel() {
    renderAddPanel();
    addPanel.classList.add('open');
  }

  function closeAddPanel() {
    addPanel.classList.remove('open');
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Drag  (Pointer Events API)
     ══════════════════════════════════════════════════════════════════════════ */

  function startDrag(cardId, handleEl, e) {
    e.preventDefault();

    var cardEl  = document.getElementById(cardId);
    var c       = getCard(cardId);
    if (!cardEl || !c || !c.visible) return;

    var rect    = cardEl.getBoundingClientRect();
    var ptrOffX = e.clientX - rect.left;
    var ptrOffY = e.clientY - rect.top;

    /* Ghost: simple rectangle that follows the pointer */
    var ghost = document.createElement('div');
    ghost.id           = 'dash-ghost';
    ghost.style.width  = rect.width  + 'px';
    ghost.style.height = rect.height + 'px';
    ghost.style.left   = rect.left   + 'px';
    ghost.style.top    = rect.top    + 'px';
    ghost.textContent  = CARD_LABELS[cardId] || '';
    document.body.appendChild(ghost);

    cardEl.style.opacity = '0.2';

    dragState = {
      cardId:     cardId,
      ptrOffX:    ptrOffX,
      ptrOffY:    ptrOffY,
      snapX:      c.x,
      snapY:      c.y,
      ghost:      ghost,
      handle:     handleEl,
      ptId:       e.pointerId,
      swapTarget: null,   // card id currently being hovered for swap
      swapTimer:  null    // setTimeout handle for the 1.5 s countdown
    };

    handleEl.setPointerCapture(e.pointerId);
    handleEl.addEventListener('pointermove',   onDragMove);
    handleEl.addEventListener('pointerup',     onDragEnd);
    handleEl.addEventListener('pointercancel', onDragEnd);

    placeholder.style.display = 'block';
    updatePlaceholder(c.x, c.y, c.w, c.h);
  }

  function onDragMove(e) {
    if (!dragState) return;
    var ds      = dragState;
    var c       = getCard(ds.cardId);
    var ghost   = ds.ghost;
    var gridRect = gridRoot.getBoundingClientRect();

    /* Move ghost */
    var ghostLeft = e.clientX - ds.ptrOffX;
    var ghostTop  = e.clientY - ds.ptrOffY;
    ghost.style.left = ghostLeft + 'px';
    ghost.style.top  = ghostTop  + 'px';

    /* Card's top-left relative to grid's inner area */
    var relLeft = ghostLeft - gridRect.left - PAD;
    var relTop  = ghostTop  - gridRect.top  - PAD;

    var cell = ptrToCell(relLeft + PAD, relTop + PAD);
    var col  = Math.max(1, Math.min(COLS - c.w + 1, cell.col));
    var row  = Math.max(1, Math.min(ROWS - c.h + 1, cell.row));

    var candidate = { id: ds.cardId, x: col, y: row, w: c.w, h: c.h, visible: true };
    if (inBounds(col, row, c.w, c.h) && !hasOverlap(candidate, ds.cardId)) {
      ds.snapX = col;
      ds.snapY = row;
    }

    updatePlaceholder(ds.snapX, ds.snapY, c.w, c.h);

    /* ── Swap hover detection ──────────────────────────────────────────────
       Find which visible card (if any) the ghost centre is currently over.
       If the ghost dwells over the same card for 1.5 s, swap the two cards. */
    var ghostCx = ghostLeft + parseFloat(ghost.style.width)  / 2;
    var ghostCy = ghostTop  + parseFloat(ghost.style.height) / 2;
    var hoveredId = null;
    for (var i = 0; i < layout.length; i++) {
      var lc = layout[i];
      if (!lc.visible || lc.id === ds.cardId) continue;
      var el = document.getElementById(lc.id);
      if (!el) continue;
      var er = el.getBoundingClientRect();
      if (ghostCx >= er.left && ghostCx <= er.right &&
          ghostCy >= er.top  && ghostCy <= er.bottom) {
        hoveredId = lc.id;
        break;
      }
    }

    if (hoveredId !== ds.swapTarget) {
      clearSwapPending();
      ds.swapTarget = hoveredId;
      if (hoveredId) {
        var tEl = document.getElementById(hoveredId);
        if (tEl) tEl.classList.add('dash-swap-pending');
        /* Capture hoveredId so the closure is stable even if swapTarget changes */
        (function (tid) {
          ds.swapTimer = setTimeout(function () {
            if (dragState && dragState.swapTarget === tid) {
              doSwap(dragState.cardId, tid);
            }
          }, 1500);
        }(hoveredId));
      }
    }
  }

  /* Cancel any pending swap countdown and remove the highlight */
  function clearSwapPending() {
    if (!dragState) return;
    var ds = dragState;
    if (ds.swapTimer) { clearTimeout(ds.swapTimer); ds.swapTimer = null; }
    if (ds.swapTarget) {
      var el = document.getElementById(ds.swapTarget);
      if (el) el.classList.remove('dash-swap-pending');
      ds.swapTarget = null;
    }
  }

  /* Exchange position + dimensions of the dragged card and the target card,
     then end the drag so both cards settle into their new slots. */
  function doSwap(draggingId, targetId) {
    var a = getCard(draggingId);
    var b = getCard(targetId);
    if (!a || !b) return;
    var ax = a.x, ay = a.y, aw = a.w, ah = a.h;
    a.x = b.x; a.y = b.y; a.w = b.w; a.h = b.h;
    b.x = ax;  b.y = ay;  b.w = aw;  b.h = ah;
    applyLayout(layout);
    endDrag();
  }

  /* Shared cleanup — called by both normal drop and swap */
  function endDrag() {
    if (!dragState) return;
    var ds     = dragState;
    var cardEl = document.getElementById(ds.cardId);
    clearSwapPending();
    if (cardEl) cardEl.style.opacity = '';
    ds.ghost.remove();
    placeholder.style.display = 'none';
    ds.handle.removeEventListener('pointermove',   onDragMove);
    ds.handle.removeEventListener('pointerup',     onDragEnd);
    ds.handle.removeEventListener('pointercancel', onDragEnd);
    try { ds.handle.releasePointerCapture(ds.ptId); } catch (_) {}
    dragState = null;
  }

  function onDragEnd(e) {
    if (!dragState) return;
    var ds = dragState;
    var c  = getCard(ds.cardId);
    /* Apply the last valid snap position */
    c.x = ds.snapX;
    c.y = ds.snapY;
    applyLayout(layout);
    endDrag();
  }

  function updatePlaceholder(x, y, w, h) {
    var pos = cellToPixel(x, y, w, h);
    placeholder.style.left   = pos.left   + 'px';
    placeholder.style.top    = pos.top    + 'px';
    placeholder.style.width  = pos.width  + 'px';
    placeholder.style.height = pos.height + 'px';
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Resize  (Pointer Events API)
     ══════════════════════════════════════════════════════════════════════════ */

  function startResize(cardId, dir, handleEl, e) {
    e.preventDefault();
    e.stopPropagation();

    var c = getCard(cardId);
    if (!c) return;

    resizeState = {
      cardId:     cardId,
      dir:        dir,
      origX:      c.x,
      origY:      c.y,
      origW:      c.w,
      origH:      c.h,
      ptrStartX:  e.clientX,
      ptrStartY:  e.clientY,
      handle:     handleEl,
      ptId:       e.pointerId
    };

    handleEl.setPointerCapture(e.pointerId);
    handleEl.addEventListener('pointermove',   onResizeMove);
    handleEl.addEventListener('pointerup',     onResizeEnd);
    handleEl.addEventListener('pointercancel', onResizeEnd);
  }

  function onResizeMove(e) {
    if (!resizeState) return;
    var rs = resizeState;
    var c  = getCard(rs.cardId);
    var sz = getCellSize();

    var dx    = e.clientX - rs.ptrStartX;
    var dy    = e.clientY - rs.ptrStartY;
    var dCols = Math.round(dx / (sz.colW + GAP));
    var dRows = Math.round(dy / (sz.rowH + GAP));

    var nx = rs.origX, ny = rs.origY, nw = rs.origW, nh = rs.origH;

    if (rs.dir.indexOf('e') !== -1)
      nw = Math.max(MIN_W, Math.min(COLS - rs.origX + 1, rs.origW + dCols));

    if (rs.dir.indexOf('s') !== -1)
      nh = Math.max(MIN_H, Math.min(ROWS - rs.origY + 1, rs.origH + dRows));

    if (rs.dir.indexOf('w') !== -1) {
      var newX = Math.max(1, Math.min(rs.origX + rs.origW - MIN_W, rs.origX + dCols));
      nw = rs.origX + rs.origW - newX;
      nx = newX;
    }

    if (rs.dir.indexOf('n') !== -1) {
      var newY = Math.max(1, Math.min(rs.origY + rs.origH - MIN_H, rs.origY + dRows));
      nh = rs.origY + rs.origH - newY;
      ny = newY;
    }

    if (!inBounds(nx, ny, nw, nh)) return;
    var candidate = { id: rs.cardId, x: nx, y: ny, w: nw, h: nh, visible: true };
    if (hasOverlap(candidate, rs.cardId)) return;

    c.x = nx;  c.y = ny;
    c.w = nw;  c.h = nh;
    applyLayout(layout);
  }

  function onResizeEnd(e) {
    if (!resizeState) return;
    var rs = resizeState;
    rs.handle.removeEventListener('pointermove',   onResizeMove);
    rs.handle.removeEventListener('pointerup',     onResizeEnd);
    rs.handle.removeEventListener('pointercancel', onResizeEnd);
    try { rs.handle.releasePointerCapture(rs.ptId); } catch (_) {}
    resizeState = null;
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Event wiring
     ══════════════════════════════════════════════════════════════════════════ */

  function setupEvents() {
    /* Edit button → enter edit mode */
    editBtn.addEventListener('click', function () {
      if (!isEditing) enterEditMode();
    });

    /* Save */
    saveBtn.addEventListener('click', function () { exitEditMode(true); });

    /* Discard */
    discardBtn.addEventListener('click', function () { exitEditMode(false); });

    /* Add Card toggle */
    addCardBtn.addEventListener('click', function (e) {
      e.stopPropagation();
      if (addPanel.classList.contains('open')) {
        closeAddPanel();
      } else {
        openAddPanel();
      }
    });

    /* Close Add Card panel on outside click */
    document.addEventListener('click', function (e) {
      if (!addPanel.contains(e.target) && e.target !== addCardBtn) {
        closeAddPanel();
      }
    });

    /* Unified pointerdown on grid root → drag or resize */
    gridRoot.addEventListener('pointerdown', function (e) {
      if (!isEditing) return;

      var dragHandle   = e.target.closest('.dash-drag-handle');
      var resizeHandle = e.target.closest('.dash-resize');

      if (dragHandle) {
        var card = dragHandle.closest('.dash-card');
        if (card) startDrag(card.id, dragHandle, e);
      } else if (resizeHandle) {
        var card2 = resizeHandle.closest('.dash-card');
        if (card2) startResize(card2.id, resizeHandle.dataset.dir, resizeHandle, e);
      }
    });

    /* Remove buttons */
    gridRoot.addEventListener('click', function (e) {
      var btn = e.target.closest('.dash-remove-btn');
      if (btn && isEditing) removeCard(btn.dataset.card);
    });

    /* Show / hide Edit button when navigating between pages */
    var pageDash = document.getElementById('page-dashboard');
    if (window.MutationObserver && pageDash) {
      new MutationObserver(function () {
        var active = pageDash.classList.contains('active');
        editBtn.style.display = active ? 'flex' : 'none';
        if (!active && isEditing) exitEditMode(false);
        syncDashRooms(active);
      }).observe(pageDash, { attributes: true, attributeFilter: ['class'] });
    }

    /* Update grid overlay CSS vars on resize */
    if (window.ResizeObserver) {
      new ResizeObserver(function () {
        if (isEditing) updateGridOverlay();
      }).observe(gridRoot);
    }
  }

  /* ══════════════════════════════════════════════════════════════════════════
     Init
     ══════════════════════════════════════════════════════════════════════════ */

  function init() {
    gridRoot     = document.getElementById('dash-grid-root');
    placeholder  = document.getElementById('dash-placeholder');
    editBtn      = document.getElementById('dashEditBtn');
    editControls = document.getElementById('dashEditControls');
    saveBtn      = document.getElementById('dashSaveBtn');
    discardBtn   = document.getElementById('dashDiscardBtn');
    addCardBtn   = document.getElementById('dashAddCardBtn');
    addPanel     = document.getElementById('dashAddPanel');

    if (!gridRoot) return;

    layout = loadLayout();   /* fast synchronous render from localStorage / default */
    applyLayout(layout);
    setupEvents();

    /* Show edit button only when dashboard page is active */
    var pageDash = document.getElementById('page-dashboard');
    var isDash   = pageDash && pageDash.classList.contains('active');
    editBtn.style.display = isDash ? 'flex' : 'none';
    if (isDash) syncDashRooms(true);

    /* Fetch server-persisted layout — updates all browsers/devices to the same
       layout without requiring each one to configure separately. */
    fetch('/api/dashboard-layout')
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (!data || !Array.isArray(data.cards) || !data.cards.length) return;
        layout = mergeLayout(data.cards);
        localStorage.setItem(LS_KEY, JSON.stringify({ cards: layout })); /* warm the local cache */
        applyLayout(layout);
      })
      .catch(function () { /* server unavailable — keep localStorage/default */ });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

})();
