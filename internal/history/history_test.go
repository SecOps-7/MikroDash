package history

import "testing"

// The inverse of every recorded write. This is the whole point of the package:
// if a pair is wrong, undo does the wrong thing to a live router.
func TestEveryRecordedWriteHasTheRightInverse(t *testing.T) {
	before := map[string]string{"address": "198.51.100.1"}
	after := map[string]string{"address": "198.51.100.2"}

	for _, tc := range []struct {
		what                 string
		fwdOp, revOp         string
		fwdValues, revValues map[string]string
		fwdID, revID         string
	}{
		{what: "create", fwdOp: "add", fwdValues: after, revOp: "remove", revID: "*1"},
		{what: "delete", fwdOp: "remove", fwdID: "*1", revOp: "add", revValues: before},
		{what: "update", fwdOp: "set", fwdID: "*1", fwdValues: after,
			revOp: "set", revID: "*1", revValues: before},
	} {
		t.Run(tc.what, func(t *testing.T) {
			e := Build("dnsStatic", "DNS Entry", tc.what, "*1", "host.lan", before, after)
			if e == nil {
				t.Fatal("nothing recorded")
			}
			if e.Forward.Op != tc.fwdOp || e.Reverse.Op != tc.revOp {
				t.Errorf("ops = %s/%s, want %s/%s",
					e.Forward.Op, e.Reverse.Op, tc.fwdOp, tc.revOp)
			}
			if e.Forward.ID != tc.fwdID || e.Reverse.ID != tc.revID {
				t.Errorf("ids = %q/%q, want %q/%q",
					e.Forward.ID, e.Reverse.ID, tc.fwdID, tc.revID)
			}
			sameValues(t, "forward", e.Forward.Values, tc.fwdValues)
			sameValues(t, "reverse", e.Reverse.Values, tc.revValues)
		})
	}
}

func sameValues(t *testing.T, side string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s values = %v, want %v", side, got, want)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s values[%s] = %q, want %q", side, k, got[k], v)
		}
	}
}

// An action with no inverse is NOT recorded. Silence is the correct failure: an
// entry that cannot be reversed but sits on the stack offers a button that does
// something else.
//
// `move`, `enable` and `disable` are in this list on purpose. The live module
// records all three; this port has no path that reaches them, so recording them
// would put an entry on the stack that applyOp cannot perform.
func TestAnActionWithNoInverseIsNotRecorded(t *testing.T) {
	for _, what := range []string{"move", "enable", "disable", "", "reboot"} {
		if e := Build("route", "Route", what, "*1", "0.0.0.0/0", nil, nil); e != nil {
			t.Errorf("%q was recorded as %+v — it has no inverse here", what, e)
		}
	}
}

// A delete's reverse is an `add`, and an add gets a NEW id from RouterOS. Both
// halves have to be re-pointed at it, or the matching remove addresses whatever
// now holds the old one.
func TestRebindPointsBothHalvesAtTheRowThatNowExists(t *testing.T) {
	e := Build("dnsStatic", "DNS Entry", "delete", "*OLD", "host.lan",
		map[string]string{"address": "198.51.100.1"}, nil)
	Rebind(e, "*NEW")

	if e.Forward.Op != "remove" || e.Forward.ID != "*NEW" {
		t.Errorf("forward = %+v, want a remove of *NEW", e.Forward)
	}
	// The `add` keeps no id: it does not address a row, it makes one.
	if e.Reverse.ID != "" {
		t.Errorf("the add half was given an id (%q); it addresses nothing", e.Reverse.ID)
	}
	Rebind(nil, "*NEW") // must not panic
}

func TestLabel(t *testing.T) {
	for _, tc := range []struct{ what, identity, want string }{
		{"create", "host.lan", "add of host.lan"},
		{"update", "host.lan", "edit of host.lan"},
		{"delete", "host.lan", "delete of host.lan"},
		// A composite identity is joined for a human rather than shown raw.
		{"delete", "forward" + Sep + "5", "delete of forward 5"},
		// No identity: the resource names itself, lower-cased mid-sentence.
		{"create", "", "add of dns entry"},
	} {
		if got := Label("DNS Entry", tc.what, tc.identity); got != tc.want {
			t.Errorf("Label(%q, %q) = %q, want %q", tc.what, tc.identity, got, tc.want)
		}
	}
}

// A secret cannot reach a stack entry, because RowValues never returns one and
// `before` is built from it. Undoing an edit that changed a passphrase leaves
// the current one alone rather than restoring a value this process never had.
//
// Asserted on the ENTRY rather than on RowValues, because the entry is what
// survives in memory for the life of the connection.
func TestNoSecretReachesAnEntry(t *testing.T) {
	// What RowValues yields for a resource with a secret field: the secret is
	// simply absent.
	before := map[string]string{"name": "guest", "disabled": "false"}
	e := Build("wlSecProfile", "Security Profile", "update", "*1", "guest",
		before, map[string]string{"name": "guest", "wpa2PreSharedKey": ""})

	for _, side := range []map[string]string{e.Forward.Values, e.Reverse.Values} {
		if v, ok := side["wpa2PreSharedKey"]; ok && v != "" {
			t.Errorf("a secret value reached the stack: %q", v)
		}
	}
}
