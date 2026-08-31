package rbac

import (
	"reflect"
	"testing"
)

func TestAnAdministratorMaySetMembership(t *testing.T) {
	body := map[string]any{"label": "Core", "siteIds": []any{"s1"}, "siteId": "s1"}
	if dropped := StripPrivilegedRouterFields(body, true); dropped != nil {
		t.Errorf("dropped %v from an administrator's write", dropped)
	}
	if _, ok := body["siteIds"]; !ok {
		t.Error("siteIds was removed from an administrator's write")
	}
}

// THE ESCALATION THIS EXISTS FOR. `router:manage` on one device is enough to
// reach this route, so without the strip a non-administrator could add their own
// device to any site — additively, invisibly, and with every site id enumerable
// from an ungated endpoint.
func TestANonAdministratorCannotSetMembership(t *testing.T) {
	body := map[string]any{"label": "Core", "siteIds": []any{"s1", "s2"}}
	dropped := StripPrivilegedRouterFields(body, false)
	if !reflect.DeepEqual(dropped, []string{"siteIds"}) {
		t.Errorf("dropped = %v, want [siteIds]", dropped)
	}
	if _, ok := body["siteIds"]; ok {
		t.Error("siteIds survived a non-administrator's write")
	}
	// THE REST OF THE EDIT IS LEFT ALONE. The live route applies it rather than
	// refusing the request, and a strip that took the label with it would be a
	// different, louder behaviour.
	if body["label"] != "Core" {
		t.Errorf("label = %v; only the privileged fields may be dropped", body["label"])
	}
}

// THE MIRROR IS AN INJECTION PATH TOO. `siteId` is the scalar the store keeps in
// step with the first entry, so dropping only `siteIds` leaves the escalation
// open through the older field.
func TestTheScalarMirrorIsStrippedAsWell(t *testing.T) {
	body := map[string]any{"siteId": "s1"}
	dropped := StripPrivilegedRouterFields(body, false)
	if !reflect.DeepEqual(dropped, []string{"siteId"}) {
		t.Errorf("dropped = %v, want [siteId]", dropped)
	}
	if _, ok := body["siteId"]; ok {
		t.Error("the siteId mirror survived a non-administrator's write — membership can still " +
			"be set through it")
	}
}

func TestBothKeysAreReportedInOrder(t *testing.T) {
	body := map[string]any{"siteIds": []any{"s1"}, "siteId": "s1"}
	if dropped := StripPrivilegedRouterFields(body, false); !reflect.DeepEqual(
		dropped, []string{"siteIds", "siteId"}) {
		t.Errorf("dropped = %v, want [siteIds siteId] in the original's order", dropped)
	}
}

// A body that never mentioned the fields reports nothing, so a caller auditing
// the return value does not record an attempt nobody made.
func TestAnUnrelatedWriteReportsNothing(t *testing.T) {
	body := map[string]any{"label": "Core"}
	if dropped := StripPrivilegedRouterFields(body, false); dropped != nil {
		t.Errorf("dropped = %v from a write that named neither field", dropped)
	}
}

func TestANilBodyIsNotAnError(t *testing.T) {
	if dropped := StripPrivilegedRouterFields(nil, false); dropped != nil {
		t.Errorf("dropped = %v from a nil body", dropped)
	}
}
