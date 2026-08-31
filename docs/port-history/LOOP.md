# LOOP.md — what is left, in the order it should be done

**This is the file the autonomous loop reads at the top of every tick.** It exists
because `PORT-QUEUE.md` is a very long *record* of decisions and their evidence,
which is the right shape for "why was this done that way" and the wrong shape for
"what is next". This is the short list. When an item is finished it is struck
through here with one line saying what closed it, and the reasoning goes to
`PORT-QUEUE.md` and `Changes.md`.

> **The counts and the "what exists" lines below were MEASURED on 2026-08-28**, not
> estimated — every one names the file it came from so the next tick can re-check
> it rather than trust it. This project has been bitten repeatedly by a summary
> that stayed plausible after it stopped being true.

---

## 0. ~~The Devices page never refreshed~~ — CLOSED 2026-08-28

`devicesFocus` sent `routers:stats` once and stopped; the live app pushes it every
2000 ms. Fixed with a per-socket ticker, 6 mutations killed, confirmed in the
browser (three devices, three online). Kept here rather than deleted because of
**how** it was found: by driving the running app, not by any gate.

## 0b. ~~Dashboard cards with no data after a restart~~ — CLOSED 2026-08-29

The operator's report ("some of the cards on the dashboard dont have any data …
usually after a container restart"). THREE separate causes, all found by driving
the running app rather than by any gate:

1. `dashcard:focus` arriving BEFORE `router:select` was dropped, so a card that
   reported itself during boot joined nothing. Fixed by recording the key
   regardless and replaying it in `rejoinCards()` (`internal/server/dashcard.go`).
2. Two cards joined a room NOTHING emits to. `CARD_ROOMS` is lifted verbatim from
   live's `dashboard-grid.js` and two of its keys do not name their own room —
   `dc-card-physports` uses `interfaces`, `card-network` uses `dhcp` — while the
   collectors emit to `dash-card-physports` and `dash-card-network`. Live has the
   identical mismatch and survives it because its session is long-lived and
   `sendInitialState` replays; this port creates the session on `router:select`,
   so the replay races the first tick and the ten-minute collector loses.
   Fixed by aliasing the JOIN (`dashCardRooms`), which leaves every emitted room
   exactly live's so `emit-rooms-audit` still passes. Pinned by
   `TestEveryCardRoomIsEmittedTo` — it reads the card keys out of the GENERATED
   `web/src/gen/grid-tables.ts` and the emit sites out of `internal/collect/`, so
   a new card or a renamed room fails rather than going quiet.
3. NOT A DEFECT, and it cost most of a tick: three of the readings blaming the
   port were measured through a browser I had broken myself. A discarded probe
   had wrapped `window.WebSocket` via `addInitScript`, which persists for the
   whole Playwright CONTEXT — and the wrapper had no `.OPEN`, so the port's
   `emit` guard compared `undefined === undefined`, called `send` on a null
   socket, and killed `main()` before the socket ever connected. Every card was
   empty and the server log showed the login with no `[ws]` line after it.
   **A browser probe is state that outlives the page. Check `String(WebSocket)`
   is native before believing a screen-scrape, and close the context after any
   probe that patches a global.**

---

## 0c. ~~The Frequency Analyser was UNREACHABLE~~ — CLOSED 2026-08-29

**The heading said "ONE HALF OPEN" until 2026-08-30 while all three numbered
defects below it were struck through.** It was written when one was open and
never re-read after the third closed — the stale-heading shape this file keeps
finding in `CLAUDE.md`. Nothing here is outstanding.

**THE FIRST OPEN PORTING ITEM SINCE THIS FILE SAID THERE WERE NONE.** Found on
2026-08-29 by driving both apps and diffing the visible buttons: live's Wireless
page carries `◎ Frequency Analyzer` and the port's did not. Section 4 records the
spectrum canvas as DONE and it is — every renderer passes `fa-dialog-check`
against the live originals. The feature was complete and had no way in.

**Two independent defects, in series. Both had to be fixed to see anything:**

1. ~~**The port never asked on page entry.**~~ **CLOSED.** `faOpenBtn` ships
   `style="display:none"` and is unhidden by exactly one line — the
   `wifiscan:interfaces` reply handler. This port emitted that request from
   `open()`, which runs when the modal opens; the modal opens from the button.
   The button waited for an answer only a click on the button could ask for.
   Live asks in three places and the port had one. Fixed by adding live's
   `mikrodash:pagechange` listener to `wireless-fa.ts`, and pinned by the new
   `tools/pagechange-audit.js` (below).

2. ~~**`master` was missing from the `/interface/wifi/print` proplist.**~~
   **CLOSED.** `wifiscan.ParseCatalogue` reads `master` and
   `ScannableInterfaces` drops every row where it is false, so the catalogue
   came back as radios NONE of which was a radio — zero interfaces, with
   `permitted: true` in the payload, so it did not even look like a permission
   problem. It survived because the proplist was copied from
   `src/routeros/wifiMenus.js` (correctly — that keeps the SHARED ones from
   drifting) while the catalogue it feeds is live's
   `src/collectors/wireless.js`, which calls the same path with NO proplist and
   therefore gets every property. Two correct sources; one field only the second
   needs. Fixed in `internal/collect/wifi.go:35`. Verified on the hAP AX3: four
   radios with real client counts (17, 2, 7, 2).

3. ~~**The catalogue is sourced from the WRONG COLLECTOR.**~~ **CLOSED 2026-08-29.**
   Moved to `internal/collect/wireless.go`, which is the collector the page runs
   and where live keeps it; `Wifi`'s copy is GONE rather than left as a second
   one, and `master` came back out of the wifi proplist with it since nothing
   there reads it. 4 mutations, 4 killed. Verified end to end: straight to the
   Wireless page with no `wifi` visit, the button is drawn and offers all four
   radios. **And moving it exposed a third defect** — `wifiscan.WifiEndpoint`
   was `/interface/wifi/registration-table/print`, lifted by
   `tools/wifiscan-catalogue-cases.js` from `WL_ENDPOINTS.wifi` because its
   anchor was a bare `wifi:` and that object sits twelve lines above
   `SSID_ENDPOINTS.wifi`. Invisible while the only caller passed the constant to
   itself — the guard compared it with itself and would have accepted anything.
   Anchor and constant both corrected, corpus regenerated.

   THE ORIGINAL TEXT, kept because the reasoning is what made the fix cheap: With both
   fixes in, the button appears **only after the `wifi` page has been visited**.
   `scannableInterfaces()` reads `rsession.Wifi().ScanCatalogue()`, and the
   Wifi collector is resumed by `case "wifi"` in `ws.go`'s focus switch — not by
   `case "wireless"`, which resumes the WIRELESS collector. So on a fresh
   session that goes straight to the page the button lives on, the catalogue is
   empty and the button is hidden. MEASURED both ways on 2026-08-29.

   **Live does not have this problem because it puts the catalogue in the
   collector the page runs**: `listScannableInterfaces` is
   `src/collectors/wireless.js:391`, not `wifi.js`.

   **THE FIX COSTS NO EXTRA ROUTER CHANNELS**, which is why it is the right one
   rather than "resume Wifi on wireless focus too": `internal/collect/wireless.go:32`
   ALREADY issues `/interface/wifi/print` with no proplist, so it holds the exact
   rows `ParseCatalogue` wants. Build the catalogue there, as live does, and have
   `scannableInterfaces()` read it from `Wireless()`. Do NOT leave two
   catalogues — one rule with two copies is the shape `a4ac96e` was.

---

## 0d. ~~"Top Connections N" did nothing~~ — CLOSED 2026-08-29

**The operator's report**, and the second defect this week found by using the app
rather than by any gate: "the amount of items shown on the Connections card on
the dashboard does not honour the Top Connections N value in Limits under
settings."

It did not, twice over:

- `internal/collect/connections.go` hardcoded `topN: 10` with NO writer anywhere
  in the tree — the setting was read by the settings page, validated, persisted,
  and then never consulted.
- **10 was not even the live default, which is 5** (`src/settings.js:126`). So
  the card had been showing the wrong number of rows on a default install too,
  and nothing compared them because both sides of every gate used the port's own
  value.

Its sibling was one line away and the same shape: `NewTalkers(..., 0)`, with a
comment saying the port "has no settings write for yet — see the settings-write
cutover item". That stopped being true on 2026-08-28 when item 1 shipped one.
**Nothing fails when a premise expires**, which is this project's recurring
lesson and is why the operator found it and the suite did not.

Both now read `topSetting(cfgSettings, …)`, which prefers the file and falls back
to the GENERATED default rather than a number typed into the port. Applied at
construction, NOT live-patched — live patches `talkers.topN` and `conns.maxConns`
four lines apart and deliberately does not patch this one, and reproducing that
asymmetry is the port's contract.

Pinned three ways because the value test and the wiring test are separate claims:
`TestTopSettingPrefersTheFileThenTheGeneratedDefault`,
`TestBothCountsAreActuallyWiredIn` (reads the construction site out of
`session.go` — every unit test passes with both collectors still hardcoded), and
`TestTheConnectionsTopNDefaultMatchesLive`. 3 mutations, 3 killed — the third
only after the default was pinned where it is stated, since `topSetting` always
overrides it and the mutation had survived everything else.

VERIFIED END TO END through the app's own API: `POST /api/settings {topN: 12}`
→ restart → the card renders 12 sources and 12 destinations. Set back to 5.

---

## 0e. ~~Nothing ever pruned the database~~ — CLOSED 2026-08-29

**Found by auditing the CLASS the operator's two reports came from**, rather than
waiting for a third one. Both of those were a setting rendered, validated,
persisted and never consumed; so every settings key was counted out of the
generated `internal/store/settings_tables.json` and checked for a reader.

113 keys, 6 with no reader, and one of the six mattered: **`dbRetentionDays`,
`dbAlertRetentionDays` and `dbAuditRetentionDays` were read by nobody, because
this port had no retention sweep at all.** Live runs `startPruneInterval` daily
(`src/db.js:1246`); the port ran nothing, so the Settings page offered a
retention policy that could not take effect and the database grew without bound.
`internal/db/purge.go` is NOT this — it is the operator-triggered cleanup card,
by router, on demand, and it deliberately excludes `audit_events`.

**Ported:** `internal/db/prune.go` (six DELETEs, three cutoffs),
`internal/server/prune_scheduler.go` (immediately, then daily), started from
`server.go` on `standalone`.

**IT TAKES A FLAG AFTER ALL — `-retention`, corrected 2026-08-29.** It was gated
on `standalone` alone, on the argument that unlike its three siblings it does not
act on the fleet. That is right for a cutover deployment and WRONG for the
routine activity this repo does constantly: `tools/live-diff.sh` stands a Go
server up against the LIVE `/data`, and `standalone` means nothing more than "no
`-node` was passed". A dry run with the flag omitted would have pruned the
production database unattended — and this is the ONE switch of the four that
DELETES, so a default-on mistake there cannot be undone.

