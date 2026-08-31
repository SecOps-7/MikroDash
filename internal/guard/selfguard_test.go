package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/routeros"
)

// The lockout guard, differentially against the live implementation.
//
// WHY THIS GUARD GETS A GENERATOR AND selfpath.go DOES NOT. selfpath warns and
// fails open: its worst case is a warning nobody saw. This one REFUSES and fails
// CLOSED, and what it refuses is the only write in the app that cannot be undone
// from inside the app — break the login and the fix is WinBox, in person.
//
// tools/selfguard-cases.js runs the live `selfGuard` over synthetic routers and
// records every decision; this replays the same inputs through the port. The
// scenarios are invented, the answers are not, and neither implementation is
// asked about itself.

type selfCaseFile struct {
	Cases []struct {
		Name       string              `json:"name"`
		UserRows   []map[string]string `json:"userRows"`
		ActiveRows []map[string]string `json:"activeRows"`
		Usernames  []string            `json:"usernames"`
		Self       struct {
			Names    []string `json:"names"`
			Groups   []string `json:"groups"`
			Resolved bool     `json:"resolved"`
			Source   string   `json:"source"`
		} `json:"self"`
		Checks []struct {
			Kind      string            `json:"kind"`
			Verb      string            `json:"verb"`
			Target    map[string]string `json:"target"`
			Values    map[string]string `json:"values"`
			ValueKeys []string          `json:"valueKeys"`
			Want      struct {
				OK     bool   `json:"ok"`
				Code   string `json:"code"`
				Detail string `json:"detail"`
			} `json:"want"`
		} `json:"checks"`
	} `json:"cases"`
}

func rowsOf(ms []map[string]string) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(ms))
	for _, m := range ms {
		out = append(out, routeros.Reply(m))
	}
	return out
}

func TestSelfGuardMatchesTheLiveImplementation(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "selfguard-cases.json"))
	if err != nil {
		t.Fatalf("cannot read the pinned cases (%v) — run: node tools/selfguard-cases.js", err)
	}
	var f selfCaseFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the case file is empty — this gate would pass on anything")
	}

	decisions := 0
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			self := ResolveSelf(rowsOf(c.UserRows), rowsOf(c.ActiveRows), c.Usernames)

			// The resolution itself, before any decision rests on it. A guard
			// that refused everything for the WRONG REASON would still look
			// right on the checks below.
			if self.Resolved != c.Self.Resolved {
				t.Errorf("resolved = %v, want %v", self.Resolved, c.Self.Resolved)
			}
			if self.Source != c.Self.Source {
				t.Errorf("source = %q, want %q", self.Source, c.Self.Source)
			}
			sameStrings(t, "names", self.Names, c.Self.Names)
			sameStrings(t, "groups", self.Groups, c.Self.Groups)

			for i, ch := range c.Checks {
				var target routeros.Reply
				if ch.Target != nil {
					target = routeros.Reply(ch.Target)
				}
				set := map[string]bool{}
				for _, k := range ch.ValueKeys {
					set[k] = true
				}
				action := UserAction{Verb: ch.Verb, Target: target, Values: ch.Values, ValueSet: set}

				var got Refusal
				switch ch.Kind {
				case "user":
					got = CheckUser(self, action)
				case "group":
					got = CheckGroup(self, action)
				case "session":
					got = CheckSession(self, target)
				default:
					t.Fatalf("check %d: unknown kind %q", i, ch.Kind)
				}
				decisions++

				if got.OK != ch.Want.OK || got.Code != ch.Want.Code || got.Detail != ch.Want.Detail {
					t.Errorf("check %d (%s %s target=%v values=%v):\n  got  ok=%v code=%q detail=%q\n  want ok=%v code=%q detail=%q",
						i, ch.Kind, ch.Verb, ch.Target, ch.Values,
						got.OK, got.Code, got.Detail,
						ch.Want.OK, ch.Want.Code, ch.Want.Detail)
				}
			}
		})
	}
	t.Logf("%d decisions compared against the live implementation", decisions)
}

func sameStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
	}
}

// TestUnresolvedRefusesEverything states the fail-closed property directly,
// rather than leaving it implied by whichever scenarios the generator happens to
// carry.
//
// The generated cases would catch a regression here today. They would stop
// catching it the moment somebody trimmed the case list, and this is the one
// property that must not quietly lapse: allowing everything because we cannot
// tell what is ours is exactly the accident the module exists to prevent.
func TestUnresolvedRefusesEverything(t *testing.T) {
	var none Self // zero value: Resolved false

	for _, tc := range []struct {
		name string
		got  Refusal
	}{
		{"user set", CheckUser(none, UserAction{Verb: "set", Target: routeros.Reply{"name": "alice"}})},
		{"user add", CheckUser(none, UserAction{Verb: "add", Values: map[string]string{"name": "bob"}})},
		{"user remove", CheckUser(none, UserAction{Verb: "remove", Target: routeros.Reply{"name": "alice"}})},
		{"group set", CheckGroup(none, UserAction{Verb: "set", Target: routeros.Reply{"name": "read"}})},
		{"group remove", CheckGroup(none, UserAction{Verb: "remove", Target: routeros.Reply{"name": "read"}})},
		{"session", CheckSession(none, routeros.Reply{"name": "alice"})},
	} {
		if tc.got.OK {
			t.Errorf("%s was ALLOWED with an unresolved self — this guard fails closed", tc.name)
		}
		if tc.got.Code != "self-unresolved" {
			t.Errorf("%s refused with %q, want self-unresolved", tc.name, tc.got.Code)
		}
	}
}
