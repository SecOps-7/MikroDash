package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"mikrodash/internal/history"
	"mikrodash/internal/historywire"
)

// overviewServer is the routers test server with the overview route and the
// purge seed: one connectivity row (up, ts 1), one monitoring run [1,2] and one
// backup run (ts 1) for each of r1 and r2. `seedPurgeables` builds those tables
// in the real schema's shape, which is why it is reused rather than retyped.
func overviewServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, _ := routersServer(t, sess, "")
	s.registerDevicesOverview(mux)
	seedPurgeables(t, s)
	return s, mux
}

func getOverview(t *testing.T, mux *http.ServeMux, query string) (int, []DeviceOverview) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/devices/overview"+query, nil)
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var rows []DeviceOverview
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decoding %s: %v", w.Body.String(), err)
		}
	}
	return w.Code, rows
}

// TestTheOverviewDrawsEachDevicesStrip — the whole path, store to wire: the
// seeded router was up at 1 and watched from 1 to 2, so a window [1,1000] is one
// millisecond up and the rest NOT MONITORED - never inferred green.
func TestTheOverviewDrawsEachDevicesStrip(t *testing.T) {
	_, mux := overviewServer(t, &Session{AuthMode: "none", Username: "admin"})
	code, rows := getOverview(t, mux, "?from=1&to=1000")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(rows) != 2 {
		t.Fatalf("%d row(s), want one per enabled router", len(rows))
	}
	want := []history.Span{{From: 1, To: 2, State: history.SpanUp}, {From: 2, To: 1000, State: history.SpanUnmonitored}}
	for _, r := range rows {
		if !reflect.DeepEqual(r.Spans, want) {
			t.Errorf("%s spans = %+v, want %+v", r.RouterID, r.Spans, want)
		}
		if r.UptimePct == nil || *r.UptimePct != 100 {
			t.Errorf("%s uptime = %v, want 100 over the one watched millisecond", r.RouterID, r.UptimePct)
		}
		if r.Backup == nil || r.Backup.LastAt != 1 {
			t.Errorf("%s backup = %+v, want the seeded run at 1", r.RouterID, r.Backup)
		}
	}

	// `routerId` NARROWS, for the device modal.
	if _, one := getOverview(t, mux, "?from=1&to=1000&routerId=r2"); len(one) != 1 || one[0].RouterID != "r2" {
		t.Errorf("routerId=r2 returned %+v, want only r2", one)
	}
}

// TestTheBackupFieldNeedsBackupsRead — the strip is visible wherever the row is,
// but the backup is backup data. A viewer with the Devices page and not Backups
// gets the strip and a NULL backup; the control grants Backups and gets it.
func TestTheBackupFieldNeedsBackupsRead(t *testing.T) {
	_, mux := overviewServer(t, &Session{AuthMode: "modern", Username: "v",
		Pages: map[string]string{"devices": "read"}})
	code, rows := getOverview(t, mux, "?from=1&to=1000")
	if code != http.StatusOK || len(rows) == 0 {
		t.Fatalf("status %d, %d rows", code, len(rows))
	}
	for _, r := range rows {
		if r.Backup != nil {
			t.Errorf("%s: a viewer without Backups read was sent the backup %+v", r.RouterID, r.Backup)
		}
		if len(r.Spans) == 0 {
			t.Errorf("%s: the strip was withheld too; it needs only the Devices row's access", r.RouterID)
		}
	}

	// THE CONTROL: the same viewer with Backups read sees it.
	_, mux = overviewServer(t, &Session{AuthMode: "modern", Username: "v",
		Pages: map[string]string{"devices": "read", "backups": "read"}})
	if _, rows := getOverview(t, mux, "?from=1&to=1000"); len(rows) == 0 || rows[0].Backup == nil {
		t.Errorf("a viewer WITH Backups read got no backup: %+v", rows)
	}
}

// TestTheOverviewRefusesWithoutTheDevicesPage — the page's own gate, before any
// router-level question.
func TestTheOverviewRefusesWithoutTheDevicesPage(t *testing.T) {
	_, mux := overviewServer(t, &Session{AuthMode: "modern", Username: "v",
		Pages: map[string]string{"backups": "read"}})
	if code, _ := getOverview(t, mux, ""); code != http.StatusForbidden {
		t.Errorf("status %d without the Devices page, want 403", code)
	}
}

// TestTheOverviewWindowIsBoundedAndDefaulted — 24 hours by default, never more
// than a month, and nonsense ignored rather than obeyed.
func TestTheOverviewWindowIsBoundedAndDefaulted(t *testing.T) {
	const now = int64(10_000_000_000)
	day := int64(24 * 3600 * 1000)
	for _, c := range []struct {
		q            string
		wantF, wantT int64
	}{
		{"", now - day, now},
		{"?from=1&to=" + msStr(now), now - 31*day, now}, // clamped to a month
		{"?from=" + msStr(now+5) + "&to=" + msStr(now), now - day, now},
		{"?to=" + msStr(now+99), now - day, now}, // the future is now
	} {
		r := httptest.NewRequest("GET", "/api/devices/overview"+c.q, nil)
		if f, to := overviewWindow(r, now); f != c.wantF || to != c.wantT {
			t.Errorf("%q -> [%d,%d], want [%d,%d]", c.q, f, to, c.wantF, c.wantT)
		}
	}
}

func msStr(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestAnOpenRunReachesTheEndOfTheWindow — found live: a run's stored
// `last_seen_at` moves only on the minute heartbeat, so every watched router's
// strip ended in a grey "not monitored" tail. A run open NOW reaches the end of
// the window; the control is r2, whose run is not open and keeps its tail.
func TestAnOpenRunReachesTheEndOfTheWindow(t *testing.T) {
	s, mux := overviewServer(t, &Session{AuthMode: "none", Username: "admin"})
	s.coverage = historywire.NewCoverage(true, s.auditDB)
	s.coverage.Update(map[string]bool{"r1": true}, 2)

	_, rows := getOverview(t, mux, "?from=1&to=1000")
	by := map[string][]history.Span{}
	for _, r := range rows {
		by[r.RouterID] = r.Spans
	}
	if sp := by["r1"]; len(sp) == 0 || sp[len(sp)-1].State != history.SpanUp || sp[len(sp)-1].To != 1000 {
		t.Errorf("r1 is being watched now and its strip ends %+v; it must reach 1000 up", sp)
	}
	if sp := by["r2"]; len(sp) == 0 || sp[len(sp)-1].State != history.SpanUnmonitored {
		t.Errorf("r2's run is NOT open, yet its strip ends %+v - the extension is not per router", sp)
	}
}
