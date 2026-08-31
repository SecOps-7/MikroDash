# Making the gates independent of `../MikroDash`

**This is a record, not a queue.** The job is finished: all 136 gates and 39
audits pass with and without the reference. `LOOP.md` carries what is left,
which is one decision about six files.

It is kept because the REASONING is not re-derivable from the diffs. Nearly
every rule below was learned by something going quietly wrong first, and the
failures are more useful than the fixes:

- a guard copied onto a block that needed none, deleting 9 comparisons while the
  gate reported green;
- three gates that could not report failure at all — an async `main()` and a
  blanket `unhandledRejection` handler swallowing their own assertions;
- an audit whose filter set went empty, so it dismissed all 37 of its findings
  and said so as a success;
- cases silently lost when a declared-difference table was emptied;
- six distinct ways a mutation can look meaningful and prove nothing.

The durable rules distilled out of this are in `CLAUDE.md` under "Gate
conversion"; what follows is how they were arrived at, gate by gate.

---

## 0. The measured baseline

Taken 2026-08-30 with `MIKRODASH_SRC=/nonexistent-reference sh tools/verify.sh`:

| layer | without the reference | state |
|---|---|---|
| Go — `go test ./...` | 37 packages, 0 failures | **already done** |
| TypeScript — `tsc` | passes | **already done** |
| gates — `tools/*-check.js` | 118 of 136 fail | to convert |
| audits — `tools/*audit*.js` | 18 of 37 fail | to convert |
| generators — `tools/*-cases.js` | 101 of 110 stale | to convert |
| `nodecheck/` | fails | decide (item 4) |
| | **`verify: 240 failing`** (2026-08-31; **193** after batch one plus 17 of batch two) | |

**They FAIL, they do not pass vacuously.** That is the good failure mode and it
must survive this work: a converted gate that goes green by asserting nothing is
strictly worse than the coupling it replaced.

`grep -rl MIKRODASH_SRC tools/ nodecheck/ | wc -l` = 266 files.

---

## 1. The design, and the acceptance test every item must meet

The corpora in `testdata/` are already committed and self-sufficient. Generators
need the reference only to REGENERATE. **The gates are the problem**: they lift
the live renderer at runtime and drive both implementations from one payload,
comparing `innerHTML`.

Convert by FREEZING the reference's output into a committed golden file, then
asserting the port reproduces it. A differential gate becomes a golden-file
assertion about our own behaviour.

Shape: a `--freeze` mode that records expected output, and "prefer frozen, fall
back to lifting" at run time. The gate keeps regenerating while the reference
exists and keeps asserting once it does not.

**THREE THINGS EVERY CONVERTED GATE MUST DO, and the third is the one that will
be skipped if it is not written down:**

1. Pass WITH the reference present — unchanged behaviour.
2. Pass WITHOUT it: `MIKRODASH_SRC=/nonexistent sh tools/verify.sh`.
3. **Still FAIL on a mutation of the port's own renderer.** Freezing a gate that
   no longer bites is neutering it while the sweep goes green — the exact
   "a check that cannot fail" defect this repo has found four times. Mutate,
   record the kill, restore.
4. **CORRUPT THE GOLDEN and confirm the gate notices.** Cheaper and more
   universal than test 3: it needs no knowledge of what the gate renders, only
   that it consumes what it froze.

   **RUN IT PROPERLY, OR IT LIES IN BOTH DIRECTIONS.** Two rules, each learned by
   getting it wrong on 2026-08-30:

   - **Corrupt EVERY live entry, not the first.** Corrupting one entry can hit a
     case the gate asserts loosely, and the gate then passes for a reason that
     says nothing about whether it consumes its golden.
   - **Judge by EXIT CODE, never by matching words in the output.** A gate fed a
     corrupted golden often CRASHES, and a crash prints a Node stack rather than
     "cases differ". A substring matcher read four healthy gates as inert, and
     `reports-charts-check` was reverted on that basis when it was fine.

   Measured correctly: **all 25 converted gates notice a fully corrupted golden
   and all 25 pass on a clean one.**

Freeze must happen WHILE the reference still exists, so a golden is provably the
live output rather than merely a recording of whatever the port did that day.

---

## 2. ~~Batch one — the 29 gates that go through `L.liveSource`~~ — **DONE 2026-08-30**

All 29 converted. Each passes on a clean golden, and all 29 notice a fully
corrupted one (by exit code). `verify.sh` is green with the reference, and the
without-reference count fell **240 -> 210**.

### THE LESSON THAT MATTERS: I diagnosed four gates wrong, and all four were the same bug

The last four were set aside across two ticks with confident, specific reasons.
Every one was wrong:

| gate | what I claimed | what it was |
|---|---|---|
| `reports-latch` | "the live output embeds the current date" | it pins a clock; the drift was the `!==` bug, plus a loop with no `name` in scope |
| `reports-tables` | "embeds the current date — 60 of 69 entries" | a KEY COLLISION: the case loop runs once per timezone, so each key was written twice and the last won |
| `audit-page` | "async plus a second live path" | the same per-timezone key collision |
| `connections-lists` | "`framesLive` is STATEFUL across calls" | two loops both keyed on `name`, plus a helper recording every call under one key |

**None was a property of the gate. Every one was a defect in how I froze it** —
and the "stateful" claim was the most confident and the most wrong, quoting the
gate's own header as evidence for a conclusion that header did not support.

The tell was available the whole time: two identical freezes of `reports-tables`
produced BYTE-IDENTICAL output. A gate that is non-deterministic cannot do that.
One cheap experiment — freeze twice, diff — refuted the diagnosis for all four,
and it was run only after the fourth had been set aside.

**So: when a frozen gate drifts, suspect the KEY before the gate.** Freeze twice
and diff. If the two freezes agree, the gate is deterministic and the drift is a
collision or a missing loop dimension.

## 3. Batch two — the 83 gates that reach the reference their own way

`grep -rl MIKRODASH_SRC tools/*-check.js | xargs grep -l readFileSync | xargs grep -L liveSource | wc -l` = **92**.

Corrected from 83 for the same reason as item 2 — `alert-filters-check.js` is one
of these, not a batch-one gate, despite importing `lib/lift`.

No shared seam, so each needs reading. Expect several to be convertible by
routing them through `lib/lift.js` instead — that is a reduction, not a
workaround, and is worth doing where it fits.