Nothing was lost: that script passes `-node`, so it was never standalone. But the
safety came from an unrelated default rather than from a decision, which is the
distinction worth acting on. Retention now joins the other three: a flag, off by
default, in the cutover checklist.

**Two traps, both lifted rather than retyped** (`tools/db-prune-cases.js`):
`alert_events` keys on `fired_at` where the other five use `ts`, and
`connectivity_events` ages on the ALERT retention despite its column being `ts` —
aging it with the metrics would throw away a year of outage history under a
90-day metric policy. A third: `x || 90` means a missing OR ZERO setting takes
the default, so an unwritten settings file keeps 90 days rather than deleting
everything. On a delete path that inversion is unrecoverable.

**6 mutations, 6 killed**, including all three above and "nothing starts the
sweep" — which is the shape the whole item is about, and which every unit test
of `Prune` passes in.

**A correction worth keeping.** On first deploy the sweep reported nothing
deleted while the database held rows 95.9 days old, and this was written up as a
failure. It was not: `dbRetentionDays` is **365** in this install, so those rows
are well inside the policy and retaining them is correct. The mistake was
checking the result against the DEFAULT rather than against the configured value
— the same class of error as reading a stale premise, arrived at from the other
direction.

---

## 0f. ~~The settings-consumption class, audited~~ — NOTHING ACTIONABLE LEFT

**The heading said "one gap left OPEN" until 2026-08-30.** The gap is
`firewallTopN`, and it is not work: it is DEAD IN THE LIVE APP TOO, the port is
correct to ignore it, and it has been reported to `../MikroDash/ToDo.md` to be
fixed at the source. Wiring it here would make the ported page behave differently
from the app it replaces. A recorded finding, not an open item.

Three defects in two days shared one shape: a setting rendered, validated,
persisted, and read by nobody. Two were the operator's reports (`topN`,
`topTalkersN`); the third was the retention sweep. `tools/settings-consumer-audit.js`
now checks the class on every sweep: 113 keys, 8 with no reader, all 8 recorded
with a reason.

**MUTATION-MEASURED, AND ONE MUTANT SURVIVED.** Reverting the `topN` wiring
leaves this audit GREEN, because `topN` is still named in `collection.go`'s
settings-fingerprint key list — a place that reacts to the value CHANGING without
applying it. Excluding the clamp-bounds table removed one such false reader; the
fingerprint list is real code doing real work and no honest heuristic separates
"names the key" from "applies the value". **So this audit would NOT have caught
the operator's own report.** What catches that shape is a call-site test
(`TestBothCountsAreActuallyWiredIn`); the audit is the cheaper net beneath it.
Recorded here because a check believed to be stronger than it is, is worse than
no check.

**Excluding the bounds table surfaced three more keys, with three different
answers:**

- **`routerPort`** — legacy single-router seed, like `routerHost`. Recorded.
- **`firewallTopN` — DEAD IN THE LIVE APP TOO.** Its only six occurrences
  upstream are its default, its env var, VIEWER_FIELDS, its clamp and the two
  settings-form key lists. No collector, no renderer. The Firewall page's Top N
  box has never done anything on either side, so the port is CORRECT to ignore
  it — wiring it up here would make the ported page behave differently from the
  app it replaces. **Reported to `../MikroDash/ToDo.md`** so it can be fixed at
  the source. (`vpnDashTopN` is NOT the same: consumed at `app.js:2096`.)
- ~~**`rosDebug`**~~ **CLOSED 2026-08-29 — see 0h below.**

## 0g. ~~Two one-shot migrations the port does not have~~ — CLOSED 2026-08-30

**The operator chose "port both" on 2026-08-30. Both are done.**

- **The legacy single-router seed** — `internal/store/legacyseed.go`, called from
  `Store.Open`, pinned by `tools/router-seed-cases.js` (7 cases, RUN against the
  live `loadAll`). 6 mutations killed.
- **The #105 collection migration** — `internal/collection/migrate.go`
  (`PlanMigration`, pure, pinned by 7 branches lifted from the live
  `planMigration`) plus `internal/store/legacymigrate.go`, called at SERVER
  STARTUP. 7 mutations killed.

**The two live in different seams because live keeps them apart:** the seed is
inside `routers.js` DATA ACCESS, the migration is a startup IIFE in `index.js`.
The first version collapsed both into `Open` and broke three unrelated tests —
`MigrateCollectionMode` SAVES SETTINGS, and live's `save()` writes
`{...load(), ...updates}`, so any save materialises every default into
settings.json. Faithful behaviour, wrong seam.

Verified against the real install: neither ran, which is correct — its
`collectionMigrated` is already true and its routers.json already exists.

THE ORIGINAL ENTRY, kept for the reasoning:

Both are Node code that DISAPPEARS at cutover, so this is a scope decision rather
than a defect:

1. **The legacy single-router seed.** `routers.js:loadAll()` writes routers.json
   from `routerHost`/`routerPort`/`routerUser`/`routerTls`/`routerTlsInsecure`
   when the file does not exist — for an install predating multi-router support.
