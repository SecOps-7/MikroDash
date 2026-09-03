/**
 * The first-run ROUTER overlay's wiring.
 *
 * ── THE DECISIONS ARE ALL IN `setup-overlay.ts` ────────────────────────────
 *
 * `collectSetupBody`, `flipPortForTls`, `SETUP_WATCH_FIELDS` and
 * `setupTestResultText` are ported and gated there. What is here is the part a
 * corpus cannot hold: the two fetches, the listeners, and the lock.
 *
 * That module has been complete and UNREACHABLE — recorded in
 * The reachable audit — for the same reason the router modal was: nothing
 * mounted it. This is the mount.
 *
 * ── SAVE IS LOCKED UNTIL A TEST PASSES, AND ANY CHANGE RE-LOCKS IT ─────────
 *
 * The interlock is the same shape as the Data Cleanup card's preview: a button
 * that acts on a result the operator actually saw. Here the result is "this host
 * answered", and the fields that can make it wrong are exactly
 * `SETUP_WATCH_FIELDS` — not every field, because re-locking on a typo in the
 * LABEL would make someone re-run a connection test to fix a name.
 *
 * ── IT RUNS WHEN THE FLEET IS EMPTY ────────────────────────────────────────
 *
 * `setup:required` is emitted when no router is configured at all, so this is
 * the one screen an operator sees before anything else works. Hard to reach on
 * an install that already has routers, which is why it stayed unwired so long —
 * and why the check below drives it rather than a browser.
 */

import { el } from '../dom';
import {
  collectSetupBody, flipPortForTls, SETUP_WATCH_FIELDS, setupTestResultText,
  type SetupBody,
} from './setup-overlay';

let testPassed = false;

/**
 * Lock or unlock Save.
 *
 * THE TITLE IS PART OF IT. A disabled button with no explanation reads as a bug;
 * the live app puts the reason in the tooltip, which is the only affordance a
 * disabled control has left.
 */
function setSaveReady(ready: boolean): void {
  testPassed = ready;
  const save = el<HTMLButtonElement>('setupSaveBtn');
  if (!save) return;
  save.disabled = !ready;
  save.style.opacity = ready ? '' : '0.45';
  save.title = ready ? '' : 'Run "Test Connection" successfully before saving';
}

function showErr(msg: string): void {
  const box = el('setupError');
  if (!box) return;
  box.textContent = msg;
  box.style.display = 'block';
}

function clearErr(): void {
  const box = el('setupError');
  if (box) box.style.display = 'none';
}

/**
 * Show the overlay.
 *
 * IT ALSO PUTS THE APP INTO ITS DISCONNECTED STATE — the body class, the
 * `_rosCurrentlyDisconnected` flag and the paused diagram. Without that the page
 * behind the overlay animates as though it were live, which is exactly what it
 * is not: there is no router at all.
 */
function showOverlay(): void {
  const ov = el('setupOverlay');
  if (!ov) return;
  ov.style.display = 'block';
  document.body.classList.add('is-disconnected');
  (globalThis as unknown as { _rosCurrentlyDisconnected?: boolean })._rosCurrentlyDisconnected = true;
  const svg = el('netDiagram') as unknown as SVGSVGElement | null;
  if (svg && typeof svg.pauseAnimations === 'function') svg.pauseAnimations();
  // ALWAYS START LOCKED, even if a previous showing left it open.
  setSaveReady(false);
}

function hideOverlay(): void {
  const ov = el('setupOverlay');
  if (!ov) return;
  ov.style.display = 'none';
  document.body.classList.remove('is-disconnected');
}

/** Read the form. The defaults live in `collectSetupBody`. */
function body(): SetupBody {
  const v = (id: string): string => el<HTMLInputElement>(id)?.value ?? '';
  const c = (id: string): boolean => !!el<HTMLInputElement>(id)?.checked;
  return collectSetupBody({
    label: v('setupLabel'), host: v('setupHost'), port: v('setupPort'),
    username: v('setupUser'), password: v('setupPass'),
    defaultIf: v('setupIf'), pingTarget: v('setupPing'),
    tls: c('setupTls'), tlsInsecure: c('setupTlsInsecure'),
  });
}

function setBusy(busy: boolean): void {
  const test = el<HTMLButtonElement>('setupTestBtn');
  const save = el<HTMLButtonElement>('setupSaveBtn');
  if (test) test.disabled = busy;
  if (save) {
    // NOT JUST `busy`. Clearing the busy state must not hand back a Save the
    // interlock had locked — the same conditional re-enable the Data Cleanup
    // card needs, and for the same reason.
    save.disabled = busy || !testPassed;
    save.textContent = busy ? 'Connecting…' : 'Connect';
  }
}

export interface SetupSocket { on(event: string, cb: () => void): void }

