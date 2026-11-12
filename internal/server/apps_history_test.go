package server

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

func appMinute(start int64, apps map[string]collect.AppBytes) collect.AppMinute {
	return collect.AppMinute{Start: start, Apps: apps}
}

// TestTheRecorderAddsEachClosedMinuteToItsHour. The collector hands over each
// minute it closes, already in bytes; the recorder adds it to the hour the
// minute starts in, for every router with Reporting on, and the unnamed bytes
// under apps.UnnamedID. A router without Reporting is not kept, and one whose
// Reporting goes off loses what was summed before the flush.
func TestTheRecorderAddsEachClosedMinuteToItsHour(t *testing.T) {
	r := newAppUsageRecorder()
	const h = 1_800_000_000_000 - 1_800_000_000_000%hourMs
	nf := func(rx, tx float64) map[string]collect.AppBytes {
		return map[string]collect.AppBytes{"netflix": {Rx: rx, Tx: tx}, "quiet": {}}
	}

	r.add("r1", appMinute(h, nf(1, 1))) // r1's Reporting is not declared yet
	r.setReporting("r1", true)
	r.add("r1", collect.AppMinute{Start: h, Apps: nf(5_000_000, 500_000), Unnamed: collect.AppBytes{Rx: 300, Tx: 30}})
	r.add("r2", appMinute(h+59*60_000, nf(7, 0))) // r2 has Reporting off
	r.add("r1", appMinute(h+59*60_000, nf(20_000_000, 0)))
	r.add("r1", appMinute(h+hourMs, nf(10_000_000, 0)))

	type at struct {
		router, app string
		hour        int64
	}
	got := map[at]db.AppUsageRow{}
	for _, row := range r.take() {
		if row.App == "quiet" {
			t.Fatalf("a row for %s, which moved nothing", row.App)
		}
		got[at{row.RouterID, row.App, row.Hour}] = row
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 25_000_000, TxBytes: 500_000}); got[at{"r1", "netflix", h}] != want {
		t.Errorf("r1's first hour holds %+v, want %+v", got[at{"r1", "netflix", h}], want)
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h + hourMs, RxBytes: 10_000_000}); got[at{"r1", "netflix", h + hourMs}] != want {
		t.Errorf("r1's second hour holds %+v, want %+v", got[at{"r1", "netflix", h + hourMs}], want)
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: apps.UnnamedID, Hour: h, RxBytes: 300, TxBytes: 30}); got[at{"r1", apps.UnnamedID, h}] != want {
		t.Errorf("r1's unnamed traffic holds %+v, want %+v", got[at{"r1", apps.UnnamedID, h}], want)
	}
	if len(got) != 3 {
		t.Errorf("rows %v, want three: r2 has Reporting off and is not kept", got)
	}
	if len(r.take()) != 0 {
		t.Error("a second take returned the same bytes again")
	}

	// Reporting switched off between a minute and the flush: nothing of that
	// router is written, and nothing after.
	r.setReporting("r2", true)
	r.add("r1", appMinute(h+hourMs+60_000, nf(8, 0)))
	r.add("r2", appMinute(h+hourMs+60_000, nf(9, 0)))
	r.setReporting("r1", false)
	r.add("r1", appMinute(h+hourMs+120_000, nf(8, 0)))
	if rows := r.take(); len(rows) != 1 || rows[0].RouterID != "r2" {
		t.Errorf("r1's Reporting went off and the rows were %v, want only r2's", rows)
	}
	if r.recording("r1") || !r.recording("r2") {
		t.Errorf("recording r1=%v r2=%v, want false and true", r.recording("r1"), r.recording("r2"))
	}
}

