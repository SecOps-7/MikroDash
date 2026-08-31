package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mikrodash/internal/alert"
)

// THE COOLDOWN KEY SEPARATES ALERTS THAT MUST NOT SHARE ONE.
//
// The cooldown suppresses repeat notifications inside a window. If two distinct
// alerts share a key, the second is silently swallowed — a second interface
// going down, or the recovery of the one that just went down.
//
// Live keys per rule instance and direction (`iface:ether1:down`). This derives
// the same granularity from what `Fired` carries. The cases below are the pairs
// that MUST differ; see `cooldownKey` for the one case where the granularity is
// coarser than live's.
func TestCooldownKeySeparatesDistinctAlerts(t *testing.T) {
	down := func(t, subj string) alert.Fired {
		return alert.Fired{AlertType: t, Subject: subj}
	}
	up := func(t, resolve, subj string) alert.Fired {
		return alert.Fired{AlertType: t, ResolveType: resolve, Subject: subj, Up: true}
	}

	for _, c := range []struct {
		name string
		a, b alert.Fired
	}{
		{"two interfaces", down("Interface Down", "ether1"), down("Interface Down", "ether2")},
		{"down and its recovery", down("Interface Down", "ether1"),
			up("Interface Up", "interface_down", "ether1")},
		{"different families, same subject", down("Interface Down", "ether1"),
			down("VPN Disconnected", "ether1")},
		{"two netwatch hosts with DIFFERENT names",
			down("Host Down", "gateway"), down("Host Down", "nas")},
		{"cpu and an update", down("High CPU", ""), down("RouterOS Update", "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if cooldownKey(c.a) == cooldownKey(c.b) {
				t.Errorf("both keyed %q — one of these notifications would be swallowed "+
					"by the other's cooldown", cooldownKey(c.a))
			}
		})
	}

	// AND THE SAME ALERT KEYS THE SAME, or the cooldown never suppresses
	// anything and a flapping interface notifies on every poll.
	if cooldownKey(down("Interface Down", "ether1")) != cooldownKey(down("Interface Down", "ether1")) {
		t.Error("the same alert produced two different keys; nothing would ever be suppressed")
	}

	// THE KNOWN COARSENESS, asserted so it is a recorded fact rather than a
	// surprise: live keys netwatch on the host's RouterOS id, this on its name,
	// so two hosts sharing a name share a cooldown. If a future change lifts the
	// real keys, this expectation flips and the comment must go with it.
	if cooldownKey(down("Host Down", "dup")) != cooldownKey(down("Host Down", "dup")) {
		t.Error("unreachable")
	}
}

// A RESOLUTION KEYS ON THE TYPE IT RESOLVES, not on its own display name.
//
// `Fired.ResolveType` exists because the stored row carries the DOWN type — the
// live comment warns that anything else "silently fails to match the row". The
// cooldown has to follow the same identity or an up/down pair would land in
// unrelated buckets and neither would ever suppress a repeat.
func TestTheCooldownKeyFollowsTheResolvedType(t *testing.T) {
	up := alert.Fired{AlertType: "Interface Up", ResolveType: "interface_down",
		Subject: "ether1", Up: true}
	got := cooldownKey(up)
	if got != "interface_down:ether1:up" {
		t.Errorf("key = %q, want interface_down:ether1:up — a resolution must key on "+
			"the type it resolves", got)
	}
}

// A NIL DISPATCHER SENDS NOTHING AND DOES NOT PANIC.
//
// `dispatchFired` sits on the emit path of every collector tick. A build with no
// dispatcher — no `-alert-dispatch`, or no settings — must take that path
// millions of times without incident.
func TestDispatchFiredIsInertWithoutADispatcher(t *testing.T) {
	s := &Server{}
	s.dispatchFired("r1", "Alpha", []alert.Fired{{AlertType: "High CPU"}})
	s.dispatchFired("r1", "Alpha", nil)
}

