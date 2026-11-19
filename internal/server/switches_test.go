package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
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
	_, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
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
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
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

func probeServer(t *testing.T, sess *Session) (*http.ServeMux, func(body string) map[string]any) {
	t.Helper()
	s, mux, _ := routersServer(t, sess, "")
	s.registerSwitches(mux)
	prev := routeros.DialReach()
	routeros.SetDialReach(netguard.Any) // the fakes listen on loopback
	t.Cleanup(func() { routeros.SetDialReach(prev) })
	return mux, func(body string) map[string]any {
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
	_, probe := probeServer(t, &Session{AuthMode: "none", Username: "admin"})

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
	_, probe := probeServer(t, &Session{AuthMode: "local", Username: "viewer"})
	if got := probe(`{"host":"198.51.100.20"}`); got["status"] != float64(http.StatusForbidden) {
		t.Errorf("a non-administrator probed a new switch: %v", got)
	}
}
