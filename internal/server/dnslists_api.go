package server

// Domain lists (Settings -> Features): a list of domains, pasted or fetched from
// a URL, kept in step with FWD static entries on chosen routers.
//
//	GET    /api/dns/lists               every list
//	POST   /api/dns/lists               create or change one
//	DELETE /api/dns/lists?id=           forget one (its entries stay)
//	POST   /api/dns/lists/preview?id=   what a sync would do, per router
//	POST   /api/dns/lists/sync?id=      do it
//
// ── ONLY WHAT THE LIST MADE ─────────────────────────────────────────────────
//
// An entry a list creates carries the comment `MikroDash list #<id> <name>`,
// and a sync adds, changes and removes only entries whose comment starts with
// `MikroDash list #<id>`. It is the id that owns them, not the name: renaming a
// list rewrites the name in their comments at the next sync and loses none.
// Forgetting a list can remove its entries from the routers first. A domain the
// router already holds under any other entry is skipped, not duplicated: an
// operator's own FWD entry for a name wins over a list's.
//
// ── A BAD FETCH REMOVES NOTHING ─────────────────────────────────────────────
//
// A source answers 200 with an error page, an empty file or a captive portal's
// login form as readily as with the list, and every one of them parses to no
// domains or very few. Synced as it stands, that removes every entry the list
// made, on every router, with nobody watching. So a fetch that yields nothing
// is refused, and one that yields under half of what the last accepted sync
// held is refused too, until an administrator looks at the preview and syncs
// it by hand (`force`). A scheduled sync never forces.
//
// The source is fetched only from a public address: the dialer refuses a
// private, loopback, link-local, multicast or unspecified one, carrier-grade
// NAT, and the other reserved ranges (internal/netguard), checked on the address it
// actually connects to, so a redirect or a name that resolves inward is refused
// the same as a literal address.
//
// Administrators only: a list writes to every router it names, on a schedule.
// A disabled router is skipped.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
	"mikrodash/internal/netguard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

const (
	dnsListFetchMax = 8 << 20
	// dnsListMax bounds one list: a job takes 5,000 changes.
	dnsListMax = 4000
)

var (
	dnsListName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)
	domainLabel = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)
)

// listFetch is the client a list's source is fetched with. Proxy is nil, so
// the address it dials is the source's own, and that address is what
// netguard.PublicOnly checks.
var listFetch = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:         netguard.Dialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("a redirect away from http")
		}
		return nil
	},
}

// listTag is the comment that marks an entry as the list's.
func listTag(l db.DNSList) string {
	return "MikroDash list #" + strconv.FormatInt(l.ID, 10) + " " + l.Name
}

// ownedBy is whether an entry's comment says the list with this id made it,
// whatever the list was called then.
func ownedBy(comment string, id int64) bool {
	p := "MikroDash list #" + strconv.FormatInt(id, 10)
	return comment == p || strings.HasPrefix(comment, p+" ")
}

