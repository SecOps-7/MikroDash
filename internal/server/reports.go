package server

// The report read endpoints: ping, traffic, bandwidth, alerts and connectivity.
//
// ── THEY LIVE UNDER /next/, NOT /api/ ───────────────────────────────────────
//
// This is the first HTTP JSON API in the Go server — every other ported page is
// fed over the WebSocket, and every /api/* path still proxies to Node. Reports
// is request/response rather than streamed, so it needs one.
//
// Registering them at `/api/reports/*` would work and would be WRONG for now:
// the LIVE Reports page is still served by Node through this proxy, so those
// routes would silently move a page nobody has ported onto an implementation
// nobody has compared. The strangler rule is that a page and its endpoints cut
// over together. These sit at `/next/api/reports/*` until the page lands, at
// which point the prefix comes off in one commit that can be reverted in one.
//
// ── THE GATE IS THE reports PAGE GRANT, AND THAT IS NOT A SHORTCUT ──────────
//
// The live endpoints use `requirePerm('router:history')`. This port has no
// permission vocabulary and does not need one HERE: `router:history` is
// conferred by exactly one thing in rbac.js —
//
//	const READ_CONFERS = { reports: ['router:history'] };
//
// — added for every rolePages row on the reports page regardless of access
// level, plus everything a builtin Administrator holds. Nothing else in the live
// source confers it: rbac.js mentions it only in the PERMISSIONS list, in that
// table, and in one projection for the router list. `can()` and `canPage()` also
// walk scope through the same `_roleSetsInScope`, which that file says is
// factored precisely "so can() and canPage() cannot drift apart".
//
// So "holds reports at read or better on this router" IS "has router:history",
// and canPage answers it with machinery already in place. If a future release
// confers router:history from somewhere else, this comment is where the
// equivalence was claimed and where it stops being true.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/reportpdf"
	"mikrodash/internal/reports"
)

// reportsPrefix is where these hang until the page cuts over.
const reportsPrefix = "/api/reports/"

// registerReports adds the read endpoints to a mux.
func (s *Server) registerReports(mux *http.ServeMux) {
	mux.HandleFunc(reportsPrefix+"ping", s.reportHandler(s.reportPing))
	mux.HandleFunc(reportsPrefix+"traffic", s.reportHandler(s.reportTraffic))
	mux.HandleFunc(reportsPrefix+"bandwidth", s.reportHandler(s.reportBandwidth))
	mux.HandleFunc(reportsPrefix+"alerts", s.reportHandler(s.reportAlerts))
	mux.HandleFunc(reportsPrefix+"connectivity", s.reportHandler(s.reportConnectivity))
	mux.HandleFunc(reportsPrefix+"schedules", s.reportHandler(s.reportSchedules))
	// The run history. `{id}` is a ServeMux wildcard, which keeps the URL the
	// same shape the live app uses — one less thing for the page to special-case
	// when these endpoints eventually move to /api/.
	mux.HandleFunc(reportsPrefix+"schedules/{id}/runs", s.reportHandler(s.reportScheduleRuns))
	// The five exports are registered by NAME rather than as `{kind}/export`.
	//
	// A consequence worth knowing: an UNKNOWN report type is not a 404 from here
	// — `/next/api/reports/nonsense/export` matches no export route and falls
	// through to the SPA catch-all, which answers 200 with index.html like any
	// other unknown /next/ path. Nothing constructs such a URL, and special-casing
	// it would mean one route existing only to say no.
	// A wildcard there conflicts with `schedules/{id}` — both match
	// `/schedules/export` and neither is more specific, so ServeMux panics at
	// startup. It is right to panic; the fix is to be explicit.
	for kind := range reports.ExportSpecs {
		mux.HandleFunc(reportsPrefix+kind+"/export", s.reportExportFor(kind))
	}
	// The writes. Method-qualified patterns, so a GET to the create path is a 405
	// from ServeMux rather than something this code has to think about.
	//
	// ONE limiter shared by all three, not one each — `_scheduleLimiter` is a
	// single middleware instance in the live app, so its 30 a minute is a budget
	// across create, update and delete together rather than 30 of each.
	lim := newRateLimiter(30, time.Minute).limit
	mux.HandleFunc("POST "+reportsPrefix+"schedules", lim(s.scheduleWriteHandler(false, s.reportScheduleCreate)))
	mux.HandleFunc("PUT "+reportsPrefix+"schedules/{id}", lim(s.scheduleWriteHandler(true, s.reportScheduleUpdate)))
	mux.HandleFunc("DELETE "+reportsPrefix+"schedules/{id}", lim(s.scheduleWriteHandler(true, s.reportScheduleDelete)))

	// "Send now" gets its OWN limiter at FIVE a minute, not the thirty the other
	// three share, and the difference is deliberate on the live side
	// (`_sendNowLimiter` is a separate middleware instance).
	//
	// The other three routes write a row. This one connects to a router, renders
	// up to five PDFs and hands a message to an SMTP server — so thirty a minute
	// is thirty outbound emails a minute to a stored recipient list, from one
	// button. A separate instance also means a burst of "Send now" cannot exhaust
	// the budget for editing a schedule, which is what a shared limiter would do.
	mux.HandleFunc("POST "+reportsPrefix+"schedules/{id}/run",
		newRateLimiter(5, time.Minute).limit(s.scheduleWriteHandler(true, s.reportScheduleRun)))
}

