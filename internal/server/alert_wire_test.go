package server

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/store"
)

// `alertSettings` — the read from settings.json that decides what alerts at all.
//
// Every case here is a way to be silently wrong: a threshold that reads as zero
// alerts on everything forever, and a toggle that reads as false silences a
// whole family. Both look like a working install.

// Named apart from `settingsServer` in `settings_api_test.go`, which serves the
// HTTP routes and builds far more than this needs. One fixture doing both jobs
// would make each test carry the other's setup.
func alertSettingsServer(t *testing.T, json string) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json": `[]`, "settings.json": json, ".secret": "test-secret",
		"users.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: st}
}

// AN EMPTY SETTINGS FILE gives the install's defaults, not zeroes.
//
// `store.Settings()` merges the file over `Settings.DEFAULTS`, so this asserts
// the merge is actually happening — a port reading the raw file would get 0 for
// the threshold, and a CPU threshold of zero alerts on every router at every
// poll, forever.
func TestAnEmptySettingsFileGivesTheDefaults(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	got := s.alertSettings()

	if got.CPUThreshold != 90 {
		t.Errorf("CPUThreshold = %v, want 90 — a zero threshold alerts on everything",
			got.CPUThreshold)
	}
	if got.PingLoss != 100 {
		t.Errorf("PingLoss = %v, want 100", got.PingLoss)
	}
}

// THE GATE DEFAULTS ARE GONE, AND SO ARE THEIR TESTS.
//
// Two checks lived here: that the four families shipping ON were read as ON
// when their key was absent, and that the interface filters kept their own
// non-uniform defaults. Both described the install-wide notif* gates, which
// notification channels replaced — every alert type is recorded now, and a
// channel's Events tab decides what is delivered. What is left to verify about
// `alertSettings` is the two thresholds, above.

// THE THRESHOLDS come through as numbers, including a zero the operator chose.
//
// Zero is a legitimate `alertPingLoss` — "alert on any loss at all" — so the
// number reader must not treat it as absent and substitute 100. That is the one
// case where "absent means default" and "present but falsy" genuinely differ.
func TestAnExplicitZeroThresholdIsNotTheDefault(t *testing.T) {
	s := alertSettingsServer(t, `{"alertPingLoss":0,"alertCpuThreshold":50}`)
	got := s.alertSettings()
	if got.PingLoss != 0 {
		t.Errorf("PingLoss = %v, want 0 — an operator asking to alert on ANY loss got "+
			"the default instead", got.PingLoss)
	}
	if got.CPUThreshold != 50 {
		t.Errorf("CPUThreshold = %v, want 50", got.CPUThreshold)
	}
}

// A settings file that is not an object at all must not take the server down.
func TestUnreadableSettingsFallBackToTheDefaults(t *testing.T) {
	s := alertSettingsServer(t, `not json`)
	got := s.alertSettings()
	if got.CPUThreshold != 90 || got.PingLoss != 100 {
		t.Errorf("an unreadable file produced %+v; the built-in thresholds should stand", got)
	}
}

// NO DATABASE, NO EVALUATOR — and no panic.
func TestTheWireIsNilWithoutAHistoryDatabase(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	if w := s.buildAlertWire(); w != nil {
		t.Error("an evaluator was built with no history database")
	}
	// And refreshing settings on a nil wire is a no-op rather than a crash.
	s.alerts = nil
	s.refreshAlertSettings()
}