// ParseDomains reads a list in any of the shapes they are published in: one
// domain a line, a hosts file (`0.0.0.0 example.com`), `*.` and leading dots,
// `#` and `!` comments, and adblock `||example.com^`. Lower-cased, de-duplicated
// and sorted; what is not a domain is counted, not guessed at, and that
// includes a single label such as a hosts file's `localhost`.
func ParseDomains(text string) (domains []string, skipped int) {
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexAny(line, "#!"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		names := strings.Fields(line)
		if len(names) > 1 {
			// Only a hosts line has more than one word: an address, then names.
			if net.ParseIP(names[0]) == nil {
				skipped++
				continue
			}
			names = names[1:]
		}
		for _, d := range names {
			d = strings.ToLower(d)
			d = strings.TrimSuffix(strings.TrimPrefix(d, "||"), "^")
			d = strings.TrimSuffix(strings.TrimLeft(strings.TrimPrefix(d, "*"), "."), ".")
			if !validDomain(d) {
				skipped++
				continue
			}
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	sort.Strings(domains)
	return domains, skipped
}

func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, l := range strings.Split(d, ".") {
		if !domainLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// WithoutCovered drops a domain whose parent is also listed, when the entries
// match subdomains: `a.example.com` adds nothing beside `example.com`.
func WithoutCovered(domains []string) []string {
	in := map[string]bool{}
	for _, d := range domains {
		in[d] = true
	}
	out := domains[:0:0]
	for _, d := range domains {
		covered := false
		for p := d; strings.Contains(p, "."); {
			p = p[strings.Index(p, ".")+1:]
			if in[p] {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	return out
}

// ListShrank says why a fetch may not be synced unattended: it yields nothing,
// or under half of the last accepted sync's count. Empty when it may.
func ListShrank(count, last int) string {
	switch {
	case count == 0:
		return "the list came back with no domains; nothing was changed"
	case last > 0 && count*2 < last:
		return fmt.Sprintf("the list shrank from %d to %d domains; nothing was changed until it is synced by hand", last, count)
	}
	return ""
}

// DNSListPlan is one router's share of a sync.
type DNSListPlan struct {
	RouterID string `json:"routerId"`
	Label    string `json:"label"`
	Add      int    `json:"add"`
	Change   int    `json:"change"`
	Remove   int    `json:"remove"`
	// Skipped is the domains the router already answers with an entry of its own.
	Skipped int    `json:"skipped"`
	Error   string `json:"error"`

	in   dnsBulkIn
	rows []routeros.Reply
}

// PlanDNSList is what one router must do to hold the list, pure: the list's
// settings, its domains and the router's static table in, a job out.
func PlanDNSList(l db.DNSList, domains []string, rows []routeros.Reply) (dnsBulkIn, int) {
	tag := listTag(l)
	mine := map[string]routeros.Reply{}
	var extra []string // a second entry of the list's for a name it already has
	theirs := map[string]bool{}
	for _, r := range rows {
		name := strings.ToLower(r["name"])
		if name == "" {
			continue
		}
		if !ownedBy(r["comment"], l.ID) {
			theirs[name] = true
		} else if _, twice := mine[name]; twice {
			extra = append(extra, r[".id"])
		} else {
			mine[name] = r
		}
	}
	want := map[string]map[string]any{}
	for _, d := range domains {
		want[d] = map[string]any{"name": d, "type": "FWD", "forwardTo": l.ForwardTo,
			"addressList": l.AddressList, "matchSubdomain": l.MatchSubdomain, "comment": tag}
	}
	var in dnsBulkIn
	skipped := 0
	for _, d := range domains {
		if theirs[d] {
			skipped++
			continue
		}
		have, ok := mine[d]
		if !ok {
			in.Add = append(in.Add, want[d])
			continue
		}
		if have["type"] != "FWD" || have["forward-to"] != l.ForwardTo || have["address-list"] != l.AddressList ||
			(have["match-subdomain"] == "true") != l.MatchSubdomain || have["comment"] != tag {
			in.Set = append(in.Set, dnsBulkSet{ID: have[".id"], Values: want[d]})
		}
	}
	for name, r := range mine {
		if _, ok := want[name]; !ok || theirs[name] {
			in.Remove = append(in.Remove, r[".id"])
		}
	}
	in.Remove = append(in.Remove, extra...)
	sort.Strings(in.Remove)
	return in, skipped
}

// listDomains is the list's domains: the pasted ones and the source's.
func listDomains(ctx context.Context, l db.DNSList) ([]string, error) {
	text := l.Domains
	if l.SourceURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.SourceURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := listFetch.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching the list: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching the list: %s", resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, dnsListFetchMax+1))
		if err != nil {
			return nil, fmt.Errorf("fetching the list: %w", err)
		}
		if len(body) > dnsListFetchMax {
			return nil, errors.New("the list is larger than 8 MB")
		}
		text += "\n" + string(body)
	}
	domains, _ := ParseDomains(text)
	if l.MatchSubdomain {
		domains = WithoutCovered(domains)
	}
	if len(domains) > dnsListMax {
		return nil, fmt.Errorf("%d domains; a list takes %d", len(domains), dnsListMax)
	}
	return domains, nil
}

func (s *Server) registerDNSLists(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dns/lists", s.dnsListsGet)
	mux.HandleFunc("POST /api/dns/lists", s.dnsListSave)
	mux.HandleFunc("DELETE /api/dns/lists", s.dnsListDelete)
	mux.HandleFunc("POST /api/dns/lists/preview", s.dnsListPreview)
	mux.HandleFunc("POST /api/dns/lists/sync", s.dnsListSync)
}

// listsGate: the feature, an administrator, the database.
func (s *Server) listsGate(w http.ResponseWriter, r *http.Request) *Session {
	sess := s.layoutSession(w, r)
	if sess == nil || !s.featureGate(w, store.FeatureDNSLists) {
		return nil
	}
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Domain lists are for administrators")
		return nil
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "the database is not open")
		return nil
	}
	return sess
}

// DNSListView is a list as the page shows it.
type DNSListView struct {
	db.DNSList
	Running bool `json:"running"`
}

func (s *Server) dnsListsGet(w http.ResponseWriter, r *http.Request) {
	if s.listsGate(w, r) == nil {
		return
	}
	lists, err := s.auditDB.DNSLists()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the lists could not be read")
		return
	}
	out := make([]DNSListView, 0, len(lists))
	s.features.mu.Lock()
	for _, l := range lists {
		out = append(out, DNSListView{DNSList: l, Running: s.features.listsRunning[l.ID]})
	}
	s.features.mu.Unlock()
	writeJSON(w, map[string]any{"lists": out})
}

func (s *Server) dnsListSave(w http.ResponseWriter, r *http.Request) {
	sess := s.listsGate(w, r)
	if sess == nil {
		return
	}
	var l db.DNSList
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&l); err != nil {
		writeJSON400OK(w)
		return
	}
	l.Name = strings.TrimSpace(l.Name)
	l.SourceURL = strings.TrimSpace(l.SourceURL)
	l.AddressList = strings.TrimSpace(l.AddressList)
	l.ForwardTo = strings.TrimSpace(l.ForwardTo)
	switch {
	case !dnsListName.MatchString(l.Name):
		writeJSONErr(w, http.StatusBadRequest, "a name of letters, digits, space, dot, dash or underscore, up to 40")
		return
	case l.SourceURL == "" && strings.TrimSpace(l.Domains) == "":
		writeJSONErr(w, http.StatusBadRequest, "a source URL or some domains")
		return
	case l.SourceURL != "" && !httpURL(l.SourceURL):
		writeJSONErr(w, http.StatusBadRequest, "the source must be an http or https URL")
		return
	case l.AddressList == "" && l.ForwardTo == "":
		writeJSONErr(w, http.StatusBadRequest, "an address list, a forwarder, or both: an entry with neither does nothing")
		return
	case l.IntervalHours < 0 || l.IntervalHours > 168:
		writeJSONErr(w, http.StatusBadRequest, "an interval of 0 (by hand) to 168 hours")
		return
	}
	for _, v := range []string{l.AddressList, l.ForwardTo} {
		if len(v) > 64 || strings.ContainsAny(v, " \t\"'=") {
			writeJSONErr(w, http.StatusBadRequest, "an address list and a forwarder are single words")
			return
		}
	}
	targets, _ := s.fleetTargets(sess, l.RouterIDs, "dns", "write")
	l.RouterIDs = make([]string, 0, len(targets))
	for _, t := range targets {
		l.RouterIDs = append(l.RouterIDs, t.ID)
	}
	was, existed := s.dnsListByID(l.ID)
	l.Dropped = []string{}
	if existed {
		l.Dropped = pendingAfterSave(was, l.RouterIDs)
	}
	id, err := s.auditDB.SaveDNSList(l)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the list could not be saved; is the name taken?")
		return
	}
	rec := s.httpRecorder(r, sess)
	rec.Record(audit.Event{Action: "dnsList.save", TargetType: "dnsList",
		TargetID: strconv.FormatInt(id, 10), TargetName: l.Name})
	purging := false
	if len(l.Dropped) > 0 {
		// Nothing visits a router that is no longer on the list, so the entries
		// it made there go now; one that cannot be reached stays pending, and
		// each sync tries it again.
		l.ID = id
		purging = s.startListSync(s.features.ctx, l, sess.Username, rec, listSyncOpts{pending: true})
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "dropped": len(l.Dropped), "purging": purging})
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) dnsListDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.listsGate(w, r)
	if sess == nil {
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if r.URL.Query().Get("entries") == "1" {
		// The entries go first, router by router, and the list with them once
		// every router has taken them off. One that could not leaves the list,
		// with what happened, to be tried again.
		l, ok := s.dnsListByID(id)
		if !ok {
			writeJSONErr(w, http.StatusNotFound, "no such list")
			return
		}
		rec := s.httpRecorder(r, sess)
		if !s.startListSync(s.features.ctx, l, sess.Username, rec, listSyncOpts{purge: true}) {
			writeJSONErr(w, http.StatusConflict, "this list is being synced")
			return
		}
		rec.Record(audit.Event{Action: "dnsList.delete", TargetType: "dnsList",
			TargetID: strconv.FormatInt(id, 10), TargetName: l.Name, Note: "removing its entries first"})
		writeJSON(w, map[string]any{"ok": true, "removing": true})
		return
	}
	if err := s.auditDB.DeleteDNSList(id); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "the list could not be removed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "dnsList.delete", TargetType: "dnsList",
		TargetID: strconv.FormatInt(id, 10)})
	writeJSON(w, map[string]any{"ok": true})
}

