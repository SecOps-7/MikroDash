/**
 * The notification bell.
 *
 * Five ids and one list, but the rules underneath are what make it usable rather
 * than noisy — and each of them reads as an implementation detail until it is
 * missing.
 */

import { el, esc } from '../dom';
import type { Socket } from '../socket';

/** How many alerts the panel keeps. */
export const MAX_ALERTS = 100;

export interface Alert {
  id: number;
  alertType: string;
  subject?: string | null;
  detail?: string | null;
  label?: string | null;
  routerName?: string | null;
  firedAt: number;
  resolvedAt?: number | null;
  acknowledgedAt?: number | null;
  acknowledgedBy?: string | null;
}

/**
 * What counts as "the same thing" for the purposes of replacing an entry.
 *
 * Type plus subject, so `link|ether1` and `link|ether2` are different alerts and
 * two successive `link|ether1` are one.
 */
export const alertKey = (a: Alert): string => `${a.alertType}|${a.subject || ''}`;

export const isOpen = (a: Alert): boolean => !a.resolvedAt;

/**
 * THE DOT MEANS UNACKNOWLEDGED OPEN ALERTS, and both halves matter.
 *
 * Not "any alert" — a resolved one is history, and a dot for it would never go
 * out. Not "any open alert" either: acknowledging is how an operator says they
 * have seen something, and a dot that stayed lit through that would train them
 * to ignore it.
 */
export const needingAttention = (alerts: Alert[]): Alert[] =>
  alerts.filter((a) => isOpen(a) && !a.acknowledgedAt);

/**
 * Replace the whole list, newest FIRED first, capped.
 *
 * The cap is applied AFTER the merge because the two feeds arrive separately —
 * trimming either alone could drop a newer alert while keeping an older one.
 */
export function setAlerts(open: Alert[], recent: Alert[]): Alert[] {
  const all = [...(open || []), ...(recent || [])];
  all.sort((a, b) => (b.firedAt || 0) - (a.firedAt || 0));
  return all.slice(0, MAX_ALERTS);
}

/**
 * Add one alert, REPLACING any open entry for the same thing.
 *
 * Without that a flapping interface buries everything else in the panel: one
 * link bouncing every thirty seconds fills all hundred slots in under an hour,
 * and the alert the operator actually needs scrolls out of reach.
 *
 * Only the OPEN entry is replaced. A resolved one for the same key is history
 * and stays.
 */
export function addAlert(alerts: Alert[], a: Alert): Alert[] {
  if (!a) return alerts;
  const k = alertKey(a);
  const kept = alerts.filter((x) => !(alertKey(x) === k && isOpen(x)));
  return [a, ...kept].slice(0, MAX_ALERTS);
}

/** Mark ids resolved. */
export function resolveAlerts(alerts: Alert[], ids: number[], resolvedAt: number): Alert[] {
  const set = new Set(ids || []);
  return alerts.map((a) => (set.has(a.id) ? { ...a, resolvedAt } : a));
}

/** Mark ids acknowledged. */
export function ackAlerts(
  alerts: Alert[], ids: number[], at: number, by: string | null,
): Alert[] {
  const set = new Set(ids || []);
  return alerts.map((a) => (set.has(a.id) ? { ...a, acknowledgedAt: at, acknowledgedBy: by } : a));
}

/** `just now` / `5m ago` / `3h ago` / `2d ago`. */
export function alertAge(ts: number, now: number): string {
  const age = now - ts;
  if (age < 60000) return 'just now';
  if (age < 3600000) return `${Math.floor(age / 60000)}m ago`;
  if (age < 86400000) return `${Math.floor(age / 3600000)}h ago`;
  return `${Math.floor(age / 86400000)}d ago`;
}

/**
 * The panel's markup.
 *
 * ── ACKNOWLEDGING IS WHAT REMOVES AN ALERT FROM THE BELL ────────────────────
 *
 * That is what makes "Clear all" clear anything. Acknowledged alerts stay in the
 * list so a later `alert:resolved` can still find them by id, and they stay in
 * the database for Reports, which is where the history belongs.
 *
 * So an acknowledged alert that is STILL OPEN is invisible here by design: the
 * operator said they had seen it. Filtering on `isOpen` instead would leave it
 * on screen and make the button appear to do nothing.
 */
