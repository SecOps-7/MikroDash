package resource

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The registry must hold EVERY declared resource.
//
// ── AN UNREGISTERED RESOURCE IS INVISIBLE, NOT BROKEN ──────────────────────
//
// `byKey` is hand-maintained: adding `var Foo = &Resource{...}` does not put it
// in the map. A resource missing from it returns nil from `ByKey`, so its page
// cannot read, write or preview anything — and nothing anywhere says so, because
// every consumer starts by asking `ByKey` and getting nothing back.
//
// It is also the map `All()` enumerates, so a resource missing here is missing
// from every checker built on `All()` as well — including the guard test. One
// omission would silently shrink two safety nets at once.
func TestEveryDeclaredResourceIsRegistered(t *testing.T) {
	b, err := os.ReadFile("resource.go")
	if err != nil {
		t.Fatalf("reading resource.go: %v", err)
	}
	declared := regexp.MustCompile(`(?m)^var ([A-Z][A-Za-z0-9]*) = &Resource\{`).
		FindAllStringSubmatch(string(b), -1)
	if len(declared) == 0 {
		t.Fatal("no resource declarations found — this test is measuring nothing")
	}

	registered := map[string]bool{}
	for _, r := range All() {
		registered[r.Key] = true
	}

	// The map is keyed by `Key`, not by the Go identifier, so the check is that
	// each declared var's OWN Key is present. Reaching the var by name needs
	// reflection Go does not offer for package-level values, so this asserts the
	// COUNT agrees and then that every registered key is non-empty and unique —
	// which together catch the omission that matters.
	if len(declared) != len(registered) {
		var names []string
		for _, d := range declared {
			names = append(names, d[1])
		}
		sort.Strings(names)
		t.Errorf("%d resources are declared but %d are registered in byKey.\n"+
			"declared: %v\nA resource missing from byKey returns nil from ByKey, so its "+
			"page silently cannot read or write anything.", len(declared), len(registered), names)
	}

	seen := map[string]bool{}
	for _, r := range All() {
		if strings.TrimSpace(r.Key) == "" {
			t.Error("a registered resource has an empty Key")
		}
		if seen[r.Key] {
			t.Errorf("two resources share the key %q — one shadows the other in byKey", r.Key)
		}
		seen[r.Key] = true
	}
}

// All() is ordered, so anything that prints or diffs it is stable.
func TestAllIsOrderedByKey(t *testing.T) {
	got := All()
	for i := 1; i < len(got); i++ {
		if got[i-1].Key > got[i].Key {
			t.Fatalf("All() is not sorted: %q before %q", got[i-1].Key, got[i].Key)
		}
	}
}