// reportReq is one authorised request: the range, and which interface.
type reportReq struct {
	reports.Params
	// Iface is the `interface` query parameter. Empty means "list the interfaces
	// instead", which is how the live traffic and bandwidth endpoints answer a
	// request that names none.
	Iface string
	// Sess is the validated session. Carried on the request because one endpoint
	// answers a question ABOUT the caller's permissions — the schedules list says
	// whether they may create one — rather than merely being gated by them.
	Sess *Session
}

// reportHandler wraps an endpoint in the checks all five share: a valid session,
// a routerId, and the reports grant on that router.
//
// The ORDER is the live app's. A missing routerId is a 400 before any
// authorization question is asked, because "which router?" has no answer to
// authorize; an unauthorized one is a 403 that says nothing about whether the id
// exists.
func (s *Server) reportHandler(fn func(http.ResponseWriter, *http.Request, reportReq)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		q := r.URL.Query()
		p := reports.ParseParams(q.Get("routerId"), q.Get("from"), q.Get("to"),
			q.Get("aggregate"), time.Now())
		if p.RouterID == "" {
			writeJSONErr(w, http.StatusBadRequest, "routerId required")
			return
		}
		if !s.mayReadReports(sess, p.RouterID) {
			writeJSONErr(w, http.StatusForbidden, "Not permitted")
			return
		}
		if s.auditDB == nil {
			// Every one of these reads the shared database. Without it there is
			// nothing to answer with, and an empty series would read as "this
			// router was quiet" rather than "the trail is unavailable".
			writeJSONErr(w, http.StatusServiceUnavailable, "history unavailable")
			return
		}
		fn(w, r, reportReq{Params: p, Iface: q.Get("interface"), Sess: sess})
	}
}

// mayReadReports is canPage's two-gate check, from an HTTP session rather than a
// socket. See the file header for why the page is "reports".
func (s *Server) mayReadReports(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if !sess.CanPage("reports", "read", routerID) {
		return false
	}
	if !s.rbac.Available() {
		return true // the documented gap, reported at startup
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "reports", "read", routerID)
	if err != nil {
		// An authorization question that cannot be answered is refused.
		log.Printf("[rbac] read reports on %s: %v", routerID, err)
		return false
	}
	return ok
}

// mayWriteReports is the same two-gate check at WRITE level, which is what
// `router:schedule` projects to. See the Scheduled-reports section below for why
// the two questions coincide.
func (s *Server) mayWriteReports(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if !sess.CanPage("reports", "write", routerID) {
		return false
	}
	if !s.rbac.Available() {
		return true // the documented gap, reported at startup
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "reports", "write", routerID)
	if err != nil {
		log.Printf("[rbac] write reports on %s: %v", routerID, err)
		return false
	}
	return ok
}

// ── the five endpoints ──────────────────────────────────────────────────────

func (s *Server) reportPing(w http.ResponseWriter, _ *http.Request, q reportReq) {
	var rows any
	var err error
	if q.Aggregate != "" {
		rows, err = s.auditDB.PingSamplesAgg(q.RouterID, q.From, q.To, q.Aggregate)
	} else {
		rows, err = s.auditDB.PingSamples(q.RouterID, q.From, q.To)
	}
	writeRows(w, rows, err)
}

// reportTraffic answers with SAMPLES when an interface is named and with the
// INTERFACE LIST when one is not. One endpoint doing two jobs is the live shape:
// the page calls it once to fill the picker and again to draw.
func (s *Server) reportTraffic(w http.ResponseWriter, _ *http.Request, q reportReq) {
	if q.Iface == "" {
		ifaces, err := s.auditDB.TrafficInterfaces(q.RouterID)
		if err != nil {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "interfaces": ifaces})
		return
	}
	var rows any
	var err error
	if q.Aggregate != "" {
		rows, err = s.auditDB.TrafficSamplesAgg(q.RouterID, q.Iface, q.From, q.To, q.Aggregate)
	} else {
		rows, err = s.auditDB.TrafficSamples(q.RouterID, q.Iface, q.From, q.To)
	}
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	summary, err := s.ifaceSummary(q.RouterID, q.Iface, q.From, q.To)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "rows": rows, "summary": summary})
}

func (s *Server) reportBandwidth(w http.ResponseWriter, _ *http.Request, q reportReq) {
	if q.Iface == "" {
		ifaces, err := s.auditDB.BandwidthInterfaces(q.RouterID)
		if err != nil {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "interfaces": ifaces})
		return
	}
	var rows any
	var err error
	if q.Aggregate != "" {
		rows, err = s.auditDB.BandwidthSamplesAgg(q.RouterID, q.Iface, q.From, q.To, q.Aggregate)
	} else {
		rows, err = s.auditDB.BandwidthSamples(q.RouterID, q.Iface, q.From, q.To)
	}
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	// THE SAME SUMMARY AS TRAFFIC, deliberately: `_ifaceSummary` merges the rate
	// and volume stats into one object and both endpoints send it whole, so the
	// two cards agree about a link no matter which chart is on screen.
	summary, err := s.ifaceSummary(q.RouterID, q.Iface, q.From, q.To)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "rows": rows, "summary": summary})
}

