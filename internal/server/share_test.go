package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mikrodash/internal/db"
	"mikrodash/internal/pages"
	"mikrodash/internal/sharelink"
	"mikrodash/internal/store"
)

const shareRoutersJSON = `[
  {"id":"r1","label":"Edge","host":"198.51.100.1","port":8728,"username":"u","password":""},
  {"id":"r2","label":"Branch","host":"198.51.100.2","port":8728,"username":"u","password":""},
  {"id":"s1","kind":"swos","label":"Core switch","host":"198.51.100.20","scheme":"http","port":80,"username":"admin","password":""}
]`

// shareServer is the real server: alice (u-1) a signed-in global
// administrator behind cookie "admin", bob (u-2) a signed-in user with no
// grants behind cookie "bob".
func shareServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".secret": "test-secret", "settings.json": `{}`, "routers.json": shareRoutersJSON,
		"users.json": seedUsersJSON,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	srv, err := New(st, Options{WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	alice := &Session{Username: "alice"}
	signedInAdmin(t, srv, alice)
	bob := &Session{Username: "bob"}
	a := NewAuth()
	a.SetLocal(func(tok string) (*Session, bool) {
		switch tok {
		case "admin":
			return alice, true
		case "bob":
			return bob, true
		}
		return nil, false
	})
	srv.auth = a
	return srv, srv.Handler()
}

func shareReq(h http.Handler, method, path, body, cookie, ip string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if ip == "" {
		ip = "10.20.0.1"
	}
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// createLink makes a link as alice and returns its id and token.
func createLink(t *testing.T, h http.Handler, body string) (id, token string) {
	t.Helper()
	w := shareReq(h, "POST", "/api/share-links", body, "admin", "", nil)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		URL  string         `json:"url"`
		Link map[string]any `json:"link"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	i := strings.Index(out.URL, "/s/")
	if i < 0 {
		t.Fatalf("no /s/ URL: %q", out.URL)
	}
	return out.Link["id"].(string), out.URL[i+3:]
}

const pageLink = `{"kind":"page","label":"Lobby","page":"dashboard","routerId":"r1","expiry":{"choice":"7d"}}`

func TestOnlyAGlobalAdministratorManagesShareLinks(t *testing.T) {
	_, h := shareServer(t)
	routes := [][2]string{{"GET", "/api/share-links"}, {"GET", "/api/share-links/options"},
		{"POST", "/api/share-links"}, {"PUT", "/api/share-links/x1"},
		{"POST", "/api/share-links/x1/revoke"}, {"DELETE", "/api/share-links/x1"}}
	for _, r := range routes {
		if w := shareReq(h, r[0], r[1], pageLink, "", "", nil); w.Code != 401 {
			t.Errorf("%s %s with no session: %d, want 401", r[0], r[1], w.Code)
		}
		if w := shareReq(h, r[0], r[1], pageLink, "bob", "", nil); w.Code != 403 {
			t.Errorf("%s %s as a user without administration: %d, want 403", r[0], r[1], w.Code)
		}
	}
	if w := shareReq(h, "GET", "/api/share-links", "", "admin", "", nil); w.Code != 200 {
		t.Errorf("control: the administrator's list answered %d", w.Code)
	}
}

func TestATokenIsShownOnceAndOnlyItsHashIsStored(t *testing.T) {
	srv, h := shareServer(t)
	id, token := createLink(t, h, pageLink)
	if !sharelink.WellFormedToken(token) {
		t.Fatalf("token %q", token)
	}
	l, _ := srv.auditDB.ShareLinkByID(id)
	if l == nil || !bytes.Equal(l.TokenHash, sharelink.HashToken(token)) || l.CreatedBy != "u-1" {
		t.Fatalf("stored: %+v", l)
	}
	list := shareReq(h, "GET", "/api/share-links", "", "admin", "", nil).Body.String()
	if strings.Contains(list, token) || strings.Contains(strings.ToLower(list), "tokenhash") {
		t.Error("the list carries the token or its hash")
	}
}

func TestACreateIsCheckedAgainstTheRules(t *testing.T) {
	_, h := shareServer(t)
	for why, body := range map[string]string{
		"an unshareable page": `{"kind":"page","label":"x","page":"settings","routerId":"r1"}`,
		"scripts":             `{"kind":"page","label":"x","page":"scripts","routerId":"r1"}`,
		"a switch":            `{"kind":"page","label":"x","page":"dashboard","routerId":"s1"}`,
		"no such router":      `{"kind":"page","label":"x","page":"dashboard","routerId":"nope"}`,
		"an http wildcard":    `{"kind":"page","label":"x","page":"dashboard","routerId":"r1","frameOrigins":["http://*.example.com"]}`,
		"a bare wildcard":     `{"kind":"page","label":"x","page":"dashboard","routerId":"r1","frameOrigins":["*"]}`,
		"an allow-all range":  `{"kind":"page","label":"x","page":"dashboard","routerId":"r1","allowedAddresses":["0.0.0.0/0"]}`,
		"a cap over the max":  `{"kind":"page","label":"x","page":"dashboard","routerId":"r1","viewerCap":26}`,
		"one slide":           `{"kind":"slideshow","label":"x","slides":[{"page":"dashboard","routerId":"r1"}]}`,
		"the agent card":      `{"kind":"card","label":"x","routerId":"r1","options":{"card":"dc-card-agent"}}`,
		"a feed with origins": `{"kind":"feed","label":"x","routerId":"r1","options":{"values":["cpu"]},"frameOrigins":["https://ha.example.com"]}`,
		"no label":            `{"kind":"page","label":" ","page":"dashboard","routerId":"r1"}`,
	} {
		if w := shareReq(h, "POST", "/api/share-links", body, "admin", "", nil); w.Code != 400 {
			t.Errorf("%s: answered %d, want 400 (%s)", why, w.Code, w.Body.String())
		}
	}
}

func TestEveryRefusalLooksTheSame(t *testing.T) {
	_, h := shareServer(t)
	_, valid := createLink(t, h, pageLink)
	revokedID, revoked := createLink(t, h, pageLink)
	shareReq(h, "POST", "/api/share-links/"+revokedID+"/revoke", "", "admin", "", nil)
	_, limited := createLink(t, h, `{"kind":"page","label":"Lobby","page":"dashboard","routerId":"r1","allowedAddresses":["198.51.100.99"]}`)
	if w := shareReq(h, "GET", "/s/"+valid, "", "", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"ticket"`) {
		t.Fatalf("control: a valid link answered %d", w.Code)
	}
	var first *httptest.ResponseRecorder
	for why, tok := range map[string]string{
		"unknown": strings.Repeat("A", 43), "malformed": "abc", "revoked": revoked, "refused address": limited,
	} {
		w := shareReq(h, "GET", "/s/"+tok, "", "", "10.30.0.1", nil)
		if w.Code != 404 {
			t.Errorf("%s: %d", why, w.Code)
		}
		if first == nil {
			first = w
			continue
		}
		if w.Body.String() != first.Body.String() ||
			w.Header().Get("Content-Security-Policy") != first.Header().Get("Content-Security-Policy") {
			t.Errorf("%s: the refusal differs from the others", why)
		}
	}
}

func TestFramingFollowsTheLink(t *testing.T) {
	_, h := shareServer(t)
	_, plain := createLink(t, h, pageLink)
	_, framed := createLink(t, h, `{"kind":"page","label":"HA","page":"dashboard","routerId":"r1","frameOrigins":["https://ha.example.com","https://*.example.org"]}`)
	w := shareReq(h, "GET", "/s/"+plain, "", "", "", nil)
	if w.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("no origins: CSP %q, XFO %q", w.Header().Get("Content-Security-Policy"), w.Header().Get("X-Frame-Options"))
	}
	w = shareReq(h, "GET", "/s/"+framed, "", "", "", nil)
	if w.Header().Get("Content-Security-Policy") != "frame-ancestors https://ha.example.com https://*.example.org" ||
		w.Header().Get("X-Frame-Options") != "" {
		t.Errorf("with origins: CSP %q, XFO %q", w.Header().Get("Content-Security-Policy"), w.Header().Get("X-Frame-Options"))
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the share page is missing no-referrer or no-store")
	}
	if !strings.Contains(w.Body.String(), `"view":"embed"`) {
		t.Error("a link with embedding origins did not default to the Embed view")
	}
}

