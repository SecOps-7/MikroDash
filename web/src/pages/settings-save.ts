/**
 * The Settings page's Save button: read the whole form, POST it, report honestly.
 *
 * ── THE BUTTON WAS BOUND TO NOTHING ─────────────────────────────────────────
 *
 * `#settingsSaveBtn` existed, was enabled and disabled by `caps.ts`, and had no
 * listener anywhere in the app. Clicking it made no request and showed no
 * banner, so NO server-side setting could be saved from the UI on any tab —
 * poll intervals, notification channels, thresholds, page visibility, session
 * timeout. `settingsResetBtn` beside it was wired, which is what made the page
 * look alive. Reported on issue #126 as "Appearance Save not working", which was
 * simply the nearest visible symptom.
 *
 * ── WHY THIS IS ITS OWN MODULE ──────────────────────────────────────────────
 *
 * `settings.ts` is the READ half and its header is a long argument for that;
 * `settings-poll.ts` takes its `reloadSettings` as a parameter precisely so it
 * need not depend on the form. Putting the write half in either would undo one
 * of those decisions. This imports from `settings-poll.ts` and nothing imports
 * it back, so there is no cycle.
 */

import { el } from '../dom';
import { FORM_FIELDS, PLACEHOLDER_CREDENTIALS } from '../gen/settings-form-map';
import { INT_FIELDS, STR_FIELDS } from '../gen/settings-write-fields';
import { showBanner, customValues } from './settings-poll';

/** A partial patch for `POST /api/settings`; the route merges it over the rest. */
export type SettingsPatch = Record<string, unknown>;

/**
 * Trimmed on the way out, as the live collector trims them.
 *
 * `STR_FIELDS` is the server's own list and the server trims those itself, so
 * doing it here is belt and braces. The two template bodies are NOT in it —
 * they are `specialCases` server-side, trimmed and cut to 512 — so they are
 * named here or they would go out with the operator's stray newline.
 */
const TRIMMED = new Set<string>([...STR_FIELDS, 'notifBody', 'notifBodyUp']);

/**
 * Read the whole Settings form into a patch.
 *
 * ── DRIVEN BY THE TABLE, NEVER BY THE DOM ───────────────────────────────────
 *
 * It walks the same `settings-form-map` lists `populateSettings` walks, reading
 * each field by id `'s_' + key`. A `[id^="s_"]` sweep would be shorter and
 * wrong: it would pick up the three `s_*Val` readout SPANS, which have no
 * `.value`, and it would need a hand-written exclusion list to keep out
 * anything added later — a second copy of a generated table, which is the thing
 * this repo is organised against.
 *
 * ── EVERY LOOKUP IS GUARDED, AND THAT IS THE LOAD-BEARING RULE ──────────────
 *
 * `if (!input) continue`, exactly as the live collector did it. Eight keys in
 * the form map have no element on this page — the legacy single-router fields,
 * `routerHost` and friends, which moved to the per-router modal — and the guard
 * is the whole reason the narrower markup needs no special case. Pinned by a
 * test, because it looks like a redundant null check and is not.
 */
