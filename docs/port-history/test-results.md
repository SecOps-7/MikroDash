# Empirical verification of the four "protocol realities"

Every statement below is backed by a wire trace taken during this session against the three
live routers. Nothing here is inferred from reading code alone; where a thing could not be
tested, it says so instead of guessing.

## Test bed

| Label | Board | RouterOS | Identity (sha256, first 8) | Access |
|---|---|---|---|---|
| AC2 | hAP ac² | 7.24 (stable), build 2026-08-14 08:33:14 | `0a97e1ee` | read **and write** (unused test router) |
| AX3 | hAP ax³ (wifi-qcom, CAPsMAN manager, 12 wifi interfaces, 1 remote CAP) | 7.24 (stable), same build | `01bf58b6` | **read-only** |
| cAP AX | cAP ax | 7.24 (stable), same build | `e4b9757e` | **read-only** |

**All three are on 7.24. None is on 7.23**, which is the version claims 1 and 2 were originally
recorded against. That limit is stated again in each section rather than buried here.

Instruments used:

- A from-scratch raw sentence tracer (framing only, no client logic) that logs every sentence
  received with a microsecond timestamp relative to the command write, and counts `!re` /
  `!done` / `!empty` / `!trap` separately. Its length-prefix code is a copy of the port's
  `proto.go`, so framing bugs cannot mask a result.
- A byte-exact `/file/read` harness that reconstructs a file twice — once as raw bytes, once
  through the exact Node path (`Buffer.toString('utf8')` → `Buffer.from(str,'latin1')`) — and
  compares length, sha256 and per-byte diff.
- `github.com/go-routeros/routeros/v3@v3.0.1` in a throwaway module, run against the same
  hardware in both sync and async mode.
- The port's own `cmd/conformance`, unchanged.

Two files were created on the AC2 (`/system/backup/save`), read, then removed; `/file/print`
was re-read afterwards and returned to its original two entries (`flash`, `flash/skins`).
Nothing was written to the AX3 or the cAP AX. Nothing was written to `../MikroDash`.

---

## Verdict table

| # | Claim | Verdict | One line |
|---|---|---|---|
| 1 | `!empty` on a one-shot is not followed by `!done`, so a client waiting for one hangs | **DOES NOT HOLD on 7.24** | 16/16 empty replies on 3 routers sent `!done` 10–30 µs after `!empty`. Zero hangs. |
| 1b | `!empty` on a *stream* means "nothing yet" | **INCONCLUSIVE** | On 7.24 a stream sends no `!empty` at all at the start; the decisive command (`frequency-scan`) could not be run on any permitted router. |
| 2 | Multi-block reply, one `!done` per interface, on wifi-qcom | **DOES NOT HOLD on 7.24** | AX3 answered 30 clients across 8 interfaces in **one block with one `!done`**, 6/6 repeats. A return-on-first-`!done` client got 30 of 30. |
| 3 | `/file/read` needs raw bytes; a UTF-8 decode corrupts it | **HOLDS for Node — DOES NOT APPLY to Go** | The corruption is real and severe (72.85 % of bytes differ), but Go's `string(b)` does not transcode: go-routeros read the same backup byte-exactly. The README's stated *mechanism* ("preserves length") is also wrong. |
| 4 | Trailing packets arrive for tags already torn down | **HOLDS** | 2–3 sentences arrive on a cancelled tag, 2–47 ms after `/cancel`, deterministically. |
| — | `go-routeros` v3.0.1 "gets two of the four wrong" and returns wrong data | **Code reading accurate; consequence DOES NOT HOLD** | The quoted `reply.go:29-44` is verbatim and the line numbers are right, but on 7.24 the library returned complete, correct data on all three routers. |

---

## Claim 1 — `!empty` and the missing `!done`

> README: "`!empty` means 'empty result' on a one-shot but 'nothing yet' on a stream … On
> RouterOS 7.23 no `!done` follows an empty print, so a client waiting for one hangs until its
> timeout."

### One-shot half — not reproduced

Raw trace, filtered print matching nothing, all three routers:

```
### AC2 (7.24)      --> /ip/firewall/filter/print ?comment=__no_such_rule_xyz__ .tag=probe1
[   7.310ms] !empty | .tag=probe1
[   7.320ms] !done  | .tag=probe1

### AX3 (7.24)
[  23.712ms] !empty | .tag=probe1
[  23.723ms] !done  | .tag=probe1

### cAP AX (7.24)
[   1.325ms] !empty | .tag=probe1
[   1.344ms] !done  | .tag=probe1
```

