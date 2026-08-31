// The Firewall page — a port of the firewall block in public/app.js.
//
// ONE CARD SERVES FOUR ROUTEROS TABLES. Which resource the Add button and the
// row clicks mean depends on the tab, which is what `FW_RES` is for.
//
// ── POSITION IS THE CONFIGURATION ───────────────────────────────────────────
//
// A rule below the final drop does nothing; the same rule above an accept
// blocks everything. So the position column reads the rule's place in the REAL
// table, never in the filtered view — a search must not renumber it — and
// reordering is suppressed while a search is active, because moving a rule past
// rules you cannot see is not something anyone can mean to do.
//
// ── THE RENDERER IS NOT THE ONLY PATH ───────────────────────────────────────
//
// A counter tick rewrites the packet and byte cells IN PLACE rather than
// rebuilding the table, so the flash animation is visible and the scroll
// position survives. Only a structural change — a different rule count, a
// different id order, or a queued pulse — takes the full path.

import { esc, el, resRow, debounce, fmtBytes } from '../dom';
import type { Socket } from '../socket';

export interface FirewallRule {
  id: string; chain: string; action: string; comment: string;
  srcAddress: string; dstAddress: string; protocol: string; dstPort: string;
  inInterface: string;
  packets: number; bytes: number; deltaPackets: number;
  disabled: boolean; dynamic: boolean;
}

export interface FirewallPayload {
  ts: number;
  filter: FirewallRule[]; nat: FirewallRule[]; mangle: FirewallRule[]; raw: FirewallRule[];
  activeTable: string; pollMs: number;
}

const FW_RES: Record<string, string> = {
  filter: 'fwFilter', nat: 'fwNat', mangle: 'fwMangle', raw: 'fwRaw',
};

const ACTION_COLOUR: Record<string, string> = {
  accept: 'rgba(52,211,153,.8)', drop: 'rgba(248,113,113,.8)',
  reject: 'rgba(251,113,133,.8)', masquerade: 'rgba(56,189,248,.8)',
  'dst-nat': 'rgba(251,191,36,.8)', 'src-nat': 'rgba(251,191,36,.8)',
  log: 'rgba(167,139,250,.8)', passthrough: 'rgba(52,211,153,.6)',
};

const CHAIN_COL: Record<string, string> = {
  forward: '#4299e1', input: '#4299e1', output: '#4299e1',
  srcnat: '#48bb78', dstnat: '#48bb78', masquerade: '#48bb78',
  prerouting: '#ed8936', postrouting: '#ed8936',
};

/**
 * The composite identity the server round-trips for a firewall rule.
 *
 * This MIRRORS IdentityOf in internal/resource — the one mirror in this file,
 * and it exists because RouterOS REUSES `*N` ids after a delete, so addressing a
 * rule by id alone is not enough to know it is still the rule that was on
 * screen. Separator included: U+0001, a character no RouterOS value contains.
 */
export function fwIdentity(r: FirewallRule): string {
  return [r.chain || '', r.action || '', r.srcAddress || '', r.dstAddress || '',
    r.comment || ''].join('\u0001');
}

export function actionBadge(a: string): string {
  const col = a === 'accept' || a === 'passthrough' ? 'rgba(52,211,153,.9)'
    : a === 'drop' || a === 'reject' || a === 'tarpit' ? 'rgba(248,113,113,.9)'
      : a === 'log' || a === 'add-src-to-address-list' ? 'rgba(167,139,250,.9)'
        : a === 'masquerade' ? 'rgba(56,189,248,.9)'
          : a === 'dst-nat' || a === 'src-nat' ? 'rgba(251,191,36,.9)'
            : 'rgba(99,130,190,.8)';
  return '<span style="font-family:var(--font-mono);font-size:.63rem;color:' + col +
    ';background:' + col.replace(/[\d.]+\)$/, '0.1)') +
    ';border:1px solid ' + col.replace(/[\d.]+\)$/, '0.25)') +
    ';border-radius:4px;padding:1px 6px;white-space:nowrap">' + esc(a) + '</span>';
}

