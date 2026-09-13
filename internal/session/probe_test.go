package session

// A DORMANCY PROBE TAKES A READING. IT DOES NOT WAKE THE COLLECTOR.
//
// ── THE DEFECT ──────────────────────────────────────────────────────────────
//
// `probe` resumed the collector through `ResumeCollector`. On a router somebody
// was viewing, that funnel hands a SLEEPING collector to `WakeForFocus`, which
// resets the dormancy state outright — backoff included. So every probe put the
// collector back to square one, and the backoff never grew past its first 60s.
//
// Measured on 2026-09-12: the viewed hAP AX3 logged "queues asleep" 497 times in
// a day, every 107-110 seconds, with no "awake" between. Unviewed routers slept
// properly, because `Needs` refused them before the dormancy branch was reached.
//
// A probe is now a single reading. An empty one leaves the collector asleep and
// the supervisor backs off; one with data wakes it through the supervisor's own
// wake path.

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/dormancy"
	"mikrodash/internal/hub"
	"mikrodash/internal/store"
)

// probeSession acquires a session against a real store. The router address is
// TEST-NET and unreachable on purpose: nothing here needs a connection.
func probeSession(t *testing.T) (*Manager, *Session) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	for name, body := range map[string]string{
		"settings.json": `{}`,
		"routers.json": `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
		  "username":"u","password":""}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, hub.New())
	s, err := m.Acquire("r1")
	if err != nil {
		m.Shutdown()
		t.Fatal(err)
	}
	return m, s
}

// asleep puts a collector to sleep the way the supervisor does in production:
// three empty readings, each with a fresh timestamp.
func asleep(t *testing.T, s *Session, key string) {
	t.Helper()
	for i := int64(1); i <= 3; i++ {
		s.dormancy.Tick(dormancy.TickInput{
			Now: i * 1000, Watching: true, StartupReady: true,
			Collectors: []dormancy.Collector{{Key: key, Enabled: true, Present: true, TS: i, Empty: true}},
		})
	}
	if !s.dormancy.IsDormant(key) {
		t.Fatalf("setup: three empty readings did not put %s to sleep", key)
	}
}

func TestAProbeReadsWithoutWakingTheCollector(t *testing.T) {
	m, s := probeSession(t)
	defer m.Shutdown()
	defer m.Release("r1")
	asleep(t, s, "queues")

	refreshed, resumed := 0, 0
	s.probe("queues", collectorTarget{
		last:    func() any { return nil },
		suspend: func() {},
		resume:  func() { resumed++ },
		refresh: func() { refreshed++ },
	})

	if !s.dormancy.IsDormant("queues") {
		t.Error("probing a sleeping collector on a viewed router woke it: the dormancy state " +
			"was reset, so its backoff can never grow and it re-sleeps every ~108s for ever")
	}
	if refreshed != 1 {
		t.Errorf("the probe took %d readings, want exactly 1", refreshed)
	}
	if resumed != 0 {
		t.Errorf("the probe resumed the collector %d time(s): an empty probe would then leave "+
			"it polling while the supervisor believes it is asleep", resumed)
	}
}

// A probe for a collector the session has no reason to run reads nothing. The
// funnel refused the resume, but the old probe went on to refresh anyway: one
// router command per probe for a page nobody had open.
func TestAProbeReadsNothingTheSessionHasNoReasonToRun(t *testing.T) {
	m, s := probeSession(t)
	defer m.Shutdown()
	m.Release("r1") // no viewer, and no hold: nothing wants `queues`
	asleep(t, s, "queues")

	refreshed := 0
	s.probe("queues", collectorTarget{
		last: func() any { return nil }, suspend: func() {}, resume: func() {},
		refresh: func() { refreshed++ },
	})
	if refreshed != 0 {
		t.Errorf("a probe read a collector the session has no reason to run (%d reading)", refreshed)
	}
}
