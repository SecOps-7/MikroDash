// The WireGuard page's Peers tab — a hand-built panel on a generated area.
//
// ── WHY THIS IS NOT A GENERATED TABLE ───────────────────────────────────────
//
// Every other tab on a generated page is a `Table` in `internal/areas`, filled
// by the shared `areas` collector. That collector reads a menu plainly once a
// minute and keeps no previous sample, which makes two of this tab's three most
// useful columns impossible:
//
//   - transfer RATES need two readings and a clock. The areas collector has
//     one reading, so a rate is not merely missing but unrepresentable.
//   - "active / stale / never" is a word DERIVED from the age of
//     `last-handshake` by `PeerState` in internal/collect/vpn.go. No router
//     returns it, and `Pills` maps a COLUMN to a kind — it cannot grade a
//     duration.
//
// A generated table would render `1m33s` in grey beside `8224731432`, which is
// the opposite of a status indicator.
//
// ── WHAT IT COSTS THE ROUTER: NOTHING ───────────────────────────────────────
//
// This panel renders `vpn:update`, the payload the dashboard's WireGuard card
// and the VPN alert rules already pay for. `page-wireguard` is in `vpnRooms`
// (internal/collect/rooms.go), so opening this tab joins a room the collector
// already emits to. No second read, no second menu, no second cadence.
//
// ── AND WHY IT OWNS THE HEADER BADGE ────────────────────────────────────────
//
// `area.ts` stops updating the count pill while a panel is up, because the
// number it would write is the Interfaces row count. So the panel writes the
// peer count itself while it is shown, and `render` in area.ts takes the badge
// back on the way to a table tab.

import { esc, el, resRow, fmtBytes, renderSortHeader, sortRows } from '../dom';
import type { SortState } from '../dom';
import type { Socket } from '../socket';
import type { Tunnel } from '../gen/payloads';
import { mountAdds, mountRows } from '../resource';
import { registerAreaPanel } from './area';

/**
 * A RouterOS last-handshake duration in seconds.
 *
 * Lifted from the VPN page, whose peer grid this tab replaces, and with its
 * reasoning: `total` is returned rather than `total || Infinity`, because a peer
 * that has just completed a handshake reports `0s` and the falsy fallback turned
 * a freshly connected peer into the stalest possible one.
 */
function hsToSecs(s: string): number {
  if (!s || s === 'never') return Infinity;
  let total = 0;
  const re = /(\d+)([wdhms])/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    const n = parseInt(m[1] as string, 10);
    if (m[2] === 'w') total += n * 604800;
    else if (m[2] === 'd') total += n * 86400;
    else if (m[2] === 'h') total += n * 3600;
    else if (m[2] === 'm') total += n * 60;
    else total += n;
  }
  return total;
}

/**
 * The handshake badge, graded by age.
 *
 * WireGuard re-keys about every three minutes while a peer passes traffic, so
 * the age of the last handshake is the only real evidence a tunnel is up. Under
 * 3 minutes is fine, under 10 a warning, older is stale — and a peer that has
 * NEVER handshaken is its own state, because "was never used" and "went away"
 * are different problems with different fixes.
 */
function hsBadge(handshake: string, state: string): string {
  if (state === 'never' || !handshake || handshake === 'never') {
    return '<span class="vpn-hs-badge hs-never">Never connected</span>';
  }
  const secs = hsToSecs(handshake);
  const cls = secs < 180 ? 'hs-ok' : secs < 600 ? 'hs-warn' : 'hs-stale';
  return '<span class="vpn-hs-badge ' + cls + '">● ' + esc(handshake) + '</span>';
}

/**
 * Peers whose allowed addresses collide, by public key.
 *
 * ── THE QUIETEST WAY TO BREAK A WIREGUARD SETUP ─────────────────────────────
 *
 * WireGuard routes by longest prefix across ALL peers on an interface, so two
 * peers claiming the same address means traffic for it reaches whichever the
 * kernel matched — permanently, and with no error anywhere. The router does not
 * refuse the configuration and RouterOS's own interface says nothing.
 *
 * Only EXACT duplicates are reported, per interface. A genuine overlap between
 * different prefixes (10.0.0.0/24 against 10.0.0.5/32) is frequently deliberate
 * — a site router plus a host route through it — so flagging those would train
 * the operator to ignore the warning, which is worse than having none.
 */
export function overlappingPeers(rows: readonly Tunnel[]): Set<string> {
  const seen = new Map<string, string[]>();
  for (const t of rows) {
    for (const a of String(t.allowedIp || '').split(',')) {
      const addr = a.trim();
      if (!addr) continue;
      const key = (t.interface || '') + '|' + addr;
      const keys = seen.get(key) || [];
      keys.push(t.publicKey);
      seen.set(key, keys);
    }
  }
  const out = new Set<string>();
  for (const keys of seen.values()) {
    if (keys.length > 1) keys.forEach((k) => out.add(k));
  }
  return out;
}

