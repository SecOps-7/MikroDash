# Port history

MikroDash was a Node.js application until 2026-08-30, when the Go + TypeScript
rewrite replaced it (`CUTOVER.md`). These are the working documents from that
rewrite, kept because they are the evidence behind decisions the code still
carries — not because they describe how the app works today. For that, read the
top-level `README.md`, `CLAUDE.md` and `AI_CONTEXT.md`.

| file | what it is |
|---|---|
| `PLAN.md` | the phase sequencing, the gates and the kill criteria, with the evidence each came from |
| `PORT-QUEUE.md` | the long record of decisions and why they were made — the right shape for "why was this done that way" |
| `LOOP.md` | the ordered work list the autonomous loop read each tick, and the account of what each item cost |
| `test-results.md` | the RouterOS 7.24 hardware results that decided `internal/routeros` would adapt a library rather than hand-write framing |

**Two of these earned their keep by being wrong in public.** `PORT-QUEUE.md`
records blockers that were closed and still read as open, and `LOOP.md` records a
heading that said "ONE HALF OPEN" for a day after every item beneath it was
struck through. Both are left as written. A document that quietly loses its
mistakes teaches nothing about how they happened, and this project's most
expensive recurring defect was a claim nobody re-measured.
