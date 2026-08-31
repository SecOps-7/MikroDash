package alert

// `Evaluator` against the LIVE `createEvaluator`, lifted and run by
// `tools/alert-eval-cases.js`.

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

type liveFired struct {
	Event   string `json:"event"`
	Payload struct {
		AlertType string  `json:"alertType"`
		Detail    string  `json:"detail"`
		Subject   *string `json:"subject"`
	} `json:"payload"`
}

type evalCase struct {
	Settings struct {
		CPUThreshold      float64 `json:"alertCpuThreshold"`
		NotifCPU          bool    `json:"notifCpu"`
		NotifRouterUpdate bool    `json:"notifRouterUpdate"`
		PingLoss          float64 `json:"alertPingLoss"`
		NotifPing         bool    `json:"notifPing"`
		NotifNetwatch     bool    `json:"notifNetwatch"`
		NotifIfaceUpDown  bool    `json:"notifIfaceUpDown"`
		NotifIfaceEther   *bool   `json:"notifIfaceEther"`
		NotifIfaceWlan    *bool   `json:"notifIfaceWlan"`
		NotifIfaceBridge  *bool   `json:"notifIfaceBridge"`
		NotifIfaceVlan    *bool   `json:"notifIfaceVlan"`
		NotifIfaceOther   *bool   `json:"notifIfaceOther"`
		NotifVPN          bool    `json:"notifVpn"`
		NotifBGP          bool    `json:"notifBgp"`
	} `json:"settings"`
	Events []struct {
		Event string `json:"event"`
		Data  struct {
			CPULoad         *float64 `json:"cpuLoad"`
			UpdateAvailable bool     `json:"updateAvailable"`
			LatestVersion   string   `json:"latestVersion"`
			Version         string   `json:"version"`
			Target          *string  `json:"target"`
			Loss            *float64 `json:"loss"`
			RTT             *float64 `json:"rtt"`
			Hosts           []struct {
				ID     string `json:"id"`
				Host   string `json:"host"`
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"hosts"`
			Interfaces []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Comment  string `json:"comment"`
				Running  bool   `json:"running"`
				Disabled bool   `json:"disabled"`
			} `json:"interfaces"`
			Tunnels []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"tunnels"`
			// FLAPPING IS `any`, not bool: the live check is `!!p.flapping`, so
			// the corpus carries a truthy NUMBER in one case and a bool decoder
			// would fail the whole file rather than the one case.
			Peers []struct {
				Key         string   `json:"key"`
				Name        string   `json:"name"`
				RemoteAddr  string   `json:"remoteAddr"`
				Description string   `json:"description"`
				State       string   `json:"state"`
				Prefixes    *float64 `json:"prefixes"`
				Flapping    any      `json:"flapping"`
				HoldTime    *float64 `json:"holdTime"`
				Keepalive   *float64 `json:"keepalive"`
			} `json:"peers"`
		} `json:"data"`
	} `json:"events"`
	Router struct {
		ID            string `json:"id"`
		AlertsEnabled bool   `json:"alertsEnabled"`
	} `json:"router"`
	Fired []liveFired `json:"fired"`
	// OpenAlerts PRE-SEEDS the store, which is how the corpus models a RESTART:
	// the database outlives the process, so a rebuilt evaluator meets rows it did
	// not file and has no memory of. Every other case starts empty, and that
	// emptiness hid the supersede question entirely -- a first announcement finds
	// nothing open, so whether it would have superseded is never asked.
	OpenAlerts []struct {
		Type    string  `json:"type"`
		Subject *string `json:"subject"`
	} `json:"openAlerts"`
}

type evalCorpus struct {
	Covered   []string            `json:"covered"`
	Uncovered map[string]string   `json:"uncovered"`
	Cases     map[string]evalCase `json:"cases"`
}

// memStore is the same little in-memory table the generator gives the live
// evaluator. A FLAT stub (HasOpen always false, Resolve always empty) would make
// every recovery silent and every duplicate fire -- the generator hit exactly
// that, and its header records it.
type memStore struct {
	open []openAlert
	seq  int64
}

type openAlert struct {
	id                        int64
	routerID, alertType, subj string
}

func (m *memStore) HasOpen(routerID, alertType, subject string) bool {
	for _, a := range m.open {
		if a.routerID == routerID && a.alertType == alertType && a.subj == subject {
			return true
		}
	}
	return false
}

func (m *memStore) Resolve(routerID, alertType, subject string) []int64 {
	var ids []int64
	kept := m.open[:0]
	for _, a := range m.open {
		if a.routerID == routerID && a.alertType == alertType && a.subj == subject {
			ids = append(ids, a.id)
			continue
		}
		kept = append(kept, a)
	}
	m.open = kept
	return ids
}

func (m *memStore) Record(routerID, alertType, subject, _ string) int64 {
	m.seq++
	m.open = append(m.open, openAlert{m.seq, routerID, alertType, subject})
	return m.seq
}

// jsTruthy is `!!v` for the decoded JSON. Only the flapping flag needs it, and
// only because one case carries the number 1 -- the live check is `!!p.flapping`
// and a port reading a strict bool would disagree with a router that reported a
// count. False, 0, "" and null are falsy; everything else is not.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

func loadEvalCorpus(t *testing.T) evalCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/alert-eval-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c evalCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}
	return c
}