2. **`collectionMigrated` (#105).** Moves the global stream/poll choice onto each
   router's `collection` block. The live comment: "Without this, anyone running
   global Poll would silently revert to Stream on upgrade."

**MEASURED on this install, and both are already applied:** `collectionMigrated`
is true, routers.json exists, and the migration's result is visible — hAP AC2
carries `{"mode":"poll"}`, which `collection.Resolve` reads. So cutting THIS
install over loses nothing.

**THE QUESTION IS WHETHER THE MERGED APP MUST UPGRADE OLDER INSTALLS.** If it
only ever has to take over this one, both migrations can be dropped and the five
settings keys deleted. If it ships as the public app, an install that skipped
several versions would lose its router list or silently switch from Poll to
Stream. Not decided unilaterally, because deleting a migration is unrecoverable
for whoever needed it.

---

## 0h. ~~"RouterOS debug" did nothing~~ — CLOSED 2026-08-29

The last unread setting with a real consumer upstream, found by
`settings-consumer-audit`. `internal/routeros/client.go` gains `Config.Debug`
and `Config.Label`; `go-routeros/v3` has `SetLogHandler(slog.Handler)` and logs
every sentence and tag at Debug level (`run.go:33`, `listen.go:71`), which is
what live's `debug: true` turns on in node-routeros.

**ONE DIAL SITE, because live has five and traces one.** `src/index.js:444` is
the page-serving session; the alert sessions, the overview sessions, the second
index site and the connection test are all untraced there. So the two pools here
are untraced too — tracing them would produce output the live app never
produces, continuously, from routers nobody is looking at. Pinned by
`TestOnlyOneDialSiteEnablesTracing`, which scans the whole tree rather than one
file: the first version read `session.go` alone and would have passed with
either pool quietly enabling it.

**VERIFIED END TO END:** `POST /api/settings {rosDebug:true}` → restart → 302
`level=DEBUG` lines, every one attributed `router="Mikrotik hAP AX3"` and none
from the two pooled routers. Set back to false.

**What was NOT ported, and why.** Live also gates three collector diagnostics on
this flag — a partial-result warning in `connections.js:147`, mode-latch lines in
`wireless.js:550`, and a BGP-stream-unavailable warning in `routing.js:523`. The
port has none of those lines because it has none of those situations: its
connections collector is POLL-only (0 stream calls, one `!done`-terminated `Do`),
so there is no truncated batch to warn about — the same structural argument
already accepted for `272eeca`. Inventing log lines so a flag has something to
gate would be backwards.

**Three things this closed that were nearly reported as defects, and were not:**

1. **The port polls where live streams — DELIBERATE AND RECORDED.**
   `PORT-QUEUE.md` iteration 7: "three permanent channels traded for one
   transient read is exactly the departure CLAUDE.md's efficiency rule licenses.
   Nothing user-visible moves." 21 collectors are switchable upstream and this
   port polls 20 of them. Checked before writing it up as a finding.
2. **`eff.Stream` is computed and reaches only the UI payload.** No collector
   consumes it. That looked like the same "displayed but not applied" shape —
   but `main.ts`'s `collection:config` handler reads only `enabled`, so nothing
   ever displays a mode the port does not use.
3. **`firewallTopN` is dead upstream too**, so the port ignoring it is correct.

---

## 0i. ~~History is only recorded while a browser is open~~ — CLOSED 2026-08-30

**VERIFIED END TO END: 60 traffic rows/hour with no browser open, which is
exactly live's rate.** Built on `internal/alertpool` — the always-on pool — with
`internal/routers.Pool` keeping its own copy for the window where the Devices
page is what is open. 6 mutations killed on the alertpool half, 5 on the routers
half.

The account below is kept in full, wrong turns included, because this took three
framings and two wrong homes to get right and the sequence is the useful part.

**The operator chose "always on with `-history`" on 2026-08-30** — no new flag,
because `-history` meaning "incomplete history" was the real defect.

**BUILT AND PINNED (5 mutations): the machinery.** `routers.Pool` gained
`WithHistory`/`SetHistoryRouter`, a `Traffic`+`Ping` pair built per session and
started for ONE router, `Conn` gained `Stream`, and the target follows an
activation. `go test ./...` green.

**AND IT IS ON THE WRONG POOL, MEASURED THE SAME DAY.** After a restart with no
browser, `syncHistoryRouter` ran with the right router id and the collectors were
never started — because `routers.Pool` had no sessions. `syncPool` is called from
the Devices page and the routers API and NOWHERE ELSE, so that pool idles until
somebody looks at something.

**The three established sockets this process holds are `internal/alertpool`'s.**
That is the always-on pool: `server.go:345` builds it and `:351` syncs it at
startup, unconditionally when standalone. An earlier entry above claimed "the
pool already holds a connection to the active router" — the FACT was right and
the attribution was wrong, and only a restart with nothing open separated them.

**THE REMAINING STEP, and it is smaller than what is already done:** the
alertpool already runs `Ping` per router with the record's own `PingTarget`
(`internal/alertpool/collectors.go:87`), so ping history costs NOTHING there —
its payloads are already produced and only need a second consumer. Traffic is one
added collector, for the active router only, on a socket already held.

`routers.Pool`'s support stays: it is correct, tested, and records while the
Devices page is open. It is simply not the always-on half.

**MEASURED 2026-08-29, both databases, same six hours, WAL included:**

| hour | LIVE traffic rows | PORT traffic rows |
|---|---|---|
| -0h | 60 | 5 |
| -1h | 60 | 20 |
| -2h | 60 | 38 |
| -3h | 60 | 35 |
| -4h | 60 | 44 |
| -5h | 60 | 16 |

Live writes one row per minute, steadily, with nobody logged in. The port's count
tracks BROWSER ACTIVITY — 44 during a heavy verification hour, 5 in the hour after
a restart when nothing was open. `ping_samples` and `bandwidth_usage` track
`traffic_samples` exactly, which is the tell: all three reach the database
through one seam.

**THE MECHANISM, on both sides:**

- This port records history in the SESSION's emit closure
  (`session.go`, `m.history.Record(...)`), and a `Session` exists only while a
  socket has selected that router. No browser, no session, no history.
- `internal/routers/pool.go` cannot fill the gap as it stands. It runs THREE
  collectors — `system`, `ifStatus`, `dhcpLeases` — every one of them emitting to
  `nowhere`, the null sink. It never collects traffic or ping at all.
- Live's pool is different in kind: `ensureRouterSession` calls `buildSession`,
  the SAME builder the page path uses, and `buildSession` wires
  `onSample: dbWriter.recordTraffic` (`src/index.js:480`). A pooled router there
  is a full session that records.

**WHY IT MATTERS AT CUTOVER.** History is what the Reports page draws. After
cutover the port owns the database, and its charts would have holes for every
period nobody happened to be logged in — which, for a monitoring dashboard, is
most of the time. It is invisible today because Node is still recording into its
own `/data` beside us.

**SCOPE CORRECTED 2026-08-30 — IT IS ONE ROUTER, NOT THE FLEET.** The entry above
framed this as a fleet-wide cost, and that was wrong. MEASURED on both databases,
per router, over the same six hours:

| table | LIVE | PORT |
|---|---|---|
| traffic_samples | 359, **hAP AX3 only** | 158, **hAP AX3 only** |
| ping_samples | 99, hAP AX3 only | 157, hAP AX3 only |

**Neither app records history for any router but the ACTIVE one.** The other two
are pooled on both sides and neither writes a history row for them. So the two
apps already agree about SCOPE, and disagree only about CONTINUITY: live records
that one router continuously, this port records it while a browser is open.

(Note the port's ping count is HIGHER than live's — 157 against 99. This is not a
uniform shortfall; the two poll ping at different intervals. Only traffic is
behind, 158 against 359.)

**SO THE FIX IS SMALL, AND CHEAPER THAN LIVE'S.** `internal/historywire` consumes
exactly TWO payload types — `*collect.TrafficSample` and `*collect.PingPayload`
(`wire.go:86,94`). Nothing else it receives is recorded. A history-only session
for the ACTIVE router alone, running those two collectors, is **two channels on
one router** — fewer than live pays, and nothing at all on the other two.

That is well inside `CLAUDE.md`'s efficiency rule rather than against it, which
is what the earlier framing got wrong.

**AND THE CONNECTION IS ALREADY OPEN — MEASURED 2026-08-30, so the last cost
objection is gone too.** The question "may this port hold a connection to the
active router with no browser attached" is already answered YES, by the running
system, with the operator's knowledge: `mdtest`'s own socket table, read from
`/proc/1/fd` inside the container with nothing open in a browser, holds

    10.0.0.2:8729   ESTABLISHED     <- hAP AX3, the ACTIVE router
    10.0.0.53:8729  ESTABLISHED
    10.0.0.4:8728   ESTABLISHED

`internal/routers/pool.go` keeps those. `syncPool` excludes only routers that
have a live `Session` (`devices.go:340`), so the active router is POOLED exactly
when no browser is watching it — which is precisely the window where history goes
missing. `Pool.Suspend()` stops the collectors and leaves the sockets up.

**SO THE FIX COSTS NO NEW CONNECTION AT ALL.** It is two extra command channels —
`Traffic` and `Ping` — on a socket that is already established and already paid
for, for ONE router, and only while nobody is watching it. The moment a browser
attaches, `Session` takes that router out of the pool and runs those two
collectors itself. The two halves are complementary rather than additive.

The earlier framings in this entry were both too pessimistic and are left above
deliberately: "a permanent stream per pooled router" (wrong: one router), then "a
new connection for the active router" (wrong: the connection exists). Each was
corrected by measuring rather than by re-reading the code.

**RECOMMENDATION:** teach the pool to run `Traffic` and `Ping` for the ACTIVE
router only, emitting into `historywire` instead of `nowhere`, behind a flag that
defaults to off — the repo's own step-0 doctrine, where what is left for the
cutover window is a flag rather than a file. `internal/historywire` already
consumes exactly those two payload types and nothing else.

NOT BUILT YET, deliberately: the analysis is finished and the build deserves its
own tick with tests and mutation checks rather than the tail end of an
investigation.

---

## 0j. ~~`routeros_update` fires and resolves on every session handover~~ — CLOSED 2026-08-30

**Fixed in `internal/alertwire`: a payload with NO `latest-version` AND NO
`status` has not been checked, and takes a CPU-only path instead of resolving.**
3 mutations killed. VERIFIED: a full browser open/close cycle — the sequence that
produced the pairs — now adds ZERO rows and fires the evaluator ZERO times,
against 8 firings in a few minutes before. The open alert stays open.

**MEASURED 2026-08-30: 50 `routeros_update` rows in 24 hours on the active
router, against ZERO in the live app over the same period.** Every row is a FIRE
("RouterOS 7.24.1 is available (running 7.24)"); the resolves close them rather
than inserting, so each pair is one row.

**A WRONG DIAGNOSIS FIRST, CORRECTED THE SAME DAY.** It looked like the alertpool
and a browser session both feeding one evaluator, because `syncAlertPool` was
never re-run when a session took a router. That WAS a real defect — two
connections and two collector sets on the router anybody was looking at — and it
is fixed. It was not this. The flap continued afterwards, at the same rate.

**THE REAL CAUSE IS NAMED IN THE CODE THAT HAS IT.** `collect/system.go`'s
`checkForUpdates` says:

> Node shares this schedule across every SystemCollector for a router, because it
> builds up to three of them per router — the active session, the overview
> session and the alert session. **This port builds ONE session per router, so
> the schedule lives on the collector. A second session type would need the
> shared map back.**

`internal/alertpool` IS that second session type. Two `System` collectors, two
private `update` rows, two 12-hour windows:

  - the SESSION's has run the check -> `latest-version` present -> available -> FIRE
  - the POOL's has not -> `latest` empty -> `updateVerdict("", "", installed)`
    returns FALSE -> the open alert RESOLVES

So every browser open/close cycle produces a fire/resolve pair. The premise in
that comment expired when the alertpool gained a System collector, and nothing
failed when it did.

**THE FIX, and it mirrors a decision already in the same evaluator.**
`updateVerdict` conflates "not checked yet" with "up to date". `cpuRule` in
`internal/alert` already handles exactly this distinction and says why: "a
reading that is not a number is skipped WITHOUT disturbing the previous verdict.
A port taking a plain float64 would read a missing value as 0, decide the CPU had
recovered, and fire a spurious resolution."

The update verdict needs the same tri-state: unknown must not resolve. The
alternative — the "shared map back" the comment names — also works and is bigger.

NOT IMPLEMENTED YET, deliberately: the diagnosis took most of a tick and the fix
deserves its own with tests and mutations rather than the tail of one.

---

## 0k. ~~No alert notification has ever been sent~~ — CLOSED 2026-08-30

**The operator chose "now" — build it, and drop `-alert-dispatch` from `mdtest`
until cutover.** Both done.

`internal/server/alert_send.go` is the caller: `dispatchFired` takes what
`Evaluate` returns (both call sites discarded it), builds the message once, and
fans it out; `perUserRecipients` is live's `recipientsFor`, asking
`rbac.Can(userID, "router:read", routerID)` AT SEND TIME so a revoked grant stops
delivery on the very next alert; `db.ListUserNotifyConfigs` reads the rows it
walks. `mdtest` now runs without `-alert-dispatch`, so nothing sends while the
live app is still alerting on the same routers.

**THE LAST APPROXIMATION IS CLOSED — 2026-08-30, AND THE CLOSING FOUND A BUG.**
Live's cooldown key is `fire()`'s first argument (`iface:ether1:down`), chosen per
rule and not carried on the fired alert; `cooldownKey` derives one from stored
type, subject and direction. This entry said the derivation was unverifiable
without lifting the real keys, so `tools/alert-eval-cases.js` was taught to
capture them from its `_deliver` stub — **113 cases, 1,095 keys**.

Recording them immediately exposed a defect in code shipped the same day: the
update-supersede path EMITS three alerts and live DELIVERS two, because live's
supersede resolution goes through `_emit` and never reaches the delivery loop.
`alert.Fired.Silent` exists because of that corpus, and `dispatchFired` skips it —
without which the port would have sent an extra "up" notification on every
version change.

**STRING EQUALITY WAS THE WRONG TEST, AND THE FIRST VERSION USED IT.** All 1,091
keys differ as strings (`update:router:down` against `routeros_update::down`), and
every difference is harmless: the key is an internal cooldown bucket nobody sees.
What a cooldown key DOES is decide which alerts share a bucket, so
`TestTheDerivedCooldownKeysPartitionLikeLive` compares the two schemes as
PARTITIONS. **250,042 pairs, zero disagreements** — including netwatch, which this
entry named as the known gap and which the corpus shows is not one. Three
mutations killed (constant key, key without subject, key without direction).

The one case the corpus cannot speak for is two netwatch hosts sharing a NAME:
live separates them by RouterOS `.id`, this does not. That is the entire
remaining difference, and it is narrower than "the key is derived".

**AND THE CALL SITES ARE NOW AUDITED AS A CLASS — 2026-08-30.**
`TestEveryAlertEvaluationReachesASink` walks the tree with an AST and fails on any
`Evaluate` whose `[]Fired` is discarded, counting the class rather than naming the
two sites known today; `TestTheSessionManagersAlertSinkIsAttached` pins the one
thing that audit cannot see — that `New` actually attaches the sink, a nil one
being silently inert by design. Four mutations, four killed, including the rename
that would leave the audit passing over nothing.

Worth stating why the gate is here and not on the callee: **every test that
existed asked `Evaluate` what it returned, and none asked the callers what they
did with it.** That is the whole reason 0k shipped.

**THE TWO TESTS WRITTEN WHEN 0k WAS OPEN BOTH FIRED ON LANDING**, which is what
made the follow-through automatic rather than remembered:
`TestTheDispatchBannerMatchesTheWiring` failed until the startup promise was
restored, and `TestNoServerFieldIsWrittenAndNeverRead` failed until the
`dispatch` exception was deleted.

**MEASURED 2026-08-30.** `Evaluate()` returns `[]Fired` and its return value is
DISCARDED at both call sites (`alertpool_wire.go:94`, `session.go:462`).
`srv.dispatch` is assigned at `server.go:401` and never read. The Dispatcher's
`Deliver` and `Recipients` are called from nowhere in the server.

So a fired alert is recorded and reaches no transport. `-alert-dispatch` has been
on since 2026-08-27 announcing "notifications will be SENT", one line after
`buildAlertWire` printed "rows are written, NOTHING is dispatched". Both appeared
at every startup and the contradiction went unread for days — including by me,
repeatedly, while reading those very logs for other reasons.

**AND THIS ENTRY'S PREDECESSOR WAS WRONGLY CLOSED.** `CLAUDE.md` blocker 5 was
struck through on 2026-08-30 on the grounds that "a real Telegram message was
delivered". One was — from the admin TEST button
(`internal/notify/admintest.go`), which is a different path entirely. The strike
has been restored with the correction attached.

**WHAT IS AND IS NOT DONE.** The transports are complete and pinned. What is
missing is the CALLER — live's `src/alerter.js`, 692 lines and event-driven —
exactly as the original blocker says. The log line now says that instead of
promising the opposite, and `TestTheDispatchBannerMatchesTheWiring` fails if the
claim and the wiring ever disagree again, in either direction.

**THE DECISION, WITH THE WORK MEASURED 2026-08-30.** "692 lines" is misleading
and was my figure. `src/alerter.js` is 692 lines of which **428 are
`createEvaluator`'s RULES**, already ported and gated in `internal/alert/eval.go`,
and `_deliver`/`_recipients` are already `internal/alertdispatch`. Everything the
caller needs exists:

| piece | state |
|---|---|
| `Build(settings, routerName, ts, Fired) Message` | DONE, `alertdispatch` |
| `Recipients(routerID, perUser)` | DONE — `perUser` is injected |
| `Allow` / `Deliver` (cooldown, send, per-recipient error) | DONE |
| `rbac.Resolver.Can(userID, "router:read", routerID)` | DONE |
| `db.UserNotifyConfig(userID)` | DONE |
| `db.ListUserNotifyConfigs()` — all rows | **MISSING, ~15 lines** |
| `perUser(routerID)` adapter — iterate configs, check `Can`, require a channel | **MISSING, ~30 lines** |
| the loop: `for _, f := range fired { … Deliver }` at the two `Evaluate` sites | **MISSING, ~25 lines** |

So roughly **70 lines plus tests**, not a rewrite.

**BUT THERE IS A CONSTRAINT THAT IS NOT ABOUT EFFORT.** The live app is still
running and still alerting on the same three routers. Wiring this caller while
that is true sends every notification TWICE — the original blocker's reasoning,
unchanged, and the one failure that cannot be undone by deleting a row. `mdtest`
currently runs WITH `-alert-dispatch`, so the moment the caller exists it would
start sending.

**So the choice is really:**

1. **Build it now and drop `-alert-dispatch` from `mdtest`** until cutover. The
   code is ready and tested; the flag stays off; nothing sends twice. This is the
   repo's own step-0 doctrine — what is left for the window is a flag.
2. **Build it at cutover**, when Node stops and the duplicate question dies with
   it.
3. **Build it now and leave dispatch ON**, accepting duplicate notifications for
   the coexistence period.

---

---

## 1. The Settings page — MOUNTED 2026-08-29, all seven groups done

`web/src/ui/page-settings.html` is 93 KB and extracted. `web/src/pages/settings.ts`
is 961 lines and its PURE half is done — 21 exported renderers and decision
functions, all gated. What is missing is the wiring and the write endpoints.

Measure the list rather than trusting this one:

```bash
# temporarily add 'settings' to build.mjs PAGES and main.ts PORTED, then:
MIKRODASH_SRC=../MikroDash node tools/wiring-audit.js
```

Do these in order — 1a unblocks the modal that 1f needs, and 1g is trivial once
the card it lives in is mounted.

### 1a. Principal writes — users, groups, roles, grants (28 elements)

`uf_*` (8), `gf_*` (9), `rf_*` (8), `addUserBtn`, `addGroupBtn`, `addRoleBtn`.

**What already exists**, verified against the source on 2026-08-28:

| layer | state |
|---|---|
| `internal/db/groupwrite.go` | `CreateGroup`, `UpdateGroup`, `DeleteGroup`, `SetGroupMembers` |
| `internal/db/rolewrite.go` | `CreateRole`, `UpdateRole`, `DeleteRole`, `SetRolePages` |
| `internal/db/grantwrite.go` | `UpsertGrant`, `DeleteGrant`, `DeleteGrantsForPrincipal` |
| `internal/db/globaladmin.go` | `WouldOrphanGlobalAdmin` — the check that stops an install locking itself out |
| `internal/store/users_create.go` | `CreateUser`, `UserCount` |
| `internal/store/users_write.go` | `SetPassword` |
| `internal/server/principals_api.go` | the four READS only (`GET /api/{groups,roles,grants,users}`) |

**Done 2026-08-28:** `store.UpdateUser` and `store.DeleteUser`
(`internal/store/users_update.go`), pinned by `tools/userwrite-cases.js` — which
RUNS the live `updateUser`/`deleteUser` against a throwaway DATA_DIR — and
`users_update_test.go`. 22 update cases, 3 delete cases, **13 mutations, 13
killed.** Two of those kills needed a test the corpus could not supply; see the
note below.

**Also done 2026-08-28:** the three USER routes — `POST/PUT/DELETE /api/users`
(`internal/server/users_write_api.go`), with `db.DeleteLayouts`,
`db.DeleteGrantsForPrincipalTx` and `db.UpsertGrantTx` added underneath.
Registered in standalone only.

**Also done:** `principals.ParseRolePages` (`internal/principals/rolepages.go`),
pinned by `tools/rolepages-cases.js` — 28 cases, 13 mutations, 13 killed. The
role routes need it before they can be written.

**Also done:** the three GROUP routes — `POST/PUT/DELETE /api/groups`
(`internal/server/groups_write_api.go`), with `db.DeleteGroupTx` underneath.

**Also done:** the three ROLE routes — `POST/PUT/DELETE /api/roles`
(`internal/server/roles_write_api.go`), with `_roleView` extracted from
`rolesGet` so the read and the three writes share one implementation.

**Also done:** the two GRANT routes — `POST/DELETE /api/grants`
(`internal/server/grants_write_api.go`), with `db.DeleteGrantTx` underneath.

**ALL ELEVEN PRINCIPAL WRITE ROUTES ARE NOW SERVED**, and the three forms'
DECISIONS are ported and pinned — `web/src/pages/principal-forms.ts`, checked
against the live `saveUser`/`saveGroup`/`saveRole` by
`tools/principal-forms-check.js` (34 scenarios, 25 mutations, 25 killed).

**ITEM 1a IS COMPLETE as of 2026-08-28.** The eleven write routes are served,
the three forms are wired, and the grant editor's add and remove are bound.
`tools/principal-forms-check.js` compares all of it against the live originals —
62 scenarios — and `tools/principals-wiring-check.js` drives the forms through a
stub DOM. Every Access Management ledger entry in `attr-audit` is closed.

**A harness rule learned the hard way, 2026-08-28:** fixture DDL must be applied
BEFORE `db.Open`, which is what `usersServer`'s `extraDDL` parameter is for. An
index created afterwards is present in `sqlite_master` and still invisible to a
connection the pool had already established — it resolves `ON CONFLICT` against
its cached schema and reports the index as missing. Nondeterministic by
connection, so it works through an HTTP handler and fails when a test calls the
writer directly.

**The roles routes need two things the others did not:** `ParseRolePages` (done)
and the `builtin` refusals — the Administrator role cannot be edited or deleted,
because "its reach is structural" and editing it would either do nothing or
silently narrow every admin in the fleet. Delete also refuses with a COUNT when
grants still reference the role, rather than surfacing the foreign-key error.

**The grants route validates five things in order** and each has its own status:
principal type (400), role (400, and `roleId` OR the legacy `role` name), scope
type (400), a required scope id (400), then EXISTENCE of the site, router or
group (404). A grant naming something that does not exist "would sit in the table
forever, conferring nothing, and read as working in the UI".

**Carry these into the group/role/grant routes**, all four learned the hard way
on the user ones:

- **`POST` and `PUT` do not use the same guards.** On users they are forty lines
  apart and differ: `if (role && ...)` — truthy — against
  `updates.role !== undefined`. Read both, do not assume.
- **A falsy check comes BEFORE a length or pattern check.** `bodyString(nil)` is
  the four characters "null", which passes a length test and matches the username
  pattern.
- **A test fixture's `roles` table must be keyed by role ID, not role NAME.** The
  projection writes ids (`admin`→`administrator`, `viewer`→`readonly`); a
  name-keyed fixture fails the foreign key on every projection, which is a logged
  error and a 200 — so the grants never appear and the test reads as "nothing was
  projected".
- **`globalAdminQuery` counts only `builtin = 1` roles.** A fixture whose roles
  are all custom has ZERO administrators, and `WouldOrphanGlobalAdmin` then
  refuses every delete.

**Read `PORT-QUEUE.md` blocker 6 before starting.** Its premise has MOVED and the
entry has not been rewritten: it says a Go write to `grants` would leave Node
honouring a revoked grant, because Node's `Rbac` memoises `_views` on a generation
counter only its own `bump()` advances. That was true when both apps shared one
`/data`. **This process now runs on its own copy**, so the blocker applies to the
MERGED app rather than to this one. Say so in the entry rather than deleting it.

`users.json` must stay a bare JSON array — that is a security property, not a
formatting preference (`internal/store` package header).

**Three traps the corpus caught, worth carrying into the HTTP layer:**

1. **An EMPTY password means "leave it alone", not "clear it".** The edit form
   renders an empty password box every time it opens, so it submits `""` on any
   save where nobody typed a new one. A port treating `""` as a value hashes the
   empty string and locks the account out of its own password on a rename.
2. **JSON `null` and an ABSENT KEY are different.** `updates.role !== undefined`
   is TRUE for an explicit null, so `{"role": null}` must be a 400 and `{}` must
   be a no-op. A Go struct decoding both to nil passed twenty-one cases and
   failed exactly the one that separates them.
3. **`allowedRouterIds` is ARRAY-guarded, not presence-guarded** — four lines
   from three fields that use the other test. A string, a number or null is
   IGNORED rather than stored or cleared, so it must decode to a nil pointer.
   An explicit `[]` must still get through, or "remove every router" is
   impossible.

**And one the corpus could NOT catch**, found by mutation: the corpus compares
the neighbouring record through `json.Marshal` of a map, which sorts keys — so a
port that decoded the whole file into maps and re-encoded it looked identical.
`TestUpdateUserLeavesTheOtherRecordsByteIdentical` seeds a deliberately unsorted
key order instead. It matters because Go sorts map keys and JavaScript does not:
one rename would rewrite every user's field order and turn a one-field edit into
a maximal diff on the one file nobody wants to have to eyeball.

### 1b. The database cleanup card (11 elements) — **DONE 2026-08-28**

`dbcAge`, `dbcByRouter`, `dbcOldest`, `dbcPreviewBtn`, `dbcPurgeBtn`, `dbcResult`,
`dbcRows`, `dbcScope`, `dbcSize`, `dbcSummary`, `dbcTypes`.

Backend `internal/db/purgestats.go` + `internal/server/db_api.go`; frontend
`web/src/pages/dbcleanup.ts`, wired from `main.ts`. Corpus `tools/purge-cases.js`
(10 predicates, 10 target sets, 24 option sets); gates `tools/dbcleanup-check.js`
(17 driven sessions, 558 DOM operations) and `internal/server/db_api_test.go`.

**Three findings, all measured rather than reasoned:**

- **A purge that deletes nothing must not VACUUM, and neither obvious observable
  can see that.** `bytesBefore == bytesAfter` fails to discriminate — a vacuum of
  an already-compact database returns the size it started with (77824 → 77824,
  measured) — and the file's mtime changes on EVERY purge because the DELETEs run
  either way. A mutant flipping the guard survived both. The property is about
  work NOT done, so it needed `DB.VacuumCountForTest`, named after the two
  `…ForTest` helpers already in that package.
- **`stats.byRouter`'s per-viewer filter cannot currently fire partially.**
  Removing it survives the suite, and not for want of a fixture: the route is
  gated on `isGlobalAdmin`, `system:principals` is GlobalOnly and stripped from
  every projected role, so only a BUILTIN role confers it — and a builtin role
  held globally confers `router:read` on the whole fleet. The filter stays because
  its fail-closed direction IS live (an RBAC error yields an EMPTY set, which
  hides the breakdown). The PREMISE is pinned by
  `TestAGlobalAdminCanAlwaysReadTheWholeFleet` in `internal/rbac`, so the note in
  `db_api.go` goes false the same day the premise does. **This corrects the
  warning below, which assumed the filter was load-bearing today.**
- **The gate itself was weaker than it read, and mutation is what said so.**
  `Object.assign` copies a getter's RESULT, so `dbcScope.value` and `dbcAge.value`
  landed as frozen data properties: four scenarios that read as "change the age,
  then preview" were previewing the default. Two mutants survived on that alone.
  Fixed with `defineProperties` + `getOwnPropertyDescriptors`, plus a select that
  loses its selection when its options are replaced — which is the behaviour
  `renderScope` reads and writes `.value` around.

19 frontend mutants and 25 backend mutants, all killed but one recorded gap: an
`rbac.Can` ERROR inside `purgeOpts` is treated as a refusal, and reaching it needs
a fixture that can break its own database mid-request. Recorded in
`db_api_test.go`, not hidden.

**BUT IT IS NOT REACHABLE IN A BROWSER, and that was found by opening one.** Every
`dbc*` element returns null from `getElementById` on the running app:
`settings` is not in `main.ts`'s `PORTED`, so `page-settings.html` is never placed
in the shell and `initDbCleanup`'s guard returns early — correctly. The code, the
routes and both gates are right; the page holding them is not mounted.

**THE REASON FOR NOT MOUNTING IT HAS EXPIRED.** `PORT-QUEUE.md` argues Settings
must land as one unit because mounting it "would replace a WORKING PROXIED PAGE
with a form the operator can only look at". There is no proxy any more — this app
is standalone. Not mounting Settings now means the app has NO settings page at
all, which is strictly worse than a partially-writable one. That makes item 1h
below the top of the list, ahead of 1c.

---

### 1h. The Settings page is MOUNTED — **DONE 2026-08-29**

`settings` added to `main.ts`'s `PORTED` and `build.mjs`'s PAGES.
`wiring-audit` clean on the first run; `page-mount-audit` now reports **26 of 26
extracted bodies mounted, 0 blocked** — the first time this port has had no
unmounted page.

**Its ledger entry was wrong in three ways, and each is a shape worth naming:**

- **the count.** "54 elements" — measured by mounting it: FIFTEEN. Most of the
  page had been ported between the note being written and being read.
- **"every one a write or a send."** They were the poll sliders, the banner,
  Reset, the routers table, the alert filters and the four Test buttons. Only the
  last four send, and they are operator-initiated one-shots rather than the
  alerter's automatic duplicates blocker 5 is about.
- **"would replace a working PROXIED page."** There is no proxy. That premise
  expired at standalone, and it was the load-bearing half.

**THE MOUNT IS WHAT FOUND THE REAL WORK, exactly as intended.** Three defects,
none visible to any of the 133 gates:

1. **A crash that killed the whole app.** `initSettingsRoutersTable` renders once
   at the end of init, and its `activeId` thunk read `activeRouterId` — declared
   with `let` LATER in `main()`. Temporal dead zone: "Cannot access
   'activeRouterId' before initialization", and `main()` died there. No
   dashboard, no sockets, nothing. A thunk defers a READ, not a reference, and
   the gate supplies its own thunks, which are initialised. Moved below the
   declarations, with a note saying it must stay there.
2. **Five of six tabs were unreachable.** `mountSettingsTabs` was ported and
   never called, so the tab buttons had no listener. `wiring-audit` passed
   throughout because it checks that IDS are bound and the tabs are addressed by
   class (`.stab`).
3. **Every form field was empty.** Nothing called `populateSettings`. Wired as
   the live `loadSettings` — one fetch feeding both the form and the poll card,
   on every visit rather than once, because another admin can change them
   underneath.

**Verified in a browser against the real install:** tabs switch and the actions
bar follows them, fields carry the install's own values (`MikroDash Alert 🛜`,
retention 365, `Europe/Berlin`), 21 slider rows with 2000ms rendering as "2s",
Data Cleanup showing 48.9 MB / 393,728 rows / three routers by name, the delete
button correctly disabled until a preview, and no console errors.

