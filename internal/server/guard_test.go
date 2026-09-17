package server

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/guard"
	"mikrodash/internal/routeros"

	"mikrodash/internal/resource"
)

// Guards are ported just-in-time with the page that needs them, so "declared
// but not ported" is a state this server is in routinely. What must never
// happen is a write proceeding as though the resource declared no guard at all.
//
// This asserts the CURRENT set, so porting a guard fails this test and forces
// the entry to be added deliberately rather than discovered later.
func TestPortedGuardsAreDeclaredExplicitly(t *testing.T) {
	want := map[string]bool{"selfPath": true, "fwGuard": true, "wifiInherit": true,
		"capsmanPush": true, "routePath": true, "addressPath": true, "queueThrottle": true, "selfAccount": true}
	if len(portedGuards) != len(want) {
		t.Errorf("portedGuards = %v; update this test when a guard is ported", portedGuards)
	}
	for k := range want {
		if !portedGuards[k] {
			t.Errorf("%s is expected to be ported but is not listed", k)
		}
	}
}

// Every guard any registered resource declares must either be ported, or be
// known to refuse. A resource whose guard is neither would write unguarded.
func TestEveryDeclaredGuardIsPortedOrRefuses(t *testing.T) {
	// ── THE REGISTRY, NOT A TYPED LIST ────────────────────────────────────
	//
	// This enumerated SIXTEEN resources by name against a registry of TWENTY.
	// The four it never looked at — DHCPLease, Route, Route6, WgPeer — were
	// unchecked for as long as the list had been typed out, and were harmless
	// only by coincidence: those four declare no guard today. Give one an
	// unported guard and its writes are refused at runtime, correctly, with no
	// test saying why.
	//
	// `resource.All()` is the registry itself, so a resource added tomorrow is
	// checked tomorrow. Same lesson as the `endpoint-audit` incident CLAUDE.md
	// records: a checker driven by a hand-typed list drifts from what it checks.
	all := resource.All()
	if len(all) == 0 {
		t.Fatal("resource.All() is empty — this test is measuring nothing")
	}
	for _, res := range all {
		for _, kind := range res.Guard {
			if !portedGuards[kind] {
				// Not a failure — this is the expected state for a resource
				// whose page is ported ahead of its guard. It is logged so the
				// list is visible in the test output rather than inferred.
				t.Logf("%s declares %q, which is not ported: its writes are REFUSED", res.Key, kind)
			}
		}
	}
	// The ones that should work right now.
	for _, res := range []*resource.Resource{resource.Bridge, resource.BridgePort, resource.Vlan} {
		for _, kind := range res.Guard {
			if !portedGuards[kind] {
				t.Errorf("%s declares %q — this page's write path is live and needs it", res.Key, kind)
			}
		}
	}
}

// TestQueueThrottleVerdict drives the queueThrottle guard through every action
// the resource write path hands it, in registry field names. The self-throttle
// arithmetic itself is guard/queueguard_test.go's; this pins which values each
// action checks.
func TestQueueThrottleVerdict(t *testing.T) {
	self := []string{"10.0.0.5"}
	tight := map[string]string{"target": "10.0.0.0/24", "maxLimit": "512k/512k", "disabled": "false"}
	loose := map[string]string{"target": "10.0.0.0/24", "maxLimit": "50M/50M", "disabled": "false"}
	elsewhere := map[string]string{"target": "192.168.9.0/24", "maxLimit": "512k/512k"}
	for _, tc := range []struct {
		name           string
		action         string
		values, before map[string]string
		self           []string
		warn           bool
	}{
		{"a tight queue over us warns on create", "create", tight, nil, self, true},
		{"a loose queue over us does not", "create", loose, nil, self, false},
		{"a tight queue elsewhere does not", "create", elsewhere, nil, self, false},
		{"no known address fails open", "create", tight, nil, nil, false},
		{"a disabled tight queue is not in force", "create",
			map[string]string{"target": "10.0.0.0/24", "maxLimit": "512k/512k", "disabled": "true"}, nil, self, false},
		{"tightening an edit warns", "update", map[string]string{"maxLimit": "512k/512k"}, loose, self, true},
		{"a comment-only edit of a tight queue does not", "update", map[string]string{"comment": "x"}, tight, self, false},
		{"enabling a tight queue warns", "enable",
			map[string]string{"target": "10.0.0.0/24", "maxLimit": "512k/512k", "disabled": "true"}, nil, self, true},
		{"disabling never warns", "disable", tight, tight, self, false},
		{"deleting never warns", "delete", nil, tight, self, false},
	} {
		v := queueThrottleVerdict(tc.self, tc.action, tc.values, tc.before)
		if v.Warned() != tc.warn {
			t.Errorf("%s: warned=%v, want %v (%+v)", tc.name, v.Warned(), tc.warn, v)
		}
		if tc.warn && (v.Code != "self-throttle" || v.Fingerprint == "") {
			t.Errorf("%s: a warning without its code or fingerprint: %+v", tc.name, v)
		}
	}
}

