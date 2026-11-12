package server

// The Apps Usage page's stored history, an optional feature (Settings ->
// Features), modelled on the Wi-Fi client history in clients_api.go:
//
//	GET    /api/apps/history?routerId=&period=day|week|month   per-app totals
//	GET    /api/apps/history/status?routerId=                 whether it is recorded
//	POST   /api/apps/history/record                           switch it on or off
//
// Gated on the Apps Usage page for that router: read to look, write to switch.
//
// ── HOW A READING BECOMES BYTES ─────────────────────────────────────────────
//
// The apps collector reports rates, one reading per poll (collect.Apps,
// OnBuild). Each reading's rate is held for the time since that router's
// previous reading, capped at twice the poll period: a reading after a gap (a
// reconnect, a suspended collector, a stalled router) is not stretched across
// the gap, which would invent traffic nobody measured. The bytes go to the hour
// the reading falls in and are written once a minute from the feature loop, one
// transaction a router, added to what the hour already holds.
//
// Only the app id and the byte counts are kept. No device, address or DNS name.

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

const hourMs = int64(time.Hour / time.Millisecond)

// appReadingSeconds is how long one reading's rate is held for: the time since
// the previous reading, at most twice the poll period. Zero for the first
// reading of a router, and for a clock that went backwards.
func appReadingSeconds(prevTS, ts int64, pollMs int) float64 {
	if prevTS <= 0 || ts <= prevTS {
		return 0
	}
	if pollMs <= 0 {
		pollMs = 5000
	}
	dt := ts - prevTS
	if limit := int64(2 * pollMs); dt > limit {
		dt = limit
	}
	return float64(dt) / 1000
}

// mbpsToBytes is the bytes a rate in megabits a second moves in `seconds`.
func mbpsToBytes(mbps, seconds float64) float64 {
	if mbps <= 0 || seconds <= 0 {
		return 0
	}
	return mbps * 1e6 / 8 * seconds
}

type appUsageKey struct {
	router, app string
	hour        int64
}

type appUsageBytes struct{ rx, tx float64 }

// appUsageRecorder sums readings into hours in memory until the next flush.
type appUsageRecorder struct {
	mu sync.Mutex
	// on is the routers being recorded now: the feature on and the router
	// switched on. Refreshed by the feature loop and by the record switch.
	on map[string]bool
	// last is each router's previous reading, recorded or not, so the first
	// reading after recording starts is not held for the time before it.
	last map[string]int64
	acc  map[appUsageKey]*appUsageBytes
}

func newAppUsageRecorder() *appUsageRecorder {
	return &appUsageRecorder{on: map[string]bool{}, last: map[string]int64{}, acc: map[appUsageKey]*appUsageBytes{}}
}

// add takes one reading of one router. Called on the collector's goroutine for
// every reading, so it does arithmetic and nothing else.
func (r *appUsageRecorder) add(routerID string, p *collect.AppUsagePayload) {
	if r == nil || p == nil || routerID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.last[routerID]
	r.last[routerID] = p.TS
	if !r.on[routerID] {
		return
	}
	secs := appReadingSeconds(prev, p.TS, p.PollMs)
	if secs == 0 {
		return
	}
	hour := p.TS - p.TS%hourMs
	for _, a := range p.Apps {
		rx, tx := mbpsToBytes(a.RxMbps, secs), mbpsToBytes(a.TxMbps, secs)
		if rx == 0 && tx == 0 {
			continue
		}
		k := appUsageKey{routerID, a.ID, hour}
		b := r.acc[k]
		if b == nil {
			b = &appUsageBytes{}
			r.acc[k] = b
		}
		b.rx += rx
		b.tx += tx
	}
}

// setOn replaces the set of routers being recorded.
func (r *appUsageRecorder) setOn(on map[string]bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.on = on
	r.mu.Unlock()
}

// take empties the sums into rows, keeping only routers still recorded.
func (r *appUsageRecorder) take() []db.AppUsageRow {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	acc := r.acc
	on := r.on
	r.acc = map[appUsageKey]*appUsageBytes{}
	r.mu.Unlock()
	rows := make([]db.AppUsageRow, 0, len(acc))
	for k, b := range acc {
		if !on[k.router] {
			continue
		}
		rows = append(rows, db.AppUsageRow{RouterID: k.router, App: k.app, Hour: k.hour,
			RxBytes: int64(math.Round(b.rx)), TxBytes: int64(math.Round(b.tx))})
	}
	return rows
}

// refreshAppRecording tells the recorder which routers it records: none while
// the feature is off.
func (s *Server) refreshAppRecording() {
	if s.appUsage == nil {
		return
	}
	if s.auditDB == nil || !s.featureOn(store.FeatureAppHistory) {
		s.appUsage.setOn(map[string]bool{})
		return
	}
	on, err := s.auditDB.AppHistoryRouters()
	if err != nil {
		log.Printf("[app-history] %v", err)
		return
	}
	s.appUsage.setOn(on)
}

// flushAppUsage writes what the recorder summed since the last flush. Called
// once a minute by the feature loop.
func (s *Server) flushAppUsage() {
	s.refreshAppRecording()
	if s.auditDB == nil {
		return
	}
	if err := s.auditDB.AddAppUsage(s.appUsage.take()); err != nil {
		log.Printf("[app-history] %v", err)
	}
}

// ── the API ─────────────────────────────────────────────────────────────────

