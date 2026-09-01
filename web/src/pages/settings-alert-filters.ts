/**
 * Settings → the alert-type toggles and the interface-kind filter card.
 *
 * ── THE DEFAULTS ARE GENERATED, AND THE GENERATOR CHECKS THEM ──────────────
 *
 * `../gen/alert-filters` is lifted by the alert-filters table generator, which
 * ASSERTS that every default matches the one `src/settings.js` uses. The live
 * source only says so in a comment — and that comment records four toggles that
 * had already drifted, defaulting on in the browser while the server had them
 * off. The window is small (script parse to the first settings broadcast) and
 * the symptom is a notification for a category the operator switched off.
 *
 * ── A REJECTED SAVE PUTS THE BOX BACK ──────────────────────────────────────
 *
 * Each toggle writes to the server immediately, so push alerts respect it
 * without a Save click. The live comment on the failure path: "A rejected save
 * used to be swallowed, leaving the box ticked and the toggle only appearing to
 * have taken effect." So a refusal reverts the checkbox, reverts the local copy,
 * re-saves that, and says so through the settings banner.
 *
 * ── THE FILTER CARD IS DIMMED, NOT HIDDEN ──────────────────────────────────
 *
 * `notifIfaceFilterCard` holds the per-interface-kind toggles, which only mean
 * anything while Interface Up/Down is on. Turning that off drops the card to 40%
 * and `pointer-events: none` — still readable, so the operator can see what
 * WOULD be filtered, and not clickable, so they cannot set a filter that does
 * nothing. Hiding it outright would make the settings look like they moved.
 */

import { el } from '../dom';
import { showBanner } from './settings-poll';
import {
  ALERT_TOGGLES, ALERT_TYPE_DEFAULTS, ALERT_IFACE_DEFAULTS,
  NOTIF_TYPES_KEY, NOTIF_IFACE_TYPES_KEY,
} from '../gen/alert-filters';

/** The live objects, seeded from the generated defaults. */
export const alertTypes: Record<string, boolean> = { ...ALERT_TYPE_DEFAULTS };
export const alertIfaceTypes: Record<string, boolean> = { ...ALERT_IFACE_DEFAULTS };

function objFor(name: string): Record<string, boolean> {
  return name === 'iface' ? alertIfaceTypes : alertTypes;
}

/**
 * Restore from localStorage.
 *
 * ONLY KEYS THE DEFAULTS ALREADY HAVE are taken (`Object.keys(...).forEach`),
 * so a stale or hand-edited entry cannot introduce a toggle the UI does not
 * know about. `!!` coerces, so a string "false" written by an older build reads
 * as true — reproduced, because that is what the live app does and changing it
 * would silently flip a stored preference on upgrade.
 */
export function loadAlertFilters(): void {
  for (const [key, obj] of [[NOTIF_TYPES_KEY, alertTypes], [NOTIF_IFACE_TYPES_KEY, alertIfaceTypes]] as const) {
    try {
      const raw = localStorage.getItem(key);
      if (!raw) continue;
      const parsed = JSON.parse(raw);
      Object.keys(obj).forEach((k) => {
        if (k in parsed) obj[k] = !!parsed[k];
      });
    } catch {
      /* a private window, a cleared store, or a corrupt entry: keep the defaults */
    }
  }
}

export function saveAlertFilters(): void {
  // TWO try/catch blocks, not one around both. The live code writes them
  // separately, so a quota failure on the first still lets the second through —
  // and a single block would silently lose the interface filters whenever the
  // type filters failed.
  try {
    localStorage.setItem(NOTIF_TYPES_KEY, JSON.stringify(alertTypes));
  } catch { /* storage refused */ }
  try {
    localStorage.setItem(NOTIF_IFACE_TYPES_KEY, JSON.stringify(alertIfaceTypes));
  } catch { /* storage refused */ }
}

/** Dim and disable the interface-kind card when Interface Up/Down is off. */
export function updateFilterCard(): void {
  const card = el('notifIfaceFilterCard');
  if (!card) return;
  const on = alertTypes.ifaceUpDown;
  card.style.opacity = on ? '1' : '0.4';
  // The EMPTY STRING, not 'auto': it removes the inline rule and lets the
  // stylesheet decide, which is what the live app writes.
  card.style.pointerEvents = on ? '' : 'none';
  card.style.transition = 'opacity .2s';
}

/** Push the in-memory state onto the checkboxes. */
export function syncAlertFilterUI(): void {
  for (const m of ALERT_TOGGLES) {
    const box = el<HTMLInputElement>(m.id);
    if (!box) continue;
    box.checked = !!objFor(m.obj)[m.field];
  }
  updateFilterCard();
}

export function initAlertFilters(): void {
  loadAlertFilters();

  for (const m of ALERT_TOGGLES) {
    const box = el<HTMLInputElement>(m.id);
    if (!box) continue;
    box.addEventListener('change', () => {
      const obj = objFor(m.obj);
      obj[m.field] = box.checked;
      saveAlertFilters();
      if (m.field === 'ifaceUpDown') updateFilterCard();
      // CAPTURED BEFORE THE REQUEST. The operator can toggle again while this is
      // in flight, so the revert below must put back what THIS request tried to
      // set, not whatever the box happens to say when the reply lands.
      const wanted = box.checked;
      void fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ [m.key]: box.checked }),
      })
        // A non-JSON body still has a status. `{ ok: r.ok }` is what the live
        // code falls back to, so an HTML error page reads as a refusal rather
        // than throwing into the empty catch below and looking like a success.
        .then((r) => r.json().catch(() => ({ ok: r.ok })))
        .then((d) => {
          if (d && d.ok) return;
          box.checked = !wanted;
          obj[m.field] = box.checked;
          saveAlertFilters();
          if (m.field === 'ifaceUpDown') updateFilterCard();
          showBanner('err', 'Could not save that alert toggle: ' + ((d && d.error) || 'not permitted'));
        })
        // EMPTY, and faithful. A network failure leaves the box as the operator
        // set it: the local copy is already saved, so the toggle still governs
        // this browser's bell, and the next Save reconciles the server.
        .catch(() => {});
    });
  }

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'settings') syncAlertFilterUI();
  });
}
