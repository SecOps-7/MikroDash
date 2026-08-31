// The Dashboard's Connections card: the live total, its sparkline, the protocol
// bars, and the two "top" lists.
//
// ── THREE FINGERPRINTS, EACH GUARDING A DIFFERENT REDRAW ────────────────────
//
// `conn:update` arrives every tick with a payload whose shape is stable, so the
// card compares a fingerprint of each section against the last one and skips the
// sections that did not move. The fingerprints are DELIBERATELY NARROW: sources
// on ip+count, destinations on key+count+country, protocols on the whole count
// object. `ts` is excluded from all of them — including it would make every
// fingerprint differ every tick and turn the whole mechanism off.
//
// Note what that means for destinations: `org`, `cat` and `city` are RENDERED
// but not fingerprinted, so a change in only those does not redraw. That is the
// live behaviour and it is reproduced, not corrected.
//
// ── THE TOTAL AND THE BARS ARE NOT DEFERRED; THE LISTS ARE ──────────────────
//
// The handler writes the total, pushes history and draws the sparkline
// immediately, then defers only the two lists to an animation frame — they are
// the expensive half. A hidden tab keeps the payload pending exactly as the
// System card does.
//
// ── THE FLAG IS BUILT HERE RATHER THAN WITH iso2Flag ────────────────────────
//
// `connections-map.ts` exports `iso2Flag`, which looks like the same function
// and is not: it returns '' for anything that is not two characters, and this
// call site has no such guard — it maps every character it is given. Reusing it
// would silently change what a malformed country renders as. The live formula is
// reproduced instead, and the difference is pinned by a case.

import { esc, el } from '../dom';
import { svcBadge } from './connections-lists';

export interface ConnPayload {
  ts?: number;
  total?: number;
  protoCounts?: { tcp: number; udp: number; icmp: number; other: number };
  topSources?: { ip: string; name: string; count: number }[];
  topDestinations?: {
    key: string; count: number; country?: string; city?: string;
    org?: string; cat?: string;
  }[];
}

const MAX_CONN_HIST = 60;
const connHistory: { ts?: number; total?: number }[] = [];

let srcFp = '', dstFp = '', protoFp = '';
let pending: ConnPayload | null = null;
let rafId: number | null = null;

/**
 * The canvas is resolved on every draw rather than cached at module load.
 *
 * The live app caches it once, which is safe there because the whole page is in
 * `index.html` before any script runs. Here the page bodies are fetched and
 * injected, so a cached lookup at import time would find nothing and the
 * sparkline would never draw. A mechanism change to keep the behaviour the same.
 */
function sparkCtx(): { canvas: HTMLCanvasElement; ctx: CanvasRenderingContext2D } | null {
  const canvas = el<HTMLCanvasElement>('connSparkCanvas');
  if (!canvas || !canvas.getContext) return null;
  const ctx = canvas.getContext('2d');
  return ctx ? { canvas, ctx } : null;
}

export function drawSparkline(history: { total?: number }[]): void {
  const c = sparkCtx();
  if (!c || !history || history.length < 2) return;
  const w = c.canvas.width, h = c.canvas.height;
  c.ctx.clearRect(0, 0, w, h);
  const vals = history.map((p) => p.total as number);
  // `|| 1` catches an all-zero history, which would otherwise divide by zero and
  // put every point at NaN.
  const maxV = Math.max.apply(null, vals) || 1;
  c.ctx.beginPath();
  c.ctx.strokeStyle = '#38bdf8';
  c.ctx.lineWidth = 1.5;
  c.ctx.lineJoin = 'round';
  for (let i = 0; i < vals.length; i++) {
    const x = (i / (vals.length - 1)) * w, y = h - (vals[i]! / maxV) * (h - 2) - 1;
    if (i === 0) c.ctx.moveTo(x, y); else c.ctx.lineTo(x, y);
  }
  c.ctx.stroke();
}

export function renderProtoBars(pc: ConnPayload['protoCounts']): void {
  const protoBars = el('protoBars');
  if (!protoBars || !pc) return;
  // `|| 1` binds to the whole sum, so a router with no connections at all
  // renders four 0% bars rather than four NaN% ones.
  const total = pc.tcp + pc.udp + pc.icmp + pc.other || 1;
  const items = [
    { k: 'TCP', c: 'tcp', v: pc.tcp }, { k: 'UDP', c: 'udp', v: pc.udp },
    { k: 'ICMP', c: 'icmp', v: pc.icmp }, { k: 'Other', c: 'other', v: pc.other },
  ];
  protoBars.innerHTML = items.map((it) => {
    const pct = Math.round((it.v / total) * 100);
    return '<div class="proto-bar-row"><div class="proto-label">' + it.k + '</div>' +
      '<div class="proto-track"><div class="proto-fill ' + it.c + '" style="width:' + pct + '%"></div></div>' +
      '<div class="proto-val">' + it.v + '</div></div>';
  }).join('');
}

