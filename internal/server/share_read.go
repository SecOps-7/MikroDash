package server

// The share read routes: GET /share/api/<route>, the REST reads a shared page
// makes, answered for a share link's viewer (the operator's ruling of
// 2026-11-20, docs/DECISIONS.md).
//
// ── AN ALLOW LIST, PER PAGE ─────────────────────────────────────────────────
//
// shareReads is the whole list. A route is answered only when it is on the
// list FOR THE PAGE the viewer's socket is showing; everything else gets the
// one refusal every share request gets. Nothing on the list holds a secret or
// a credential, reads the router on demand, or names another router: each
// answer is a database or memory read for the one router the socket shows.
//
// ── THE SOCKET DECIDES, NOT THE REQUEST ─────────────────────────────────────
//
// A read is answered only for a viewer whose socket is open (the ticket's
// viewer id, recorded by the socket), which holds REST reads to the viewer
// cap the socket enforces. The router and the page come from that socket;
// a routerId in the query is overwritten, never trusted. Every request also
// re-checks the link (revoked, expired, creator, routers, address), as every
// share request does. A share read never builds a Session and never sees a
// cookie.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/db"
	"mikrodash/internal/sitedoc"
	"mikrodash/internal/store"
)

// shareRead is one REST read a shared page may make.
type shareRead struct {
	// Path is the app's route, as the page's module calls it.
	Path string
	// Pages are the pages that make this read.
	Pages []string
	// Kinds, for /api/router-doc, are the document kinds those pages read.
	Kinds []string
	serve func(s *Server, w http.ResponseWriter, q url.Values, l *db.ShareLink, routerID string)
}

// shareReads is the allow list. TestTheShareReadListIsTheAllowList holds it:
// not empty, every path a real GET route of the app, every page shareable.
var shareReads = []shareRead{
	{Path: "/api/apps/history", Pages: []string{"apps"}, serve: (*Server).shareAppHistory},
	{Path: "/api/devices/overview", Pages: []string{"devices"}, serve: (*Server).shareDevicesOverview},
	{Path: "/api/interfaces/history", Pages: []string{"interfaces"}, serve: (*Server).shareInterfaceHistory},
	{Path: "/api/localcc", Pages: []string{"connections", "dashboard"}, serve: (*Server).shareLocalCC},
	{Path: "/api/wifi/history", Pages: []string{"wifi-clients"}, serve: (*Server).shareWifiHistory},
	{Path: "/api/wifi/history/status", Pages: []string{"wifi-clients"}, serve: (*Server).shareWifiHistoryStatus},
	{Path: "/api/router-doc", Pages: []string{"network-topology"}, Kinds: []string{sitedoc.KindTopologyLinks}, serve: (*Server).shareRouterDoc},
	{Path: "/api/router-doc", Pages: []string{"wan"}, Kinds: []string{sitedoc.KindWANUplinks}, serve: (*Server).shareRouterDoc},
	{Path: "/api/topology-layout", Pages: []string{"network-topology"}, serve: (*Server).shareTopologyLayout},
	// Built for pages not shared yet (the operator's ruling of 2026-11-20
	// narrowed sharing to six pages): add the entry back when its page is.
	//   /api/router-doc kind wifi-map and /api/router-doc/image (wifi-map).
	// Left off on purpose, should DNS be shared again: /api/dns/cache and
	// /api/dns/trace (they read the router on demand, and the cache shows
	// what people visited).
}

// shareReadsFor are the paths a page may read, for the shell's config: the
// shell sends these to /share/api and answers every other /api/ call itself.
func shareReadsFor(pagesShown []string) []string {
	out := []string{}
	for _, rd := range shareReads {
		for _, p := range pagesShown {
			if slices.Contains(rd.Pages, p) && !slices.Contains(out, rd.Path) {
				out = append(out, rd.Path)
			}
		}
	}
	return out
}

// findShareRead is the entry for path on page, kind (router-doc) considered.
func findShareRead(path, page, kind string) *shareRead {
	for i := range shareReads {
		rd := &shareReads[i]
		if rd.Path != path || !slices.Contains(rd.Pages, page) {
			continue
		}
		if len(rd.Kinds) > 0 && !slices.Contains(rd.Kinds, kind) {
			continue
		}
		return rd
	}
	return nil
}

var shareReadLimit = newRateLimiter(120, time.Minute)