**The settings WRITES are still cutover-gated.** The page mounting and its Save
button being live are two different questions, and only the first has changed.

### 1c. Notification test buttons — **DONE 2026-08-29**

`btn-test-{telegram,smtp,ntfy,pushbullet}` and their four `test-*-result` lines.
Backend `internal/server/test_notif_api.go` + `internal/notify/admintest.go`;
frontend `web/src/pages/settings-notif-test.ts`; corpus
`tools/test-notif-cases.js` (23 cases, 17 changing); gate
`tools/notif-test-check.js` (27 scenarios).

**Nothing was sent.** Every Go test either stops before the transport or reaches
a channel whose `Precondition` fails, and the frontend gate's `fetch` is a fake.

**The merge needed a corpus because it uses TWO GUARDS.** `botToken && {...}`
means a falsy value does NOT override, which is what makes "test without saving"
work; `smtpPort !== undefined && {...}` means an explicitly-sent value overrides
even when falsy. So `smtpSecure: false` really turns TLS off for the test while
`botToken: ""` keeps the stored token — and a port using one guard for all
fourteen fields would be wrong on half of them without failing anything. The
coercions are odd too and now pinned: `0`, `"abc"` and `null` all become port
587, and `"yes"` and `1` are both NOT true.

**A SECURITY FINDING, and it corrected one of this port's own comments.**
`safe.Message` redacts paths, IPv4 addresses, emails and bot tokens — it has NO
hostname rule, and neither does the live `sanitizeErr`. `usernotify_api.go`
claimed the sanitiser closed the internal-probe hole on the per-user test route;
it closes the IP half only. An ordinary account can still enter an internal
NAME, press Test, and learn from the reply whether it resolves. Corrected in
place, asserted as still-present by `TestTheFailureBodyIsSanitised`, and filed in
`../MikroDash/ToDo.md` with a worked example and a patch.

