package resource

import (
	"slices"
	"strings"
	"testing"
)

// FIELDS A ROUTER BEFORE 7.25 DOES NOT KNOW ARE SENT ONLY WHEN FILLED IN.
//
// RouterOS refuses a write that names a property it does not have, so a field
// added in 7.25 that went out on every save would break every save on a 7.24
// router: a rename, a comment, a disable. Blank must mean "not sent at all" on
// a create, on an edit through the form (which carries the stored row) and on
// an undo or fleet write (which does not), and never the `=!prop=` removal
// form. A value that is filled in is sent as typed.
func TestA725FieldIsSentOnlyWhenFilledIn(t *testing.T) {
	for _, tc := range []struct {
		res          *Resource
		field, ros   string
		base         map[string]string
		value, wrong string
	}{
		{WgPeer, "clientMtu", "client-mtu",
			map[string]string{"interface": "wg0", "publicKey": strings.Repeat("A", 43) + "=", "allowedAddress": "10.0.0.2/32"},
			"1380", "abc"},
		{DHCPServer, "ipv6OnlyPreferred", "ipv6-only-preferred",
			map[string]string{"name": "dhcp1", "interface": "bridge"},
			"yes", "maybe"},
	} {
		for _, editing := range []bool{false, true} {
			for _, stored := range []map[string]string{nil, {"comment": "x"}} {
				v, errs := tc.res.Validate(clone(tc.base), editing)
				if len(errs) > 0 {
					t.Fatalf("%s: the base row is invalid: %v", tc.res.Key, errs)
				}
				v.Stored = stored
				for _, a := range tc.res.BuildArgs(v) {
					if strings.Contains(a, tc.ros) {
						t.Errorf("%s editing=%v stored=%v: blank %s was sent as %q; a router "+
							"before 7.25 refuses the whole write", tc.res.Key, editing, stored != nil, tc.field, a)
					}
				}
			}
			vals := clone(tc.base)
			vals[tc.field] = tc.value
			v, errs := tc.res.Validate(vals, editing)
			if len(errs) > 0 {
				t.Fatalf("%s: %s=%s refused: %v", tc.res.Key, tc.field, tc.value, errs)
			}
			if !slices.Contains(tc.res.BuildArgs(v), "="+tc.ros+"="+tc.value) {
				t.Errorf("%s editing=%v: %s=%s was not sent: %v", tc.res.Key, editing, tc.field, tc.value, tc.res.BuildArgs(v))
			}
		}
		// THE FORM'S ROUND TRIP. An edit opens with RowValues of the stored row,
		// and a 7.24 row has no such property: whatever the form pre-fills is
		// sent back on save, so a declared default would reach the old router.
		if got, ok := tc.res.RowValues(map[string]string{"comment": "x"})[tc.field]; ok && got != "" {
			t.Errorf("%s: a row without %s opens the form with %q, which the next save "+
				"sends to a router that does not know it", tc.res.Key, tc.ros, got)
		}
		vals := clone(tc.base)
		vals[tc.field] = tc.wrong
		if _, errs := tc.res.Validate(vals, true); len(errs) == 0 {
			t.Errorf("%s: %s=%q was accepted", tc.res.Key, tc.field, tc.wrong)
		}
	}
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestAWireGuardClientFieldIsClearedOnlyWhereTheRouterHasIt (ClearIfHeld). A
// peer save sent every blank client field as a clear, so on a router before
// 7.21 a rename was refused for `client-allowed-address`, a property it does
// not have. The rows are the shapes RouterOS returns: measured on CHR Test,
// 7.25.1, an empty `client-endpoint` and `client-allowed-address` are listed
// and an empty `client-address`, `client-dns` and `client-keepalive` are not.
func TestAWireGuardClientFieldIsClearedOnlyWhereTheRouterHasIt(t *testing.T) {
	base := map[string]string{"interface": "wg0", "publicKey": strings.Repeat("A", 43) + "=",
		"allowedAddress": "10.0.0.2/32", "comment": "renamed"}
	args := func(stored map[string]string) []string {
		v, errs := WgPeer.Validate(clone(base), true)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		v.Stored = stored
		return WgPeer.BuildArgs(v)
	}
	has := func(a []string, w string) bool { return slices.Contains(a, w) }

	// A 7.20 router: no client-allowed-address at all, the other two listed.
	old := args(map[string]string{"client-endpoint": "", "client-listen-port": "0"})
	if has(old, "=client-allowed-address=") {
		t.Errorf("a router without client-allowed-address was sent a clear of it: %v", old)
	}
	if !has(old, "=client-endpoint=") {
		t.Errorf("a property the router lists was not cleared: %v", old)
	}

	// 7.25.1: a client DNS that HAS a value is listed, so blanking it clears it.
	cur := args(map[string]string{"client-endpoint": "", "client-allowed-address": "", "client-dns": "1.1.1.1"})
	for _, w := range []string{"=client-dns=", "=client-allowed-address=", "=client-endpoint="} {
		if !has(cur, w) {
			t.Errorf("%s was not cleared on a router that holds it: %v", w, cur)
		}
	}
	// ...and one left out because it is empty has nothing to clear.
	if has(cur, "=client-keepalive=") || has(cur, "=client-address=") {
		t.Errorf("an empty, unlisted property was cleared: %v", cur)
	}

	// No stored row (an undo, a fleet write): clears as before, so an undo of a
	// change that set a client field still takes it back.
	if a := args(nil); !has(a, "=client-dns=") || !has(a, "=client-allowed-address=") {
		t.Errorf("without the stored row the clears went missing: %v", a)
	}
}
