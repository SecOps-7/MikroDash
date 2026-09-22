/**
 * Settings → the poll-interval sliders, the preset profiles, the settings
 * banner and the reset button.
 *
 * Four LOOP items in one module because they are one IIFE in the live app and
 * share three things across it: `fmtMs`, `showBanner` (which the live code
 * hoists onto `window` for the alert toggles) and the profile UI state.
 *
 * ── THE TABLES ARE GENERATED, NOT RETYPED ───────────────────────────────────
 *
 * `../gen/poll-tables` is lifted by the poll table generator. Three ways these
 * drift silently, all in the generator's header: a slider missing from the table
 * is simply not drawn (the interval stays live and uneditable), a key missing
 * from a profile is skipped rather than defaulted, and a profile button with no
 * entry lights up and writes nothing.
 *
 * ── `cfg.streamed` IS DEAD AGAINST TODAY'S TABLE ────────────────────────────
 *
 * No row currently carries it — measured, and the generator records it. The
 * branch is reproduced anyway because the live app has it and a row could gain
 * the flag at any time; the poll-sliders check injects a synthetic table
 * so BOTH sides actually execute it rather than both skipping it.
 */

import { el } from '../dom';
import { POLL_SLIDERS, POLL_PROFILES, POLL_PROFILE_KEY, type PollSlider } from '../gen/poll-tables';

export type PollData = Record<string, unknown>;

/**
 * `fmtMs`: the label beside each slider.
 *
 * The `ms % 1000 === 0` test is what makes 1500ms read "1.5s" and 2000ms read
 * "2s" rather than "2.0s". Reproduced exactly — it is the difference between a
 * row of tidy numbers and a row of trailing zeroes.
 */
export function fmtMs(ms: number): string {
  if (ms >= 60000) return (ms / 60000).toFixed(0) + 'm';
  if (ms >= 1000) return (ms / 1000).toFixed(ms % 1000 === 0 ? 0 : 1) + 's';
  return ms + 'ms';
}

/**
 * Which profile the current settings correspond to, or 'custom'.
 *
 * A profile matches only if EVERY key it names is already the stored value. The
 * live comment records that `standard` deliberately does not match
 * `Settings.DEFAULTS`, "which is why a fresh install detects Custom rather than
 * Standard — pre-existing, and left alone". Reproduced, quirk included: a port
 * that tidied this would change what a fresh install displays.
 */
export function detectProfile(
  data: PollData,
  profiles: Record<string, Record<string, number>> = POLL_PROFILES,
): string {
  for (const name of Object.keys(profiles)) {
    const p = profiles[name] as Record<string, number>;
    let match = true;
    for (const k of Object.keys(p)) {
      if (data[k] !== p[k]) { match = false; break; }
    }
    if (match) return name;
  }
  return 'custom';
}

/** The profile buttons' active state, and the remembered choice. */
export function setPollProfileUI(name: string): void {
  document.querySelectorAll('.poll-profile-btn').forEach((btn) => {
    btn.classList.toggle('active', (btn as HTMLElement).dataset.profile === name);
  });
  // The live `try/catch` is load-bearing rather than defensive: localStorage
  // THROWS on access in a private window and behind a cookie block, and an
  // uncaught throw here would abandon the click before the sliders moved.
  try {
    localStorage.setItem(POLL_PROFILE_KEY, name);
  } catch {
    /* a browser that refuses storage still gets working buttons */
  }
}

/** A preset button: write its values into the sliders, then mark it active. */
export function applyPollProfile(
  name: string,
  sliders: PollSlider[] = POLL_SLIDERS,
  profiles: Record<string, Record<string, number>> = POLL_PROFILES,
): void {
  const p = profiles[name];
  if (p) {
    sliders.forEach((cfg) => {
      if (cfg.streamed) return;
      // A profile with no value for this slider LEAVES IT ALONE rather than
      // writing undefined into it — the live "belt and braces behind the drift
      // test". The drift test is the poll table generator, which records the
      // coverage gaps per profile.
      if (p[cfg.key] === undefined) return;
      const slider = el<HTMLInputElement>('s_' + cfg.key);
      const valEl = el('sv_' + cfg.key);
      if (slider) {
        slider.value = String(p[cfg.key]);
        if (valEl) valEl.textContent = fmtMs(p[cfg.key] as number);
      }
    });
  }
  // OUTSIDE the `if`. An unknown profile still becomes the active button and is
  // still remembered, which is what makes `custom` work before it has been
  // saved — it has no entry in the table until the operator saves one.
  setPollProfileUI(name);
}

/**
 * The settings banner.
 *
 * An ERROR STAYS UP. Only ok/info clear themselves after four seconds; a failure
 * the operator did not see is a failure they will assume did not happen.
 */