// dnsListByID reads one list.
func (s *Server) dnsListByID(id int64) (db.DNSList, bool) {
	lists, err := s.auditDB.DNSLists()
	if err != nil {
		return db.DNSList{}, false
	}
	for _, l := range lists {
		if l.ID == id {
			return l, true
		}
	}
	return db.DNSList{}, false
}

// planDNSListFleet plans every enabled router the list names for these domains.
// A disabled router is left out of a sync, which visits it when it is enabled;
// a purge, whose point is that nothing is left behind, reports it as a plan that
// failed. A router that is no longer in MikroDash has nothing left to clean and
// is left out of both.
func (s *Server) planDNSListFleet(ctx context.Context, l db.DNSList, domains []string, purge bool) []DNSListPlan {
	labels := map[string]string{}
	disabled := map[string]string{}
	if all, _ := s.store.Routers(); all != nil {
		for _, r := range all {
			label := firstNonEmpty(r.Label, firstNonEmpty(r.Host, r.ID))
			if r.Disabled {
				disabled[r.ID] = label
			} else {
				labels[r.ID] = label
			}
		}
	}
	targets := make([]fleetTarget, 0, len(l.RouterIDs))
	var unavailable []DNSListPlan
	for _, id := range l.RouterIDs {
		switch {
		case labels[id] != "":
			targets = append(targets, fleetTarget{ID: id, Label: labels[id]})
		case purge && disabled[id] != "":
			unavailable = append(unavailable, DNSListPlan{RouterID: id, Label: disabled[id], Error: "disabled"})
		}
	}
	return append(unavailable, fleetEach(ctx, targets, func(ctx context.Context, t fleetTarget) DNSListPlan {
		p := DNSListPlan{RouterID: t.ID, Label: t.Label}
		sn, drop, ok := s.fleetSession(ctx, t.ID, dnsBulkHold)
		if !ok {
			p.Error = "unreachable"
			return p
		}
		defer drop()
		rows, rerr := sn.Exec(collect.DNSStaticCmd())
		if rerr != nil {
			p.Error = "read-failed"
			return p
		}
		p.rows = rows
		p.in, p.Skipped = PlanDNSList(l, domains, rows)
		p.in.RouterID, p.in.Label = t.ID, "domain list "+l.Name
		p.Add, p.Change, p.Remove = len(p.in.Add), len(p.in.Set), len(p.in.Remove)
		return p
	})...)
}