**The gate caught the port being NICER than the live app.** On a reply body of
literal `null` the live code throws (`data.ok` unguarded) and prints the
TypeError as the result line; the port had added a guard showing "✗ failed".
Reproduced faithfully instead, with the reason recorded so it is not re-added on
sight.

**A test of mine was probing the LAN.** The first sanitiser test used
`10.11.12.13` and took ten seconds — because the route did its job and tried to
connect. That is an RFC1918 address which could be a real host on the operator's
network. Now `.invalid`, which never resolves: no packet leaves the machine, and
the error still names the host the assertion needs.

21 frontend mutants and the corpus replay, all killed.

### 1d + 1e. Poll sliders, profiles, banner and reset — **DONE 2026-08-28**

`pollSlidersWrap`, `pollCustomSaveBtn`, `pollCustomSaveStatus`, `settingsBanner`,
`settingsResetBtn`. Ported as ONE module (`web/src/pages/settings-poll.ts`)
because they are one IIFE in the live app and share `fmtMs`, `showBanner` and the
profile state; splitting them would have duplicated all three.

Tables GENERATED by `tools/poll-tables.js` (19 sliders, 5 profiles, 6 offered
buttons), gated by `tools/poll-sliders-check.js` — 21 driven scenarios, 2,290 DOM
operations. Wired from `main.ts`.

**The generator cross-checks the MARKUP, not a pattern**, because all three
drift modes here are silent: a slider missing from the table is simply not drawn
(the interval stays live and uneditable), a key missing from a profile is skipped
rather than defaulted, and a profile button with no entry lights up and writes
nothing at all.

**Three measurements worth keeping:**

- **`cfg.streamed` is dead against today's table** — no row carries it, so driven
  against the real table both sides would skip that branch and agree about code
  neither ran. The gate INJECTS a table with a streamed row so both execute it.
- **The `streamed` guard in `customValues` is equivalent**, provably: a streamed
  row never gets an `s_` input, so the `el()` lookup already returns null.
  Recorded in the code rather than left looking like a missing case.
- **Restoring the custom profile before vs after `detectProfile` is equivalent
  too**, because the detect's FALLBACK is itself `'custom'` — the same answer by
  match in one ordering and by fallback in the other. The live order is kept, and
  the note says what would end the equivalence. An earlier version of that comment
  claimed the reversal would "light the wrong button"; that was wrong and is
  corrected.

26 mutants, 23 killed, 3 recorded equivalent with the reasoning above.

**Two ledgers failed in the correct direction and were closed:** `attr-audit`'s
`data-profile` stopped being unread, and `template-id-audit` gained `s_`/`sv_` as
constructed-id prefixes beside the existing `rtrColl_`.

### 1f. The routers table on the Settings page — **DONE 2026-08-28**

`rtrTbody`, and with it the router modal's ONLY opener.
`web/src/pages/settings-routers.ts`, gated by `tools/settings-routers-check.js`
(23 driven scenarios). Wired from `main.ts`, which also now calls
`initRouterModal` for the first time.

**Two ledgers closed by it, both failing in the right direction:**
`reachable-audit` and `module-reachability-audit` each recorded `router-modal.ts`
and `router-form.ts` as deliberately unreachable, and the modal's entry ended
"Delete this entry the moment Settings can call it." That moment arrived.

**Note the distinction those entries make, which still holds:** reachable means
the module is in the bundle and has a caller. The Settings PAGE still does not
mount (1h), so nobody can click that Edit button yet. Two different audits ask
two different questions and neither answers the other's.

**A REAL GAP was found by `attr-audit`, not by review.** The port rendered
`data-rtr-conn` and read it nowhere. The live app uses it to repaint one badge in
place on `router:status` — so without the reader, a router going offline would
have kept reading "Online" in that table until the next `routers:update`. Ported
as `updateRouterStatusBadge` and wired.

That reader carries a live QUIRK, reproduced rather than fixed: the disabled
badge also carries `data-rtr-conn`, so a status event replaces "Disabled" with
Online/Offline until the next full render. Pinned in the gate as such, and filed
in `../MikroDash/ToDo.md` with a worked example and a suggested fix.

20 mutants, all killed.