- 3a. **DONE 2026-08-30 — sorted, and they are far more uniform than expected.**

  | shape | count |
  |---|---|
  | read `public/app.js` themselves, with an IDENTICAL line | **79** |
  | read some other reference file | 11 |
  | `require()` a live module | **0** |

  `const LIVE = path.resolve(process.env.MIKRODASH_SRC || ...)` is identical in
  87 of the 92. So there is a seam after all — it just is not `lib/lift.js` yet.

- 3b. **THE PATTERN, proven on `alert-filters-check`** (exemplar converted, all
  four acceptance tests pass):

  1. **Route the direct read through `L.liveSource(ROOT)`.** It already returns
     `''` when the reference is absent instead of throwing ENOENT at require.
  2. **Make the module-scope live assembly LAZY.** These gates build their VM
     source at module scope from their own `slice()` helpers, which throw on an
     empty source — `cannot find NOTIF_TYPES_KEY` — even though the VM that uses
     it runs inside a function the frozen output means we never enter.
  3. **Freeze the OUTPUT, not the source.**

  **Step 3 is the one to get right, and step 2 is what makes it possible.**
  Freezing the assembled SOURCE also makes the gate run without the reference,
  and it is cheaper — one `G.value` per gate. It also **vendors the reference's
  JavaScript into `testdata/` and keeps executing it**, which is the opposite of
  what this work is for: the JS was supposed to stop existing. The exemplar was
  built that way first, measured at 4.4 KB of reference source, and redone.
  Its golden now contains none.

- 3c. **IN PROGRESS — 10 of 90 done.** 39 gates
  converted in total; all 39 pass on a clean golden and all 39 notice a
  corrupted one.

  **A MEASUREMENT OF MY OWN MAKING WAS WRONG.** "Remaining batch two" read 66
  when it was 90: the `slice()` guard's comment MENTIONS `L.liveSource`, and the
  filter was `grep -L liveSource`. A gate that merely names the seam in a comment
  counted as converted. **Filter on the CALL — `L.liveSource(` — not the word.**

  **The remaining 80, by what blocks them:**

  | shape | count |
  |---|---|
  | inline slicing (no helper to guard) | 28 |
  | carries the `slice()` guard, needs routing | 24 |
  | a differently-spelled slice helper | 15 |
  | no slicing at all | 14 |
  | another named helper | 9 |

  **Two failure classes among gates that route cleanly but still fail without the
  reference:**
  - **The live half is still entered** — `c.openAccountModal is not a function`,
    `ctx.buildSliders is not a function`. The gate calls into its live context by
    a name the converter does not wrap.
  - **A module-scope assert on lifted text** — "the gauge slice lost its return",
    "the `_flushSysUpdate` slice lost document.hidden". Same shape as batch one's
    capsman and logs; the fix is to freeze the lifted value.

  **`sched-remove-check` cannot be frozen as it stands** and was reverted: it
  asserts a KNOWN DIFFERENCE between live and port rather than equality, so a
  corrupted golden still differs and still satisfies it. A gate whose assertion
  is "these two disagree in exactly this way" passes the corruption test for the
  wrong reason.

  **A shared win worth more than the two gates:** 26 batch-two gates define a
  BYTE-IDENTICAL `function slice(decl, close, name)`. It now returns `''` when
  the source is empty, so it no longer throws `cannot find <name>` at module
  scope before a frozen output can be served. That is the batch-two equivalent
  of the batch-one lift stubs, and it is a precondition for converting any of
  them.

  **Two converter defects, both found by a gate failing WITH the reference:**
  - `const L = require(...)` was inserted after the LAST top-level require,
    which landed it AFTER the `liveSource` call in two gates —
    "Cannot access 'L' before initialization". It now goes immediately before
    the first use, anchored on the line that used to read app.js.
  - The case-name heuristic assumed a `name` variable in scope and produced
    `ReferenceError: name is not defined` — the same guess that bit
    `reports-latch`. **`G.seq()` now supplies a counter key**, so the converter
    never fabricates a variable. Opaque keys in drift messages are the price.

  **Five gates were converted, found to still fail without the reference, and
  REVERTED rather than left half-done:** `account`, `access-summary`,
  `auth-visibility`, `bandwidth-chart`, `bandwidth-card`. Each needs its own
  reading — a differently-spelled slice helper, or a live path the wrap misses.
  They pass WITH the reference either way, so reverting costs nothing and keeps
  the tree honest about what is actually converted.
- 3d. **THE MECHANICAL CONVERTER IS EXHAUSTED — measured 2026-08-30.**

  It converted 39 gates and then stopped paying. A whole tick on the 14
  "no-slicing" gates produced **zero** conversions and two regressions, each
  reverted. The remaining 80 need reading one at a time.

  What it can still do, and what it cannot:

  | it handles | it does not |
  |---|---|
  | routing a reference read through `L.liveSource` | a gate with NO named live function to wrap (`live=0`) |
  | freezing `const X = L.fn(...)` at module scope | values built inline or inside array literals |
  | wrapping `liveRun`/`liveRunner`/`framesLive` | a live context called by its own method names (`c.openAccountModal`) |
  | picking `__GOLD` when `G` is taken | module-scope asserts on lifted TEXT |

  **Three converter refinements this tick, each real but insufficient:**
  - Route EVERY reference read, not only `app.js` — five gates read
    `public/js/dashboard-grid.js`, `index.html` or `src/*.js`.
  - Match `SRC` as well as `LIVE`, but **never `ROOT`**: 14 gates use `ROOT` for
    the PORT's own root, and routing those would have redirected reads of our own
    testdata into the reference. Checked before generalising, not after.
  - Anchor the inserted require on any `L.liveSource(` call, not the no-argument
    form.

  **Recommended next step: stop batching and convert individually**, cheapest
  shape first — the 24 already carrying the `slice()` guard need only a live-call
  wrap. Expect roughly one gate per few minutes rather than a group per tick.

  **CONFIRMED 2026-08-30: individual conversion works where batching failed.**
  `account-check` and `poll-sliders-check` both landed on the first attempt once
  read properly — 41 gates, 198 failing. Two shapes now have worked examples:

  - **A live half run INLINE inside a compare helper** (`account-check`'s
    `compareAsync`): wrap the whole live block in one `G.live(...)` closure
    returning the state it produced. The port half stays untouched.
  - **A named `runLive(sc)`** (`poll-sliders-check`, `alert-filters-check`): wrap
    the call. The `slice()` guard already handles module-scope assembly, so no
    laziness was needed after all.

  **Order matters when doing this by hand:** run the converter FIRST, then make
  the hand edit. Editing a reverted file and only then converting produced
  `ReferenceError: G is not defined`, because the golden the edit referenced had
  not been inserted yet.