export function initFirewallPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tableEl = el('firewallTable');
  if (!tableEl) return;
  const firewallTable: HTMLElement = tableEl;

  let data: Partial<FirewallPayload> = {};
  let tab = 'filter';
  let search = '';
  const writable: Record<string, boolean> = {};
  // Which row to pulse once the table redraws. A reorder moves a row among
  // thirty near-identical ones, and without a cue the eye has no way to follow
  // it — including when the move came from undo rather than the operator's hand.
  let pulse: string | null = null;
  let rafId: number | null = null;

  const rulesFor = (t: string): FirewallRule[] =>
    (t === 'filter' ? data.filter : t === 'nat' ? data.nat
      : t === 'raw' ? data.raw : data.mangle) || [];

  // ── Summary cards ─────────────────────────────────────────────────────────

  function updateSummary(d: Partial<FirewallPayload>): void {
    const filter = d.filter || [], nat = d.nat || [], mangle = d.mangle || [], raw = d.raw || [];
    const all = [...filter, ...nat, ...mangle, ...raw];

    const setCount = (totalId: string, disId: string, rules: FirewallRule[]) => {
      const tot = el(totalId), dis = el(disId);
      if (tot) tot.textContent = String(rules.length);
      const nDis = rules.filter((r) => r.disabled).length;
      if (dis) dis.textContent = nDis > 0 ? (nDis + ' off') : '';
    };
    setCount('fwCntFilter', 'fwCntFilterDis', filter);
    setCount('fwCntNat', 'fwCntNatDis', nat);
    setCount('fwCntMangle', 'fwCntMangleDis', mangle);
    setCount('fwCntRaw', 'fwCntRawDis', raw);

    // Action breakdown — what is IN FORCE, so disabled rules are left out. The
    // Rule Counts card above deliberately keeps them: its "N off" badge is what
    // they are for, and it sat empty for as long as the collector dropped them.
    const actionCounts: Record<string, number> = {};
    all.forEach((r) => {
      if (r.disabled) return;
      const a = r.action || '?';
      actionCounts[a] = (actionCounts[a] || 0) + 1;
    });
    const actionEntries = Object.entries(actionCounts).sort((a, b) => b[1] - a[1]).slice(0, 7);
    const maxA = actionEntries.length ? actionEntries[0]![1] : 1;
    const listEl = el('fwActionList');
    if (listEl) {
      listEl.innerHTML = actionEntries.map((e) => {
        const col = ACTION_COLOUR[e[0]] || 'rgba(99,130,190,.7)';
        return '<div class="fw-action-row">' +
          '<span class="fw-action-name" style="color:' + col + '">' + esc(e[0]) + '</span>' +
          '<div class="fw-action-bar-wrap"><div class="fw-action-bar" style="width:' +
          Math.round((e[1] / maxA) * 100) + '%;background:' + col + '"></div></div>' +
          '<span class="fw-action-count">' + e[1] + '</span>' +
        '</div>';
      }).join('') ||
        '<div class="fw-action-row"><span class="fw-action-name" style="color:var(--text-muted)">No rules</span></div>';
    }

    updateChainCount(d);
  }

  function updateChainCount(d: Partial<FirewallPayload>): void {
    const e = el('fwChainCount');
    if (!e) return;
    const all = (d.filter || []).concat(d.nat || []).concat(d.mangle || []).concat(d.raw || []);
    const counts: Record<string, number> = {};
    // Disabled rules are in the payload now, but a chain's weight is about what
    // actually runs.
    all.forEach((r) => { if (r.chain && !r.disabled) counts[r.chain] = (counts[r.chain] || 0) + 1; });
    const entries = Object.keys(counts).map((k) => [k, counts[k]] as [string, number])
      .sort((a, b) => b[1] - a[1]);
    if (!entries.length) {
      e.innerHTML = '<span style="color:var(--text-muted);font-size:.7rem">No rules</span>';
      return;
    }
    const max = entries[0]![1];
    const bars = entries.map((en) => {
      const h = Math.max(3, Math.round((en[1] / max) * 88)) + '%';
      const col = CHAIN_COL[en[0]] || '#a0aec0';
      return '<div class="fw-vbar-col">' +
        '<span class="fw-vbar-count">' + en[1] + '</span>' +
        '<div class="fw-vbar" style="height:' + h + ';background:' + col + '"></div>' +
      '</div>';
    }).join('');
    const labels = entries.map((en) =>
      '<span class="fw-vbar-label" title="' + esc(en[0]) + '">' + esc(en[0]) + '</span>').join('');
    e.innerHTML = '<div class="fw-vbar-bars">' + bars + '</div>' +
      '<div class="fw-vbar-labels">' + labels + '</div>';
  }

  // ── The in-place counter path ─────────────────────────────────────────────

  function updateCountersInPlace(d: Partial<FirewallPayload>): boolean {
    const rules = (tab === 'filter' ? d.filter : tab === 'nat' ? d.nat
      : tab === 'raw' ? d.raw : d.mangle) || [];
    const rows = firewallTable.querySelectorAll<HTMLElement>('tr[data-rule-id]');
    if (!rows.length) return false;
    if (rows.length !== rules.length) return false; // rule count changed — full re-render
    let idMatch = true;
    rows.forEach((row, i) => {
      if (row.dataset.ruleId !== (rules[i] && rules[i].id)) idMatch = false;
    });
    if (!idMatch) return false;

    rows.forEach((row, i) => {
      const r = rules[i];
      if (!r) return;
      const pktCell = row.querySelector<HTMLElement>('.fw-pkt');
      const byteCell = row.querySelector<HTMLElement>('.fw-byte');
      if (pktCell) {
        const newPkt = (r.deltaPackets > 0 ? '<span class="fw-delta-dot"></span>' : '') +
          r.packets.toLocaleString();
        if (pktCell.innerHTML !== newPkt) {
          pktCell.innerHTML = newPkt;
          pktCell.classList.remove('fw-cell-flash');
          void pktCell.offsetWidth; // force reflow to restart the animation
          pktCell.classList.add('fw-cell-flash');
        }
      }
      if (byteCell) {
        const newByte = r.bytes > 0 ? fmtBytes(r.bytes) : '—';
        if (byteCell.textContent !== newByte) {
          byteCell.textContent = newByte;
          byteCell.classList.remove('fw-cell-flash');
          void byteCell.offsetWidth;
          byteCell.classList.add('fw-cell-flash');
        }
      }
    });
    return true;
  }

  // ── The table ─────────────────────────────────────────────────────────────

  function renderTab(): void {
    const full = rulesFor(tab);
    // Position is the rule's place in the REAL table, not in the filtered view
    // — it is what decides whether the rule ever runs, so a search must not
    // renumber it.
    const pos: Record<string, number> = {};
    full.forEach((r, i) => { pos[r.id] = i; });
    const resKey = FW_RES[tab] || 'fwFilter';
    // Two different questions. A viewer who may not write has no use for the
    // controls at all, so their COLUMNS go — leaving two empty columns would be
    // dead space on every row. A search only suppresses the controls:
    // reordering inside a filtered view would move a rule past rules you cannot
    // see, but the columns stay so the table does not reflow on every keystroke.
    const mayWrite = !!writable[resKey];
    const canMove = mayWrite && !search;
    const table = firewallTable.parentElement;
    if (table) table.classList.toggle('fw-noedit', !mayWrite);
    const last = full.length - 1;

    let rules = full;
    if (search) {
      const q = search;
      rules = rules.filter((r) =>
        (r.chain && r.chain.toLowerCase().includes(q)) ||
        (r.action && r.action.toLowerCase().includes(q)) ||
        (r.srcAddress && r.srcAddress.toLowerCase().includes(q)) ||
        (r.dstAddress && r.dstAddress.toLowerCase().includes(q)) ||
        (r.comment && r.comment.toLowerCase().includes(q)) ||
        (r.protocol && r.protocol.toLowerCase().includes(q)) ||
        (r.dstPort && r.dstPort.includes(q)));
    }
    if (!rules.length) {
      firewallTable.innerHTML = '<tr><td colspan="9" class="empty-state">' +
        (search ? 'No rules match search' : 'No rules') + '</td></tr>';
      pulse = null;
      return;
    }

    firewallTable.innerHTML = rules.map((r) => {
      const at = pos[r.id];
      // data-res-move and data-res-drag, not firewall-specific names: the engine
      // owns both flows, including the guard prompt a reorder can raise, and any
      // future ordered resource gets the same behaviour for free.
      const moveCell = canMove
        ? '<button class="fw-move" data-res-move="up" title="Move up"' + (at === 0 ? ' disabled' : '') + '>&#9650;</button>' +
          '<button class="fw-move" data-res-move="down" title="Move down"' + (at === last ? ' disabled' : '') + '>&#9660;</button>'
        : '';
      // U+283F, the six-dot braille cell — the conventional grip, and a single
      // character rather than an SVG repeated down thirty rows.
      const dragCell = canMove
        ? '<span class="fw-drag" data-res-drag title="Drag to reorder">&#10303;</span>' : '';
      let sd = [r.srcAddress, r.dstAddress].filter(Boolean).join(' → ') || (r.inInterface || '');
      if (!sd && r.dstPort) sd = ':' + r.dstPort;
      if (r.protocol) sd += (sd ? ' ' : '') + '/ ' + r.protocol;
      const deltaIndicator = r.deltaPackets > 0 ? '<span class="fw-delta-dot"></span>' : '';
      // `dynamic` rules belong to a service, not to us; the write path refuses
      // them independently, and here they simply do not offer an edit.
      const pulseCls = (pulse && pulse === r.id) ? ' fw-pulse' : '';
      return '<tr class="' + pulseCls.trim() + '" data-rule-id="' + esc(r.id) + '"' +
        (r.disabled ? ' style="opacity:.4"' : '') +
        (r.dynamic ? '' : resRow(r.id, fwIdentity(r), resKey)) + '>' +
        // Handle and arrows together — they do the same job, so they sit as one
        // group of controls with the position reading beside them.
        '<td class="fw-dragcell">' + dragCell + '</td>' +
        '<td class="fw-movecell">' + moveCell + '</td>' +
        '<td class="fw-pos">' + at + '</td>' +
        '<td style="font-size:.7rem;color:var(--text-muted)">' + esc(r.chain) + '</td>' +
        '<td>' + actionBadge(r.action) + '</td>' +
        '<td style="font-size:.7rem;max-width:150px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(sd || '—') + '</td>' +
        '<td style="font-size:.7rem;color:var(--text-muted);max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(r.comment || '—') + '</td>' +
        '<td class="fw-pkt text-end" style="font-family:var(--font-mono);white-space:nowrap">' + deltaIndicator + r.packets.toLocaleString() + '</td>' +
        '<td class="fw-byte text-end" style="font-family:var(--font-mono);font-size:.7rem;color:var(--text-muted);white-space:nowrap">' + (r.bytes > 0 ? fmtBytes(r.bytes) : '—') + '</td>' +
      '</tr>';
    }).join('');

    // One pulse per move. Cleared after the render that showed it, so a later
    // redraw for an unrelated counter tick does not flash the row again — and
    // the class is stripped once the animation has run, so a row does not keep
    // wearing a state it is no longer in.
    if (pulse) {
      pulse = null;
      setTimeout(() => {
        firewallTable.querySelectorAll('tr.fw-pulse')
          .forEach((tr) => tr.classList.remove('fw-pulse'));
      }, 1250);
    }
  }

  /** Point the Add slot at the table now on screen, and redraw it. */
  function syncAddSlot(): void {
    const slot = document.querySelector('[data-res-add^="fw"]');
    if (!slot) return;
    slot.setAttribute('data-res-add', FW_RES[tab] || 'fwFilter');
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  // ── Wiring ────────────────────────────────────────────────────────────────

  socket.on('firewall:update', (d: FirewallPayload) => {
    const wasEmpty = !data.filter;
    data = d;
    updateSummary(d);
    // A DRAG IS IN PROGRESS, and these are the very rows being rearranged.
    // Rebuilding the table underneath the pointer would replace the node being
    // dragged with a fresh one — leaving the original detached and re-inserted
    // as a duplicate — and would yank the row out from under the cursor even if
    // it did not. The payload is kept; the table catches up on release.
    if (document.body.classList.contains('res-dragging-body')) return;
    // A pending pulse forces the full path. The in-place update rewrites counter
    // cells only, so it would leave the cue queued until some unrelated
    // structural change flashed the row at the wrong moment — which is what
    // happens after undoing an EDIT, where nothing moved and the order is
    // unchanged.
    if (!wasEmpty && !pulse && updateCountersInPlace(d)) return;
    if (rafId === null) {
      rafId = requestAnimationFrame(() => { rafId = null; renderTab(); });
    }
  });

  // The page draws its own controls from `permitted`; every gate is re-checked
  // server-side against a fresh read regardless.
  socket.on('res:schema', (d: { key?: string; permitted?: boolean }) => {
    if (!d || !d.key) return;
    writable[d.key] = !!d.permitted;
    // The arrows appear and disappear with the answer, so redraw once it lands.
    if (FW_RES[tab] === d.key) renderTab();
  });

  socket.on('res:ok', (d: { resource?: string; action?: string; movedId?: string }) => {
    if (!d || FW_RES[tab] !== d.resource) return;
    if (d.action === 'move' || d.action === 'undo' || d.action === 'redo') {
      pulse = d.movedId || null;
    }
  });

  // A drag reorders the table optimistically, before the router has agreed. If
  // the write is refused there is no fresh payload coming to correct it, so the
  // table is redrawn from the last one — which still holds what the router
  // actually has.
  socket.on('res:error', (d: { resource?: string }) => {
    if (!d || FW_RES[tab] !== d.resource) return;
    renderTab();
  });

  const searchEl = el<HTMLInputElement>('fwSearch');
  if (searchEl) {
    searchEl.addEventListener('input', debounce(() => {
      search = (searchEl.value || '').trim().toLowerCase();
      renderTab();
    }, 200));
  }

  // `.fw-tab` and `data-fw`, which is what the MARKUP carries.
  //
  // This selected `[data-fwtab]` — an attribute that appears nowhere in the
  // extracted page — so it matched nothing, no listener was ever attached, and
  // the four tabs did nothing at all. The Firewall page could only show Filter.
  // Everything inside the handler was already right; it simply never ran.
  //
  // Found by asking which `data-*` attributes this port RENDERS and never READS,
  // which is the same question that turned up the reorder arrows and the
  // Scheduled tab's write buttons. The id-based wiring audit cannot see any of
  // the three.
  document.querySelectorAll<HTMLElement>('.fw-tab').forEach((b) => {
    b.addEventListener('click', () => {
      tab = b.getAttribute('data-fw') || 'filter';
      document.querySelectorAll<HTMLElement>('.fw-tab').forEach((o) => {
        const on = o === b;
        o.classList.toggle('active', on);
      });
      firewallTable.setAttribute('data-res-rows', FW_RES[tab] || 'fwFilter');
      syncAddSlot();
      // Ask the server to point the counter refresh at THIS table. The active
      // table is shared session state, so the server re-checks who may change it.
      socket.emit('firewall:tab', tab);
      renderTab();
    });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'firewall') return;
    if (data.filter) renderTab();
  });

  void isVisible;
}
