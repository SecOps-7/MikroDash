package server

// The Apps Usage page's custom apps, categories, client filter and Unnamed
// detail (#167):
//
//	GET    /api/apps/catalogue?routerId=           every app, the rules, the categories, canEdit
//	GET    /api/apps/categories?routerId=          the categories (a shared page reads this one)
//	GET    /api/apps/clients?routerId=             the clients with traffic in the last hour
//	GET    /api/apps/live?routerId=&client=        the last hour for one client
//	GET    /api/apps/unnamed?routerId=&client=     the last hour's unnamed traffic in detail
//	POST   /api/apps/rules/preview                 a rule judged, and what it would have named
//	POST   /api/apps/rules                         save a rule (create, or update)
//	DELETE /api/apps/rules/{id}                    delete a rule
//	POST   /api/apps/categories                    save a category
//	DELETE /api/apps/categories/{id}               delete a category
//
// ── WHO MAY DO WHAT ─────────────────────────────────────────────────────────
//
// Reading needs the Apps Usage page for the router asked about. Every write,
// and the preview, needs a global administrator (the operator, #167: rules
// change what every viewer of every router sees), and is audited.
//
// ── ONE SET OF RULES, FOR EVERY ROUTER ──────────────────────────────────────
//
// The rules are the install's (internal/db/apprules.go). Each write reloads
// them into apps.Current(), which every router's collector reads at its next
// reading: a saved rule names the traffic that arrives after it, on every
// router, and nothing that came before (internal/apps/rules.go).
//
// ── THE CLIENT FILTER AND THE UNNAMED DETAIL ARE THE LAST HOUR ──────────────
//
// Both are the apps collector's window, in memory (internal/collect/apps.go),
// so they need the router's session to be running and cover the last hour
// only. The stored history keeps no client and no destination.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/apps"
	"mikrodash/internal/audit"
	"mikrodash/internal/collect"
	"mikrodash/internal/db"
)

// appRulesMu serialises the writes: each reads every rule, judges one against
// the rest, writes it and reloads, and two at once could each pass a check the
// other's write would have failed.
var appRulesMu sync.Mutex

func (s *Server) registerAppRules(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps/catalogue", s.appCatalogueGet)
	mux.HandleFunc("GET /api/apps/categories", s.appCategoriesGet)
	mux.HandleFunc("GET /api/apps/clients", s.appClientsGet)
	mux.HandleFunc("GET /api/apps/live", s.appLiveGet)
	mux.HandleFunc("GET /api/apps/unnamed", s.appUnnamedGet)
	mux.HandleFunc("POST /api/apps/rules/preview", s.appRulePreview)
	mux.HandleFunc("POST /api/apps/rules", s.appRuleSave)
	mux.HandleFunc("DELETE /api/apps/rules/{id}", s.appRuleDelete)
	mux.HandleFunc("POST /api/apps/categories", s.appCategorySave)
	mux.HandleFunc("DELETE /api/apps/categories/{id}", s.appCategoryDelete)
}

// appRuleState is every saved rule and custom category.
func (s *Server) appRuleState() ([]apps.Rule, []apps.Category, error) {
	if s.auditDB == nil {
		return nil, nil, errors.New("the database is not open")
	}
	rows, err := s.auditDB.AppRules()
	if err != nil {
		return nil, nil, err
	}
	rules := make([]apps.Rule, 0, len(rows))
	for _, row := range rows {
		var r apps.Rule
		if err := json.Unmarshal([]byte(row.Rule), &r); err != nil {
			log.Printf("[apps] rule %s is not readable, skipped: %v", row.ID, err)
			continue
		}
		r.ID = row.ID
		rules = append(rules, r)
	}
	crows, err := s.auditDB.AppCategories()
	if err != nil {
		return nil, nil, err
	}
	cats := make([]apps.Category, 0, len(crows))
	for _, c := range crows {
		cats = append(cats, apps.Category{ID: c.ID, Name: c.Name, Tone: c.Tone})
	}
	return rules, cats, nil
}

