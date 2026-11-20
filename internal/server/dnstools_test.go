package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// matchIDs are the ids of the matches, sorted: the trace imposes no order.
func matchIDs(ms []DNSTraceMatch) string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// TestTraceListsEveryEntryThatCatchesTheName. The manual documents no order
// between entries that match, so none is imposed: every one that catches the
// name is returned, whatever its kind, and a regexp Go cannot compile is
// reported instead of being skipped.
func TestTraceListsEveryEntryThatCatchesTheName(t *testing.T) {
	rows := []routeros.Reply{
		{".id": "*1", "name": "example.com", "type": "FWD", "address-list": "to_vpn", "match-subdomain": "true"},
		{".id": "*2", "name": "cdn.example.com", "type": "FWD", "forward-to": "corp", "match-subdomain": "true",
			"disabled": "true"},
		{".id": "*3", "regexp": `^img[0-9]+\.example\.com$`, "type": "A", "address": "198.51.100.1"},
		{".id": "*4", "name": "other.com", "type": "FWD", "match-subdomain": "true"},
		{".id": "*5", "name": "example.com.evil", "type": "A", "address": "198.51.100.2"},
		{".id": "*6", "regexp": `(`, "type": "A", "address": "198.51.100.3"},
	}
	got, unevaluated := TraceMatches("A.CDN.Example.com.", rows)
	if matchIDs(got) != "*1,*2" {
		t.Errorf("matches = %s; want both parents", matchIDs(got))
	}
	if len(unevaluated) != 1 || unevaluated[0] != "(" {
		t.Errorf("unevaluated = %v; want the regexp Go cannot compile", unevaluated)
	}
	got, _ = TraceMatches("img7.example.com", rows)
	if matchIDs(got) != "*1,*3" {
		t.Errorf("matches = %s; want the subdomain parent and the regexp", matchIDs(got))
	}
	if got, _ := TraceMatches("notexample.com", rows); len(got) != 0 {
		t.Errorf("a suffix that is not a label boundary matched: %+v", got)
	}
}

// traceRouter is a router holding a FWD entry feeding to_vpn, one of the two
// resolved addresses on the list, a mangle rule marking for `vpn`, and that
// table with no active route. It records the menus it was asked for.
func traceRouter(asked *[]string, static ...routeros.Reply) func(routeros.Cmd) ([]routeros.Reply, error) {
	if len(static) == 0 {
		static = []routeros.Reply{{".id": "*1", "name": "example.com", "type": "FWD",
			"address-list": "to_vpn", "match-subdomain": "true", "forward-to": "corp"}}
	}
	return func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		*asked = append(*asked, cmd.Path)
		args := strings.Join(cmd.Args, " ")
		switch cmd.Path {
		case "/ip/dns/static/print":
			return static, nil
		case "/ip/dns/cache/print":
			if strings.Contains(args, "?name=www.example.com") {
				return []routeros.Reply{
					{"name": "www.example.com", "type": "A", "data": "198.51.100.10", "ttl": "5m"},
					{"name": "www.example.com", "type": "A", "data": "198.51.100.11", "ttl": "5m"}}, nil
			}
			return nil, nil
		case "/ip/firewall/address-list/print":
			if strings.Contains(args, "?address=198.51.100.10") {
				return []routeros.Reply{{"list": "to_vpn", "address": "198.51.100.10", "dynamic": "true"}}, nil
			}
			return nil, nil
		case "/ip/firewall/mangle/print":
			return []routeros.Reply{{".id": "*9", "action": "mark-routing", "new-routing-mark": "vpn",
				"dst-address-list": "to_vpn", "packets": "42"}}, nil
		case "/ip/route/print":
			return []routeros.Reply{{"dst-address": "0.0.0.0/0", "gateway": "wg0", "routing-table": "vpn",
				"active": "false"}}, nil
		}
		return nil, nil
	}
}

func traceStates(tr DNSTrace) string {
	states := []string{}
	for _, s := range tr.Steps {
		states = append(states, s.Step+":"+s.State)
	}
	return strings.Join(states, " ")
}

