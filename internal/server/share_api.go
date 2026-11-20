package server

// Settings > Shared links: the ONLY place a share link is made, listed, edited,
// revoked or deleted (the operator's ruling of 2026-11-20). Every route here
// is for global administrators (principalsGuard); a refusal is recorded by the
// refused-write middleware.
//
// The rules (what may be shared, the limits, the origins and addresses) are in
// internal/sharelink; this file applies them, stores the result and records it
// in the audit trail. The token is returned ONCE, in the create response, and
// only its hash is stored.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/pages"
	"mikrodash/internal/safe"
	"mikrodash/internal/sharelink"
)

const shareLinksPrefix = "/api/share-links"

func (s *Server) registerShareAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET "+shareLinksPrefix, s.principalsGuard(s.shareList))
	mux.HandleFunc("GET "+shareLinksPrefix+"/options", s.principalsGuard(s.shareOptions))
	mux.HandleFunc("POST "+shareLinksPrefix, s.principalsGuard(s.shareCreate))
	mux.HandleFunc("PUT "+shareLinksPrefix+"/{id}", s.principalsGuard(s.shareUpdate))
	mux.HandleFunc("POST "+shareLinksPrefix+"/{id}/revoke", s.principalsGuard(s.shareRevoke))
	mux.HandleFunc("DELETE "+shareLinksPrefix+"/{id}", s.principalsGuard(s.shareDelete))
}

// ── Live viewers ────────────────────────────────────────────────────────────
//
// shareViewers counts each link's live share sockets and can close them, so a
// revocation (or an edit that moves a link) reaches a screen at once.

type shareViewers struct {
	mu    sync.Mutex
	conns map[string]map[*shareConn]bool
}

func (v *shareViewers) add(id string, c *shareConn, cap int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.conns == nil {
		v.conns = map[string]map[*shareConn]bool{}
	}
	if len(v.conns[id]) >= cap {
		return false
	}
	if v.conns[id] == nil {
		v.conns[id] = map[*shareConn]bool{}
	}
	v.conns[id][c] = true
	return true
}

func (v *shareViewers) remove(id string, c *shareConn) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.conns[id], c)
	if len(v.conns[id]) == 0 {
		delete(v.conns, id)
	}
}

// byViewer is the open socket of one viewer of a link, or nil.
func (v *shareViewers) byViewer(id, viewer string) *shareConn {
	v.mu.Lock()
	defer v.mu.Unlock()
	for c := range v.conns[id] {
		if c.viewer == viewer {
			return c
		}
	}
	return nil
}

func (v *shareViewers) count(id string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.conns[id])
}

func (v *shareViewers) list(id string) []*shareConn {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]*shareConn, 0, len(v.conns[id]))
	for c := range v.conns[id] {
		out = append(out, c)
	}
	return out
}

// endShare closes every live viewer of a link with the reason the screen
// shows ("revoked" and "expired" read the same to a viewer).
func (s *Server) endShare(id, reason string) {
	for _, c := range s.shareLive.list(id) {
		_ = reason // one sentence for every reason: see shareConn.end
		c.end()
	}
}

// reloadShare tells every live viewer of a link to reload: an edit changed
// what it shows.
func (s *Server) reloadShare(id string) {
	for _, c := range s.shareLive.list(id) {
		c.reload()
	}
}

// ── The request body ────────────────────────────────────────────────────────

type shareBody struct {
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Page     string            `json:"page"`
	RouterID string            `json:"routerId"`
	Layout   string            `json:"layout"` // "mine" or "default", for the Dashboard
	Slides   []sharelink.Slide `json:"slides"`
	Options  sharelink.Options `json:"options"`
	Expiry   struct {
		Choice string `json:"choice"`
		DateMs int64  `json:"dateMs"`
	} `json:"expiry"`
	AllowedAddresses []string `json:"allowedAddresses"`
	FrameOrigins     []string `json:"frameOrigins"`
	ViewerCap        int      `json:"viewerCap"`
	Base             string   `json:"base"`
	// KeepExpiry, on an edit, leaves the expiry as it is.
	KeepExpiry bool `json:"keepExpiry"`
}

