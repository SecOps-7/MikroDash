// The WAN page — a port of the WAN IIFE in public/app.js.
//
// RENEW AND RELEASE ARE WIRED, as of 2026-08-24. They were held back until
// `wanGuard` was ported, because that guard answers a different question from
// selfPath — not "which interface carries us" but "which UPLINK carries this
// session" — and an unguarded Release button on somebody's only WAN is not a
// button worth shipping early. The guard is now `internal/guard/wanguard.go`
// and the server half is `internal/server/wan.go`.
//
// THE WARNING IS THE SERVER'S, NOT THIS PAGE'S. Nothing here decides whether an
// action is dangerous: the page sends, the server answers `self-cutoff` with a
// fingerprint, and this fills the dialog from that answer and sends it back. A
// page that made the judgement itself could be talked out of it by a browser.

import { esc, el, renderSortHeader, type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';

export interface WANDhcp {
  id: string; status: string; server: string;
  primaryDns: string; secondaryDns: string; expiresAfter: string; invalid: boolean;
}

export interface WAN {
  name: string; type: string; isTunnel: boolean; state: string; since: string;
  running: boolean | null; address: string; isPublic: boolean | null;
  gateway: string; routeDistance: string; routeActive: boolean; hasDefaultRoute: boolean;
  rxMbps: number | null; txMbps: number | null;
  rxBytes: number | null; txBytes: number | null;
  dhcp: WANDhcp | null;
}

export interface WANPayload {
  ts: number; pollMs: number; wans: WAN[]; ratesAvailable: boolean;
  activeDefaultWan: string; publicIp: string;
  detectionEnabled: boolean; available: boolean; denied: boolean;
}

const COLS: SortCol[] = [
  { key: '', label: 'Uplink' }, { key: '', label: 'Address' }, { key: '', label: 'Gateway' },
  { key: '', label: 'Route' }, { key: '', label: 'Lease' }, { key: '', label: 'Rate' },
  { key: '', label: '' },
];

function dash(t?: string): string {
  return '<span style="color:var(--text-muted)"' + (t ? ' title="' + t + '"' : '') + '>&mdash;</span>';
}

// This page's own rate format — 'Gb/s' and 'kb/s', not dom.ts's fmtMbps.
function fmtMb(v: number): string {
  return v >= 1000 ? (v / 1000).toFixed(2) + ' Gb/s'
    : v >= 1 ? v.toFixed(1) + ' Mb/s'
    : (v * 1000).toFixed(0) + ' kb/s';
}

/** The router's own timestamp, as an age. Its clock, not ours. */
function since(ts: string): string {
  if (!ts) return '';
  const t = Date.parse(ts.replace(' ', 'T'));
  if (!isFinite(t)) return '';
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 90) return s + 's';
  if (s < 5400) return Math.round(s / 60) + 'm';
  if (s < 172800) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}

function rateCell(w: WAN): string {
  if (w.rxMbps === null && w.txMbps === null) {
    // null is "Interface Rates is not collecting", which is not "idle".
    return dash('Interface Rates collection is off for this router');
  }
  return '<div class="q-rate">' +
    '<div class="q-rate-line"><span class="q-rate-arrow ' + (w.rxMbps ? 'rx' : 'zero') + '">&#8595;</span>' +
    '<span class="q-rate-val ' + (w.rxMbps ? 'rx' : 'zero') + '">' + esc(fmtMb(w.rxMbps || 0)) + '</span></div>' +
    '<div class="q-rate-line"><span class="q-rate-arrow ' + (w.txMbps ? 'tx' : 'zero') + '">&#8593;</span>' +
    '<span class="q-rate-val ' + (w.txMbps ? 'tx' : 'zero') + '">' + esc(fmtMb(w.txMbps || 0)) + '</span></div>' +
    '</div>';
}

