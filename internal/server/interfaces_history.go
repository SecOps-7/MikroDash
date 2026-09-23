package server

// The per-interface traffic history the Interfaces modal draws (#59).
//
// ── WHY THIS IS NOT A REPORTS ENDPOINT ──────────────────────────────────────
//
// `/api/reports/*` answers the same questions from the same tables, and reusing
// it would have been less code. It is gated on the REPORTS page grant, and this
// panel opens from a row on the Interfaces page: an operator who may look at an
// interface would have had to hold a second page to see that same interface's
// traffic an hour ago.
//
// So the gate is INTERFACES READ, decided by the operator on 2026-09-23. The
// justification is that this is the data the page already shows live, only
// older — not a new disclosure. The Reports endpoints keep their own gate; what
// separates them is the range, the export and the scheduling, not the rows.
//
// ── THE RECORDING SWITCH IS NOT WRITTEN HERE ────────────────────────────────
//
// Turning recording on for an interface changes the ROUTER RECORD, and that is
// `PUT /api/routers/{id}`, gated on managing that router. This endpoint only
// reports whether the caller may do it (`mayRecord`) and what the list holds, so
// the panel can offer the control to somebody it will work for rather than
// offering it to everyone and letting the server refuse.

import (
	"log"
	"net/http"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/routers"
	"mikrodash/internal/store"
)

// ifaceHistoryRanges is what the panel offers, and the aggregation each is
// drawn at.
//
// THE AGGREGATION IS CHOSEN HERE RATHER THAN BY THE BROWSER, because it is a
// statement about how many points a panel that size can show: an hour is 60
// minute rows and needs none, while thirty days by hour would be 720 points a
// few hundred pixels wide. A browser picking its own aggregation would be
// asking the database a question about layout.
var ifaceHistoryRanges = map[string]struct {
	Span time.Duration
	Agg  string
}{
	"1h":  {time.Hour, ""},
	"24h": {24 * time.Hour, "hour"},
	"7d":  {7 * 24 * time.Hour, "hour"},
	"30d": {30 * 24 * time.Hour, "day"},
}

// ifaceHistoryReply is the whole panel in one response: the series, what it is
// in, and whether this interface is being recorded at all.
type ifaceHistoryReply struct {
	OK   bool               `json:"ok"`
	Rows []db.TrafficSample `json:"rows"`
	// Resolution is "minute" or "hour" — which table answered. The panel names
	// its peak from it, for the reason `db.Resolution` gives.
	Resolution string `json:"resolution"`
	// Recorded says whether this interface is one the router keeps history for.
	// FALSE IS THE INTERESTING CASE: it is why a chart is empty, and without it
	// the panel would show "no data" for an interface that is merely not
	// switched on, which reads as a fault.
	Recorded bool `json:"recorded"`
	// MayRecord is whether the CALLER may change that, which is a router-manage
	// question rather than an interfaces one.
	MayRecord bool `json:"mayRecord"`
	// RecordedIfaces is the STORED list, sent only to somebody who may write it:
	// the switch replaces the whole array, so the panel needs what is in it.
	//
	// STORED, NOT RESOLVED, and the difference matters. The resolved set always
	// contains the default interface whether or not the array names it, so
	// sending that would make the switch write today's WAN into the stored list
	// as a side effect of recording something else — pinning it there after the
	// operator later points `defaultIf` somewhere new. The WAN is recorded
	// because it is the WAN, and it should stay implicit.
	RecordedIfaces []string `json:"recordedIfaces"`
	// DefaultIf is recorded always and cannot be switched off here — it is the
	// WAN every report and capacity line reads.
	DefaultIf string   `json:"defaultIf"`
	RxTotalMb float64  `json:"rxTotalMb"`
	TxTotalMb float64  `json:"txTotalMb"`
	RxMaxMbps *float64 `json:"rxMaxMbps"`
	TxMaxMbps *float64 `json:"txMaxMbps"`
}