// reportAlerts sends the label ALONGSIDE the raw key rather than instead of it:
// sorting, filtering and the CSV export all key off alert_type, and only the
// display wants a name.
func (s *Server) reportAlerts(w http.ResponseWriter, _ *http.Request, q reportReq) {
	rows, err := s.auditDB.AlertEvents(q.RouterID, q.From, q.To)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		out = append(out, map[string]any{
			"id": a.ID, "alert_type": a.AlertType, "subject": a.Subject,
			"detail": a.Detail, "fired_at": a.FiredAt, "resolved_at": a.ResolvedAt,
			"acknowledged_at": a.AcknowledgedAt, "acknowledged_by": a.AcknowledgedBy,
			"alert_label": reports.LabelFor(a.AlertType),
		})
	}
	writeJSON(w, map[string]any{"ok": true, "rows": out})
}

// reportConnectivity annotates the raw series with each outage's duration. The
// AGGREGATED form is not annotated: a bucket is a percentage over a window, and
// "how long was this one down" has no answer there.
func (s *Server) reportConnectivity(w http.ResponseWriter, _ *http.Request, q reportReq) {
	if q.Aggregate != "" {
		rows, err := s.auditDB.ConnectivityEventsAgg(q.RouterID, q.From, q.To, q.Aggregate)
		writeRows(w, rows, err)
		return
	}
	events, err := s.auditDB.ConnectivityEvents(q.RouterID, q.From, q.To)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]reports.ConnRow, 0, len(events))
	for _, e := range events {
		rows = append(rows, reports.ConnRow{TS: e.TS, Connected: e.Connected})
	}
	writeJSON(w, map[string]any{"ok": true, "rows": reports.AnnotateDowntime(rows)})
}

// ── the interface summary ───────────────────────────────────────────────────

// ifaceSummary is build.js's, merged from the two summary queries plus the
// router's configured line speed.
//
// CAPACITY COMES FROM THE REQUESTED ROUTER, not from whichever one the browser
// happens to have selected — a report for router B viewed while A is on screen
// still uses B's line speed. The id has already been authorised by the time this
// runs, and only the two capacity integers are read off the record.
func (s *Server) ifaceSummary(routerID, iface string, from, to int64) (map[string]any, error) {
	t, err := s.auditDB.TrafficSummary(routerID, iface, from, to, 95)
	if err != nil {
		return nil, err
	}
	b, err := s.auditDB.BandwidthSummary(routerID, iface, from, to)
	if err != nil {
		return nil, err
	}
	down, up := s.capacityOf(routerID)
	return map[string]any{
		// TWO COUNTS, NAMED APART, AND NO `samples` KEY AT ALL.
		//
		// Both summary queries carry a sample count and build.js used to merge
		// them with `{ ...t, ...b }`, so the second spread won and every consumer
		// got the BANDWIDTH count — the card under the RATE chart reported how
		// many volume rows exist. The two genuinely differ: a bandwidth bucket is
		// only written when the minute actually moved bytes. 4,637 against 3,793
		// on one real range, neither labelled with its table.
		//
		// Found by this port comparing against the live db.js on the same
		// database, reported upstream, and fixed there in `Pruning keeps its
		// promise` (v0.7.33+) — by naming them apart rather than picking one,
		// because the traffic tab means one and the bandwidth tab and PDF mean the
		// other. The ambiguous key is deliberately NOT carried forward, so a
		// consumer has to say which it means.
		"trafficSamples": t.Samples, "bandwidthSamples": b.Samples,
		"rxAvgMbps": t.RxAvgMbps, "txAvgMbps": t.TxAvgMbps,
		"rxMaxMbps": t.RxMaxMbps, "txMaxMbps": t.TxMaxMbps,
		"rxP95Mbps": t.RxP95Mbps, "txP95Mbps": t.TxP95Mbps,
		"rxTotalMb": b.RxTotalMb, "txTotalMb": b.TxTotalMb,
		"rxMaxMb": b.RxMaxMb, "txMaxMb": b.TxMaxMb,
		"capacityDownMbps": down, "capacityUpMbps": up,
		"rxPeakPct": reports.UtilPct(t.RxMaxMbps, down),
		"txPeakPct": reports.UtilPct(t.TxMaxMbps, up),
		"rxP95Pct":  reports.UtilPct(t.RxP95Mbps, down),
		"txP95Pct":  reports.UtilPct(t.TxP95Mbps, up),
	}, nil
}

// capacityOf reads the configured line speeds, defaulting the way build.js does.
//
// The record holds NUMBERS — routers.js normalises them on write — and the live
// code still runs them through `parseInt(...) || 1000`, which accepts either. So
// the value goes through the one gated implementation of that rule rather than a
// second copy written for ints: for any integer the two are identical, and one
// of them has a case set behind it.
func (s *Server) capacityOf(routerID string) (down, up int) {
	down, up = reports.CapacityOr(""), reports.CapacityOr("")
	if s.store == nil {
		return down, up
	}
	routers, _ := s.store.Routers()
	for _, r := range routers {
		if r.ID == routerID {
			return reports.CapacityOr(strconv.Itoa(r.BwDownMbps)),
				reports.CapacityOr(strconv.Itoa(r.BwUpMbps))
		}
	}
	return down, up
}

