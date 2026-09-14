# Collector Architecture

**What this is.** The current shape of MikroDash's collector layer, and why it is
that shape. Not a plan and not a history: it describes what the code does today.

**It is gated.** `internal/verify/architecture_test.go` re-measures the collector
table and every number under "Measured facts", and fails in both directions: a
claim that has gone stale fails, and so does a collector that exists and is not
described here. A change to the collector layer changes this file in the same
commit.

---

## Design goals

The collector layer is built to be **simple, efficient and uniform**, and each goal
is a concrete property of the design rather than an aspiration:

| goal | the property | where it shows |
|---|---|---|
| **simple** | one mechanism per job | one demand rule (`Session.Wants`), one stream entry point (`JoinStream`), one place a collector starts (`ResumeCollector`) |
| **efficient** | as few router channels as possible | one read per menu for every asker; a pushed stream in place of polling where the router supports it |
| **uniform** | every collector is described the same way | how it acquires, how it derives, and who it is for, each declared in the same place and each checked by a gate |

## The shape, in one picture

```
        RouterOS                              this process
   ┌──────────────┐
   │  a menu      │   1  ACQUISITION      internal/collect/*.go  (the reads)
   │  /ip/dns     │      one subscription per menu,             internal/roscache
   │  ...         │      one scheduler per router,              internal/roslimit
   └──────┬───────┘      one read shared by every asker
          │ rows
          ▼
   ╔══════════════╗   2  DERIVATION       BuildX(...) / FoldX(...)
   ║ pure function║      rows in, payload out. No I/O, no receiver,
   ╚══════┬═══════╝      callable from a test without a router
          │ payload
          ▼
   ┌──────────────┐   3  VIEWS            internal/collect/rooms.go
   │ page-dns     │      a collector declares its ROOMS; the hub knows
   │ dash-card-…  │      who is in each; demand decides what runs
   └──────────────┘      internal/server/demand.go
```

The three layers answer three different questions, and keeping them apart is what
lets each change without the others:

| layer | the question it answers | where it lives |
|---|---|---|
| **Acquisition** | *how do rows get here, and how often* | `internal/roscache`, `scheduled` in `internal/collect` |
| **Derivation** | *what do these rows mean* | `BuildX` / `FoldX` functions, pure |
| **Views** | *who is listening, and should this run at all* | `rooms.go`, `internal/server/demand.go` |

---

## Layer 1 — Acquisition

**Its job: get rows off the router as few times as possible.** The documented
bottleneck is concurrent API channels on the MikroTik, not CPU here — so
"efficient" means *fewer router channels*, never faster parsing.

### Set A and Set B

Every acquisition is one of two kinds, and the split decides everything else about
it. `KindOf` in `internal/collect/acquisition.go` is the classifier.

**Set A — a cacheable query.** A menu read that answers with the current contents
of a table. Rows are *successive readings of a keyed value*, so a later read
replaces an earlier one and a cache entry can stand for the menu.

**Set B — a measurement or a stream.** Rows are *distinct elements*, not readings
of one value: a ping result counted into loss, a log line, a traffic sample. No
cache can serve them, because there is no "current value" to hold.

That distinction is not stylistic. A rolling cache entry backed by set B rows
reports nonsense — 0% packet loss for ever, dropped log lines — and nothing fails.
`roscache.Unrollable` refuses the two menus where it would.

### How a set A read happens

1. A collector declares a **subscription**: a menu, a proplist and a cadence
   (`scheduled` in `internal/collect/scheduled.go`).
2. `internal/roscache` keeps the **demand set** — who wants which menu.
3. **One scheduler goroutine per router** decides when each menu is due, reads it
   once, and delivers to every subscriber. Two collectors on one menu with
   byte-identical proplists cost one read: `conns` and `bandwidth` share
   `/ip/firewall/connection/print` this way.
4. `internal/roslimit` caps commands in flight at 8 per router.

