package server

import (
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/resource"
)

// auditSink adapts the database to what internal/audit asks for.
//
// The two event types are deliberately separate rather than shared: internal/db
// knows nothing about auditing and internal/audit knows nothing about SQLite, so
// neither can grow a dependency on the other. The cost is this function, and it
// is a fair price for a dependency graph with no cycle in it.
type auditSink struct{ db *db.DB }

func (s auditSink) InsertAuditEvent(ev audit.DBEvent) error {
	return s.db.InsertAuditEvent(db.Event{
		TS: ev.TS, ActorID: ev.ActorID, ActorName: ev.ActorName, ActorIP: ev.ActorIP,
		Action: ev.Action, Scope: ev.Scope, RouterID: ev.RouterID,
		TargetType: ev.TargetType, TargetID: ev.TargetID, TargetName: ev.TargetName,
		Outcome: ev.Outcome, Detail: ev.Detail,
	})
}

// recorder returns the audit recorder for this connection's user.
//
// A nil audit database yields a recorder that records nothing, which is the
// right failure: the app has to be able to run against a /data whose database
// this build cannot open, and refusing to serve pages because the trail is
// unavailable would be worse than an incomplete trail. It is reported once at
// startup rather than per event.
// auditSystem records an event with no operator behind it.
//
// The `/raw` route is the only one that needs this: a router fetching a backup
// is not a session, so there is no username and no client IP to attribute. The
// live side uses `audit.system()` at exactly the same call sites.
func (s *Server) auditSystem(ev audit.Event) {
	var sink audit.Sink
	if s.auditDB != nil {
		sink = auditSink{s.auditDB}
	}
	audit.New(sink, audit.System(), nowMillis).Record(ev)
}

func (cn *conn) recorder() *audit.Recorder {
	var sink audit.Sink
	if cn.srv.auditDB != nil {
		sink = auditSink{cn.srv.auditDB}
	}
	name := ""
	if cn.sess != nil {
		name = cn.sess.Username
	}
	return audit.New(sink, audit.ForUser("", name, cn.clientIP), nowMillis)
}

// httpRecorder is the same recorder for an HTTP request rather than a socket.
//
// `clientIPOf` is shared with the WebSocket path deliberately: an audit column
// that says "the reverse proxy" for every user records nothing, and the rule for
// unwinding X-Forwarded-For must not differ between the two ways into this
// server — one of them would be wrong and nobody would notice which.
func (s *Server) httpRecorder(r *http.Request, sess *Session) *audit.Recorder {
	var sink audit.Sink
	if s.auditDB != nil {
		sink = auditSink{s.auditDB}
	}
	name := ""
	if sess != nil {
		name = sess.Username
	}
	return audit.New(sink, audit.ForUser(s.userIDFor(name), name, clientIPOf(r)), nowMillis)
}

// nowMillis is Date.now(): epoch MILLISECONDS, which is what every ts in this
// table already holds. Seconds would sort correctly and render as 1970.
func nowMillis() int64 { return time.Now().UnixMilli() }