/**
 * Show the first-run router wizard now, without waiting for a socket event.
 *
 * ── WHY THE EVENT ALONE IS NOT ENOUGH ───────────────────────────────────────
 *
 * `setup:required` is broadcast when the LAST router is deleted, which reaches
 * every browser already open. It says nothing to a browser that arrives at an
 * install which never had a router — and that is a first run, the one case this
 * overlay exists for. A new operator got the dashboard instead, drawn in full
 * with every card empty and nothing on screen saying a router was needed or
 * where to add one (issue #124).
 *
 * Emitting the event on connect instead was tried and RACES: the server sends it
 * as the socket registers, while the browser only subscribes once `main()` has
 * finished its awaited fetches, so the frame lands before the listener exists.
 * The overlay stayed hidden, which is the same silence with more moving parts.
 *
 * The caller already has the answer. `main()` fetches `/api/routers` before it
 * gets here, so "is the fleet empty" is a value in hand rather than something to
 * be told, and calling this directly has no timing to get wrong.
 */
export function showSetupOverlayNow(): void {
  if (!el('setupOverlay')) return;
  showOverlay();
}

export function initSetupOverlay(socket: SetupSocket): void {
  const ov = el('setupOverlay');
  // The live guard. On an install that HAS routers this markup is present but
  // the event never fires, so the listeners below are registered and idle.
  if (!ov) return;

  socket.on('setup:required', showOverlay);

  // ANY change to a connection field spends the passing test.
  for (const id of SETUP_WATCH_FIELDS) {
    const field = el<HTMLInputElement>(id);
    if (!field) continue;
    const evt = field.type === 'checkbox' ? 'change' : 'input';
    field.addEventListener(evt, () => {
      // ONLY WHEN A TEST HAD PASSED. Clearing the result line on every keystroke
      // would wipe a failure message the operator is still reading.
      if (!testPassed) return;
      setSaveReady(false);
      const res = el('setupTestResult');
      if (res) res.textContent = '';
    });
  }

  const tls = el<HTMLInputElement>('setupTls');
  const port = el<HTMLInputElement>('setupPort');
  if (tls && port) {
    tls.addEventListener('change', () => {
      const next = flipPortForTls(port.value, tls.checked);
      // NULL MEANS LEAVE IT — the operator's own port is not overwritten.
      if (next !== null) port.value = next;
    });
  }

  el('setupTestBtn')?.addEventListener('click', () => {
    clearErr();
    setSaveReady(false);
    const res = el('setupTestResult');
    if (res) {
      res.textContent = 'Testing…';
      res.style.color = '';
    }
    const test = el<HTMLButtonElement>('setupTestBtn');
    if (test) test.disabled = true;
    const b = body();
    // CHECKED AFTER the button was disabled and before the request, so the
    // early return has to re-enable it. The live code does the same.
    if (!b.host) {
      showErr('Host is required');
      if (test) test.disabled = false;
      return;
    }
    void fetch('/api/routers/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(b),
    })
      .then((r) => r.json())
      .then((d) => {
        if (test) test.disabled = false;
        if (res) {
          res.textContent = setupTestResultText(!!d.ok, d.boardName || '', d.error || '');
          res.style.color = d.ok ? 'var(--color-success, #34d399)' : '#f87171';
        }
        setSaveReady(!!d.ok);
      })
      .catch(() => {
        if (test) test.disabled = false;
        if (res) {
          res.textContent = '✗ Request failed — check browser console';
          res.style.color = '#f87171';
        }
        setSaveReady(false);
      });
  });

  el('setupSaveBtn')?.addEventListener('click', () => {
    // BELT AND SUSPENDERS, and the live comment calls it that. The button is
    // already disabled; this guards the path where something else invoked it.
    if (!testPassed) return;
    clearErr();
    const b = body();
    if (!b.host) {
      showErr('Host is required');
      return;
    }
    setBusy(true);
    // TWO REQUESTS, and the second depends on the first's id. Adding a router
    // does not select it — a first-run install with a router nobody activated
    // would show an empty dashboard and no way to understand why.
    void fetch('/api/routers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(b),
    })
      .then((r) => r.json())
      .then((d) => {
        if (!d.ok) throw new Error(d.error || 'Failed to add router');
        const id = d.router && d.router.id;
        if (!id) throw new Error('No router ID returned');
        return fetch('/api/routers/' + encodeURIComponent(id) + '/activate', {
          method: 'POST', credentials: 'same-origin',
        }).then((r) => r.json());
      })
      .then((d) => {
        // `switching` IS SUCCESS. The server answers that when it has accepted
        // the switch and is still tearing the old session down, and treating it
        // as a failure would show an error over a activation that worked.
        if (!d.ok && !d.switching) throw new Error(d.error || 'Failed to activate router');
        hideOverlay();
        setBusy(false);
      })
      .catch((e) => {
        showErr((e && e.message) || 'Unexpected error');
        setBusy(false);
      });
  });

  // Locked on mount, before anything can be typed.
  setSaveReady(false);
}