`!done` follows `!empty` by **10 µs, 11 µs and 19 µs** respectively. The tracer kept reading for
a further 12 s in each case and saw nothing more.

Swept across 16 distinct empty-result shapes (two routers × eight menus — filtered `print` with a
non-matching `?` argument, and unfiltered `print` of a genuinely empty menu):

| Command | AC2 | AX3 |
|---|---|---|
| `/ip/dns/static/print ?name=__nope__` | empty=1 done=1 @4.3 ms | empty=1 done=1 @2.0 ms |
| `/queue/simple/print` | empty=1 done=1 @4.6 ms | empty=1 done=1 @1.8 ms |
| `/system/script/print` | empty=1 done=1 @3.8 ms | *(4 rows — n/a)* |
| `/ppp/active/print` | empty=1 done=1 @31.4 ms | empty=1 done=1 @14.0 ms |
| `/tool/netwatch/print` | empty=1 done=1 @21.8 ms | *(3 rows — n/a)* |
| `/interface/vlan/print ?name=__nope__` | empty=1 done=1 @3.9 ms | empty=1 done=1 @2.3 ms |
| `/ip/firewall/nat/print ?comment=__nope__` | empty=1 done=1 @4.8 ms | empty=1 done=1 @3.2 ms |
| `/interface/bridge/print ?name=__nope__` | empty=1 done=1 @5.1 ms | empty=1 done=1 @1.8 ms |
| `/interface/wifi/access-list/print` | — | empty=1 done=1 @1.8 ms |
| `/interface/wifi/radio/print` (AC2) | empty=1 done=1 @3.6 ms | — |

**Not one case produced a bare `!empty`.** A client that ignores `!empty` and waits for `!done`
returns promptly on every one of them — and that is exactly what go-routeros does (see below):
it returned 0 rows in 1.2–27 ms on every empty print, on all three routers.

**What this does and does not settle.** It does not prove the 7.23 observation was wrong; the
Node patch header records it against "a live 7.23.3 hAP AX3", and no 7.23 device is available
to re-test. I searched MikroTik's changelogs for 7.22 → 7.24 and found **no entry** describing a
change to binary-API `!empty`/`!done` framing, so I cannot attribute the difference to a
documented fix either. What it does settle is that **the hazard is absent from every router this
project currently targets**, and that the README states it in the present tense without a version
qualifier on the behaviour itself.

### Streaming half — inconclusive

The README's second meaning ("nothing yet") rests on `/interface/wifi/frequency-scan`. That
could not be tested:

- The **AC2** — the only router where a scan would be permitted — has no usable radio. Its
  `/interface/wifi/radio/print` returns `!empty`, both `wifi1` and `wifi2` are `disabled=true`
  `running=false`, `/interface/wireless/print` traps with `no such command or directory
  (wireless)`, and the scan itself refuses with `!trap =message=failure: interface not bound`.
- The **AX3** and **cAP AX** are the live network. A frequency scan takes the radio off-channel;
  running one is exactly the kind of disruption ruled out.

What *could* be observed on 7.24 is that a streaming command does **not** open with `!empty`.
`/queue/simple/listen` on an empty menu (AC2) stayed completely silent for 6 s, and `!empty`
arrived only at termination:

```
[6004.277ms] --> /cancel =tag=probe1
[6006.738ms] !trap  | .tag=probe1 | =category=2 | =message=interrupted
[6047.620ms] !empty | .tag=probe1
[6047.624ms] !done  | .tag=probe1
```

`/log/print =follow-only= ?message=__nope_zzz__` behaved identically (silent for 12 s, then
`!trap` → `!empty` → `!done` after cancel). So on the shapes that could be tested, `!empty` on a
stream is a *terminal* marker, not a "nothing yet" marker — the opposite of the README's
description. That is suggestive but not decisive, because neither of these is the command the
claim was recorded against.

---

## Claim 2 — multi-block `!done` on wifi-qcom

> README: "wifi-qcom devices answer the registration table one block **per interface**. Returning
> on the first `!done` yields one interface's clients and looks correct." … "on the AX3 that is
> **3 instead of 26**."