export function showBanner(type: string, msg: string): void {
  const banner = el('settingsBanner');
  if (!banner) return;
  banner.className = 'sbanner show sbanner-' + type;
  banner.textContent = msg;
  if (type !== 'err') {
    setTimeout(() => {
      banner.className = 'sbanner';
    }, 4000);
  }
}

/**
 * Draw the sliders.
 *
 * A HEADING PER RANGE, emitted when the range changes rather than up front, so
 * the two groups appear in table order and a re-ordered table re-groups itself.
 * The live comment: "so a reader can see which sliders share a ceiling rather
 * than inferring it from the numbers."
 */
export function buildSliders(data: PollData, sliders: PollSlider[] = POLL_SLIDERS): void {
  const wrap = el('pollSlidersWrap');
  if (!wrap) return;
  wrap.innerHTML = '';
  let lastRange: string | null = null;
  sliders.forEach((cfg) => {
    const range = cfg.max === 30000 ? 'live' : 'slow';
    if (range !== lastRange) {
      lastRange = range;
      const hdr = document.createElement('div');
      hdr.className = 'poll-group-hdr';
      hdr.textContent = range === 'live'
        ? 'Live data — 1s to 30s'
        : 'Slow-changing data — 10s to 10m';
      wrap.appendChild(hdr);
    }
    const row = document.createElement('div');
    row.style.cssText = 'margin-bottom:.7rem';
    if (cfg.streamed) {
      row.innerHTML =
        '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:.25rem">' +
          '<span style="font-size:.75rem;color:var(--text-muted)">' + cfg.label + '</span>' +
          '<span style="font-size:.68rem;font-family:var(--font-ui);padding:.15rem .5rem;border-radius:4px;background:rgba(99,190,130,.12);color:#6dba8a;border:1px solid rgba(99,190,130,.25)">Event-driven</span>' +
        '</div>';
      wrap.appendChild(row);
      return;
    }
    const min = cfg.min as number;
    const max = cfg.max as number;
    const raw = data[cfg.key];
    // CLAMPED, and the fallback is `min` rather than the stored value: an
    // interval outside the slider's range would otherwise set `.value` to
    // something the input silently rounds, and the label beside it would then
    // disagree with the control.
    const val = raw != null ? Math.max(min, Math.min(max, raw as number)) : min;
    row.innerHTML =
      '<label class="sform-label">' + cfg.label + '</label>' +
      '<div style="display:flex;align-items:center;gap:.6rem">' +
        '<input type="range" id="s_' + cfg.key + '" ' +
          'min="' + min + '" max="' + max + '" step="' + cfg.step + '" value="' + val + '" ' +
          'style="flex:1;accent-color:var(--accent-rx)">' +
        '<span class="srange-val" id="sv_' + cfg.key + '">' + fmtMs(val) + '</span>' +
      '</div>';
    wrap.appendChild(row);
    const slider = el<HTMLInputElement>('s_' + cfg.key);
    const valEl = el('sv_' + cfg.key);
    if (slider && valEl) {
      slider.addEventListener('input', () => {
        valEl.textContent = fmtMs(parseInt(slider.value, 10));
        // Moving ANY slider by hand means the selection is no longer a preset.
        setPollProfileUI('custom');
      });
    }
  });
}

/**
 * The values the custom-profile save sends, read back off the drawn sliders.
 *
 * ── THE `streamed` GUARD HERE IS BELT AND BRACES, AND THAT IS MEASURED ──────
 *
 * Removing it changes nothing, and a mutant doing so SURVIVES: `buildSliders`
 * never creates an `s_<key>` input for a streamed row, so the lookup below
 * already returns null and the row is already skipped. Kept because the live
 * code has it and because it states the intent at the point of use — but
 * recorded as equivalent rather than left looking like an untested branch that
 * someone should go and write a case for.
 */
export function customValues(sliders: PollSlider[] = POLL_SLIDERS): Record<string, number> {
  const out: Record<string, number> = {};
  sliders.forEach((cfg) => {
    if (cfg.streamed) return;
    const e = el<HTMLInputElement>('s_' + cfg.key);
    if (e) out[cfg.key] = parseInt(e.value, 10);
  });
  return out;
}

/**
 * The custom save's body: every interval as its own key, PLUS the whole set
 * again as a JSON string under `customPollProfile`.
 *
 * Both halves are needed and they are not redundant: the flat keys are what the
 * collectors actually read, and the JSON blob is what lets the Custom button
 * restore this exact set later. A port sending only the flat keys would save the
 * intervals and lose the preset.
 */
