package historywire

import "testing"

// The outage debounce, and the clock that makes it work.
//
// ── A SIX-SECOND RECONNECT WAS BEING FILED AS AN OUTAGE ────────────────────
//
// `history.Connectivity` holds no timer: the caller supplies time through
// `Tick`, which is what makes its rules testable without one. Nothing called
// it, so a non-zero threshold could never fire and the only workable setting
// was zero — its own branch, meaning "record every close immediately".
//
// The Reports page then showed the active router flapping online and offline,
// because a routine reconnect takes about five seconds and every one of them
// was written down. The live app debounces with `connDownThresholdSec`,
// default 30s, and a blip shorter than that never reaches the database.

const minute = int64(60_000)

func TestABlipShorterThanTheThresholdIsNotRecorded(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 30_000, min1)
	up := len(s.rows)

	// The connection drops and is back six seconds later — the shape the
	// operator was seeing in the report.
	w.Disconnected("r-1", 30_000, min1+1000)
	w.TickAll(min1 + 3000)
	w.Connected("r-1", 30_000, min1+7000)
	w.TickAll(min1 + 40_000) // long past the threshold, but it was cancelled

	if len(s.rows) != up {
		t.Errorf("a six-second reconnect wrote %d row(s); the report shows the "+
			"router flapping for something nobody would call an outage",
			len(s.rows)-up)
	}
}

func TestAnOutageLongerThanTheThresholdIsRecorded(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 30_000, min1)
	before := len(s.rows)

	w.Disconnected("r-1", 30_000, min1+1000)
	// Nothing yet — the debounce is still running.
	w.TickAll(min1 + 20_000)
	if len(s.rows) != before {
		t.Fatalf("wrote before the threshold elapsed")
	}
	// Past it now.
	w.TickAll(min1 + 40_000)
	if len(s.rows) == before {
		t.Fatal("a real outage was never recorded — the debounce swallowed it")
	}
	// ── AND IT CARRIES THE OBSERVED MOMENT ──────────────────────────────────
	//
	// Rule 2: the outage started when the link went, not when the timer fired.
	// Recording the firing moment would under-report every outage by the
	// threshold.
	last := s.rows[len(s.rows)-1]
	if last.TS != min1+1000 {
		t.Errorf("the outage is stamped %d, want the moment it was observed (%d)",
			last.TS, min1+1000)
	}
}

// TestTickAllIsWhatFiresIt — without a caller the debounce never expires, which
// is why zero was the only usable threshold before this existed.
func TestTickAllIsWhatFiresIt(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 30_000, min1)
	before := len(s.rows)
	w.Disconnected("r-1", 30_000, min1+1000)

	// No tick, however long passes.
	if len(s.rows) != before {
		t.Fatal("the disconnect wrote immediately at a non-zero threshold")
	}
	w.TickAll(min1 + minute)
	if len(s.rows) == before {
		t.Error("TickAll did not fire the expired debounce; a non-zero threshold " +
			"can then never record an outage at all")
	}
}

// TestAZeroThresholdStillRecordsAtOnce. Zero is a deliberate setting with its
// own branch, not an absence, and an operator who chooses it wants every close.
func TestAZeroThresholdStillRecordsAtOnce(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 0, min1)
	before := len(s.rows)
	w.Disconnected("r-1", 0, min1+1000)
	if len(s.rows) == before {
		t.Error("a zero threshold did not record the close immediately")
	}
}

// TestTickAllIsPerRouter — one router's expiring debounce must not write a row
// for another, and a fleet-wide sweep is where that is easy to get wrong.
func TestTickAllIsPerRouter(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 30_000, min1)
	w.Connected("r-2", 30_000, min1)
	before := len(s.rows)

	w.Disconnected("r-1", 30_000, min1+1000)
	w.TickAll(min1 + 40_000)

	got := s.rows[before:]
	if len(got) != 1 {
		t.Fatalf("one router's outage wrote %d rows", len(got))
	}
	if got[0].RouterID != "r-1" {
		t.Errorf("the outage was filed against %s", got[0].RouterID)
	}
}

// TestTickAllOnADisabledWireDoesNothing — the ticker runs only when recording
// is on, but the guard belongs here too: every other entry point has it.
func TestTickAllOnADisabledWireDoesNothing(t *testing.T) {
	s := &fakeStore{}
	w := New(false, s)
	w.Connected("r-1", 30_000, min1)
	w.Disconnected("r-1", 30_000, min1+1000)
	w.TickAll(min1 + minute)
	if len(s.rows) != 0 {
		t.Errorf("a disabled wire wrote %d rows", len(s.rows))
	}
}