**Not reproduced.** The AX3 is exactly the hardware named — hAP ax³, wifi-qcom, RouterOS 7.24,
running as a CAPsMAN manager with 12 wifi interfaces and one remote CAP, with 30 associated
clients spread over 8 interfaces. The full trace of
`/interface/wifi/registration-table/print`:

```
[  11.869ms] !re | =.id=*28  | =interface=5GHz WiFi6   | …
[  11.887ms] !re | =.id=*110 | =interface=2.4GHz WiFi6 | …
[  11.901ms] !re | =.id=*114 | =interface=2.4GHz WiFi6 | …
… 27 more !re, interfaces interleaved …
[  12.353ms] !re | =.id=*4F2 | =interface=2.4GHz WiFi3 | …
[  12.354ms] !done | .tag=probe1

!re sentences        : 30
!done sentences      : 1
rows per block       : [30]
rows before 1st !done: 30      ← what a return-on-first-!done client sees
```

Two things stand out. There is exactly **one** `!done`. And the rows are **not grouped by
interface at all** — they are ordered by descending client uptime, interleaving all 8
interfaces. That ordering is incompatible with a per-interface block structure.

Six consecutive repeats, to rule out a timing race:

```
re=30 done=1  firstdone=12.010ms
re=30 done=1  firstdone=15.275ms
re=30 done=1  firstdone=16.093ms
re=30 done=1  firstdone=11.139ms
re=30 done=1  firstdone=14.334ms
re=30 done=1  firstdone=21.566ms
```

Also tried on the AX3 with the exact argument forms the Node collectors use
(`=.proplist=interface,ssid` from `wifi.js`, `=.proplist=interface,mac-address`): 30 rows,
`done=1` each time.

Widened to 14 further command shapes on the AX3, including deliberately large replies, with the
tracer left reading for 5 s after the first `!done` so a late block could not be missed:

| Command | rows | `!done` count |
|---|---|---|
| `/log/print` | 1000 | 1 |
| `/ip/firewall/connection/print` | 411 | 1 |
| `/interface/bridge/host/print` | 69 | 1 |
| `/ip/arp/print` | 49 | 1 |
| `/ip/dhcp-server/lease/print` | 45 | 1 |
| `/interface/print` | 30 | 1 |
| `/ip/route/print` | 23 | 1 |
| `/system/logging/print` | 13 | 1 |
| `/interface/wifi/print` | 12 | 1 |
| `/interface/wifi/radio/print` | 4 | 1 |
| `/interface/wifi/capsman/remote-cap/print` | 1 | 1 |

**No multi-block reply was observed anywhere on RouterOS 7.24.** Contrast:

> README: "on the AX3 that is 3 instead of 26"

Observed: a return-on-first-`!done` client gets **30 of 30**. That is not a near-miss; the
mechanism the number describes is absent.

### The conformance case does not test this

`caseMultiBlockDone` in `cmd/conformance/main.go` counts rows and distinct interface names. It
never counts `!done` sentences, so it produces the identical `ok` result whether the reply
arrived in one block or eight. Its output on the AX3 —

```
ok  multi-block !done is accumulated   30 clients across 8 interfaces
```

— is what the README cites as "the result that matters". It is consistent with the wire trace,
but it does not distinguish the two hypotheses, and the wire trace shows the single-block one is
true. The harness passes on all three routers (7/7, 7/7, 7/7) and that remains accurate; it is
this one case's *name* that overstates what it verified.

---

## Claim 3 — per-call decoding for `/file/read`

> README: "A UTF-8 decode substitutes U+FFFD per bad byte, which **preserves length** — a
> corrupted backup still matches its expected size."

Created `mdxprobe2.backup` on the AC2 with `/system/backup/save =dont-encrypt=yes`, 46,910 bytes,
read it in two 32,768-byte chunks, and reconstructed it twice.

```
file                       : mdxprobe2.backup (declared size 46910)
RAW bytes                  : 46910   sha256=382f7dd0…e8ed28
chunks NOT valid UTF-8     : 2 of 2
individually invalid bytes : 7873
UTF-8 decoded code points  : 46392
after latin1 re-encode     : 46395 bytes  sha256=af6a7244…ca7d07
LENGTH MATCHES declared?   : raw=true   utf8path=false
CONTENT IDENTICAL?         : false
bytes differing            : 33800 of 46395  (72.85%)
distinct byte values       : raw=256  utf8path=209
```