func (s *Server) registerAppHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps/history", s.appHistoryGet)
	mux.HandleFunc("GET /api/apps/history/status", s.appHistoryStatus)
	mux.HandleFunc("POST /api/apps/history/record", s.appHistoryRecord)
}

// AppHistoryApp is one application's total over the period.
type AppHistoryApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Logo     string `json:"logo"`
	Dark     bool   `json:"dark"`
	RxBytes  int64  `json:"rxBytes"`
	TxBytes  int64  `json:"txBytes"`
}

// AppHistoryBucket is one hour (period day) or one day (week, month): each
// app's bytes, both directions.
type AppHistoryBucket struct {
	TS   int64            `json:"ts"`
	Apps map[string]int64 `json:"apps"`
}

// AppHistory is what GET /api/apps/history answers.
type AppHistory struct {
	Apps    []AppHistoryApp    `json:"apps"`
	Buckets []AppHistoryBucket `json:"buckets"`
	From    int64              `json:"from"`
	To      int64              `json:"to"`
}

// appHistoryBuckets is where each period's buckets start: the last 24 hours by
// the hour, or the last 7 or 30 days by the day in the install's display
// timezone, the current one last. Pure.
func appHistoryBuckets(period string, now time.Time, loc *time.Location) []int64 {
	var out []int64
	switch period {
	case "week", "month":
		n := 7
		if period == "month" {
			n = 30
		}
		t := now.In(loc)
		today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		for i := n - 1; i >= 0; i-- {
			out = append(out, today.AddDate(0, 0, -i).UnixMilli())
		}
	default:
		h := now.UnixMilli() - now.UnixMilli()%hourMs
		for i := int64(23); i >= 0; i-- {
			out = append(out, h-i*hourMs)
		}
	}
	return out
}

// buildAppHistory sums hourly rows into the buckets and the per-app totals,
// busiest app first. Pure apart from the catalogue it names apps from.
func buildAppHistory(rows []db.AppUsageRow, starts []int64, to int64, cat *apps.Catalogue) AppHistory {
	out := AppHistory{Apps: []AppHistoryApp{}, Buckets: make([]AppHistoryBucket, len(starts)), To: to}
	if len(starts) > 0 {
		out.From = starts[0]
	}
	for i, ts := range starts {
		out.Buckets[i] = AppHistoryBucket{TS: ts, Apps: map[string]int64{}}
	}
	totals := map[string]*AppHistoryApp{}
	for _, r := range rows {
		// The bucket an hour belongs to is the last one starting at or before it.
		i := sort.Search(len(starts), func(i int) bool { return starts[i] > r.Hour }) - 1
		if i < 0 || r.Hour > to {
			continue
		}
		out.Buckets[i].Apps[r.App] += r.RxBytes + r.TxBytes
		a := totals[r.App]
		if a == nil {
			a = &AppHistoryApp{ID: r.App, Name: r.App, Category: "other"}
			if cat != nil {
				if app, ok := cat.App(r.App); ok {
					a.Name, a.Category, a.Color, a.Logo, a.Dark = app.Name, app.Category, app.Color, app.Logo, app.Dark
				}
			}
			totals[r.App] = a
		}
		a.RxBytes += r.RxBytes
		a.TxBytes += r.TxBytes
	}
	for _, a := range totals {
		out.Apps = append(out.Apps, *a)
	}
	sort.Slice(out.Apps, func(i, j int) bool {
		x, y := out.Apps[i], out.Apps[j]
		if x.RxBytes+x.TxBytes != y.RxBytes+y.TxBytes {
			return x.RxBytes+x.TxBytes > y.RxBytes+y.TxBytes
		}
		return x.Name < y.Name
	})
	return out
}

func (s *Server) appHistoryGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rid := q.Get("routerId")
	if s.routerFeatureGate(w, r, store.FeatureAppHistory, "apps", rid, "read") == nil {
		return
	}
	loc, err := time.LoadLocation(s.displayTZ())
	if err != nil {
		loc = time.UTC
	}
	now := time.Now()
	starts := appHistoryBuckets(q.Get("period"), now, loc)
	to := now.UnixMilli()
	rows, err := s.auditDB.AppUsage(rid, starts[0], to)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the history could not be read")
		return
	}
	writeJSON(w, buildAppHistory(rows, starts, to, apps.Default()))
}

func (s *Server) appHistoryStatus(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerFeatureGate(w, r, store.FeatureAppHistory, "apps", rid, "read") == nil {
		return
	}
	on, err := s.auditDB.AppHistoryRouters()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the setting could not be read")
		return
	}
	writeJSON(w, map[string]any{"recording": on[rid]})
}

func (s *Server) appHistoryRecord(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RouterID string `json:"routerId"`
		On       bool   `json:"on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON400OK(w)
		return
	}
	sess := s.routerFeatureGate(w, r, store.FeatureAppHistory, "apps", in.RouterID, "write")
	if sess == nil {
		return
	}
	if err := s.auditDB.SetAppHistory(in.RouterID, in.On); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the setting could not be saved")
		return
	}
	action := "apps.history.off"
	if in.On {
		action = "apps.history.on"
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: action, TargetType: "apps-history", RouterID: in.RouterID})
	s.refreshAppRecording()
	s.syncFeatureHolds()
	writeJSON(w, map[string]any{"ok": true, "recording": in.On})
}