// shareRouterOK reports whether an id is a router (not a switch: share links
// show routers only, as the router selector does).
func (s *Server) shareRouterOK(id string) bool {
	if id == "" || s.store == nil {
		return false
	}
	list, err := s.store.RouterRecords()
	if err != nil {
		return false
	}
	for _, r := range list {
		if r.ID == id {
			return true
		}
	}
	return false
}

// shareLayout snapshots the creator's saved Dashboard layout, or "" for the
// default one.
func (s *Server) shareLayout(choice string, sess *Session) string {
	if choice != "mine" || s.auditDB == nil {
		return ""
	}
	blob, err := s.auditDB.Layout(s.userIDFor(sess.Username), "dashboard")
	if err != nil || len(blob) == 0 {
		return ""
	}
	return string(blob)
}

// buildShareLink validates a body into a row. It is the one validation path,
// for create and edit alike, so an edit is checked exactly as a create is.
func (s *Server) buildShareLink(b shareBody, sess *Session, now time.Time) (db.ShareLink, error) {
	var l db.ShareLink
	kind := sharelink.Kind(b.Kind)
	if !kind.Valid() {
		return l, errors.New("choose what to share: a page, a slideshow, a Dashboard card or a sensor feed")
	}
	label, err := sharelink.CheckLabel(b.Label)
	if err != nil {
		return l, err
	}
	origins, err := sharelink.NormalizeOrigins(b.FrameOrigins)
	if err != nil {
		return l, err
	}
	if kind == sharelink.KindFeed && len(origins) > 0 {
		return l, errors.New("a sensor feed is read by Home Assistant's server, not shown in a frame; it takes no embedding origins")
	}
	addrs, err := sharelink.NormalizeAllowList(b.AllowedAddresses)
	if err != nil {
		return l, err
	}
	opts := b.Options
	if err := opts.Normalize(kind, len(origins) > 0); err != nil {
		return l, err
	}
	l = db.ShareLink{Label: label, Kind: string(kind), FrameOrigins: origins, AllowedCIDRs: addrs}
	switch kind {
	case sharelink.KindPage, sharelink.KindCard:
		page := b.Page
		if kind == sharelink.KindCard {
			page = "dashboard"
		}
		if !pages.IsShareable(page) {
			return l, errors.New("that page cannot be shared")
		}
		if !s.shareRouterOK(b.RouterID) {
			return l, errors.New("choose a router")
		}
		l.Page, l.RouterID = page, b.RouterID
		if page == "dashboard" {
			l.Layout = s.shareLayout(b.Layout, sess)
		}
	case sharelink.KindFeed:
		if !s.shareRouterOK(b.RouterID) {
			return l, errors.New("choose a router")
		}
		l.RouterID = b.RouterID
	case sharelink.KindSlideshow:
		if err := sharelink.CheckSlides(b.Slides); err != nil {
			return l, err
		}
		layout := s.shareLayout(b.Layout, sess)
		for i, sl := range b.Slides {
			if !s.shareRouterOK(sl.RouterID) {
				return l, errors.New("slide " + strconv.Itoa(i+1) + ": choose a router")
			}
			ds := db.ShareSlide{Page: sl.Page, RouterID: sl.RouterID, Seconds: sl.Secs}
			if sl.Page == "dashboard" {
				ds.Layout = layout
			}
			l.Slides = append(l.Slides, ds)
		}
	}
	if kind.Viewable() {
		viewers, err := sharelink.CheckCap(b.ViewerCap)
		if err != nil {
			return l, err
		}
		l.ViewerCap = viewers
	}
	optJSON, _ := json.Marshal(opts)
	l.Options = string(optJSON)
	exp, err := sharelink.ResolveExpiry(b.Expiry.Choice, b.Expiry.DateMs, now)
	if err != nil {
		return l, err
	}
	l.ExpiresAt = exp
	return l, nil
}