### 1g. The alert-filter toggles and their card — **DONE 2026-08-28**

`notifIfaceFilterCard` was the one element `wiring-audit` named, but it is the
tail of a 13-toggle IIFE and none of the behaviour was ported — only the
checkboxes' ids, which the generated settings form map already bound.
`web/src/pages/settings-alert-filters.ts`, gated by `tools/alert-filters-check.js`
(19 driven scenarios, 433 operations).

**A COMMENT BECAME A CHECK.** The live defaults carry this warning: *"These must
match src/settings.js DEFAULTS … drift means the bell can fire for categories the
server has switched off. netwatch, bridge, vlan and other were all true here
against false on the server."* That last sentence records a drift that had
already happened, in four places, and was fixed by hand — a comment cannot stop
it recurring, because nothing fails when the two lists disagree.
`tools/alert-filters-tables.js` now asserts all thirteen against the generated
`internal/store/settings_tables.json`, and checks the map both ways: a toggle
with no default, and a default with no toggle.

**19 mutants, all killed — but three needed scenarios the first suite lacked,
and each was a real gap:**

- **two clicks in flight.** `wanted` is captured before the request precisely so
  a refusal reverts what THAT request sent; every scenario awaited between
  clicks, so reading the box at reply time instead was invisible. Needed a fetch
  the scenario releases by hand.
- **a stored key the defaults do not have.** Loading it was covered; the SAVE is
  what exposes it, because an adopted key gets serialised back into localStorage
  as a field with no control and no way to clear it.
- **the two storage writes are two try blocks, not one.** Browsers throw per
  `setItem`, so the second write survives the first failing. Needed a fake store
  that refuses exactly one key.

---

## 2. The fleet map's SVG half — **DONE 2026-08-29**

`web/src/pages/routers-map.ts` — the backdrop, the markers, pan/zoom, Auto Frame,
the popover and the tray. `wiring-audit`'s three EXPECTED entries closed, and its
known gaps went from 12 to 9. `page-gate-audit` now reports **83 of 83 page
modules gated (100%), 0 ungated**.

**Two recorded DIVERGENCES went with it**: `applyView` no longer rewrites a stored
`'map'` to `'comfortable'`, and `renderRoutersStats` no longer throws on it. Both
existed only while the view could not be drawn.

**The audits found three things review did not:**

- **`resize()` read `window._lastRtrRows`, which THIS PORT NEVER PUBLISHES.** The
  live app writes it; the port keeps the rows module-private. So every zoom left
  the markers at their previous screen size — the one thing the divide-by-scale
  arithmetic exists to prevent — and it would not have looked wrong in a browser,
  because markers at a slightly wrong radius still look like markers.
  `announcement-audit` caught it as a `window.` read with no writer.
- **`module-reachability-audit` could not see `import()`.** The module was in
  `dist/app.js` and the audit said it was "imported by nothing reachable". Fixed
  in the AUDIT, not the module: one that cannot follow a dynamic import
  mis-reports every lazily-loaded module.
- **The dynamic import was wrong anyway, and a GATE said so.**
  `routers-grid-check` passed its 23 checks and then crashed, because the
  import's `.then` fired after the harness had torn down its fake `document`.
  That is a real defect in miniature: the map mounted at an unpredictable time
  relative to the first payload. Now mounted from `main.ts`, which is neither
  cyclic nor deferred.

**The pure piece was split out and gated.** `keepLabels` — which place names to
drop when they overlap — is compared against the live inline block by
`tools/map-labels-check.js`: 13 cases, 19 labels kept and 12 dropped, 11 mutants
all killed. It is the piece a browser cannot check by eye, since a wrong divisor
is invisible at one zoom and wrong at every other.

**Browser-verified against the real install:** 175 country paths drawn, zoom
1 → 1.4 → 1.96 and back, reset to `translate(0px, 0px) scale(1)`, Auto Frame
toggling `is-on`/`aria-pressed` and persisting `"1"`/`"0"`, no console errors.

**ONE THING COULD NOT BE VERIFIED IN A BROWSER, and it is recorded rather than
claimed.** All three of this operator's routers sit on private WAN addresses, so
none geolocates and the map correctly shows ZERO markers with all three in the
"No location" tray. That is the right output for this fleet — but it means the
marker rendering, the cluster count, the ripple stagger and the popover
positioning have not been seen working against real data. They are ported from
the live source line by line and their arithmetic half is gated; the SVG half
awaits a fleet with a routable WAN address, or an operator-set town.

## 3. The alerting engine — 786 lines

`wc -l ../MikroDash/src/alerter.js` — 786. `internal/alert` holds the FORMATTING
only; the evaluator, the cooldown and the dispatch are not ported.

The split, and the reasoning behind it, is in the subsection below — which is the
ORIGINAL text and survived the accident described at the top of this file. This
paragraph deliberately does not restate it: two copies of a rule about an
irreversible action is how one of them goes stale.

### Step 1 of 3 is DONE — 2026-08-29

`internal/db/alertwrite.go` gained `HasOpenAlert`, `InsertAlertEvent` and
`ResolveAlertEvent`: the three calls `alert.Store` needs. Nothing had implemented
that interface, which is why `alert.NewEvaluator` — ported and tested since well
before this — was **constructed by nobody**, the same shape as the router modal
and the background pool before it.

`subject IS ?`, never `= ?`, is the load-bearing detail: `= NULL` matches nothing
in SQL, so a router-wide alert (CPU, router status, RouterOS update — none has a
subject) would read as never-open, file another row every evaluation, and resolve
none of them.

`internal/alertwire` is the adapter and the per-router evaluator map:
`Wire.Evaluate(router, event, payload)` takes a collector payload, runs the
rules, and files or resolves rows. 14 tests, 10 mutants all killed. **Nothing
dispatches.**

**STEP 1 IS COMPLETE — 2026-08-29.** The call is in
`internal/session/session.go`, in the single `emit` closure every collector
receives, which is where the live app puts `alerter.evaluateForRouter` too.
`internal/server/alert_wire.go` builds the wire from settings.json; it is nil
without a history database, and nil is inert.

**Verified end to end against the real fleet, not just in tests.** With the CPU
threshold temporarily set to 1 in the port's OWN `/data` copy (a directory; the
live app uses a separate docker volume), hAP AX3 — the one router with
`alertsEnabled: true` — fired one alert and the database went 4353 → 4354
events. The threshold was restored and confirmed byte-identical.

**Two things that verification taught, both live behaviour rather than defects:**

- **The restart did NOT resolve the alert.** The CPU resolve branch needs
  `prevCPUAlert != nil && *prevCPUAlert`, and a rebuilt evaluator has no memory —
  so a row whose condition quietly went away has no route out except the operator
  clearing it. That is exactly what `resolveAllAlerts` exists for, and exactly
  what the live `hasOpenAlert` comment describes.
- **Clearing it returned `{"count":2}`**, not 1: the port's test database already
  held one open row. Recorded because "I cleared my own artefact" would have been
  the tidier claim and the wrong one.

**A design error caught mid-tick and worth recording.** The first draft of the
wire read payloads as `map[string]any`, on the belief that importing
`internal/collect` would be a cycle. It would not — `collect` imports only
`internal/routeros`. The maps would have made every rule INERT in production
while every test passed, because the collectors emit typed structs and the type
assertion would have failed on all of them. The tests would have been green over
code that could never fire an alert.

**SIX of ten mutants survived the first run, and not one was equivalent.** Each
was a missing test: a nil ping loss read as zero (which RESOLVES an outstanding
alert while the router is still unreachable), an interface an admin disabled
filed as an outage, the running version passed as the latest one, the per-event
instant read per write, an evaluator built for a router with no id, and
`SetSettings` rebuilding rather than replacing.

**AN ACCIDENT, recorded like the LOOP.md one above.** This tick's first act was
to overwrite `internal/db/alertwrite.go`, which already existed and held the
alert ROUTES' three writes. `cat >` through Bash does not check for an existing
file the way the Write tool does. Reconstructed from the surviving
`alertwrite_test.go` and its live corpus, and all three tests pass — but the
recovery worked only because the test file happened not to share the name.

### Step 2 is DONE and the switch is OFF — 2026-08-29

`internal/alertdispatch` holds the message assembly, the cooldown decision, the
delivery and the recipient fan-out. `cmd/mikrodash` gained `-alert-dispatch`,
**default false**. The running app logs which state it is in on every start.

Gated by `tools/alert-dispatch-cases.js`, which runs the live `_deliver` and the
inline message assembly with `notifier.send` stubbed: 13 messages, 7 cooldown
sequences, 10 sends and 6 holds. 14 mutants, all killed.

**A disabled dispatcher does not even stamp the cooldown.** Otherwise turning it
on would find every subject already warm and silently swallow the first alert of
each kind — which is the failure most likely to be blamed on the switch not
working.

**Two corpus cases did not discriminate and were fixed, both found by mutation:**
the alertType-override case used a vars value that happened to EQUAL the label,
so both precedence orders produced the same string; and no case covered a
RESOLUTION with both body templates empty, so a port sending the warning glyph
for a recovery survived every other message case.

### STEP 3 IS THE OPERATOR'S, and it is the only thing left in this item

Turning `-alert-dispatch` on. Everything else is built and tested. **Do not
enable it autonomously** — see below.

### Read this before wiring the dispatch

`PORT-QUEUE.md` blocker 5 is the one blocker on that list whose reasoning did NOT
move when the port went standalone, and it is worth restating rather than
rediscovering:

> Both engines evaluate the same conditions against the same physical routers, and
> the cooldown is an in-memory map rather than a shared row, so neither sees the
> other's sends. **A duplicated Telegram message or email cannot be un-received.**

The operator's instruction on 2026-08-28 was "wire everything in", and that is
their call to make. The shape that honours it without taking the irreversible step
on their behalf:

1. Wire the **evaluator and the database writes** first. Those are idempotent
   within this install's own database, they make the Alerts page and the alert
   counts on Devices real, and nothing leaves the machine.
2. Wire the **dispatch** behind a flag that defaults OFF while the live app is up
   — the same shape as `-no-pool`, and for a stronger reason: a doubled background
   session costs a router channel, a doubled notification costs the operator's
   attention and cannot be taken back.
3. **Ask before turning it on.** Not a blocker on the work — build all of it —
   but the switch is the operator's.

---

## 4. The Frequency Analyser's spectrum canvas — **DONE 2026-08-29**

`web/src/pages/wireless-fa-chart.ts`. Glue only: every decision — the config, the
datasets, the tooltip, the band geometry — was already ported and gated, which is
why this was last and why it is thin.

**IT NEVER STARTS A SCAN.** This item carried the warning that a spectral scan
takes a radio off the air and drops every client on it. That is
`/interface/wireless/spectral-scan`, issued by the scan button; the canvas draws
rows that have already arrived. Verified: the dialog was opened and the chart
built with **no scan requested** and nothing in the server log.

