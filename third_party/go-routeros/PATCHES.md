# go-routeros v3.0.1, patched for MikroDash

This is `github.com/go-routeros/routeros/v3` at v3.0.1 (MIT, see `LICENSE`), used
through a `replace` in the repository's `go.mod`. Only the library's non-test
sources are kept, and its `go.mod` loses the `require` block that only those
tests needed (testify).

## The one change: `RunArgsContext` registers its tag before sending

Upstream `run.go` writes the command, and only then adds its tag to the map the
async loop routes replies by. The loop discards a sentence whose tag is not in
that map ("cannot find tag for this sentence, ignore"), so a reply that arrives
in between is lost and the call waits for ever. `ListenArgsQueueContext` does
not have the gap: it holds the lock across its write.

Measured on 2026-09-13 against a local fake router, with eight callers issuing
commands at once and no MikroDash code involved: 38 of 1,600 commands never
returned. On a live hAP ax3 each lost command held one of MikroDash's eight
per-router command slots, until every poll on that router had stopped.

The tag is now registered first, and removed again if the write fails. No network
write happens under the client's lock.

v3.0.1 was the latest release on 2026-09-13. Remove this directory and the
`replace` once an upstream release carries the fix;
`TestNoReplyIsLostBeforeItsTagIsRegistered` in `internal/routeros` says whether
the one in use does.