// shareBase is the address a link is shown with: the one the form gives, or
// the address the administrator is on.
func shareBase(r *http.Request, given string) (string, error) {
	if strings.TrimSpace(given) != "" {
		return sharelink.NormalizeBase(given)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return sharelink.NormalizeBase(scheme + "://" + r.Host)
}

// ── Audit ───────────────────────────────────────────────────────────────────

// shareAudit writes one row per router the link shows, so each router's audit
// view shows who shared it.
func (s *Server) shareAudit(rec *audit.Recorder, action string, l db.ShareLink, note string, extra ...audit.KV) {
	routers := l.RouterIDs()
	if len(routers) == 0 {
		routers = []string{""}
	}
	for _, rid := range routers {
		rec.Record(audit.Event{Action: action, RouterID: rid, TargetType: "share-link",
			TargetID: l.ID, TargetName: l.Label, Note: note, Extra: extra})
	}
}

func describeExpiry(exp *int64) string {
	if exp == nil {
		return "never expires"
	}
	return "expires " + time.UnixMilli(*exp).UTC().Format(time.RFC3339)
}

// shareSubject describes what a link shows, for the audit note.
func shareSubject(l db.ShareLink) string {
	switch l.Kind {
	case string(sharelink.KindSlideshow):
		parts := make([]string, 0, len(l.Slides))
		for _, sl := range l.Slides {
			parts = append(parts, sl.Page+"@"+sl.RouterID)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string(sharelink.KindFeed):
		return "feed@" + l.RouterID
	}
	return l.Page + "@" + l.RouterID
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (s *Server) shareCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	var b shareBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed body")
		return
	}
	now := time.Now()
	l, err := s.buildShareLink(b, sess, now)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	base, err := shareBase(r, b.Base)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	token, hash, err := sharelink.NewToken()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not make a token")
		return
	}
	id, err := newUUID()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not make an id")
		return
	}
	creator := s.userIDFor(sess.Username)
	if creator == "" {
		writeJSONErr(w, http.StatusForbidden, "your account has no id to own the link")
		return
	}
	l.ID, l.TokenHash, l.CreatedBy, l.CreatedAt = id, hash, creator, now.UnixMilli()
	if err := s.auditDB.InsertShareLink(l); err != nil {
		log.Printf("[share] create: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the link")
		return
	}
	s.shareAudit(s.httpRecorder(r, sess), "share.create", l,
		l.Kind+" "+shareSubject(l)+", "+describeExpiry(l.ExpiresAt),
		audit.KV{Key: "allowedAddresses", Value: l.AllowedCIDRs},
		audit.KV{Key: "frameOrigins", Value: l.FrameOrigins})
	writeJSON(w, map[string]any{
		"ok": true, "link": s.shareView(l),
		// SHOWN ONCE. Only the hash is stored; nothing can show it again.
		"url":          base + "/s/" + token,
		"mixedContent": sharelink.MixedContent(base, l.FrameOrigins),
	})
}