// TestTheHistoryIsBucketedByHourOrDay. A day is the last 24 hours, the current
// one last; a week and a month are days in the display timezone, and each
// hour's bytes land in the bucket that holds it.
func TestTheHistoryIsBucketedByHourOrDay(t *testing.T) {
	loc := time.FixedZone("plus2", 2*3600)
	now := time.Date(2026, 11, 12, 10, 30, 0, 0, loc)
	day := appHistoryBuckets("day", now, loc)
	if len(day) != 24 || day[23] != now.Truncate(time.Hour).UnixMilli() || day[1]-day[0] != hourMs {
		t.Fatalf("day buckets %v", day)
	}
	week := appHistoryBuckets("week", now, loc)
	if len(week) != 7 || week[6] != time.Date(2026, 11, 12, 0, 0, 0, 0, loc).UnixMilli() {
		t.Fatalf("week buckets start %v, want local midnights ending today", week)
	}
	if n := len(appHistoryBuckets("month", now, loc)); n != 30 {
		t.Errorf("a month has %d buckets, want 30", n)
	}

	cat := apps.Default()
	rows := []db.AppUsageRow{
		{App: "netflix", Hour: week[6] - hourMs, RxBytes: 100, TxBytes: 10},   // 23:00 yesterday
		{App: "netflix", Hour: week[6], RxBytes: 200, TxBytes: 20},            // midnight today
		{App: "teams", Hour: week[6] + 2*hourMs, RxBytes: 1000, TxBytes: 500}, // today
		{App: "not-in-the-catalogue", Hour: week[6], RxBytes: 1},
		{App: apps.UnnamedID, Hour: week[6] - hourMs, RxBytes: 40, TxBytes: 4},
		{App: apps.UnnamedID, Hour: week[6] + hourMs, RxBytes: 50, TxBytes: 5},
	}
	h := buildAppHistory(rows, week, now.UnixMilli(), cat)
	if h.From != week[0] {
		t.Errorf("from %d, want %d", h.From, week[0])
	}
	if got := h.Buckets[5].Apps["netflix"]; got != 110 {
		t.Errorf("yesterday's netflix is %d, want 110", got)
	}
	if got := h.Buckets[6].Apps; got["netflix"] != 220 || got["teams"] != 1500 {
		t.Errorf("today's bucket is %v", got)
	}
	if len(h.Apps) != 3 || h.Apps[0].ID != "teams" || h.Apps[1].ID != "netflix" {
		t.Fatalf("apps %+v, want teams then netflix, busiest first", h.Apps)
	}
	if h.Apps[1].RxBytes != 300 || h.Apps[1].TxBytes != 30 {
		t.Errorf("netflix totals %+v", h.Apps[1])
	}
	if want, _ := cat.App("netflix"); h.Apps[1].Name != want.Name || h.Apps[1].Color != want.Color {
		t.Errorf("netflix was not named from the catalogue: %+v", h.Apps[1])
	}
	if h.Apps[2].Name != "not-in-the-catalogue" || h.Apps[2].Category != "other" {
		t.Errorf("an app the catalogue no longer has was dropped or misnamed: %+v", h.Apps[2])
	}
	// The unnamed traffic is its own figure, never an app.
	if h.UnnamedRxBytes != 90 || h.UnnamedTxBytes != 9 || h.Buckets[5].Unnamed != 44 || h.Buckets[6].Unnamed != 55 {
		t.Errorf("unnamed %d/%d, buckets %d and %d; want 90/9, 44 and 55", h.UnnamedRxBytes, h.UnnamedTxBytes, h.Buckets[5].Unnamed, h.Buckets[6].Unnamed)
	}
	if _, ok := h.Buckets[6].Apps[apps.UnnamedID]; ok {
		t.Error("the unnamed traffic was counted as an app in a bucket")
	}
	if b, _ := json.Marshal(buildAppHistory(nil, week, 0, cat)); !strings.Contains(string(b), `"apps":[]`) {
		t.Errorf("an empty history sends a null list: %s", b)
	}
}

const appHistoryGateDDL = `
CREATE TABLE app_usage (router_id TEXT NOT NULL, app TEXT NOT NULL, hour INTEGER NOT NULL,
  rx_bytes INTEGER NOT NULL DEFAULT 0, tx_bytes INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (router_id, app, hour));`