export function panelHTML(alerts: Alert[], now: number): string {
  const shown = alerts.filter((a) => !a.acknowledgedAt);
  if (!shown.length) return '<div class="notif-empty">No alerts</div>';

  const open = shown.filter(isOpen);
  const done = shown.filter((a) => !isOpen(a));

  const row = (a: Alert): string => {
    const cls = `notif-item${isOpen(a) ? ' is-open' : ' is-resolved'}`;
    // COERCED, not trusted. Every other interpolation here goes through esc(),
    // and the id lands in two ATTRIBUTES where escaping is not what saves you —
    // a quote would end the attribute. It is declared `number` and arrives from
    // JSON, which does not enforce that, so `Number(...)` makes the declaration
    // true. `|| 0` covers a NaN, which would otherwise print as the word.
    //
    // The server sends database integers, so this is belt and braces — but it
    // costs one call and removes the need to be sure of that.
    const id = Number(a.id) || 0;
    // An OPEN alert is timed from when it FIRED; a resolved one from when it
    // RESOLVED. Showing the fired time for a resolved alert would say "3h ago"
    // about something that ended a minute ago.
    const when = isOpen(a) ? a.firedAt : (a.resolvedAt || a.firedAt);
    return `<div class="${cls}" data-alert-id="${id}">` +
      `<div class="notif-item-title">${esc(a.label || a.alertType)}` +
      `${a.subject ? ` — ${esc(a.subject)}` : ''}</div>` +
      `<div class="notif-item-body">${esc(a.detail || '')}</div>` +
      `<div class="notif-item-time">` +
      `${a.routerName ? `<span class="notif-item-router">${esc(a.routerName)}</span> · ` : ''}` +
      `${alertAge(when, now)}</div>` +
      (isOpen(a)
        ? `<button class="notif-ack-btn" data-ack="${id}">Acknowledge</button>` : '') +
      `</div>`;
  };

  return open.map(row).join('') +
    (open.length && done.length ? '<div class="notif-sep">Recently resolved</div>' : '') +
    done.map(row).join('');
}

/** Whether the dot is shown. */
export const dotDisplay = (alerts: Alert[]): string =>
  (needingAttention(alerts).length ? 'block' : 'none');

// ── wiring ──────────────────────────────────────────────────────────────────

/**
 * @param activeRouterId reads the router the shell is showing. The live app
 *   reaches for `window._activeRouterId`; this port passes an accessor instead,
 *   which is the same value without a global — and the same shape `main.ts`
 *   already hands the router dropdown.
 */
