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
)

// TestAReadingIsHeldForTheTimeSinceTheLastOne. One reading's rate covers the
// time since the router's previous reading, so 8 Mbps for 5 s is 5 MB, and
// both directions are converted on their own.
func TestAReadingIsHeldForTheTimeSinceTheLastOne(t *testing.T) {
	if got := appReadingSeconds(10_000, 15_000, 5000); got != 5 {
		t.Errorf("a reading 5 s after the last is held for %v s, want 5", got)
	}
	if got := mbpsToBytes(8, 5); got != 5_000_000 {
		t.Errorf("8 Mbps for 5 s is %v bytes, want 5,000,000", got)
	}
	if got := mbpsToBytes(-1, 5); got != 0 {
		t.Errorf("a negative rate made %v bytes", got)
	}
	for _, c := range []struct {
		name     string
		prev, ts int64
		pollMs   int
		want     float64
	}{
		{"the first reading of a router", 0, 15_000, 5000, 0},
		{"a clock that went backwards", 20_000, 15_000, 5000, 0},
		{"a reading after a gap is capped at two polls", 10_000, 310_000, 5000, 10},
		{"exactly two polls is not cut", 10_000, 20_000, 5000, 10},
		{"no poll period reads as the 5 s default", 10_000, 110_000, 0, 10},
	} {
		if got := appReadingSeconds(c.prev, c.ts, c.pollMs); got != c.want {
			t.Errorf("%s: held for %v s, want %v", c.name, got, c.want)
		}
	}
}

func appReading(ts int64, apps ...collect.AppUsage) *collect.AppUsagePayload {
	return &collect.AppUsagePayload{TS: ts, PollMs: 5000, Apps: apps}
}

// TestTheRecorderSumsOnlyTheRoutersItRecords. Readings of a router that is not
// recorded are not kept, the first reading after recording starts is not held
// for the time before it, bytes go to the hour the reading falls in, and a gap
// adds no more than two polls' worth.
func TestTheRecorderSumsOnlyTheRoutersItRecords(t *testing.T) {
	r := newAppUsageRecorder()
	const h = 1_800_000_000_000 - 1_800_000_000_000%hourMs
	nf := func(rx, tx float64) collect.AppUsage { return collect.AppUsage{ID: "netflix", RxMbps: rx, TxMbps: tx} }

	r.add("r1", appReading(h+1000, nf(8, 8)))
	r.add("r2", appReading(h+1000, nf(8, 8)))
	r.setOn(map[string]bool{"r1": true})
	r.add("r1", appReading(h+6000, nf(8, 0.8)))   // 5 s
	r.add("r2", appReading(h+6000, nf(8, 0.8)))   // not recorded
	r.add("r1", appReading(h+606_000, nf(16, 0))) // 600 s gap: held for 10 s
	r.add("r1", appReading(h+hourMs+1000, nf(8, 0)))

	rows := r.take()
	got := map[int64]db.AppUsageRow{}
	for _, row := range rows {
		if row.RouterID != "r1" || row.App != "netflix" {
			t.Fatalf("a row for %s/%s; only r1 is recorded: %+v", row.RouterID, row.App, rows)
		}
		got[row.Hour] = row
	}
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 5_000_000 + 20_000_000, TxBytes: 500_000}); got[h] != want {
		t.Errorf("the first hour holds %+v, want %+v", got[h], want)
	}
	// The next hour's reading came 3,395 s after the last: two polls, 10 s.
	if want := (db.AppUsageRow{RouterID: "r1", App: "netflix", Hour: h + hourMs, RxBytes: 10_000_000}); got[h+hourMs] != want {
		t.Errorf("the second hour holds %+v, want %+v", got[h+hourMs], want)
	}
	if len(r.take()) != 0 {
		t.Error("a second take returned the same bytes again")
	}

	// Switched off between readings and the flush: nothing is written for it.
	r.add("r1", appReading(h+hourMs+6000, nf(8, 0)))
	r.setOn(map[string]bool{})
	if rows := r.take(); len(rows) != 0 {
		t.Errorf("a router switched off still had %v written", rows)
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
  rx_bytes INTEGER NOT NULL DEFAULT 0, tx_bytes INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (router_id, app, hour));