export function flushConnUpdate(): void {
  rafId = null;
  const data = pending;
  if (!data) return;
  pending = null;

  const nextSrcFp = JSON.stringify((data.topSources as NonNullable<ConnPayload['topSources']>)
    .map((x) => ({ ip: x.ip, count: x.count })));
  if (nextSrcFp !== srcFp) {
    srcFp = nextSrcFp;
    const topSources = el('topSources');
    if (topSources) {
      if (data.topSources && data.topSources.length) {
        topSources.innerHTML = data.topSources.map((s) =>
          '<div class="top-row"><div style="display:flex;align-items:center;gap:.4rem;min-width:0;overflow:hidden">' +
          '<span class="card-badge" style="flex-shrink:0">' + esc(s.ip) + '</span>' +
          '<div class="top-name" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' +
          esc(s.name) + '</div></div><div class="top-count">' + s.count + '</div></div>').join('');
      } else {
        topSources.innerHTML = '<div class="empty-state">—</div>';
      }
    }
  }

  const nextDstFp = JSON.stringify((data.topDestinations as NonNullable<ConnPayload['topDestinations']>)
    .map((x) => ({ key: x.key, count: x.count, country: x.country })));
  if (nextDstFp !== dstFp) {
    dstFp = nextDstFp;
    const topDests = el('topDests');
    if (topDests) {
      if (data.topDestinations && data.topDestinations.length) {
        topDests.innerHTML = data.topDestinations.map((d) => {
          let flag = '', geoLabel = '';
          if (d.country) {
            // No length guard — see the header. This is not `iso2Flag`.
            flag = d.country.split('').map((c) =>
              String.fromCodePoint(0x1F1E6 - 65 + c.toUpperCase().charCodeAt(0))).join('');
            geoLabel = flag + (d.city ? ' ' + esc(d.city) + ' · ' + esc(d.country) : '');
          }
          return '<div class="top-row">' +
            '<div style="flex:1;min-width:0;overflow:hidden">' +
              '<div style="display:flex;align-items:center;gap:0;overflow:hidden">' +
                '<span class="top-name text-truncate has-ip-tip" data-ip="' + esc(d.key) +
                  '" data-org="' + (d.org ? esc(d.org) : '') +
                  '" data-cat="' + esc(d.cat || '') + '">' + esc(d.key) + '</span>' +
                (d.org ? svcBadge(d.org, d.cat || null) : '') +
              '</div>' +
            '</div>' +
            (geoLabel ? '<div class="top-geo">' + geoLabel + '</div>' : '') +
            '<div class="top-count">' + d.count + '</div>' +
          '</div>';
        }).join('');
      } else {
        topDests.innerHTML = '<div class="empty-state">—</div>';
      }
    }
  }
}

/** The `conn:update` handler. The immediate half runs now; the lists defer. */
export function noteConnUpdate(data: ConnPayload): void {
  const connTotal = el('connTotal');
  if (connTotal) connTotal.textContent = String(data.total);
  connHistory.push({ ts: data.ts, total: data.total });
  if (connHistory.length > MAX_CONN_HIST) connHistory.shift();
  drawSparkline(connHistory);
  const nextProtoFp = JSON.stringify(data.protoCounts);
  if (nextProtoFp !== protoFp) {
    protoFp = nextProtoFp;
    renderProtoBars(data.protoCounts);
  }
  // Excludes ts — the payload shape is stable between ticks when nothing moved.
  pending = data;
  if (!rafId) rafId = requestAnimationFrame(flushConnUpdate);
}

/** Called when the tab becomes visible again, alongside the System card's. */
export function flushPendingConn(): void {
  if (pending && !rafId) rafId = requestAnimationFrame(flushConnUpdate);
}

/**
 * Forget the caches. A switch to another router must redraw everything: the new
 * router's first payload could fingerprint identically to the old router's last
 * one — two routers with the same top talker at the same count is not far-fetched
 * on a fleet — and the card would keep the previous router's rows.
 */
export function resetConnCaches(): void {
  srcFp = ''; dstFp = ''; protoFp = '';
  connHistory.length = 0;
  pending = null;
}
