package server

// How a share link is viewed: /s/<token>, the view ticket, the share REST
// routes and the sensor feed. The plan is the #165 share-links plan; the
// rules are in internal/sharelink.
//
// ── A SHARE IS NOT A SESSION ────────────────────────────────────────────────
//
// Nothing here builds a Session for a visitor or calls s.auth.Validate. The
// token opens these routes and nothing else; every other route ignores it.
// The view ticket (an HMAC over the link id and a short expiry) only says
// WHICH link a view was opened through: every request re-checks the link.
//
// ── ONE ANSWER FOR EVERY REFUSAL ────────────────────────────────────────────
//
// An unknown, expired or revoked link, a refused address, and a creator who
// is no longer an administrator all get the same status and the same page,
// so a visitor learns nothing about why. Only the audit trail says.
//
// ── FRAMING ─────────────────────────────────────────────────────────────────
//
// A share document may be framed only by the origins its link lists
// (frame-ancestors); otherwise it says 'none' and X-Frame-Options DENY.
// setShareFraming also offers the value to a wrapping writer that implements
// AllowFrameAncestors, which is how the app-wide framing guard (a separate
// change, fix/frame-guard) is to let a share response through while every
// other response keeps 'none'.

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/pages"
	"mikrodash/internal/sharelink"
)

func (s *Server) registerShareView(mux *http.ServeMux) {
	open := newRateLimiter(20, time.Minute).limit
	mux.HandleFunc("GET /s/{token}", open(s.shareOpen))
	sockets := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("GET /share/ws", sockets(s.handleShareWS))
	mux.HandleFunc("POST /share/api/ticket", s.shareTicketRenew)
	mux.HandleFunc("GET /share/api/feed", s.shareFeed)
	// The read routes a shared page makes: an allow list, see share_read.go.
	mux.HandleFunc("GET /share/api/{route...}", s.shareReadRoute)
	mux.Handle("GET /share.js", s.shareAsset("share.js"))
	// The app's stylesheet, which /app.css serves only to a signed-in browser.
	mux.Handle("GET /share.css", s.shareAsset("app.css"))
}

// frameAncestorsAllower is implemented by a response writer that enforces a
// framing policy of its own (fix/frame-guard's writer, once merged): it lets
// a share response carry its link's frame-ancestors.
type frameAncestorsAllower interface {
	AllowFrameAncestors(policy string)
}

// setShareFraming sets a share response's framing headers from its link.
func setShareFraming(w http.ResponseWriter, origins []string) {
	policy := sharelink.FrameAncestors(origins)
	h := w.Header()
	h.Set("Content-Security-Policy", policy)
	if policy == "frame-ancestors 'none'" {
		h.Set("X-Frame-Options", "DENY")
	} else {
		// X-Frame-Options cannot list origins (its ALLOW-FROM is ignored by
		// current browsers), and frame-ancestors overrides it where both are
		// present: so it is not sent when origins are allowed.
		h.Del("X-Frame-Options")
	}
	for rw := w; rw != nil; {
		if a, ok := rw.(frameAncestorsAllower); ok {
			a.AllowFrameAncestors(policy)
			break
		}
		u, ok := rw.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		rw = u.Unwrap()
	}
}

// shareHeaders are set on every share response.
func shareHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}

// ── The check every share request runs ──────────────────────────────────────

// shareLinkCheck loads a link and decides whether it may be used now from
// this address.
func (s *Server) shareLinkCheck(id, ip string) (*db.ShareLink, string) {
	if s.auditDB == nil || id == "" {
		return nil, "unknown"
	}
	l, err := s.auditDB.ShareLinkByID(id)
	if err != nil || l == nil {
		return nil, "unknown"
	}
	return s.shareCheckLink(l, ip)
}

