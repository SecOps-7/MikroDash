package server

// The two fleet syncs resolve the default interface and declare what history
// keeps. Both halves were the bug: the syncs took `r.DefaultIf` RAW, so a
// router with none gave the traffic collector an empty interface list — and
// `syncStream` opens nothing for an empty list, so the pools that run when
// nobody is watching recorded no traffic at all. Issue #126.

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/alertpool"
	"mikrodash/internal/historywire"
	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
)

// Neither router names a default interface on r1; r2 names its own.
const blankIfFixture = `[
  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "defaultIf":""},
  {"id":"r2","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":"",
   "defaultIf":"sfp1"}
]`

func refuse(routeros.Config) (alertpool.Conn, error) { return nil, os.ErrClosed }

func recServer(t *testing.T) *Server {
	t.Helper()
	s, _, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"defaultIf":"ether5"}`)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"),
		[]byte(blankIfFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	s.historyWire = historywire.New(true, nil)
	return s
}

// TestTheGlobalDefaultInterfaceIsRead — the middle rung of the precedence, and
// the one neither pool consulted.
func TestTheGlobalDefaultInterfaceIsRead(t *testing.T) {
	s := recServer(t)
	if got := s.globalDefaultIf(); got != "ether5" {
		t.Fatalf("globalDefaultIf = %q, want the install setting", got)
	}
	if got := routers.DefaultIfFor("", s.globalDefaultIf()); got != "ether5" {
		t.Errorf("a blank defaultIf resolved to %q", got)
	}
}

// TestTheAlertPoolSyncDeclaresWhatToRecord. This pool is the one holding
// routers nobody is watching, so its declaration is what makes a series
// continuous rather than following a browser tab.
func TestTheAlertPoolSyncDeclaresWhatToRecord(t *testing.T) {
	s := recServer(t)
	s.alertPool = alertpool.New(refuse, 0, nil, nil, nil)
	t.Cleanup(s.alertPool.Close)

	s.syncAlertPool()

	if !s.historyWire.Records("r1", "ether5") {
		t.Error("r1 does not record the install-wide default interface, so a " +
			"router with no default of its own records nothing in the background")
	}
	if s.historyWire.Records("r1", "ether3") {
		t.Error("r1 still records an interface only a viewer would have added — " +
			"the browser is deciding what gets written")
	}
	if !s.historyWire.Records("r2", "sfp1") {
		t.Error("r2 does not record its own default interface")
	}
}

// TestTheOverviewPoolSyncDeclaresItToo — either pool may hold a given router,
// so a declaration made by only one of them leaves gaps on handover.
func TestTheOverviewPoolSyncDeclaresItToo(t *testing.T) {
	s := recServer(t)
	s.pool = routers.NewPool(
		func(routeros.Config) (routers.Conn, error) { return nil, os.ErrClosed },
		0, nil, nil)
	t.Cleanup(s.pool.Close)

	s.syncPool()

	if !s.historyWire.Records("r1", "ether5") {
		t.Error("the overview pool's sync declared nothing for r1")
	}
	if s.historyWire.Records("r2", "ether5") {
		t.Error("r2 records r1's interface; the declaration is not per router")
	}
}
