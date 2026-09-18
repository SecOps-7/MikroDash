package resource

import (
	"strings"
	"testing"
)

// A PRESENCE FLAG (TypeFlag), measured on /routing/table's `fib` on the CHR,
// RouterOS 7.24: a table with fib reads `fib=""`, one without has no `fib` key
// at all; `set fib=no` is accepted and CHANGES NOTHING; `set !fib=` clears it.
// So reading is presence, and switching it off on an edit is the `!` word — a
// `no` would be a save that reports success and leaves the flag on.
func TestAFlagIsReadAsPresenceAndClearedWithABang(t *testing.T) {
	f := fieldOf(t, RoutingTable, "fib")
	if f.Type != TypeFlag || f.input() != "checkbox" {
		t.Fatalf("fib is %s rendered as %s, want a flag rendered as a checkbox", f.Type, f.input())
	}

	if got := RoutingTable.RowValues(map[string]string{"name": "t", "fib": ""})["fib"]; got != true {
		t.Errorf("a row carrying fib=\"\" reads fib %v, want true", got)
	}
	// THE CONTROL, and the half a presence read can get wrong: a row WITHOUT the
	// key is false, not absent from the values — an absent value would let an
	// edit form fall back to its default and a diff miss the change.
	if got, ok := RoutingTable.RowValues(map[string]string{"name": "t"})["fib"]; !ok || got != false {
		t.Errorf("a row without fib reads fib %v (present %v), want false", got, ok)
	}

	args := func(fib string, editing bool) string {
		v, errs := RoutingTable.Validate(map[string]string{"name": "t", "fib": fib}, editing)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return strings.Join(RoutingTable.BuildArgs(v), " ")
	}
	if got := args("true", false); !strings.Contains(got, "=fib=yes") {
		t.Errorf("create with fib on sent %q", got)
	}
	if got := args("true", true); !strings.Contains(got, "=fib=yes") {
		t.Errorf("edit with fib on sent %q", got)
	}
	if got := args("false", true); !strings.Contains(got, "=!fib=") || strings.Contains(got, "=fib=no") {
		t.Errorf("edit with fib off sent %q, want =!fib=", got)
	}
	// A create keeps RouterOS's default, which is off: nothing to clear.
	if got := args("false", false); strings.Contains(got, "fib") {
		t.Errorf("create with fib off sent %q, want no fib word", got)
	}
}

func fieldOf(t *testing.T, r *Resource, name string) Field {
	t.Helper()
	for _, f := range r.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("%s has no field %q", r.Key, name)
	return Field{}
}
