package resource

import (
	"strings"
	"testing"
)

// THE INTERFACE RESOURCE EDITS A COMMENT AND THE ENABLED FLAG, AND NOTHING ELSE.
//
// Its name identifies the row and is shown in the form, but renaming an
// interface from here would orphan everything this app keys on the name: WAN
// uplinks, the traffic pick, topology pins. So the name is a DISPLAY field, and
// these pin what that has to mean end to end: never validated, never built into
// a sentence, and never mistaken for a rename by the lockout guard.

func TestAnInterfaceNameIsShownAndNeverSent(t *testing.T) {
	// A hand-built request that carries a changed name anyway.
	v, errs := Iface.Validate(map[string]string{
		"name": "renamed", "comment": "uplink", "disabled": "false",
	}, true)
	if len(errs) > 0 {
		t.Fatalf("a comment edit failed validation: %+v", errs)
	}
	if _, has := v.Values["name"]; has {
		t.Errorf("the display field was validated into the write: %+v", v.Values)
	}
	args := strings.Join(Iface.BuildArgs(v), " ")
	if strings.Contains(args, "=name=") {
		t.Errorf("the name reached the RouterOS sentence: %s", args)
	}
	for _, want := range []string{"=comment=uplink", "=disabled=no"} {
		if !strings.Contains(args, want) {
			t.Errorf("sentence %q lacks %s", args, want)
		}
	}
}

func TestTheInterfaceSchemaSaysWhatTheFormMayDo(t *testing.T) {
	d := Iface.Describe()
	if d["creatable"] != false {
		t.Errorf("creatable = %v, want false: /interface has no add", d["creatable"])
	}
	fields, _ := d["fields"].([]map[string]any)
	display := map[string]bool{}
	for _, f := range fields {
		display[f["name"].(string)] = f["display"].(bool)
	}
	if !display["name"] || display["comment"] || display["disabled"] {
		t.Errorf("display flags = %v, want only name", display)
	}
	if DNSStatic.Describe()["creatable"] != true {
		t.Error("an ordinary resource described itself as not creatable")
	}
}

func TestOnlyADisableIsAnInterfaceGuardTarget(t *testing.T) {
	before := map[string]string{".id": "*1", "name": "ether1", "disabled": "false", "comment": ""}

	comment, _ := Iface.Validate(map[string]string{"comment": "uplink", "disabled": "false"}, true)
	if got := Iface.GuardTargets("update", comment.Values, before); len(got) != 0 {
		t.Errorf("a comment-only edit is a guard target %v; the never-sent name read as a rename", got)
	}

	disable, _ := Iface.Validate(map[string]string{"comment": "", "disabled": "true"}, true)
	got := Iface.GuardTargets("update", disable.Values, before)
	if len(got) != 1 || got[0] != "ether1" {
		t.Errorf("disabling ether1 gave targets %v, want [ether1]: selfPath would never warn", got)
	}

	// The display rule must not reach a resource whose name IS sent.
	vlanBefore := map[string]string{"name": "vlan10", "vlan-id": "10", "interface": "bridge", "disabled": "false"}
	rename, errs := Vlan.Validate(map[string]string{
		"name": "vlan20", "vlanId": "10", "interface": "bridge", "disabled": "false",
	}, true)
	if len(errs) > 0 {
		t.Fatalf("vlan rename failed validation: %+v", errs)
	}
	if got := Vlan.GuardTargets("update", rename.Values, vlanBefore); len(got) == 0 {
		t.Error("renaming a VLAN is no longer a guard target")
	}
}

func TestAnInterfaceIsNeverRemovedAndADynamicOneIsReadOnly(t *testing.T) {
	if Iface.RemovableWhen(map[string]string{"name": "ether1"}) {
		t.Error("an interface row is removable")
	}
	if !Iface.NoCreate {
		t.Error("the interface resource allows a create")
	}
	if !Iface.ReadOnlyWhen(map[string]string{"name": "<pppoe-home>", "dynamic": "true"}) {
		t.Error("a dynamic interface is editable")
	}
	if Iface.ReadOnlyWhen(map[string]string{"name": "ether1", "dynamic": "false"}) {
		t.Error("a static interface is read-only")
	}
}

// A DISPLAY FIELD IS NEVER CLEARABLE.
//
// Validate is the one place a display field is dropped, and BuildArgs clears any
// clearable field an edit did not send. A field that were both would therefore
// be sent BLANK on every edit, which for an interface is a rename to nothing.
// The combination is refused here rather than guarded twice at runtime.
func TestNoDisplayFieldIsClearable(t *testing.T) {
	seen := 0
	for _, r := range All() {
		for _, f := range r.Fields {
			if !f.Display {
				continue
			}
			seen++
			if f.Clearable {
				t.Errorf("%s.%s is both Display and Clearable: an edit would send it blank",
					r.Key, f.Name)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no display field in the registry; this test is measuring nothing")
	}
}