// writeRows is the `{ok:true, rows}` envelope every series endpoint sends.
func writeRows(w http.ResponseWriter, rows any, err error) {
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "rows": rows})
}

// ── Scheduled reports ───────────────────────────────────────────────────────
//
// READING the list is the same grant as reading a report, and that is the live
// app's reasoning rather than a simplification: "anyone who can already export a
// report may see what is scheduled, and visibility is itself a control — a
// mail-out nobody can see is the bad case."
//
// CREATING one is a write-level grant, because a schedule mails router history to
// arbitrary third-party addresses indefinitely without anyone signing in again.
// In rbac.js that is `router:schedule`, and `WRITE_CONFERS = { reports:
// ['router:schedule'] }` is the only thing that grants it — so the projection is
// the same one this file already uses for reads, one access level up:
// canPage("reports", "write"). `permitted` is answered so the page can hide what
// it cannot do.
//
// This used to add "the write endpoints are NOT ported yet", which stopped being
// true in this same file: POST, PUT and DELETE on `schedules` are registered
// above. The page's own header records which BUTTON is bound to which of them —
// Remove is, Edit waits on the form, and Send now has no handler here at all.

// scheduleSections and scheduleNeedsInterface are `Reports.SECTIONS` and
// `Reports.NEEDS_INTERFACE` from build.js. Sent to the page rather than hardcoded
// there, so the vocabulary has one definition.
var (
	scheduleSections       = []string{"ping", "traffic", "bandwidth", "alerts", "connectivity"}
	scheduleNeedsInterface = []string{"traffic", "bandwidth"}
)

// jsonArray decodes a column holding a JSON array, falling back to an empty one.
//
// The original's `parse(s, fallback)` does exactly this, and the fallback is not
// defensive noise: these columns are written by a validator, but a row predating
// a schema change or hand-edited in sqlite3 would otherwise take the whole page
// down rather than showing one odd schedule.
func jsonArray(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}

// reportScheduleRuns is one schedule's run history.
//
// ── A ROUTER YOU MAY READ MUST NOT REACH ANOTHER ROUTER'S SCHEDULE ──────────
//
// The gate authorises `routerId` from the query string; the schedule id comes
// from the path and is not authorised by anything. So the row's OWN router_id is
// compared against the authorised one and a mismatch is a 404 — not a 403, which
// would confirm the id exists. This is `_scheduleRow`'s shape, and the live
// source calls it the pattern "_sendBackupPart establishes: naming a router you
// may write must never reach a record belonging to another".
func (s *Server) reportScheduleRuns(w http.ResponseWriter, r *http.Request, q reportReq) {
	row, err := s.auditDB.ReportSchedule(r.PathValue("id"))
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	if row == nil || row.RouterID != q.RouterID {
		writeJSONErr(w, http.StatusNotFound, "not found")
		return
	}
	runs, err := s.auditDB.ReportRuns(row.ID, 20)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "runs": runs})
}

func (s *Server) reportSchedules(w http.ResponseWriter, _ *http.Request, q reportReq) {
	rows, err := s.auditDB.ReportSchedulesFor(q.RouterID)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		// The page shows when each last ran, so the list is useful without opening
		// the history for every row in turn.
		var last any
		if runs, err := s.auditDB.ReportRuns(r.ID, 1); err == nil && len(runs) > 0 {
			last = map[string]any{"ran_at": runs[0].RanAt, "outcome": runs[0].Outcome}
		}
		iface := ""
		if r.Interface != nil {
			iface = *r.Interface
		}
		var disabledReason any
		if r.DisabledReason != nil && *r.DisabledReason != "" {
			disabledReason = *r.DisabledReason
		}
		out = append(out, map[string]any{
			"id": r.ID, "routerId": r.RouterID, "name": r.Name,
			"sections": jsonArray(r.Sections), "iface": iface, "aggregate": r.Aggregate,
			// RECIPIENTS ARE SENT. The original's comment is explicit that they are
			// not secrets — and a schedule whose destinations cannot be seen is the
			// case that gate exists to prevent.
			"recipients": jsonArray(r.Recipients), "frequency": r.Frequency,
			"sendHour": r.SendHour, "enabled": r.Enabled != 0,
			"disabledReason": disabledReason,
			"createdAt":      r.CreatedAt, "updatedAt": r.UpdatedAt,
			"lastRun": last,
		})
	}

	// So the page can say "this will never send" at creation time rather than in a
	// run row a month later.
	smtpReady := false
	if s.store != nil {
		if cfg, err := s.store.Settings(); err == nil {
			host, _ := cfg["smtpHost"].(string)
			from, _ := cfg["smtpFrom"].(string)
			smtpReady = host != "" && from != ""
		}
	}

	writeJSON(w, map[string]any{
		"ok": true, "schedules": out, "smtpReady": smtpReady,
		"permitted":      s.mayWriteReports(q.Sess, q.RouterID),
		"sections":       scheduleSections,
		"needsInterface": scheduleNeedsInterface,
	})
}

