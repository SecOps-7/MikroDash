# Port history

MikroDash was a Node.js application until 2026-08-30, when the Go + TypeScript
rewrite replaced it. Two documents from that rewrite are kept here.
They are not kept as history: each carries knowledge the code still depends on.

| file | why it is still here |
|---|---|
| `gate-conversion.md` | the rules for what to do when a gate touches something that no longer exists, and the failures they were learned from. `tools/gate-conversion/*.py` implement them, and gates still get re-aimed. |
| `test-results.md` | the RouterOS **7.24** hardware results. `internal/collect/traffic.go` cites them, and "not reproduced" is not "never true" -- the claims are version-qualified, so the version matters as much as the result. |
| `PORT-QUEUE.md` | **62 comments in shipped code cite it by name** -- 29 in `internal/`, 26 in `tools/`, 6 in `web/src/`, 1 in `cmd/`. It is the "why" behind decisions the code still carries. |

**The rest were deleted on 2026-08-31**, when the operator asked for the port
documents that were no longer needed to go: `PLAN.md`, `LOOP.md`, `LOOP-final.md`,
`COPY-TO-LIVE.md` and the five `retired/` checks. They were the working documents
of a job that is finished -- a phase plan for phases that all shipped, a handover
for a copy that happened, and checks that cannot run because what they checked was
deleted. Each had ZERO citations outside this directory, which is what made them
safe to remove. They are in git history at `v0.8.2` and every commit before it.

`PORT-QUEUE.md` was deleted in that same pass and **put back**, because it failed
the operator's own test. "No longer needed" is not a judgement about whether the
work is finished -- it plainly is -- but about whether anything still depends on
the document, and 62 live comments do. Deleting it would have turned every one of
them into a pointer at nothing, which is the orphaned-reference defect
`tools/citation-audit.js` exists to catch and would not have caught here: those
are bare-name mentions, not the path-shaped citations it validates.

**Some of what it says is now false**, and that is the reason to read it carefully
rather than the reason to delete it. Comments citing "blocker 5" describe a
blocker that is CLOSED -- `CLAUDE.md` records that all of them are. Read it for
why a decision was made, never for what the current state is.

The active architecture work moved OUT of this directory in the same pass:
`docs/architecture-next.md`. It was re-measured against the Go tree and is being
executed, which makes it a plan rather than a record, and leaving it filed under
"port history" was what made it hard to find in the first place.
