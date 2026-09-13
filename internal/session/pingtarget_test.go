package session

// THE DASHBOARD SESSION PINGS THE ROUTER'S OWN TARGET.
//
// It passed "" to NewPing, so every router pinged the default 1.1.1.1 — the hAP
// AX3's record said 9.9.9.9 and was ignored. The Devices pool already passed
// `cfg.PingTarget`, and its comment claimed the two were the same values.

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/store"
)

func sessionWithRecord(t *testing.T, routerJSON string) (*Manager, *Session) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	for name, body := range map[string]string{"settings.json": `{}`, "routers.json": routerJSON} {
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

func TestTheSessionPingsTheRoutersOwnTarget(t *testing.T) {
	m, s := sessionWithRecord(t, `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
	  "username":"u","password":"","pingTarget":"198.51.100.9"}]`)
	defer m.Shutdown()
	defer m.Release("r1")
	if got := s.ping.History().Target; got != "198.51.100.9" {
		t.Errorf("the session pings %q; the router's record says 198.51.100.9", got)
	}
}

func TestARecordWithNoPingTargetPingsTheDefault(t *testing.T) {
	m, s := sessionWithRecord(t, `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
	  "username":"u","password":""}]`)
	defer m.Shutdown()
	defer m.Release("r1")
	if got := s.ping.History().Target; got != "1.1.1.1" {
		t.Errorf("a record with no pingTarget pings %q, want the default 1.1.1.1", got)
	}
}

func TestApplyPingTargetReachesALiveSession(t *testing.T) {
	m, s := sessionWithRecord(t, `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
	  "username":"u","password":"","pingTarget":"198.51.100.9"}]`)
	defer m.Shutdown()
	defer m.Release("r1")

	m.ApplyPingTarget("r1", "198.51.100.10")
	if got := s.ping.History().Target; got != "198.51.100.10" {
		t.Errorf("after ApplyPingTarget the live session pings %q, want 198.51.100.10", got)
	}
	m.ApplyPingTarget("no-such-router", "198.51.100.11") // a router with no session is not an error
}