Repeated on a second, independently generated backup: 72.79 % differing, 210 of 256 distinct
byte values surviving. (The Node patch header records "177 of its 256 distinct byte values
intact" for its own test blob — same failure, same order of magnitude.)

**The danger is real and is worse than the README says: the content is destroyed.** But the
stated mechanism is wrong in a way that matters.

> "which **preserves length** — a corrupted backup still matches its expected size"

Observed: **length is not preserved.** 46,910 raw bytes become 46,395 — 515 bytes short, 1.1 %.
The mechanism the README describes (one U+FFFD per invalid byte) accounts for only part of what
happens: binary data also contains byte sequences that are *accidentally valid* multi-byte UTF-8,
and those collapse from 2–4 bytes into one code point. Consequently the length check that
`../MikroDash/src/backups/runner.js` already performs —

```js
if (out.length !== size) throw new Error('read ' + out.length + ' of ' + size + ' bytes …');
```

— **would have caught this**, on both test files. The corruption is not silent in the way the
README claims.

### It does not apply to a Go client at all

go-routeros v3.0.1 reading the same file on the same connection:

```
go-routeros /file/read mdxprobe2.backup: 46910 bytes (declared 46910)
  sha256=382f7dd0…e8ed28   validUTF8=false
```

Byte-identical to the raw read. The reason is structural: `proto/reader.go` does
`sen.Word = string(b)` and `Pair{string(t[0]), string(t[1])}` — Go's `string` is a byte
sequence, and conversion from `[]byte` never transcodes or substitutes. The U+FFFD problem is
an artefact of node-routeros calling `iconv.decode(...)`; **no Go client can reproduce it.**

This means `Cmd.Raw` in `internal/routeros/client.go` is a no-op. Grepping the port, nothing sets
it, and setting it would change nothing: `dispatch()` already builds every value with
`string(w)`, unconditionally, for raw and non-raw calls alike. It is a correctly-reasoned defence
against a hazard that does not exist in this language. `ValidUTF8()` remains useful — it is a
diagnostic the caller opts into, and the conformance case that uses it is a genuine check.

---

## Claim 4 — trailing packets for torn-down tags

> README: "Trailing packets arrive for tags already torn down. Raising on them kills a connection
> every collector shares."

**Reproduced, deterministically.** `/interface/monitor-traffic =interface=ether1 =interval=1` on
the AC2, cancelled after 3.5 s:

```
[   5.248ms] !re | .tag=probe1 | =name=ether1 | … | =.section=0
[1009.879ms] !re | .tag=probe1 | … | =.section=1
[2017.518ms] !re | .tag=probe1 | … | =.section=2
[3019.330ms] !re | .tag=probe1 | … | =.section=3
[3500.946ms] --> /cancel =tag=probe1
[3503.020ms] !trap | .tag=probe1 | =category=2 | =message=interrupted   ← after cancel
[3503.025ms] !done | .tag=cancelprobe
[3503.877ms] !done | .tag=probe1                                        ← after cancel

sentences tagged probe1 AFTER /cancel was written: 2
```

Three repeats, all `2`. A `listen` produces more, and later:

```
/queue/simple/listen (AC2), cancelled at 6.0 s
[6006.738ms] !trap  | .tag=probe1 | =category=2 | =message=interrupted
[6047.620ms] !empty | .tag=probe1        ← 43 ms after cancel
[6047.624ms] !done  | .tag=probe1
sentences tagged probe1 AFTER /cancel was written: 3
```

`/log/print =follow-only=` behaved identically. So a client that removes the tag at the moment it
writes `/cancel` — which is exactly what `Client.Stream`'s `stop()` does, `c.cancel(tag)`
immediately followed by `c.release(tag)` — will always see 2–3 sentences for a tag it no longer
knows. The unknown-tag branch in `dispatch()` is genuinely load-bearing.

**But go-routeros handles it too**, and the README says otherwise:

> README: "It also raises `UnknownReplyError` on an unrecognised word rather than discarding it,
> which is closer to the `UNREGISTEREDTAG` behaviour that used to take down a shared connection."

That conflates an unrecognised **word** with an unrecognised **tag**. `async.go:70-77`:

```go
c.mu.Lock()
r, ok := c.tags[sen.Tag]
c.mu.Unlock()

// cannot find tag for this sentence, ignore
if !ok {
    continue
}
```

Verified against the AC2 — start a `Listen`, cancel it, then keep using the same connection:

```
listen: received 3 rows, cancelled
post-cancel call 1: ok rows=1
post-cancel call 2: ok rows=1
post-cancel call 3: ok rows=1
```

`UnknownReplyError` fires only on a sentence whose first word is none of
`!re`/`!done`/`!trap`/`!fatal`/`!empty`/`""`. Across every trace taken in this session — hundreds
of sentences, five routers-worth of menus, streams, cancels and traps — **no such word was ever
observed**. It is a hypothetical, not the `UNREGISTEREDTAG` failure mode.

One real caveat, from code reading rather than a trace: this protection exists only in **async**
mode. `runArgsContextSync` (`run.go:82-112`) has no tag map and reads sentences in arrival order,
so a trailing sentence would be misattributed to the next command. Any concurrent use of
go-routeros must call `Async()`, which MikroDash would need regardless.

---

## go-routeros/routeros v3.0.1 — code and behaviour

### The README's quotation is accurate

`reply.go` in v3.0.1 is 44 lines, and `processSentence` occupies **exactly lines 29–44**:

```go
29  func (r *Reply) processSentence(sen *proto.Sentence) (bool, error) {
30      switch sen.Word {
31      case reSentence:
32          r.Re = append(r.Re, sen)
33      case doneSentence:
34          r.Done = sen
35          return true, nil
36      case trapSentence, fatalSentence:
37          return sen.Word == fatalSentence, &DeviceError{sen}
38      case "", emptySentence:
39          // API docs say that empty sentences should be ignored
40      default:
41          return true, &UnknownReplyError{sen}
42      }
43      return false, nil
44  }
```

Both behaviours the README describes are there: it returns on the first `!done`, and it ignores
`!empty` unconditionally. The citation is honest and precise.

### What it actually returned

Run against all three routers, sync and async:

```
### hAP AC2 (7.24) sync
  sanity /system/resource/print         8.9ms  rows=1   done=true
  EMPTY filtered firewall print         6.5ms  rows=0   done=true
  EMPTY /queue/simple/print             3.2ms  rows=0   done=true
  EMPTY /ppp/active/print              27.4ms  rows=0   done=true
  WIFI registration-table/print         3.2ms  rows=0   done=true

### hAP ax³ (7.24) sync
  sanity /system/resource/print         4.0ms  rows=1   done=true
  EMPTY filtered firewall print         6.2ms  rows=0   done=true
  EMPTY /queue/simple/print             1.8ms  rows=0   done=true
  EMPTY /ppp/active/print              13.9ms  rows=0   done=true
  WIFI registration-table/print        17.4ms  rows=30  done=true
  WIFI registration-table (repeat)     16.9ms  rows=30  done=true

### hAP ax³ (7.24) async
  WIFI registration-table/print        12.4ms  rows=30  done=true
  WIFI registration-table (repeat)      9.8ms  rows=30  done=true

### cAP ax (7.24) sync — all empty prints 1.1–12.1ms, rows=0, done=true
```

- **Zero hangs.** Every empty print returned in single-digit-to-low-tens of milliseconds. The
  context deadline was 10 s and was never approached.
- **No truncation.** The AX3 registration table came back with **all 30 clients**, matching the
  wire trace and the port's own client exactly, in both modes and on repeats.
- `/file/read` byte-exact (above).
- Survived listen-cancel with the connection intact (above).

**The README's argument — that this library returns wrong or incomplete data against these
routers — is not supported by any observation made here.** The code does what the README says
it does; on RouterOS 7.24 that turns out not to matter.

### The cost of the defence

The hand-written client's answer to claim 2 is a 20 ms settle window after every `!done`
(`defaultSettle`, `client.go`). Because no multi-block reply exists on 7.24, that window always
expires, and it is paid on every non-empty call. Same connection, same commands, median of 7,
hand-written client vs go-routeros:

| Command | hand-written | go-routeros | rows |
|---|---|---|---|
| AX3 `/system/resource/print` | **23.1 ms** | 3.3 ms | 1 |
| AX3 `/ip/address/print` | **24.0 ms** | 4.2 ms | 13 |
| AX3 `/interface/wifi/registration-table/print` | **31.4 ms** | 12.9 ms | 30 |
| AX3 `/interface/print` | **47.8 ms** | 33.5 ms | 30 |
| AX3 `/ip/firewall/filter/print ?comment=__nope__` | 4.4 ms | 3.8 ms | 0 |
| AC2 `/system/resource/print` | **27.2 ms** | 7.1 ms | 1 |
| AC2 `/ip/address/print` | **24.6 ms** | 4.8 ms | 2 |

A flat ~20 ms per call, ~7× on small reads. Empty prints are the one case that is *not* slowed,
because the `!empty` short-circuit fires — that is claim 1's handling paying for itself, on a
router where waiting for `!done` would also have worked. With 30 collectors polling, this is a
real budget item, and it currently buys protection against a behaviour that could not be
observed on any of the three routers.

---

## Recommendation

**On the four claims.** Two hold, two do not, and one of the two that hold does not apply to Go:

- Claim 4 is the only one that is unambiguously a live protocol reality on 7.24, and the
  hand-written client handles it correctly. So does go-routeros in async mode.
- Claim 3 is a real and severe hazard **in Node**, and structurally impossible in Go. `Cmd.Raw`
  is dead code. The README's "preserves length" is wrong — the existing Node length check
  catches it.
- Claims 1 and 2 could not be reproduced on any router this project targets, despite testing
  the exact hardware, the exact commands and the exact argument forms named. Claim 2's headline
  number ("3 instead of 26") inverts on 7.24: a naive client gets all 30.

**On the question actually being asked** — keep the from-scratch client, or adopt go-routeros?
The evidence collected here does not support the README's stated reason for the from-scratch
client. On this hardware, go-routeros v3.0.1 in async mode returned correct, complete data on
every test, faster, and handled the one reality that does reproduce. If the choice rested only
on "it gets two of the four realities wrong", it should be revisited: that argument is not
currently true against RouterOS 7.24.

That is not the same as saying "switch". Three things are worth weighing that this exercise did
*not* disprove:

1. **7.23 was not re-tested and could not be.** The Node patch header records both claims against
   a live 7.23.3 hAP ax³ with specific detail. The honest reading is that the behaviour changed
   or was version/build-specific, not that the original observation was fabricated — and
   MikroDash may still need to run against 7.23 boxes in the field. If so, the from-scratch
   client's handling is insurance with a measured price (~20 ms/call), and that price should be
   stated as the tradeoff it is rather than as a correctness argument.
2. **The zero-dependency argument stands on its own** and is untouched by any of this. It is
   currently the *strongest* remaining reason for the hand-written client, and the README treats
   it as secondary.
3. **The frequency-scan streaming case was never tested.** It is the one claim-1 scenario with a
   plausible mechanism left, and if it reproduces on the AX3 it is a genuine differentiator,
   since go-routeros ignores `!empty` on a `Listen` too. Testing it requires taking a radio
   off-channel and so needs the owner's explicit go-ahead on a chosen router and time.

**Concrete suggestions, in order of value:**

- Fix `caseMultiBlockDone` to count `!done` sentences, not rows. As written it cannot fail for
  the reason it exists, and it is the evidence the README leans on hardest. If it counted blocks
  it would currently report "1 block" on the AX3 — which is the finding.
- Correct the README's claim-3 wording: the failure is content corruption (72.85 % of bytes), and
  length is *not* preserved (46,910 → 46,395). Note that it is a Node-only failure.
- Correct the `UnknownReplyError` sentence: go-routeros discards unknown **tags**
  (`async.go:74-77`); the sync-mode absence of a tag map is the real hazard worth naming.
- Version-qualify claims 1 and 2 in both the README and `client.go`, e.g. "observed on 7.23.3;
  **not reproducible on 7.24** — see `test-results.md`". Leaving them in the present tense makes
  the port's central justification read as current when it is not.
- Consider making `Settle` default to 0 with an opt-in, or gating it on RouterOS version, and
  delete `Cmd.Raw`. Both are ~20 ms and one dead field respectively, and neither is doing work on
  7.24.

---

*Method note: raw traces, the throwaway go-routeros module, the file-read harness and the latency
comparison were all run outside this repository; only this file was written into it. No
credentials appear here or in any file created during this session — they were read from the
running container at call time. The AC2's file list was verified back to its original state after
both write tests.*