CREATE TABLE app_history_routers (router_id TEXT PRIMARY KEY);`

// TestTheAppHistoryChoiceFollowsItsGates. Asking whether a router is recorded,
// switching it and reading it need the feature, a signed-in user and the Apps
// Usage page on that router (write to switch), before anything is read or
// written; with them, the switch is saved and read back.
func TestTheAppHistoryChoiceFollowsItsGates(t *testing.T) {
	grant := appHistoryGateDDL + `
INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','apps','write'), ('role-r','apps','read');`
	on := `{"appHistoryEnabled":true}`
	for _, c := range []struct {
		name, settings, extra, method, url, body string
		signedIn                                 bool
		want                                     int
	}{
		{"status, signed out", on, grant, "GET", "/api/apps/history/status?routerId=r-A", "", false, 401},
		{"status, switched off", `{}`, grant, "GET", "/api/apps/history/status?routerId=r-A", "", true, 403},
		{"status, the Wi-Fi feature is not this one", `{"wifiHistoryEnabled":true}`, grant, "GET", "/api/apps/history/status?routerId=r-A", "", true, 403},
		{"status, bad router id", on, grant, "GET", "/api/apps/history/status?routerId=../x", "", true, 400},
		{"status, no Apps Usage grant", on, appHistoryGateDDL, "GET", "/api/apps/history/status?routerId=r-A", "", true, 403},
		{"status, granted", on, grant, "GET", "/api/apps/history/status?routerId=r-A", "", true, 200},
		{"history, no Apps Usage grant", on, appHistoryGateDDL, "GET", "/api/apps/history?routerId=r-A&period=week", "", true, 403},
		{"history, granted", on, grant, "GET", "/api/apps/history?routerId=r-B&period=week", "", true, 200},
		{"record, signed out", on, grant, "POST", "/api/apps/history/record", `{"routerId":"r-A","on":true}`, false, 401},
		{"record, switched off", `{}`, grant, "POST", "/api/apps/history/record", `{"routerId":"r-A","on":true}`, true, 403},
		{"record, read only on the router", on, grant, "POST", "/api/apps/history/record", `{"routerId":"r-B","on":true}`, true, 403},
		{"record, no Apps Usage grant", on, appHistoryGateDDL, "POST", "/api/apps/history/record", `{"routerId":"r-A","on":true}`, true, 403},
		{"record, granted", on, grant, "POST", "/api/apps/history/record", `{"routerId":"r-A","on":true}`, true, 200},
	} {
		s := gateServer(t, c.settings, c.extra)
		req := httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
		if c.signedIn {
			req.Header.Set("Cookie", authed)
		}
		w := httptest.NewRecorder()
		h := s.appHistoryStatus
		if c.method == "POST" {
			h = s.appHistoryRecord
		} else if strings.HasPrefix(c.url, "/api/apps/history?") {
			h = s.appHistoryGet
		}
		h(w, req)
		if w.Code != c.want {
			t.Errorf("%s: answered %d (%s), want %d", c.name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
		recorded, _ := s.auditDB.AppHistoryRouters()
		if c.want != 200 && len(recorded) != 0 {
			t.Errorf("%s: a refused request switched recording on: %v", c.name, recorded)
		}
		if c.name == "record, granted" && !recorded["r-A"] {
			t.Errorf("%s: answered %d and r-A is not recorded: %v", c.name, w.Code, recorded)
		}
		if c.name == "history, granted" {
			var got AppHistory
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Buckets) != 7 || got.Apps == nil {
				t.Errorf("%s: %v %s", c.name, err, w.Body.String())
			}
		}
	}
}

// TestAppUsageRoundsToWholeBytes guards the one float-to-int step: a sum is
// rounded, not truncated.
func TestAppUsageRoundsToWholeBytes(t *testing.T) {
	r := newAppUsageRecorder()
	r.setOn(map[string]bool{"r1": true})
	r.add("r1", appReading(1000))
	r.add("r1", appReading(1001, collect.AppUsage{ID: "x", RxMbps: 0.0000799})) // 0.00999 bytes in 1 ms
	r.add("r1", appReading(2001, collect.AppUsage{ID: "x", RxMbps: 0.0000075})) // 0.9375 bytes in 1 s
	rows := r.take()
	if len(rows) != 1 || rows[0].RxBytes != int64(math.Round(0.00999+0.9375)) {
		t.Errorf("rows %+v, want 1 byte", rows)
	}
}