// shareCheckLink decides whether a link may be used now from this address. A
// link whose creator is gone or no longer a global administrator, or one of
// whose routers was deleted, is revoked HERE (the at-use half of
// auto-revocation) and its live viewers are ended.
func (s *Server) shareCheckLink(l *db.ShareLink, ip string) (*db.ShareLink, string) {
	routersExist := true
	for _, rid := range l.RouterIDs() {
		if !s.shareRouterOK(rid) {
			routersExist = false
		}
	}
	// The CREATOR is asked about, by user id, straight of the grant graph: no
	// session is built here, for the visitor or for anyone else.
	exists := s.userIDFor(s.usernameFor(l.CreatedBy)) == l.CreatedBy
	isAdmin := false
	if exists && s.rbac != nil {
		ok, err := s.rbac.Can(l.CreatedBy, "system:principals", "")
		isAdmin = err == nil && ok
	}
	reason := sharelink.Check(sharelink.State{
		Revoked: l.RevokedAt != nil, ExpiresAt: l.ExpiresAt,
		CreatorExists: exists, CreatorIsAdmin: isAdmin, RoutersExist: routersExist,
	}, time.Now())
	switch reason {
	case "":
	case sharelink.ReasonCreatorDeleted, sharelink.ReasonCreatorNotAdmin, sharelink.ReasonRouterDeleted:
		s.shareAutoRevoke(*l, reason)
		return nil, reason
	default:
		return nil, reason
	}
	if !shareSubjectShareable(*l) {
		return nil, "not-shareable"
	}
	if !sharelink.AddressAllowed(l.AllowedCIDRs, ip) {
		return nil, "address"
	}
	return l, ""
}

// shareSubjectShareable reports whether what a link shows may still be
// shared: its page, its card, at least one of its slides, or at least one of
// its feed's values. The operator widens and narrows the shared pages
// (internal/pages); a link whose subject he has since stopped sharing is
// refused while that lasts, NOT revoked, so it works again if he shares the
// page again. Settings > Shared links says so on the link.
func shareSubjectShareable(l db.ShareLink) bool {
	switch sharelink.Kind(l.Kind) {
	case sharelink.KindPage:
		return pages.IsShareable(l.Page)
	case sharelink.KindCard:
		return sharelink.CardShareable(shareCardOf(l))
	case sharelink.KindSlideshow:
		for _, sl := range l.Slides {
			if pages.IsShareable(sl.Page) {
				return true
			}
		}
		return false
	case sharelink.KindFeed:
		var o sharelink.Options
		_ = json.Unmarshal([]byte(l.Options), &o)
		for _, v := range o.Values {
			if sharelink.FeedValueAllowed(v) {
				return true
			}
		}
		return false
	}
	return false
}

// shareAutoRevoke revokes a link for a reason no administrator chose, records
// it as the system, and ends its live viewers. Revocation is final.
func (s *Server) shareAutoRevoke(l db.ShareLink, reason string) {
	ok, err := s.auditDB.RevokeShareLink(l.ID, "", reason, time.Now().UnixMilli())
	if err != nil || !ok {
		return
	}
	s.endShare(l.ID, reason)
	s.shareAudit(audit.New(auditSink{s.auditDB}, audit.System(), nowMillis), "share.revoke", l,
		"revoked automatically: "+reason)
}

// sweepShareLinks is the write-time half of auto-revocation: after a write
// that can end a link's validity (a user deleted, a grant, group or role
// changed, a router deleted), every active link is checked, and one whose
// creator is gone or no longer a global administrator, or that shows a
// deleted router, is revoked and its viewers ended. The at-use check in
// shareCheckLink catches anything a sweep did not run for.
func (s *Server) sweepShareLinks() {
	if s.auditDB == nil {
		return
	}
	links, err := s.auditDB.ShareLinks()
	if err != nil {
		return
	}
	for i := range links {
		l := links[i]
		if l.RevokedAt != nil {
			continue
		}
		// The address check plays no part in a sweep: "" is not a visitor.
		s.shareCheckLink(&l, "")
	}
}

// shareFromTicket resolves a view ticket to a link that may be used now, and
// says which viewer the ticket was issued to.
func (s *Server) shareFromTicket(ticket string, r *http.Request) (*db.ShareLink, sharelink.Ticket, string) {
	tk, ok := sharelink.VerifyTicket(s.shareKey, ticket, time.Now())
	if !ok {
		return nil, tk, "ticket"
	}
	l, reason := s.shareLinkCheck(tk.LinkID, clientIPOf(r))
	return l, tk, reason
}

// shareDenied records a refused share request, through the shared limiter.
func (s *Server) shareDenied(r *http.Request, linkID, reason string) {
	var sink audit.Sink
	if s.auditDB != nil {
		sink = auditSink{s.auditDB}
	}
	rec := audit.New(sink, audit.Actor{Name: "share", IP: audit.NormalizeIP(clientIPOf(r))}, nowMillis).
		WithDeniedLimit(s.deniedLimit)
	routerID := ""
	if linkID != "" && s.auditDB != nil {
		if l, _ := s.auditDB.ShareLinkByID(linkID); l != nil && len(l.RouterIDs()) > 0 {
			routerID = l.RouterIDs()[0]
		}
	}
	rec.Denied(audit.Event{Action: "share.denied", RouterID: routerID, TargetType: "share-link",
		TargetID: linkID, Note: reason})
}