export function initNotifications(socket: Socket, activeRouterId: () => string): void {
  let alerts: Alert[] = [];

  const render = (): void => {
    const list = el('notifList');
    if (list) list.innerHTML = panelHTML(alerts, Date.now());
    const dot = el('notifDot');
    if (dot) dot.style.display = dotDisplay(alerts);
  };

  const toggle = el('notifToggleBtn');
  const panel = el('notifPanel');
  if (toggle && panel) {
    toggle.addEventListener('click', () => {
      panel.classList.toggle('open');
      // Re-rendered on OPEN, not only on new data: every row carries a relative
      // age, and a panel left closed for an hour would reopen saying "just now"
      // about something from an hour ago.
      if (panel.classList.contains('open')) render();
    });
  }

  // ── THE TWO WRITE ACTIONS ARE HTTP, AND THAT WAS A CORRECTION ─────────────
  //
  // An earlier version of this file emitted `alert:ack` and `alerts:clear-all`
  // over the socket. That was a protocol I INVENTED: the live app does neither —
  // it POSTs, and there is no such inbound socket action anywhere in
  // `src/index.js`. `inbound-audit` caught it in one line: "this port EMITS it
  // and ws.go does not answer it. A control wired to an event nobody handles
  // does nothing and reports nothing." The routes exist now
  // (`internal/server/alerts_api.go`) and these are the calls that were meant.

  const clearBtn = el('notifClearBtn');
  if (clearBtn) {
    // SAY SO WHEN IT DOES NOT WORK. Swallowing the error made a 403 — a user
    // restricted to another router — look exactly like success: the panel just
    // sat there, which is indistinguishable from the button being broken.
    const clearFail = (msg: string): void => {
      const was = clearBtn.textContent;
      clearBtn.textContent = msg;
      setTimeout(() => { clearBtn.textContent = was; }, 2000);
    };
    clearBtn.addEventListener('click', () => {
      const rid = activeRouterId();
      if (!rid) { clearFail('No router'); return; }
      (clearBtn as HTMLButtonElement).disabled = true;
      fetch('/api/alerts/clear-all', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ routerId: rid }),
      })
        // BOTH halves: the HTTP status AND the body's `ok`. A 200 carrying
        // `{ok:false}` is a refusal the status does not show.
        .then((r) => r.json().then((j: { ok?: boolean }) => ({ ok: r.ok && !!(j && j.ok) })))
        .then((res) => {
          if (!res.ok) { clearFail('Failed'); return; }
          // DO NOT WAIT FOR `alerts:cleared-all` TO EMPTY THE PANEL. The server
          // emits only when it actually cleared something, so a second click —
          // or a click when nothing is open — would otherwise leave the list
          // exactly as it was and read as a broken button.
          const ids = alerts.map((a) => a.id);
          const now = Date.now();
          alerts = resolveAlerts(alerts, ids, now);
          alerts = ackAlerts(alerts, ids, now, null);
          render();
        })
        .catch(() => { clearFail('Failed'); })
        .then(() => { (clearBtn as HTMLButtonElement).disabled = false; });
    });
  }

  // Per-row acknowledge. DELEGATED, because rows are re-rendered on every event
  // — a listener bound per button would be lost on the next render.
  const listEl = el('notifList');
  if (listEl) {
    listEl.addEventListener('click', (e: Event) => {
      const target = e.target as HTMLElement | null;
      const btn = target && target.closest ? target.closest('.notif-ack-btn') : null;
      if (!btn) return;
      // The panel's own click handler must not see this: the button lives inside
      // the row, and without this the row's handler would run too.
      e.stopPropagation();
      const id = parseInt(btn.getAttribute('data-ack') || '', 10);
      if (!id) return;
      // NO FAILURE UI, and no optimistic update — a quirk of the live app,
      // reproduced rather than improved. The row disappears when `alert:acked`
      // comes back, so a refusal simply leaves it there. Unlike Clear all, this
      // is one row among several and a silent no-op is visible: the button the
      // operator just pressed did nothing.
      fetch('/api/alerts/' + id + '/ack', { method: 'POST', credentials: 'same-origin' })
        .catch(() => {});
    });
  }

  socket.on('alerts:open', (d: { open?: Alert[]; recent?: Alert[] }) => {
    if (!d) return;
    alerts = setAlerts(d.open || [], d.recent || []);
    render();
  });

  socket.on('alert:fired', (a: Alert) => {
    if (!a) return;
    alerts = addAlert(alerts, a);
    render();
  });

  socket.on('alert:resolved', (d: { ids?: number[]; resolvedAt?: number }) => {
    if (!d) return;
    alerts = resolveAlerts(alerts, d.ids || [], d.resolvedAt || Date.now());
    render();
  });

  socket.on('alert:acked', (a: Alert) => {
    if (!a) return;
    alerts = ackAlerts(alerts, [a.id], a.acknowledgedAt || Date.now(), a.acknowledgedBy || null);
    render();
  });

  socket.on('alerts:cleared-all', (d: { ids?: number[]; at?: number }) => {
    if (!d) return;
    // BOTH, and in this order: resolving is what clears the Routers page count,
    // acknowledging is what empties the bell.
    const at = d.at || Date.now();
    alerts = resolveAlerts(alerts, d.ids || [], at);
    alerts = ackAlerts(alerts, d.ids || [], at, null);
    render();
  });
}