// loadAppRules makes the saved rules the catalogue every collector names
// with. At startup, and after every write.
func (s *Server) loadAppRules() {
	if s.auditDB == nil {
		return
	}
	rules, _, err := s.appRuleState()
	if err != nil {
		log.Printf("[apps] the custom apps could not be read: %v", err)
		return
	}
	apps.SetRules(rules)
}

// categorySet is every category an app may be put in.
func categorySet(cats []apps.Category) map[string]bool {
	out := map[string]bool{}
	for _, c := range apps.Default().Categories() {
		out[c] = true
	}
	for _, c := range cats {
		out[c.ID] = true
	}
	return out
}

// appRulesAdmin is the write gate: signed in, a global administrator, and the
// database open.
func (s *Server) appRulesAdmin(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return nil, false
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "the database is not open")
		return nil, false
	}
	return sess, true
}

// ── reading ─────────────────────────────────────────────────────────────────

// AppCatalogueApp is one app as the Manage apps tab lists it.
type AppCatalogueApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Logo     string `json:"logo"`
	Dark     bool   `json:"dark"`
	Icon     string `json:"icon"`
	Custom   bool   `json:"custom"`
	// Rule is the saved rule for this app, nil when there is none.
	Rule *apps.Rule `json:"rule"`
}

// AppCategoryView is one category: a built-in one has no name or tone here
// (the page labels and colours those), a custom one has both.
type AppCategoryView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tone    string `json:"tone"`
	Builtin bool   `json:"builtin"`
	// Apps is how many apps are in it now.
	Apps int `json:"apps"`
}

func categoryViews(cat *apps.Catalogue, cats []apps.Category) []AppCategoryView {
	count := map[string]int{}
	for _, a := range cat.Apps() {
		count[a.Category]++
	}
	out := []AppCategoryView{}
	for _, c := range apps.Default().Categories() {
		out = append(out, AppCategoryView{ID: c, Builtin: true, Apps: count[c]})
	}
	for _, c := range cats {
		out = append(out, AppCategoryView{ID: c.ID, Name: c.Name, Tone: c.Tone, Apps: count[c.ID]})
	}
	return out
}

func (s *Server) appCatalogueGet(w http.ResponseWriter, r *http.Request) {
	sess := s.routerPageGate(w, r, "apps", r.URL.Query().Get("routerId"), "read")
	if sess == nil {
		return
	}
	rules, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	byID := map[string]*apps.Rule{}
	for i := range rules {
		byID[rules[i].ID] = &rules[i]
	}
	cat := apps.Current()
	list := make([]AppCatalogueApp, 0, len(cat.Apps()))
	for _, a := range cat.Apps() {
		list = append(list, AppCatalogueApp{ID: a.ID, Name: a.Name, Category: a.Category, Color: a.Color, Logo: a.Logo,
			Dark: a.Dark, Icon: a.Icon, Custom: a.Custom, Rule: byID[a.ID]})
	}
	writeJSON(w, map[string]any{"apps": list, "categories": categoryViews(cat, cats), "canEdit": s.isGlobalAdmin(sess)})
}

func (s *Server) appCategoriesGet(w http.ResponseWriter, r *http.Request) {
	if s.routerPageGate(w, r, "apps", r.URL.Query().Get("routerId"), "read") == nil {
		return
	}
	_, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"categories": categoryViews(apps.Current(), cats)})
}

// appsCollector is a router's running apps collector, nil when its session is
// not running.
func (s *Server) appsCollector(rid string) *collect.Apps {
	if rs := s.liveSession(rid); rs != nil {
		return rs.Apps()
	}
	return nil
}

// clientParam is the client filter: "" for every client, else an address.
// False when it is neither, having answered.
func clientParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	c := strings.TrimSpace(r.URL.Query().Get("client"))
	if c == "" {
		return "", true
	}
	a, err := netip.ParseAddr(c)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "client is an IP address")
		return "", false
	}
	return a.String(), true
}

func (s *Server) appClientsGet(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerPageGate(w, r, "apps", rid, "read") == nil {
		return
	}
	out := []collect.AppClient{}
	if a := s.appsCollector(rid); a != nil {
		out = a.Clients()
	}
	writeJSON(w, map[string]any{"clients": out})
}