// shareInvalid is the one answer to every refusal of a share page.
func shareInvalid(w http.ResponseWriter) {
	shareHeaders(w)
	setShareFraming(w, nil)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(shareInvalidHTML))
}

const shareInvalidHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>MikroDash</title>
<style>body{margin:0;font-family:system-ui,sans-serif;background:#0f172a;color:#cbd5e1;
display:flex;align-items:center;justify-content:center;min-height:100vh}
p{max-width:28rem;text-align:center;line-height:1.5;padding:0 16px}</style></head>
<body><p>This shared link is no longer valid. Ask whoever shared it for a new one.</p></body></html>`

// ── /s/<token> ──────────────────────────────────────────────────────────────

// shareConfig is what the share shell is handed: the ticket, and what to show.
type shareConfig struct {
	Ticket    string            `json:"ticket"`
	TicketExp int64             `json:"ticketExp"`
	Kind      string            `json:"kind"`
	Label     string            `json:"label"`
	Page      string            `json:"page"`
	Router    string            `json:"router"`
	Layout    json.RawMessage   `json:"layout,omitempty"`
	Options   sharelink.Options `json:"options"`
	Slides    []shareSlideView  `json:"slides"`
	ExpiresAt *int64            `json:"expiresAt"`
	Full      bool              `json:"full"` // the viewer cap is reached
	// Reads are the /api/ paths the link's pages may read through
	// /share/api (share_read.go); the shell answers every other one itself.
	Reads []string `json:"reads"`
	// Cards are the Dashboard cards a shared Dashboard may draw: those whose
	// data comes only from shared pages. The server feeds no other card.
	Cards []string `json:"cards"`
}

type shareSlideView struct {
	Page      string          `json:"page"`
	Router    string          `json:"router"`
	Secs      int             `json:"secs"`
	Shareable bool            `json:"shareable"`
	Layout    json.RawMessage `json:"layout,omitempty"`
}

func (s *Server) shareRouterLabel(id string) string {
	list, err := s.store.RouterRecords()
	if err != nil {
		return ""
	}
	for _, r := range list {
		if r.ID == id {
			if r.Label != "" {
				return r.Label
			}
			return r.Host
		}
	}
	return ""
}

func rawOrNil(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

func (s *Server) shareOpen(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !sharelink.WellFormedToken(token) || s.auditDB == nil {
		s.shareDenied(r, "", "unknown")
		shareInvalid(w)
		return
	}
	l, err := s.auditDB.ShareLinkByHash(sharelink.HashToken(token))
	if err != nil || l == nil || !sharelink.Kind(l.Kind).Viewable() {
		s.shareDenied(r, "", "unknown")
		shareInvalid(w)
		return
	}
	if ok, reason := s.shareCheckLink(l, clientIPOf(r)); ok == nil {
		s.shareDenied(r, l.ID, reason)
		shareInvalid(w)
		return
	}
	now := time.Now()
	exp := now.Add(sharelink.TicketTTL)
	cfg := shareConfig{
		Ticket: sharelink.SignTicket(s.shareKey, l.ID, sharelink.NewViewer(), exp), TicketExp: exp.UnixMilli(),
		Kind: l.Kind, Label: l.Label, Page: l.Page, Router: s.shareRouterLabel(l.RouterID),
		Layout: rawOrNil(l.Layout), ExpiresAt: l.ExpiresAt, Slides: []shareSlideView{},
	}
	_ = json.Unmarshal([]byte(l.Options), &cfg.Options)
	viewers := l.ViewerCap
	if viewers <= 0 {
		viewers = sharelink.DefaultViewerCap
	}
	cfg.Full = s.shareLive.count(l.ID) >= viewers
	for _, sl := range l.Slides {
		cfg.Slides = append(cfg.Slides, shareSlideView{Page: sl.Page, Router: s.shareRouterLabel(sl.RouterID),
			Secs: sl.Seconds, Shareable: pages.IsShareable(sl.Page), Layout: rawOrNil(sl.Layout)})
	}
	shown := []string{l.Page}
	for _, sl := range l.Slides {
		shown = append(shown, sl.Page)
	}
	if sharelink.Kind(l.Kind) == sharelink.KindCard {
		shown = nil // a card is fed by its socket alone
	}
	cfg.Reads = shareReadsFor(shown)
	cfg.Cards = []string{}
	for _, c := range sharelink.ShareableCards() {
		cfg.Cards = append(cfg.Cards, c.ID)
	}
	_ = s.auditDB.TouchShareLink(l.ID, now.UnixMilli())
	s.shareAudit(audit.New(auditSink{s.auditDB}, audit.Actor{Name: "share:" + l.Label,
		IP: audit.NormalizeIP(clientIPOf(r))}, nowMillis), "share.open", *l, "a new viewer")
	shareHeaders(w)
	setShareFraming(w, l.FrameOrigins)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.shareShell(cfg))
}

// shareShell is web/dist/share.html with the config inlined where the
// placeholder is. json.Marshal escapes <, > and &, so the config cannot close
// the script element it sits in.
func (s *Server) shareShell(cfg shareConfig) []byte {
	b, _ := json.Marshal(cfg)
	var shell []byte
	if s.webDir != "" {
		shell, _ = os.ReadFile(filepath.Join(s.webDir, "share.html"))
	}
	if len(shell) == 0 || !bytes.Contains(shell, []byte("<!--SHARE-CONFIG-->")) {
		return []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` +
			html.EscapeString(cfg.Label) + `</title></head><body><script id="share-config" type="application/json">` +
			string(b) + `</script></body></html>`)
	}
	// The marker sits inside share.html's own #share-config element.
	return bytes.Replace(shell, []byte("<!--SHARE-CONFIG-->"), b, 1)
}