// ── Schedule writes ─────────────────────────────────────────────────────────
//
// ── "Send now" IS HERE NOW, AND THIS NOTE SAID OTHERWISE FOR TOO LONG ───────
//
// This block claimed `POST /schedules/{id}/run` was "deliberately absent"
// because "neither the builder nor the mailer is ported". Both are:
// `reports_run.go` serves the route, `internal/reportpdf` draws the document and
// `internal/mailer` sends it. The route is registered a few dozen lines above
// this comment. Corrected 2026-08-27.
//
// That makes it the THIRD stale claim in this file's Scheduled-tab notes, after
// `rptSchedNew` and the rate limiter — and the second one that survived a
// session which was reading this exact block. The pattern is worth naming: a
// note describing what is MISSING has no gate, because nothing fails when the
// thing arrives. The absent route was recorded in three places and all three had
// to be corrected by hand.
//
// The button itself was drawn and unbound until the same date; it is wired now
// and pinned by the sched-run check (11 mutations).
//
// Its rate limiter: the live app caps Send now at 5 per minute against 30 for
// the others, because that one endpoint actually sends mail.
//
// ── RATE LIMITING IS PORTED, AND THIS NOTE WAS STALE ────────────────────────
//
// It said: "This server has no limiter at all — not for these, not for anything."
// That is contradicted by `lim(...)` on the POST, PUT and DELETE registrations a
// few dozen lines above, and by `ratelimit.go`, which implements exactly the
// three limiters the live app puts on `/api/reports/schedules` at 30/min — and
// deliberately none on the reads, because the original has none there and a port
// that adds protection still behaves differently. Corrected 2026-08-25.

// scheduleWriteReq is a write request whose target has been resolved and checked.
type scheduleWriteReq struct {
	reportReq
	// Row is the existing schedule for an update or delete, and nil for a create.
	Row *db.ReportSchedule
}

// scheduleWriteHandler is the write counterpart of reportHandler: session,
// routerId, the WRITE gate, and — for a request naming a schedule — the
// ownership check.
//
// The ownership check is the same one the runs endpoint makes and for the same
// reason: the id comes from the PATH and is authorised by nothing, so the row's
// own router_id is compared against the authorised one and a mismatch is a 404.
func (s *Server) scheduleWriteHandler(
	needsRow bool, fn func(http.ResponseWriter, *http.Request, scheduleWriteReq),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		q := r.URL.Query()
		routerID := q.Get("routerId")
		if routerID == "" {
			writeJSONErr(w, http.StatusBadRequest, "routerId required")
			return
		}
		if !s.mayWriteReports(sess, routerID) {
			writeJSONErr(w, http.StatusForbidden, "Not permitted")
			return
		}
		if s.auditDB == nil {
			// A WRITE with no audit trail is refused rather than performed
			// silently. Reads degrade to "history unavailable"; a write that
			// cannot be recorded is a different matter, and the live app's own
			// framing — the trail is the point — makes refusing the safer default.
			writeJSONErr(w, http.StatusServiceUnavailable, "the audit trail is unavailable")
			return
		}

		req := scheduleWriteReq{reportReq: reportReq{
			Params: reports.Params{RouterID: routerID}, Sess: sess}}
		if needsRow {
			row, err := s.auditDB.ReportSchedule(r.PathValue("id"))
			if err != nil {
				writeJSONErrFrom(w, http.StatusInternalServerError, err)
				return
			}
			if row == nil || row.RouterID != routerID {
				writeJSONErr(w, http.StatusNotFound, "not found")
				return
			}
			req.Row = row
		}
		fn(w, r, req)
	}
}

// decodeScheduleInput reads the submitted schedule.
func decodeScheduleInput(r *http.Request) (reports.ScheduleInput, error) {
	var in reports.ScheduleInput
	dec := json.NewDecoder(r.Body)
	// A field the port does not know is a field the operator thinks they set.
	// Refusing it is how a renamed key is found at the dialog rather than in a
	// report that quietly reports the wrong thing.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, err
	}
	return in, nil
}

// storeSchedule writes a validated schedule and returns its public form.
func (s *Server) storeSchedule(v reports.ValidSchedule) (map[string]any, error) {
	sections, err := json.Marshal(v.Sections)
	if err != nil {
		return nil, err
	}
	recipients, err := json.Marshal(v.Recipients)
	if err != nil {
		return nil, err
	}
	enabled := 0
	if v.Enabled {
		enabled = 1
	}
	row := db.ReportSchedule{
		ID: v.ID, RouterID: v.RouterID, Name: v.Name,
		Sections: string(sections), Aggregate: v.Aggregate,
		Recipients: string(recipients), Frequency: v.Frequency,
		SendHour: v.SendHour, Enabled: enabled,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
	// The nullable columns are pointers, and an empty string is NULL rather than
	// '' — the read path distinguishes them and so does the live writer.
	if v.Iface != "" {
		iface := v.Iface
		row.Interface = &iface
	}
	if v.CreatedBy != "" {
		by := v.CreatedBy
		row.CreatedBy = &by
	}
	if err := s.auditDB.UpsertReportSchedule(row); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": v.ID, "routerId": v.RouterID, "name": v.Name, "sections": v.Sections,
		"iface": v.Iface, "aggregate": v.Aggregate, "recipients": v.Recipients,
		"frequency": v.Frequency, "sendHour": v.SendHour, "enabled": v.Enabled,
		"disabledReason": nil, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt,
	}, nil
}