export function collectSettingsForm(
  pollValues: () => Record<string, number> = customValues,
): SettingsPatch {
  const out: SettingsPatch = {};

  // ── BOOLEANS MUST BE BOOLEANS ─────────────────────────────────────────────
  //
  // The server accepts a literal `true` or the string "true" and NOTHING else,
  // so a checkbox serialised the HTML-form way — "on" — would read as false and
  // switch every page off. `.checked` is already the right type; this comment is
  // here so nobody "simplifies" it into a string.
  //
  // `checkGuarded` is included even though `settings-alert-filters.ts` saves
  // those 13 the instant they change. Its network-failure path leaves the box as
  // the operator set it and says "the next Save reconciles the server" — which
  // is only true if this sends them.
  for (const key of [...FORM_FIELDS.checkOn, ...FORM_FIELDS.checkOff,
    ...FORM_FIELDS.checkGuarded]) {
    const input = el<HTMLInputElement>('s_' + key);
    if (!input) continue;
    out[key] = input.checked;
  }

  for (const key of FORM_FIELDS.value) {
    // The write-only credentials are handled below; `smtpUser` is NOT one of
    // them and belongs here — see the note on it there.
    if (PLACEHOLDER_CREDENTIALS[key]) continue;
    const input = el<HTMLInputElement>('s_' + key);
    if (!input) continue;
    const raw = input.value;

    if (key === 'updateCheckHours') {
      // SKIPPED WHEN BLANK, and clamped when not. The server IGNORES an
      // out-of-range integer rather than clamping it, so without this a typed
      // 500 is silently dropped and the old value reappears after the reload
      // with nothing on screen to explain it.
      if (raw === '') continue;
      const n = parseInt(raw, 10);
      if (!Number.isFinite(n)) continue;
      out[key] = Math.max(1, Math.min(168, n));
      continue;
    }
    if (key === 'smtpPort') {
      // `|| 587`, as the live collector has it: a blank SMTP port means the
      // default, not "leave whatever was there".
      out[key] = parseInt(raw, 10) || 587;
      continue;
    }
    if (INT_FIELDS[key]) {
      const n = parseInt(raw, 10);
      // NOT SENT when unparseable. The server would ignore a NaN anyway, but
      // `JSON.stringify(NaN)` is `null`, and sending null for a number reads as
      // an attempt rather than an omission.
      if (Number.isFinite(n)) out[key] = n;
      continue;
    }
    // `displayTimezone` and `smtpUser` fall through UNTRIMMED, deliberately.
    // The server does not trim credentials, and a timezone is chosen from a
    // select rather than typed.
    out[key] = TRIMMED.has(key) ? raw.trim() : raw;
  }

  // ── THE WRITE-ONLY CREDENTIALS: OMIT WHEN BLANK ───────────────────────────
  //
  // `populateSettings` clears these on every load and puts the meaning in the
  // placeholder, so an untouched box is ALWAYS empty. The server reads an empty
  // string as an explicit destructive clear — so sending them unconditionally
  // would wipe every stored secret on the first Save, and the page would still
  // render "not set" afterwards, making it look deliberate.
  //
  // Consequence, accepted rather than papered over: there is no way to CLEAR a
  // credential from this form, exactly as in the app this was ported from.
  // Telling the two empties apart needs a per-field dirty flag or an explicit
  // Clear control, and any shortcut is the bug above.
  //
  // `routerPass` is in this table and has no input here — router credentials
  // belong to the per-router modal, and the server does not accept it on this
  // route at all — so the guard drops it.
  //
  // `smtpUser` is NOT here. It is an ordinary value field that receives the MASK
  // from the server and hands it straight back, and `store.IsMasked` drops it
  // from the updates. That round trip is what keeps it safe, and it holds only
  // while all three of `disclose.go`'s mask list, the form map's kinds and the
  // server's mask test agree — which is why both ends of it are tested.
  for (const key of Object.keys(PLACEHOLDER_CREDENTIALS)) {
    const input = el<HTMLInputElement>('s_' + key);
    if (!input || input.value === '') continue;
    out[key] = input.value;
  }

  // ── THE ONE ID THAT IS NOT `s_<key>` ──────────────────────────────────────
  //
  // The control is `s_authEnabled`; the setting is `authMode`, a two-valued
  // string. `populateSettings` sets the box from `authModeOf(data)` — it did not
  // until 0.8.15, and collecting an unpopulated box would have posted
  // `authMode: 'none'` and switched sign-in off for the whole install on the
  // first Save from any tab.
  const auth = el<HTMLInputElement>('s_authEnabled');
  if (auth) out.authMode = auth.checked ? 'modern' : 'none';

  // ── THE POLL SLIDERS ──────────────────────────────────────────────────────
  //
  // Built at runtime into `#pollSlidersWrap`, so they are absent from the form
  // map and would otherwise be missed — and a Save that ignored them would make
  // the Polling tab's button do nothing, which is this bug one tab over.
  //
  // `customValues()` is reused rather than the loop retyped. `customPollProfile`
  // is deliberately NOT written: that key is the operator's saved preset and
  // belongs to the Save Custom Profile button, not to this one.
  Object.assign(out, pollValues());

  return out;
}

/**
 * Bind the Save button.
 *
 * `reloadSettings` is the same loader `initPollAndBanner` gets, so Save and
 * Reset refresh the page identically.
 */
export function initSettingsSave(reloadSettings: () => void): void {
  const btn = el<HTMLButtonElement>('settingsSaveBtn');
  if (!btn) return;

  // CAPTURED ONCE, AS MARKUP. The label carries an inline SVG, and the live code
  // restored it with `textContent` on the network-error path — so one failed
  // request permanently deleted the icon. Keeping the original innerHTML makes
  // every path restore the same button.
  //
  // The round trip is this button's OWN static markup from `page-settings.html`,
  // read and written back untouched. Nothing a user or a router supplied ever
  // reaches it, which is what makes the innerHTML safe here.
  const original = btn.innerHTML;
  const restore = (): void => {
    btn.disabled = false;
    btn.innerHTML = original;
  };

  btn.addEventListener('click', () => {
    btn.disabled = true;
    btn.textContent = 'Saving…';
    void fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(collectSettingsForm()),
    })
      // A reverse proxy's HTML error page still has a status. Falling back to
      // `{ ok: r.ok }` makes it read as a refusal instead of throwing into the
      // catch, where it would be reported as a network failure it is not.
      .then((r) => r.json().catch(() => ({ ok: r.ok })))
      .then((d: { ok?: boolean; error?: string }) => {
        restore();
        if (d && d.ok) {
          showBanner('ok', '✓ Settings saved');
          // LOAD-BEARING, not cosmetic. It re-blanks the credential inputs so a
          // second Save does not re-post a secret typed for the first, and it
          // re-reads what the server actually stored — which, because invalid
          // values are ignored rather than clamped, is not always what was sent.
          reloadSettings();
          return;
        }
        // ── NO RELOAD ON A REFUSAL ──────────────────────────────────────────
        //
        // Reloading here would repaint every field from the server and blank the
        // credential boxes, so everything the operator typed would vanish and
        // the page would look exactly as it does after a success. The sibling
        // Reset button carries the scar for the other half of this — it once
        // reported "✓ Reset to defaults" on a 403 — and this is the worse half,
        // because it destroys work rather than merely lying about it.
        showBanner('err', 'Save failed: ' + ((d && d.error) || 'unknown error'));
      })
      .catch((e) => {
        restore();
        showBanner('err', 'Request failed: ' + e);
      });
  });
}
