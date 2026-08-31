package server

// The audit trail's two READ endpoints: the page's query and its CSV export.
//
// (The WRITE half — recording events — is audit.go, which shares only the
// package. Recording and querying are separate files in the live repo too:
// src/audit.js records, src/index.js queries.)
//
// ── THE GATE IS PER-ROW, NOT PER-REQUEST ────────────────────────────────────
//
// Every other ported endpoint asks one question — "may this session see page P
// on router R" — and answers 403 when the answer is no. The audit trail cannot:
// half its rows have no router at all. index.js says so directly, and explains
// why these are deliberately not under /api/reports/*, whose handlers answer 400
// without a router.
//
// So the session resolves to TWO permissions, both passed to the query:
//
//	scope='app'     a user, role, grant or settings change — system:principals
//	scope='router'  filtered to the routers this session may see — router:history
//
// ANY SIGNED-IN USER MAY REACH THIS. A session holding neither gets an empty
// list rather than a 403, because the page is legitimately empty for them —
// answering 403 would tell them the trail exists and they are excluded, which is
// a different statement from "nothing here concerns you".
//
// ── A ROUTERID FILTER NARROWS, NEVER WIDENS ─────────────────────────────────
//
// `?routerId=` is intersected with the permitted set before the query sees it,
// so naming a router the session cannot see falls back to the full permitted
// list rather than selecting that router. That is the live behaviour and it is
// the whole reason the filter is applied to `RouterIDs` rather than to
// db.Query's separate `RouterID` field.
//
// That field exists — db.js accepts `o.routerId` and ANDs it as `router_id = ?`
// — but `_auditQuery` never sets it, so it is dead in the live call path. Wiring
// the query parameter to it would look equivalent and would not be: it ANDs
// AFTER the visibility clause rather than intersecting with it, so with
// `includeApp` true it would return app-scoped rows alongside a router the
// session may not see. Left unset here, matching the original.

import (
	"log"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/reports"
)

// auditPrefix is where these hang until the page cuts over — the same staging
// rule reports.go documents, for the same reason: the live Audit page is still
// served by Node through this proxy.
const auditPrefix = "/api/audit"

func (s *Server) registerAudit(mux *http.ServeMux) {
	mux.HandleFunc("GET "+auditPrefix, s.auditQuery)
	mux.HandleFunc("GET "+auditPrefix+"/export", s.auditExport)
}

// auditOutcomes is the filter whitelist. Anything else becomes no filter at all
// rather than an error, matching `['ok','denied','failed'].includes(...) ? … : ”`.
var auditOutcomes = map[string]bool{"ok": true, "denied": true, "failed": true}

// auditScope resolves the session's two permissions.
//
// FAILS CLOSED ON AN UNANSWERABLE QUESTION. An RBAC error yields no scope at
// all, so the caller answers 500 rather than serving a trail whose filtering
// could not be computed.
func (s *Server) auditScope(sess *Session) (includeApp bool, routerIDs []string, err error) {
	if sess.AuthMode == "none" {
		// One local operator with full reach, matching rbac.js's single copy of
		// this short circuit. Every router, and the app scope too.
		//
		// The problem list is DISCARDED, as it is where server.go builds the
		// rbac resolver's router list. Those are per-router credential-decrypt
		// failures, and such a router stays in the list with an empty password:
		// one router encrypted under an old key must not hide the other five,
		// and a credential is not consulted to decide what a trail may show.
		list, _ := s.store.Routers()
		ids := make([]string, 0, len(list))
		for _, r := range list {
			ids = append(ids, r.ID)
		}
		return true, ids, nil
	}

	uid := s.userIDFor(sess.Username)
	// NO "resolver unavailable" FALLBACK HERE, unlike reports.go's documented
	// gap. The resolver reads the same database the trail lives in, so an
	// unavailable resolver means an unavailable trail — the caller has already
	// answered 503 and never reaches this.
	app, err := s.rbac.Can(uid, "system:principals", "")
	if err != nil {
		return false, nil, err
	}
	ids, err := s.rbac.EffectiveRouterIDs(uid, "router:history")
	if err != nil {
		return false, nil, err
	}
	return app, ids, nil
}