// clientIPOf resolves the address to record for a WebSocket upgrade.
//
// X-Forwarded-For first, because this server is designed to sit behind a reverse
// proxy and RemoteAddr would then be the proxy for every user in the trail — an
// audit column saying the same thing for everybody records nothing. Only the
// FIRST entry is taken; the rest are whatever the client claimed on the way in.
//
// This mirrors what index.js does at connect rather than what Express's `trust
// proxy` does, because a socket's upgrade request never passes through that
// middleware — the same reason src/audit.js's fromSocket reads the handshake.
func clientIPOf(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); first != "" {
			return audit.NormalizeIP(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return audit.NormalizeIP(host)
}

// auditValues masks every secret-typed field BY TYPE, before anything is diffed.
//
// THIS IS THE FIRST LINE OF DEFENCE AND THE ONE THAT ACTUALLY HOLDS. audit's
// name matching is the second: it catches `routerPass` and `telegramBotToken`,
// but a resource field is named for the form, and `wpa2PreSharedKey` matches no
// credential pattern at all. What keeps a WPA passphrase or a WireGuard
// pre-shared key out of the trail is that the field DECLARES itself secret, and
// this reads that declaration.
//
// It matters most on the `after` side, which is worth saying plainly: `before`
// comes from RowValues, which already drops secrets because the router's stored
// value is never read back into the form. The value at risk is the one the
// operator just typed, and it arrives in the submitted values and nowhere else.
//
// Presence is still recorded — «set» or «unset» rather than omission — because
// "the passphrase was set" is exactly what an audit trail is for. Dropping the
// field would lose it.
//
// Mirrors _resAuditValues in src/index.js, and lives in the server package for
// the same reason it lives in index.js there: it is about how a write is
// recorded, not about what a resource is.
func auditValues(res *resource.Resource, values map[string]any) map[string]any {
	out := map[string]any{}
	if values == nil {
		return out
	}
	for _, f := range res.Fields {
		v, ok := values[f.Name]
		if !ok {
			continue
		}
		if f.Type == resource.TypeSecret {
			s, _ := v.(string)
			if s != "" {
				out[f.Name] = audit.Set
			} else {
				out[f.Name] = audit.Unset
			}
			continue
		}
		if f.Type == resource.TypeBool {
			// BOTH SIDES OF THE DIFF SAY THE SAME THING IN DIFFERENT WORDS.
			// RowValues gives a real boolean and Validate gives "yes"/"no", so
			// `false` against `"no"` read as a change and EVERY save of every
			// resource carrying a checkbox recorded one nobody made — noise in
			// the one table that cannot be pruned selectively, burying the edit
			// that did happen. A field the router omits entirely
			// (matchSubdomain on a DNS entry) is the same shape: null against
			// "no" is not a change either.
			//
			// The port found this, reported it, and the live side fixed it in
			// _resAuditValues; this is the re-sync. Normalised HERE rather than
			// in RowValues or Validate because those two feed the FORM and the
			// WRITE, and this is a reporting problem.
			out[f.Name] = v == true || v == "yes" || v == "true"
			continue
		}
		out[f.Name] = v
	}
	return out
}

// stringValuesAsAny lifts the validated form values into the shape Diff takes.
//
// The values stay STRINGS, including the "yes"/"no" a bool validates to, and
// that is not an oversight — see the note at the resSave call site. Converting
// them here would silently fix a live-app quirk the port is required to
// reproduce.
func stringValuesAsAny(v map[string]string) map[string]any {
	out := make(map[string]any, len(v))
	for k, s := range v {
		out[k] = s
	}
	return out
}

// ── the page gate ────────────────────────────────────────────────────────────

// canPage is the authorization every write and every page subscription passes
// through. It is TWO answers ANDed, and the order is the point.
//
//  1. Session.CanPage — the union Node computed and sent in `caps.pages`,
//     intersected with the readable-router list. Node's own answer, and a
//     SUPERSET of the truth wherever access varies between routers.
//  2. internal/rbac — the per-router answer, resolved from the grant graph
//     exactly as rbac.js's canPage resolves it.
//
// ANDing rather than substituting is a deliberate safety property: the resolver
// can only ever make the answer STRICTER, so a bug in the port cannot grant
// access Node would refuse. The worst a mistake in internal/rbac can do is deny
// something that should have been allowed, which is visible and complained
// about, rather than permit something that should not, which is neither.
//
// When the database could not be opened the resolver is unavailable and the
// coarse gate stands alone — which is the pre-existing over-permission, live
// again. That is a real degradation and it is logged once at startup rather
// than silently tolerated.
func (cn *conn) canPage(page, access string) bool {
	if cn.sess == nil {
		return false
	}
	// 'none' auth mode has no identity and every request is implicitly admin.
	// rbac.js keeps exactly one copy of this short circuit; so does the port.
	if cn.sess.AuthMode == "none" {
		return true
	}
	if !cn.sess.CanPage(page, access, cn.routerID) {
		return false
	}
	if !cn.srv.rbac.Available() {
		return true // the documented gap, reported at startup
	}
	ok, err := cn.srv.rbac.CanPage(cn.userID, page, access, cn.routerID)
	if err != nil {
		// An authorization question that cannot be answered is refused. A read
		// blanking is recoverable and visible; a write allowed by a failed
		// lookup is neither.
		log.Printf("[rbac] %s %s on %s: %v", access, page, cn.routerID, err)
		return false
	}
	return ok
}

// userIDFor maps a username to the id the grant graph is keyed by.
//
// /api/auth/status sends a username and never an id, on purpose. users.json —
// which this process already reads and decrypts — carries both, so the mapping
// costs nothing and needs no change to the live repo. Usernames are the login
// identifier and therefore unique; a miss means the record was deleted between
// the session being minted and this lookup, and answering "" makes every
// subsequent question fail closed.
func (s *Server) userIDFor(username string) string {
	if s.store == nil || username == "" {
		return ""
	}
	users, err := s.store.Users()
	if err != nil {
		log.Printf("[rbac] cannot read users.json: %v", err)
		return ""
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u.ID
		}
	}
	return ""
}