- 3e. **THE RECIPE, as of 45 gates converted.** Three steps, in this order:

  1. **Run the converter.** It now matches `runLive(` as well as `liveRun(` —
     that reversed name alone was blocking three gates, and it had been missing
     from the pattern since the start.
  2. **Guard any module-scope assert that validates the LIFT** with
     `L.hasReference(ROOT)`. "the gauge slice lost its return", "cannot find
     MAX_CLIENT_POINTS", "cannot bound the VPN mini card" are all this shape:
     valuable while the reference exists, meaningless without it. Guarding says
     that; freezing the lifted TEXT would instead put the reference's JavaScript
     in `testdata/`, which is what this work removes.
  3. **Wrap whatever the gate calls into the live context**, even when it is not
     a named `liveRun`. `gauge-check` calls `ctx.gauge(label, pct, cls)` directly
     in a triple loop — wrapping it with a composite key froze 757 cases.

  Eight gates still need steps 2 and 3 together and were reverted rather than
  left half-done: `caps`, `sched-run`, `system-card`, `traffic-buffer`,
  `vpn-card`, `sched-runs`, `sched-form`, `stale`.

- 3f. **A CATEGORY THAT CANNOT MEET ACCEPTANCE TEST 4 — three members and
  counting.** `sched-remove`, `sched-run` and `sched-runs` assert a KNOWN,
  INTENDED DIFFERENCE between live and port (the report-endpoint prefix), not
  equality. Their own words: "the two now AGREE, so the recorded difference is
  stale".

  Freezing them looks fine — they pass with the reference, pass without it — and
  then a corrupted golden STILL DIFFERS, so the assertion is still satisfied and
  the corruption test passes for the wrong reason. All three were reverted.

  **They need their expected difference recorded explicitly** (e.g. freeze the
  live value AND assert the specific delta) before they can be converted. Do that
  deliberately, as its own item, rather than as part of a batch — a gate in this
  family that slips through looks converted and checks nothing.

- 3g. **PREREQUISITE DONE — 40 slicing helpers guarded across 34 gates.**
  `slice()` was not the only one: `lift`, `grab`, `braceBody`, `lineWith` and
  `siteMemberRows` all index into `src` and throw. Each now returns `''` on an
  empty source.

  **It unlocked nothing on its own**, and that is the point of recording it: the
  guard removes the module-scope crash, and every one of the eight gates tried
  afterwards then failed one step later with `api.startDrag is not a function` —
  the live context being called by its own method names. **The guard is a
  prerequisite, not a conversion.** Expect to need step 3 (wrap the live call)
  for essentially every remaining gate.

  **One exclusion, learned by breaking it:** a helper that declares its OWN
  `const src = ...` cannot be guarded on the outer `src` — the reference sits in
  the temporal dead zone and the gate dies with "Cannot access 'src' before
  initialization" WITH the reference present. `access-summary-check` was the one;
  the patcher now skips any helper that redeclares `src`.

- 3h. **`grid-drag-check` attempted and reverted — the shape, so the next
  attempt is cheap.** Two of three parts converted cleanly:

  1. **The main loop converted well.** It drives live and port step by step and
     compares snapshots. The live side is STATEFUL across steps, so the whole run
     is replayed as ONE `G.value('snaps:' + name, ...)` returning the snapshot
     sequence, and the port loop indexes into it. One value per script, not one
     per comparison.
  2. **Four believability blocks defeated it**, and the reason is specific:
     they declare `const L = liveSide(ll)`, which SHADOWS the lift module — also
     bound to `L`. So `L.hasReference(ROOT)` inside such a block resolves to the
     live side, not the module. Guarding from outside the block works in
     principle; a mechanical insert placed the guard on the wrong lines and broke
     the gate WITH the reference.

  **The fix for this shape: rename the import.** In any gate that binds `L` to
  something else, import the lifter as `LIFT` (the converter already does this
  for `G` -> `__GOLD` when `G` is taken; it needs the same for `L`). Then the
  guards are unambiguous and can be placed inline.

- 3i. **`grid-drag-check` CONVERTED — 47 gates.** The `L`-shadowing fix worked:
  the converter now binds the lifter to `LIFT` when the gate already uses `L`,
  which is what made `LIFT.hasReference(ROOT)` unambiguous inside blocks that
  declare `const L = liveSide(...)`.

  Three edits beyond the converter, all likely to recur in the `grid-*` family:
  - **Two stateful loops** frozen as one `G.value` each, returning the ORDERED
    snapshot sequence; the port loop indexes into it.
  - **Three believability blocks** guarded with `LIFT.hasReference(ROOT)`.
  - A block opening `{ // comment` needs the same guard as one opening `{` — an
    exact-match walk-back missed it and left the gate failing.

  **The converter is now committed at `tools/gate-conversion/convert.py`** with
  its limits written down, rather than living in a scratchpad. Its siblings
  (`grid-edit`, `grid-resize`, `grid-store`, `grid-layout`) each still need their
  own live call frozen — `L.enterEditMode`, `api.startResize`, `L.loadLayout` —
  and were reverted.

- 3j. **`grid-store-check` CONVERTED — 48 gates.** 56 recordings, 62 comparisons.
  Two reusable things came out of it:

  - **A `cmp(what, a, b)` helper is a mechanical conversion.** Where a gate funnels
    every comparison through one helper whose first argument is a stable key, the
    live argument can be wrapped by paren-matching and a top-level comma split:
    `cmp(K, live, port)` → `cmp(K, G.live(K, () => live), port)`. That handled 8 of
    this gate's sites in one pass and is worth trying FIRST on any `cmp`-shaped
    sibling. The script is `tools/gate-conversion/wrapcmp.py`.
  - **It is not sufficient where the live side is DRIVEN before it is read.**
    `L.saveLayout()` mutates the world and `lw.logs` is read afterwards, so wrapping
    only the read still calls the live method. Those need the drive AND the read in
    one closure — and where an `await` separates them (`logs` fills on the next
    microtask) the closure must be `async`, which `G.live` already handles.

  **One assertion had to change target, not just move.** The shared-room dedupe
  property was asserted against the LIVE side — the half that stops existing. It is
  restated against the PORT, which is what needs to keep deduping. Watch for that
  shape: a live-only assertion converts to nothing unless someone notices it is
  aimed at the wrong implementation.

  Remaining `grid-*` siblings: `grid-edit`, `grid-resize`, `grid-layout`.

