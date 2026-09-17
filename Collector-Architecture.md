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

### Table collectors: one lifecycle

**Sixteen collectors are plain tables**: one subscription whose cadence is simply
"this table was read". They are `arp`, `bridges`, `capsman`, `dhcpLeases`,
`dhcpNetworks`, `dns`, `ipAddresses`, `netwatch`, `packages`, `ppp`, `queues`,
`rosusers`, `routing`, `system`, `talkers` and `wan`. Each embeds `tableCore`
(`internal/collect/table.go`) and declares only what differs:

| a table collector declares | where |
|---|---|
| its menu, interval bounds, slow lane, heartbeat and stream key | a `tableSpec`, in its constructor |
| how rows become a payload, including any secondary menu it reads | `derive` |
| where the payload goes | `send` |
| what a new connection must learn again | `reset` |

The core provides everything else, once:

- `Start`, `Stop`, `Suspend`, `Resume`, `Reconnected`, `UseCache`, `SetPollMs`,
  `Last`, `RefreshNow` and `Tick`;
- the emit gate: send when the fingerprint changed or the heartbeat is due;
- the slow lane, which `RefreshNow` runs at once after a write;
- `retire`, which stops reading a menu the router does not have, or refuses,
  until the next connection.

A failed read keeps the last payload. The cadence is always the live interval, so
a re-tune changes how often the router is read and not only the number the page
shows. `internal/collect/table_test.go` fails if a table collector declares one of
the core's methods itself; `System.Start`, which also starts the update check, is
the one recorded override.

**The other thirteen keep their own mechanism, each for a stated reason:**

- derived from another collector's output: `vlans`, `bandwidth`;
- a set B stream: `logs`, `ping`, `traffic`;
- a non-plain command: `vpn`;
- a menu chosen at runtime: `firewall`, `wifi`, `wireless`, and `areas` — which
  is the case in its strongest form: `internal/areas` declares each generated
  page, and which menus are read is the set of areas whose room is occupied, so
  neither the menu nor the cadence can be a `tableSpec` field;
- a residual loop beside the subscription: `ifStatus`;
- two payloads with separate emit gates: `conns`;
- a second loop pinging each neighbour: `topology`.

What every collector answers is uniform; the mechanism inside acquisition is not,
and that is deliberate.

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
shared.** A nil `Merge` means single-owner: seven of the eight streamed
menus have exactly one consumer, and a second holder is *refused* rather than
merged, because two collectors quietly fighting over one channel is a real bug
class. A non-nil `Merge` means shared — it combines the holders' commands (the
union of the interfaces, the finest interval) and each row is fanned out to
holders that asked for one. `/interface/monitor-traffic` is the only shared menu:
`ifStatus` wants a snapshot, `traffic` wants every packet, and they hold **one
channel** between them.

### The stream/poll duality

**Only live data streams.** A stream trades commands for a channel held open for
the life of the subscription. That pays for live data: gauges, connections,
sessions, clients, and state that decides what a page shows now. It never pays for
metadata, configuration that changes when somebody edits the router: the stream
re-sends an unchanged table every interval on a channel that is open the whole
time, while a poll costs one command and holds the channel for one read. It is the
fast/slow rule applied to delivery, so a collector's slow lane polls, and so does
every metadata collector (`TestNoMetadataCollectorStreams`).

A live collector offers both delivery modes, and the operator's per-router
Stream/Poll setting chooses. **Both honour the same interval slider** — choosing
Poll must never silently mean slower. Two conditions are required to stream: this
project's judgement that the menu is live and safe (`session.streamableMenus`)
*and* the operator's setting. Either saying no means poll.

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