func TestEvaluatorMatchesLive(t *testing.T) {
	c := loadEvalCorpus(t)

	// COVERAGE IS PART OF THE CONTRACT. The corpus names the families it does not
	// cover; if one moved out of `uncovered` without this port implementing it,
	// its cases would go silently uncompared.
	implemented := map[string]bool{
		"system:update": true, "ping:update": true, "netwatch:update": true,
		"ifstatus:update": true, "vpn:update": true, "routing:update": true,
	}
	for _, fam := range c.Covered {
		if !implemented[fam] {
			t.Errorf("the corpus covers %q, which this port does not implement", fam)
		}
	}
	if len(c.Covered) != len(implemented) {
		t.Errorf("the corpus covers %v; this port implements %d families -- a family ported "+
			"without being added to COVERED has its cases silently uncompared",
			c.Covered, len(implemented))
	}
	// UNCOVERED IS NOW EMPTY, AND THAT IS THE CORRECT ANSWER. It used to be
	// asserted NON-empty, on the grounds that an empty list would mean the
	// evaluator was fully ported and it was not. `routing:update` was the last
	// family and landed on 2026-08-27, so the old assertion became a gate
	// insisting a gap stay open. What replaces it still fails in both
	// directions: an entry here naming a family this port DOES implement is
	// stale and says so, and the generator's own check fails on a family that is
	// neither covered nor given a reason.
	for fam, why := range c.Uncovered {
		if implemented[fam] {
			t.Errorf("the corpus records %q as uncovered (%q), but this port implements it "+
				"-- the entry is stale and its cases are going uncompared", fam, why)
		}
		if why == "" {
			t.Errorf("%q is recorded as uncovered with no reason", fam)
		}
	}

	// Believability: both outcomes appear, or a port that never fired would pass.
	var fires, silent bool
	for _, tc := range c.Cases {
		if len(tc.Fired) > 0 {
			fires = true
		} else {
			silent = true
		}
	}
	if !fires || !silent {
		t.Fatal("every case fires the same number of times, so this corpus proves nothing")
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			store := &memStore{}
			for _, a := range tc.OpenAlerts {
				subj := ""
				if a.Subject != nil {
					subj = *a.Subject
				}
				store.Record(tc.Router.ID, a.Type, subj, "")
			}
			ev := NewEvaluator(Settings{
				CPUThreshold:      tc.Settings.CPUThreshold,
				NotifCPU:          tc.Settings.NotifCPU,
				NotifRouterUpdate: tc.Settings.NotifRouterUpdate,
				PingLoss:          tc.Settings.PingLoss,
				NotifPing:         tc.Settings.NotifPing,
				NotifNetwatch:     tc.Settings.NotifNetwatch,
				NotifIfaceUpDown:  tc.Settings.NotifIfaceUpDown,
				IfaceTypeFilters:  ifaceFilters(tc),
				NotifVPN:          tc.Settings.NotifVPN,
				NotifBGP:          tc.Settings.NotifBGP,
			}, store)
			router := Router{ID: tc.Router.ID, AlertsEnabled: tc.Router.AlertsEnabled}

			var got []Fired
			for _, e := range tc.Events {
				var fired []Fired
				switch e.Event {
				case "system:update":
					fired = ev.SystemUpdate(router, e.Data.CPULoad,
						e.Data.UpdateAvailable, e.Data.LatestVersion, e.Data.Version)
				case "ping:update":
					fired = ev.PingUpdate(router, e.Data.Target, e.Data.Loss, e.Data.RTT)
				case "netwatch:update":
					hosts := make([]NetwatchHost, 0, len(e.Data.Hosts))
					for _, h := range e.Data.Hosts {
						hosts = append(hosts, NetwatchHost{
							ID: h.ID, Host: h.Host, Name: h.Name, Status: h.Status,
						})
					}
					fired = ev.NetwatchUpdate(router, hosts)
				case "ifstatus:update":
					ifaces := make([]Interface, 0, len(e.Data.Interfaces))
					for _, i := range e.Data.Interfaces {
						ifaces = append(ifaces, Interface{
							Name: i.Name, Type: i.Type, Comment: i.Comment,
							Running: i.Running, Disabled: i.Disabled,
						})
					}
					fired = ev.IfstatusUpdate(router, ifaces)
				case "vpn:update":
					tunnels := make([]VPNTunnel, 0, len(e.Data.Tunnels))
					for _, tn := range e.Data.Tunnels {
						tunnels = append(tunnels, VPNTunnel{Name: tn.Name, State: tn.State})
					}
					fired = ev.VPNUpdate(router, tunnels)
				case "routing:update":
					peers := make([]BGPPeer, 0, len(e.Data.Peers))
					for _, pr := range e.Data.Peers {
						peers = append(peers, BGPPeer{
							Key: pr.Key, Name: pr.Name, RemoteAddr: pr.RemoteAddr,
							Description: pr.Description, State: pr.State,
							Prefixes: pr.Prefixes, Flapping: jsTruthy(pr.Flapping),
							HoldTime: pr.HoldTime, Keepalive: pr.Keepalive,
						})
					}
					fired = ev.RoutingUpdate(router, peers)
				default:
					t.Fatalf("the corpus carries a %q event, which this port does not "+
						"implement -- coverage has drifted", e.Event)
				}
				// NOTHING TO DO HERE ANY MORE, and the reason is worth
				// keeping. The test used to record the open rows itself, once
				// the whole event had returned -- which made the dedup guard
				// blind to an alert fired EARLIER IN THE SAME EVENT.
				// `bgpTwoPeersOneName` caught it: one routing:update carries
				// every peer, and two peers sharing a name share an alert
				// subject. `emit` now records inline, as the live `fire` does.
				got = append(got, fired...)
			}

			if len(got) != len(tc.Fired) {
				t.Fatalf("%d alerts, live %d\n  got  %s\n  live %s",
					len(got), len(tc.Fired), showFired(got), showLive(tc.Fired))
			}
			for i, want := range tc.Fired {
				wantUp := want.Event == "alert:resolved"
				if got[i].Up != wantUp {
					t.Errorf("alert %d: Up = %v, live event %q", i, got[i].Up, want.Event)
					continue
				}
				// The STORED type is what the row carries and what a resolve
				// matches on, so it is what the corpus records.
				stored := StoredType(got[i].AlertType)
				if got[i].Up && got[i].ResolveType != "" {
					stored = got[i].ResolveType
				}
				if stored != want.Payload.AlertType {
					t.Errorf("alert %d: type %q, live %q", i, stored, want.Payload.AlertType)
				}
				if got[i].Detail != want.Payload.Detail {
					t.Errorf("alert %d detail:\n  got  %q\n  live %q",
						i, got[i].Detail, want.Payload.Detail)
				}
				// THE SUBJECT decides which open alert a resolve matches, so it
				// is part of the answer rather than decoration — it is what makes
				// ping deduplicate per target. Live stores an empty subject as
				// null.
				wantSubj := ""
				if want.Payload.Subject != nil {
					wantSubj = *want.Payload.Subject
				}
				if got[i].Subject != wantSubj {
					t.Errorf("alert %d subject: %q, live %q", i, got[i].Subject, wantSubj)
				}
			}
		})
	}
}

