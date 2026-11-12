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
// minute starts in, for every router, while the feature is on. Off, nothing is
// kept, and what was summed before it went off is not written.
func TestTheRecorderAddsEachClosedMinuteToItsHour(t *testing.T) {
	r := newAppUsageRecorder()
	const h = 1_800_000_000_000 - 1_800_000_000_000%hourMs
	nf := func(rx, tx float64) map[string]collect.AppBytes {
		return map[string]collect.AppBytes{"netflix": {Rx: rx, Tx: tx}, "quiet": {}}
	}

	r.add("r1", appMinute(h, nf(1, 1))) // the feature is not on yet
	r.setOn(true)
	r.add("r1", appMinute(h, nf(5_000_000, 500_000)))
	r.add("r2", appMinute(h+59*60_000, nf(7, 0)))
	r.add("r1", appMinute(h+59*60_000, nf(20_000_000, 0)))
	r.add("r1", appMinute(h+hourMs, nf(10_000_000, 0)))

	type at struct {
		router string
		hour   int64
	}
	got := map[at]db.AppUsageRow{}
	for _, row := range r.take() {
		if row.App != "netflix" {
			t.Fatalf("a row for %s, which moved nothing", row.App)
		}
		got[at{row.RouterID, row.Hour}] = row
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 25_000_000, TxBytes: 500_000}); got[at{"r1", h}] != want {
		t.Errorf("r1's first hour holds %+v, want %+v", got[at{"r1", h}], want)
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h + hourMs, RxBytes: 10_000_000}); got[at{"r1", h + hourMs}] != want {
		t.Errorf("r1's second hour holds %+v, want %+v", got[at{"r1", h + hourMs}], want)
	}
	if want := (db.AppUsageRow{RouterID: "r2", App: "netflix", Hour: h, RxBytes: 7}); got[at{"r2", h}] != want {
		t.Errorf("r2, which nobody switched on, holds %+v, want %+v: every router is recorded", got[at{"r2", h}], want)
	}
	if len(got) != 3 {
		t.Errorf("rows %v, want three", got)
	}
	if len(r.take()) != 0 {
		t.Error("a second take returned the same bytes again")
	}

	// Switched off between a minute and the flush: nothing is written.
	r.add("r1", appMinute(h+hourMs+60_000, nf(8, 0)))
	r.setOn(false)
	if rows := r.take(); len(rows) != 0 {
		t.Errorf("the feature was switched off and %v was still written", rows)
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
	if b, _ := json.Marshal(buildAppHistory(nil, week, 0, cat)); !strings.Contains(string(b), `"apps":[]`) {
		t.Errorf("an empty history sends a null list: %s", b)
	}
}

const appHistoryGateDDL = `
CREATE TABLE app_usage (router_id TEXT NOT NULL, app TEXT NOT NULL, hour INTEGER NOT NULL,
  rx_bytes INTEGER NOT NULL DEFAULT 0, tx_bytes INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (router_id, app, hour));`

// TestTheAppHistoryFollowsItsGates. Reading the history needs the feature, a
// signed-in user and the Apps Usage page on that router. The feature is on by
// default, so settings that never mention it read it as on; it is off only
// when switched off.
func TestTheAppHistoryFollowsItsGates(t *testing.T) {
	grant := appHistoryGateDDL + `
INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','apps','write'), ('role-r','apps','read');`
	off := `{"appHistoryEnabled":false}`
	for _, c := range []struct {
		name, settings, extra, url string
		signedIn                   bool
		want                       int
	}{
		{"signed out", `{}`, grant, "/api/apps/history?routerId=r-A&period=week", false, 401},
		{"switched off", off, grant, "/api/apps/history?routerId=r-A&period=week", true, 403},
		{"bad router id", `{}`, grant, "/api/apps/history?routerId=../x&period=week", true, 400},
		{"no Apps Usage grant", `{}`, appHistoryGateDDL, "/api/apps/history?routerId=r-A&period=week", true, 403},
		{"granted, on by default", `{}`, grant, "/api/apps/history?routerId=r-B&period=week", true, 200},
	} {
		s := gateServer(t, c.settings, c.extra)
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
		}
	}
}

// TestEveryRouterIsHeldForAppHistoryByDefault. No opt-in: with settings that
// never mention the feature, every router is held for it and the recorder
// keeps what it is given; switched off, no router is held and nothing is kept.
// Without a database there is nowhere to record, so nothing is held either.
func TestEveryRouterIsHeldForAppHistoryByDefault(t *testing.T) {
	minute := appMinute(0, map[string]collect.AppBytes{"netflix": {Rx: 1000}})
	for _, c := range []struct {
		name, settings string
		want           bool
	}{
		{"settings that never mention it", `{}`, true},
		{"switched on", `{"appHistoryEnabled":true}`, true},
		{"switched off", `{"appHistoryEnabled":false}`, false},
	} {
		s := gateServer(t, c.settings, appHistoryGateDDL)
		for _, rid := range []string{"r-A", "r-B", "a-router-nobody-chose"} {
			if got := s.featureHoldWanted(store.FeatureAppHistory, rid); got != c.want {
				t.Errorf("%s: %s held for app history = %v, want %v", c.name, rid, got, c.want)
			}
		}
		s.appUsage = newAppUsageRecorder()
		s.syncFeatureHolds()
		s.appUsage.add("r-A", minute)
		if got := len(s.appUsage.take()) == 1; got != c.want {
			t.Errorf("%s: the recorder kept a minute = %v, want %v", c.name, got, c.want)
		}
	}
	s := gateServer(t, `{}`, appHistoryGateDDL)
	s.auditDB = nil
	if s.featureHoldWanted(store.FeatureAppHistory, "r-A") {
		t.Error("a server with no database holds routers for a history it cannot write")
	}
}

// TestAppUsageRoundsToWholeBytes guards the one float-to-int step: a sum is
// rounded, not truncated.
func TestAppUsageRoundsToWholeBytes(t *testing.T) {
	r := newAppUsageRecorder()
	r.setOn(true)
	r.add("r1", appMinute(0, map[string]collect.AppBytes{"x": {Rx: 0.00999}}))
	r.add("r1", appMinute(60_000, map[string]collect.AppBytes{"x": {Rx: 0.9375}}))
	rows := r.take()
	if len(rows) != 1 || rows[0].RxBytes != int64(math.Round(0.00999+0.9375)) {
		t.Errorf("rows %+v, want 1 byte", rows)
	}
}
