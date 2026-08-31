# Handover: replacing the live repo's contents with this port

Written 2026-08-31, for whoever performs the copy. **The author of this file
cannot perform it**: `.claude/settings.json` denies every path under
`/DATA/Backup/Projects/MikroDash` except `ToDo.md`, and that boundary is
deliberate. This is a briefing, not an instruction to route around it.

## The decisions already made

- **Tree only, no port history.** The port's 106 commits do not come across. The
  live repo's 361 commits and its tags are preserved; the port lands as one
  cutover commit on `main`.
- **The Node implementation is removed in the same commit.** `src/`, `public/`,
  `test/` go. They stay reachable through the live repo's own history and the
  `v0.7.40` tag, so nothing is lost — but see the one-way door below.

## The one-way door, and the verification that must precede it

`tools/lib/lift.js`'s `referenceAbsent()` decides on ONE fact:

    absent = !fs.existsSync(path.join(live, 'public', 'app.js'));

Every gate and audit in this repo compares against a **recording** of the live
implementation, and when a reference IS present each recording is re-derived and
compared against it. That is the only thing keeping a recording honest.

**The moment `public/app.js` stops existing, that check can never run again.**
The gates degrade correctly — they drop into recording-only mode and stay green,
by design — but "are these recordings still what the live source says?" becomes
permanently unanswerable.

So it was answered immediately before the handover, and this is the record:

    reference:  v0.7.40-1-g9f9b2b6  (2 files uncommitted in the live tree)
    command:    MIKRODASH_SRC=../MikroDash sh tools/verify.sh
    result:     verify: green
                gates       136 run, 0 failed
                audits       36 run, 0 failed
                gate census 161 tracked, 0 shrunk
                generators  110 checked + 12 from the image, 0 stale
                nodecheck     7 pass, 0 fail
                Go          gofmt, vet, test ok (37 packages)
                tsc         ok

If the copy is delayed past further changes to either repo, **run it again**.
It costs about four minutes and it is the last chance.

## What must NOT cross unchanged

| path | why |
|---|---|
| `.claude/settings.json` | 61 deny rules naming `/DATA/Backup/Projects/MikroDash/**` as a forbidden EXTERNAL path. Inside that repo they would block it from editing itself. Rewrite or drop before the first agent session there. |
| `.serena/` | Tooling cache and a project graph rooted at this path. |
| `.geo-cache/` | Does not exist; listed so nobody recreates it. The real DB-IP download works. |

## What crosses and is easy to mistake for junk

| path | size | what it is |
|---|---|---|
| `testdata/golden-gates/` | 25 MB | The recordings. **This is the verification.** Deleting it does not "clean up" the repo, it removes every gate's ability to check anything. |
| `internal/collect/testdata/live-proplists.json` | small | Same, for the Go proplist drift gate. |
| `internal/resource/testdata/live-resources.js` | 56 KB | Same, for the resources gate. It is a copy of the live `resources.js` and will look like leftover Node. It is not. |
| `nodecheck/` | 312 KB | Three remaining differential tests. Two record their live half; all three run without a reference. |
| `docs/port-history/` | 1.3 MB | The record, including `retired/` — five checks whose question the cutover removed, kept rather than deleted. |

## After the copy: what to expect

- `sh tools/verify.sh` stays green. The 105 generator `--check` runs report as a
  SKIP and say why: they regenerate FROM the reference, and there is none.
- `MIKRODASH_SRC` no longer needs setting. Leaving it set to a path that no
  longer holds `public/app.js` is harmless — `referenceAbsent` keys on the file,
  not the directory.
- The build needs **Go only**. `web/package.json` retains `typecheck` (`tsc
  --noEmit`) and its two devDependencies: `esbuild`, which the 132 frontend gates
  use to bundle their port halves, and `typescript`. Neither is needed to produce
  a build.
- Verified end to end on 2026-08-31: `docker build` from the committed
  Dockerfile, geo downloaded fresh, 195,390 places generated, image 180 MB with
  no `node` or `npm` binary, `app.js` 665,375 bytes — byte-identical to the last
  Node-produced build. Run against a copy of the live `/data`, it served the
  dashboard, connected to all three routers, and rendered live traffic, system
  gauges, 321 connections and seven days of ping history.
