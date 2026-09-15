package collect

// Fixtures in, Node's payload out — the differential gate for the port.
//
// testdata/fixtures/ holds what a live router SAID; testdata/golden/ holds what
// the Node collector MADE of it (tools/make-golden.js). This replays the first
// into the Go collector and requires the second, field for field. It is the
// whole reason the fixture corpus was captured before any Go was written: the
// port is not asked to be plausible, it is asked to be identical.
//
// GAPS ARE LISTED, NOT SKIPPED SILENTLY. Every golden without a Go collector is
// reported by name, and the count is asserted, so "we have ported one of
// sixteen" is visible in the test output rather than inferred from its absence.
//
// Wall-clock fields are excluded on both sides — `ts`, taken at emit, and
// `deltaWindowMs`, the measured gap between two metadata commits. Neither can
// come from a fixture, and `deltaWindowMs` was seen flipping between 320 and 321
// on consecutive regenerations, which would make this gate intermittent.

import (
	"encoding/json"
	"fmt"
	"mikrodash/internal/hub"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/routeros"
)

const testdata = "../../testdata"

// ported maps a collector key to a run that produces its payload. Adding a
// collector here is the only edit needed to bring it under this gate.
var ported = map[string]func(Reader, Emit) any{
	// Driven through RefreshNow, not Tick, because that is the method
	// collector-snapshot.js used when the fixture was captured — which is why
	// the capture holds two commands and not five. Tick would additionally ask
	// the BGP menus, and the replay would answer them from nothing.
	"routing": func(r Reader, e Emit) any {
		c := NewRouting(r, e, 30000)
		c.RefreshNow()
		return c.Last()
	},
	// Top Talkers. The golden was generated in POLL mode, which is why its
	// `pollMs` is 30000 rather than the 0 a streamed collector reports, and with
	// the default top-5 cut — 64 rows in the fixture, five devices out.
	"talkers": func(r Reader, e Emit) any {
		c := NewTalkers(r, e, 30000, 0) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	"packages": func(r Reader, e Emit) any {
		c := NewPackages(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	"dns": func(r Reader, e Emit) any {
		c := NewDNS(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// nil RateSource, matching the replay: the Node harness constructs the
	// collector without an ifStatus, so the golden carries null rates and
	// ratesAvailable:false. When interfaceStatus is ported the rates light up
	// on the live path without this changing.
	"bridges": func(r Reader, e Emit) any {
		c := NewBridges(r, e, nil, 30000)
		c.Tick()
		return c.Last()
	},
	// nil rates AND nil lease counts, matching the replay: the Node harness
	// constructs this collector without an ifStatus or a dhcpLeases, so the
	// golden carries null rates, ratesAvailable:false and clients:0.
	"vlans": func(r Reader, e Emit) any {
		c := NewVlans(r, e, nil, nil, 30000)
		c.Tick()
		return c.Last()
	},
	// Driven through RefreshNow, matching collector-snapshot.js: that is what
	// produced the three exchanges in the fixture. Start() would do the same
	// reads and then leave a poll loop running in the test.
	"dhcpLeases": func(r Reader, e Emit) any {
		c := NewDHCPLeases(r, e, 600000) // the interval the golden was generated at
		c.RefreshNow()
		return c.Last()
	},
	// nil LeaseIPs and the default WAN interface, matching the replay: the Node
	// harness constructs this collector without a dhcpLeases and without a
	// wanIface, so the golden carries leaseCount 0 throughout and resolves the
	// WAN address against "WAN1".
	"dhcpNetworks": func(r Reader, e Emit) any {
		c := NewDHCPNetworks(r, e, nil, "", 30000)
		c.Tick()
		return c.Last()
	},
	// ONE tick, matching the replay: rates are derived from two readings, so a
	// single tick leaves every rxRate null and the totals null with them. That
	// is what the golden captured and it is the honest empty state — this fleet
	// runs no PPP.
	"ppp": func(r Reader, e Emit) any {
		c := NewPPP(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// RefreshNow, not Tick: that is the snapshot method collector-snapshot.js
	// picks, so the capture holds ONE exchange — the peers read — and the
	// golden's `ppp` and `ipsec` are empty because the other tables were never
	// asked for. Tick would ask, and the replay would answer from nothing.
	"vpn": func(r Reader, e Emit) any {
		c := NewVPN(r, e, 30000)
		c.RefreshNow()
		return c.Last()
	},
	// "replay" is the username the fixture harness authenticates as, so `self`
	// resolves to nothing and every row comes back `protected: false`. That is
	// what the golden holds, and it is the honest empty state — the real
	// protection is exercised by internal/guard's differential gate instead.
	"rosusers": func(r Reader, e Emit) any {
		c := NewRosUsers(r, e, []string{"replay"}, 30000)
		c.Tick()
		return c.Last()
	},
	// nil firewall, matching the replay: the Node harness constructs this
	// collector without one, so `fasttrack` is the honest "cannot say" — state
	// "unknown" rather than a false "clear". ActiveFasttrack is covered on its
	// own instead, since no fixture carries both menus at once.
	"queues": func(r Reader, e Emit) any {
		c := NewQueues(r, e, nil, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// Tick, not Start: the capture holds the four full-table reads that
	// _loadInitial issues, and nothing from the counter stream — which carries
	// only .id/packets/bytes and could not be replayed against them anyway.
	// deltaPackets is therefore 0 throughout, which is the honest first reading.
	"firewall": func(r Reader, e Emit) any {
		c := NewFirewall(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// ONE tick, which is the load: `dirty` starts true, so the first tick reads
	// all five menus and emits. The capture holds exactly those five.
	//
	// The legacy half of this collector is NOT reachable here and never will be
	// — no router in this fleet runs /interface/wireless — so it is gated by
	// tools/wifiview-cases.js instead. See internal/collect/wifiview.go.
	"wifi": func(r Reader, e Emit) any {
		c := NewWifi(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// ONE tick, which reads everything: `dirty` starts true, so the config half
	// and the live half both run. The capture holds all eleven exchanges.
	"capsman": func(r Reader, e Emit) any {
		c := NewCapsman(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// ONE tick, and the payload it produces carries NO SERIAL — which is the
	// live behaviour, not a gap. The Node collector fires its static read
	// fire-and-forget and then builds the payload from fields that read has not
	// filled, so the serial and the licence level appear on the second emit.
	// This side defers the read to the second tick, which reproduces the same
	// two-emit sequence deterministically. The golden holds the first one.
	//
	// Health and the update row are absent from the capture, so tempC is null
	// and the Updates card is blank — also what the golden holds.
	"system": func(r Reader, e Emit) any {
		c := NewSystem(r, e, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	// The golden here is the `logs:history` EMIT, not a snapshot payload — this
	// collector parks nothing, it forwards. LoadInitial is the whole of what a
	// fixture can drive: /log/listen is a push channel and the replay reader
	// cannot open one, so the live tail is exercised against hardware instead.
	"logs": func(r Reader, e Emit) any {
		c := NewLogs(r, e)
		c.LoadInitial()
		return c.Last()
	},
	// The label is the fixture's own router model, which is what the Node replay
	// harness sets as `routerLabel` — it names the CORE node, and the golden
	// holds it. nil rate source, matching the replay: without an ifStatus every
	// interface reads as physical, which only affects the bridge-over-port
	// preference in pickIfaces.
	"topology": func(r Reader, e Emit) any {
		// The router id joined this signature on 2026-08-28: the payload carries
		// it and never did, because the constructor had no way to be told.
		c := NewTopology(r, e, nil, "r-fixture", "identity-0cc5 ax^3", 30000)
		c.Tick()
		return c.Last()
	},
	// nil lease source, matching the replay: the Node harness constructs this
	// collector without a dhcpLeases and without an arp, so every client comes
	// back with an empty name and no IP. That is what the golden holds, and the
	// warning the Node collector logs about it ("30/30 clients have no ARP
	// entry") is the same observation.
	"wireless": func(r Reader, e Emit) any {
		c := NewWireless(r, e, nil, 30000) // the interval the golden was generated at
		c.Tick()
		return c.Last()
	},
	"netwatch": func(r Reader, e Emit) any {
		c := NewNetwatch(r, e, 30000)
		c.Tick()
		return c.Last()
	},
	"wan": func(r Reader, e Emit) any {
		c := NewWan(r, e, nil, 30000)
		c.Tick()
		return c.Last()
	},
	// Driven through EVERY recorded tick. This collector differences error and
	// drop counters between readings, so one tick has no baseline — and stopping
	// early leaves it on an earlier tick than the Node collector finished on,
	// which shows up as a difference in every byte counter that moved in
	// between. Neither is a porting error; both are the harness not reproducing
	// the router's cadence.
	"ifStatus": func(r Reader, e Emit) any {
		// "r-fixture" is what nodecheck/helpers/fixture-replay.js passes the
		// live collectors, so the golden carries it. It was "" here and '' in
		// the golden — agreement on a value the app never emits.
		c := NewIfStatus(r, e, "r-fixture", 30000)
		n := 1
		if rr, ok := r.(*replayReader); ok {
			n = rr.maxTicks()
		}
		for i := 0; i < n; i++ {
			c.Tick()
		}
		return c.Last()
	},
}

// ── the replay router ────────────────────────────────────────────────────────

type exchange struct {
	Cmd    string           `json:"cmd"`
	Params []string         `json:"params"`
	Rows   []routeros.Reply `json:"rows"`
}

type fixture struct {
	Collector string     `json:"collector"`
	Exchanges []exchange `json:"exchanges"`
	// Streams are what a collector received on an `=interval=N` channel. The Go
	// ports read those menus with ordinary prints instead — fewer open channels,
	// which is what CLAUDE.md means by more efficient — so the recorded ticks are
	// served here as successive answers to the same read.
	Streams []exchange `json:"streams"`
}

// replayReader answers only from the capture.
//
// Matching is by command AND parameters, because several collectors read the
// same menu with different proplists and the answers differ accordingly. An
// unmatched command falls back to the same menu's rows rather than returning
// nothing: answering with what was captured beats pretending the menu is
// absent, which would exercise a fallback the collector should not be taking.
type replayReader struct {
	byKey map[string][]routeros.Reply
	byCmd map[string][]routeros.Reply
	// ticks holds each recorded stream's rows split by `.section` — one entry
	// per delivery the router made. Successive reads of the same menu walk
	// forward through them, which is what lets a collector that differences
	// counters between polls be tested at all: one tick has no baseline.
	ticks map[string][][]routeros.Reply
	at    map[string]int
	asked []string
}

func newReplayReader(f fixture) *replayReader {
	r := &replayReader{
		byKey: map[string][]routeros.Reply{}, byCmd: map[string][]routeros.Reply{},
		ticks: map[string][][]routeros.Reply{}, at: map[string]int{},
	}
	for _, ex := range f.Exchanges {
		r.byKey[key(ex.Cmd, ex.Params)] = ex.Rows
		if _, ok := r.byCmd[ex.Cmd]; !ok {
			r.byCmd[ex.Cmd] = ex.Rows
		}
	}
	for _, st := range f.Streams {
		var groups [][]routeros.Reply
		last := ""
		for _, row := range st.Rows {
			sec := row[".section"]
			if len(groups) == 0 || sec != last {
				groups = append(groups, nil)
				last = sec
			}
			groups[len(groups)-1] = append(groups[len(groups)-1], row)
		}
		if _, ok := r.ticks[st.Cmd]; !ok {
			r.ticks[st.Cmd] = groups
		}
	}
	return r
}

func key(cmd string, params []string) string {
	if params == nil {
		params = []string{}
	}
	b, _ := json.Marshal(params)
	return cmd + " " + string(b)
}

func (r *replayReader) Connected() bool { return true }

// maxTicks is how many deliveries the router made on its busiest recorded
// stream. A collector that differences between readings has to be driven
// through all of them to end where the Node one ended: its final payload
// reflects the LAST tick, and its deltas the gap between the last two. Driving
// fewer leaves the Go payload on an earlier tick, and every byte counter that
// moved in between shows up as a difference that is not a porting error.
func (r *replayReader) maxTicks() int {
	n := 1
	for _, groups := range r.ticks {
		if len(groups) > n {
			n = len(groups)
		}
	}
	return n
}

func (r *replayReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.asked = append(r.asked, key(cmd.Path, cmd.Args))
	if rows, ok := r.byKey[key(cmd.Path, cmd.Args)]; ok {
		return rows, nil
	}
	if rows, ok := r.byCmd[cmd.Path]; ok {
		return rows, nil
	}
	// A menu the Node collector streamed. Hand back one recorded tick per read,
	// holding on the last one so a collector polling more often than the capture
	// recorded sees a steady state rather than running out of data.
	if groups, ok := r.ticks[cmd.Path]; ok && len(groups) > 0 {
		i := r.at[cmd.Path]
		if i >= len(groups) {
			i = len(groups) - 1
		}
		r.at[cmd.Path] = i + 1
		return groups[i], nil
	}
	return nil, nil
}

// ── deliberate divergence from the Node golden ───────────────────────────────

// addedSinceNode names the fields this port deliberately adds beyond what the
// Node implementation sent, per collector.
//
// ── WHY A LEDGER AND NOT A REGENERATED GOLDEN ───────────────────────────────
//
// The goldens cannot be regenerated: `tools/make-golden.js` read the Node source
// and that source is gone, so the run skips. Hand-editing one is worse than
// useless — CLAUDE.md's rule is that a hand-copied table "is a fork with no
// update path" — and deleting the gate to make an addition quiet is the one
// thing this repository has explicitly lost coverage to before.
//
// So an addition is RECORDED. The field is stripped from the Go payload before
// the diff, and everything else still has to match the Node recording exactly.
//
// ── AND IT FAILS IN BOTH DIRECTIONS ─────────────────────────────────────────
//
// An unrecorded addition fails the diff, and a recorded one that has STOPPED
// being produced fails here — otherwise this list becomes folklore: a set of
// excuses for differences nobody re-measures, outliving the changes it
// describes. That is the rule the surviving ledgers in internal/verify carry and
// it is the reason this one is safe to add.
//
// Paths are `field`, `list[].field`, or either with a dotted prefix naming the
// objects to descend through first — `profiles.channel[].legacy`. The prefix
// arrived with the legacy CAPsMAN rows, which sit two levels down; a flat name
// resolved to nothing and the strip silently did nothing, which is the direction
// that looks like a pass.
type addedField struct {
	Spec string
	// ProvenBy names a test in this package that shows the field is still
	// produced, for an addition THIS CORPUS CANNOT EXERCISE.
	//
	// ── WHY THE ESCAPE HATCH EXISTS, AND WHY IT IS NOT A HOLE ───────────────
	//
	// The default rule is that at least one row must carry a non-empty value,
	// because presence alone proves nothing: the struct field has no `omitempty`,
	// so deleting its assignment still marshals `"ap": ""` and the ledger would
	// see a happy row. That rule assumes the corpus can reach the field at all.
	//
	// `wifi.networks[].ap` is the case where it cannot: the AX3's radios are its
	// own, so a correct implementation reports "" on every row, and no capture in
	// this repository has a CAP-provisioned interface. The header above says what
	// to do — "the entry needs a fixture that does" — and there is none to have,
	// so the proof moves to a named unit test instead of being quietly dropped.
	//
	// IT FAILS IN BOTH DIRECTIONS LIKE EVERYTHING ELSE HERE: the named test must
	// exist in this package, so deleting it takes the exemption with it.
	ProvenBy string
}

func added(spec string) addedField { return addedField{Spec: spec} }

var addedSinceNode = map[string][]addedField{
	// Each interface's RouterOS `.id`, added 2026-09-15 so the Interfaces page can
	// edit a row through the resource engine (#97): its comment, or enabling and
	// disabling it. Purely additive. The capture's proplist predates the field,
	// so every replayed row carries an empty id; the proof is a named test.
	"ifStatus": {
		{Spec: "interfaces[].id", ProvenBy: "TestAnInterfaceCarriesItsRouterOSID"},
	},
	// Whether a host is disabled and how often it is probed, added 2026-09-15 for
	// the NetWatch page (#97), which shows and edits both. Purely additive; the
	// capture read the full row, so both are filled from the recording.
	"netwatch": {
		added("hosts[].disabled"), added("hosts[].interval"),
	},
	// The 802.11 generation a client negotiated. Added 2026-09-07 for the WiFi
	// Clients page's Standard column: the Node app read the registration table's
	// `band` and kept only the frequency half, so the generation was collected
	// and discarded. Purely additive — no existing field changed.
	// `comment` joined it on 2026-09-13, for the Wi-Fi map's per-field client
	// labels: the hostname and the operator's own note are two different
	// answers and the map lets either be switched off. No capture in this
	// repository has a lease comment — the capture tool drops the field — so
	// the proof is a named test. See `ProvenBy`.
	"wireless": {
		added("clients[].standard"),
		{Spec: "clients[].comment", ProvenBy: "TestWirelessClientCarriesTheLeaseComment"},
	},
	// The legacy CAPsMAN tree, added 2026-09-13. The Node app read
	// `/interface/wifi/capsman` and nothing else, so a manager also running
	// `/caps-man` showed none of those CAPs at all. Purely additive: `legacy`
	// marks which tree a row came from and is false on every row the Node app
	// produced, and the two payload flags are false on a router without the
	// menus.
	//
	// `legacyAvailable` reads TRUE against this fixture and that is the replay
	// harness rather than the router: `replayReader.Do` answers an unrecorded
	// menu with no rows and no error, which is how a collector latches a menu as
	// present. The AX3 itself refuses `/caps-man`. Nothing downstream of the
	// flag is exercised here — the recorded tables are empty, so no legacy CAP,
	// rule or profile is produced — which is why the entry is a strip rather
	// than a fixture.
	"capsman": {
		added("caps[].legacy"), added("provisioning[].legacy"),
		added("legacyAvailable"), added("legacyManager"),
		added("profiles.configuration[].legacy"), added("profiles.security[].legacy"),
		added("profiles.channel[].legacy"), added("profiles.datapath[].legacy"),
	},
	// The operator's own cabling, added 2026-09-13. A discovery protocol cannot
	// see through a switch that forwards none of it, so everything behind a SwOS
	// box looks directly attached to the port it arrives on — and nothing the
	// manager can read says otherwise. `pinned` is on every node and every edge,
	// false throughout this fixture because no pin is declared, and `pinsEnabled`
	// says whether the switch is on at all.
	"topology": {
		added("pinsEnabled"), added("edges[].pinned"),
		// The node list is a union of three shapes and only the neighbour one
		// carries this, so the corpus cannot show a non-empty value on every row
		// — `stripAdded` counts rows that HAVE the key, and the core and the
		// clients do not.
		{Spec: "nodes[].pinned", ProvenBy: "TestPinnedParentsOverrideTheInference"},
	},
	// The uplink SET became a choice on 2026-09-13. `detect-interface-list` ships
	// as `none`, so the honest reading of most routers is zero uplinks — and the
	// page has spent its life explaining a command somebody has to run on the
	// ROUTER before a DASHBOARD shows anything. `uplinkSource` says which answer
	// is in force, `manualNames` is the declared list whether or not it is, and
	// `interfaces` is the picker's options, read off the `/interface/print` this
	// collector already had in hand. Every row gains `manual`, false on this
	// fixture because the set is still the router's.
	"wan": {
		added("uplinkSource"), added("manualNames"), added("interfaces"),
		added("wans[].manual"),
	},
	// Which ACCESS POINT a network is broadcast by, added 2026-09-13 so the Wifi
	// Networks page can group by hardware and the Wi-Fi map can pin one box
	// rather than one radio. Read from `cap` on the interface, which the Node app
	// never asked for — so no capture in this repository has the field at all,
	// and on the AX3 it would read "" anyway: its radios are its own rather than
	// a CAP's. See `ProvenBy`.
	"wifi": {
		{Spec: "networks[].ap", ProvenBy: "TestBuildWifiViewReadsTheAccessPointFromCap"},
		{Spec: "radios[].ap", ProvenBy: "TestBuildWifiViewReadsTheAccessPointFromCap"},
	},
}

// collectDeclares reports whether this package declares a function by that name.
//
// READ FROM SOURCE, because a test cannot call a test. Reflection sees no test
// functions and `go test` will not hand out its own list, so the only way to ask
// "does that proof still exist" is to look at the files. Cached per run: the
// ledger asks it once per entry and the directory is small either way.
func collectDeclares(t *testing.T, fn string) bool {
	t.Helper()
	declaredOnce.Do(func() {
		declared = map[string]bool{}
		entries, err := os.ReadDir(".")
		if err != nil {
			return
		}
		re := regexp.MustCompile(`(?m)^func ([A-Za-z_]\w*)\(`)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			b, rerr := os.ReadFile(e.Name())
			if rerr != nil {
				continue
			}
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				declared[m[1]] = true
			}
		}
	})
	return declared[fn]
}

var (
	declaredOnce sync.Once
	declared     map[string]bool
)

// stripAdded removes one recorded field from the payload.
//
// Returns how many rows carried a NON-EMPTY value and how many rows there were,
// because those are the two different failures: zero out of zero is an empty
// fixture and says nothing, while zero out of twenty means the addition has
// stopped happening and the ledger entry is stale.
//
// NON-EMPTY, NOT PRESENT, AND THE DIFFERENCE WAS MEASURED. The first version
// counted presence and a mutation deleting the field's ASSIGNMENT survived it:
// the struct field has no `omitempty`, so an unset string still marshals as
// `"standard": ""` and the ledger saw twenty happy rows. A ledger that cannot
// tell "still produced" from "still declared" is the folklore it was written to
// avoid.
func stripAdded(payload any, spec string) (filled, possible int) {
	root, ok := payload.(map[string]any)
	if !ok {
		return 0, 0
	}
	// Descend the dotted prefix first, so `profiles.channel[].legacy` reaches the
	// list it names. A prefix that resolves to nothing returns 0,0 — the same
	// answer as an absent list, which is what an addition to a collector this
	// fixture does not exercise looks like.
	for {
		head, rest, found := strings.Cut(spec, ".")
		if !found || strings.Contains(head, "[]") {
			break
		}
		next, ok := root[head].(map[string]any)
		if !ok {
			return 0, 0
		}
		root, spec = next, rest
	}
	listName, field, isList := strings.Cut(spec, "[].")
	nonEmpty := func(v any) bool {
		s, isStr := v.(string)
		return !isStr || s != ""
	}
	if !isList {
		v, has := root[spec]
		if !has {
			return 0, 1
		}
		delete(root, spec)
		if nonEmpty(v) {
			return 1, 1
		}
		return 0, 1
	}
	rows, ok := root[listName].([]any)
	if !ok {
		return 0, 0
	}
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		possible++
		v, has := row[field]
		if !has {
			continue
		}
		delete(row, field)
		if nonEmpty(v) {
			filled++
		}
	}
	return filled, possible
}

// ── the gate ─────────────────────────────────────────────────────────────────

func TestGoldenPayloads(t *testing.T) {
	goldens := listGoldens(t)
	if len(goldens) == 0 {
		t.Fatal("no goldens under testdata/golden — run: node tools/make-golden.js")
	}

	var unported []string
	run := 0
	for _, g := range goldens {
		build, ok := ported[g.collector]
		if !ok {
			unported = append(unported, g.router+"/"+g.collector)
			continue
		}
		run++
		t.Run(g.router+"/"+g.collector, func(t *testing.T) {
			var f fixture
			readJSON(t, filepath.Join(testdata, "fixtures", g.router, g.collector+".json"), &f)

			got := build(newReplayReader(f), hub.NewRelay(func(room string, _ hub.Named, payload any) {}))
			if got == nil || reflect.ValueOf(got).IsNil() {
				t.Fatal("the collector produced no payload")
			}

			var want any
			readJSON(t, g.file, &want)

			// Recorded additions come out before the comparison, and each one
			// must actually have been there — see addedSinceNode.
			gotAny := normaliseTS(toAny(t, got))
			for _, add := range addedSinceNode[g.collector] {
				filled, possible := stripAdded(gotAny, add.Spec)
				if add.ProvenBy != "" {
					// The corpus cannot show this one. What it CAN still show is
					// that the field is in the payload at all, and the named test
					// carries the rest.
					if possible > 0 && !collectDeclares(t, add.ProvenBy) {
						t.Errorf("addedSinceNode points %q at %q, which internal/collect "+
							"does not declare. The proof was renamed or deleted and the "+
							"exemption outlived it.", add.Spec, add.ProvenBy)
					}
					continue
				}
				if possible > 0 && filled == 0 {
					t.Errorf("addedSinceNode records %q for %s, and not one of the "+
						"%d row(s) carries a value for it. Either the addition was "+
						"removed and this entry must go with it, or it was renamed "+
						"and the entry must follow — a ledger that outlives its "+
						"change is how a difference stops being re-measured.\n"+
						"If a fixture legitimately has nothing to report here, name "+
						"the test that proves it instead, in ProvenBy.",
						add.Spec, g.collector, possible)
				}
			}

			if diff := diffJSON(gotAny, want, ""); diff != "" {
				t.Errorf("payload differs from the Node golden:\n%s", diff)
			}
		})
	}

	if run == 0 {
		t.Error("no ported collector was exercised — the gate is asserting nothing")
	}
	sort.Strings(unported)
	t.Logf("%d of %d goldens covered; not yet ported: %s",
		run, len(goldens), strings.Join(unported, ", "))
}

// TestGoldenCorpusIsReachable fails loudly when the corpus is missing rather
// than letting the gate above pass vacuously in a checkout that never ran the
// generator.
func TestGoldenCorpusIsReachable(t *testing.T) {
	if _, err := os.Stat(filepath.Join(testdata, "fixtures")); err != nil {
		t.Fatalf("fixture corpus unreachable: %v", err)
	}
}

type golden struct{ router, collector, file string }

func listGoldens(t *testing.T) []golden {
	t.Helper()
	root := filepath.Join(testdata, "golden")
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []golden
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			out = append(out, golden{
				router:    d.Name(),
				collector: strings.TrimSuffix(f.Name(), ".json"),
				file:      filepath.Join(root, d.Name(), f.Name()),
			})
		}
	}
	return out
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// toAny round-trips through JSON so both sides are compared as the browser
// would receive them — a Go struct and a decoded object are not comparable, and
// the wire form is what the contract is about.
func toAny(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// wallClock names the fields that cannot come from a fixture, matching
// tools/make-golden.js. The two must agree or the gate compares a zeroed field
// against a live one and fails for a reason that has nothing to do with the port.
var wallClock = map[string]bool{"ts": true, "deltaWindowMs": true,
	"firstSeen": true, "lastSeen": true}

// normaliseTS zeroes every numeric wall-clock field.
func normaliseTS(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = normaliseTS(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			if wallClock[k] {
				if _, isNum := x[k].(float64); isNum {
					x[k] = float64(0)
					continue
				}
			}
			x[k] = normaliseTS(x[k])
		}
		return x
	}
	return v
}

// diffJSON reports the differences by path, because "not deeply equal" on a
// payload this size is a fact without a location.
func diffJSON(got, want any, path string) string {
	at := path
	if at == "" {
		at = "(root)"
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("  %s: want object, got %T\n", at, got)
		}
		var b strings.Builder
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		for k := range g {
			if _, seen := w[k]; !seen {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			wv, inWant := w[k]
			gv, inGot := g[k]
			switch {
			case !inGot:
				b.WriteString(fmt.Sprintf("  %s.%s: missing from the Go payload (Node has %v)\n", at, k, wv))
			case !inWant:
				b.WriteString(fmt.Sprintf("  %s.%s: present only in the Go payload (%v)\n", at, k, gv))
			default:
				b.WriteString(diffJSON(gv, wv, path+"."+k))
			}
		}
		return b.String()
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("  %s: want array, got %T\n", at, got)
		}
		if len(g) != len(w) {
			return fmt.Sprintf("  %s: length %d, Node has %d\n", at, len(g), len(w))
		}
		var b strings.Builder
		for i := range w {
			b.WriteString(diffJSON(g[i], w[i], fmt.Sprintf("%s[%d]", path, i)))
		}
		return b.String()
	default:
		if !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("  %s: %#v, Node has %#v\n", at, got, want)
		}
		return ""
	}
}