// auditQueryFor builds the database query from the request and the scope.
func auditQueryFor(r *http.Request, includeApp bool, permitted []string) db.Query {
	q := r.URL.Query()

	// The narrowing. See the file header: an id outside the permitted set is
	// ignored, not honoured and not refused.
	ids := permitted
	if want := q.Get("routerId"); want != "" {
		for _, id := range permitted {
			if id == want {
				ids = []string{want}
				break
			}
		}
	}

	to := reports.LeadingInt(q.Get("to"))
	if to == 0 {
		to = time.Now().UnixMilli()
	}

	return db.Query{
		IncludeApp: includeApp,
		RouterIDs:  ids,
		From:       reports.LeadingInt(q.Get("from")),
		To:         to,
		// CLIPPED BY BYTES where the original clips by UTF-16 code units. These
		// are filter values rather than stored ones, so the only reachable
		// difference is where a multi-byte search term gets cut — and both
		// languages cut mid-character, just at different offsets.
		Actor:   clip(q.Get("actor"), 100),
		Action:  clip(q.Get("action"), 60),
		Search:  clip(q.Get("search"), 100),
		Outcome: outcomeFilter(q.Get("outcome")),
		Limit:   reports.ClampInt(reports.LeadingInt(q.Get("limit"))),
		Offset:  reports.ClampInt(reports.LeadingInt(q.Get("offset"))),
	}
}