function leaseCell(w: WAN): string {
  // "no DHCP client", not "static": a tunnel's address is configured rather
  // than leased, and calling that static invites reading it as a static WAN.
  if (!w.dhcp) return '<span class="muted-note">no DHCP client</span>';
  const d = w.dhcp;
  const ok = d.status === 'bound';
  return '<div><span class="wl-band ' + (ok ? 'wl-band-6' : 'wl-band-24') + '">' + esc(d.status || '?') + '</span>' +
    (d.expiresAfter ? '<div class="muted-note">expires in ' + esc(d.expiresAfter) + '</div>' : '') +
    (d.invalid ? '<div class="muted-note" style="color:rgba(248,113,113,.9)">invalid</div>' : '') + '</div>';
}

export function initWanPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tb = el('wanTable');
  if (!tb) return;

  let data: WANPayload | null = null;
  // Never populated: this port does not ask for caps, so the page renders in
  let caps = { permitted: false, routerName: '' };
  // The row an action is in flight for, so its two buttons disable together.
  let busy = '';

  // The status line. Eight seconds, then it clears itself. `dataset.status` is
  // the same collision guard queues.ts documents at length: render() writes
  // this element too and runs again on the next payload, so an unmarked message
  // was erased in the same tick.
  let statusTimer: ReturnType<typeof setTimeout> | null = null;
  function setStatus(text: string): void {
    const e = el('wanActionNote');
    if (!e) return;
    e.textContent = text || '';
    if (text) e.dataset.status = '1'; else delete e.dataset.status;
    if (statusTimer) clearTimeout(statusTimer);
    if (text) {
      statusTimer = setTimeout(() => { e.textContent = ''; delete e.dataset.status; }, 8000);
    }
  }
  const headerSort: SortState = { col: '', dir: 'asc' };

  function emptyState(): string {
    if (!data) return 'Waiting for WAN data&hellip;';
    if (data.denied) return 'This router\'s MikroDash account cannot read the internet-detection state.';
    if (!data.detectionEnabled) return 'Internet detection is not enabled on this router.';
    return 'No uplink currently reports an internet connection.';
  }

  // The actions cell, reproducing the live markup attribute for attribute.
  //
  // Nothing to renew on a STATIC uplink: a tunnel's address is configured, not
  // leased, so a router with no DHCP client on that interface gets no buttons at
  // all rather than buttons that would fail.
  function actions(w: WAN): string {
    if (!w.dhcp) return '';
    if (!caps.permitted) return '';
    const b = (verb: string, label: string, cls?: string): string =>
      '<button class="ru-act' + (cls ? ' ' + cls : '') + '" data-wanact="' + verb +
      '" data-id="' + esc(w.dhcp!.id) + '" data-name="' + esc(w.name) + '"' +
      (busy === w.dhcp!.id ? ' disabled' : '') + '>' + label + '</button>';
    return b('renew', 'Renew') + ' ' + b('release', 'Release', 'danger');
  }

  function render(): void {
    const wans = (data && data.wans) || [];
    renderSortHeader('wanThead', COLS, headerSort, () => {});
    const badge = el('wanBadge');
    if (badge) badge.textContent = String(wans.length);

    tb!.innerHTML = wans.length ? wans.map((w) => {
      const age = since(w.since);
      return '<tr' + (w.running === false ? ' style="opacity:.62"' : '') + '>' +
        '<td>' + esc(w.name) +
        '<div class="muted-note">' + esc(w.isTunnel ? 'tunnel · ' + w.type : w.type || 'interface') +
        (age ? ' · up ' + esc(age) : '') + '</div></td>' +
        '<td>' + (w.address ? esc(w.address) : dash()) +
        (w.isPublic === true ? '<div class="muted-note" style="color:var(--accent-rx)">public</div>'
          : w.isPublic === false ? '<div class="muted-note">private</div>' : '') + '</td>' +
        '<td>' + (w.gateway ? esc(w.gateway) : dash()) + '</td>' +
        '<td>' + (w.hasDefaultRoute
          ? (w.routeActive
            ? '<span class="wl-band wl-band-6">active</span>'
            : '<span class="wl-band wl-band-24">standby</span>') +
          '<div class="muted-note">distance ' + esc(w.routeDistance || '?') + '</div>'
          : dash('No default route via this uplink')) + '</td>' +
        '<td>' + leaseCell(w) + '</td>' +
        '<td>' + rateCell(w) + '</td>' +
        '<td>' + actions(w) + '</td>' +
        '</tr>';
    }).join('') : '<tr><td colspan="7" class="empty-state">' + emptyState() + '</td></tr>';

    const note = el('wanActionNote');
    // Never clears a message it did not write — see setStatus.
    if (note && !note.dataset.status) {
      note.textContent = caps.permitted ? '' : 'read-only — you do not have write access to this router';
    }
    renderNotice();
    renderSummary();
  }

  function renderNotice(): void {
    const card = el('wanNoticeCard'), body = el('wanNotice');
    if (!card || !body) return;
    if (!data || data.detectionEnabled || data.denied) { card.style.display = 'none'; return; }
    card.style.display = '';
    // The default is detect-interface-list=none, so this is the common case
    // rather than a fault. Say what to run.
    body.innerHTML = '<strong>Internet detection is switched off on this router.</strong> ' +
      'RouterOS decides which interfaces reach the internet, and it is not looking. ' +
      'This page shows what it reports, so it has nothing to show until detection is on. Enable it with ' +
      '<code>/interface detect-internet set detect-interface-list=all</code> — it is read-only and adds no traffic ' +
      'beyond an occasional probe.';
  }

  function renderSummary(): void {
    const d = data || ({} as Partial<WANPayload>);
    const wans = d.wans || [];
    const set = (id: string, v: string) => { const n = el(id); if (n) n.textContent = v; };
    set('wanSumCount', String(wans.length || '—'));
    set('wanSumActive', d.activeDefaultWan || (wans.length ? 'none active' : '—'));
    set('wanSumPublic', (d.publicIp || '').split('/')[0] || '—');
    const rateEl = el('wanSumRate');
    if (!rateEl) return;
    const any = wans.some((w) => w.rxMbps !== null || w.txMbps !== null);
    if (!any) { rateEl.innerHTML = '&mdash;'; return; }
    let rx = 0, tx = 0;
    wans.forEach((w) => { rx += w.rxMbps || 0; tx += w.txMbps || 0; });
    // Coloured per direction rather than as one figure, so the summary reads
    // the same way as the rate cells in the table below it.
    rateEl.innerHTML =
      '<span style="color:var(--accent-rx)">&#8595; ' + esc(fmtMb(rx)) + '</span> ' +
      '<span style="color:var(--accent-tx)">&#8593; ' + esc(fmtMb(tx)) + '</span>';
  }

  function send(verb: string, id: string, name: string, ack?: string): void {
    busy = id;
    render();
    // `ack` is omitted rather than sent empty on the first attempt: the server
    // reads a missing ack as "not answered" (self-cutoff) and a wrong one as
    // "answered about something else" (stale-warning), and they are different
    // answers to the operator.
    socket.emit('wan:' + verb, { id, expectedName: name, ack: ack || undefined });
  }

  document.addEventListener('click', (e) => {
    const t = e.target as HTMLElement | null;
    const b = t?.closest?.('[data-wanact]') as HTMLElement | null;
    if (!b) return;
    const verb = b.getAttribute('data-wanact') || '';
    const id = b.getAttribute('data-id') || '';
    const name = b.getAttribute('data-name') || '';
    // The ordinary confirm, which is NOT the self-cutoff warning. This one asks
    // "did you mean to do this at all"; the dialog below asks "do you know this
    // will drop your own connection". Conflating them would train the operator
    // to dismiss both with one habit.
    const msg = verb === 'release'
      ? 'Release the DHCP lease on "' + name + '"?\n\nThe uplink goes down until the client rebinds — usually seconds, but it is a real outage.'
      : 'Renew the DHCP lease on "' + name + '"?\n\nThe uplink blips briefly while the lease is renewed.';
    if (!window.confirm(msg)) return;
    send(verb, id, name);
  });

  el('wanWarnGo')?.addEventListener('click', () => {
    el('wanWarnWrap')?.classList.remove('open');
    const v = (id: string): string => (el<HTMLInputElement>(id)?.value) || '';
    send(v('wanWarnVerb'), v('wanWarnId'), v('wanWarnName'), v('wanWarnAck'));
  });

  socket.on('wan:update', (d: WANPayload) => {
    if (!d) return;
    data = d;
    busy = '';
    renderSummary();
    if (isVisible('wan')) render();
  });

  socket.on('wan:caps', (d: { permitted: boolean; routerName: string }) => {
    if (!d) return;
    caps = d;
    if (isVisible('wan')) render();
  });

  socket.on('wan:ok', (d: { action?: string; name?: string }) => {
    busy = '';
    // "Requested", not "renewed": the lease settles over the next second or two
    // and the next tick is what reports the outcome.
    setStatus((d && d.action === 'release' ? 'Released the lease on ' : 'Requested a renewal on ') +
      ((d && d.name) || ''));
  });

  socket.on('wan:error', (d: {
    code?: string; message?: string; name?: string; verb?: string; fingerprint?: string;
    warning?: { address?: string; wan?: string; certain?: boolean };
  }) => {
    busy = '';
    const code = d && d.code;

    if (code === 'self-cutoff' || code === 'stale-warning') {
      const w = (d && d.warning) || {};
      const set = (id: string, v: string): void => {
        const n = el<HTMLInputElement>(id);
        if (n) n.value = v;
      };
      set('wanWarnName', (d && d.name) || '');
      set('wanWarnVerb', (d && d.verb) || 'renew');
      set('wanWarnAck', (d && d.fingerprint) || '');
      // THE ID COMES FROM THE PAYLOAD, NOT FROM THE ERROR. The server answers
      // with the interface NAME, so the row it refers to is looked up here and
      // its lease id taken from that — the same id the button carried. An empty
      // one means the uplink went away between the click and the answer, and
      // the retry then fails `stale-row` rather than acting on the wrong lease.
      const row = ((data && data.wans) || []).find((x) => x.name === (d && d.name));
      set('wanWarnId', (row && row.dhcp && row.dhcp.id) || '');

      const body = el('wanWarnBody');
      if (body) {
        body.innerHTML =
          '<p>MikroDash reaches ' + esc(caps.routerName || 'this router') + ' from <code>' +
            esc(w.address || '') + '</code>, which is not on any of its connected subnets — so that ' +
            'traffic arrives over a WAN.</p>' +
          (w.certain
            ? '<p><strong>' + esc(w.wan || '') + ' is the uplink carrying the active default route</strong>, ' +
              'which means it is carrying this session.</p>'
            : '<p>This router has more than one active default route, so which uplink carries this ' +
              'session cannot be determined — <strong>' + esc(w.wan || '') + ' may be the one.</strong></p>') +
          '<p>' + (((el<HTMLInputElement>('wanWarnVerb')?.value) || '') === 'release'
            ? 'Releasing the lease takes the uplink down until the client rebinds.'
            : 'Renewing blips the uplink briefly.') +
          ' The dashboard will lose this router until it comes back. It should return on its own.</p>' +
          (code === 'stale-warning'
            ? '<p><em>The situation changed since you confirmed, so please confirm again.</em></p>' : '');
      }
      el('wanWarnWrap')?.classList.add('open');
      return;
    }

    const msg: Record<string, string> = {
      denied: 'You do not have write access to this router',
      unavailable: 'WAN collection is not running for this router',
      'bad-request': 'Invalid request',
      'stale-row': 'That uplink changed on the router — the page has been refreshed',
      'router-write-policy': 'The RouterOS user needs write permission for this',
      unsupported: 'This router does not support that command',
    };
    setStatus((code && msg[code]) || (d && d.message) || 'Action failed');
    if (isVisible('wan')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'wan') return;
    // Permission is a property of this socket, not of the shared payload.
    socket.emit('wan:caps', {});
    if (data) render();
  });
}
