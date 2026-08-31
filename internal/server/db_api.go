package server

// `GET /api/db/stats` and `POST /api/db/purge` — the database cleanup card.
//
// ── THE PERMISSION IS PER SCOPE, AND A GLOBAL PURGE IS NOT A BIG ROUTER ONE ──
//
// The live comment on `_purgeScope`: "A global purge deletes history for every
// router, including ones the caller may not even be able to see, so it is a
// system-level action rather than a scoped one."
//
// So there are two different permissions and they are not ordered:
//
//	no routerId   `system:db`      — every router, including invisible ones
//	a routerId    `router:purge`   — on that router
//
// A restricted admin with `router:purge` on one router therefore CANNOT purge
// everything, and the refusal names the way out — "Select a router" — rather
// than simply saying no.
//
// ── THE PREVIEW RUNS THE SAME PREDICATE AS THE DELETE ───────────────────────
//
// `dryRun` answers `CountPurge` with the options the real purge would use, which
// is the point: the "this will delete N rows" the operator confirms is exact
// rather than an estimate. `internal/db`'s `purgeWhere` is one function for the
// same reason.
//
// ── THE AUDIT ROW SURVIVES THE PURGE IT DESCRIBES ──────────────────────────
//
// `audit_events` is absent from `PURGE_TABLES`, and the live comment says that
// is deliberate: "this row survives the very purge it describes — which is the
// point of keeping it out."
//
// ── STATS IS FILTERED PER VIEWER, AND ONLY THE PER-ROUTER HALF ─────────────
//
// `s.byRouter = s.byRouter.filter(r => readable.has(r.routerId))`. The totals,
// the byType counts and the file size are NOT filtered — a restricted admin sees
// the true size of the database and only loses the breakdown naming routers they
// cannot read.

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/safe"
)

// purgeAges is the live `_PURGE_AGES`, in days. ZERO means "everything,
// regardless of age" — see `db.purgeWhere`.
var purgeAges = []float64{0, 1, 7, 30, 90, 365}

func (s *Server) registerDBAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/db/stats", s.dbGuard(s.dbStats))
	mux.HandleFunc("POST /api/db/purge", s.dbGuard(s.dbPurge))
}

// dbGuard is `Rbac.requireGlobalAdmin` on both routes.
//
// THE ROUTE GATE AND THE SCOPE GATE ARE DIFFERENT QUESTIONS. This one is "may
// you use this card at all"; `purgeOpts` below is "may you purge THIS". The live
// app gates both routes on global admin and then checks the scope separately,
// and collapsing them would let a global admin skip the scope check — which is
// where `system:db` versus `router:purge` lives.
func (s *Server) dbGuard(h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Validate(r.Header.Get("Cookie"))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if !s.isGlobalAdmin(sess) {
			writeJSONErr(w, http.StatusForbidden, "Administrator access required")
			return
		}
		if s.auditDB == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		h(w, r, sess)
	}
}

func (s *Server) dbStats(w http.ResponseWriter, _ *http.Request, sess *Session) {
	st, err := s.auditDB.Stats()
	if err != nil {
		log.Printf("[db] stats: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the database statistics")
		return
	}

	// ONLY byRouter IS FILTERED. See the file header: the totals and the file
	// size are the true ones, so a restricted admin is not misled about how much
	// history the install holds — they simply do not get the breakdown naming
	// routers they cannot read.
	//
	// ── IT CANNOT CURRENTLY FIRE PARTIALLY, AND THAT WAS MEASURED ──────────
	//
	// Removing this filter entirely survives the suite. Not for want of a
	// fixture: the route above is gated on `isGlobalAdmin`, `system:principals`
	// is GlobalOnly and stripped from every projected role, so only a BUILTIN
	// role confers it — and a builtin role held globally confers `router:read`
	// on the whole fleet. Anyone who reaches this line therefore sees every
	// router anyway.
	//
	// It stays because the OTHER direction is live: `visibleRouters` returns an
	// EMPTY set (not nil) when the resolver errors, and an empty set hides the
	// breakdown rather than showing it. Deleting the filter would turn an RBAC
	// failure into a disclosure.
	//
	// The premise is pinned by `TestAGlobalAdminCanAlwaysReadTheWholeFleet` in
	// internal/rbac, which fails the day a page is wired to confer
	// `system:principals` — and this paragraph becomes wrong at the same moment
	// rather than quietly ageing into a false claim.
	if visible := s.visibleRouters(sess); visible != nil {
		kept := make([]db.RouterRows, 0, len(st.ByRouter))
		for _, r := range st.ByRouter {
			if visible[r.RouterID] {
				kept = append(kept, r)
			}
		}
		st.ByRouter = kept
	}

	writeJSON(w, map[string]any{
		"ok": true, "bytes": st.Bytes, "total": st.Total, "byType": st.ByType,
		"oldestTs": st.OldestTS, "byRouter": st.ByRouter,
	})
}

