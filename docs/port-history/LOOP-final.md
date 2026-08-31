# LOOP.md — the work list

The short, ordered list of what is left. An autonomous tick reads this FIRST.

**The job: make every gate assert Go/TypeScript behaviour without needing
`../MikroDash`.** The rewrite is finished and cut over; the Node repo is now a
frozen reference that will eventually be deleted or moved. Until the gates stop
depending on it, this repo cannot be verified by anyone who does not have it —
which is already true for any contributor cloning it today, while
`CONTRIBUTING.md` tells them to run `sh tools/verify.sh`.

**Every count below names the command it came from. Re-measure rather than
trust it** — that rule exists because this file has carried stale numbers before.

---

## THE JOB IS DONE

Measured 2026-08-31, `sh tools/verify.sh` both ways:

| | with `../MikroDash` | without it |
|---|---|---|
| gates | 136 run, 0 failed | 136 run, 0 failed |
| audits | 36 run, 0 failed | 36 run, 0 failed |
| gate census | 161 tracked, 0 shrunk | 161 tracked, 0 shrunk |
| generators | 110 + 12 checked, 0 stale | skipped, and says so |
| nodecheck | 7 pass, 0 fail | 7 pass, 0 fail |
| Go + tsc | ok | ok |
| **total** | **green** | **green** |

**`sh tools/verify.sh` is green on a clone with no reference repo.** That was the
job. `../MikroDash` is now optional: with it present, every recording is
re-derived and compared against the source it claims to describe, which is the
only thing that keeps a recording honest.

### The six that were retired rather than converted

Five checks moved to `docs/port-history/retired/`, with the reasoning, because
the cutover removed the question each asked. A sixth, `conn-tables`, was on that
list by mistake — it turned out to have a port side — and is converted and live.

**The cost is stated there and is real:** `nodecheck` fell from 129 assertions to
7. The 122 that went all asked whether a fixture reproduced the LIVE collectors,
and there is no live collector left to ask. Their result is the committed
fixtures, which `internal/collect` compares against on every run.

## The finished work

The gate conversion — 136 gates, 39 audits, both Go gates and two `nodecheck`
files — is recorded in **`docs/port-history/gate-conversion.md`**, gate by gate,
with the reasoning. The durable rules are in `CLAUDE.md` under "Gate conversion".

Nothing in that record is a queue. It is there because the reasoning is not
re-derivable from the diffs.

## 4. `nodecheck/` — ANSWERED, and it is BOTH

Measured, not argued: the deciding question is whether a test has a PORT side.

| test | port side? | disposition |
|---|---|---|
| `topojson-decode` | yes | **CONVERTED** — the atlas was already the port's own; the oracle's output is recorded |
| `reports-presets` | yes | **CONVERTED** — three lifted live sources recorded, live halves still run |
| `conn-tables` | no | **retire** — no port half to compare against |
| `dns-fingerprint` | no | **retire** — no port half to compare against |
| `fixture-differential` | no | **retire** — replays fixtures into the LIVE collectors |

**The three without a port side cannot be converted.** Freezing a check that has
only a live half produces a recording compared against itself. Their result is
not lost: it is the committed FIXTURES, which the Go differential gate consumes
directly.

Retiring them is an operator decision, like the three audits in 6b — the same
question, and worth deciding once for all six.

## 4b. (was: convert or retire, deliberately)

`nodecheck/` replays fixtures into the REFERENCE collectors. That is its whole
purpose, so unlike the gates it cannot be converted: without the Node collectors
there is nothing for it to run.

It is how a fixture is proved faithful before the Go side is asked to reproduce
it, and the fixtures are already captured and committed.

- 4a. Decide: retire it, or keep it as a reference-only suite that SKIPS loudly
  when `MIKRODASH_SRC` is absent and is excluded from the failure count.
  **A skip must be reported as a skip**, not folded into green.
- 4b. Whichever is chosen, `KNOWN_INCOMPLETE` and its "the gap still exists"
  assertion must survive, or be deleted with a reason.

---

## 5. Generators — DONE 2026-08-31

- 5a. **DONE.** `verify.sh` reports the generator `--check` runs as a SKIP when
  the reference is absent, and says why. Measured before deciding: **67 of the
  generators RUN the reference and 36 read it**, so without one there is nothing
  to regenerate FROM and every generator reports stale — a number that means
  nothing. The corpora are committed and the gates consume them directly; what
  disappears is the ability to re-derive them.
- 5b. **DONE.** With the reference present nothing changed: `generators: 110
  checked, 0 stale`, plus `container generators: 12 checked from the image`.
  Verified by running both ways.

---

## 6. Audits — DONE 2026-08-31, except the three in the header

`sh tools/verify.sh` → `audits: 39 run, 0 failed`, both with and without the
reference apart from the three flagged above.

- 6a/6b. **DONE.** 16 needed work; 13 were converted and 3 ask a question the
  cutover removed. Each is recorded in
  `docs/port-history/gate-conversion.md`. The distinction that decided every one:
  **an audit usually needs only the FACTS it derived from the reference, not the
  source** — freeze the small thing — **unless it EXECUTES the live code**, as
  `schema-audit` does with the live `MIGRATIONS`.
- 6c. **DONE.** Both Go gates that would have become permanent skips now read
  recordings and run on any clone.

---

## 7. Close it out — 5 of 6, the last is the header's decision

- 7a. **`MIKRODASH_SRC=/nonexistent sh tools/verify.sh --no-docker` → 5 failing**,
  all of them the six flagged files or a consequence. Not green, and cannot be
  until that decision is made.
- 7b. **DONE.** With the reference: `verify: green`, `gate census: 164 gates
  tracked, 0 shrunk`.
- 7c. **DONE.** `CLAUDE.md`, `CONTRIBUTING.md` and `verify.sh`'s own header no
  longer tell a contributor they need the reference — they said so until
  2026-08-31, months after it stopped being true of most of the repo and hours
  after it stopped being true of any of it.
- 7d. **DONE.** `tools/doc-claim-audit.js` → 5 of 5 claims re-measured and true.
- 7e. **NEW, and the only thing left:** decide the six files in the header, then
  re-run both sweeps. Nothing else is blocked.

---

## The standing per-tick protocol

1. Take the top unfinished item.
2. Do it against the acceptance tests in item 1 — all three, including the
   mutation.
3. `MIKRODASH_SRC=../MikroDash sh tools/verify.sh` must be green, AND
   `MIKRODASH_SRC=/nonexistent sh tools/verify.sh` must have fewer failures than
   the tick before. Record both numbers.
4. Update this file, `Changes.md`, and any count that moved.
5. Commit. Redeploy only if the app changed — this work does not touch it.
