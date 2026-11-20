package resource

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/pages"
)

// ── EVERY RESOURCE'S `Page` MUST BE A LIVE PAGE KEY ────────────────────────
//
// `Page` is not a label. It is the RBAC key, and both gates that read it refuse
// an unknown page BEFORE they consult any role:
//
//	Session.CanPage   `s.Pages[page]` is absent  → deny
//	rbac.CanPage      `r.pages[page]` is false   → deny, before the builtin
//	                  short circuit, so not even an administrator gets through
//
// So a `Page` naming a renamed key does not degrade: it denies everyone, and
// nothing logs or errors.
//
// IT HAD ALREADY HAPPENED. `wifiNet`, `wlNet` and `wlSecProfile` declared
// `Page: "wifi"` after the 2026-09-01 rename moved that key to `wifi-networks`.
// Because `resSchema` is gated on READ, the WiFi Networks page did not merely
// refuse writes: it never received a schema, so no Add button was drawn and
// clicking a row did nothing. Nothing logged, nothing errored.
//
// CLAUDE.md's page-key table calls the permission meaning "the dangerous one"
// and records `rbac.PageKeys` being fixed to read `internal/pages` so it could
// not drift again. This is the same medicine one layer out: the registry is now
// compared against the same source, so a rename that misses a resource fails
// here instead of in the field.
func TestEveryResourcePageIsALivePageKey(t *testing.T) {
	live := map[string]bool{}
	for _, k := range pages.Keys() {
		live[k] = true
	}
	if len(live) < 20 {
		t.Fatalf("only %d page keys read from internal/pages — the source moved "+
			"and this check would pass anything", len(live))
	}

	for _, r := range All() {
		if live[r.Page] {
			continue
		}
		msg := ""
		if to, renamed := pages.Renamed[r.Page]; renamed {
			msg = " — it was renamed to " + to + ", so use that"
		}
		t.Errorf("resource %q declares Page %q, which is not a live page key%s.\n"+
			"Both permission gates deny an unknown page before consulting any "+
			"role, so this resource is unreachable for every principal.", r.Key, r.Page, msg)
	}
}

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
