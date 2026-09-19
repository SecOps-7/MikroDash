package resource

import (
	"strings"
	"testing"
)

// TestEveryClearableIntegerSaysHowItClears. Measured on the CHR, 7.24.1: every
// Clearable integer field refused `=<ros>=` ("an integer required"), so an edit
// leaving one blank failed the whole write. Each now declares ClearBang (the
// property is removed by `=!<ros>=`) or its default in ClearAs; the bang form
// is a silent no-op on the menus that have a default, which is why it is not
// the answer for all of them (review loop).
func TestEveryClearableIntegerSaysHowItClears(t *testing.T) {
	n := 0
	for _, res := range All() {
		for _, f := range res.Fields {
			if f.Type != TypeInt || !f.Clearable {
				continue
			}
			n++
			if f.ClearAs == "" && !f.ClearBang {
				t.Errorf("%s.%s is a Clearable integer with no ClearAs or ClearBang: RouterOS refuses "+
					"`=%s=` for an integer, so clearing it fails the write", res.Key, f.Name, f.ROS)
			}
		}
	}
	if n < 8 {
		t.Fatalf("found %d Clearable integer fields, want at least the 8 measured", n)
	}

	// The bang form is what a blank one sends.
	v, errs := RoutingRule.Validate(map[string]string{"action": "lookup", "table": "main"}, true)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := strings.Join(RoutingRule.BuildArgs(v), " "); !strings.Contains(got, "=!min-prefix=") ||
		strings.Contains(got, "=min-prefix=") {
		t.Errorf("a blank min-prefix sent %q, want =!min-prefix=", got)
	}
}
