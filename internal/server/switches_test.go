package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/netguard"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

func switchByLabel(t *testing.T, dir, label string) map[string]any {
	t.Helper()
	for _, r := range routerFile(t, dir) {
		if r["label"] == label {
			return r
		}
	}
	t.Fatalf("no record labelled %q", label)
	return nil
}

func TestASwitchIsCreatedThroughTheRouterRoute(t *testing.T) {
	adminSess := &Session{Username: "admin"}
	adminSrv, mux, dir := routersServer(t, adminSess, "")
	signedInAdmin(t, adminSrv, adminSess)
	w := routerPost(mux, `{"kind":"swos","host":"198.51.100.20","label":"Core switch","username":"admin","password":"sw-secret","tls":true,"defaultIf":"ether9"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sw-secret") {
		t.Fatal("the password came back")
	}
	rec := switchByLabel(t, dir, "Core switch")
	if rec["kind"] != store.KindSwOS || rec["scheme"] != "http" || rec["port"] != float64(80) {
		t.Errorf("record %v", rec)
	}
	// Router-only fields are not part of a switch record.
	if _, ok := rec["tls"]; ok {
		t.Error("a router-only field was stored on a switch")
	}
	if rec["password"] == "sw-secret" || rec["password"] == "" {
		t.Errorf("password not sealed: %v", rec["password"])
	}
}

func TestTheRouterListKeepsSwitchesApart(t *testing.T) {
	adminSess := &Session{Username: "admin"}
	adminSrv, mux, _ := routersServer(t, adminSess, "")
	signedInAdmin(t, adminSrv, adminSess)
	if w := routerPost(mux, `{"kind":"swos","host":"198.51.100.20","label":"Core switch"}`, authed); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/routers", nil)
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out struct {
		Routers  []map[string]any `json:"routers"`
		Switches []map[string]any `json:"switches"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Routers {
		if r["kind"] == store.KindSwOS {
			t.Error("a switch is in `routers`, which the router selector reads")
		}
	}
	if len(out.Switches) != 1 || out.Switches[0]["label"] != "Core switch" {
		t.Errorf("switches = %v", out.Switches)
	}
}

func TestASwitchEditKeepsItsKind(t *testing.T) {
	adminSess := &Session{Username: "admin"}
	adminSrv, mux, dir := routersServer(t, adminSess, "")
	signedInAdmin(t, adminSrv, adminSess)
	if w := routerPost(mux, `{"kind":"swos","host":"198.51.100.20","label":"Core switch"}`, authed); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	id := switchByLabel(t, dir, "Core switch")["id"].(string)
	w := routerPut(mux, id, `{"kind":"","label":"Renamed","tls":true,"password":"new-pass","port":"8080"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	rec := routerByID(t, dir, id)
	if rec["kind"] != store.KindSwOS || rec["label"] != "Renamed" || rec["port"] != float64(8080) {
		t.Errorf("record %v", rec)
	}
	if _, ok := rec["tls"]; ok || rec["password"] == "new-pass" {
		t.Errorf("tls %v password %v: unknown keys kept or the password stored in clear", rec["tls"], rec["password"])
	}
	// And a router cannot be turned into a switch by an edit.
	routerID := routerFile(t, dir)[0]["id"].(string)
	if w := routerPut(mux, routerID, `{"kind":"swos"}`, authed); w.Code != http.StatusOK {
		t.Fatalf("router edit: %d %s", w.Code, w.Body.String())
	}
	if k, _ := routerByID(t, dir, routerID)["kind"].(string); k != "" {
		t.Errorf("a router edit set kind %q", k)
	}
}

func probeServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, func(body string) map[string]any) {
	t.Helper()
	s, mux, _ := routersServer(t, sess, "")
	s.registerSwitches(mux)
	prev := routeros.DialReach()
	routeros.SetDialReach(netguard.Any) // the fakes listen on loopback
	t.Cleanup(func() { routeros.SetDialReach(prev) })
	return s, mux, func(body string) map[string]any {
		req := httptest.NewRequest("POST", "/api/switches/probe", strings.NewReader(body))
		req.Header.Set("Cookie", authed)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		out["status"] = float64(w.Code)
		return out
	}
}

func fakeDevice(t *testing.T, h http.HandlerFunc) (string, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Hostname(), u.Port()
}

func TestTheProbeAcceptsSwOSAndTurnsAwayRouterOS(t *testing.T) {
	admin := &Session{Username: "admin"}
	adminSrv, _, probe := probeServer(t, admin)
	signedInAdmin(t, adminSrv, admin)

	host, port := fakeDevice(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sys.b" {
			_, _ = w.Write([]byte("{brd:'4353533331382d3136472d32532b',ver:'322e3139',id:'4f6666696365'}"))
			return
		}
		http.NotFound(w, r)
	})
	got := probe(`{"host":"` + host + `","port":"` + port + `","username":"admin","password":"x"}`)
	if got["ok"] != true || got["model"] != "CSS318-16G-2S+" || got["family"] != "swos" {
		t.Errorf("a SwOS switch: %v", got)
	}

	// RouterOS's web server, as CHR Test answered it: 404 on sys.b, an index
	// page titled RouterOS.
	host, port = fakeDevice(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("<!doctype html><html><title>RouterOS router configuration page</title>"))
			return
		}
		http.NotFound(w, r)
	})
	got = probe(`{"host":"` + host + `","port":"` + port + `"}`)
	if got["ok"] != false || !strings.Contains(got["error"].(string), "add it as a router instead") {
		t.Errorf("a RouterOS device: %v", got)
	}

	// Something that answers sys.b but is not SwOS.
	host, port = fakeDevice(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{}"))
	})
	got = probe(`{"host":"` + host + `","port":"` + port + `"}`)
	if got["ok"] != false || !strings.Contains(got["error"].(string), "did not answer as a SwOS switch") {
		t.Errorf("not SwOS: %v", got)
	}
}

func TestTheProbeIsForAdministrators(t *testing.T) {
	_, _, probe := probeServer(t, &Session{Username: "viewer"})
	if got := probe(`{"host":"198.51.100.20"}`); got["status"] != float64(http.StatusForbidden) {
		t.Errorf("a non-administrator probed a new switch: %v", got)
	}
	// An edit is no exception: the probe dials whatever host the body names,
	// and only an administrator may point the server at a host.
	if got := probe(`{"id":"s1","host":"203.0.113.9"}`); got["status"] != float64(http.StatusForbidden) {
		t.Errorf("a non-administrator probed with a switch id: %v", got)
	}
}

// TestTheProbeIsRateLimited: ten a minute, as POST /api/routers/test, because
// each probe opens a connection to a host the caller chose.
func TestTheProbeIsRateLimited(t *testing.T) {
	_, _, probe := probeServer(t, &Session{Username: "viewer"})
	for i := 0; i < 10; i++ {
		if got := probe(`{"host":"198.51.100.20"}`); got["status"] == float64(http.StatusTooManyRequests) {
			t.Fatalf("request %d was refused as over the limit", i+1)
		}
	}
	if got := probe(`{"host":"198.51.100.20"}`); got["status"] != float64(http.StatusTooManyRequests) {
		t.Errorf("the eleventh probe in a minute was not refused: %v", got)
	}
}

// TestTheProbeNeverSendsAStoredPasswordElsewhere: a blank password reuses the
// stored one only for the address and login it is stored with.
func TestTheProbeNeverSendsAStoredPasswordElsewhere(t *testing.T) {
	list := []store.Switch{{ID: "s1", Host: "198.51.100.20", Port: 80, Scheme: "http", Username: "admin", Password: "stored"}}
	if got := storedSwitchPassword(list, "s1", "198.51.100.20", 80, "http", "admin"); got != "stored" {
		t.Errorf("the switch's own endpoint: %q", got)
	}
	for _, c := range []struct {
		host, scheme, user string
		port               int
	}{
		{"203.0.113.9", "http", "admin", 80},
		{"198.51.100.20", "http", "admin", 8080},
		{"198.51.100.20", "https", "admin", 80},
		{"198.51.100.20", "http", "other", 80},
	} {
		if got := storedSwitchPassword(list, "s1", c.host, c.port, c.scheme, c.user); got != "" {
			t.Errorf("%+v received the stored password", c)
		}
	}
}

// TestADelegateCannotMoveASwitchsEndpoint, scheme included.
func TestADelegateCannotMoveASwitchsEndpoint(t *testing.T) {
	body := map[string]any{"label": "x", "host": "203.0.113.9", "port": 8080, "scheme": "http", "password": "p"}
	stripSwitchPrivileged(body, false)
	for _, k := range []string{"host", "port", "scheme", "password"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s survived a delegate's edit", k)
		}
	}
	if body["label"] != "x" {
		t.Error("the label was stripped too")
	}
	admin := map[string]any{"scheme": "https"}
	stripSwitchPrivileged(admin, true)
	if admin["scheme"] != "https" {
		t.Error("an administrator's scheme was stripped")
	}
}

// TestTheRouterSelectorNeverReceivesASwitch pins docs/DECISIONS.md: the
// selector lists routers only. Its sources are GET /api/routers (also what a
// reconnect re-fetches) and the routers:update event (sent on every change and
// on a connect, always from routerListForSocket). Checked after a switch is
// added and after it is edited, and the event's every send is held to that one
// builder, so a new send cannot bypass the filter.
func TestTheRouterSelectorNeverReceivesASwitch(t *testing.T) {
	sess := &Session{Username: "admin"}
	s, mux, dir := routersServer(t, sess, "")
	signedInAdmin(t, s, sess)
	if w := routerPost(mux, `{"kind":"swos","host":"198.51.100.20","label":"Core switch"}`, authed); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	swID := switchByLabel(t, dir, "Core switch")["id"].(string)
	check := func(when string) {
		t.Helper()
		for _, r := range s.routerListForSocket(sess) {
			if r["id"] == swID || r["kind"] == store.KindSwOS {
				t.Errorf("%s: the routers:update payload carries the switch", when)
			}
		}
		req := httptest.NewRequest("GET", "/api/routers", nil)
		req.Header.Set("Cookie", authed)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var out struct {
			Routers []map[string]any `json:"routers"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if len(out.Routers) == 0 {
			t.Fatalf("%s: no routers listed at all; the check would pass against nothing", when)
		}
		for _, r := range out.Routers {
			if r["id"] == swID || r["kind"] == store.KindSwOS {
				t.Errorf("%s: GET /api/routers lists the switch among the routers", when)
			}
		}
	}
	check("after adding a switch")
	if w := routerPut(mux, swID, `{"label":"Core switch 2","disabled":false}`, authed); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	check("after editing a switch")

	// Every send of routers:update builds its list with routerListForSocket,
	// or sends an empty one.
	src := ""
	for _, f := range []string{"devices.go", "routers_api.go", "switches.go", "ws.go", "server.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src += string(b)
	}
	sends := regexp.MustCompile(`EvRoutersUpdate\.\w+\([^\n]*`).FindAllString(src, -1)
	if len(sends) == 0 {
		t.Fatal("no send of routers:update found; this check reads nothing")
	}
	for _, line := range sends {
		if !strings.Contains(line, "routerListForSocket(") && !strings.Contains(line, "[]map[string]any{}") {
			t.Errorf("a routers:update send does not use routerListForSocket: %s", line)
		}
	}
}
