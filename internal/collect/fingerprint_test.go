package collect

import (
	"reflect"
	"testing"
)

// The change-fingerprint gate.
//
// ── WHY A REFLECTIVE TEST AND NOT A LIST OF CASES ───────────────────────────
//
// A fingerprint built from a hand-listed tuple cannot see a field left off the
// list. The collector re-reads the router, hashes an identical string and
// returns WITHOUT EMITTING — so an edit that really landed never reaches an open
// page. It hides because these collectors also hash something that moves on its
// own: on a busy router the fingerprint changes for an unrelated reason a tick
// or two later and the table catches up, looking merely slow. On an idle device
// the update never arrives at all.
//
// The live app had exactly this in four collectors, and the reason it survived
// is that NO GATE COULD SEE IT. The goldens compare payload SHAPE; this is emit
// FREQUENCY. Both sides can be byte-identical in every payload and still differ
// in when they send one.
//
// So the rule — every field the page renders belongs in the fingerprint — is
// enforced here by walking the payload row with reflection rather than by
// listing fields. A field ADDED to a payload later and forgotten in the
// fingerprint fails this test on the day it is added, which a case list cannot
// do because nobody adds the case they forgot.
//
// ── THE EXCLUSIONS ARE THE INTERESTING PART ─────────────────────────────────
//
// Each name below is a field that must NOT move the fingerprint, and each costs
// something to get wrong in the opposite direction. Byte counters creep up on an
// idle link from broadcast traffic alone; including them would defeat the
// suppression the check exists for and put every collector back to emitting
// every tick.

// mutate sets a field to a value different from the one it holds, for the field
// kinds these payloads actually use.
func mutate(f reflect.Value) bool {
	switch f.Kind() {
	case reflect.String:
		f.SetString(f.String() + "-changed")
		return true
	case reflect.Bool:
		f.SetBool(!f.Bool())
		return true
	case reflect.Float64, reflect.Float32:
		f.SetFloat(f.Float() + 7)
		return true
	case reflect.Int, reflect.Int64:
		f.SetInt(f.Int() + 7)
		return true
	case reflect.Slice:
		if f.Type().Elem().Kind() == reflect.String {
			f.Set(reflect.ValueOf([]string{"changed"}))
			return true
		}
	case reflect.Ptr:
		if f.Type().Elem().Kind() == reflect.Float64 {
			v := 99.0
			if !f.IsNil() {
				v = f.Elem().Float() + 7
			}
			f.Set(reflect.ValueOf(&v))
			return true
		}
	}
	return false
}

// assertFieldsCovered walks one row struct, mutating each field in turn and
// checking the fingerprint reacts — except for the named exclusions, which must
// NOT move it.
func assertFieldsCovered(
	t *testing.T, label string, row any, exclude map[string]string, fp func(any) string,
) {
	t.Helper()
	rt := reflect.TypeOf(row).Elem()
	base := fp(row)

	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		fresh := reflect.New(rt)
		fresh.Elem().Set(reflect.ValueOf(row).Elem())
		field := fresh.Elem().Field(i)
		if !field.CanSet() || !mutate(field) {
			continue
		}
		moved := fp(fresh.Interface()) != base

		if why, excluded := exclude[name]; excluded {
			if moved {
				t.Errorf("%s.%s moved the fingerprint and must not: %s", label, name, why)
			}
			continue
		}
		if !moved {
			t.Errorf("%s.%s does NOT move the fingerprint.\n"+
				"    Every field the page renders belongs in it — otherwise an edit to this "+
				"field is re-read, hashed identically, and never emitted, so an open page "+
				"keeps showing the old value after a save that really landed.\n"+
				"    If it genuinely should not emit (a counter that moves on its own), add "+
				"it to the exclusion map with the reason.", label, name)
		}
	}
}