func (s *Server) dnsListPreview(w http.ResponseWriter, r *http.Request) {
	if s.listsGate(w, r) == nil {
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	l, ok := s.dnsListByID(id)
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "no such list")
		return
	}
	domains, err := listDomains(r.Context(), l)
	if err != nil {
		writeJSONErr(w, http.StatusBadGateway, safe.Message(err.Error()))
		return
	}
	writeJSON(w, map[string]any{"domains": len(domains), "lastCount": l.LastCount,
		"refused": ListShrank(len(domains), l.LastCount), "routers": s.planDNSListFleet(r.Context(), l, domains, false)})
}

func (s *Server) dnsListSync(w http.ResponseWriter, r *http.Request) {
	sess := s.listsGate(w, r)
	if sess == nil {
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	l, ok := s.dnsListByID(id)
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "no such list")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if !s.startListSync(s.features.ctx, l, sess.Username, s.httpRecorder(r, sess), listSyncOpts{force: force}) {
		writeJSONErr(w, http.StatusConflict, "this list is already being synced")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// listSyncOpts says how a sync differs from the usual one. `force` accepts a
// fetch that shrank, and only an administrator's own Sync passes it. `purge`
// holds no domains at all, so every entry the list made goes, and forgets the
// list once every router has done it. `pending` runs only the removal of the
// entries on the routers a save took off the list, and keeps the list.
type listSyncOpts struct{ force, purge, pending bool }

// startListSync runs one list's sync in the background and records how it
// went on the list. False when it is already running.
func (s *Server) startListSync(ctx context.Context, l db.DNSList, user string, rec *audit.Recorder,
	opts listSyncOpts) bool {
	s.features.mu.Lock()
	if s.features.listsRunning[l.ID] {
		s.features.mu.Unlock()
		return false
	}
	s.features.listsRunning[l.ID] = true
	s.features.mu.Unlock()

	go func() {
		defer func() {
			s.features.mu.Lock()
			delete(s.features.listsRunning, l.ID)
			s.features.mu.Unlock()
		}()
		mark := func(result string, count int) {
			_ = s.auditDB.MarkDNSListSynced(l.ID, time.Now().UnixMilli(), result, count)
		}
		if opts.purge {
			still, lines := s.purgeRouters(ctx, l, unionIDs(l.RouterIDs, l.Dropped), user, rec)
			s.finishForget(l, still, lines)
			return
		}
		leftover, resync := s.settleDropped(ctx, l, user, rec)
		if resync {
			if now, ok := s.dnsListByID(l.ID); ok {
				l = now
			} else {
				return
			}
		}
		if opts.pending && !resync {
			if leftover != "" {
				mark(leftover, -1)
			} else {
				mark("the entries on the routers taken off the list are gone", -1)
			}
			return
		}
		if leftover != "" {
			leftover = " · " + leftover
		}
		domains, err := listDomains(ctx, l)
		if err != nil {
			mark("failed: "+safe.Message(err.Error())+leftover, -1)
			return
		}
		if why := ListShrank(len(domains), l.LastCount); why != "" && (!opts.force || len(domains) == 0) {
			mark("refused: "+why+leftover, -1)
			return
		}
		var lines []string
		for _, p := range s.planDNSListFleet(ctx, l, domains, false) {
			if p.Error != "" {
				lines = append(lines, p.Label+": "+p.Error)
				continue
			}
			if p.Add+p.Change+p.Remove == 0 {
				lines = append(lines, p.Label+": in step")
				continue
			}
			text, _ := s.runListJob(ctx, user, p, rec)
			lines = append(lines, p.Label+": "+text)
		}
		mark(strings.Join(lines, " · ")+leftover, len(domains))
	}()
	return true
}

// purgeRouters removes every entry the list made from these routers and says
// which of them still hold some: one that is disabled, unreachable, or whose
// removal failed. A router that is no longer in MikroDash has nothing to clean.
func (s *Server) purgeRouters(ctx context.Context, l db.DNSList, ids []string, user string,
	rec *audit.Recorder) (still, lines []string) {

	l.RouterIDs = ids
	for _, p := range s.planDNSListFleet(ctx, l, nil, true) {
		if p.Error != "" {
			lines = append(lines, p.Label+": "+p.Error)
			still = append(still, p.RouterID)
			continue
		}
		if p.Add+p.Change+p.Remove == 0 {
			continue
		}
		text, ok := s.runListJob(ctx, user, p, rec)
		lines = append(lines, p.Label+": "+text)
		if !ok {
			still = append(still, p.RouterID)
		}
	}
	return still, lines
}

// settleDropped retries the removal of the entries on the routers a save took
// off the list, and keeps the ones that are not clean for the next sync. A
// save that put a router back waits for this removal to finish, then returns
// true so the current list is synced instead of leaving that router empty.
func (s *Server) settleDropped(ctx context.Context, l db.DNSList, user string, rec *audit.Recorder) (string, bool) {
	if len(l.Dropped) == 0 {
		return "", false
	}
	still, lines := s.purgeRouters(ctx, l, l.Dropped, user, rec)
	resync := false
	// A save while this ran may have changed which routers are pending, or put
	// one back on the list that the old purge has just emptied.
	if now, ok := s.dnsListByID(l.ID); ok {
		still, resync = settledDropped(l, now, still)
	}
	_ = s.auditDB.SetDNSListDropped(l.ID, still)
	if len(still) == 0 {
		return "", resync
	}
	return "entries are still on a router taken off the list: " + strings.Join(lines, " · "), resync
}

// settledDropped reconciles one purge's result with a save that arrived while
// it ran. Routers that could not be cleaned stay pending unless a save put
// them back on the list; new pending routers from a save are kept for the next
// pass; and any router put back on the list triggers a resync.
func settledDropped(before, now db.DNSList, still []string) (dropped []string, resync bool) {
	resync = !slices.Equal(before.RouterIDs, now.RouterIDs)
	for _, id := range still {
		if !slices.Contains(now.RouterIDs, id) && !slices.Contains(dropped, id) {
			dropped = append(dropped, id)
		}
	}
	for _, id := range now.Dropped {
		if !slices.Contains(before.Dropped, id) && !slices.Contains(now.RouterIDs, id) && !slices.Contains(dropped, id) {
			dropped = append(dropped, id)
		}
	}
	for _, id := range before.Dropped {
		if slices.Contains(now.RouterIDs, id) {
			resync = true
		}
	}
	return dropped, resync
}

// finishForget drops a list once none of its routers holds an entry of it, and
// otherwise keeps it with what is left.
func (s *Server) finishForget(l db.DNSList, still, lines []string) {
	if len(still) == 0 {
		_ = s.auditDB.DeleteDNSList(l.ID)
		return
	}
	_ = s.auditDB.MarkDNSListSynced(l.ID, time.Now().UnixMilli(),
		"not forgotten, its entries are still on a router: "+strings.Join(lines, " · "), -1)
}

// pendingAfterSave is the routers whose entries are still to be removed once a
// list is saved with these routers: the ones it had, or was waiting on, and no
// longer names.
func pendingAfterSave(was db.DNSList, now []string) []string {
	out := []string{}
	for _, id := range unionIDs(was.Dropped, was.RouterIDs) {
		if !slices.Contains(now, id) {
			out = append(out, id)
		}
	}
	return out
}

func unionIDs(a, b []string) []string {
	out := append([]string{}, a...)
	for _, id := range b {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// runListJob runs one router's share of a list's sync to the end and says how
// it went, and whether nothing failed. It is paced by `user`'s job allowance on
// that router, which for a scheduled sync is the system's.
func (s *Server) runListJob(ctx context.Context, user string, p DNSListPlan, rec *audit.Recorder) (string, bool) {
	if code, errs := bulkSize(p.in); code != "" {
		return code + " " + strings.Join(errs, "; "), false
	}
	steps, errs := planDNSBulk(p.in, p.rows)
	if len(errs) > 0 {
		return "invalid: " + strings.Join(errs, "; "), false
	}
	sn, drop, ok := s.fleetSession(ctx, p.RouterID, dnsBulkHold)
	if !ok {
		return "unreachable", false
	}
	finished := make(chan DNSBulkJob, 1)
	if _, code := s.beginDNSBulk(sn, drop, user, p.in, steps, rec, false,
		func(j DNSBulkJob) { finished <- j }); code != "" {
		return code, false
	}
	select {
	case j := <-finished:
		return fmt.Sprintf("%d added, %d changed, %d removed, %d failed", j.Added, j.Changed, j.Removed, j.Failed) +
			skippedNote(p.Skipped), j.Failed == 0 && j.State == "done"
	case <-ctx.Done():
		return "stopped: MikroDash shut down", false
	}
}

func skippedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d already held by other entries", n)
}

// syncDueLists starts every list whose interval has passed since its last sync.
func (s *Server) syncDueLists(ctx context.Context, now time.Time) {
	lists, err := s.auditDB.DNSLists()
	if err != nil {
		return
	}
	for _, l := range lists {
		if l.IntervalHours <= 0 || now.UnixMilli()-l.LastSyncAt < int64(l.IntervalHours)*3_600_000 {
			continue
		}
		s.startListSync(ctx, l, "system", audit.New(auditSinkOf(s), audit.System(), nowMillis), listSyncOpts{})
	}
}
