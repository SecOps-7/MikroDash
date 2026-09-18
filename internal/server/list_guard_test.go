package server

import (
	"testing"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
)

// THE ADAPTER BETWEEN A WRITE AND THE LIST GUARD (2026-09-18).
//
// guard/listguard_test.go pins the judgement. These pin what the server adds in
// front of it: the stored row read by its RouterOS names, a PARTIAL edit laid
// over it (the assistant's change_row sends only what changes), and only the
// filter rows whose clause matches this kind of list reaching the guard.

var listPath = guard.ManagementPath{Resolved: true, Addresses: []string{"198.51.100.5"},
	Interfaces: []string{"ether2"}, Address: "198.51.100.5"}

func filterRow(id, chain, action, clause, match string) routeros.Reply {
	return routeros.Reply{".id": id, "chain": chain, "action": action, "disabled": "false", clause: match}
}

func TestAPartialEditThatMovesAnEntryOntoUsWarns(t *testing.T) {
	stored := map[string]string{".id": "*1", "list": "blocklist", "address": "198.51.100.77", "disabled": "false"}
	rules := []routeros.Reply{filterRow("*4", "input", "drop", "src-address-list", "blocklist")}
	// Only the address is sent; the list comes from the stored row.
	v := listDecision(resource.AddressList, "update", map[string]string{"address": "198.51.100.5"}, stored, listPath, 8729, rules)
	if !v.Warned() || v.Detail["list"] != "blocklist" || v.Detail["move"] != "joins" {
		t.Fatalf("pointing a blocklist entry at our own address: %+v", v)
	}
	// Control: the same edit to someone else's address.
	if v := listDecision(resource.AddressList, "update", map[string]string{"address": "198.51.100.78"}, stored, listPath, 8729, rules); v.Level != "none" {
		t.Errorf("pointing it at another address warned: %+v", v)
	}
}

func TestDisablingOurAcceptEntryWarns(t *testing.T) {
	stored := map[string]string{".id": "*2", "list": "mgmt", "address": "198.51.100.0/24", "disabled": "false"}
	rules := []routeros.Reply{filterRow("*3", "input", "accept", "src-address-list", "mgmt")}
	// The form sends a checkbox as RouterOS spells it.
	if v := listDecision(resource.AddressList, "update", map[string]string{"disabled": "yes"}, stored, listPath, 8729, rules); !v.Warned() || v.Detail["effect"] != "loses-accept" {
		t.Fatalf("disabling the entry that admits us: %+v", v)
	}
	if v := listDecision(resource.AddressList, "delete", nil, stored, listPath, 8729, rules); !v.Warned() {
		t.Fatalf("deleting the entry that admits us: %+v", v)
	}
}

// Only the clause for THIS kind of list reaches the guard, and only input rules.
func TestOnlyMatchingRulesReachTheGuard(t *testing.T) {
	stored := map[string]string{".id": "*2", "list": "mgmt", "address": "198.51.100.0/24", "disabled": "false"}
	for name, rules := range map[string][]routeros.Reply{
		"an interface-list rule of the same name": {filterRow("*3", "input", "accept", "in-interface-list", "mgmt")},
		"a forward rule":  {filterRow("*3", "forward", "accept", "src-address-list", "mgmt")},
		"no rules at all": nil,
	} {
		if v := listDecision(resource.AddressList, "delete", nil, stored, listPath, 8729, rules); v.Level != "none" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// The resource declares the guard, so every write path runs it.
func TestAddressListDeclaresTheListGuard(t *testing.T) {
	for _, g := range resource.AddressList.Guard {
		if g == "listLockout" && portedGuards[g] {
			return
		}
	}
	t.Fatalf("addressList declares %v; without listLockout its writes are unguarded", resource.AddressList.Guard)
}

// Interface lists: the default `!LAN drop`, reached by a member and by the list.
func TestInterfaceListWritesReachTheGuard(t *testing.T) {
	rules := []routeros.Reply{filterRow("*5", "input", "drop", "in-interface-list", "!LAN")}
	member := map[string]string{".id": "*1", "list": "LAN", "interface": "ether2", "disabled": "false"}
	if v := listDecision(resource.IfListMember, "update", map[string]string{"disabled": "yes"}, member, listPath, 8729, rules); !v.Warned() || v.Code != "list-cutoff" {
		t.Errorf("disabling the management port's LAN membership: %+v", v)
	}
	lan := map[string]string{".id": "*2", "name": "LAN", "include": "", "exclude": ""}
	if v := listDecision(resource.IfList, "delete", nil, lan, listPath, 8729, rules); !v.Warned() || v.Code != "list-redefine" {
		t.Errorf("deleting LAN: %+v", v)
	}
	// Controls: a comment on either, and the same member on another port.
	if v := listDecision(resource.IfList, "update", map[string]string{"comment": "x"}, lan, listPath, 8729, rules); v.Level != "none" {
		t.Errorf("a comment on LAN warned: %+v", v)
	}
	other := map[string]string{".id": "*3", "list": "LAN", "interface": "ether5", "disabled": "false"}
	if v := listDecision(resource.IfListMember, "delete", nil, other, listPath, 8729, rules); v.Level != "none" {
		t.Errorf("removing another port from LAN warned: %+v", v)
	}
	for _, r := range []*resource.Resource{resource.IfList, resource.IfListMember} {
		found := false
		for _, g := range r.Guard {
			found = found || g == "listLockout"
		}
		if !found {
			t.Errorf("%s does not declare listLockout", r.Key)
		}
	}
}

// THE SERVICE ADAPTER: which row is ours follows TLS, a partial edit is laid
// over the stored row, and the row actions count as the edits they are.
func TestServiceWritesReachTheGuard(t *testing.T) {
	self := []string{"198.51.100.5"}
	apiSSL := map[string]string{".id": "*6", "name": "api-ssl", "port": "8729", "address": "", "disabled": "false"}
	if v := serviceDecision(true, self, true, "update", map[string]string{"port": "9000"}, apiSSL); !v.Refused() || v.Code != "service-port" {
		t.Errorf("re-porting api-ssl over TLS: %+v", v)
	}
	if v := serviceDecision(true, self, true, "disable", nil, apiSSL); !v.Refused() || v.Code != "service-disable" {
		t.Errorf("the disable action on api-ssl: %+v", v)
	}
	// Controls: the same edit when MikroDash speaks plain api, and a comment-free
	// no-op on our own row.
	if v := serviceDecision(false, self, true, "update", map[string]string{"port": "9000"}, apiSSL); v.Level != "none" {
		t.Errorf("re-porting api-ssl while MikroDash uses plain api: %+v", v)
	}
	if v := serviceDecision(true, self, true, "update", map[string]string{"address": "198.51.100.0/24"}, apiSSL); v.Level != "none" {
		t.Errorf("an address list that admits us: %+v", v)
	}
}
