package server

// The Apps Usage page's stored history, recorded for every router with
// Reporting on (the router's own setting, Devices -> edit -> Monitoring):
//
//	GET    /api/apps/history?routerId=&period=day|week|month   per-app totals
//
// Gated on the Apps Usage page for that router.
//
// ── REPORTING IS THE SWITCH ─────────────────────────────────────────────────
//
// Until 2026-11-12 this was a feature of its own in Settings -> Features, on by
// default for every router. The operator asked for it to follow Reporting
// instead, the setting that already decides whether a router's traffic, ping
// and uptime are written to disk: one switch per router for "keep this
// router's history". So the session's `history` hold runs the apps collector
// (session.historyFeeds) and the recorder below keeps the minutes of exactly
// the routers declareReporting says are on. The cost, per router with
// Reporting on: its connection table read continuously (the read Connections
// and Bandwidth share, so a router whose pages are open pays nothing extra)
// and its DNS cache once a minute.
//
// ── WHERE THE BYTES COME FROM ───────────────────────────────────────────────
//
// The apps collector already turns its readings into bytes per one-minute
// bucket for the page's last hour (internal/collect/apps.go). Each minute it
// closes is handed here (OnMinute) and added to its hour: the page and the
// history sum the same bytes, computed once. The sums are written once a
// minute from the feature loop, one transaction, added to what the hour
// already holds, so the current hour is on the page a minute after it starts.
//
// Only the app id and the byte counts are kept. No device, address or DNS name.
// The traffic no step could name is kept too, as one row per hour under
// apps.UnnamedID, so the Unnamed card has a figure on the stored tabs.

import (
	"log"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
)

const hourMs = int64(time.Hour / time.Millisecond)

type appUsageKey struct {
	router, app string
	hour        int64
}

type appUsageBytes struct{ rx, tx float64 }

// appUsageRecorder sums closed minutes into hours in memory until the next
// flush.
type appUsageRecorder struct {
	mu sync.Mutex
	// reporting is the routers whose minutes are kept: those with Reporting
	// on, as declareReporting last said.
	reporting map[string]bool
	acc       map[appUsageKey]*appUsageBytes
}

func newAppUsageRecorder() *appUsageRecorder {
	return &appUsageRecorder{reporting: map[string]bool{}, acc: map[appUsageKey]*appUsageBytes{}}
}

// add takes one closed minute of one router. Called on the collector's
// goroutine, so it does arithmetic and nothing else.
func (r *appUsageRecorder) add(routerID string, m collect.AppMinute) {
	if r == nil || routerID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.reporting[routerID] {
		return
	}
	hour := m.Start - m.Start%hourMs
	put := func(id string, b collect.AppBytes) {
		if b.Rx <= 0 && b.Tx <= 0 {
			return
		}
		k := appUsageKey{routerID, id, hour}
		a := r.acc[k]
		if a == nil {
			a = &appUsageBytes{}
			r.acc[k] = a
		}
		a.rx += max(b.Rx, 0)
		a.tx += max(b.Tx, 0)
	}
	for id, b := range m.Apps {
		put(id, b)
	}
	put(apps.UnnamedID, m.Unnamed)
}

// setReporting says whether a router's minutes are kept. Off, what was summed
// for it since the last flush is dropped, as Reporting off writes nothing.
func (r *appUsageRecorder) setReporting(routerID string, on bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		r.reporting[routerID] = true
		return
	}
	delete(r.reporting, routerID)
	for k := range r.acc {
		if k.router == routerID {
			delete(r.acc, k)
		}
	}
}

// recording is whether a router's app usage is being kept.
func (r *appUsageRecorder) recording(routerID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reporting[routerID]
}

// take empties the sums into rows.
func (r *appUsageRecorder) take() []db.AppUsageRow {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	acc := r.acc
	r.acc = map[appUsageKey]*appUsageBytes{}
	r.mu.Unlock()
	rows := make([]db.AppUsageRow, 0, len(acc))
	for k, b := range acc {
		rows = append(rows, db.AppUsageRow{RouterID: k.router, App: k.app, Hour: k.hour,
			RxBytes: int64(math.Round(b.rx)), TxBytes: int64(math.Round(b.tx))})
	}
	return rows
}

// flushAppUsage writes what the recorder summed since the last flush. Called
// once a minute by the feature loop. Without a database the sums are dropped,
// so they cannot pile up.
func (s *Server) flushAppUsage() {
	rows := s.appUsage.take()
	if s.auditDB == nil {
		return
	}
	if err := s.auditDB.AddAppUsage(rows); err != nil {
		log.Printf("[app-history] %v", err)
	}
}

// ── the API ─────────────────────────────────────────────────────────────────

func (s *Server) registerAppHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps/history", s.appHistoryGet)
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
// app's bytes, both directions, and the unnamed bytes beside them.
type AppHistoryBucket struct {
	TS      int64            `json:"ts"`
	Apps    map[string]int64 `json:"apps"`
	Unnamed int64            `json:"unnamed"`
}

// AppHistory is what GET /api/apps/history answers.
type AppHistory struct {
	Apps    []AppHistoryApp    `json:"apps"`
	Buckets []AppHistoryBucket `json:"buckets"`
	// The traffic no step could name over the period. Recorded since
	// 2026-11-12: an hour stored before then counts none.
	UnnamedRxBytes int64 `json:"unnamedRxBytes"`
	UnnamedTxBytes int64 `json:"unnamedTxBytes"`
	From           int64 `json:"from"`
	To             int64 `json:"to"`
	// Recording is whether this router's app usage is being kept now, which
	// is its Reporting setting. Off, the page says so rather than showing an
	// empty period as if nothing had been used.
	Recording bool `json:"recording"`
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
		if r.App == apps.UnnamedID {
			out.Buckets[i].Unnamed += r.RxBytes + r.TxBytes
			out.UnnamedRxBytes += r.RxBytes
			out.UnnamedTxBytes += r.TxBytes
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
	if s.routerPageGate(w, r, "apps", rid, "read") == nil {
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
	h := buildAppHistory(rows, starts, to, apps.Default())
	h.Recording = s.appUsage.recording(rid)
	writeJSON(w, h)
}