**Most sharing happens one level up, between collectors rather than between
subscriptions.** `arp` and `dhcpLeases` each own their menu, and their consumers
read the derived index through a capability (see "The in-process edges" below)
instead of subscribing themselves. So a menu usually has one subscriber, and a
subscriber count understates how much is shared. The subscription-level share
exists — `conns` and `bandwidth` on the connection table — but only while both are
wanted at once; across seven pages on 2026-09-10 no menu had two.

### Streams: a second filler for the same entry

A cache entry can be kept current by an open channel instead of a read —
`/print =interval=N`, which the router pushes. The entry is the same; only the
filler changes. **A menu absent from the table polls**, which is what makes this
reversible one collector at a time.

Two mechanisms make it safe:

- **Round detection.** A `/print =interval=N` re-prints the whole table with no
  separator between rounds. A repeated key, or a quiet gap, ends a round — without
  which the entry could only accumulate, and rows that left the table would pile
  up for the life of the session.
- **The empty-table rule.** An empty table sends nothing, which is
  indistinguishable from a dead channel. The watchdog already reopens a quiet
  channel, so *silence surviving a deliberate restart* is evidence of an empty
  table rather than a broken one.

**One entry point, `JoinStream`, and a `Join` declares whether the menu is
shared.** A nil `Merge` means single-owner: thirteen of the fourteen streamed
menus have exactly one consumer, and a second holder is *refused* rather than
merged, because two collectors quietly fighting over one channel is a real bug
class. A non-nil `Merge` means shared — it combines the holders' commands (the
union of the interfaces, the finest interval) and each row is fanned out to
holders that asked for one. `/interface/monitor-traffic` is the only shared menu:
`ifStatus` wants a snapshot, `traffic` wants every packet, and they hold **one
channel** between them.

### The stream/poll duality

Every collector offers both delivery modes, and the operator's per-router
Stream/Poll setting chooses. **Both honour the same interval slider** — choosing
Poll must never silently mean slower. Two conditions are required to stream: this
project's judgement that the menu is safe (`session.streamableMenus`) *and* the
operator's setting. Either saying no means poll.

### Acquisitions that are not router reads

Two sources sit in this layer and read something else:

- **`arp`** reads `/ip/arp/print` like any table and emits nothing. Its whole
  output is an in-memory IP↔MAC index that four collectors join against.
- **`PTRCache`** asks *this process's resolver*, not the router, what an address
  calls itself — the last fallback for naming a device with no DHCP lease.

---

## Layer 2 — Derivation

**Its job: turn rows into a payload, and nothing else.** A derivation takes rows
(and, where a collector carries state between ticks, the prior state) and returns
the payload. It performs no I/O, holds no receiver, and can be called from a test
without building a collector or a router.

Two shapes, and the difference follows Set A / Set B exactly:

```go
// table     map over the current state
func BuildX(prior State, rows []routeros.Reply) (Payload, State)

// sequence  fold one element in
func FoldX(prior State, row routeros.Reply, now int64) (Sample, State)
```

`FoldPing`, `FoldLog` and `FoldTraffic` are the three folds — the three set B
acquisitions. Everything else is a map.

**The purity is load-bearing in one specific way.** `append` into a slice with
spare capacity writes *through* to the caller's backing array, and a ring that has
been trimmed always has spare capacity. Every fold copies, and each says so where
it does.

**24 of 27 collectors have an extracted derivation.** The three without are
exactly the set B streams, whose derivation is per-pushed-row and lives in the
fold. `internal/verify/derivations_test.go` is the ledger, and it fails both ways.

---

## Layer 3 — Views

**Its job: decide who receives a payload, and therefore what runs at all.**

A collector declares its **rooms** in `internal/collect/rooms.go` — one line per
audience, `page-<key>` for a page and `dash-card-<name>` for a dashboard card.
Nothing else states the audience: an `emit` takes the declaration, and demand
reads the same one, so what a collector sends to and what keeps it running cannot
disagree.