func (s *Server) shareUpdate(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	cur, err := s.auditDB.ShareLinkByID(id)
	if err != nil || cur == nil {
		writeJSONErr(w, http.StatusNotFound, "no such link")
		return
	}
	if cur.RevokedAt != nil {
		writeJSONErr(w, http.StatusConflict, "a revoked link cannot be edited")
		return
	}
	var b shareBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed body")
		return
	}
	if b.Kind != cur.Kind {
		writeJSONErr(w, http.StatusBadRequest, "a link's kind cannot change; make a new link")
		return
	}
	next, err := s.buildShareLink(b, sess, time.Now())
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	if b.KeepExpiry {
		next.ExpiresAt = cur.ExpiresAt
	}
	// NEVER CHANGED BY AN EDIT: the token, the creator (an edit by another
	// administrator does not make the link theirs), and the counters. A
	// Dashboard link edited without a layout choice keeps its snapshot.
	next.ID, next.TokenHash, next.CreatedBy, next.CreatedAt = cur.ID, cur.TokenHash, cur.CreatedBy, cur.CreatedAt
	if b.Layout == "" && next.Page == "dashboard" && cur.Page == "dashboard" {
		next.Layout = cur.Layout
	}
	if err := s.auditDB.UpdateShareLink(next); err != nil {
		writeJSONErr(w, http.StatusConflict, "the link changed or was revoked; reload")
		return
	}
	// Audited with the page and router before and after, on the audit view
	// of every router involved, old and new.
	both := next
	both.Slides = append(append([]db.ShareSlide{}, cur.Slides...), next.Slides...)
	if cur.RouterID != next.RouterID {
		both.Slides = append(both.Slides, db.ShareSlide{RouterID: cur.RouterID})
	}
	s.shareAudit(s.httpRecorder(r, sess), "share.update", both,
		"before: "+shareSubject(*cur)+", "+describeExpiry(cur.ExpiresAt)+
			"; after: "+shareSubject(next)+", "+describeExpiry(next.ExpiresAt))
	s.reloadShare(id)
	writeJSON(w, map[string]any{"ok": true, "link": s.shareView(next)})
}

func (s *Server) shareRevoke(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	cur, err := s.auditDB.ShareLinkByID(id)
	if err != nil || cur == nil {
		writeJSONErr(w, http.StatusNotFound, "no such link")
		return
	}
	ok, err := s.auditDB.RevokeShareLink(id, s.userIDFor(sess.Username), sharelink.ReasonAdmin, time.Now().UnixMilli())
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not revoke the link")
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusConflict, "the link is already revoked")
		return
	}
	s.endShare(id, "revoked")
	s.shareAudit(s.httpRecorder(r, sess), "share.revoke", *cur, "revoked by an administrator")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) shareDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	cur, err := s.auditDB.ShareLinkByID(id)
	if err != nil || cur == nil {
		writeJSONErr(w, http.StatusNotFound, "no such link")
		return
	}
	expired := cur.ExpiresAt != nil && time.Now().UnixMilli() >= *cur.ExpiresAt
	if cur.RevokedAt == nil && !expired {
		writeJSONErr(w, http.StatusConflict, "revoke the link before deleting it")
		return
	}
	if _, err := s.auditDB.DeleteShareLink(id); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not delete the link")
		return
	}
	s.endShare(id, "revoked")
	s.shareAudit(s.httpRecorder(r, sess), "share.delete", *cur, "deleted")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) shareList(w http.ResponseWriter, r *http.Request, sess *Session) {
	links, err := s.auditDB.ShareLinks()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	out := make([]map[string]any, 0, len(links))
	never := 0
	for _, l := range links {
		v := s.shareView(l)
		if v["status"] == "active" && l.ExpiresAt == nil {
			never++
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"ok": true, "links": out, "neverExpiring": never,
		"maxViewerCap": sharelink.MaxViewerCap})
}