- 3k. **`grid-layout-check` CONVERTED — 49 gates.** 1040 recordings, 1038
  comparisons. `wrapcmp.py` handled 17 sites; the rest were four shapes worth
  recognising by name, because none is visible from the diff alone:

  - **A CASE TABLE BUILT FROM THE LIVE SIDE.** `L.COLS`, `L.ROWS` and
    `L.cloneLayout(L.DEFAULT_LAYOUT)` construct the inputs, not the answers. They
    go `undefined` without the reference, every derived key turns into
    `inBounds(,,1,1)`, and the gate fails on a missing recording rather than on a
    defect. Freeze the table inputs as `G.value`s. This does not weaken the gate:
    those constants are separately compared against the port's.
  - **A LIVE CALL HOISTED OUT OF ITS CLOSURE.** `const live = L.getCellSize()` on
    the line above the `cmp` runs on replay whether or not the golden is used —
    the exact thing freezing exists to prevent. `wrapcmp.py` cannot see this; it
    only moves what is inside the call.
  - **A SWEEP THAT COMPARES ONLY ON DISAGREEMENT.** 400 random iterations call
    live every time but reach `cmp` only when the two sides differ, so a PASSING
    run records nothing and the frozen gate then has nothing to replay. Freeze per
    ITERATION, not per comparison. Safe here because the RNG is seeded and the
    port loop regenerates the identical layout, so iteration N is the same case in
    both directions by construction — do not reproduce the call order by hand.
  - Lift-validity assertions guarded, as in 3i.

  Remaining `grid-*` siblings: `grid-edit` (6 cmp sites), `grid-resize` (4 cmp
  sites, no `L.<method>` calls at all — likely `api.startResize`).

- 3l. **`grid-edit-check` CONVERTED — 50 gates.** 53 recordings, 138 comparisons
  (unchanged, so the census does not shrink). Recipe 3i fitted a third gate: both
  scripted loops drive the live world step by step outside the comparison, so each
  live run is frozen as ONE ordered snapshot sequence that the port loop indexes.

  **The recurring lesson, now on its third gate: LIVE-ONLY ASSERTIONS.** Four here
  — three room-asymmetry properties and the panel believability check — asserted
  against the LIVE side. They convert to nothing at all unless someone notices
  they are aimed at the implementation that is going away. Re-aim them at the
  PORT: the property is the same, and it is the port that has to keep satisfying
  it. **Grep a candidate gate for assertions mentioning the live world before
  starting** (`r.lw`, `L.`, "the live ...") — that is where the silent losses are.

  Also: `runScript` now builds the live editor only when `LIFT.hasReference(ROOT)`,
  so a helper that constructs BOTH sides does not have to be split in two.

  Remaining `grid-*` sibling: `grid-resize` (4 cmp sites, no `L.<method>` calls —
  likely `api.startResize`).

- 3m. **`grid-resize-check` CONVERTED — 51 gates. THE `grid-*` FAMILY IS DONE**
  (`grid-drag`, `grid-store`, `grid-layout`, `grid-edit`, `grid-resize`). 106
  recordings, 331 comparisons; all three lockstep loops frozen as ordered
  sequences, recipe 3i's fourth fitting.

  **The 3l pre-check earned itself immediately.** Grepping for live-world
  assertions first found four property blocks — the clamp-bites precondition and
  three believability blocks — all driving `liveSide` for no reason beyond it
  being in scope. `portSide` returns the same `{w, handle, api}` shape, so
  re-aiming them was a one-word change per block. **One of them is the only thing
  that catches a port that never releases the pointer capture**; converted the
  naive way, that mutation would have gone live.

  Running total across the family: recipe 3i (freeze the driven run as an ordered
  sequence) fitted 4 of 5 gates, and every one of the 5 carried at least one
  live-only assertion.

- 3n. **THE CENSUS CAUGHT A CONVERTED GATE CHECKING LESS — fix landed.**
  `grid-drag-check` reported 76 comparisons with the reference and 67 without.
  The cause is a distinction worth naming, because it is invisible in a diff and
  the gate stays GREEN either way:

  > **A `hasReference` guard belongs only on a block that ASKS THE LIVE SIDE A
  > QUESTION. It must never wrap a block that COMPARES, even one whose live half
  > is frozen — a frozen comparison answers perfectly well without a reference,
  > and guarding it deletes the check in the only condition that now occurs.**

  One of grid-drag's three guards sat on a fully converted comparison block and
  dropped 9 cases. Demonstrated rather than assumed: **with the guard, dropping
  the `inBounds` refusal in `dashboard-grid-drag.ts:142` PASSES.** Without it that
  mutation fails. The other two guards are now re-aimed at the port instead.

  Swept every gate carrying a `hasReference` guard for the same shape — no other
  guard wraps a `cmp()`, so this was the only instance. **The census is the only
  thing that could have found this**, which is the argument for keeping it.

- 3o. **`notif-bell-check` CONVERTED — 52 gates, in TWO recordings.** A much
  cheaper shape than the `grid-*` family, and worth checking for FIRST on any
  remaining gate:

  > **If the gate TRANSCRIBES the live half into itself rather than lifting it,
  > almost nothing needs freezing.** The transcribed model is ordinary gate code
  > and runs without a reference. Only the values genuinely read from the live
  > source — here `MAX_ALERTS` and the lifted `esc()` — have to be frozen.

  58 cases, 2 recordings. Route the read through `LIFT.liveSource` so it yields
  `''` instead of throwing ENOENT, freeze the lifted values, and guard the
  source-content assertions — which is the legitimate use of a guard, since each
  asks the live SOURCE a question (contrast 3n).

  One detail worth copying: `MAX > 0` is re-asserted OUTSIDE the guard, so a
  corrupted golden cannot smuggle a non-number past a check that only ran when a
  reference was present.

