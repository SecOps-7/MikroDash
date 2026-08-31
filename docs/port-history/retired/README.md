# Retired checks

Five checks, retired on 2026-08-31 because the cutover removed the question each
one asked. They are **kept rather than deleted**: a deleted check reads exactly
like a lost one, and this repo's most expensive recurring defect is a premise
that expires while nothing fails.

None of them could be converted. Every other gate and audit now compares against
a recording of the live implementation — but that only works where there are two
sides to compare. **A check with only a live half freezes into a recording
compared against itself**, which is worse than no check because it still gets
counted.

| file | what it asked | why it stopped having an answer |
|---|---|---|
| `cutover-premise-audit.js` | are the facts behind the remaining CUTOVER BLOCKERS still true? | every blocker is closed; the cutover happened |
| `coexistence-audit.js` | which `/data` files does this port write that the RUNNING NODE APP caches? | Node is stopped |
| `lift-audit.js` | can `live-renderer.js` still lift each page it claims? | live-renderer lifts FROM the reference |
| `dns-fingerprint.test.js` | what does the live DNS collector consider a change worth pushing? | no port half to compare against |
| `fixture-differential.test.js` | do the fixtures replay faithfully into the LIVE collectors? | no port half to compare against |

## A sixth was nearly retired by mistake

`conn-tables.test.js` was on this list until it was moved and its requires
fixed — at which point it turned out to compare four data tables against
`web/src/pages/connections-map.ts`. **It has a port side**; the classification
that put it here searched source text for `web/src` and missed a path built as
`path.join(__dirname, '..', 'web', 'src', …)`.

It is converted and live: the four live tables are recorded in
`nodecheck/testdata/conn-tables-live.json`. Changing one `CC_NAMES` entry in the
port fails it without a reference.

The lesson is the cheap one — **a grep for a path string does not find a path
that is built** — and the reason it was caught is that moving a file forces you
to fix its requires, which forces you to run it.

## What this cost, stated plainly

`nodecheck` went from **129 passing assertions to 7**. The three retired test
files held nearly all of them, and they are gone from the sweep.

That is the honest price and it was accepted deliberately: those 122 assertions
all asked whether a fixture faithfully reproduced the LIVE collectors, and there
is no longer a live collector to ask. Keeping them would have meant a suite that
fails on every clone, or one frozen into comparing a recording with itself.

## What was NOT lost

**The two remaining `nodecheck` files' result is the committed fixtures.** Their job was
to prove a fixture faithful to the live collectors *before* the Go side was asked
to reproduce it. That proof was performed; the fixtures under
`testdata/fixtures/` are its output, and `internal/collect` compares the Go
collectors against them on every run. Retiring the proof does not un-prove it.

**`cutover-premise-audit` recorded its own obsolescence in advance.** Its header
said a blocker "that has quietly become false is work deferred for no reason".
That is what happened to the audit itself — the blockers closed, and the audit
went on asserting facts about a decision already taken.

**`lift-audit` guarded a two-step comparison** (`live-renderer.js` writes a
bundle; a step-two gate consumes it). Both steps are reference machinery. If a
future need arises to compare against a lifted live page, this file is the
worked example of how to check that the lift still functions.

## If the reference comes back

These run as they always did — nothing here was rewritten, only moved:

```sh
MIKRODASH_SRC=../MikroDash node docs/port-history/retired/cutover-premise-audit.js
MIKRODASH_SRC=../MikroDash node --test docs/port-history/retired/*.test.js
```

`tools/verify.sh` discovers what to check by globbing `tools/` and `nodecheck/`,
so moving them here is what takes them out of the sweep. Moving one back is what
puts it in again.