func ticketOf(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`"ticket":"([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no ticket in %q", body)
	}
	return m[1]
}

func TestATicketRenewsOnlyWhileItsLinkIsValid(t *testing.T) {
	_, h := shareServer(t)
	id, token := createLink(t, h, pageLink)
	tk := ticketOf(t, shareReq(h, "GET", "/s/"+token, "", "", "", nil).Body.String())
	auth := map[string]string{"Authorization": "Share " + tk}
	if w := shareReq(h, "POST", "/share/api/ticket", "", "", "", auth); w.Code != 200 {
		t.Fatalf("a valid ticket did not renew: %d", w.Code)
	}
	if w := shareReq(h, "POST", "/share/api/ticket", "", "", "", map[string]string{"Authorization": "Share " + tk + "x"}); w.Code != 401 {
		t.Errorf("a forged ticket renewed: %d", w.Code)
	}
	shareReq(h, "POST", "/api/share-links/"+id+"/revoke", "", "admin", "", nil)
	if w := shareReq(h, "POST", "/share/api/ticket", "", "", "", auth); w.Code != 401 {
		t.Errorf("a revoked link's ticket renewed: %d", w.Code)
	}
}

// TestAShareOpensNothingElse: a valid ticket and a valid feed token, sent to
// every other route with no session, change no answer: each route answers
// exactly as it does to the same request with no credential at all.
func TestAShareOpensNothingElse(t *testing.T) {
	_, h := shareServer(t)
	_, token := createLink(t, h, pageLink)
	_, feed := createLink(t, h, `{"kind":"feed","label":"HA","routerId":"r1","options":{"values":["cpu"]}}`)
	tk := ticketOf(t, shareReq(h, "GET", "/s/"+token, "", "", "", nil).Body.String())
	own := map[string]bool{"GET /s/{token}": true, "GET /share/ws": true,
		"POST /share/api/ticket": true, "GET /share/api/feed": true, "GET /share.js": true,
		"GET /share.css": true}
	checked := 0
	for i, pattern := range routesFromSource(t) {
		if own[pattern] {
			continue
		}
		ip := "10.40." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250+1)
		bare := httptest.NewRecorder()
		h.ServeHTTP(bare, requestForRoute(pattern, ip))
		for _, auth := range []string{"Share " + tk, "Bearer " + feed} {
			req := requestForRoute(pattern, ip)
			req.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != bare.Code && w.Code != http.StatusTooManyRequests && bare.Code != http.StatusTooManyRequests {
				t.Errorf("%s answered %d to a share credential and %d without one", pattern, w.Code, bare.Code)
			}
			checked++
		}
	}
	if checked < 200 {
		t.Fatalf("only %d requests checked", checked)
	}
}

func TestTheFeedTakesItsTokenInAHeaderOnly(t *testing.T) {
	_, h := shareServer(t)
	_, feed := createLink(t, h, `{"kind":"feed","label":"HA","routerId":"r1","options":{"values":["cpu","online","model","interfaces"],"interfaces":["ether1"]}}`)
	bearer := map[string]string{"Authorization": "Bearer " + feed}
	w := shareReq(h, "GET", "/share/api/feed", "", "", "", bearer)
	if w.Code != 200 {
		t.Fatalf("a valid feed: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if _, ok := got["updated_at"]; !ok || got["cpu"] != nil || got["model"] != nil {
		t.Errorf("a router nobody holds: %v (values not held must be null)", got)
	}
	if w := shareReq(h, "GET", "/share/api/feed?token="+feed, "", "", "10.50.0.1", nil); w.Code != 401 {
		t.Errorf("a token in the URL was accepted: %d", w.Code)
	}
	for i := 0; i < sharelink.FeedPollsPerMinute; i++ {
		shareReq(h, "GET", "/share/api/feed", "", "", "", bearer)
	}
	if w := shareReq(h, "GET", "/share/api/feed", "", "", "", bearer); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Errorf("a poll past the limit answered %d", w.Code)
	}
	// A feed token does not open a page.
	if w := shareReq(h, "GET", "/s/"+feed, "", "", "", nil); w.Code != 404 {
		t.Errorf("a feed token opened a share page: %d", w.Code)
	}
}

// TestALinkDiesWithItsCreatorsAdministration: auto-revocation at use.
func TestALinkDiesWithItsCreatorsAdministration(t *testing.T) {
	srv, h := shareServer(t)
	id, token := createLink(t, h, pageLink)
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 200 {
		t.Fatalf("control: %d", w.Code)
	}
	srv.rbac = nil // the grant graph no longer says alice is an administrator
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 404 {
		t.Errorf("the creator lost administration and the link still opened: %d", w.Code)
	}
	l, _ := srv.auditDB.ShareLinkByID(id)
	if l == nil || l.RevokedAt == nil || l.RevokeReason != sharelink.ReasonCreatorNotAdmin || l.RevokedBy != "" {
		t.Errorf("the link was not revoked automatically with its reason: %+v", l)
	}
}

func TestADeletedRouterRevokesItsLinks(t *testing.T) {
	srv, h := shareServer(t)
	id, token := createLink(t, h, `{"kind":"slideshow","label":"Wall","slides":[{"page":"dashboard","routerId":"r1"},{"page":"interfaces","routerId":"r2"}]}`)
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 200 {
		t.Fatalf("control: %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(srv.store.Dir, "routers.json"),
		[]byte(`[{"id":"r1","label":"Edge","host":"198.51.100.1","port":8728,"username":"u","password":""}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 404 {
		t.Errorf("a slideshow survived the deletion of one of its routers: %d", w.Code)
	}
	l, _ := srv.auditDB.ShareLinkByID(id)
	if l == nil || l.RevokeReason != sharelink.ReasonRouterDeleted {
		t.Errorf("not revoked for the router: %+v", l)
	}
}

func TestAnEditKeepsTheTokenAndIsRechecked(t *testing.T) {
	srv, h := shareServer(t)
	id, token := createLink(t, h, pageLink)
	edit := `{"kind":"page","label":"Lobby 2","page":"interfaces","routerId":"r2","keepExpiry":true}`
	if w := shareReq(h, "PUT", "/api/share-links/"+id, edit, "admin", "", nil); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	l, _ := srv.auditDB.ShareLinkByID(id)
	if l.Page != "interfaces" || l.RouterID != "r2" || !bytes.Equal(l.TokenHash, sharelink.HashToken(token)) || l.CreatedBy != "u-1" {
		t.Errorf("after the edit: %+v", l)
	}
	bad := `{"kind":"page","label":"x","page":"files","routerId":"r2"}`
	if w := shareReq(h, "PUT", "/api/share-links/"+id, bad, "admin", "", nil); w.Code != 400 {
		t.Errorf("an edit to an unshareable page: %d", w.Code)
	}
	page, _ := srv.auditDB.QueryAuditEvents(db.Query{IncludeApp: true, RouterIDs: []string{"r1", "r2"}, Action: "share.update", Limit: 20})
	routers := map[string]bool{}
	for _, row := range page.Rows {
		if row.RouterID != nil {
			routers[*row.RouterID] = true
		}
	}
	if !routers["r1"] || !routers["r2"] {
		t.Errorf("the edit was not audited on both the old and the new router: %v", routers)
	}
	if w := shareReq(h, "DELETE", "/api/share-links/"+id, "", "admin", "", nil); w.Code != 409 {
		t.Errorf("an active link was deleted: %d", w.Code)
	}
}

// TestAWriteRevokesWithoutWaitingForAUse: the write-time sweep. Deleting a
// router through its route, and a permission change, revoke the links they
// end before anybody opens them.
func TestAWriteRevokesWithoutWaitingForAUse(t *testing.T) {
	srv, h := shareServer(t)
	onR2, _ := createLink(t, h, `{"kind":"page","label":"Branch","page":"dashboard","routerId":"r2"}`)
	onR1, _ := createLink(t, h, pageLink)
	if w := shareReq(h, "DELETE", "/api/routers/r2", "", "admin", "", nil); w.Code != 200 {
		t.Fatalf("delete router: %d %s", w.Code, w.Body.String())
	}
	if l, _ := srv.auditDB.ShareLinkByID(onR2); l == nil || l.RevokeReason != sharelink.ReasonRouterDeleted {
		t.Errorf("the deleted router's link was not revoked by the delete: %+v", l)
	}
	if l, _ := srv.auditDB.ShareLinkByID(onR1); l == nil || l.RevokedAt != nil {
		t.Fatalf("control: the other router's link was revoked too: %+v", l)
	}
	srv.rbac = nil
	srv.bumpPermissions()
	if l, _ := srv.auditDB.ShareLinkByID(onR1); l == nil || l.RevokeReason != sharelink.ReasonCreatorNotAdmin {
		t.Errorf("a permission change did not revoke the link of a creator who lost administration: %+v", l)
	}
}

// TestAShareHearsOnlyItsSubject: the hub filter a share's client carries.
func TestAShareHearsOnlyItsSubject(t *testing.T) {
	sc := &shareConn{}
	if _, ok := sc.filter("router-r1-page-dns", "dns:update", 1); ok {
		t.Error("a share showing nothing yet accepted a frame")
	}
	sc.view.Store(&shareView{routerID: "r1", page: "dns"})
	for _, c := range []struct {
		room, event string
		want        bool
	}{
		{"router-r1", "system:update", true},
		{"router-r1", "router:status", true},
		{"router-r1", "alerts:acked", false},
		{"router-r1", "collection:config", false},
		{"router-r1", "router:disabled", false},
		{"router-r1", "leases:list", false}, // the DNS page does not name devices from leases
		{"router-r1-page-dns", "dns:update", true},
		{"", "dns:update", true}, // the replay of its own page
	} {
		if _, got := sc.filter(c.room, c.event, 1); got != c.want {
			t.Errorf("page dns: %q %s = %v, want %v", c.room, c.event, got, c.want)
		}
	}
	sc.view.Store(&shareView{routerID: "r1", page: "dhcp"})
	if _, ok := sc.filter("router-r1", "leases:list", 1); !ok {
		t.Error("the DHCP page did not receive the lease list")
	}
}

// TestACardLinksSocketSendsOnlyItsCardsData: for every shareable card, the
// socket filter drops every event the card does not read, from any room or a
// direct send, and sends each one it does read exactly as its fields project
// (sharelink.TestEveryCardLinkReceivesOnlyItsCardsFields holds the fields).
func TestACardLinksSocketSendsOnlyItsCardsData(t *testing.T) {
	events := []string{"system:update", "conn:update", "traffic:update", "traffic:history", "ifstatus:update",
		"ifstatus:names", "wireless:update", "lan:overview", "lan:wan", "vpn:update", "firewall:update",
		"logs:history", "logs:new", "wan:update", "wan:status", "ping:update", "ping:history",
		"talkers:update", "netwatch:update", "routing:update", "stream:health", "leases:list",
		"router:status", "alerts:acked", "dns:update"}
	payload := map[string]any{"secret": "s", "total": 3, "cpuLoad": 1, "rx_mbps": 2,
		"interfaces": []any{map[string]any{"name": "ether1", "type": "ether", "mac": "m"}}}
	cards := 0
	for _, c := range sharelink.ShareableCards() {
		data, _ := sharelink.CardDataOf(c.ID)
		cards++
		sc := &shareConn{}
		sc.view.Store(&shareView{routerID: "r1", card: data})
		for _, ev := range events {
			for _, room := range []string{"", "router-r1", "router-r1-page-dashboard", "router-r1-dash-card-connections"} {
				out, ok := sc.filter(room, ev, payload)
				paths, reads := data[ev]
				if ok != reads {
					t.Errorf("%s: %s via %q passed %v, want %v", c.ID, ev, room, ok, reads)
					continue
				}
				if !ok {
					continue
				}
				want, _ := sharelink.Project(paths, payload)
				if got, _ := json.Marshal(out); string(got) != string(want) {
					t.Errorf("%s: %s sent %s, want its projection %s", c.ID, ev, got, want)
				}
				if strings.Contains(string(want), "secret") {
					t.Errorf("%s: %s carried a field no card reads", c.ID, ev)
				}
			}
		}
		for _, ctl := range []string{"share:end", "share:reload", "router:active", "router:switched"} {
			if out, ok := sc.filter("", ctl, payload); !ok || out != nil {
				t.Errorf("%s: control event %s was not sent whole", c.ID, ctl)
			}
		}
	}
	if cards == 0 {
		t.Fatal("no shareable cards")
	}
}

func shareDial(t *testing.T, base, token string, h http.Handler) (*websocket.Conn, error) {
	t.Helper()
	tk := ticketOf(t, shareReq(h, "GET", "/s/"+token, "", "", "", nil).Body.String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/share/ws",
		&websocket.DialOptions{Subprotocols: []string{shareSubprotocol, tk}})
	return c, err
}

// readUntilClose reads frames until the socket closes, and returns the close code.
func readUntilClose(c *websocket.Conn, within time.Duration) websocket.StatusCode {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestAnEditClosesTheLiveViewAndTheCapHolds(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	id, token := createLink(t, h, `{"kind":"page","label":"Lobby","page":"bandwidth","routerId":"r1","viewerCap":1}`)
	first, err := shareDial(t, ts.URL, token, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer first.CloseNow()
	waitFor(t, func() bool { return srv.shareLive.count(id) == 1 })
	second, err := shareDial(t, ts.URL, token, h)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if code := readUntilClose(second, 3*time.Second); code != 4003 {
		t.Errorf("a viewer past the cap closed with %d, want 4003", code)
	}
	edit := `{"kind":"page","label":"Lobby","page":"interfaces","routerId":"r2","viewerCap":1,"keepExpiry":true}`
	if w := shareReq(h, "PUT", "/api/share-links/"+id, edit, "admin", "", nil); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if code := readUntilClose(first, 3*time.Second); code != 4002 {
		t.Errorf("the live view of an edited link closed with %d, want 4002", code)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// TestWarmUpJoinsOnlyTheNextSlideAhead: share:warm joins the next slide's
// room early, on the same router, and nothing else.
func TestWarmUpJoinsOnlyTheNextSlideAhead(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	_, token := createLink(t, h, `{"kind":"slideshow","label":"Wall","options":{"warmUpSecs":5},"slides":[
		{"page":"bandwidth","routerId":"r1"},{"page":"interfaces","routerId":"r1"},{"page":"wifi-clients","routerId":"r1"}]}`)
	c, err := shareDial(t, ts.URL, token, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	send := func(event string, i int) {
		b, _ := json.Marshal(map[string]any{"event": event, "data": i})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	room := func(p string) int { return srv.hub.Occupants("router-r1-page-" + p) }
	send("share:slide", 0)
	waitFor(t, func() bool { return room("bandwidth") == 1 })
	send("share:warm", 2) // not the next slide
	// Absence cannot be waited for, and any other frame would move the
	// rooms; so a plain wait, ample for one frame on a local socket.
	time.Sleep(300 * time.Millisecond)
	if room("wifi-clients") != 0 {
		t.Error("a slide two ahead was warmed")
	}
	send("share:warm", 1)
	waitFor(t, func() bool { return room("interfaces") == 1 })
	send("share:slide", 1)
	waitFor(t, func() bool { return room("bandwidth") == 0 })
	if room("interfaces") != 1 {
		t.Errorf("the warmed slide's room after it was shown: %d occupants", room("interfaces"))
	}
}

// TestTheShareReadListIsTheAllowList: the read routes are an explicit list,
// not empty, each a real GET route of the app, each for shareable pages, and
// a router-doc entry names the kinds its page reads.
func TestTheShareReadListIsTheAllowList(t *testing.T) {
	if len(shareReads) == 0 {
		t.Fatal("the share read allow list is empty")
	}
	routes := map[string]bool{}
	for _, p := range routesFromSource(t) {
		routes[p] = true
	}
	for _, rd := range shareReads {
		if !routes["GET "+rd.Path] {
			t.Errorf("%s is on the share read list but is not a GET route of the app", rd.Path)
		}
		if len(rd.Pages) == 0 || rd.serve == nil {
			t.Errorf("%s names no page or no handler", rd.Path)
		}
		for _, p := range rd.Pages {
			if !pages.IsShareable(p) {
				t.Errorf("%s is listed for %s, which is not shareable", rd.Path, p)
			}
		}
		if rd.Path == "/api/router-doc" && len(rd.Kinds) == 0 {
			t.Errorf("a router-doc entry for %v names no kinds: every document would be readable", rd.Pages)
		}
	}
}

// openShareView opens a link's page and its socket, and returns the ticket.
func openShareView(t *testing.T, ts *httptest.Server, h http.Handler, token string) (*websocket.Conn, string) {
	t.Helper()
	tk := ticketOf(t, shareReq(h, "GET", "/s/"+token, "", "", "", nil).Body.String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/share/ws",
		&websocket.DialOptions{Subprotocols: []string{shareSubprotocol, tk}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c, tk
}

func TestAShareReadIsAnsweredOnlyOnItsListForAnOpenView(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	id, token := createLink(t, h, `{"kind":"page","label":"Topology","page":"network-topology","routerId":"r1"}`)
	c, tk := openShareView(t, ts, h, token)
	defer c.CloseNow()
	waitFor(t, func() bool { return srv.shareLive.count(id) == 1 })
	auth := map[string]string{"Authorization": "Share " + tk}

	// On the list for this page, and the router is the socket's, whatever the query says.
	w := shareReq(h, "GET", "/share/api/router-doc?kind=topology-links&routerId=r2", "", "", "", auth)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"routerId":"r1"`) {
		t.Fatalf("an allowed read: %d %s", w.Code, w.Body.String())
	}
	refused := shareReq(h, "GET", "/s/"+strings.Repeat("A", 43), "", "", "10.60.0.1", nil).Body.String()
	for why, path := range map[string]string{
		"not on any list":          "/share/api/routers",
		"another page's read":      "/share/api/interfaces/history?interface=ether1&range=1h",
		"another kind of document": "/share/api/router-doc?kind=wan-uplinks",
		"an administrative read":   "/share/api/users",
	} {
		w := shareReq(h, "GET", path, "", "", "", auth)
		if w.Code != 404 || w.Body.String() != refused {
			t.Errorf("%s (%s): %d, want the one refusal", why, path, w.Code)
		}
	}
	if w := shareReq(h, "POST", "/share/api/router-doc?kind=topology-links", "{}", "", "", auth); w.Code == 200 {
		t.Error("a POST to a share read was answered")
	}
	// A ticket whose view has no open socket: past the cap, or never connected.
	other := ticketOf(t, shareReq(h, "GET", "/s/"+token, "", "", "", nil).Body.String())
	if w := shareReq(h, "GET", "/share/api/router-doc?kind=topology-links", "", "", "", map[string]string{"Authorization": "Share " + other}); w.Code != 404 {
		t.Errorf("a read for a view with no open socket: %d", w.Code)
	}
	shareReq(h, "POST", "/api/share-links/"+id+"/revoke", "", "admin", "", nil)
	if w := shareReq(h, "GET", "/share/api/router-doc?kind=topology-links", "", "", "", auth); w.Code != 404 {
		t.Errorf("a revoked link's read: %d", w.Code)
	}
}

// TestASignedInSessionOpensNoShareRoute: a share never uses a session. An
// administrator's cookie, with no ticket, opens no share route; with a
// ticket, the cookie changes nothing.
func TestASignedInSessionOpensNoShareRoute(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	id, token := createLink(t, h, `{"kind":"page","label":"Topology","page":"network-topology","routerId":"r1"}`)
	for _, path := range []string{"/share/api/router-doc?kind=topology-links", "/share/api/feed"} {
		if w := shareReq(h, "GET", path, "", "admin", "", nil); w.Code == 200 {
			t.Errorf("%s answered an administrator's cookie with no ticket", path)
		}
	}
	if w := shareReq(h, "POST", "/share/api/ticket", "", "admin", "", nil); w.Code != 401 {
		t.Errorf("a ticket was issued for a cookie: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/share/ws", nil)
	req.Header.Set("Cookie", "mikrodash_sid=admin")
	req.RemoteAddr = "10.20.0.1:1"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("the share socket accepted a cookie with no ticket: %d", w.Code)
	}
	// With a ticket, a cookie alongside changes no answer.
	c, tk := openShareView(t, ts, h, token)
	defer c.CloseNow()
	waitFor(t, func() bool { return srv.shareLive.count(id) == 1 })
	auth := map[string]string{"Authorization": "Share " + tk}
	a := shareReq(h, "GET", "/share/api/wifi/history", "", "", "", auth)
	b := shareReq(h, "GET", "/share/api/wifi/history", "", "admin", "", auth)
	if a.Code != 404 || b.Code != a.Code || b.Body.String() != a.Body.String() {
		t.Errorf("a cookie changed a share read's answer: %d then %d", a.Code, b.Code)
	}
}

// TestAShareReadsItsOwnRoutersCountry: the local country a shared page reads
// is the SHARED router's, never the install's active router (which the link
// may not show), and the WAN address is never sent.
func TestAShareReadsItsOwnRoutersCountry(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	routers := `[
  {"id":"r1","label":"Edge","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "geo":{"place":{"name":"Marl","region":"North Rhine-Westphalia","cc":"DE","lat":51.6567,"lon":7.09038}}},
  {"id":"r2","label":"Branch","host":"198.51.100.2","port":8728,"username":"u","password":"",
   "geo":{"place":{"name":"Lyon","region":"Auvergne-Rhone-Alpes","cc":"FR","lat":45.764,"lon":4.8357}}}]`
	for name, body := range map[string]string{"routers.json": routers, "settings.json": `{"activeRouterId":"r2"}`} {
		if err := os.WriteFile(filepath.Join(srv.store.Dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if cc := srv.activeRouterPlaceCC(); cc != "FR" {
		t.Fatalf("control: the active router's country is %q, want FR", cc)
	}
	id, token := createLink(t, h, `{"kind":"page","label":"Home","page":"dashboard","routerId":"r1"}`)
	c, tk := openShareView(t, ts, h, token)
	defer c.CloseNow()
	waitFor(t, func() bool { return srv.shareLive.count(id) == 1 })
	w := shareReq(h, "GET", "/share/api/localcc", "", "", "", map[string]string{"Authorization": "Share " + tk})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cc":"DE"`) || !strings.Contains(w.Body.String(), `"wanIp":""`) {
		t.Errorf("a share of r1 read %d %s; want r1's country (DE) and no WAN address", w.Code, w.Body.String())
	}
}

// TestALinkWhosePageIsNoLongerSharedIsRefusedNotRevoked: the operator widens
// and narrows the shared pages. A link to a page he stops sharing is refused
// while that lasts, and works again, the same link, when he shares it again.
func TestALinkWhosePageIsNoLongerSharedIsRefusedNotRevoked(t *testing.T) {
	srv, h := shareServer(t)
	id, token := createLink(t, h, `{"kind":"page","label":"Ifaces","page":"interfaces","routerId":"r1"}`)
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 200 {
		t.Fatalf("control: %d", w.Code)
	}
	i := slices.IndexFunc(pages.All, func(p pages.Page) bool { return p.Key == "interfaces" })
	pages.All[i].Share = pages.NotShareable
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 404 {
		t.Errorf("a link to a page no longer shared opened: %d", w.Code)
	}
	if l, _ := srv.auditDB.ShareLinkByID(id); l == nil || l.RevokedAt != nil {
		t.Fatalf("the link was revoked; it must only be refused: %+v", l)
	}
	list := shareReq(h, "GET", "/api/share-links", "", "admin", "", nil).Body.String()
	if !strings.Contains(list, "refused until it is") {
		t.Error("Settings does not say why the link is refused")
	}
	pages.All[i].Share = pages.Shareable
	if w := shareReq(h, "GET", "/s/"+token, "", "", "", nil); w.Code != 200 {
		t.Errorf("the same link did not work again once its page was shared again: %d", w.Code)
	}
}

// TestASharedDevicesPageShowsOnlyItsRouter: the Devices page, shared, gets
// its two-second stats frame and its overview for the shared router only,
// never the rest of the fleet.
func TestASharedDevicesPageShowsOnlyItsRouter(t *testing.T) {
	srv, h := shareServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	id, token := createLink(t, h, `{"kind":"page","label":"Devices","page":"devices","routerId":"r1"}`)
	c, tk := openShareView(t, ts, h, token)
	defer c.CloseNow()
	waitFor(t, func() bool { return srv.shareLive.count(id) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var rows []map[string]any
	for rows == nil {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("no routers:stats frame arrived: %v", err)
		}
		var f struct {
			Event string           `json:"event"`
			Data  []map[string]any `json:"data"`
		}
		if json.Unmarshal(b, &f) == nil && f.Event == "routers:stats" {
			rows = f.Data
		}
	}
	if len(rows) != 1 || rows[0]["id"] != "r1" {
		t.Fatalf("the stats frame carried %d rows (%v); want only r1", len(rows), rows)
	}
	for _, k := range []string{"host", "publicIp", "serial", "lastError", "loginProfile", "siteId", "siteName", "geo"} {
		if v := rows[0][k]; v != nil && v != "" {
			t.Errorf("the shared row carries %s (%v): where a router is reached is never shared", k, v)
		}
	}
	if rows[0]["label"] != "Edge" {
		t.Errorf("control: the shared row lost its name: %v", rows[0]["label"])
	}
	w := shareReq(h, "GET", "/share/api/devices/overview", "", "", "", map[string]string{"Authorization": "Share " + tk})
	var ov []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ov)
	if w.Code != 200 || len(ov) != 1 || ov[0]["routerId"] != "r1" || ov[0]["backup"] != nil {
		t.Errorf("the overview: %d %s; want r1's strip only, without its backups", w.Code, w.Body.String())
	}
}