**IT SURFACED TWO CALLBACKS THAT HAD NEVER EXISTED.** `spectrumConfig` declares
`legendLabels` and `legendClick` as dependencies, and `tools/fa-chart-check.js`
supplies its own stubs for them — correct for what that gate asks (does the
config wire them into the right places), and it meant the real implementations
were never written and never missed. The config had been passing for a day
without them. Now gated by `tools/fa-legend-check.js`: 7 mutants, all killed.

**Built ON OPEN rather than at mount**, because Chart.js measures its element at
construction and the canvas lives in a `display:none` dialog. Verified in a
browser: 1042x220, not zero. Destroyed on close.

**`wiring-audit` now reports 0 known gaps across 0 ported pages.**

## 5. The first-run ROUTER overlay — **DONE 2026-08-29**

`web/src/pages/setup-overlay-wire.ts` is MOUNTED, because the route that blocked
it is ported: `internal/server/routers_activate.go` serves
`POST /api/routers/{id}/activate`, which was a 404 as recently as this morning.

`reachable-audit` now reports **111 of 111 modules reachable, 0 unreachable, 0
recorded** — the first time this port has had no inert module.

**One deliberate divergence, recorded rather than slipped in:** an unknown router
is refused with a 404. The live route hands the id to `switchRouter`, which fails
asynchronously AFTER the 200 has gone out — so a typo yields a cheerful
`{ok:true, switching:true}` and then a socket error the caller may not be
listening to. The overlay's Connect would report success over a blank dashboard.
Refusing up front is only possible because this route can answer before it
commits to anything, and it cannot change the outcome of a valid request.

**A BUG I INTRODUCED AND CAUGHT BY READING, WHICH NO TEST WOULD HAVE CAUGHT.**
Factoring the DELETE route's room-move into a shared helper, the first version
was `moveDefaultFollowers(next)` — moving every connection not already on the
target. The two callers select different connections:

    DELETE    the sockets that were on the router just removed
    ACTIVATE  the sockets that were following the OLD DEFAULT

and neither means "everyone not already here". A session pinned to a third router
matches that wider test and would have been dragged across — changing what it
receives, not merely what its selector says. The helper takes `from` for exactly
that reason, and there are now two tests driving real WebSocket connections,
which nothing in the suite did before.

**Verified live:** re-activating the current router answers
`{"alreadyActive":true,"ok":true}` and leaves settings.json untouched; an unknown
id answers 404. A real switch was NOT exercised against the fleet — it would tear
down and rebuild sessions on hardware for no verification the no-op path does not
already give.

## 6. Deferred, and deliberately — per-page URLs

The operator asked on 2026-08-28 whether the app should have `/logs`, `/devices`
and so on instead of one URL for everything. **The live app does not**: every page
is a `<div class="page-view">` toggled by a class, and the URL never changes. The
port reproduces that exactly.

It is a good idea — bookmarkable pages, a working back button, shareable links —
and the machinery is already there, because `showPage()` is the single funnel
every navigation path goes through. But it is a **user-visible behaviour change**,
and the rule that makes this port's DOM gates mean anything is that it reproduces
rather than improves.

**So it belongs after cutover, as a deliberate enhancement to the merged app.**
Recorded here so it is not lost, and NOT to be picked up by a tick that has run
out of the items above.

---

## Where the numbers come from

| claim | source |
|---|---|
| ~~54~~ **15** unwired Settings elements | `tools/wiring-audit.js` with `settings` mounted. 54 was the 2026-08-25 figure and was never re-measured; mounting it on 2026-08-28 said 15, and all 15 are now done |
| 510-line fleet map | `../MikroDash/public/app.js:11432–11941` — the IIFE, brace to brace |
| 786-line alerter | `wc -l ../MikroDash/src/alerter.js` |
| which guards are ported | `portedGuards` in `internal/server/resource.go`, never the prose |
| which pages are mounted | `tools/page-mount-audit.js` |
| which modules ship | `tools/reachable-audit.js`, `tools/module-reachability-audit.js` |
| background collector count | `TestTheBackgroundCollectorCountIsRecorded` — **ask the test, never a grep** |

---

## 7. UPSTREAM DRIFT SWEEP — the live app moved and the port did not follow

**THE STANDING ASSUMPTION OF THIS FILE WAS WRONG, FOUND 2026-08-29.** Everything
below says the porting work is finished and only the operator's switches remain.
That was true against the live app as it stood when it was written. The live app
is now at **v0.7.38-6 with 106 commits in fourteen days**, and one of them —
`d7548b0`, "The Traffic selection survives a reconnect" — was a user-visible bug
the port carried until this tick.

**No gate went red. Three gates covered that exact area and none could see it.**
So a green `verify.sh` is evidence about the gates, not about parity.

**THE BACKLOG: ten commits since 2026-08-27 touch user-visible client code. One is
done. Work through the other nine, newest first, and record each as PORTED,
ALREADY PRESENT, or NOT APPLICABLE with the evidence:**

| commit | subject | state |
|---|---|---|
| `d7548b0` | The Traffic selection survives a reconnect | **PORTED 2026-08-29** — the one real gap. Split `resetTraffic`; new gate `traffic-pick-persist-check.js` |
| `2af8164` | sameEndpoint reads the string "false" as false | **ALREADY PRESENT** — `internal/routers/endpoint.go:145` is the fixed `=== true \|\| === 'true'` form, and its comment records that the port is where the live bug was reported from |
| `f4ade9e` | The connection test names the service it actually tried | **ALREADY PRESENT**, both halves — `classify.go:116` carries "invalid user"/"wrong password", and `routers_conntest.go:143` passes `cfg.TLS`, the coerced flag, naming `f4ade9e` |
| `7185a92` | The prune tests can now see the prune | **ALREADY ADDRESSED** — `eval_test.go:509-532` calls `capMap` directly. MEASURED: replacing its body with `return` FAILS the suite, which is the exact mutation upstream ran to prove its own blindness |
| `5531371` | Show the release notes in the RouterOS Update dialog | **ALREADY PRESENT** — `internal/changelog`, the `packages:notes` channel, `upgrade.ts`'s `notesAreForThisDialog`, and `upd_notes` in the extracted markup |
| `07da9a9` | Crossing the alert state bound stops silencing the fleet | **ALREADY PRESENT** — `internal/alert/eval.go:189` prunes ABSENT keys rather than clearing, with the reasoning for why a trim would be backwards |
| `272eeca` | A late interface rejoins its cycle instead of replacing it | **NOT APPLICABLE, structurally** — the bug lives in a `=interval=` stream's debounce/batch. MEASURED: the port's `ifstatus.go` has 0 Stream calls and 2 `Do` calls; a `Do` is `!done`-terminated, so there is no batch to commit provisionally. The live collector has 21 stream markers |
| `b3ffec0` | A later RouterOS release is announced, not swallowed | **ALREADY PRESENT** — `eval.go:614` computes `supersede` from a non-empty previous version |
| `c8f87f3` | A null name is a missing name, not a site called "null" | **ALREADY PRESENT** — `sites/parse.go:78` uses the LOOSE `== null` test |
| `02df614` | A membership save broadcasts its permissions change once | **ALREADY PRESENT** — exactly one broadcast in `sites_api.go` |

### ROUND TWO — 2026-08-29, later the same day

**The live app moved again while this file said the sweep was complete.**
`v0.7.38-6` → `v0.7.38-12`: eight commits the table above has never seen. That is
the argument for re-running the `git log` rather than reading this section, in one
observation — the sweep is a STANDING task, not a finished one.

| commit | subject | state |
|---|---|---|
| `dd6173b` | One predicate for every boolean off a request body | **PORTED 2026-08-29.** `disabled` and `alertsEnabled` moved from `!!` truthiness onto `jsIsTrue` in `CoerceRouterPatch` and `AddRouter`; `jsTruthy` deleted rather than left as a spare. Upstream's teeth: `PUT {disabled:"false"}` is an operator ENABLING a router, and truthiness disabled it |
| `a4ac96e` | The WAN address is withheld by every path, not by three of four | **PORTED 2026-08-29.** `routerListFor` deleted; `GET /api/routers` now serves the socket shape, so `geo.auto.ip` is withheld from a principal without `system:settings`. The port had reproduced the live defect on purpose and its own note said "when upstream fixes it, delete `routerListFor`" — this is that. 3 mutations, 3 killed |
| `dccbf62` | tlsInsecure was one of four coercions, not one of one | **ALREADY PRESENT** — `routerpatch.go` names the commit |
| `f5416c2` | A null username is missing, not the four characters "null" | **ALREADY PRESENT** — `users_write_api.go:250` tests `isString` before the pattern and names `f5416c2` |
| `51aac86` | sanitizeErr redacts a hostname, not only an address | **ALREADY PRESENT** — `internal/safe/errors.go:76` carries the identical `sanHost` regex in the same chain position |
| `45d9798` | A status event leaves a disabled row saying Disabled | **ALREADY PRESENT** — `settings-routers.ts:170` is `if (!row \|\| row.disabled) return`, following `d7529e0` |
| `d6c4ee0` | The _userPickedIf rule is a ledger, not a window | **NOT APPLICABLE** — `test/` only, no `src/` or `public/` change |
| `9614c67` | The classifier ledger proves it found something | **NOT APPLICABLE** — `test/` only |

**AND ROUND TWO FOUND SOMETHING UPSTREAM DID NOT HAVE.** `dd6173b` also
normalises STORED booleans on read. On the Node side that is defensive; on this
side it is a fleet-erasing bug, because `Routers()` decodes routers.json into
`[]Router` in ONE Unmarshal. MEASURED: one `"disabled":"false"` anywhere in the
file makes `Routers()` return **zero** routers while `PublicRouters()` — which is
map-based — still returns two. The browser lists a fleet that every session,
every collector and the pool sees as empty. Closed by `normalizeStoredRouterBools`,
retried only after the typed decode has already failed and REPORTED as a problem
rather than silently repaired. 4 mutations, 3 killed, 1 equivalent and recorded
as such (removing the `isBool` guard cannot change an answer, because `jsIsTrue`
on a real bool is identity — the guard stays because it makes the function's
claim true by construction).

---

**ROUND ONE, 2026-08-29. One real gap in ten; six were fixes upstream adopted
FROM the port's own reports, one was already correct, one is structurally
impossible here.** That ratio is the useful number: the port was tracking upstream
closely, and the one it missed was the one commit that changed CLIENT state
handling rather than server logic — the half with the fewest differential gates.

**A SECOND, LARGER BUG WAS FOUND DURING THE SWEEP AND WAS NOT UPSTREAM AT ALL:**
three consumers passed AES-GCM *ciphertext* to Telegram, SMTP and ntfy as if it
were the credential (`Changes.md`, 2026-08-29). Found by sending one real
notification, not by any gate. If a future sweep wants a lesson from this one, it
is that **the gates cover payload shape and pure logic, and do not cover "does the
value actually work against the real service".**

