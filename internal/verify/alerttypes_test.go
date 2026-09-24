package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/alert"
)

// THE ALERT CATALOGUE AGAINST THE RULES THAT RAISE THE ALERTS.
//
// ── WHY A LEDGER AND NOT A ROUND TRIP ──────────────────────────────────────
//
// `alert.Types()` exists so the notification-channel modal can draw a toggle per
// event. Nothing else consumes it, so nothing else notices when it drifts: a
// rule that starts raising a new alert type simply has no toggle, and every
// channel silently fails to subscribe to it. The reverse is quieter still — an
// entry for a type no rule raises renders a toggle that can never fire.
//
// A round-trip test cannot see either, because the catalogue agrees with itself
// whatever it contains. So this reads the EVALUATOR'S SOURCE, which is where the
// types actually live as string literals, and fails in both directions.
//
// ── WHAT IT READS ──────────────────────────────────────────────────────────
//
// `AlertType: "..."` in internal/alert/eval.go and routerstatus.go. `emit` also
// re-emits `AlertType: f.AlertType` for the supersede path, which is a variable
// rather than a literal and so never matches — correct, because that path raises
// no NEW type.
//
// The two backup events are not in the evaluator at all: they are raised by
// `cfg.Notify(kind, ...)` in internal/backups/runfor.go. They are checked
// against that file instead, for the same reason and in the same two directions.
var reAlertTypeLiteral = regexp.MustCompile(`AlertType:\s*"([^"]+)"`)

func alertSourceTypes(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, rel := range []string{
		filepath.Join("internal", "alert", "eval.go"),
		filepath.Join("internal", "alert", "routerstatus.go"),
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		for _, m := range reAlertTypeLiteral.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
	}
	// AN EMPTY SCAN IS A BROKEN SCAN, not a passing ledger. If the rules are
	// ever restructured so the literals stop matching, this must fail loudly
	// rather than report that every catalogue entry is fine.
	if len(out) == 0 {
		t.Fatal("no AlertType literals found in the evaluator — this scan has broken, " +
			"and an empty result agrees with every assertion below")
	}
	return out
}

func TestEveryAlertTypeRaisedHasACatalogueEntry(t *testing.T) {
	root := repoRoot(t)
	raised := alertSourceTypes(t, root)

	// Every display string the catalogue knows, on both the down and up sides.
	known := map[string]bool{}
	for _, ty := range alert.Types() {
		if ty.Backup {
			continue
		}
		known[ty.Down] = true
		if ty.Up != "" {
			known[ty.Up] = true
		}
	}

	missing := []string{}
	for display := range raised {
		if !known[display] {
			missing = append(missing, display)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the evaluator raises %v, and alert.Types() has no entry for it — "+
			"a channel cannot subscribe to an event the modal never draws", missing)
	}
}

func TestEveryCatalogueEntryIsRaisedBySomeRule(t *testing.T) {
	root := repoRoot(t)
	raised := alertSourceTypes(t, root)

	stale := []string{}
	for _, ty := range alert.Types() {
		if ty.Backup {
			continue
		}
		if !raised[ty.Down] {
			stale = append(stale, ty.Key+" ("+ty.Down+")")
		}
		// The up side is checked too: a recovery string that no rule emits
		// means the modal promises a resolution notification that never comes.
		if ty.Up != "" && !raised[ty.Up] {
			stale = append(stale, ty.Key+" up ("+ty.Up+")")
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("alert.Types() lists %v, which no rule raises — the modal draws a toggle "+
			"that can never fire", stale)
	}
}

// THE KEY IS NOT A SECOND SPELLING. It must be exactly what `StoredType`
// derives, because that is what `alert_events.alert_type` holds, what
// `ResolveType` carries and what `cooldownKey` builds on. A catalogue key that
// merely looks right would subscribe a channel to a value nothing ever emits.
//
// THE BACKUP EVENTS ARE OUTSIDE THIS RULE, and the first run of this ledger is
// how that was established: it rejected `backup_fail` against a Down string of
// "Backup Failed". The identity being asserted here is about a RECORDED alert —
// the stored type is the row's `alert_type`. A backup notification records no
// row at all, so it has no display string to derive from; its key is `backup_`
// plus the runner's kind, pinned by TestTheBackupEventsMatchTheBackupRunner.
func TestACatalogueKeyIsTheStoredFormOfItsDownType(t *testing.T) {
	for _, ty := range alert.Types() {
		if ty.Backup {
			continue
		}
		if got := alert.StoredType(ty.Down); got != ty.Key {
			t.Errorf("catalogue key %q does not match StoredType(%q) = %q — the channel "+
				"would subscribe to a value the recorder never writes", ty.Key, ty.Down, got)
		}
	}
}

// THE TWO BACKUP EVENTS, against the file that actually raises them.
func TestTheBackupEventsMatchTheBackupRunner(t *testing.T) {
	root := repoRoot(t)
	rel := filepath.Join("internal", "backups", "runfor.go")
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}

	kinds := regexp.MustCompile(`Notify\(\s*"([^"]+)"`).FindAllStringSubmatch(string(b), -1)
	if len(kinds) == 0 {
		t.Fatal("no cfg.Notify kinds found in the backup runner — this scan has broken")
	}
	raised := map[string]bool{}
	for _, m := range kinds {
		raised[m[1]] = true
	}

	// The catalogue spells them `backup_<kind>`, because a channel's event list
	// is one namespace and a bare "fail" would collide with anything else that
	// ever fails.
	listed := map[string]bool{}
	for _, ty := range alert.Types() {
		if !ty.Backup {
			continue
		}
		kind := strings.TrimPrefix(ty.Key, "backup_")
		listed[kind] = true
		if !raised[kind] {
			t.Errorf("alert.Types() lists backup event %q, and the backup runner never "+
				"raises kind %q", ty.Key, kind)
		}
	}
	for kind := range raised {
		if !listed[kind] {
			t.Errorf("the backup runner raises kind %q and alert.Types() has no entry for "+
				"it — no channel can subscribe to it", kind)
		}
	}
}

// EVERY GATE NAMES A REAL SETTING. A catalogue entry whose gate is misspelled
// reads as "this event is switched off install-wide" for ever, because the
// settings lookup misses and falls to the default.
func TestEveryCatalogueGateNamesARealSetting(t *testing.T) {
	root := repoRoot(t)
	rel := filepath.Join("internal", "store", "settings_tables.json")
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	src := string(b)
	for _, ty := range alert.Types() {
		if !strings.Contains(src, `"`+ty.Gate+`"`) {
			t.Errorf("catalogue entry %q is gated on setting %q, which is not in %s",
				ty.Key, ty.Gate, rel)
		}
	}
}