// reportScheduleCreate is POST /schedules.
//
// The ID IS MINTED HERE, never taken from the request. A caller-supplied id
// would let one operator overwrite another's schedule through the create path,
// which the update path's ownership check exists to prevent.
func (s *Server) reportScheduleCreate(w http.ResponseWriter, r *http.Request, q scheduleWriteReq) {
	in, err := decodeScheduleInput(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	id, err := newScheduleID()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "cannot generate an id")
		return
	}
	actor := ""
	if q.Sess != nil {
		actor = s.userIDFor(q.Sess.Username)
	}
	v, err := reports.Validate(in, id, q.RouterID, actor, 0, time.Now())
	if err != nil {
		// The validator's messages are written for an operator to read.
		writeJSONErrFrom(w, http.StatusBadRequest, err)
		return
	}
	pub, err := s.storeSchedule(v)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	// RECIPIENTS GO INTO THE TRAIL. They are not secrets — the read endpoint
	// sends them — and who a schedule mails to is the single most useful thing
	// this record can carry.
	s.httpRecorder(r, q.Sess).Record(audit.Event{
		Action: "report.schedule.create", TargetType: "report-schedule",
		Scope: "router", RouterID: q.RouterID, TargetID: v.ID, TargetName: v.Name,
		Extra: []audit.KV{
			{Key: "frequency", Value: v.Frequency},
			{Key: "sections", Value: v.Sections},
			{Key: "recipients", Value: v.Recipients},
		},
	})
	writeJSON(w, map[string]any{"ok": true, "schedule": pub})
}

// reportScheduleUpdate is PUT /schedules/{id}.
//
// The id, routerId, createdBy and createdAt come from the STORED ROW, never from
// the request — so an edit cannot move a schedule to another router or rewrite
// who created it. The storage layer refuses the last two as well; this is the
// first of the two fences.
func (s *Server) reportScheduleUpdate(w http.ResponseWriter, r *http.Request, q scheduleWriteReq) {
	in, err := decodeScheduleInput(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	createdBy := ""
	if q.Row.CreatedBy != nil {
		createdBy = *q.Row.CreatedBy
	}
	v, err := reports.Validate(in, q.Row.ID, q.Row.RouterID, createdBy, q.Row.CreatedAt, time.Now())
	if err != nil {
		writeJSONErrFrom(w, http.StatusBadRequest, err)
		return
	}
	pub, err := s.storeSchedule(v)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, q.Sess).Record(audit.Event{
		Action: "report.schedule.update", TargetType: "report-schedule",
		Scope: "router", RouterID: q.Row.RouterID, TargetID: q.Row.ID, TargetName: v.Name,
		Extra: []audit.KV{
			{Key: "frequency", Value: v.Frequency},
			{Key: "sections", Value: v.Sections},
			{Key: "recipients", Value: v.Recipients},
			{Key: "enabled", Value: v.Enabled},
		},
	})
	writeJSON(w, map[string]any{"ok": true, "schedule": pub})
}