**`git -C ../MikroDash log --since=... --format='%h %ad %s'` is how this list was
built; re-run it rather than trusting the table, exactly as the note at the top of
this file says.** Anything older than 2026-08-27 was being tracked live by earlier
sessions and is likely already in; verify before assuming either way.

---

## Where this stands — 2026-08-29

> **READ SECTION 7 FIRST — it is at the bottom of this file and it is the work.**
> Everything in this section was written when the porting was finished against the
> live app *as it then stood*. On 2026-08-29 the live app was measured at
> **v0.7.38-6, 106 commits in fourteen days**, and one of them was a user-visible
> bug the port had carried with every gate green. Sections 0–6 keep their numbers
> and their order so older `PORT-QUEUE.md` references still resolve.

**EVERY ITEM IN THIS FILE IS CLOSED, AND NO PORTING WORK REMAINS.** The audits
that would say otherwise:

| audit | reading |
|---|---|
| `wiring-audit` | 0 known gaps across 0 ported pages |
| `page-mount-audit` | 26 of 26 extracted bodies mounted, 0 blocked |
| `reachable-audit` | 111 of 111 modules reachable, 0 unrecorded |
| `page-gate-audit` | 84 of 84 page modules gated |
| `endpoint-audit` | 29 of 29 served by Go, 0 proxied |
| `dash-coverage-check` | 110 of 122 ids with a writer, 12 unported and all recorded |

`PORT-QUEUE.md` has **seven** open items and **not one is porting work**: three
are built-and-switched-off awaiting the operator (notification dispatch, the
background pool, the backup scheduler), two are the operator's decisions
(Routers' background sessions, live verification), and two are the cutover
itself.

**So the remaining work is verification and cutover, both of which are the
operator's to schedule.** That is a different state from "nearly done", and it is
worth saying plainly rather than leaving a reader to infer it from seven entries
that each look open.

**CUTOVER STEP 0 IS NOW COMPLETE, 2026-08-29.** Every switch the cutover window
flips is now CODE THAT EXISTS AND IS TESTED, rather than something to be written
inside the window: the history writers (`internal/db/historywrite.go`,
`internal/historywire`), the alert dispatch, the background pool, and — as of this
tick — the backup scheduler's constructor
(`internal/server/backup_scheduler.go`, `-backup-scheduler`, off by default,
9 mutants killed). What is left in every case is a FLAG, not a file.

**The history recorder caught up on the same tick.** It had been the one piece
still described as "the call site and the flag are the cutover step itself",
which was inconsistent with the standard applied to the scheduler an hour
earlier. `internal/server/history_wire.go`, `-history` (off), and the `Record`
and `Flush` call sites in `session.go` now exist and are pinned; a live session
held 100 seconds with the flag off added ZERO rows to all four history tables.
**And the flag has now been run ON against the test database** — rows land in the
right tables with the minute-midpoint `ts` Node uses, and the multi-interface
branch was confirmed by subscribing ether2 and watching its rows appear. Turned
back off afterwards. Step 0 is now tested, not just written.

**And building it found a real bug, which is the argument for step 0 in one
sentence.** `Manager.Release` stopped 5 of the 14 collectors the connect block
starts; the other nine kept a self-rescheduling `time.Timer` alive forever on a
released session, reading through a closed client every 1–60 seconds, for every
router ever acquired. The scheduler is the first caller that acquires and
releases routers *nobody is watching*, on a timer — it would have been the first
thing to make that unbounded, and it would have been found during the cutover
window instead of a week before it. Fixed and pinned
(`internal/session/release_test.go`).

**The one thing waiting on a decision is `-alert-dispatch`.** It is built, gated
and off; turning it on while the Node app also runs means both engines send every
notification.

---

## An accident, recorded rather than tidied away

**Sections 2 and 3 were DELETED by an edit on 2026-08-28 and restored on
2026-08-29.** The edit rewrote item 1g by replacing everything from its heading to
"the next `### ` heading" — and the next `### ` was inside section 3, because
sections 2 and 3 are `##`-level and the search stepped straight over them. Two
items vanished from the work list and nothing failed; the tell was a later tick
reading the headings and finding `## 1.` followed by `## 4.`.

`LOOP.md` is untracked, so there was no git copy to restore from. The sections
below were rebuilt from measurable sources — the audit's own EXPECTED entries,
`app.js:11432–11941` counted brace to brace, `wc -l` on the alerter — rather than
from memory. The "Read this before wiring the dispatch" subsection is the
ORIGINAL text, which survived because it was the boundary the bad search landed
on.

`tools/loop-sections-audit.js` now fails if the numbering has a hole.

## The sweep's own blindness — closed 2026-08-30

`verify.sh` threw away every passing gate's output, so `gates: 136 run, 0 failed`
could not distinguish a gate that compared 40 cases from one that compared none.
It now records the largest number each gate prints into `testdata/gate-census.txt`
and fails when one shrinks; growth ratchets silently. 161 gates tracked.

**The measurement came first and came back clean** — no gate in the tree is
currently blind, and all seven Go source-walking tests already guard against it.
The ratchet is there so that stays true, because this project has twice paid for a
check nobody was watching (`endpoint-audit`, `hook-selftest`). Stability was
verified before the ratchet was trusted: two identical passes, byte-identical
counts. Mutations: a blinded gate and a deleted gate both caught.

## The documentation's own claims — audited 2026-08-30

`CLAUDE.md` tells its reader to distrust it and names the tool behind each number.
Nothing re-ran those tools. Three claims were wrong at once: `page-gate-audit`
quoted as "68 of 70" when it is 85 of 85 (prose understating finished work),
`hook-selftest` described by the contract it had BEFORE the hook was disabled on
2026-08-23, and "three generators need better-sqlite3" sitting two paragraphs
above a note saying that list is counted rather than typed — it is 12.

`tools/doc-claim-audit.js` re-measures five claims on every sweep, with a
hand-written claim list and an unmatched locator treated as a FAILURE rather than
a skip. Five mutations killed, including the two original stale sentences and a
change to the SOURCE with the doc left standing.

## Structural DOM parity, all 26 pages — checked 2026-08-30, no defect

The first actual comparison of the RENDERED PAGES. Prior verification covered
payloads, socket events and console errors; none of those is the DOM, which is
what "nothing user-visible may change" governs.

The raw diff claimed 212 live-only signatures and 30 port-only. **All were
artifacts**, from three causes worth knowing before running it again: sampling
before the page settled (`map-arc` reads 0 vs 18 early, 18 vs 18 after 14s), a
reused browser context in which each app had been left on a different page, and
hidden-page DOM retention. Controlled for all three, topology renders identically
on both (4 nodes, 3 edges, "3 + 33" devices, 1.0 ms) and a typed filter survives
navigation identically.

**It is a procedure, not a gate.** It needs both apps and a browser, and it cannot
tell "never rendered" from "no data this instant" — every difference must be
re-probed with a settle and a pinned page. A flaky gate would be worse than none.

## Both go/no-go gates PASSED, and a write reached a real router — 2026-08-30

**B1 `cmd/conformance`: 7/7 on hAP AX3, cAP AX and hAP AC2.** It never needed the operator —
`-data /data -router "<label>"` decrypts the credential out of the live store, and that had been
documented in the tool's own header the whole time. The AX3 run exercised the completeness case that
replaced block-boundary detection: 29 clients across 6 interfaces, per-interface sum 29.

**B2 `cmd/compat`: passed against the LIVE /data** — settings key, 2/2 credentials, envelope
round-trip, 3 users, 3 routers, schema v15, 290 audit rows, 13 backup pairs.

**Live write verification on AC2, through the real UI.** dnsStatic and fwFilter, each created then
deleted, each verified by the LIVE NODE APP reading the same physical router — which is what proves
the write reached hardware rather than optimistic UI. `fwGuard` correctly stayed silent on a disabled
TEST-NET rule. Audit rows correct including `actor_name`. AC2 restored exactly.

**One wrong call, corrected in place:** I reported the DNS page as a port defect before the AC2
session had connected. It matched live once connected. A difference measured before the thing has
settled is not a difference.

**One real unexplained thing:** the FIRST router switch never connected (2 minutes, no session line)
while the second took 7 seconds, with the client sending `router:select` correctly both times. It
rhymes with the operator's "sometimes when I sign in, some of the cards … dont have any data".

## The standing per-tick protocol

Unchanged, and it is not optional — it is what makes the loop safe to leave alone:

1. **Take the top unfinished item.** Do not skip ahead because something looks
   easier; the order below is dependency order.
2. **Port it against a corpus generated by RUNNING or LIFTING the live
   implementation.** Never against a reading of it. `tools/*-cases.js` and
   `tools/*-check.js` are the two shapes: a generator writes a corpus with
   `--check`, a gate drives both implementations and compares.
3. **Mutation-check every gate.** Report kills honestly: DID NOT BUILD is not a
   kill, ANCHOR MISSED is not a kill, and an equivalent mutant is recorded with
   the measurement that shows it is equivalent.
4. **End GREEN**: `MIKRODASH_SRC=../MikroDash sh tools/verify.sh`.
5. **Update this file, `PORT-QUEUE.md` and `Changes.md`.**
6. **Verify in the browser.** Playwright is configured; the live app is on
   **:3081** and this port on **:3085**, so the two can be driven side by side.
   *This is not optional either* — the Devices-page refresh defect (item 0 below)
   was invisible to 127 gates and obvious within ten seconds of a browser.
7. **REDEPLOY, every tick.** The operator asked for this on 2026-08-28 — "rebuild
   the current docker image after every tick, id like to be able to see the
   changes as they go in" — so a tick is not finished until the running app has
   the change in it. Verify green FIRST: deploying a red tree puts a known-broken
   build in front of the person watching.

   ```bash
   cd web && npm run build && cd ..
   docker run --rm -v "$PWD":/src -w /src -v mikrodash-gomod:/go/pkg/mod \
     golang:1.25-alpine go build -o /src/bin/mikrodash ./cmd/mikrodash
   docker restart mdtest
   ```

   Then confirm it actually came up — a container that restarts into a crash
   loop reports success from `docker restart`. `curl -s -o /dev/null -w '%{http_code}'
   http://127.0.0.1:3085/login` should be 200.

   **A restart drops every session**, so an open browser lands back on the login
   page. That is expected, not a defect to chase.

8. `ScheduleWakeup` with `<<autonomous-loop-dynamic>>` at **180s** (or let the
   `*/2 * * * *` cron fire), and end the tick with an estimated **% complete for
   the whole port**.

### The constraints that do not move

- **`../MikroDash` is never written to**, except `ToDo.md`, which is where defects
  found in the live app are recorded.
- **Router writes: hAP AC2 (`10.0.0.53`) only.** hAP AX3 (`10.0.0.2`) is the
  operator's home router and cAP AX (`10.0.0.4`) is read-only.
- **No credential and nothing identifying reaches any file.** Check with
  `grep -rl` for the test password after any run that used one.
- **No version bump and no release notes** during a working session.
- **Nothing user-visible may change.** A page that renders differently has not
  been ported, however correct its data.

---
