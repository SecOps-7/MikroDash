// The Add/Edit Router dialog's wiring.
//
// ── PLUMBING ONLY ───────────────────────────────────────────────────────────
//
// Every decision this dialog makes lives in a pinned pure function:
// `routerFormValues`, `collectRouterForm`, `seedGeoPicker`, `syncCollDeps`,
// `collectorGridHtml`, `splitBw`/`joinBw`, `TestGate`, `testResultMessage` and
// `labelAfterTest` in router-form.ts, and the town picker — now mounted from
// the SHARED `mountCityPicker` in city-picker.ts, as the live app mounts it for
// this dialog and the site form alike. What is left here is
// elements, listeners and two fetches.
//
// ── WHY THE PICKER HAD TO COME FIRST ────────────────────────────────────────
//
// The save sends `geo: { place: picker.get() }`, and the store reads a null
// `place` as "clear the override". `CityPickerState.get()` returns null while
// the box is only PREVIEWING the automatic location — so an operator editing a
// label does not convert that location into a manual override. Wiring this
// dialog before that guard existed would have wiped a hand-set location on every
// unrelated edit, with nothing on screen to say so.

import { el, esc } from '../dom';
import {
  // `cityListHtml`, `shouldSearchCity`, `formatPlace` and `CITY_DEBOUNCE_MS` were
  // imported here while this file had its own picker wiring. They moved with it
  // into `mountCityPicker` on 2026-08-26. `CityPickerState` stays for the
  // fallback below, when the dialog's markup is absent.
  CityPickerState, mountCityPicker,
} from './city-picker';
import {
  TestGate, collectRouterForm, collectorGridHtml, labelAfterTest, routerFormValues,
  seedGeoPicker, syncCollDeps, testResultMessage,
  storedSiteIds,
  type CollectorDef, type StoredRouter, type SiteRow,
} from './router-form';

/**
 * The stored record for whatever the modal currently has open, or undefined.
 *
 * Read at SAVE time rather than captured when the modal opened: membership is
 * edited in Access Management now, so another tab can change it underneath this
 * one, and a snapshot would write back a list that is already stale.
 *
 * `undefined` for a device this browser does not know is load-bearing —
 * `siteIdsForSave` turns that into an ABSENT `siteIds`, and the server reads
 * absent as "leave membership alone" where an empty array means "remove every
 * site".
 */
function storedRecord(routers: () => StoredRouter[]): StoredRouter | undefined {
  const id = el<HTMLInputElement>('rtrModalId')?.value.trim() || '';
  return id ? routers().find((r) => r.id === id) : undefined;
}