- 3p. **`collector-coverage-check` CONVERTED — 54 gates.** A third shape, and the
  one that applies to every remaining COVERAGE gate:

  > **Freeze the FACTS extracted from the reference; leave the comparison they
  > feed pointing at the port.** Here that is the live collector set and each
  > file's emits, checked against `goSrc` read from `internal/` on every run. The
  > question changes from "does the port cover the reference" to "does the port
  > still cover the collector set the reference had" — the same check for as long
  > as it matters, and answerable without a reference.

  A scan-sanity assertion (`files.length > 20`) is deliberately left UNGUARDED:
  it validates the recording as much as the scan, because a golden that lost most
  of its collectors reads exactly like a broken directory walk.

  **Acceptance trap worth remembering:** the first Go mutation I tried was
  `dns:update`, which appears in TWO Go files, so renaming one occurrence changed
  nothing and the gate correctly passed. Pick an event with a single site
  (`grep -rho '"ev"' internal/ --include=*.go | wc -l`) or the test proves nothing.

  Still ENOENT without the reference: `conn-filters`, `dashboard-wiring`,
  `traffic-pick-persist`, `usernotify-form`. `dash-coverage`, `notes-rules` and
  `orphan` already pass without it and need no work.

- 3q. **`traffic-pick-persist-check` CONVERTED — 55 gates, no golden.** Part 1
  asks the live source where it clears `_userPickedIf`; parts 2 and 3 check the
  port. Part 1 feeds nothing downstream, so it is guarded and nothing is frozen.

  **Two things worth carrying forward:**

  - **A guarded block can leave the success line lying.** This gate printed
    "live clear site verified" whether or not the guarded block ran. Grep a
    converted gate's final `console.log` for claims about the live side —
    a gate that reports a check it skipped is the quiet version of 3n.
  - **A mutation that does not do the forbidden thing proves nothing.** My first
    attempt added an exported clearer nothing called, and the gate passed —
    correctly, because what it forbids is a connect handler CALLING a clearer.
    Read what the gate actually asserts before choosing the mutation; this is the
    second time in two gates that a lazily-chosen mutation looked like a hole.

  Still ENOENT: `conn-filters`, `dashboard-wiring`, `usernotify-form`.

- 3r. **`usernotify-form-check` CONVERTED — 56 gates.** Three recordings
  (`UN_BOOLS`, `UN_STRS`, `UN_CREDS`); the forms are built from the port each run.

  **The guard against a vacuous pass is the important part.** A non-emptiness
  assertion on all three lists sits OUTSIDE the guard, because a golden holding
  three empty lists makes every comparison trivially true and the gate reports 61
  cases identical while checking nothing. Every frozen LIST needs this; the
  `notif-bell` equivalent is `MAX > 0`.

  Remaining from the cheap set: `conn-filters` (285 lines), `dashboard-wiring`
  (415). `dash-coverage`, `notes-rules`, `orphan` already pass without a
  reference and need no work.

- 3s. **`conn-filters-check` CONVERTED — 57 gates, no golden.** Two live-source
  assertions guarded, read routed. Acceptance: turning either exclusivity
  condition into `if (false)` fails without the reference.

  **THE MUTATION-QUALITY RULE, now costing real time — three bad mutations in two
  ticks.** A mutation is only evidence if it (a) APPLIES — the anchor exists,
  (b) COMPILES, and (c) changes what the gate actually observes.

  - An anchor that does not match renames nothing (`usernotify`, `collector-coverage`).
  - **A mutation that does not compile makes the gate exit non-zero from an
    esbuild CRASH.** That reads identically to a detection and is not one. Grep
    the output for `esbuild`/`Node.js v` before calling a mutation caught — this
    is the same error corrected earlier for the golden-corruption tests.
  - A mutation changing only internal state passes correctly if the gate observes
    DOM state. That is the gate's scope, not a hole.

- 3t. **A REAL GAP IN `conn-filters-check`, recorded rather than fixed.** It checks
  observable DOM state — label, select value, select class — but never the
  filtered LIST contents. A port that clears the select while leaving
  `filteredBySrc` set passes, and would show a list still filtered by a client the
  UI says is not chosen. **Verified pre-existing** against the original gate from
  git with the reference present, so the conversion did not cause it. Widening
  coverage is separate work from converting; do it deliberately or not at all.

- 3u. **`dashboard-wiring-check` CONVERTED — 58 gates. THE CHEAP SET IS DONE.**
  One guarded block (does the live app still register a handler for each card's
  event — a staleness check against an upstream that no longer moves).
  Acceptance: commenting the `talkers:update` and `netwatch:update` subscriptions
  each fail without the reference, verified not to be esbuild crashes.

  The nine gates classified "reads the reference but executes nothing from it"
  are resolved: six converted (`grid-wiring`, `collector-coverage`,
  `traffic-pick-persist`, `usernotify-form`, `conn-filters`, `dashboard-wiring`),
  three (`dash-coverage`, `notes-rules`, `orphan`) already passed without a
  reference.

  **What is left in item 3 is the hard category only**: gates that EXECUTE lifted
  code (`new Function` / `vm`), which is where the `grid-*` recipes apply and
  where each conversion costs a tick or so.

- 3v. **`lan-wan-check` CONVERTED — 59 gates.** Lift and its validity assertions
  guarded; 8 live runs frozen. Baseline: gate failures 65 → 60, total 186 → 181.

  **Reusable move:** the believability check (`new Set(Object.values(CASES).map(
  liveRun))`) was re-running the live handler purely to count distinct outputs.
  Rebuild it from the values the comparison loop ALREADY collected — it then works
  without a reference, needs no second recording per case, and additionally
  catches a golden flattened to one repeated value, which the original could not.
  Any gate with a "the corpus must produce more than one distinct X" check has
  this shape.

  **THE MUTATION TRAP, THIRD VARIANT: the anchor landed in a COMMENT.** A
  whole-file `replace(needle, …, 1)` took line 277 — prose describing the code —
  instead of line 283, the code. Same for `ndWanIp`, first mentioned in a comment
  11 lines above its use. Both applied, both changed nothing, and the gate was
  right to pass. **Apply mutations to a NAMED LINE and print the resulting line.**
  Combined with the compile check, that is the acceptance procedure:

      line-targeted → print the mutated line → grep output for esbuild/Node.js v
      → only then read the exit code