// TestTheCPURuleIsAnEdgeNotALevel.
//
// Stated separately because it is the property the whole design turns on:
// `system:update` arrives about every two seconds, so a level test would page
// the operator continuously while a CPU stayed busy.
func TestTheCPURuleIsAnEdgeNotALevel(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{CPUThreshold: 80, NotifCPU: true}, store)
	r := Router{ID: "r1", AlertsEnabled: true}

	load := func(v float64) []Fired {
		// `emit` records its own open rows now, so this only returns them.
		return ev.SystemUpdate(r, &v, false, "", "")
	}

	total := 0
	for _, v := range []float64{90, 91, 92, 93} {
		total += len(load(v))
	}
	if total != 1 {
		t.Errorf("four high readings fired %d alerts, want 1 -- that is a LEVEL test, and it "+
			"would page the operator every two seconds", total)
	}
	// Believability: the rule is not simply mute.
	if n := len(load(10)); n != 1 {
		t.Errorf("the recovery fired %d alerts, want 1", n)
	}
}

// TestAMissingCPUReadingDoesNotResetTheEdge.
//
// `cpuLoad` is a pointer for this: a port taking a plain float64 reads an absent
// reading as 0, decides the CPU recovered, and fires a spurious resolution.
func TestAMissingCPUReadingDoesNotResetTheEdge(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{CPUThreshold: 80, NotifCPU: true}, store)
	r := Router{ID: "r1", AlertsEnabled: true}

	high := 90.0
	ev.SystemUpdate(r, &high, false, "", "")
	if n := len(ev.SystemUpdate(r, nil, false, "", "")); n != 0 {
		t.Errorf("an absent reading fired %d alerts", n)
	}
	again := 91.0
	if n := len(ev.SystemUpdate(r, &again, false, "", "")); n != 0 {
		t.Error("the absent reading reset the edge state, so the alert re-fired")
	}
}

