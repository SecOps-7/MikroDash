package collect

import "testing"

// The anti-flap rule is the whole reason this type exists, so it is the first
// thing tested: a stream that dies every fifteen seconds delivers a burst of
// rows after each restart, and a counter reset by "data arrived" never climbs.

func TestThreeRestartsDegrade(t *testing.T) {
	var h StreamHealth
	for i := 1; i <= 2; i++ {
		if deg, changed := h.RecordRestart(int64(i)); deg || changed {
			t.Fatalf("restart %d reported degraded=%v changed=%v, want neither yet",
				i, deg, changed)
		}
	}
	deg, changed := h.RecordRestart(3)
	if !deg || !changed {
		t.Errorf("the third restart reported degraded=%v changed=%v", deg, changed)
	}
	if h.Since() != 3 {
		t.Errorf("Since = %d, want the moment it degraded", h.Since())
	}
}

// TestStayingDegradedIsNotATransition — the caller emits on `changed`, so a
// stream that is degraded and staying degraded must not push a frame to every
// browser on every five-second tick.
func TestStayingDegradedIsNotATransition(t *testing.T) {
	var h StreamHealth
	for i := 0; i < 3; i++ {
		h.RecordRestart(1)
	}
	for i := 0; i < 10; i++ {
		if deg, changed := h.RecordRestart(2); !deg || changed {
			t.Fatalf("a further restart reported degraded=%v changed=%v", deg, changed)
		}
	}
	if h.Restarts() != 13 {
		t.Errorf("Restarts = %d, want every restart still counted", h.Restarts())
	}
}

// TestABurstOfDataDoesNotCountAsRecovery is the rule from the header.
func TestABurstOfDataDoesNotCountAsRecovery(t *testing.T) {
	var h StreamHealth
	for i := 0; i < 3; i++ {
		h.RecordRestart(1)
	}
	// Fifteen seconds of uptime, then it dies again. That is the flapping
	// stream, and it must stay degraded.
	if deg, changed := h.RecordHealthy(15_000); !deg || changed {
		t.Errorf("15s of uptime cleared the degraded state (degraded=%v changed=%v) — "+
			"a stream that dies every 15s would never be reported at all",
			deg, changed)
	}
	if h.Restarts() != 3 {
		t.Errorf("Restarts = %d; the count was reset by a burst of data", h.Restarts())
	}
}

func TestAMinuteOfUptimeRecovers(t *testing.T) {
	var h StreamHealth
	for i := 0; i < 3; i++ {
		h.RecordRestart(1)
	}
	deg, changed := h.RecordHealthy(streamHealthyMs)
	if deg || !changed {
		t.Errorf("a full healthy window reported degraded=%v changed=%v", deg, changed)
	}
	if h.Restarts() != 0 {
		t.Errorf("Restarts = %d after recovery", h.Restarts())
	}
}

// TestAHealthyStreamReportsNothing — the common case, and it must be silent.
// A collector that emitted on every healthy tick would send a frame per stream
// per five seconds to every browser, for ever.
func TestAHealthyStreamReportsNothing(t *testing.T) {
	var h StreamHealth
	for i := 0; i < 20; i++ {
		if deg, changed := h.RecordHealthy(600_000); deg || changed {
			t.Fatalf("a healthy tick reported degraded=%v changed=%v", deg, changed)
		}
	}
}

// TestRestartsBelowTheThresholdClearQuietly. One or two restarts never reported
// anything, so clearing them must not announce a recovery from a state nobody
// was told about.
func TestRestartsBelowTheThresholdClearQuietly(t *testing.T) {
	var h StreamHealth
	h.RecordRestart(1)
	h.RecordRestart(2)
	deg, changed := h.RecordHealthy(streamHealthyMs)
	if deg {
		t.Error("reported degraded after two restarts")
	}
	if changed {
		t.Error("announced a recovery from a degraded state that was never announced")
	}
	if h.Restarts() != 0 {
		t.Errorf("Restarts = %d; the count should still clear", h.Restarts())
	}
}

func TestResetDropsEverything(t *testing.T) {
	var h StreamHealth
	for i := 0; i < 5; i++ {
		h.RecordRestart(1)
	}
	h.Reset()
	if h.Degraded() || h.Restarts() != 0 || h.Since() != 0 {
		t.Errorf("after Reset: degraded=%v restarts=%d since=%d",
			h.Degraded(), h.Restarts(), h.Since())
	}
}
