# go-routeros v3.0.1, patched for MikroDash

This is `github.com/go-routeros/routeros/v3` at v3.0.1 (MIT, see `LICENSE`), used
through a `replace` in the repository's `go.mod`. Only the library's non-test
sources are kept, and its `go.mod` loses the `require` block that only those
tests needed (testify).

## Change 1: `RunArgsContext` registers its tag before sending

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

## Change 2: the debug trace masks credentials

`RunArgsContext` and `ListenArgsQueueContext` log every word they send at slog's
Debug level. MikroDash installs a Debug handler when the "Debug Logging
(ROS_DEBUG)" setting is on, and with it on, every write that carries a secret put
that secret in the container log in clear text: backup and restore passwords,
RouterOS user passwords, Wi-Fi passphrases, WireGuard keys and PPP secrets. The
login itself was never logged, because MikroDash installs the handler after
logging in. Code scanning alert #158.

Both calls now log `redactSentence(...)` (`redact.go`), which replaces the value
of any attribute whose key matches the credential rule `tools/capture-fixtures.js`
uses. The key stays, so the trace still reads `=password=***`. The words sent to
the router are unchanged.

`TestTracingNeverLogsACredential` in `internal/routeros` says whether a
replacement library still needs this.

## Change 3: a listener on an uncancellable context starts no watcher

`ListenArgsQueueContext` started a goroutine per listener that waited on
`<-ctx.Done()` and then cancelled the connection's reader. `ListenArgs` passes
`context.Background()`, whose `Done()` is nil, so that goroutine could never
wake: every stream opened parked one for the life of the process. MikroDash
opens its streams through `ListenArgs`, and a stream reopened every ten seconds
parked about 8,600 a day per menu.

The watcher is now started only when `ctx.Done()` is not nil, which is exactly
the case in which it can ever run. Behaviour for a cancellable context is
unchanged; note that cancelling one still cancels the whole connection's reader,
which is why MikroDash never passes one.

`TestAStreamLeavesNoGoroutineBehind` in `internal/routeros` says whether a
replacement library still needs this.

## Change 4: the tag counter cannot be unaligned (2026-10-01)

`Client.nextTag` is updated with 64-bit atomics. On 32-bit ARM an `int64` is
only 4-byte aligned, and a 64-bit atomic on an unaligned address PANICS with
`unaligned 64-bit atomic operation` rather than merely being slow. The field sat
after a run of mixed-size fields, so on linux/arm it landed at a 4-mod-8 offset
and the first command the client sent took the whole process down. Reported by
mvdteam against the arm/v7 image running in a container on a hAP ac3, where it
fired on "Test API Connection" every time (issue #146); fixed by
TastyHeadphones (#147).

It is an `atomic.Int64` now. That type embeds `align64`, which the compiler
special-cases to force 8-byte alignment WHEREVER the field sits - so the type is
the fix, and its position at the top of the struct is belt and braces.

`TestTheTagCounterCannotBeUnaligned` in `internal/routeros` says whether a
replacement library still needs this. It asserts the field's TYPE rather than
its offset, because `unsafe.Offsetof(nextTag)%8 == 0` is a tautology once the
type carries `align64` - measured by moving the field back to its original slot
and watching that assertion still pass - and on amd64 it holds for a plain
`int64` too, so it could not have failed for the original bug on the only
architecture the suite runs on.