- 3w. **THE ACCEPTANCE PROCEDURE IS NOW A TOOL — `tools/gate-conversion/accept.py`.**

      python3 tools/gate-conversion/accept.py <gate-name> [file:line:old:new ...]

  It runs all four tests and refuses to call a mutation caught unless it landed on
  the NAMED LINE, changed that line, did not crash the build, and failed. Passing
  no mutations reports NOT ACCEPTED.

  **Use it for every remaining conversion.** Three of the first six hand-run
  mutations this session were misread — missing anchor, comment anchor, build
  crash — and all three looked like passes. The tool was self-tested by feeding it
  each of those three shapes plus a real pair; it rejects the three and accepts
  the real ones.

- 3x. **Converted this tick: `traffic-pick-check` (7 recordings),
  `fw-tabs-check` (4). 62 gates.** Both ACCEPTED by the harness.

  **The harness earned its keep twice more, and had two bugs of its own — both
  found by it disagreeing with a result:**

  - `crashed()` matched `Node.js v\d`, the footer printed on ANY uncaught throw
    including the AssertionError a gate raises when it CATCHES a mutation. Real
    detections were reported as build crashes. An assertion failure is now a
    detection; only esbuild/SyntaxError/module-load failures are crashes.
  - Mutations were ONE colon-joined argument, so any mutation text containing a
    colon was mangled — `socket.emit('firewall:tab', tab)` was the first, and it
    presented as a BUILD CRASH. A bad argument disguised as a gate result. They
    are now groups of three: `<file:line> <old> <new>`.

  **Both bugs made the harness LESS trusting, never more** — it reported passes as
  failures, not the reverse. That is the right direction for this tool to fail in,
  and worth preserving if it is changed.

- 3y. **`view-preset-check` CONVERTED — 63 gates.** 9 recordings; believability set
  rebuilt from the loop's values.

  **A batch patch to a helper does not reach the checks written beside it.**
  `slice()` had the empty-source guard from the earlier batch and a comment naming
  `L.liveSource` — but the read was never routed and a `deriveSrc` assertion sat
  outside the helper, so the gate still died at module scope. Expect this on any
  gate touched by that batch: the comment claims more than the code does.

  **HARNESS BUG 3, the worst kind so far: false confidence.** With test 2 failing,
  the gate throws under EVERY mutation, so all of them report as caught —
  equivalent ones included. A list-reorder mutation read as caught while the gate
  was dying at module scope, then correctly read as NOT caught once it passed.
  **The harness now skips test 3 when test 2 failed** rather than drawing a
  conclusion from it. That reorder is an equivalent mutant: the preset comparison
  is exact, so at most one named preset matches and order cannot matter.

- 3z. **THE LIVE-SOURCE READ IS NOW ROUTED IN ALL 35 REMAINING BATCH GATES.**
  Every one carried the empty-source guard — several with a comment naming
  `L.liveSource` — while still reading via `fs.readFileSync`, so they died of
  ENOENT before reaching the guard their own comment described. Swept for exactly
  that mismatch after `view-preset-check` turned out to have it.

  Verified behaviour-preserving: **all 35 still pass with the reference**, which is
  the property that matters, since `liveSource` returns identical content when one
  is present. Two needed a second edit the pattern missed — `access-summary` reads
  `app.js` again inside a helper, `networks-card` reads `index.html`.

  **Without the reference, ENOENT failures are now ZERO.** All 33 that still fail
  do so for the one remaining reason: their live runs are not frozen. That is now
  the ONLY work left on each of them, which makes the rest of item 3 uniform.

- 3aa. **`tools/gate-conversion/guard-lift.py`** — guards a gate's lift-validity
  assertions by EVIDENCE: run without a reference, read the assertion that fired,
  guard that one, repeat until the gate fails for a non-assertion reason.

  **The rule that makes it safe was learned by it doing the wrong thing.** Its
  first version guarded whatever fired, and took out four BELIEVABILITY assertions
  in `iputil-card` in one run — the gate would have gone green having stopped
  checking that a gauge renders at all. It now guards ONLY a question about the
  SOURCE TEXT (`.includes(`, `.indexOf(`, `src`/`Src`/`body`, or the `\w+At`
  anchor-index convention) and refuses anything else, because a believability
  assertion has to be RE-AIMED at the port and that is a judgement it cannot make.

  So a refusal is the tool working. Partly-guarded gates left for hand
  finishing: `iputil-card`, `physports-card`, `wireless-cards`, `networks-card`.
  Guarded through and needing only freezing: `connflow-card`, `routing-cards`.

