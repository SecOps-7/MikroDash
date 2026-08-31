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
	// The four families the defaults ship ON.
	for name, on := range map[string]bool{
		"NotifCPU": got.NotifCPU, "NotifPing": got.NotifPing,
		"NotifVPN": got.NotifVPN, "NotifBGP": got.NotifBGP,
		"NotifIfaceUpDown": got.NotifIfaceUpDown,
	} {
		if !on {
			t.Errorf("%s is off by default — an absent key was read as false", name)
		}
	}
	// And the three that ship OFF. Reading these as true would alert on things
	// the install deliberately does not watch.
	for name, on := range map[string]bool{
		"NotifNetwatch": got.NotifNetwatch, "NotifRouterUpdate": got.NotifRouterUpdate,
	} {
		if on {
			t.Errorf("%s is on by default", name)
		}
	}
}

// THE INTERFACE FILTERS carry their own defaults, and they are not uniform:
// ether and wlan ship on, bridge/vlan/other off.
func TestTheInterfaceFiltersKeepTheirOwnDefaults(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	got := s.alertSettings().IfaceTypeFilters
	for k, want := range map[string]bool{
		"notifIfaceEther": true, "notifIfaceWlan": true,
		"notifIfaceBridge": false, "notifIfaceVlan": false, "notifIfaceOther": false,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if len(got) != 5 {
		t.Errorf("%d filters, want 5 — a missing key reads as false and silences that kind",
			len(got))
	}
}

// AN EXPLICIT FALSE OVERRIDES AN ON-BY-DEFAULT TOGGLE, which is the whole point
// of the setting. `flag`'s fallback must not win over a value that is present.
func TestAnExplicitFalseIsHonoured(t *testing.T) {
	s := alertSettingsServer(t, `{"notifCpu":false,"notifPing":false,"notifIfaceEther":false}`)
	got := s.alertSettings()
	if got.NotifCPU {
		t.Error("notifCpu:false was overridden by the default")
	}
	if got.NotifPing {
		t.Error("notifPing:false was overridden by the default")
	}
	if got.IfaceTypeFilters["notifIfaceEther"] {
		t.Error("notifIfaceEther:false was overridden by the default")
	}
}

// AN EXPLICIT TRUE turns on a family that ships off.
func TestAnExplicitTrueIsHonoured(t *testing.T) {
	s := alertSettingsServer(t, `{"notifNetwatch":true,"notifRouterUpdate":true,"notifIfaceVlan":true}`)
	got := s.alertSettings()
	if !got.NotifNetwatch || !got.NotifRouterUpdate {
		t.Errorf("an explicit true was ignored: netwatch=%v update=%v",
			got.NotifNetwatch, got.NotifRouterUpdate)
	}
	if !got.IfaceTypeFilters["notifIfaceVlan"] {
		t.Error("notifIfaceVlan:true was ignored")
	}
}

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
	if got.CPUThreshold != 90 || !got.NotifCPU {
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