// reportScheduleDelete is DELETE /schedules/{id}.
//
// The record is written with the name from the row that was DELETED, because
// after this the only place that name exists is the trail.
func (s *Server) reportScheduleDelete(w http.ResponseWriter, r *http.Request, q scheduleWriteReq) {
	if err := s.auditDB.RemoveReportSchedule(q.Row.ID); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, q.Sess).Record(audit.Event{
		Action: "report.schedule.delete", TargetType: "report-schedule",
		Scope: "router", RouterID: q.Row.RouterID, TargetID: q.Row.ID, TargetName: q.Row.Name,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// newScheduleID is a v4 UUID, matching `crypto.randomUUID()`.
func newScheduleID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ── CSV and PDF export ──────────────────────────────────────────────────────
//
// This said "PDF IS NOT PORTED, AND `?format=pdf` SAYS SO" and answered 501 on
// the reasoning that a document renderer with fonts, page breaks and a layout
// engine "would be a project, not a slice". It was a project, and it is done:
// `internal/reportpdf` draws the report and `internal/reports` builds it.
//
// The refusal was still the right call while it stood — returning CSV under a
// .pdf filename would have been a wrong file that opens, which is worse than a
// refusal that explains itself.

// exportRows assembles the LABELLED rows for one report type — the CSV's shape,
// with timestamps already rendered.
//
// It is a thin layer over rawRows because the PDF needs the same data twice and
// differently: the table wants the labelled rows, and the chart wants the raw
// ones, since a series plotted against "2026-08-25 06:00" has no x axis. Fetching
// once and labelling once keeps the two from drifting into separate queries.
func (s *Server) exportRows(kind string, q reportReq, tz string) ([]map[string]any, error) {
	if kind == "connectivity" {
		conn, err := s.connRows(q)
		if err != nil {
			return nil, err
		}
		return reports.LabelConnectivity(conn, tz), nil
	}
	raw, err := s.rawRows(kind, q)
	if err != nil {
		return nil, err
	}
	if kind == "alerts" {
		return reports.LabelAlerts(raw, tz), nil
	}
	return reports.LabelSamples(raw, tz), nil
}

// rawRows fetches one report type's rows with nothing rendered yet.
func (s *Server) rawRows(kind string, q reportReq) ([]map[string]any, error) {
	switch kind {
	case "ping":
		var rows any
		var err error
		if q.Aggregate != "" {
			rows, err = s.auditDB.PingSamplesAgg(q.RouterID, q.From, q.To, q.Aggregate)
		} else {
			rows, err = s.auditDB.PingSamples(q.RouterID, q.From, q.To)
		}
		if err != nil {
			return nil, err
		}
		return toMaps(rows), nil

	case "traffic":
		var rows any
		var err error
		if q.Aggregate != "" {
			rows, err = s.auditDB.TrafficSamplesAgg(q.RouterID, q.Iface, q.From, q.To, q.Aggregate)
		} else {
			rows, err = s.auditDB.TrafficSamples(q.RouterID, q.Iface, q.From, q.To)
		}
		if err != nil {
			return nil, err
		}
		return toMaps(rows), nil

	case "bandwidth":
		var rows any
		var err error
		if q.Aggregate != "" {
			rows, err = s.auditDB.BandwidthSamplesAgg(q.RouterID, q.Iface, q.From, q.To, q.Aggregate)
		} else {
			rows, err = s.auditDB.BandwidthSamples(q.RouterID, q.Iface, q.From, q.To)
		}
		if err != nil {
			return nil, err
		}
		return toMaps(rows), nil

	case "alerts":
		rows, err := s.auditDB.AlertEvents(q.RouterID, q.From, q.To)
		if err != nil {
			return nil, err
		}
		return toMaps(rows), nil
	}
	return nil, nil
}

// connRows is the connectivity series, annotated with each outage's duration.
//
// The RAW series, not buckets — an export of buckets would have no per-outage
// duration to report, which is the column people open this file for.
func (s *Server) connRows(q reportReq) ([]reports.ConnRow, error) {
	events, err := s.auditDB.ConnectivityEvents(q.RouterID, q.From, q.To)
	if err != nil {
		return nil, err
	}
	conn := make([]reports.ConnRow, 0, len(events))
	for _, e := range events {
		conn = append(conn, reports.ConnRow{TS: e.TS, Connected: e.Connected})
	}
	return reports.AnnotateDowntime(conn), nil
}

// routerLabel is `_routerLabel`: the router's label, else its host, else the id
// it was asked about.
func (s *Server) routerLabel(routerID string) string {
	if s.store == nil {
		return routerID
	}
	routers, _ := s.store.Routers()
	for _, r := range routers {
		if r.ID == routerID {
			return reports.RouterLabel(r.Label, r.Host, routerID)
		}
	}
	return routerID
}

// toMaps re-decodes typed rows into the generic shape the CSV writer takes.
//
// Through JSON rather than reflection, so the keys are exactly the payload's —
// the CSV column list names `rx_mbps`, and a struct-field walk would produce
// `RxMbps` and quietly emit a column of blanks.
func toMaps(v any) []map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// reportExportFor serves one report type's `/export`.
func (s *Server) reportExportFor(kind string) http.HandlerFunc {
	spec := reports.ExportSpecs[kind]
	return s.reportHandler(func(w http.ResponseWriter, r *http.Request, q reportReq) {
		s.writeExport(w, r, q, kind, spec)
	})
}

func (s *Server) writeExport(
	w http.ResponseWriter, r *http.Request, q reportReq, kind string, spec reports.ExportSpec,
) {
	wantPDF := strings.ToLower(r.URL.Query().Get("format")) == "pdf"

	// The live endpoint refuses an export of traffic or bandwidth with no
	// interface, because those reports are per-interface and one covering "all of
	// them" would be a different document.
	if (kind == "traffic" || kind == "bandwidth") && q.Iface == "" {
		writeJSONErr(w, http.StatusBadRequest, "interface required for export")
		return
	}

	tz := ""
	if s.store != nil {
		if cfg, err := s.store.Settings(); err == nil {
			tz, _ = cfg["displayTimezone"].(string)
		}
	}
	if wantPDF {
		s.writePDF(w, kind, q, tz)
		return
	}

	rows, err := s.exportRows(kind, q, tz)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	// `; charset=utf-8`, which the live source does NOT spell but the live
	// RESPONSE carries: `res.setHeader('Content-Type', 'text/csv')` followed by
	// `res.send(string)` makes Express append the charset for a text type.
	// Measured on 2026-08-29 by comparing the two responses — the port sent bare
	// `text/csv` where the live app sent `text/csv; charset=utf-8`.
	//
	// It matters for the same reason the charset always does: without it a
	// browser or spreadsheet guesses the encoding, so an alert detail or a router
	// label carrying a non-ASCII rune opens as mojibake. The report bodies are
	// byte-identical between the two apps; only the header differed.
	//
	// `audit_api.go`'s CSV export already sent it, so this was the port
	// disagreeing with ITSELF as well as with the live response.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// The filename is a CONSTANT from the spec, never built from a router name or
	// an interface — a header built from router-controlled text is how a response
	// header gets split.
	w.Header().Set("Content-Disposition", `attachment; filename="`+spec.Filename+`"`)
	_, _ = w.Write([]byte(reports.ToCSV(rows, spec.Columns)))
}

// ── PDF export ──────────────────────────────────────────────────────────────

// writePDF renders one report as a PDF.
//
// RENDERED TO A BUFFER FIRST, unlike the live `pipe()`, which sets its headers
// and then streams into the response. The live comment admits what that costs:
// "the piped path commits its headers before rendering starts, so a failure
// mid-render produces a truncated PDF rather than a 500". Buffering changes no
// successful response and turns that failure into an error the caller can see,
// which is a backend mechanic rather than a behaviour.
//
// The document is bounded before it is built — `CapRows` caps the table at 5,000
// rows and `Thin` caps each series at 150 points — so the buffer cannot grow with
// the range the caller asked for.
func (s *Server) writePDF(w http.ResponseWriter, kind string, q reportReq, tz string) {
	build, err := s.buildPDF(kind, q, tz)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeRenderedPDF(w, build, tz)
}

// writeRenderedPDF is the half that needs no database, so it can be tested
// without one: draw the build, then answer with it.
func writeRenderedPDF(w http.ResponseWriter, build reports.PDFBuild, tz string) {
	cv, doc := reportpdf.NewFPDFCanvas()
	reportpdf.Render(cv, build.Title, build.Columns, build.Rows, &build.Meta, tz)

	var buf bytes.Buffer
	if err := reportpdf.Output(doc, &buf); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	// The filename comes from the report's TITLE, which is a constant inside each
	// builder — never from a router name, an interface or anything else the
	// caller supplies. That is the same rule the CSV filename follows, and for
	// the same reason: a header built from router-controlled text is how a
	// response header gets split.
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+build.Title+`.pdf"`)
	_, _ = w.Write(buf.Bytes())
}

// buildPDF assembles one section's columns, rows and meta.
func (s *Server) buildPDF(kind string, q reportReq, tz string) (reports.PDFBuild, error) {
	label := s.routerLabel(q.RouterID)
	from, to := float64(q.From), float64(q.To)

	if kind == "connectivity" {
		conn, err := s.connRows(q)
		if err != nil {
			return reports.PDFBuild{}, err
		}
		return reports.BuildConnectivity(conn, label, from, to, tz), nil
	}

	raw, err := s.rawRows(kind, q)
	if err != nil {
		return reports.PDFBuild{}, err
	}

	switch kind {
	case "ping":
		return reports.BuildPing(raw, label, from, to, tz), nil
	case "alerts":
		return reports.BuildAlerts(raw, label, from, to, tz), nil
	case "traffic", "bandwidth":
		sum, err := s.pdfSummary(q)
		if err != nil {
			return reports.PDFBuild{}, err
		}
		if kind == "traffic" {
			return reports.BuildTraffic(raw, sum, label, from, to, tz), nil
		}
		return reports.BuildBandwidth(raw, sum, q.Aggregate, label, from, to, tz), nil
	}
	return reports.PDFBuild{}, fmt.Errorf("unknown report section %q", kind)
}

// ifaceSummary is the live `ifaceSummary`: two SQL summaries over the WHOLE
// range plus the router's configured line capacity.
//
// Over the whole range and NOT over the returned rows, which is the live
// reasoning and worth keeping: once an aggregation is selected those rows are
// averages, so a maximum over them is a peak of averages — and they are capped
// by the query LIMIT besides.
func (s *Server) pdfSummary(q reportReq) (reports.IfaceSummary, error) {
	t, err := s.auditDB.TrafficSummary(q.RouterID, q.Iface, q.From, q.To, 95)
	if err != nil {
		return reports.IfaceSummary{}, err
	}
	b, err := s.auditDB.BandwidthSummary(q.RouterID, q.Iface, q.From, q.To)
	if err != nil {
		return reports.IfaceSummary{}, err
	}
	down, up := s.capacityOf(q.RouterID)
	return reports.IfaceSummary{
		RxAvgMbps: t.RxAvgMbps, TxAvgMbps: t.TxAvgMbps,
		RxMaxMbps: t.RxMaxMbps, TxMaxMbps: t.TxMaxMbps,
		RxP95Mbps: t.RxP95Mbps, TxP95Mbps: t.TxP95Mbps,
		RxTotalMb: b.RxTotalMb, TxTotalMb: b.TxTotalMb,
		RxMaxMb: b.RxMaxMb, TxMaxMb: b.TxMaxMb,
		// The AMBIGUOUS `samples` key the live code deliberately refuses to carry
		// forward: the bandwidth tab and the PDF mean the bandwidth count.
		BandwidthSamples: b.Samples,
		CapacityDown:     down,
		CapacityUp:       up,
	}, nil
}
