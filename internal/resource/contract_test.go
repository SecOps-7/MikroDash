package resource

import (
	"strings"
	"testing"
)

// WHAT A FORM IS, BEYOND ITS FIELDS, IS WRITTEN DOWN HERE.
//
// None of these is a field, so no field check sees them, and each fails
// silently when lost:
//
//   - guard: a resource that loses its guard simply stops being checked, which
//     is the one failure a guard cannot survive;
//   - identity: the staleness check. A wrong one fails to REFUSE a write on a
//     row that changed under the operator;
//   - actions: `makeStatic` is the only way a dynamic DHCP lease can be edited
//     at all, so losing it makes a row unreachable rather than wrong;
//   - requiresMenu: what stops a form being offered where its menu is absent;
//   - ordered: draws the reorder arrows, and is true only where the router's
//     order decides (CAPsMAN provisioning rules, first match wins).
//
// These were held to the Node app's declarations until 2026-11-27. There is no
// outside reference for them: they are this app's design, so the table IS the
// reference, and a change to one is a deliberate edit here.
var formContract = map[*Resource]struct {
	guard, identity, actions, requiresMenu string
	ordered                                bool
}{
	Bridge:     {guard: "selfPath", identity: "name"},
	BridgePort: {guard: "selfPath", identity: "interface"},
	Vlan:       {guard: "selfPath", identity: "name"},
	Route:      {guard: "routePath", identity: "dstAddress"},
	Route6:     {guard: "routePath", identity: "dstAddress"},
	DHCPLease:  {identity: "macAddress", actions: "makeStatic|make-static|Make Static"},
	WgPeer:     {identity: "publicKey"},
	WifiNet: {guard: "selfPath,wifiInherit", identity: "name",
		actions: "enable|enable|Enable,disable|disable|Disable", requiresMenu: "/interface/wifi"},
	WlNet: {guard: "selfPath", identity: "name",
		actions: "enable|enable|Enable,disable|disable|Disable", requiresMenu: "/interface/wireless"},
	WlSecProfile: {identity: "name", requiresMenu: "/interface/wireless/security-profiles"},
	CapsProvisioningRes: {identity: "supportedBands,action,masterConfiguration,nameFormat",
		actions: "enable|enable|Enable,disable|disable|Disable", requiresMenu: "/interface/wifi/provisioning",
		ordered: true},
	CapsConfig:   {guard: "capsmanPush", identity: "name", requiresMenu: "/interface/wifi/configuration"},
	CapsSecurity: {guard: "capsmanPush", identity: "name", requiresMenu: "/interface/wifi/security"},
	CapsChannel:  {guard: "capsmanPush", identity: "name", requiresMenu: "/interface/wifi/channel"},
	CapsDatapath: {guard: "capsmanPush", identity: "name", requiresMenu: "/interface/wifi/datapath"},
}

func TestEveryRouterHeldFormKeepsItsContract(t *testing.T) {
	// Every form the menu recordings hold is in the table, and nothing else.
	inGate := map[*Resource]bool{}
	for _, forms := range routerHeld {
		for _, r := range forms {
			inGate[r] = true
			if _, ok := formContract[r]; !ok {
				t.Errorf("%s is held to a menu recording and has no contract here", r.Key)
			}
		}
	}
	for r, want := range formContract {
		if !inGate[r] {
			t.Errorf("%s has a contract here and is held to no menu recording", r.Key)
		}
		var acts []string
		for _, a := range r.Actions {
			acts = append(acts, a.Key+"|"+a.Verb+"|"+a.Label)
		}
		got := map[string]string{
			"guard": strings.Join(r.Guard, ","), "identity": strings.Join(r.Identity, ","),
			"actions": strings.Join(acts, ","), "requiresMenu": r.RequiresMenu,
		}
		for what, w := range map[string]string{"guard": want.guard, "identity": want.identity,
			"actions": want.actions, "requiresMenu": want.requiresMenu} {
			if got[what] != w {
				t.Errorf("%s %s: %q, the contract says %q", r.Key, what, got[what], w)
			}
		}
		if r.Ordered != want.ordered {
			t.Errorf("%s ordered: %v, the contract says %v", r.Key, r.Ordered, want.ordered)
		}
	}
}