func (s *Server) appLiveGet(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerPageGate(w, r, "apps", rid, "read") == nil {
		return
	}
	client, ok := clientParam(w, r)
	if !ok {
		return
	}
	var p *collect.AppUsagePayload
	if a := s.appsCollector(rid); a != nil {
		p = a.LiveFor(client)
	}
	writeJSON(w, map[string]any{"payload": p})
}

func (s *Server) appUnnamedGet(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	if s.routerPageGate(w, r, "apps", rid, "read") == nil {
		return
	}
	client, ok := clientParam(w, r)
	if !ok {
		return
	}
	out := collect.AppUnnamed{Rows: []collect.AppUnnamedRow{}}
	if a := s.appsCollector(rid); a != nil {
		out = a.Unnamed(client)
	}
	writeJSON(w, out)
}

// ── writing ─────────────────────────────────────────────────────────────────

type appRuleBody struct {
	RouterID string    `json:"routerId"`
	Rule     apps.Rule `json:"rule"`
	// Create: a new custom app, whose id the server gives. Otherwise the rule
	// with Rule.ID is replaced; a built-in app's additions are made the first
	// time they are saved.
	Create bool `json:"create"`
}

func readAppRuleBody(w http.ResponseWriter, r *http.Request) (appRuleBody, bool) {
	var b appRuleBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request")
		return b, false
	}
	if b.Create {
		b.Rule.ID = ""
		b.Rule.Builtin = false
	}
	return b, true
}

func findRule(rules []apps.Rule, id string) *apps.Rule {
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i]
		}
	}
	return nil
}

func hasBlocking(cs []apps.Conflict) bool {
	for _, c := range cs {
		if c.Blocking {
			return true
		}
	}
	return false
}

// judgeRule is a rule cleaned and judged against the saved ones. The status
// is 0 when it may be saved.
func judgeRule(b appRuleBody, rules []apps.Rule, cats []apps.Category) (apps.Rule, []apps.Conflict, int, string) {
	prev := findRule(rules, b.Rule.ID)
	if !b.Create && !b.Rule.Builtin && prev == nil {
		return b.Rule, nil, http.StatusNotFound, "there is no custom app " + b.Rule.ID
	}
	if prev != nil && prev.Builtin != b.Rule.Builtin {
		return b.Rule, nil, http.StatusBadRequest, "a custom app cannot become a built-in one, or the reverse"
	}
	rule, conflicts, err := apps.Default().Check(b.Rule, rules, categorySet(cats))
	if err != nil {
		return rule, conflicts, http.StatusBadRequest, err.Error()
	}
	return rule, conflicts, 0, ""
}

func (s *Server) appRulePreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.appRulesAdmin(w, r); !ok {
		return
	}
	b, ok := readAppRuleBody(w, r)
	if !ok {
		return
	}
	rules, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	rule, conflicts, status, msg := judgeRule(b, rules, cats)
	if status != 0 {
		writeJSONErr(w, status, msg)
		return
	}
	dests, bytes := 0, int64(0)
	if a := s.appsCollector(b.RouterID); a != nil {
		dests, bytes = a.Preview(rule)
	}
	writeJSON(w, map[string]any{"ok": true, "rule": rule, "conflicts": conflicts,
		"matches": map[string]any{"destinations": dests, "bytes": bytes}})
}

func (s *Server) appRuleSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.appRulesAdmin(w, r)
	if !ok {
		return
	}
	b, ok := readAppRuleBody(w, r)
	if !ok {
		return
	}
	appRulesMu.Lock()
	defer appRulesMu.Unlock()
	rules, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	rule, conflicts, status, msg := judgeRule(b, rules, cats)
	if status != 0 {
		writeJSONErr(w, status, msg)
		return
	}
	if hasBlocking(conflicts) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "conflicts": conflicts,
			"error": "another rule already claims some of this; see the conflicts"})
		return
	}
	raw, err := json.Marshal(rule)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	prev := findRule(rules, rule.ID)
	if err := s.auditDB.PutAppRule(rule.ID, string(raw), s.userIDFor(sess.Username), time.Now().UnixMilli()); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.loadAppRules()
	ev := audit.Event{Action: "apps.rule.create", TargetType: "app-rule", TargetID: rule.ID, TargetName: rule.Name,
		After: ruleAudit(rule)}
	if prev != nil {
		ev.Action, ev.Before = "apps.rule.update", ruleAudit(*prev)
	}
	s.httpRecorder(r, sess).Record(ev)
	writeJSON(w, map[string]any{"ok": true, "rule": rule, "conflicts": conflicts})
}