func showFired(fs []Fired) string {
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, map[string]any{"up": f.Up, "type": StoredType(f.AlertType)})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func showLive(fs []liveFired) string {
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, map[string]any{"event": f.Event, "type": f.Payload.AlertType})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// TestAnAbsentInterfaceFilterIsEnabled.
//
// Direct rather than through the corpus, because every generated case carries
// all five filters -- the live DEFAULTS ship them -- so the corpus cannot tell
// `!ok || v` from `ok && v`. A map read that treated a missing key as OFF would
// silence the whole interface family for any caller that built its settings from
// a partial object, which is what a first-run install and every hand-written
// caller look like.
func TestAnAbsentInterfaceFilterIsEnabled(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{
		NotifIfaceUpDown: true,
		// notifIfaceEther is ABSENT.
		IfaceTypeFilters: map[string]bool{"notifIfaceWlan": false},
	}, store)
	r := Router{ID: "r1", AlertsEnabled: true}

	ev.IfstatusUpdate(r, []Interface{{Name: "ether1", Running: true}})
	got := ev.IfstatusUpdate(r, []Interface{{Name: "ether1", Running: false}})
	if len(got) != 1 {
		t.Errorf("an interface whose type filter is ABSENT fired %d alerts, want 1", len(got))
	}

	// Believability: an explicitly FALSE filter really does suppress, so the
	// assertion above is about absence rather than about a gate that never fires.
	ev2 := NewEvaluator(Settings{
		NotifIfaceUpDown: true,
		IfaceTypeFilters: map[string]bool{"notifIfaceEther": false},
	}, &memStore{})
	ev2.IfstatusUpdate(r, []Interface{{Name: "ether1", Running: true}})
	if n := len(ev2.IfstatusUpdate(r, []Interface{{Name: "ether1", Running: false}})); n != 0 {
		t.Errorf("an explicitly disabled filter fired %d alerts", n)
	}
}