**The event is declared too, with its payload type.** A collector emits through
`EvX.Emit(relay, room, payload)`, where `EvX = hub.Declare[XPayload]("x:update")`
in `internal/collect/events.go`. The compiler checks every payload against its
event, and `cmd/tsgen` generates the browser's type for each event from the same
declarations, so what a collector sends and what a page expects cannot disagree
either. The payload never carries a null array: `TestNoPayloadSendsANullArray`
builds every collector from empty input and from every capture.

### Demand

`Session.Wants` in `internal/session/needs.go` holds the whole rule, and it is
stated **once**:

> a collector runs if anybody is in any room it declares

plus two consumers that occupy no room and never will: **alerting** (the rules are
not in a room) and the **non-viewer holds** (a session kept alive for history, for
the Devices page, or merely warm).

**Two appliers ask it, and they differ only in when they act.**
`internal/server/demand.go` applies it on browser events and defers a suspend by a
grace period — a page refresh empties every room and refills it a second later.
`Session.applyDemand` applies it when the session's own reasons change, which is
already at the end of a grace.

The session's applier returns early while a viewer is present, and that is not a
second copy of the rule: it says **which applier owns the viewer case**, and the
answer is the server, because the rooms are not joined yet when the session's
version runs from the connect path.

Every event that can change the answer re-asks it — a page focus, a page blur, a
card blur, a router switch, and a socket closing. A suspend waits out a grace
period and re-asks when it fires, so a page refresh does not stop and restart a
channel to save one second of polling.

### Holds: a reason that is not a viewer

A session may be kept alive by something other than a browser, and each such
reason is a **hold** naming the collectors it needs:

| hold | why the session exists | what it runs |
|---|---|---|
| `alerts` | the rules must be evaluated | `session.AlertFeeds` |
| `history` | traffic and ping are being recorded | `historyFeeds` |
| `devices` | the Devices page reads a payload per router | `devicesFeeds` |
| `warm` | the page must be able to say "up" instantly | **nothing** — a connection only |

**A hold is inert unless something takes it.** A reason with a feed list runs
nothing until some caller `Retain`s it. `internal/verify/holds_test.go` fails in
both directions: a reason read and never taken, and a reason taken and never read.

**The holds are asked without the viewer term.** `Needs` returns true for
everything while a viewer is present, because it answers "what is this session
allowed to run". `Wants` clears `Viewer` before asking it, so a hold still counts
on the router somebody is looking at, and room occupancy decides the rest.

### Rooms a collector does not emit to

`keepAliveFor` is the exception, and there are **3** entries. It exists for an
in-process dependency no emit can express:

- **`ifStatus`** is the rate source for five collectors, four of which live on
  pages it sends nothing to. Gating on its own audience alone would blank every
  throughput column on Bridges, VLANs, WAN and Bandwidth.
- **`dhcpLeases`** emits *router-wide*, so it has no guardable audience at all,
  while the DHCP and Connections pages render it directly.
- **`arp`** emits nothing whatsoever. Its rooms are its four consumers'.

---

## How the three fit together

One payload, end to end — the DNS page:

1. **Views.** A browser opens `/dns` and joins `router-<id>-page-dns`.
   `applyDemand` asks the rule of every collector; `dns` declares `page-dns`, so it
   is wanted, and `ResumeCollector("dns")` starts it.
2. **Acquisition.** `dns` subscribes to `/ip/dns/print` at its cadence. The
   router's scheduler reads it once — or a channel fills the entry, if this router
   streams — and delivers rows to every subscriber.
3. **Derivation.** `ParseDNSSettings` and `ParseStaticEntries` turn rows into a
   `DNSPayload`. No lock is held, nothing is emitted, and the same call in a test
   needs no router.