// ruleAudit is a rule as the audit trail records it.
func ruleAudit(r apps.Rule) map[string]any {
	return map[string]any{"builtin": r.Builtin, "name": r.Name, "category": r.Category, "color": r.Color,
		"icon": r.Icon, "domains": r.Domains, "asns": r.ASNs, "ports": r.Ports}
}

func (s *Server) appRuleDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.appRulesAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	appRulesMu.Lock()
	defer appRulesMu.Unlock()
	rules, _, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	prev := findRule(rules, id)
	if prev == nil {
		writeJSONErr(w, http.StatusNotFound, "there is no rule "+id)
		return
	}
	if err := s.auditDB.DeleteAppRule(id); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	// A custom app's stored hours become unnamed traffic: the bytes were
	// moved, and the app that named them is gone (internal/db/apprules.go).
	if !prev.Builtin {
		if err := s.auditDB.FoldAppUsage(id, apps.UnnamedID); err != nil {
			log.Printf("[apps] folding %s's history into unnamed: %v", id, err)
		}
	}
	s.loadAppRules()
	s.httpRecorder(r, sess).Record(audit.Event{Action: "apps.rule.delete", TargetType: "app-rule", TargetID: id,
		TargetName: prev.Name, Before: ruleAudit(*prev)})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) appCategorySave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.appRulesAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Category apps.Category `json:"category"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	appRulesMu.Lock()
	defer appRulesMu.Unlock()
	_, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	var prev *apps.Category
	for i := range cats {
		if cats[i].ID == body.Category.ID {
			prev = &cats[i]
		}
	}
	if body.Category.ID != "" && prev == nil {
		writeJSONErr(w, http.StatusNotFound, "there is no category "+body.Category.ID)
		return
	}
	c, err := apps.CheckCategory(body.Category, apps.Default().Categories(), cats)
	if err != nil {
		writeJSONErrFrom(w, http.StatusBadRequest, err)
		return
	}
	if err := s.auditDB.PutAppCategory(c.ID, c.Name, c.Tone, s.userIDFor(sess.Username), time.Now().UnixMilli()); err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	ev := audit.Event{Action: "apps.category.create", TargetType: "app-category", TargetID: c.ID, TargetName: c.Name,
		After: map[string]any{"name": c.Name, "tone": c.Tone}}
	if prev != nil {
		ev.Action, ev.Before = "apps.category.update", map[string]any{"name": prev.Name, "tone": prev.Tone}
	}
	s.httpRecorder(r, sess).Record(ev)
	writeJSON(w, map[string]any{"ok": true, "category": c})
}

func (s *Server) appCategoryDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.appRulesAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	appRulesMu.Lock()
	defer appRulesMu.Unlock()
	rules, cats, err := s.appRuleState()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	name := ""
	for _, c := range cats {
		if c.ID == id {
			name = c.Name
		}
	}
	if name == "" {
		writeJSONErr(w, http.StatusNotFound, "there is no category "+id)
		return
	}
	// A category in use is not deleted from under its apps: they are moved
	// out of it first, deliberately.
	for _, rule := range rules {
		if rule.Category == id {
			writeJSONErr(w, http.StatusConflict, "this category still holds apps; move them to another category first")
			return
		}
	}
	if err := s.auditDB.DeleteAppCategory(id); err != nil {
		if errors.Is(err, db.ErrAppRuleNotFound) {
			writeJSONErr(w, http.StatusNotFound, "there is no category "+id)
			return
		}
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "apps.category.delete", TargetType: "app-category",
		TargetID: id, TargetName: name, Before: map[string]any{"name": name}})
	writeJSON(w, map[string]any{"ok": true})
}