func (s *Server) shareReadRoute(w http.ResponseWriter, r *http.Request) {
	shareHeaders(w)
	setShareFraming(w, nil)
	l, tk, reason := s.shareFromTicket(shareAuthTicket(r), r)
	if l == nil {
		s.shareDenied(r, "", reason)
		shareInvalid(w)
		return
	}
	// The viewer's socket must be open: that is the viewer cap, for REST.
	sc := s.shareLive.byViewer(l.ID, tk.Viewer)
	if sc == nil {
		s.shareDenied(r, l.ID, "no-open-view")
		shareInvalid(w)
		return
	}
	if ok, _, _ := shareReadLimit.take(l.ID + "|" + tk.Viewer); !ok {
		writeJSONErr(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	sc.mu.Lock()
	page, routerID := sc.page, sc.routerID
	sc.mu.Unlock()
	q := r.URL.Query()
	rd := findShareRead("/api/"+r.PathValue("route"), page, q.Get("kind"))
	if rd == nil || routerID == "" {
		s.shareDenied(r, l.ID, "route")
		shareInvalid(w)
		return
	}
	q.Set("routerId", routerID)
	rd.serve(s, w, q, l, routerID)
}

func (s *Server) shareInterfaceHistory(w http.ResponseWriter, q url.Values, _ *db.ShareLink, rid string) {
	iface := q.Get("interface")
	if iface == "" {
		writeJSONErr(w, http.StatusBadRequest, "routerId and interface required")
		return
	}
	s.serveInterfaceHistory(w, q.Get("range"), rid, iface, false)
}

// shareLocalCC is the local country OF THE SHARED ROUTER, never the
// install's active one (which may be a router the link does not show), and
// never the WAN address: a share is not an administrator.
func (s *Server) shareLocalCC(w http.ResponseWriter, _ url.Values, _ *db.ShareLink, rid string) {
	placeCC := ""
	if loc := s.routerPlace(rid); loc != nil {
		placeCC = loc.CC
	}
	writeJSON(w, localCCPayload(s.wanIPOf(rid), false, countryOf, placeCC))
}

func (s *Server) shareWifiHistory(w http.ResponseWriter, q url.Values, _ *db.ShareLink, rid string) {
	if !s.featureOn(store.FeatureWifiHistory) {
		writeJSONErr(w, http.StatusForbidden, "this feature is switched off in Settings -> Features")
		return
	}
	hours, _ := strconv.Atoi(q.Get("hours"))
	if hours < 1 || hours > 24*90 {
		hours = 24
	}
	to := time.Now().UnixMilli()
	rows, err := s.auditDB.WifiSamples(rid, q.Get("mac"), to-int64(hours)*3_600_000, to)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the history could not be read")
		return
	}
	writeJSON(w, map[string]any{"samples": rows, "from": to - int64(hours)*3_600_000, "to": to})
}

func (s *Server) shareWifiHistoryStatus(w http.ResponseWriter, _ url.Values, _ *db.ShareLink, rid string) {
	if !s.featureOn(store.FeatureWifiHistory) {
		writeJSONErr(w, http.StatusForbidden, "this feature is switched off in Settings -> Features")
		return
	}
	on, err := s.auditDB.WifiHistoryRouters()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the setting could not be read")
		return
	}
	writeJSON(w, map[string]any{"recording": on[rid]})
}

func (s *Server) shareRouterDoc(w http.ResponseWriter, q url.Values, _ *db.ShareLink, rid string) {
	kind := q.Get("kind")
	blob, err := s.auditDB.Doc(rid, kind)
	if err != nil {
		writeJSON500OK(w)
		return
	}
	doc, ok := sitedoc.Clean(kind, blob)
	if !ok {
		writeJSON400OK(w)
		return
	}
	writeJSON(w, map[string]any{"kind": kind, "routerId": rid, "doc": doc})
}

// shareTopologyLayout is the link creator's saved topology positions, else
// the shared ones: the layout the administrator saw when sharing.
func (s *Server) shareTopologyLayout(w http.ResponseWriter, _ url.Values, l *db.ShareLink, rid string) {
	positions := json.RawMessage(`{}`)
	all := map[string]json.RawMessage{}
	blob, err := s.auditDB.Layout(l.CreatedBy, "topology")
	if err != nil || blob == nil {
		blob, _ = s.auditDB.Layout(db.SharedLayoutUser, "topology")
	}
	if blob != nil && json.Unmarshal(blob, &all) == nil && len(all[rid]) > 0 {
		positions = all[rid]
	}
	writeJSON(w, map[string]any{"positions": positions})
}

// shareDevicesOverview is the shared router's connectivity strip, never the
// fleet's, and without its backups (the Backups page is never shared).
func (s *Server) shareDevicesOverview(w http.ResponseWriter, q url.Values, _ *db.ShareLink, rid string) {
	req := &http.Request{URL: &url.URL{RawQuery: q.Encode()}}
	from, to := overviewWindow(req, time.Now().UnixMilli())
	out, status, msg := s.deviceOverviewRows(from, to, func(id string) bool { return id == rid }, map[string]bool{})
	if status != 0 {
		writeJSONErr(w, status, msg)
		return
	}
	writeJSON(w, out)
}

// shareAppHistory is the shared router's per-app usage history.
func (s *Server) shareAppHistory(w http.ResponseWriter, q url.Values, _ *db.ShareLink, rid string) {
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