// shareView is a link as the Settings tab shows it: never the token or its
// hash.
func (s *Server) shareView(l db.ShareLink) map[string]any {
	var opts sharelink.Options
	_ = json.Unmarshal([]byte(l.Options), &opts)
	status := "active"
	switch {
	case l.RevokedAt != nil:
		status = "revoked"
	case l.ExpiresAt != nil && time.Now().UnixMilli() >= *l.ExpiresAt:
		status = "expired"
	}
	slides := make([]map[string]any, 0, len(l.Slides))
	warnings := []string{}
	for i, sl := range l.Slides {
		shareable := pages.IsShareable(sl.Page)
		slides = append(slides, map[string]any{"page": sl.Page, "routerId": sl.RouterID,
			"secs": sl.Seconds, "shareable": shareable})
		if !shareable {
			warnings = append(warnings, "slide "+strconv.Itoa(i+1)+" ("+sl.Page+") is no longer shareable and is skipped")
		}
	}
	if l.Kind != string(sharelink.KindSlideshow) && l.Page != "" && !pages.IsShareable(l.Page) {
		warnings = append(warnings, "its page ("+l.Page+") is not shared any more; the link is refused until it is")
	}
	if l.Kind == string(sharelink.KindCard) && !sharelink.CardShareable(opts.Card) {
		warnings = append(warnings, "its card is not shared any more; the link is refused until it is")
	}
	if l.Kind == string(sharelink.KindSlideshow) && !shareSubjectShareable(l) {
		warnings = append(warnings, "none of its slides is shared any more; the link is refused until one is")
	}
	return map[string]any{
		"id": l.ID, "label": l.Label, "kind": l.Kind, "page": l.Page, "routerId": l.RouterID,
		"slides": slides, "options": opts, "frameOrigins": l.FrameOrigins,
		"allowedAddresses": l.AllowedCIDRs, "viewerCap": l.ViewerCap,
		"viewersNow": s.shareLive.count(l.ID), "createdBy": s.usernameFor(l.CreatedBy),
		"createdAt": l.CreatedAt, "expiresAt": l.ExpiresAt, "revokedAt": l.RevokedAt,
		"revokeReason": l.RevokeReason, "lastUsedAt": l.LastUsedAt, "useCount": l.UseCount,
		"status": status, "neverExpires": l.ExpiresAt == nil, "hasLayout": l.Layout != "",
		"warnings": warnings,
	}
}

// usernameFor is the username of a user id, or the id when the user is gone.
func (s *Server) usernameFor(id string) string {
	if s.store == nil {
		return id
	}
	users, err := s.store.Users()
	if err != nil {
		return id
	}
	for _, u := range users {
		if u.ID == id {
			return u.Username
		}
	}
	return id
}

// shareOptions is what the New shared link form offers: the shareable pages,
// the routers (no switches), the shareable cards, the feed's values, the
// limits, and the address MikroDash sees for this administrator.
func (s *Server) shareOptions(w http.ResponseWriter, r *http.Request, sess *Session) {
	type opt struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	pgs := []opt{}
	for _, p := range pages.All {
		if p.Share == pages.Shareable {
			pgs = append(pgs, opt{p.Key, p.Title})
		}
	}
	routers := []opt{}
	if list, err := s.store.RouterRecords(); err == nil {
		for _, r := range list {
			label := r.Label
			if label == "" {
				label = r.Host
			}
			routers = append(routers, opt{r.ID, label})
		}
	}
	cards := []opt{}
	for _, c := range sharelink.ShareableCards() {
		cards = append(cards, opt{c.ID, c.Label})
	}
	values := []opt{}
	for _, v := range sharelink.FeedValues {
		if sharelink.FeedValueAllowed(v.Key) {
			values = append(values, opt{v.Key, v.Label})
		}
	}
	base, _ := shareBase(r, "")
	_, forwarded := r.Header["X-Forwarded-For"]
	writeJSON(w, map[string]any{
		"ok": true, "pages": pgs, "routers": routers, "cards": cards, "feedValues": values,
		"defaultViewerCap": sharelink.DefaultViewerCap, "maxViewerCap": sharelink.MaxViewerCap,
		"minSlides": sharelink.MinSlides, "maxSlides": sharelink.MaxSlides,
		"minSlideSecs": sharelink.MinSlideSecs, "maxSlideSecs": sharelink.MaxSlideSecs,
		"defaultSlideSecs": sharelink.DefaultSlideSecs, "maxWarmUpSecs": sharelink.MaxWarmUpSecs,
		"yourAddress": clientIPOf(r), "base": base,
		// A forwarded header reached MikroDash but the address it sees is
		// still the peer's: the proxy is not trusted, so an allow-list would
		// see the proxy, not the screen.
		"proxyUntrusted": forwarded && clientIPOf(r) == peerIP(r),
	})
}

// peerIP is the TCP peer's address.
func peerIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return audit.NormalizeIP(strings.Trim(host, "[]"))
}