func (s *Server) dbPurge(w http.ResponseWriter, r *http.Request, sess *Session) {
	body, err := readBodyMap(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	opts, msg := s.purgeOpts(body, sess)
	if msg != "" {
		writeJSONErr(w, http.StatusBadRequest, msg)
		return
	}

	// Read ONCE and threaded through, so the preview and the delete cannot use
	// different cutoffs — the live code calls `Date.now()` inside `_purgeWhere`,
	// where a slow request could straddle a millisecond.
	now := time.Now().UnixMilli()

	// THE DRY RUN. Same options, same predicate, no writes and no audit row — a
	// preview is not an action.
	if dry, _ := body["dryRun"].(bool); dry {
		counts, err := s.auditDB.CountPurge(opts, now)
		if err != nil {
			log.Printf("[db] purge preview: %v", err)
			writeJSONErr(w, http.StatusInternalServerError, "could not count the rows")
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "dryRun": true, "total": counts.Total, "byType": counts.ByType,
		})
		return
	}

	before, err := s.auditDB.Stats()
	if err != nil {
		log.Printf("[db] stats before purge: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the database statistics")
		return
	}

	deleted, err := s.auditDB.Purge(opts, now)
	if err != nil {
		log.Printf("[db] purge: %v", err)
		writeJSONErr(w, http.StatusInternalServerError,
			safe.Message("could not purge: "+err.Error()))
		return
	}

	// RECORDED BEFORE THE VACUUM, and the row outlives the purge it describes —
	// `audit_events` is absent from the purge tables on purpose.
	ev := audit.Event{
		Action: "db.purge", TargetType: "database",
		Extra: []audit.KV{
			{Key: "deleted", Value: deleted},
			{Key: "types", Value: typesLabel(opts.Types)},
			{Key: "olderThanDays", Value: body["olderThanDays"]},
		},
		RouterID: opts.RouterID,
	}
	s.httpRecorder(r, sess).Record(ev)

	// ONLY WHEN SOMETHING WENT. The live line is
	// `result.deleted > 0 ? db.vacuum() : { before, after: before }` — a VACUUM
	// rewrites the whole file, which is not free, and a purge that matched
	// nothing has nothing to reclaim.
	bytesBefore, bytesAfter := before.Bytes, before.Bytes
	if deleted > 0 {
		b, a, verr := s.auditDB.Vacuum()
		if verr != nil {
			// THE ROWS ARE ALREADY GONE. A failed vacuum means the file did not
			// shrink, not that the purge failed — reporting an error here would
			// invite the operator to run it again.
			log.Printf("[db] vacuum after purge: %v", verr)
		} else {
			bytesBefore, bytesAfter = b, a
		}
	}

	who := "local"
	if sess != nil && sess.Username != "" {
		who = sess.Username
	}
	log.Printf("[db] purge by %s: %d rows", who, deleted)

	writeJSON(w, map[string]any{
		"ok": true, "deleted": deleted,
		"bytesBefore": bytesBefore, "bytesAfter": bytesAfter,
	})
}