4. **Views again.** The collector emits the payload to `page-dns`, and the hub
   fans it out to whoever is in that room.
5. The browser closes the tab. `releaseRouter` leaves the rooms **first**, then
   re-asks demand — so the departing connection is no longer counted as its own
   audience — and `dns` is handed to a grace timer that suspends it if nobody
   comes back.

The layers touch only at declared seams: a subscription (rows come back), a return
value (a payload comes out), a room name (which decides whether step 2 happens at
all). Nothing in the derivation knows who is watching; nothing in the view layer
knows what a menu is.

### Reading the layers on a running install

The Dashboard's **API Diagnostics** card is this document made observable: three
sections, one per layer, for the router the operator has selected. It asks the
router nothing — every figure is already in this process — which is the property
that lets it be added without changing what it measures.
`internal/session/diagnostics.go` assembles it and `internal/server/diagnostics.go`
sends it to the socket every two seconds while the card is on screen.

| section | what it reads | where the number comes from |
|---|---|---|
| Acquisition | router commands/min, in flight against the cap, open channels, menus split into pushed and polled | `roslimit`, `roscache.Demand`, `roscache.StreamedMenus` |
| Menus read | the subscribed menus themselves, pushed first | `roscache.Demand` |
| Derivation | payloads/min | one counter in the session's single emit closure |
| Views | collectors running of those demand can gate, occupied rooms, dormant, and the holds | `Session.Wants`, `hub.Occupants`, `Session.holds` |

The card lists menus rather than subscriber counts because of where sharing
happens: see "Most sharing happens one level up" under Layer 1.

---

## The gates that decide whether a collector runs

Three, layered rather than competing:

| gate | asks | where |
|---|---|---|
| **demand** | is anybody in a room it feeds, or does a hold need it | `Session.Wants`, applied from `internal/server/demand.go` and `Session.applyDemand` |
| **enablement** | has the operator turned it off for this router | `Session.CollectorEnabled` |
| **dormancy** | has it reported nothing for long enough to back off | `internal/dormancy` |

`ResumeCollector` is the only place a collector starts, precisely so that a gate
which knows nothing about dormancy cannot undo it. **`roslimit`** sits underneath
all three, capping commands in flight per router.

The session's idle grace is separate from all three: it closes the router
*connection* when nothing holds the session, which is a different question from
whether a collector runs.

---

## Every collector

Acquisition is the subscribed menu; a leading `—` marks a set B stream. Views are
the rooms it emits to, and `—` means router-wide or nothing.

