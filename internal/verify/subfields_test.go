package verify

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSubscriptionsDeclareTheirFields refuses a `scheduled` literal that does
// not say which fields it wants.
//
// ── THE DEFECT THIS EXISTS FOR ──────────────────────────────────────────────
//
// `roscache.Subscribe` reads an empty field list as "every field", and
// all-fields STICKS: once one subscriber asks for it, the menu's proplist is
// dropped for the rest of the session and no narrower caller can shrink it back.
// So a subscription that omits `fields` does not merely fail to narrow a read -
// it WIDENS it, permanently, for every collector sharing that menu.
//
// Eighteen collectors were migrated onto the scheduler in 3.2 and every one of
// them omitted it. `/ip/firewall/connection` - the heaviest read in the app, and
// the one phase 1 spent its effort on - was being asked for with a seven-field
// proplist and served with all of them, on a router with thousands of rows.
//
// NOTHING FAILED. The payloads were right, every test was green, and the only
// way to see it was to print `Demand()`. That is the shape this repository keeps
// losing coverage to, so the answer is a check rather than a note.
//
// ── WHY IT DEMANDS THE WORD RATHER THAN A VALUE ─────────────────────────────
//
// A menu with no proplist of its own - `/tool/netwatch`, `/ip/neighbor`,
// `/ip/dns` - genuinely wants every field, and `fields: nil` is the right
// answer there. Since the correct value is sometimes nil, this cannot check the
// value; it checks that the author WROTE THE FIELD DOWN, which is the decision
// that was actually skipped. `fieldsOf(cmd)` makes the common case one call.
func TestSubscriptionsDeclareTheirFields(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "collect")

	// Brace-MATCHED, not regular. Half these literals contain a function
	// literal -- `cadence: func() time.Duration { ... }` -- so a `[^{}]*` body
	// stops at the inner brace and skips the declaration entirely. The first
	// version of this check did exactly that and reported 12 of 19, which is the
	// failure mode a checker is least likely to be caught in: it passed.
	found := 0
	for _, name := range collectGoFiles(t, dir) {
		if isTestSource(name) {
			continue
		}
		src := mustRead(t, filepath.Join(dir, name))
		flat := strings.Join(strings.Fields(src), " ")
		for i := 0; ; {
			j := strings.Index(flat[i:], "scheduled{")
			if j < 0 {
				break
			}
			start := i + j
			depth, end := 0, -1
			for k := start + len("scheduled"); k < len(flat); k++ {
				switch flat[k] {
				case '{':
					depth++
				case '}':
					if depth--; depth == 0 {
						end = k + 1
					}
				}
				if end >= 0 {
					break
				}
			}
			if end < 0 {
				t.Fatalf("%s: unbalanced scheduled literal at offset %d", name, start)
			}
			m := flat[start:end]
			i = end
			// `scheduled{}` zero values and the type declaration itself.
			if !strings.Contains(m, "menu:") {
				continue
			}
			found++
			if !strings.Contains(m, "fields:") {
				t.Errorf("%s subscribes without declaring fields:\n\t%s\n"+
					"An omitted field list means EVERY field, and all-fields sticks for the "+
					"session - so this widens the menu's read for every collector sharing it. "+
					"Use fieldsOf(<the command>), or write `fields: nil` with the reason.",
					name, m)
			}
		}
	}

	// A floor, not just "more than nothing". Every file embedding the helper
	// builds exactly one subscription, so the two counts must agree -- which is
	// what would have caught the regex above.
	want := 0
	for _, name := range collectGoFiles(t, dir) {
		if isTestSource(name) {
			continue
		}
		src := strings.Join(strings.Fields(mustRead(t, filepath.Join(dir, name))), " ")
		if !strings.Contains(src, "sched scheduled") {
			continue
		}
		// A COLLECTOR THAT SUBSCRIBES TO NO MENU HAS NO FIELDS TO DECLARE. The
		// areas collector takes the helper for the cache and the lifecycle, and
		// chooses its menus per tick from the declarations - `scheduled{loop:
		// ...}` with no cmd. Counting it here would make this floor unreachable
		// and the check would fail for ever on a file that has nothing to say.
		if strings.Contains(src, "a.sched = scheduled{loop: a.poll}") {
			continue
		}
		want++
	}
	if found < want {
		t.Fatalf("%d collectors embed the helper but only %d subscriptions were read. This "+
			"check has stopped seeing some of them, which is how it would pass while the "+
			"defect it exists for is present.", want, found)
	}
	t.Logf("%d subscriptions, all declaring their fields", found)
}