const COLS = [
  { key: 'name', label: 'Peer' },
  { key: 'interface', label: 'Interface' },
  { key: 'state', label: 'Status' },
  { key: 'lastHandshake', label: 'Handshake' },
  { key: 'allowedIp', label: 'Allowed Addresses' },
  { key: 'endpoint', label: 'Endpoint' },
  { key: 'keepalive', label: 'Keepalive' },
  { key: '', label: 'Rx / Tx', style: 'text-align:right' },
];

const sort: SortState = { col: 'name', dir: 'asc' };

export function initWireguardPeers(socket: Socket): void {
  let host: HTMLElement | null = null;
  let shown = false;
  let last: Tunnel[] = [];

  function draw(): void {
    // NOT DRAWN WHILE HIDDEN. `vpn:update` arrives every few seconds whether or
    // not this tab is up, and rebuilding a table nobody is looking at is work
    // charged to every viewer of every other tab on this page.
    if (!shown || !host) return;

    const badge = el('areaBadge-wireguard');
    if (badge) {
      badge.textContent = String(last.length);
      badge.className = 'card-badge' + (last.length ? ' active-blue' : '');
    }

    const body = el('wgPeerRows');
    if (!body) return;
    if (!last.length) {
      body.innerHTML = '<tr><td colspan="' + COLS.length +
        '"><div class="empty-state">No peers yet. Add one to hand a device its configuration.</div></td></tr>';
      return;
    }

    const clashing = overlappingPeers(last);
    const rows = sortRows(last, sort.col, sort.dir);
    body.innerHTML = rows.map((t) => {
      const rxR = t.rxRate || 0;
      const txR = t.txRate || 0;
      const rate = (rxR > 0 || txR > 0)
        ? '<span style="color:var(--accent-rx)">↓ ' + esc(fmtBytes(Math.round(rxR))) + '/s</span> ' +
          '<span style="color:var(--accent-tx)">↑ ' + esc(fmtBytes(Math.round(txR))) + '/s</span>'
        : '<span style="color:var(--accent-rx)">↓ ' + esc(fmtBytes(t.rx || 0)) + '</span> ' +
          '<span style="color:var(--accent-tx)">↑ ' + esc(fmtBytes(t.tx || 0)) + '</span>';
      const flags =
        (t.disabled ? '<span class="vpn-hs-badge hs-warn">disabled</span> ' : '') +
        (t.responder ? '<span class="vpn-hs-badge hs-info">responder</span> ' : '') +
        (clashing.has(t.publicKey)
          ? '<span class="vpn-hs-badge hs-stale" title="Another peer on this interface claims the same allowed address. WireGuard routes by longest prefix, so only one of them will ever receive that traffic.">overlap</span>'
          : '');
      return '<tr' + resRow(t.id, t.publicKey, 'wgPeer') + '>' +
        '<td style="font-weight:600">' + esc(t.name || '—') + '</td>' +
        '<td>' + esc(t.interface || '—') + '</td>' +
        '<td>' + hsBadge(t.lastHandshake, t.state) + '</td>' +
        '<td style="font-size:.72rem">' + esc(t.lastHandshake || '—') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(t.allowedIp || '—') +
          (flags ? ' ' + flags : '') + '</td>' +
        '<td style="font-family:var(--font-mono);font-size:.72rem">' + esc(t.endpoint || '—') + '</td>' +
        '<td style="font-size:.72rem">' + esc(t.keepalive || '—') + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.72rem">' + rate + '</td>' +
        '</tr>';
    }).join('');
  }

  function layout(h: HTMLElement): void {
    h.innerHTML =
      '<div class="hdr-actions" style="justify-content:flex-end;margin-bottom:.5rem">' +
        '<span data-res-add="wgPeer"></span>' +
      '</div>' +
      '<div class="table-responsive">' +
        '<table class="table table-sm rt-table">' +
          '<thead><tr id="wgPeerHead"></tr></thead>' +
          '<tbody id="wgPeerRows" data-res-rows="wgPeer"></tbody>' +
        '</table>' +
      '</div>';
    renderSortHeader('wgPeerHead', COLS, sort, draw);
    // THE ADD SLOT IS NEW MARKUP, so the resource engine has to be told: it
    // binds slots on mount and on this event, which is how every generated
    // area's tab switch does it (area.ts syncAddSlot).
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  socket.on('vpn:update', (d) => {
    last = (d.tunnels || []).filter((t) => t.type === 'WireGuard');
    draw();
  });

  registerAreaPanel('wireguard', 'peers', {
    show(h: HTMLElement) {
      if (host !== h) {
        host = h;
        layout(h);
      }
      shown = true;
      draw();
    },
    hide() {
      shown = false;
    },
  });

  mountAdds(socket);
  mountRows(socket);
}
