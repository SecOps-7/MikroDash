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

// ── `alertSettings` IS GONE, AND ITS QUESTION MOVED ───────────────────────
//
// Three tests stood here, on the read from settings.json that decided what
// alerted at all: the defaults when the file is empty, an operator's explicit
// values (including a zero they chose), and an unreadable file falling back
// rather than to zeroes.
//
// The thresholds are a property of each notification channel now. The same
// question survives one step along — `SeedChannelTuning` reads those same three
// keys once and carries them onto every channel — so the cases are there, in
// `tuning_migrate_test.go`, rather than deleted.
//
// ONE OF THEM COULD NOT SURVIVE, and it is worth saying which. A zero
// `alertPingLoss` meant "alert on any loss at all", and there is no way to ask
// for that any more: `alert.FloorPingLoss` records nothing under 50%, and a zero
// in a channel's tuning reads as "use the default". An install that had set it
// to zero is carried to the default. That is a real loss of reach, accepted
// with the fixed floor.

// NO DATABASE, NO EVALUATOR — and no panic.
func TestTheWireIsNilWithoutAHistoryDatabase(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	if w := s.buildAlertWire(); w != nil {
		t.Error("an evaluator was built with no history database")
	}
	// And refreshing on a nil wire is a no-op rather than a crash. It no longer
	// touches the evaluators at all — nothing they read is a setting — but the
	// dispatcher half still runs and must survive a half-built server.
	s.alerts = nil
	s.refreshAlertSettings()
}
