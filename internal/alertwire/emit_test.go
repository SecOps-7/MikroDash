package alertwire

import (
	"testing"

	"mikrodash/internal/alert"
	"mikrodash/internal/collect"
	"mikrodash/internal/hub"
)

// EVERY RULE FAMILY RUNS ON A PAYLOAD SENT THE WAY A COLLECTOR SENDS IT.
//
// The other tests hand `Evaluate` payloads built by hand, so when the events
// became typed values (6673154, v0.8.52) the switch kept matching pointers, the
// suite stayed green, and no alert rule ran on live data (found with issue
// #135). Each event goes through `Event[T].Emit` and a `hub.Relay`, which is
// what the session's closure receives.
//
// "Runs" is read off `Routers()`: an evaluator is built only once a rule family
// has matched the payload, so a fresh wire with one evaluator after one emit
// means that family ran. The CPU and ping cases also assert what fired.
func TestEveryRuleRunsOnAnEmittedPayload(t *testing.T) {
	loss := 100
	emits := map[string]func(hub.Relay){
		"system:update": func(r hub.Relay) {
			collect.EvSystemUpdate.Emit(r, "", collect.SystemPayload{CPULoad: 94})
		},
		"ping:update": func(r hub.Relay) {
			collect.EvPingUpdate.Emit(r, "", collect.PingPayload{Target: "1.1.1.1", Loss: &loss})
		},
		"ifstatus:update": func(r hub.Relay) {
			collect.EvIfstatusUpdate.Emit(r, "", collect.IfStatusPayload{})
		},
		"vpn:update": func(r hub.Relay) {
			collect.EvVpnUpdate.Emit(r, "", collect.VPNPayload{})
		},
		"netwatch:update": func(r hub.Relay) {
			collect.EvNetwatchUpdate.Emit(r, "", collect.NetwatchPayload{})
		},
		"routing:update": func(r hub.Relay) {
			collect.EvRoutingUpdate.Emit(r, "", collect.RoutingPayload{})
		},
	}
	for event, emit := range emits {
		w, _ := wireOn(t)
		var fired []alert.Fired
		emit(hub.NewRelay(func(_ string, e hub.Named, payload any) {
			fired = append(fired, w.Evaluate(router, e.Name(), payload)...)
		}))
		if w.Routers() != 1 {
			t.Errorf("%s: no rule ran on the emitted payload; the evaluator does not accept "+
				"the type the event sends", event)
		}
		if (event == "system:update" || event == "ping:update") && len(fired) != 1 {
			t.Errorf("%s: an emitted payload over the threshold fired %v", event, fired)
		}
	}
}
