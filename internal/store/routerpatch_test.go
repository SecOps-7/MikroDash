package store

import (
	"os"
	"path/filepath"
	"testing"
)

// ── ONE BAD FIELD USED TO TAKE THE WHOLE FLEET ─────────────────────────────
//
// This is the test that matters, and it is written at the FLEET level rather
// than the field level on purpose: the damage is not "one router has a wrong
// value", it is `Routers()` returning nothing at all, because routers.json is
// decoded into `[]Router` in a single Unmarshal.
func TestAStringWhereABoolBelongsDoesNotEraseTheFleet(t *testing.T) {
	for _, f := range []struct{ key, val string }{
		{"disabled", `"false"`},
		{"disabled", `"true"`},
		{"tls", `"false"`},
		{"tlsInsecure", `"true"`},
		{"alertsEnabled", `"1"`},
		{"port", `"8729"`},
		{"bwDownMbps", `"500"`},
		{"connDownThresholdSec", `"45"`},
	} {
		t.Run(f.key+"="+f.val, func(t *testing.T) {
			st := twoRouterStore(t)

			// The patch a client can send, typed the way the route now types it.
			patch := CoerceRouterPatch(map[string]any{f.key: jsonValue(t, f.val)})
			if err := st.UpdateRouter("a", patch); err != nil {
				t.Fatalf("UpdateRouter: %v", err)
			}

			rs, problems := st.Routers()
			if len(rs) != 2 {
				t.Errorf("the fleet is %d routers, want 2 — a %s of %s made routers.json "+
					"undecodable, so EVERY router vanished, not just this one. problems=%v",
					len(rs), f.key, f.val, problems)
			}
			for _, p := range problems {
				t.Errorf("unexpected problem: %v", p)
			}
		})
	}
}

// The coercions themselves, against the live expressions they mirror.
func TestCoerceRouterPatchMatchesTheLiveExpressions(t *testing.T) {
	for _, c := range []struct {
		key  string
		in   any
		want any
	}{
		// `data.tls !== false && data.tls !== 'false'`
		{"tls", "false", false}, {"tls", false, false},
		{"tls", "true", true}, {"tls", "anything", true},
		// `=== true || === 'true'` — the exact word only (dccbf62).
		{"tlsInsecure", "false", false}, {"tlsInsecure", "true", true},
		{"tlsInsecure", "TRUE", false}, {"tlsInsecure", "1", false},
		{"tlsInsecure", true, true},
		// `_isTrue` since upstream `dd6173b` — the SAME rule as tlsInsecure, no
		// longer `!!` truthiness. These four lines are the fix: `"false"` used to
		// be TRUE here, so a PUT sending `disabled: "false"` — an operator
		// ENABLING a router — disabled it and tore the session down.
		{"disabled", "false", false}, {"disabled", false, false}, {"disabled", "", false},
		{"disabled", "true", true}, {"disabled", true, true},
		{"alertsEnabled", "false", false}, {"alertsEnabled", true, true},
		{"alertsEnabled", "true", true},
		// JUNK IS FALSE, which for `disabled` is the conservative direction:
		// leave the router in service. Upstream names these four by hand and so
		// does this, because "everything else is false" is the half of the rule a
		// reader assumes rather than checks.
		{"disabled", "1", false}, {"disabled", "yes", false},
		{"disabled", "on", false}, {"disabled", float64(1), false},
		{"alertsEnabled", "1", false}, {"tlsInsecure", "yes", false},
		// AND `tls` STILL DOES NOT USE IT. It defaults to ON, so its question is
		// "not false and not 'false'" — a different question whose safe direction
		// is the opposite one. `tls: "1"` is TRUE where `disabled: "1"` is false,
		// and that asymmetry is deliberate on both sides.
		{"tls", "1", true},
		// numbers
		{"port", "8729", 8729},
		{"bwDownMbps", "0", 1000}, {"bwDownMbps", "500", 500},
		{"connDownThresholdSec", "45", 45}, {"connDownThresholdSec", "999", 30},
	} {
		got := CoerceRouterPatch(map[string]any{c.key: c.in})[c.key]
		if got != c.want {
			t.Errorf("%s=%#v -> %#v, want %#v", c.key, c.in, got, c.want)
		}
	}
}

// AN ABSENT KEY IS UNTOUCHED — otherwise this stops being a patch.
func TestCoerceRouterPatchLeavesAbsentKeysAlone(t *testing.T) {
	got := CoerceRouterPatch(map[string]any{"label": "Alpha"})
	if len(got) != 1 {
		t.Errorf("the patch grew keys it was not given: %#v", got)
	}
	if got["label"] != "Alpha" {
		t.Errorf("an untyped field was altered: %#v", got["label"])
	}
}

func twoRouterStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `[
	  {"id":"a","label":"Alpha","host":"198.51.100.1","username":"u","password":"","disabled":false},
	  {"id":"b","label":"Beta","host":"198.51.100.2","username":"u","password":"","disabled":false}
	]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func jsonValue(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := jsonUnmarshalString(raw, &v); err != nil {
		t.Fatalf("fixture %q: %v", raw, err)
	}
	return v
}

// THE STORE PROTECTS ITSELF, not just the one route that happens to call it.
//
// `TestAStringWhereABoolBelongsDoesNotEraseTheFleet` passes its patch through
// `CoerceRouterPatch` explicitly, and `TestAWronglyTypedPatchDoesNotEraseTheFleet`
// goes through the HTTP route. Neither says anything about the OTHER five
// callers of `UpdateRouter`, which hand-construct patches from Go values.
//
// They are correct today. Nothing made them stay correct, and nothing made a
// sixth caller safe — the invariant was enforced by convention across six sites,
// which is the shape that fails. This drives the store DIRECTLY with the raw
// value a careless caller would pass.
func TestUpdateRouterTypesItsOwnPatch(t *testing.T) {
	for _, c := range []struct {
		key string
		raw any
	}{
		{"disabled", "false"},
		{"disabled", "true"},
		{"tls", "false"},
		{"tlsInsecure", "true"},
		{"alertsEnabled", "1"},
		{"port", "8729"},
		{"bwDownMbps", "500"},
	} {
		t.Run(c.key, func(t *testing.T) {
			st := twoRouterStore(t)
			// NOT through CoerceRouterPatch — that is the point.
			if err := st.UpdateRouter("a", map[string]any{c.key: c.raw}); err != nil {
				t.Fatalf("UpdateRouter: %v", err)
			}
			rs, problems := st.Routers()
			if len(rs) != 2 {
				t.Errorf("the fleet is %d routers, want 2 — a raw %s reached disk and made "+
					"routers.json undecodable. problems=%v", len(rs), c.key, problems)
			}
		})
	}
}

// AND A CORRECTLY-TYPED PATCH IS UNCHANGED, or the coercion would be rewriting
// what the other five callers deliberately send.
func TestUpdateRouterLeavesWellTypedValuesAlone(t *testing.T) {
	st := twoRouterStore(t)
	if err := st.UpdateRouter("a", map[string]any{
		"disabled": true, "port": 8728, "tls": false, "bwDownMbps": 250,
		"label": "Renamed",
	}); err != nil {
		t.Fatal(err)
	}
	rs, _ := st.Routers()
	var got *Router
	for i := range rs {
		if rs[i].ID == "a" {
			got = &rs[i]
		}
	}
	if got == nil {
		t.Fatal("router a is gone")
	}
	if !got.Disabled || got.Port != 8728 || got.TLS || got.BwDownMbps != 250 || got.Label != "Renamed" {
		t.Errorf("a well-typed patch was altered: %+v", *got)
	}
}

// A STORED BOOLEAN HELD AS A STRING MUST NOT ERASE THE FLEET.
//
// The read half of upstream `dd6173b`. Written at the FLEET level for the same
// reason as the test at the top of this file: the damage is not one wrong field,
// it is `Routers()` answering nothing while `PublicRouters()` — which is
// map-based and never decodes into `Router` — still answers two. The browser
// lists a fleet that every session, collector and the pool sees as empty.
//
// The value is `"false"` on `disabled` deliberately: that is the case where the
// repaired reading and the truthy reading DISAGREE, so a normaliser that used
// `!!` would satisfy "the fleet decodes" and still take the router out of
// service. Asserting the count alone would miss it.
func TestAStoredStringBooleanDoesNotEraseTheFleet(t *testing.T) {
	for _, c := range []struct {
		name, field, val string
		wantDisabled     bool
	}{
		{"disabledStringFalse", "disabled", `"false"`, false},
		{"disabledStringTrue", "disabled", `"true"`, true},
		{"disabledJunk", "disabled", `"1"`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("s"), 0o600); err != nil {
				t.Fatal(err)
			}
			body := `[
			  {"id":"a","label":"A","host":"198.51.100.1","port":8728,"username":"u","password":"","` +
				c.field + `":` + c.val + `},
			  {"id":"b","label":"B","host":"198.51.100.2","port":8728,"username":"u","password":""}
			]`
			if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			rs, problems := st.Routers()
			if len(rs) != 2 {
				t.Fatalf("the fleet is %d routers, want 2 — one stored %s of %s made "+
					"routers.json undecodable and EVERY router vanished. problems=%v",
					len(rs), c.field, c.val, problems)
			}
			if rs[0].Disabled != c.wantDisabled {
				t.Errorf("disabled=%s read as %v, want %v — the repair used the wrong rule. "+
					"`!!(\"false\")` is TRUE, which takes a router the operator asked to "+
					"ENABLE out of service", c.val, rs[0].Disabled, c.wantDisabled)
			}
			// REPORTED. A silent repair leaves a file that works today and breaks
			// on the next reader that does not have this code.
			if len(problems) == 0 {
				t.Error("the repair was silent; routers.json is still wrong on disk and " +
					"nothing said so")
			}
		})
	}
}

// AN HONEST FILE IS NOT TOUCHED, and an unrelated malformed one still reports
// its own error rather than a confusing second one.
func TestTheStoredBoolRepairIsNarrow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Well-formed: no repair, no problems.
	good := `[{"id":"a","label":"A","host":"198.51.100.1","port":8728,"username":"u","password":"","disabled":true}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, problems := st.Routers()
	if len(rs) != 1 || !rs[0].Disabled {
		t.Fatalf("an honest file decoded to %d routers, disabled=%v", len(rs), len(rs) > 0 && rs[0].Disabled)
	}
	if len(problems) != 0 {
		t.Errorf("an honest file reported problems: %v", problems)
	}

	// Malformed in a way the repair cannot help: the ORIGINAL error, not a
	// second one from the retry.
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(`{"not":"an array"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, problems = st.Routers()
	if len(rs) != 0 || len(problems) != 1 {
		t.Errorf("a malformed file gave %d routers and %d problems, want 0 and 1: %v",
			len(rs), len(problems), problems)
	}
}