// ifaceFilters builds the per-type gate from a case's settings.
//
// A case sets only the filter it is about, so an ABSENT one must read as
// ENABLED -- the live DEFAULTS ship every one of these true, and a map that read
// a missing key as "off" would silence the whole family for every case that did
// not list all five.
func ifaceFilters(tc evalCase) map[string]bool {
	out := map[string]bool{}
	for key, v := range map[string]*bool{
		"notifIfaceEther":  tc.Settings.NotifIfaceEther,
		"notifIfaceWlan":   tc.Settings.NotifIfaceWlan,
		"notifIfaceBridge": tc.Settings.NotifIfaceBridge,
		"notifIfaceVlan":   tc.Settings.NotifIfaceVlan,
		"notifIfaceOther":  tc.Settings.NotifIfaceOther,
	} {
		if v != nil {
			out[key] = *v
		}
	}
	return out
}

// TestTheBoundPrunesWhatIsGoneAndKeepsWhatIsLive.
//
// ── WHY THIS IS NOT IN THE CORPUS ───────────────────────────────────────────
//
// The corpus compares EMITTED ALERTS, and the prune emits nothing. A churn case
// there produces the same alert count whether the prune runs or not, because the
// count is decided by the second payload either way. So `capmap-never-prunes`
// survives every corpus case and this test is what kills it -- the map itself is
// the observable, and only a Go test can see it.
//
// The properties, all four of which the live `_capMap` has:
//
//  1. UNDER the bound it touches nothing. Not an optimisation: upstream a
//     payload can be a PROVISIONAL snapshot mid-cycle, and pruning against a
//     partial list would drop live state and recreate the bug the prune fixes.
//  2. OVER the bound, keys absent from the payload go.
//  3. OVER the bound, keys present in the payload STAY -- the old `clear()` is
//     what took them, and taking them is the reported defect.
//  4. A nil live set is a no-op, so a family that collected nothing cannot
//     empty the map by accident.
func TestTheBoundPrunesWhatIsGoneAndKeepsWhatIsLive(t *testing.T) {
	fill := func(n int) map[string]bool {
		m := map[string]bool{}
		for i := 0; i < n; i++ {
			m["k"+strconv.Itoa(i)] = true
		}
		return m
	}

	// 1. Under the bound, nothing is touched even though almost everything is
	// absent from the live set.
	under := fill(stateMax)
	capMap(under, map[string]bool{"k0": true})
	if len(under) != stateMax {
		t.Errorf("under the bound the map went from %d to %d -- it must touch nothing, "+
			"because a payload is not always the complete fleet", stateMax, len(under))
	}

	// 2 and 3. Over the bound, the absent go and the live stay.
	over := fill(stateMax + 1)
	live := map[string]bool{"k0": true, "k7": true, "nobody-has-seen-this": true}
	capMap(over, live)
	if len(over) != 2 {
		t.Errorf("over the bound %d keys survived, want 2 -- only the keys in the live set "+
			"belong, and a key the live set names but the map never held is not created", len(over))
	}
	for _, k := range []string{"k0", "k7"} {
		if !over[k] {
			t.Errorf("%q was pruned while still in the payload -- that is the `clear()` defect "+
				"this replaced, and it silences the fleet for a round", k)
		}
	}

	// 4. A nil live set is a no-op rather than a wipe.
	nilLive := fill(stateMax + 1)
	capMap(nilLive, nil)
	if len(nilLive) != stateMax+1 {
		t.Errorf("a nil live set emptied the map (%d left) -- a family that collected nothing "+
			"must not lose its state", len(nilLive))
	}
}

