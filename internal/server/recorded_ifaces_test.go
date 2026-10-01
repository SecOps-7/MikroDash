package server

import (
	"os"
	"strings"
	"testing"
)

// ── WHICH INTERFACES A ROUTER RECORDS REACHES BOTH HALVES (#59) ────────────
//
// The recorder decides what is WRITTEN; the traffic collector decides what is
// MEASURED. Declaring to the recorder alone is the failure this exists for: an
// interface the stream does not carry produces no samples to write, so it would
// record nothing while the operator believes it is recording, and the symptom
// is an empty chart rather than an error.
//
// SOURCE-READ, in the house style of `connthresh_test.go`: reaching this live
// needs a store, a session manager and a router, and the mutations worth
// killing are all in the wiring — a declaration no save path reaches, a raw
// list where the resolved one belongs, or one half of the pair dropped.
func TestTheRecordedInterfacesReachTheRecorderAndTheStream(t *testing.T) {
	devices := read(t, "devices.go")

	if !strings.Contains(devices, "s.historyWire.SetRecordedInterfaces(routerID, recorded)") {
		t.Error("declareRecordedInterfaces no longer tells the recorder what to write")
	}
	if !strings.Contains(devices, "s.sessions.ApplyRecordedIfaces(routerID, recorded)") {
		t.Error("declareRecordedInterfaces no longer tells the live session what to " +
			"stream, so a ticked interface would record nothing and say nothing")
	}

	// THE RESOLVED LIST, NEVER THE RAW FIELD. `store.RecordedIfacesFor` puts the
	// default interface in and can never answer empty; an empty list tells the
	// recorder to record EVERY interface in the stream.
	// ONE FILE NOW. `devices.go` resolved its own copy for the overview pool's
	// `syncPool`, deleted on 2026-10-01; `syncFleetHolds` is the one place the
	// recorded set is declared from, for every router.
	for _, f := range []string{"fleet_holds.go"} {
		src := read(t, f)
		if !strings.Contains(src, "store.RecordedIfacesFor(r,") {
			t.Errorf("%s declares a recorded set that is not store.RecordedIfacesFor's; "+
				"a raw list drops the default interface, and an empty one records everything", f)
		}
	}

	// The anchor these read by, so a rename re-aims this test rather than
	// silently passing over a file that no longer declares anything.
	if !strings.Contains(read(t, "fleet_holds.go"), "s.declareReporting(r)") {
		t.Fatal("the anchor is gone: syncFleetHolds no longer declares reporting. " +
			"Re-aim rather than delete.")
	}
}

// `TestThePoolAppliesTheRecordedSetOnEverySync` lived here and was DELETED with
// the overview pool on 2026-10-01: it read `internal/routers/pool.go` for the
// pool pushing the recorded set onto its own sessions. The router's session gets
// it from `declareRecordedInterfaces` -> `ApplyRecordedIfaces`, asserted above.

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