- 3ab. **THE GOVERNING RULE, now stable across ~20 conversions.** Every piece of a
  gate that touches the reference is exactly one of three things, and the wrong
  treatment is silent in all three directions:

  | What it is | Treatment | If treated wrongly |
  |---|---|---|
  | A question about the SOURCE TEXT — "is the handler still at this anchor", "did the slice keep its element" | **GUARD** with `LIFT.hasReference(ROOT)` | Gate dies at module scope, or (if frozen) records a meaningless string |
  | A VALUE lifted from the source that the port comparison consumes — `MAX_ALERTS`, `LIMITS`, `esc()`, `COLS`/`ROWS`, a case table | **FREEZE** with `G.value`, plus a sanity assertion on the recording | Guarding leaves it undefined and makes every comparison vacuously true |
  | A BEHAVIOUR assertion — "a tooltip is built", "the centre draws the total", "a missing svg draws nothing" | **RE-AIM at the port** | Guarding silently deletes a real check; the gate goes green having stopped looking |

  `guard-lift.py` does the first automatically and REFUSES the other two, which is
  why its refusals are the tool working. The freeze cases always want a
  companion assertion on the RECORDING (`MAX > 0`, `LIMITS.sources > 0`, "the
  recorded esc() definition is not one") — without it a corrupted or empty golden
  makes the gate report full agreement while comparing nothing.

- 3ac. **Converted: `iputil-card` (17), `physports-card` (31). 69 gates.** Both
  ACCEPTED. Baseline: gate failures 60 → 50, total 181 → 171.

  **`physports-card` is the argument for the re-aim rule.** It carried FIVE
  live-only assertion blocks, two of them load-bearing:

  - the **ToDo #16 XSS regression guard** — a quote in an interface name must stay
    escaped in the `title` attribute so `onmouseover` cannot become one;
  - the **deliberate `dcEsc`/`esc` split** — the label leaves the quote alone and
    the title does not, pinned so nobody "fixes" it.

  Both were aimed at the LIVE card. Guarded or dropped they would have vanished
  silently, and the gate would still have reported 31 cases identical. Verified
  after re-aiming that swapping the two escapers is caught in both directions.

  One block's own comment said "asserted on the live side" — which was exactly
  the thing to change, not a reason to leave it.

- 3ad. **A GATE THAT COULD NOT REPORT FAILURE — found and fixed.**
  `sched-runs-check` calls an async `main()` bare. An AssertionError inside it
  became an unhandled rejection the process swallowed: **exit 0, no output at
  all.** Found only because guarding the gate made an assertion fire and the run
  still "passed".

  The gate is otherwise sound — with a reference it catches a real port mutation
  (exit 1, "1 of 9 cases differ"). The defect is narrow and total: any failure
  inside `main()` is invisible. Fixed with `main().catch()` in the three gates
  calling an async main with no catch — `sched-runs`, `reports-latch`,
  `reports-tabs` — each proved by forcing a throw.

  **`guard-lift.py` had called it a pass**, deciding on exit code alone. A gate
  whose comparisons all sit behind the guarded thing exits 0 having compared
  nothing, and prints nothing because it never reaches its summary. It now demands
  OUTPUT and the same counts the gate prints WITH a reference.

  **Worth stating plainly: exit 0 has now been wrong three separate ways this
  session** — a crash exiting non-zero read as a detection, a broken gate making
  every mutation look caught, and now a swallowed rejection reading as a pass.
  Judge a gate by what it SAYS it checked, not by its exit code alone. That is
  also the argument for the gate census.

- 3ae. **TWO MORE GATES COULD NOT REPORT FAILURE, and there is now an audit.**
  `sched-remove` and `sched-run` had the same defect as `sched-runs` from a
  different cause: a blanket `process.on('unhandledRejection', () => {})`.

  **The handler is legitimate in intent** — the live Remove/Send handlers `fetch`
  without a `.catch`, so the page genuinely rejects with nobody listening and the
  gate must survive it. It also swallowed the gate's own assertions. Narrowed so
  an AssertionError is fatal and everything else still "logs and continues".

  **Static reading flagged 16 gates; exactly 2 swallowed.** Testing beat inferring
  eightfold, which is why `tools/canfail-audit.js` INJECTS a throw rather than
  grepping. It covers the 26 gates with an async entry point or such a handler and
  fails if any exits 0. Self-tested by restoring the broken form.

  Where it sits among the other checks:

  - `canfail-audit` — would this gate tell us if it failed **at all**?
  - the gate census — did this gate check **less than it used to**?
  - `accept.py` — does this gate still catch a **port defect** without a reference?

  Each catches a failure the others cannot see, and this session produced a real
  instance of all three.

- 3af. **A FOURTH TREATMENT, from the `sched-*` family: a DECLARED DIFFERENCE.**
  `sched-remove` and `sched-run` each carry a block asserting the two sides
  DIFFER in a stated way — the live app sends a request with no router selected,
  the port returns early.

  > **Freeze it; never guard it.** The live half is what the difference is
  > declared AGAINST. Guarding deletes the only check that the difference still
  > holds. Frozen, all its assertions keep working — including "the two now AGREE,
  > so the recorded difference is stale", which still fires if the PORT drifts
  > toward the recording.

  So the table in 3ab has a fourth row: source-text question → guard; lifted value
  → freeze; behaviour assertion → re-aim at the port; **declared difference →
  freeze the live half and keep every assertion**.

  **`sched-runs` also proved a case can be lost with the difference it declared.**
  When `STATED` was emptied the CASES driving it went too, leaving the port's two
  defensive fallbacks with no coverage; both mutations survived until restored.
  Swept the repo for other emptied declared-difference tables: `sched-runs` was
  the only one. `caps-check` and `fixture-differential` have populated tables and
  assert their gaps still exist, so they are self-checking.

- 3ag. **Converted: `map-tooltip` (15), `modals` (33), `stale` (112). 75 gates.**

  **A FIFTH MUTATION TRAP — the mutation was in the WRONG MODULE.** I mutated
  `dashboard-card-map.ts` for a gate that bundles `connections-worldmap.ts`. It
  applied, compiled, and changed nothing the gate loads, so it read as NOT CAUGHT
  and looked exactly like a coverage gap.

  Found only by reading the GOLDEN, which recorded `display: 'none'` for the very
  case in question — proving the gate did cover the behaviour and the mutation was
  at fault. **`accept.py` cannot catch this**: it verifies the line changed, not
  that the file is the one under test.

  > Before choosing a mutation, run `grep -n "esbuild" -A2 tools/<gate>.js` and
  > mutate the module it names. A `grep` for the element id finds whichever file
  > mentions it, which is frequently a different card.

  Full list of ways a mutation can prove nothing, all seen this session: anchor
  missing; anchor in a COMMENT; mutation does not COMPILE; mutation in the wrong
  MODULE; mutation is EQUIVALENT; gate already failing so everything "fails".

- 3ah. **FREEZE THE LIFTED SOURCE, NOT THE OUTPUTS, where a gate lifts EXECUTABLE
  code.** `traffic-buffer-check`: **7 recordings for 84 comparisons**, because the
  six lifted lines and the `windowedPoints` body are frozen and `liveCtx` still
  compiles and RUNS them without a reference.

  Strictly better than freezing outputs, wherever it applies:

  - the recording is a fraction of the size (7 against 84);
  - the live half still EXECUTES against inputs, so the comparison stays a real
    execution rather than a lookup — a new case added later still gets a live
    answer, which a frozen-output gate cannot do;
  - assertions on the lines themselves then validate the RECORDING, so
    `assert.match(capLine, /1800/)` stays unguarded and keeps earning its place.

  **Applies to any gate that builds its live half with `vm.runInContext` or
  `new Function` over lifted text** — which is most of the remaining hard set.
  Check for that shape BEFORE reaching for `G.live` per case.

- 3ai. **`freeze-src.py` applied to 14 gates, freezing 37 lifted sources.** Six
  now pass both ways; the rest need their remaining live calls frozen.

  **The tool broke five gates before it worked**, in two ways, and BOTH were
  caught only by re-running each touched gate WITH the reference:

  - it emitted `assert.ok` into gates that never require `node:assert`;
  - it found its insertion point by scanning for the next `;`, which lands inside
    a multi-line IIFE body.

  > **After any batch edit, re-run every touched gate against the REFERENCE before
  > looking at anything else.** A conversion tool that breaks the gate is
  > indistinguishable, from the without-reference side, from a gate that simply
  > is not converted yet — both just fail. The with-reference run is what separates
  > them, and it is the cheap check.

  That is the fifth tool this session to have a bug found within minutes of first
  use, and the third whose bug would have gone unnoticed without a check that
  deliberately looks the other way.

- 3aj. **`freeze-src.py` has plateaued — convert the last 12 by hand.**
  It detects each gate's own lifter functions, matches multi-line calls, handles
  the bare `const X = src.slice(...)` form, and guards five anchor-assertion
  shapes. Each of the last two widenings bought exactly two gates.

  The remaining 12 (`bandwidth-chart`, `city-picker`, `login-page`, `map-fs`,
  `map-zoom`, `preflight`, `principal-forms`, `router-form`, `settings-tabs`,
  `settings-validate`, `setup-overlay`, `upgrade-dialog`) each fail on a bespoke
  anchor shape. **Reach for the joined-result trick, not a fourteenth regex:**
  freeze what the `vm`/`new Function` actually executes, which covers every lift
  inside it whatever form each takes.

  Status: gate failures 14 → 12 without a reference; nothing broken with one.

- 3ak. **ITEM 3 IS COMPLETE — all 136 gates pass with AND without the reference.**

  `settings-validate` was the last and the only one that REQUIRED a reference
  module rather than lifting text. What it takes from `pages.js` is one data
  table, so `SETTING_KEYS` is frozen and `Pages` rebuilt from it. The
  reconstruction is self-checking: if the live block ever reaches for another
  field, the with-reference run fails immediately.

  | | with the reference | without it |
  |---|---|---|
  | gates | 136 run, **0 failed** | 136 run, **0 failed** |
  | audits | 38 run, 0 failed | 38 run, 16 failed |
  | generators | 110 checked, 0 stale | 110 checked, 99 stale |
  | census | 163 tracked, 0 shrunk | 163 tracked, 2 shrunk |

  **What is left is items 5 and 6, and item 5 may be a non-problem.** A generator
  whose whole job is to RUN the reference and regenerate a corpus cannot check
  itself without one — that is not staleness, it is the tool being asked a
  question that no longer exists. Decide that deliberately before "fixing" it.

  The 16 audits need the same treatment the gates got, and the three treatments in
  3ab plus the declared-difference rule in 3af should cover them.
- 6a. **`stub-audit` was passing while checking nothing** — 0 injected definitions
  without a reference, 37 with one. Its `liveFns` set is a lifted value it FILTERS
  on, so an empty set dismissed every injection as "a name the live app does not
  have". Frozen with a floor. **Found by comparing reported COUNTS, not exit
  codes** — the third time that check has found a gate or audit going quietly
  vacuous.

- 6a2. **`tools/vacuity-audit.js` — the check that should have existed all along.**
  Runs every gate both ways and compares what each SAYS it checked. **120 gates
  print a count and all check the same either way.**

  The census compares a gate against its own PAST; this compares the two
  CONDITIONS, which is where the conversion's risk lives. Every instance found by
  hand this session was passing at the time: `grid-drag` 76/67, `sched-runs`
  printed nothing, `stub-audit` 37/0.

  **Run this after converting anything.** It is the cheapest possible answer to
  "did I just delete a check", and three of this session's worst findings would
  have been one command instead of a lucky read.

- 6b. **THREE AUDITS REST ON PREMISES THE CUTOVER RETIRED. Operator decision.**

  - `cutover-premise-audit` asserts that the facts behind the REMAINING CUTOVER
    BLOCKERS are still true. Every blocker is closed and the cutover is done, so
    its premise has expired — exactly the defect its own header warns about.
  - `coexistence-audit` asks which `/data` files this port writes that the RUNNING
    NODE APP caches. Node is stopped.
  - `lift-audit` checks that `live-renderer.js` can still lift each page it
    claims — and live-renderer lifts FROM the reference, so it has no subject
    without one. It is a declared exception in `vacuity-audit` meanwhile.

  Both are LEFT IN PLACE and flagged. Retiring a check is the operator's call, and
  a deleted audit reads identically to a lost one. The options are: delete them,
  or keep them as records under `docs/port-history/`. Do not convert them — there
  is nothing left for either to ask.

- 6c. **TWO GO GATES BECOME PERMANENT SKIPS — not yet converted.**

      internal/collect/drift_test.go    TestProplistsMatchTheLiveCollectors
      internal/resource/options_test.go (the resources.js gate)

  Both `t.Skipf` when `MIKRODASH_SRC` is absent. CLAUDE.md already names this
  exact hazard — *"without it that test SKIPS, which is a gate that never runs"* —
  and it was written while the reference was merely sometimes absent. It is about
  to be always absent.

  **This is the `pagechange-audit` class, on the Go side.** The question is still
  live (do the port's proplists match what the live collectors asked for?); only
  the data source is going. The fix is the same: freeze the extracted live
  proplists into `testdata/`, read them always, and — while a reference is still
  present — assert the frozen copy still matches it, so the recording cannot go
  stale unnoticed.

  **BOTH DONE.** `drift_test.go` records the 19 live proplists in
  `internal/collect/testdata/live-proplists.json`; `options_test.go` records the
  whole of `resources.js` (56 KB) in `internal/resource/testdata/`. Each
  re-derives and compares its recording while a reference is present, so neither
  can go stale unnoticed.

  **The two chose different units deliberately.** The proplist gate freezes the
  DERIVED lists, because its extraction is a few regexes. The resources gate
  freezes the WHOLE FILE, because its parser — walking declarations, resolving
  option sources, reading every attribute — is a large part of what the gate
  proves, and freezing derived fields would retire the parser along with the
  reference. A parser nothing runs is one nobody can trust when it is next needed.

  Verified: 19 and 20 subtests respectively run with ZERO skips without a
  reference, port mutations still fail, and corrupted or truncated recordings are
  refused.