| collector | acquisition | derivation | views |
|---|---|---|---|
| `arp` | `/ip/arp/print` | `BuildARP` | — (none at all) |
| `bandwidth` | `/ip/firewall/connection/print` | `BuildBandwidth` | `page-bandwidth`, `dash-card-bandwidth` |
| `bridges` | `/interface/bridge/host/print` | `BuildBridgeRows` | `page-bridges` |
| `capsman` | `/interface/wifi/registration-table/print` | `BuildCapsmanView`, `BuildCapsmanLegacyView` | `page-capsman` |
| `conns` | `/ip/firewall/connection/print` | `BuildConns` | `page-connections`, `dash-card-connections` |
| `dhcpLeases` | `/ip/dhcp-server/lease/print` | `BuildLeases` | — (router-wide) |
| `dhcpNetworks` | `/ip/dhcp-server/network/print` | `BuildLanOverview` | `page-dhcp`, `dash-card-network` |
| `dns` | `/ip/dns/print` | `ParseDNSSettings`, `ParseStaticEntries` | `page-dns` |
| `firewall` | the table on screen | `BuildFirewallRule` | `page-firewall`, `dash-card-firewall` |
| `ifStatus` | `/interface/print` | `BuildIfStatus` | `page-interfaces`, `page-network-topology`, `dash-card-physports` |
| `logs` | — `/log/listen` | fold: `FoldLog` | `page-logs`, `dash-card-logs` |
| `netwatch` | `/tool/netwatch/print` | `BuildNetwatch` | `page-dashboard` |
| `packages` | `/system/package/print` | `BuildPackages` | `page-packages` |
| `ping` | — `/tool/ping` | fold: `FoldPing` | `page-dashboard` |
| `ppp` | `/ppp/active/print` | `ParsePPPSessions` | `page-ppp` |
| `queues` | `/queue/simple/print` | `BuildQueueRows` | `page-queues` |
| `rosusers` | `/user/print` | `BuildUsersView` | `page-users` |
| `routing` | `/routing/bgp/session/print` | `BuildRouting` | `page-routing`, `page-dashboard` |
| `system` | `/system/resource/print` | `buildSystem` | — (router-wide) |
| `talkers` | `/ip/kid-control/device/print` | `BuildTalkers` | `page-dashboard` |
| `topology` | `/ip/neighbor/print` | `BuildTopology` | `page-network-topology` |
| `traffic` | — `/interface/monitor-traffic` | fold: `FoldTraffic` | per-interface rooms |
| `vlans` | `/interface/vlan/print` | `BuildVlanRows` | `page-vlans` |
| `vpn` | `/ppp/active/print` | `ParsePppSessions`, `ParseIpsecPeers` | `page-vpn`, `dash-card-vpn` |
| `wan` | `/interface/detect-internet/state/print` | `BuildWanRows` | `page-wan` |
| `wifi` | `/interface/wifi/print` | `BuildWifiView`, `BuildCapsLegacyNetworks` | `page-wifi-networks` |
| `wireless` | `/interface/wifi/registration-table/print` | `BuildWirelessView` | `page-wifi-clients`, `dash-card-wireless` |

**Two collectors have no `Start()`.** `packages` and `routing` are page-gated
only: the session brings them up with `Resume()` and nothing else. That is what
"page-gated" means in this design, not an omission.

**The in-process edges** — one collector reading another's output — are declared
as capabilities, never as a pointer to the producer: `RateSource`, `LeaseSource`,
`LeaseIPs`, `LeaseCounts`, `NetworkSource`, `SystemSource`, `FilterRowSource`,
`ARPByIP`, `ARPByMAC`, `NameByIP`. A consumer names the *question* it needs
answered, which is what lets it be tested without the producer, and what makes the
edge visible from the consumer's own type.
`internal/verify/collectoredges_test.go` is the ledger.

---

## Measured facts

The gate re-computes each of these. If one is wrong, the gate fails rather than
the document quietly lying.

| fact | value |
|---|---|
| registry rows | 27 |
| collectors with a Go implementation | 27 |
| disableable by the operator | 22 |
| dormancy-eligible | 19 |
| gated by demand (`session.TargetKeys`) | 25 |
| menus enabled for stream delivery | 14 |
| collectors declaring rooms | 23 |
| `keepAliveFor` entries | 3 |
| collectors with an extracted derivation | 24 |

---

## Changing this architecture

Adding or changing a collector touches all three layers, and each has a gate that
tells you if you missed one:

1. **Acquisition** — declare the subscription (`scheduled`), or record why it is
   set B. `internal/verify/scheduled_test.go`, `acquisition_test.go`.
2. **Derivation** — a package-level `BuildX`/`FoldX`, listed in
   `internal/verify/derivations_test.go`.
3. **Views** — rooms in `internal/collect/rooms.go`, or a `keepAliveFor` entry
   with its reason. `internal/server/demand_test.go` refuses a collector no room
   can ever want.
4. **Lifecycle** — `internal/session`: construct it, add it to the `UseCache`
   list, the dormancy target table, and the connect/reconnect/teardown blocks. The
   counts in `lifecycle_test.go` and `release_test.go` are pinned.
5. **This file** — `internal/verify/architecture_test.go` fails until the table
   and the numbers above match the code.
