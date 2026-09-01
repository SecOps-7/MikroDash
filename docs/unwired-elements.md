# Element ids the app renders but does not wire

This is the annotated record from the wiring audit, kept when the
port-parity harness was retired on 2026-09-01.

**The audit itself could not survive.** It answered "which ids does the old
implementation touch that this one does not", and it computed that by reading the
old source directly, with no frozen fallback. With that source gone the question
has no answer and the mechanism has nothing to run against.

**The reasoning is a different matter.** These notes were written by someone
reading both codebases side by side, and each says *why* a particular unwired
element is acceptable rather than merely that it is. Nothing else records that,
and the entries below are the only surviving statement of which parts of the
interface are deliberately inert.

**Read it as a record, not as a list of current state.** Several entries describe
their own closure, because the audit failed in both directions and an entry that
stopped being true had to be deleted rather than left standing. That habit is
worth keeping wherever a ledger replaces it — `internal/verify/` and `web/test/`
both do it.

The mechanism is in git history at `v0.8.2`.

---

```js
const EXPECTED = {
  // ── THE FLEET MAP'S IMPERATIVE HALF — CLOSED 2026-08-29 ───────────────────
  //
  // `rtrMapViewport`, `rtrMapPop` and `rtrMapAutoFrame` were here while
  // `app.js:11432-11941` (510 lines, brace to brace) had no port. The PURE half
  // had been done for some time — `project`, `layout`, `popHtml`, `groupPopHtml`,
  // `renderTray`, `clampTranslate` and `fitToMarkers` in `routers.ts` — and what
  // was missing was the SVG construction and the gestures, which is why exactly
  // three ids remained: the viewport it draws into, the popover it positions and
  // the Auto Frame button it toggles.
  //
  // `web/src/pages/routers-map.ts` is that half. The entry went the way this
  // audit is built to make them go: the port started touching the ids and the
  // record failed for describing a state the code was no longer in.
  //
  // The recorded DIVERGENCE went with it — `applyView` no longer rewrites a
  // stored 'map' to 'comfortable', and `renderRoutersStats` no longer throws on
  // it. Both existed only while the view could not be drawn.
  // The six `wanWarn*` ids were recorded here while the WAN page was read-only.
  // They were DELETED on 2026-08-24 when renew/release and the self-cutoff
  // dialog landed — and this audit is what said so: the port started touching
  // them and the record failed in the direction that catches an entry outliving
  // its problem. That is the half of a ledger that is easy to leave out and the
  // only half that keeps one honest.

  // NOT the "Firewall Analyser" — an earlier version of this record said that and
  // it was simply wrong. `fa` is the FREQUENCY Analyser: a WiFi channel scan that
  // takes the chosen radio off the air and drops every client on it. It is
  // server-side-first (`src/wifiScan.js`, 335 lines, no Go equivalent), so the
  // button cannot ship before the thing it triggers.
  // `faOpenBtn` was recorded here for the same reason and went with it.

  // ── shell.html ────────────────────────────────────────────────────────────

  // rtrmodal (32)
  
  
  
  
  
  
  
  
  
   

  // upd_notes (1) — arrived 2026-08-27 with upstream `5531371`, and CLOSED
  // 2026-08-28 when `packages:notes` was ported end to end. Its entry deferred
  // to "the upgrade dialog group", which had been closed three days earlier —
  // three ledgers pointing at each other, none of them checking. This audit
  // failed on the entry the moment the port touched the id, which is the half
  // that keeps a record honest.
  // fa (15)
  // Fourteen `fa*` ids were recorded here as the Frequency Analyser's gap. The
  // dialog shipped on 2026-08-26 and this audit refused every entry as it became
  // untrue, one sweep at a time.
  //
  // ONE REMAINS, and it is a real gap rather than a leftover:
  // `faSpectrum` WAS here — "the spectrum CANVAS: constructing the chart against
  // the element and registering the band plugin, which needs a browser and
  // Chart.js rather than a decision". Closed 2026-08-29 by
  // `web/src/pages/wireless-fa-chart.ts`, which is exactly that and nothing
  // more: every decision it uses was already ported and gated.
  //
  // Writing it surfaced TWO CALLBACKS THAT HAD NEVER EXISTED. `spectrumConfig`
  // declares `legendLabels` and `legendClick` as dependencies, and
  // the fa-chart check supplies its own stubs for them — correct for what
  // that gate asks, and it meant the real implementations were never written and
  // never missed. They are `faLegendLabels` and `faLegendClick` now, gated by
  // the fa-legend check.
  //
  // IT NEVER STARTS A SCAN. This item carried the warning that a spectral scan
  // takes a radio off the air and drops every client on it; that is the scan
  // button's request, not the canvas's.

  // setup — WAS 14, then 8, now NONE. `web/src/pages/setup-overlay-wire.ts`
  // (2026-08-29) binds the overlay, both buttons, the error and result lines and
  // the three non-re-locking fields, so every id this record held is touched.
  //
  // MENTIONED IS NOT REACHABLE, and that split is recorded where it belongs:
  // this audit answers "does the port touch this element", and
  // `reachable-audit` answers "is the module in the bundle". The wire module is
  // recorded THERE as deliberately inert, because
  // `POST /api/routers/:id/activate` is not ported — measured, 404 — and
  // mounting a Connect button whose second request cannot succeed would leave a
  // first-run install with a router added and not selected.
  //
  // The same split is already written down for `pages/router-modal`, whose 32
  // ids read as bound while nobody could open it.

  // The twenty-one `un*` ids were recorded here as the per-user notification
  // gap. The tab shipped on 2026-08-26 — the transports, the storage, the three
  // routes and then the page — and this audit refused each entry as it stopped
  // being true.


  // notifpanel (5)

};
```