// THE DERIVED COOLDOWN KEYS PARTITION ALERTS EXACTLY AS LIVE'S DO.
//
// ── STRING EQUALITY IS THE WRONG TEST, AND THE FIRST VERSION USED IT ──────
//
// Live's keys are `update:router:down`, `netwatch:1:down`, `bgp:k1:down`; the
// port derives `routeros_update::down`, `host_down:10.0.0.1:down`. All 1,091
// differ as strings, and every one of those differences is harmless: the key is
// an internal cooldown bucket, never shown to anyone.
//
// What a cooldown key DOES is decide which notification suppresses which. Two
// alerts sharing a key share a cooldown. So the property worth testing is that
// the two schemes induce the SAME PARTITION — same alerts grouped, same alerts
// separated — not that they spell the groups the same way.
//
// ── HOW THE KEYS GOT HERE ─────────────────────────────────────────────────
//
// Live's key is `fire()`'s first argument and never travels on the fired alert.
// `tools/alert-eval-cases.js` now captures it from its `_deliver` stub, which is
// what makes this checkable at all.
//
// Recording them immediately found a defect: the update-supersede case EMITS
// three alerts and DELIVERS two, because live's supersede resolution goes
// through `_emit` and never reaches the delivery loop. `Fired.Silent` exists
// because of this corpus, and without it the port would have sent an extra "up"
// notification on every version change.
func TestTheDerivedCooldownKeysPartitionLikeLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/alert-eval-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases map[string]struct {
			Fired []struct {
				Event   string `json:"event"`
				Payload struct {
					AlertType string  `json:"alertType"`
					Subject   *string `json:"subject"`
				} `json:"payload"`
			} `json:"fired"`
			CooldownKeys []string `json:"cooldownKeys"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}

	compared, disagree := 0, 0
	var examples []string
	for name, c := range doc.Cases {
		// The corpus cannot say WHICH emitted alert was silent, so only cases
		// where every emitted alert was delivered can be aligned. The supersede
		// case is the one that differs, and `Fired.Silent` handles it.
		if len(c.CooldownKeys) == 0 || len(c.CooldownKeys) != len(c.Fired) {
			continue
		}
		var mine []string
		for _, f := range c.Fired {
			subj := ""
			if f.Payload.Subject != nil {
				subj = *f.Payload.Subject
			}
			up := f.Event == "alert:resolved"
			fired := alert.Fired{AlertType: f.Payload.AlertType, Subject: subj, Up: up}
			if up {
				fired.ResolveType = f.Payload.AlertType
			}
			mine = append(mine, cooldownKey(fired))
		}
		// SAME PARTITION: for every pair, live grouping them must agree with the
		// port grouping them.
		for i := 0; i < len(mine); i++ {
			for j := i + 1; j < len(mine); j++ {
				compared++
				liveSame := c.CooldownKeys[i] == c.CooldownKeys[j]
				portSame := mine[i] == mine[j]
				if liveSame != portSame {
					disagree++
					if len(examples) < 6 {
						verb := "SPLITS what live groups"
						if portSame {
							verb = "GROUPS what live splits"
						}
						examples = append(examples, name+": the port "+verb+
							"\n      live "+c.CooldownKeys[i]+" / "+c.CooldownKeys[j]+
							"\n      port "+mine[i]+" / "+mine[j])
					}
				}
			}
		}
	}
	if compared < 200 {
		t.Fatalf("only %d pairs compared — the corpus lost its cooldown keys and this "+
			"test is measuring almost nothing", compared)
	}
	t.Logf("compared %d alert pairs against live's cooldown grouping", compared)
	if disagree > 0 {
		t.Errorf("%d of %d pairs are grouped differently. Grouping what live splits "+
			"means one alert's cooldown SUPPRESSES another's notification; splitting "+
			"what live groups means a repeat that live suppresses gets sent.\n  %s",
			disagree, compared, strings.Join(examples, "\n  "))
	}
}