// TestSelfAccountDecision drives the selfAccount guard through the writes the
// resource engine hands it. The lockout rules themselves are
// guard/selfguard_test.go's; this pins which values each action judges, and that
// every refusal is a REFUSAL, never a warning an acknowledgement could pass.
func TestSelfAccountDecision(t *testing.T) {
	self := guard.Self{Names: []string{"mikrodash"}, Groups: []string{"full"}, Resolved: true, Source: "active"}
	ours := routeros.Reply{"name": "mikrodash", "group": "full"}
	other := routeros.Reply{"name": "alice", "group": "read"}
	for _, tc := range []struct {
		name   string
		self   guard.Self
		menu   string
		action string
		values map[string]string
		before routeros.Reply
		rule   string // "" means allowed
	}{
		{"editing another user is allowed", self, "/user", "update", map[string]string{"name": "alice", "group": "read", "comment": "x"}, other, ""},
		{"creating another user is allowed", self, "/user", "create", map[string]string{"name": "bob", "group": "read"}, nil, ""},
		{"deleting another user is allowed", self, "/user", "delete", nil, other, ""},
		{"editing our account is refused", self, "/user", "update", map[string]string{"name": "mikrodash", "group": "full"}, ours, "protected-account"},
		{"disabling our account is refused", self, "/user", "disable", map[string]string{"name": "mikrodash", "group": "full"}, ours, "protected-account"},
		{"deleting our account is refused", self, "/user", "delete", nil, ours, "protected-account"},
		{"creating a user with our name is refused", self, "/user", "create", map[string]string{"name": "MikroDash", "group": "read"}, nil, "protected-name-value"},
		{"moving a user into our group is refused", self, "/user", "update", map[string]string{"name": "alice", "group": "full"}, other, "protected-group-value"},
		{"an edit that omits group still judges the stored group", self, "/user", "update", map[string]string{"comment": "x"},
			routeros.Reply{"name": "carol", "group": "full"}, "protected-group-value"},
		{"editing our group is refused", self, "/user/group", "update", map[string]string{"name": "full"}, routeros.Reply{"name": "full"}, "protected-group"},
		{"deleting our group is refused", self, "/user/group", "delete", nil, routeros.Reply{"name": "full"}, "protected-group"},
		{"renaming a group onto ours is refused", self, "/user/group", "update", map[string]string{"name": "full"}, routeros.Reply{"name": "ops"}, "protected-group-value"},
		{"deleting another group is allowed", self, "/user/group", "delete", nil, routeros.Reply{"name": "ops"}, ""},
		{"an unidentified MikroDash refuses everything", guard.Self{}, "/user", "create", map[string]string{"name": "bob", "group": "read"}, nil, "self-unresolved"},
		{"a menu the guard does not know refuses", self, "/ip/address", "create", map[string]string{}, nil, "self-unresolved"},
	} {
		v := selfAccountDecision(tc.self, tc.menu, tc.action, tc.values, tc.before)
		if tc.rule == "" {
			if v.Refused() || v.Warned() {
				t.Errorf("%s: got %+v, want allowed", tc.name, v)
			}
			continue
		}
		if !v.Refused() || v.Code != tc.rule {
			t.Errorf("%s: got %+v, want a refusal by %s", tc.name, v, tc.rule)
		}
		if v.Fingerprint != "" || ackGate(v, "anything") != nil {
			t.Errorf("%s: a refusal carries a fingerprint or reaches the acknowledgement gate: %+v", tc.name, v)
		}
	}
}

// TestEveryGuardedWritePathHonoursARefusal. ackGate passes anything that is not
// a warning, so a refusal is only enforced where a write path calls
// guardRefusal after verdictFor. Every function that asks for a verdict must do
// both; a new write path that forgets would let a refused write through.
func TestEveryGuardedWritePathHonoursARefusal(t *testing.T) {
	funcRe := regexp.MustCompile(`(?m)^func `)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		starts := funcRe.FindAllStringIndex(src, -1)
		for i, st := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			body := src[st[0]:end]
			if !strings.Contains(body, "cn.verdictFor(") {
				continue
			}
			callers++
			if !strings.Contains(body, "cn.guardRefusal(") {
				header := body
				if nl := strings.IndexByte(header, '\n'); nl > 0 {
					header = header[:nl]
				}
				t.Errorf("%s: %s asks verdictFor but never checks guardRefusal", name, header)
			}
		}
	}
	if callers < 5 {
		t.Fatalf("found %d functions calling verdictFor, expected at least 5: this test is measuring nothing", callers)
	}
}