**25 of 28 collectors have an extracted derivation.** The three without are
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
  throughput column on Bridges, VLANs, WAN and Bandwidth. It is also kept awake by
  the Dashboard's Network Flow card (`dash-card-wireless`), whose Wired count reads
  the interface list it sends router-wide (issue #132).
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
| **enablement** | is this collector switched off in the resolved collection config | `Session.CollectorEnabled` |
| **dormancy** | has it reported nothing for long enough to back off | `internal/dormancy` |

**NOTHING CAN BE SWITCHED OFF TODAY, AND THE GATE IS STILL THERE.** The row
above described the install-wide Ping / Latency toggle until `71d4c5a` removed
it: ping was the last collector an operator could disable, so `Enabled` is now
true for every key on every router. The gate is not dead code — `CollectorEnabled`
is checked on the connect path and on the page-focus RESUME path, which is what
stops a switched-off collector coming back the moment somebody opens its page,
and an unknown key reads as ENABLED so a new collector runs rather than silently
never starting. What changed is that the answer is currently always yes.

`ResumeCollector` is the only place a collector starts, precisely so that a gate
which knows nothing about dormancy cannot undo it. **`roslimit`** sits underneath
all three, capping commands in flight per router.

The session's idle grace is separate from all three: it closes the router
*connection* when nothing holds the session, which is a different question from
whether a collector runs.

---

## Generated pages: one collector, many areas

`internal/areas` declares a RouterOS menu as a page — its key, title, nav group,
the resources its tables show and how often to read them — and everything else is
generated from that: the page, the nav entry, the permission key, the visibility
toggle, the `list_` tool and `change_row`'s coverage.

**It exists because the alternative does not scale.** MikroMCP reaches perhaps
sixty menus this app does not. As hand-built pages that is sixty passes through
"Adding a collector" below, sixty registry rows, sixty session fields. As areas it
is one collector, already in the registry as `areas`, and a declaration each.

| what it does | how |
|---|---|
| chooses its menus | per tick, from the declarations and which page rooms are occupied |
| declares its rooms | one `page-<key>` per area, generated — so the demand rule applies unchanged |
| reads | poll only, each area on its own declared interval; configuration never streams |
| derives | `BuildAreaRows`: rows to id, identity and values, keyed by the resource's FIELD names |
| sends | `area:update`, to that area's room alone |
| writes | nothing of its own: the resource engine's pipeline, guards, audit and undo |

**Two things stay per area and neither can be generated:** a captured fixture to
replay, and a row in `docs/routeros-api-surface.md` for each menu. Both are held
by ledgers in `internal/verify/areas_test.go`, along with the page key, the nav
group, the resources and the columns.

**It is not a second registry and not a second permission model.** An area POINTS
at resources, and `internal/resource` keeps saying what a row is; an area's `Key`
IS a page key, so the per-user, per-router matrix gates it exactly as it gates a
hand-built page.

---

## Every collector

Acquisition is the subscribed menu; a leading `—` marks a set B stream. Views are
the rooms it emits to, and `—` means router-wide or nothing.

| collector | acquisition | derivation | views |
|---|---|---|---|
| `areas` | the menus of the areas being looked at, `internal/areas` | `BuildAreaRows` | one `page-<area>` per declared area |
| `arp` | `/ip/arp/print` | `BuildARP` | — (none at all) |
| `bandwidth` | `/ip/firewall/connection/print` | `BuildBandwidth` | `page-bandwidth` |
| `bridges` | `/interface/bridge/host/print` | `BuildBridgeRows` | `page-bridges` |
| `capsman` | `/interface/wifi/registration-table/print` | `BuildCapsmanView`, `BuildCapsmanLegacyView` | `page-capsman` |
| `conns` | `/ip/firewall/connection/print` | `BuildConns` | `page-connections`, `dash-card-connections` |
| `dhcpLeases` | `/ip/dhcp-server/lease/print` | `BuildLeases` | — (router-wide) |
| `dhcpNetworks` | `/ip/dhcp-server/network/print` | `BuildLanOverview` | `page-dhcp`, `dash-card-network` |
| `dns` | `/ip/dns/print` | `ParseDNSSettings`, `ParseStaticEntries` | `page-dns` |
| `ipAddresses` | `/ip/address/print` | `BuildIPAddresses` | `page-ip-addresses` |
| `firewall` | the table on screen | `BuildFirewallRule` | `page-firewall`, `dash-card-firewall` |
| `ifStatus` | `/interface/print` | `BuildIfStatus` | `page-interfaces`, `page-network-topology`, `dash-card-physports` |
| `logs` | — `/log/listen` | fold: `FoldLog` | `page-logs`, `dash-card-logs` |
| `netwatch` | `/tool/netwatch/print` | `BuildNetwatch` | `page-dashboard`, `page-netwatch` |
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

**Two collectors are never started.** `packages` and `routing` are page-gated
only: the session brings them up with `Resume()` and never calls the `Start()`
they have as table collectors. That is what "page-gated" means in this design,
not an omission.

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
| registry rows | 29 |
| collectors with a Go implementation | 29 |
| suspendable when idle (`disableable`) | 24 |
| dormancy-eligible | 19 |
| gated by demand (`session.TargetKeys`) | 27 |
| menus enabled for stream delivery | 8 |
| collectors declaring rooms | 24 |
| `keepAliveFor` entries | 3 |
| collectors with an extracted derivation | 26 |

---

## Changing this architecture

Adding or changing a collector touches all three layers, every one of which has
more than one place to change. The checklist below is the whole list.

### Adding a collector

Each row names where the change goes, when it applies, and what fails if it is
missed. **"By hand" means nothing fails**, and the row says so rather than
implying a gate that does not exist.

| # | touchpoint | when | what fails if it is missed |
|---|---|---|---|
| 1 | a registry row, with its poll bounds, in `internal/collection/collection_tables.json` | always | `TestTheEmbeddedRegistryMatchesTheCorpus`; "registry rows" below |
| 2 | the recorded resolutions in `testdata/collection-cases.json` and `testdata/collection-payload-cases.json` | always | `internal/collection/collection_test.go`, `internal/collection/payload_test.go` |
| 3 | `internal/collection/pollmap.json` and `testdata/settings-apply-cases.json` | it has a poll key | `TestEveryReTunedCollectorHasASetter` |
| 4 | the collector in `internal/collect`. A table collector embeds `tableCore` and supplies a `tableSpec`, `derive`, `send` and `reset`; any other collector names which reason under "Table collectors: one lifecycle" applies, and subscribes through `scheduled` | always | `internal/collect/table_test.go`, whose re-tune list must gain it; `internal/verify/scheduled_test.go`; `internal/verify/subfields_test.go`; `internal/verify/acquisition_test.go` |
| 5 | a menu another collector also reads goes through the cache | it shares a menu | `internal/verify/sharedmenu_test.go` |
| 6 | stream delivery: probe `=interval=` on hardware with `cmd/streamcost`, then add a line to `internal/session/streammenus.go` | the menu streams | `internal/session/streammenus_test.go`; "menus enabled for stream delivery" below |
| 7 | a package-level `BuildX` or `FoldX`, and its entry in the ledger in `internal/verify/derivations_test.go` | always | `TestEveryCollectorDeclaresItsDerivation` |
| 8 | a test that every rendered field moves the fingerprint | it sends a payload | by hand: each collector has its own test, and no ledger lists them |
| 9 | the event, `hub.Declare[T]` in `internal/collect/events.go`, then regenerate `web/src/gen/payloads.ts` | it sends a payload | `internal/verify/event_test.go`; the tsgen check in `tools/verify.sh` |
| 10 | a builder in `internal/collect/nullarrays_test.go` | it sends a payload | `TestNoPayloadSendsANullArray` |
| 11 | its rooms in `internal/collect/rooms.go`, or a `keepAliveFor` entry with the reason | always | `internal/collect/rooms_test.go`; `internal/server/demand_test.go` |
| 12 | `internal/session/session.go`: the field, the accessor, construction, the `UseCache` list, and the connect, reconnect, suspend and both teardown blocks | always | `internal/session/lifecycle_test.go` and `internal/session/release_test.go`, which pin the counts |
| 13 | a target in `internal/session/dormancy_targets.go`, built from closures rather than method values: a promoted method value dereferences a collector the session did not build | always | `internal/session/prime_test.go`; `TestASavedCollectorSwitchReachesTheLiveSession` panics on a method value |
| 14 | `internal/session/retune.go` | it has a poll key | `TestEveryReTunedCollectorHasASetter` |
| 15 | `internal/session/needs.go` | it feeds alerts, history or the Devices page | `internal/verify/holds_test.go` |
| 16 | an empty key in the registry, or a measured reason in `internal/verify/emptykey_test.go` | it is dormancy-eligible | `TestCollectorsWithoutAnEmptyKeyHaveAMeasuredReason` |
| 17 | a page: `internal/pages/pages.go`, `internal/server/pages_table.json`, `web/src/ui/page-<key>.html`, `web/src/pages/<key>.ts`, `web/src/main.ts` and the nav in `web/src/ui/shell.html` | it has a page | the pagesgen check in `tools/verify.sh`; `TestVisibilityGuardsNameRealPages` |
| 18 | settings: `internal/store/settings_tables.json`, `internal/store/settings_write_tables.json`, `internal/store/pagekeys.json`, `internal/store/disclose.go`, the `testdata/settings-*-cases.json` corpora, `testdata/poll-tables.json`, `testdata/settings-form-map.json`, `testdata/view-presets.json` and `web/src/ui/page-settings.html` | it has a page toggle or a poll key | `TestTheEmbeddedKeyListIsTheLiveOne`; the generator checks in `tools/verify.sh` |
| 19 | every RouterOS command it issues, in `docs/routeros-api-surface.md` | always | by hand: the file is frozen and extended from the RouterOS documentation |
| 20 | this file: the "Every collector" table, the table-collector list and the measured facts | always | `internal/verify/architecture_test.go`; `internal/verify/checklist_test.go` |
| 21 | CHANGING a poll range means all four places that clamp it: the collector's `tableSpec` in `internal/collect`, `pollBounds` in `internal/collection/collection_tables.json` (applied by `clamp()` in `Resolve`), the write validator `internal/store/settings_write_tables.json`, and `pollBounds` in `internal/store/settings_tables.json` — plus the slider `max` in `testdata/poll-tables.json` from row 18 | its poll range changes | by hand: NOTHING compares the four. `internal/store/merge_test.go` only counts them, so raising one and missing the rest is capped silently on the way to the collector and the UI reports an interval the router never used. The only check that catches it is a save, a RELOAD and a read-back |

**The checklist is gated too.** `internal/verify/checklist_test.go` fails when a
file that names every registry collector is missing from this table, which is how
a new place that lists collectors shows up, and when a path or a test named here
does not exist. It also holds the table-collector list above to the files that
embed `tableCore`, in both directions.
