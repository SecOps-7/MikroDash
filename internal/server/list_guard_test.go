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