func TestIfStatusFingerprintCoversEveryRenderedField(t *testing.T) {
	row := &Interface{
		Name: "ether1", Type: "ether", Running: true, Disabled: false,
		Comment: "uplink", MacAddr: "02:00:00:00:00:01",
		RxMbps: 1.5, TxMbps: 2.5, IPs: []string{"198.51.100.1/24"},
	}
	assertFieldsCovered(t, "Interface", row, map[string]string{
		// Byte totals creep up on an idle link from broadcast traffic alone.
		// Including them defeats the idle suppression entirely — the collector
		// would emit every tick and the 60s heartbeat would be pointless.
		"RxBytes": "a cumulative counter that moves on an idle link",
		"TxBytes": "a cumulative counter that moves on an idle link",
		// Derived from Errors/Drops, which ARE in. A window that shifts with no
		// underlying change is not a change worth pushing.
		"ErrorsDelta":   "derived from Errors, which is covered",
		"DropsDelta":    "derived from Drops, which is covered",
		"DeltaWindowMs": "the measurement window, not a property of the link",
		// Not rendered by the list view.
		"LastLinkUp": "not a rendered column; LinkDowns covers a flap",
	}, func(r any) string { return ifStatusFingerprint([]Interface{*r.(*Interface)}) })
}

func TestFirewallFingerprintCoversTheWholeRule(t *testing.T) {
	f := &Firewall{}
	row := FirewallRule{}
	// Fill what the type actually has, generically: the point is coverage of
	// every field, so the starting values only have to be non-zero.
	rv := reflect.ValueOf(&row).Elem()
	for i := 0; i < rv.NumField(); i++ {
		mutate(rv.Field(i))
	}
	assertFieldsCovered(t, "FirewallRule", &row, nil, func(r any) string {
		return f.fingerprint(&FirewallPayload{Filter: []FirewallRule{*r.(*FirewallRule)}})
	})
}

// The SECOND firewall fingerprint gate, and the one that had no cover at all.
//
// `TestFirewallFingerprintCoversTheWholeRule` above reflects over
// **FirewallRule** and always parks its row in `Filter`. That proves every field
// of a rule is hashed. It proves nothing whatever about the payload's OTHER
// TABLES: `fingerprint` names them one at a time in a map literal, and a table
// left off that literal fails no test.
//
// Which matters the moment there is more than one family. Forget `filter6` and
// the collector re-reads the router, hashes an identical string and returns
// without emitting — so an IPv6 edit never reaches an open page on a quiet
// ruleset. Exactly the defect the comment on `fingerprint` was written about,
// one level up: there it was a field, here it is a whole table.
//
// So: every slice field on the payload, discovered rather than listed, must move
// the fingerprint on its own AND be distinguishable from every other. The second
// half is what catches a copy-paste that hashes `p.Filter` twice under two keys.
func TestFirewallFingerprintCoversEveryTable(t *testing.T) {
	f := &Firewall{}
	rt := reflect.TypeOf(FirewallPayload{})
	row := []FirewallRule{{ID: "x", Chain: "input", Action: "drop"}}

	base := f.fingerprint(&FirewallPayload{})
	seen := map[string]string{} // fingerprint -> the field that produced it
	tables := 0

	for i := 0; i < rt.NumField(); i++ {
		ft := rt.Field(i)
		if ft.Type != reflect.TypeOf([]FirewallRule(nil)) {
			continue
		}
		tables++
		p := &FirewallPayload{}
		reflect.ValueOf(p).Elem().Field(i).Set(reflect.ValueOf(row))
		got := f.fingerprint(p)

		if got == base {
			t.Errorf("%s does not reach fingerprint(): a change to that table "+
				"would never be emitted", ft.Name)
			continue
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s fingerprint identically — fingerprint() is "+
				"reading the same field under two keys", ft.Name, other)
			continue
		}
		seen[got] = ft.Name
	}

	// A guard against the test quietly measuring nothing if the payload is ever
	// restructured away from named slice fields.
	if tables != 8 {
		t.Fatalf("found %d rule tables on FirewallPayload, expected 8 "+
			"(four IPv4, four IPv6) — update this test deliberately", tables)
	}

	// Ipv6Disabled is not a table, but it is rendered: it decides whether the
	// page shows an IPv6 tab, so a change to it has to reach the page too.
	yes, no := true, false
	fpNil := f.fingerprint(&FirewallPayload{})
	fpYes := f.fingerprint(&FirewallPayload{Ipv6Disabled: &yes})
	fpNo := f.fingerprint(&FirewallPayload{Ipv6Disabled: &no})
	if fpNil == fpYes || fpNil == fpNo || fpYes == fpNo {
		t.Errorf("Ipv6Disabled's three states must be distinguishable: "+
			"nil=%q true=%q false=%q", fpNil, fpYes, fpNo)
	}
}