// shareAsset serves one built file of the share shell.
func (s *Server) shareAsset(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shareHeaders(w)
		setShareFraming(w, nil)
		if s.webDir == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.webDir, name))
	})
}

// ── The ticket ──────────────────────────────────────────────────────────────

// shareAuthTicket reads `Authorization: Share <ticket>`.
func shareAuthTicket(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Share "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

var shareRenewals = newRateLimiter(4, time.Minute)

// shareTicketRenew issues a fresh ticket for a valid one, re-checking the
// link: a revoked link's ticket cannot be renewed.
func (s *Server) shareTicketRenew(w http.ResponseWriter, r *http.Request) {
	shareHeaders(w)
	setShareFraming(w, nil)
	l, tk, reason := s.shareFromTicket(shareAuthTicket(r), r)
	if l == nil {
		s.shareDenied(r, "", reason)
		writeJSONErr(w, http.StatusUnauthorized, "not valid")
		return
	}
	if ok, _, _ := shareRenewals.take(l.ID + "|" + clientIPOf(r)); !ok {
		writeJSONErr(w, http.StatusTooManyRequests, "too many renewals")
		return
	}
	exp := time.Now().Add(sharelink.TicketTTL)
	writeJSON(w, map[string]any{"ok": true, "ticket": sharelink.SignTicket(s.shareKey, l.ID, tk.Viewer, exp),
		"ticketExp": exp.UnixMilli()})
}

// ── The sensor feed ─────────────────────────────────────────────────────────

var feedPolls = newRateLimiter(sharelink.FeedPollsPerMinute, time.Minute)

// feedDay counts a feed's polls per UTC day, so the audit trail gets one row
// per link per day rather than one per poll.
var feedDay = struct {
	sync.Mutex
	day   map[string]string
	count map[string]int
}{day: map[string]string{}, count: map[string]int{}}

// feedStale is how old a value may be before the feed reports it as null.
const feedStale = 5 * time.Minute

func (s *Server) shareFeed(w http.ResponseWriter, r *http.Request) {
	shareHeaders(w)
	setShareFraming(w, nil)
	if r.URL.Query().Has("token") {
		// The token belongs in a header, never a URL that a proxy or a log
		// would keep.
		s.shareDenied(r, "", "token-in-url")
		writeJSONErr(w, http.StatusUnauthorized, "not valid")
		return
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !sharelink.WellFormedToken(token) || s.auditDB == nil {
		s.shareDenied(r, "", "unknown")
		writeJSONErr(w, http.StatusUnauthorized, "not valid")
		return
	}
	l, err := s.auditDB.ShareLinkByHash(sharelink.HashToken(token))
	if err != nil || l == nil || l.Kind != string(sharelink.KindFeed) {
		s.shareDenied(r, "", "unknown")
		writeJSONErr(w, http.StatusUnauthorized, "not valid")
		return
	}
	if ok, reason := s.shareCheckLink(l, clientIPOf(r)); ok == nil {
		s.shareDenied(r, l.ID, reason)
		writeJSONErr(w, http.StatusUnauthorized, "not valid")
		return
	}
	if ok, _, reset := feedPolls.take(l.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(reset))
		writeJSONErr(w, http.StatusTooManyRequests, "polled too often")
		return
	}
	now := time.Now()
	s.noteFeedPoll(*l, r, now)
	var opts sharelink.Options
	_ = json.Unmarshal([]byte(l.Options), &opts)
	writeJSON(w, s.feedValues(l.RouterID, opts, now))
}

func (s *Server) noteFeedPoll(l db.ShareLink, r *http.Request, now time.Time) {
	day := now.UTC().Format("2006-01-02")
	feedDay.Lock()
	prevDay, prev := feedDay.day[l.ID], feedDay.count[l.ID]
	first := prevDay != day
	if first {
		feedDay.day[l.ID], feedDay.count[l.ID] = day, 0
	}
	feedDay.count[l.ID]++
	feedDay.Unlock()
	if !first {
		return
	}
	_ = s.auditDB.TouchShareLink(l.ID, now.UnixMilli())
	note := "first poll today"
	if prevDay != "" {
		note += "; polled " + strconv.Itoa(prev) + " times on " + prevDay
	}
	s.shareAudit(audit.New(auditSink{s.auditDB}, audit.Actor{Name: "share:" + l.Label,
		IP: audit.NormalizeIP(clientIPOf(r))}, nowMillis), "share.open", l, note)
}

// feedValues reads the chosen values from what MikroDash already holds for
// the router. It opens no router channel: a value that is not held, or is
// older than feedStale, is null.
func (s *Server) feedValues(routerID string, opts sharelink.Options, now time.Time) map[string]any {
	want := map[string]bool{}
	for _, v := range opts.Values {
		// A value whose page is no longer shared is left out, not nulled.
		if sharelink.FeedValueAllowed(v) {
			want[v] = true
		}
	}
	out := map[string]any{"updated_at": now.UnixMilli()}
	fresh := func(ts int64) bool { return ts > 0 && now.Sub(time.UnixMilli(ts)) <= feedStale }
	rs := s.sessions.Live()[routerID]
	if want["online"] {
		if up, known := s.connTrack.Online(routerID); known {
			out["online"] = up
		} else {
			out["online"] = nil
		}
	}
	if want["version"] || want["model"] {
		routers, _ := s.store.RouterRecords()
		for _, r := range routers {
			if r.ID != routerID {
				continue
			}
			if want["version"] {
				out["version"] = nilIfEmpty(r.OSVersion)
			}
			if want["model"] {
				out["model"] = nilIfEmpty(r.Model)
			}
		}
	}
	for _, k := range []string{"uptime", "cpu", "memory", "temperature"} {
		if want[k] {
			out[k] = nil
		}
	}
	if rs != nil {
		if last := rs.System().Last(); last != nil && fresh(last.TS) {
			if want["uptime"] {
				out["uptime"] = nilIfEmpty(last.UptimeRaw)
			}
			if want["cpu"] {
				out["cpu"] = last.CPULoad
			}
			if want["memory"] {
				out["memory"] = last.MemPct
			}
			if want["temperature"] && last.TempC != nil {
				out["temperature"] = *last.TempC
			}
		}
	}
	if want[sharelink.FeedInterfaces] {
		have := map[string]map[string]any{}
		if rs != nil {
			if last := rs.IfStatus().Last(); last != nil && fresh(last.TS) {
				for _, i := range last.Interfaces {
					have[i.Name] = map[string]any{"running": i.Running, "rx_mbps": i.RxMbps, "tx_mbps": i.TxMbps}
				}
			}
		}
		ifs := map[string]any{}
		for _, name := range opts.Interfaces {
			if v, ok := have[name]; ok {
				ifs[name] = v
			} else {
				ifs[name] = nil
			}
		}
		out["interfaces"] = ifs
	}
	if want["alerts"] {
		out["alerts"] = nil
		if counts, err := s.auditDB.CountOpenAlertsByRouter(); err == nil {
			out["alerts"] = counts[routerID]
		}
	}
	if want["wifi_clients"] {
		out["wifi_clients"] = nil
		if rs != nil {
			if last := rs.Wireless().Last(); last != nil && fresh(last.TS) {
				out["wifi_clients"] = len(last.Clients)
			}
		}
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