export function initRouterModal(opts: {
  sites: () => Record<string, SiteRow>;
  // The fleet as the page currently knows it, read at SAVE time rather than
  // captured when the modal opened. `siteIdsToSave` reorders the STORED
  // membership, and the store is what another tab's Access Management edit would
  // have changed underneath this one — a snapshot taken at open would then write
  // back a list that is already out of date.
  routers: () => StoredRouter[];
  onSaved: () => void;
}): { open: (router: StoredRouter | null) => void } {
  const gate = new TestGate();
  let registry: CollectorDef[] | null = null;

  const v = <T extends HTMLElement = HTMLInputElement>(id: string): T | null => el<T>(id);
  const input = (id: string): HTMLInputElement | null => el<HTMLInputElement>(id);

  // ── the town picker ───────────────────────────────────────────────────────
  //
  // MOUNTED FROM THE SHARED FUNCTION, as the live app does — `_mountCityPicker`
  // (app.js:11225) serves this dialog and the site form alike. This file carried
  // its own copy for as long as it was the only caller; the site form made a
  // second one, and two implementations of one thing drift.
  //
  // The migration waited for the router-modal-picker check, which was
  // written and passing against the INLINE version first — six mutations killed
  // — so this change had something to be checked against rather than being an
  // unverifiable refactor of working code.
  //
  // ── NO `onChange`, AND THAT IS A CORRECTION ───────────────────────────────
  //
  // This file used to call `gate.invalidate()` when a town was picked, so
  // Test → pick a town → Save re-tested instead of writing. **The live app does
  // not do that**: `_geoPickerEnsure` mounts with `{ clearEl }` only
  // (app.js:8266), and so does the site form (app.js:5461) — nothing in the
  // original passes `onChange` at all. The location is not part of what
  // `/api/routers/test` checks, so there is nothing for a changed town to
  // invalidate.
  //
  // Found by a mutation that SURVIVED: deleting the `onChange` call from the
  // shared mount broke no gate, which sent me to the live source to ask what
  // should have been asserting it. The answer was that nothing should — the code
  // was the addition, not the test the gap. The option stays on
  // `mountCityPicker` because the live `_mountCityPicker` declares it too.
  const geoInput = el<HTMLInputElement>('rtrModalGeo');
  const geoList = el('rtrModalGeoList');
  const place = geoInput && geoList
    ? mountCityPicker(geoInput, geoList, { clearEl: el('rtrModalGeoClear') })
    : new CityPickerState();

  function paintBox(): void {
    const box = input('rtrModalGeo');
    if (box) box.value = place.text();
  }

  // ── the collector grid ────────────────────────────────────────────────────
  function paintGrid(): void {
    const holder = el('rtrModalCollectors');
    if (!holder || !registry) return;
    holder.innerHTML = collectorGridHtml(registry, esc);
  }
  function gridToggles(): HTMLInputElement[] {
    return Array.from(
      el('rtrModalCollectors')?.querySelectorAll<HTMLInputElement>('[data-coll]') || [],
    );
  }
  function readGrid(): { key: string; checked: boolean; requires: string[]; disabled: boolean; dimmed: boolean }[] {
    return gridToggles().map((t) => {
      const lbl = t.closest('.stoggle');
      const raw = lbl?.getAttribute('data-requires') || '';
      return {
        key: t.getAttribute('data-coll') || '',
        checked: t.checked,
        requires: raw ? raw.split(',') : [],
        disabled: t.disabled,
        dimmed: (lbl as HTMLElement | null)?.style.opacity === '.5',
      };
    });
  }
  function applyDeps(): void {
    const next = syncCollDeps(readGrid());
    gridToggles().forEach((t, i) => {
      const s = next[i];
      if (!s) return;
      t.checked = s.checked;
      t.disabled = s.disabled;
      const lbl = t.closest('.stoggle') as HTMLElement | null;
      if (lbl) lbl.style.opacity = s.dimmed ? '.5' : '';
    });
  }
  el('rtrModalCollectors')?.addEventListener('change', applyDeps);

  async function ensureRegistry(): Promise<void> {
    if (registry) return;
    try {
      const r = await fetch('/api/collectors', { credentials: 'same-origin' });
      if (!r.ok) return;
      const d = await r.json() as { collectors?: CollectorDef[] };
      if (!Array.isArray(d?.collectors)) return;
      registry = d.collectors;
      paintGrid();
    } catch {
      // The modal still saves; the grid is simply empty — and an empty grid
      // OMITS the collection block rather than sending `off: []`.
    }
  }

  // ── mode ──────────────────────────────────────────────────────────────────
  function setMode(mode: string): void {
    const hidden = input('rtrModalMode');
    if (hidden) hidden.value = mode === 'poll' ? 'poll' : 'stream';
    el('rtrModalModeWrap')?.querySelectorAll<HTMLElement>('[data-mode]').forEach((b) => {
      b.classList.toggle('active', b.getAttribute('data-mode') === hidden?.value);
    });
  }
  el('rtrModalModeWrap')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-mode]') as HTMLElement | null;
    if (!b) return;
    e.preventDefault();
    setMode(b.getAttribute('data-mode') || 'stream');
  });

  // ── open ──────────────────────────────────────────────────────────────────
  function open(router: StoredRouter | null): void {
    const f = routerFormValues(router, opts.sites());
    const set = (id: string, val: string): void => { const n = input(id); if (n) n.value = val; };
    const check = (id: string, on: boolean): void => { const n = input(id); if (n) n.checked = on; };
    const title = el('rtrModalTitle');
    if (title) title.textContent = f.title;
    set('rtrModalId', f.id); set('rtrModalLabel', f.label);
    // THE PRIMARY PICKER OFFERS ONLY THE SITES THIS DEVICE IS ALREADY IN, so it
    // cannot name one it does not belong to and no control here can add or
    // remove a membership by accident.
    //
    // A site deleted since the device was filed has no name to show and is left
    // OUT of the picker — but it is not dropped from the device: see
    // `siteIdsToSave`, which reorders the stored list rather than rebuilding it
    // from these options.
    const primary = el<HTMLSelectElement>('rtrModalPrimarySite');
    if (primary) {
      const all = opts.sites();
      primary.innerHTML = '<option value="">— No site —</option>';
      (f.siteIds || []).filter((id) => all[id]).forEach((id) => {
        const o = document.createElement('option');
        o.value = id;
        o.textContent = all[id]!.name || id;
        primary.appendChild(o);
      });
      // The primary is the FIRST entry, which is the order the store keeps.
      primary.value = f.primarySite || (f.siteIds && f.siteIds[0]) || '';
    }
    set('rtrModalHost', f.host); set('rtrModalPort', f.port); set('rtrModalUser', f.username);
    set('rtrModalPass', '');
    const pass = input('rtrModalPass');
    if (pass) pass.placeholder = f.passPlaceholder;
    set('rtrModalIf', f.defaultIf); set('rtrModalPing', f.pingTarget);
    check('rtrModalTls', f.tls); check('rtrModalTlsInsecure', f.tlsInsecure);
    check('rtrModalAlertsEnabled', f.alertsEnabled);
    set('rtrModalDownThresh', String(f.downThreshold));
    set('rtrModalBwDown', String(f.bwDown.value)); set('rtrModalBwDownUnit', f.bwDown.unit);
    set('rtrModalBwUp', String(f.bwUp.value)); set('rtrModalBwUpUnit', f.bwUp.unit);
    setMode(f.mode);

    const site = (router && router.siteId) ? (opts.sites()[router.siteId] || null) : null;
    const seed = seedGeoPicker(router?.geo as never, site, esc);
    if (seed.mode === 'set') place.set(seed.value);
    else if (seed.mode === 'preview') place.preview(seed.value);
    else place.clear();
    paintBox();
    const hint = el('rtrModalGeoHint');
    if (hint) hint.innerHTML = seed.hint;

    const applyOff = (): void => {
      gridToggles().forEach((t) => {
        t.checked = f.off.indexOf(t.getAttribute('data-coll') || '') === -1;
      });
      applyDeps();
    };
    if (registry) applyOff(); else void ensureRegistry().then(applyOff);

    hideTestResult();
    // An EDIT starts ready — its stored credentials already worked. An ADD must
    // test first.
    if (router) gate.pass();
    el('rtrModalBg')?.classList.add('open');
    input('rtrModalHost')?.focus();
  }

  // ── test and save ─────────────────────────────────────────────────────────
  function showTestResult(ok: boolean, msg: string): void {
    const box = el('rtrTestResult');
    if (!box) return;
    box.style.display = '';
    // `className` is REPLACED, not toggled, and the failure class is `err`.
    // I wrote `classList.toggle('ok'/'bad')` — wrong on both counts, and
    // `class-hook-audit` caught it because nothing styles `bad`. Replacing is
    // what the original does and it also clears whatever the previous result
    // left behind, which a toggle pair does not.
    box.className = 'rtr-test-result ' + (ok ? 'ok' : 'err');
    box.textContent = msg;
  }
  function hideTestResult(): void {
    const box = el('rtrTestResult');
    if (box) box.style.display = 'none';
  }

  function body(): Record<string, unknown> {
    return collectRouterForm({
      id: input('rtrModalId')?.value || '',
      label: input('rtrModalLabel')?.value || '',
      // The device's STORED membership, straight from the record this browser
      // holds — `collectRouterForm` reorders it and leaves it ABSENT when there
      // is no record. See `siteIdsForSave`.
      siteIds: storedSiteIds(storedRecord(opts.routers)),
      primarySite: el<HTMLSelectElement>('rtrModalPrimarySite')?.value || '',
      geoPlace: place.get(),
      host: input('rtrModalHost')?.value || '',
      port: input('rtrModalPort')?.value || '',
      username: input('rtrModalUser')?.value || '',
      password: input('rtrModalPass')?.value || '',
      defaultIf: input('rtrModalIf')?.value || '',
      pingTarget: input('rtrModalPing')?.value || '',
      tls: !!input('rtrModalTls')?.checked,
      tlsInsecure: !!input('rtrModalTlsInsecure')?.checked,
      bwDownRaw: input('rtrModalBwDown')?.value || '',
      bwDownUnit: input('rtrModalBwDownUnit')?.value || 'mbps',
      bwUpRaw: input('rtrModalBwUp')?.value || '',
      bwUpUnit: input('rtrModalBwUpUnit')?.value || 'mbps',
      alertsEnabled: !!input('rtrModalAlertsEnabled')?.checked,
      downThresholdRaw: input('rtrModalDownThresh')?.value || '',
      mode: input('rtrModalMode')?.value || 'stream',
      toggles: readGrid(),
    });
  }

  async function runTest(data: Record<string, unknown>): Promise<boolean> {
    const r = await fetch('/api/routers/test', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin', body: JSON.stringify(data),
    });
    const j = await r.json() as { ok?: boolean; boardName?: string; error?: string };
    showTestResult(!!j.ok, testResultMessage(!!j.ok, j.boardName, j.error));
    if (j.ok) {
      gate.pass();
      const lbl = input('rtrModalLabel');
      if (lbl) lbl.value = labelAfterTest(lbl.value, j.boardName);
      return true;
    }
    return false;
  }

  v<HTMLButtonElement>('rtrModalTestBtn')?.addEventListener('click', async () => {
    const btn = v<HTMLButtonElement>('rtrModalTestBtn');
    const data = body();
    if (!data.host) { showTestResult(false, 'Host is required'); return; }
    if (btn) { btn.disabled = true; btn.textContent = 'Testing…'; }
    hideTestResult();
    try { await runTest(data); } catch (e) { showTestResult(false, '✗ Request failed: ' + String(e)); }
    if (btn) { btn.disabled = false; btn.textContent = 'Test Connection'; }
  });

  v<HTMLButtonElement>('rtrModalSaveBtn')?.addEventListener('click', async () => {
    const btn = v<HTMLButtonElement>('rtrModalSaveBtn');
    const data = body();
    if (!data.host) { showTestResult(false, 'Host is required'); return; }
    if (btn) { btn.disabled = true; btn.textContent = gate.maySaveDirectly() ? 'Saving…' : 'Testing…'; }
    try {
      // SAVE ONLY AFTER A PASSING TEST. Credentials that were never tried
      // against the router they now name must not reach the store.
      if (!gate.maySaveDirectly() && !(await runTest(data))) return;
      const id = String(data.id || '');
      const r = await fetch(id ? '/api/routers/' + encodeURIComponent(id) : '/api/routers', {
        method: id ? 'PUT' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin', body: JSON.stringify(data),
      });
      if (!r.ok) { showTestResult(false, '✗ Save failed'); return; }
      el('rtrModalBg')?.classList.remove('open');
      opts.onSaved();
    } catch (e) {
      showTestResult(false, '✗ Request failed: ' + String(e));
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = 'Save'; }
    }
  });

  // Any edit invalidates a passing test — see TestGate.
  for (const id of ['rtrModalHost', 'rtrModalPort', 'rtrModalUser', 'rtrModalPass', 'rtrModalGeo']) {
    input(id)?.addEventListener('input', () => { if (gate.invalidate()) hideTestResult(); });
  }
  for (const id of ['rtrModalTls', 'rtrModalTlsInsecure']) {
    input(id)?.addEventListener('change', () => { if (gate.invalidate()) hideTestResult(); });
  }

  for (const id of ['rtrModalCancelBtn', 'rtrModalCloseBtn']) {
    el(id)?.addEventListener('click', () => { el('rtrModalBg')?.classList.remove('open'); });
  }

  return { open };
}
