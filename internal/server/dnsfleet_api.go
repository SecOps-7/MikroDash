package server

// `/api/dns/fleet` — the same static entries, read across several routers.
//
// ── WHY THIS IS NOT THE DNS COLLECTOR ───────────────────────────────────────
//
// A collector belongs to a session and a session belongs to one router. The
// question this answers is about SEVERAL — "is that record on both of them, and
// does it say the same thing" — which no single session can answer. So it reads
// each router directly, on demand, from a hold it takes and gives back.
//
// ON DEMAND, NOT POLLED. The comparison is something an operator asks for while
// they are looking at it; polling every router's DNS table in the background
// would be a standing cost for a page nobody is on. A hold is taken per router
// for the length of the read and dropped after — `Retain` builds the session if
// there is not one already and the grace timer takes it down again.
//
// ── THE WRITE PATH IS THE SAME ONE, MINUS THE SOCKET ────────────────────────
//
// `resSave` in resource.go is bound to the connection's ACTIVE router: its
// permission check, its audit recorder and its error channel are all `cn`. It
// cannot be pointed at a second router without being rebuilt around one, so the
// add here re-uses the parts that carry the RULES — the resource descriptor's
// `Validate` and `BuildArgs` — and states the rest explicitly: an RBAC check per
// router, a read before the write, and an audit row per router.
//
// THAT IS ONLY SAFE BECAUSE `dnsStatic` DECLARES NO GUARD. A guarded resource
// would lose its guard down this path, which is why the endpoint refuses to
// write anything else: the resource is named here, not taken from the request.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/topology"
)

// dnsFleetHold is the reason this endpoint's session holds carry, so a stuck
// one can be explained by name — see session.Manager.Retain.
const dnsFleetHold = "dns-fleet"

// fleetMaxRouters bounds one request. A fleet larger than this is a fine thing
// to have and not a thing to read serially in one HTTP call. Shared with the
// topology's peer merge, which reads the same way for the same reason.
const fleetMaxRouters = 16

// dnsFleetEntry is one record, with everything needed to COPY it.
//
// ── THE DISPLAY SHAPE IS NOT ENOUGH TO RECREATE A RECORD ────────────────────
//
// `collect.DNSStaticEntry` collapses nine type-specific properties into one
// `address` column, which is right for a table and lossless for nothing: an MX
// record's preference and an SRV record's port, priority and weight are not in
// it at all. Copying from that would quietly drop them.
//
// `values` is the resource descriptor's own view of the row — the same map the
// edit form is filled from — so a copy carries every field the form has.
type dnsFleetEntry struct {
	collect.DNSStaticEntry
	Values map[string]any `json:"values"`
}

type dnsFleetRouter struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	// Error is why this router contributed nothing. Present rather than dropped:
	// a router silently missing from the comparison reads as a router with no
	// records, which is the opposite of the truth.
	Error   string          `json:"error"`
	Entries []dnsFleetEntry `json:"entries"`
}

func (s *Server) registerDNSFleet(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dns/fleet", s.dnsFleetGet)
	mux.HandleFunc("POST /api/dns/fleet-add", s.dnsFleetAdd)
}

// fleetTargets resolves the requested ids against the stored fleet, keeping only
// those this caller may reach the named page on.
//
// ── A ROUTER THE CALLER MAY NOT SEE IS DROPPED, NOT REFUSED ─────────────────
//
// Refusing the whole request would tell the caller that an id they guessed
// exists, which is the cross-router probe issue #108 closed elsewhere. Dropping
// it answers about exactly the routers they already have access to.
//
// THE PAGE IS THE PERMISSION. A caller allowed to read DNS across the fleet is
// not thereby allowed to read everybody's neighbour tables, so each endpoint
// names its own page and the grant is checked per router.
func (s *Server) fleetTargets(sess *Session, ids []string, page, access string) []struct{ ID, Label string } {
	known := map[string]string{}
	all, _ := s.store.Routers()
	for _, r := range all {
		if !r.Disabled {
			// THE ADDRESS WHEN THERE IS NO LABEL. A router nobody named reached
			// the fleet table's column heading and the Add dialog's picker as its
			// UUID, which says nothing about which box is about to be written to.
			known[r.ID] = firstNonEmpty(r.Label, firstNonEmpty(r.Host, r.ID))
		}
	}
	uid := s.userIDFor(sess.Username)
	out := []struct{ ID, Label string }{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || !topology.IsValidRouterID(id) {
			continue
		}
		seen[id] = true
		label, ok := known[id]
		if !ok {
			continue
		}
		if !permitted(s.rbac.CanPage(uid, page, access, id)) {
			continue
		}
		if len(out) >= fleetMaxRouters {
			break
		}
		out = append(out, struct{ ID, Label string }{id, label})
	}
	return out
}