// TestEveryCappedMapIsActuallyPruned is a LEDGER, in the manner of the audits:
// it drives one event per family with a payload over the bound, then a second
// naming one survivor, and asserts each map came down. `prevBGPPfxAlert` was
// never bounded at all -- upstream and in the first version of this port, which
// mirrored the four it saw capped -- so "the map I forgot" is the exact failure
// this is here to catch.
func TestEveryCappedMapIsActuallyPruned(t *testing.T) {
	big := func(n int, f func(i int)) {
		for i := 0; i < n; i++ {
			f(i)
		}
	}
	r := Router{ID: "r1", AlertsEnabled: true}
	set := Settings{NotifIfaceUpDown: true, NotifNetwatch: true, NotifVPN: true,
		NotifBGP: true, IfaceTypeFilters: map[string]bool{"other": true, "ether": true}}
	ev := NewEvaluator(set, &memStore{})

	var ifaces []Interface
	var hosts []NetwatchHost
	var tuns []VPNTunnel
	var peers []BGPPeer
	pfx := 100.0
	hold := 3.0
	keep := 0.0
	big(stateMax+1, func(i int) {
		n := "x" + strconv.Itoa(i)
		ifaces = append(ifaces, Interface{Name: n, Running: true})
		hosts = append(hosts, NetwatchHost{ID: n, Host: n, Status: "up"})
		tuns = append(tuns, VPNTunnel{Name: n, State: "active"})
		// Flapping and a bad hold timer so `prevBGPFlap` and `prevBGPHold` are
		// WRITTEN -- both are only set on a transition, so a quiet peer leaves
		// them empty and the assertion below would pass against nothing.
		peers = append(peers, BGPPeer{Key: n, Name: n, State: "established",
			Prefixes: &pfx, Flapping: true, HoldTime: &hold, Keepalive: &keep})
	})
	ev.IfstatusUpdate(r, ifaces)
	ev.NetwatchUpdate(r, hosts)
	ev.VPNUpdate(r, tuns)
	ev.RoutingUpdate(r, peers)
	// A second BGP reading swings the prefix count, so `prevBGPPfxAlert` is
	// written too -- it is set only when an alert opens.
	swung := 200.0
	var peers2 []BGPPeer
	for _, p := range peers {
		q := p
		q.Prefixes = &swung
		peers2 = append(peers2, q)
	}
	ev.RoutingUpdate(r, peers2)

	maps := map[string]int{
		"prevIfState": len(ev.prevIfState), "prevNetwatchState": len(ev.prevNetwatchState),
		"prevVPNState": len(ev.prevVPNState), "prevBGPState": len(ev.prevBGPState),
		"prevBGPPfx": len(ev.prevBGPPfx), "prevBGPFlap": len(ev.prevBGPFlap),
		"prevBGPHold": len(ev.prevBGPHold), "prevBGPPfxAlert": len(ev.prevBGPPfxAlert),
	}
	for name, n := range maps {
		if n <= stateMax {
			t.Fatalf("%s holds %d entries after a payload of %d -- it is not being written, "+
				"so the prune assertion below would pass against an empty map",
				name, n, stateMax+1)
		}
	}

	// One survivor each.
	ev.IfstatusUpdate(r, []Interface{{Name: "x0", Running: true}})
	ev.NetwatchUpdate(r, []NetwatchHost{{ID: "x0", Host: "x0", Status: "up"}})
	ev.VPNUpdate(r, []VPNTunnel{{Name: "x0", State: "active"}})
	ev.RoutingUpdate(r, []BGPPeer{{Key: "x0", Name: "x0", State: "established",
		Prefixes: &swung, Flapping: true, HoldTime: &hold, Keepalive: &keep}})

	for name, n := range map[string]int{
		"prevIfState": len(ev.prevIfState), "prevNetwatchState": len(ev.prevNetwatchState),
		"prevVPNState": len(ev.prevVPNState), "prevBGPState": len(ev.prevBGPState),
		"prevBGPPfx": len(ev.prevBGPPfx), "prevBGPFlap": len(ev.prevBGPFlap),
		"prevBGPHold": len(ev.prevBGPHold), "prevBGPPfxAlert": len(ev.prevBGPPfxAlert),
	} {
		if n != 1 {
			t.Errorf("%s holds %d entries after a payload naming one survivor, want 1 -- "+
				"this map is not in the prune (was %d)", name, n, maps[name])
		}
	}
}
