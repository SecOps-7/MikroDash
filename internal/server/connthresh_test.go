package server

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/connstate"
	"mikrodash/internal/store"
)

// ── THE OUTAGE DEBOUNCE REACHES THE DEBOUNCER, AND FOLLOWS A SAVE ──────────
//
// Passing a hardcoded zero is not a small error: zero is its own branch meaning
// "record every close at once", and it turned a routine six-second reconnect
// into an outage in the Reports page.
//
// ── RE-AIMED TWICE, AND THE SECOND TIME IS THE INTERESTING ONE ─────────────
//
// This began in `internal/server/recorded_ifaces_test.go`, driving the deleted
// `alertpool`'s status hook. It then moved to `internal/session`, where the
// session resolved the threshold when it was BUILT and held it — and that is
// exactly the defect reported on 2026-09-20: a router held for alerting or
// recording is never rebuilt, so an operator who changed the Offline threshold
// saw the field save and the record update while the running debounce went on
// using the old value until the process restarted.
//
// So the session no longer carries a threshold at all. It is declared per
// router on the tracker, from the sync that already runs on every router save,
// and these are the two halves of that: the resolution, and the wiring.

func ptr(n int) *int { return &n }

// TestUnsetIsNotZero is the distinction the whole debounce rests on.
func TestUnsetIsNotZero(t *testing.T) {
	if _, ok := connDownSecOf(store.Router{}); ok {
		t.Error("a router with no setting reports one; the live 30s default is lost")
	}
	sec, ok := connDownSecOf(store.Router{ConnDownThresholdSec: ptr(0)})
	if !ok || sec != 0 {
		t.Errorf("a deliberate zero read as (%d, %v), want (0, true)", sec, ok)
	}
	if got := connstate.ThresholdMs(connDownSecOf(store.Router{})); got != 30_000 {
		t.Errorf("an unset debounce resolved to %dms, want the live 30s default", got)
	}
	if got := connstate.ThresholdMs(
		connDownSecOf(store.Router{ConnDownThresholdSec: ptr(0)})); got != 0 {
		t.Errorf("a router asking for zero resolved to %dms; zero is a deliberate "+
			"setting, not an absence", got)
	}
}

// TestTheThresholdIsDeclaredFromTheFleetSync pins the WIRING, which is the half
// a pure test of `connDownSecOf` cannot see.
//
// SOURCE-READ, because reaching it live needs a store, a session manager and a
// router. The mutations this kills are the two that were actually made: a
// literal in the argument, and a declaration that no save path reaches.
func TestTheThresholdIsDeclaredFromTheFleetSync(t *testing.T) {
	devices, err := os.ReadFile("devices.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(
		`s\.connTrack\.SetThreshold\(r\.ID, connstate\.ThresholdMs\(connDownSecOf\(r\)\)\)`).
		Match(devices) {
		t.Error("declareConnThreshold no longer resolves the threshold from the " +
			"router's own record; every outage would be debounced against one value")
	}

	holds, err := os.ReadFile("fleet_holds.go")
	if err != nil {
		t.Fatal(err)
	}
	// BESIDE `declareReporting`, in the loop that runs on every router save.
	// A declaration nothing calls is the restart-required bug with extra steps.
	if !strings.Contains(string(holds), "s.declareConnThreshold(r)") {
		t.Error("syncFleetHolds does not declare the threshold, so a saved change " +
			"reaches the running debounce only when the process restarts")
	}
	if !strings.Contains(string(holds), "s.declareReporting(r)") {
		t.Fatal("the anchor this test reads by is gone: syncFleetHolds no longer " +
			"declares reporting either. Re-aim rather than delete.")
	}
}

// TestTheSessionCarriesNoThresholdOfItsOwn is the other direction, and the one
// that actually failed in the field.
//
// A session that holds a threshold holds a COPY, and a copy taken when the
// session was built is a copy nothing updates. The tracker owns the value; the
// session's job is to say when the link opened and closed, and nothing else.
func TestTheSessionCarriesNoThresholdOfItsOwn(t *testing.T) {
	src, err := os.ReadFile("../session/session.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if strings.Contains(body, "connThreshMs") {
		t.Error("the session carries a threshold again. It cannot be kept current: " +
			"a router held for alerting or recording is never rebuilt, so the saved " +
			"value would apply only after a restart. Declare it on the tracker.")
	}
	calls := regexp.MustCompile(`s\.conn\.(Connected|Disconnected)\(([^)]*)\)`).
		FindAllStringSubmatch(body, -1)
	if len(calls) < 3 {
		t.Fatalf("found %d connectivity call(s); the session had three (one connect, "+
			"two teardown paths). A lost one is an outage nobody records.", len(calls))
	}
	for _, c := range calls {
		if n := strings.Count(c[2], ",") + 1; n != 2 {
			t.Errorf("s.conn.%s(%s) passes %d arguments, want the router id and the "+
				"time — a third is a threshold, which is the staleness this removed",
				c[1], c[2], n)
		}
	}
}