func (s *Server) dnsFleetGet(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	ids := strings.Split(r.URL.Query().Get("routers"), ",")
	targets := s.fleetTargets(sess, ids, "dns", "read")

	out := make([]dnsFleetRouter, 0, len(targets))
	for _, t := range targets {
		row := dnsFleetRouter{ID: t.ID, Label: t.Label, Entries: []dnsFleetEntry{}}
		sn, err := s.sessions.Retain(t.ID, dnsFleetHold)
		if err != nil || sn == nil {
			row.Error = "unreachable"
			out = append(out, row)
			continue
		}
		rows, rerr := sn.Exec(collect.DNSStaticCmd())
		s.sessions.Drop(t.ID, dnsFleetHold)
		if rerr != nil {
			row.Error = safe.Message(rerr.Error())
			out = append(out, row)
			continue
		}
		row.OK = true
		row.Entries = dnsFleetEntries(rows)
		out = append(out, row)
	}
	writeJSON(w, map[string]any{"routers": out})
}

type dnsFleetAddResult struct {
	ID   string `json:"id"`
	OK   bool   `json:"ok"`
	Code string `json:"code"`
}

func (s *Server) dnsFleetAdd(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	var body struct {
		RouterIDs []string          `json:"routerIds"`
		Values    map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		writeJSON400OK(w)
		return
	}
	// THE RESOURCE IS NAMED HERE, NOT TAKEN FROM THE REQUEST. See the header:
	// this path has no guard chain, so it may only ever write the one resource
	// that declares no guard.
	res := resource.DNSStatic
	validated, errs := res.Validate(body.Values, false)
	if len(errs) > 0 {
		writeJSON(w, map[string]any{"ok": false, "code": "invalid", "errors": errs})
		return
	}
	name := validated.Values["name"]

	targets := s.fleetTargets(sess, body.RouterIDs, "dns", "write")
	if len(targets) == 0 {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	results := make([]dnsFleetAddResult, 0, len(targets))
	for _, t := range targets {
		results = append(results, s.dnsFleetAddOne(r, sess, res, validated, name, t.ID))
	}
	writeJSON(w, map[string]any{"ok": true, "results": results})
}

// dnsFleetAddOne writes one record to one router, and records what happened.
func (s *Server) dnsFleetAddOne(r *http.Request, sess *Session, res *resource.Resource,
	validated resource.Validated, name, routerID string) dnsFleetAddResult {

	sn, err := s.sessions.Retain(routerID, dnsFleetHold)
	if err != nil || sn == nil {
		return dnsFleetAddResult{ID: routerID, Code: "unreachable"}
	}
	defer s.sessions.Drop(routerID, dnsFleetHold)

	out := dnsFleetAddResult{ID: routerID}
	werr := sn.InWriteQueue(func() error {
		rows, rerr := sn.Exec(collect.DNSStaticCmd())
		if rerr != nil {
			out.Code = "read-failed"
			return nil
		}
		// ALREADY THERE IS NOT AN ERROR, and saying so is the point of this
		// endpoint: "synchronise" pressed twice must not produce two records, and
		// a router that already agrees is the answer the operator wanted.
		for _, row := range rows {
			if row["name"] == name && row["type"] == validated.Values["type"] {
				out.OK = true
				out.Code = "already-present"
				return nil
			}
		}
		if _, aerr := sn.Exec(routeros.Cmd{
			Path: res.Menu + "/add", Args: res.BuildArgs(validated)}); aerr != nil {
			out.Code = writeFailCode(aerr)
			return nil
		}
		out.OK = true
		out.Code = "added"
		s.httpRecorder(r, sess).Record(audit.Event{
			Action: res.Key + ".create", TargetType: res.Key, TargetName: name,
			RouterID: routerID, Note: "copied from another router",
		})
		return nil
	})
	if werr != nil {
		out.OK = false
		out.Code = writeFailCode(werr)
	}
	return out
}

// dnsFleetEntries projects one router's rows for the comparison.
//
// THE DISPLAY HALF AND THE COPY HALF COME FROM THE SAME ROW, joined by `.id`, so
// the two cannot describe different records. Sorted by name and then type, which
// is the key the page lines the columns up on — a record has to sit on the same
// line in every router's column or the comparison is unreadable.
func dnsFleetEntries(rows []routeros.Reply) []dnsFleetEntry {
	shown := collect.ParseStaticEntries(rows)
	raw := map[string]map[string]string{}
	for _, r := range rows {
		if id := r[".id"]; id != "" {
			raw[id] = r
		}
	}
	out := make([]dnsFleetEntry, 0, len(shown))
	for _, e := range shown {
		out = append(out, dnsFleetEntry{
			DNSStaticEntry: e,
			Values:         resource.DNSStatic.RowValues(raw[e.ID]),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Type < b.Type
	})
	return out
}