var allAccess = traceAccess{Lists: true, Mangle: true, Routes: true}

func TestTheVerdictWalksTheChain(t *testing.T) {
	var asked []string
	tr, err := traceDomain(context.Background(), traceRouter(&asked), "www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	want := "Static entry:ok Resolved:ok Address list:warn Mangle:ok Route:fail"
	if got := traceStates(tr); got != want {
		t.Errorf("steps = %s\nwant    %s\n%+v", got, want, tr.Steps)
	}

	none := TraceSteps(DNSTrace{Name: "x.org", Answering: []int{}}, traceAccess{})
	if len(none) != 1 || none[0].State != "fail" {
		t.Errorf("a name no entry catches: %+v", none)
	}
}

// TestOnlyAnEntryThatAnswersALookupIsFollowed. An exact TXT or MX entry for a
// name catches it and answers a different question than the one a client
// opening a site asks, so it must not stand in for the FWD entry that feeds the
// list.
func TestOnlyAnEntryThatAnswersALookupIsFollowed(t *testing.T) {
	var asked []string
	tr, err := traceDomain(context.Background(), traceRouter(&asked,
		routeros.Reply{".id": "*1", "name": "www.example.com", "type": "TXT", "text": "v=spf1 -all"},
		routeros.Reply{".id": "*2", "name": "www.example.com", "type": "MX", "mx-exchange": "mail.example.com"},
		routeros.Reply{".id": "*3", "name": "example.com", "type": "FWD", "address-list": "to_vpn",
			"match-subdomain": "true"}), "www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Matches) != 3 || len(tr.Answering) != 1 || tr.Matches[tr.Answering[0]].ID != "*3" {
		t.Fatalf("matches %d, answering %v; want the FWD entry alone to answer", len(tr.Matches), tr.Answering)
	}
	if tr.List != "to_vpn" {
		t.Errorf("the chain followed %q, want to_vpn", tr.List)
	}

	// Only entries of other kinds: nothing answers, and the verdict says why.
	asked = nil
	tr, err = traceDomain(context.Background(), traceRouter(&asked,
		routeros.Reply{".id": "*1", "name": "www.example.com", "type": "TXT", "text": "v=spf1 -all"}),
		"www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if got := traceStates(tr); got != "Static entry:fail" || !strings.Contains(tr.Steps[0].Text, "TXT") {
		t.Errorf("steps = %s %q", got, tr.Steps[0].Text)
	}
}

// TestSeveralAnsweringEntriesAreListedNotPicked. Entries feeding different
// lists end the chain with the reason; entries feeding the same list carry on.
func TestSeveralAnsweringEntriesAreListedNotPicked(t *testing.T) {
	var asked []string
	tr, err := traceDomain(context.Background(), traceRouter(&asked,
		routeros.Reply{".id": "*1", "name": "www.example.com", "type": "FWD", "address-list": "to_vpn"},
		routeros.Reply{".id": "*2", "name": "example.com", "type": "FWD", "address-list": "to_other",
			"match-subdomain": "true"}), "www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if got := traceStates(tr); got != "Static entry:warn Address list:skip" {
		t.Errorf("steps = %s", got)
	}
	if !strings.Contains(tr.Steps[0].Text, "does not document which") {
		t.Errorf("the first step does not say the choice is undocumented: %q", tr.Steps[0].Text)
	}
	for _, p := range asked {
		if strings.Contains(p, "mangle") || strings.Contains(p, "route") {
			t.Errorf("a chain was followed past entries that disagree: %s", p)
		}
	}

	asked = nil
	tr, err = traceDomain(context.Background(), traceRouter(&asked,
		routeros.Reply{".id": "*1", "name": "www.example.com", "type": "A", "address": "198.51.100.1",
			"address-list": "to_vpn"},
		routeros.Reply{".id": "*2", "name": "example.com", "type": "FWD", "address-list": "to_vpn",
			"match-subdomain": "true"}), "www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if got := traceStates(tr); got != "Static entry:warn Resolved:ok Address list:warn Mangle:ok Route:fail" {
		t.Errorf("steps = %s", got)
	}
}

// TestTheChainIsIPv4AndSaysSo. Only /ip/firewall/mangle and /ip/route are read,
// so an AAAA answer is reported and not followed, and no IPv6 menu is asked.
func TestTheChainIsIPv4AndSaysSo(t *testing.T) {
	var asked []string
	exec := traceRouter(&asked)
	v6 := func(cmd routeros.Cmd) ([]routeros.Reply, error) {
		if cmd.Path == "/ip/dns/cache/print" && strings.Contains(strings.Join(cmd.Args, " "), "?name=www.example.com") {
			return []routeros.Reply{{"name": "www.example.com", "type": "AAAA", "data": "2001:db8::1", "ttl": "5m"},
				{"name": "www.example.com", "type": "A", "data": "198.51.100.10", "ttl": "5m"}}, nil
		}
		return exec(cmd)
	}
	tr, err := traceDomain(context.Background(), v6, "www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Steps[1].Text, "IPv4 only") {
		t.Errorf("the resolved step does not say AAAA is not followed: %q", tr.Steps[1].Text)
	}
	for _, p := range asked {
		if strings.Contains(p, "ipv6") {
			t.Errorf("an IPv6 menu was read: %s", p)
		}
	}
}

// TestARegexpTheServerCannotCompileIsSaidSo. The old answer was that no entry
// catches the name, which is a claim the trace could not make.
func TestARegexpTheServerCannotCompileIsSaidSo(t *testing.T) {
	var asked []string
	tr, err := traceDomain(context.Background(), traceRouter(&asked,
		routeros.Reply{".id": "*1", "regexp": `(`, "type": "A", "address": "198.51.100.1"}),
		"www.example.com", allAccess)
	if err != nil {
		t.Fatal(err)
	}
	if got := traceStates(tr); got != "Static entry:fail Regexp:warn" || !strings.Contains(tr.Steps[1].Text, "(") {
		t.Errorf("steps = %s %+v", got, tr.Steps)
	}
}

// TestTheTraceReadsOnlyWhatItsViewerMaySee. A viewer with DNS read access but
// not the Address Lists, Firewall or Routing page is told each link was
// withheld, and the router is never asked for those menus.
func TestTheTraceReadsOnlyWhatItsViewerMaySee(t *testing.T) {
	var asked []string
	tr, err := traceDomain(context.Background(), traceRouter(&asked), "www.example.com", traceAccess{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Static entry:ok Resolved:ok Address list:skip Mangle:skip"; traceStates(tr) != want {
		t.Errorf("steps = %s\nwant    %s", traceStates(tr), want)
	}
	for _, p := range asked {
		if strings.Contains(p, "address-list") || strings.Contains(p, "mangle") || strings.Contains(p, "route") {
			t.Errorf("the router was asked for %s on behalf of a viewer who may not read it", p)
		}
	}
	if len(tr.Listed)+len(tr.Mangle)+len(tr.Routes) != 0 {
		t.Errorf("withheld rows reached the answer: %+v %+v %+v", tr.Listed, tr.Mangle, tr.Routes)
	}

	asked = nil
	tr, err = traceDomain(context.Background(), traceRouter(&asked), "www.example.com",
		traceAccess{Lists: true, Mangle: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Static entry:ok Resolved:ok Address list:warn Mangle:ok Route:skip"; traceStates(tr) != want {
		t.Errorf("without Routing: steps = %s\nwant    %s", traceStates(tr), want)
	}
	for _, p := range asked {
		if p == "/ip/route/print" {
			t.Error("the routes were read for a viewer without the Routing page")
		}
	}
}

// ── the endpoints' gates ────────────────────────────────────────────────────

// gateServer is a server with one signed-in user, u-1, whose grants are the
// rbac fixture's (dns write on r-A, read on r-B) plus `extra` SQL, and the
// settings file `settings`. No router session exists: every case here is a
// refusal that must come before one is asked for.
func gateServer(t *testing.T, settings, extra string) *Server {
	t.Helper()
	return gateServerWith(t, settings, extra, `[]`)
}

// gateServerWith is gateServer with the routers file given.
func gateServerWith(t *testing.T, settings, extra, routers string) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json":  routers,
		"settings.json": settings,
		".secret":       "test-secret",
		"users.json":    `[{"id":"u-1","username":"someone","passwordHash":"x","salt":"y","role":"viewer"}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ddir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(ddir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(rbacDDL + extra); err != nil {
		t.Fatal(err)
	}
	h.Close()
	database, err := db.Open(ddir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	resolver := rbac.New(database, func() []rbac.Router { return []rbac.Router{{ID: "r-A"}, {ID: "r-B"}} })
	return &Server{store: st, rbac: resolver, auditDB: database,
		auth: authFor("tok", &Session{Username: "someone"})}
}

func gateCall(s *Server, h func(http.ResponseWriter, *http.Request), url string, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", url, nil)
	if signedIn {
		req.Header.Set("Cookie", authed)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

// TestTheDNSToolsRefuseBeforeTheyReachARouter. Each gate is held on its own:
// removing the sign-in check, the feature switch, the DNS read check or the
// administrator check from either handler fails one of these.
func TestTheDNSToolsRefuseBeforeTheyReachARouter(t *testing.T) {
	for _, c := range []struct {
		name, settings, url string
		signedIn            bool
		handler             func(*Server) func(http.ResponseWriter, *http.Request)
		want                int
		says                string
	}{
		{"cache, signed out", `{"dnsCacheEnabled":true}`, "/api/dns/cache?routerId=r-A", false,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsCacheGet }, 401, "not signed in"},
		{"cache, switched off", `{}`, "/api/dns/cache?routerId=r-A", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsCacheGet }, 403, "switched off"},
		{"cache, bad router id", `{"dnsCacheEnabled":true}`, "/api/dns/cache?routerId=../x", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsCacheGet }, 400, ""},
		{"cache, a router the user cannot read", `{"dnsCacheEnabled":true}`, "/api/dns/cache?routerId=r-X", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsCacheGet }, 403, "Not permitted"},
		{"cache, DNS read but not an administrator", `{"dnsCacheEnabled":true}`, "/api/dns/cache?routerId=r-A", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsCacheGet }, 403, "administrators"},
		{"trace, signed out", `{"dnsTraceEnabled":true}`, "/api/dns/trace?routerId=r-A&name=a.b", false,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsTraceGet }, 401, "not signed in"},
		{"trace, switched off", `{"dnsCacheEnabled":true}`, "/api/dns/trace?routerId=r-A&name=a.b", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsTraceGet }, 403, "switched off"},
		{"trace, a router the user cannot read", `{"dnsTraceEnabled":true}`, "/api/dns/trace?routerId=r-X&name=a.b", true,
			func(s *Server) func(http.ResponseWriter, *http.Request) { return s.dnsTraceGet }, 403, "Not permitted"},
	} {
		s := gateServer(t, c.settings, "")
		w := gateCall(s, c.handler(s), c.url, c.signedIn)
		if w.Code != c.want || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s: answered %d %s; want %d %q", c.name, w.Code, strings.TrimSpace(w.Body.String()), c.want, c.says)
		}
	}
}

// TestTheTraceIsCutToTheViewersPages. The access handed to the chain is what
// the viewer's grants on that router say, page by page.
func TestTheTraceIsCutToTheViewersPages(t *testing.T) {
	sess := &Session{Username: "someone"}
	dnsOnly := gateServer(t, `{}`, "")
	if got := dnsOnly.traceAccessFor(sess, "r-A"); got != (traceAccess{}) {
		t.Errorf("a DNS-only user got %+v", got)
	}
	more := gateServer(t, `{}`, `
INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','address-lists','read'), ('role-w','firewall','read');`)
	if got := more.traceAccessFor(sess, "r-A"); got != (traceAccess{Lists: true, Mangle: true}) {
		t.Errorf("a user granted Address Lists and Firewall got %+v", got)
	}
	if got := more.traceAccessFor(sess, "r-B"); got != (traceAccess{}) {
		t.Errorf("the grants of r-A reached r-B: %+v", got)
	}
}