func (s *Server) registerInterfaceHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/interfaces/history", s.interfaceHistory)
}

func (s *Server) interfaceHistory(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	q := r.URL.Query()
	routerID, iface := q.Get("routerId"), q.Get("interface")
	if routerID == "" || iface == "" {
		writeJSONErr(w, http.StatusBadRequest, "routerId and interface required")
		return
	}
	if !s.mayReadInterfaces(sess, routerID) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}
	if s.auditDB == nil {
		// An empty series would read as "this interface was idle" rather than
		// "there is no history to read", which is the distinction this panel
		// exists to draw.
		writeJSONErr(w, http.StatusServiceUnavailable, "history unavailable")
		return
	}
	rng, ok := ifaceHistoryRanges[q.Get("range")]
	if !ok {
		// NOT a default. An unknown range is the caller asking for something
		// that does not exist, and answering with an hour would draw a chart
		// labelled with the range they asked for and filled with another.
		writeJSONErr(w, http.StatusBadRequest, "unknown range")
		return
	}

	now := time.Now()
	from, to := now.Add(-rng.Span).UnixMilli(), now.UnixMilli()

	var rows []db.TrafficSample
	if rng.Agg != "" {
		rows, err = s.auditDB.TrafficSamplesAgg(routerID, iface, from, to, rng.Agg)
	} else {
		rows, err = s.auditDB.TrafficSamples(routerID, iface, from, to)
	}
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []db.TrafficSample{}
	}
	t, err := s.auditDB.TrafficSummary(routerID, iface, from, to, 95)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	b, err := s.auditDB.BandwidthSummary(routerID, iface, from, to)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	rec, stored, defaultIf := s.recordedFor(routerID)
	out := ifaceHistoryReply{
		OK: true, Rows: rows, Resolution: db.Resolution(from, to),
		DefaultIf: defaultIf,
		RxTotalMb: b.RxTotalMb, TxTotalMb: b.TxTotalMb,
		RxMaxMbps: t.RxMaxMbps, TxMaxMbps: t.TxMaxMbps,
	}
	for _, n := range rec {
		if n == iface {
			out.Recorded = true
		}
	}
	if s.mayManageRouter(sess, routerID) {
		out.MayRecord = true
		out.RecordedIfaces = stored
		if out.RecordedIfaces == nil {
			out.RecordedIfaces = []string{}
		}
	}
	writeJSON(w, out)
}

// recordedFor returns what is actually recorded, what is STORED, and the
// default interface — three answers because the first two differ and each has
// a job.
//
// THE RESOLVED SET (`store.RecordedIfacesFor`, the same resolver the recorder
// uses) answers "is this interface recorded": the default is always recorded
// whether or not the stored list names it, so reading the raw array would tell
// an operator the WAN is not being recorded while its rows arrive every minute.
//
// THE STORED LIST is what a write replaces, so it is what the switch extends.
func (s *Server) recordedFor(routerID string) (resolved, stored []string, defaultIf string) {
	if s.store == nil {
		return nil, nil, ""
	}
	global := ""
	if cfg, err := s.store.Settings(); err == nil {
		if v, ok := cfg["defaultIf"].(string); ok {
			global = v
		}
	}
	rs, _ := s.store.Routers()
	for _, r := range rs {
		if r.ID == routerID {
			def := routers.DefaultIfFor(r.DefaultIf, global)
			return store.RecordedIfacesFor(r, def), r.RecordedIfaces, def
		}
	}
	return nil, nil, ""
}

// mayReadInterfaces is the Interfaces page's read grant, in the same two-gate
// shape `mayReadReports` uses — see that function for why an unavailable RBAC
// is a documented gap rather than a refusal.
func (s *Server) mayReadInterfaces(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if !sess.CanPage("interfaces", "read", routerID) {
		return false
	}
	if !s.rbac.Available() {
		return true
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "interfaces", "read", routerID)
	if err != nil {
		log.Printf("[rbac] read interface history on %s: %v", routerID, err)
		return false
	}
	return ok
}