export function customSaveBody(vals: Record<string, number>): Record<string, unknown> {
  const payload: Record<string, unknown> = {};
  for (const k of Object.keys(vals)) payload[k] = vals[k];
  payload.customPollProfile = JSON.stringify(vals);
  return payload;
}

function showCustomStatus(ok: boolean, msg: string): void {
  const st = el('pollCustomSaveStatus');
  if (!st) return;
  st.textContent = msg;
  st.style.color = ok ? 'var(--accent-ok)' : 'var(--accent-err)';
  st.style.opacity = '1';
  setTimeout(() => {
    st.style.opacity = '0';
  }, 3000);
}

/**
 * The poll half of the live `populate(data)`, in its order.
 *
 * ── THE ORDER MATCHES THE LIVE ONE AND IS CURRENTLY EQUIVALENT ─────────────
 *
 * `POLL_PROFILES.custom` is filled from the stored blob before `detectProfile`
 * runs, so the detect can MATCH it. Moving the restore after the detect survives
 * the gate, and that is not a missing case — it is provable:
 * `detectProfile`'s fallback is ITSELF `'custom'`, so a stored set matching the
 * saved profile returns "custom" by match in one ordering and "custom" by
 * fallback in the other. Same answer for every input, and both orderings leave
 * the profile restored before the operator can click anything.
 *
 * Kept in the live order anyway: it is what the live app does, and the two stop
 * being equivalent the moment the fallback is anything other than the name being
 * restored.
 *
 * ── AND A CORRUPT BLOB IS SWALLOWED ────────────────────────────────────────
 *
 * The live `try { … } catch(e) {}` is empty on purpose: `customPollProfile` is a
 * string in a settings file a human can edit, and a JSON error there must not
 * stop the sliders being drawn. The cost of the empty catch is that the operator
 * silently loses their custom preset; the cost of removing it is a Settings page
 * that renders nothing at all.
 */
export function applyPollSettings(data: PollData): void {
  if (data.customPollProfile) {
    try {
      POLL_PROFILES.custom = JSON.parse(data.customPollProfile as string);
    } catch {
      /* a hand-edited settings file must not stop the page rendering */
    }
  }
  buildSliders(data);
  setPollProfileUI(detectProfile(data));
}

// ── The wiring ──────────────────────────────────────────────────────────────

/**
 * `reloadSettings` is passed in rather than imported.
 *
 * The reset needs to re-read the whole settings page afterwards, and that loader
 * lives in the module that owns the form. Importing it here would make these two
 * modules mutually dependent for one call.
 */
export function initPollAndBanner(reloadSettings: () => void): void {
  document.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('.poll-profile-btn') as HTMLElement | null;
    if (!btn || !btn.dataset.profile) return;
    applyPollProfile(btn.dataset.profile);
  });

  const saveBtn = el<HTMLButtonElement>('pollCustomSaveBtn');
  if (saveBtn) {
    saveBtn.addEventListener('click', () => {
      const vals = customValues();
      saveBtn.disabled = true;
      saveBtn.textContent = 'Saving…';
      fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(customSaveBody(vals)),
      })
        .then((r) => r.json())
        .then((d) => {
          saveBtn.disabled = false;
          saveBtn.textContent = 'Save Custom Profile';
          if (d && d.ok) {
            // The table gains a `custom` row only now, which is why
            // `applyPollProfile` marks an unknown profile active anyway.
            POLL_PROFILES.custom = vals;
            setPollProfileUI('custom');
            showCustomStatus(true, '✓ Saved');
          } else {
            showCustomStatus(false, '✗ ' + ((d && d.error) || 'failed'));
          }
        })
        .catch(() => {
          saveBtn.disabled = false;
          saveBtn.textContent = 'Save Custom Profile';
          showCustomStatus(false, '✗ Request failed');
        });
    });
  }

  const resetBtn = el<HTMLButtonElement>('settingsResetBtn');
  if (resetBtn) {
    resetBtn.addEventListener('click', () => {
      if (!confirm('Reset all settings to defaults? This cannot be undone.')) return;
      fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ _reset: true }),
      })
        .then((r) => r.json())
        // CHECKING `d.ok` IS NOT DECORATION. The live comment records that this
        // reported "✓ Reset to defaults" on a 403 too — "the one thing it must
        // never do, claim a destructive change happened when it did not, is
        // exactly what it did".
        .then((d) => {
          if (d && d.ok) {
            showBanner('ok', '✓ Reset to defaults');
            reloadSettings();
          } else {
            showBanner('err', 'Reset failed: ' + ((d && d.error) || 'not permitted'));
          }
        })
        .catch((e) => showBanner('err', 'Reset failed: ' + e));
    });
  }
}