// TestTheAppHistoryFollowsItsGates. Reading the history needs a signed-in
// user and the Apps Usage page on that router; there is no feature switch any
// more. The answer says whether the router is being recorded, which is its
// Reporting setting.
func TestTheAppHistoryFollowsItsGates(t *testing.T) {
	grant := appHistoryGateDDL + `
INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','apps','write'), ('role-r','apps','read');`
	for _, c := range []struct {
		name, extra, url string
		signedIn         bool
		want             int
		recording        bool
	}{
		{"signed out", grant, "/api/apps/history?routerId=r-A&period=week", false, 401, false},
		{"bad router id", grant, "/api/apps/history?routerId=../x&period=week", true, 400, false},
		{"no Apps Usage grant", appHistoryGateDDL, "/api/apps/history?routerId=r-A&period=week", true, 403, false},
		{"granted, Reporting on", grant, "/api/apps/history?routerId=r-A&period=week", true, 200, true},
		{"granted, Reporting off", grant, "/api/apps/history?routerId=r-B&period=week", true, 200, false},
	} {
		s := gateServer(t, `{}`, c.extra)
		s.appUsage = newAppUsageRecorder()
		on, off := true, false
		s.declareReporting(store.Router{ID: "r-A", ReportingEnabled: &on})
		s.declareReporting(store.Router{ID: "r-B", ReportingEnabled: &off})
		req := httptest.NewRequest("GET", c.url, nil)
		if c.signedIn {
			req.Header.Set("Cookie", authed)
		}
		w := httptest.NewRecorder()
		s.appHistoryGet(w, req)
		if w.Code != c.want {
			t.Errorf("%s: answered %d (%s), want %d", c.name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
		if c.want == 200 {
			var got AppHistory
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Buckets) != 7 || got.Apps == nil {
				t.Errorf("%s: %v %s", c.name, err, w.Body.String())
			}
			if got.Recording != c.recording {
				t.Errorf("%s: recording = %v, want %v", c.name, got.Recording, c.recording)
			}
		}
	}
}

// TestReportingDecidesWhichRoutersAreRecorded. declareReporting, which both
// fleet syncs call for every router, is what tells the recorder: a router with
// Reporting on is kept, one never set or set off is not.
func TestReportingDecidesWhichRoutersAreRecorded(t *testing.T) {
	s := gateServer(t, `{}`, appHistoryGateDDL)
	s.appUsage = newAppUsageRecorder()
	on, off := true, false
	for _, r := range []store.Router{{ID: "on", ReportingEnabled: &on}, {ID: "off", ReportingEnabled: &off}, {ID: "never-set"}} {
		s.declareReporting(r)
		s.appUsage.add(r.ID, appMinute(0, map[string]collect.AppBytes{"netflix": {Rx: 1000}}))
	}
	rows := s.appUsage.take()
	if len(rows) != 1 || rows[0].RouterID != "on" {
		t.Errorf("rows %+v, want only the router with Reporting on", rows)
	}
	// Turned off later, it stops.
	s.declareReporting(store.Router{ID: "on", ReportingEnabled: &off})
	s.appUsage.add("on", appMinute(60_000, map[string]collect.AppBytes{"netflix": {Rx: 1000}}))
	if rows := s.appUsage.take(); len(rows) != 0 {
		t.Errorf("Reporting turned off and %v was still kept", rows)
	}
}

// TestAppUsageRoundsToWholeBytes guards the one float-to-int step: a sum is
// rounded, not truncated.
func TestAppUsageRoundsToWholeBytes(t *testing.T) {
	r := newAppUsageRecorder()
	r.setReporting("r1", true)
	r.add("r1", appMinute(0, map[string]collect.AppBytes{"x": {Rx: 0.00999}}))
	r.add("r1", appMinute(60_000, map[string]collect.AppBytes{"x": {Rx: 0.9375}}))
	rows := r.take()
	if len(rows) != 1 || rows[0].RxBytes != int64(math.Round(0.00999+0.9375)) {
		t.Errorf("rows %+v, want 1 byte", rows)
	}
}