// purgeOpts is `_purgeOpts` and `_purgeScope` together: what the request means,
// or the message explaining why it is refused.
//
// THE ORDER IS THE LIVE ONE — scope, then types, then age — so a request that is
// wrong in more than one way reports the FIRST, and `tools/purge-cases.js`
// carries a case that pins exactly that.
func (s *Server) purgeOpts(body map[string]any, sess *Session) (db.PurgeOpts, string) {
	// `String((req.body && req.body.routerId) || '').trim()` — an absent key, a
	// null and a string of spaces all read as "every router".
	routerID := ""
	if v, ok := body["routerId"]; ok && v != nil {
		routerID = strings.TrimSpace(bodyString(v))
	}

	// ── THE SCOPE ───────────────────────────────────────────────────────
	if sess != nil && sess.AuthMode != "none" && s.rbac != nil {
		uid := s.userIDFor(sess.Username)
		perm, scope, refusal := "system:db", "", "Select a router — your account cannot purge all routers"
		if routerID != "" {
			perm, scope, refusal = "router:purge", routerID, "Router not permitted"
		}
		ok, err := s.rbac.Can(uid, perm, scope)
		if err != nil {
			log.Printf("[db] permission check: %v", err)
			// AN ERROR IS NOT A YES. The same rule `visibleRouters` follows:
			// failing to answer must not read as permission granted.
			return db.PurgeOpts{}, "could not check your permissions"
		}
		if !ok {
			return db.PurgeOpts{}, refusal
		}
	}

	// ── THE TYPES ───────────────────────────────────────────────────────
	//
	// An ABSENT list means every type. A list that WAS sent and survives
	// filtering as empty is refused, because the alternative is silently
	// widening a request for one type into a request for all of them.
	var types []string
	if raw, sent := stringListFrom(body["types"]); sent {
		for _, t := range raw {
			for _, known := range db.PurgeTypes {
				if t == known {
					types = append(types, t)
					break
				}
			}
		}
		if len(types) == 0 {
			return db.PurgeOpts{}, "No valid data types selected"
		}
	}

	// ── THE AGE ─────────────────────────────────────────────────────────
	//
	// Only the presets the UI offers. `Number(undefined)` is NaN and NaN is in
	// no list, so an ABSENT age is refused too — which is what stops a caller
	// omitting it and getting "everything" by accident.
	days, ok := jsNumber(body["olderThanDays"])
	if !ok || !isPurgeAge(days) {
		return db.PurgeOpts{}, "Invalid age filter"
	}

	return db.PurgeOpts{
		RouterID:    routerID,
		Types:       types,
		OlderThanMs: int64(days) * 86400000,
	}, ""
}

func isPurgeAge(d float64) bool {
	for _, a := range purgeAges {
		if a == d {
			return true
		}
	}
	return false
}

// jsNumber is `Number(v)` for the shapes a JSON body carries.
//
// The live check is `_PURGE_AGES.includes(Number(body.olderThanDays))`, so `"7"`
// is accepted and `"week"` is not — and neither is an ABSENT value, because
// `Number(undefined)` is NaN and NaN is in no list.
//
// `Number("")` IS ZERO, though, and zero is a preset — so an empty string is
// accepted as "everything, regardless of age". Reproduced rather than tightened:
// it is what the live route does, and a port that refused it would reject a
// request the app it replaces accepts.
func jsNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case bool:
		// `Number(true)` is 1, which IS a preset. Faithful, and reachable only
		// from a hand-written request.
		if t {
			return 1, true
		}
		return 0, true
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		// nil, an object, an array: `Number` gives NaN for all but a
		// single-element numeric array, which nothing sends.
		return 0, false
	}
}

// typesLabel is the live `opts.types || 'all'` in the audit row.
func typesLabel(types []string) any {
	if len(types) == 0 {
		return "all"
	}
	return types
}
