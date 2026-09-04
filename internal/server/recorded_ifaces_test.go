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

	"time"

	"mikrodash/internal/alertpool"
	"mikrodash/internal/history"
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

// ── THE OUTAGE DEBOUNCE REACHES THE STATUS HOOK ────────────────────────────
//
// `alertPoolStatus` is handed a router id and a bool, so the threshold has to
// come from somewhere it can reach cheaply — reading the record there would
// mean decrypting every router's password on every connect and drop. The fleet
// syncs cache it.
//
// Passing a hardcoded zero instead is not a small error: zero is its own branch
// meaning "record every close at once", and it turned a routine six-second
// reconnect into an outage in the Reports page. A mutation restoring that zero
// survived every other test in this package.

type rowSink struct{ n int }

func (r *rowSink) PersistHistoryLogged(rows []history.Row) int { r.n += len(rows); return len(rows) }

// threshFixture: r1 leaves the debounce unset (live default 30s), r2 asks for
// zero, which is a deliberate setting rather than an absence.
const threshFixture = `[
  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "defaultIf":"ether1"},
  {"id":"r2","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":"",
   "defaultIf":"ether1","connDownThresholdSec":0}
]`

func threshServer(t *testing.T) (*Server, *rowSink) {
	t.Helper()
	s, _, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, `{}`)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"),
		[]byte(threshFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &rowSink{}
	s.historyWire = historywire.New(true, sink)
	s.alertPool = alertpool.New(refuse, 0, nil, nil, nil)
	t.Cleanup(s.alertPool.Close)
	s.syncAlertPool()
	return s, sink
}

func TestTheSyncCachesEachRoutersDebounce(t *testing.T) {
	s, _ := threshServer(t)
	if got := s.connThresholdMs("r1"); got != 30_000 {
		t.Errorf("a router with no setting resolved to %dms, want the live 30s default", got)
	}
	if got := s.connThresholdMs("r2"); got != 0 {
		t.Errorf("a router asking for zero resolved to %dms; zero is a deliberate "+
			"setting, not an absence", got)
	}
}

// TestABriefDropIsNotRecordedThroughTheHook is the reported bug, driven through
// the hook the pool actually calls.
func TestABriefDropIsNotRecordedThroughTheHook(t *testing.T) {
	s, sink := threshServer(t)
	s.alertPoolStatus("r1", true)
	up := sink.n

	// Down and back, the way a routine reconnect goes.
	s.alertPoolStatus("r1", false)
	if sink.n != up {
		t.Errorf("the drop was written immediately (%d rows) — the hook is using a "+
			"zero threshold rather than the router's 30s", sink.n-up)
	}
	s.alertPoolStatus("r1", true)
	// Long past the threshold: the reconnect must have cancelled it outright.
	s.historyWire.TickAll(time.Now().Add(time.Hour).UnixMilli())
	if sink.n != up {
		t.Errorf("a six-second reconnect ended up as %d recorded row(s)", sink.n-up)
	}
}

// The other direction, or the test above passes against a hook that records
// nothing at all.
func TestARouterAskingForZeroStillRecordsAtOnce(t *testing.T) {
	s, sink := threshServer(t)
	s.alertPoolStatus("r2", true)
	before := sink.n
	s.alertPoolStatus("r2", false)
	if sink.n == before {
		t.Error("a router configured with a zero threshold did not record its " +
			"close immediately")
	}
}