func outcomeFilter(v string) string {
	if auditOutcomes[v] {
		return v
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// auditRouterNames resolves the router ids on a page of rows to the labels an
// operator recognises.
//
// Built once per request rather than per row: a page is 200 events and most name
// the same handful of routers.
//
// DISCLOSES NOTHING NEW. Every row here has already passed the visibility scope,
// and each already carries the id — this only turns an opaque uuid into the name
// for the same device. A deleted router resolves to nothing, and the two callers
// disagree about what that means on purpose: the table keeps its generic marker,
// the export keeps the id, because an export is where a dangling reference still
// has to be followable.
func (s *Server) auditRouterNames(rows []db.Row) map[string]string {
	names := map[string]string{}
	want := map[string]bool{}
	for _, r := range rows {
		if r.RouterID != nil && *r.RouterID != "" {
			want[*r.RouterID] = true
		}
	}
	if len(want) == 0 {
		return names
	}
	// Problems discarded for the reason auditScope gives; a router that failed
	// to decrypt still has the label this is looking for.
	list, _ := s.store.Routers()
	for _, rt := range list {
		if !want[rt.ID] {
			continue
		}
		if rt.Label != "" {
			names[rt.ID] = rt.Label
		} else {
			names[rt.ID] = rt.Host
		}
	}
	return names
}

// auditRow is a trail row as the page receives it: every stored column plus the
// resolved router name.
//
// EMBEDDED rather than copied field by field, so a column added to db.Row
// appears here without an edit — and so the JSON tags cannot drift from the ones
// the page's markup was written against.
type auditRow struct {
	db.Row
	RouterName string `json:"router_name"`
}

// auditRead is everything the two endpoints share: the session, the trail's
// availability, the scope, the query and the router names.
func (s *Server) auditRead(w http.ResponseWriter, r *http.Request, export bool) (db.Page, []auditRow, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return db.Page{}, nil, false
	}
	if s.auditDB == nil {
		// An empty list would read as "nothing has happened" rather than "the
		// trail is unavailable", and for an audit trail those must never look
		// the same.
		writeJSONErr(w, http.StatusServiceUnavailable, "audit trail unavailable")
		return db.Page{}, nil, false
	}

	includeApp, permitted, err := s.auditScope(sess)
	if err != nil {
		log.Printf("[audit] scope failed for %s: %v", sess.Username, err)
		writeJSONErr(w, http.StatusInternalServerError, "audit unavailable")
		return db.Page{}, nil, false
	}

	q := auditQueryFor(r, includeApp, permitted)
	if export {
		// An export is a snapshot of the FILTERED VIEW, not of the page — but
		// still bounded, because the file has no row cap of its own.
		q.Limit, q.Offset = 1000, 0
	}

	page, err := s.auditDB.QueryAuditEvents(q)
	if err != nil {
		log.Printf("[audit] query failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "audit query failed")
		return db.Page{}, nil, false
	}

	names := s.auditRouterNames(page.Rows)
	out := make([]auditRow, 0, len(page.Rows))
	for _, row := range page.Rows {
		name := ""
		if row.RouterID != nil {
			name = names[*row.RouterID]
		}
		out = append(out, auditRow{Row: row, RouterName: name})
	}
	return page, out, true
}

func (s *Server) auditQuery(w http.ResponseWriter, r *http.Request) {
	page, rows, ok := s.auditRead(w, r, false)
	if !ok {
		return
	}
	facets, err := s.auditDB.AuditFacets()
	if err != nil {
		log.Printf("[audit] facets failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "audit query failed")
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "rows": rows, "total": page.Total,
		"limit": page.Limit, "offset": page.Offset, "facets": facets,
	})
}

// auditExportCols is the column order, and it is NOT db.Row's — an export is
// read by a person as often as by a program, so it carries the resolved actor
// and router rather than the stored ids.
var auditExportCols = []string{"ts", "actor", "ip", "action", "target", "router", "outcome", "detail"}

func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.URL.Query().Get("format"), "pdf") {
		// Deliberately unported. Said plainly rather than silently handing back a
		// CSV under a .pdf filename.
		//
		// IT NO LONGER READS "like the reports PDF" — that comparison was true
		// when written and stopped being true when `internal/reportpdf` landed on
		// fpdf; `reports.go` records that its own 501 went with it. So the audit
		// export is the ONLY PDF this port declines, and it declines it on its
		// own account rather than by analogy to something already done.
		//
		// Nothing technical blocks it now: the canvas, the metrics and the
		// encoder are all there. It is unbuilt work, not an unavailable
		// capability, and saying so is the difference between a reader closing
		// the question and re-deriving it.
		writeJSONErr(w, http.StatusNotImplemented, "PDF export is not implemented in this build")
		return
	}
	_, rows, ok := s.auditRead(w, r, true)
	if !ok {
		return
	}

	flat := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		router := row.RouterName
		if router == "" && row.RouterID != nil {
			// The id where the router no longer exists — see auditRouterNames.
			router = *row.RouterID
		}
		flat = append(flat, map[string]any{
			// TsFmt with no zone, which renders UTC WITH a suffix. A file
			// outliving the session it was downloaded in has to say which zone
			// it is in; the page, which does not, formats differently on purpose.
			"ts":     reports.TsFmt(row.TS, ""),
			"actor":  row.ActorName,
			"ip":     deref(row.ActorIP),
			"action": row.Action,
			"target": firstNonEmpty(deref(row.TargetName), deref(row.TargetID)),
			"router": router,
			// The stored JSON verbatim, as the live export sends it. Whatever it
			// holds has already been through the recorder's redaction, so no
			// credential VALUE can be in it.
			"detail":  deref(row.Detail),
			"outcome": row.Outcome,
		})
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mikrodash-audit.csv"`)
	// reports.ToCSV, not a second CSV writer: the live app uses ONE `_toCsv` for
	// both exports, including its formula-injection prefix rule. An audit row
	// carries actor names and targets that came from a router, so that rule is
	// load-bearing here too.
	_, _ = w.Write([]byte(reports.ToCSV(flat, auditExportCols)))
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
